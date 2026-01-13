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

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/coordinator"
	"github.com/spf13/cobra"
)

var coordinatorCmd = &cobra.Command{
	Use:   "auth-coordinator",
	Short: "Run the distributed auth recovery coordinator daemon",
	Long: `Monitor terminal panes for Claude Code rate limits and coordinate authentication.

The coordinator watches terminal panes for rate limit messages. When detected, it:
1. Auto-injects /login command
2. Selects Claude subscription login method
3. Extracts the OAuth URL
4. Exposes the URL via HTTP API for the local auth-agent
5. Receives auth codes from the agent and injects them
6. Resumes the session automatically

TERMINAL BACKENDS:
  WezTerm (PREFERRED) - Use WezTerm's native mux-server for best integration.
    Benefits: integrated multiplexing, domain awareness, rich metadata.

  tmux (FALLBACK) - For Ghostty, Alacritty, iTerm2, or other terminals.
    Requires: tmux server running (tmux new-session -d)
    Limitations: no domain awareness, extra process layer, less metadata.

This daemon should run on the remote machine where Claude Code sessions are running.
The local auth-agent connects to this coordinator to complete OAuth flows.

Examples:
  # Start coordinator (auto-detects best backend)
  caam auth-coordinator

  # Force WezTerm backend
  caam auth-coordinator --backend wezterm

  # Force tmux backend (for Ghostty/Alacritty/iTerm2)
  caam auth-coordinator --backend tmux

  # Custom port and verbose logging
  caam auth-coordinator --port 7891 --verbose

SSH Tunnel Setup (run on local Mac):
  ssh -R 7890:localhost:7891 user@remote-server -N`,
	RunE: runCoordinator,
}

var (
	coordinatorPort         int
	coordinatorPollMs       int
	coordinatorResumePrompt string
	coordinatorVerbose      bool
	coordinatorBackend      string
)

func init() {
	rootCmd.AddCommand(coordinatorCmd)

	coordinatorCmd.Flags().IntVar(&coordinatorPort, "port", 7890, "API server port")
	coordinatorCmd.Flags().IntVar(&coordinatorPollMs, "poll-interval", 500, "Pane poll interval in milliseconds")
	coordinatorCmd.Flags().StringVar(&coordinatorResumePrompt, "resume-prompt",
		"proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		"Text to inject after successful auth")
	coordinatorCmd.Flags().BoolVar(&coordinatorVerbose, "verbose", false, "Verbose output")
	coordinatorCmd.Flags().StringVar(&coordinatorBackend, "backend", "auto",
		"Terminal multiplexer backend: wezterm (preferred), tmux, or auto")
}

func runCoordinator(cmd *cobra.Command, args []string) error {
	// Setup logger
	logLevel := slog.LevelInfo
	if coordinatorVerbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	// Parse backend flag
	var backend coordinator.Backend
	switch strings.ToLower(coordinatorBackend) {
	case "wezterm":
		backend = coordinator.BackendWezTerm
	case "tmux":
		backend = coordinator.BackendTmux
	case "auto", "":
		backend = coordinator.BackendAuto
	default:
		return fmt.Errorf("invalid backend %q: use wezterm, tmux, or auto", coordinatorBackend)
	}

	// Create coordinator config
	config := coordinator.DefaultConfig()
	config.Backend = backend
	config.PollInterval = time.Duration(coordinatorPollMs) * time.Millisecond
	config.ResumePrompt = coordinatorResumePrompt
	config.Logger = logger

	// Create coordinator
	coord := coordinator.New(config)

	// Set up callbacks
	coord.OnAuthRequest = func(req *coordinator.AuthRequest) {
		fmt.Printf("[%s] AUTH NEEDED pane=%d url=%s\n",
			time.Now().Format("15:04:05"),
			req.PaneID,
			truncateURL(req.URL))
	}

	coord.OnAuthComplete = func(paneID int, account string) {
		fmt.Printf("[%s] AUTH COMPLETE pane=%d account=%s\n",
			time.Now().Format("15:04:05"),
			paneID,
			account)
	}

	coord.OnAuthFailed = func(paneID int, err error) {
		fmt.Printf("[%s] AUTH FAILED pane=%d error=%s\n",
			time.Now().Format("15:04:05"),
			paneID,
			err)
	}

	// Create API server
	api := coordinator.NewAPIServer(coord, coordinatorPort, logger)

	// Start coordinator
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	if err := coord.Start(ctx); err != nil {
		return fmt.Errorf("start coordinator: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start API server in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- api.Start()
	}()

	fmt.Printf("Auth coordinator started\n")
	fmt.Printf("  Backend: %s\n", coord.Backend())
	fmt.Printf("  API: http://localhost:%d\n", coordinatorPort)
	fmt.Printf("  Poll interval: %dms\n", coordinatorPollMs)
	if coord.Backend() == "tmux" {
		fmt.Println("\nNote: Using tmux fallback. WezTerm is recommended for better integration.")
	}
	fmt.Println("\nWaiting for rate limits...")
	fmt.Println("Press Ctrl+C to stop.")

	// Wait for signal or error
	select {
	case <-sigCh:
		fmt.Println("\nShutting down...")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("API server error: %w", err)
		}
	case <-ctx.Done():
	}

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := api.Shutdown(shutdownCtx); err != nil {
		logger.Warn("API shutdown error", "error", err)
	}

	if err := coord.Stop(); err != nil {
		logger.Warn("coordinator stop error", "error", err)
	}

	fmt.Println("Coordinator stopped.")
	return nil
}

func truncateURL(url string) string {
	if len(url) > 80 {
		return url[:77] + "..."
	}
	return url
}

// statusCmd shows coordinator status
var coordinatorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show auth-coordinator status",
	RunE: func(cmd *cobra.Command, args []string) error {
		// This would make an HTTP request to the coordinator
		// For now, just show how to check
		fmt.Println("To check coordinator status:")
		fmt.Printf("  curl http://localhost:%d/status\n", coordinatorPort)
		fmt.Println("\nTo see pending auth requests:")
		fmt.Printf("  curl http://localhost:%d/auth/pending\n", coordinatorPort)
		return nil
	},
}

func init() {
	coordinatorCmd.AddCommand(coordinatorStatusCmd)
}

// filterClaudePanes returns true for panes likely running Claude Code.
func filterClaudePanes(pane coordinator.Pane) bool {
	title := strings.ToLower(pane.Title)
	return strings.Contains(title, "claude") ||
		strings.Contains(title, "cc") ||
		strings.Contains(title, "anthropic")
}
