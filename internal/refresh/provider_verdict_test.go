package refresh

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// Issue #108: when the token endpoint refuses a Codex refresh token as
// revoked, caam must say so structurally (without echoing the body) and
// record the rejection against that credential, so ls/status stop calling
// the profile healthy.
func TestRefreshCodexToken_RejectionIsStructuredAndBodyFree(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		rejected bool
		code     string
	}{
		{"revoked nested code", 401, `{"error":{"message":"sk-leak","code":"refresh_token_invalidated"}}`, true, "refresh_token_invalidated"},
		{"expired nested code", 401, `{"error":{"code":"refresh_token_expired"}}`, true, "refresh_token_expired"},
		{"bare 401", 401, `Unauthorized`, true, ""},
		{"403 with oauth code", 403, `{"error":"access_denied"}`, true, "access_denied"},
		{"403 edge challenge page", 403, `<html>Just a moment...</html>`, false, ""},
		{"400 invalid_grant", 400, `{"error":"invalid_grant","error_description":"sk-leak"}`, true, "invalid_grant"},
		{"400 other", 400, `{"error":"invalid_request"}`, false, ""},
		{"500", 500, `oops`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			old := CodexTokenURL
			CodexTokenURL = server.URL
			defer func() { CodexTokenURL = old }()

			_, err := RefreshCodexToken(context.Background(), "rt")
			if err == nil {
				t.Fatal("expected an error")
			}
			var rej *RefreshRejectedError
			if got := errors.As(err, &rej); got != tc.rejected {
				t.Fatalf("rejected = %v, want %v (err %v)", got, tc.rejected, err)
			}
			if !tc.rejected {
				return
			}
			if rej.Code != tc.code || rej.StatusCode != tc.status {
				t.Errorf("got %+v, want code %q status %d", rej, tc.code, tc.status)
			}
			if strings.Contains(err.Error(), "sk-leak") {
				t.Errorf("error echoes the response body: %v", err)
			}
		})
	}
}

func TestRefreshProfile_RecordsProviderVerdict(t *testing.T) {
	setup := func(t *testing.T) (*authfile.Vault, *health.Storage, string) {
		t.Helper()
		root := t.TempDir()
		vault := authfile.NewVault(filepath.Join(root, "vault"))
		dir := filepath.Join(root, "vault", "codex", "work")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		authPath := filepath.Join(dir, "auth.json")
		if err := os.WriteFile(authPath, []byte(`{"tokens":{"access_token":"at","refresh_token":"revoked-rt"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		return vault, health.NewStorage(filepath.Join(root, "health.json")), authPath
	}
	stub := func(t *testing.T, fn func(context.Context, string) (*TokenResponse, error)) {
		t.Helper()
		orig := RefreshCodexToken
		RefreshCodexToken = fn
		t.Cleanup(func() { RefreshCodexToken = orig })
	}

	t.Run("rejection", func(t *testing.T) {
		vault, store, authPath := setup(t)
		stub(t, func(context.Context, string) (*TokenResponse, error) {
			return nil, &RefreshRejectedError{Provider: "codex", StatusCode: 401, Code: "refresh_token_invalidated"}
		})
		if err := RefreshProfile(context.Background(), "codex", "work", vault, store); err == nil {
			t.Fatal("expected the rejection to surface")
		}
		h, err := store.GetProfile("codex", "work")
		if err != nil || h == nil {
			t.Fatalf("no health record: %v", err)
		}
		if h.ProviderRejection != "refresh_token_invalidated" {
			t.Errorf("reason = %q", h.ProviderRejection)
		}
		data, _ := os.ReadFile(authPath)
		h.CredentialFingerprint = health.CodexCredentialFingerprint(data)
		if !h.ProviderRejected() {
			t.Error("rejection not attributed to the refreshed credential")
		}
	})

	t.Run("reused", func(t *testing.T) {
		vault, store, _ := setup(t)
		stub(t, func(context.Context, string) (*TokenResponse, error) { return nil, ErrRefreshTokenReused })
		err := RefreshProfile(context.Background(), "codex", "work", vault, store)
		var reused *RefreshTokenReusedError
		if !errors.As(err, &reused) {
			t.Fatalf("err = %v, want RefreshTokenReusedError", err)
		}
		h, _ := store.GetProfile("codex", "work")
		if h == nil || h.ProviderRejection != "refresh_token_reused" {
			t.Errorf("reuse not recorded: %+v", h)
		}
	})

	t.Run("transient failure records nothing", func(t *testing.T) {
		vault, store, _ := setup(t)
		stub(t, func(context.Context, string) (*TokenResponse, error) {
			return nil, errors.New("codex refresh error 503: busy")
		})
		_ = RefreshProfile(context.Background(), "codex", "work", vault, store)
		stored, err := store.Load()
		if err != nil {
			t.Fatal(err)
		}
		if h := stored.Profiles["codex/work"]; h != nil {
			t.Errorf("a transient failure persisted health metadata: %+v", h)
		}
		if h, err := store.GetProfile("codex", "work"); err != nil || h == nil || h.ProviderRejected() || !h.ProviderVerifiedAt().IsZero() {
			t.Errorf("passive vault health should exist without a provider verdict: %+v, %v", h, err)
		}
	})

	t.Run("success clears and verifies the new credential", func(t *testing.T) {
		vault, store, authPath := setup(t)
		if err := store.RecordProviderVerification("codex", "work", health.ProviderVerification{Reason: "x", Fingerprint: "old"}); err != nil {
			t.Fatal(err)
		}
		stub(t, func(context.Context, string) (*TokenResponse, error) {
			return &TokenResponse{AccessToken: "at2", RefreshToken: "rt2", ExpiresIn: 3600}, nil
		})
		if err := RefreshProfile(context.Background(), "codex", "work", vault, store); err != nil {
			t.Fatal(err)
		}
		h, _ := store.GetProfile("codex", "work")
		if h == nil || !h.ProviderRejectedAt.IsZero() || h.LastVerifiedAt.IsZero() {
			t.Fatalf("success not recorded: %+v", h)
		}
		data, _ := os.ReadFile(authPath)
		if h.VerifiedFingerprint != health.CodexCredentialFingerprint(data) {
			t.Error("acceptance recorded against the old credential, not the refreshed one")
		}
	})

	t.Run("nil store is fine", func(t *testing.T) {
		vault, _, _ := setup(t)
		stub(t, func(context.Context, string) (*TokenResponse, error) {
			return nil, &RefreshRejectedError{Provider: "codex", StatusCode: 401}
		})
		if err := RefreshProfile(context.Background(), "codex", "work", vault, nil); err == nil {
			t.Fatal("expected an error")
		}
	})
}
