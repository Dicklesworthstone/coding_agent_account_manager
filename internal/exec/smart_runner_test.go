package exec

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/handoff"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
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
		if sr.retryConfig.MaxRetries != config.DefaultWrapConfig().MaxRetries {
			t.Error("nil retry config should use default retry budget")
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
	t.Run("explicit zero retry policy is preserved", func(t *testing.T) {
		cfg := smartRetryPolicy(0, 0)
		cfg.CooldownDuration = 0
		sr := NewSmartRunner(&Runner{}, SmartRunnerOptions{RetryConfig: cfg})
		if sr.retryConfig.MaxRetries != 0 || sr.cooldownDuration != 0 {
			t.Fatal("explicit zero retry or cooldown setting replaced with defaults")
		}
		cfg.MaxRetries = 9
		if sr.retryConfig.MaxRetries != 0 {
			t.Fatal("runner retained mutable caller retry policy")
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

func TestSmartRunnerRejectsExpiredHandoffBeforeChangingAuth(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	vault := authfile.NewVault(filepath.Join(root, "custom-vault"))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(-time.Hour).Unix())))
	expired := []byte(fmt.Sprintf(`{"tokens":{"access_token":%q}}`, "e30."+payload+".synthetic"))
	writeSmartSwitchFile(t, vault.BackupPath("codex", "expired", "auth.json"), expired)
	livePath := filepath.Join(root, "codex", "auth.json")
	live := smartSwitchCredentials("live", time.Now().Add(time.Hour))
	writeSmartSwitchFile(t, livePath, live)
	sr := NewSmartRunner(&Runner{}, SmartRunnerOptions{
		Vault:       vault,
		Rotation:    rotation.NewSelector(rotation.AlgorithmRoundRobin, nil, nil),
		Notifier:    &mockNotifier{},
		RetryConfig: smartRetryPolicy(3, 0),
	})
	sr.currentProfile = "live"
	called := false
	sr.loginHandler = &smartSwitchLoginHandler{
		LoginHandler: handoff.GetHandler("codex"),
		trigger: func() error {
			called = true
			return nil
		},
	}
	sr.handleRateLimit(context.Background())
	if called || sr.getState() != HandoffFailed || sr.currentProfile != "live" {
		t.Fatalf("expired credential advanced handoff: called=%v state=%s current=%s", called, sr.getState(), sr.currentProfile)
	}
	assertSmartSwitchFile(t, livePath, live)
	profiles, err := vault.List("codex")
	if err != nil || len(profiles) != 1 || profiles[0] != "expired" {
		t.Fatalf("failed selection changed vault: profiles=%v err=%v", profiles, err)
	}
}

func TestSmartRunnerRateLimitExcludesCurrentBeforeEverySelection(t *testing.T) {
	for _, algorithm := range []rotation.Algorithm{rotation.AlgorithmSmart, rotation.AlgorithmRandom, rotation.AlgorithmRoundRobin} {
		for _, policy := range []rotation.Policy{rotation.PolicyAvailability, rotation.PolicyDrain} {
			t.Run(string(algorithm)+"/"+string(policy), func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
				t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
				vault := authfile.NewVault(filepath.Join(root, "vault"))
				current := smartSwitchCredentials("current", time.Now().Add(time.Hour))
				backup := smartSwitchCredentials("backup", time.Now().Add(time.Hour))
				writeSmartSwitchFile(t, vault.BackupPath("codex", "current", "auth.json"), current)
				writeSmartSwitchFile(t, vault.BackupPath("codex", "backup", "auth.json"), backup)
				livePath := filepath.Join(root, "codex", "auth.json")
				writeSmartSwitchFile(t, livePath, current)
				selector := rotation.NewSelector(algorithm, nil, nil)
				selector.SetPolicy(policy)
				selector.SetProfileHealth(map[string]*health.ProfileHealth{
					"current": {TokenRenewable: true, PlanType: "enterprise"},
					"backup":  {TokenRenewable: true},
				})
				// No DB: recording a cooldown cannot be the only thing that
				// prevents selecting the higher-scoring current account.
				sr := NewSmartRunner(&Runner{}, SmartRunnerOptions{Vault: vault, Rotation: selector, Notifier: &mockNotifier{}, RetryConfig: smartRetryPolicy(3, 0)})
				sr.currentProfile = "current"
				var err error
				sr.detector, err = ratelimit.NewDetector(ratelimit.ProviderCodex, nil)
				if err != nil {
					t.Fatal(err)
				}
				calls := 0
				sr.loginHandler = &smartSwitchLoginHandler{LoginHandler: handoff.GetHandler("codex"), trigger: func() error {
					calls++
					assertSmartSwitchFile(t, livePath, backup)
					sr.loginDone <- loginResult{success: true}
					return nil
				}}
				sr.handleRateLimit(context.Background())
				if calls != 1 || sr.currentProfile != "backup" || sr.getState() != Running {
					t.Fatalf("usable backup not selected: calls=%d current=%s state=%s", calls, sr.currentProfile, sr.getState())
				}
			})
		}
	}
}

func TestSmartRunnerRecordsLimitWhenNoBackupExists(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	db, err := caamdb.OpenAt(filepath.Join(root, "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	current := smartSwitchCredentials("current", time.Now().Add(time.Hour))
	writeSmartSwitchFile(t, vault.BackupPath("codex", "current", "auth.json"), current)
	livePath := filepath.Join(root, "codex", "auth.json")
	writeSmartSwitchFile(t, livePath, current)
	sr := NewSmartRunner(&Runner{}, SmartRunnerOptions{Vault: vault, DB: db, Rotation: rotation.NewSelector(rotation.AlgorithmSmart, nil, db), Notifier: &mockNotifier{}})
	sr.currentProfile = "current"
	sr.loginHandler = handoff.GetHandler("codex")
	sr.handleRateLimit(context.Background())
	if sr.getState() != HandoffFailed {
		t.Fatalf("state = %s, want failed without backup", sr.getState())
	}
	cooldown, err := db.ActiveCooldown("codex", "current", time.Now())
	if err != nil || cooldown == nil {
		t.Fatalf("detected rate limit was not persisted: cooldown=%+v err=%v", cooldown, err)
	}
	assertSmartSwitchFile(t, livePath, current)
}

func TestSmartRunnerHandoffPreservesActualCredentialOwner(t *testing.T) {
	for _, tc := range []struct {
		name            string
		liveOwner       string
		malformedTarget bool
		loginFails      bool
		corruptRollback bool
		backupMode      string
	}{
		{name: "rotated named login", liveOwner: "alice"},
		{name: "external login does not overwrite startup profile", liveOwner: "carol"},
		{name: "malformed target does not trigger backup or rollback", liveOwner: "alice", malformedTarget: true},
		{name: "rollback preserves tokens rotated during failed login", liveOwner: "alice", loginFails: true},
		{name: "rollback restores actual external login", liveOwner: "carol", loginFails: true},
		{name: "failed rollback remains failed", liveOwner: "alice", loginFails: true, corruptRollback: true},
		{name: "disabled unnamed backup cannot claim rollback", liveOwner: "carol", loginFails: true, backupMode: "never"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
			codexHome := filepath.Join(root, "codex")
			t.Setenv("CODEX_HOME", codexHome)
			if tc.backupMode != "" {
				cfg := config.DefaultSPMConfig()
				cfg.Safety.AutoBackupBeforeSwitch = tc.backupMode
				if err := cfg.Save(); err != nil {
					t.Fatal(err)
				}
			}
			vault := authfile.NewVault(filepath.Join(root, "vault"))
			base := time.Date(2033, 1, 1, 0, 0, 0, 0, time.UTC)
			alice := smartSwitchCredentials("alice", base)
			live := smartSwitchCredentials(tc.liveOwner, base.Add(time.Hour))
			bob := smartSwitchCredentials("bob", base.Add(2*time.Hour))
			rotatedBob := smartSwitchCredentials("bob", base.Add(3*time.Hour))
			if tc.malformedTarget {
				bob = []byte(`{"tokens":`)
			}
			alicePath := vault.BackupPath("codex", "alice", "auth.json")
			bobPath := vault.BackupPath("codex", "bob", "auth.json")
			livePath := filepath.Join(codexHome, "auth.json")
			writeSmartSwitchFile(t, alicePath, alice)
			writeSmartSwitchFile(t, bobPath, bob)
			writeSmartSwitchFile(t, livePath, live)

			notifier := &mockNotifier{}
			sr := NewSmartRunner(&Runner{}, SmartRunnerOptions{
				Vault:       vault,
				Rotation:    rotation.NewSelector(rotation.AlgorithmRoundRobin, nil, nil),
				Notifier:    notifier,
				RetryConfig: smartRetryPolicy(3, 0),
			})
			sr.currentProfile = "alice"
			var err error
			sr.detector, err = ratelimit.NewDetector(ratelimit.ProviderCodex, nil)
			if err != nil {
				t.Fatal(err)
			}
			loginCalls := 0
			sr.loginHandler = &smartSwitchLoginHandler{
				LoginHandler: handoff.GetHandler("codex"),
				trigger: func() error {
					loginCalls++
					assertSmartSwitchFile(t, livePath, bob)
					if tc.loginFails {
						writeSmartSwitchFile(t, livePath, rotatedBob)
						if tc.corruptRollback {
							writeSmartSwitchFile(t, alicePath, []byte(`{"tokens":`))
						}
						return fmt.Errorf("synthetic login failure after token rotation")
					}
					sr.loginDone <- loginResult{success: true}
					return nil
				},
			}
			sr.handleRateLimit(context.Background())

			if tc.malformedTarget {
				if loginCalls != 0 || sr.getState() != HandoffFailed || sr.currentProfile != "alice" {
					t.Fatalf("malformed target advanced handoff: calls=%d state=%s current=%s", loginCalls, sr.getState(), sr.currentProfile)
				}
				assertSmartSwitchFile(t, livePath, live)
				assertSmartSwitchFile(t, alicePath, alice)
				profiles, err := vault.List("codex")
				if err != nil || len(profiles) != 2 {
					t.Fatalf("malformed target changed vault: profiles=%v err=%v", profiles, err)
				}
				return
			}
			if loginCalls != 1 {
				t.Fatalf("login calls = %d, want 1", loginCalls)
			}
			wantAlice := alice
			if tc.liveOwner == "alice" {
				wantAlice = live
			}
			if !tc.corruptRollback {
				assertSmartSwitchFile(t, alicePath, wantAlice)
			}
			if !tc.loginFails {
				assertSmartSwitchFile(t, livePath, bob)
				if sr.currentProfile != "bob" || sr.handoffCount != 1 || sr.getState() != Running {
					t.Fatalf("successful handoff state: current=%s count=%d state=%s", sr.currentProfile, sr.handoffCount, sr.getState())
				}
			} else if tc.corruptRollback || tc.backupMode == "never" {
				assertSmartSwitchFile(t, livePath, rotatedBob)
				if sr.currentProfile != "bob" || sr.getState() != HandoffFailed {
					t.Fatalf("failed rollback reported recovery: current=%s state=%s", sr.currentProfile, sr.getState())
				}
			} else {
				assertSmartSwitchFile(t, livePath, live)
				assertSmartSwitchFile(t, bobPath, rotatedBob)
				if sr.getState() != Running || sr.handoffCount != 0 {
					t.Fatalf("rollback state: state=%s count=%d", sr.getState(), sr.handoffCount)
				}
				if tc.liveOwner == "alice" && sr.currentProfile != "alice" {
					t.Fatalf("rollback target = %s, want alice", sr.currentProfile)
				}
				if tc.liveOwner == "carol" && !authfile.IsSystemProfile(sr.currentProfile) {
					t.Fatalf("external login rollback used startup profile: %s", sr.currentProfile)
				}
			}
			if tc.liveOwner == "carol" && tc.backupMode != "never" {
				if !authfile.IsSystemProfile(sr.previousProfile) {
					t.Fatalf("external login was not saved for recovery: %q", sr.previousProfile)
				}
				assertSmartSwitchFile(t, vault.BackupPath("codex", sr.previousProfile, "auth.json"), live)
			}
		})
	}
}

type smartSwitchLoginHandler struct {
	handoff.LoginHandler
	trigger func() error
}

func (h *smartSwitchLoginHandler) TriggerLogin(pty.Controller) error { return h.trigger() }

func smartSwitchCredentials(account string, refreshed time.Time) []byte {
	claims := fmt.Sprintf(`{"sub":%q,"email":%q,"iat":%d,"https://api.openai.com/auth":{"chatgpt_account_id":%q}}`,
		"user-"+account, account+"@example.com", refreshed.Unix(), account)
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".synthetic"
	return []byte(fmt.Sprintf(`{"tokens":{"id_token":%q,"access_token":%q,"refresh_token":%q,"account_id":%q},"last_refresh":%q}`,
		token, token, "refresh-"+account+refreshed.Format("150405"), account, refreshed.Format(time.RFC3339)))
}

func writeSmartSwitchFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func assertSmartSwitchFile(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("unexpected credential content at %s", path)
	}
}

func smartRetryPolicy(retries int, delay time.Duration) *config.WrapConfig {
	cfg := config.DefaultWrapConfig()
	cfg.MaxRetries = retries
	cfg.InitialDelay = config.Duration(delay)
	cfg.MaxDelay = config.Duration(delay)
	cfg.Jitter = false
	return &cfg
}

// Use real vault switching and login completion with synthetic credentials,
// without a database to hide missing session-local account exclusion.
func newSmartRetryRunner(t *testing.T, names []string, cfg *config.WrapConfig) (*SmartRunner, string, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("CODEX_HOME", filepath.Join(root, "codex"))
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	credentials := make(map[string][]byte, len(names))
	for _, name := range names {
		credentials[name] = smartSwitchCredentials(name, time.Now().UTC())
		writeSmartSwitchFile(t, vault.BackupPath("codex", name, "auth.json"), credentials[name])
	}
	livePath := filepath.Join(root, "codex", "auth.json")
	writeSmartSwitchFile(t, livePath, credentials[names[0]])
	sr := NewSmartRunner(&Runner{}, SmartRunnerOptions{
		Vault:       vault,
		Rotation:    rotation.NewSelector(rotation.AlgorithmRoundRobin, nil, nil),
		Notifier:    &mockNotifier{},
		RetryConfig: cfg,
	})
	sr.currentProfile = names[0]
	var err error
	sr.detector, err = ratelimit.NewDetector(ratelimit.ProviderCodex, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sr, livePath, credentials
}

func TestSmartRunner_RetryBudgetAndNoAccountReuse(t *testing.T) {
	for _, tc := range []struct {
		name         string
		retries      int
		profiles     []string
		failFirst    bool
		wantAttempts []string
		wantFinal    string
		wantSuccess  int
	}{
		{"disabled", 0, []string{"alice", "bob"}, false, nil, "alice", 0},
		{"budget stops with unused backup", 1, []string{"alice", "bob", "carol"}, false, []string{"bob"}, "bob", 1},
		{"limited accounts never cycle", 5, []string{"alice", "bob"}, false, []string{"bob"}, "bob", 1},
		{"all available backups", 5, []string{"alice", "bob", "carol"}, false, []string{"bob", "carol"}, "carol", 2},
		{"failed login consumes budget", 1, []string{"alice", "bob", "carol"}, true, []string{"bob"}, "alice", 0},
		{"failed target is skipped after rollback", 3, []string{"alice", "bob", "carol"}, true, []string{"bob", "carol"}, "carol", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := smartRetryPolicy(tc.retries, 0)
			cfg.CooldownDuration = 0
			sr, livePath, credentials := newSmartRetryRunner(t, tc.profiles, cfg)
			var attempts []string
			sr.loginHandler = &smartSwitchLoginHandler{
				LoginHandler: handoff.GetHandler("codex"),
				trigger: func() error {
					attempts = append(attempts, sr.currentProfile)
					if tc.failFirst && len(attempts) == 1 {
						return fmt.Errorf("synthetic login failure")
					}
					sr.loginDone <- loginResult{success: true}
					return nil
				},
			}
			for i := 0; i < 7 && sr.getState() == Running; i++ {
				sr.handleRateLimit(context.Background())
			}
			if !reflect.DeepEqual(attempts, tc.wantAttempts) {
				t.Fatalf("login attempts = %v, want %v", attempts, tc.wantAttempts)
			}
			if sr.handoffAttempts != len(tc.wantAttempts) || sr.handoffCount != tc.wantSuccess {
				t.Fatalf("handoff attempts/successes = %d/%d, want %d/%d", sr.handoffAttempts, sr.handoffCount, len(tc.wantAttempts), tc.wantSuccess)
			}
			if sr.getState() != HandoffFailed || sr.currentProfile != tc.wantFinal || !sr.rateLimitHit {
				t.Fatalf("final state=%s profile=%s hit=%v", sr.getState(), sr.currentProfile, sr.rateLimitHit)
			}
			assertSmartSwitchFile(t, livePath, credentials[tc.wantFinal])
			// Repeated observations after stopping must not redispatch or notify.
			notifier := sr.notifier.(*mockNotifier)
			alerts := len(notifier.alerts)
			sr.handleRateLimit(context.Background())
			if len(notifier.alerts) != alerts || sr.handoffAttempts != len(tc.wantAttempts) {
				t.Fatal("exhausted runner retried or notified again")
			}
		})
	}
}

func TestSmartRunner_RecordsRateLimitWithoutHandoff(t *testing.T) {
	for _, tc := range []struct {
		name     string
		retries  int
		cooldown time.Duration
	}{
		{"no backup", 3, 15 * time.Minute},
		{"retries disabled", 0, 15 * time.Minute},
		{"explicit zero cooldown", 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := smartRetryPolicy(tc.retries, 0)
			cfg.CooldownDuration = config.Duration(tc.cooldown)
			sr, livePath, credentials := newSmartRetryRunner(t, []string{"alice"}, cfg)
			db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "caam.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sr.db = db
			sr.authPool = authpool.NewAuthPool()
			sr.authPool.AddProfile("codex", "alice")
			if err := sr.authPool.SetStatus("codex", "alice", authpool.PoolStatusReady); err != nil {
				t.Fatal(err)
			}
			called := false
			sr.loginHandler = &smartSwitchLoginHandler{
				LoginHandler: handoff.GetHandler("codex"),
				trigger:      func() error { called = true; return nil },
			}
			sr.handleRateLimit(context.Background())
			if called || !sr.rateLimitHit || !sr.triedProfiles["alice"] || sr.handoffAttempts != 0 {
				t.Fatal("rate limit was not recorded without a handoff")
			}
			assertSmartSwitchFile(t, livePath, credentials["alice"])
			cooldown, err := db.ActiveCooldown("codex", "alice", time.Now())
			if err != nil {
				t.Fatal(err)
			}
			wantCooldown := tc.cooldown > 0
			if (cooldown != nil) != wantCooldown || sr.authPool.GetProfile("codex", "alice").IsInCooldown() != wantCooldown {
				t.Fatalf("persistent cooldown did not honor %v", tc.cooldown)
			}
		})
	}
}

func TestSmartRunner_CancelBackoffBeforeChangingAuth(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backoff    time.Duration
		retryAfter string
	}{
		{"configured delay", time.Minute, ""},
		{"server delay with zero backoff", 0, "Retry-After: 60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr, livePath, credentials := newSmartRetryRunner(t, []string{"alice", "bob"}, smartRetryPolicy(3, tc.backoff))
			called := false
			sr.loginHandler = &smartSwitchLoginHandler{
				LoginHandler: handoff.GetHandler("codex"),
				trigger: func() error {
					called = true
					sr.loginDone <- loginResult{success: true}
					return nil
				},
			}
			if tc.retryAfter != "" {
				sr.detector.Check(tc.retryAfter)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			sr.handleRateLimit(ctx)
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("handoff did not wait for configured/server backoff")
			}
			if called || sr.handoffAttempts != 0 || sr.currentProfile != "alice" || sr.getState() != HandoffFailed {
				t.Fatal("cancellation advanced handoff")
			}
			assertSmartSwitchFile(t, livePath, credentials["alice"])
			for name, want := range credentials {
				assertSmartSwitchFile(t, sr.vault.BackupPath("codex", name, "auth.json"), want)
			}
		})
	}
}

func TestSmartRunner_RechecksEligibilityAfterBackoff(t *testing.T) {
	for _, tc := range []struct {
		name       string
		backoff    time.Duration
		retryAfter string
	}{
		{"configured delay", time.Minute, ""},
		{"server delay with zero backoff", 0, "Retry-After: 60"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				sr, livePath, credentials := newSmartRetryRunner(t, []string{"alice", "bob"}, smartRetryPolicy(3, tc.backoff))
				// Bob is usable at selection, but its nonrenewable token expires
				// halfway through the wait. Fake time makes the boundary exact.
				payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"sub":"user-bob","exp":%d}`, time.Now().Add(30*time.Second).Unix())))
				credentials["bob"] = []byte(fmt.Sprintf(`{"tokens":{"access_token":%q}}`, "e30."+payload+".synthetic"))
				writeSmartSwitchFile(t, sr.vault.BackupPath("codex", "bob", "auth.json"), credentials["bob"])
				called := false
				sr.loginHandler = &smartSwitchLoginHandler{
					LoginHandler: handoff.GetHandler("codex"),
					trigger: func() error {
						called = true
						sr.loginDone <- loginResult{success: true}
						return nil
					},
				}
				if tc.retryAfter != "" {
					sr.detector.Check(tc.retryAfter)
				}
				start := time.Now()
				sr.handleRateLimit(context.Background())
				if elapsed := time.Since(start); elapsed != time.Minute {
					t.Fatalf("handoff elapsed %v, want the full one-minute backoff", elapsed)
				}
				if called || sr.handoffAttempts != 0 || sr.triedProfiles["bob"] || sr.currentProfile != "alice" || sr.getState() != HandoffFailed {
					t.Fatal("credential that expired during backoff advanced handoff or consumed a retry")
				}
				assertSmartSwitchFile(t, livePath, credentials["alice"])
				for name, want := range credentials {
					assertSmartSwitchFile(t, sr.vault.BackupPath("codex", name, "auth.json"), want)
				}
				profiles, err := sr.vault.List("codex")
				if err != nil || !reflect.DeepEqual(profiles, []string{"alice", "bob"}) {
					t.Fatalf("ineligible handoff changed vault inventory: profiles=%v err=%v", profiles, err)
				}
			})
		})
	}
}

type smartRetryOutputController struct {
	pty.Controller
	output []string
}

func (c *smartRetryOutputController) Close() error { return nil }

func (c *smartRetryOutputController) ReadOutput() (string, error) {
	if len(c.output) == 0 {
		return "", io.EOF
	}
	output := c.output[0]
	c.output = c.output[1:]
	return output, nil
}

func TestSmartRunner_RetryAfterOutputDelaysHandoff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backoff time.Duration
		output  []string
	}{
		{"header after error in same packet", 0, []string{"HTTP 429 Too Many Requests\nRetry-After: 60\n"}},
		{"split header arrives during backoff", 40 * time.Millisecond, []string{"HTTP 429 Too Many Requests\n", "Retry-Af", "ter: 60\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sr, livePath, credentials := newSmartRetryRunner(t, []string{"alice", "bob"}, smartRetryPolicy(3, tc.backoff))
			called := false
			sr.loginHandler = &smartSwitchLoginHandler{
				LoginHandler: handoff.GetHandler("codex"),
				trigger: func() error {
					called = true
					sr.loginDone <- loginResult{success: true}
					return nil
				},
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan struct{})
			sr.monitorOutput(ctx, &smartRetryOutputController{output: tc.output}, done, nil)
			<-done
			sr.wg.Wait()
			if called || sr.handoffAttempts != 0 || sr.getState() != HandoffFailed || sr.detector.RetryAfter().IsZero() {
				t.Fatal("complete Retry-After output did not delay the handoff until cancellation")
			}
			assertSmartSwitchFile(t, livePath, credentials["alice"])
		})
	}
}

func TestSmartRunner_RunRecordsUnretriedRateLimit(t *testing.T) {
	for _, trailingNewline := range []bool{true, false} {
		t.Run(fmt.Sprintf("newline=%v", trailingNewline), func(t *testing.T) {
			sr, livePath, credentials := newSmartRetryRunner(t, []string{"alice"}, smartRetryPolicy(0, 0))
			db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "caam.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			sr.db = db
			originalExec := ExecCommand
			ExecCommand = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
				format := "%s"
				if trailingNewline {
					format = "%s\\n"
				}
				return exec.CommandContext(ctx, "sh", "-c", "printf '"+format+"' 'HTTP 429 Too Many Requests'; exit 7")
			}
			t.Cleanup(func() { ExecCommand = originalExec })
			prof, err := profile.NewStore(filepath.Join(t.TempDir(), "profiles")).Create("codex", "alice", "oauth")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = sr.Run(ctx, RunOptions{
				Provider:     &mockProvider{id: "codex", defaultBin: "codex"},
				Profile:      prof,
				NoLock:       true,
				UseGlobalEnv: true,
			})
			var exitErr *ExitCodeError
			if !errors.As(err, &exitErr) || exitErr.Code != 7 {
				t.Fatalf("Run error = %v, want exit code 7", err)
			}
			if !sr.rateLimitHit || sr.handoffAttempts != 0 || sr.handoffCount != 0 {
				t.Fatal("disabled retries lost the detected rate limit")
			}
			sessions, err := db.GetWrapSessions("codex", time.Time{}, 10)
			if err != nil || len(sessions) != 1 {
				t.Fatalf("sessions=%v error=%v", sessions, err)
			}
			if !sessions[0].RateLimitHit || sessions[0].ExitCode != 7 || sessions[0].ProfileName != "alice" {
				t.Fatalf("incorrect session result: %+v", sessions[0])
			}
			assertSmartSwitchFile(t, livePath, credentials["alice"])
		})
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
	t.Setenv("OPENAI_API_KEY", "synthetic-ambient")
	t.Setenv("ANTHROPIC_API_KEY", "synthetic-unrelated")
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
		for _, entry := range captured.Env {
			if entry == "OPENAI_API_KEY=synthetic-ambient" {
				t.Fatal("ambient API key overrides selected Codex file")
			}
		}
		if !bytes.Contains([]byte(fmt.Sprint(captured.Env)), []byte("ANTHROPIC_API_KEY=synthetic-unrelated")) {
			t.Fatal("unrelated provider key was removed from child environment")
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
