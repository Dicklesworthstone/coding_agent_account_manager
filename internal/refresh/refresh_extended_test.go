package refresh

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRefreshProfile_Claude_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Create vault for Claude profile")
	rootDir := h.TempDir
	vaultDir := filepath.Join(rootDir, "vault")
	vault := authfile.NewVault(vaultDir)

	// Create Claude profile
	profileDir := filepath.Join(vaultDir, "claude", "test")
	require.NoError(t, os.MkdirAll(profileDir, 0755))

	// Write auth file
	authPath := filepath.Join(profileDir, ".claude.json")
	initialContent := `{"refreshToken": "old-refresh-token", "accessToken": "old-access", "expiresAt": "2020-01-01T00:00:00Z"}`
	require.NoError(t, os.WriteFile(authPath, []byte(initialContent), 0600))
	h.EndStep("Setup")

	// 2. Refresh - should return UnsupportedError
	h.StartStep("Refresh", "Call RefreshProfile (expect UnsupportedError)")
	err := RefreshProfile(context.Background(), "claude", "test", vault, nil)

	// Claude refresh is disabled (see docs/CLAUDE_AUTH_INVENTORY.md CLAUDE-006)
	// The refresh API endpoint is speculative/undocumented, so we return UnsupportedError
	require.Error(t, err, "Claude refresh should return error")

	var unsupportedErr *UnsupportedError
	require.ErrorAs(t, err, &unsupportedErr, "error should be UnsupportedError")
	assert.Equal(t, "claude", unsupportedErr.Provider)
	assert.Contains(t, unsupportedErr.Reason, "token refresh disabled")
	h.EndStep("Refresh")

	// 3. Verify auth file unchanged
	h.StartStep("Verify", "Auth file should be unchanged")
	content, err := os.ReadFile(authPath)
	require.NoError(t, err)

	// File should contain original values (not modified)
	assert.Contains(t, string(content), "old-refresh-token")
	assert.Contains(t, string(content), "old-access")
	h.EndStep("Verify")
}

func TestRefreshProfile_Codex_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Mock Codex refresh and vault")
	rootDir := h.TempDir
	vaultDir := filepath.Join(rootDir, "vault")
	vault := authfile.NewVault(vaultDir)
	
	// Create Codex profile
	profileDir := filepath.Join(vaultDir, "codex", "test")
	require.NoError(t, os.MkdirAll(profileDir, 0755))
	
	authPath := filepath.Join(profileDir, "auth.json")
	initialContent := `{"refresh_token": "old-codex-refresh", "access_token": "old-codex-access"}`
	require.NoError(t, os.WriteFile(authPath, []byte(initialContent), 0600))
	
	// Mock RefreshCodexToken
	originalRefresh := RefreshCodexToken
	defer func() { RefreshCodexToken = originalRefresh }()
	
	RefreshCodexToken = func(ctx context.Context, refreshToken string) (*TokenResponse, error) {
		assert.Equal(t, "old-codex-refresh", refreshToken)
		return &TokenResponse{
			AccessToken:  "new-codex-access",
			RefreshToken: "new-codex-refresh",
			ExpiresIn:    3600,
		}, nil
	}
	
	h.EndStep("Setup")
	
	// 2. Refresh
	h.StartStep("Refresh", "Call RefreshProfile")
	err := RefreshProfile(context.Background(), "codex", "test", vault, nil)
	require.NoError(t, err)
	h.EndStep("Refresh")
	
	// 3. Verify
	h.StartStep("Verify", "Check updated auth file")
	content, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "new-codex-access")
	assert.Contains(t, string(content), "new-codex-refresh")
	h.EndStep("Verify")
}

func TestRefreshProfile_Gemini_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Mock Gemini refresh and vault")
	rootDir := h.TempDir
	vaultDir := filepath.Join(rootDir, "vault")
	vault := authfile.NewVault(vaultDir)
	
	// Create Gemini profile
	profileDir := filepath.Join(vaultDir, "gemini", "test")
	require.NoError(t, os.MkdirAll(profileDir, 0755))
	
	settingsPath := filepath.Join(profileDir, "settings.json")
	initialSettings := `{"accessToken": "old-gemini-access"}`
	require.NoError(t, os.WriteFile(settingsPath, []byte(initialSettings), 0600))
	
	// Gemini needs oauth_creds.json for client info
	credsPath := filepath.Join(profileDir, "oauth_creds.json")
	credsContent := `{"client_id": "test-id", "client_secret": "test-secret", "refresh_token": "gemini-refresh"}`
	require.NoError(t, os.WriteFile(credsPath, []byte(credsContent), 0600))
	
	// Mock RefreshGeminiToken
	originalRefresh := RefreshGeminiToken
	defer func() { RefreshGeminiToken = originalRefresh }()
	
	RefreshGeminiToken = func(ctx context.Context, clientID, clientSecret, refreshToken string) (*GoogleTokenResponse, error) {
		assert.Equal(t, "test-id", clientID)
		assert.Equal(t, "test-secret", clientSecret)
		assert.Equal(t, "gemini-refresh", refreshToken)
		return &GoogleTokenResponse{
			AccessToken: "new-gemini-access",
			ExpiresIn:   3600,
		}, nil
	}
	
	h.EndStep("Setup")
	
	// 2. Refresh
	h.StartStep("Refresh", "Call RefreshProfile")
	err := RefreshProfile(context.Background(), "gemini", "test", vault, nil)
	require.NoError(t, err)
	h.EndStep("Refresh")
	
	// 3. Verify
	h.StartStep("Verify", "Check updated auth file")
	content, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "new-gemini-access")
	// Verify it contains updated expiry if possible (ExpiresIn was set)
	// UpdateGeminiAuth sets expires_at/expiry etc.
	h.EndStep("Verify")
}
