package usage

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// codexTestJWT builds an unsigned JWT whose payload is claims.
func codexTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal claims: %v", err)
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

// scopedUsageServer mimics /wham/usage for a multi-workspace login: scoped to
// wantAccount it reports 43% used, unscoped (or scoped to anything else) it
// reports 86%. It records the header it saw.
func scopedUsageServer(t *testing.T, wantAccount string, seen *[]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("chatgpt-account-id") // header names are case-insensitive
		*seen = append(*seen, got)
		used := 86
		if got != "" && got == wantAccount {
			used = 43
		}
		w.Header().Set("Content-Type", "application/json")
		body, _ := json.Marshal(map[string]any{
			"plan_type": "pro",
			"rate_limit": map[string]any{
				"primary_window": map[string]any{"used_percent": used, "reset_at": 1900000000, "limit_window_seconds": 18000},
			},
		})
		_, _ = io.WriteString(w, string(body))
	}))
}

func TestCodexFetcher_ScopesUsageToTokenAccount(t *testing.T) {
	const account = "11111111-2222-3333-4444-555555555555"
	token := codexTestJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{
			"chatgpt_account_id": account,
			"chatgpt_user_id":    "user-abc",
			"user_id":            "user-abc",
		},
	})

	var seen []string
	server := scopedUsageServer(t, account, &seen)
	defer server.Close()

	f := NewCodexFetcher()
	f.baseURL = server.URL
	info, err := f.Fetch(context.Background(), token)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(seen) != 1 || seen[0] != account {
		t.Fatalf("ChatGPT-Account-Id sent = %q, want %q", seen, account)
	}
	if info.AccountID != account {
		t.Errorf("AccountID = %q, want %q", info.AccountID, account)
	}
	if info.PrimaryWindow == nil || info.PrimaryWindow.UsedPercent != 43 {
		t.Fatalf("primary window = %+v, want 43%% used as reported", info.PrimaryWindow)
	}
	if info.PrimaryWindow.Utilization != 0.43 {
		t.Errorf("Utilization = %v, want 0.43", info.PrimaryWindow.Utilization)
	}
}

func TestCodexFetcher_ExplicitAccountIDWins(t *testing.T) {
	token := codexTestJWT(t, map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "from-token"},
	})
	var seen []string
	server := scopedUsageServer(t, "explicit", &seen)
	defer server.Close()

	f := NewCodexFetcher()
	f.baseURL = server.URL
	info, err := f.FetchWithOptions(context.Background(), token, &CodexFetchOptions{AccountID: "  explicit \n"})
	if err != nil {
		t.Fatalf("FetchWithOptions: %v", err)
	}
	if len(seen) != 1 || seen[0] != "explicit" {
		t.Fatalf("ChatGPT-Account-Id sent = %q, want explicit", seen)
	}
	if info.AccountID != "explicit" {
		t.Errorf("AccountID = %q, want explicit", info.AccountID)
	}
}

func TestCodexFetcher_NoAccountHeaderWithoutAnID(t *testing.T) {
	cases := map[string]string{
		"opaque token": "not-a-jwt",
		// user_id identifies the person, not the workspace; it must never be
		// sent as the account id.
		"user id only": codexTestJWT(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"user_id": "user-abc", "chatgpt_user_id": "user-abc"},
		}),
		"control characters": codexTestJWT(t, map[string]any{
			"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct\r\nX-Evil: 1"},
		}),
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			var seen []string
			server := scopedUsageServer(t, "irrelevant", &seen)
			defer server.Close()

			f := NewCodexFetcher()
			f.baseURL = server.URL
			info, err := f.Fetch(context.Background(), token)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if len(seen) != 1 || seen[0] != "" {
				t.Fatalf("ChatGPT-Account-Id sent = %q, want none", seen)
			}
			if info.AccountID != "" {
				t.Errorf("AccountID = %q, want empty", info.AccountID)
			}
		})
	}
}
