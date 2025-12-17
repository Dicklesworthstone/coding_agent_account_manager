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
