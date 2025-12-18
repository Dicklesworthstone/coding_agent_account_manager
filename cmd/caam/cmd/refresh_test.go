package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

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
