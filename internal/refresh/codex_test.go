package refresh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshCodexToken(t *testing.T) {
	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}

		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)

		if body["grant_type"] != "refresh_token" {
			t.Errorf("expected grant_type refresh_token, got %s", body["grant_type"])
		}
		if body["refresh_token"] != "test-refresh-token" {
			t.Errorf("unexpected refresh token: %s", body["refresh_token"])
		}
		if body["client_id"] != CodexClientID {
			t.Errorf("unexpected client id: %s", body["client_id"])
		}

		resp := TokenResponse{
			AccessToken:  "new-access-token",
			RefreshToken: "new-refresh-token",
			ExpiresIn:    3600,
			TokenType:    "Bearer",
		}
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	// Override URL
	oldURL := CodexTokenURL
	CodexTokenURL = server.URL
	defer func() { CodexTokenURL = oldURL }()

	// Test
	resp, err := RefreshCodexToken(context.Background(), "test-refresh-token")
	if err != nil {
		t.Fatalf("RefreshCodexToken failed: %v", err)
	}

	if resp.AccessToken != "new-access-token" {
		t.Errorf("expected access token new-access-token, got %s", resp.AccessToken)
	}
	if resp.RefreshToken != "new-refresh-token" {
		t.Errorf("expected refresh token new-refresh-token, got %s", resp.RefreshToken)
	}
	if resp.ExpiresIn != 3600 {
		t.Errorf("expected expires in 3600, got %d", resp.ExpiresIn)
	}
}

func TestUpdateCodexAuth(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	// Create initial auth file
	initialAuth := map[string]interface{}{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    1500000000,
		"token_type":    "Bearer",
	}
	data, _ := json.Marshal(initialAuth)
	os.WriteFile(path, data, 0600)

	// Update
	newResp := &TokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	}

	if err := UpdateCodexAuth(path, newResp); err != nil {
		t.Fatalf("UpdateCodexAuth failed: %v", err)
	}

	// Verify
	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	if updatedAuth["access_token"] != "new-access" {
		t.Errorf("access_token not updated")
	}
	if updatedAuth["refresh_token"] != "new-refresh" {
		t.Errorf("refresh_token not updated")
	}

	// Check expiry update (should be > initial)
	if val, ok := updatedAuth["expires_at"].(float64); !ok || val <= 1500000000 {
		t.Errorf("expires_at not updated correctly: %v", updatedAuth["expires_at"])
	}
}

func TestUpdateCodexAuth_TokensFormat(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	initialAuth := map[string]interface{}{
		"tokens": map[string]interface{}{
			"access_token":  "old-access",
			"refresh_token": "old-refresh",
			"expires_at":    1500000000,
		},
		"other_field": "preserve-me",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}

	newResp := &TokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	}

	if err := UpdateCodexAuth(path, newResp); err != nil {
		t.Fatalf("UpdateCodexAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	tokensRaw, ok := updatedAuth["tokens"]
	if !ok {
		t.Fatalf("tokens missing after update")
	}
	tokens, ok := tokensRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("tokens has unexpected type")
	}

	if tokens["access_token"] != "new-access" {
		t.Errorf("access_token not updated")
	}
	if tokens["refresh_token"] != "new-refresh" {
		t.Errorf("refresh_token not updated")
	}
	if val, ok := tokens["expires_at"].(float64); !ok || val <= 1500000000 {
		t.Errorf("expires_at not updated correctly: %v", tokens["expires_at"])
	}
	if updatedAuth["other_field"] != "preserve-me" {
		t.Errorf("other_field not preserved")
	}
}

// =============================================================================
// RefreshCodexToken Error Path Tests
// =============================================================================

func TestRefreshCodexToken_EmptyRefreshToken(t *testing.T) {
	_, err := RefreshCodexToken(context.Background(), "")
	if err == nil {
		t.Error("RefreshCodexToken should error on empty refresh token")
	}
	if err.Error() != "refresh token is empty" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRefreshCodexToken_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("Unauthorized"))
	}))
	defer server.Close()

	oldURL := CodexTokenURL
	CodexTokenURL = server.URL
	defer func() { CodexTokenURL = oldURL }()

	_, err := RefreshCodexToken(context.Background(), "test-token")
	if err == nil {
		t.Error("RefreshCodexToken should error on HTTP 401")
	}
}

func TestRefreshCodexToken_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	oldURL := CodexTokenURL
	CodexTokenURL = server.URL
	defer func() { CodexTokenURL = oldURL }()

	_, err := RefreshCodexToken(context.Background(), "test-token")
	if err == nil {
		t.Error("RefreshCodexToken should error on invalid JSON response")
	}
}

// =============================================================================
// VerifyCodexToken Tests
// =============================================================================

func TestVerifyCodexToken(t *testing.T) {
	// Issue #78: every non-200 answer used to collapse into one error that
	// doctor reported as "access token rejected by API". The probe must keep
	// the status, retry transient answers exactly once, and never retry a
	// definitive rejection.
	tests := []struct {
		name         string
		statuses     []int // answers in request order; the last one repeats
		wantErr      bool
		wantStatus   int
		wantAttempts int
		wantRejected bool
		wantTransit  bool
	}{
		{name: "200 accepted", statuses: []int{200}, wantAttempts: 1},
		{name: "204 accepted", statuses: []int{204}, wantAttempts: 1},
		{name: "401 rejected without retry", statuses: []int{401}, wantErr: true, wantStatus: 401, wantAttempts: 1, wantRejected: true},
		{name: "403 rejected without retry", statuses: []int{403}, wantErr: true, wantStatus: 403, wantAttempts: 1, wantRejected: true},
		{name: "429 then 200 recovers", statuses: []int{429, 200}, wantAttempts: 2},
		{name: "503 then 200 recovers", statuses: []int{503, 200}, wantAttempts: 2},
		{name: "500 twice is transient", statuses: []int{500, 500}, wantErr: true, wantStatus: 500, wantAttempts: 2, wantTransit: true},
		{name: "429 then 401 is rejected", statuses: []int{429, 401}, wantErr: true, wantStatus: 401, wantAttempts: 2, wantRejected: true},
		{name: "404 is neither", statuses: []int{404}, wantErr: true, wantStatus: 404, wantAttempts: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					t.Errorf("expected GET, got %s", r.Method)
				}
				if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
					t.Errorf("unexpected auth header: %q", got)
				}
				idx := calls
				if idx >= len(tt.statuses) {
					idx = len(tt.statuses) - 1
				}
				calls++
				w.WriteHeader(tt.statuses[idx])
			}))
			defer server.Close()

			oldURL := CodexVerifyURL
			CodexVerifyURL = server.URL
			defer func() { CodexVerifyURL = oldURL }()

			err := VerifyCodexToken(context.Background(), "test-token")
			if calls != tt.wantAttempts {
				t.Errorf("expected %d request(s), server saw %d", tt.wantAttempts, calls)
			}
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("expected success, got %v", err)
				}
				return
			}
			var verifyErr *TokenVerifyError
			if !errors.As(err, &verifyErr) {
				t.Fatalf("expected *TokenVerifyError, got %T: %v", err, err)
			}
			if verifyErr.StatusCode != tt.wantStatus {
				t.Errorf("StatusCode = %d, want %d", verifyErr.StatusCode, tt.wantStatus)
			}
			if verifyErr.Attempts != tt.wantAttempts {
				t.Errorf("Attempts = %d, want %d", verifyErr.Attempts, tt.wantAttempts)
			}
			if verifyErr.Rejected() != tt.wantRejected {
				t.Errorf("Rejected() = %v, want %v", verifyErr.Rejected(), tt.wantRejected)
			}
			if verifyErr.Transient() != tt.wantTransit {
				t.Errorf("Transient() = %v, want %v", verifyErr.Transient(), tt.wantTransit)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("status %d", tt.wantStatus)) {
				t.Errorf("error text should name the status: %q", err.Error())
			}
		})
	}
}

func TestVerifyCodexToken_TransportFailureCarriesNoStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := server.URL
	server.Close() // connection refused from here on

	oldURL := CodexVerifyURL
	CodexVerifyURL = url
	defer func() { CodexVerifyURL = oldURL }()

	err := VerifyCodexToken(context.Background(), "test-token")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	var verifyErr *TokenVerifyError
	if errors.As(err, &verifyErr) {
		t.Fatalf("transport failure must not be a TokenVerifyError: %v", err)
	}
	if !strings.HasPrefix(err.Error(), "request failed:") {
		t.Errorf("transport failures keep the 'request failed:' prefix, got %q", err.Error())
	}
}

func TestVerifyCodexToken_CancelledDuringBackoff(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Retry-After", "3")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	oldURL := CodexVerifyURL
	CodexVerifyURL = server.URL
	defer func() { CodexVerifyURL = oldURL }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	err := VerifyCodexToken(ctx, "test-token")
	if calls != 1 {
		t.Errorf("expected the retry to be abandoned, server saw %d requests", calls)
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline error, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("cancellation should cut the backoff short, took %s", elapsed)
	}
}

func TestVerifyCodexToken_RefusesNonAllowlistedHost(t *testing.T) {
	oldURL := CodexVerifyURL
	CodexVerifyURL = "https://evil.example.com/v1/me"
	defer func() { CodexVerifyURL = oldURL }()

	err := VerifyCodexToken(context.Background(), "test-token")
	if err == nil || !strings.Contains(err.Error(), "not allowlisted") {
		t.Fatalf("expected allowlist refusal, got %v", err)
	}
}

func TestCodexVerifyBackoff(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"", codexVerifyRetryDelay},
		{"garbage", codexVerifyRetryDelay},
		{"0", codexVerifyRetryDelay},
		{"-5", codexVerifyRetryDelay},
		{"1", time.Second},
		{" 2 ", 2 * time.Second},
		{"120", codexVerifyRetryMax},
		{"Wed, 21 Oct 2015 07:28:00 GMT", codexVerifyRetryDelay},
	}
	for _, tt := range tests {
		if got := codexVerifyBackoff(tt.header); got != tt.want {
			t.Errorf("codexVerifyBackoff(%q) = %s, want %s", tt.header, got, tt.want)
		}
	}
}

// =============================================================================
// UpdateCodexAuth Error Path Tests
// =============================================================================

func TestUpdateCodexAuth_MissingFile(t *testing.T) {
	resp := &TokenResponse{AccessToken: "token"}
	err := UpdateCodexAuth("/nonexistent/path/auth.json", resp)
	if err == nil {
		t.Error("UpdateCodexAuth should error on missing file")
	}
}

func TestUpdateCodexAuth_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	if err := os.WriteFile(path, []byte("not valid json"), 0600); err != nil {
		t.Fatal(err)
	}

	resp := &TokenResponse{AccessToken: "token"}
	err := UpdateCodexAuth(path, resp)
	if err == nil {
		t.Error("UpdateCodexAuth should error on invalid JSON")
	}
}

func TestUpdateCodexAuth_InvalidTokensFormat(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	// tokens is not a map
	initialAuth := map[string]interface{}{
		"tokens": "not a map",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	resp := &TokenResponse{AccessToken: "token"}
	err := UpdateCodexAuth(path, resp)
	if err == nil {
		t.Error("UpdateCodexAuth should error on invalid tokens format")
	}
}

func TestUpdateCodexAuth_NoRefreshToken(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	initialAuth := map[string]interface{}{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	// Response without refresh token
	resp := &TokenResponse{
		AccessToken: "new-access",
		ExpiresIn:   3600,
	}

	if err := UpdateCodexAuth(path, resp); err != nil {
		t.Fatalf("UpdateCodexAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	if updatedAuth["access_token"] != "new-access" {
		t.Errorf("access_token not updated")
	}
	if updatedAuth["refresh_token"] != "old-refresh" {
		t.Errorf("refresh_token should be preserved")
	}
}

func TestUpdateCodexAuth_NoExpiresIn(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	initialAuth := map[string]interface{}{
		"access_token": "old-access",
		"expires_at":   1500000000,
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	// Response without ExpiresIn
	resp := &TokenResponse{
		AccessToken: "new-access",
	}

	if err := UpdateCodexAuth(path, resp); err != nil {
		t.Fatalf("UpdateCodexAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	// expires_at should remain unchanged since ExpiresIn is 0
	if val, ok := updatedAuth["expires_at"].(float64); ok && val != 1500000000 {
		t.Errorf("expires_at should remain unchanged when ExpiresIn is 0")
	}
}
