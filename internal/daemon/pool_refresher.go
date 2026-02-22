package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// PoolRefresher implements authpool.Refresher using the existing refresh package.
type PoolRefresher struct {
	vault       *authfile.Vault
	healthStore *health.Storage
}

// NewPoolRefresher creates a new refresher for use with AuthPool.
func NewPoolRefresher(vault *authfile.Vault, healthStore *health.Storage) *PoolRefresher {
	return &PoolRefresher{
		vault:       vault,
		healthStore: healthStore,
	}
}

// Refresh implements authpool.Refresher.
// It refreshes the token for the given provider/profile and returns the new expiry time.
func (r *PoolRefresher) Refresh(ctx context.Context, provider, profile string) (time.Time, error) {
	err := refresh.RefreshProfile(ctx, provider, profile, r.vault, r.healthStore)
	if err != nil {
		return time.Time{}, err
	}

	// Get the new expiry time after refresh
	expiry, err := r.getTokenExpiry(provider, profile)
	if err != nil {
		// Refresh succeeded but we couldn't determine expiry - return a default
		return time.Now().Add(time.Hour), nil
	}

	return expiry, nil
}

// getTokenExpiry reads the token expiry for a profile.
func (r *PoolRefresher) getTokenExpiry(provider, profile string) (time.Time, error) {
	vaultPath := r.vault.ProfilePath(provider, profile)

	var expiryInfo *health.ExpiryInfo
	var err error

	switch provider {
	case "claude":
		expiryInfo, err = health.ParseClaudeExpiry(vaultPath)
	case "codex":
		expiryInfo, err = health.ParseCodexExpiry(filepath.Join(vaultPath, "auth.json"))
	case "gemini":
		// Migrate legacy vault filename before reading.
		_ = authfile.MigrateGeminiVaultDir(vaultPath)
		expiryInfo, err = health.ParseGeminiExpiry(vaultPath)
	default:
		return time.Time{}, fmt.Errorf("unknown provider: %s", provider)
	}

	if err != nil {
		return time.Time{}, err
	}

	if expiryInfo == nil || expiryInfo.ExpiresAt.IsZero() {
		return time.Time{}, fmt.Errorf("expiry not found")
	}

	return expiryInfo.ExpiresAt, nil
}
