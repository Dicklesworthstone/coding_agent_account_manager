package authpool

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Refresher is the interface for token refresh implementations.
type Refresher interface {
	// Refresh refreshes the token for a profile.
	// Returns the new token expiry time on success.
	Refresh(ctx context.Context, provider, profile string) (time.Time, error)
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
	stopCh    chan struct{}
	refreshWg sync.WaitGroup
	semaphore chan struct{}
}

// NewMonitor creates a new token monitor.
func NewMonitor(pool *AuthPool, refresher Refresher, config MonitorConfig) *Monitor {
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
	m.stopCh = make(chan struct{})

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

	close(m.stopCh)
	m.running = false
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
	// Try to acquire semaphore (non-blocking)
	select {
	case m.semaphore <- struct{}{}:
		// Got slot, proceed
	default:
		// No slots available, skip this refresh cycle
		return
	}

	m.refreshWg.Add(1)
	go func() {
		defer m.refreshWg.Done()
		defer func() { <-m.semaphore }()

		m.doRefresh(ctx, provider, profile)
	}()
}

// doRefresh performs the actual refresh operation.
func (m *Monitor) doRefresh(ctx context.Context, provider, profile string) {
	// Mark as refreshing
	if err := m.pool.SetStatus(provider, profile, PoolStatusRefreshing); err != nil {
		return // Profile may have been removed
	}

	// Call start callback
	if m.config.OnRefreshStart != nil {
		m.config.OnRefreshStart(provider, profile)
	}

	// Check if refresher is available
	if m.refresher == nil {
		m.pool.SetError(provider, profile, fmt.Errorf("no refresher configured"))
		if m.config.OnRefreshComplete != nil {
			m.config.OnRefreshComplete(provider, profile, time.Time{}, fmt.Errorf("no refresher configured"))
		}
		return
	}

	// Perform refresh
	newExpiry, err := m.refresher.Refresh(ctx, provider, profile)

	if err != nil {
		m.pool.SetError(provider, profile, err)
		if m.config.OnRefreshComplete != nil {
			m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
		}
		return
	}

	// Success - update pool state
	m.pool.MarkRefreshed(provider, profile, newExpiry)

	if m.config.OnRefreshComplete != nil {
		m.config.OnRefreshComplete(provider, profile, newExpiry, nil)
	}
}

// ForceRefresh triggers an immediate refresh for a specific profile.
func (m *Monitor) ForceRefresh(ctx context.Context, provider, profile string) error {
	// Verify profile exists
	p := m.pool.GetProfile(provider, profile)
	if p == nil {
		return fmt.Errorf("profile %s/%s not found", provider, profile)
	}

	// Check if already refreshing
	if p.Status == PoolStatusRefreshing {
		return fmt.Errorf("profile %s/%s already refreshing", provider, profile)
	}

	// Perform refresh directly (synchronous)
	m.doRefresh(ctx, provider, profile)

	// Check result
	p = m.pool.GetProfile(provider, profile)
	if p != nil && p.Status == PoolStatusError {
		return fmt.Errorf("refresh failed: %s", p.ErrorMessage)
	}

	return nil
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
