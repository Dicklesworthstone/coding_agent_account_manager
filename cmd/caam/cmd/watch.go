package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/discovery"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/spf13/cobra"
)

var watchCmd = newWatchCmd()

type watchOptions struct {
	once               bool
	providers          []string
	verbose            bool
	debounceInterval   time.Duration
	pollInterval       time.Duration
	watchOptionalFiles bool
}

func newWatchCmd() *cobra.Command {
	var opts watchOptions
	cmd := &cobra.Command{
		Use:   "watch",
		Short: "Watch for auth file changes and auto-discover accounts",
		Long: `Monitor auth file locations for changes and automatically save accounts.

When you log into a supported AI coding CLI in the normal way, caam watch detects
the credential changes and saves them to the vault. New accounts use an account
label when available, or a generated profile name when identity is unavailable.

This eliminates the need to manually run 'caam backup' after each login.
Filesystem notifications are backed by periodic checks so logins are also captured
when notifications are unavailable or the CLI replaces its credential directory.

Examples:
  # One-time scan of current auth files
  caam watch --once

  # Run as foreground daemon
  caam watch

  # Watch only Claude auth files
  caam watch --provider claude

  # Check credentials every second and include optional settings changes
  caam watch --poll-interval 1s --watch-optional

  # Watch with verbose logging
  caam watch --verbose`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWatch(cmd, opts)
		},
	}
	cmd.Flags().BoolVar(&opts.once, "once", false, "Scan once and exit (no daemon)")
	cmd.Flags().StringSliceVar(&opts.providers, "provider", nil, "Providers to watch (default: claude,codex,gemini,agy,grok,opencode,cursor)")
	cmd.Flags().BoolVar(&opts.verbose, "verbose", false, "Verbose output")
	cmd.Flags().DurationVar(&opts.debounceInterval, "debounce", 500*time.Millisecond, "Wait for credential writes to settle before saving")
	cmd.Flags().DurationVar(&opts.pollInterval, "poll-interval", 2*time.Second, "Interval between periodic credential checks")
	cmd.Flags().BoolVar(&opts.watchOptionalFiles, "watch-optional", false, "Also react to changes in optional settings files")
	return cmd
}

func init() {
	rootCmd.AddCommand(watchCmd)
}

func runWatch(cmd *cobra.Command, opts watchOptions) error {
	if opts.debounceInterval <= 0 {
		return fmt.Errorf("--debounce must be greater than zero")
	}
	if opts.pollInterval <= 0 {
		return fmt.Errorf("--poll-interval must be greater than zero")
	}
	providers, err := watchProviderNames(opts.providers)
	if err != nil {
		return err
	}
	if err := cmd.Context().Err(); err != nil {
		return err
	}

	// Callbacks and lifecycle messages may write concurrently. Use one lock for
	// both command streams, which callers can direct to the same writer.
	var outputMu sync.Mutex
	out := watchWriter{mu: &outputMu, writer: cmd.OutOrStdout()}
	errOut := watchWriter{mu: &outputMu, writer: cmd.ErrOrStderr()}
	logLevel := slog.LevelInfo
	if opts.verbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(errOut, &slog.HandlerOptions{
		Level: logLevel,
	}))

	if opts.once {
		return runWatchOnce(providers, out, logger)
	}

	return runWatchDaemon(cmd.Context(), providers, opts, out, errOut, logger)
}

func watchProviderNames(requested []string) ([]string, error) {
	if len(requested) == 0 {
		requested = []string{"claude", "codex", "gemini", "agy", "grok", "opencode", "cursor"}
	}
	providers := make([]string, 0, len(requested))
	seen := make(map[string]bool, len(requested))
	for _, name := range requested {
		fileSet, ok := authfile.GetAuthFileSet(strings.TrimSpace(name))
		if !ok {
			return nil, fmt.Errorf("unknown provider: %s", name)
		}
		if !seen[fileSet.Tool] {
			providers = append(providers, fileSet.Tool)
			seen[fileSet.Tool] = true
		}
	}
	return providers, nil
}

func runWatchOnce(providers []string, out io.Writer, logger *slog.Logger) error {
	fmt.Fprintln(out, "Scanning current auth files...")

	discovered, err := discovery.WatchOnce(vault, providers, logger)
	if len(discovered) > 0 {
		fmt.Fprintf(out, "\nSaved %d account(s):\n", len(discovered))
		for _, d := range discovered {
			fmt.Fprintf(out, "  + %s\n", d)
		}
		fmt.Fprintln(out, "\nProfiles saved to vault. Use 'caam activate <tool> <profile>' to switch.")
	}
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	if len(discovered) == 0 {
		fmt.Fprintln(out, "No new accounts discovered.")
		fmt.Fprintln(out, "\nTo see existing profiles: caam ls")
	}
	return nil
}

func runWatchDaemon(ctx context.Context, providers []string, opts watchOptions, out, errOut io.Writer, logger *slog.Logger) error {
	ctx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	watcher, err := discovery.NewWatcher(vault, discovery.WatcherConfig{
		Providers:          providers,
		DebounceInterval:   opts.debounceInterval,
		PollInterval:       opts.pollInterval,
		WatchOptionalFiles: opts.watchOptionalFiles,
		Logger:             logger,
		OnDiscovery: func(provider, email string, ident *identity.Identity) {
			planInfo := ""
			if ident != nil && ident.PlanType != "" {
				planInfo = fmt.Sprintf(" (%s)", ident.PlanType)
			}
			fmt.Fprintf(out, "[%s] Saved: %s/%s%s\n",
				timeNow(), provider, email, planInfo)
		},
		OnChange: func(provider, path string) {
			if opts.verbose {
				fmt.Fprintf(out, "[%s] Auth file changed: %s (%s)\n",
					timeNow(), path, provider)
			}
		},
		OnError: func(err error) {
			fmt.Fprintf(errOut, "[%s] Error: %v\n", timeNow(), err)
		},
	})
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	// A malformed provider must not hide successful captures or prevent watching
	// for the next complete login that can repair its credentials.
	fmt.Fprintln(out, "Scanning current auth files...")
	discovered, err := discovery.WatchOnce(vault, providers, logger)
	if len(discovered) > 0 {
		fmt.Fprintf(out, "Initial scan saved %d account(s):\n", len(discovered))
		for _, d := range discovered {
			fmt.Fprintf(out, "  + %s\n", d)
		}
	}
	if err != nil {
		logger.Warn("initial scan incomplete; continuing to watch for credential changes", "error", err)
	}

	if err := watcher.Start(ctx); err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}

	fmt.Fprintf(out, "Watching providers: %s (periodic checks every %s)\n", strings.Join(providers, ", "), opts.pollInterval)
	fmt.Fprintln(out, "Press Ctrl+C to stop.")

	var runErr error
	select {
	case <-ctx.Done():
	case <-watcher.Done():
		if ctx.Err() == nil {
			runErr = fmt.Errorf("auth file watcher stopped unexpectedly")
		}
	}

	if err := watcher.Stop(); err != nil {
		return fmt.Errorf("stop watcher: %w", err)
	}

	fmt.Fprintln(out, "Watcher stopped.")
	return runErr
}

type watchWriter struct {
	mu     *sync.Mutex
	writer io.Writer
}

func (w watchWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(p)
}

func timeNow() string {
	return time.Now().Format("15:04:05")
}
