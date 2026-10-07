// Package daemon provides a background service for proactive token management.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/update"
)

// DefaultCheckInterval is the default time between refresh checks.
const DefaultCheckInterval = 5 * time.Minute

// DefaultRefreshThreshold is how long before expiry to trigger a refresh.
const DefaultRefreshThreshold = 30 * time.Minute

// Config holds daemon configuration.
type Config struct {
	// CheckInterval is how often to check for profiles needing refresh.
	CheckInterval time.Duration

	// RefreshThreshold is how long before expiry to trigger refresh.
	RefreshThreshold time.Duration

	// Verbose enables debug logging.
	Verbose bool

	// LogPath is the path to write daemon logs (empty for stdout).
	LogPath string

	// UseAuthPool enables the new AuthPool-based token monitoring.
	// When enabled, the daemon uses authpool.Monitor for proactive refresh.
	UseAuthPool bool

	// MaxConcurrentRefreshes limits concurrent refresh operations when using AuthPool.
	// Default: 3
	MaxConcurrentRefreshes int

	// Notifier receives alerts for credentials that need attention (failed
	// refreshes, rejected logins, expiring sessions). Repeats of the same
	// alert are suppressed for AlertInterval. Nil disables alerts.
	Notifier notify.Notifier

	// AlertInterval is the minimum time between identical alerts.
	// Default: 1h
	AlertInterval time.Duration

	// UpdateChecker, when set, is asked every UpdateCheckInterval whether a
	// newer caam release exists; each new version is announced once through
	// Notifier. Nothing is installed.
	UpdateChecker UpdateChecker

	// UpdateCheckInterval is the time between update checks. Default: 24h
	UpdateCheckInterval time.Duration
}

// UpdateChecker reports whether a newer caam release exists.
type UpdateChecker interface {
	Check(ctx context.Context) (*update.CheckResult, error)
}

// DefaultAlertInterval keeps a condition that persists across checks from
// re-alerting every check interval.
const DefaultAlertInterval = time.Hour

// DefaultConfig returns the default daemon configuration.
func DefaultConfig() *Config {
	return &Config{
		CheckInterval:    DefaultCheckInterval,
		RefreshThreshold: DefaultRefreshThreshold,
		Verbose:          false,
	}
}

// Daemon manages background token refresh.
type Daemon struct {
	config      *Config
	vault       *authfile.Vault
	healthStore *health.Storage
	logger      *log.Logger
	logFile     *os.File // Log file handle for cleanup
	pidFile     *os.File // PID file handle for locking
	notifier    notify.Notifier

	// backupScheduler handles automatic backups (may be nil if disabled)
	backupScheduler *BackupScheduler

	// lastUpdateCheck and announcedVersion pace update checks and keep each
	// new release to one announcement. Used only by the run loop.
	lastUpdateCheck  time.Time
	announcedVersion string

	// authPool manages pooled profile states (may be nil if not enabled)
	authPool *authpool.AuthPool

	// poolMonitor runs the background token monitoring (may be nil if not enabled)
	poolMonitor *authpool.Monitor

	ctx           context.Context
	cancel        context.CancelFunc
	configChanged chan struct{} // Signal to reload config in runLoop
	wg            sync.WaitGroup

	mu      sync.Mutex
	running bool
	stats   Stats

	// reloginWarnings remembers logins already warned about across vault and
	// live aliases. It is protected by mu; replacement logins have new fingerprints.
	reloginWarnings map[string]string

	// refreshSkips suppresses repeated notices, never refresh eligibility checks.
	// It is protected by mu; a changed reason is reported on the next check.
	refreshSkips map[string]string

	configMu sync.RWMutex // Protects config access during runtime reloads
}

// Stats tracks daemon activity.
type Stats struct {
	StartTime       time.Time
	LastCheck       time.Time
	CheckCount      int64
	RefreshCount    int64
	RefreshErrors   int64
	ProfilesChecked int64

	// Backup stats
	LastBackup    time.Time
	NextBackup    time.Time
	BackupCount   int64
	BackupErrors  int64
	BackupEnabled bool

	// Pool stats (when UseAuthPool is enabled)
	PoolEnabled       bool
	PoolMonitorActive bool
	PoolSummary       *authpool.PoolSummary
}

// getCheckInterval returns the check interval with proper locking.
func (d *Daemon) getCheckInterval() time.Duration {
	d.configMu.RLock()
	defer d.configMu.RUnlock()
	return d.config.CheckInterval
}

// getRefreshThreshold returns the refresh threshold with proper locking.
func (d *Daemon) getRefreshThreshold() time.Duration {
	d.configMu.RLock()
	defer d.configMu.RUnlock()
	return d.config.RefreshThreshold
}

// isVerbose returns the verbose setting with proper locking.
func (d *Daemon) isVerbose() bool {
	d.configMu.RLock()
	defer d.configMu.RUnlock()
	return d.config.Verbose
}

// New creates a new daemon instance.
func New(vault *authfile.Vault, healthStore *health.Storage, cfg *Config) *Daemon {
	if vault != nil && healthStore != nil {
		healthStore.SetVaultPath(vault.BasePath())
	}
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.CheckInterval <= 0 {
		cfg.CheckInterval = DefaultCheckInterval
	}
	if cfg.RefreshThreshold <= 0 {
		cfg.RefreshThreshold = DefaultRefreshThreshold
	}

	logger := log.New(os.Stdout, "[caam-daemon] ", log.LstdFlags)
	var logFile *os.File
	if cfg.LogPath != "" {
		f, err := os.OpenFile(cfg.LogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err == nil {
			logger = log.New(f, "[caam-daemon] ", log.LstdFlags)
			logFile = f
		}
	}

	notifier := notify.Nop()
	if cfg.Notifier != nil {
		interval := cfg.AlertInterval
		if interval <= 0 {
			interval = DefaultAlertInterval
		}
		notifier = notify.NewThrottled(cfg.Notifier, interval)
	}

	d := &Daemon{
		config:      cfg,
		vault:       vault,
		healthStore: healthStore,
		logger:      logger,
		logFile:     logFile,
		notifier:    notifier,
	}

	// Initialize backup scheduler from global config
	globalCfg, err := config.Load()
	if err == nil && globalCfg.Backup.IsEnabled() {
		d.backupScheduler = NewBackupScheduler(&globalCfg.Backup, vault.BasePath(), logger)
		if loadErr := d.backupScheduler.LoadState(); loadErr != nil {
			logger.Printf("Warning: failed to load backup state: %v", loadErr)
		}
	}

	// Initialize auth pool if enabled
	if cfg.UseAuthPool {
		d.initAuthPool()
	}

	return d
}

// initAuthPool sets up the AuthPool and its monitor.
func (d *Daemon) initAuthPool() {
	// Create pool with callbacks for logging
	d.authPool = authpool.NewAuthPool(
		authpool.WithVault(d.vault),
		authpool.WithHealthStorage(d.healthStore),
		authpool.WithRefreshThreshold(d.config.RefreshThreshold),
		authpool.WithOnStateChange(func(profile *authpool.PooledProfile, oldStatus, newStatus authpool.PoolStatus) {
			if d.isVerbose() {
				d.logger.Printf("Pool: %s/%s status changed: %s -> %s",
					profile.Provider, profile.ProfileName, oldStatus, newStatus)
			}
		}),
	)

	// Load existing state if available
	stateOpts := authpool.PersistOptions{}
	if err := d.authPool.Load(stateOpts); err != nil {
		d.logger.Printf("Warning: failed to load pool state: %v", err)
	}

	// Create refresher
	refresher := NewPoolRefresher(d.vault, d.healthStore)

	// Configure monitor
	maxConcurrent := d.config.MaxConcurrentRefreshes
	if maxConcurrent <= 0 {
		maxConcurrent = 3
	}

	monitorConfig := authpool.MonitorConfig{
		CheckInterval:    d.config.CheckInterval,
		RefreshThreshold: d.config.RefreshThreshold,
		MaxConcurrent:    maxConcurrent,
		RefreshTimeout:   30 * time.Second,
		OnReconcileError: func(err error) {
			d.logger.Printf("Pool: vault reconciliation failed: %v", err)
		},
		OnRefreshStart: func(provider, profile string) {
			d.logger.Printf("Pool: starting refresh for %s/%s", provider, profile)
		},
		OnRefreshSkipped: func(provider, profile string, err error) {
			d.logRefreshSkip(provider, profile, err)
		},
		OnRefreshComplete: func(provider, profile string, newExpiry time.Time, err error) {
			if refresh.IsSkipped(err) {
				d.logger.Printf("Pool: %s/%s refresh skipped: %v", provider, profile, err)
				return
			}
			d.mu.Lock()
			if err != nil && !refresh.IsDeliveryIncomplete(err) {
				d.stats.RefreshErrors++
				d.mu.Unlock()
				d.logger.Printf("Pool: %s/%s refresh failed: %v", provider, profile, err)
			} else {
				d.stats.RefreshCount++
				d.mu.Unlock()
				d.logger.Printf("Pool: %s/%s refreshed, expires %v",
					provider, profile, newExpiry.Format(time.RFC3339))
				if err != nil {
					d.logger.Printf("Pool: %s/%s credential delivery warning: %v", provider, profile, err)
				}
			}
		},
	}

	d.poolMonitor = authpool.NewMonitor(d.authPool, refresher, monitorConfig)
	d.logger.Println("Auth pool initialized")
}

// Start begins the daemon's main loop.
func (d *Daemon) Start() error {
	// Install the signal handler before the PID file becomes visible: a
	// supervisor (or test) that detects the PID file and immediately sends
	// SIGTERM must hit the graceful-shutdown path, not the default handler
	// (which kills the process and leaves the PID file behind).
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		signal.Stop(sigCh)
		return fmt.Errorf("daemon already running")
	}

	// Acquire PID lock first
	if err := d.acquirePIDLock(); err != nil {
		d.mu.Unlock()
		signal.Stop(sigCh)
		return fmt.Errorf("acquire pid lock: %w", err)
	}

	d.running = true
	d.stats.StartTime = time.Now()
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.configChanged = make(chan struct{}, 1)
	d.mu.Unlock()

	d.logger.Printf("Starting daemon (check interval: %v, refresh threshold: %v)",
		d.config.CheckInterval, d.config.RefreshThreshold)

	// Start pool monitor if enabled
	if d.poolMonitor != nil {
		// Load profiles from vault into the pool
		if err := d.authPool.LoadFromVault(d.ctx); err != nil {
			d.logger.Printf("Warning: failed to load profiles into pool: %v", err)
		}
		if err := d.poolMonitor.Start(d.ctx); err != nil {
			d.logger.Printf("Warning: failed to start pool monitor: %v", err)
		} else {
			d.logger.Println("Pool monitor started")
		}
	}

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runLoop()
	}()

	// Wait for signal
	for {
		select {
		case sig := <-sigCh:
			if sig == syscall.SIGHUP {
				d.logger.Println("Received SIGHUP, reloading config...")
				d.ReloadConfig()
				continue
			}
			d.logger.Printf("Received signal %v, shutting down...", sig)
			signal.Stop(sigCh) // Clean up signal handler before stopping
			return d.Stop()
		case <-d.ctx.Done():
			signal.Stop(sigCh) // Clean up signal handler before stopping
			return d.Stop()
		}
	}
}

// ReloadConfig reloads the configuration from disk.
func (d *Daemon) ReloadConfig() {
	// Load global config
	globalCfg, err := config.LoadSPMConfig()
	if err != nil {
		d.logger.Printf("Error reloading config: %v", err)
		return
	}

	// Check if reload is enabled
	if !globalCfg.Runtime.ReloadOnSIGHUP {
		d.logger.Println("Reload on SIGHUP is disabled in config")
		return
	}

	// Apply updates with proper locking
	d.configMu.Lock()
	d.config.Verbose = globalCfg.Daemon.Verbose
	d.config.CheckInterval = globalCfg.Daemon.CheckInterval.Duration()
	if d.config.CheckInterval <= 0 {
		d.config.CheckInterval = DefaultCheckInterval
	}
	d.config.RefreshThreshold = globalCfg.Daemon.RefreshThreshold.Duration()
	if d.config.RefreshThreshold <= 0 {
		d.config.RefreshThreshold = DefaultRefreshThreshold
	}
	d.configMu.Unlock()

	d.logger.Println("Config reloaded (runtime settings applied)")

	// Signal runLoop to update ticker
	select {
	case d.configChanged <- struct{}{}:
	default:
		// Already signaled
	}
}

// Stop gracefully stops the daemon.
func (d *Daemon) Stop() error {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return nil
	}
	d.running = false
	d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
	}

	// Stop pool monitor if running
	if d.poolMonitor != nil {
		d.poolMonitor.Stop()
		d.logger.Println("Pool monitor stopped")

		// Save pool state
		if d.authPool != nil {
			stateOpts := authpool.PersistOptions{}
			if err := d.authPool.Save(stateOpts); err != nil {
				d.logger.Printf("Warning: failed to save pool state: %v", err)
			}
		}
	}

	// Wait for goroutines to finish with timeout
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		d.logger.Println("Daemon stopped gracefully")
	case <-time.After(10 * time.Second):
		d.logger.Println("Daemon stop timed out")
	}

	// Close log file if we opened one
	if d.logFile != nil {
		d.logFile.Close()
		d.logFile = nil
	}

	// Release PID lock
	d.mu.Lock()
	if d.pidFile != nil {
		health.UnlockFile(d.pidFile)
		d.pidFile.Close()
		os.Remove(d.pidFile.Name()) // Clean up file
		d.pidFile = nil
	}
	d.mu.Unlock()

	return nil
}

// IsRunning returns whether the daemon is currently running.
func (d *Daemon) IsRunning() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// GetStats returns a copy of the daemon statistics.
func (d *Daemon) GetStats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()

	stats := d.stats

	// Add backup stats if scheduler is enabled
	if d.backupScheduler != nil {
		stats.BackupEnabled = true
		state := d.backupScheduler.GetState()
		stats.LastBackup = state.LastBackup
		stats.BackupCount = state.BackupCount
		stats.NextBackup = d.backupScheduler.NextBackupTime()
	}

	// Add pool stats if enabled
	if d.authPool != nil {
		stats.PoolEnabled = true
		if d.poolMonitor != nil {
			stats.PoolMonitorActive = d.poolMonitor.IsRunning()
		}
		stats.PoolSummary = d.authPool.Summary()
	}

	return stats
}

// GetAuthPool returns the auth pool (may be nil if not enabled).
func (d *Daemon) GetAuthPool() *authpool.AuthPool {
	return d.authPool
}

// GetPoolMonitor returns the pool monitor (may be nil if not enabled).
func (d *Daemon) GetPoolMonitor() *authpool.Monitor {
	return d.poolMonitor
}

// runLoop is the main daemon loop.
func (d *Daemon) runLoop() {
	// Helper to check if pool monitor is handling refresh
	shouldUsePoolRefresh := func() bool {
		return d.poolMonitor != nil && d.poolMonitor.IsRunning()
	}

	// Do an initial check immediately
	if !shouldUsePoolRefresh() {
		d.checkAndRefresh()
	} else {
		d.checkCursorSessions()
	}
	d.checkAndBackup()
	d.checkForUpdate()

	interval := d.getCheckInterval()
	if interval <= 0 {
		interval = DefaultCheckInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.configChanged:
			newInterval := d.getCheckInterval()
			if newInterval <= 0 {
				newInterval = DefaultCheckInterval
			}
			if newInterval != interval {
				interval = newInterval
				ticker.Reset(interval)
				d.logger.Printf("Updated check interval to %v", interval)
			}
		case <-ticker.C:
			// Check each iteration in case pool monitor state changed
			if !shouldUsePoolRefresh() {
				d.checkAndRefresh()
			} else {
				d.checkCursorSessions()
			}
			d.checkAndBackup()
			d.checkForUpdate()
		}
	}
}

// acquirePIDLock securely acquires the PID file lock
func (d *Daemon) acquirePIDLock() error {
	path := PIDFilePath()

	// Create parent directory if needed
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create pid dir: %w", err)
	}

	// Open file (CREATE | RDWR)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return fmt.Errorf("open pid file: %w", err)
	}

	// Try to lock
	if err := health.LockFile(f); err != nil {
		f.Close()
		return fmt.Errorf("lock pid file (is another daemon running?): %w", err)
	}

	// Check if another process wrote a PID and is still running
	// Note: We have the lock, so no one else is writing now.
	// But previous process might have crashed leaving PID.
	// Or we are the first.

	// Read existing PID
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		var pid int
		if _, err := fmt.Sscanf(string(data), "%d", &pid); err == nil {
			if IsProcessRunning(pid) && pid != os.Getpid() {
				health.UnlockFile(f)
				f.Close()
				return fmt.Errorf("daemon already running (pid %d); stop it with 'caam daemon stop' first", pid)
			}
		}
	}

	// Truncate and write our PID
	if err := f.Truncate(0); err != nil {
		health.UnlockFile(f)
		f.Close()
		return fmt.Errorf("truncate pid file: %w", err)
	}
	if _, err := f.Seek(0, 0); err != nil {
		health.UnlockFile(f)
		f.Close()
		return fmt.Errorf("seek pid file: %w", err)
	}

	if _, err := fmt.Fprintf(f, "%d\n", os.Getpid()); err != nil {
		health.UnlockFile(f)
		f.Close()
		return fmt.Errorf("write pid file: %w", err)
	}

	// Sync to disk to ensure PID is visible to other processes
	if err := f.Sync(); err != nil {
		health.UnlockFile(f)
		f.Close()
		return fmt.Errorf("sync pid file: %w", err)
	}

	// Important: Do NOT close f here. We hold the lock as long as f is open.
	d.pidFile = f
	return nil
}

// checkAndBackup creates a backup if one is due.
func (d *Daemon) checkAndBackup() {
	if d.backupScheduler == nil {
		return
	}

	if !d.backupScheduler.ShouldBackup() {
		if d.isVerbose() {
			next := d.backupScheduler.NextBackupTime()
			if !next.IsZero() {
				d.logger.Printf("Next backup scheduled for %v", next.Format(time.RFC3339))
			}
		}
		return
	}

	backupPath, err := d.backupScheduler.CreateBackup()
	if err != nil {
		d.mu.Lock()
		d.stats.BackupErrors++
		d.mu.Unlock()
		d.logger.Printf("Backup failed: %v", err)
		return
	}

	if backupPath != "" {
		d.logger.Printf("Backup created: %s", backupPath)
	}
}

// checkAndRefresh checks all profiles and refreshes those that need it.
func (d *Daemon) checkAndRefresh() {
	d.mu.Lock()
	d.stats.LastCheck = time.Now()
	d.stats.CheckCount++
	d.mu.Unlock()

	if d.isVerbose() {
		d.logger.Println("Checking profiles for refresh...")
	}

	providers := []string{"claude", "codex", "gemini", "grok", "cursor"}
	var totalChecked int64

	// Use a semaphore to limit concurrency
	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup

	for _, provider := range providers {
		profiles, err := d.vault.List(provider)
		if err != nil {
			if d.isVerbose() {
				d.logger.Printf("Could not list %s profiles: %v", provider, err)
			}
			continue
		}

		for _, profile := range profiles {
			if authfile.IsSystemProfile(profile) {
				continue
			}
			totalChecked++
			wg.Add(1)
			sem <- struct{}{} // Acquire token
			go func(pProvider, pProfile string) {
				defer wg.Done()
				defer func() { <-sem }() // Release token
				d.checkProfile(pProvider, pProfile)
			}(provider, profile)
		}
	}

	wg.Wait()
	d.checkLiveCursorSession()

	d.mu.Lock()
	d.stats.ProfilesChecked += totalChecked
	d.mu.Unlock()

	if d.isVerbose() {
		d.logger.Printf("Checked %d profiles", totalChecked)
	}
}

// checkProfile checks a single profile and refreshes if needed.
func (d *Daemon) checkProfile(provider, profile string) {
	// Get health data for this profile
	ph := d.getProfileHealth(provider, profile)
	if ph == nil {
		return
	}
	if provider == "cursor" {
		if err := refresh.Preflight(provider, profile, d.vault); refresh.IsSkipped(err) {
			d.logRefreshSkip(provider, profile, err)
		}
		d.warnCursorSession(profile, ph, time.Now())
		return
	}

	// Decide from the current credential before announcing any attempt. Repeat
	// this on every check so a backup can make a previously stale token usable.
	if err := refresh.Preflight(provider, profile, d.vault); err != nil {
		if refresh.IsSkipped(err) {
			d.logRefreshSkip(provider, profile, err)
		} else {
			d.mu.Lock()
			d.stats.RefreshErrors++
			d.mu.Unlock()
			d.logger.Printf("%s/%s: refresh preflight failed: %v", provider, profile, err)
			d.alert(notify.Warning, "Credential check failed", provider, profile, err.Error(),
				fmt.Sprintf("run 'caam doctor' or log in again and 'caam backup %s %s'", provider, profile))
		}
		return
	}
	if ph.SelfRefreshing {
		d.logRefreshSkip(provider, profile, &refresh.UnsupportedError{
			Provider: provider,
			Reason:   fmt.Sprintf("the %s CLI renews this credential when it runs", provider),
		})
		return
	}

	// Check if refresh is needed
	if !refresh.ShouldRefresh(ph, d.getRefreshThreshold()) {
		if d.isVerbose() && !ph.TokenExpiresAt.IsZero() {
			ttl := time.Until(ph.TokenExpiresAt)
			if ttl > 0 {
				d.logger.Printf("%s/%s: token OK (expires in %v)", provider, profile, ttl.Round(time.Minute))
			} else {
				d.logger.Printf("%s/%s: token expired (%v ago)", provider, profile, (-ttl).Round(time.Minute))
			}
		}
		return
	}

	ttl := time.Until(ph.TokenExpiresAt)
	d.logger.Printf("%s/%s: refreshing token (expires in %v)", provider, profile, ttl.Round(time.Minute))

	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	err := refresh.RefreshProfile(ctx, provider, profile, d.vault, d.healthStore)
	if refresh.IsSkipped(err) {
		// Credentials may change after preflight. Always finish an announced
		// attempt with its outcome, even when the result is a safe skip.
		d.logger.Printf("%s/%s: refresh skipped: %v", provider, profile, err)
		return
	}

	d.mu.Lock()
	if err != nil && !refresh.IsDeliveryIncomplete(err) {
		d.stats.RefreshErrors++
		d.mu.Unlock()

		d.logger.Printf("%s/%s: refresh failed: %v", provider, profile, err)
		d.alertRefreshFailure(provider, profile, err)
	} else {
		d.stats.RefreshCount++
		d.mu.Unlock()
		if updated := d.getProfileHealth(provider, profile); updated != nil && !updated.TokenExpiresAt.IsZero() {
			d.logger.Printf("%s/%s: token refreshed successfully (new expiry %s)", provider, profile, updated.TokenExpiresAt.Format(time.RFC3339))
		} else {
			d.logger.Printf("%s/%s: token refreshed successfully (new expiry unavailable)", provider, profile)
		}
		if err != nil {
			d.logger.Printf("%s/%s: credential delivery warning: %v", provider, profile, err)
		}
	}
}

// alert sends a notification; the notifier suppresses repeats.
func (d *Daemon) alert(level notify.AlertLevel, title, provider, profile, message, action string) {
	if d.notifier == nil {
		return
	}
	label := provider
	if profile != "" {
		label = provider + "/" + profile
	}
	if err := d.notifier.Notify(&notify.Alert{
		Level:     level,
		Title:     title,
		Message:   message,
		Profile:   label,
		Action:    action,
		Timestamp: time.Now(),
	}); err != nil {
		d.logger.Printf("%s: alert delivery failed: %v", label, err)
	}
}

// checkForUpdate asks whether a newer caam release exists when a check is
// due and announces each new version once.
func (d *Daemon) checkForUpdate() {
	if d.config.UpdateChecker == nil {
		return
	}
	interval := d.config.UpdateCheckInterval
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	if !d.lastUpdateCheck.IsZero() && time.Since(d.lastUpdateCheck) < interval {
		return
	}
	d.lastUpdateCheck = time.Now()

	parent := d.ctx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()
	res, err := d.config.UpdateChecker.Check(ctx)
	if err != nil {
		d.logger.Printf("update check failed: %v", err)
		return
	}
	if res == nil || !res.UpdateAvailable || res.LatestVersion == d.announcedVersion {
		return
	}
	d.announcedVersion = res.LatestVersion
	d.logger.Printf("caam %s is available (running %s)", res.LatestVersion, res.CurrentVersion)
	// The version is in the title: alerts are throttled per title, and each
	// release deserves its own announcement.
	d.alert(notify.Info, "caam "+res.LatestVersion+" available", "caam", "",
		fmt.Sprintf("caam %s is available (running %s).", res.LatestVersion, res.CurrentVersion),
		"caam update, then caam update --remotes for distributed coordinators")
}

// alertRefreshFailure distinguishes a rejected login, which needs a human,
// from a failure that may clear on a later check.
func (d *Daemon) alertRefreshFailure(provider, profile string, err error) {
	var rejected *refresh.RefreshRejectedError
	var reused *refresh.RefreshTokenReusedError
	if errors.As(err, &rejected) || errors.As(err, &reused) {
		d.alert(notify.Critical, "Login required", provider, profile,
			fmt.Sprintf("the %s provider rejected this login", provider),
			fmt.Sprintf("log in to %s again, then run 'caam backup %s %s'", provider, provider, profile))
		return
	}
	d.alert(notify.Warning, "Token refresh failed", provider, profile, err.Error(),
		"caam retries on the next check; run 'caam refresh "+provider+" "+profile+"' to retry now")
}

// logRefreshSkip reports a reason once per profile without suppressing future
// preflight checks or treating a non-attempt as a refresh error.
func (d *Daemon) logRefreshSkip(provider, profile string, err error) {
	key := provider + "/" + profile
	reason := err.Error()
	d.mu.Lock()
	if d.refreshSkips == nil {
		d.refreshSkips = make(map[string]string)
	}
	if d.refreshSkips[key] == reason {
		d.mu.Unlock()
		return
	}
	d.refreshSkips[key] = reason
	d.mu.Unlock()
	d.logger.Printf("%s: not refreshed by caam (%s)", key, reason)
}

// getProfileHealth returns the health data for a profile.
func (d *Daemon) getProfileHealth(provider, profile string) *health.ProfileHealth {
	var ph *health.ProfileHealth
	if d.healthStore != nil {
		ph, _ = d.healthStore.GetProfile(provider, profile)
	}

	// Expiry and renewal policy come from the current credential. Cached health
	// retains account statistics but cannot make an absent or replaced login valid.
	vaultPath := d.vault.ProfilePath(provider, profile)
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
	case "grok":
		expiryInfo, err = health.ParseGrokExpiry(filepath.Join(vaultPath, "auth.json"))
	default:
		return nil
	}

	if err != nil || expiryInfo == nil {
		return nil
	}

	if ph == nil {
		ph = &health.ProfileHealth{}
	}
	ph.TokenExpiresAt = expiryInfo.ExpiresAt
	ph.SelfRefreshing = expiryInfo.SelfRefreshing
	ph.TokenRenewable = expiryInfo.Renewable
	ph.ReloginWarningLead = expiryInfo.ReloginWarningLead
	ph.CredentialFingerprint = expiryInfo.Fingerprint
	return ph
}

// isUnsupportedError checks if an error is an UnsupportedError.
// Uses errors.As to properly handle wrapped errors.
func isUnsupportedError(err error, target **refresh.UnsupportedError) bool {
	if err == nil {
		return false
	}

	var ue *refresh.UnsupportedError
	if errors.As(err, &ue) {
		if target != nil {
			*target = ue
		}
		return true
	}

	return false
}

// pidFilePath stores the configured PID file path.
var pidFilePath string

// SetPIDFilePath sets the path for the PID file.
func SetPIDFilePath(path string) {
	pidFilePath = path
}

// PIDFilePath returns the path to the daemon's PID file.
func PIDFilePath() string {
	if pidFilePath != "" {
		return pidFilePath
	}
	return filepath.Join(os.TempDir(), "caam-daemon.pid")
}

// LogFilePath returns the default path for daemon logs.
func LogFilePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "caam-daemon.log")
	}
	return filepath.Join(homeDir, ".local", "share", "caam", "daemon.log")
}

// RemovePIDFile removes the PID file.
func RemovePIDFile() error {
	return os.Remove(PIDFilePath())
}

// ReadPIDFile reads the PID from the PID file.
func ReadPIDFile() (int, error) {
	data, err := os.ReadFile(PIDFilePath())
	if err != nil {
		return 0, err
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		return 0, err
	}

	return pid, nil
}

// IsProcessRunning checks if a process with the given PID is running.
func IsProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix, FindProcess always succeeds. We need to send signal 0 to check.
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}

	// EPERM means the process exists but we can't signal it (different user).
	// Only ESRCH means the process doesn't exist.
	if errors.Is(err, syscall.EPERM) {
		return true
	}

	return false
}

// GetDaemonStatus returns the current daemon status.
func GetDaemonStatus() (running bool, pid int, err error) {
	pid, err = ReadPIDFile()
	if err != nil {
		if os.IsNotExist(err) {
			return false, 0, nil
		}
		return false, 0, err
	}

	if IsProcessRunning(pid) {
		return true, pid, nil
	}

	// PID file exists but process is not running - stale PID file
	_ = RemovePIDFile()
	return false, 0, nil
}

// StopDaemonByPID sends SIGTERM to the daemon process.
func StopDaemonByPID(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process: %w", err)
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("send SIGTERM: %w", err)
	}

	return nil
}

// warnCursorSession logs once per login, without attempting an unsupported
// refresh. A digest identifies replacement credentials even when they happen
// to expire at the same instant; no token material is retained or logged.
func (d *Daemon) warnCursorSession(profile string, ph *health.ProfileHealth, now time.Time) {
	if ph == nil || ph.TokenExpiresAt.IsZero() || ph.CredentialRenewable() {
		return
	}
	lead := ph.ReloginWarningLead
	if lead <= 0 {
		lead = health.CursorReloginLead
	}
	if ph.TokenExpiresAt.Sub(now) > lead {
		return
	}

	fingerprint := ph.CredentialFingerprint
	if fingerprint == "" {
		fingerprint = ph.TokenExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	key := fingerprint
	d.mu.Lock()
	if d.reloginWarnings == nil {
		d.reloginWarnings = make(map[string]string)
	}
	if d.reloginWarnings[key] == fingerprint {
		d.mu.Unlock()
		return
	}
	d.reloginWarnings[key] = fingerprint
	d.mu.Unlock()

	action := health.CursorReloginInstructions(profile)
	if profile == "" {
		profile = "active"
		action = "log in again with cursor-agent login"
	}
	ttl := ph.TokenExpiresAt.Sub(now)
	if ttl <= 0 {
		d.logger.Printf("cursor/%s: session EXPIRED; %s", profile, action)
		d.alert(notify.Critical, "Session expired", "cursor", profile, "the Cursor session has expired", action)
	} else {
		d.logger.Printf("cursor/%s: session expires in %v; %s", profile, ttl.Round(time.Minute), action)
		d.alert(notify.Warning, "Session expiring", "cursor", profile,
			fmt.Sprintf("the Cursor session expires in %v", ttl.Round(time.Minute)), action)
	}
}

// checkCursorSessions remains active when the auth pool handles renewable
// providers. A session that needs a human login must still warn well before
// the pool's short refresh threshold.
func (d *Daemon) checkCursorSessions() {
	profiles, err := d.vault.List("cursor")
	if err == nil {
		for _, profile := range profiles {
			if !authfile.IsSystemProfile(profile) {
				d.checkProfile("cursor", profile)
			}
		}
	}
	d.checkLiveCursorSession()
}

// checkLiveCursorSession inspects the current login using cursor-agent's
// resolved paths, even when it has not been backed up to a named profile.
func (d *Daemon) checkLiveCursorSession() {
	info, err := health.ParseCursorExpiry("")
	if err != nil || info == nil {
		return
	}
	profile, _ := d.vault.ActiveProfile(authfile.CursorAuthFiles())
	d.warnCursorSession(profile, &health.ProfileHealth{
		TokenExpiresAt:        info.ExpiresAt,
		SelfRefreshing:        info.SelfRefreshing,
		TokenRenewable:        info.Renewable,
		ReloginWarningLead:    info.ReloginWarningLead,
		CredentialFingerprint: info.Fingerprint,
	}, time.Now())
}
