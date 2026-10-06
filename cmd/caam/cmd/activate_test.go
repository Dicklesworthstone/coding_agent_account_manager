package cmd

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/spf13/cobra"
)

func TestActivate_AutoBackupsOriginalOnFirstSwitch(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate Codex auth location.
	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}

	original := []byte(`{"access_token":"original","token_type":"Bearer"}`)
	originalAuthPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(originalAuthPath, original, 0600); err != nil {
		t.Fatalf("WriteFile(original auth) error = %v", err)
	}

	// Use a temp vault.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	// Create a target profile in the vault with different contents.
	targetProfileDir := vault.ProfilePath("codex", "target")
	if err := os.MkdirAll(targetProfileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(target profile) error = %v", err)
	}
	target := []byte(`{"access_token":"target","token_type":"Bearer"}`)
	if err := os.WriteFile(filepath.Join(targetProfileDir, "auth.json"), target, 0600); err != nil {
		t.Fatalf("WriteFile(target auth) error = %v", err)
	}

	if err := runActivate(activateCmd, []string{"codex", "target"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	// Ensure the original state was preserved as a system `_original` profile.
	origBackupPath := vault.BackupPath("codex", "_original", "auth.json")
	gotOriginal, err := os.ReadFile(origBackupPath)
	if err != nil {
		t.Fatalf("ReadFile(_original auth) error = %v", err)
	}
	if string(gotOriginal) != string(original) {
		t.Fatalf("_original auth mismatch: got %q want %q", gotOriginal, original)
	}

	// Ensure metadata marks it as a system "first-activate" backup.
	metaRaw, err := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "_original"), "meta.json"))
	if err != nil {
		t.Fatalf("ReadFile(_original meta.json) error = %v", err)
	}
	var meta struct {
		Type          string   `json:"type"`
		CreatedBy     string   `json:"created_by"`
		OriginalPaths []string `json:"original_paths"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("Unmarshal(_original meta.json) error = %v", err)
	}
	if meta.Type != "system" {
		t.Fatalf("meta.type = %q, want %q", meta.Type, "system")
	}
	if meta.CreatedBy != "first-activate" {
		t.Fatalf("meta.created_by = %q, want %q", meta.CreatedBy, "first-activate")
	}
	if len(meta.OriginalPaths) != 1 || meta.OriginalPaths[0] != originalAuthPath {
		t.Fatalf("meta.original_paths = %v, want [%s]", meta.OriginalPaths, originalAuthPath)
	}

	// Ensure activation actually switched the auth file.
	gotActive, err := os.ReadFile(originalAuthPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(gotActive) != string(target) {
		t.Fatalf("active auth mismatch: got %q want %q", gotActive, target)
	}
}

func TestActivate_AutoBackupsUnsavedStateBeforeSwitch(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate Codex auth location.
	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	// Isolate CAAM_HOME (SPM config lives here).
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}

	unsaved := []byte(`{"access_token":"unsaved","token_type":"Bearer"}`)
	authPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(authPath, unsaved, 0600); err != nil {
		t.Fatalf("WriteFile(current auth) error = %v", err)
	}

	// Configure safety settings to keep only 1 auto-backup.
	if err := os.MkdirAll(os.Getenv("CAAM_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CAAM_HOME) error = %v", err)
	}
	spmCfg := []byte("version: 1\nsafety:\n  auto_backup_before_switch: smart\n  max_auto_backups: 1\n")
	if err := os.WriteFile(filepath.Join(os.Getenv("CAAM_HOME"), "config.yaml"), spmCfg, 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	// Use a temp vault.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	// Ensure _original exists so the first-activate backup does not suppress the smart backup.
	if err := os.MkdirAll(vault.ProfilePath("codex", "_original"), 0700); err != nil {
		t.Fatalf("MkdirAll(_original) error = %v", err)
	}

	// Pre-create an old auto-backup that should be rotated out.
	oldBackup := "_backup_20000101_000000"
	oldBackupDir := vault.ProfilePath("codex", oldBackup)
	if err := os.MkdirAll(oldBackupDir, 0700); err != nil {
		t.Fatalf("MkdirAll(old backup) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldBackupDir, "auth.json"), []byte(`{"access_token":"old"}`), 0600); err != nil {
		t.Fatalf("WriteFile(old backup auth) error = %v", err)
	}

	// Create a target profile in the vault with different contents.
	targetProfileDir := vault.ProfilePath("codex", "target")
	if err := os.MkdirAll(targetProfileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(target profile) error = %v", err)
	}
	target := []byte(`{"access_token":"target","token_type":"Bearer"}`)
	if err := os.WriteFile(filepath.Join(targetProfileDir, "auth.json"), target, 0600); err != nil {
		t.Fatalf("WriteFile(target auth) error = %v", err)
	}

	if err := runActivate(activateCmd, []string{"codex", "target"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	// Ensure activation actually switched the auth file.
	gotActive, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(gotActive) != string(target) {
		t.Fatalf("active auth mismatch: got %q want %q", gotActive, target)
	}

	// Ensure an auto-backup was created for the unsaved state and old backups were rotated out.
	entries, err := os.ReadDir(filepath.Join(vault.BasePath(), "codex"))
	if err != nil {
		t.Fatalf("ReadDir(codex) error = %v", err)
	}
	var backupProfiles []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "_backup_") {
			backupProfiles = append(backupProfiles, e.Name())
		}
	}
	if len(backupProfiles) != 1 {
		t.Fatalf("found %d backup profiles, want 1: %v", len(backupProfiles), backupProfiles)
	}
	if backupProfiles[0] == oldBackup {
		t.Fatalf("expected old backup rotated out, but found %q", backupProfiles[0])
	}

	gotBackup, err := os.ReadFile(vault.BackupPath("codex", backupProfiles[0], "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile(auto-backup auth) error = %v", err)
	}
	if string(gotBackup) != string(unsaved) {
		t.Fatalf("auto-backup auth mismatch: got %q want %q", gotBackup, unsaved)
	}
}

func codexSwitchTestCredential(t *testing.T, account, token string, refreshed time.Time) string {
	t.Helper()
	claims, err := json.Marshal(map[string]any{
		"sub": account, "email": account + "@example.invalid",
		"iat": refreshed.Unix(), "exp": refreshed.Add(24 * time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".SYNTHETIC"
	return fmt.Sprintf(`{"tokens":{"account_id":%q,"id_token":%q,"access_token":%q,"refresh_token":%q},"last_refresh":%q}`,
		account, jwt, jwt, "SYNTHETIC-"+token, refreshed.Format(time.RFC3339))
}

func runActivateJSONCommand(t *testing.T, tool, target string, flags ...string) (activateOutput, error) {
	t.Helper()
	command := &cobra.Command{
		Use: activateCmd.Use, Args: activateCmd.Args, RunE: runActivate,
		SilenceUsage: true, SilenceErrors: true,
	}
	command.Flags().Bool("json", true, "")
	command.Flags().Bool("auto", false, "")
	command.Flags().Bool("force", false, "")
	command.Flags().Bool("backup-current", false, "")
	command.Flags().Bool("reload-daemon", false, "")
	command.SetArgs(append([]string{tool, target}, flags...))
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	err := command.Execute()
	if strings.Contains(stdout.String()+stderr.String(), "SYNTHETIC-") {
		t.Fatal("activation output exposed credential material")
	}
	var output activateOutput
	decoder := json.NewDecoder(&stdout)
	if decodeErr := decoder.Decode(&output); decodeErr != nil {
		t.Fatalf("activation did not return JSON: %v (command error: %v)", decodeErr, err)
	}
	if decodeErr := decoder.Decode(new(any)); decodeErr != io.EOF {
		t.Fatalf("activation returned extra output: %v", decodeErr)
	}
	return output, err
}

func requireSwitchCredential(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != expected {
		t.Fatalf("unexpected credential contents at %s", path)
	}
}

func TestActivateSwitchPreservesRotatedOutgoingCredential(t *testing.T) {
	home := setupRobotCredentialEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	old := codexSwitchTestCredential(t, "work", "SPENT-REFRESH", now.Add(-time.Hour))
	live := codexSwitchTestCredential(t, "work", "LIVE-REFRESH", now)
	target := codexSwitchTestCredential(t, "personal", "TARGET-REFRESH", now)
	writeRobotCredentialProfile(t, "codex", "work", map[string]string{"auth.json": old})
	writeRobotCredentialProfile(t, "codex", "personal", map[string]string{"auth.json": target})
	livePath := filepath.Join(home, ".codex", "auth.json")
	writeNativeTestCredential(t, livePath, live)

	output, err := runActivateJSONCommand(t, "codex", "personal")
	if err != nil || !output.Success || output.ResnapshottedProfile != "work" || output.PreviousProfile != "work" {
		t.Fatalf("switch did not report preserving the outgoing owner: %+v, %v", output, err)
	}
	requireSwitchCredential(t, vault.BackupPath("codex", "work", "auth.json"), live)
	requireSwitchCredential(t, livePath, target)
}

func TestActivateSwitchKeepsNewerLiveCredential(t *testing.T) {
	home := setupRobotCredentialEnv(t)
	now := time.Now().UTC().Truncate(time.Second)
	saved := codexSwitchTestCredential(t, "work", "SPENT-REFRESH", now.Add(-time.Hour))
	live := codexSwitchTestCredential(t, "work", "LIVE-REFRESH", now)
	writeRobotCredentialProfile(t, "codex", "work", map[string]string{"auth.json": saved})
	livePath := filepath.Join(home, ".codex", "auth.json")
	writeNativeTestCredential(t, livePath, live)

	output, err := runActivateJSONCommand(t, "codex", "work")
	if err != nil || !output.Success || !output.KeptLive || output.AutoBackup != "" {
		t.Fatalf("same-account activation did not retain newer credentials: %+v, %v", output, err)
	}
	requireSwitchCredential(t, livePath, live)
	profiles, err := vault.List("codex")
	if err != nil || len(profiles) != 1 || profiles[0] != "work" {
		t.Fatalf("same-account no-op unexpectedly changed profile membership: %v, %v", profiles, err)
	}
}

func TestActivateSwitchRejectsInvalidTargetBeforePreservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
	}{
		{"missing-credential", nil},
		{"malformed-json", map[string]string{"auth.json": `{"access_token":`}},
		{"null-json", map[string]string{"auth.json": `null`}},
		{"array-json", map[string]string{"auth.json": `[]`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setupRobotCredentialEnv(t)
			livePath := filepath.Join(home, ".codex", "auth.json")
			live := `{"access_token":"SYNTHETIC-UNSAVED-LOGIN"}`
			writeNativeTestCredential(t, livePath, live)
			writeRobotCredentialProfile(t, "codex", "target", tc.files)
			output, err := runActivateJSONCommand(t, "codex", "target")
			if err == nil || output.Success {
				t.Fatalf("invalid target was accepted: %+v, %v", output, err)
			}
			requireSwitchCredential(t, livePath, live)
			profiles, listErr := vault.List("codex")
			if listErr != nil || len(profiles) != 1 || profiles[0] != "target" {
				t.Fatalf("invalid target created recovery profiles: %v, %v", profiles, listErr)
			}
			for name, content := range tc.files {
				requireSwitchCredential(t, vault.BackupPath("codex", "target", name), content)
			}
		})
	}
}

func TestActivateSwitchBackupPolicy(t *testing.T) {
	for _, tc := range []struct {
		mode        string
		forceBackup bool
		wantBackup  bool
	}{
		{"smart", false, true},
		{"always", false, true},
		{"never", false, false},
		{"never", true, true},
	} {
		t.Run(fmt.Sprintf("%s/override=%t", tc.mode, tc.forceBackup), func(t *testing.T) {
			home := setupRobotCredentialEnv(t)
			writeNativeTestCredential(t, filepath.Join(home, "caam", "config.yaml"),
				fmt.Sprintf("version: 1\nsafety:\n  auto_backup_before_switch: %s\n  max_auto_backups: 5\n", tc.mode))
			// A previous first-activation backup exists; test the configured policy.
			writeRobotCredentialProfile(t, "codex", "_original", map[string]string{"auth.json": `{"access_token":"SYNTHETIC-ORIGINAL"}`})
			live := `{"access_token":"SYNTHETIC-UNSAVED"}`
			target := `{"access_token":"SYNTHETIC-TARGET"}`
			livePath := filepath.Join(home, ".codex", "auth.json")
			writeNativeTestCredential(t, livePath, live)
			writeRobotCredentialProfile(t, "codex", "target", map[string]string{"auth.json": target})
			var flags []string
			if tc.forceBackup {
				flags = append(flags, "--backup-current")
			}
			output, err := runActivateJSONCommand(t, "codex", "target", flags...)
			if err != nil || !output.Success || (output.AutoBackup != "") != tc.wantBackup {
				t.Fatalf("unexpected backup policy result: %+v, %v", output, err)
			}
			requireSwitchCredential(t, livePath, target)
			if tc.wantBackup {
				requireSwitchCredential(t, vault.BackupPath("codex", output.AutoBackup, "auth.json"), live)
			}
		})
	}
}
