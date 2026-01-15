// Package wrap provides the core wrapper logic for transparent account switching.
//
// It wraps AI CLI tool execution with rate limit detection and automatic
// profile switching, enabling seamless account rotation when limits are hit.
package wrap

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/ratelimit"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
)

// ExecCommand allows mocking exec.CommandContext in tests
var ExecCommand = exec.CommandContext

// Config holds configuration for the wrapper.
type Config struct {
	// Provider is the AI CLI provider (claude, codex, gemini).
	Provider string

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

	// Stdout is where to write stdout. Defaults to os.Stdout.
	Stdout io.Writer

	// Stderr is where to write stderr. Defaults to os.Stderr.
	Stderr io.Writer
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		MaxRetries:        3,
		InitialDelay:      30 * time.Second,
		MaxDelay:          5 * time.Minute,
		BackoffMultiplier: 2.0,
		Jitter:            true,
		CooldownDuration:  60 * time.Minute,
		NotifyOnSwitch:    true,
		Algorithm:         rotation.AlgorithmSmart,
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
	}
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
		Stdout:            os.Stdout,
		Stderr:            os.Stderr,
	}
}

// NextDelay calculates the delay before the next retry attempt using exponential backoff.
// Formula: delay = min(initial * multiplier^attempt, max)
// With jitter: delay *= (0.8 + random*0.4) for ±20% variation
func (c *Config) NextDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}

	// Calculate base delay with exponential backoff
	initial := float64(c.InitialDelay)
	multiplier := c.BackoffMultiplier
	if multiplier <= 0 {
		multiplier = 2.0
	}

	delay := initial * math.Pow(multiplier, float64(attempt))

	// Cap at max delay
	maxDelay := float64(c.MaxDelay)
	if delay > maxDelay {
		delay = maxDelay
	}

	// Apply jitter if enabled (±20%)
	if c.Jitter {
		jitterFactor := 0.8 + rand.Float64()*0.4
		delay *= jitterFactor
	}

	return time.Duration(delay)
}

// ShouldRetry returns true if another retry should be attempted.
func (c *Config) ShouldRetry(attempt int) bool {
	return attempt < c.MaxRetries
}

// Result is the outcome of a wrapped execution.
type Result struct {
	// ExitCode is the exit code of the last process run.
	ExitCode int

	// ProfilesUsed is the list of profiles that were used, in order.
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
	}

	// Defer recording of the session
	defer func() {
		result.Duration = time.Since(result.StartTime)
		w.recordSession(result)
	}()

	// Get available profiles
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

	// Create selector
	selector := rotation.NewSelector(w.config.Algorithm, w.healthStore, w.db)

	// Select initial profile
	currentProfile := ""
	selection, err := selector.Select(w.config.Provider, profiles, currentProfile)
	if err != nil {
		result.Err = fmt.Errorf("select profile: %w", err)
		result.ExitCode = 1
		return result
	}

	currentProfile = selection.Selected

	// Run with retry loop
	for attempt := 0; attempt <= w.config.MaxRetries; attempt++ {
		result.ProfilesUsed = append(result.ProfilesUsed, currentProfile)

		if w.config.NotifyOnSwitch && attempt > 0 {
			fmt.Fprintf(w.config.Stderr, "⚠️  Switching to '%s'...\n", currentProfile)
		} else if w.config.NotifyOnSwitch && attempt == 0 {
			fmt.Fprintf(w.config.Stderr, "Using profile '%s'...\n", currentProfile)
		}

		// Run the command
		exitCode, rateLimitHit, runErr := w.runOnce(ctx, currentProfile)
		result.ExitCode = exitCode

		if runErr != nil && !rateLimitHit {
			result.Err = runErr
			return result
		}

		// Check if rate limit was hit
		if rateLimitHit {
			result.RateLimitHit = true
			result.RetryCount++

			// Record cooldown
			if w.db != nil {
				w.db.SetCooldown(
					w.config.Provider,
					currentProfile,
					time.Now(),
					w.config.CooldownDuration,
					"auto-detected via caam wrap",
				)
			}

			// Check if we can retry
			if attempt >= w.config.MaxRetries {
				if w.config.NotifyOnSwitch {
					fmt.Fprintf(w.config.Stderr, "⚠️  Rate limit hit. No more retries available.\n")
				}
				return result
			}

			// Try to select a new profile
			selection, err = selector.Select(w.config.Provider, profiles, currentProfile)
			if err != nil {
				if w.config.NotifyOnSwitch {
					fmt.Fprintf(w.config.Stderr, "⚠️  Rate limit hit. %v\n", err)
				}
				return result
			}

			currentProfile = selection.Selected

			// Calculate and apply backoff delay before retry
			delay := w.config.NextDelay(attempt)
			if w.config.NotifyOnSwitch {
				fmt.Fprintf(w.config.Stderr, "⏳ Waiting %v before retry...\n", delay.Round(time.Second))
			}

			// Wait with context cancellation support
			select {
			case <-ctx.Done():
				result.Err = ctx.Err()
				return result
			case <-time.After(delay):
				// Continue to retry
			}
			continue
		}

		// Success or non-rate-limit error
		return result
	}

	return result
}

// runOnce executes the command once with the given profile.
// Returns exit code, whether rate limit was hit, and any error.
func (w *Wrapper) runOnce(ctx context.Context, profile string) (int, bool, error) {
	// Get auth file set for this provider
	fileSet, ok := AuthFileSetForProvider(w.config.Provider)
	if !ok {
		return 1, false, fmt.Errorf("unknown provider: %s", w.config.Provider)
	}

	// Activate the profile (restore auth files)
	if err := w.vault.Restore(fileSet, profile); err != nil {
		return 1, false, fmt.Errorf("activate profile %s: %w", profile, err)
	}

	// Create rate limit detector
	detector, err := ratelimit.NewDetector(
		ratelimit.ProviderFromString(w.config.Provider),
		w.config.CustomPatterns,
	)
	if err != nil {
		return 1, false, fmt.Errorf("create detector: %w", err)
	}

	// Build command
	bin := binForProvider(w.config.Provider)
	cmd := ExecCommand(ctx, bin, w.config.Args...)

	if w.config.WorkDir != "" {
		cmd.Dir = w.config.WorkDir
	}

	// Set up I/O with rate limit detection
	cmd.Stdin = os.Stdin

	// Create tee writers that check for rate limits and forward output
	stdoutTee := &teeWriter{
		dest:     w.config.Stdout,
		detector: detector,
	}
	stderrTee := &teeWriter{
		dest:     w.config.Stderr,
		detector: detector,
	}

	cmd.Stdout = stdoutTee
	cmd.Stderr = stderrTee

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

	// Run the command
	err = cmd.Run()

	// Flush tee writers to ensure all buffered data is checked for patterns
	stdoutTee.Flush()
	stderrTee.Flush()

	// Check for rate limit detection
	rateLimitHit := detector.Detected()

	// Determine exit code
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = 1
		}
	}

	return exitCode, rateLimitHit, nil
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

	// Also check partial buffer in case a rate limit message doesn't end with newline
	// (e.g., JSON error response or final output)
	if len(t.buffer) > 0 {
		t.detector.Check(string(t.buffer))
	}

	// Enforce buffer limit to prevent OOM on long lines without newlines
	if len(t.buffer) > maxBufferSize {
		t.buffer = nil
	}

	// Forward to destination
	return t.dest.Write(p)
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
	switch provider {
	case "codex":
		return authfile.CodexAuthFiles(), true
	case "claude":
		return authfile.ClaudeAuthFiles(), true
	case "gemini":
		return authfile.GeminiAuthFiles(), true
	default:
		return authfile.AuthFileSet{}, false
	}
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
		fmt.Fprintf(w.config.Stderr, "warning: failed to record session stats: %v\n", err)
	}
}
