package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/handoff"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/notify"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
)

// ExecCommand allows mocking exec.CommandContext in tests
var ExecCommand = exec.CommandContext

// SmartRunner orchestrates the auto-handoff flow for seamless profile switching.
// When a rate limit is detected in the CLI output, SmartRunner:
// 1. Selects the best backup profile using the rotation algorithm
// 2. Swaps auth files atomically
// 3. Injects the login command via PTY
// 4. Waits for login completion
// 5. Notifies the user and continues execution
//
// On any failure, it rolls back to the original profile and shows manual instructions.
type SmartRunner struct {
	*Runner

	detector      *ratelimit.Detector
	rotation      *rotation.Selector
	vault         *authfile.Vault
	db            *caamdb.DB
	authPool      *authpool.AuthPool
	ptyController pty.Controller
	loginHandler  handoff.LoginHandler
	handoffConfig *config.HandoffConfig
	notifier      notify.Notifier

	// State (protected by mu)
	mu              sync.Mutex
	currentProfile  string
	previousProfile string // For rollback
	handoffCount    int
	state           HandoffState

	// Login detection channel
	loginDone chan loginResult
}

// loginResult captures the outcome of a login attempt.
type loginResult struct {
	success bool
	message string
}

// SmartRunnerOptions configures the SmartRunner.
type SmartRunnerOptions struct {
	HandoffConfig *config.HandoffConfig
	Notifier      notify.Notifier
	Vault         *authfile.Vault
	DB            *caamdb.DB
	AuthPool      *authpool.AuthPool
	Rotation      *rotation.Selector
}

// NewSmartRunner creates a new SmartRunner.
func NewSmartRunner(runner *Runner, opts SmartRunnerOptions) *SmartRunner {
	// Use default notifier if none provided
	notifier := opts.Notifier
	if notifier == nil {
		notifier = &notify.TerminalNotifier{}
	}

	return &SmartRunner{
		Runner:        runner,
		vault:         opts.Vault,
		db:            opts.DB,
		authPool:      opts.AuthPool,
		rotation:      opts.Rotation,
		handoffConfig: opts.HandoffConfig,
		notifier:      notifier,
		state:         Running,
		loginDone:     make(chan loginResult, 1),
	}
}

// Run executes the command with smart handoff capabilities.
func (r *SmartRunner) Run(ctx context.Context, opts RunOptions) error {
	// Initialize rate limit detector
	detector, err := ratelimit.NewDetector(
		ratelimit.ProviderFromString(opts.Provider.ID()),
		nil, // Use default patterns
	)
	if err != nil {
		return fmt.Errorf("create detector: %w", err)
	}
	r.detector = detector

	// Get login handler
	r.loginHandler = handoff.GetHandler(opts.Provider.ID())
	if r.loginHandler == nil {
		// Fallback to basic runner if no login handler (can't do handoff)
		return r.Runner.Run(ctx, opts)
	}

	r.currentProfile = opts.Profile.Name

	// Lock profile
	if !opts.NoLock {
		if err := opts.Profile.LockWithCleanup(); err != nil {
			return fmt.Errorf("lock profile: %w", err)
		}
		defer opts.Profile.Unlock()
	}

	// Get env
	providerEnv, err := opts.Provider.Env(ctx, opts.Profile)
	if err != nil {
		return fmt.Errorf("get provider env: %w", err)
	}

	// Build command
	bin := opts.Provider.DefaultBin()
	cmd := ExecCommand(ctx, bin, opts.Args...)
	
	// Apply env (same as Runner.Run)
	envMap := make(map[string]string)
	for _, e := range os.Environ() {
		parts := splitEnv(e)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}
	for k, v := range providerEnv {
		envMap[k] = v
	}
	for k, v := range opts.Env {
		envMap[k] = v
	}
	cmd.Env = make([]string, 0, len(envMap))
	for k, v := range envMap {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	// Create PTY controller
	ctrl, err := pty.NewController(cmd, nil)
	if err != nil {
		return fmt.Errorf("create pty controller: %w", err)
	}
	r.ptyController = ctrl
	defer ctrl.Close()

	// Start the PTY (this executes the command)
	if err := ctrl.Start(); err != nil {
		return fmt.Errorf("start pty: %w", err)
	}

	// Start output monitoring in background
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	go r.monitorOutput(monitorCtx, ctrl)

	// Wait for command completion using the controller's Wait method
	exitCode, waitErr := ctrl.Wait()

	// Update profile metadata
	opts.Profile.LastUsedAt = time.Now()
	if saveErr := opts.Profile.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to save profile metadata: %v\n", saveErr)
	}

	if waitErr != nil {
		return fmt.Errorf("command failed: %w", waitErr)
	}
	if exitCode != 0 {
		return fmt.Errorf("command exited with code %d", exitCode)
	}

	return nil
}

// handleRateLimit handles the rate limit detection and handoff flow.
func (r *SmartRunner) handleRateLimit(ctx context.Context) {
	r.mu.Lock()
	if r.state != Running {
		r.mu.Unlock()
		return // Already handling or failed
	}
	r.state = RateLimited
	r.mu.Unlock()

	// Notify detection
	r.notifyHandoff(r.currentProfile, "selecting backup...")

	// Get file set
	fileSet, ok := authfile.GetAuthFileSet(r.loginHandler.Provider())
	if !ok {
		r.failWithManual("unknown provider file set")
		return
	}

	// 1. Save current state for rollback
	r.previousProfile = r.currentProfile
	if err := r.vault.Backup(fileSet, r.currentProfile); err != nil {
		r.failWithManual("failed to backup current profile: %v", err)
		return
	}

	defer func() {
		if r.state == HandoffFailed {
			r.rollback(fileSet)
		}
	}()

	// 2. Select best backup profile
	r.setState(SelectingBackup)
	
	// Get all profiles
	profiles, err := r.vault.List(r.loginHandler.Provider())
	if err != nil {
		r.failWithManual("failed to list profiles: %v", err)
		return
	}

	// Select best
	selection, err := r.rotation.Select(r.loginHandler.Provider(), profiles, r.currentProfile)
	if err != nil {
		r.failWithManual("no backup available: %v", err)
		return
	}
	nextProfile := selection.Selected

	if nextProfile == r.currentProfile {
		r.failWithManual("no other profiles available")
		return
	}

	r.notifyHandoff(r.currentProfile, nextProfile)

	// 3. Mark current profile as in cooldown (if authPool is available)
	cooldownDuration := 60 * time.Minute
	if r.authPool != nil {
		r.authPool.SetCooldown(r.loginHandler.Provider(), r.currentProfile, cooldownDuration)
	}
	if r.db != nil {
		r.db.SetCooldown(r.loginHandler.Provider(), r.currentProfile, time.Now(), cooldownDuration, "auto-detected via SmartRunner")
	}

	// 4. Swap auth files
	r.setState(SwappingAuth)
	if err := r.vault.Restore(fileSet, nextProfile); err != nil {
		r.failWithManual("auth swap failed: %v", err)
		return
	}

	// 5. Inject login command
	r.setState(LoggingIn)
	if err := r.loginHandler.TriggerLogin(r.ptyController); err != nil {
		r.failWithManual("login trigger failed: %v", err)
		return
	}

	// 6. Wait for login completion (monitorOutput detects success/failure and signals via loginDone)
	loginTimeout := 30 * time.Second
	if r.handoffConfig != nil && r.handoffConfig.DebounceDelay.Duration() > 0 {
		loginTimeout = r.handoffConfig.DebounceDelay.Duration() * 10 // 10x debounce as timeout
		if loginTimeout < 30*time.Second {
			loginTimeout = 30 * time.Second
		}
	}

	select {
	case result := <-r.loginDone:
		if !result.success {
			r.failWithManual("login failed: %s", result.message)
			return
		}
	case <-time.After(loginTimeout):
		r.failWithManual("login timed out after %v", loginTimeout)
		return
	case <-ctx.Done():
		r.failWithManual("context cancelled during login")
		return
	}

	// 7. Success!
	r.setState(LoginComplete)
	r.currentProfile = nextProfile
	r.handoffCount++

	r.notifier.Notify(&notify.Alert{
		Level:   notify.Info,
		Title:   "Profile switched",
		Message: fmt.Sprintf("Switched to %s. Continue working.", nextProfile),
	})

	// Reset detector state so we don't immediately trigger again
	r.detector.Reset()
	r.setState(Running)
}

func (r *SmartRunner) rollback(fileSet authfile.AuthFileSet) {
	fmt.Fprintf(os.Stderr, "Rolling back to %s...\n", r.previousProfile)
	if err := r.vault.Restore(fileSet, r.previousProfile); err != nil {
		fmt.Fprintf(os.Stderr, "Rollback failed: %v\n", err)
	}
	r.currentProfile = r.previousProfile
	r.detector.Reset()
	r.setState(Running)
}

func (r *SmartRunner) failWithManual(format string, args ...interface{}) {
	r.setState(HandoffFailed)
	msg := fmt.Sprintf(format, args...)
	
	r.notifier.Notify(&notify.Alert{
		Level:   notify.Warning,
		Title:   "Auto-handoff failed",
		Message: msg,
		Action:  "Run 'caam ls' to see available profiles, then 'caam activate <profile>'",
	})
	
	fmt.Fprintf(os.Stderr, "\n[caam] Auto-handoff failed: %s\n", msg)
}

func (r *SmartRunner) notifyHandoff(from, to string, msg ...string) {
	message := fmt.Sprintf("Rate limit on %s, switching to %s...", from, to)
	if len(msg) > 0 {
		message = msg[0]
	}
	r.notifier.Notify(&notify.Alert{
		Level:   notify.Info,
		Title:   "Switching profiles",
		Message: message,
	})
}

func (r *SmartRunner) setState(s HandoffState) {
	r.mu.Lock()
	r.state = s
	r.mu.Unlock()
}

func (r *SmartRunner) monitorOutput(ctx context.Context, ctrl pty.Controller) {
	// Create an observing writer to handle split packets and buffering
	// Use a local flag to prevent repeated dispatching within this loop context
	dispatched := false
	
	writer := ratelimit.NewObservingWriter(r.detector, func(line string) {
		// This callback is triggered when a complete line is processed
		if !dispatched && r.detector.Detected() {
			dispatched = true
			go r.handleRateLimit(ctx)
		}
	})

	for {
		// Poll for output (ReadOutput is non-blocking with timeout)
		output, err := ctrl.ReadOutput()
		if err != nil {
			break
		}
		
		if output != "" {
			os.Stdout.Write([]byte(output))
			
			r.mu.Lock()
			state := r.state
			r.mu.Unlock()

			if state == Running {
				// If detector was reset (e.g. after successful handoff), allow new dispatch
				if !r.detector.Detected() {
					dispatched = false
				}

				// Only write to observer if we haven't dispatched yet
				// This avoids processing output during the handoff transition
				if !dispatched {
					writer.Write([]byte(output))
				}
			} else if state == LoggingIn {
				// Check for login completion and signal handleRateLimit
				if r.loginHandler.IsLoginComplete(output) {
					select {
					case r.loginDone <- loginResult{success: true}:
					default:
						// Channel already has a value
					}
				} else if failed, msg := r.loginHandler.IsLoginFailed(output); failed {
					select {
					case r.loginDone <- loginResult{success: false, message: msg}:
					default:
						// Channel already has a value
					}
				}
			}
		}
		
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Millisecond):
			// Yield
		}
	}
}

func splitEnv(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '=' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}
