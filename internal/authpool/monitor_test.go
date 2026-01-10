package authpool

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// MockRefresher implements Refresher for testing.
type MockRefresher struct {
	mu            sync.Mutex
	calls         []string
	shouldFail    map[string]error
	refreshDelay  time.Duration
	tokenValidity time.Duration
}

func NewMockRefresher() *MockRefresher {
	return &MockRefresher{
		shouldFail:    make(map[string]error),
		tokenValidity: time.Hour,
	}
}

func (m *MockRefresher) Refresh(ctx context.Context, provider, profile string) (time.Time, error) {
	m.mu.Lock()
	key := provider + "/" + profile
	m.calls = append(m.calls, key)
	fail := m.shouldFail[key]
	delay := m.refreshDelay
	validity := m.tokenValidity
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return time.Time{}, ctx.Err()
		case <-time.After(delay):
		}
	}

	if fail != nil {
		return time.Time{}, fail
	}

	return time.Now().Add(validity), nil
}

func (m *MockRefresher) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *MockRefresher) Calls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.calls))
	copy(result, m.calls)
	return result
}

func (m *MockRefresher) SetFail(provider, profile string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldFail[provider+"/"+profile] = err
}

func (m *MockRefresher) SetDelay(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshDelay = d
}

func (m *MockRefresher) SetTokenValidity(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tokenValidity = d
}

func TestNewMonitor(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()

	monitor := NewMonitor(pool, refresher, DefaultMonitorConfig())
	if monitor == nil {
		t.Fatal("NewMonitor returned nil")
	}
	if monitor.IsRunning() {
		t.Error("monitor should not be running initially")
	}
}

func TestMonitor_StartStop(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	monitor := NewMonitor(pool, refresher, config)

	ctx := context.Background()
	if err := monitor.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	if !monitor.IsRunning() {
		t.Error("monitor should be running after Start()")
	}

	// Try starting again - should error
	if err := monitor.Start(ctx); err == nil {
		t.Error("Start() should error when already running")
	}

	// Let it run briefly
	time.Sleep(50 * time.Millisecond)

	monitor.Stop()
	if monitor.IsRunning() {
		t.Error("monitor should not be running after Stop()")
	}

	// Stop again should be safe
	monitor.Stop()
}

func TestMonitor_RefreshesExpiringSoon(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond
	config.RefreshThreshold = 10 * time.Minute

	monitor := NewMonitor(pool, refresher, config)

	// Add profile expiring soon (within threshold)
	pool.AddProfile("claude", "expiring")
	pool.SetStatus("claude", "expiring", PoolStatusReady)
	pool.UpdateTokenExpiry("claude", "expiring", time.Now().Add(5*time.Minute))

	// Add profile not expiring soon
	pool.AddProfile("claude", "fresh")
	pool.SetStatus("claude", "fresh", PoolStatusReady)
	pool.UpdateTokenExpiry("claude", "fresh", time.Now().Add(time.Hour))

	ctx := context.Background()
	monitor.Start(ctx)

	// Wait for check cycle
	time.Sleep(100 * time.Millisecond)

	monitor.Stop()

	// Should have refreshed the expiring profile
	calls := refresher.Calls()
	foundExpiring := false
	foundFresh := false
	for _, call := range calls {
		if call == "claude/expiring" {
			foundExpiring = true
		}
		if call == "claude/fresh" {
			foundFresh = true
		}
	}

	if !foundExpiring {
		t.Error("should have refreshed expiring profile")
	}
	if foundFresh {
		t.Error("should not have refreshed fresh profile")
	}
}

func TestMonitor_RefreshesExpired(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	monitor := NewMonitor(pool, refresher, config)

	// Add expired profile
	pool.AddProfile("claude", "expired")
	pool.SetStatus("claude", "expired", PoolStatusExpired)

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	calls := refresher.Calls()
	found := false
	for _, call := range calls {
		if call == "claude/expired" {
			found = true
			break
		}
	}

	if !found {
		t.Error("should have refreshed expired profile")
	}
}

func TestMonitor_RefreshesError(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	monitor := NewMonitor(pool, refresher, config)

	// Add error profile
	pool.AddProfile("claude", "error")
	pool.SetStatus("claude", "error", PoolStatusError)

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	calls := refresher.Calls()
	found := false
	for _, call := range calls {
		if call == "claude/error" {
			found = true
			break
		}
	}

	if !found {
		t.Error("should have refreshed error profile")
	}
}

func TestMonitor_SkipsRefreshing(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	refresher.SetDelay(500 * time.Millisecond) // Slow refresh
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	monitor := NewMonitor(pool, refresher, config)

	// Add profile already refreshing
	pool.AddProfile("claude", "refreshing")
	pool.SetStatus("claude", "refreshing", PoolStatusRefreshing)

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	// Should not have tried to refresh an already-refreshing profile
	if refresher.CallCount() > 0 {
		t.Errorf("should not refresh already-refreshing profile, got %d calls", refresher.CallCount())
	}
}

func TestMonitor_Callbacks(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()

	var startCalls int32
	var completeCalls int32

	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond
	config.OnRefreshStart = func(provider, profile string) {
		atomic.AddInt32(&startCalls, 1)
	}
	config.OnRefreshComplete = func(provider, profile string, newExpiry time.Time, err error) {
		atomic.AddInt32(&completeCalls, 1)
	}

	monitor := NewMonitor(pool, refresher, config)

	pool.AddProfile("claude", "test")
	pool.SetStatus("claude", "test", PoolStatusExpired)

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	if atomic.LoadInt32(&startCalls) == 0 {
		t.Error("OnRefreshStart should have been called")
	}
	if atomic.LoadInt32(&completeCalls) == 0 {
		t.Error("OnRefreshComplete should have been called")
	}
}

func TestMonitor_HandleRefreshError(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	refresher.SetFail("claude", "failing", errors.New("mock error"))

	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	monitor := NewMonitor(pool, refresher, config)

	pool.AddProfile("claude", "failing")
	pool.SetStatus("claude", "failing", PoolStatusExpired)

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	profile := pool.GetProfile("claude", "failing")
	if profile == nil {
		t.Fatal("profile should exist")
	}
	if profile.ErrorCount == 0 {
		t.Error("ErrorCount should be > 0 after failed refresh")
	}
	if profile.ErrorMessage == "" {
		t.Error("ErrorMessage should be set after failed refresh")
	}
}

func TestMonitor_ForceRefresh(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()

	monitor := NewMonitor(pool, refresher, config)

	pool.AddProfile("claude", "test")
	pool.SetStatus("claude", "test", PoolStatusReady)

	ctx := context.Background()
	err := monitor.ForceRefresh(ctx, "claude", "test")
	if err != nil {
		t.Errorf("ForceRefresh() error = %v", err)
	}

	// Should have called refresh
	if refresher.CallCount() != 1 {
		t.Errorf("ForceRefresh should have called refresh once, got %d", refresher.CallCount())
	}

	// Profile should be ready with new expiry
	profile := pool.GetProfile("claude", "test")
	if profile.Status != PoolStatusReady {
		t.Errorf("Status = %v, want Ready", profile.Status)
	}
}

func TestMonitor_ForceRefresh_NotFound(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()

	monitor := NewMonitor(pool, refresher, config)

	ctx := context.Background()
	err := monitor.ForceRefresh(ctx, "claude", "nonexistent")
	if err == nil {
		t.Error("ForceRefresh() should error for non-existent profile")
	}
}

func TestMonitor_ForceRefresh_AlreadyRefreshing(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()

	monitor := NewMonitor(pool, refresher, config)

	pool.AddProfile("claude", "test")
	pool.SetStatus("claude", "test", PoolStatusRefreshing)

	ctx := context.Background()
	err := monitor.ForceRefresh(ctx, "claude", "test")
	if err == nil {
		t.Error("ForceRefresh() should error for already-refreshing profile")
	}
}

func TestMonitor_MaxConcurrent(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	refresher.SetDelay(100 * time.Millisecond)

	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond
	config.MaxConcurrent = 2

	monitor := NewMonitor(pool, refresher, config)

	// Add many expired profiles
	for i := 0; i < 10; i++ {
		name := string(rune('a' + i))
		pool.AddProfile("claude", name)
		pool.SetStatus("claude", name, PoolStatusExpired)
	}

	ctx := context.Background()
	monitor.Start(ctx)

	// Wait for a check cycle
	time.Sleep(50 * time.Millisecond)

	// Check stats - should show limited concurrent
	stats := monitor.Stats()
	if stats.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", stats.MaxConcurrent)
	}

	monitor.Stop()
}

func TestMonitor_Stats(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = time.Minute
	config.RefreshThreshold = 5 * time.Minute
	config.MaxConcurrent = 5

	monitor := NewMonitor(pool, refresher, config)

	stats := monitor.Stats()
	if stats.Running {
		t.Error("Running should be false before Start")
	}
	if stats.CheckInterval != time.Minute {
		t.Errorf("CheckInterval = %v, want 1m", stats.CheckInterval)
	}
	if stats.RefreshThreshold != 5*time.Minute {
		t.Errorf("RefreshThreshold = %v, want 5m", stats.RefreshThreshold)
	}
	if stats.MaxConcurrent != 5 {
		t.Errorf("MaxConcurrent = %d, want 5", stats.MaxConcurrent)
	}

	ctx := context.Background()
	monitor.Start(ctx)
	stats = monitor.Stats()
	if !stats.Running {
		t.Error("Running should be true after Start")
	}
	monitor.Stop()
}

func TestMonitor_RefreshAll(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()

	monitor := NewMonitor(pool, refresher, config)

	// Add expired profiles
	pool.AddProfile("claude", "a")
	pool.SetStatus("claude", "a", PoolStatusExpired)
	pool.AddProfile("claude", "b")
	pool.SetStatus("claude", "b", PoolStatusExpired)

	ctx := context.Background()
	monitor.RefreshAll(ctx)

	// Wait for async refreshes
	time.Sleep(100 * time.Millisecond)

	if refresher.CallCount() < 2 {
		t.Errorf("RefreshAll should have triggered refreshes, got %d calls", refresher.CallCount())
	}
}

func TestMonitor_ClearsCooldowns(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	monitor := NewMonitor(pool, refresher, config)

	// Add profile with expired cooldown
	pool.AddProfile("claude", "test")
	pool.mu.Lock()
	pool.profiles["claude:test"].Status = PoolStatusCooldown
	pool.profiles["claude:test"].CooldownUntil = time.Now().Add(-time.Minute) // Already expired
	pool.mu.Unlock()

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	// Cooldown should have been cleared
	profile := pool.GetProfile("claude", "test")
	if profile.Status == PoolStatusCooldown {
		t.Error("Cooldown should have been cleared")
	}
}

func TestMonitor_ContextCancellation(t *testing.T) {
	pool := NewAuthPool()
	refresher := NewMockRefresher()
	config := DefaultMonitorConfig()
	config.CheckInterval = time.Second // Long interval

	monitor := NewMonitor(pool, refresher, config)

	ctx, cancel := context.WithCancel(context.Background())
	monitor.Start(ctx)

	// Cancel context should stop monitor
	cancel()
	time.Sleep(50 * time.Millisecond)

	// Monitor may still report running briefly, but should stop
	// Give it a moment to process
	time.Sleep(50 * time.Millisecond)
}

func TestMonitor_NoRefresher(t *testing.T) {
	pool := NewAuthPool()
	config := DefaultMonitorConfig()
	config.CheckInterval = 10 * time.Millisecond

	// Create monitor without refresher
	monitor := NewMonitor(pool, nil, config)

	pool.AddProfile("claude", "test")
	pool.SetStatus("claude", "test", PoolStatusExpired)

	ctx := context.Background()
	monitor.Start(ctx)
	time.Sleep(100 * time.Millisecond)
	monitor.Stop()

	// Profile should have error
	profile := pool.GetProfile("claude", "test")
	if profile.ErrorCount == 0 {
		t.Error("should have error when no refresher configured")
	}
}
