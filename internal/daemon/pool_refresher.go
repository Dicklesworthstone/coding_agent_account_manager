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

// Preflight lets the pool reject unsupported or stale credentials before
// announcing a refresh or marking the account unavailable.
func (r *PoolRefresher) Preflight(provider, profile string) error {
	return refresh.Preflight(provider, profile, r.vault)
}

// Refresh implements authpool.Refresher.
// It refreshes the token for the given provider/profile and returns the new expiry time.
func (r *PoolRefresher) Refresh(ctx context.Context, provider, profile string) (time.Time, error) {
	refreshErr := refresh.RefreshProfile(ctx, provider, profile, r.vault, r.healthStore)
	if refreshErr != nil && !refresh.IsDeliveryIncomplete(refreshErr) {
		return time.Time{}, refreshErr
	}

	// Expiry is scheduling metadata, not the outcome of the completed token
	// exchange. An opaque response may omit its lifetime, or a native writer
	// may replace the source before this read. Preserve renewal success (and
	// any delivery warning) rather than charging a pool error for that read.
	expiry, err := r.getTokenExpiry(provider, profile)
	if err != nil {
		return time.Time{}, refreshErr
	}

	return expiry, refreshErr
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
	case "cursor":
		expiryInfo, err = health.ParseCursorExpiry(filepath.Join(vaultPath, "auth.json"))
	case "opencode", "grok":
		// No token expiry parsing for these providers yet
		return time.Time{}, nil
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
