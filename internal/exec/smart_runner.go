package exec

import (
	"context"
	"errors"
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
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"golang.org/x/term"
)

// ExecCommand allows mocking exec.CommandContext in tests
var ExecCommand = exec.CommandContext

// SmartRunner orchestrates the auto-handoff flow for seamless profile switching.
// When a rate limit is detected in the CLI output, SmartRunner:
// 1. Records the limit and selects an untried backup within the retry budget
// 2. Waits for configured backoff and any explicit Retry-After deadline
// 3. Swaps auth files atomically
// 4. Injects the login command via PTY and waits for login completion
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
	retryConfig   config.WrapConfig
	notifier      notify.Notifier

	// Cooldown duration to apply when rate limit is detected
	cooldownDuration time.Duration

	// State (protected by mu)
	mu              sync.Mutex
	currentProfile  string
	previousProfile string // For rollback
	handoffCount    int
	handoffAttempts int
	rateLimitHit    bool
	triedProfiles   map[string]bool
	state           HandoffState

	// WaitGroup to track background goroutines (handleRateLimit)
	wg sync.WaitGroup

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

	// RetryConfig is the effective provider retry policy. Nil uses defaults;
	// an explicit MaxRetries of zero disables automatic handoffs.
	RetryConfig      *config.WrapConfig
	Notifier         notify.Notifier
	Vault            *authfile.Vault
	DB               *caamdb.DB
	AuthPool         *authpool.AuthPool
	Rotation         *rotation.Selector
	CooldownDuration time.Duration
}

// NewSmartRunner creates a new SmartRunner.
func NewSmartRunner(runner *Runner, opts SmartRunnerOptions) *SmartRunner {
	// Use default notifier if none provided
	notifier := opts.Notifier
	if notifier == nil {
		notifier = &notify.TerminalNotifier{}
	}
	if opts.Vault != nil && opts.Rotation != nil {
		opts.Rotation.SetVaultPath(opts.Vault.BasePath())
	}
	retryConfig := config.DefaultWrapConfig()
	if opts.RetryConfig != nil {
		retryConfig = *opts.RetryConfig
	}
	cooldownDuration := retryConfig.CooldownDuration.Duration()
	if opts.CooldownDuration > 0 {
		cooldownDuration = opts.CooldownDuration
	}

	return &SmartRunner{
		Runner:           runner,
		vault:            opts.Vault,
		db:               opts.DB,
		authPool:         opts.AuthPool,
		rotation:         opts.Rotation,
		handoffConfig:    opts.HandoffConfig,
		retryConfig:      retryConfig,
		notifier:         notifier,
		cooldownDuration: cooldownDuration,
		triedProfiles:    make(map[string]bool),
		state:            Running,
		loginDone:        make(chan loginResult, 1),
	}
}

// Run executes the command with smart handoff capabilities.
func (r *SmartRunner) Run(ctx context.Context, opts RunOptions) (err error) {
	// Claude Code is a full TUI application that manages its own terminal.
	// The nested PTY wrapper conflicts with its terminal handling, causing hangs.
	// Bypass SmartRunner entirely but still log the session for analytics.
	if opts.Provider.ID() == "claude" {
		r.currentProfile = opts.Profile.Name
		if r.db != nil {
			_ = r.db.Log(caamdb.Event{
				Type:        caamdb.EventActivate,
				Provider:    opts.Provider.ID(),
				ProfileName: r.currentProfile,
				Timestamp:   time.Now(),
			})
			startTime := time.Now()
			defer func() {
				duration := time.Since(startTime)
				finalCode := 0
				if err != nil {
					var exitErr *ExitCodeError
					if errors.As(err, &exitErr) {
						finalCode = exitErr.Code
					} else {
						finalCode = 1
					}
				}
				_ = r.db.RecordWrapSession(caamdb.WrapSession{
					Provider:        opts.Provider.ID(),
					ProfileName:     r.currentProfile,
					StartedAt:       startTime,
					EndedAt:         time.Now(),
					DurationSeconds: int(duration.Seconds()),
					ExitCode:        finalCode,
				})
			}()
		}
		return r.Runner.Run(ctx, opts)
	}

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

	r.mu.Lock()
	r.currentProfile = opts.Profile.Name
	r.previousProfile = ""
	r.handoffCount = 0
	r.handoffAttempts = 0
	r.rateLimitHit = false
	r.triedProfiles = make(map[string]bool)
	r.state = Running
	r.mu.Unlock()

	// Log activation event
	if r.db != nil {
		_ = r.db.Log(caamdb.Event{
			Type:        caamdb.EventActivate,
			Provider:    opts.Provider.ID(),
			ProfileName: r.currentProfile,
			Timestamp:   time.Now(),
		})
	}

	// Track session
	startTime := time.Now()
	defer func() {
		if r.db != nil {
			duration := time.Since(startTime)
			// Determine final exit code from error
			finalCode := 0
			if err != nil {
				var exitErr *ExitCodeError
				// Check if it's an ExitCodeError (wrapper type in this package)
				if errors.As(err, &exitErr) {
					finalCode = exitErr.Code
				} else {
					finalCode = 1 // Generic error
				}
			}

			session := caamdb.WrapSession{
				Provider:        opts.Provider.ID(),
				ProfileName:     r.currentProfile, // Use the final profile
				StartedAt:       startTime,
				EndedAt:         time.Now(),
				DurationSeconds: int(duration.Seconds()),
				ExitCode:        finalCode,
				RateLimitHit:    r.rateLimitHit,
			}
			if r.handoffCount > 0 {
				session.Notes = fmt.Sprintf("handoffs: %d", r.handoffCount)
			}
			_ = r.db.RecordWrapSession(session)
		}
	}()

	// Lock profile
	if !opts.NoLock {
		if err := opts.Profile.LockWithCleanup(); err != nil {
			return fmt.Errorf("lock profile: %w", err)
		}
		defer opts.Profile.Unlock()
	}

	// Get env. Honor UseGlobalEnv exactly like Runner.Run does (issue #64):
	// vault-based runs (`caam run`) swap auth files inside the REAL home, so
	// injecting the provider's isolated-profile env (HOME, CODEX_HOME, ...)
	// would point the tool at a profile directory that is not logged in.
	environment := provider.CredentialEnvironment(opts.Provider.ID(), provider.AuthModeOAuth, nil)
	if !opts.UseGlobalEnv {
		if preparer, ok := opts.Provider.(provider.ProfileRunPreparer); ok {
			if err := preparer.PrepareRun(ctx, opts.Profile); err != nil {
				return fmt.Errorf("prepare %s launch: %w", opts.Provider.ID(), err)
			}
		}
		environment, err = provider.ProfileEnvironment(ctx, opts.Provider, opts.Profile)
		if err != nil {
			return fmt.Errorf("get provider env: %w", err)
		}
	}

	// Build command
	bin := opts.Provider.DefaultBin()
	cmd := ExecCommand(ctx, bin, opts.Args...)

	cmd.Env = provider.MergeEnvironment(os.Environ(), environment, opts.Env)
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	// Terminal proxying (issue #74): when stdin is a real terminal, the child's
	// PTY is created at that terminal's size and follows it, the terminal is
	// switched to raw mode so keystrokes reach the child un-echoed and
	// un-interpreted, and stdin is relayed into the PTY. See terminal_proxy.go.
	stdinFd := int(os.Stdin.Fd())
	interactive := term.IsTerminal(stdinFd)
	ptyOpts := pty.DefaultOptions()
	if interactive {
		if rows, cols, ok := terminalSize(stdinFd); ok {
			ptyOpts.Rows, ptyOpts.Cols = rows, cols
		}
	}

	// Create PTY controller
	ctrl, err := pty.NewController(cmd, ptyOpts)
	if err != nil {
		return fmt.Errorf("create pty controller: %w", err)
	}
	r.ptyController = ctrl
	defer ctrl.Close()

	// Start the PTY (this executes the command)
	if err := ctrl.Start(); err != nil {
		return fmt.Errorf("start pty: %w", err)
	}

	// restoreTerminal puts the real terminal back into its pre-run state. It
	// is called explicitly as soon as the child has exited (before anything
	// else is printed) and deferred as a safety net for early returns.
	restoreTerminal := func() {}
	if interactive {
		if state, rawErr := term.MakeRaw(stdinFd); rawErr == nil {
			var once sync.Once
			restoreTerminal = func() {
				once.Do(func() { _ = term.Restore(stdinFd, state) })
			}
		}
		stopResize := watchTerminalResize(stdinFd, ctrl)
		defer stopResize()
	}
	defer restoreTerminal()

	// Relay input into the child's PTY. This runs for pipes as well as
	// terminals so `echo prompt | caam run ...` reaches the tool; the goroutine
	// ends when stdin is exhausted or the PTY closes.
	go relayStdin(os.Stdin, ctrl)

	var capture *codexSessionCapture
	if opts.Provider.ID() == "codex" {
		capture = &codexSessionCapture{}
	}

	// Start output monitoring in background
	monitorCtx, cancelMonitor := context.WithCancel(ctx)
	defer cancelMonitor()
	monitorDone := make(chan struct{})

	var observer func(string)
	if capture != nil {
		observer = capture.ObserveLine
	}
	go r.monitorOutput(monitorCtx, ctrl, monitorDone, observer)

	// Wait for command completion using the controller's Wait method
	exitCode, waitErr := ctrl.Wait()

	// Cancel monitor context, wait for monitor to stop, then wait for any handoff goroutines.
	cancelMonitor()
	<-monitorDone
	r.wg.Wait()

	// The child is gone and its output fully drained: give the terminal back
	// before any further (cooked-mode) output such as warnings below.
	restoreTerminal()

	// Update profile metadata
	now := time.Now()
	opts.Profile.LastUsedAt = now
	if capture != nil {
		if sessionID := capture.ID(); sessionID != "" {
			opts.Profile.LastSessionID = sessionID
			opts.Profile.LastSessionTS = now.UTC()
		}
	}
	if saveErr := opts.Profile.Save(); saveErr != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to save profile metadata: %v\n", saveErr)
	}

	if waitErr != nil {
		return fmt.Errorf("command failed: %w", waitErr)
	}
	if exitCode != 0 {
		return &ExitCodeError{Code: exitCode}
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
	currentProfile := r.currentProfile
	r.rateLimitHit = true
	r.triedProfiles[currentProfile] = true
	attempt := r.handoffAttempts
	r.mu.Unlock()

	// A known-limited account stays unavailable even if there is no backup,
	// retries are disabled, or a later handoff fails. The in-session tried set
	// also protects callers that do not have a database or persistent cooldown.
	if r.cooldownDuration > 0 {
		if r.authPool != nil {
			r.authPool.SetCooldown(r.loginHandler.Provider(), currentProfile, r.cooldownDuration)
		}
		if r.db != nil {
			if _, err := r.db.SetCooldown(r.loginHandler.Provider(), currentProfile, time.Now(), r.cooldownDuration, "auto-detected via SmartRunner"); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to record rate limit cooldown: %v\n", err)
			}
		}
	}
	if !r.retryConfig.ShouldRetry(attempt) {
		r.failWithManual("retry budget exhausted after %d handoff attempts", attempt)
		return
	}
	if err := ctx.Err(); err != nil {
		r.failWithManual("context cancelled before handoff: %v", err)
		return
	}
	if r.vault == nil || r.rotation == nil {
		r.failWithManual("no backup selection is configured")
		return
	}

	// Notify detection
	r.notifyHandoff(currentProfile, "selecting backup...")

	// Get file set
	fileSet, ok := authfile.GetAuthFileSet(r.loginHandler.Provider())
	if !ok {
		r.failWithManual("unknown provider file set")
		return
	}

	spmConfig, err := config.LoadSPMConfig()
	if err != nil {
		r.failWithManual("load activation safety settings: %v", err)
		return
	}
	switchOptions := authfile.SwitchOptions{
		BackupMode:     spmConfig.Safety.AutoBackupBeforeSwitch,
		MaxAutoBackups: spmConfig.Safety.MaxAutoBackups,
	}
	r.previousProfile = ""

	// Select and validate a target before preserving or changing live auth.
	r.setState(SelectingBackup)

	// Get all profiles
	profiles, err := r.vault.List(r.loginHandler.Provider())
	if err != nil {
		r.failWithManual("failed to list profiles: %v", err)
		return
	}
	r.mu.Lock()
	available := make([]string, 0, len(profiles))
	for _, name := range profiles {
		if !r.triedProfiles[name] {
			available = append(available, name)
		}
	}
	r.mu.Unlock()

	// Select best
	selection, err := r.rotation.Select(r.loginHandler.Provider(), available, currentProfile)
	if err != nil {
		r.failWithManual("no backup available: %v", err)
		return
	}
	nextProfile := selection.Selected

	if nextProfile == currentProfile {
		r.failWithManual("no other profiles available")
		return
	}

	backoff := r.retryConfig.NextDelay(attempt)
	delay := ratelimit.RetryDelay(backoff, r.retryAfter(), time.Now())
	r.notifyHandoff(currentProfile, nextProfile,
		fmt.Sprintf("Rate limit on %s; retrying with %s after %s.", currentProfile, nextProfile, delay.Round(time.Millisecond)))
	if err := r.waitBeforeHandoff(ctx, backoff); err != nil {
		r.failWithManual("context cancelled before handoff: %v", err)
		return
	}
	// Charge attempts before changing auth, including failed swaps and logins.
	// Never select a failed handoff target again after rollback in this session.
	r.mu.Lock()
	r.handoffAttempts++
	r.triedProfiles[nextProfile] = true
	r.mu.Unlock()

	// 4. Preserve the verified live owner and swap auth files. The profile
	// label recorded at process startup may no longer own the live login.
	r.setState(SwappingAuth)
	switchResult, err := r.vault.Switch(fileSet, nextProfile, switchOptions)
	if switchResult != nil {
		r.previousProfile = switchResult.AutoBackup
		if r.previousProfile == "" {
			r.previousProfile = switchResult.ResnapshottedProfile
		}
		if r.previousProfile == "" {
			r.previousProfile = switchResult.PreviousProfile
		}
		for _, warning := range switchResult.Warnings {
			fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
		}
	}
	defer func() {
		if r.getState() == HandoffFailed && switchResult != nil && switchResult.RestoreStarted {
			r.rollback(fileSet, switchOptions)
		}
	}()
	if err != nil {
		r.failWithManual("auth swap failed: %v", err)
		return
	}
	r.currentProfile = nextProfile

	// 5. Inject login command
	r.drainLoginDone()
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

func (r *SmartRunner) retryAfter() time.Time {
	if r.detector == nil {
		return time.Time{}
	}
	return r.detector.RetryAfter()
}

// Keep backoff interruptible while the child continues to own its terminal.
// A Retry-After header received during the wait may extend its deadline.
func (r *SmartRunner) waitBeforeHandoff(ctx context.Context, backoff time.Duration) error {
	backoffUntil := time.Now().Add(backoff)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := time.Now()
		delay := ratelimit.RetryDelay(backoffUntil.Sub(now), r.retryAfter(), now)
		if delay <= 0 {
			return nil
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (r *SmartRunner) rollback(fileSet authfile.AuthFileSet, opts authfile.SwitchOptions) {
	if r.previousProfile == "" {
		r.failWithManual("no preserved outgoing login is available for rollback")
		return
	}
	fmt.Fprintf(os.Stderr, "Rolling back to %s...\n", r.previousProfile)
	result, err := r.vault.Switch(fileSet, r.previousProfile, opts)
	if err != nil {
		r.failWithManual("rollback failed: %v", err)
		return
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(os.Stderr, "Warning: %s\n", warning)
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

func (r *SmartRunner) getState() HandoffState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *SmartRunner) drainLoginDone() {
	for {
		select {
		case <-r.loginDone:
			continue
		default:
			return
		}
	}
}

func (r *SmartRunner) monitorOutput(ctx context.Context, ctrl pty.Controller, done chan<- struct{}, observer func(string)) {
	defer close(done)
	// Create an observing writer to handle split packets and buffering
	// Use a local flag to prevent repeated dispatching within this loop context
	dispatched := false

	writer := ratelimit.NewObservingWriter(r.detector, observer)
	dispatch := func() {
		if !dispatched && r.getState() == Running && r.detector.Detected() {
			dispatched = true
			r.wg.Add(1)
			go func() {
				defer r.wg.Done()
				r.handleRateLimit(ctx)
			}()
		}
	}
	defer func() {
		writer.Flush()
		dispatch()
	}()

	// draining indicates context was cancelled and we're draining remaining PTY output
	draining := false

	for {
		// Poll for output (ReadOutput is non-blocking with timeout)
		output, err := ctrl.ReadOutput()
		if err != nil {
			// EOF or error - PTY closed, stop reading
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

				// Process the whole packet before dispatch so headers following
				// the error line are visible to the retry wait.
				writer.Write([]byte(output))
				dispatch()
			} else if state == RateLimited || state == SelectingBackup {
				// The process still owns its terminal during backoff. Keep
				// collecting headers that may extend the server's deadline.
				writer.Write([]byte(output))
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

		// Check context cancellation
		if !draining {
			select {
			case <-ctx.Done():
				// Context cancelled, but continue draining PTY buffer until EOF
				// Set a deadline to prevent infinite draining if process doesn't exit
				draining = true
				go func() {
					time.Sleep(5 * time.Second)
					// Force close PTY if still draining after timeout
					ctrl.Close()
				}()
			case <-time.After(10 * time.Millisecond):
				// Yield
			}
		}
		// In drain mode, continue looping without delay until ReadOutput returns EOF
	}
}
