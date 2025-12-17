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
