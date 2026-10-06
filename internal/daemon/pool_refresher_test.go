package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func TestNewPoolRefresher(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	refresher := NewPoolRefresher(vault, store)
	if refresher == nil {
		t.Fatal("NewPoolRefresher returned nil")
	}
	if refresher.vault != vault {
		t.Error("vault not set correctly")
	}
	if refresher.healthStore != store {
		t.Error("healthStore not set correctly")
	}
}

func TestPoolRefresher_ImplementsInterface(t *testing.T) {
	// Verify PoolRefresher implements authpool.Refresher
	var _ authpool.Refresher = (*PoolRefresher)(nil)
}

func TestPoolRefresher_Refresh_ProfileNotFound(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")
	refresher := NewPoolRefresher(vault, store)

	ctx := context.Background()
	_, err := refresher.Refresh(ctx, "claude", "nonexistent")

	// Should fail because profile doesn't exist
	if err == nil {
		t.Error("expected error for nonexistent profile")
	}
}

func TestPoolRefresher_Refresh_UnsupportedProvider(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")
	refresher := NewPoolRefresher(vault, store)

	ctx := context.Background()
	_, err := refresher.Refresh(ctx, "unsupported", "profile")

	// Should fail for unsupported provider
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestDaemonWithAuthPool_New(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: time.Minute,
		UseAuthPool:      true,
	}

	d := New(vault, store, cfg)
	if d == nil {
		t.Fatal("New returned nil")
	}
	if d.authPool == nil {
		t.Error("authPool should be initialized when UseAuthPool is true")
	}
	if d.poolMonitor == nil {
		t.Error("poolMonitor should be initialized when UseAuthPool is true")
	}
}

func TestDaemonWithAuthPool_NotEnabled(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: time.Minute,
		UseAuthPool:      false,
	}

	d := New(vault, store, cfg)
	if d == nil {
		t.Fatal("New returned nil")
	}
	if d.authPool != nil {
		t.Error("authPool should be nil when UseAuthPool is false")
	}
	if d.poolMonitor != nil {
		t.Error("poolMonitor should be nil when UseAuthPool is false")
	}
}

func TestDaemonWithAuthPool_Stats(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: time.Minute,
		UseAuthPool:      true,
	}

	d := New(vault, store, cfg)

	stats := d.GetStats()
	if !stats.PoolEnabled {
		t.Error("PoolEnabled should be true")
	}
	if stats.PoolSummary == nil {
		t.Error("PoolSummary should not be nil")
	}
}

func TestDaemonWithAuthPool_GetAuthPool(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
		UseAuthPool:   true,
	}

	d := New(vault, store, cfg)

	pool := d.GetAuthPool()
	if pool == nil {
		t.Error("GetAuthPool should return the pool")
	}
}

func TestDaemonWithAuthPool_GetPoolMonitor(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
		UseAuthPool:   true,
	}

	d := New(vault, store, cfg)

	monitor := d.GetPoolMonitor()
	if monitor == nil {
		t.Error("GetPoolMonitor should return the monitor")
	}
}

func TestDaemon_MaxConcurrentRefreshes(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval:          50 * time.Millisecond,
		UseAuthPool:            true,
		MaxConcurrentRefreshes: 5,
	}

	d := New(vault, store, cfg)

	monitor := d.GetPoolMonitor()
	if monitor == nil {
		t.Fatal("GetPoolMonitor should return the monitor")
	}

	stats := monitor.Stats()
	if stats.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent = %d, want 5", stats.MaxConcurrent)
	}
}

func TestDaemon_DefaultMaxConcurrent(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
		UseAuthPool:   true,
		// MaxConcurrentRefreshes not set, should default to 3
	}

	d := New(vault, store, cfg)

	monitor := d.GetPoolMonitor()
	if monitor == nil {
		t.Fatal("GetPoolMonitor should return the monitor")
	}

	stats := monitor.Stats()
	if stats.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3 (default)", stats.MaxConcurrent)
	}
}

func TestPoolRefresher_GetTokenExpiry_UnknownProvider(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")
	refresher := NewPoolRefresher(vault, store)

	_, err := refresher.getTokenExpiry("unknown", "profile")
	if err == nil {
		t.Error("expected error for unknown provider")
	}
	if err.Error() != "unknown provider: unknown" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestPoolRefresher_GetTokenExpiry_ClaudeNoFile(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")
	refresher := NewPoolRefresher(vault, store)

	_, err := refresher.getTokenExpiry("claude", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent claude profile")
	}
}

func TestPoolRefresher_GetTokenExpiry_CodexNoFile(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")
	refresher := NewPoolRefresher(vault, store)

	_, err := refresher.getTokenExpiry("codex", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent codex profile")
	}
}

func TestPoolRefresher_GetTokenExpiry_GeminiNoFile(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")
	refresher := NewPoolRefresher(vault, store)

	_, err := refresher.getTokenExpiry("gemini", "nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent gemini profile")
	}
}

func TestPoolRefresherGetCursorExpiry(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	refresher := NewPoolRefresher(vault, nil)
	expiry := time.Now().Add(48 * time.Hour).Truncate(time.Second)
	path := filepath.Join(vault.ProfilePath("cursor", "work"), "auth.json")
	writeDaemonCursorAuth(t, path, expiry, "session", "")
	got, err := refresher.getTokenExpiry("cursor", "work")
	if err != nil || !got.Equal(expiry) {
		t.Fatalf("Cursor expiry = %v, %v; want %v", got, err, expiry)
	}
	if _, err := refresher.getTokenExpiry("cursor", "missing"); err == nil {
		t.Error("missing Cursor auth must not report a successful zero expiry")
	}
}

func TestDaemonWithAuthPool_Verbose(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(t.TempDir() + "/health.json")

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: time.Minute,
		UseAuthPool:      true,
		Verbose:          true,
	}

	d := New(vault, store, cfg)
	if d == nil {
		t.Fatal("New returned nil")
	}
	if d.authPool == nil {
		t.Error("authPool should be initialized when UseAuthPool is true")
	}
}
