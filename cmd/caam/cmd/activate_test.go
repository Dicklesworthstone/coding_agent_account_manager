package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
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

func requireClaudeSavedOrganization(t *testing.T, profile, accountID, organizationID, email string, credential []byte) {
	t.Helper()
	credentialPath := vault.BackupPath("claude", profile, ".credentials.json")
	requireSwitchCredential(t, credentialPath, string(credential))
	binding, err := identity.ReadClaudeIdentityBinding(credentialPath)
	if err != nil || binding == nil || binding.Identity.AccountID != accountID || binding.Identity.OrganizationID != organizationID || binding.Identity.Email != email || !binding.MatchesCredentials(credential) {
		t.Fatalf("saved profile %s lost verified token ownership: %v", profile, err)
	}
	settings, err := os.ReadFile(vault.BackupPath("claude", profile, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Account struct {
			AccountID      string `json:"accountUuid"`
			OrganizationID string `json:"organizationUuid"`
			Email          string `json:"emailAddress"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal(settings, &doc); err != nil || doc.Account.AccountID != accountID || doc.Account.OrganizationID != organizationID || doc.Account.Email != email {
		t.Fatalf("saved profile %s retained mismatched OAuth metadata: %v", profile, err)
	}
}

func TestBackupClaudeCorrectsStaleOrganizationWithoutChangingNativeFiles(t *testing.T) {
	fileSet, credentials := setupClaudeOrganizationProfiles(t)
	writeNativeTestCredential(t, fileSet.Files[0].Path, string(credentials["team"]))
	writeNativeTestCredential(t, fileSet.Files[1].Path, `{"numStartups":12,"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-personal","organizationName":"Personal"}}`)
	before, err := captureBackupTree(filepath.Dir(fileSet.Files[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		if !bytes.Equal(data, credentials["team"]) {
			return nil, fmt.Errorf("unexpected credential generation")
		}
		return claudeCLIIdentityBinding(t, data, "org-team", "Team"), nil
	}
	command := &cobra.Command{}
	command.Flags().Bool("json", true, "")
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	if err := runBackup(command, []string{"claude", "team-snapshot"}); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	var output backupOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || !output.Success {
		t.Fatalf("backup did not report success: %v", err)
	}
	if strings.Contains(stdout.String(), "SYNTHETIC-") {
		t.Fatal("backup exposed token material")
	}
	requireClaudeSavedOrganization(t, "team-snapshot", "shared-account-id", "org-team", "shared@example.test", credentials["team"])
	assertBackupTreeUnchanged(t, filepath.Dir(fileSet.Files[0].Path), before)
}

func TestBackupClaudeRejectsReplacingNamedProfileWithDifferentOrganization(t *testing.T) {
	fileSet, credentials := setupClaudeOrganizationProfiles(t)
	writeNativeTestCredential(t, fileSet.Files[0].Path, string(credentials["team"]))
	writeNativeTestCredential(t, fileSet.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-personal"}}`)
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		return claudeCLIIdentityBinding(t, data, "org-team", "Team"), nil
	}
	// An initialized vault retains its private process lock between commands.
	// Include it in the baseline so the refusal must leave the entire tree intact.
	if err := os.WriteFile(filepath.Join(vault.BasePath(), ".caam-mutation.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := captureBackupTree(vault.BasePath())
	if err != nil {
		t.Fatal(err)
	}
	command := &cobra.Command{}
	command.Flags().Bool("json", true, "")
	var stdout bytes.Buffer
	command.SetOut(&stdout)
	err = runBackup(command, []string{"claude", "personal"})
	var output backupOutput
	if err == nil || json.Unmarshal(stdout.Bytes(), &output) != nil || output.Success || output.Error == "" {
		t.Fatalf("backup replaced an existing different organization: %v", err)
	}
	if strings.Contains(stdout.String(), "SYNTHETIC-") {
		t.Fatal("failed backup exposed token material")
	}
	assertBackupTreeUnchanged(t, vault.BasePath(), before)
}

func TestBackupClaudeUnavailableIdentityIsPreservedWithExplicitWarning(t *testing.T) {
	for _, verificationError := range []error{identity.ErrClaudeIdentityUnavailable, identity.ErrClaudeTokenExpired} {
		t.Run(verificationError.Error(), func(t *testing.T) {
			fileSet, _ := setupClaudeOrganizationProfiles(t)
			credential := `{"claudeAiOauth":{"accessToken":"SYNTHETIC-offline-access","refreshToken":"SYNTHETIC-offline-refresh","expiresAt":1600000000000}}`
			settings := `{"numStartups":4,"oauthAccount":{"accountUuid":"stale-account","emailAddress":"stale@example.test","organizationUuid":"stale-org"}}`
			writeNativeTestCredential(t, fileSet.Files[0].Path, credential)
			writeNativeTestCredential(t, fileSet.Files[1].Path, settings)
			vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				return nil, verificationError
			}
			command := &cobra.Command{}
			command.Flags().Bool("json", true, "")
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			if err := runBackup(command, []string{"claude", "offline"}); err != nil {
				t.Fatalf("offline preservation failed: %v", err)
			}
			var output backupOutput
			if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || !output.Success || len(output.Warnings) != 1 || output.Warnings[0] != claudeBackupIdentityWarning {
				t.Fatalf("backup did not explain unverified identity: %v", err)
			}
			requireSwitchCredential(t, vault.BackupPath("claude", "offline", ".credentials.json"), credential)
			saved, err := os.ReadFile(vault.BackupPath("claude", "offline", ".claude.json"))
			if err != nil {
				t.Fatal(err)
			}
			requireOfflineClaudeSnapshot(t, ".claude.json", []byte(settings), saved)
			requireSwitchCredential(t, fileSet.Files[0].Path, credential)
			requireSwitchCredential(t, fileSet.Files[1].Path, settings)
			if strings.Contains(stdout.String(), "SYNTHETIC-") {
				t.Fatal("offline backup exposed token material")
			}
		})
	}
}

func TestActivateClaudeAutoBackupKeepsTokenAndOrganizationTogether(t *testing.T) {
	fileSet, credentials := setupClaudeOrganizationProfiles(t)
	// A different login arrived while an older Claude session left its
	// account metadata behind. It must not overwrite either named profile.
	unsaved := []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-other-access","refreshToken":"SYNTHETIC-other-refresh","expiresAt":%d,"subscriptionType":"team"}}`, time.Now().Add(4*time.Hour).UnixMilli()))
	writeNativeTestCredential(t, fileSet.Files[0].Path, string(unsaved))
	writeNativeTestCredential(t, fileSet.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-personal","organizationName":"Personal"}}`)
	writeNativeTestCredential(t, vault.BackupPath("claude", "_original", ".credentials.json"), string(credentials["personal"]))
	writeNativeTestCredential(t, filepath.Join(os.Getenv("CAAM_HOME"), "config.yaml"), "version: 1\nanalytics:\n  enabled: false\nsafety:\n  auto_backup_before_switch: smart\n  max_auto_backups: 5\n")
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		if bytes.Equal(data, unsaved) {
			binding := claudeCLIIdentityBinding(t, data, "org-other", "Other Team")
			binding.Identity.AccountID = "other-account-id"
			binding.Identity.Email = "other@example.test"
			return binding, nil
		}
		for name, credential := range credentials {
			if bytes.Equal(data, credential) {
				return claudeCLIIdentityBinding(t, data, "org-"+name, "Shared display name"), nil
			}
		}
		return nil, fmt.Errorf("unexpected credential generation")
	}
	output, err := runActivateJSONCommand(t, "claude", "personal")
	if err != nil || !output.Success || !strings.HasPrefix(output.AutoBackup, "_backup_") || output.PreviousProfile != "" || output.ResnapshottedProfile != "" {
		t.Fatalf("switch did not safely preserve the unsaved account: %+v, %v", output, err)
	}
	requireSwitchCredential(t, fileSet.Files[0].Path, string(credentials["personal"]))
	for name, credential := range credentials {
		requireSwitchCredential(t, vault.BackupPath("claude", name, ".credentials.json"), string(credential))
	}
	requireClaudeSavedOrganization(t, output.AutoBackup, "other-account-id", "org-other", "other@example.test", unsaved)
	// Restarted reporting must still recover the backup's actual token owner.
	vault = authfile.NewVault(vault.BasePath())
	id := getVaultIdentity("claude", output.AutoBackup)
	if id == nil || !id.Verified || id.AccountID != "other-account-id" || id.OrganizationID != "org-other" || id.Email != "other@example.test" {
		t.Fatal("restart lost the automatic backup's identity binding")
	}
	var liveSettings struct {
		Account struct {
			OrganizationID string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	live, err := os.ReadFile(fileSet.Files[1].Path)
	if err != nil || json.Unmarshal(live, &liveSettings) != nil || liveSettings.Account.OrganizationID != "org-personal" {
		t.Fatalf("activation did not publish target organization metadata: %v", err)
	}
}

func TestActivateClaudeSameEmailDifferentOrganizationDoesNotKeepWrongLiveGrant(t *testing.T) {
	fileSet, credentials := setupClaudeOrganizationProfiles(t)
	rotatedPersonal := []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-personal-rotated-access","refreshToken":"SYNTHETIC-personal-rotated-refresh","expiresAt":%d,"subscriptionType":"max"}}`, time.Now().Add(8*time.Hour).UnixMilli()))
	writeNativeTestCredential(t, fileSet.Files[0].Path, string(rotatedPersonal))
	// A longer-lived Personal grant plus stale Team metadata must not make
	// activating Team a false same-account no-op.
	writeNativeTestCredential(t, fileSet.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-team","organizationName":"Team"}}`)
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		if bytes.Equal(data, rotatedPersonal) {
			return claudeCLIIdentityBinding(t, data, "org-personal", "Personal"), nil
		}
		for name, credential := range credentials {
			if bytes.Equal(data, credential) {
				return claudeCLIIdentityBinding(t, data, "org-"+name, "Shared display name"), nil
			}
		}
		return nil, fmt.Errorf("unexpected credential generation")
	}
	output, err := runActivateJSONCommand(t, "claude", "team")
	if err != nil || !output.Success || output.KeptLive || output.PreviousProfile != "personal" || output.ResnapshottedProfile != "personal" {
		t.Fatalf("same email confused organization activation: %+v, %v", output, err)
	}
	requireSwitchCredential(t, fileSet.Files[0].Path, string(credentials["team"]))
	requireSwitchCredential(t, vault.BackupPath("claude", "personal", ".credentials.json"), string(rotatedPersonal))
	bound, err := identity.ReadClaudeIdentityBinding(vault.BackupPath("claude", "personal", ".credentials.json"))
	if err != nil || bound == nil || bound.Identity.OrganizationID != "org-personal" || !bound.MatchesCredentials(rotatedPersonal) {
		t.Fatalf("preserved refresh lost its personal organization binding: %v", err)
	}
}

func TestActivateClaudeRejectsMismatchedAccessTokenBeforeChangingLive(t *testing.T) {
	fileSet, credentials := setupClaudeOrganizationProfiles(t)
	writeNativeTestCredential(t, fileSet.Files[0].Path, string(credentials["team"]))
	writeNativeTestCredential(t, fileSet.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-team"}}`)
	var tampered map[string]any
	if err := json.Unmarshal(credentials["personal"], &tampered); err != nil {
		t.Fatal(err)
	}
	tampered["claudeAiOauth"].(map[string]any)["accessToken"] = "SYNTHETIC-replaced-access"
	changed, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	writeNativeTestCredential(t, vault.BackupPath("claude", "personal", ".credentials.json"), string(changed))
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		return claudeCLIIdentityBinding(t, data, "org-team", "Team"), nil
	}
	// Retain the normal inert lock in the baseline; no credential, metadata,
	// or other vault-tree change is permitted when activation is refused.
	if err := os.WriteFile(filepath.Join(vault.BasePath(), ".caam-mutation.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	liveBefore, err := captureBackupTree(filepath.Dir(fileSet.Files[0].Path))
	if err != nil {
		t.Fatal(err)
	}
	vaultBefore, err := captureBackupTree(vault.BasePath())
	if err != nil {
		t.Fatal(err)
	}
	output, err := runActivateJSONCommand(t, "claude", "personal")
	if err == nil || output.Success || output.Error == "" {
		t.Fatalf("mismatched saved access token was activated: %+v, %v", output, err)
	}
	assertBackupTreeUnchanged(t, filepath.Dir(fileSet.Files[0].Path), liveBefore)
	assertBackupTreeUnchanged(t, vault.BasePath(), vaultBefore)
}

func TestActivateClaudeLegacyProfileRequiresProviderIdentity(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			files, credentials := setupClaudeOrganizationProfiles(t)
			writeNativeTestCredential(t, vault.BackupPath("claude", "team", "meta.json"), `{"identity":"shared@example.test","description":"legacy Team label"}`)
			writeNativeTestCredential(t, files.Files[0].Path, string(credentials["personal"]))
			writeNativeTestCredential(t, files.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","organizationUuid":"org-personal"}}`)
			writeNativeTestCredential(t, filepath.Join(vault.BasePath(), ".caam-mutation.lock"), "")
			before, err := captureBackupTree(vault.BasePath())
			if err != nil {
				t.Fatal(err)
			}
			liveBefore, err := captureBackupTree(filepath.Dir(files.Files[0].Path))
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			oldTransport := http.DefaultTransport
			http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet || req.URL.String() != "https://api.anthropic.com/api/oauth/profile" ||
					req.Header.Get("Authorization") != "Bearer SYNTHETIC-team-access" {
					t.Fatal("identity request did not verify the selected saved credential")
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"SYNTHETIC-private-response"}`)), Header: make(http.Header)}, nil
			})
			t.Cleanup(func() { http.DefaultTransport = oldTransport })
			// Force can bypass a cooldown, never missing ownership evidence.
			output, err := runActivateJSONCommand(t, "claude", "team", "--force")
			if err == nil || output.Success || calls != 1 || !strings.Contains(output.Error, "could not be verified") {
				t.Fatalf("legacy profile was activated without a provider identity: %+v, %v; calls=%d", output, err, calls)
			}
			assertBackupTreeUnchanged(t, vault.BasePath(), before)
			assertBackupTreeUnchanged(t, filepath.Dir(files.Files[0].Path), liveBefore)
		})
	}
}

func TestActivateClaudeLegacyProfileVerifiesOnceAndKeepsLabel(t *testing.T) {
	files, credentials := setupClaudeOrganizationProfiles(t)
	// A deliberately misleading name stays a label; the provider's UUIDs
	// determine the selected identity and the CLI exposes them explicitly.
	const label = "personal-looking-label"
	if err := os.Rename(vault.ProfilePath("claude", "team"), vault.ProfilePath("claude", label)); err != nil {
		t.Fatal(err)
	}
	writeNativeTestCredential(t, vault.BackupPath("claude", label, "meta.json"), `{"identity":"shared@example.test","description":"keep operator label"}`)
	writeNativeTestCredential(t, files.Files[0].Path, string(credentials["personal"]))
	writeNativeTestCredential(t, files.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","organizationUuid":"org-personal"},"numStartups":42}`)
	calls := 0
	oldTransport := http.DefaultTransport
	http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.String() != "https://api.anthropic.com/api/oauth/profile" || req.Header.Get("Authorization") != "Bearer SYNTHETIC-team-access" {
			t.Fatal("provider request used a different account's token")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"account":{"uuid":"shared-account-id","email":"shared@example.test"},"organization":{"uuid":"org-team","name":"Team"}}`)), Header: make(http.Header)}, nil
	})
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	output, err := runActivateJSONCommand(t, "claude", label)
	if err != nil || !output.Success || output.Profile != label || output.Identity == nil || !output.Identity.Verified || output.Identity.OrganizationID != "org-team" || calls != 1 {
		t.Fatalf("legacy activation failed to expose the verified owner: %+v, %v; calls=%d", output, err, calls)
	}
	requireSwitchCredential(t, files.Files[0].Path, string(credentials["team"]))
	requireClaudeSavedOrganization(t, label, "shared-account-id", "org-team", "shared@example.test", credentials["team"])
	// A restarted command can rely on the exact persisted pair, including
	// after another Claude session restores a stale shared organization label.
	vault = authfile.NewVault(vault.BasePath())
	writeNativeTestCredential(t, files.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","organizationUuid":"org-personal"},"numStartups":43}`)
	output, err = runActivateJSONCommand(t, "claude", label)
	if err != nil || !output.Success || output.Identity == nil || output.Identity.OrganizationID != "org-team" || calls != 1 {
		t.Fatalf("repeated activation lost the proven identity or required network: %+v, %v; calls=%d", output, err, calls)
	}
	listed := runLsJSON(t, []string{"claude"})
	for _, row := range listed["profiles"].([]any) {
		profile := row.(map[string]any)
		if profile["active"] != (profile["name"] == label) {
			t.Fatal("real listing selected a stale shared label after legacy verification")
		}
	}
}

func TestActivateClaudeRefusesSourceChangesDuringLegacyVerification(t *testing.T) {
	for _, changed := range []string{"saved-credentials", "saved-binding", "live-settings"} {
		t.Run(changed, func(t *testing.T) {
			files, credentials := setupClaudeOrganizationProfiles(t)
			writeNativeTestCredential(t, vault.BackupPath("claude", "team", "meta.json"), `{"identity":"shared@example.test"}`)
			writeNativeTestCredential(t, files.Files[0].Path, string(credentials["personal"]))
			writeNativeTestCredential(t, files.Files[1].Path, `{"oauthAccount":{"organizationUuid":"org-personal"}}`)
			vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				if !bytes.Equal(data, credentials["team"]) {
					t.Fatal("unexpected provider credential")
				}
				switch changed {
				case "saved-credentials":
					writeNativeTestCredential(t, vault.BackupPath("claude", "team", ".credentials.json"), string(credentials["personal"]))
				case "saved-binding":
					proof := claudeCLIIdentityBinding(t, data, "org-personal", "Personal")
					metadata, err := json.Marshal(map[string]any{"claude_identity": proof})
					if err != nil {
						t.Fatal(err)
					}
					writeNativeTestCredential(t, vault.BackupPath("claude", "team", "meta.json"), string(metadata))
				case "live-settings":
					// The target is verified first. A live metadata write at this
					// point is captured afterward, so it is safely normalized.
					writeNativeTestCredential(t, files.Files[1].Path, `{"oauthAccount":{"organizationUuid":"org-unrelated"},"numStartups":91}`)
				}
				return claudeCLIIdentityBinding(t, data, "org-team", "Team"), nil
			}
			output, err := runActivateJSONCommand(t, "claude", "team")
			if changed == "live-settings" {
				if err != nil || !output.Success || output.Identity == nil || output.Identity.OrganizationID != "org-team" {
					t.Fatalf("captured stale native label blocked a verified activation: %+v, %v", output, err)
				}
				requireSwitchCredential(t, files.Files[0].Path, string(credentials["team"]))
			} else {
				if err == nil || output.Success {
					t.Fatalf("concurrent saved source change was activated: %+v, %v", output, err)
				}
				requireSwitchCredential(t, files.Files[0].Path, string(credentials["personal"]))
			}
		})
	}
}

func TestBackupClaudeReverifiesLegacyOwnerBeforeOverwrite(t *testing.T) {
	for _, organization := range []string{"org-team", "org-personal"} {
		t.Run(organization, func(t *testing.T) {
			files, credentials := setupClaudeOrganizationProfiles(t)
			writeNativeTestCredential(t, vault.BackupPath("claude", "team", "meta.json"), `{"identity":"shared@example.test","description":"legacy Team"}`)
			const rotated = `{"claudeAiOauth":{"accessToken":"SYNTHETIC-rotated-access","refreshToken":"SYNTHETIC-rotated-refresh","expiresAt":4102444800000}}`
			writeNativeTestCredential(t, files.Files[0].Path, rotated)
			writeNativeTestCredential(t, files.Files[1].Path, `{"oauthAccount":{"emailAddress":"shared@example.test","organizationUuid":"org-team"}}`)
			verifiedLegacy := false
			vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				if bytes.Equal(data, credentials["team"]) {
					verifiedLegacy = true
					return claudeCLIIdentityBinding(t, data, "org-team", "Team"), nil
				}
				if string(data) != rotated {
					t.Fatal("backup verified the wrong captured source")
				}
				return claudeCLIIdentityBinding(t, data, organization, "Shared email"), nil
			}
			command := &cobra.Command{Use: backupCmd.Use, Args: backupCmd.Args, RunE: runBackup, SilenceErrors: true, SilenceUsage: true}
			command.Flags().Bool("json", false, "")
			var stdout bytes.Buffer
			command.SetOut(&stdout)
			command.SetErr(&stdout)
			command.SetArgs([]string{"claude", "team", "--json"})
			err := command.Execute()
			var result backupOutput
			if json.Unmarshal(stdout.Bytes(), &result) != nil || !verifiedLegacy || strings.Contains(stdout.String(), "SYNTHETIC-") {
				t.Fatal("backup skipped the legacy owner's verification or exposed credentials")
			}
			if organization == "org-team" {
				if err != nil || !result.Success {
					t.Fatalf("verified same-owner refresh was not backed up: %v", err)
				}
				requireClaudeSavedOrganization(t, "team", "shared-account-id", "org-team", "shared@example.test", []byte(rotated))
			} else {
				if err == nil || result.Success {
					t.Fatal("shared email allowed a different organization's grant to replace a legacy profile")
				}
				requireSwitchCredential(t, vault.BackupPath("claude", "team", ".credentials.json"), string(credentials["team"]))
			}
			requireSwitchCredential(t, files.Files[0].Path, rotated)
		})
	}
}
