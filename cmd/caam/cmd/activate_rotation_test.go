package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

func TestActivateForcedRotationHonorsCooldownOverride(t *testing.T) {
	for _, tc := range []struct {
		name       string
		profiles   map[string]string
		auto       bool
		force      bool
		useDefault bool
		want       string
		wantErr    string
	}{
		{name: "auto sole cooling refused", profiles: map[string]string{"a": "a"}, auto: true, wantErr: "cooldown"},
		{name: "auto sole cooling forced", profiles: map[string]string{"a": "a"}, auto: true, force: true, want: "a"},
		{name: "auto cooling and system forced", profiles: map[string]string{"a": "a", "_backup_saved": "system"}, auto: true, force: true, want: "a"},
		{name: "auto system only forced refused", profiles: map[string]string{"_backup_saved": "system"}, auto: true, force: true, wantErr: "no user profiles"},
		{name: "cooling default rotates without force", profiles: map[string]string{"a": "a", "b": "b"}, useDefault: true, want: "b"},
		{name: "forced cooling default is retained", profiles: map[string]string{"a": "a", "b": "b"}, useDefault: true, force: true, want: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cleanup := setupNextTestEnv(t)
			defer cleanup()
			oldCfg, oldHealth, oldDB := cfg, healthStore, globalDB
			cfg, healthStore, globalDB = config.DefaultConfig(), nil, nil
			t.Cleanup(func() {
				if globalDB != nil {
					_ = globalDB.Close()
				}
				cfg, healthStore, globalDB = oldCfg, oldHealth, oldDB
			})
			if tc.useDefault {
				cfg.SetDefault("codex", "a")
			}
			spmCfg := config.DefaultSPMConfig()
			spmCfg.Project.Enabled = false
			spmCfg.Stealth.Rotation.Enabled = true
			spmCfg.Stealth.Rotation.Algorithm = "round_robin"
			spmCfg.Stealth.Cooldown.Enabled = true
			if err := spmCfg.Save(); err != nil {
				t.Fatal(err)
			}
			createTestProfiles(t, tc.profiles)
			authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
			live := []byte(`{"access_token":"synthetic-unsaved"}`)
			if err := os.WriteFile(authPath, live, 0600); err != nil {
				t.Fatal(err)
			}
			db, err := getDB()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.SetCooldown("codex", "a", time.Now().UTC(), time.Hour, "synthetic limit"); err != nil {
				t.Fatal(err)
			}
			c := &cobra.Command{}
			c.Flags().Bool("backup-current", false, "")
			c.Flags().Bool("force", tc.force, "")
			c.Flags().Bool("auto", tc.auto, "")
			c.Flags().Bool("json", true, "")
			c.Flags().Bool("reload-daemon", false, "")
			var output bytes.Buffer
			c.SetOut(&output)
			err = runActivate(c, []string{"codex"})
			var result activateOutput
			if decodeErr := json.Unmarshal(output.Bytes(), &result); decodeErr != nil {
				t.Fatalf("invalid activation result %q: %v", output.String(), decodeErr)
			}
			got, readErr := os.ReadFile(authPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) || result.Success {
					t.Fatalf("expected refusal containing %q: result=%+v, err=%v", tc.wantErr, result, err)
				}
				if !bytes.Equal(got, live) {
					t.Fatal("refused automatic activation changed the live credential")
				}
				return
			}
			if err != nil || !result.Success || result.Profile != tc.want {
				t.Fatalf("activation = %+v, %v; want %s", result, err, tc.want)
			}
			if string(got) != `{"access_token":"`+tc.want+`"}` {
				t.Fatalf("activated incorrect credential: %s", got)
			}
			if tc.useDefault && tc.force && result.Rotation != nil {
				t.Fatalf("explicitly forced default unexpectedly entered rotation: %+v", result)
			}
		})
	}
}

func TestActivate_AutoSelect_ChoosesNonCooldownProfile(t *testing.T) {
	tmpDir := t.TempDir()

	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}

	// Create current auth state.
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"current"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Use a temp vault with two profiles.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	if err := os.MkdirAll(vault.ProfilePath("codex", "a"), 0700); err != nil {
		t.Fatalf("MkdirAll(profile a) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"), []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(profile a) error = %v", err)
	}
	if err := os.MkdirAll(vault.ProfilePath("codex", "b"), 0700); err != nil {
		t.Fatalf("MkdirAll(profile b) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), []byte(`{"access_token":"b"}`), 0600); err != nil {
		t.Fatalf("WriteFile(profile b) error = %v", err)
	}

	// Put profile a in cooldown.
	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.SetCooldown("codex", "a", time.Now().UTC(), 60*time.Minute, ""); err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	c := &cobra.Command{}
	c.Flags().Bool("backup-current", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().Bool("auto", true, "")
	_ = c.Flags().Set("auto", "true")

	if err := runActivate(c, []string{"codex"}); err != nil {
		t.Fatalf("runActivate(--auto) error = %v", err)
	}

	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"b"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"b"}`)
	}
}

func TestActivate_NoProfileNoDefault_UsesRotationWhenEnabled(t *testing.T) {
	tmpDir := t.TempDir()

	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}

	// Enable rotation in SPM config.
	spmCfg := []byte("version: 1\nstealth:\n  rotation:\n    enabled: true\n    algorithm: smart\n")
	if err := os.WriteFile(config.SPMConfigPath(), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	// Ensure caam global config has no default profiles for this test.
	oldCfg := cfg
	cfg = nil
	t.Cleanup(func() { cfg = oldCfg })

	// Use a temp vault with one profile.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	if err := os.MkdirAll(vault.ProfilePath("codex", "only"), 0700); err != nil {
		t.Fatalf("MkdirAll(profile) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault.ProfilePath("codex", "only"), "auth.json"), []byte(`{"access_token":"only"}`), 0600); err != nil {
		t.Fatalf("WriteFile(profile auth) error = %v", err)
	}

	c := &cobra.Command{}
	c.Flags().Bool("backup-current", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().Bool("auto", false, "")

	if err := runActivate(c, []string{"codex"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"only"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"only"}`)
	}
}

func TestActivate_DefaultInCooldown_AutoSelectsAlternative(t *testing.T) {
	tmpDir := t.TempDir()

	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}

	// Enable rotation in SPM config.
	spmCfg := []byte("version: 1\nstealth:\n  rotation:\n    enabled: true\n    algorithm: smart\n")
	if err := os.WriteFile(config.SPMConfigPath(), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	// Set caam global config default to profile a.
	oldCfg := cfg
	cfg = config.DefaultConfig()
	cfg.SetDefault("codex", "a")
	t.Cleanup(func() { cfg = oldCfg })

	// Use a temp vault with two profiles.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	if err := os.MkdirAll(vault.ProfilePath("codex", "a"), 0700); err != nil {
		t.Fatalf("MkdirAll(profile a) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault.ProfilePath("codex", "a"), "auth.json"), []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(profile a) error = %v", err)
	}
	if err := os.MkdirAll(vault.ProfilePath("codex", "b"), 0700); err != nil {
		t.Fatalf("MkdirAll(profile b) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(vault.ProfilePath("codex", "b"), "auth.json"), []byte(`{"access_token":"b"}`), 0600); err != nil {
		t.Fatalf("WriteFile(profile b) error = %v", err)
	}

	// Put default profile a in cooldown.
	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.SetCooldown("codex", "a", time.Now().UTC(), 60*time.Minute, ""); err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	c := &cobra.Command{}
	c.Flags().Bool("backup-current", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().Bool("auto", false, "")

	if err := runActivate(c, []string{"codex"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"b"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"b"}`)
	}
}
