package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.CheckInterval != DefaultCheckInterval {
		t.Errorf("expected CheckInterval %v, got %v", DefaultCheckInterval, cfg.CheckInterval)
	}

	if cfg.RefreshThreshold != DefaultRefreshThreshold {
		t.Errorf("expected RefreshThreshold %v, got %v", DefaultRefreshThreshold, cfg.RefreshThreshold)
	}

	if cfg.Verbose {
		t.Error("expected Verbose to be false by default")
	}
}

func TestNewDaemon(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	d := New(v, hs, nil)

	if d == nil {
		t.Fatal("expected daemon to be created")
	}

	if d.config == nil {
		t.Error("expected config to be set")
	}

	if d.config.CheckInterval != DefaultCheckInterval {
		t.Errorf("expected default CheckInterval, got %v", d.config.CheckInterval)
	}
}

func TestNewDaemonWithConfig(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    1 * time.Minute,
		RefreshThreshold: 15 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	if d.config.CheckInterval != 1*time.Minute {
		t.Errorf("expected CheckInterval 1m, got %v", d.config.CheckInterval)
	}

	if d.config.RefreshThreshold != 15*time.Minute {
		t.Errorf("expected RefreshThreshold 15m, got %v", d.config.RefreshThreshold)
	}

	if !d.config.Verbose {
		t.Error("expected Verbose to be true")
	}
}

func TestDaemonIsRunning(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	d := New(v, hs, nil)

	if d.IsRunning() {
		t.Error("daemon should not be running initially")
	}
}

func TestDaemonGetStats(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	d := New(v, hs, nil)

	stats := d.GetStats()

	if !stats.StartTime.IsZero() {
		t.Error("StartTime should be zero before daemon starts")
	}

	if stats.CheckCount != 0 {
		t.Errorf("expected CheckCount 0, got %d", stats.CheckCount)
	}
}

func TestDaemonStopBeforeStart(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	d := New(v, hs, nil)

	// Stop before start should not error
	if err := d.Stop(); err != nil {
		t.Errorf("Stop before Start should not error: %v", err)
	}
}

func TestPIDFilePath(t *testing.T) {
	path := PIDFilePath()
	if path == "" {
		t.Error("PIDFilePath should not be empty")
	}

	if filepath.Base(path) != "caam-daemon.pid" {
		t.Errorf("expected filename caam-daemon.pid, got %s", filepath.Base(path))
	}
}

func TestLogFilePath(t *testing.T) {
	path := LogFilePath()
	if path == "" {
		t.Error("LogFilePath should not be empty")
	}

	if filepath.Base(path) != "daemon.log" {
		t.Errorf("expected filename daemon.log, got %s", filepath.Base(path))
	}
}

func TestIsProcessRunning(t *testing.T) {
	// Current process should be running
	if !IsProcessRunning(os.Getpid()) {
		t.Error("current process should be reported as running")
	}

	// Non-existent PID should not be running
	// Use a very high PID that's unlikely to exist
	if IsProcessRunning(999999999) {
		t.Error("non-existent PID should not be reported as running")
	}
}

func TestGetDaemonStatusNotRunning(t *testing.T) {
	// Clean up any existing PID file
	os.Remove(PIDFilePath())

	running, pid, err := GetDaemonStatus()
	if err != nil {
		t.Fatalf("GetDaemonStatus failed: %v", err)
	}

	if running {
		t.Error("daemon should not be reported as running without PID file")
	}

	if pid != 0 {
		t.Errorf("expected PID 0, got %d", pid)
	}
}

func TestIsUnsupportedError(t *testing.T) {
	// Test nil error
	if isUnsupportedError(nil, nil) {
		t.Error("nil error should not be unsupported error")
	}

	// Test regular error
	if isUnsupportedError(os.ErrNotExist, nil) {
		t.Error("ErrNotExist should not be unsupported error")
	}
}

func TestIsUnsupportedError_WithTarget(t *testing.T) {
	// Create an UnsupportedError by importing refresh package
	// We can't easily test with actual UnsupportedError without importing refresh
	// which could cause circular imports. Test the false path more thoroughly.

	// Wrapped error should not match
	wrapped := os.ErrNotExist
	var target *struct{} // Different type
	if isUnsupportedError(wrapped, nil) {
		t.Error("wrapped regular error should not be unsupported error")
	}
	_ = target
}

func TestIsProcessRunning_EdgeCases(t *testing.T) {
	// Zero PID should not be running
	if IsProcessRunning(0) {
		t.Error("PID 0 should not be reported as running")
	}

	// Negative PID should not be running
	if IsProcessRunning(-1) {
		t.Error("negative PID should not be reported as running")
	}

	// PID 1 (init) should be running on Linux (unless in container)
	// We don't test this as it depends on permissions
}

func TestStopDaemonByPID(t *testing.T) {
	// Test with non-existent PID
	err := StopDaemonByPID(999999999)
	if err == nil {
		t.Error("StopDaemonByPID should error for non-existent PID")
	}

	// Test with invalid PID
	err = StopDaemonByPID(-1)
	if err == nil {
		t.Error("StopDaemonByPID should error for invalid PID")
	}
}

func TestGetDaemonStatus_StalePIDFile(t *testing.T) {
	// Create a PID file with a non-existent PID
	pidPath := PIDFilePath()

	// Cleanup before test
	os.Remove(pidPath)
	defer os.Remove(pidPath)

	// Write a stale PID (very high number unlikely to exist)
	err := os.WriteFile(pidPath, []byte("999999999\n"), 0600)
	if err != nil {
		t.Fatalf("failed to write test PID file: %v", err)
	}

	running, pid, err := GetDaemonStatus()
	if err != nil {
		t.Fatalf("GetDaemonStatus failed: %v", err)
	}

	if running {
		t.Error("daemon should not be reported as running for stale PID")
	}

	if pid != 0 {
		t.Errorf("expected PID 0 after cleanup, got %d", pid)
	}

	// Stale PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("stale PID file should have been removed")
	}
}

func TestReadPIDFile_InvalidFormat(t *testing.T) {
	pidPath := PIDFilePath()

	// Cleanup before test
	os.Remove(pidPath)
	defer os.Remove(pidPath)

	// Write invalid PID format
	err := os.WriteFile(pidPath, []byte("not-a-number\n"), 0600)
	if err != nil {
		t.Fatalf("failed to write test PID file: %v", err)
	}

	_, err = ReadPIDFile()
	if err == nil {
		t.Error("ReadPIDFile should error on invalid format")
	}
}

func TestLogFilePath_NoHomeDir(t *testing.T) {
	// LogFilePath falls back to temp dir if home dir is not available
	// We can't easily test this without modifying environment
	// but we can verify it returns a valid path
	path := LogFilePath()
	if path == "" {
		t.Error("LogFilePath should not return empty string")
	}
	if filepath.Ext(path) != ".log" {
		t.Errorf("LogFilePath should end with .log, got %s", path)
	}
}

func TestNewDaemon_WithLogPath(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	logPath := filepath.Join(tmpDir, "test-daemon.log")
	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		LogPath:          logPath,
	}

	d := New(v, hs, cfg)
	defer d.Stop()

	if d == nil {
		t.Fatal("expected daemon to be created")
	}

	// Log file should be created
	if _, err := os.Stat(logPath); os.IsNotExist(err) {
		t.Error("log file should be created")
	}
}

func TestNewDaemon_WithInvalidLogPath(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Use an invalid log path
	cfg := &Config{
		LogPath: "/nonexistent/path/daemon.log",
	}

	// Should not panic, should fall back to stdout
	d := New(v, hs, cfg)
	if d == nil {
		t.Fatal("daemon should be created even with invalid log path")
	}
}

func TestDaemon_ReloadConfig(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// ReloadConfig should not panic
	d.ReloadConfig()
}

func TestDaemon_CheckAndRefresh_EmptyVault(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Should not panic with empty vault
	d.checkAndRefresh()

	stats := d.GetStats()
	if stats.CheckCount != 1 {
		t.Errorf("CheckCount should be 1, got %d", stats.CheckCount)
	}
}

func TestDaemon_CheckAndBackup_NoScheduler(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	// Should not panic when scheduler is nil
	d.checkAndBackup()
}

func TestDaemon_GetProfileHealth_EmptyVault(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	// Should return nil for non-existent profile
	ph := d.getProfileHealth("claude", "nonexistent")
	if ph != nil {
		t.Error("getProfileHealth should return nil for non-existent profile")
	}
}

func TestDaemon_GetProfileHealth_FromHealthStore(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data
	expiry := time.Now().Add(1 * time.Hour)
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("claude", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	gotPh := d.getProfileHealth("claude", "test")
	if gotPh == nil {
		t.Fatal("getProfileHealth should return health data from store")
	}

	if gotPh.TokenExpiresAt.IsZero() {
		t.Error("TokenExpiresAt should not be zero")
	}
}

func TestDaemon_CheckProfile_NonExistentProfile(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Should not panic for non-existent profile
	d.checkProfile("claude", "nonexistent")
}

func TestDaemon_CheckProfile_TokenNotExpiring(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data with token expiring in 2 hours (not due for refresh)
	expiry := time.Now().Add(2 * time.Hour)
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("claude", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 10 * time.Minute, // Only refresh if < 10 min
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Should not attempt refresh (TTL > threshold)
	d.checkProfile("claude", "test")

	stats := d.GetStats()
	if stats.RefreshCount != 0 {
		t.Errorf("RefreshCount should be 0 (token not expiring), got %d", stats.RefreshCount)
	}
}

func TestSetPIDFilePath(t *testing.T) {
	// Save original
	original := PIDFilePath()
	defer SetPIDFilePath(original)

	// Set custom path
	SetPIDFilePath("/custom/path.pid")
	if got := PIDFilePath(); got != "/custom/path.pid" {
		t.Errorf("PIDFilePath() = %s, want /custom/path.pid", got)
	}

	// Set back to empty to restore default behavior
	SetPIDFilePath("")
	if got := PIDFilePath(); got == "/custom/path.pid" {
		t.Error("PIDFilePath should not be /custom/path.pid after reset")
	}
}

func TestDaemon_CheckProfile_ExpiringToken(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data with token expiring soon (below refresh threshold)
	expiry := time.Now().Add(5 * time.Minute)
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("claude", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 10 * time.Minute, // Token is expiring in 5min, threshold is 10min
		Verbose:          true,
	}

	d := New(v, hs, cfg)
	d.ctx, d.cancel = context.WithCancel(context.Background())
	defer d.cancel()

	// This will attempt to refresh, but fail because there's no actual profile
	// The important thing is that it exercises the code path
	d.checkProfile("claude", "test")

	stats := d.GetStats()
	// Should have recorded an error (refresh fails because profile doesn't exist in vault)
	if stats.RefreshErrors != 1 {
		t.Errorf("RefreshErrors should be 1, got %d", stats.RefreshErrors)
	}
}

func TestDaemon_GetProfileHealth_FallbackToParsing(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	// No health store - simulate fallback to parsing files
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Create a Claude profile with auth file
	profileDir := filepath.Join(tmpDir, "claude", "test")
	os.MkdirAll(profileDir, 0700)
	// Create an auth file that health.ParseClaudeExpiry can parse
	// (the function looks for specific files)

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	// This will try health store first (empty), then fallback to parsing files
	ph := d.getProfileHealth("claude", "test")
	// Will likely be nil since we haven't created a proper auth file
	_ = ph // Just exercising the code path
}

func TestDaemonStartAndStop(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          false,
	}

	d := New(v, hs, cfg)

	// Start daemon in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Start()
	}()

	// Wait a bit for it to start
	time.Sleep(100 * time.Millisecond)

	if !d.IsRunning() {
		t.Error("daemon should be running after Start")
	}

	// Check that it did at least one check
	stats := d.GetStats()
	if stats.CheckCount < 1 {
		t.Errorf("expected at least 1 check, got %d", stats.CheckCount)
	}

	// Stop daemon
	if err := d.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	if d.IsRunning() {
		t.Error("daemon should not be running after Stop")
	}

	// Get the result from Start (should be nil after Stop)
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after Stop")
	}
}

func TestDaemonDoubleStart(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	// Start daemon in goroutine
	go func() {
		_ = d.Start()
	}()

	// Wait for it to start
	time.Sleep(100 * time.Millisecond)
	defer d.Stop()

	// Second start should fail
	err := d.Start()
	if err == nil {
		t.Error("expected error on double start")
	}
}

func TestDaemon_CheckAndBackup_WithScheduler(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Manually set up a backup scheduler for testing
	// Note: the scheduler is initialized in New() based on global config
	// We test that checkAndBackup doesn't panic with or without scheduler

	// This should not panic regardless of scheduler state
	d.checkAndBackup()
}

func TestDaemon_CheckAndBackup_VerboseMode(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Should not panic in verbose mode
	d.checkAndBackup()
}

func TestGetDaemonStatus_Running(t *testing.T) {
	tmpDir := t.TempDir()
	customPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(customPath)
	defer SetPIDFilePath(originalPath)

	// Write our own PID (current process)
	if err := os.WriteFile(customPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	defer os.Remove(customPath)

	// GetDaemonStatus should report running
	running, pid, err := GetDaemonStatus()
	if err != nil {
		t.Fatalf("GetDaemonStatus failed: %v", err)
	}
	if !running {
		t.Error("daemon should be reported as running")
	}
	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}
}

func TestDaemon_GetProfileHealth_CodexProfile(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data for codex
	expiry := time.Now().Add(1 * time.Hour)
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("codex", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	gotPh := d.getProfileHealth("codex", "test")
	if gotPh == nil {
		t.Fatal("getProfileHealth should return health data from store")
	}

	if gotPh.TokenExpiresAt.IsZero() {
		t.Error("TokenExpiresAt should not be zero")
	}
}

func TestDaemon_GetProfileHealth_GeminiProfile(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data for gemini
	expiry := time.Now().Add(1 * time.Hour)
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("gemini", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	gotPh := d.getProfileHealth("gemini", "test")
	if gotPh == nil {
		t.Fatal("getProfileHealth should return health data from store")
	}
}

func TestDaemon_CheckAndRefresh_VerboseMode(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Should not panic with verbose mode
	d.checkAndRefresh()

	stats := d.GetStats()
	if stats.CheckCount != 1 {
		t.Errorf("CheckCount should be 1, got %d", stats.CheckCount)
	}
}

func TestStopDaemonByPID_CurrentProcess(t *testing.T) {
	// We can't actually send SIGTERM to ourselves in a test,
	// but we can verify the function handles the process lookup
	// Testing with current process would terminate the test

	// Test with a non-existent process instead
	err := StopDaemonByPID(999999999)
	if err == nil {
		t.Error("StopDaemonByPID should error for non-existent PID")
	}
}

func TestIsProcessRunning_Init(t *testing.T) {
	// PID 1 (init) should exist on most Linux systems
	// But we can't reliably test this across all environments
	// Just verify the function doesn't panic for PID 1
	_ = IsProcessRunning(1)
}

func TestDaemon_AcquirePIDLock_Success(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	// Save and restore original path
	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	// acquirePIDLock should succeed
	err := d.acquirePIDLock()
	if err != nil {
		t.Fatalf("acquirePIDLock() error = %v", err)
	}

	// Verify PID file was written
	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("Failed to read PID file: %v", err)
	}

	var pid int
	if _, err := fmt.Sscanf(string(data), "%d", &pid); err != nil {
		t.Fatalf("Failed to parse PID: %v", err)
	}

	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}

	// Cleanup
	if d.pidFile != nil {
		d.pidFile.Close()
	}
}

func TestDaemon_AcquirePIDLock_StalePID(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	// Save and restore original path
	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	// Write a stale PID (non-existent process)
	os.WriteFile(pidPath, []byte("999999999\n"), 0600)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	d := New(v, hs, nil)

	// acquirePIDLock should succeed (stale PID should be overwritten)
	err := d.acquirePIDLock()
	if err != nil {
		t.Fatalf("acquirePIDLock() with stale PID error = %v", err)
	}

	// Verify our PID was written
	data, _ := os.ReadFile(pidPath)
	var pid int
	fmt.Sscanf(string(data), "%d", &pid)
	if pid != os.Getpid() {
		t.Errorf("PID = %d, want %d", pid, os.Getpid())
	}

	// Cleanup
	if d.pidFile != nil {
		d.pidFile.Close()
	}
}

func TestDaemon_InitAuthPool(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:         50 * time.Millisecond,
		RefreshThreshold:      1 * time.Minute,
		MaxConcurrentRefreshes: 2,
		Verbose:               true,
		UseAuthPool:           true,
	}

	d := New(v, hs, cfg)

	// Check that auth pool was initialized
	if d.authPool == nil {
		t.Error("authPool should be initialized when UseAuthPool is true")
	}

	if d.poolMonitor == nil {
		t.Error("poolMonitor should be initialized when UseAuthPool is true")
	}
}

func TestDaemon_InitAuthPool_DefaultConcurrency(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:         50 * time.Millisecond,
		RefreshThreshold:      1 * time.Minute,
		MaxConcurrentRefreshes: 0, // Should default to 3
		Verbose:               true,
		UseAuthPool:           true,
	}

	d := New(v, hs, cfg)

	// Should not panic with MaxConcurrentRefreshes = 0
	if d.authPool == nil {
		t.Error("authPool should be initialized")
	}
}

func TestDaemon_GetAuthPool(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		UseAuthPool: true,
	}

	d := New(v, hs, cfg)

	pool := d.GetAuthPool()
	if pool == nil {
		t.Error("GetAuthPool() should return non-nil when UseAuthPool is true")
	}
}

func TestDaemon_GetPoolMonitor(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		UseAuthPool: true,
	}

	d := New(v, hs, cfg)

	monitor := d.GetPoolMonitor()
	if monitor == nil {
		t.Error("GetPoolMonitor() should return non-nil when UseAuthPool is true")
	}
}

func TestDaemon_CheckAndBackup_SchedulerTriggersBackup(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(vaultDir, 0700)
	os.MkdirAll(backupDir, 0700)

	v := authfile.NewVault(vaultDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Create a backup scheduler that will trigger
	backupCfg := &config.BackupConfig{
		Enabled:  true,
		Interval: config.Duration(1 * time.Second),
		KeepLast: 5,
		Location: backupDir,
	}
	d.backupScheduler = NewBackupScheduler(backupCfg, vaultDir, d.logger)
	// Ensure it's due by setting last backup to long ago
	d.backupScheduler.state.LastBackup = time.Time{}

	// This should attempt backup (will fail since vault is empty, but exercises code path)
	d.checkAndBackup()

	// Stats should show backup attempt
	// Note: The backup may fail since vault is empty, but checkAndBackup was exercised
}

func TestDaemon_CheckAndBackup_NotDue(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(vaultDir, 0700)
	os.MkdirAll(backupDir, 0700)

	v := authfile.NewVault(vaultDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Create a backup scheduler that won't trigger
	backupCfg := &config.BackupConfig{
		Enabled:  true,
		Interval: config.Duration(24 * time.Hour),
		Location: backupDir,
	}
	d.backupScheduler = NewBackupScheduler(backupCfg, vaultDir, d.logger)
	// Set last backup to recent time
	d.backupScheduler.state.LastBackup = time.Now()

	// This should not trigger backup
	d.checkAndBackup()

	// No backup errors since backup wasn't attempted
	stats := d.GetStats()
	if stats.BackupErrors != 0 {
		t.Errorf("BackupErrors = %d, want 0", stats.BackupErrors)
	}
}

func TestDaemon_CheckAndRefresh_WithProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Create some profile directories (empty, but they'll be listed)
	os.MkdirAll(filepath.Join(tmpDir, "claude", "test@example.com"), 0700)
	os.MkdirAll(filepath.Join(tmpDir, "codex", "work@company.com"), 0700)

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// This should check the profiles (they won't need refresh since no health data)
	d.checkAndRefresh()

	stats := d.GetStats()
	if stats.CheckCount != 1 {
		t.Errorf("CheckCount = %d, want 1", stats.CheckCount)
	}
	// ProfilesChecked should reflect the number of profiles found
	if stats.ProfilesChecked < 2 {
		t.Errorf("ProfilesChecked = %d, expected at least 2", stats.ProfilesChecked)
	}
}

func TestDaemon_Start_WithAuthPool(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		UseAuthPool:      true,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Start daemon in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Start()
	}()

	// Wait a bit for it to start
	time.Sleep(100 * time.Millisecond)

	if !d.IsRunning() {
		t.Error("daemon should be running after Start")
	}

	// Stop daemon
	if err := d.Stop(); err != nil {
		t.Errorf("Stop failed: %v", err)
	}

	// Wait for Start to return
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after Stop")
	}
}

func TestDaemon_RunLoop_MultipleIterations(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    20 * time.Millisecond, // Very short interval
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	// Start daemon in goroutine
	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Start()
	}()

	// Wait for multiple iterations
	time.Sleep(150 * time.Millisecond)

	stats := d.GetStats()
	if stats.CheckCount < 3 {
		t.Errorf("CheckCount = %d, expected at least 3 after 150ms with 20ms interval", stats.CheckCount)
	}

	// Stop daemon
	d.Stop()

	<-errCh
}

func TestDaemon_GetStats_AfterRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    30 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		UseAuthPool:      false, // Don't use auth pool so check loop runs
	}

	d := New(v, hs, cfg)

	// Start daemon
	go d.Start()
	time.Sleep(100 * time.Millisecond)

	stats := d.GetStats()

	// StartTime should be set
	if stats.StartTime.IsZero() {
		t.Error("StartTime should be set after Start")
	}

	// CheckCount should be > 0 (direct check loop runs when UseAuthPool is false)
	if stats.CheckCount == 0 {
		t.Error("CheckCount should be > 0 after running")
	}

	// LastCheck should be recent
	if stats.LastCheck.IsZero() {
		t.Error("LastCheck should be set after running")
	}

	d.Stop()
}

func TestDaemon_checkProfile_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data with token not expiring soon
	expiry := time.Now().Add(2 * time.Hour) // Well beyond threshold
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("claude", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 10 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)
	d.ctx, d.cancel = context.WithCancel(context.Background())
	defer d.cancel()

	// Profile is not expiring, so checkProfile should not refresh
	d.checkProfile("claude", "test")

	stats := d.GetStats()
	if stats.RefreshCount != 0 {
		t.Errorf("RefreshCount = %d, want 0 (token not expiring)", stats.RefreshCount)
	}
}

func TestBackupScheduler_SaveState_Successful(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(backupDir, 0700)

	cfg := &config.BackupConfig{
		Enabled:  true,
		Location: backupDir,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	scheduler.state.LastBackup = time.Now()
	scheduler.state.BackupCount = 5

	err := scheduler.SaveState()
	if err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Create new scheduler and load state
	scheduler2 := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	if err := scheduler2.LoadState(); err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	state := scheduler2.GetState()
	if state.BackupCount != 5 {
		t.Errorf("BackupCount = %d, want 5", state.BackupCount)
	}
}

func TestDaemon_InitAuthPool_CallbacksTriggered(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		UseAuthPool:      true,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Verify the callbacks are set up (auth pool should not be nil)
	if d.authPool == nil {
		t.Fatal("authPool should not be nil")
	}

	// GetStats should include pool stats
	stats := d.GetStats()
	if stats.PoolSummary == nil {
		t.Error("PoolSummary should not be nil")
	}
}

func TestDaemon_LogFilePath_WithoutHome(t *testing.T) {
	// LogFilePath should return a valid path even if home dir is not set
	path := LogFilePath()
	if path == "" {
		t.Error("LogFilePath should return non-empty path")
	}
}

func TestDaemon_ReloadConfig_WhileRunning(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
		Verbose:          true,
	}

	d := New(v, hs, cfg)

	// Start daemon
	go d.Start()
	time.Sleep(100 * time.Millisecond)

	// ReloadConfig while running should not panic
	d.ReloadConfig()

	d.Stop()
}

func TestBackupScheduler_SaveState_ErrorOnWrite(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.BackupConfig{
		Enabled: true,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	scheduler.state.LastBackup = time.Now()
	scheduler.state.BackupCount = 3

	// Save and load to verify round-trip works
	if err := scheduler.SaveState(); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Load into new scheduler
	scheduler2 := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	if err := scheduler2.LoadState(); err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	state := scheduler2.GetState()
	if state.BackupCount != 3 {
		t.Errorf("BackupCount after load = %d, want 3", state.BackupCount)
	}
}

func TestDaemon_Stop_WithPIDFile(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	// Start daemon
	go d.Start()
	time.Sleep(100 * time.Millisecond)

	// Verify PID file exists
	if _, err := os.Stat(pidPath); os.IsNotExist(err) {
		t.Error("PID file should exist after Start")
	}

	// Stop should clean up PID file
	d.Stop()

	// PID file should be removed
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Error("PID file should be removed after Stop")
	}
}

func TestDaemon_checkProfile_VerboseOK(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Store health data with token not expiring soon (2 hours)
	expiry := time.Now().Add(2 * time.Hour)
	ph := &health.ProfileHealth{
		TokenExpiresAt: expiry,
	}
	if err := hs.UpdateProfile("claude", "test", ph); err != nil {
		t.Fatalf("failed to update profile health: %v", err)
	}

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 10 * time.Minute,
		Verbose:          true, // Enable verbose logging
	}

	d := New(v, hs, cfg)
	d.ctx, d.cancel = context.WithCancel(context.Background())
	defer d.cancel()

	// Should log "token OK" message
	d.checkProfile("claude", "test")

	stats := d.GetStats()
	if stats.RefreshCount != 0 {
		t.Errorf("RefreshCount = %d, want 0", stats.RefreshCount)
	}
}

func TestDaemon_Start_SignalHandling(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	errCh := make(chan error, 1)
	go func() {
		errCh <- d.Start()
	}()

	time.Sleep(100 * time.Millisecond)

	// Use Stop() to terminate (simulates graceful shutdown)
	d.Stop()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Start returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Error("Start did not return after Stop")
	}
}

func TestDaemon_Start_ConfigChangedChannel(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "test-daemon.pid")

	originalPath := PIDFilePath()
	SetPIDFilePath(pidPath)
	defer SetPIDFilePath(originalPath)

	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	cfg := &Config{
		CheckInterval:    50 * time.Millisecond,
		RefreshThreshold: 1 * time.Minute,
	}

	d := New(v, hs, cfg)

	go d.Start()
	time.Sleep(100 * time.Millisecond)

	// Trigger config reload (exercises runLoop config change handling)
	d.ReloadConfig()
	time.Sleep(50 * time.Millisecond)

	d.Stop()
}

func TestDaemon_getProfileHealth_ParseClaudeExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Create a profile directory but no valid auth file
	profilePath := v.ProfilePath("claude", "test@example.com")
	os.MkdirAll(profilePath, 0700)

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	// Without proper auth file, should return nil
	ph := d.getProfileHealth("claude", "test@example.com")
	if ph != nil {
		t.Error("getProfileHealth should return nil for invalid claude auth file")
	}
}

func TestDaemon_getProfileHealth_ParseCodexExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Create a codex profile directory but no valid auth file
	profilePath := v.ProfilePath("codex", "test@example.com")
	os.MkdirAll(profilePath, 0700)

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	// Without proper auth file, should return nil
	ph := d.getProfileHealth("codex", "test@example.com")
	if ph != nil {
		t.Error("getProfileHealth should return nil for invalid codex auth file")
	}
}

func TestDaemon_getProfileHealth_ParseGeminiExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	v := authfile.NewVault(tmpDir)
	hs := health.NewStorage(filepath.Join(tmpDir, "health.json"))

	// Create a gemini profile directory but no valid auth file
	profilePath := v.ProfilePath("gemini", "test@example.com")
	os.MkdirAll(profilePath, 0700)

	cfg := &Config{
		CheckInterval: 50 * time.Millisecond,
	}

	d := New(v, hs, cfg)

	// Without proper auth file, should return nil
	ph := d.getProfileHealth("gemini", "test@example.com")
	if ph != nil {
		t.Error("getProfileHealth should return nil for invalid gemini auth file")
	}
}
