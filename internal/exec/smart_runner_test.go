package exec

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
)

// =============================================================================
// HandoffState Tests
// =============================================================================

func TestHandoffState_String(t *testing.T) {
	tests := []struct {
		state    HandoffState
		expected string
	}{
		{Running, "RUNNING"},
		{RateLimited, "RATE_LIMITED"},
		{SelectingBackup, "SELECTING_BACKUP"},
		{SwappingAuth, "SWAPPING_AUTH"},
		{LoggingIn, "LOGGING_IN"},
		{LoginComplete, "LOGIN_COMPLETE"},
		{HandoffFailed, "HANDOFF_FAILED"},
		{ManualMode, "MANUAL_MODE"},
		{HandoffState(999), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("HandoffState.String() = %v, want %v", got, tt.expected)
			}
		})
	}
}

// =============================================================================
// SmartRunner Tests
// =============================================================================

func TestNewSmartRunner(t *testing.T) {
	t.Run("creates runner with defaults", func(t *testing.T) {
		registry := provider.NewRegistry()
		runner := NewRunner(registry)

		sr := NewSmartRunner(runner, SmartRunnerOptions{})

		if sr == nil {
			t.Fatal("NewSmartRunner returned nil")
		}
		if sr.Runner != runner {
			t.Error("Runner not set correctly")
		}
		if sr.state != Running {
			t.Errorf("initial state = %v, want %v", sr.state, Running)
		}
		if sr.notifier == nil {
			t.Error("notifier should have default value")
		}
	})

	t.Run("creates runner with custom options", func(t *testing.T) {
		registry := provider.NewRegistry()
		runner := NewRunner(registry)
		vault := authfile.NewVault(t.TempDir())
		pool := authpool.NewAuthPool()
		notifier := &notify.TerminalNotifier{}
		handoffCfg := &config.HandoffConfig{
			AutoTrigger:      true,
			MaxRetries:       3,
			FallbackToManual: true,
		}

		sr := NewSmartRunner(runner, SmartRunnerOptions{
			Vault:            vault,
			AuthPool:         pool,
			Notifier:         notifier,
			HandoffConfig:    handoffCfg,
			CooldownDuration: 30 * time.Minute,
		})

		if sr.vault != vault {
			t.Error("vault not set correctly")
		}
		if sr.authPool != pool {
			t.Error("authPool not set correctly")
		}
		if sr.notifier != notifier {
			t.Error("notifier not set correctly")
		}
		if sr.handoffConfig != handoffCfg {
			t.Error("handoffConfig not set correctly")
		}
		if sr.cooldownDuration != 30*time.Minute {
			t.Errorf("cooldownDuration = %v, want 30m", sr.cooldownDuration)
		}
	})
}

func TestSmartRunner_setState(t *testing.T) {
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	sr := NewSmartRunner(runner, SmartRunnerOptions{})

	states := []HandoffState{
		Running,
		RateLimited,
		SelectingBackup,
		SwappingAuth,
		LoggingIn,
		LoginComplete,
		HandoffFailed,
		ManualMode,
	}

	for _, state := range states {
		t.Run(state.String(), func(t *testing.T) {
			sr.setState(state)

			sr.mu.Lock()
			got := sr.state
			sr.mu.Unlock()

			if got != state {
				t.Errorf("setState() = %v, want %v", got, state)
			}
		})
	}
}

func TestSmartRunner_InitialState(t *testing.T) {
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	sr := NewSmartRunner(runner, SmartRunnerOptions{})

	if sr.handoffCount != 0 {
		t.Errorf("initial handoffCount = %d, want 0", sr.handoffCount)
	}
	if sr.currentProfile != "" {
		t.Errorf("initial currentProfile = %q, want empty", sr.currentProfile)
	}
	if sr.previousProfile != "" {
		t.Errorf("initial previousProfile = %q, want empty", sr.previousProfile)
	}
}

func TestSmartRunner_DrainLoginDone(t *testing.T) {
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	sr := NewSmartRunner(runner, SmartRunnerOptions{})

	sr.loginDone <- loginResult{success: true}
	sr.drainLoginDone()

	select {
	case <-sr.loginDone:
		t.Fatal("expected loginDone to be empty after drain")
	default:
	}

	// Ensure drain is safe on empty channel
	sr.drainLoginDone()
}

// =============================================================================
// Mock Notifier for Testing
// =============================================================================

type mockNotifier struct {
	alerts []*notify.Alert
}

func (m *mockNotifier) Notify(alert *notify.Alert) error {
	m.alerts = append(m.alerts, alert)
	return nil
}

func (m *mockNotifier) Name() string {
	return "mock"
}

func (m *mockNotifier) Available() bool {
	return true
}

func TestSmartRunner_NotifierIntegration(t *testing.T) {
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	notifier := &mockNotifier{}

	sr := NewSmartRunner(runner, SmartRunnerOptions{
		Notifier: notifier,
	})

	// Test notifyHandoff
	sr.notifyHandoff("profile1", "profile2")

	if len(notifier.alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(notifier.alerts))
	}
	if notifier.alerts[0].Level != notify.Info {
		t.Errorf("expected Info level, got %v", notifier.alerts[0].Level)
	}
	if notifier.alerts[0].Title != "Switching profiles" {
		t.Errorf("unexpected title: %s", notifier.alerts[0].Title)
	}
}

func TestSmartRunner_FailWithManual(t *testing.T) {
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	notifier := &mockNotifier{}

	sr := NewSmartRunner(runner, SmartRunnerOptions{
		Notifier: notifier,
	})
	sr.currentProfile = "test-profile"

	sr.failWithManual("test error: %s", "details")

	if sr.state != HandoffFailed {
		t.Errorf("state = %v, want %v", sr.state, HandoffFailed)
	}
	if len(notifier.alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(notifier.alerts))
	}
	if notifier.alerts[0].Level != notify.Warning {
		t.Errorf("expected Warning level, got %v", notifier.alerts[0].Level)
	}
}

func TestSmartRunner_WithRotation(t *testing.T) {
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	selector := rotation.NewSelector(rotation.AlgorithmSmart, nil, nil)

	sr := NewSmartRunner(runner, SmartRunnerOptions{
		Rotation: selector,
	})

	if sr.rotation != selector {
		t.Error("rotation selector not set correctly")
	}
}

// =============================================================================
// UseGlobalEnv Regression (issue #64)
// =============================================================================

// envTrackingProvider wraps mockProvider (exec_test.go) to count Env calls, so
// tests can prove whether SmartRunner asked for the provider's isolated env.
type envTrackingProvider struct {
	mockProvider
	envCalls int
}

func (p *envTrackingProvider) Env(_ context.Context, _ *profile.Profile) (map[string]string, error) {
	p.envCalls++
	return p.mockProvider.envVars, p.mockProvider.envErr
}

// TestSmartRunner_Run_UseGlobalEnv is the regression test for issue #64:
// vault-based `caam run` sets UseGlobalEnv=true, but SmartRunner.Run used to
// call Provider.Env unconditionally, replacing HOME/CODEX_HOME with the
// isolated profile paths and pointing codex at a profile dir that is not
// logged in. SmartRunner must honor UseGlobalEnv exactly like Runner.Run.
func TestSmartRunner_Run_UseGlobalEnv(t *testing.T) {
	// Mock the spawned command with a trivially-succeeding shell so the PTY
	// path runs for real while letting us inspect the env caam handed it.
	var captured *exec.Cmd
	origExec := ExecCommand
	ExecCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "sh", "-c", "true")
		captured = cmd
		return cmd
	}
	t.Cleanup(func() { ExecCommand = origExec })

	isolatedHome := "/isolated/codex_home"
	newProv := func() *envTrackingProvider {
		return &envTrackingProvider{mockProvider: mockProvider{
			id:         "codex", // codex has a login handler, so we stay on the SmartRunner path
			defaultBin: "codex",
			envVars: map[string]string{
				"HOME":       "/isolated/home",
				"CODEX_HOME": isolatedHome,
			},
		}}
	}

	run := func(t *testing.T, prov *envTrackingProvider, useGlobal bool) []string {
		t.Helper()
		captured = nil
		store := profile.NewStore(t.TempDir())
		prof, err := store.Create("codex", "vault-active", "oauth")
		if err != nil {
			t.Fatalf("create profile: %v", err)
		}
		sr := NewSmartRunner(NewRunner(provider.NewRegistry()), SmartRunnerOptions{})
		if err := sr.Run(context.Background(), RunOptions{
			Profile:      prof,
			Provider:     prov,
			NoLock:       true,
			UseGlobalEnv: useGlobal,
		}); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if captured == nil {
			t.Fatal("ExecCommand was never invoked (fell off the SmartRunner path?)")
		}
		return captured.Env
	}

	hasEnv := func(env []string, want string) bool {
		for _, e := range env {
			if e == want {
				return true
			}
		}
		return false
	}

	t.Run("vault run keeps the global environment", func(t *testing.T) {
		prov := newProv()
		env := run(t, prov, true)
		if prov.envCalls != 0 {
			t.Fatalf("Provider.Env called %d times; UseGlobalEnv must skip it entirely", prov.envCalls)
		}
		if hasEnv(env, "CODEX_HOME="+isolatedHome) {
			t.Fatalf("isolated CODEX_HOME leaked into a UseGlobalEnv run: %v", env)
		}
		if hasEnv(env, "HOME=/isolated/home") {
			t.Fatalf("isolated HOME leaked into a UseGlobalEnv run: %v", env)
		}
	})

	t.Run("isolated run still applies provider env", func(t *testing.T) {
		prov := newProv()
		env := run(t, prov, false)
		if prov.envCalls != 1 {
			t.Fatalf("Provider.Env called %d times, want 1", prov.envCalls)
		}
		if !hasEnv(env, "CODEX_HOME="+isolatedHome) {
			t.Fatalf("provider env missing from isolated run: %v", env)
		}
	})
}
