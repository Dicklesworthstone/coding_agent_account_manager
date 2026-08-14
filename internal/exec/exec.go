// Package exec handles running AI CLI tools with profile isolation.
package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"golang.org/x/term"
)

// Runner executes AI CLI tools with profile isolation.
type Runner struct {
	registry *provider.Registry
}

// NewRunner creates a new runner with the given provider registry.
func NewRunner(registry *provider.Registry) *Runner {
	return &Runner{registry: registry}
}

// RunOptions configures the exec behavior.
type RunOptions struct {
	// Profile is the profile to use.
	Profile *profile.Profile

	// Provider is the provider to use.
	Provider provider.Provider

	// Args are the arguments to pass to the CLI.
	Args []string

	// WorkDir is the working directory.
	WorkDir string

	// NoLock disables profile locking.
	NoLock bool

	// Env are additional environment variables.
	Env map[string]string

	// OnRateLimit is called when a rate limit is detected in command output.
	// It is invoked asynchronously and at most once per run.
	OnRateLimit func(ctx context.Context) error

	// RateLimitDelay debounces the rate limit callback to avoid rapid triggers.
	// If zero, the callback fires immediately.
	RateLimitDelay time.Duration

	// UseGlobalEnv disables directory isolation (HOME, etc.), forcing the tool
	// to use the global user environment. This is required for vault-based
	// auth file swapping (caam run).
	UseGlobalEnv bool
}

// ExitCodeError wraps a process exit code.
type ExitCodeError struct {
	Code int

	// NeedsTTY is true when the wrapped tool's stderr indicated it could not
	// start because stdin/stdout was not a terminal (TTY). Callers use this to
	// print an accurate hint pointing at the tool's non-interactive form
	// instead of a misleading usage dump (issue #36).
	NeedsTTY bool
}

func (e *ExitCodeError) Error() string {
	return fmt.Sprintf("exit code %d", e.Code)
}

// ttyRequiredRe matches stderr lines emitted by wrapped CLIs that abort because
// they need an interactive terminal. It is intentionally conservative: it only
// fires on phrasings that specifically describe a missing TTY (e.g. codex's
// "stdin is not a terminal" or the Ink/Node "Raw mode is not supported"), so
// unrelated runtime failures (auth errors, command-not-found, etc.) are never
// mislabeled as TTY problems.
var ttyRequiredRe = regexp.MustCompile(`(?i)` +
	`not a (tty|terminal)` +
	`|raw mode is not supported` +
	`|requires (an? )?(interactive )?(tty|terminal)` +
	`|must (be run|run) in (an? )?(interactive )?terminal` +
	`|no tty` +
	`|inappropriate ioctl for device`)

// ttyErrorDetector observes output lines and records whether any matched the
// "needs a terminal" signature. It is safe for use by the stderr-copy goroutine
// while the line is observed; the result is read only after cmd.Run returns and
// the observer has been flushed.
type ttyErrorDetector struct {
	mu  sync.Mutex
	hit bool
}

func (d *ttyErrorDetector) observe(line string) {
	if ttyRequiredRe.MatchString(line) {
		d.mu.Lock()
		d.hit = true
		d.mu.Unlock()
	}
}

func (d *ttyErrorDetector) tripped() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hit
}

// Run executes the AI CLI tool with profile isolation.
func (r *Runner) Run(ctx context.Context, opts RunOptions) error {
	// Lock profile if not disabled
	if !opts.NoLock {
		// Use LockWithCleanup to handle stale locks from dead processes
		if err := opts.Profile.LockWithCleanup(); err != nil {
			return fmt.Errorf("lock profile: %w", err)
		}
		defer opts.Profile.Unlock()
	}

	// Get provider environment
	var providerEnv map[string]string
	var err error
	if !opts.UseGlobalEnv {
		providerEnv, err = opts.Provider.Env(ctx, opts.Profile)
		if err != nil {
			return fmt.Errorf("get provider env: %w", err)
		}

		// Refresh passthrough symlinks (HOME dotfiles + XDG config/data/state
		// entries) on every isolated run. Profiles created before the XDG
		// passthrough existed — or before a new tool was installed in the real
		// home — get healed here instead of requiring re-creation (issue #69).
		// Best-effort: a failure to lay down convenience symlinks must not
		// block running the tool.
		if pseudoHome := providerEnv["HOME"]; pseudoHome != "" {
			if mgr, perr := passthrough.NewManager(); perr == nil && pseudoHome != mgr.RealHome() {
				if serr := mgr.SetupPassthroughs(pseudoHome); serr != nil {
					fmt.Fprintf(os.Stderr, "Warning: refresh passthroughs: %v\n", serr)
				}
				xdgConfig := providerEnv["XDG_CONFIG_HOME"]
				if xdgConfig == "" {
					xdgConfig = filepath.Join(pseudoHome, ".config")
				}
				if serr := mgr.SetupXDGPassthroughs(pseudoHome, xdgConfig); serr != nil {
					fmt.Fprintf(os.Stderr, "Warning: refresh xdg passthroughs: %v\n", serr)
				}
			}
		}
	}

	// Build command
	bin := opts.Provider.DefaultBin()
	cmd := exec.CommandContext(ctx, bin, opts.Args...)

	// Set up environment with deduplication (last one wins in our map logic)
	envMap := make(map[string]string)

	// 1. Start with inherited environment
	for _, e := range os.Environ() {
		parts := strings.SplitN(e, "=", 2)
		if len(parts) == 2 {
			envMap[parts[0]] = parts[1]
		}
	}

	// 2. Apply provider environment (overrides inherited)
	for k, v := range providerEnv {
		envMap[k] = v
	}

	// 3. Apply custom environment options (overrides provider)
	for k, v := range opts.Env {
		envMap[k] = v
	}

	// Reassemble into slice
	cmd.Env = make([]string, 0, len(envMap))
	for k, v := range envMap {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Set working directory
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	var capture *codexSessionCapture
	var stdoutObserver, stderrObserver *lineObserverWriter
	var rateObserver func(line string)
	if opts.Provider.ID() == "codex" {
		capture = &codexSessionCapture{}
	}

	if opts.OnRateLimit != nil {
		detector, err := ratelimit.NewDetector(ratelimit.ProviderFromString(opts.Provider.ID()), nil)
		if err != nil {
			return fmt.Errorf("create rate limit detector: %w", err)
		}

		var once sync.Once
		trigger := func() {
			once.Do(func() {
				invoke := func() {
					if err := opts.OnRateLimit(ctx); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: rate limit callback failed: %v\n", err)
					}
				}
				if opts.RateLimitDelay <= 0 {
					go invoke()
					return
				}
				go func() {
					timer := time.NewTimer(opts.RateLimitDelay)
					defer timer.Stop()
					select {
					case <-ctx.Done():
						return
					case <-timer.C:
						invoke()
					}
				}()
			})
		}

		rateObserver = func(line string) {
			if detector.Check(line) {
				trigger()
			}
		}
	}

	// Observers applied to both stdout and stderr.
	var sharedObservers []func(string)
	if capture != nil {
		sharedObservers = append(sharedObservers, capture.ObserveLine)
	}
	if rateObserver != nil {
		sharedObservers = append(sharedObservers, rateObserver)
	}

	// When stdin is not a terminal, also watch stderr for the "needs a terminal"
	// signature so callers can surface a precise non-interactive hint instead of
	// a misleading usage dump (issue #36). We only add this in the non-TTY case:
	// when stdin IS a terminal we leave stderr wiring exactly as before so normal
	// interactive sessions keep their inherited terminal stderr (some TUIs check
	// isatty(stderr) and a pipe would change their behavior).
	var ttyErr ttyErrorDetector
	stderrObservers := sharedObservers
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		stderrObservers = append(append([]func(string){}, sharedObservers...), ttyErr.observe)
	}

	fanout := func(obs []func(string)) func(string) {
		return func(line string) {
			for _, o := range obs {
				o(line)
			}
		}
	}

	if len(sharedObservers) > 0 {
		stdoutObserver = newLineObserverWriter(os.Stdout, fanout(sharedObservers))
	}
	if len(stderrObservers) > 0 {
		stderrObserver = newLineObserverWriter(os.Stderr, fanout(stderrObservers))
	}

	// Connect stdio
	cmd.Stdin = os.Stdin
	if stdoutObserver != nil {
		cmd.Stdout = stdoutObserver
	} else {
		cmd.Stdout = os.Stdout
	}
	if stderrObserver != nil {
		cmd.Stderr = stderrObserver
	} else {
		cmd.Stderr = os.Stderr
	}

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case sig := <-sigChan:
				if cmd.Process != nil {
					cmd.Process.Signal(sig)
				}
			case <-done:
				return
			}
		}
	}()
	defer close(done)
	defer signal.Stop(sigChan)

	// Run command
	runErr := cmd.Run()
	if stdoutObserver != nil {
		stdoutObserver.Flush()
	}
	if stderrObserver != nil {
		stderrObserver.Flush()
	}

	now := time.Now()
	opts.Profile.LastUsedAt = now
	if capture != nil {
		if sessionID := capture.ID(); sessionID != "" {
			opts.Profile.LastSessionID = sessionID
			opts.Profile.LastSessionTS = now.UTC()
		}
	}
	if err := opts.Profile.Save(); err != nil {
		// Don't hide the original process exit code, but do surface metadata issues
		// when the command otherwise succeeded.
		if runErr == nil {
			return fmt.Errorf("save profile metadata: %w", err)
		}
		fmt.Fprintf(os.Stderr, "Warning: failed to save profile metadata: %v\n", err)
	}

	if runErr != nil {
		if exitErr, ok := runErr.(*exec.ExitError); ok {
			// Propagate the actual exit code, plus whether the failure looked
			// like a missing-terminal abort so callers can give a useful hint.
			return &ExitCodeError{Code: exitErr.ExitCode(), NeedsTTY: ttyErr.tripped()}
		}
		return fmt.Errorf("run command: %w", runErr)
	}

	return nil
}

// RunInteractive runs an interactive session with the AI CLI.
func (r *Runner) RunInteractive(ctx context.Context, opts RunOptions) error {
	return r.Run(ctx, opts)
}

// LoginFlow runs the login flow for a profile.
func (r *Runner) LoginFlow(ctx context.Context, prov provider.Provider, prof *profile.Profile) error {
	return prov.Login(ctx, prof)
}

// Status checks the authentication status of a profile.
func (r *Runner) Status(ctx context.Context, prov provider.Provider, prof *profile.Profile) (*provider.ProfileStatus, error) {
	return prov.Status(ctx, prof)
}
