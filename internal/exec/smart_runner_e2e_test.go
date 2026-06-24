package exec

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMockCLI_Handoff simulates a CLI tool that hits a rate limit and then accepts login.
func TestMockCLI_Handoff(t *testing.T) {
	if os.Getenv("GO_WANT_MOCK_CLI") != "1" {
		return
	}

	// 1. Output rate limit message
	fmt.Println("Processing...")
	time.Sleep(100 * time.Millisecond)
	fmt.Println("Error: rate limit exceeded")

	// 2. Wait for login command. The SmartRunner handoff path is exercised with
	// the gemini provider (claude is intentionally bypassed by SmartRunner.Run
	// because Claude Code manages its own terminal), so the injected login
	// command is gemini's "/auth" and success must match a gemini completion
	// pattern.
	reader := bufio.NewReader(os.Stdin)
	line, _ := reader.ReadString('\n')

	if strings.TrimSpace(line) == "/auth" {
		fmt.Println("Logging in...")
		time.Sleep(100 * time.Millisecond)
		fmt.Println("successfully authenticated")
	} else {
		fmt.Printf("Unknown command: %s", line)
		os.Exit(1)
	}

	// Keep running a bit
	time.Sleep(500 * time.Millisecond)
}

type MockNotifier struct {
	Alerts []*notify.Alert
}

func (m *MockNotifier) Notify(alert *notify.Alert) error {
	m.Alerts = append(m.Alerts, alert)
	return nil
}
func (m *MockNotifier) Name() string    { return "mock" }
func (m *MockNotifier) Available() bool { return true }

func TestSmartRunner_E2E(t *testing.T) {
	// Skip in CI: This test requires PTY functionality that behaves
	// differently across platforms and CI environments
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
		t.Skip("Skipping E2E test in CI environment due to PTY limitations")
	}

	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize environment")
	rootDir := h.TempDir
	vaultDir := filepath.Join(rootDir, "vault")

	// The handoff swaps auth files into the live provider location. Point
	// GEMINI_HOME at a temp dir so the test never touches the real ~/.gemini.
	geminiHome := filepath.Join(rootDir, "gemini-home")
	require.NoError(t, os.MkdirAll(geminiHome, 0755))
	h.SetEnv("GEMINI_HOME", geminiHome)
	// Seed a live auth file so the handoff has a current state to back up before
	// swapping to the backup profile.
	require.NoError(t, os.WriteFile(filepath.Join(geminiHome, "settings.json"),
		[]byte(`{"selectedAuthType":"oauth-personal","account":"active@example.com"}`), 0600))

	// Setup profiles for the gemini provider (claude is bypassed by SmartRunner).
	// Profile 1 (Current): "active"
	// Profile 2 (Backup): "backup"
	// settings.json is gemini's required auth file; give each profile distinct
	// content so round-robin selection treats them as different identities.
	createProfile := func(name string) {
		dir := filepath.Join(vaultDir, "gemini", name)
		require.NoError(t, os.MkdirAll(dir, 0755))
		content := fmt.Sprintf(`{"selectedAuthType":"oauth-personal","account":"%s@example.com"}`, name)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0600))
	}
	createProfile("active")
	createProfile("backup")

	vault := authfile.NewVault(vaultDir)

	// Setup Temp DB
	dbPath := filepath.Join(rootDir, "caam.db")
	db, err := caamdb.OpenAt(dbPath)
	require.NoError(t, err)
	defer db.Close()

	// Mock ExecCommand
	originalExec := ExecCommand
	defer func() { ExecCommand = originalExec }()

	ExecCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		fmt.Println("DEBUG: ExecCommand called")
		cs := []string{"-test.run=^TestMockCLI_Handoff$", "--"}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(), "GO_WANT_MOCK_CLI=1")
		return cmd
	}

	// Setup SmartRunner
	cfg := config.DefaultSPMConfig().Handoff
	notifier := &MockNotifier{}

	// Need mock provider registry?
	// SmartRunner.Run uses opts.Provider.
	// We need a provider that returns "claude" ID.
	// We can use the real Claude provider, but we need to ensure it uses our mocked paths?
	// `DetectExistingAuth` etc. are not used by `SmartRunner.Run` directly, only `Runner.Run`.
	// `Runner.Run` calls `opts.Provider.Env`.
	// `internal/provider/claude/claude.go` implements `Env`.

	// Using a mock provider is safer to avoid file system dependency issues.
	// Use gemini (not claude): SmartRunner.Run short-circuits claude to the plain
	// Runner (which would invoke the real "mock-bin" and ignore the ExecCommand
	// mock). gemini stays on the PTY handoff path this test is verifying.
	mockProv := &MockProvider{id: "gemini"} // Reuse MockProvider if exported or define locally

	// SmartRunner needs rotation selector
	// Selector needs health store and db
	selector := rotation.NewSelector(rotation.AlgorithmRoundRobin, nil, db)

	runner := &Runner{}

	opts := SmartRunnerOptions{
		HandoffConfig: &cfg,
		Vault:         vault,
		DB:            db,
		Rotation:      selector,
		Notifier:      notifier,
	}

	sr := NewSmartRunner(runner, opts)

	// Prepare RunOptions
	prof, err := profile.NewStore(filepath.Join(rootDir, "profiles")).Create("gemini", "active", "oauth")
	require.NoError(t, err)

	runOpts := RunOptions{
		Profile:  prof,
		Provider: mockProv,
		Args:     []string{},
		Env:      map[string]string{"GO_WANT_MOCK_CLI": "1"},
	}

	h.EndStep("Setup")

	// 2. Run
	h.StartStep("Run", "Execute SmartRunner")

	// Run should:
	// 1. Start mock CLI
	// 2. Detect "rate limit exceeded"
	// 3. Trigger handoff
	// 4. Select "backup"
	// 5. Swap auth (mocked vault works on temp dir)
	// 6. Inject "/login"
	// 7. Detect "successfully logged in"
	// 8. Notify user

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err = sr.Run(ctx, runOpts)
	require.NoError(t, err)

	h.EndStep("Run")

	// 3. Verify
	h.StartStep("Verify", "Check state and notifications")

	assert.Equal(t, "backup", sr.currentProfile)
	assert.Equal(t, 1, sr.handoffCount)

	// Check notifications
	require.NotEmpty(t, notifier.Alerts)
	foundSwitch := false
	for _, a := range notifier.Alerts {
		if strings.Contains(a.Message, "Switched to backup") {
			foundSwitch = true
		}
	}
	assert.True(t, foundSwitch, "Did not notify about switch")

	// Check DB for Activation Event
	activations, err := db.GetEvents("gemini", "active", time.Now().Add(-1*time.Hour), 10)
	require.NoError(t, err)
	assert.NotEmpty(t, activations, "Should have logged activation event")
	assert.Equal(t, caamdb.EventActivate, activations[0].Type)

	// Check DB for Wrap Session
	sessions, err := db.GetWrapSessions("gemini", time.Now().Add(-1*time.Hour), 10)
	require.NoError(t, err)
	assert.NotEmpty(t, sessions, "Should have recorded wrap session")
	assert.Equal(t, "backup", sessions[0].ProfileName, "Session should record final profile")
	assert.True(t, sessions[0].RateLimitHit, "Session should mark rate limit hit")

	h.EndStep("Verify")
}

// Local MockProvider (minimal)
type MockProvider struct {
	id string
}

func (m *MockProvider) ID() string          { return m.id }
func (m *MockProvider) DisplayName() string { return "Mock" }
func (m *MockProvider) DefaultBin() string  { return "mock-bin" }
func (m *MockProvider) Env(ctx context.Context, p *profile.Profile) (map[string]string, error) {
	return nil, nil
}

// Other methods needed for interface compliance...
func (m *MockProvider) SupportedAuthModes() []provider.AuthMode                      { return nil }
func (m *MockProvider) AuthFiles() []provider.AuthFileSpec                           { return nil }
func (m *MockProvider) PrepareProfile(ctx context.Context, p *profile.Profile) error { return nil }
func (m *MockProvider) Login(ctx context.Context, p *profile.Profile) error          { return nil }
func (m *MockProvider) Logout(ctx context.Context, p *profile.Profile) error         { return nil }
func (m *MockProvider) Status(ctx context.Context, p *profile.Profile) (*provider.ProfileStatus, error) {
	return nil, nil
}
func (m *MockProvider) ValidateProfile(ctx context.Context, p *profile.Profile) error { return nil }
func (m *MockProvider) DetectExistingAuth() (*provider.AuthDetection, error)          { return nil, nil }
func (m *MockProvider) ImportAuth(ctx context.Context, s string, p *profile.Profile) ([]string, error) {
	return nil, nil
}
func (m *MockProvider) ValidateToken(ctx context.Context, p *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	return nil, nil
}
