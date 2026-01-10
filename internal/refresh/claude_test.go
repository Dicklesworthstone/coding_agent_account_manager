package refresh

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRefreshClaudeToken(t *testing.T) {
	// Mock server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}

		err := r.ParseForm()
		if err != nil {
			t.Fatal(err)
		}

		if r.Form.Get("grant_type") != "refresh_token" {
			t.Errorf("expected grant_type refresh_token, got %s", r.Form.Get("grant_type"))
		}
		if r.Form.Get("refresh_token") != "test-refresh-token" {
			t.Errorf("unexpected refresh token: %s", r.Form.Get("refresh_token"))
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
	oldURL := ClaudeTokenURL
	ClaudeTokenURL = server.URL
	defer func() { ClaudeTokenURL = oldURL }()

	// Test
	resp, err := RefreshClaudeToken(context.Background(), "test-refresh-token")
	if err != nil {
		t.Fatalf("RefreshClaudeToken failed: %v", err)
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

func TestUpdateClaudeAuth(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	// Create initial auth file
	initialAuth := map[string]interface{}{
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"expires_at":    "2020-01-01T00:00:00Z",
		"other_field":   "preserve-me",
	}
	data, _ := json.Marshal(initialAuth)
	os.WriteFile(path, data, 0600)

	// Update
	newResp := &TokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	}

	if err := UpdateClaudeAuth(path, newResp); err != nil {
		t.Fatalf("UpdateClaudeAuth failed: %v", err)
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
	if updatedAuth["other_field"] != "preserve-me" {
		t.Errorf("other_field not preserved")
	}

	// Check expiry update (approximate)
	if _, ok := updatedAuth["expires_at"]; !ok {
		t.Error("expires_at missing")
	}
}

func TestUpdateClaudeAuth_CredentialsFormat(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "credentials.json")

	initialAuth := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken":  "old-access",
			"refreshToken": "old-refresh",
			"expiresAt":    1700000000000,
		},
		"other_field": "preserve-me",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write credentials.json: %v", err)
	}

	newResp := &TokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	}

	if err := UpdateClaudeAuth(path, newResp); err != nil {
		t.Fatalf("UpdateClaudeAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	oauthRaw, ok := updatedAuth["claudeAiOauth"]
	if !ok {
		t.Fatalf("claudeAiOauth missing after update")
	}
	oauth, ok := oauthRaw.(map[string]interface{})
	if !ok {
		t.Fatalf("claudeAiOauth has unexpected type")
	}

	if oauth["accessToken"] != "new-access" {
		t.Errorf("accessToken not updated")
	}
	if oauth["refreshToken"] != "new-refresh" {
		t.Errorf("refreshToken not updated")
	}
	if _, ok := oauth["expiresAt"]; !ok {
		t.Errorf("expiresAt missing after update")
	}
	if updatedAuth["other_field"] != "preserve-me" {
		t.Errorf("other_field not preserved")
	}
}

// =============================================================================
// RefreshClaudeToken Error Path Tests
// =============================================================================

func TestRefreshClaudeToken_EmptyRefreshToken(t *testing.T) {
	_, err := RefreshClaudeToken(context.Background(), "")
	if err == nil {
		t.Error("RefreshClaudeToken should error on empty refresh token")
	}
	if err.Error() != "refresh token is empty" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRefreshClaudeToken_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	oldURL := ClaudeTokenURL
	ClaudeTokenURL = server.URL
	defer func() { ClaudeTokenURL = oldURL }()

	_, err := RefreshClaudeToken(context.Background(), "test-token")
	if err == nil {
		t.Error("RefreshClaudeToken should error on HTTP 401")
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRefreshClaudeToken_InvalidJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not valid json"))
	}))
	defer server.Close()

	oldURL := ClaudeTokenURL
	ClaudeTokenURL = server.URL
	defer func() { ClaudeTokenURL = oldURL }()

	_, err := RefreshClaudeToken(context.Background(), "test-token")
	if err == nil {
		t.Error("RefreshClaudeToken should error on invalid JSON response")
	}
	if !strings.Contains(err.Error(), "decode response") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRefreshClaudeToken_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow response
		time.Sleep(2 * time.Second)
		json.NewEncoder(w).Encode(TokenResponse{})
	}))
	defer server.Close()

	oldURL := ClaudeTokenURL
	ClaudeTokenURL = server.URL
	defer func() { ClaudeTokenURL = oldURL }()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := RefreshClaudeToken(ctx, "test-token")
	if err == nil {
		t.Error("RefreshClaudeToken should error on context timeout")
	}
}

// =============================================================================
// claudeExpiryFromResponse Tests
// =============================================================================

func TestClaudeExpiryFromResponse_NilResponse(t *testing.T) {
	result := claudeExpiryFromResponse(nil)
	if !result.IsZero() {
		t.Error("claudeExpiryFromResponse(nil) should return zero time")
	}
}

func TestClaudeExpiryFromResponse_ExpiresAt(t *testing.T) {
	expiryStr := "2025-12-25T10:30:00Z"
	resp := &TokenResponse{
		ExpiresAt: expiryStr,
	}

	result := claudeExpiryFromResponse(resp)
	expected, _ := time.Parse(time.RFC3339, expiryStr)
	if !result.Equal(expected) {
		t.Errorf("claudeExpiryFromResponse = %v, want %v", result, expected)
	}
}

func TestClaudeExpiryFromResponse_ExpiresIn(t *testing.T) {
	resp := &TokenResponse{
		ExpiresIn: 3600, // 1 hour
	}

	before := time.Now()
	result := claudeExpiryFromResponse(resp)
	after := time.Now()

	expectedMin := before.Add(time.Duration(resp.ExpiresIn) * time.Second)
	expectedMax := after.Add(time.Duration(resp.ExpiresIn) * time.Second)

	if result.Before(expectedMin) || result.After(expectedMax) {
		t.Errorf("claudeExpiryFromResponse = %v, expected between %v and %v", result, expectedMin, expectedMax)
	}
}

func TestClaudeExpiryFromResponse_InvalidExpiresAt(t *testing.T) {
	resp := &TokenResponse{
		ExpiresAt: "not-a-valid-date",
		ExpiresIn: 3600,
	}

	result := claudeExpiryFromResponse(resp)
	// Should fall back to ExpiresIn
	if result.IsZero() {
		t.Error("claudeExpiryFromResponse should fall back to ExpiresIn")
	}
}

func TestClaudeExpiryFromResponse_NoExpiry(t *testing.T) {
	resp := &TokenResponse{
		AccessToken: "token",
		// No ExpiresAt or ExpiresIn
	}

	result := claudeExpiryFromResponse(resp)
	if !result.IsZero() {
		t.Error("claudeExpiryFromResponse should return zero time when no expiry provided")
	}
}

// =============================================================================
// UpdateClaudeAuth Error Path Tests
// =============================================================================

func TestUpdateClaudeAuth_MissingFile(t *testing.T) {
	resp := &TokenResponse{AccessToken: "token"}
	err := UpdateClaudeAuth("/nonexistent/path/auth.json", resp)
	if err == nil {
		t.Error("UpdateClaudeAuth should error on missing file")
	}
}

func TestUpdateClaudeAuth_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	if err := os.WriteFile(path, []byte("not valid json"), 0600); err != nil {
		t.Fatal(err)
	}

	resp := &TokenResponse{AccessToken: "token"}
	err := UpdateClaudeAuth(path, resp)
	if err == nil {
		t.Error("UpdateClaudeAuth should error on invalid JSON")
	}
}

func TestUpdateClaudeAuth_InvalidClaudeAiOauth(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	// claudeAiOauth is not a map
	initialAuth := map[string]interface{}{
		"claudeAiOauth": "not a map",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	resp := &TokenResponse{AccessToken: "token"}
	err := UpdateClaudeAuth(path, resp)
	if err == nil {
		t.Error("UpdateClaudeAuth should error on invalid claudeAiOauth format")
	}
}

func TestUpdateClaudeAuth_CamelCaseFormat(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	initialAuth := map[string]interface{}{
		"accessToken":  "old-access",
		"refreshToken": "old-refresh",
		"expiresAt":    "2020-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	resp := &TokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	}

	if err := UpdateClaudeAuth(path, resp); err != nil {
		t.Fatalf("UpdateClaudeAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	if updatedAuth["accessToken"] != "new-access" {
		t.Errorf("accessToken not updated")
	}
	if updatedAuth["refreshToken"] != "new-refresh" {
		t.Errorf("refreshToken not updated")
	}
}

func TestUpdateClaudeAuth_NoRefreshToken(t *testing.T) {
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

	// Response without refresh token - should preserve old one
	resp := &TokenResponse{
		AccessToken: "new-access",
		ExpiresIn:   3600,
	}

	if err := UpdateClaudeAuth(path, resp); err != nil {
		t.Fatalf("UpdateClaudeAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	// refresh_token should be preserved (not updated to empty)
	if updatedAuth["refresh_token"] != "old-refresh" {
		t.Errorf("refresh_token was unexpectedly modified: got %v", updatedAuth["refresh_token"])
	}
}

func TestUpdateClaudeAuth_NewFileFields(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "auth.json")

	// No existing access_token or accessToken field
	initialAuth := map[string]interface{}{
		"other_field": "value",
	}
	data, _ := json.Marshal(initialAuth)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	resp := &TokenResponse{
		AccessToken:  "new-access",
		RefreshToken: "new-refresh",
		ExpiresIn:    3600,
	}

	if err := UpdateClaudeAuth(path, resp); err != nil {
		t.Fatalf("UpdateClaudeAuth failed: %v", err)
	}

	updatedData, _ := os.ReadFile(path)
	var updatedAuth map[string]interface{}
	json.Unmarshal(updatedData, &updatedAuth)

	// Should default to snake_case for new fields
	if updatedAuth["access_token"] != "new-access" {
		t.Errorf("access_token not set correctly")
	}
	if updatedAuth["refresh_token"] != "new-refresh" {
		t.Errorf("refresh_token not set correctly")
	}
}
