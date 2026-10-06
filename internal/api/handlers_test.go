package api

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "<1m"},
		{1 * time.Minute, "1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h 0m"},
		{1*time.Hour + 30*time.Minute, "1h 30m"},
		{2 * time.Hour, "2h 0m"},
		{2*time.Hour + 45*time.Minute, "2h 45m"},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestNewHandlers(t *testing.T) {
	// Test with nil dependencies
	h := NewHandlers(nil, nil, nil)
	if h == nil {
		t.Fatal("NewHandlers() returned nil")
	}
}

func TestGetStatusWithNilDeps(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should not panic with nil vault
	// Note: This will return empty tools since vault is nil
	status, err := h.GetStatus()
	if err != nil {
		t.Fatalf("GetStatus() error = %v", err)
	}
	if status == nil {
		t.Fatal("GetStatus() returned nil")
	}
	if status.Version == "" {
		t.Error("GetStatus() version is empty")
	}
	if status.Timestamp == "" {
		t.Error("GetStatus() timestamp is empty")
	}
}

func TestGetProfilesWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// With nil vault, should return error when trying to list
	_, err := h.GetProfiles("")
	if err == nil {
		t.Error("GetProfiles() expected error with nil vault")
	}
}

func TestGetProfilesWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	_, err := h.GetProfiles("unknown-tool")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("GetProfiles() expected unknown tool error, got %v", err)
	}
}

func TestGetUsageWithNilDeps(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return empty usage without error
	usage, err := h.GetUsage("")
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage == nil {
		t.Fatal("GetUsage() returned nil")
	}
	if len(usage.Usage) != 0 {
		t.Errorf("GetUsage() with nil deps should return empty, got %d entries", len(usage.Usage))
	}
}

func TestGetCoordinators(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return empty coordinators list
	coords, err := h.GetCoordinators()
	if err != nil {
		t.Fatalf("GetCoordinators() error = %v", err)
	}
	if coords == nil {
		t.Fatal("GetCoordinators() returned nil")
	}
	if coords.Coordinators == nil {
		t.Error("GetCoordinators() coordinators list is nil")
	}
}

func TestActivateWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := ActivateRequest{
		Tool:    "unknown",
		Profile: "test",
	}

	_, err := h.Activate(req)
	if err == nil {
		t.Error("Activate() expected error for unknown tool")
	}
}

func TestActivateWithMissingProfile(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := ActivateRequest{
		Tool:    "codex",
		Profile: "",
	}

	_, err := h.Activate(req)
	if err == nil || !strings.Contains(err.Error(), "profile is required") {
		t.Errorf("Activate() expected profile required error, got %v", err)
	}
}

func TestActivatePreservesOutgoingCredentials(t *testing.T) {
	for _, tc := range []struct {
		name       string
		liveOwner  string
		backupMode string
		malformed  bool
		wantNamed  bool
		wantBackup bool
	}{
		{name: "rotated named login", liveOwner: "alice", wantNamed: true},
		{name: "unmatched login", liveOwner: "carol", wantBackup: true},
		{name: "always backs up named login", liveOwner: "alice", backupMode: "always", wantNamed: true, wantBackup: true},
		{name: "never retains named login", liveOwner: "alice", backupMode: "never", wantNamed: true},
		{name: "never skips unnamed backup", liveOwner: "carol", backupMode: "never"},
		{name: "malformed target leaves named login intact", liveOwner: "alice", malformed: true},
		{name: "malformed target leaves unmatched login intact", liveOwner: "carol", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
			t.Setenv("CAAM_KEYCHAIN", "0")
			cfg := config.DefaultSPMConfig()
			if tc.backupMode != "" {
				cfg.Safety.AutoBackupBeforeSwitch = tc.backupMode
			}
			if err := cfg.Save(); err != nil {
				t.Fatal(err)
			}

			vault := authfile.NewVault(filepath.Join(root, "vault"))
			oldAlice := apiActivationCredentials("alice-old", 2000000000000)
			live := apiActivationCredentials(tc.liveOwner+"-rotated", 2000003600000)
			bob := apiActivationCredentials("bob", 2000007200000)
			if tc.malformed {
				bob = []byte(`{"claudeAiOauth":{"accessToken":null}}`)
			}
			for name, credentials := range map[string][]byte{"alice": oldAlice, "bob": bob} {
				writeAPIActivationFile(t, vault.BackupPath("claude", name, ".credentials.json"), credentials)
				writeAPIActivationFile(t, vault.BackupPath("claude", name, ".claude.json"), apiActivationIdentity(name))
			}
			// A pre-existing recovery snapshot is immutable, even when its
			// account matches the live login.
			original := []byte(`{"claudeAiOauth":{"accessToken":"original-synthetic"}}`)
			writeAPIActivationFile(t, vault.BackupPath("claude", "_original", ".credentials.json"), original)
			livePath := filepath.Join(home, ".claude", ".credentials.json")
			identityPath := filepath.Join(home, ".claude.json")
			writeAPIActivationFile(t, livePath, live)
			writeAPIActivationFile(t, identityPath, apiActivationIdentity(tc.liveOwner))

			h := NewHandlers(vault, nil, nil)
			result, err := h.Activate(ActivateRequest{Tool: "claude", Profile: "bob"})
			if tc.malformed {
				if err == nil || result != nil {
					t.Fatalf("malformed target accepted: result=%+v err=%v", result, err)
				}
				assertAPIActivationFile(t, livePath, live)
				assertAPIActivationFile(t, identityPath, apiActivationIdentity(tc.liveOwner))
			} else {
				if err != nil || result == nil || !result.Success {
					t.Fatalf("Activate failed: result=%+v err=%v", result, err)
				}
				assertAPIActivationFile(t, livePath, bob)
				if (result.ResnapshottedProfile == "alice") != tc.wantNamed {
					t.Errorf("resnapshotted profile = %q, want named preservation %v", result.ResnapshottedProfile, tc.wantNamed)
				}
				if (result.AutoBackup != "") != tc.wantBackup {
					t.Errorf("auto backup = %q, want backup %v", result.AutoBackup, tc.wantBackup)
				}
				if tc.wantBackup {
					if !authfile.IsSystemProfile(result.AutoBackup) {
						t.Fatalf("recovery copy is not a system profile: %q", result.AutoBackup)
					}
					assertAPIActivationFile(t, vault.BackupPath("claude", result.AutoBackup, ".credentials.json"), live)
				}
			}
			wantAlice := oldAlice
			if tc.wantNamed {
				wantAlice = live
			}
			assertAPIActivationFile(t, vault.BackupPath("claude", "alice", ".credentials.json"), wantAlice)
			assertAPIActivationFile(t, vault.BackupPath("claude", "bob", ".credentials.json"), bob)
			assertAPIActivationFile(t, vault.BackupPath("claude", "_original", ".credentials.json"), original)
			profiles, err := vault.List("claude")
			if err != nil {
				t.Fatal(err)
			}
			wantCount := 3
			if tc.wantBackup {
				wantCount++
			}
			if len(profiles) != wantCount {
				t.Errorf("profiles = %v, want %d after switch", profiles, wantCount)
			}
			if tc.wantNamed {
				if _, err := h.Activate(ActivateRequest{Tool: "claude", Profile: "alice"}); err != nil {
					t.Fatal(err)
				}
				assertAPIActivationFile(t, livePath, live)
			}
		})
	}
}

func TestActivateInvalidSafetyConfigDoesNotChangeLogin(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAAM_HOME", root)
	writeAPIActivationFile(t, config.SPMConfigPath(), []byte("safety:\n  auto_backup_before_switch: invalid\n"))
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	h := NewHandlers(vault, nil, nil)
	result, err := h.Activate(ActivateRequest{Tool: "claude", Profile: "bob"})
	if err == nil || result != nil || !strings.Contains(err.Error(), "safety settings") {
		t.Fatalf("invalid policy accepted: result=%+v err=%v", result, err)
	}
	if _, err := os.Stat(vault.BasePath()); !os.IsNotExist(err) {
		t.Fatalf("invalid policy mutated vault: %v", err)
	}
}

func TestActivateReportsKeptLiveCredentials(t *testing.T) {
	for _, tc := range []struct {
		name       string
		liveToken  string
		liveExpiry int64
	}{
		{name: "identical credentials", liveToken: "alice-saved", liveExpiry: 2000000000000},
		{name: "newer live credentials", liveToken: "alice-rotated", liveExpiry: 2000003600000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
			t.Setenv("CAAM_KEYCHAIN", "0")
			vault := authfile.NewVault(filepath.Join(root, "vault"))
			saved := apiActivationCredentials("alice-saved", 2000000000000)
			live := apiActivationCredentials(tc.liveToken, tc.liveExpiry)
			identity := apiActivationIdentity("alice")
			savedPath := vault.BackupPath("claude", "alice", ".credentials.json")
			livePath := filepath.Join(home, ".claude", ".credentials.json")
			writeAPIActivationFile(t, savedPath, saved)
			writeAPIActivationFile(t, vault.BackupPath("claude", "alice", ".claude.json"), identity)
			writeAPIActivationFile(t, livePath, live)
			writeAPIActivationFile(t, filepath.Join(home, ".claude.json"), identity)
			h := NewHandlers(vault, nil, nil)
			for attempt := 0; attempt < 2; attempt++ {
				result, err := h.Activate(ActivateRequest{Tool: "claude", Profile: "alice"})
				if err != nil || result == nil || !result.Success || !result.KeptLive {
					t.Fatalf("attempt %d: no-op response = %+v, err=%v", attempt, result, err)
				}
				if result.Message != "kept live credentials for claude/alice" {
					t.Errorf("attempt %d: misleading message %q", attempt, result.Message)
				}
				if result.AutoBackup != "" || result.ResnapshottedProfile != "" {
					t.Errorf("attempt %d: no-op reported preservation writes: %+v", attempt, result)
				}
				assertAPIActivationFile(t, livePath, live)
				assertAPIActivationFile(t, savedPath, saved)
			}
		})
	}
}

func apiActivationCredentials(token string, expires int64) []byte {
	return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`, token, "refresh-"+token, expires))
}

func apiActivationIdentity(account string) []byte {
	return []byte(fmt.Sprintf(`{"oauthAccount":{"accountUuid":%q,"emailAddress":%q},"userID":"shared-machine"}`, "account-"+account, account+"@example.com"))
}

func writeAPIActivationFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertAPIActivationFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("unexpected credential content at %s", path)
	}
}

func TestBackupWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := BackupRequest{
		Tool:    "unknown",
		Profile: "test",
	}

	_, err := h.Backup(req)
	if err == nil {
		t.Error("Backup() expected error for unknown tool")
	}
}

func TestBackupWithMissingProfile(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	req := BackupRequest{
		Tool:    "codex",
		Profile: "",
	}

	_, err := h.Backup(req)
	if err == nil || !strings.Contains(err.Error(), "profile is required") {
		t.Errorf("Backup() expected profile required error, got %v", err)
	}
}

func TestDeleteProfileWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	err := h.DeleteProfile("unknown", "test")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("DeleteProfile() expected unknown tool error, got %v", err)
	}
}

func TestDeleteProfileWithMissingProfile(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	err := h.DeleteProfile("codex", "")
	if err == nil || !strings.Contains(err.Error(), "profile is required") {
		t.Errorf("DeleteProfile() expected profile required error, got %v", err)
	}
}

func TestDeleteProfileWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	err := h.DeleteProfile("codex", "test")
	if err == nil {
		t.Error("DeleteProfile() expected error with nil vault")
	}
}

func TestGetProfileWithUnknownTool(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	_, err := h.GetProfile("unknown-tool", "test")
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("GetProfile() expected unknown tool error, got %v", err)
	}
}

func TestGetProfileWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	_, err := h.GetProfile("codex", "test")
	if err == nil {
		t.Error("GetProfile() expected error with nil vault")
	}
}

func TestGetProfileHealthWithNilStore(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return nil without panic
	health := h.getProfileHealth("claude", "test")
	if health != nil {
		t.Errorf("getProfileHealth() with nil store should return nil, got %v", health)
	}
}

func TestGetProfileIdentityWithNilVault(t *testing.T) {
	h := NewHandlers(nil, nil, nil)

	// Should return nil without panic
	id := h.getProfileIdentity("claude", "test")
	if id != nil {
		t.Errorf("getProfileIdentity() with nil vault should return nil, got %v", id)
	}
}

func TestToolsMapContainsExpectedTools(t *testing.T) {
	expectedTools := []string{"codex", "claude", "gemini"}

	for _, tool := range expectedTools {
		if _, ok := tools[tool]; !ok {
			t.Errorf("tools map missing %q", tool)
		}
	}
}
