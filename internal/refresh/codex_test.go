package refresh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

func TestVerifyCodexToken_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader != "Bearer test-token" {
			t.Errorf("unexpected auth header: %s", authHeader)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Override URL via test helper - we need to test with real endpoint
	// Skip this test since VerifyCodexToken uses a hardcoded URL
	t.Skip("VerifyCodexToken uses hardcoded URL, tested via integration tests")
}

func TestVerifyCodexToken_Unauthorized(t *testing.T) {
	// Skip since VerifyCodexToken uses hardcoded URL
	t.Skip("VerifyCodexToken uses hardcoded URL, tested via integration tests")
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
