package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
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

// TestApplyLiveExpiryMarksClaudeSelfRefreshing covers PR #84: a Claude
// credential carrying a refresh token is renewed in place by Claude Code, so
// its access-token TTL must not lower the verdict or recommend a refresh.
func TestApplyLiveExpiryMarksClaudeSelfRefreshing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, ".config"))

	claudeDir := filepath.Join(tmp, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Last minutes of an ~8h access token, refreshable by Claude Code.
	creds := fmt.Sprintf(
		`{"claudeAiOauth":{"accessToken":"tok","refreshToken":"ref","expiresAt":%d,"subscriptionType":"max"}}`,
		time.Now().Add(10*time.Minute).UnixMilli(),
	)
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}

	ph := &health.ProfileHealth{}
	applyLiveExpiry("claude", ph)

	if !ph.SelfRefreshing {
		t.Fatal("SelfRefreshing = false, want true for a refreshable Claude credential")
	}
	if status := health.CalculateStatus(ph); status != health.StatusHealthy {
		t.Errorf("status = %v, want healthy", status)
	}
	if rec := health.FormatRecommendation("claude", "main", ph); rec != "" {
		t.Errorf("FormatRecommendation() = %q, want empty (caam cannot refresh Claude)", rec)
	}

	// The same credential without a refresh token is treated as before.
	creds = fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"tok","expiresAt":%d}}`, time.Now().Add(10*time.Minute).UnixMilli())
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(creds), 0o600); err != nil {
		t.Fatalf("write creds: %v", err)
	}
	ph = &health.ProfileHealth{}
	applyLiveExpiry("claude", ph)
	if ph.SelfRefreshing {
		t.Error("SelfRefreshing = true without a refresh token, want false")
	}
	if status := health.CalculateStatus(ph); status == health.StatusHealthy {
		t.Errorf("status = %v, want a downgraded verdict for a non-refreshable token expiring in 10m", status)
	}
}

func TestCursorHealthUsesIsolatedPathsAndClearsSessionExpiry(t *testing.T) {
	livePaths := setupCursorHealthVault(t)
	profileStore = profile.NewStore(filepath.Join(t.TempDir(), "profiles"))
	prof, err := profileStore.Create("cursor", "seat", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	expiry := now.Add(8 * 24 * time.Hour)
	paths := authfile.ResolveCursorPaths(prof.HomePath(), runtime.GOOS, func(string) string { return "" })
	writeNativeTestCredential(t, paths.AuthFile, string(cursorHealthCredential(t, expiry, false)))
	// Neither ambient paths nor the profile's separate XDG directory are
	// where Cursor's provider launches the isolated CLI.
	writeNativeTestCredential(t, livePaths.AuthFile, string(cursorHealthCredential(t, now.Add(40*24*time.Hour), false)))
	writeNativeTestCredential(t, filepath.Join(prof.XDGConfigPath(), "cursor", "auth.json"), string(cursorHealthCredential(t, now.Add(-time.Hour), false)))
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "seat"), "auth.json"), string(cursorHealthCredential(t, now.Add(-24*time.Hour), false)))
	if err := healthStore.SetTokenExpiry("cursor", "seat", now.Add(-40*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	ph := buildProfileHealth("cursor", "seat")
	if !ph.TokenExpiresAt.Equal(expiry) || ph.ReloginWarningLead != health.CursorReloginLead || ph.CredentialRenewable() {
		t.Fatalf("isolated Cursor health used another credential source: %+v", ph)
	}
	writeNativeTestCredential(t, paths.AuthFile, string(cursorHealthCredential(t, time.Time{}, true)))
	ph = buildProfileHealth("cursor", "seat")
	if !ph.TokenExpiresAt.IsZero() || !ph.SelfRefreshing || !ph.TokenRenewable || ph.ReloginWarningLead != 0 {
		t.Fatalf("API-key-only isolated login inherited stale session expiry: %+v", ph)
	}
	assertCursorSignals(t, health.CredentialSignals(ph, health.DefaultHealthConfig()), false, true)

	// An existing opaque or damaged isolated login is newer evidence than
	// the parseable, expired vault snapshot. Only a missing isolated auth
	// file permits the vault fallback.
	for _, data := range []string{`{"accessToken":"SYNTHETIC-OPAQUE"}`, `{"accessToken":`} {
		writeNativeTestCredential(t, paths.AuthFile, data)
		ph = buildProfileHealth("cursor", "seat")
		if !ph.TokenExpiresAt.IsZero() || ph.CredentialRenewable() || ph.ReloginWarningLead != 0 {
			t.Fatalf("unparseable isolated login inherited the vault deadline: %+v", ph)
		}
		signals := health.CredentialSignals(ph, health.DefaultHealthConfig())
		if signals.LoginRequired != nil || signals.LaunchUsable != nil || signals.RefreshDue != nil {
			t.Errorf("unparseable isolated expiry must remain unknown: %+v", signals)
		}
	}
	if _, err := profileStore.Create("cursor", "missing-auth", "oauth"); err != nil {
		t.Fatal(err)
	}
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "missing-auth"), "auth.json"), string(cursorHealthCredential(t, expiry, false)))
	if ph := buildProfileHealth("cursor", "missing-auth"); !ph.TokenExpiresAt.Equal(expiry) {
		t.Fatalf("missing isolated credential prevented valid vault fallback: %+v", ph)
	}

	// The live helper must make the same decision when the active session is
	// replaced with an API key with no access token yet.
	ph = &health.ProfileHealth{TokenExpiresAt: now.Add(-time.Hour), ReloginWarningLead: health.CursorReloginLead}
	writeNativeTestCredential(t, livePaths.AuthFile, string(cursorHealthCredential(t, time.Time{}, true)))
	applyLiveExpiry("cursor", ph)
	if !ph.TokenExpiresAt.IsZero() || !ph.SelfRefreshing || !ph.TokenRenewable || ph.ReloginWarningLead != 0 {
		t.Fatalf("active API key inherited stale session state: %+v", ph)
	}
	assertCursorSignals(t, health.CredentialSignals(ph, health.DefaultHealthConfig()), false, true)
}

func TestCursorHealthDoesNotInventAnExpiryForOpaqueCredentials(t *testing.T) {
	paths := setupCursorHealthVault(t)
	past := time.Now().Add(-time.Hour)
	if err := healthStore.SetTokenExpiry("cursor", "seat", past); err != nil {
		t.Fatal(err)
	}
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "seat"), "auth.json"), `{"accessToken":"SYNTHETIC-OPAQUE-SESSION"}`)
	ph := buildProfileHealth("cursor", "seat")
	if !ph.TokenExpiresAt.IsZero() || ph.CredentialRenewable() || ph.ReloginWarningLead != 0 {
		t.Fatalf("opaque credential inherited historical health: %+v", ph)
	}
	signals := health.CredentialSignals(ph, health.DefaultHealthConfig())
	if signals.LoginRequired != nil || signals.LaunchUsable != nil || signals.RefreshDue != nil {
		t.Errorf("opaque expiry must remain unknown: %+v", signals)
	}
	writeNativeTestCredential(t, paths.AuthFile, `{"accessToken":"SYNTHETIC-OPAQUE-SESSION"}`)
	ph.TokenExpiresAt = past
	ph.ReloginWarningLead = health.CursorReloginLead
	applyLiveExpiry("cursor", ph)
	if !ph.TokenExpiresAt.IsZero() || ph.ReloginWarningLead != 0 {
		t.Fatalf("opaque active login kept an unrelated expiry: %+v", ph)
	}
}
