package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/spf13/cobra"
)

func TestActivate_CooldownPreventsSwitchUnlessForced(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate auth + config locations.
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

	// Enable cooldown enforcement.
	spmCfg := []byte("version: 1\nstealth:\n  cooldown:\n    enabled: true\n    default_minutes: 60\n")
	if err := os.WriteFile(filepath.Join(os.Getenv("CAAM_HOME"), "config.yaml"), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	original := []byte(`{"access_token":"original","token_type":"Bearer"}`)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, original, 0600); err != nil {
		t.Fatalf("WriteFile(original auth) error = %v", err)
	}

	// Use a temp vault and seed a target profile.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	targetProfileDir := vault.ProfilePath("codex", "target")
	if err := os.MkdirAll(targetProfileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(target profile) error = %v", err)
	}
	target := []byte(`{"access_token":"target","token_type":"Bearer"}`)
	if err := os.WriteFile(filepath.Join(targetProfileDir, "auth.json"), target, 0600); err != nil {
		t.Fatalf("WriteFile(target auth) error = %v", err)
	}

	// Create an active cooldown for codex/target.
	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open() error = %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	if _, err := db.SetCooldown("codex", "target", now, 60*time.Minute, ""); err != nil {
		t.Fatalf("SetCooldown() error = %v", err)
	}

	// Without --force, activation should not switch.
	denyCmd := &cobra.Command{}
	denyCmd.SetIn(strings.NewReader("n\n"))
	denyCmd.SetOut(ioDiscard{})
	denyCmd.Flags().Bool("backup-current", false, "")
	denyCmd.Flags().Bool("force", false, "")

	err = runActivate(denyCmd, []string{"codex", "target"})
	if err == nil {
		// In interactive runs, user answered "n"; still should not switch.
	} else {
		// In non-interactive runs, cooldown check returns an error to avoid blocking.
	}

	gotActive, readErr := os.ReadFile(authPath)
	if readErr != nil {
		t.Fatalf("ReadFile(active auth) error = %v", readErr)
	}
	if string(gotActive) != string(original) {
		t.Fatalf("auth switched despite cooldown: got %q want %q", gotActive, original)
	}

	// With --force, activation should proceed.
	forceCmd := &cobra.Command{}
	forceCmd.SetOut(ioDiscard{})
	forceCmd.Flags().Bool("backup-current", false, "")
	forceCmd.Flags().Bool("force", true, "")
	_ = forceCmd.Flags().Set("force", "true")

	if err := runActivate(forceCmd, []string{"codex", "target"}); err != nil {
		t.Fatalf("runActivate(--force) error = %v", err)
	}

	gotForced, readErr := os.ReadFile(authPath)
	if readErr != nil {
		t.Fatalf("ReadFile(active auth after force) error = %v", readErr)
	}
	if string(gotForced) != string(target) {
		t.Fatalf("auth mismatch after force: got %q want %q", gotForced, target)
	}
}

// ioDiscard is a minimal io.Writer that drops writes without importing io in this test file.
type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }
