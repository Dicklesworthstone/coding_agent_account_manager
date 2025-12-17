// Package exec handles running AI CLI tools with profile isolation.
package exec

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/user/caam/internal/profile"
	"github.com/user/caam/internal/provider"
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
}

// Run executes the AI CLI tool with profile isolation.
func (r *Runner) Run(ctx context.Context, opts RunOptions) error {
	// Lock profile if not disabled
	if !opts.NoLock {
		if err := opts.Profile.Lock(); err != nil {
			return fmt.Errorf("lock profile: %w", err)
		}
		defer opts.Profile.Unlock()
	}

	// Get provider environment
	providerEnv, err := opts.Provider.Env(ctx, opts.Profile)
	if err != nil {
		return fmt.Errorf("get provider env: %w", err)
	}

	// Build command
	bin := opts.Provider.DefaultBin()
	cmd := exec.CommandContext(ctx, bin, opts.Args...)

	// Set up environment
	cmd.Env = os.Environ()
	for k, v := range providerEnv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	for k, v := range opts.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Set working directory
	if opts.WorkDir != "" {
		cmd.Dir = opts.WorkDir
	}

	// Connect stdio
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Handle signals
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range sigChan {
			if cmd.Process != nil {
				cmd.Process.Signal(sig)
			}
		}
	}()
	defer signal.Stop(sigChan)

	// Run command
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// Return the actual exit code
			os.Exit(exitErr.ExitCode())
		}
		return fmt.Errorf("run command: %w", err)
	}

	// Update last used timestamp
	opts.Profile.UpdateLastUsed()

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
