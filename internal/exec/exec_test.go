package exec

import (
	"context"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
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
