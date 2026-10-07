// Package wrap provides the core wrapper logic for transparent account switching.
//
// It wraps AI CLI tool execution with rate limit detection and automatic
// profile switching, enabling seamless account rotation when limits are hit.
package wrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
)

// ExecCommand allows mocking exec.CommandContext in tests
var ExecCommand = exec.CommandContext

// Config holds configuration for the wrapper.
type Config struct {
	// Provider is the AI CLI provider (claude, codex, gemini).
	Provider string

	// InitialProfile keeps the caller's current account for the first attempt.
	// An empty value selects an initial account using Selector or Algorithm.
	InitialProfile string

	// Selector carries the caller's rotation policy and usage information.
	Selector *rotation.Selector

	// SwitchOptions are the caller's resolved activation safety settings.
	// If nil, the standalone wrapper loads the existing SPM safety settings.
	SwitchOptions *authfile.SwitchOptions

	// Bin overrides the native executable (for example, cursor-agent).
	Bin string

	// Args are the arguments to pass to the CLI.
	Args []string

	// WorkDir is the working directory for the command.
	WorkDir string

	// MaxRetries is the maximum number of retry attempts on rate limit.
	// Set to 0 for no retries, 1 for one retry, etc.
	MaxRetries int

	// InitialDelay is the delay before the first retry.
	// Default: 30s
	InitialDelay time.Duration

	// MaxDelay is the maximum delay between retries.
	// Default: 5m
	MaxDelay time.Duration

	// BackoffMultiplier is the factor by which delay increases after each retry.
	// Default: 2.0
	BackoffMultiplier float64

	// Jitter adds randomization to delays to prevent thundering herd.
	// When true, delays vary by ±20%. Default: true
	Jitter bool

	// CooldownDuration is how long to set cooldown after a rate limit.
	CooldownDuration time.Duration

	// NotifyOnSwitch controls whether to print a message when switching profiles.
	NotifyOnSwitch bool

	// CustomPatterns are custom rate limit detection patterns.
	// If empty, default patterns for the provider are used.
	CustomPatterns []string

	// Algorithm is the rotation algorithm to use (smart, round_robin, random).
	Algorithm rotation.Algorithm

	// Stdin is passed to the child. Defaults to os.Stdin.
	Stdin io.Reader

	// ReplayStdin privately spools finite redirected input before activation,
	// then replays exactly those bytes on each retry. Input is limited to 16 MiB.
	// Without replay, consumed input prevents a retry; an untracked *os.File
	// also prevents retry because its consumption cannot be proven safe.
	ReplayStdin bool

	// Stdout is where to write stdout. Defaults to os.Stdout.
	Stdout io.Writer

	// Stderr is where to write stderr. Defaults to os.Stderr.
	Stderr io.Writer
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return ConfigFromGlobal(config.DefaultConfig(), "")
}

// ConfigFromGlobal creates a wrap.Config using settings from the global config.
// Provider-specific overrides are applied if available.
func ConfigFromGlobal(cfg *config.Config, provider string) Config {
	wrapCfg := cfg.Wrap.ForProvider(provider)
	return Config{
		Provider:          provider,
		MaxRetries:        wrapCfg.MaxRetries,
		InitialDelay:      wrapCfg.InitialDelay.Duration(),
		MaxDelay:          wrapCfg.MaxDelay.Duration(),
		BackoffMultiplier: wrapCfg.BackoffMultiplier,
		Jitter:            wrapCfg.Jitter,
		CooldownDuration:  wrapCfg.CooldownDuration.Duration(),
		NotifyOnSwitch:    true,
		Algorithm:         rotation.AlgorithmSmart,
		Stdin:             os.Stdin,
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
	}
}

func (c *Config) retryConfig() config.WrapConfig {
	return config.WrapConfig{
		MaxRetries:        c.MaxRetries,
		InitialDelay:      config.Duration(c.InitialDelay),
		MaxDelay:          config.Duration(c.MaxDelay),
		BackoffMultiplier: c.BackoffMultiplier,
		Jitter:            c.Jitter,
		CooldownDuration:  config.Duration(c.CooldownDuration),
	}
}

// NextDelay shares the validated policy used by configuration and handoffs.
func (c *Config) NextDelay(attempt int) time.Duration {
	policy := c.retryConfig()
	return policy.NextDelay(attempt)
}

// ShouldRetry returns true if another retry should be attempted.
func (c *Config) ShouldRetry(attempt int) bool {
	return attempt < c.MaxRetries
}

// Result is the outcome of a wrapped execution.
type Result struct {
	// ExitCode is the exit code of the last process run.
	ExitCode int

	// ProfilesUsed is the list of profiles whose processes started, in order.
	ProfilesUsed []string

	// RateLimitHit is true if a rate limit was detected.
	RateLimitHit bool

	// RetryCount is how many retries were attempted.
	RetryCount int

	// Err is any error that occurred during execution.
	Err error

	// StartTime is when the wrap session started.
	StartTime time.Time

	// Duration is how long the wrap session ran.
	Duration time.Duration
}

// Wrapper orchestrates wrapped execution of AI CLI tools.
type Wrapper struct {
	vault       *authfile.Vault
	db          *caamdb.DB
	healthStore *health.Storage
	config      Config
}

// NewWrapper creates a new wrapper with the given dependencies.
func NewWrapper(vault *authfile.Vault, db *caamdb.DB, healthStore *health.Storage, config Config) *Wrapper {
	// Apply defaults for nil outputs
	if config.Stdout == nil {
		config.Stdout = os.Stdout
	}
	if config.Stderr == nil {
		config.Stderr = os.Stderr
	}
	if config.Stdin == nil {
		config.Stdin = os.Stdin
	}

	return &Wrapper{
		vault:       vault,
		db:          db,
		healthStore: healthStore,
		config:      config,
	}
}

// Run executes the wrapped command with automatic rate limit handling.
func (w *Wrapper) Run(ctx context.Context) *Result {
	result := &Result{
		StartTime: time.Now(),
		ExitCode:  1,
	}

	// Defer recording of the session
	defer func() {
		result.Duration = time.Since(result.StartTime)
		w.recordSession(result)
	}()

	if err := ctx.Err(); err != nil {
		result.Err = err
		return result
	}
	if err := w.config.retryConfig().Validate(); err != nil {
		result.Err = fmt.Errorf("invalid retry configuration: %w", err)
		return result
	}
	if w.vault == nil {
		result.Err = fmt.Errorf("vault is required")
		return result
	}
	fileSet, ok := AuthFileSetForProvider(w.config.Provider)
	if !ok {
		result.Err = fmt.Errorf("unknown provider: %s", w.config.Provider)
		return result
	}
	detector, err := ratelimit.NewDetector(ratelimit.ProviderFromString(w.config.Provider), w.config.CustomPatterns)
	if err != nil {
		result.Err = fmt.Errorf("create detector: %w", err)
		return result
	}
	var switchOptions authfile.SwitchOptions
	if w.config.SwitchOptions != nil {
		switchOptions = *w.config.SwitchOptions
	} else {
		spmConfig, err := config.LoadSPMConfig()
		if err != nil {
			result.Err = fmt.Errorf("load activation safety settings: %w", err)
			return result
		}
		switchOptions = authfile.SwitchOptions{
			BackupMode:     spmConfig.Safety.AutoBackupBeforeSwitch,
			MaxAutoBackups: spmConfig.Safety.MaxAutoBackups,
		}
	}

	profiles, err := w.vault.List(w.config.Provider)
	if err != nil {
		result.Err = fmt.Errorf("list profiles: %w", err)
		result.ExitCode = 1
		return result
	}

	if len(profiles) == 0 {
		result.Err = fmt.Errorf("no profiles available for %s", w.config.Provider)
		result.ExitCode = 1
		return result
	}

	selector := w.config.Selector
	if selector == nil {
		selector = rotation.NewSelector(w.config.Algorithm, w.healthStore, w.db)
	}
	selector.SetVaultPath(w.vault.BasePath())

	currentProfile := w.config.InitialProfile
	if currentProfile != "" {
		// Apply the same credential and cooldown eligibility checks without
		// silently replacing the caller's chosen initial account.
		profiles = []string{currentProfile}
	}
	selection, err := selector.Select(w.config.Provider, profiles, "")
	if err != nil {
		result.Err = fmt.Errorf("select profile: %w", err)
		result.ExitCode = 1
		return result
	}
	if selection == nil || selection.Selected == "" {
		result.Err = fmt.Errorf("no profile selected for %s", w.config.Provider)
		return result
	}
	currentProfile = selection.Selected

	input, err := prepareInput(ctx, w.config.Stdin, w.config.ReplayStdin)
	if err != nil {
		result.Err = fmt.Errorf("prepare stdin: %w", err)
		return result
	}
	defer input.Close()
	exhausted := make(map[string]bool)
	for {
		if err := ctx.Err(); err != nil {
			result.Err = err
			return result
		}
		// A token can expire while waiting; eligibility is checked again
		// immediately before Switch, which validates the actual file bytes.
		if _, err := selector.Select(w.config.Provider, []string{currentProfile}, ""); err != nil {
			result.Err = fmt.Errorf("profile is no longer launchable: %w", err)
			return result
		}
		if err := input.Rewind(); err != nil {
			result.Err = fmt.Errorf("rewind stdin: %w", err)
			return result
		}
		if w.config.NotifyOnSwitch {
			fmt.Fprintf(w.config.Stderr, "Using profile '%s'...\n", currentProfile)
		}
		attempt := w.runOnce(ctx, currentProfile, input.reader, fileSet, switchOptions, detector)
		if attempt.started {
			if len(result.ProfilesUsed) > 0 {
				result.RetryCount++
			}
			result.ProfilesUsed = append(result.ProfilesUsed, currentProfile)
		}
		result.ExitCode = attempt.exitCode
		if attempt.err != nil {
			result.Err = attempt.err
			return result
		}
		if !attempt.rateLimitHit {
			return result // Success and unrelated failures are never replayed.
		}
		result.RateLimitHit = true
		exhausted[currentProfile] = true
		if w.db != nil && w.config.CooldownDuration > 0 {
			cooldown := ratelimit.RetryDelay(w.config.CooldownDuration, attempt.retryAfter, time.Now())
			if _, err := w.db.SetCooldown(w.config.Provider, currentProfile, time.Now(), cooldown, "auto-detected via caam run"); err != nil {
				fmt.Fprintf(w.config.Stderr, "Warning: failed to record cooldown: %v\n", err)
			}
		}
		if !w.config.ShouldRetry(result.RetryCount) {
			return result
		}
		if !input.CanRetry() {
			result.Err = fmt.Errorf("cannot safely retry consumed or untracked stdin; redirect input from a file (< file) to enable replay")
			return result
		}
		profiles, err = w.vault.List(w.config.Provider)
		if err != nil {
			result.Err = fmt.Errorf("list backup profiles: %w", err)
			return result
		}
		candidates := make([]string, 0, len(profiles))
		for _, name := range profiles {
			if !exhausted[name] && !authfile.IsSystemProfile(name) {
				candidates = append(candidates, name)
			}
		}
		if len(candidates) == 0 {
			return result // Preserve the last child status after exhausting accounts.
		}
		selection, err = selector.Select(w.config.Provider, candidates, currentProfile)
		if err != nil {
			result.Err = fmt.Errorf("select unused backup profile: %w", err)
			return result
		}
		if selection == nil || selection.Selected == "" || exhausted[selection.Selected] {
			result.Err = fmt.Errorf("no unused backup profile selected for %s", w.config.Provider)
			return result
		}
		delay := ratelimit.RetryDelay(w.config.NextDelay(result.RetryCount), attempt.retryAfter, time.Now())
		if w.config.NotifyOnSwitch {
			fmt.Fprintf(w.config.Stderr, "Rate limit reached; waiting %v before retry with '%s'...\n", delay.Round(time.Millisecond), selection.Selected)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			result.Err = ctx.Err()
			return result
		case <-timer.C:
		}
		currentProfile = selection.Selected
	}
}

type attemptResult struct {
	exitCode     int
	started      bool
	rateLimitHit bool
	retryAfter   time.Time
	err          error
}

func (w *Wrapper) runOnce(ctx context.Context, profile string, stdin io.Reader, fileSet authfile.AuthFileSet, options authfile.SwitchOptions, detector *ratelimit.Detector) attemptResult {
	attempt := attemptResult{exitCode: 1}
	if err := ctx.Err(); err != nil {
		attempt.err = err
		return attempt
	}
	result, err := w.vault.Switch(fileSet, profile, options)
	if err != nil {
		attempt.err = fmt.Errorf("activate profile %s: %w", profile, err)
		return attempt
	}
	for _, warning := range result.Warnings {
		fmt.Fprintf(w.config.Stderr, "Warning: %s\n", warning)
	}

	detector.Reset()
	bin := w.config.Bin
	if bin == "" {
		bin = binForProvider(w.config.Provider)
	}
	cmd := ExecCommand(ctx, bin, w.config.Args...)

	if w.config.WorkDir != "" {
		cmd.Dir = w.config.WorkDir
	}
	// Vault activation selects credentials in the native home. Ambient API
	// keys must not override that account on any attempt. Retain unrelated
	// caller environment, including explicit command environment additions.
	cmd.Env = provider.MergeEnvironment(cmd.Environ(), provider.CredentialEnvironment(w.config.Provider, provider.AuthModeOAuth, nil), nil)

	cmd.Stdin = stdin
	// Bound cleanup when a child leaves inherited pipes open after exiting
	// or cancellation. The caller owns signal handling through ctx.
	cmd.WaitDelay = time.Second

	outputMu := &sync.Mutex{}
	stdoutTee := &teeWriter{
		dest:     w.config.Stdout,
		detector: detector,
		outputMu: outputMu,
	}
	stderrTee := &teeWriter{
		dest:     w.config.Stderr,
		detector: detector,
		outputMu: outputMu,
	}

	cmd.Stdout = stdoutTee
	cmd.Stderr = stderrTee

	if err := cmd.Start(); err != nil {
		attempt.err = fmt.Errorf("start %s: %w", bin, err)
		return attempt
	}
	attempt.started = true
	err = cmd.Wait()
	stdoutTee.Flush()
	stderrTee.Flush()
	if ctx.Err() != nil {
		attempt.err = ctx.Err()
		return attempt
	}
	if outputErr := errors.Join(stdoutTee.Error(), stderrTee.Error()); outputErr != nil {
		attempt.err = fmt.Errorf("forward process output: %w", outputErr)
		return attempt
	}
	if err == nil {
		attempt.exitCode = 0 // Successful output mentioning limits is not a failure.
		return attempt
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		attempt.err = fmt.Errorf("wait for %s: %w", bin, err)
		return attempt
	}
	attempt.exitCode = exitErr.ExitCode()
	if status, ok := exitErr.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		attempt.exitCode = 128 + int(status.Signal())
	}
	if attempt.exitCode < 1 {
		attempt.exitCode = 1
	}
	attempt.rateLimitHit = detector.Detected()
	attempt.retryAfter = detector.RetryAfter()
	return attempt
}

const maxReplayInputBytes = 16 << 20

type preparedInput struct {
	reader  io.Reader
	spool   *os.File
	tracked *countingInput
	opaque  bool
}

func prepareInput(ctx context.Context, source io.Reader, replay bool) (*preparedInput, error) {
	if !replay {
		if _, ok := source.(*os.File); ok {
			return &preparedInput{reader: source, opaque: true}, nil
		}
		tracked := &countingInput{reader: source}
		return &preparedInput{reader: tracked, tracked: tracked}, nil
	}
	file, err := os.CreateTemp("", "caam-run-stdin-*")
	if err != nil {
		return nil, err
	}
	input := &preparedInput{reader: file, spool: file}
	if closer, ok := source.(io.Closer); ok {
		closed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			_ = closer.Close() // Only cancellation closes the caller's source.
			close(closed)
		})
		defer func() {
			if !stop() {
				<-closed
			}
		}()
	}
	n, err := io.Copy(file, io.LimitReader(contextInput{ctx: ctx, reader: source}, maxReplayInputBytes+1))
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && n > maxReplayInputBytes {
		err = fmt.Errorf("stdin exceeds the 16 MiB replay limit")
	}
	if err != nil {
		input.Close()
		return nil, err
	}
	return input, nil
}

func (input *preparedInput) Rewind() error {
	if input.spool == nil {
		return nil
	}
	_, err := input.spool.Seek(0, io.SeekStart)
	return err
}

func (input *preparedInput) CanRetry() bool {
	return input.spool != nil || (!input.opaque && input.tracked.consumed.Load() == 0)
}

func (input *preparedInput) Close() {
	if input.spool != nil {
		_ = input.spool.Close()
		_ = os.Remove(input.spool.Name())
	}
}

type countingInput struct {
	reader   io.Reader
	consumed atomic.Int64
}

func (input *countingInput) Read(p []byte) (int, error) {
	n, err := input.reader.Read(p)
	input.consumed.Add(int64(n))
	return n, err
}

type contextInput struct {
	ctx    context.Context
	reader io.Reader
}

func (input contextInput) Read(p []byte) (int, error) {
	if err := input.ctx.Err(); err != nil {
		return 0, err
	}
	return input.reader.Read(p)
}

// maxBufferSize is the maximum buffer size before forcing a flush (64KB).
// This prevents unbounded memory growth when output contains no newlines.
const maxBufferSize = 64 * 1024

// teeWriter writes to a destination while also checking for rate limits.
// It buffers data to ensure rate limit patterns aren't missed when split
// across multiple Write calls (e.g., "rate li" then "mit exceeded").
type teeWriter struct {
	mu       sync.Mutex
	dest     io.Writer
	detector *ratelimit.Detector
	buffer   []byte
	outputMu *sync.Mutex // Shared when stdout and stderr have the same destination.
	writeErr error
}

func (t *teeWriter) Write(p []byte) (n int, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Append to buffer for line-based pattern matching
	t.buffer = append(t.buffer, p...)

	// Process complete lines
	for {
		idx := bytes.IndexByte(t.buffer, '\n')
		if idx == -1 {
			break
		}

		// Extract and check complete line
		line := string(t.buffer[:idx])
		t.buffer = t.buffer[idx+1:]
		t.detector.Check(line)
	}

	// Enforce buffer limit to prevent OOM on long lines without newlines
	if len(t.buffer) > maxBufferSize {
		t.detector.Check(string(t.buffer))
		t.buffer = nil
	}

	if t.outputMu != nil {
		t.outputMu.Lock()
		defer t.outputMu.Unlock()
	}
	n, err = t.dest.Write(p)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	if err != nil && t.writeErr == nil {
		t.writeErr = err
	}
	return n, err
}

func (t *teeWriter) Error() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.writeErr
}

// Flush checks any remaining buffered data for rate limit patterns.
// Call this after the command completes.
func (t *teeWriter) Flush() {
	t.mu.Lock()
	defer t.mu.Unlock()

	if len(t.buffer) > 0 {
		t.detector.Check(string(t.buffer))
		t.buffer = nil
	}
}

// AuthFileSetForProvider allows mocking auth file set lookup in tests
var AuthFileSetForProvider = func(provider string) (authfile.AuthFileSet, bool) {
	return authfile.GetAuthFileSet(provider)
}

// binForProvider returns the binary name for a provider.
func binForProvider(provider string) string {
	switch provider {
	case "codex":
		return "codex"
	case "claude":
		return "claude"
	case "gemini":
		return "gemini"
	case "opencode":
		return "opencode"
	case "cursor":
		return "cursor"
	default:
		return provider
	}
}

// recordSession records a wrap session to the database for cost tracking.
func (w *Wrapper) recordSession(result *Result) {
	if w.db == nil {
		return
	}

	// Determine the primary profile used (last one in the list)
	profileName := ""
	if len(result.ProfilesUsed) > 0 {
		profileName = result.ProfilesUsed[len(result.ProfilesUsed)-1]
	}

	if profileName == "" {
		return
	}

	session := caamdb.WrapSession{
		Provider:     w.config.Provider,
		ProfileName:  profileName,
		StartedAt:    result.StartTime,
		EndedAt:      result.StartTime.Add(result.Duration),
		ExitCode:     result.ExitCode,
		RateLimitHit: result.RateLimitHit,
	}

	// Notes can include retry count or error info
	if result.RetryCount > 0 {
		session.Notes = fmt.Sprintf("retries: %d", result.RetryCount)
	}

	// Best effort - log error if recording fails
	if err := w.db.RecordWrapSession(session); err != nil {
		fmt.Fprintf(w.config.Stderr, "Warning: failed to record session stats: %v\n", err)
	}
}
