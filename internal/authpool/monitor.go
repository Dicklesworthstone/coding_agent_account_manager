package authpool

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// Refresher is the interface for token refresh implementations.
type Refresher interface {
	// Refresh refreshes the token for a profile.
	// Returns the new token expiry time on success.
	Refresh(ctx context.Context, provider, profile string) (time.Time, error)
}

// Preflighter lets a refresher reject unsafe or unsupported work before the
// monitor changes pool state or announces a refresh attempt.
type Preflighter interface {
	Preflight(provider, profile string) error
}

// MonitorConfig configures the background monitor loop.
type MonitorConfig struct {
	// CheckInterval is how often to check profile states.
	// Default: 1 minute
	CheckInterval time.Duration

	// RefreshThreshold is how early to refresh before expiry.
	// Default: 5 minutes (from AuthPool)
	RefreshThreshold time.Duration

	// MaxConcurrent limits concurrent refresh operations.
	// Default: 3
	MaxConcurrent int

	// OnRefreshStart is called when a refresh starts.
	OnRefreshStart func(provider, profile string)

	// OnRefreshComplete is called when a refresh completes.
	OnRefreshComplete func(provider, profile string, newExpiry time.Time, err error)

	// OnRefreshSkipped reports a non-attempt. It does not change error counters.
	OnRefreshSkipped func(provider, profile string, err error)
}

// DefaultMonitorConfig returns the default monitor configuration.
func DefaultMonitorConfig() MonitorConfig {
	return MonitorConfig{
		CheckInterval:    time.Minute,
		RefreshThreshold: 5 * time.Minute,
		MaxConcurrent:    3,
	}
}

// Monitor runs a background token monitoring loop.
type Monitor struct {
	pool      *AuthPool
	refresher Refresher
	config    MonitorConfig

	// State
	mu        sync.Mutex
	running   bool
	stopping  bool // Set when Stop() is called, prevents new refreshes
	stopCh    chan struct{}
	stopOnce  sync.Once // Ensures stopCh is only closed once
	refreshWg sync.WaitGroup
	semaphore chan struct{}
}

// NewMonitor creates a new token monitor.
// Panics if pool is nil.
func NewMonitor(pool *AuthPool, refresher Refresher, config MonitorConfig) *Monitor {
	if pool == nil {
		panic("authpool: NewMonitor called with nil pool")
	}

	if config.CheckInterval == 0 {
		config.CheckInterval = time.Minute
	}
	if config.RefreshThreshold == 0 {
		config.RefreshThreshold = pool.refreshThreshold
	}
	if config.MaxConcurrent == 0 {
		config.MaxConcurrent = 3
	}

	return &Monitor{
		pool:      pool,
		refresher: refresher,
		config:    config,
		semaphore: make(chan struct{}, config.MaxConcurrent),
	}
}

// Start begins the background monitoring loop.
// Returns immediately. Use Stop() to stop the loop.
func (m *Monitor) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("monitor already running")
	}

	m.running = true
	m.stopping = false // Reset stopping flag for new cycle
	m.stopCh = make(chan struct{})
	m.stopOnce = sync.Once{} // Reset for new Start cycle

	go m.runLoop(ctx)
	return nil
}

// Stop stops the background monitoring loop.
// Waits for any in-flight refresh operations to complete.
func (m *Monitor) Stop() {
	m.mu.Lock()
	if !m.running {
		m.mu.Unlock()
		return
	}

	// Set stopping flag BEFORE releasing lock to prevent new refreshes
	// from starting. This prevents race with refreshWg.Wait() below.
	m.stopping = true
	m.running = false

	// Use sync.Once to ensure stopCh is only closed once, preventing panic
	// if Stop() is called concurrently or multiple times.
	m.stopOnce.Do(func() {
		close(m.stopCh)
	})
	m.mu.Unlock()

	// Wait for in-flight refreshes to complete
	m.refreshWg.Wait()
}

// IsRunning returns whether the monitor is running.
func (m *Monitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running
}

// runLoop is the main monitoring loop.
func (m *Monitor) runLoop(ctx context.Context) {
	ticker := time.NewTicker(m.config.CheckInterval)
	defer ticker.Stop()

	// Run initial check immediately
	m.checkAndRefresh(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.checkAndRefresh(ctx)
		}
	}
}

// checkAndRefresh checks all profiles and triggers refresh for those needing it.
func (m *Monitor) checkAndRefresh(ctx context.Context) {
	// Clear expired cooldowns first
	m.pool.CheckAndUpdateCooldowns()

	// Get profiles that need refresh
	profiles := m.pool.GetProfilesNeedingRefresh("")

	for _, profile := range profiles {
		// Cursor sessions require a login; API-backed credentials renew through Cursor.
		// Neither supports the CAAM refresh adapter. The daemon emits session warnings.
		if profile.Provider == "cursor" {
			continue
		}
		// Skip if already refreshing
		if profile.Status == PoolStatusRefreshing {
			continue
		}

		// Check if expiring soon or already expired/error
		needsRefresh := profile.IsExpiringSoon(m.config.RefreshThreshold) ||
			profile.Status == PoolStatusExpired ||
			profile.Status == PoolStatusError

		if needsRefresh {
			m.triggerRefresh(ctx, profile.Provider, profile.ProfileName)
		}
	}
}

// triggerRefresh starts a refresh operation for a profile.
func (m *Monitor) triggerRefresh(ctx context.Context, provider, profile string) {
	if provider == "cursor" {
		return
	}

	// Try to acquire semaphore (non-blocking)
	select {
	case m.semaphore <- struct{}{}:
		// Got slot, proceed
	default:
		// No slots available, skip this refresh cycle
		return
	}
	if err := m.preflight(provider, profile); err != nil {
		<-m.semaphore
		return
	}

	prevStatus, reserved := m.pool.TryMarkRefreshing(provider, profile)
	if !reserved {
		<-m.semaphore
		return
	}

	// Check if we're stopping before adding to WaitGroup to prevent race
	// with Stop() calling refreshWg.Wait(). Must hold lock to safely check
	// stopping flag and add to WaitGroup atomically.
	// Note: we check stopping (not running) to allow RefreshAll to work
	// when the monitor hasn't been started.
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		// Revert the optimistic status change so the pool doesn't stay stuck.
		if prevStatus != PoolStatusRefreshing {
			m.pool.restoreRefreshStatus(provider, profile, prevStatus)
		}
		<-m.semaphore
		return
	}
	m.refreshWg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.refreshWg.Done()
		defer func() { <-m.semaphore }()

		_ = m.doRefresh(ctx, provider, profile, prevStatus)
	}()
}

// preflight checks the current credential on every request. Skips preserve the
// pool entry so a later backup can make the profile refreshable again.
func (m *Monitor) preflight(provider, profile string) error {
	var err error
	if m.refresher == nil {
		err = fmt.Errorf("no refresher configured")
	} else if checker, ok := m.refresher.(Preflighter); ok {
		err = checker.Preflight(provider, profile)
	}
	if err != nil {
		if refresh.IsSkipped(err) {
			if m.config.OnRefreshSkipped != nil {
				m.config.OnRefreshSkipped(provider, profile, err)
			}
		} else {
			m.pool.SetError(provider, profile, err)
			if m.config.OnRefreshComplete != nil {
				m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
			}
		}
	}
	return err
}

// doRefresh performs the actual refresh operation.
func (m *Monitor) doRefresh(ctx context.Context, provider, profile string, prevStatus PoolStatus) error {
	// The caller reserved the profile already. Do not overwrite a cooldown
	// recorded after reservation while this goroutine was waiting to run.
	if m.pool.GetProfile(provider, profile) == nil {
		return fmt.Errorf("profile %s/%s not found", provider, profile)
	}

	// Call start callback
	if m.config.OnRefreshStart != nil {
		m.config.OnRefreshStart(provider, profile)
	}

	// Perform refresh
	newExpiry, err := m.refresher.Refresh(ctx, provider, profile)

	if err != nil {
		m.pool.restoreRefreshStatus(provider, profile, prevStatus)
		if refresh.IsSkipped(err) {
			if m.config.OnRefreshComplete != nil {
				m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
			}
			return err
		}
		m.pool.SetError(provider, profile, err)
		if m.config.OnRefreshComplete != nil {
			m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
		}
		return err
	}

	// Success - update pool state
	m.pool.MarkRefreshed(provider, profile, newExpiry)

	if m.config.OnRefreshComplete != nil {
		m.config.OnRefreshComplete(provider, profile, newExpiry, nil)
	}
	return nil
}

// ForceRefresh triggers an immediate refresh for a specific profile.
// This respects the MaxConcurrent semaphore to prevent overwhelming the system.
func (m *Monitor) ForceRefresh(ctx context.Context, provider, profile string) error {
	if provider == "cursor" {
		return fmt.Errorf("automatic refresh is unavailable for %s/%s", provider, profile)
	}

	// Acquire semaphore slot (blocking, with context cancellation)
	select {
	case m.semaphore <- struct{}{}:
		// Got slot, proceed
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.semaphore }()

	// Participate in WaitGroup for graceful shutdown
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return fmt.Errorf("monitor is stopping")
	}
	m.refreshWg.Add(1)
	m.mu.Unlock()
	defer m.refreshWg.Done()
	if err := m.preflight(provider, profile); err != nil {
		return err
	}

	// Try to mark as refreshing. This checks existence AND current status atomically.
	// We do this AFTER acquiring the semaphore to ensure we don't hold the lock
	// while waiting for the semaphore, but also to prevent races where another
	// routine starts refreshing while we wait.
	previous, reserved := m.pool.TryMarkRefreshing(provider, profile)
	if !reserved {
		// Need to distinguish "not found" vs "already refreshing"
		p := m.pool.GetProfile(provider, profile)
		if p == nil {
			return fmt.Errorf("profile %s/%s not found", provider, profile)
		}
		return fmt.Errorf("profile %s/%s already refreshing", provider, profile)
	}

	// Perform refresh (synchronous)
	return m.doRefresh(ctx, provider, profile, previous)
}

// RefreshAll triggers refresh for all profiles that need it.
// This is useful for startup or manual refresh.
func (m *Monitor) RefreshAll(ctx context.Context) {
	m.checkAndRefresh(ctx)
}

// Stats returns current monitor statistics.
type MonitorStats struct {
	Running          bool          `json:"running"`
	CheckInterval    time.Duration `json:"check_interval"`
	RefreshThreshold time.Duration `json:"refresh_threshold"`
	MaxConcurrent    int           `json:"max_concurrent"`
	ActiveRefreshes  int           `json:"active_refreshes"`
}

// Stats returns current monitor statistics.
func (m *Monitor) Stats() MonitorStats {
	m.mu.Lock()
	running := m.running
	m.mu.Unlock()

	// Count active refreshes (approximate - semaphore capacity minus available)
	activeRefreshes := len(m.semaphore)

	return MonitorStats{
		Running:          running,
		CheckInterval:    m.config.CheckInterval,
		RefreshThreshold: m.config.RefreshThreshold,
		MaxConcurrent:    m.config.MaxConcurrent,
		ActiveRefreshes:  activeRefreshes,
	}
}

// WithCheckInterval sets the check interval option.
func WithCheckInterval(d time.Duration) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.CheckInterval = d
	}
}

// WithMonitorRefreshThreshold sets the refresh threshold option.
func WithMonitorRefreshThreshold(d time.Duration) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.RefreshThreshold = d
	}
}

// WithMaxConcurrent sets the max concurrent refreshes option.
func WithMaxConcurrent(n int) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.MaxConcurrent = n
	}
}

// WithOnRefreshStart sets the refresh start callback.
func WithOnRefreshStart(fn func(provider, profile string)) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.OnRefreshStart = fn
	}
}

// WithOnRefreshComplete sets the refresh complete callback.
func WithOnRefreshComplete(fn func(provider, profile string, newExpiry time.Time, err error)) func(*MonitorConfig) {
	return func(c *MonitorConfig) {
		c.OnRefreshComplete = fn
	}
}
