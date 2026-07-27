package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
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
// SmartRunner PTY-path env scrub tests
// =============================================================================

// captureSmartRunnerCmdEnv runs SmartRunner.Run for the codex provider (which
// has a login handler and therefore takes the PTY path) with ExecCommand
// mocked to capture the *exec.Cmd handed to the PTY controller. The env
// assertions are made on the captured command, so the test proves what the
// PTY child would receive even on machines where the PTY itself cannot start
// (Run errors from pty setup are tolerated).
func captureSmartRunnerCmdEnv(t *testing.T, extraEnv map[string]string) map[string]string {
	t.Helper()

	tmpDir := t.TempDir()
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	prof := &profile.Profile{
		Name:     "test",
		Provider: "codex",
		BasePath: tmpDir,
	}

	mock := &mockProvider{
		id:         "codex",
		defaultBin: "true",
		envVars:    map[string]string{"CODEX_HOME": filepath.Join(tmpDir, "codex-home")},
	}

	var captured *osexec.Cmd
	origExec := ExecCommand
	ExecCommand = func(ctx context.Context, name string, args ...string) *osexec.Cmd {
		captured = osexec.CommandContext(ctx, name, args...)
		return captured
	}
	defer func() { ExecCommand = origExec }()

	runner := NewRunner(provider.NewRegistry())
	sr := NewSmartRunner(runner, SmartRunnerOptions{})

	// PTY startup can fail in constrained environments; the env is assembled
	// before the controller is created, so only the capture matters here.
	_ = sr.Run(context.Background(), RunOptions{
		Profile:      prof,
		Provider:     mock,
		NoLock:       true,
		Env:          extraEnv,
		UseGlobalEnv: true, // matches `caam run` vault-based switching
	})

	if captured == nil {
		t.Fatal("ExecCommand was never invoked; SmartRunner did not take the PTY path")
	}

	env := make(map[string]string, len(captured.Env))
	for _, e := range captured.Env {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				env[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return env
}

func TestSmartRunner_PTYPathScrubsAmbientAuthEnv(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-key")

	env := captureSmartRunnerCmdEnv(t, nil)

	if v, ok := env["OPENAI_API_KEY"]; ok {
		t.Errorf("ambient OPENAI_API_KEY leaked into the PTY child env (value %q)", v)
	}
	if _, ok := env["CODEX_HOME"]; !ok {
		t.Error("provider env (CODEX_HOME) missing from PTY child env")
	}
}

func TestSmartRunner_PTYPathInjectedEnvWinsOverScrub(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "ambient-key")

	env := captureSmartRunnerCmdEnv(t, map[string]string{"OPENAI_API_KEY": "profile-key"})

	if env["OPENAI_API_KEY"] != "profile-key" {
		t.Errorf("OPENAI_API_KEY = %q, want injected opts.Env value %q to win over the scrub",
			env["OPENAI_API_KEY"], "profile-key")
	}
}
