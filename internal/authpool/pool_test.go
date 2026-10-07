package authpool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func writePoolCredential(t *testing.T, root, provider, name, file string, value any) string {
	t.Helper()
	path := filepath.Join(root, provider, name, file)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func poolCodexCredential(expiry time.Time, generation string) map[string]any {
	claims := fmt.Sprintf(`{"exp":%d,"sub":"synthetic-account"}`, expiry.Unix())
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
	return map[string]any{"tokens": map[string]any{"access_token": jwt, "refresh_token": generation}}
}

func TestPoolReconcilesRealVaultCredentialsAndMembership(t *testing.T) {
	root := t.TempDir()
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	store := health.NewStorage(filepath.Join(root, "custom-health.json"))
	pool := NewAuthPool(WithVault(vault), WithHealthStorage(store))
	expired := time.Now().Add(-time.Hour).Truncate(time.Second)
	future := time.Now().Add(time.Hour).Truncate(time.Second)
	path := writePoolCredential(t, vault.BasePath(), "codex", "renewable", "auth.json", poolCodexCredential(expired, "synthetic-old"))
	writePoolCredential(t, vault.BasePath(), "codex", "_original", "auth.json", poolCodexCredential(expired, "synthetic-system"))
	writePoolCredential(t, vault.BasePath(), "unrecognized", "ignored", "auth.json", poolCodexCredential(expired, "synthetic-other"))
	writePoolCredential(t, vault.BasePath(), "codex", "api", "auth.json", map[string]any{"OPENAI_API_KEY": "synthetic-api"})
	writePoolCredential(t, vault.BasePath(), "codex", "broken", "auth.json", map[string]any{"policy": true})
	writePoolCredential(t, vault.BasePath(), "gemini", "renewable", "oauth_creds.json", map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_at": expired.Unix()})
	writePoolCredential(t, vault.BasePath(), "claude", "native", ".credentials.json", map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expired.UnixMilli()}})
	writePoolCredential(t, vault.BasePath(), "grok", "native", "auth.json", map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_at": expired.Unix()})
	jwt := poolCodexCredential(expired, "unused")["tokens"].(map[string]any)["access_token"]
	writePoolCredential(t, vault.BasePath(), "cursor", "session", "auth.json", map[string]any{"accessToken": jwt, "refreshToken": "synthetic-session-alias"})
	writePoolCredential(t, vault.BasePath(), "cursor", "api", "auth.json", map[string]any{"accessToken": jwt, "apiKey": "synthetic-key"})
	// A malformed ancillary file must not override canonical Cursor auth.
	writePoolCredential(t, vault.BasePath(), "cursor", "api", "cli-config.json", false)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.LoadFromVault(context.Background()); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]PoolStatus{
		"codex:renewable": PoolStatusExpired, "codex:api": PoolStatusReady,
		"codex:broken": PoolStatusError, "gemini:renewable": PoolStatusExpired,
		"claude:native": PoolStatusReady, "grok:native": PoolStatusReady,
		"cursor:session": PoolStatusExpired, "cursor:api": PoolStatusReady,
	} {
		pool.mu.RLock()
		got := pool.profiles[key].Clone()
		pool.mu.RUnlock()
		if got == nil || got.Status != want {
			t.Errorf("%s = %+v, want %s", key, got, want)
		}
	}
	if pool.Count() != 8 || len(pool.GetProfilesNeedingRefresh("")) != 2 {
		t.Fatalf("membership/renewal candidates are wrong: %+v / %+v", pool.GetAllProfiles(""), pool.GetProfilesNeedingRefresh(""))
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(before) {
		t.Fatal("read-only reconciliation changed vault credentials")
	}
	// Preserve operational state while the source remains the same, then
	// recover from a new grant without replaying stale error/cached expiry.
	pool.SetCooldown("codex", "renewable", time.Hour)
	pool.SetError("codex", "renewable", fmt.Errorf("synthetic transient failure"))
	if err := pool.LoadFromVault(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := pool.GetProfile("codex", "renewable"); p.Status != PoolStatusCooldown || p.ErrorCount != 1 {
		t.Fatalf("reconciliation discarded current cooldown/error state: %+v", p)
	}
	writePoolCredential(t, vault.BasePath(), "codex", "renewable", "auth.json", poolCodexCredential(future, "synthetic-replacement"))
	if err := pool.LoadFromVault(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := pool.GetProfile("codex", "renewable"); p.Status != PoolStatusCooldown || p.ErrorCount != 0 || !p.TokenExpiry.Equal(future) {
		t.Fatalf("replacement lost cooldown or retained old credential facts: %+v", p)
	}
	if err := os.Rename(filepath.Dir(path), filepath.Join(root, "removed-profile")); err != nil {
		t.Fatal(err)
	}
	writePoolCredential(t, vault.BasePath(), "codex", "added", "auth.json", poolCodexCredential(expired, "synthetic-added"))
	if err := pool.LoadFromVault(context.Background()); err != nil {
		t.Fatal(err)
	}
	if pool.GetProfile("codex", "renewable") != nil || pool.GetStatus("codex", "added") != PoolStatusExpired {
		t.Fatal("later reconciliation did not apply membership changes")
	}
}

func TestPoolSelectedAPIKeyOwnsHealthAndGeneration(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			vault := authfile.NewVault(filepath.Join(root, "vault"))
			store := health.NewStorage(filepath.Join(root, "health.json"))
			pool := NewAuthPool(WithVault(vault), WithHealthStorage(store))
			old := poolCodexCredential(time.Now().Add(-time.Hour), "synthetic-unused-refresh")
			writeKey := func(key string) {
				if provider == "codex" {
					body := map[string]any{"auth_mode": "apikey", "OPENAI_API_KEY": key, "tokens": old["tokens"]}
					writePoolCredential(t, vault.BasePath(), provider, "work", "auth.json", body)
				} else {
					writePoolCredential(t, vault.BasePath(), provider, "work", "settings.json", map[string]any{"security": map[string]any{"auth": map[string]any{"selectedType": "gemini-api-key"}}})
					path := filepath.Join(vault.ProfilePath(provider, "work"), ".env")
					if err := os.WriteFile(path, []byte("export GEMINI_API_KEY='"+key+"' # selected account\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			writeKey("synthetic-selected-key")
			if provider == "gemini" {
				// A selected API key must not even validate an unused OAuth cache.
				if err := os.WriteFile(filepath.Join(vault.ProfilePath(provider, "work"), "oauth_creds.json"), []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			oldData, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.RecordProviderVerification(provider, "work", health.ProviderVerification{
				Reason: "refresh_token_invalidated", Fingerprint: health.CodexCredentialFingerprint(oldData),
			}); err != nil {
				t.Fatal(err)
			}
			if err := pool.LoadFromVault(context.Background()); err != nil {
				t.Fatal(err)
			}
			first := pool.GetProfile(provider, "work")
			if first == nil || first.Status != PoolStatusReady || !first.TokenExpiry.IsZero() || first.refreshable || first.generation == "" {
				t.Fatalf("unused OAuth determined selected key state: %+v", first)
			}
			if candidates := pool.GetProfilesNeedingRefresh(provider); len(candidates) != 0 {
				t.Fatalf("API key scheduled unused OAuth refresh: %+v", candidates)
			}
			for range pool.maxRetries {
				pool.SetError(provider, "work", fmt.Errorf("synthetic failure for selected key"))
			}
			// Rotating unused OAuth cannot erase the selected key's errors.
			old["tokens"].(map[string]any)["refresh_token"] = "synthetic-unused-rotated"
			if provider == "gemini" {
				writePoolCredential(t, vault.BasePath(), provider, "work", "oauth_creds.json", map[string]any{
					"access_token": "synthetic-unused-access", "refresh_token": "synthetic-unused-rotated", "expires_at": time.Now().Add(-time.Hour).Unix(),
				})
			}
			writeKey("synthetic-selected-key")
			if err := pool.LoadFromVault(context.Background()); err != nil {
				t.Fatal(err)
			}
			if current := pool.GetProfile(provider, "work"); current.generation != first.generation || current.ErrorCount != pool.maxRetries {
				t.Fatalf("unchanged selected key lost its operational errors: %+v", current)
			}
			writeKey("synthetic-replacement-key")
			if err := pool.LoadFromVault(context.Background()); err != nil {
				t.Fatal(err)
			}
			if current := pool.GetProfile(provider, "work"); current.Status != PoolStatusReady || current.ErrorCount != 0 || current.generation == first.generation || current.refreshable {
				t.Fatalf("selected key replacement inherited old grant state: %+v", current)
			}
		})
	}
}

func TestPoolSelectedGeminiKeyCannotBorrowOAuth(t *testing.T) {
	for _, key := range []string{"", "GEMINI_API_KEY=\n", "GEMINI_API_KEY='unterminated\n"} {
		t.Run(fmt.Sprintf("key-bytes=%d", len(key)), func(t *testing.T) {
			vault := authfile.NewVault(t.TempDir())
			pool := NewAuthPool(WithVault(vault))
			writePoolCredential(t, vault.BasePath(), "gemini", "work", "settings.json", map[string]any{"selectedAuthType": "gemini-api-key"})
			writePoolCredential(t, vault.BasePath(), "gemini", "work", "oauth_creds.json", map[string]any{"access_token": "synthetic-unrelated-access", "refresh_token": "synthetic-unrelated-refresh", "expires_at": time.Now().Add(time.Hour).Unix()})
			if key != "" {
				if err := os.WriteFile(filepath.Join(vault.ProfilePath("gemini", "work"), ".env"), []byte(key), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := pool.LoadFromVault(context.Background()); err != nil {
				t.Fatal(err)
			}
			if current := pool.GetProfile("gemini", "work"); current == nil || current.Status != PoolStatusError || current.refreshable {
				t.Fatalf("missing selected key borrowed unused OAuth: %+v", current)
			}
		})
	}
}

func TestPoolCurrentProviderRejectionAndInvalidSource(t *testing.T) {
	root := t.TempDir()
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	store := health.NewStorage(filepath.Join(root, "health.json"))
	pool := NewAuthPool(WithVault(vault), WithHealthStorage(store))
	path := writePoolCredential(t, vault.BasePath(), "codex", "work", "auth.json", poolCodexCredential(time.Now().Add(-time.Minute), "synthetic-rejected"))
	h, err := store.GetProfile("codex", "work")
	if err != nil || h == nil {
		t.Fatalf("read health: %v %+v", err, h)
	}
	if err := store.RecordProviderVerification("codex", "work", health.ProviderVerification{Reason: "refresh_token_invalidated", Fingerprint: h.CredentialFingerprint}); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", "{", `{"tokens":{"refresh_token":"synthetic-rejected"}}`} {
		if body != "" {
			if err := os.WriteFile(path, []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
		}
		if err := pool.LoadFromVault(context.Background()); err != nil {
			t.Fatal(err)
		}
		p := pool.GetProfile("codex", "work")
		if p.Status != PoolStatusError || len(pool.GetProfilesNeedingRefresh("")) != 0 {
			t.Fatalf("rejected/invalid source was eligible: %+v", p)
		}
		if body != "" && !p.TokenExpiry.IsZero() {
			t.Fatal("invalid source retained cached expiry")
		}
	}
	writePoolCredential(t, vault.BasePath(), "codex", "work", "auth.json", poolCodexCredential(time.Now().Add(time.Hour), "synthetic-new-login"))
	if err := pool.LoadFromVault(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := pool.GetProfile("codex", "work"); p.Status != PoolStatusReady || p.ErrorMessage != "" {
		t.Fatalf("new login did not recover rejected profile: %+v", p)
	}
}

func TestPoolAcceptsCompleteRefreshOnlyGeminiADC(t *testing.T) {
	for _, filename := range []string{"oauth_creds.json", "oauth_credentials.json", "settings.json"} {
		t.Run(filename, func(t *testing.T) {
			root := t.TempDir()
			vault := authfile.NewVault(filepath.Join(root, "vault"))
			pool := NewAuthPool(WithVault(vault), WithHealthStorage(health.NewStorage(filepath.Join(root, "health.json"))))
			path := writePoolCredential(t, vault.BasePath(), "gemini", "adc", filename, map[string]any{
				"client_id": "synthetic-id", "client_secret": "synthetic-secret", "refresh_token": "synthetic-refresh", "type": "authorized_user",
			})
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := pool.LoadFromVault(context.Background()); err != nil {
				t.Fatal(err)
			}
			p := pool.GetProfile("gemini", "adc")
			if p == nil || p.Status != PoolStatusReady || !p.refreshable || !p.TokenExpiry.IsZero() {
				t.Fatalf("complete ADC grant was not eligible: %+v", p)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(after) != string(before) {
				t.Fatalf("passive ADC reconciliation changed source: %v", err)
			}
			writePoolCredential(t, vault.BasePath(), "gemini", "adc", filename, map[string]any{"refresh_token": "synthetic-incomplete"})
			if err := pool.LoadFromVault(context.Background()); err != nil {
				t.Fatal(err)
			}
			if p := pool.GetProfile("gemini", "adc"); p.Status != PoolStatusError || p.refreshable {
				t.Fatalf("incomplete refresh-only source was accepted as ADC: %+v", p)
			}
		})
	}
}

func TestPoolOldReservationCannotCompleteReplacement(t *testing.T) {
	pool := NewAuthPool()
	pool.AddProfile("codex", "work")
	previous, old, ok := pool.reserveRefresh("codex", "work")
	if !ok {
		t.Fatal("reserve original")
	}
	pool.RemoveProfile("codex", "work")
	pool.AddProfile("codex", "work")
	_, current, ok := pool.reserveRefresh("codex", "work")
	if !ok || current == old {
		t.Fatal("replacement reservation was reused")
	}
	pool.finishRefresh("codex", "work", old, previous, time.Now().Add(time.Hour), true, nil)
	pool.finishRefresh("codex", "work", old, previous, time.Time{}, true, fmt.Errorf("old failure"))
	if p := pool.GetProfile("codex", "work"); !p.inFlight || p.Status != PoolStatusRefreshing || p.ErrorCount != 0 || !p.LastRefresh.IsZero() {
		t.Fatalf("old result changed replacement reservation: %+v", p)
	}
	pool.SetCooldown("codex", "work", time.Hour)
	pool.finishRefresh("codex", "work", current, PoolStatusUnknown, time.Now().Add(time.Hour), true, nil)
	if p := pool.GetProfile("codex", "work"); p.inFlight || p.Status != PoolStatusCooldown || p.LastRefresh.IsZero() {
		t.Fatalf("valid completion lost concurrent cooldown: %+v", p)
	}
}

func TestNewAuthPool(t *testing.T) {
	p := NewAuthPool()
	if p == nil {
		t.Fatal("NewAuthPool() returned nil")
	}

	// Check defaults
	if p.refreshThreshold != 5*time.Minute {
		t.Errorf("refreshThreshold = %v, want 5m", p.refreshThreshold)
	}
	if p.cooldownDuration != 5*time.Minute {
		t.Errorf("cooldownDuration = %v, want 5m", p.cooldownDuration)
	}
	if p.maxRetries != 3 {
		t.Errorf("maxRetries = %d, want 3", p.maxRetries)
	}
}

func TestNewAuthPool_WithOptions(t *testing.T) {
	p := NewAuthPool(
		WithRefreshThreshold(10*time.Minute),
		WithCooldownDuration(15*time.Minute),
		WithMaxRetries(5),
	)

	if p.refreshThreshold != 10*time.Minute {
		t.Errorf("refreshThreshold = %v, want 10m", p.refreshThreshold)
	}
	if p.cooldownDuration != 15*time.Minute {
		t.Errorf("cooldownDuration = %v, want 15m", p.cooldownDuration)
	}
	if p.maxRetries != 5 {
		t.Errorf("maxRetries = %d, want 5", p.maxRetries)
	}
}

func TestAuthPool_AddProfile(t *testing.T) {
	p := NewAuthPool()

	// Add a profile
	profile := p.AddProfile("claude", "test")
	if profile == nil {
		t.Fatal("AddProfile() returned nil")
	}
	if profile.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", profile.Provider)
	}
	if profile.ProfileName != "test" {
		t.Errorf("ProfileName = %q, want test", profile.ProfileName)
	}
	if profile.Status != PoolStatusUnknown {
		t.Errorf("Status = %v, want Unknown", profile.Status)
	}

	// Adding same profile returns existing
	profile2 := p.AddProfile("claude", "test")
	if profile2.Provider != profile.Provider || profile2.ProfileName != profile.ProfileName {
		t.Error("AddProfile should return existing profile for duplicate")
	}

	// Count should be 1
	if p.Count() != 1 {
		t.Errorf("Count() = %d, want 1", p.Count())
	}
}

func TestAuthPool_RemoveProfile(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	if p.Count() != 1 {
		t.Fatalf("Count() = %d, want 1", p.Count())
	}

	p.RemoveProfile("claude", "test")

	if p.Count() != 0 {
		t.Errorf("Count() = %d after remove, want 0", p.Count())
	}

	// Remove non-existent should not panic
	p.RemoveProfile("claude", "nonexistent")
}

func TestAuthPool_GetProfile(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Get existing
	profile := p.GetProfile("claude", "test")
	if profile == nil {
		t.Fatal("GetProfile() returned nil for existing profile")
	}
	if profile.Provider != "claude" {
		t.Errorf("Provider = %q, want claude", profile.Provider)
	}

	// Get non-existent
	profile2 := p.GetProfile("claude", "nonexistent")
	if profile2 != nil {
		t.Errorf("GetProfile() = %v for non-existent, want nil", profile2)
	}
}

func TestAuthPool_GetStatus(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Default status is unknown
	status := p.GetStatus("claude", "test")
	if status != PoolStatusUnknown {
		t.Errorf("GetStatus() = %v, want Unknown", status)
	}

	// Non-existent profile
	status2 := p.GetStatus("claude", "nonexistent")
	if status2 != PoolStatusUnknown {
		t.Errorf("GetStatus() for non-existent = %v, want Unknown", status2)
	}
}

func TestAuthPool_SetStatus(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Set status
	err := p.SetStatus("claude", "test", PoolStatusReady)
	if err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}

	status := p.GetStatus("claude", "test")
	if status != PoolStatusReady {
		t.Errorf("GetStatus() = %v, want Ready", status)
	}

	// Set status on non-existent returns error
	err = p.SetStatus("claude", "nonexistent", PoolStatusReady)
	if err == nil {
		t.Error("SetStatus() on non-existent should return error")
	}
}

func TestAuthPool_SetStatus_ClearsErrorOnReady(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	// Set error first
	p.SetError("claude", "test", errTest("test error"))
	profile := p.GetProfile("claude", "test")
	if profile.ErrorCount == 0 {
		t.Fatal("ErrorCount should be > 0 after SetError")
	}

	// Set status to ready should clear error
	p.SetStatus("claude", "test", PoolStatusReady)
	profile = p.GetProfile("claude", "test")
	if profile.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d after SetStatus(Ready), want 0", profile.ErrorCount)
	}
	if profile.ErrorMessage != "" {
		t.Errorf("ErrorMessage = %q after SetStatus(Ready), want empty", profile.ErrorMessage)
	}
}

func TestAuthPool_TryMarkRefreshing(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	if previous, ok := p.TryMarkRefreshing("claude", "test"); !ok || previous != PoolStatusUnknown {
		t.Fatal("TryMarkRefreshing() = false, want true for first call")
	}

	if status := p.GetStatus("claude", "test"); status != PoolStatusRefreshing {
		t.Fatalf("Status after TryMarkRefreshing() = %v, want Refreshing", status)
	}

	if _, ok := p.TryMarkRefreshing("claude", "test"); ok {
		t.Fatal("TryMarkRefreshing() = true, want false when already refreshing")
	}

	if _, ok := p.TryMarkRefreshing("claude", "missing"); ok {
		t.Fatal("TryMarkRefreshing() = true, want false for missing profile")
	}
}

type errTest string

func (e errTest) Error() string { return string(e) }

func TestAuthPool_SetError(t *testing.T) {
	p := NewAuthPool(WithMaxRetries(3))
	p.AddProfile("claude", "test")

	// First error
	p.SetError("claude", "test", errTest("error 1"))
	profile := p.GetProfile("claude", "test")
	if profile.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", profile.ErrorCount)
	}
	if profile.Status == PoolStatusError {
		t.Error("Status should not be Error after 1 error")
	}

	// Second error
	p.SetError("claude", "test", errTest("error 2"))
	profile = p.GetProfile("claude", "test")
	if profile.ErrorCount != 2 {
		t.Errorf("ErrorCount = %d, want 2", profile.ErrorCount)
	}

	// Third error should trigger error status
	p.SetError("claude", "test", errTest("error 3"))
	profile = p.GetProfile("claude", "test")
	if profile.ErrorCount != 3 {
		t.Errorf("ErrorCount = %d, want 3", profile.ErrorCount)
	}
	if profile.Status != PoolStatusError {
		t.Errorf("Status = %v, want Error after 3 errors", profile.Status)
	}

	// SetError on non-existent should not panic
	p.SetError("claude", "nonexistent", errTest("error"))
}

func TestAuthPool_SetCooldown(t *testing.T) {
	p := NewAuthPool(WithCooldownDuration(5 * time.Minute))
	p.AddProfile("claude", "test")

	// Set cooldown with explicit duration
	p.SetCooldown("claude", "test", 10*time.Minute)
	profile := p.GetProfile("claude", "test")
	if profile.Status != PoolStatusCooldown {
		t.Errorf("Status = %v, want Cooldown", profile.Status)
	}
	if profile.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be set")
	}

	// Clear and test default duration
	p.ClearCooldown("claude", "test")
	p.SetCooldown("claude", "test", 0) // Use default
	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusCooldown {
		t.Errorf("Status = %v, want Cooldown", profile.Status)
	}

	// SetCooldown on non-existent should not panic
	p.SetCooldown("claude", "nonexistent", time.Minute)
}

func TestAuthPool_ClearCooldown(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetCooldown("claude", "test", time.Minute)

	profile := p.GetProfile("claude", "test")
	if profile.Status != PoolStatusCooldown {
		t.Fatal("Profile should be in cooldown")
	}

	p.ClearCooldown("claude", "test")
	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v after ClearCooldown, want Ready", profile.Status)
	}
	if !profile.CooldownUntil.IsZero() {
		t.Error("CooldownUntil should be zero after ClearCooldown")
	}

	// ClearCooldown on non-existent should not panic
	p.ClearCooldown("claude", "nonexistent")
}

func TestAuthPool_UpdateTokenExpiry(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetStatus("claude", "test", PoolStatusReady)

	// Set future expiry
	future := time.Now().Add(time.Hour)
	p.UpdateTokenExpiry("claude", "test", future)
	profile := p.GetProfile("claude", "test")
	if profile.TokenExpiry.IsZero() {
		t.Error("TokenExpiry should be set")
	}

	// Set past expiry should mark as expired
	past := time.Now().Add(-time.Hour)
	p.UpdateTokenExpiry("claude", "test", past)
	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusExpired {
		t.Errorf("Status = %v after past expiry, want Expired", profile.Status)
	}

	// UpdateTokenExpiry on non-existent should not panic
	p.UpdateTokenExpiry("claude", "nonexistent", future)
}

func TestAuthPool_MarkRefreshed(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetError("claude", "test", errTest("error"))
	p.SetError("claude", "test", errTest("error"))

	profile := p.GetProfile("claude", "test")
	if profile.ErrorCount != 2 {
		t.Fatal("ErrorCount should be 2")
	}

	// Mark refreshed
	expiry := time.Now().Add(time.Hour)
	p.MarkRefreshed("claude", "test", expiry)

	profile = p.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v, want Ready", profile.Status)
	}
	if profile.ErrorCount != 0 {
		t.Errorf("ErrorCount = %d, want 0", profile.ErrorCount)
	}
	if profile.LastRefresh.IsZero() {
		t.Error("LastRefresh should be set")
	}

	// MarkRefreshed on non-existent should not panic
	p.MarkRefreshed("claude", "nonexistent", expiry)
}

func TestAuthPool_MarkUsed(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")

	profile := p.GetProfile("claude", "test")
	if !profile.LastUsed.IsZero() {
		t.Error("LastUsed should be zero initially")
	}

	p.MarkUsed("claude", "test")
	profile = p.GetProfile("claude", "test")
	if profile.LastUsed.IsZero() {
		t.Error("LastUsed should be set after MarkUsed")
	}

	// MarkUsed on non-existent should not panic
	p.MarkUsed("claude", "nonexistent")
}

func TestAuthPool_GetReadyProfiles(t *testing.T) {
	p := NewAuthPool()

	// Add profiles with different statuses
	p.AddProfile("claude", "ready1")
	p.SetStatus("claude", "ready1", PoolStatusReady)

	p.AddProfile("claude", "ready2")
	p.SetStatus("claude", "ready2", PoolStatusReady)

	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	p.AddProfile("codex", "ready")
	p.SetStatus("codex", "ready", PoolStatusReady)

	// Get all ready profiles
	ready := p.GetReadyProfiles("")
	if len(ready) != 3 {
		t.Errorf("GetReadyProfiles('') = %d profiles, want 3", len(ready))
	}

	// Get ready profiles for specific provider
	claudeReady := p.GetReadyProfiles("claude")
	if len(claudeReady) != 2 {
		t.Errorf("GetReadyProfiles('claude') = %d profiles, want 2", len(claudeReady))
	}
}

func TestAuthPool_GetReadyProfiles_SortOrder(t *testing.T) {
	p := NewAuthPool()

	// Add profiles with different priorities
	p.AddProfile("claude", "low")
	p.SetStatus("claude", "low", PoolStatusReady)

	p.AddProfile("claude", "high")
	p.SetStatus("claude", "high", PoolStatusReady)

	// Manually set priority (normally done through vault or other means)
	p.mu.Lock()
	p.profiles["claude:high"].Priority = 10
	p.profiles["claude:low"].Priority = 1
	p.mu.Unlock()

	ready := p.GetReadyProfiles("claude")
	if len(ready) != 2 {
		t.Fatalf("GetReadyProfiles() = %d profiles, want 2", len(ready))
	}

	// High priority should be first
	if ready[0].ProfileName != "high" {
		t.Errorf("First profile = %s, want 'high' (higher priority)", ready[0].ProfileName)
	}
}

func TestAuthPool_GetProfilesNeedingRefresh(t *testing.T) {
	p := NewAuthPool(WithRefreshThreshold(10 * time.Minute))

	// Ready profile with distant expiry
	p.AddProfile("claude", "ok")
	p.SetStatus("claude", "ok", PoolStatusReady)
	p.UpdateTokenExpiry("claude", "ok", time.Now().Add(time.Hour))

	// Expired profile
	p.AddProfile("claude", "expired")
	p.SetStatus("claude", "expired", PoolStatusExpired)

	// Error profile
	p.AddProfile("claude", "error")
	p.SetStatus("claude", "error", PoolStatusError)

	// Profile expiring soon
	p.AddProfile("claude", "soon")
	p.SetStatus("claude", "soon", PoolStatusReady)
	p.UpdateTokenExpiry("claude", "soon", time.Now().Add(5*time.Minute))

	needsRefresh := p.GetProfilesNeedingRefresh("claude")
	// Should include: expired, error, soon (expiring within threshold)
	if len(needsRefresh) != 3 {
		t.Errorf("GetProfilesNeedingRefresh() = %d profiles, want 3", len(needsRefresh))
	}
}

func TestAuthPool_GetProfilesInCooldown(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "ready")
	p.SetStatus("claude", "ready", PoolStatusReady)

	p.AddProfile("claude", "cool1")
	p.SetCooldown("claude", "cool1", time.Hour)

	p.AddProfile("claude", "cool2")
	p.SetCooldown("claude", "cool2", time.Hour)

	inCooldown := p.GetProfilesInCooldown("claude")
	if len(inCooldown) != 2 {
		t.Errorf("GetProfilesInCooldown() = %d profiles, want 2", len(inCooldown))
	}
}

func TestAuthPool_GetAllProfiles(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "a")
	p.AddProfile("claude", "b")
	p.AddProfile("codex", "c")

	all := p.GetAllProfiles("")
	if len(all) != 3 {
		t.Errorf("GetAllProfiles('') = %d profiles, want 3", len(all))
	}

	claudeOnly := p.GetAllProfiles("claude")
	if len(claudeOnly) != 2 {
		t.Errorf("GetAllProfiles('claude') = %d profiles, want 2", len(claudeOnly))
	}
}

func TestAuthPool_SelectBest(t *testing.T) {
	p := NewAuthPool()

	// No profiles
	best := p.SelectBest("claude")
	if best != nil {
		t.Errorf("SelectBest() with no profiles = %v, want nil", best)
	}

	// Add non-ready profiles
	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	best = p.SelectBest("claude")
	if best != nil {
		t.Errorf("SelectBest() with no ready profiles = %v, want nil", best)
	}

	// Add ready profile
	p.AddProfile("claude", "ready")
	p.SetStatus("claude", "ready", PoolStatusReady)

	best = p.SelectBest("claude")
	if best == nil {
		t.Fatal("SelectBest() = nil, want profile")
	}
	if best.ProfileName != "ready" {
		t.Errorf("SelectBest() = %s, want 'ready'", best.ProfileName)
	}
}

func TestAuthPool_CountByStatus(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "ready1")
	p.SetStatus("claude", "ready1", PoolStatusReady)

	p.AddProfile("claude", "ready2")
	p.SetStatus("claude", "ready2", PoolStatusReady)

	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	p.AddProfile("claude", "error")
	p.SetStatus("claude", "error", PoolStatusError)

	counts := p.CountByStatus()
	if counts[PoolStatusReady] != 2 {
		t.Errorf("Ready count = %d, want 2", counts[PoolStatusReady])
	}
	if counts[PoolStatusCooldown] != 1 {
		t.Errorf("Cooldown count = %d, want 1", counts[PoolStatusCooldown])
	}
	if counts[PoolStatusError] != 1 {
		t.Errorf("Error count = %d, want 1", counts[PoolStatusError])
	}
}

func TestAuthPool_CheckAndUpdateCooldowns(t *testing.T) {
	p := NewAuthPool()

	// Add profile with expired cooldown
	p.AddProfile("claude", "test")
	p.mu.Lock()
	p.profiles["claude:test"].Status = PoolStatusCooldown
	p.profiles["claude:test"].CooldownUntil = time.Now().Add(-time.Minute) // Already expired
	p.mu.Unlock()

	cleared := p.CheckAndUpdateCooldowns()
	if cleared != 1 {
		t.Errorf("CheckAndUpdateCooldowns() = %d, want 1", cleared)
	}

	profile := p.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v after cooldown cleared, want Ready", profile.Status)
	}
}

func TestAuthPool_CheckAndUpdateCooldowns_FiresCallback(t *testing.T) {
	var callbackCalled bool
	var callbackProfile *PooledProfile
	var callbackOldStatus, callbackNewStatus PoolStatus
	var wg sync.WaitGroup

	wg.Add(1)
	p := NewAuthPool(
		WithOnStateChange(func(profile *PooledProfile, oldStatus, newStatus PoolStatus) {
			callbackCalled = true
			callbackProfile = profile
			callbackOldStatus = oldStatus
			callbackNewStatus = newStatus
			wg.Done()
		}),
	)

	// Add profile with expired cooldown
	p.AddProfile("claude", "test")
	p.mu.Lock()
	p.profiles["claude:test"].Status = PoolStatusCooldown
	p.profiles["claude:test"].CooldownUntil = time.Now().Add(-time.Minute) // Already expired
	p.mu.Unlock()

	cleared := p.CheckAndUpdateCooldowns()
	if cleared != 1 {
		t.Fatalf("CheckAndUpdateCooldowns() = %d, want 1", cleared)
	}

	// Wait for callback (it's async)
	wg.Wait()

	if !callbackCalled {
		t.Error("OnStateChange callback was not called")
	}
	if callbackProfile == nil || callbackProfile.ProfileName != "test" {
		t.Error("callback received wrong profile")
	}
	if callbackOldStatus != PoolStatusCooldown {
		t.Errorf("oldStatus = %v, want Cooldown", callbackOldStatus)
	}
	if callbackNewStatus != PoolStatusReady {
		t.Errorf("newStatus = %v, want Ready", callbackNewStatus)
	}
}

func TestAuthPool_Summary(t *testing.T) {
	p := NewAuthPool()

	p.AddProfile("claude", "ready")
	p.SetStatus("claude", "ready", PoolStatusReady)

	p.AddProfile("claude", "cooldown")
	p.SetCooldown("claude", "cooldown", time.Hour)

	p.AddProfile("codex", "error")
	p.SetStatus("codex", "error", PoolStatusError)

	summary := p.Summary()
	if summary.TotalProfiles != 3 {
		t.Errorf("TotalProfiles = %d, want 3", summary.TotalProfiles)
	}
	if summary.ReadyCount != 1 {
		t.Errorf("ReadyCount = %d, want 1", summary.ReadyCount)
	}
	if summary.CooldownCount != 1 {
		t.Errorf("CooldownCount = %d, want 1", summary.CooldownCount)
	}
	if summary.ErrorCount != 1 {
		t.Errorf("ErrorCount = %d, want 1", summary.ErrorCount)
	}
	if summary.ByProvider["claude"] != 2 {
		t.Errorf("ByProvider[claude] = %d, want 2", summary.ByProvider["claude"])
	}
	if summary.ByProvider["codex"] != 1 {
		t.Errorf("ByProvider[codex] = %d, want 1", summary.ByProvider["codex"])
	}
}

func TestAuthPool_OnStateChangeCallback(t *testing.T) {
	var callbackCalled bool
	var callbackProfile *PooledProfile
	var callbackOldStatus, callbackNewStatus PoolStatus
	var wg sync.WaitGroup

	wg.Add(1)
	p := NewAuthPool(
		WithOnStateChange(func(profile *PooledProfile, oldStatus, newStatus PoolStatus) {
			callbackCalled = true
			callbackProfile = profile
			callbackOldStatus = oldStatus
			callbackNewStatus = newStatus
			wg.Done()
		}),
	)

	p.AddProfile("claude", "test")
	p.SetStatus("claude", "test", PoolStatusReady)

	// Wait for callback (it's async)
	wg.Wait()

	if !callbackCalled {
		t.Error("OnStateChange callback was not called")
	}
	if callbackProfile == nil || callbackProfile.ProfileName != "test" {
		t.Error("callback received wrong profile")
	}
	if callbackOldStatus != PoolStatusUnknown {
		t.Errorf("oldStatus = %v, want Unknown", callbackOldStatus)
	}
	if callbackNewStatus != PoolStatusReady {
		t.Errorf("newStatus = %v, want Ready", callbackNewStatus)
	}
}

func TestAuthPool_Concurrency(t *testing.T) {
	p := NewAuthPool()
	p.AddProfile("claude", "test")
	p.SetStatus("claude", "test", PoolStatusReady)

	var wg sync.WaitGroup
	const goroutines = 100

	// Concurrent reads
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.GetProfile("claude", "test")
			p.GetStatus("claude", "test")
			p.GetReadyProfiles("claude")
			p.Count()
		}()
	}

	// Concurrent writes
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			p.MarkUsed("claude", "test")
			if n%2 == 0 {
				p.SetStatus("claude", "test", PoolStatusReady)
			}
		}(i)
	}

	wg.Wait()
}

// TestPooledProfile tests the PooledProfile methods.
func TestPooledProfile_Key(t *testing.T) {
	p := &PooledProfile{
		Provider:    "claude",
		ProfileName: "test",
	}
	if p.Key() != "claude:test" {
		t.Errorf("Key() = %q, want 'claude:test'", p.Key())
	}
}

func TestPooledProfile_IsExpired(t *testing.T) {
	tests := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero", time.Time{}, false},
		{"future", time.Now().Add(time.Hour), false},
		{"past", time.Now().Add(-time.Hour), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PooledProfile{TokenExpiry: tc.expiry}
			if got := p.IsExpired(); got != tc.want {
				t.Errorf("IsExpired() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPooledProfile_IsExpiringSoon(t *testing.T) {
	threshold := 10 * time.Minute

	tests := []struct {
		name   string
		expiry time.Time
		want   bool
	}{
		{"zero", time.Time{}, false},
		{"far future", time.Now().Add(time.Hour), false},
		{"within threshold", time.Now().Add(5 * time.Minute), true},
		{"past", time.Now().Add(-time.Minute), true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PooledProfile{TokenExpiry: tc.expiry}
			if got := p.IsExpiringSoon(threshold); got != tc.want {
				t.Errorf("IsExpiringSoon(%v) = %v, want %v", threshold, got, tc.want)
			}
		})
	}
}

func TestPooledProfile_IsInCooldown(t *testing.T) {
	tests := []struct {
		name  string
		until time.Time
		want  bool
	}{
		{"zero", time.Time{}, false},
		{"future", time.Now().Add(time.Hour), true},
		{"past", time.Now().Add(-time.Hour), false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &PooledProfile{CooldownUntil: tc.until}
			if got := p.IsInCooldown(); got != tc.want {
				t.Errorf("IsInCooldown() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPooledProfile_TimeUntilExpiry(t *testing.T) {
	p := &PooledProfile{}

	// Zero expiry
	if p.TimeUntilExpiry() != 0 {
		t.Error("TimeUntilExpiry() should be 0 for zero expiry")
	}

	// Future expiry
	p.TokenExpiry = time.Now().Add(time.Hour)
	ttl := p.TimeUntilExpiry()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Errorf("TimeUntilExpiry() = %v, expected ~1h", ttl)
	}

	// Past expiry
	p.TokenExpiry = time.Now().Add(-time.Hour)
	if p.TimeUntilExpiry() != 0 {
		t.Error("TimeUntilExpiry() should be 0 for past expiry")
	}
}

func TestPooledProfile_TimeInCooldown(t *testing.T) {
	p := &PooledProfile{}

	// Zero cooldown
	if p.TimeInCooldown() != 0 {
		t.Error("TimeInCooldown() should be 0 for zero cooldown")
	}

	// Future cooldown
	p.CooldownUntil = time.Now().Add(time.Hour)
	ttl := p.TimeInCooldown()
	if ttl < 59*time.Minute || ttl > time.Hour {
		t.Errorf("TimeInCooldown() = %v, expected ~1h", ttl)
	}

	// Past cooldown
	p.CooldownUntil = time.Now().Add(-time.Hour)
	if p.TimeInCooldown() != 0 {
		t.Error("TimeInCooldown() should be 0 for past cooldown")
	}
}

func TestPooledProfile_Clone(t *testing.T) {
	original := &PooledProfile{
		Provider:     "claude",
		ProfileName:  "test",
		Status:       PoolStatusReady,
		TokenExpiry:  time.Now().Add(time.Hour),
		ErrorCount:   2,
		ErrorMessage: "test error",
		Priority:     5,
	}

	clone := original.Clone()
	if clone == original {
		t.Error("Clone() should return a different pointer")
	}
	if clone.Provider != original.Provider {
		t.Error("Clone() Provider mismatch")
	}
	if clone.ProfileName != original.ProfileName {
		t.Error("Clone() ProfileName mismatch")
	}
	if clone.Status != original.Status {
		t.Error("Clone() Status mismatch")
	}
	if clone.ErrorCount != original.ErrorCount {
		t.Error("Clone() ErrorCount mismatch")
	}
	if clone.Priority != original.Priority {
		t.Error("Clone() Priority mismatch")
	}

	// Test nil clone
	var nilProfile *PooledProfile
	if nilProfile.Clone() != nil {
		t.Error("Clone() of nil should return nil")
	}
}

func TestPoolStatus_String(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   string
	}{
		{PoolStatusUnknown, "unknown"},
		{PoolStatusReady, "ready"},
		{PoolStatusRefreshing, "refreshing"},
		{PoolStatusExpired, "expired"},
		{PoolStatusCooldown, "cooldown"},
		{PoolStatusError, "error"},
		{PoolStatus(99), "unknown"},
	}

	for _, tc := range tests {
		if got := tc.status.String(); got != tc.want {
			t.Errorf("PoolStatus(%d).String() = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestPoolStatus_IsUsable(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   bool
	}{
		{PoolStatusUnknown, false},
		{PoolStatusReady, true},
		{PoolStatusRefreshing, false},
		{PoolStatusExpired, false},
		{PoolStatusCooldown, false},
		{PoolStatusError, false},
	}

	for _, tc := range tests {
		if got := tc.status.IsUsable(); got != tc.want {
			t.Errorf("PoolStatus(%v).IsUsable() = %v, want %v", tc.status, got, tc.want)
		}
	}
}

func TestPoolStatus_NeedsRefresh(t *testing.T) {
	tests := []struct {
		status PoolStatus
		want   bool
	}{
		{PoolStatusUnknown, false},
		{PoolStatusReady, false},
		{PoolStatusRefreshing, false},
		{PoolStatusExpired, true},
		{PoolStatusCooldown, false},
		{PoolStatusError, true},
	}

	for _, tc := range tests {
		if got := tc.status.NeedsRefresh(); got != tc.want {
			t.Errorf("PoolStatus(%v).NeedsRefresh() = %v, want %v", tc.status, got, tc.want)
		}
	}
}
