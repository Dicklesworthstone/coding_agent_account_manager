package cmd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

func TestPoolRefreshAllWaitsForEveryOutcome(t *testing.T) {
	for _, partialFailure := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_failure=%t", partialFailure), func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			v := authfile.NewVault(authfile.DefaultVaultPath())
			for _, name := range []string{"a", "b"} {
				path := filepath.Join(v.ProfilePath("codex", name), "auth.json")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				claims := fmt.Sprintf(`{"sub":"synthetic-%s","exp":%d}`, name, time.Now().Add(8*time.Hour).Unix())
				jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
				body := fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":%q}}`, jwt, "synthetic-"+name)
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			cursorPath := filepath.Join(v.ProfilePath("cursor", "native"), "auth.json")
			if err := os.MkdirAll(filepath.Dir(cursorPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cursorPath, []byte(`{"accessToken":"synthetic-session"}`), 0600); err != nil {
				t.Fatal(err)
			}
			entered := make(chan string, 2)
			release := make(chan struct{})
			wantErr := errors.New("synthetic endpoint unavailable")
			original := refresh.RefreshCodexToken
			refresh.RefreshCodexToken = func(ctx context.Context, token string) (*refresh.TokenResponse, error) {
				entered <- token
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				if partialFailure && token == "synthetic-b" {
					return nil, wantErr
				}
				return &refresh.TokenResponse{AccessToken: "synthetic-fresh-access", RefreshToken: "synthetic-fresh-" + token, ExpiresIn: 3600}, nil
			}
			t.Cleanup(func() { refresh.RefreshCodexToken = original })
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := &cobra.Command{}
			cmd.SetContext(ctx)
			cmd.Flags().Bool("all", true, "")
			cmd.Flags().Duration("timeout", 5*time.Second, "")
			var output bytes.Buffer
			cmd.SetOut(&output)
			done := make(chan error, 1)
			go func() { done <- runPoolRefresh(cmd, nil) }()
			for range 2 {
				select {
				case <-entered:
				case <-time.After(3 * time.Second):
					cancel()
					<-done
					t.Fatal("--all did not begin both unexpired renewable grants")
				}
			}
			select {
			case err := <-done:
				close(release)
				t.Fatalf("CLI returned before renewal completed: %v", err)
			default:
			}
			close(release)
			select {
			case err := <-done:
				if (err != nil) != partialFailure || (partialFailure && !errors.Is(err, wantErr)) {
					t.Fatalf("batch error=%v, partial failure=%t", err, partialFailure)
				}
			case <-time.After(3 * time.Second):
				cancel()
				<-done
				t.Fatal("CLI did not join released renewals")
			}
			for _, want := range []string{"codex/a: refreshed", "cursor/native: skipped"} {
				if !strings.Contains(output.String(), want) {
					t.Errorf("missing %q in completed output: %s", want, output.String())
				}
			}
			want := "codex/b: refreshed"
			if partialFailure {
				want = "codex/b: error"
			}
			if !strings.Contains(output.String(), want) {
				t.Errorf("missing %q in completed output: %s", want, output.String())
			}
			body, err := os.ReadFile(filepath.Join(v.ProfilePath("codex", "a"), "auth.json"))
			if err != nil || !bytes.Contains(body, []byte("synthetic-fresh-access")) {
				t.Fatalf("CLI exited before credential publication: %v", err)
			}
		})
	}
}

func TestPoolRefreshRejectsInvalidCredentialsWithoutExchange(t *testing.T) {
	for _, kind := range []string{"malformed", "missing access", "provider rejected"} {
		for _, all := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/all=%t", kind, all), func(t *testing.T) {
				t.Setenv("CAAM_HOME", t.TempDir())
				t.Setenv("CODEX_HOME", t.TempDir())
				v := authfile.NewVault(authfile.DefaultVaultPath())
				path := filepath.Join(v.ProfilePath("codex", "broken"), "auth.json")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				body := `{`
				if kind == "missing access" {
					body = `{"tokens":{"refresh_token":"synthetic-refresh"}}`
				} else if kind == "provider rejected" {
					claims := fmt.Sprintf(`{"sub":"synthetic-account","exp":%d}`, time.Now().Add(time.Hour).Unix())
					jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
					body = fmt.Sprintf(`{"tokens":{"access_token":%q,"refresh_token":"synthetic-refresh"}}`, jwt)
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "provider rejected" {
					store := health.NewStorage(health.DefaultHealthPath())
					store.SetVaultPath(v.BasePath())
					h, err := store.GetProfile("codex", "broken")
					if err != nil || h == nil {
						t.Fatalf("read current credentials: %+v %v", h, err)
					}
					if err := store.RecordProviderVerification("codex", "broken", health.ProviderVerification{Reason: "refresh_token_invalidated", Fingerprint: h.CredentialFingerprint}); err != nil {
						t.Fatal(err)
					}
				}
				var calls atomic.Int32
				original := refresh.RefreshCodexToken
				refresh.RefreshCodexToken = func(context.Context, string) (*refresh.TokenResponse, error) {
					calls.Add(1)
					return nil, errors.New("invalid source must not reach token exchange")
				}
				t.Cleanup(func() { refresh.RefreshCodexToken = original })
				cmd := &cobra.Command{}
				cmd.SetContext(context.Background())
				cmd.Flags().Bool("all", all, "")
				cmd.Flags().Duration("timeout", time.Second, "")
				var output bytes.Buffer
				cmd.SetOut(&output)
				args := []string{"codex/broken"}
				if all {
					args = nil
				}
				err := runPoolRefresh(cmd, args)
				if err == nil || refresh.IsSkipped(err) || calls.Load() != 0 {
					t.Fatalf("invalid credential reported as success/skip or exchanged: err=%v calls=%d", err, calls.Load())
				}
				if !strings.Contains(output.String(), "codex/broken: error:") || strings.Contains(output.String(), ": skipped:") {
					t.Fatalf("invalid credential outcome is misleading: %s", output.String())
				}
				after, readErr := os.ReadFile(path)
				if readErr != nil || string(after) != body {
					t.Fatalf("rejected credential source changed: %v", readErr)
				}
			})
		}
	}
}

func TestRefreshCLIReportsPreflightSkips(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	claims, err := json.Marshal(map[string]any{
		"sub": "SYNTHETIC-ACCOUNT", "email": "work@example.com", "exp": now.Add(2 * time.Minute).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	jwt := "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".SYNTHETIC"
	codexAuth := func(refreshed time.Time, token string) string {
		return fmt.Sprintf(`{"tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q},"last_refresh":%q}`,
			jwt, jwt, token, refreshed.Format(time.RFC3339))
	}
	for _, tc := range []struct {
		name     string
		provider string
		filename string
		body     string
		live     string
		reason   string
	}{
		{"stale Codex vault", "codex", "auth.json", codexAuth(now.Add(-time.Hour), "SYNTHETIC-OLD-REFRESH"), codexAuth(now, "SYNTHETIC-LIVE-REFRESH"), "caam backup codex work"},
		{"Claude native renewal", "claude", ".credentials.json", `{"claudeAiOauth":{"accessToken":"SYNTHETIC-ACCESS","refreshToken":"SYNTHETIC-REFRESH"}}`, "", "Claude Code handles refresh internally"},
		{"Grok native renewal", "grok", "auth.json", `{"refresh_token":"SYNTHETIC-REFRESH"}`, "", "Grok Build handles token renewal"},
		{"OpenCode unsupported", "opencode", "auth.json", `{"refresh":"SYNTHETIC-REFRESH"}`, "", "use OpenCode to authenticate"},
		{"Cursor session", "cursor", "auth.json", `{"accessToken":"SYNTHETIC-SESSION"}`, "", "caam login cursor work"},
		{"Gemini missing client configuration", "gemini", "settings.json", `{"refresh_token":"SYNTHETIC-REFRESH"}`, "", "missing oauth client credentials"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			oldVault, oldHealthStore := vault, healthStore
			vault = authfile.NewVault(filepath.Join(root, "vault"))
			healthStore = health.NewStorage(filepath.Join(root, "health.json"))
			t.Cleanup(func() { vault, healthStore = oldVault, oldHealthStore })
			dir := vault.ProfilePath(tc.provider, "work")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, tc.filename)
			if err := os.WriteFile(path, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			liveDir := filepath.Join(root, "live")
			t.Setenv("CODEX_HOME", liveDir)
			if tc.live != "" {
				if err := os.MkdirAll(liveDir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(liveDir, "auth.json"), []byte(tc.live), 0600); err != nil {
					t.Fatal(err)
				}
			}

			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				posts.Add(1)
				w.WriteHeader(http.StatusInternalServerError)
			}))
			t.Cleanup(server.Close)
			oldCodexURL, oldGeminiURL := refresh.CodexTokenURL, refresh.GeminiTokenURL
			refresh.CodexTokenURL, refresh.GeminiTokenURL = server.URL, server.URL
			t.Cleanup(func() { refresh.CodexTokenURL, refresh.GeminiTokenURL = oldCodexURL, oldGeminiURL })

			for _, force := range []bool{false, true} {
				should, reason, err := shouldRefreshProfile(tc.provider, "work", 10*time.Minute, force)
				if err != nil || should || !strings.Contains(reason, tc.reason) {
					t.Fatalf("eligibility (force=%v) = %v, %q, %v; want skip with %q", force, should, reason, err, tc.reason)
				}
				for _, dryRun := range []bool{false, true} {
					output, err := captureStdout(t, func() error {
						return refreshSingle(context.Background(), tc.provider, "work", 10*time.Minute, dryRun, force, false)
					})
					if err != nil || !strings.Contains(output, tc.reason) || strings.Contains(output, "Refreshing ") || strings.Contains(output, "would be refreshed") {
						t.Errorf("single refresh (force=%v, dry=%v) = %q, %v; want skip before announcement", force, dryRun, output, err)
					}
					output, err = captureStdout(t, func() error {
						refreshed, skipped, failed, err := refreshTool(context.Background(), tc.provider, 10*time.Minute, dryRun, force, false)
						if refreshed != 0 || skipped != 1 || failed != 0 {
							t.Errorf("batch counts = %d refreshed, %d skipped, %d failed; want 0,1,0", refreshed, skipped, failed)
						}
						return err
					})
					if err != nil || !strings.Contains(output, tc.reason) || strings.Contains(output, "Refreshing ") || strings.Contains(output, "would refresh") {
						t.Errorf("batch refresh (force=%v, dry=%v) = %q, %v; want skip before announcement", force, dryRun, output, err)
					}
				}
			}
			if posts.Load() != 0 {
				t.Fatalf("skipped CLI actions made %d token endpoint requests", posts.Load())
			}
			if data, err := os.ReadFile(path); err != nil || !bytes.Equal(data, []byte(tc.body)) {
				t.Errorf("skipped CLI actions changed the vault: %v", err)
			}
			if tc.live != "" {
				if data, err := os.ReadFile(filepath.Join(liveDir, "auth.json")); err != nil || !bytes.Equal(data, []byte(tc.live)) {
					t.Errorf("skipped CLI actions changed the live credential: %v", err)
				}
			}
		})
	}
}

func TestRefreshSingle_CodexUpdatesAuth(t *testing.T) {
	tmpDir := t.TempDir()

	// Keep SPM config reads inside tmpDir.
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", tmpDir)

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("codex", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	original := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    time.Now().Add(2 * time.Minute).Unix(),
		"token_type":    "Bearer",
	}
	raw, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	authPath := filepath.Join(profileDir, "auth.json")
	if err := os.WriteFile(authPath, raw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer ts.Close()

	oldTokenURL := refresh.CodexTokenURL
	refresh.CodexTokenURL = ts.URL
	t.Cleanup(func() { refresh.CodexTokenURL = oldTokenURL })

	if err := refreshSingle(context.Background(), "codex", "main", 10*time.Minute, false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	if got := updated["access_token"]; got != "new-access" {
		t.Fatalf("access_token = %v, want %v", got, "new-access")
	}
	if got := updated["refresh_token"]; got != "new-refresh" {
		t.Fatalf("refresh_token = %v, want %v", got, "new-refresh")
	}
	if got, ok := updated["expires_at"].(float64); !ok || got <= float64(original["expires_at"].(int64)) {
		t.Fatalf("expires_at not updated: %v", updated["expires_at"])
	}
}

func TestRefreshSingle_SkipsWhenNotExpiring(t *testing.T) {
	tmpDir := t.TempDir()

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("codex", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	original := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    time.Now().Add(2 * time.Hour).Unix(),
		"token_type":    "Bearer",
	}
	raw, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	authPath := filepath.Join(profileDir, "auth.json")
	if err := os.WriteFile(authPath, raw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := refreshSingle(context.Background(), "codex", "main", 10*time.Minute, false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(updatedRaw) != string(raw) {
		t.Fatalf("auth.json changed but should have been skipped")
	}
}

func TestRefreshSingle_SkipsWhenUnsupported(t *testing.T) {
	tmpDir := t.TempDir()

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	profileDir := vault.ProfilePath("gemini", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	original := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expiry":        time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
		"token_type":    "Bearer",
	}
	raw, err := json.MarshalIndent(original, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}

	settingsPath := filepath.Join(profileDir, "settings.json")
	if err := os.WriteFile(settingsPath, raw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// No oauth_creds.json present, so refresh should be treated as unsupported and skipped.
	if err := refreshSingle(context.Background(), "gemini", "main", 10*time.Minute, false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	updatedRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := updated["access_token"]; got != "old-access" {
		t.Fatalf("access_token = %v, want %v", got, "old-access")
	}
}

func TestRefreshSingle_GeminiUpdatesSelectedOAuthSource(t *testing.T) {
	tmpDir := t.TempDir()

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	oldHealthStore := healthStore
	healthStore = health.NewStorage(filepath.Join(tmpDir, "health.json"))
	t.Cleanup(func() { healthStore = oldHealthStore })

	profileDir := vault.ProfilePath("gemini", "main")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	settings := map[string]any{
		"access_token":  "old-access",
		"refresh_token": "ignored-refresh-token",
		"expiry":        time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
		"token_type":    "Bearer",
	}
	settingsRaw, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	settingsPath := filepath.Join(profileDir, "settings.json")
	if err := os.WriteFile(settingsPath, settingsRaw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	adc := map[string]any{
		"client_id":     "test-client",
		"client_secret": "test-secret",
		"refresh_token": "test-refresh",
		"access_token":  "old-selected-access",
		"expiry":        time.Now().Add(2 * time.Minute).UTC().Format(time.RFC3339),
		"type":          "authorized_user",
	}
	adcRaw, err := json.MarshalIndent(adc, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	oauthCredPath := filepath.Join(profileDir, "oauth_creds.json")
	if err := os.WriteFile(oauthCredPath, adcRaw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	var calls atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse refresh request: %v", err)
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		if r.Form.Get("refresh_token") != "test-refresh" || r.Form.Get("client_id") != "test-client" {
			t.Error("refresh request did not use the selected OAuth grant")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"new-access","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer ts.Close()

	oldTokenURL := refresh.GeminiTokenURL
	refresh.GeminiTokenURL = ts.URL
	t.Cleanup(func() { refresh.GeminiTokenURL = oldTokenURL })

	if err := refreshSingle(context.Background(), "gemini", "main", 10*time.Minute, false, false, true); err != nil {
		t.Fatalf("refreshSingle() error = %v", err)
	}

	if calls.Load() != 1 {
		t.Fatalf("refresh requests = %d, want 1", calls.Load())
	}
	unchangedSettings, err := os.ReadFile(settingsPath)
	if err != nil || !bytes.Equal(unchangedSettings, settingsRaw) {
		t.Fatalf("refresh changed unrelated settings: %v", err)
	}
	updatedRaw, err := os.ReadFile(oauthCredPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var updated map[string]any
	if err := json.Unmarshal(updatedRaw, &updated); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := updated["access_token"]; got != "new-access" {
		t.Fatalf("access_token = %v, want %v", got, "new-access")
	}
	if got := updated["refresh_token"]; got != "test-refresh" {
		t.Fatalf("refresh_token = %v, want original selected grant", got)
	}
	expiryText, ok := updated["expiry"].(string)
	if !ok {
		t.Fatal("selected OAuth source has no expiry string")
	}
	expiry, err := time.Parse(time.RFC3339, expiryText)
	if err != nil || time.Until(expiry) < 59*time.Minute || time.Until(expiry) > time.Hour {
		t.Fatalf("refreshed expiry = %q, want about one hour: %v", expiryText, err)
	}
	h, err := healthStore.GetProfile("gemini", "main")
	if err != nil || h == nil || !h.TokenRenewable || !h.TokenExpiresAt.Equal(expiry) {
		t.Fatalf("health did not resolve the refreshed grant: %+v, %v", h, err)
	}
	if got, want := refreshedTTL("gemini", "main"), health.FormatTimeRemaining(expiry); got != want {
		t.Fatalf("refreshed TTL = %q, want %q", got, want)
	}
}

// TestRefreshSingle_ClaudeReturnsUnsupported verifies that Claude refresh is correctly
// disabled and returns a graceful skip (not an error) when attempted.
// This is a regression test for caam-tfzr.1 / CLAUDE-006.
// See: docs/CLAUDE_AUTH_INVENTORY.md
func TestRefreshSingle_ClaudeReturnsUnsupported(t *testing.T) {
	tmpDir := t.TempDir()

	// Setup temp CAAM_HOME to avoid affecting real config
	oldCaamHome := os.Getenv("CAAM_HOME")
	t.Cleanup(func() { _ = os.Setenv("CAAM_HOME", oldCaamHome) })
	_ = os.Setenv("CAAM_HOME", tmpDir)

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	oldHealthStore := healthStore
	healthStore = health.NewStorage(filepath.Join(tmpDir, "health.json"))
	t.Cleanup(func() { healthStore = oldHealthStore })

	profileDir := vault.ProfilePath("claude", "test-profile")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	// Create a Claude credentials file with short expiry to trigger refresh attempt
	credentials := map[string]any{
		"claudeAiOauth": map[string]any{
			"accessToken":      "sk-ant-oat01-test-opaque-token",
			"refreshToken":     "sk-ant-ort01-test-refresh-token",
			"expiresAt":        time.Now().Add(2 * time.Minute).UnixMilli(),
			"subscriptionType": "claude_pro_2025",
		},
	}
	credRaw, err := json.MarshalIndent(credentials, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent() error = %v", err)
	}
	credPath := filepath.Join(profileDir, ".credentials.json")
	if err := os.WriteFile(credPath, credRaw, 0600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	// Attempt refresh - should return nil (graceful skip) not an error
	// because Claude refresh is disabled and handled via ErrUnsupported
	err = refreshSingle(context.Background(), "claude", "test-profile", 10*time.Minute, false, false, true)

	// Claude refresh should NOT return an error - it's skipped gracefully
	if err != nil {
		t.Fatalf("refreshSingle() should return nil for unsupported Claude refresh, got error = %v", err)
	}

	// Verify the file wasn't modified (no refresh occurred)
	afterRaw, err := os.ReadFile(credPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var afterCreds map[string]any
	if err := json.Unmarshal(afterRaw, &afterCreds); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}

	oauth, ok := afterCreds["claudeAiOauth"].(map[string]any)
	if !ok {
		t.Fatal("claudeAiOauth not found after refresh attempt")
	}

	// Access token should be unchanged (no refresh occurred)
	if got := oauth["accessToken"]; got != "sk-ant-oat01-test-opaque-token" {
		t.Errorf("accessToken was modified unexpectedly: got %v", got)
	}
}
