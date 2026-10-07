package api

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
)

func TestCursorAPIHealthReadsCurrentCustomVault(t *testing.T) {
	root := t.TempDir()
	vault := authfile.NewVault(filepath.Join(root, "custom-vault"))
	healthPath := filepath.Join(root, "metadata", "health.json")
	if err := os.MkdirAll(filepath.Dir(healthPath), 0700); err != nil {
		t.Fatal(err)
	}
	store := health.NewStorage(healthPath)
	if err := store.UpdateProfile("cursor", "work", &health.ProfileHealth{TokenExpiresAt: time.Now().Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(healthPath)
	if err != nil {
		t.Fatal(err)
	}
	// A same-named snapshot beside health.json must not supply the answer.
	writeAPIActivationFile(t, filepath.Join(root, "metadata", "vault", "cursor", "work", "auth.json"), []byte(`{"apiKey":"synthetic-decoy"}`))
	h := NewHandlers(vault, store, nil)
	for _, tc := range []struct {
		name          string
		ttl           time.Duration
		apiKey        string
		body          string
		wantStatus    string
		wantRenewable bool
		wantLogin     *bool
		wantAdvice    bool
	}{
		{name: "long lead session", ttl: 5 * 24 * time.Hour, wantStatus: "warning", wantLogin: apiBool(false), wantAdvice: true},
		{name: "expired session", ttl: -time.Hour, wantStatus: "critical", wantLogin: apiBool(true), wantAdvice: true},
		{name: "renewable expired access", ttl: -time.Hour, apiKey: "synthetic-api-key", wantStatus: "healthy", wantRenewable: true, wantLogin: apiBool(false)},
		{name: "new distant session", ttl: 30 * 24 * time.Hour, wantStatus: "healthy", wantLogin: apiBool(false)},
		{name: "opaque replacement", body: `{"accessToken":"opaque-synthetic"}`, wantStatus: "warning"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := tc.body
			if body == "" {
				payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(tc.ttl).Unix())))
				body = fmt.Sprintf(`{"accessToken":%q,"apiKey":%q}`, "e30."+payload+".synthetic", tc.apiKey)
			}
			writeAPIActivationFile(t, vault.BackupPath("cursor", "work", "auth.json"), []byte(body))
			result, err := h.GetProfile("cursor", "work")
			if err != nil || result == nil || result.Health == nil {
				t.Fatalf("GetProfile: result=%+v err=%v", result, err)
			}
			got := result.Health
			if got.Status != tc.wantStatus || got.Renewable != tc.wantRenewable {
				t.Errorf("health = %+v, want status=%s renewable=%v", got, tc.wantStatus, tc.wantRenewable)
			}
			if tc.wantLogin == nil {
				if got.LoginRequired != nil || got.LaunchUsable != nil || got.ExpiresAt != "" {
					t.Errorf("unknown credential retained previous facts: %+v", got)
				}
			} else if got.LoginRequired == nil || *got.LoginRequired != *tc.wantLogin ||
				got.LaunchUsable == nil || *got.LaunchUsable == *tc.wantLogin || got.RefreshDue == nil || *got.RefreshDue {
				t.Errorf("incorrect launch/login/refresh signals: %+v", got)
			}
			if (got.Recommendation != "") != tc.wantAdvice || strings.Contains(got.Recommendation, "caam refresh") {
				t.Errorf("incorrect recommendation: %q", got.Recommendation)
			}
			encoded, err := json.Marshal(result)
			if err != nil || !bytes.Contains(encoded, []byte(`"launch_usable":`)) {
				t.Fatalf("health signals missing from API JSON: %s, err=%v", encoded, err)
			}
		})
	}
	after, err := os.ReadFile(healthPath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("read-only health changed stored metadata: err=%v", err)
	}
}

func apiBool(value bool) *bool { return &value }

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
	usage, err := h.GetUsage(context.Background(), "", "")
	if err != nil {
		t.Fatalf("GetUsage() error = %v", err)
	}
	if usage == nil {
		t.Fatal("GetUsage() returned nil")
	}
	if len(usage.Usage) != 0 {
		t.Errorf("GetUsage() with nil deps should return empty, got %d entries", len(usage.Usage))
	}
	if usage.Available {
		t.Fatal("missing database reported available activity")
	}
}

func TestGetUsageReadsActivityWithoutHealthOrVault(t *testing.T) {
	d, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	now := time.Now().UTC().Truncate(time.Second)
	for _, e := range []caamdb.Event{
		{Timestamp: now.Add(-2 * time.Hour), Type: caamdb.EventActivate, Provider: "grok", ProfileName: "work"},
		{Timestamp: now.Add(-30 * time.Minute), Type: caamdb.EventSwitch, Provider: "grok", ProfileName: "work"},
		{Timestamp: now.Add(-20 * time.Minute), Type: caamdb.EventError, Provider: "grok", ProfileName: "work", Details: map[string]any{"access_token": "synthetic-do-not-export"}},
		{Timestamp: now.Add(-10 * time.Minute), Type: caamdb.EventDeactivate, Provider: "grok", ProfileName: "work", Duration: 5 * time.Minute},
		{Timestamp: now.Add(-2 * time.Minute), Type: caamdb.EventActivate, Provider: "opencode", ProfileName: "personal"},
		{Timestamp: now.Add(-time.Minute), Type: caamdb.EventActivate, Provider: "grok", ProfileName: "_original"},
		{Timestamp: now.Add(-time.Minute), Type: caamdb.EventActivate, Provider: "unknown", ProfileName: "unsupported"},
	} {
		if err := d.LogEvent(e); err != nil {
			t.Fatal(err)
		}
	}
	h := NewHandlers(nil, nil, d)
	got, err := h.GetUsage(context.Background(), "", "24h")
	if err != nil || got == nil || !got.Available || len(got.Usage) != 2 {
		t.Fatalf("usage = %+v, err = %v", got, err)
	}
	grok := got.Usage[0]
	if grok.Tool != "grok" || grok.Profile != "work" || grok.Activations != 2 || grok.ErrorCount != 1 || grok.ActiveSeconds != 300 || grok.LastActivity != now.Add(-10*time.Minute).Format(time.RFC3339) {
		t.Errorf("grok activity = %+v", grok)
	}
	if got.Usage[1].Tool != "opencode" || got.Usage[1].Activations != 1 {
		t.Errorf("OpenCode omitted from activity: %+v", got.Usage)
	}
	since, err := time.Parse(time.RFC3339, got.Since)
	if err != nil {
		t.Fatal(err)
	}
	until, err := time.Parse(time.RFC3339, got.Until)
	if err != nil || until.Sub(since) != 24*time.Hour {
		t.Fatalf("invalid interval: %+v, err = %v", got, err)
	}
	short, err := h.GetUsage(context.Background(), "grok", "1h")
	if err != nil || len(short.Usage) != 1 || short.Usage[0].Activations != 1 {
		t.Fatalf("time/provider filters = %+v, err = %v", short, err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"synthetic-do-not-export", "access_token", "total_calls", "last_used"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Errorf("usage exposed %q: %s", forbidden, encoded)
		}
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := h.GetUsage(context.Background(), "", "24h"); err == nil {
		t.Fatal("database failure was reported as empty data")
	}
}

func TestGetUsageAvailableEmptyPeriod(t *testing.T) {
	d, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "activity.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	h := NewHandlers(nil, nil, d)
	for _, period := range []string{"", "1h", "24h", "7d", "30d"} {
		got, err := h.GetUsage(context.Background(), "", period)
		if err != nil || got == nil || !got.Available || got.Usage == nil || len(got.Usage) != 0 {
			t.Fatalf("period %q: %+v, err = %v", period, got, err)
		}
	}
}

func TestAPIIncludesEveryVaultProvider(t *testing.T) {
	vault := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	writeAPIActivationFile(t, vault.BackupPath("agy", "work", "antigravity-oauth-token"), []byte("synthetic-private-value"))
	for _, tool := range []string{"grok", "opencode"} {
		writeAPIActivationFile(t, vault.BackupPath(tool, "work", "auth.json"), []byte(`{"access_token":"synthetic-private-value"}`))
	}
	h := NewHandlers(vault, nil, nil)
	status, err := h.GetStatus()
	if err != nil || status.Version != version.Short() {
		t.Fatalf("status = %+v, err = %v", status, err)
	}
	var names []string
	for _, tool := range status.Tools {
		names = append(names, tool.Tool)
	}
	if strings.Join(names, ",") != "agy,claude,codex,cursor,gemini,grok,opencode" {
		t.Fatalf("provider status coverage/order = %v", names)
	}
	profiles, err := h.GetProfiles("")
	if err != nil || profiles.Count != 3 || profiles.Profiles[0].Tool != "agy" || profiles.Profiles[1].Tool != "grok" || profiles.Profiles[2].Tool != "opencode" {
		t.Fatalf("profiles = %+v, err = %v", profiles, err)
	}
	for _, tool := range []string{"agy", "grok", "opencode"} {
		if profile, err := h.GetProfile(tool, "work"); err != nil || profile.Tool != tool {
			t.Fatalf("provider %s: %+v, err = %v", tool, profile, err)
		}
	}
	encoded, err := json.Marshal(profiles)
	if err != nil || strings.Contains(string(encoded), "synthetic-private-value") {
		t.Fatalf("profile serialization leaked credentials: %s, err = %v", encoded, err)
	}
}

func TestAPIBackupRequiresExplicitOverwrite(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	saved := []byte(`{"tokens":{"access_token":"synthetic-old","refresh_token":"old-refresh"}}`)
	live := []byte(`{"tokens":{"access_token":"synthetic-new","refresh_token":"new-refresh"}}`)
	writeAPIActivationFile(t, filepath.Join(root, "codex", "auth.json"), live)
	writeAPIActivationFile(t, vault.BackupPath("codex", "work", "auth.json"), saved)
	h := NewHandlers(vault, nil, nil)
	result, err := h.Backup(BackupRequest{Tool: "codex", Profile: "work"})
	if err != nil || result.Success || !strings.Contains(result.Message, "already exists") {
		t.Fatalf("unconfirmed overwrite = %+v, err = %v", result, err)
	}
	got, err := os.ReadFile(vault.BackupPath("codex", "work", "auth.json"))
	if err != nil || !bytes.Equal(got, saved) {
		t.Fatalf("unconfirmed overwrite changed saved login: %s, err = %v", got, err)
	}
	result, err = h.Backup(BackupRequest{Tool: "codex", Profile: "work", Overwrite: true})
	if err != nil || !result.Success {
		t.Fatalf("confirmed backup = %+v, err = %v", result, err)
	}
	got, err = os.ReadFile(vault.BackupPath("codex", "work", "auth.json"))
	if err != nil || !bytes.Equal(got, live) {
		t.Fatalf("confirmed backup did not save current login: %s, err = %v", got, err)
	}
	if _, err := h.Backup(BackupRequest{Tool: "codex", Profile: "_original", Overwrite: true}); err == nil {
		t.Fatal("API allowed a system profile to be replaced")
	}

	var wg sync.WaitGroup
	results := make(chan bool, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Separate handlers/vault instances still share managed switch exclusion.
			other := NewHandlers(authfile.NewVault(vault.BasePath()), nil, nil)
			r, err := other.Backup(BackupRequest{Tool: "codex", Profile: "new"})
			if err != nil {
				t.Errorf("concurrent backup: %v", err)
				results <- false
				return
			}
			results <- r.Success
		}()
	}
	wg.Wait()
	close(results)
	succeeded := 0
	for success := range results {
		if success {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("concurrent unconfirmed backups succeeded %d times, want exactly one", succeeded)
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

func TestGetCoordinatorsFromAgentConfig(t *testing.T) {
	var agentHits int
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		agentHits++
		if r.URL.Path != "/coordinators" {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode([]map[string]any{
			{"name": "csd", "is_healthy": true, "last_check": time.Now().UTC().Format(time.RFC3339)},
			{"name": "css", "is_healthy": false, "last_check": time.Now().UTC().Format(time.RFC3339), "last_error": "ssh tunnel 1.2.3.4: connection refused"},
			{"name": "new", "is_healthy": false},
		})
	}))
	defer agentSrv.Close()
	_, portStr, _ := net.SplitHostPort(agentSrv.Listener.Addr().String())
	port, _ := strconv.Atoi(portStr)

	path := filepath.Join(t.TempDir(), "distributed-agent.json")
	fc := agent.FileConfig{
		Port: port,
		Coordinators: []*agent.CoordinatorEndpoint{
			{Name: "csd", DisplayName: "CSD", URL: "http://127.0.0.1:7890", Token: "secret-1", SSH: &agent.SSHTunnel{Host: "100.64.0.5"}},
			{Name: "css", URL: "http://127.0.0.1:7890", Token: "secret-2", SSH: &agent.SSHTunnel{Host: "1.2.3.4"}},
			{Name: "new", URL: "http://100.64.0.9:7890", Token: "secret-3"},
			{Name: "gone", URL: "http://127.0.0.1:7890"},
		},
	}
	if err := agent.WriteFileConfig(path, fc); err != nil {
		t.Fatal(err)
	}

	h := NewHandlers(nil, nil, nil)
	h.agentConfigPath = path
	resp, err := h.GetCoordinators()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]CoordinatorStatus{}
	for _, c := range resp.Coordinators {
		got[c.ID] = c
	}
	if got["csd"].Status != "healthy" || got["csd"].Transport != "ssh" || got["csd"].Endpoint != "http://127.0.0.1:7890 via ssh 100.64.0.5" || got["csd"].LastSeen == "" {
		t.Errorf("csd = %+v", got["csd"])
	}
	if got["css"].Status != "unreachable" || !strings.Contains(got["css"].Error, "connection refused") {
		t.Errorf("css = %+v", got["css"])
	}
	if got["new"].Status != "pending" || got["new"].Transport != "direct" {
		t.Errorf("new = %+v", got["new"])
	}
	if got["gone"].Status != "unknown" {
		t.Errorf("gone = %+v", got["gone"])
	}
	data, _ := json.Marshal(resp)
	if strings.Contains(string(data), "secret-") {
		t.Fatalf("coordinator tokens leaked: %s", data)
	}

	// Agent down: entries remain, marked as such.
	agentSrv.Close()
	resp, err = h.GetCoordinators()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Coordinators {
		if c.Status != "agent_not_running" {
			t.Errorf("%s status = %q with the agent down", c.ID, c.Status)
		}
	}
}

func TestGetCoordinatorsWithoutAgentConfig(t *testing.T) {
	h := NewHandlers(nil, nil, nil)
	h.agentConfigPath = filepath.Join(t.TempDir(), "missing.json")
	resp, err := h.GetCoordinators()
	if err != nil || resp == nil || resp.Coordinators == nil || len(resp.Coordinators) != 0 {
		t.Fatalf("resp = %+v, err = %v", resp, err)
	}

	single := filepath.Join(t.TempDir(), "agent.json")
	if err := agent.WriteFileConfig(single, agent.FileConfig{Port: 1, CoordinatorURL: "http://localhost:7890"}); err != nil {
		t.Fatal(err)
	}
	h.agentConfigPath = single
	resp, err = h.GetCoordinators()
	if err != nil || len(resp.Coordinators) != 1 || resp.Coordinators[0].Status != "agent_not_running" {
		t.Fatalf("single-coordinator resp = %+v, err = %v", resp, err)
	}

	bad := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(bad, []byte("{"), 0o600)
	h.agentConfigPath = bad
	if _, err := h.GetCoordinators(); err == nil {
		t.Fatal("a corrupt agent config must be reported")
	}
}

func TestCoordinatorResponsesDoNotExposeURLOrTransportSecrets(t *testing.T) {
	endpoint := "http://synthetic-user:synthetic-password@localhost:7890/private-path?token=synthetic-query#synthetic-fragment"
	agentSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]map[string]any{{
			"name": "remote", "last_check": time.Now().UTC().Format(time.RFC3339),
			"last_error": "Get " + endpoint + ": connection refused; synthetic-bearer",
		}})
	}))
	defer agentSrv.Close()
	_, portText, _ := net.SplitHostPort(agentSrv.Listener.Addr().String())
	port, _ := strconv.Atoi(portText)
	path := filepath.Join(t.TempDir(), "agent.json")
	h := NewHandlers(nil, nil, nil)
	h.agentConfigPath = path
	for _, fc := range []agent.FileConfig{
		{Port: port, CoordinatorURL: endpoint},
		{Port: port, Coordinators: []*agent.CoordinatorEndpoint{{Name: "remote", URL: endpoint, Token: "synthetic-bearer"}}},
	} {
		if err := agent.WriteFileConfig(path, fc); err != nil {
			t.Fatal(err)
		}
		resp, err := h.GetCoordinators()
		if err != nil || len(resp.Coordinators) != 1 || resp.Coordinators[0].Endpoint != "http://localhost:7890" {
			t.Fatalf("coordinators = %+v, err = %v", resp, err)
		}
		body, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		for _, secret := range []string{"synthetic-", "private-path"} {
			if strings.Contains(string(body), secret) {
				t.Fatal("coordinator response exposed configured or transport credentials")
			}
		}
	}
	if got := coordinatorDisplayEndpoint("http://user:secret@host:invalid"); got != "invalid coordinator URL" {
		t.Fatalf("malformed endpoint was exposed: %q", got)
	}
	if got := coordinatorErrorSummary("upstream body contains synthetic-bearer"); strings.Contains(got, "synthetic-") {
		t.Fatal("unknown transport error was passed through")
	}
}

func TestProfileReadsLeaveGeminiLegacySnapshotsUntouched(t *testing.T) {
	for _, withCurrent := range []bool{false, true} {
		t.Run(fmt.Sprintf("current=%v", withCurrent), func(t *testing.T) {
			vault := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			legacy := []byte(`{"email":"legacy@example.com","access_token":"synthetic-legacy"}`)
			current := []byte(`{"email":"current@example.com","access_token":"synthetic-current"}`)
			legacyPath := vault.BackupPath("gemini", "work", "oauth_credentials.json")
			currentPath := vault.BackupPath("gemini", "work", "oauth_creds.json")
			writeAPIActivationFile(t, legacyPath, legacy)
			if withCurrent {
				writeAPIActivationFile(t, currentPath, current)
			}
			h := NewHandlers(vault, nil, nil)
			profile, err := h.GetProfile("gemini", "work")
			wantEmail := "legacy@example.com"
			if withCurrent {
				wantEmail = "current@example.com"
			}
			if err != nil || profile.Identity == nil || profile.Identity.Email != wantEmail {
				t.Fatalf("profile = %+v, err = %v", profile, err)
			}
			if _, err := h.GetProfiles(""); err != nil {
				t.Fatal(err)
			}
			assertAPIActivationFile(t, legacyPath, legacy)
			if withCurrent {
				assertAPIActivationFile(t, currentPath, current)
			} else if _, err := os.Lstat(currentPath); !os.IsNotExist(err) {
				t.Fatalf("GET migrated legacy snapshot: %v", err)
			}
		})
	}
}

func TestAPIReadsClaudeKeychainWithoutWritingMirror(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	items := testutil.FakeKeychain(t)
	credential := []byte(`{"claudeAiOauth":{"accessToken":"synthetic-keychain","refreshToken":"synthetic-refresh","expiresAt":1893456000000}}`)
	testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), string(credential))
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	writeAPIActivationFile(t, vault.BackupPath("claude", "work", ".credentials.json"), credential)
	h := NewHandlers(vault, nil, nil)
	status, err := h.GetStatus()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range status.Tools {
		if tool.Tool == "claude" {
			found = tool.LoggedIn && tool.ActiveProfile == "work"
		}
	}
	if !found {
		t.Fatalf("status did not recognize the authoritative keychain: %+v", status.Tools)
	}
	for _, tool := range []string{"", "claude"} {
		if _, err := h.GetProfiles(tool); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := h.GetProfile("claude", "work")
	if err != nil || !profile.Active {
		t.Fatalf("profile = %+v, err = %v", profile, err)
	}
	if _, err := os.Lstat(filepath.Join(root, "home", ".claude", ".credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("GET created a live keychain mirror: %v", err)
	}
	if got, ok := testutil.FakeKeychainRead(t, items, keychain.ClaudeService); !ok || got != string(credential) {
		t.Fatal("GET changed the authoritative keychain")
	}
	assertAPIActivationFile(t, vault.BackupPath("claude", "work", ".credentials.json"), credential)
}

func TestActivityMetadataDoesNotTrustDiagnosticValues(t *testing.T) {
	profiles := map[[2]string]bool{
		{"claude", "work"}:                   true,
		{"codex", "only-other-provider"}:     true,
		{"claude", strings.Repeat("x", 257)}: true,
	}
	for _, tc := range []struct {
		name  string
		key   string
		value any
		want  string
	}{
		{"known profile reference", "from", "work", "work"},
		{"profile syntax is not enough", "from", "synthetic-sensitive-value", ""},
		{"other provider is not enough", "previous_profile", "only-other-provider", ""},
		{"oversized profile is bounded", "switched_to", strings.Repeat("x", 257), ""},
		{"nested profile is not a reference", "from", map[string]any{"profile": "work"}, ""},
		{"known reason", "reason", "refresh_token_expired", "refresh_token_expired"},
		{"reason prefix is not enough", "reason", "refresh_token_expired_sensitive", ""},
		{"provider reason can be a credential", "reason", "synthetic-sensitive-value", ""},
		{"nested reason", "reason", []any{"rate_limit"}, ""},
		{"null operation", "operation", nil, ""},
		{"known algorithm", "algorithm", "smart", "smart"},
		{"arbitrary message", "message", "rate_limit", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDetails(map[string]any{tc.key: tc.value}, "claude", profiles)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("diagnostic metadata reached response: %v", got)
				}
			} else if len(got) != 1 || got[tc.key] != tc.want {
				t.Fatalf("safe metadata = %v, want %s=%q", got, tc.key, tc.want)
			}
		})
	}
}

func TestActivityDistinguishesEmptyAvailableDatabase(t *testing.T) {
	db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "caam.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	got, err := NewHandlers(nil, nil, db).GetActivity(0)
	if err != nil || got == nil || !got.Available || got.Events == nil || len(got.Events) != 0 || got.Cooldowns == nil || len(got.Cooldowns) != 0 {
		t.Fatalf("GetActivity = %+v, %v; want available empty lists", got, err)
	}
}
