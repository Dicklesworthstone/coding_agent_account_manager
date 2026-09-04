package health

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #102: `caam ls` reported three live Codex profiles as "warning" from a
// months-old access-token expiry that the Codex CLI renews on next use. The
// fix separates "needs a refresh" (SelfRefreshing, which drives warnings and
// the refresh daemon) from "needs a human" (Renewable, which drives the
// verdict), and does it for every provider.

// TestParsersSeparateRenewalFromReauth pins the two flags for all four
// providers. SelfRefreshing stays Claude-only — caam refreshes the others and
// must keep being told their tokens are near expiry — while Renewable follows
// the stored refresh token everywhere.
func TestParsersSeparateRenewalFromReauth(t *testing.T) {
	lapsed := time.Now().Add(-72 * time.Hour)

	t.Run("claude with refresh token", func(t *testing.T) {
		dir := t.TempDir()
		writeJSON(t, filepath.Join(dir, ".credentials.json"), map[string]any{
			"claudeAiOauth": map[string]any{
				"accessToken": "a", "refreshToken": "r", "expiresAt": lapsed.UnixMilli(),
			},
		})
		info, err := ParseClaudeExpiry(dir)
		if err != nil {
			t.Fatalf("ParseClaudeExpiry: %v", err)
		}
		if !info.SelfRefreshing {
			t.Error("SelfRefreshing = false, want true (caam must not refresh Claude)")
		}
		if !info.Renewable {
			t.Error("Renewable = false, want true")
		}
	})

	t.Run("claude without refresh token", func(t *testing.T) {
		dir := t.TempDir()
		writeJSON(t, filepath.Join(dir, ".credentials.json"), map[string]any{
			"claudeAiOauth": map[string]any{"accessToken": "a", "expiresAt": lapsed.UnixMilli()},
		})
		info, err := ParseClaudeExpiry(dir)
		if err != nil {
			t.Fatalf("ParseClaudeExpiry: %v", err)
		}
		if info.SelfRefreshing || info.Renewable {
			t.Errorf("SelfRefreshing=%v Renewable=%v, want both false", info.SelfRefreshing, info.Renewable)
		}
	})

	t.Run("codex", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		writeJSON(t, path, map[string]any{
			"access_token": "a", "refresh_token": "r", "expires_at": lapsed.Unix(),
		})
		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry: %v", err)
		}
		if info.SelfRefreshing {
			t.Error("SelfRefreshing = true, want false: caam refreshes Codex and needs the expiry signal")
		}
		if !info.Renewable {
			t.Error("Renewable = false, want true: the refresh token renews this without a human")
		}
	})

	t.Run("codex without refresh token", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		writeJSON(t, path, map[string]any{"access_token": "a", "expires_at": lapsed.Unix()})
		info, err := ParseCodexExpiry(path)
		if err != nil {
			t.Fatalf("ParseCodexExpiry: %v", err)
		}
		if info.Renewable {
			t.Error("Renewable = true without a refresh token, want false")
		}
	})

	t.Run("grok", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "auth.json")
		if err := os.WriteFile(path, []byte(`{"https://auth.x.ai::00000000-0000-0000-0000-000000000000":`+
			`{"key":"SYNTHETIC","auth_mode":"sso","refresh_token":"SYNTHETIC-REFRESH",`+
			`"expires_at":"2020-01-01T00:00:00Z"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		info, err := ParseGrokExpiry(path)
		if err != nil {
			t.Fatalf("ParseGrokExpiry: %v", err)
		}
		if info.SelfRefreshing {
			t.Error("SelfRefreshing = true, want false")
		}
		if !info.Renewable {
			t.Error("Renewable = false, want true")
		}
	})

	t.Run("gemini", func(t *testing.T) {
		dir := t.TempDir()
		writeJSON(t, filepath.Join(dir, "oauth_creds.json"), map[string]any{
			"access_token": "a", "refresh_token": "r", "expiry": lapsed.Format(time.RFC3339),
		})
		info, err := ParseGeminiExpiry(dir)
		if err != nil {
			t.Fatalf("ParseGeminiExpiry: %v", err)
		}
		if info.SelfRefreshing {
			t.Error("SelfRefreshing = true, want false")
		}
		if !info.Renewable {
			t.Error("Renewable = false, want true")
		}
	})
}

// TestRenewableTokenIsNotAnExpiredAccount is the reported symptom, at the
// verdict layer: a lapsed access token with something to renew it must not
// read as critical, while a lapsed one without must.
func TestRenewableTokenIsNotAnExpiredAccount(t *testing.T) {
	lapsed := time.Now().Add(-10 * 24 * time.Hour)

	renewable := &ProfileHealth{TokenExpiresAt: lapsed, TokenRenewable: true, PlanType: "pro"}
	if got := CalculateStatus(renewable); got != StatusHealthy {
		t.Errorf("renewable lapsed token: status = %v, want healthy", got)
	}
	if reasons := StatusReasons(renewable); len(reasons) != 0 {
		t.Errorf("renewable lapsed token: reasons = %v, want none", reasons)
	}
	if got := FormatRecommendation("codex", "cx", renewable); got == "" {
		t.Error("renewable lapsed token: want a refresh recommendation, got none")
	} else if want := "caam refresh codex cx"; !strings.Contains(got, want) {
		t.Errorf("renewable lapsed token: recommendation = %q, want it to mention %q", got, want)
	}

	dead := &ProfileHealth{TokenExpiresAt: lapsed, PlanType: "pro"}
	if got := CalculateStatus(dead); got != StatusCritical {
		t.Errorf("non-renewable lapsed token: status = %v, want critical", got)
	}
	if got := FormatRecommendation("codex", "cx", dead); !strings.Contains(got, "caam login codex cx") {
		t.Errorf("non-renewable lapsed token: recommendation = %q, want a login", got)
	}
}

// TestCredentialSignals pins the three-signal contract, including the rule
// that absent evidence stays nil rather than being promoted either way.
func TestCredentialSignals(t *testing.T) {
	cfg := DefaultHealthConfig()
	now := time.Now()

	tests := []struct {
		name                               string
		health                             *ProfileHealth
		refreshDue, launchUsable, loginReq *bool
	}{
		{
			name:   "codex lapsed but renewable",
			health: &ProfileHealth{TokenExpiresAt: now.Add(-72 * time.Hour), TokenRenewable: true},
			// caam refreshes Codex, so the refresh signal must stay hot even
			// though nobody needs to log in.
			refreshDue: ptr(true), launchUsable: ptr(true), loginReq: ptr(false),
		},
		{
			name:       "codex lapsed with nothing to renew",
			health:     &ProfileHealth{TokenExpiresAt: now.Add(-72 * time.Hour)},
			refreshDue: ptr(true), launchUsable: ptr(false), loginReq: ptr(true),
		},
		{
			name: "claude self-refreshing and lapsed",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-72 * time.Hour),
				SelfRefreshing: true, TokenRenewable: true,
			},
			// caam's Claude refresh is disabled: never claim a refresh is due.
			refreshDue: ptr(false), launchUsable: ptr(true), loginReq: ptr(false),
		},
		{
			name:       "healthy token far from expiry",
			health:     &ProfileHealth{TokenExpiresAt: now.Add(8 * time.Hour), TokenRenewable: true},
			refreshDue: ptr(false), launchUsable: ptr(true), loginReq: ptr(false),
		},
		{
			name:       "inside the refresh window",
			health:     &ProfileHealth{TokenExpiresAt: now.Add(20 * time.Minute), TokenRenewable: true},
			refreshDue: ptr(true), launchUsable: ptr(true), loginReq: ptr(false),
		},
		{
			name:       "rate limited but valid",
			health:     &ProfileHealth{TokenExpiresAt: now.Add(8 * time.Hour), TokenRenewable: true, RateLimitedUntil: now.Add(time.Hour)},
			refreshDue: ptr(false), launchUsable: ptr(false), loginReq: ptr(false),
		},
		{
			name:   "no expiry evidence at all",
			health: &ProfileHealth{},
			// Unknown stays unknown; nothing is promoted to healthy or dead.
			refreshDue: nil, launchUsable: nil, loginReq: nil,
		},
		{
			name:       "no expiry evidence but capped",
			health:     &ProfileHealth{RateLimitedUntil: now.Add(time.Hour)},
			refreshDue: nil, launchUsable: ptr(false), loginReq: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CredentialSignals(tc.health, cfg)
			checkBoolPtr(t, "refresh_due", got.RefreshDue, tc.refreshDue)
			checkBoolPtr(t, "launch_usable", got.LaunchUsable, tc.launchUsable)
			checkBoolPtr(t, "login_required", got.LoginRequired, tc.loginReq)
		})
	}

	if got := CredentialSignals(nil, cfg); got.RefreshDue != nil || got.LaunchUsable != nil || got.LoginRequired != nil {
		t.Errorf("nil health: signals = %+v, want all nil", got)
	}
}

// TestSignalsMarshalUnknownAsNull: a controller must be able to tell "no" from
// "we don't know" on the wire.
func TestSignalsMarshalUnknownAsNull(t *testing.T) {
	data, err := json.Marshal(CredentialSignals(&ProfileHealth{}, DefaultHealthConfig()))
	if err != nil {
		t.Fatal(err)
	}
	want := `{"refresh_due":null,"launch_usable":null,"login_required":null}`
	if string(data) != want {
		t.Errorf("marshal = %s, want %s", data, want)
	}
}

func ptr(b bool) *bool { return &b }

func checkBoolPtr(t *testing.T, field string, got, want *bool) {
	t.Helper()
	switch {
	case got == nil && want == nil:
	case got == nil || want == nil:
		t.Errorf("%s = %v, want %v", field, fmtBoolPtr(got), fmtBoolPtr(want))
	case *got != *want:
		t.Errorf("%s = %v, want %v", field, *got, *want)
	}
}

func fmtBoolPtr(b *bool) string {
	if b == nil {
		return "nil"
	}
	if *b {
		return "true"
	}
	return "false"
}

func writeJSON(t *testing.T, path string, body map[string]any) {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}
