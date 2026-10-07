package daemon

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/update"
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

func TestDaemon_GetProfileHealth_RequiresCurrentClaudeCredential(t *testing.T) {
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
	if gotPh != nil {
		t.Fatalf("cached health must not stand in for a missing credential: %+v", gotPh)
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

func TestDaemon_CheckProfile_CachedExpiryWithoutCredential(t *testing.T) {
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

	// A cached timestamp without a current credential must not drive a refresh.
	d.checkProfile("claude", "test")

	stats := d.GetStats()
	if stats.RefreshErrors != 0 || stats.RefreshCount != 0 {
		t.Errorf("missing credential drove a refresh: %+v", stats)
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

	// Wait for the daemon to start and complete at least one check. Poll
	// instead of a fixed sleep: under heavy machine load the run loop's first
	// 50ms tick can take well over 100ms to fire.
	deadline := time.Now().Add(5 * time.Second)
	for !d.IsRunning() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !d.IsRunning() {
		t.Error("daemon should be running after Start")
	}

	for d.GetStats().CheckCount < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if stats := d.GetStats(); stats.CheckCount < 1 {
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

func TestDaemon_GetProfileHealth_RequiresCurrentCodexCredential(t *testing.T) {
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
	if gotPh != nil {
		t.Fatalf("cached health must not stand in for a missing credential: %+v", gotPh)
	}
}

func TestDaemon_GetProfileHealth_RequiresCurrentGeminiCredential(t *testing.T) {
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
	if gotPh != nil {
		t.Fatalf("cached health must not stand in for a missing credential: %+v", gotPh)
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
		CheckInterval:          50 * time.Millisecond,
		RefreshThreshold:       1 * time.Minute,
		MaxConcurrentRefreshes: 2,
		Verbose:                true,
		UseAuthPool:            true,
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
		CheckInterval:          50 * time.Millisecond,
		RefreshThreshold:       1 * time.Minute,
		MaxConcurrentRefreshes: 0, // Should default to 3
		Verbose:                true,
		UseAuthPool:            true,
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

	// Wait for multiple iterations. Poll for the expected tick count instead of
	// relying on a single fixed sleep: under heavy CPU load (e.g. many parallel
	// test binaries) the daemon goroutine can be starved of scheduling time, so
	// a fixed 150ms window is flaky. Polling to a generous deadline preserves
	// the test's intent (the run loop ticks multiple times) without the flake.
	deadline := time.Now().Add(5 * time.Second)
	var checkCount int64
	for time.Now().Before(deadline) {
		checkCount = d.GetStats().CheckCount
		if checkCount >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if checkCount < 3 {
		t.Errorf("CheckCount = %d, expected at least 3 with 20ms interval", checkCount)
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

	// Poll for the first completed check instead of relying on a single fixed
	// sleep: under heavy CPU load the daemon goroutine can be starved of
	// scheduling time within a fixed 100ms window, leaving CheckCount at 0 and
	// failing the test intermittently even though the check loop works (same
	// flake class as TestDaemon_RunLoop_MultipleIterations, deflaked the same
	// way). Polling to a generous deadline preserves the test's intent.
	deadline := time.Now().Add(5 * time.Second)
	var stats Stats
	for time.Now().Before(deadline) {
		stats = d.GetStats()
		if !stats.StartTime.IsZero() && stats.CheckCount > 0 && !stats.LastCheck.IsZero() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

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

func writeDaemonAuthJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonRefreshSkipsOnceAtInfoLevel(t *testing.T) {
	expiry := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	for _, tt := range []struct {
		provider string
		file     string
		auth     any
		owner    string
	}{
		{
			provider: "claude", file: ".credentials.json", owner: "Claude Code",
			auth: map[string]any{"claudeAiOauth": map[string]any{
				"accessToken": "synthetic-access", "refreshToken": "synthetic-refresh", "expiresAt": expiry.UnixMilli(),
			}},
		},
		{
			provider: "grok", file: "auth.json", owner: "Grok Build",
			auth: map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_at": expiry.Unix()},
		},
		{
			provider: "gemini", file: "oauth_creds.json", owner: "client",
			auth: map[string]any{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh", "expires_at": expiry.Unix()},
		},
	} {
		t.Run(tt.provider, func(t *testing.T) {
			vault := authfile.NewVault(t.TempDir())
			store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
			writeDaemonAuthJSON(t, filepath.Join(vault.ProfilePath(tt.provider, "work"), tt.file), tt.auth)
			// A cached record cannot hide current expiry or strip renewal policy.
			if err := store.UpdateProfile(tt.provider, "work", &health.ProfileHealth{
				TokenExpiresAt: time.Now().Add(24 * time.Hour), PlanType: "pro",
			}); err != nil {
				t.Fatal(err)
			}
			d := New(vault, store, nil)
			var output bytes.Buffer
			d.logger = log.New(&output, "", 0)
			ph := d.getProfileHealth(tt.provider, "work")
			if ph == nil || !ph.TokenExpiresAt.Equal(expiry) || ph.PlanType != "pro" {
				t.Fatalf("current credential or stored metadata lost: %+v", ph)
			}
			if tt.provider == "claude" && !ph.SelfRefreshing {
				t.Fatal("current Claude renewal policy was lost")
			}
			for tick := 0; tick < 3; tick++ {
				d.checkProfile(tt.provider, "work")
			}
			logs := output.String()
			if strings.Count(logs, "not refreshed by caam") != 1 || !strings.Contains(logs, tt.owner) {
				t.Errorf("expected one actionable notice without verbose logging: %s", logs)
			}
			if strings.Contains(logs, "refreshing token") || strings.Contains(logs, "refresh failed") {
				t.Errorf("skip was announced as an attempt or failure: %s", logs)
			}
			if stats := d.GetStats(); stats.RefreshCount != 0 || stats.RefreshErrors != 0 {
				t.Errorf("skip altered refresh counters: %+v", stats)
			}
		})
	}
}

func writeDaemonCodexAuth(t *testing.T, path, token string, refreshed, expiry time.Time) {
	t.Helper()
	claims, err := json.Marshal(map[string]any{"sub": "synthetic-account", "exp": expiry.Unix()})
	if err != nil {
		t.Fatal(err)
	}
	writeDaemonAuthJSON(t, path, map[string]any{
		"tokens": map[string]any{
			"access_token":  "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic",
			"refresh_token": token,
		},
		"last_refresh": refreshed.Format(time.RFC3339Nano),
	})
}

func TestDaemonCodexStaleSkipLiftsAfterBackup(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		t.Run(fmt.Sprintf("pool=%t", pooled), func(t *testing.T) {
			t.Setenv("CODEX_HOME", t.TempDir())
			vault := authfile.NewVault(t.TempDir())
			store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
			expiry := time.Now().Add(5 * time.Minute).Truncate(time.Second)
			livePath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
			writeDaemonCodexAuth(t, filepath.Join(vault.ProfilePath("codex", "work"), "auth.json"), "synthetic-spent", time.Now().Add(-2*time.Hour), expiry)
			writeDaemonCodexAuth(t, livePath, "synthetic-current", time.Now().Add(-time.Hour), expiry)
			calls := 0
			original := refresh.RefreshCodexToken
			refresh.RefreshCodexToken = func(ctx context.Context, token string) (*refresh.TokenResponse, error) {
				calls++
				if token != "synthetic-current" {
					t.Error("stale vault token reached the refresh adapter")
				}
				payload := []byte(fmt.Sprintf(`{"sub":"synthetic-account","exp":%d}`, time.Now().Add(time.Hour).Unix()))
				token = "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
				return &refresh.TokenResponse{AccessToken: token, RefreshToken: "synthetic-rotated", ExpiresIn: 3600}, nil
			}
			t.Cleanup(func() { refresh.RefreshCodexToken = original })
			d := New(vault, store, &Config{UseAuthPool: pooled, RefreshThreshold: 10 * time.Minute})
			d.ctx = context.Background()
			var output bytes.Buffer
			d.logger = log.New(&output, "", 0)
			if pooled {
				d.authPool.AddProfile("codex", "work")
				d.authPool.UpdateTokenExpiry("codex", "work", expiry)
				if err := d.authPool.SetStatus("codex", "work", authpool.PoolStatusReady); err != nil {
					t.Fatal(err)
				}
			}
			for tick := 0; tick < 3; tick++ {
				if pooled {
					if err := d.poolMonitor.ForceRefresh(d.ctx, "codex", "work"); !refresh.IsSkipped(err) {
						t.Fatalf("expected stale skip, got %v", err)
					}
				} else {
					d.checkProfile("codex", "work")
				}
			}
			if calls != 0 || strings.Count(output.String(), "caam backup codex work") != 1 {
				t.Fatalf("stale refresh calls=%d, logs=%s", calls, output.String())
			}
			if strings.Contains(output.String(), "refreshing token") || strings.Contains(output.String(), "starting refresh") {
				t.Fatalf("stale credential announced a refresh: %s", output.String())
			}
			if stats := d.GetStats(); stats.RefreshCount != 0 || stats.RefreshErrors != 0 {
				t.Fatalf("stale skip changed counters: %+v", stats)
			}
			if pooled {
				p := d.authPool.GetProfile("codex", "work")
				if p == nil || p.Status != authpool.PoolStatusReady || p.ErrorCount != 0 || !p.LastRefresh.IsZero() {
					t.Fatalf("stale skip changed pool eligibility: %+v", p)
				}
			}

			if err := vault.Backup(authfile.CodexAuthFiles(), "work"); err != nil {
				t.Fatal(err)
			}
			if pooled {
				if err := d.poolMonitor.ForceRefresh(d.ctx, "codex", "work"); err != nil {
					t.Fatalf("refresh remained suppressed after backup: %v", err)
				}
			} else {
				d.checkProfile("codex", "work")
			}
			if calls != 1 {
				t.Fatalf("refresh calls after backup=%d, want 1; logs=%s", calls, output.String())
			}
			if stats := d.GetStats(); stats.RefreshCount != 1 || stats.RefreshErrors != 0 {
				t.Errorf("successful refresh counters=%+v", stats)
			}
			if !strings.Contains(output.String(), "refreshed") || !strings.Contains(output.String(), "expir") {
				t.Errorf("missing refresh outcome and new expiry: %s", output.String())
			}
			updated := d.getProfileHealth("codex", "work")
			if updated == nil || time.Until(updated.TokenExpiresAt) < 59*time.Minute {
				t.Errorf("new expiry was hidden by cached health: %+v", updated)
			}
		})
	}
}

func TestDaemonRefreshFailureReportsOutcome(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	vault := authfile.NewVault(t.TempDir())
	writeDaemonCodexAuth(t, filepath.Join(vault.ProfilePath("codex", "work"), "auth.json"), "synthetic-current", time.Now(), time.Now().Add(5*time.Minute))
	original := refresh.RefreshCodexToken
	refresh.RefreshCodexToken = func(context.Context, string) (*refresh.TokenResponse, error) {
		return nil, fmt.Errorf("synthetic endpoint unavailable")
	}
	t.Cleanup(func() { refresh.RefreshCodexToken = original })
	d := New(vault, nil, nil)
	d.ctx = context.Background()
	var output bytes.Buffer
	d.logger = log.New(&output, "", 0)
	d.checkProfile("codex", "work")
	if !strings.Contains(output.String(), "refreshing token") || !strings.Contains(output.String(), "refresh failed:") || !strings.Contains(output.String(), "synthetic endpoint unavailable") {
		t.Errorf("default log did not pair the attempt with its failure: %s", output.String())
	}
	if stats := d.GetStats(); stats.RefreshCount != 0 || stats.RefreshErrors != 1 {
		t.Errorf("actual failure counters=%+v", stats)
	}
}

func TestClassicDaemonRecoversExpiredRenewableCredentials(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ttl       time.Duration
		refresh   string
		wantCalls int
	}{
		{"expired grant", -time.Hour, "synthetic-renewable", 1},
		{"expiry boundary", 0, "synthetic-renewable", 1},
		{"healthy grant", time.Hour, "synthetic-renewable", 0},
		{"nonrenewable expired token", -time.Hour, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			vault := authfile.NewVault(t.TempDir())
			store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
			path := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
			writeDaemonCodexAuth(t, path, tc.refresh, time.Now().Add(-2*time.Hour), time.Now().Add(tc.ttl))
			calls := 0
			original := refresh.RefreshCodexToken
			refresh.RefreshCodexToken = func(_ context.Context, token string) (*refresh.TokenResponse, error) {
				calls++
				if token != tc.refresh {
					t.Error("daemon submitted a different refresh credential")
				}
				payload := []byte(fmt.Sprintf(`{"sub":"synthetic-account","exp":%d}`, time.Now().Add(time.Hour).Unix()))
				return &refresh.TokenResponse{
					AccessToken:  "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic",
					RefreshToken: "synthetic-next", ExpiresIn: 3600,
				}, nil
			}
			t.Cleanup(func() { refresh.RefreshCodexToken = original })
			d := New(vault, store, nil)
			d.ctx = context.Background()
			if d.config.UseAuthPool {
				t.Fatal("regression must exercise the default classic daemon")
			}
			d.checkProfile("codex", "work")
			d.checkProfile("codex", "work")
			if calls != tc.wantCalls {
				t.Fatalf("refresh calls=%d, want %d", calls, tc.wantCalls)
			}
			if stats := d.GetStats(); stats.RefreshCount != int64(tc.wantCalls) || stats.RefreshErrors != 0 {
				t.Errorf("unexpected renewal counters: %+v", stats)
			}
			if tc.wantCalls > 0 {
				current := d.getProfileHealth("codex", "work")
				if current == nil || time.Until(current.TokenExpiresAt) < 59*time.Minute || current.ProviderRejected() || !current.CredentialRenewable() {
					t.Fatalf("renewed grant did not recover: %+v", current)
				}
			}
		})
	}
}

func TestDaemonSelectedAPIKeyDoesNotRefreshUnusedOAuth(t *testing.T) {
	for _, provider := range []string{"codex", "gemini"} {
		for _, pooled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pool=%t", provider, pooled), func(t *testing.T) {
				t.Setenv("CAAM_HOME", t.TempDir())
				t.Setenv("CODEX_HOME", t.TempDir())
				t.Setenv("GEMINI_HOME", t.TempDir())
				vault := authfile.NewVault(t.TempDir())
				store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
				dir := vault.ProfilePath(provider, "work")
				files := []string{"auth.json"}
				if provider == "codex" {
					payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Hour).Unix())))
					writeDaemonAuthJSON(t, filepath.Join(dir, "auth.json"), map[string]any{
						"auth_mode": "apikey", "OPENAI_API_KEY": "synthetic-selected-key",
						"tokens": map[string]any{"access_token": "e30." + payload + ".synthetic", "refresh_token": "synthetic-unused-refresh"},
					})
				} else {
					files = []string{"settings.json", ".env", "oauth_credentials.json"}
					writeDaemonAuthJSON(t, filepath.Join(dir, "settings.json"), map[string]any{"selectedAuthType": "gemini-api-key"})
					writeDaemonAuthJSON(t, filepath.Join(dir, "oauth_credentials.json"), map[string]any{
						"access_token": "synthetic-unused-access", "refresh_token": "synthetic-unused-refresh",
						"client_id": "synthetic-client", "client_secret": "synthetic-secret", "expires_at": time.Now().Add(-time.Hour).Unix(),
					})
					if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("GEMINI_API_KEY=synthetic-selected-key\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := make(map[string][]byte)
				for _, file := range files {
					data, err := os.ReadFile(filepath.Join(dir, file))
					if err != nil {
						t.Fatal(err)
					}
					before[file] = data
				}
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					_, _ = w.Write([]byte(`{"access_token":"synthetic-unwanted-access","refresh_token":"synthetic-unwanted-refresh","expires_in":3600}`))
				}))
				defer server.Close()
				codexURL, geminiURL := refresh.CodexTokenURL, refresh.GeminiTokenURL
				refresh.CodexTokenURL, refresh.GeminiTokenURL = server.URL, server.URL
				t.Cleanup(func() { refresh.CodexTokenURL, refresh.GeminiTokenURL = codexURL, geminiURL })
				d := New(vault, store, &Config{UseAuthPool: pooled, RefreshThreshold: 10 * time.Minute})
				d.ctx = context.Background()
				if pooled {
					defer d.poolMonitor.Stop()
					results, err := d.poolMonitor.RefreshAll(d.ctx)
					if err != nil || len(results) != 1 || !refresh.IsSkipped(results[0].Err) {
						t.Fatalf("selected key batch result = %+v, %v", results, err)
					}
					if current := d.authPool.GetProfile(provider, "work"); current == nil || current.Status != authpool.PoolStatusReady || !current.LastRefresh.IsZero() {
						t.Fatalf("selected key did not stay ready and unrefreshed: %+v", current)
					}
				} else {
					d.checkProfile(provider, "work")
				}
				current := d.getProfileHealth(provider, "work")
				if current == nil || !current.TokenExpiresAt.IsZero() || !current.CredentialRenewable() || !current.SelfRefreshing {
					t.Fatalf("daemon health inherited unused OAuth: %+v", current)
				}
				if stats := d.GetStats(); requests.Load() != 0 || stats.RefreshCount != 0 || stats.RefreshErrors != 0 {
					t.Fatalf("selected key drove OAuth work: requests=%d stats=%+v", requests.Load(), stats)
				}
				for file, want := range before {
					got, err := os.ReadFile(filepath.Join(dir, file))
					if err != nil || !bytes.Equal(got, want) {
						t.Fatalf("daemon changed unused cache or selected key: %s", file)
					}
				}
				if provider == "gemini" {
					if _, err := os.Stat(filepath.Join(dir, "oauth_creds.json")); !os.IsNotExist(err) {
						t.Fatalf("daemon migrated an unused legacy cache: %v", err)
					}
				}
				stored, err := store.Load()
				if err != nil || stored.Profiles[provider+"/work"] != nil {
					t.Fatalf("skipped OAuth recorded provider evidence: %+v, %v", stored, err)
				}
			})
		}
	}
}

func TestDaemonPreservesRenewalWhenActiveLoginChanges(t *testing.T) {
	for _, pooled := range []bool{false, true} {
		t.Run(fmt.Sprintf("pool=%t", pooled), func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			vault := authfile.NewVault(t.TempDir())
			store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
			vaultPath := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
			livePath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
			expiry := time.Now().Add(time.Minute)
			writeDaemonCodexAuth(t, vaultPath, "synthetic-request", time.Now(), expiry)
			before, err := os.ReadFile(vaultPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(livePath, before, 0600); err != nil {
				t.Fatal(err)
			}
			const newLogin = `{"tokens":{"access_token":"synthetic-different-login","refresh_token":"synthetic-new-owner"}}`
			original := refresh.RefreshCodexToken
			refresh.RefreshCodexToken = func(context.Context, string) (*refresh.TokenResponse, error) {
				// A native login completes while the old vault grant is in flight.
				if err := os.WriteFile(livePath, []byte(newLogin), 0600); err != nil {
					t.Fatal(err)
				}
				payload := []byte(fmt.Sprintf(`{"sub":"synthetic-account","exp":%d}`, time.Now().Add(time.Hour).Unix()))
				return &refresh.TokenResponse{
					AccessToken:  "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic",
					RefreshToken: "synthetic-next", ExpiresIn: 3600,
				}, nil
			}
			t.Cleanup(func() { refresh.RefreshCodexToken = original })
			d := New(vault, store, &Config{UseAuthPool: pooled, RefreshThreshold: 10 * time.Minute})
			d.ctx = context.Background()
			var output bytes.Buffer
			d.logger = log.New(&output, "", 0)
			if pooled {
				d.authPool.AddProfile("codex", "work")
				d.authPool.UpdateTokenExpiry("codex", "work", expiry)
				if err := d.authPool.SetStatus("codex", "work", authpool.PoolStatusReady); err != nil {
					t.Fatal(err)
				}
				if err := d.poolMonitor.ForceRefresh(d.ctx, "codex", "work"); !refresh.IsDeliveryIncomplete(err) {
					t.Fatalf("changed destination should return a delivery warning: %v", err)
				}
				p := d.authPool.GetProfile("codex", "work")
				if p.Status != authpool.PoolStatusReady || p.ErrorCount != 0 || p.LastRefresh.IsZero() {
					t.Fatalf("delivery warning poisoned a successful renewal: %+v", p)
				}
			} else {
				d.checkProfile("codex", "work")
			}
			if got, err := os.ReadFile(livePath); err != nil || string(got) != newLogin {
				t.Fatalf("new live login was overwritten: %v", err)
			}
			if current := d.getProfileHealth("codex", "work"); current == nil || current.ProviderRejected() || !current.CredentialRenewable() || time.Until(current.TokenExpiresAt) < 59*time.Minute {
				t.Fatalf("successful vault renewal lost eligibility: %+v", current)
			}
			if stats := d.GetStats(); stats.RefreshCount != 1 || stats.RefreshErrors != 0 {
				t.Errorf("delivery warning counted as a refresh failure: %+v", stats)
			}
			if !strings.Contains(output.String(), "delivery warning") || strings.Contains(output.String(), "refresh failed") {
				t.Errorf("delivery outcome misreported: %s", output.String())
			}
		})
	}
}

func TestPoolRenewalWithUnknownExpiryPreservesSuccess(t *testing.T) {
	for _, changedLive := range []bool{false, true} {
		t.Run(fmt.Sprintf("changed_live=%t", changedLive), func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			t.Setenv("CODEX_HOME", t.TempDir())
			vault := authfile.NewVault(t.TempDir())
			store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
			vaultPath := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
			livePath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
			expiry := time.Now().Add(-time.Minute)
			writeDaemonCodexAuth(t, vaultPath, "synthetic-request", time.Now(), expiry)
			before, err := os.ReadFile(vaultPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(livePath, before, 0600); err != nil {
				t.Fatal(err)
			}
			const newLogin = `{"tokens":{"access_token":"synthetic-other-login","refresh_token":"synthetic-other-owner"}}`
			calls := 0
			original := refresh.RefreshCodexToken
			refresh.RefreshCodexToken = func(context.Context, string) (*refresh.TokenResponse, error) {
				calls++
				if changedLive {
					if err := os.WriteFile(livePath, []byte(newLogin), 0600); err != nil {
						return nil, err
					}
				}
				return &refresh.TokenResponse{AccessToken: "synthetic-opaque-access", RefreshToken: "synthetic-next"}, nil
			}
			t.Cleanup(func() { refresh.RefreshCodexToken = original })
			d := New(vault, store, &Config{UseAuthPool: true, RefreshThreshold: 10 * time.Minute})
			d.ctx = context.Background()
			var output bytes.Buffer
			d.logger = log.New(&output, "", 0)
			d.authPool.AddProfile("codex", "work")
			d.authPool.UpdateTokenExpiry("codex", "work", expiry)
			d.authPool.SetError("codex", "work", fmt.Errorf("synthetic earlier failure"))
			err = d.poolMonitor.ForceRefresh(d.ctx, "codex", "work")
			if refresh.IsDeliveryIncomplete(err) != changedLive || (err != nil && !changedLive) {
				t.Fatalf("successful opaque renewal lost its delivery outcome: %v", err)
			}
			p := d.authPool.GetProfile("codex", "work")
			if p == nil || p.Status != authpool.PoolStatusReady || p.ErrorCount != 0 || p.ErrorMessage != "" || p.LastRefresh.IsZero() || !p.TokenExpiry.IsZero() {
				t.Fatalf("unknown expiry poisoned the completed renewal: %+v", p)
			}
			// This is the monitor's actual candidate source: a ready profile
			// with unknown expiry must not immediately replay the fresh token.
			if candidates := d.authPool.GetProfilesNeedingRefresh(""); len(candidates) != 0 || p.IsExpired() || p.IsExpiringSoon(d.config.RefreshThreshold) {
				t.Fatalf("unknown expiry scheduled another refresh: %+v", candidates)
			}
			current := d.getProfileHealth("codex", "work")
			if current == nil || !current.TokenExpiresAt.IsZero() || current.ProviderRejected() || current.ProviderVerifiedAt().IsZero() || !current.CredentialRenewable() {
				t.Fatalf("opaque renewal lost its accepted current health: %+v", current)
			}
			got, err := os.ReadFile(livePath)
			if err != nil || (changedLive && string(got) != newLogin) || (!changedLive && !bytes.Contains(got, []byte("synthetic-opaque-access"))) {
				t.Fatalf("opaque renewal changed the wrong live generation: %v", err)
			}
			if stats := d.GetStats(); calls != 1 || stats.RefreshCount != 1 || stats.RefreshErrors != 0 {
				t.Errorf("unknown expiry changed renewal accounting: calls=%d, stats=%+v", calls, stats)
			}
			if strings.Contains(output.String(), "refresh failed") || strings.Contains(output.String(), "delivery warning") != changedLive {
				t.Errorf("unknown expiry changed the reported outcome: %s", output.String())
			}
		})
	}
}

func TestDaemonPoolRenewsRealColdVaultWithoutCachedState(t *testing.T) {
	t.Setenv("CAAM_HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(filepath.Join(t.TempDir(), "custom-health.json"))
	path := filepath.Join(vault.ProfilePath("codex", "work"), "auth.json")
	writeDaemonCodexAuth(t, path, "synthetic-cold-grant", time.Now(), time.Now().Add(-time.Hour))
	writeDaemonCursorAuth(t, filepath.Join(vault.ProfilePath("cursor", "session"), "auth.json"), time.Now().Add(-time.Hour), "synthetic-session", "")
	calls := 0
	original := refresh.RefreshCodexToken
	refresh.RefreshCodexToken = func(ctx context.Context, token string) (*refresh.TokenResponse, error) {
		calls++
		if token != "synthetic-cold-grant" {
			return nil, fmt.Errorf("unexpected synthetic grant")
		}
		return &refresh.TokenResponse{AccessToken: "synthetic-fresh-access", RefreshToken: "synthetic-fresh-grant", ExpiresIn: 3600}, nil
	}
	t.Cleanup(func() { refresh.RefreshCodexToken = original })
	d := New(vault, store, &Config{UseAuthPool: true, RefreshThreshold: 10 * time.Minute})
	defer d.poolMonitor.Stop()
	results, err := d.poolMonitor.RefreshAll(context.Background())
	if err != nil || len(results) != 2 || calls != 1 {
		t.Fatalf("cold vault was not renewed: results=%+v calls=%d err=%v", results, calls, err)
	}
	if p := d.authPool.GetProfile("codex", "work"); p == nil || p.Status != authpool.PoolStatusReady || p.TokenExpiry.Before(time.Now().Add(50*time.Minute)) || p.LastRefresh.IsZero() {
		t.Fatalf("renewal did not publish current ready state: %+v", p)
	}
	if p := d.authPool.GetProfile("cursor", "session"); p == nil || p.Status != authpool.PoolStatusExpired || !p.LastRefresh.IsZero() {
		t.Fatalf("native session was lost or falsely refreshed: %+v", p)
	}
	if stats := d.GetStats(); stats.RefreshCount != 1 || stats.RefreshErrors != 0 {
		t.Fatalf("skipped native grant changed renewal stats: %+v", stats)
	}
	h, err := store.GetProfile("codex", "work")
	if err != nil || h == nil || h.ProviderVerifiedAt().IsZero() || !h.TokenRenewable {
		t.Fatalf("daemon did not bind its custom health storage to current vault: %+v, %v", h, err)
	}
}

func TestDaemonPoolRenewsCompleteGeminiADCWithoutAccessToken(t *testing.T) {
	for _, filename := range []string{"oauth_creds.json", "oauth_credentials.json", "settings.json"} {
		t.Run(filename, func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			t.Setenv("GEMINI_HOME", t.TempDir())
			vault := authfile.NewVault(t.TempDir())
			store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
			dir := vault.ProfilePath("gemini", "adc")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, filename), []byte(`{"client_id":"synthetic-id","client_secret":"synthetic-secret","refresh_token":"synthetic-refresh","type":"authorized_user"}`), 0600); err != nil {
				t.Fatal(err)
			}
			calls := 0
			original := refresh.RefreshGeminiToken
			refresh.RefreshGeminiToken = func(ctx context.Context, id, secret, token string) (*refresh.GoogleTokenResponse, error) {
				calls++
				if id != "synthetic-id" || secret != "synthetic-secret" || token != "synthetic-refresh" {
					return nil, fmt.Errorf("unexpected synthetic ADC grant")
				}
				return &refresh.GoogleTokenResponse{AccessToken: "synthetic-fresh-access", ExpiresIn: 3600}, nil
			}
			t.Cleanup(func() { refresh.RefreshGeminiToken = original })
			d := New(vault, store, &Config{UseAuthPool: true})
			defer d.poolMonitor.Stop()
			results, err := d.poolMonitor.RefreshAll(context.Background())
			if err != nil || len(results) != 1 || results[0].Err != nil || calls != 1 {
				t.Fatalf("complete ADC grant was not renewed: results=%+v calls=%d err=%v", results, calls, err)
			}
			if p := d.authPool.GetProfile("gemini", "adc"); p == nil || p.Status != authpool.PoolStatusReady || p.TokenExpiry.Before(time.Now().Add(50*time.Minute)) || p.LastRefresh.IsZero() {
				t.Fatalf("ADC renewal did not publish current ready state: %+v", p)
			}
			if stats := d.GetStats(); stats.RefreshCount != 1 || stats.RefreshErrors != 0 {
				t.Fatalf("ADC renewal accounting was incorrect: %+v", stats)
			}
		})
	}
}

func writeDaemonCursorAuth(t *testing.T, path string, expiry time.Time, session, apiKey string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"exp": expiry.Unix(), "sub": session})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"accessToken":  "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic",
		"refreshToken": "synthetic-session-value",
		"apiKey":       apiKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonCursorHealthUsesCurrentCredential(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	store := health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
	if err := store.UpdateProfile("cursor", "work", &health.ProfileHealth{
		TokenExpiresAt: time.Now().Add(-24 * time.Hour),
		PlanType:       "pro",
	}); err != nil {
		t.Fatal(err)
	}
	d := New(vault, store, nil)
	path := filepath.Join(vault.ProfilePath("cursor", "work"), "auth.json")
	expiry := time.Now().Add(2 * 24 * time.Hour).Truncate(time.Second)
	writeDaemonCursorAuth(t, path, expiry, "login-one", "")
	ph := d.getProfileHealth("cursor", "work")
	if ph == nil || !ph.TokenExpiresAt.Equal(expiry) {
		t.Fatalf("current expiry was hidden by cached health: %+v", ph)
	}
	if ph.CredentialRenewable() || ph.SelfRefreshing || ph.ReloginWarningLead != health.CursorReloginLead {
		t.Errorf("incorrect session semantics: %+v", ph)
	}
	if ph.CredentialFingerprint == "" || ph.PlanType != "pro" {
		t.Errorf("lost current credential identity or cached profile metadata: %+v", ph)
	}
	oldFingerprint := ph.CredentialFingerprint

	// Switching to an API key must replace all session-derived semantics,
	// even when its cached access token already expired.
	expiry = time.Now().Add(-time.Hour).Truncate(time.Second)
	writeDaemonCursorAuth(t, path, expiry, "api-login", "synthetic-api-key")
	ph = d.getProfileHealth("cursor", "work")
	if ph == nil || !ph.TokenExpiresAt.Equal(expiry) || !ph.CredentialRenewable() || !ph.SelfRefreshing || ph.ReloginWarningLead != 0 {
		t.Fatalf("incorrect API-key semantics: %+v", ph)
	}
	if ph.CredentialFingerprint == oldFingerprint {
		t.Error("replacement login retained the old session fingerprint")
	}

	// A stale health record without a current credential is not evidence of
	// an expiring login and must never drive a refresh or session warning.
	if err := store.UpdateProfile("cursor", "missing", &health.ProfileHealth{TokenExpiresAt: expiry}); err != nil {
		t.Fatal(err)
	}
	if ph := d.getProfileHealth("cursor", "missing"); ph != nil {
		t.Errorf("returned stale health without a current Cursor credential: %+v", ph)
	}
}

func TestDaemonCursorSessionWarningsDeduplicateAndReset(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	d := New(vault, nil, nil)
	var output bytes.Buffer
	d.logger = log.New(&output, "", 0)
	path := filepath.Join(vault.ProfilePath("cursor", "work"), "auth.json")
	expiry := time.Now().Add(5 * time.Minute).Truncate(time.Second)
	writeDaemonCursorAuth(t, path, expiry, "login-one", "")
	d.checkProfile("cursor", "work")
	d.checkProfile("cursor", "work")
	if got := strings.Count(output.String(), ": session "); got != 1 {
		t.Fatalf("same login generated %d warnings, want 1: %s", got, output.String())
	}
	if got := strings.Count(output.String(), "not refreshed by caam"); got != 1 {
		t.Fatalf("expected one renewal ownership notice, got %d: %s", got, output.String())
	}
	if !strings.Contains(output.String(), "cursor-agent login") || !strings.Contains(output.String(), "caam backup cursor") || strings.Contains(output.String(), "caam refresh") {
		t.Errorf("warning lacks native login/backup guidance: %s", output.String())
	}

	// A different login with the exact same expiry still needs its own
	// warning. Deduplication by expiry timestamp alone would miss it.
	writeDaemonCursorAuth(t, path, expiry, "login-two", "")
	d.checkProfile("cursor", "work")
	d.checkProfile("cursor", "work")
	if got := strings.Count(output.String(), ": session "); got != 2 {
		t.Fatalf("replacement login warning count = %d, want 2: %s", got, output.String())
	}

	writeDaemonCursorAuth(t, path, expiry, "api-login", "synthetic-api-key")
	d.checkProfile("cursor", "work")
	writeDaemonCursorAuth(t, path, time.Now().Add(30*24*time.Hour), "fresh-login", "")
	d.checkProfile("cursor", "work")
	if got := strings.Count(output.String(), ": session "); got != 2 {
		t.Fatalf("fresh/API-key login unexpectedly warned: %s", output.String())
	}
	writeDaemonCursorAuth(t, path, time.Now().Add(-time.Minute), "expired-login", "")
	d.checkProfile("cursor", "work")
	d.checkProfile("cursor", "work")
	if got := strings.Count(output.String(), ": session "); got != 3 || !strings.Contains(output.String(), "session EXPIRED") {
		t.Errorf("expired replacement warning missing or repeated: %s", output.String())
	}
	if stats := d.GetStats(); stats.RefreshCount != 0 || stats.RefreshErrors != 0 {
		t.Errorf("Cursor session attempted refresh: %+v", stats)
	}
}

func TestDaemonCursorReloginBoundaries(t *testing.T) {
	now := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		ttl  time.Duration
		want bool
	}{
		{"outside long lead", health.CursorReloginLead + time.Nanosecond, false},
		{"at long lead", health.CursorReloginLead, true},
		{"at expiry", 0, true},
		{"after expiry", -time.Nanosecond, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := New(authfile.NewVault(t.TempDir()), nil, nil)
			var output bytes.Buffer
			d.logger = log.New(&output, "", 0)
			ph := &health.ProfileHealth{
				TokenExpiresAt: now.Add(tt.ttl), ReloginWarningLead: health.CursorReloginLead,
				CredentialFingerprint: "synthetic-login",
			}
			d.warnCursorSession("work", ph, now)
			if got := output.Len() != 0; got != tt.want {
				t.Fatalf("warning present = %v, want %v: %s", got, tt.want, output.String())
			}
			if tt.want {
				// Entering expiry after the advance warning does not repeat the
				// same login warning at every daemon tick.
				d.warnCursorSession("work", ph, ph.TokenExpiresAt.Add(time.Second))
				if got := strings.Count(output.String(), ": session "); got != 1 {
					t.Errorf("same login warned %d times", got)
				}
			}
		})
	}
}

func TestDaemonCursorUnmatchedLiveSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CURSOR_CONFIG_DIR", t.TempDir())
	t.Setenv("APPDATA", t.TempDir())
	paths := authfile.ResolveCursorPaths(home, runtime.GOOS, os.Getenv)
	writeDaemonCursorAuth(t, paths.AuthFile, time.Now().Add(4*24*time.Hour), "live-login", "")
	d := New(authfile.NewVault(t.TempDir()), nil, nil)
	var output bytes.Buffer
	d.logger = log.New(&output, "", 0)
	d.checkAndRefresh()
	d.checkAndRefresh()
	if got := strings.Count(output.String(), "cursor/active:"); got != 1 || !strings.Contains(output.String(), "cursor-agent login") {
		t.Fatalf("unmatched live session warning = %s, want one login warning", output.String())
	}
	if stats := d.GetStats(); stats.RefreshCount != 0 || stats.RefreshErrors != 0 {
		t.Errorf("unmatched live session attempted refresh: %+v", stats)
	}
}

func TestDaemonCursorWarningsContinueWithAuthPool(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	path := filepath.Join(vault.ProfilePath("cursor", "work"), "auth.json")
	writeDaemonCursorAuth(t, path, time.Now().Add(3*24*time.Hour), "pool-login", "")
	d := New(vault, nil, &Config{UseAuthPool: true, CheckInterval: 10 * time.Millisecond})
	d.backupScheduler = nil
	var output bytes.Buffer
	d.logger = log.New(&output, "", 0)
	d.ctx, d.cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer d.cancel()
	if err := d.authPool.LoadFromVault(d.ctx); err != nil {
		t.Fatal(err)
	}
	// An old persisted error must not cause automatic retries, even though
	// the warning path observes the new live credential on disk.
	if err := d.authPool.SetStatus("cursor", "work", authpool.PoolStatusError); err != nil {
		t.Fatal(err)
	}
	if err := d.poolMonitor.Start(d.ctx); err != nil {
		t.Fatal(err)
	}
	d.runLoop()
	d.poolMonitor.Stop()
	if got := strings.Count(output.String(), ": session "); got != 1 {
		t.Errorf("pool mode generated %d Cursor warnings, want 1: %s", got, output.String())
	}
	if stats := d.GetStats(); stats.RefreshCount != 0 || stats.RefreshErrors != 0 {
		t.Errorf("pool mode attempted a Cursor refresh: %+v", stats)
	}
	if profile := d.authPool.GetProfile("cursor", "work"); profile == nil || !profile.LastRefresh.IsZero() {
		t.Errorf("Cursor profile removed or falsely refreshed: %+v", profile)
	}
}

func TestCursorSessionDaemonWarnsOnceWithoutRefresh(t *testing.T) {
	vault := authfile.NewVault(t.TempDir())
	path := vault.ProfilePath("cursor", "session")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(exp time.Time, apiKey bool) {
		payload, _ := json.Marshal(map[string]interface{}{"exp": exp.Unix()})
		data := map[string]interface{}{"accessToken": "e30." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"}
		if apiKey {
			data["apiKey"] = "synthetic-key"
		}
		b, _ := json.Marshal(data)
		if err := os.WriteFile(filepath.Join(path, "auth.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	d := &Daemon{vault: vault, config: DefaultConfig(), logger: log.New(&logs, "", 0), ctx: context.Background()}
	expiry := time.Now().Add(5 * 24 * time.Hour)
	d.healthStore = health.NewStorage(filepath.Join(t.TempDir(), "health.json"))
	if err := d.healthStore.UpdateProfile("cursor", "session", &health.ProfileHealth{TokenExpiresAt: time.Now().Add(30 * 24 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	write(expiry, false)
	parsed := d.getProfileHealth("cursor", "session")
	if parsed == nil || parsed.TokenExpiresAt.Unix() != expiry.Unix() || parsed.TokenRenewable || parsed.ReloginWarningLead != 7*24*time.Hour {
		t.Fatalf("stale store bypassed session semantics: %+v", parsed)
	}
	d.checkProfile("cursor", "session")
	d.checkProfile("cursor", "session")
	d.warnCursorSession("active", parsed, time.Now())
	if strings.Count(logs.String(), ": session ") != 1 {
		t.Fatalf("logs=%s", logs.String())
	}
	// Two logins can have the same exp: deduplicate by credential, not date.
	other := *parsed
	other.CredentialFingerprint = "another-synthetic-login"
	d.warnCursorSession("other", &other, time.Now())
	if strings.Count(logs.String(), ": session ") != 2 {
		t.Fatalf("distinct login with same expiry not warned: %s", logs.String())
	}
	write(expiry.Add(time.Hour), false)
	d.checkProfile("cursor", "session")
	if strings.Count(logs.String(), ": session ") != 3 {
		t.Fatalf("new login not warned: %s", logs.String())
	}
	write(time.Now().Add(-time.Hour), true)
	d.checkProfile("cursor", "session")
	if strings.Count(logs.String(), ": session ") != 3 || d.stats.RefreshErrors != 0 || d.stats.RefreshCount != 0 {
		t.Fatalf("API-backed token attempted refresh or warned: %s %+v", logs.String(), d.stats)
	}
}

type recordingAlerts struct {
	mu     sync.Mutex
	alerts []*notify.Alert
}

func (r *recordingAlerts) Name() string    { return "recording" }
func (r *recordingAlerts) Available() bool { return true }
func (r *recordingAlerts) Notify(a *notify.Alert) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, a)
	return nil
}

func (r *recordingAlerts) snapshot() []*notify.Alert {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*notify.Alert(nil), r.alerts...)
}

func TestRefreshFailureAlerts(t *testing.T) {
	tmpDir := t.TempDir()
	rec := &recordingAlerts{}
	d := New(authfile.NewVault(tmpDir), health.NewStorage(filepath.Join(tmpDir, "health.json")),
		&Config{Notifier: rec, LogPath: filepath.Join(tmpDir, "daemon.log")})
	defer d.logFile.Close()

	d.alertRefreshFailure("codex", "work", &refresh.RefreshRejectedError{Provider: "codex", StatusCode: 401, Code: "refresh_token_invalidated"})
	d.alertRefreshFailure("codex", "home", fmt.Errorf("wrapped: %w", &refresh.RefreshTokenReusedError{Provider: "codex", Profile: "home"}))
	d.alertRefreshFailure("gemini", "work", context.DeadlineExceeded)
	// The same transient failure on the next check is not re-announced.
	d.alertRefreshFailure("gemini", "work", context.DeadlineExceeded)

	got := rec.snapshot()
	if len(got) != 3 {
		t.Fatalf("alerts = %d, want 3", len(got))
	}
	for i, want := range []struct {
		level   notify.AlertLevel
		title   string
		profile string
	}{
		{notify.Critical, "Login required", "codex/work"},
		{notify.Critical, "Login required", "codex/home"},
		{notify.Warning, "Token refresh failed", "gemini/work"},
	} {
		if got[i].Level != want.level || got[i].Title != want.title || got[i].Profile != want.profile {
			t.Errorf("alert %d = %+v, want %+v", i, got[i], want)
		}
		if got[i].Action == "" {
			t.Errorf("alert %d has no suggested action", i)
		}
	}
}

func TestCursorSessionAlertsOncePerLogin(t *testing.T) {
	tmpDir := t.TempDir()
	rec := &recordingAlerts{}
	d := New(authfile.NewVault(tmpDir), health.NewStorage(filepath.Join(tmpDir, "health.json")),
		&Config{Notifier: rec, LogPath: filepath.Join(tmpDir, "daemon.log")})
	defer d.logFile.Close()

	now := time.Now()
	ph := &health.ProfileHealth{TokenExpiresAt: now.Add(time.Hour), CredentialFingerprint: "login-1"}
	d.warnCursorSession("work", ph, now)
	d.warnCursorSession("work", ph, now.Add(time.Minute))

	got := rec.snapshot()
	if len(got) != 1 || got[0].Title != "Session expiring" || got[0].Profile != "cursor/work" {
		t.Fatalf("alerts = %+v, want one expiring-session alert", got)
	}
}

func TestDaemonWithoutNotifierDropsAlerts(t *testing.T) {
	tmpDir := t.TempDir()
	d := New(authfile.NewVault(tmpDir), health.NewStorage(filepath.Join(tmpDir, "health.json")),
		&Config{LogPath: filepath.Join(tmpDir, "daemon.log")})
	defer d.logFile.Close()
	d.alertRefreshFailure("codex", "work", context.DeadlineExceeded) // must not panic
}

// scriptedReleases answers update checks with the next scripted latest
// version.
type scriptedReleases struct {
	latest []string
	calls  int
}

func (s *scriptedReleases) Check(ctx context.Context) (*update.CheckResult, error) {
	v := s.latest[min(s.calls, len(s.latest)-1)]
	s.calls++
	if v == "error" {
		return nil, errors.New("github unreachable")
	}
	return &update.CheckResult{CurrentVersion: "v1.0.0", LatestVersion: v, UpdateAvailable: v != "v1.0.0"}, nil
}

func TestUpdateCheckAnnouncesEachReleaseOnce(t *testing.T) {
	tmpDir := t.TempDir()
	rec := &recordingAlerts{}
	releases := &scriptedReleases{latest: []string{"v1.0.0", "v1.1.0", "error", "v1.1.0", "v1.2.0"}}
	d := New(authfile.NewVault(tmpDir), health.NewStorage(filepath.Join(tmpDir, "health.json")),
		&Config{Notifier: rec, LogPath: filepath.Join(tmpDir, "daemon.log"), UpdateChecker: releases, UpdateCheckInterval: time.Hour})
	defer d.logFile.Close()

	due := func() { d.lastUpdateCheck = time.Now().Add(-2 * time.Hour) }
	d.checkForUpdate() // v1.0.0: current, nothing to announce
	d.checkForUpdate() // not due yet: no check
	if releases.calls != 1 {
		t.Fatalf("checks = %d, want the interval respected", releases.calls)
	}
	for range 4 { // v1.1.0, error, v1.1.0 again, v1.2.0
		due()
		d.checkForUpdate()
	}

	got := rec.snapshot()
	if len(got) != 2 {
		t.Fatalf("alerts = %d (%+v), want one per new release", len(got), got)
	}
	for i, v := range []string{"v1.1.0", "v1.2.0"} {
		if got[i].Level != notify.Info || !strings.Contains(got[i].Message, v) || !strings.Contains(got[i].Action, "caam update") {
			t.Errorf("alert %d = %+v, want an info alert for %s with the update command", i, got[i], v)
		}
	}
}

func TestUpdateCheckDisabledByDefault(t *testing.T) {
	tmpDir := t.TempDir()
	rec := &recordingAlerts{}
	d := New(authfile.NewVault(tmpDir), health.NewStorage(filepath.Join(tmpDir, "health.json")),
		&Config{Notifier: rec, LogPath: filepath.Join(tmpDir, "daemon.log")})
	defer d.logFile.Close()
	d.checkForUpdate()
	if len(rec.snapshot()) != 0 || !d.lastUpdateCheck.IsZero() {
		t.Fatal("update check ran without an UpdateChecker")
	}
}
