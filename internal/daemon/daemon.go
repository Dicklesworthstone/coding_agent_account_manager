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
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
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
}

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

	// backupScheduler handles automatic backups (may be nil if disabled)
	backupScheduler *BackupScheduler

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
	LastBackup      time.Time
	NextBackup      time.Time
	BackupCount     int64
	BackupErrors    int64
	BackupEnabled   bool

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

	d := &Daemon{
		config:      cfg,
		vault:       vault,
		healthStore: healthStore,
		logger:      logger,
		logFile:     logFile,
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
		OnRefreshStart: func(provider, profile string) {
			if d.isVerbose() {
				d.logger.Printf("Pool: starting refresh for %s/%s", provider, profile)
			}
		},
		OnRefreshComplete: func(provider, profile string, newExpiry time.Time, err error) {
			d.mu.Lock()
			if err != nil {
				d.stats.RefreshErrors++
				d.mu.Unlock()
				d.logger.Printf("Pool: %s/%s refresh failed: %v", provider, profile, err)
			} else {
				d.stats.RefreshCount++
				d.mu.Unlock()
				if d.isVerbose() {
					d.logger.Printf("Pool: %s/%s refreshed, expires %v",
						provider, profile, newExpiry.Format(time.RFC3339))
				}
			}
		},
	}

	d.poolMonitor = authpool.NewMonitor(d.authPool, refresher, monitorConfig)
	d.logger.Println("Auth pool initialized")
}

// Start begins the daemon's main loop.
func (d *Daemon) Start() error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("daemon already running")
	}
	
	// Acquire PID lock first
	if err := d.acquirePIDLock(); err != nil {
		d.mu.Unlock()
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

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

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

	if d.cancel != nil {
		d.cancel()
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
	}
	d.checkAndBackup()

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
			}
			d.checkAndBackup()
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
				return fmt.Errorf("daemon already running (pid %d)", pid)
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

	providers := []string{"claude", "codex", "gemini"}
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

	// Check if refresh is needed
	if !refresh.ShouldRefresh(ph, d.getRefreshThreshold()) {
		if d.isVerbose() && !ph.TokenExpiresAt.IsZero() {
			ttl := time.Until(ph.TokenExpiresAt)
			d.logger.Printf("%s/%s: token OK (expires in %v)", provider, profile, ttl.Round(time.Minute))
		}
		return
	}

	ttl := time.Until(ph.TokenExpiresAt)
	d.logger.Printf("%s/%s: refreshing token (expires in %v)", provider, profile, ttl.Round(time.Minute))

	ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
	defer cancel()

	err := refresh.RefreshProfile(ctx, provider, profile, d.vault, d.healthStore)

	d.mu.Lock()
	if err != nil {
		d.stats.RefreshErrors++
		d.mu.Unlock()

		// Don't log unsupported errors as failures
		var unsupErr *refresh.UnsupportedError
		if ok := isUnsupportedError(err, &unsupErr); ok {
			if d.isVerbose() {
				d.logger.Printf("%s/%s: refresh not supported (%s)", provider, profile, unsupErr.Reason)
			}
		} else {
			d.logger.Printf("%s/%s: refresh failed: %v", provider, profile, err)
		}
	} else {
		d.stats.RefreshCount++
		d.mu.Unlock()
		d.logger.Printf("%s/%s: token refreshed successfully", provider, profile)
	}
}

// getProfileHealth returns the health data for a profile.
func (d *Daemon) getProfileHealth(provider, profile string) *health.ProfileHealth {
	// First try the health store
	if d.healthStore != nil {
		ph, err := d.healthStore.GetProfile(provider, profile)
		if err == nil && ph != nil && !ph.TokenExpiresAt.IsZero() {
			return ph
		}
	}

	// Fall back to parsing the auth files directly
	vaultPath := d.vault.ProfilePath(provider, profile)
	var expiryInfo *health.ExpiryInfo
	var err error

	switch provider {
	case "claude":
		expiryInfo, err = health.ParseClaudeExpiry(vaultPath)
	case "codex":
		expiryInfo, err = health.ParseCodexExpiry(filepath.Join(vaultPath, "auth.json"))
	case "gemini":
		expiryInfo, err = health.ParseGeminiExpiry(vaultPath)
	}

	if err != nil || expiryInfo == nil {
		return nil
	}

	return &health.ProfileHealth{
		TokenExpiresAt: expiryInfo.ExpiresAt,
	}
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
