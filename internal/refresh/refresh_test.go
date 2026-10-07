package refresh

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func TestRefreshNativeCredentialsAreSkippedWithoutMutation(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider string
		body     string
		want     string
	}{
		{"Cursor session", "cursor", `{"accessToken":"SYNTHETIC-SESSION","refreshToken":"SYNTHETIC-SESSION"}`, "caam login cursor work"},
		{"Cursor API key", "cursor", `{"apiKey":"SYNTHETIC-KEY"}`, "cursor-agent renews tokens"},
		{"Claude", "claude", `{"refreshToken":"SYNTHETIC-REFRESH"}`, "Claude Code handles refresh internally"},
		{"Grok", "grok", `{"refresh_token":"SYNTHETIC-REFRESH"}`, "Grok Build handles token renewal"},
		{"OpenCode", "opencode", `{"refresh":"SYNTHETIC-REFRESH"}`, "use OpenCode to authenticate"},
		{"unknown provider", "other", `{}`, "provider not supported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := authfile.NewVault(t.TempDir())
			path := filepath.Join(vault.ProfilePath(tc.provider, "work"), "auth.json")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			healthPath := filepath.Join(t.TempDir(), "health.json")
			store := health.NewStorage(healthPath)
			for range 2 {
				for _, err := range []error{
					Preflight(tc.provider, "work", vault),
					RefreshProfile(context.Background(), tc.provider, "work", vault, store),
				} {
					if !errors.Is(err, ErrUnsupported) || !IsSkipped(err) || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("refresh error = %v, want skipped and unsupported with %q", err, tc.want)
					}
				}
			}
			after, readErr := os.ReadFile(path)
			if readErr != nil || !bytes.Equal(after, []byte(tc.body)) {
				t.Fatalf("unsupported refresh changed auth.json: read error = %v", readErr)
			}
			if _, err := os.Stat(healthPath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unsupported refresh wrote health metadata: %v", err)
			}
		})
	}
}

func TestRefreshProfileCodexPreflightPreventsStaleTokenPosts(t *testing.T) {
	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	newer := base.Add(time.Hour)
	snapshot := codexPreflightAuth(t, "alice", "SYNTHETIC-VAULT-REFRESH", base, base)
	for _, tc := range []struct {
		name     string
		live     []byte
		wantSkip bool
	}{
		{"newer live last_refresh", codexPreflightAuth(t, "alice", "SYNTHETIC-LIVE-REFRESH", base, newer), true},
		{"newer live JWT iat", codexPreflightAuth(t, "alice", "SYNTHETIC-LIVE-REFRESH", newer, time.Time{}), true},
		{"equal freshness", codexPreflightAuth(t, "alice", "SYNTHETIC-LIVE-REFRESH", base, base), false},
		{"older live credential", codexPreflightAuth(t, "alice", "SYNTHETIC-LIVE-REFRESH", base.Add(-time.Hour), base.Add(-time.Hour)), false},
		{"different account newer", codexPreflightAuth(t, "bob", "SYNTHETIC-LIVE-REFRESH", newer, newer), false},
		{"unknown live identity", codexPreflightAuth(t, "", "SYNTHETIC-LIVE-REFRESH", newer, newer), false},
		{"unknown live freshness", codexPreflightAuth(t, "alice", "SYNTHETIC-LIVE-REFRESH", time.Time{}, time.Time{}), false},
		{"no live credential", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			liveDir := filepath.Join(root, "live")
			t.Setenv("CODEX_HOME", liveDir)
			vault := authfile.NewVault(filepath.Join(root, "vault"))
			snapshotPath := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
			writePreflightAuth(t, snapshotPath, snapshot)
			livePath := filepath.Join(liveDir, "auth.json")
			if tc.live != nil {
				writePreflightAuth(t, livePath, tc.live)
			}

			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				var request map[string]string
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Errorf("decode refresh request: %v", err)
				}
				if r.Method != http.MethodPost || request["refresh_token"] != "SYNTHETIC-VAULT-REFRESH" {
					t.Errorf("unexpected refresh request method or credential")
				}
				_, _ = w.Write([]byte(`{"access_token":"SYNTHETIC-NEW-ACCESS","refresh_token":"SYNTHETIC-NEW-REFRESH","expires_in":3600}`))
			}))
			t.Cleanup(server.Close)
			oldURL := CodexTokenURL
			CodexTokenURL = server.URL
			t.Cleanup(func() { CodexTokenURL = oldURL })

			err := Preflight("codex", "work", vault)
			if errors.Is(err, ErrStaleCredential) != tc.wantSkip || (err != nil && !tc.wantSkip) {
				t.Fatalf("Preflight error = %v, want skipped = %v", err, tc.wantSkip)
			}
			if posts.Load() != 0 {
				t.Fatal("Preflight made a token endpoint request")
			}
			if data, err := os.ReadFile(snapshotPath); err != nil || !bytes.Equal(data, snapshot) {
				t.Fatalf("Preflight changed the vault: %v", err)
			}

			healthPath := filepath.Join(root, "health.json")
			err = RefreshProfile(context.Background(), "codex", "work", vault, health.NewStorage(healthPath))
			if tc.wantSkip {
				var stale *StaleCredentialError
				if !errors.As(err, &stale) || !IsSkipped(err) || stale.Provider != "codex" || stale.Profile != "work" {
					t.Fatalf("RefreshProfile error = %v, want a typed stale-credential skip", err)
				}
				if !strings.Contains(err.Error(), "caam backup codex work") {
					t.Fatalf("skip has no recovery guidance: %v", err)
				}
				if posts.Load() != 0 {
					t.Fatal("a stale vault refresh token reached the token endpoint")
				}
				if data, err := os.ReadFile(snapshotPath); err != nil || !bytes.Equal(data, snapshot) {
					t.Fatalf("skipped refresh changed the vault: %v", err)
				}
				if _, err := os.Stat(healthPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("skipped refresh wrote a provider verdict: %v", err)
				}
			} else {
				if err != nil || posts.Load() != 1 {
					t.Fatalf("RefreshProfile = %v; token endpoint calls = %d, want one successful attempt", err, posts.Load())
				}
				if data, err := os.ReadFile(snapshotPath); err != nil || !bytes.Contains(data, []byte("SYNTHETIC-NEW-REFRESH")) {
					t.Fatalf("successful refresh did not update the vault: %v", err)
				}
			}
			if tc.live != nil {
				if data, err := os.ReadFile(livePath); err != nil || !bytes.Equal(data, tc.live) {
					t.Fatalf("refresh overwrote a live credential that did not match the vault snapshot: %v", err)
				}
			} else if _, err := os.Stat(livePath); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("refresh unexpectedly created a live credential: %v", err)
			}
		})
	}
}

func TestRefreshProfileRechecksCodexAndResumesAfterBackup(t *testing.T) {
	root := t.TempDir()
	liveDir := filepath.Join(root, "live")
	t.Setenv("CODEX_HOME", liveDir)
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	snapshotPath := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
	livePath := filepath.Join(liveDir, "auth.json")
	base := time.Now().UTC().Truncate(time.Second).Add(-2 * time.Hour)
	old := codexPreflightAuth(t, "alice", "SYNTHETIC-OLD-REFRESH", base, base)
	newer := codexPreflightAuth(t, "alice", "SYNTHETIC-LIVE-REFRESH", base.Add(time.Hour), base.Add(time.Hour))
	writePreflightAuth(t, snapshotPath, old)
	writePreflightAuth(t, livePath, old)

	var posts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		var request map[string]string
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode refresh request: %v", err)
		}
		if request["refresh_token"] != "SYNTHETIC-LIVE-REFRESH" {
			t.Error("a superseded refresh token reached the token endpoint")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"SYNTHETIC-NEW-ACCESS","refresh_token":"SYNTHETIC-NEW-REFRESH","expires_in":3600}`))
	}))
	t.Cleanup(server.Close)
	oldURL := CodexTokenURL
	CodexTokenURL = server.URL
	t.Cleanup(func() { CodexTokenURL = oldURL })

	if err := Preflight("codex", "work", vault); err != nil {
		t.Fatal(err)
	}
	// The CLI rotates after the caller's preflight. RefreshProfile must make
	// its own fresh check before it reads or posts the old refresh token.
	writePreflightAuth(t, livePath, newer)
	for range 2 {
		if err := RefreshProfile(context.Background(), "codex", "work", vault, nil); !errors.Is(err, ErrStaleCredential) {
			t.Fatalf("RefreshProfile after native rotation = %v, want stale skip", err)
		}
	}
	if posts.Load() != 0 {
		t.Fatal("repeated skipped refreshes made token endpoint requests")
	}
	if err := vault.Backup(authfile.CodexAuthFiles(), "work"); err != nil {
		t.Fatalf("back up the current login: %v", err)
	}
	if err := Preflight("codex", "work", vault); err != nil {
		t.Fatalf("Preflight stayed blocked after backup: %v", err)
	}
	if err := RefreshProfile(context.Background(), "codex", "work", vault, nil); err != nil {
		t.Fatalf("refresh after backup: %v", err)
	}
	if posts.Load() != 1 {
		t.Fatalf("token endpoint calls = %d, want exactly one", posts.Load())
	}
	for _, path := range []string{snapshotPath, livePath} {
		if data, err := os.ReadFile(path); err != nil || !bytes.Contains(data, []byte("SYNTHETIC-NEW-REFRESH")) {
			t.Errorf("successful refresh did not update %s: %v", path, err)
		}
	}
}

func TestPreflightGeminiReadsConfigurationWithoutMigration(t *testing.T) {
	const complete = `{"client_id":"SYNTHETIC-ID","client_secret":"SYNTHETIC-SECRET","refresh_token":"SYNTHETIC-REFRESH"}`
	for _, tc := range []struct {
		name        string
		files       map[string]string
		unsupported bool
		malformed   bool
	}{
		{name: "current ADC", files: map[string]string{"oauth_creds.json": complete}},
		{name: "settings ADC", files: map[string]string{"settings.json": complete}},
		{name: "legacy ADC", files: map[string]string{"oauth_credentials.json": complete}},
		{name: "missing ADC", unsupported: true},
		{name: "incomplete ADC", files: map[string]string{"oauth_creds.json": `{"refresh_token":"SYNTHETIC-REFRESH"}`}, unsupported: true},
		{name: "incomplete current does not use superseded legacy", files: map[string]string{"oauth_creds.json": `{}`, "oauth_credentials.json": complete}, unsupported: true},
		{name: "incomplete current does not use settings login", files: map[string]string{"oauth_creds.json": `{}`, "settings.json": complete}, unsupported: true},
		{name: "malformed current does not fall back", files: map[string]string{"oauth_creds.json": `{`, "settings.json": complete}, malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault := authfile.NewVault(t.TempDir())
			dir := vault.ProfilePath("gemini", "work")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			for name, body := range tc.files {
				writePreflightAuth(t, filepath.Join(dir, name), []byte(body))
			}
			err := Preflight("gemini", "work", vault)
			if tc.unsupported {
				if !errors.Is(err, ErrUnsupported) || !IsSkipped(err) {
					t.Fatalf("Preflight = %v, want missing-configuration skip", err)
				}
				if err := RefreshProfile(context.Background(), "gemini", "work", vault, nil); !errors.Is(err, ErrUnsupported) {
					t.Fatalf("RefreshProfile = %v, want missing-configuration skip", err)
				}
			} else if tc.malformed {
				if err == nil || IsSkipped(err) {
					t.Fatalf("Preflight = %v, want malformed-configuration error", err)
				}
			} else if err != nil {
				t.Fatalf("Preflight = %v, want supported configuration", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != len(tc.files) {
				t.Fatalf("Preflight changed profile files: entries = %d, error = %v", len(entries), err)
			}
			for name, body := range tc.files {
				if data, err := os.ReadFile(filepath.Join(dir, name)); err != nil || !bytes.Equal(data, []byte(body)) {
					t.Errorf("Preflight changed %s: %v", name, err)
				}
			}
		})
	}
}

func TestGeminiRefreshKeepsSelectedGrantAndHydratesCurrentHealth(t *testing.T) {
	for _, settings := range []string{
		`{"theme":"synthetic-policy","security":{"auth":{"selectedType":"oauth-personal"}}}`,
		`{"access_token":"synthetic-other-access","refresh_token":"synthetic-other-refresh","expiry":"2030-01-01T00:00:00Z"}`,
	} {
		t.Run(settings, func(t *testing.T) {
			root := t.TempDir()
			vault := authfile.NewVault(filepath.Join(root, "custom-vault"))
			dir := vault.ProfilePath("gemini", "work")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			settingsPath := filepath.Join(dir, "settings.json")
			writePreflightAuth(t, settingsPath, []byte(settings))
			credentialPath := filepath.Join(dir, "oauth_creds.json")
			writePreflightAuth(t, credentialPath, []byte(`{"client_id":"synthetic-id","client_secret":"synthetic-secret","refresh_token":"synthetic-current-refresh","access_token":"synthetic-expired-access","expiry":"2020-01-01T00:00:00Z"}`))
			store := health.NewStorage(filepath.Join(root, "metadata", "health.json"))
			if err := os.MkdirAll(filepath.Dir(store.Path()), 0700); err != nil {
				t.Fatal(err)
			}
			store.SetVaultPath(vault.BasePath())
			before, err := store.GetProfile("gemini", "work")
			if err != nil || before == nil || !before.TokenRenewable || !before.TokenExpiresAt.Before(time.Now()) {
				t.Fatalf("canonical expired renewable grant not selected: %+v, %v", before, err)
			}
			oldRefresh := RefreshGeminiToken
			t.Cleanup(func() { RefreshGeminiToken = oldRefresh })
			RefreshGeminiToken = func(_ context.Context, clientID, clientSecret, refreshToken string) (*GoogleTokenResponse, error) {
				if clientID != "synthetic-id" || clientSecret != "synthetic-secret" || refreshToken != "synthetic-current-refresh" {
					t.Fatal("refresh selected another grant")
				}
				return &GoogleTokenResponse{AccessToken: "synthetic-new-access", ExpiresIn: 3600}, nil
			}
			if err := Preflight("gemini", "work", vault); err != nil {
				t.Fatal(err)
			}
			if err := RefreshProfile(context.Background(), "gemini", "work", vault, store); err != nil {
				t.Fatal(err)
			}
			if got, err := os.ReadFile(settingsPath); err != nil || string(got) != settings {
				t.Fatalf("refresh modified unrelated settings: %v", err)
			}
			if got, err := os.ReadFile(credentialPath); err != nil || !bytes.Contains(got, []byte("synthetic-new-access")) {
				t.Fatalf("selected source did not receive refreshed token: %v", err)
			}
			reloaded := health.NewStorage(store.Path())
			reloaded.SetVaultPath(vault.BasePath())
			after, err := reloaded.GetProfile("gemini", "work")
			if err != nil || after == nil || !after.TokenRenewable || time.Until(after.TokenExpiresAt) < 59*time.Minute || after.CredentialFingerprint != before.CredentialFingerprint {
				t.Fatalf("successful refresh lost current expiry or grant identity after reload: %+v, %v", after, err)
			}
		})
	}
}

func codexPreflightAuth(t *testing.T, account, refreshToken string, issuedAt, lastRefresh time.Time) []byte {
	t.Helper()
	claims := map[string]any{}
	if account != "" {
		claims["sub"] = account
		claims["email"] = account + "@example.com"
	}
	if !issuedAt.IsZero() {
		claims["iat"] = issuedAt.Unix()
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	jwt := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(payload) + ".SYNTHETIC"
	auth := map[string]any{
		"tokens": map[string]any{"id_token": jwt, "access_token": jwt, "refresh_token": refreshToken},
	}
	if !lastRefresh.IsZero() {
		auth["last_refresh"] = lastRefresh.Format(time.RFC3339)
	}
	data, err := json.Marshal(auth)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writePreflightAuth(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// =============================================================================
// ShouldRefresh Tests
// =============================================================================

func TestShouldRefresh_NilHealth(t *testing.T) {
	// Nil health should return false - we don't know if refresh is needed
	result := ShouldRefresh(nil, DefaultRefreshThreshold)
	if result {
		t.Error("ShouldRefresh(nil) = true, want false")
	}
}

func TestShouldRefresh_ZeroExpiry(t *testing.T) {
	// Zero expiry time means unknown - should return false
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Time{}, // zero time
	}
	result := ShouldRefresh(h, DefaultRefreshThreshold)
	if result {
		t.Error("ShouldRefresh with zero expiry = true, want false")
	}
}

func TestShouldRefresh_CredentialPolicy(t *testing.T) {
	expiry := time.Now().Add(5 * time.Minute)
	for _, tc := range []struct {
		name   string
		health health.ProfileHealth
		want   bool
	}{
		{
			name:   "Cursor session needs a login",
			health: health.ProfileHealth{TokenExpiresAt: expiry, ReloginWarningLead: 7 * 24 * time.Hour},
		},
		{
			name:   "Cursor API key renews through cursor-agent",
			health: health.ProfileHealth{TokenExpiresAt: expiry, TokenRenewable: true, SelfRefreshing: true},
		},
		{
			name:   "Claude self-refresh remains CLI-owned",
			health: health.ProfileHealth{TokenExpiresAt: expiry, SelfRefreshing: true},
		},
		{
			name:   "caam-owned renewable token can refresh",
			health: health.ProfileHealth{TokenExpiresAt: expiry, TokenRenewable: true},
			want:   true,
		},
		{
			name:   "stale relogin lead cannot block a renewable token",
			health: health.ProfileHealth{TokenExpiresAt: expiry, TokenRenewable: true, ReloginWarningLead: 7 * 24 * time.Hour},
			want:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShouldRefresh(&tc.health, 0); got != tc.want {
				t.Errorf("ShouldRefresh = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldRefresh_NotExpiring(t *testing.T) {
	// Token expiring in 2 hours with 10min threshold - should not refresh
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(2 * time.Hour),
	}
	result := ShouldRefresh(h, 10*time.Minute)
	if result {
		t.Errorf("ShouldRefresh with 2h TTL and 10min threshold = true, want false")
	}
}

func TestShouldRefresh_Expiring(t *testing.T) {
	// Token expiring in 5 minutes with 10min threshold - should refresh
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(5 * time.Minute),
	}
	result := ShouldRefresh(h, 10*time.Minute)
	if !result {
		t.Errorf("ShouldRefresh with 5min TTL and 10min threshold = false, want true")
	}
}

func TestShouldRefresh_AlreadyExpired(t *testing.T) {
	// A hard-expired login needs a human, while an expired access token with
	// a renewal credential must recover after daemon downtime.
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(-5 * time.Minute),
	}
	result := ShouldRefresh(h, 10*time.Minute)
	if result {
		t.Errorf("ShouldRefresh with expired token = true, want false")
	}
	h.TokenRenewable = true
	if !ShouldRefresh(h, 10*time.Minute) {
		t.Fatal("expired renewable access token was not scheduled for recovery")
	}
	h.ProviderRejectedAt = time.Now()
	if ShouldRefresh(h, 10*time.Minute) {
		t.Fatal("known rejected refresh token was scheduled again")
	}
	h.ProviderRejectedAt = time.Time{}
	h.SelfRefreshing = true
	if ShouldRefresh(h, 10*time.Minute) {
		t.Fatal("expired native-owned token was scheduled for CAAM renewal")
	}
}

func TestShouldRefresh_DefaultThreshold(t *testing.T) {
	// When threshold is 0, should use DefaultRefreshThreshold (10 minutes)
	// Token expiring in 5 minutes should trigger refresh
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(5 * time.Minute),
	}
	result := ShouldRefresh(h, 0) // 0 means use default
	if !result {
		t.Errorf("ShouldRefresh with 5min TTL and default threshold = false, want true")
	}

	// Token expiring in 15 minutes should NOT trigger refresh with default threshold
	h2 := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(15 * time.Minute),
	}
	result2 := ShouldRefresh(h2, 0)
	if result2 {
		t.Errorf("ShouldRefresh with 15min TTL and default threshold = true, want false")
	}
}

func TestShouldRefresh_CustomThreshold(t *testing.T) {
	// Custom 30-minute threshold
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(20 * time.Minute),
	}

	// With 30min threshold, 20min TTL should trigger refresh
	result := ShouldRefresh(h, 30*time.Minute)
	if !result {
		t.Errorf("ShouldRefresh with 20min TTL and 30min threshold = false, want true")
	}

	// With 10min threshold, 20min TTL should NOT trigger refresh
	result2 := ShouldRefresh(h, 10*time.Minute)
	if result2 {
		t.Errorf("ShouldRefresh with 20min TTL and 10min threshold = true, want false")
	}
}

func TestShouldRefresh_EdgeCaseJustAboveThreshold(t *testing.T) {
	// Token expiring just above threshold - should NOT refresh (ttl must be < threshold)
	threshold := 10 * time.Minute
	// Add a buffer to account for test execution time
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(threshold + 1*time.Second),
	}
	result := ShouldRefresh(h, threshold)
	if result {
		t.Errorf("ShouldRefresh with TTL above threshold = true, want false")
	}
}

func TestShouldRefresh_EdgeCaseJustBelowThreshold(t *testing.T) {
	// Token expiring just below threshold - should refresh (ttl < threshold)
	threshold := 10 * time.Minute
	h := &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(threshold - 1*time.Second),
	}
	result := ShouldRefresh(h, threshold)
	if !result {
		t.Errorf("ShouldRefresh with TTL below threshold = false, want true")
	}
}

// =============================================================================
// getRefreshTokenFromJSON Tests
// =============================================================================

func TestGetRefreshToken_SnakeCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token":  "test-access-token",
		"refresh_token": "test-refresh-token-snake",
		"token_type":    "Bearer",
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "test-refresh-token-snake" {
		t.Errorf("token = %q, want %q", token, "test-refresh-token-snake")
	}
}

func TestGetRefreshToken_CamelCase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"accessToken":  "test-access-token",
		"refreshToken": "test-refresh-token-camel",
		"tokenType":    "Bearer",
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "test-refresh-token-camel" {
		t.Errorf("token = %q, want %q", token, "test-refresh-token-camel")
	}
}

func TestGetRefreshToken_SnakeCasePreferredOverCamelCase(t *testing.T) {
	// When both are present, snake_case should be preferred
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"refresh_token": "snake-wins",
		"refreshToken":  "camel-loses",
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "snake-wins" {
		t.Errorf("token = %q, want %q (snake_case should be preferred)", token, "snake-wins")
	}
}

func TestGetRefreshToken_Missing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token": "test-access-token",
		"token_type":   "Bearer",
		// No refresh_token or refreshToken
	}
	writeJSON(t, path, content)

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when refresh_token is missing")
	}
}

func TestGetRefreshToken_EmptyToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token":  "test-access-token",
		"refresh_token": "", // Empty string
	}
	writeJSON(t, path, content)

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when refresh_token is empty")
	}
}

func TestGetRefreshToken_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	// Write invalid JSON
	if err := os.WriteFile(path, []byte("{not valid json"), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error on invalid JSON")
	}
}

func TestGetRefreshToken_MissingFile(t *testing.T) {
	_, err := getRefreshTokenFromJSON("/nonexistent/path/auth.json")
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when file doesn't exist")
	}
	if !os.IsNotExist(err) {
		t.Logf("Note: error is not os.IsNotExist, got: %v", err)
	}
}

func TestGetRefreshToken_NonStringToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"access_token":  "test-access-token",
		"refresh_token": 12345, // Number instead of string
	}
	writeJSON(t, path, content)

	_, err := getRefreshTokenFromJSON(path)
	if err == nil {
		t.Error("getRefreshTokenFromJSON should error when refresh_token is not a string")
	}
}

// =============================================================================
// readNestedStringField Tests
// =============================================================================

func TestReadNestedStringField_ValidNested(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": "nested-token",
			"access_token":  "nested-access",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token", "refreshToken")
	if result != "nested-token" {
		t.Errorf("readNestedStringField = %q, want %q", result, "nested-token")
	}
}

func TestReadNestedStringField_CamelCaseFallback(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refreshToken": "camel-token",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token", "refreshToken")
	if result != "camel-token" {
		t.Errorf("readNestedStringField = %q, want %q", result, "camel-token")
	}
}

func TestReadNestedStringField_NilMap(t *testing.T) {
	result := readNestedStringField(nil, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField(nil) = %q, want empty", result)
	}
}

func TestReadNestedStringField_MissingNestedKey(t *testing.T) {
	m := map[string]interface{}{
		"other": map[string]interface{}{
			"refresh_token": "token",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty (nested key missing)", result)
	}
}

func TestReadNestedStringField_NotAMap(t *testing.T) {
	m := map[string]interface{}{
		"tokens": "not a map",
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty (not a map)", result)
	}
}

func TestReadNestedStringField_KeyNotString(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": 12345, // Not a string
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty (not a string)", result)
	}
}

func TestReadNestedStringField_EmptyValue(t *testing.T) {
	m := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": "",
		},
	}

	result := readNestedStringField(m, "tokens", "refresh_token")
	if result != "" {
		t.Errorf("readNestedStringField = %q, want empty", result)
	}
}

// =============================================================================
// readStringField Tests
// =============================================================================

func TestReadStringField_FirstKeyMatch(t *testing.T) {
	m := map[string]interface{}{
		"key1": "value1",
		"key2": "value2",
	}

	result := readStringField(m, "key1", "key2")
	if result != "value1" {
		t.Errorf("readStringField = %q, want %q", result, "value1")
	}
}

func TestReadStringField_SecondKeyMatch(t *testing.T) {
	m := map[string]interface{}{
		"key2": "value2",
	}

	result := readStringField(m, "key1", "key2")
	if result != "value2" {
		t.Errorf("readStringField = %q, want %q", result, "value2")
	}
}

func TestReadStringField_NilMap(t *testing.T) {
	result := readStringField(nil, "key1")
	if result != "" {
		t.Errorf("readStringField(nil) = %q, want empty", result)
	}
}

func TestReadStringField_NoMatch(t *testing.T) {
	m := map[string]interface{}{
		"other": "value",
	}

	result := readStringField(m, "key1", "key2")
	if result != "" {
		t.Errorf("readStringField = %q, want empty (no match)", result)
	}
}

func TestReadStringField_NotString(t *testing.T) {
	m := map[string]interface{}{
		"key1": 123,
	}

	result := readStringField(m, "key1")
	if result != "" {
		t.Errorf("readStringField = %q, want empty (not a string)", result)
	}
}

// =============================================================================
// getRefreshTokenFromJSON Nested Token Tests
// =============================================================================

func TestGetRefreshToken_NestedTokensField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"tokens": map[string]interface{}{
			"refresh_token": "nested-refresh-token",
		},
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "nested-refresh-token" {
		t.Errorf("token = %q, want %q", token, "nested-refresh-token")
	}
}

func TestGetRefreshToken_ClaudeAiOauth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")

	content := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"refreshToken": "claude-oauth-refresh",
		},
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "claude-oauth-refresh" {
		t.Errorf("token = %q, want %q", token, "claude-oauth-refresh")
	}
}

func TestGetRefreshToken_TopLevelPreferred(t *testing.T) {
	// When token exists at top level and nested, top level should be preferred
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")

	content := map[string]interface{}{
		"refresh_token": "top-level-token",
		"tokens": map[string]interface{}{
			"refresh_token": "nested-token",
		},
	}
	writeJSON(t, path, content)

	token, err := getRefreshTokenFromJSON(path)
	if err != nil {
		t.Fatalf("getRefreshTokenFromJSON error: %v", err)
	}
	if token != "top-level-token" {
		t.Errorf("token = %q, want %q (top level should be preferred)", token, "top-level-token")
	}
}

// =============================================================================
// snapshotMatchesProfile Tests
// =============================================================================

func TestSnapshotMatchesProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(filepath.Join(tmpDir, "vault"))
	livePath := filepath.Join(tmpDir, "live", "auth.json")

	if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
		t.Fatalf("failed to create live dir: %v", err)
	}

	snapshot := map[string][]byte{
		livePath: []byte(`{"access_token":"live"}`),
	}

	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: livePath},
		},
	}

	backupPath := vault.BackupPath("codex", "work", filepath.Base(livePath))
	if err := os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	if err := os.WriteFile(backupPath, snapshot[livePath], 0600); err != nil {
		t.Fatalf("failed to write backup file: %v", err)
	}

	if !snapshotMatchesProfile(fileSet, vault, "work", snapshot) {
		t.Errorf("snapshotMatchesProfile() = false, want true")
	}
}

func TestSnapshotMatchesProfile_Mismatch(t *testing.T) {
	tmpDir := t.TempDir()
	vault := authfile.NewVault(filepath.Join(tmpDir, "vault"))
	livePath := filepath.Join(tmpDir, "live", "auth.json")

	if err := os.MkdirAll(filepath.Dir(livePath), 0700); err != nil {
		t.Fatalf("failed to create live dir: %v", err)
	}

	snapshot := map[string][]byte{
		livePath: []byte(`{"access_token":"live"}`),
	}

	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: livePath},
		},
	}

	backupPath := vault.BackupPath("codex", "work", filepath.Base(livePath))
	if err := os.MkdirAll(filepath.Dir(backupPath), 0700); err != nil {
		t.Fatalf("failed to create backup dir: %v", err)
	}
	if err := os.WriteFile(backupPath, []byte(`{"access_token":"different"}`), 0600); err != nil {
		t.Fatalf("failed to write backup file: %v", err)
	}

	if snapshotMatchesProfile(fileSet, vault, "work", snapshot) {
		t.Errorf("snapshotMatchesProfile() = true, want false")
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

func writeJSON(t *testing.T, path string, data interface{}) {
	t.Helper()
	jsonBytes, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("failed to marshal JSON: %v", err)
	}
	if err := os.WriteFile(path, jsonBytes, 0600); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}
