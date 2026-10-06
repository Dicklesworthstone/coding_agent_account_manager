package health

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func writeHealthVaultJSON(t *testing.T, root, provider, name, file string, value any) string {
	t.Helper()
	path := filepath.Join(root, provider, name, file)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestStorageCurrentVaultRenewalAcrossProviders(t *testing.T) {
	expiry := time.Now().Add(-time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		provider, file string
		credential     any
		selfRefreshing bool
	}{
		{"claude", ".credentials.json", map[string]any{"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expiry.UnixMilli()}}, true},
		{"codex", "auth.json", map[string]any{"tokens": map[string]any{"access_token": unsignedJWT(t, map[string]any{"exp": expiry.Unix()}), "refresh_token": "synthetic-refresh"}}, false},
		{"gemini", "oauth_creds.json", map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_at": expiry.Unix()}, false},
		{"grok", "auth.json", map[string]any{"https://auth.x.ai::synthetic": map[string]any{"key": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_at": expiry.Format(time.RFC3339)}}, false},
		{"cursor", "auth.json", map[string]any{"accessToken": unsignedJWT(t, map[string]any{"exp": expiry.Unix()}), "apiKey": "synthetic-key"}, true},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			root := t.TempDir()
			vault := filepath.Join(root, "vault")
			path := writeHealthVaultJSON(t, vault, tc.provider, "work", tc.file, tc.credential)
			before, _ := os.ReadFile(path)
			s := NewStorage(filepath.Join(root, "health.json"))
			if err := s.UpdateProfile(tc.provider, "work", &ProfileHealth{TokenExpiresAt: expiry.Add(-time.Hour), PlanType: "max", ErrorCount1h: 1}); err != nil {
				t.Fatal(err)
			}
			healthBefore, _ := os.ReadFile(s.Path())
			h, err := NewStorage(s.Path()).GetProfile(tc.provider, "work")
			if err != nil || h == nil {
				t.Fatalf("GetProfile: %v, %v", h, err)
			}
			if !h.TokenExpiresAt.Equal(expiry) || !h.TokenRenewable || h.SelfRefreshing != tc.selfRefreshing || h.CredentialFingerprint == "" {
				t.Fatalf("current credential semantics lost: %+v", h)
			}
			if h.PlanType != "max" || h.ErrorCount1h != 1 {
				t.Fatalf("stored health metadata lost: %+v", h)
			}
			if signals := CredentialSignals(h, DefaultHealthConfig()); signals.LoginRequired == nil || *signals.LoginRequired {
				t.Fatalf("renewable access expiry requires a login: %+v", signals)
			}
			all, err := s.ListProfiles()
			if err != nil || !all[tc.provider+"/work"].TokenRenewable {
				t.Fatalf("ListProfiles lost renewal: %+v, %v", all, err)
			}
			after, _ := os.ReadFile(path)
			healthAfter, _ := os.ReadFile(s.Path())
			if !bytes.Equal(before, after) || !bytes.Equal(healthBefore, healthAfter) {
				t.Fatal("report read modified credentials or persisted health")
			}
		})
	}
}

func TestStorageCurrentCursorCustomVaultAndReplacement(t *testing.T) {
	root := t.TempDir()
	s := NewStorage(filepath.Join(root, "health.json"))
	custom := t.TempDir()
	expiry := time.Now().Add(6 * 24 * time.Hour).Truncate(time.Second)
	token := unsignedJWT(t, map[string]any{"exp": expiry.Unix(), "nonce": "synthetic-session"})
	path := writeHealthVaultJSON(t, custom, "cursor", "work", "auth.json", map[string]string{"accessToken": token, "refreshToken": token})
	writeHealthVaultJSON(t, filepath.Join(root, "vault"), "cursor", "work", "auth.json", map[string]string{"apiKey": "synthetic-default-key"})
	s.SetVaultPath(custom)
	h, err := s.GetProfile("cursor", "work")
	if err != nil || h == nil || h.TokenRenewable || h.SelfRefreshing || !h.TokenExpiresAt.Equal(expiry) || h.ReloginWarningLead != CursorReloginLead {
		t.Fatalf("custom vault session not resolved: %+v, %v", h, err)
	}
	if status, err := s.GetStatus("cursor", "work"); err != nil || status != StatusWarning {
		t.Fatalf("missing long-lead warning: %v, %v", status, err)
	}
	if err := s.RecordProviderVerification("cursor", "work", ProviderVerification{Reason: "access_token_rejected", Fingerprint: h.CredentialFingerprint}); err != nil {
		t.Fatal(err)
	}
	// Optional Cursor configuration must not clear a rejection of this login.
	writeHealthVaultJSON(t, custom, "cursor", "work", "cli-config.json", map[string]string{"theme": "synthetic-dark"})
	h, _ = s.GetProfile("cursor", "work")
	if !h.ProviderRejected() {
		t.Fatal("unrelated config churn cleared provider rejection")
	}
	// Atomic replacement at the same path must be seen without rebuilding Storage.
	next := writeHealthVaultJSON(t, custom, "cursor", "work", "replacement.json", map[string]string{"apiKey": "synthetic-replacement-key"})
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
	h, _ = s.GetProfile("cursor", "work")
	if !h.TokenRenewable || !h.SelfRefreshing || !h.TokenExpiresAt.IsZero() || h.ReloginWarningLead != 0 || h.ProviderRejected() {
		t.Fatalf("replaced session retained stale facts: %+v", h)
	}
	s.SetVaultPath("")
	defaultHealth, _ := s.GetProfile("cursor", "work")
	if defaultHealth.CredentialFingerprint == h.CredentialFingerprint || !defaultHealth.TokenRenewable {
		t.Fatalf("empty binding did not restore sibling vault: %+v", defaultHealth)
	}
}

func TestStorageCurrentVaultInvalidSourcePreservesRejection(t *testing.T) {
	for _, body := range []string{"", "{", `{}`, `{"accessToken":null}`, `{"expiresAt":2000000000}`} {
		t.Run(body, func(t *testing.T) {
			root := t.TempDir()
			s := NewStorage(filepath.Join(root, "health.json"))
			h := &ProfileHealth{TokenExpiresAt: time.Now().Add(-time.Hour), ProviderRejectedAt: time.Now(), ProviderRejection: "rejected", RejectedFingerprint: "old", PlanType: "pro"}
			if err := s.UpdateProfile("cursor", "work", h); err != nil {
				t.Fatal(err)
			}
			if body != "" {
				path := writeHealthVaultJSON(t, filepath.Join(root, "vault"), "cursor", "work", "auth.json", nil)
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, err := s.GetProfile("cursor", "work")
			if err != nil || !got.TokenExpiresAt.IsZero() || got.CredentialRenewable() || got.CredentialFingerprint != "" || got.ReloginWarningLead != 0 || !got.ProviderRejected() || got.PlanType != "pro" {
				t.Fatalf("invalid source retained expiry or erased rejection: %+v, %v", got, err)
			}
		})
	}
}

func TestStorageCurrentVaultOnlyProfilesAndBoundary(t *testing.T) {
	root := t.TempDir()
	s := NewStorage(filepath.Join(root, "health.json"))
	now := time.Now().Truncate(time.Second)
	writeHealthVaultJSON(t, filepath.Join(root, "vault"), "cursor", "work", "auth.json", map[string]string{"accessToken": unsignedJWT(t, map[string]any{"exp": now.Unix()})})
	writeHealthVaultJSON(t, filepath.Join(root, "vault"), "cursor", "_original", "auth.json", map[string]string{"apiKey": "synthetic-key"})
	writeHealthVaultJSON(t, filepath.Join(root, "vault"), "unrecognized", "work", "auth.json", map[string]string{"apiKey": "synthetic-key"})
	all, err := s.ListProfiles()
	if err != nil || len(all) != 1 || all["cursor/work"] == nil {
		t.Fatalf("vault-only discovery = %+v, %v", all, err)
	}
	h := all["cursor/work"]
	for _, delta := range []time.Duration{-time.Second, 0, time.Second} {
		signals := credentialSignalsAt(h, DefaultHealthConfig(), now.Add(delta))
		if signals.LoginRequired == nil || *signals.LoginRequired != (delta >= 0) {
			t.Fatalf("login-required boundary delta=%v: %+v", delta, signals)
		}
	}
	if _, err := os.Stat(s.Path()); !os.IsNotExist(err) {
		t.Fatalf("read created a health file: %v", err)
	}
}

func TestStorageRemovedSourceDoesNotReuseCachedExpiry(t *testing.T) {
	root := t.TempDir()
	s := NewStorage(filepath.Join(root, "health.json"))
	expiry := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := s.UpdateProfile("claude", "work", &ProfileHealth{TokenExpiresAt: expiry, PlanType: "max"}); err != nil {
		t.Fatal(err)
	}
	path := writeHealthVaultJSON(t, filepath.Join(root, "vault"), "claude", "work", ".credentials.json", map[string]any{
		"claudeAiOauth": map[string]any{"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expiry.UnixMilli()},
	})
	h, err := s.GetProfile("claude", "work")
	if err != nil || h == nil || !h.CredentialRenewable() {
		t.Fatalf("expected current renewable login: %+v, %v", h, err)
	}
	if err := os.Rename(path, path+".previous"); err != nil {
		t.Fatal(err)
	}
	h, err = s.GetProfile("claude", "work")
	if err != nil || h == nil || !h.TokenExpiresAt.IsZero() || h.CredentialRenewable() || h.PlanType != "max" {
		t.Fatalf("removed source retained cached credential facts: %+v, %v", h, err)
	}
	if signals := CredentialSignals(h, DefaultHealthConfig()); signals.LoginRequired != nil || signals.LaunchUsable != nil {
		t.Fatalf("missing source converted cached access expiry to hard expiry: %+v", signals)
	}
	stored, err := s.Load()
	if err != nil || !stored.Profiles["claude/work"].TokenExpiresAt.Equal(expiry) {
		t.Fatalf("report read mutated persistence: %+v, %v", stored, err)
	}
}

func TestStorageClaudeInvalidPrimaryDoesNotReviveLegacyLogin(t *testing.T) {
	for _, source := range []string{"malformed", "directory", "dangling symlink"} {
		t.Run(source, func(t *testing.T) {
			root := t.TempDir()
			vault := filepath.Join(root, "vault")
			writeHealthVaultJSON(t, vault, "claude", "work", ".claude.json", map[string]any{
				"access_token": "synthetic-old-access", "refresh_token": "synthetic-old-refresh", "expires_at": time.Now().Add(time.Hour).Unix(),
			})
			primary := filepath.Join(vault, "claude", "work", ".credentials.json")
			switch source {
			case "malformed":
				if err := os.WriteFile(primary, []byte("{"), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(primary, 0700); err != nil {
					t.Fatal(err)
				}
			case "dangling symlink":
				if err := os.Symlink(filepath.Join(root, "missing"), primary); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			s := NewStorage(filepath.Join(root, "health.json"))
			h, err := s.GetProfile("claude", "work")
			if err != nil || h == nil || !h.TokenExpiresAt.IsZero() || h.CredentialRenewable() || h.CredentialFingerprint != "" {
				t.Fatalf("invalid primary revived older credential: %+v, %v", h, err)
			}
		})
	}
}

func TestStorageListProfilesToleratesNullMetadata(t *testing.T) {
	s := NewStorage(filepath.Join(t.TempDir(), "health.json"))
	if err := os.WriteFile(s.Path(), []byte(`{"profiles":{"claude/absent":null,"unknown/absent":null,"bad-key":null}}`), 0600); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListProfiles()
	if err != nil || len(all) != 0 {
		t.Fatalf("null records should be ignored: %+v, %v", all, err)
	}
}

func TestStorageVaultBindingConcurrentReads(t *testing.T) {
	s := NewStorage(filepath.Join(t.TempDir(), "health.json"))
	root := t.TempDir()
	writeHealthVaultJSON(t, root, "cursor", "work", "auth.json", map[string]string{"apiKey": "synthetic-key"})
	var workers sync.WaitGroup
	for range 3 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for range 20 {
				s.SetVaultPath(root)
				if _, err := s.GetProfile("cursor", "work"); err != nil {
					t.Error(err)
				}
				if _, err := s.ListProfiles(); err != nil {
					t.Error(err)
				}
				s.SetVaultPath("")
			}
		}()
	}
	workers.Wait()
}
