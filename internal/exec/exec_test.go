package exec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"golang.org/x/term"
)

// =============================================================================
// Mock Provider for Testing
// =============================================================================

type mockProvider struct {
	id          string
	displayName string
	defaultBin  string
	authModes   []provider.AuthMode
	envVars     map[string]string
	envErr      error
	statusResp  *provider.ProfileStatus
	statusErr   error
	loginErr    error
	logoutErr   error
}

func (m *mockProvider) ID() string          { return m.id }
func (m *mockProvider) DisplayName() string { return m.displayName }
func (m *mockProvider) DefaultBin() string  { return m.defaultBin }

func (m *mockProvider) SupportedAuthModes() []provider.AuthMode {
	return m.authModes
}

func (m *mockProvider) AuthFiles() []provider.AuthFileSpec {
	return nil
}

func (m *mockProvider) PrepareProfile(_ context.Context, _ *profile.Profile) error {
	return nil
}

func (m *mockProvider) Env(_ context.Context, _ *profile.Profile) (map[string]string, error) {
	return m.envVars, m.envErr
}

func (m *mockProvider) Login(_ context.Context, _ *profile.Profile) error {
	return m.loginErr
}

func (m *mockProvider) Logout(_ context.Context, _ *profile.Profile) error {
	return m.logoutErr
}

func (m *mockProvider) Status(_ context.Context, _ *profile.Profile) (*provider.ProfileStatus, error) {
	return m.statusResp, m.statusErr
}

func (m *mockProvider) Cleanup(_ context.Context, _ *profile.Profile) error {
	return nil
}

func (m *mockProvider) ValidateProfile(_ context.Context, _ *profile.Profile) error {
	return nil
}

func (m *mockProvider) DetectExistingAuth() (*provider.AuthDetection, error) {
	return &provider.AuthDetection{Provider: m.id, Found: false}, nil
}

func (m *mockProvider) ImportAuth(_ context.Context, _ string, _ *profile.Profile) ([]string, error) {
	return nil, nil
}

func (m *mockProvider) ValidateToken(_ context.Context, _ *profile.Profile, _ bool) (*provider.ValidationResult, error) {
	return &provider.ValidationResult{Provider: m.id, Valid: true, Method: "passive"}, nil
}

// =============================================================================
// NewRunner Tests
// =============================================================================

func TestNewRunner(t *testing.T) {
	t.Run("creates runner with registry", func(t *testing.T) {
		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		if runner == nil {
			t.Fatal("NewRunner returned nil")
		}
		if runner.registry != registry {
			t.Error("registry not set correctly")
		}
	})

	t.Run("creates runner with nil registry", func(t *testing.T) {
		runner := NewRunner(nil)

		if runner == nil {
			t.Fatal("NewRunner returned nil even with nil registry")
		}
		if runner.registry != nil {
			t.Error("registry should be nil")
		}
	})
}

// =============================================================================
// RunOptions Tests
// =============================================================================

func TestRunOptions(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	mock := &mockProvider{id: "test", defaultBin: "echo"}

	opts := RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"hello", "world"},
		WorkDir:  "/tmp",
		NoLock:   true,
		Env:      map[string]string{"KEY": "VALUE"},
	}

	if opts.Profile != prof {
		t.Error("Profile not set")
	}
	if opts.Provider == nil {
		t.Error("Provider not set")
	}
	if len(opts.Args) != 2 {
		t.Error("Args not set")
	}
	if opts.WorkDir != "/tmp" {
		t.Error("WorkDir not set")
	}
	if !opts.NoLock {
		t.Error("NoLock not set")
	}
	if opts.Env["KEY"] != "VALUE" {
		t.Error("Env not set")
	}
}

// =============================================================================
// LoginFlow Tests
// =============================================================================

func TestLoginFlow(t *testing.T) {
	t.Run("delegates to provider", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "test",
			BasePath: tmpDir,
		}

		mock := &mockProvider{
			id:       "test",
			loginErr: nil,
		}

		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		err := runner.LoginFlow(context.Background(), mock, prof)
		if err != nil {
			t.Errorf("LoginFlow() error = %v", err)
		}
	})

	t.Run("returns provider error", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "test",
			BasePath: tmpDir,
		}

		mock := &mockProvider{
			id:       "test",
			loginErr: context.DeadlineExceeded,
		}

		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		err := runner.LoginFlow(context.Background(), mock, prof)
		if err != context.DeadlineExceeded {
			t.Errorf("LoginFlow() error = %v, want %v", err, context.DeadlineExceeded)
		}
	})
}

// =============================================================================
// Status Tests
// =============================================================================

func TestStatus(t *testing.T) {
	t.Run("returns provider status", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "test",
			BasePath: tmpDir,
		}

		expectedStatus := &provider.ProfileStatus{
			LoggedIn:  true,
			AccountID: "test@example.com",
		}

		mock := &mockProvider{
			id:         "test",
			statusResp: expectedStatus,
			statusErr:  nil,
		}

		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		status, err := runner.Status(context.Background(), mock, prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status != expectedStatus {
			t.Errorf("Status() = %+v, want %+v", status, expectedStatus)
		}
	})

	t.Run("returns provider error", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "test",
			BasePath: tmpDir,
		}

		mock := &mockProvider{
			id:        "test",
			statusErr: context.Canceled,
		}

		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		_, err := runner.Status(context.Background(), mock, prof)
		if err != context.Canceled {
			t.Errorf("Status() error = %v, want %v", err, context.Canceled)
		}
	})
}

// =============================================================================
// RunInteractive Tests
// =============================================================================

func TestRunInteractive(t *testing.T) {
	// RunInteractive is just a wrapper around Run, so we test that it
	// correctly delegates. Since Run has side effects (executes commands,
	// potentially calls os.Exit), we only test the delegation pattern here.

	t.Run("is alias for Run", func(t *testing.T) {
		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		// Verify method exists and has correct signature
		var _ func(context.Context, RunOptions) error = runner.RunInteractive
	})
}

// =============================================================================
// Runner Struct Tests
// =============================================================================

func TestRunner(t *testing.T) {
	t.Run("stores registry reference", func(t *testing.T) {
		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		if runner.registry != registry {
			t.Error("Runner should store registry reference")
		}
	})
}

// =============================================================================
// Run Environment Setup Tests
// =============================================================================

// Note: Testing the actual Run method is challenging because:
// 1. It executes real commands
// 2. It calls os.Exit on command failure
// 3. It connects to stdin/stdout/stderr
//
// These behaviors make unit testing difficult. The Run method is better
// tested through E2E integration tests (caam-0ka, caam-05i, caam-ckk).
//
// Here we test the supporting components that can be safely unit tested.

func TestRunOptionsDefaults(t *testing.T) {
	// Test that zero-value RunOptions behaves correctly
	opts := RunOptions{}

	if opts.Profile != nil {
		t.Error("default Profile should be nil")
	}
	if opts.Provider != nil {
		t.Error("default Provider should be nil")
	}
	if opts.Args != nil {
		t.Error("default Args should be nil")
	}
	if opts.WorkDir != "" {
		t.Error("default WorkDir should be empty")
	}
	if opts.NoLock {
		t.Error("default NoLock should be false")
	}
	if opts.Env != nil {
		t.Error("default Env should be nil")
	}
}

func TestRunOptionsWithEnv(t *testing.T) {
	// Test environment variable handling
	opts := RunOptions{
		Env: map[string]string{
			"FOO":       "bar",
			"BAZ":       "qux",
			"EMPTY":     "",
			"WITH=SIGN": "value",
		},
	}

	if len(opts.Env) != 4 {
		t.Errorf("Env has %d entries, want 4", len(opts.Env))
	}
	if opts.Env["FOO"] != "bar" {
		t.Error("FOO env var not set correctly")
	}
	if opts.Env["EMPTY"] != "" {
		t.Error("EMPTY env var should be empty string")
	}
	if opts.Env["WITH=SIGN"] != "value" {
		t.Error("WITH=SIGN env var not set correctly")
	}
}

// =============================================================================
// Integration with Provider Interface
// =============================================================================

func TestProviderIntegration(t *testing.T) {
	// Verify that mockProvider satisfies the provider.Provider interface
	var _ provider.Provider = (*mockProvider)(nil)

	t.Run("mock provider methods", func(t *testing.T) {
		mock := &mockProvider{
			id:          "test-id",
			displayName: "Test Provider",
			defaultBin:  "/usr/bin/test",
			authModes:   []provider.AuthMode{provider.AuthModeOAuth},
			envVars:     map[string]string{"TEST": "value"},
		}

		if mock.ID() != "test-id" {
			t.Error("ID() failed")
		}
		if mock.DisplayName() != "Test Provider" {
			t.Error("DisplayName() failed")
		}
		if mock.DefaultBin() != "/usr/bin/test" {
			t.Error("DefaultBin() failed")
		}
		if len(mock.SupportedAuthModes()) != 1 {
			t.Error("SupportedAuthModes() failed")
		}

		env, err := mock.Env(context.Background(), nil)
		if err != nil {
			t.Errorf("Env() error = %v", err)
		}
		if env["TEST"] != "value" {
			t.Error("Env() failed")
		}
	})
}

// =============================================================================
// Run Method Tests
// =============================================================================

func TestRun_EnvError(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	mock := &mockProvider{
		id:         "test",
		defaultBin: "true",
		envErr:     context.DeadlineExceeded,
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		NoLock:   true,
	})

	if err == nil {
		t.Fatal("Run() should return error when Env() fails")
	}
	if !strings.Contains(err.Error(), "get provider env") {
		t.Errorf("Error should mention 'get provider env', got: %v", err)
	}
}

func TestRun_LockError(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	// Create a profile directory and simulate a stale lock
	// by holding a lock ourselves
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Lock the profile
	if err := prof.LockWithCleanup(); err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}
	defer prof.Unlock()

	// Create a second profile pointing to same location
	prof2 := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	mock := &mockProvider{
		id:         "test",
		defaultBin: "true",
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof2,
		Provider: mock,
		NoLock:   false, // Try to acquire lock
	})

	if err == nil {
		t.Fatal("Run() should return error when profile is already locked")
	}
	if !strings.Contains(err.Error(), "lock profile") {
		t.Errorf("Error should mention 'lock profile', got: %v", err)
	}
}

func TestRun_CommandNotFound(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	mock := &mockProvider{
		id:         "test",
		defaultBin: "/nonexistent/command/path",
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		NoLock:   true,
	})

	if err == nil {
		t.Fatal("Run() should return error when command not found")
	}
	if !strings.Contains(err.Error(), "run command") {
		t.Errorf("Error should mention 'run command', got: %v", err)
	}
}

func TestRun_WorkDirOption(t *testing.T) {
	tmpDir := t.TempDir()
	workDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Create a marker file in workDir
	markerFile := filepath.Join(workDir, "marker.txt")
	if err := os.WriteFile(markerFile, []byte("test"), 0644); err != nil {
		t.Fatalf("Failed to create marker file: %v", err)
	}

	mock := &mockProvider{
		id:         "test",
		defaultBin: "test", // test -f marker.txt
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	// This will exit 0 if marker.txt exists in workDir, exit 1 otherwise
	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"-f", "marker.txt"},
		WorkDir:  workDir,
		NoLock:   true,
	})

	// test -f marker.txt should succeed (exit 0) so no error
	if err != nil {
		t.Errorf("Run() with WorkDir error = %v", err)
	}
}

func TestRun_EnvironmentMerging(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Provider provides PROVIDER_VAR, opts provides CUSTOM_VAR
	mock := &mockProvider{
		id:         "test",
		defaultBin: "sh",
		envVars:    map[string]string{"PROVIDER_VAR": "provider_value"},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	// Run a command that checks for our env vars
	// sh -c 'test "$PROVIDER_VAR" = "provider_value" && test "$CUSTOM_VAR" = "custom_value"'
	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"-c", `test "$PROVIDER_VAR" = "provider_value" && test "$CUSTOM_VAR" = "custom_value"`},
		Env:      map[string]string{"CUSTOM_VAR": "custom_value"},
		NoLock:   true,
	})

	if err != nil {
		t.Errorf("Environment variables not properly merged: %v", err)
	}
}

func TestRun_EnvironmentOverride(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Provider provides VAR=provider, opts overrides with VAR=custom
	mock := &mockProvider{
		id:         "test",
		defaultBin: "sh",
		envVars:    map[string]string{"VAR": "provider"},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	// Custom env should override provider env
	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"-c", `test "$VAR" = "custom"`},
		Env:      map[string]string{"VAR": "custom"},
		NoLock:   true,
	})

	if err != nil {
		t.Errorf("Custom env should override provider env: %v", err)
	}
}

func TestRun_ProfileMetadataUpdated(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "test",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	beforeRun := time.Now()

	mock := &mockProvider{
		id:         "test",
		defaultBin: "true",
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		NoLock:   true,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// LastUsedAt should have been updated
	if prof.LastUsedAt.Before(beforeRun) {
		t.Error("LastUsedAt was not updated after Run()")
	}
}

func TestRun_CodexSessionCapture(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Simulate codex output with session ID
	mock := &mockProvider{
		id:         "codex", // Must be "codex" to trigger session capture
		defaultBin: "echo",
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"To continue this session, run codex resume 12345678-1234-1234-1234-123456789abc"},
		NoLock:   true,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Session ID should have been captured
	if prof.LastSessionID != "12345678-1234-1234-1234-123456789abc" {
		t.Errorf("LastSessionID = %q, want %q", prof.LastSessionID, "12345678-1234-1234-1234-123456789abc")
	}
}

// Note: Testing context cancellation is difficult because Run() calls os.Exit()
// when a command exits with a non-zero exit code (including when killed by context).
// This behavior is by design - see the comment in Run() about propagating exit codes.
// Context cancellation is better tested through E2E integration tests.

func TestRun_RateLimitCallback(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "claude",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	callbackCalled := make(chan struct{}, 1)

	mock := &mockProvider{
		id:         "claude", // Must match a known provider for rate limit detection
		defaultBin: "echo",
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"Error: rate limit exceeded"}, // Claude rate limit message
		NoLock:   true,
		OnRateLimit: func(ctx context.Context) error {
			select {
			case callbackCalled <- struct{}{}:
			default:
			}
			return nil
		},
		RateLimitDelay: 0, // Immediate callback
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	// Wait for callback (with timeout)
	select {
	case <-callbackCalled:
		// Success - callback was invoked
	case <-time.After(2 * time.Second):
		t.Error("Rate limit callback was not invoked")
	}
}

func TestRun_RateLimitCallbackWithDelay(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "test",
		Provider: "claude",
		BasePath: tmpDir,
	}

	// Ensure profile directory exists
	if err := os.MkdirAll(prof.BasePath, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	callbackTime := make(chan time.Time, 1)

	mock := &mockProvider{
		id:         "claude",
		defaultBin: "echo",
		envVars:    map[string]string{},
	}

	registry := provider.NewRegistry()
	runner := NewRunner(registry)

	startTime := time.Now()

	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"Error: rate limit exceeded"},
		NoLock:   true,
		OnRateLimit: func(ctx context.Context) error {
			select {
			case callbackTime <- time.Now():
			default:
			}
			return nil
		},
		RateLimitDelay: 200 * time.Millisecond,
	})

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	select {
	case t1 := <-callbackTime:
		elapsed := t1.Sub(startTime)
		if elapsed < 150*time.Millisecond {
			t.Errorf("Callback invoked too early: %v", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Error("Rate limit callback was not invoked")
	}
}

// TestRun_NeedsTTYDetection verifies issue #36: when the wrapped tool aborts
// because stdin is not a terminal, Run returns an *ExitCodeError carrying both
// the real exit code and NeedsTTY=true (detected from the tool's stderr).
func TestRun_NeedsTTYDetection(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal; TTY-error detection only runs when stdin is not a TTY")
	}
	tmpDir := t.TempDir()
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prof := &profile.Profile{Name: "test", Provider: "test", BasePath: tmpDir}
	mock := &mockProvider{id: "test", defaultBin: "sh", envVars: map[string]string{}}

	runner := NewRunner(provider.NewRegistry())
	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"-c", "echo 'Error: stdin is not a terminal' >&2; exit 7"},
		NoLock:   true,
	})

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *ExitCodeError, got %T: %v", err, err)
	}
	if exitErr.Code != 7 {
		t.Errorf("Code = %d, want 7 (real exit code must be preserved)", exitErr.Code)
	}
	if !exitErr.NeedsTTY {
		t.Errorf("NeedsTTY = false, want true (stderr contained 'stdin is not a terminal')")
	}
}

// TestRun_NoFalseTTYDetection verifies the precision improvement: a non-zero
// exit whose stderr is unrelated to terminals must NOT be flagged NeedsTTY, so
// caam never prints a misleading "needs a terminal" hint for, e.g., an auth
// error or a command-not-found.
func TestRun_NoFalseTTYDetection(t *testing.T) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		t.Skip("stdin is a terminal")
	}
	tmpDir := t.TempDir()
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prof := &profile.Profile{Name: "test", Provider: "test", BasePath: tmpDir}
	mock := &mockProvider{id: "test", defaultBin: "sh", envVars: map[string]string{}}

	runner := NewRunner(provider.NewRegistry())
	err := runner.Run(context.Background(), RunOptions{
		Profile:  prof,
		Provider: mock,
		Args:     []string{"-c", "echo 'auth error: status 401 unauthorized-ish' >&2; exit 13"},
		NoLock:   true,
	})

	var exitErr *ExitCodeError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected *ExitCodeError, got %T: %v", err, err)
	}
	if exitErr.Code != 13 {
		t.Errorf("Code = %d, want 13", exitErr.Code)
	}
	if exitErr.NeedsTTY {
		t.Errorf("NeedsTTY = true for a non-TTY failure; want false")
	}
}

// =============================================================================
// Profile refresh hook (issue #90)
// =============================================================================

type refreshingProvider struct {
	mockProvider
	refreshed  []*profile.Profile
	refreshErr error
}

func (r *refreshingProvider) RefreshProfile(_ context.Context, p *profile.Profile) error {
	r.refreshed = append(r.refreshed, p)
	return r.refreshErr
}

var _ provider.ProfileRefresher = (*refreshingProvider)(nil)

func TestRun_RefreshesProviderProfileBeforeLaunch(t *testing.T) {
	prof := &profile.Profile{Name: "test", Provider: "test", BasePath: t.TempDir()}
	mock := &refreshingProvider{mockProvider: mockProvider{
		id:         "test",
		defaultBin: "/nonexistent/command/path",
		envVars:    map[string]string{"HOME": prof.HomePath()},
	}}

	runner := NewRunner(provider.NewRegistry())
	err := runner.Run(context.Background(), RunOptions{Profile: prof, Provider: mock, NoLock: true})
	if err == nil || !strings.Contains(err.Error(), "run command") {
		t.Fatalf("expected the launch itself to fail, got %v", err)
	}
	if len(mock.refreshed) != 1 || mock.refreshed[0] != prof {
		t.Fatalf("RefreshProfile calls = %v, want exactly one with the run's profile", mock.refreshed)
	}
}

func TestRun_RefreshFailureDoesNotBlockLaunch(t *testing.T) {
	prof := &profile.Profile{Name: "test", Provider: "test", BasePath: t.TempDir()}
	mock := &refreshingProvider{
		mockProvider: mockProvider{id: "test", defaultBin: "true", envVars: map[string]string{}},
		refreshErr:   errors.New("boom"),
	}

	runner := NewRunner(provider.NewRegistry())
	if err := runner.Run(context.Background(), RunOptions{Profile: prof, Provider: mock, NoLock: true}); err != nil {
		t.Fatalf("Run() = %v, want nil: a refresh failure is a warning only", err)
	}
	if len(mock.refreshed) != 1 {
		t.Fatalf("RefreshProfile calls = %d, want 1", len(mock.refreshed))
	}
}

func TestRun_GlobalEnvSkipsProfileRefresh(t *testing.T) {
	prof := &profile.Profile{Name: "test", Provider: "test", BasePath: t.TempDir()}
	mock := &refreshingProvider{mockProvider: mockProvider{id: "test", defaultBin: "true"}}

	runner := NewRunner(provider.NewRegistry())
	if err := runner.Run(context.Background(), RunOptions{Profile: prof, Provider: mock, NoLock: true, UseGlobalEnv: true}); err != nil {
		t.Fatalf("Run() = %v", err)
	}
	if len(mock.refreshed) != 0 {
		t.Fatalf("RefreshProfile must not run for a global-env launch, got %d calls", len(mock.refreshed))
	}
}
