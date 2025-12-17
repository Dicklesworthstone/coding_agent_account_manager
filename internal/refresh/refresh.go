package refresh

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// DefaultRefreshThreshold is the time before expiry to trigger a refresh.
const DefaultRefreshThreshold = 10 * time.Minute

// ShouldRefresh determines if a profile needs refreshing.
func ShouldRefresh(h *health.ProfileHealth, threshold time.Duration) bool {
	if h == nil || h.TokenExpiresAt.IsZero() {
		return false // Unknown expiry, do not assume refresh needed (avoid loops)
	}

	if threshold == 0 {
		threshold = DefaultRefreshThreshold
	}

	ttl := time.Until(h.TokenExpiresAt)
	return ttl > 0 && ttl < threshold
}

// RefreshProfile orchestrates the refresh for a specific provider/profile.
func RefreshProfile(ctx context.Context, provider, profile string, vault *authfile.Vault, store *health.Storage) error {
	vaultPath := vault.ProfilePath(provider, profile)

	switch provider {
	case "claude":
		return refreshClaude(ctx, vaultPath)
	case "codex":
		return refreshCodex(ctx, vaultPath)
	case "gemini":
		return refreshGemini(ctx, provider, profile, store, vaultPath)
	default:
		return fmt.Errorf("refresh not supported for provider: %s", provider)
	}
}

func refreshClaude(ctx context.Context, vaultPath string) error {
	info, err := health.ParseClaudeExpiry(vaultPath)
	if err != nil {
		return fmt.Errorf("parse auth: %w", err)
	}
	
	if info.Source == "" {
		return fmt.Errorf("auth file source unknown")
	}
	
	refreshToken, err := getRefreshTokenFromJSON(info.Source)
	if err != nil {
		return fmt.Errorf("read refresh token: %w", err)
	}
	
	resp, err := RefreshClaudeToken(ctx, refreshToken)
	if err != nil {
		return fmt.Errorf("refresh api: %w", err)
	}
	
	if err := UpdateClaudeAuth(info.Source, resp); err != nil {
		return fmt.Errorf("update auth: %w", err)
	}
	
	return nil
}

func refreshCodex(ctx context.Context, vaultPath string) error {
	authPath := filepath.Join(vaultPath, "auth.json")
	
	refreshToken, err := getRefreshTokenFromJSON(authPath)
	if err != nil {
		return fmt.Errorf("read refresh token: %w", err)
	}
	
	resp, err := RefreshCodexToken(ctx, refreshToken)
	if err != nil {
		return fmt.Errorf("refresh api: %w", err)
	}
	
	if err := UpdateCodexAuth(authPath, resp); err != nil {
		return fmt.Errorf("update auth: %w", err)
	}
	
	return nil
}

func refreshGemini(ctx context.Context, provider, profile string, store *health.Storage, vaultPath string) error {
	info, err := health.ParseGeminiExpiry(vaultPath)
	if err != nil {
		return fmt.Errorf("parse gemini auth: %w", err)
	}
	
	adc, err := ReadADC(info.Source)
	if err != nil {
		return fmt.Errorf("read adc: %w", err)
	}
	
	resp, err := RefreshGeminiToken(ctx, adc.ClientID, adc.ClientSecret, adc.RefreshToken)
	if err != nil {
		return fmt.Errorf("refresh api: %w", err)
	}
	
	if err := UpdateGeminiHealth(store, provider, profile, resp); err != nil {
		return fmt.Errorf("update health: %w", err)
	}
	
	return nil
}

// getRefreshTokenFromJSON reads a JSON file and extracts the refresh_token field.
// Supports snake_case and camelCase.
func getRefreshTokenFromJSON(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return "", err
	}

	if val, ok := auth["refresh_token"].(string); ok && val != "" {
		return val, nil
	}
	if val, ok := auth["refreshToken"].(string); ok && val != "" {
		return val, nil
	}

	return "", fmt.Errorf("refresh_token not found in %s", path)
}
