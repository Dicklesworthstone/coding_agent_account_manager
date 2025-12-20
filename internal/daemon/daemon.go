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

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	running bool
	stats   Stats
}

// Stats tracks daemon activity.
type Stats struct {
	StartTime       time.Time
	LastCheck       time.Time
	CheckCount      int64
	RefreshCount    int64
	RefreshErrors   int64
	ProfilesChecked int64
}

// New creates a new daemon instance.
func New(vault *authfile.Vault, healthStore *health.Storage, cfg *Config) *Daemon {
	if cfg == nil {
		cfg = DefaultConfig()
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

	return &Daemon{
		config:      cfg,
		vault:       vault,
		healthStore: healthStore,
		logger:      logger,
		logFile:     logFile,
	}
}

// Start begins the daemon's main loop.
func (d *Daemon) Start() error {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return fmt.Errorf("daemon already running")
	}
	d.running = true
	d.stats.StartTime = time.Now()
	d.ctx, d.cancel = context.WithCancel(context.Background())
	d.mu.Unlock()

	d.logger.Printf("Starting daemon (check interval: %v, refresh threshold: %v)",
		d.config.CheckInterval, d.config.RefreshThreshold)

	// Set up signal handling
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		d.runLoop()
	}()

	// Wait for signal
	select {
	case sig := <-sigCh:
		d.logger.Printf("Received signal %v, shutting down...", sig)
	case <-d.ctx.Done():
	}

	signal.Stop(sigCh)
	return d.Stop()
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
	return d.stats
}

// runLoop is the main daemon loop.
func (d *Daemon) runLoop() {
	// Do an initial check immediately
	d.checkAndRefresh()

	ticker := time.NewTicker(d.config.CheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-ticker.C:
			d.checkAndRefresh()
		}
	}
}

// checkAndRefresh checks all profiles and refreshes those that need it.
func (d *Daemon) checkAndRefresh() {
	d.mu.Lock()
	d.stats.LastCheck = time.Now()
	d.stats.CheckCount++
	d.mu.Unlock()

	if d.config.Verbose {
		d.logger.Println("Checking profiles for refresh...")
	}

	providers := []string{"claude", "codex", "gemini"}
	var totalChecked int64

	for _, provider := range providers {
		profiles, err := d.vault.List(provider)
		if err != nil {
			if d.config.Verbose {
				d.logger.Printf("Could not list %s profiles: %v", provider, err)
			}
			continue
		}

		for _, profile := range profiles {
			totalChecked++
			d.checkProfile(provider, profile)
		}
	}

	d.mu.Lock()
	d.stats.ProfilesChecked += totalChecked
	d.mu.Unlock()

	if d.config.Verbose {
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
	if !refresh.ShouldRefresh(ph, d.config.RefreshThreshold) {
		if d.config.Verbose && !ph.TokenExpiresAt.IsZero() {
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
			if d.config.Verbose {
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

// PIDFilePath returns the path to the daemon's PID file.
func PIDFilePath() string {
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

// WritePIDFile writes the current process ID to the PID file.
func WritePIDFile() error {
	return os.WriteFile(PIDFilePath(), []byte(fmt.Sprintf("%d", os.Getpid())), 0600)
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
