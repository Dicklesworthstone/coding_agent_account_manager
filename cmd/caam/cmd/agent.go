package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "auth-agent",
	Short: "Run the local auth agent for automated OAuth completion",
	Long: `Receive OAuth URLs from the remote coordinator and complete authentication
using browser automation.

The auth-agent runs on your local machine (e.g., Mac) where your browser has
existing Google account sessions. It:
1. Receives auth request URLs from the remote coordinator
2. Opens Chrome and navigates to the OAuth URL
3. Selects the appropriate Google account (using LRU strategy by default)
4. Extracts the challenge code
5. Sends the code back to the coordinator

The coordinator then injects this code into the waiting Claude Code session.

SSH Tunnel Setup (run on local Mac):
  ssh -R 7890:localhost:7891 user@remote-server -N

This forwards the coordinator's port 7890 to your local agent's port 7891.

Examples:
  # Start agent with default settings
  caam auth-agent

  # Specify coordinator URL and accounts
  caam auth-agent --coordinator http://localhost:7890 \
    --accounts alice@gmail.com,bob@gmail.com

  # Use specific Chrome profile
  caam auth-agent --chrome-profile ~/Library/Application\ Support/Google/Chrome/Default

  # Verbose logging
  caam auth-agent --verbose`,
	RunE: runAgent,
}

var (
	agentPort           int
	agentCoordinator    string
	agentAccounts       []string
	agentStrategy       string
	agentChromeProfile  string
	agentHeadless       bool
	agentVerbose        bool
)

func init() {
	rootCmd.AddCommand(agentCmd)

	agentCmd.Flags().IntVar(&agentPort, "port", 7891, "HTTP server port")
	agentCmd.Flags().StringVar(&agentCoordinator, "coordinator", "http://localhost:7890",
		"Coordinator URL (via SSH tunnel)")
	agentCmd.Flags().StringSliceVar(&agentAccounts, "accounts", nil,
		"Google account emails for rotation (comma-separated)")
	agentCmd.Flags().StringVar(&agentStrategy, "strategy", "lru",
		"Account selection strategy: lru, round_robin, random")
	agentCmd.Flags().StringVar(&agentChromeProfile, "chrome-profile", "",
		"Chrome user data directory (uses temp profile if empty)")
	agentCmd.Flags().BoolVar(&agentHeadless, "headless", false,
		"Run Chrome in headless mode (may not work with Google OAuth)")
	agentCmd.Flags().BoolVar(&agentVerbose, "verbose", false, "Verbose output")
}

func runAgent(cmd *cobra.Command, args []string) error {
	// Setup logger
	logLevel := slog.LevelInfo
	if agentVerbose {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	// Parse strategy
	var strategy agent.AccountStrategy
	switch agentStrategy {
	case "lru":
		strategy = agent.StrategyLRU
	case "round_robin":
		strategy = agent.StrategyRoundRobin
	case "random":
		strategy = agent.StrategyRandom
	default:
		return fmt.Errorf("unknown strategy: %s", agentStrategy)
	}

	// Create config
	config := agent.Config{
		Port:              agentPort,
		CoordinatorURL:    agentCoordinator,
		PollInterval:      2 * time.Second,
		ChromeUserDataDir: agentChromeProfile,
		Headless:          agentHeadless,
		AccountStrategy:   strategy,
		Accounts:          agentAccounts,
		Logger:            logger,
	}

	// Create agent
	ag := agent.New(config)

	// Set up callbacks
	ag.OnAuthStart = func(url, account string) {
		acc := account
		if acc == "" {
			acc = "(auto)"
		}
		fmt.Printf("[%s] Starting auth for %s\n",
			time.Now().Format("15:04:05"), acc)
	}

	ag.OnAuthComplete = func(account, code string) {
		fmt.Printf("[%s] Auth completed: %s (code: %s...)\n",
			time.Now().Format("15:04:05"),
			account,
			truncateCode(code))
	}

	ag.OnAuthFailed = func(account string, err error) {
		fmt.Printf("[%s] Auth FAILED for %s: %v\n",
			time.Now().Format("15:04:05"),
			account,
			err)
	}

	// Start agent
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	if err := ag.Start(ctx); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Printf("Auth agent started\n")
	fmt.Printf("  API: http://localhost:%d\n", agentPort)
	fmt.Printf("  Coordinator: %s\n", agentCoordinator)
	fmt.Printf("  Strategy: %s\n", agentStrategy)
	if len(agentAccounts) > 0 {
		fmt.Printf("  Accounts: %v\n", agentAccounts)
	}
	if agentChromeProfile != "" {
		fmt.Printf("  Chrome profile: %s\n", agentChromeProfile)
	}
	fmt.Println("\nWaiting for auth requests...")
	fmt.Println("Press Ctrl+C to stop.")

	// Wait for signal
	select {
	case <-sigCh:
		fmt.Println("\nShutting down...")
	case <-ctx.Done():
	}

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := ag.Stop(shutdownCtx); err != nil {
		logger.Warn("agent stop error", "error", err)
	}

	fmt.Println("Agent stopped.")
	return nil
}

func truncateCode(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[:4]
}

// testAuthCmd tests OAuth flow manually
var testAuthCmd = &cobra.Command{
	Use:   "test [oauth-url]",
	Short: "Test OAuth completion with a URL",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		url := args[0]

		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		}))

		browser := agent.NewBrowser(agent.BrowserConfig{
			UserDataDir: agentChromeProfile,
			Headless:    agentHeadless,
			Logger:      logger,
		})
		defer browser.Close()

		fmt.Println("Opening browser for OAuth...")
		code, account, err := browser.CompleteOAuth(cmd.Context(), url, "")
		if err != nil {
			return fmt.Errorf("OAuth failed: %w", err)
		}

		fmt.Printf("\nSuccess!\n")
		fmt.Printf("  Code: %s\n", code)
		fmt.Printf("  Account: %s\n", account)
		return nil
	},
}

func init() {
	agentCmd.AddCommand(testAuthCmd)
	testAuthCmd.Flags().StringVar(&agentChromeProfile, "chrome-profile", "",
		"Chrome user data directory")
	testAuthCmd.Flags().BoolVar(&agentHeadless, "headless", false,
		"Run Chrome in headless mode")
}
