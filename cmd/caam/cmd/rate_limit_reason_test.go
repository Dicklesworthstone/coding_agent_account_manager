package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// TestGetHealthReasonRateLimitCap covers PR #82: an active rate-limit cooldown
// must be reported as "rate limited", never as "token expired" or "token
// expiring soon", even when the recorded expiry (possibly from a stale vault
// snapshot) is in the past.
func TestGetHealthReasonRateLimitCap(t *testing.T) {
	now := time.Now()

	capped := &health.ProfileHealth{
		TokenExpiresAt:   now.Add(-5 * 24 * time.Hour),
		ErrorCount1h:     0,
		RateLimitedUntil: now.Add(16 * time.Minute),
	}
	got := getHealthReason(capped, health.StatusWarning)
	if !strings.Contains(got, "rate limited") {
		t.Errorf("getHealthReason() = %q, want it to contain %q", got, "rate limited")
	}
	if strings.Contains(got, "expired") || strings.Contains(got, "expiring") {
		t.Errorf("getHealthReason() = %q, must not blame the token for an active cap", got)
	}

	// A genuinely expired token with no cooldown still classifies as expired.
	expired := &health.ProfileHealth{
		TokenExpiresAt: now.Add(-1 * time.Hour),
	}
	if got := getHealthReason(expired, health.StatusCritical); got != "token expired" {
		t.Errorf("getHealthReason() = %q, want %q", got, "token expired")
	}
}

// TestApplyLiveExpiryOverridesStaleSnapshot verifies that the live credential
// expiry replaces a stale vault-snapshot expiry for the active profile, so a
// valid live token is not reported as expired (PR #82).
func TestApplyLiveExpiryOverridesStaleSnapshot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	liveExpiry := time.Now().Add(3 * time.Hour).UnixMilli()
	creds := fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"tok","refreshToken":"ref","expiresAt":%d,"subscriptionType":"max","scopes":["user:inference"]}}`,
		liveExpiry,
	)
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	ph := &health.ProfileHealth{
		// Stale vault snapshot: expired five days ago.
		TokenExpiresAt: time.Now().Add(-5 * 24 * time.Hour),
	}
	applyLiveExpiry("claude", ph)

	if !ph.TokenExpiresAt.Equal(time.UnixMilli(liveExpiry)) {
		t.Errorf("TokenExpiresAt = %v, want live expiry %v", ph.TokenExpiresAt, time.UnixMilli(liveExpiry))
	}
	if status := health.CalculateStatus(ph); status == health.StatusCritical {
		t.Errorf("status = %v; a valid live token must not be critical", status)
	}
	if reasons := strings.Join(health.StatusReasons(ph), ", "); strings.Contains(reasons, "Token expired") {
		t.Errorf("StatusReasons() = %q, must not report Token expired for a valid live token", reasons)
	}
}
