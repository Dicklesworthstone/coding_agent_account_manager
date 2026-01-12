package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/discovery"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/spf13/cobra"
)

var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "Watch for auth file changes and auto-discover accounts",
	Long: `Monitor auth file locations for changes and automatically save new accounts.

When you log into an AI coding CLI (Claude Code, Codex, Gemini) in the normal way,
caam watch detects the auth file changes and automatically creates a vault profile
with the account email as the profile name.

This eliminates the need to manually run 'caam backup' after each login.

Examples:
  # One-time scan of current auth files
  caam watch --once

  # Run as foreground daemon
  caam watch

  # Watch only Claude auth files
  caam watch --providers claude

  # Watch with verbose logging
  caam watch --verbose`,
	RunE: runWatch,
}

var (
	watchOnce      bool
	watchProviders []string
	watchVerbose   bool
)

func init() {
	rootCmd.AddCommand(watchCmd)
	watchCmd.Flags().BoolVar(&watchOnce, "once", false, "Scan once and exit (no daemon)")
	watchCmd.Flags().StringSliceVar(&watchProviders, "providers", nil, "Providers to watch (default: all)")
	watchCmd.Flags().BoolVar(&watchVerbose, "verbose", false, "Verbose output")
}

func runWatch(cmd *cobra.Command, args []string) error {
	// Setup logger
	logLevel := slog.LevelInfo
	if watchVerbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	// Default to all providers
	providers := watchProviders
	if len(providers) == 0 {
		providers = []string{"claude", "codex", "gemini"}
	}

	// Validate providers
	validProviders := map[string]bool{"claude": true, "codex": true, "gemini": true}
	for _, p := range providers {
		if !validProviders[strings.ToLower(p)] {
			return fmt.Errorf("unknown provider: %s", p)
		}
	}

	if watchOnce {
		return runWatchOnce(providers, logger)
	}

	return runWatchDaemon(cmd.Context(), providers, logger)
}

func runWatchOnce(providers []string, logger *slog.Logger) error {
	fmt.Println("Scanning current auth files...")

	discovered, err := discovery.WatchOnce(vault, providers, logger)
	if err != nil {
		return fmt.Errorf("scan failed: %w", err)
	}

	if len(discovered) == 0 {
		fmt.Println("No new accounts discovered.")
		fmt.Println("\nTo see existing profiles: caam ls")
		return nil
	}

	fmt.Printf("\nDiscovered %d account(s):\n", len(discovered))
	for _, d := range discovered {
		fmt.Printf("  + %s\n", d)
	}
	fmt.Println("\nProfiles saved to vault. Use 'caam activate <tool> <email>' to switch.")
	return nil
}

func runWatchDaemon(ctx context.Context, providers []string, logger *slog.Logger) error {
	fmt.Println("Starting auth file watcher...")
	fmt.Printf("Watching providers: %s\n", strings.Join(providers, ", "))
	fmt.Println("Press Ctrl+C to stop.")

	// First do a one-time scan
	discovered, err := discovery.WatchOnce(vault, providers, logger)
	if err != nil {
		logger.Warn("initial scan failed", "error", err)
	} else if len(discovered) > 0 {
		fmt.Printf("Initial scan discovered %d account(s):\n", len(discovered))
		for _, d := range discovered {
			fmt.Printf("  + %s\n", d)
		}
		fmt.Println()
	}

	// Create watcher
	watcher, err := discovery.NewWatcher(vault, discovery.WatcherConfig{
		Providers: providers,
		Logger:    logger,
		OnDiscovery: func(provider, email string, ident *identity.Identity) {
			planInfo := ""
			if ident != nil && ident.PlanType != "" {
				planInfo = fmt.Sprintf(" (%s)", ident.PlanType)
			}
			fmt.Printf("[%s] Discovered: %s/%s%s\n",
				timeNow(), provider, email, planInfo)
		},
		OnChange: func(provider, path string) {
			if watchVerbose {
				fmt.Printf("[%s] Auth file changed: %s (%s)\n",
					timeNow(), path, provider)
			}
		},
		OnError: func(err error) {
			fmt.Fprintf(os.Stderr, "[%s] Error: %v\n", timeNow(), err)
		},
	})
	if err != nil {
		return fmt.Errorf("create watcher: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start watcher
	if err := watcher.Start(ctx); err != nil {
		return fmt.Errorf("start watcher: %w", err)
	}

	fmt.Println("Watching for auth file changes...")

	// Wait for signal or context cancellation
	select {
	case <-ctx.Done():
	case sig := <-sigCh:
		fmt.Printf("\nReceived %s, stopping...\n", sig)
	}

	if err := watcher.Stop(); err != nil {
		logger.Warn("error stopping watcher", "error", err)
	}

	fmt.Println("Watcher stopped.")
	return nil
}

func timeNow() string {
	return time.Now().Format("15:04:05")
}
