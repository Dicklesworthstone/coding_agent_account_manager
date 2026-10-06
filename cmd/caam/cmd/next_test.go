package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/spf13/cobra"
)

// setupNextTestEnv sets up a test environment with vault and profiles.
func setupNextTestEnv(t *testing.T) (tmpDir string, cleanup func()) {
	t.Helper()
	tmpDir = t.TempDir()

	oldCodexHome := os.Getenv("CODEX_HOME")
	oldCaamHome := os.Getenv("CAAM_HOME")

	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}

	// Use a temp vault
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))

	cleanup = func() {
		_ = os.Setenv("CODEX_HOME", oldCodexHome)
		_ = os.Setenv("CAAM_HOME", oldCaamHome)
		vault = oldVault
	}

	return tmpDir, cleanup
}

// createTestProfiles creates test profiles in the vault.
func createTestProfiles(t *testing.T, profiles map[string]string) {
	t.Helper()
	for name, token := range profiles {
		profPath := vault.ProfilePath("codex", name)
		if err := os.MkdirAll(profPath, 0700); err != nil {
			t.Fatalf("MkdirAll(profile %s) error = %v", name, err)
		}
		content := `{"access_token":"` + token + `"}`
		if err := os.WriteFile(filepath.Join(profPath, "auth.json"), []byte(content), 0600); err != nil {
			t.Fatalf("WriteFile(profile %s) error = %v", name, err)
		}
	}
}

func TestNext_RotatesToNextProfile(t *testing.T) {
	tmpDir, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state (matches profile a)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create two profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
	})

	// Enable rotation in SPM config
	spmCfg := []byte("version: 1\nstealth:\n  rotation:\n    enabled: true\n    algorithm: round_robin\n")
	if err := os.MkdirAll(filepath.Dir(config.SPMConfigPath()), 0700); err != nil {
		t.Fatalf("MkdirAll(config dir) error = %v", err)
	}
	if err := os.WriteFile(config.SPMConfigPath(), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}
	_ = tmpDir // Suppress unused variable warning

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext() error = %v", err)
	}

	// Verify auth was switched to profile b
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"b"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"b"}`)
	}
}

func TestNext_SkipsCooldownProfile(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state (matches profile a)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create three profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
		"c": "c",
	})

	// Put profile b in cooldown
	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	defer db.Close()

	if _, err := db.SetCooldown("codex", "b", time.Now().UTC(), 60*time.Minute, ""); err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "smart", "")
	_ = c.Flags().Set("algorithm", "smart")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext() error = %v", err)
	}

	// Should have skipped b (cooldown) and picked c
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"c"}` {
		t.Fatalf("auth mismatch: got %q want (c profile), not (b profile which is in cooldown)", string(got))
	}
}

func TestNext_DryRunDoesNotActivate(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	originalContent := `{"access_token":"original"}`
	if err := os.WriteFile(authPath, []byte(originalContent), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create two profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
	})

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", true, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")
	_ = c.Flags().Set("dry-run", "true")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext(--dry-run) error = %v", err)
	}

	// Verify auth was NOT changed
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != originalContent {
		t.Fatalf("dry-run should not modify auth: got %q want %q", string(got), originalContent)
	}
}

func TestNext_SingleProfile_AlreadyActive(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state that matches the only profile
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"only"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create only one profile
	createTestProfiles(t, map[string]string{
		"only": "only",
	})

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	// Should not error, just inform that only one profile is available
	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext() error = %v", err)
	}
}

func TestNext_SoleUserEligibilityAndPreservation(t *testing.T) {
	for _, tc := range []struct {
		name          string
		profiles      map[string]string
		cooling       bool
		alreadyActive bool
		force         bool
		dryRun        bool
		wantErr       string
	}{
		{name: "sole cooling user", profiles: map[string]string{"only": "only"}, cooling: true, wantErr: "cooldown"},
		{name: "sole cooling active user", profiles: map[string]string{"only": "only"}, cooling: true, alreadyActive: true, wantErr: "cooldown"},
		{name: "sole original snapshot", profiles: map[string]string{"_original": "recovery"}, wantErr: "no user profiles"},
		{name: "sole auto snapshot", profiles: map[string]string{"_backup_20261006": "recovery"}, wantErr: "no user profiles"},
		{name: "cooling user and backup", profiles: map[string]string{"only": "only", "_original": "recovery"}, cooling: true, wantErr: "cooldown"},
		{name: "healthy sole user", profiles: map[string]string{"only": "only"}},
		{name: "healthy user and backup", profiles: map[string]string{"only": "only", "_original": "recovery"}},
		{name: "explicit forced cooldown", profiles: map[string]string{"only": "only"}, cooling: true, force: true},
		{name: "force cannot choose snapshot", profiles: map[string]string{"_original": "recovery"}, force: true, wantErr: "no user profiles"},
		{name: "healthy dry run", profiles: map[string]string{"only": "only"}, dryRun: true},
		{name: "cooling dry run", profiles: map[string]string{"only": "only"}, cooling: true, dryRun: true, wantErr: "cooldown"},
		{name: "forced cooling dry run", profiles: map[string]string{"only": "only"}, cooling: true, force: true, dryRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, cleanup := setupNextTestEnv(t)
			defer cleanup()
			oldHealth := healthStore
			healthStore = nil
			t.Cleanup(func() { healthStore = oldHealth })
			cfg := config.DefaultSPMConfig()
			cfg.Stealth.Cooldown.Enabled = true
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}
			createTestProfiles(t, tc.profiles)
			beforeProfiles, err := vault.List("codex")
			if err != nil {
				t.Fatal(err)
			}

			authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
			live := `{"access_token":"live-unsaved"}`
			if tc.alreadyActive {
				live = `{"access_token":"only"}`
			}
			if err := os.WriteFile(authPath, []byte(live), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.cooling {
				db, err := caamdb.Open()
				if err != nil {
					t.Fatal(err)
				}
				_, cooldownErr := db.SetCooldown("codex", "only", time.Now().UTC(), time.Hour, "synthetic limit")
				closeErr := db.Close()
				if cooldownErr != nil || closeErr != nil {
					t.Fatalf("record cooldown: %v, close: %v", cooldownErr, closeErr)
				}
			}

			c := &cobra.Command{}
			c.Flags().Bool("dry-run", tc.dryRun, "")
			c.Flags().Bool("quiet", true, "")
			c.Flags().Bool("force", tc.force, "")
			c.Flags().String("algorithm", "smart", "")
			c.Flags().String("policy", "availability", "")
			c.Flags().Bool("usage-aware", false, "")
			c.Flags().Bool("reload-daemon", false, "")
			err = runNext(c, []string{"codex"})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("runNext() = %v; want refusal containing %q", err, tc.wantErr)
				}
			} else if err != nil {
				t.Fatalf("runNext() = %v", err)
			}

			got, err := os.ReadFile(authPath)
			if err != nil {
				t.Fatal(err)
			}
			afterProfiles, err := vault.List("codex")
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantErr != "" || tc.dryRun {
				if string(got) != live || !reflect.DeepEqual(afterProfiles, beforeProfiles) {
					t.Fatalf("refused/dry-run switch changed credentials: live=%q, profiles=%v (before %v)", got, afterProfiles, beforeProfiles)
				}
				return
			}
			if string(got) != `{"access_token":"only"}` {
				t.Fatalf("did not activate sole eligible user: %s", got)
			}
			preserved := false
			for _, name := range afterProfiles {
				if !authfile.IsSystemProfile(name) {
					continue
				}
				data, err := os.ReadFile(filepath.Join(vault.ProfilePath("codex", name), "auth.json"))
				if err != nil {
					t.Fatal(err)
				}
				preserved = preserved || string(data) == live
			}
			if !preserved {
				t.Fatal("switch to sole user discarded the unnamed outgoing credential")
			}
		})
	}
}

func TestNextSelectionForcePreservesNativeQuotaEligibility(t *testing.T) {
	oldHealth := healthStore
	healthStore = nil
	t.Cleanup(func() { healthStore = oldHealth })
	for _, provider := range []string{"grok", "cursor"} {
		for _, force := range []bool{false, true} {
			for _, tc := range []struct {
				name  string
				usage *rotation.UsageInfo
				ok    bool
			}{
				{name: "unmeasured"},
				{name: "failed measurement", usage: &rotation.UsageInfo{Error: "unmeasured"}},
				{name: "exhausted", usage: &rotation.UsageInfo{PrimaryPercent: 100}},
				{name: "measured reset zero", usage: &rotation.UsageInfo{AvailScore: 100}, ok: true},
			} {
				for _, policy := range []string{"availability", "drain"} {
					cfg := config.DefaultSPMConfig()
					cfg.Stealth.Rotation.Policy = policy
					data := map[string]*rotation.UsageInfo{"only": tc.usage}
					selected, err := selectProfileWithRotationAndUsage(provider, []string{"only"}, "", cfg, nil, data, force)
					if tc.ok {
						if err != nil || selected == nil || selected.Selected != "only" {
							t.Errorf("%s/%s/%s force=%v rejected measured zero: %+v, %v", provider, policy, tc.name, force, selected, err)
						}
					} else if err == nil || selected != nil {
						t.Errorf("%s/%s/%s force=%v bypassed quota eligibility: %+v, %v", provider, policy, tc.name, force, selected, err)
					}
				}
			}
		}
	}
}

func TestAutomaticRotationRefusesUnusableCursorWithoutMutation(t *testing.T) {
	for _, command := range []string{"auto", "next"} {
		for _, rejected := range []bool{false, true} {
			for _, force := range []bool{false, true} {
				name := fmt.Sprintf("%s/rejected=%t/force=%t", command, rejected, force)
				t.Run(name, func(t *testing.T) {
					paths := setupCursorHealthVault(t)
					defaultVault := vault
					vault = authfile.NewVault(filepath.Join(t.TempDir(), "custom-vault"))
					now := time.Now().Truncate(time.Second)
					body := cursorHealthCredential(t, now.Add(-time.Hour), false)
					if rejected {
						body = cursorHealthCredential(t, now.Add(time.Hour), true)
					}
					credentialPath := filepath.Join(vault.ProfilePath("cursor", "only"), "auth.json")
					writeNativeTestCredential(t, credentialPath, string(body))
					// A healthy same-named default-vault account must not supply
					// eligibility for the different vault being activated.
					writeNativeTestCredential(t, filepath.Join(defaultVault.ProfilePath("cursor", "only"), "auth.json"), string(cursorHealthCredential(t, now.Add(24*time.Hour), true)))
					if rejected {
						info, err := health.ParseCursorExpiry(credentialPath)
						if err != nil {
							t.Fatal(err)
						}
						if err := healthStore.RecordProviderVerification("cursor", "only", health.ProviderVerification{Reason: "access_token_rejected", Fingerprint: info.Fingerprint}); err != nil {
							t.Fatal(err)
						}
					}
					live := `{"apiKey":"SYNTHETIC-UNSAVED-LIVE-KEY"}`
					writeNativeTestCredential(t, paths.AuthFile, live)
					c := &cobra.Command{}
					c.Flags().Bool("force", force, "")
					var err error
					if command == "auto" {
						c.Flags().Bool("auto", true, "")
						c.Flags().Bool("json", false, "")
						err = runActivate(c, []string{"cursor"})
					} else {
						c.Flags().Bool("quiet", true, "")
						c.Flags().Bool("dry-run", false, "")
						c.Flags().String("algorithm", "round_robin", "")
						c.Flags().String("policy", "availability", "")
						c.Flags().Bool("usage-aware", false, "")
						err = runNext(c, []string{"cursor"})
					}
					if err == nil || !strings.Contains(err.Error(), "login required") {
						t.Fatalf("automatic rotation accepted unusable account: %v", err)
					}
					requireSwitchCredential(t, paths.AuthFile, live)
					requireSwitchCredential(t, credentialPath, string(body))
					profiles, listErr := vault.List("cursor")
					if listErr != nil || !reflect.DeepEqual(profiles, []string{"only"}) {
						t.Fatalf("failed selection created backups or changed vault: %v, %v", profiles, listErr)
					}
				})
			}
		}
	}
}

func TestNextSelectionKeepsRenewableCursorAfterSessionReplacement(t *testing.T) {
	setupCursorHealthVault(t)
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "custom-vault"))
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "only"), "auth.json"), string(cursorHealthCredential(t, time.Time{}, true)))
	if err := healthStore.SetTokenExpiry("cursor", "only", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	for _, algorithm := range []string{"smart", "round_robin", "random"} {
		cfg := config.DefaultSPMConfig()
		cfg.Stealth.Rotation.Algorithm = algorithm
		cfg.Stealth.Rotation.Policy = "drain"
		result, err := selectProfileWithRotationAndUsage("cursor", []string{"only"}, "", cfg, nil, nil, true)
		if err != nil || result.Selected != "only" {
			t.Fatalf("API key inherited old session deadline under %s: %+v, %v", algorithm, result, err)
		}
	}
}

func TestNextRoundRobinRetryCannotChooseExpiredBackup(t *testing.T) {
	paths := setupCursorHealthVault(t)
	current := string(cursorHealthCredential(t, time.Time{}, true))
	writeNativeTestCredential(t, paths.AuthFile, current)
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "current"), "auth.json"), current)
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "expired"), "auth.json"), string(cursorHealthCredential(t, time.Now().Add(-time.Hour), false)))
	c := &cobra.Command{}
	c.Flags().Bool("force", true, "")
	c.Flags().Bool("quiet", true, "")
	c.Flags().Bool("dry-run", false, "")
	c.Flags().String("algorithm", "smart", "")
	c.Flags().String("policy", "availability", "")
	c.Flags().Bool("usage-aware", false, "")
	// Smart picks the current usable key. runNext then retries round-robin
	// because two profiles exist; the retry must not revive the expired one.
	if err := runNext(c, []string{"cursor"}); err != nil {
		t.Fatal(err)
	}
	requireSwitchCredential(t, paths.AuthFile, current)
	if active, err := vault.ActiveProfile(authfile.CursorAuthFiles()); err != nil || active != "current" {
		t.Fatalf("fallback activated an unusable backup: active=%q, error=%v", active, err)
	}
}

func TestNext_UnknownTool_ReturnsError(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	err := runNext(c, []string{"unknown"})
	if err == nil {
		t.Fatal("runNext(unknown tool) should return error")
	}
	if !contains(err.Error(), "unknown tool") {
		t.Fatalf("error should mention 'unknown tool': %v", err)
	}
}

func TestNext_NoProfiles_ReturnsError(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Don't create any profiles

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "", "")

	err := runNext(c, []string{"codex"})
	if err == nil {
		t.Fatal("runNext() with no profiles should return error")
	}
	if !contains(err.Error(), "no profiles found") {
		t.Fatalf("error should mention 'no profiles found': %v", err)
	}
}

func TestNext_AlgorithmOverride(t *testing.T) {
	_, cleanup := setupNextTestEnv(t)
	defer cleanup()

	// Create current auth state
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"a"}`), 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Create two profiles
	createTestProfiles(t, map[string]string{
		"a": "a",
		"b": "b",
	})

	c := &cobra.Command{}
	c.Flags().Bool("dry-run", false, "")
	c.Flags().Bool("quiet", false, "")
	c.Flags().Bool("force", false, "")
	c.Flags().String("algorithm", "round_robin", "")
	_ = c.Flags().Set("algorithm", "round_robin")

	if err := runNext(c, []string{"codex"}); err != nil {
		t.Fatalf("runNext(--algorithm round_robin) error = %v", err)
	}

	// Verify auth was switched
	got, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(got) != `{"access_token":"b"}` {
		t.Fatalf("auth mismatch: got %q want %q", string(got), `{"access_token":"b"}`)
	}
}

// contains checks if substr is in s.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
