package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
)

func claudeOAuthCreds(expiresAtMilli int64, subscription string) string {
	return fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"access-tok","refreshToken":"refresh-tok","expiresAt":%d,"subscriptionType":%q,"rateLimitTier":"default_claude_max_5x","scopes":["user:inference"]}}`,
		expiresAtMilli, subscription,
	)
}

// TestStatus_RateLimitCapIsNotTokenExpired drives the REAL `caam status --json`
// path (runStatus), not a helper. The 2026-08-30 incident: a utilization cap
// was reported as "Token expired" from a stale vault snapshot, with a
// machine-wide re-login recommendation, while the live credential was valid
// and error_count was 0.
func TestStatus_RateLimitCapIsNotTokenExpired(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg-config"))

	oldVault := vault
	oldDB := globalDB
	globalDB = nil
	t.Cleanup(func() {
		vault = oldVault
		if globalDB != nil {
			globalDB.Close()
		}
		globalDB = oldDB
	})
	vault = authfile.NewVault(authfile.DefaultVaultPath())

	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	credPath := filepath.Join(claudeDir, ".credentials.json")

	// Vault snapshot: expired five days ago, plan "pro" — the frozen fields
	// caam status was serving.
	staleExpiry := time.Now().Add(-5 * 24 * time.Hour).UnixMilli()
	if err := os.WriteFile(credPath, []byte(claudeOAuthCreds(staleExpiry, "pro")), 0600); err != nil {
		t.Fatalf("write stale creds: %v", err)
	}
	fileSet := tools["claude"]()
	if err := vault.Backup(fileSet, "pm-account"); err != nil {
		t.Fatalf("backup: %v", err)
	}

	// Live credential refreshes in place: same tokens (profile still matches),
	// expiry two hours in the future, subscription max.
	liveExpiry := time.Now().Add(2 * time.Hour).UnixMilli()
	if err := os.WriteFile(credPath, []byte(claudeOAuthCreds(liveExpiry, "max")), 0600); err != nil {
		t.Fatalf("write live creds: %v", err)
	}
	active, err := vault.ActiveProfile(fileSet)
	if err != nil || active != "pm-account" {
		t.Fatalf("ActiveProfile = %q, %v; want pm-account (rotation-stable hash)", active, err)
	}

	db, err := caamdb.Open()
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer db.Close()
	if _, err := db.SetCooldown("claude", "pm-account", time.Now(), 16*time.Minute, "utilization cap"); err != nil {
		t.Fatalf("SetCooldown: %v", err)
	}

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	statusCmd.SetOut(w)
	if err := statusCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json flag: %v", err)
	}
	t.Cleanup(func() { _ = statusCmd.Flags().Set("json", "false") })

	runErr := runStatus(statusCmd, []string{"claude"})
	w.Close()
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("runStatus error: %v", runErr)
	}
	rawOut, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read status output: %v", err)
	}

	var out statusOutput
	if err := json.Unmarshal(rawOut, &out); err != nil {
		t.Fatalf("unmarshal status json %q: %v", string(rawOut), err)
	}
	if len(out.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d (%s)", len(out.Tools), string(rawOut))
	}
	st := out.Tools[0]
	if st.Health == nil {
		t.Fatalf("missing health in %s", string(rawOut))
	}
	if st.Health.Status == "critical" {
		t.Errorf("status=%q; a rate-limit cap must not be critical/token-expired; output=%s", st.Health.Status, string(rawOut))
	}
	reason := strings.ToLower(st.Health.Reason)
	if !strings.Contains(reason, "rate limited") {
		t.Errorf("health.reason=%q, want rate limited; output=%s", st.Health.Reason, string(rawOut))
	}
	if strings.Contains(reason, "token expired") {
		t.Errorf("health.reason=%q still says token expired; output=%s", st.Health.Reason, string(rawOut))
	}
	joined := strings.Join(out.Recommendations, "\n")
	if strings.Contains(strings.ToLower(joined), "caam login") {
		t.Errorf("recommendations %q must not recommend machine-wide re-login for a cap", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "rate limited") {
		t.Errorf("recommendations %q, want rate limited; output=%s", joined, string(rawOut))
	}
}

func TestStatus_LiveTokenOverridesStaleVaultExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("HOME", tmpDir)
	t.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam_home"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpDir, "xdg-data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmpDir, "xdg-config"))

	oldVault := vault
	oldDB := globalDB
	globalDB = nil
	t.Cleanup(func() {
		vault = oldVault
		if globalDB != nil {
			globalDB.Close()
		}
		globalDB = oldDB
	})
	vault = authfile.NewVault(authfile.DefaultVaultPath())

	claudeDir := filepath.Join(tmpDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	credPath := filepath.Join(claudeDir, ".credentials.json")
	staleExpiry := time.Now().Add(-5 * 24 * time.Hour).UnixMilli()
	if err := os.WriteFile(credPath, []byte(claudeOAuthCreds(staleExpiry, "pro")), 0600); err != nil {
		t.Fatalf("write stale creds: %v", err)
	}
	fileSet := tools["claude"]()
	if err := vault.Backup(fileSet, "main"); err != nil {
		t.Fatalf("backup: %v", err)
	}
	liveExpiry := time.Now().Add(2 * time.Hour).UnixMilli()
	if err := os.WriteFile(credPath, []byte(claudeOAuthCreds(liveExpiry, "max")), 0600); err != nil {
		t.Fatalf("write live creds: %v", err)
	}

	r, w, _ := os.Pipe()
	oldStdout := os.Stdout
	os.Stdout = w
	statusCmd.SetOut(w)
	if err := statusCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json flag: %v", err)
	}
	t.Cleanup(func() { _ = statusCmd.Flags().Set("json", "false") })

	runErr := runStatus(statusCmd, []string{"claude"})
	w.Close()
	os.Stdout = oldStdout
	if runErr != nil {
		t.Fatalf("runStatus error: %v", runErr)
	}
	rawOut, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var out statusOutput
	if err := json.Unmarshal(rawOut, &out); err != nil {
		t.Fatalf("unmarshal %q: %v", string(rawOut), err)
	}
	if len(out.Tools) != 1 || out.Tools[0].Health == nil {
		t.Fatalf("unexpected output %s", string(rawOut))
	}
	st := out.Tools[0]
	if st.Health.Status == "critical" {
		t.Errorf("live-valid token reported critical: %s", string(rawOut))
	}
	if strings.Contains(strings.ToLower(st.Health.Reason), "token expired") {
		t.Errorf("live-valid token reported token expired: %s", string(rawOut))
	}
	joined := strings.Join(out.Recommendations, "\n")
	if strings.Contains(strings.ToLower(joined), "caam login") {
		t.Errorf("live-valid token recommended login: %q", joined)
	}
}
