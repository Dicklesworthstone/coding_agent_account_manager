package authpool

import (
	"context"
	"errors"
	"fmt"
	"sort"
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

	// RefreshTimeout bounds each attempt, including a credential-lock wait.
	// Zero leaves the deadline to the caller.
	RefreshTimeout time.Duration

	// OnReconcileError reports a failed vault inventory. No stale candidates
	// are dispatched after a failed check.
	OnReconcileError func(error)

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
	lifecycle  sync.Mutex // Serializes Start against a joining Stop.
	mu         sync.Mutex
	running    bool
	stopping   bool // Set when Stop() is called, prevents new refreshes
	ctx        context.Context
	cancel     context.CancelFunc
	loopDone   chan struct{}
	stopParent func() bool
	refreshWg  sync.WaitGroup
	semaphore  chan struct{}
}

// NewMonitor creates a new token monitor.
// Panics if pool is nil.
func NewMonitor(pool *AuthPool, refresher Refresher, config MonitorConfig) *Monitor {
	if pool == nil {
		panic("authpool: NewMonitor called with nil pool")
	}

	if config.CheckInterval <= 0 {
		config.CheckInterval = time.Minute
	}
	if config.RefreshThreshold == 0 {
		config.RefreshThreshold = pool.refreshThreshold
	}
	if config.MaxConcurrent <= 0 {
		config.MaxConcurrent = 3
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Monitor{
		pool:      pool,
		refresher: refresher,
		config:    config,
		semaphore: make(chan struct{}, config.MaxConcurrent),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start begins the background monitoring loop.
// Returns immediately. Use Stop() to stop the loop.
func (m *Monitor) Start(ctx context.Context) error {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		return fmt.Errorf("monitor already running")
	}

	m.running = true
	m.stopping = false // Reset stopping flag for new cycle
	if m.ctx.Err() != nil {
		m.ctx, m.cancel = context.WithCancel(context.Background())
	}
	m.stopParent = context.AfterFunc(ctx, m.cancel)
	m.loopDone = make(chan struct{})

	go func(runCtx context.Context, done chan struct{}) {
		defer close(done)
		m.runLoop(runCtx)
	}(m.ctx, m.loopDone)
	return nil
}

// Stop stops the background monitoring loop.
// Waits for any in-flight refresh operations to complete.
func (m *Monitor) Stop() {
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	m.mu.Lock()

	// Set stopping flag BEFORE releasing lock to prevent new refreshes
	// from starting. This prevents race with refreshWg.Wait() below.
	m.stopping = true
	m.cancel()
	if m.stopParent != nil {
		m.stopParent()
	}
	done := m.loopDone
	m.mu.Unlock()

	// Cancel before joining: refresh can be waiting on another process's
	// credential lock rather than an HTTP timeout.
	if done != nil {
		<-done
	}
	m.refreshWg.Wait()
	m.mu.Lock()
	m.running = false
	m.mu.Unlock()
}

// IsRunning returns whether the monitor is running.
func (m *Monitor) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running && m.ctx.Err() == nil
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
		case <-ticker.C:
			m.checkAndRefresh(ctx)
		}
	}
}

// checkAndRefresh checks all profiles and triggers refresh for those needing it.
func (m *Monitor) checkAndRefresh(ctx context.Context) {
	if err := m.reconcile(ctx); err != nil {
		if m.config.OnReconcileError != nil && ctx.Err() == nil {
			m.config.OnReconcileError(err)
		}
		return
	}
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

func (m *Monitor) reconcile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if m.pool.vault != nil {
		return m.pool.LoadFromVault(ctx)
	}
	return nil
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

	prevStatus, reservation, reserved := m.pool.reserveRefresh(provider, profile)
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
			m.pool.finishRefresh(provider, profile, reservation, prevStatus, time.Time{}, false, nil)
		}
		<-m.semaphore
		return
	}
	m.refreshWg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.refreshWg.Done()
		defer func() { <-m.semaphore }()

		_ = m.doRefresh(ctx, provider, profile, prevStatus, reservation)
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
func (m *Monitor) doRefresh(ctx context.Context, provider, profile string, prevStatus PoolStatus, reservation uint64) error {
	if m.config.RefreshTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, m.config.RefreshTimeout)
		defer cancel()
	}
	// Results describe the attempted source. Re-read after releasing its
	// reservation so a concurrent replacement or deletion wins in the pool.
	defer func() {
		if err := m.reconcile(context.Background()); err != nil && m.config.OnReconcileError != nil {
			m.config.OnReconcileError(err)
		}
	}()
	// The caller reserved the profile already. Do not overwrite a cooldown
	// recorded after reservation while this goroutine was waiting to run.
	current := m.pool.GetProfile(provider, profile)
	if current == nil || current.reservation != reservation {
		return fmt.Errorf("%w: profile %s/%s was replaced", refresh.ErrCredentialChanged, provider, profile)
	}

	// Call start callback
	if m.config.OnRefreshStart != nil {
		m.config.OnRefreshStart(provider, profile)
	}

	// Perform refresh
	newExpiry, err := m.refresher.Refresh(ctx, provider, profile)
	m.pool.finishRefresh(provider, profile, reservation, prevStatus, newExpiry, true, err)

	if err != nil && !refresh.IsDeliveryIncomplete(err) {
		if errors.Is(err, context.Canceled) {
			if m.config.OnRefreshSkipped != nil {
				m.config.OnRefreshSkipped(provider, profile, err)
			}
			return err
		}
		if refresh.IsSkipped(err) {
			if m.config.OnRefreshComplete != nil {
				m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
			}
			return err
		}
		if m.config.OnRefreshComplete != nil {
			m.config.OnRefreshComplete(provider, profile, time.Time{}, err)
		}
		return err
	}

	// The vault grant is renewed even when a live destination changed during
	// the request. Keep it eligible and report that delivery warning separately.

	if m.config.OnRefreshComplete != nil {
		m.config.OnRefreshComplete(provider, profile, newExpiry, err)
	}
	return err
}

// beginOperation binds a whole operation to one monitor lifecycle. In
// particular, a batch remains joined between jobs and cannot resume against a
// new lifecycle after Stop returns and Start creates a fresh context.
func (m *Monitor) beginOperation(ctx context.Context) (context.Context, func(), error) {
	m.mu.Lock()
	if m.stopping {
		m.mu.Unlock()
		return nil, nil, fmt.Errorf("monitor is stopping: %w", context.Canceled)
	}
	owned := m.ctx
	m.refreshWg.Add(1)
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(owned, cancel)
	if owned.Err() != nil {
		cancel()
	}
	return ctx, func() {
		stop()
		cancel()
		m.refreshWg.Done()
	}, nil
}

// ForceRefresh triggers an immediate refresh for a specific profile.
// This respects the MaxConcurrent semaphore to prevent overwhelming the system.
func (m *Monitor) ForceRefresh(ctx context.Context, provider, profile string) error {
	ctx, finish, err := m.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer finish()
	if err := m.reconcile(ctx); err != nil {
		return err
	}
	current := m.pool.GetProfile(provider, profile)
	if current == nil {
		return fmt.Errorf("profile %s/%s not found", provider, profile)
	}
	if err := current.refreshEligibilityError(); err != nil {
		if refresh.IsSkipped(err) && m.config.OnRefreshSkipped != nil {
			m.config.OnRefreshSkipped(provider, profile, err)
		}
		return err
	}

	// Acquire semaphore slot (blocking, with context cancellation)
	select {
	case m.semaphore <- struct{}{}:
		// Got slot, proceed
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.semaphore }()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.preflight(provider, profile); err != nil {
		return err
	}

	// Try to mark as refreshing. This checks existence AND current status atomically.
	// We do this AFTER acquiring the semaphore to ensure we don't hold the lock
	// while waiting for the semaphore, but also to prevent races where another
	// routine starts refreshing while we wait.
	previous, reservation, reserved := m.pool.reserveRefresh(provider, profile)
	if !reserved {
		// Need to distinguish "not found" vs "already refreshing"
		p := m.pool.GetProfile(provider, profile)
		if p == nil {
			return fmt.Errorf("profile %s/%s not found", provider, profile)
		}
		if err := p.refreshEligibilityError(); err != nil {
			if refresh.IsSkipped(err) && m.config.OnRefreshSkipped != nil {
				m.config.OnRefreshSkipped(provider, profile, err)
			}
			return err
		}
		return fmt.Errorf("profile %s/%s already refreshing", provider, profile)
	}

	// Perform refresh (synchronous)
	return m.doRefresh(ctx, provider, profile, previous, reservation)
}

// RefreshResult is one completed manual batch outcome. Err may describe a
// skipped native/session credential or successful renewal with a delivery warning.
type RefreshResult struct {
	Provider string
	Profile  string
	Expiry   time.Time
	Err      error
}

// RefreshAll attempts every eligible profile, including unexpired renewable
// grants, and joins the bounded batch before returning. Results are stable by
// provider/name; unsupported profiles remain visible as skipped outcomes.
func (m *Monitor) RefreshAll(ctx context.Context) ([]RefreshResult, error) {
	ctx, finish, err := m.beginOperation(ctx)
	if err != nil {
		return nil, err
	}
	defer finish()
	if err := m.reconcile(ctx); err != nil {
		return nil, err
	}
	profiles := m.pool.GetAllProfiles("")
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Key() < profiles[j].Key() })
	results := make([]RefreshResult, len(profiles))
	var batch sync.WaitGroup
	jobs := make(chan int)
	for range min(m.config.MaxConcurrent, len(profiles)) {
		batch.Add(1)
		go func() {
			defer batch.Done()
			for i := range jobs {
				p := profiles[i]
				err := m.ForceRefresh(ctx, p.Provider, p.ProfileName)
				result := RefreshResult{Provider: p.Provider, Profile: p.ProfileName, Err: err}
				if current := m.pool.GetProfile(p.Provider, p.ProfileName); current != nil {
					result.Expiry = current.TokenExpiry
				}
				results[i] = result
			}
		}()
	}
dispatch:
	for i := range profiles {
		select {
		case jobs <- i:
		case <-ctx.Done():
			for j := i; j < len(profiles); j++ {
				results[j] = RefreshResult{Provider: profiles[j].Provider, Profile: profiles[j].ProfileName, Err: ctx.Err()}
			}
			break dispatch
		}
	}
	close(jobs)
	batch.Wait()
	return results, ctx.Err()
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
