package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
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
	agentPort             int
	agentCoordinator      string
	agentCoordinatorToken string
	agentAccounts         []string
	agentStrategy         string
	agentChromeProfile    string
	agentHeadless         bool
	agentVerbose          bool
	agentConfigPath       string
)

func init() {
	rootCmd.AddCommand(agentCmd)

	agentCmd.Flags().IntVar(&agentPort, "port", 7891, "HTTP server port")
	agentCmd.Flags().StringVar(&agentCoordinator, "coordinator", "http://localhost:7890",
		"Coordinator URL (via SSH tunnel)")
	agentCmd.Flags().StringVar(&agentCoordinatorToken, "coordinator-token", "",
		"Coordinator auth token (shared secret)")
	agentCmd.Flags().StringSliceVar(&agentAccounts, "accounts", nil,
		"Google account emails for rotation (comma-separated)")
	agentCmd.Flags().StringVar(&agentStrategy, "strategy", "lru",
		"Account selection strategy: lru, round_robin, random")
	agentCmd.Flags().StringVar(&agentChromeProfile, "chrome-profile", "",
		"Chrome user data directory (uses temp profile if empty)")
	agentCmd.Flags().BoolVar(&agentHeadless, "headless", false,
		"Run Chrome in headless mode (may not work with Google OAuth)")
	agentCmd.Flags().BoolVar(&agentVerbose, "verbose", false, "Verbose output")
	agentCmd.Flags().StringVar(&agentConfigPath, "config", "", "Path to JSON config file")
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

	if agentConfigPath != "" {
		return runAgentFromConfig(cmd, logger, agentConfigPath)
	}

	// Parse strategy
	strategy, err := parseStrategy(agentStrategy)
	if err != nil {
		return err
	}

	// Create config
	config := agent.DefaultConfig()
	config.Port = agentPort
	config.CoordinatorURL = agentCoordinator
	config.CoordinatorToken = agentCoordinatorToken
	config.PollInterval = 2 * time.Second
	config.ChromeUserDataDir = agentChromeProfile
	config.Headless = agentHeadless
	config.AccountStrategy = strategy
	config.Accounts = agentAccounts
	config.Logger = logger

	return runSingleAgent(cmd, logger, config, agentStrategy, agentAccounts, agentChromeProfile)
}

func truncateCode(code string) string {
	if len(code) <= 4 {
		return code
	}
	return code[:4]
}

func runAgentFromConfig(cmd *cobra.Command, logger *slog.Logger, path string) error {
	useMulti, singleCfg, multiCfg, err := loadAgentConfig(path)
	if err != nil {
		return err
	}

	if useMulti {
		multiCfg.Logger = logger
		if multiCfg.PollInterval == 0 {
			multiCfg.PollInterval = 2 * time.Second
		}
		if multiCfg.AccountStrategy == "" {
			multiCfg.AccountStrategy = agent.StrategyLRU
		}
		if multiCfg.Port == 0 {
			multiCfg.Port = 7891
		}
		if len(multiCfg.Coordinators) == 0 {
			return fmt.Errorf("config has no coordinators")
		}
		return runMultiAgent(cmd, logger, multiCfg)
	}

	singleCfg.Logger = logger
	if singleCfg.PollInterval == 0 {
		singleCfg.PollInterval = 2 * time.Second
	}
	if singleCfg.AccountStrategy == "" {
		singleCfg.AccountStrategy = agent.StrategyLRU
	}
	if singleCfg.Port == 0 {
		singleCfg.Port = 7891
	}
	if singleCfg.CoordinatorURL == "" {
		return fmt.Errorf("config missing coordinator URL")
	}

	strategy := string(singleCfg.AccountStrategy)
	return runSingleAgent(cmd, logger, singleCfg, strategy, singleCfg.Accounts, singleCfg.ChromeUserDataDir)
}

func runSingleAgent(cmd *cobra.Command, logger *slog.Logger, config agent.Config, strategy string, accounts []string, chromeProfile string) error {
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
	fmt.Printf("  API: http://localhost:%d\n", config.Port)
	fmt.Printf("  Coordinator: %s\n", config.CoordinatorURL)
	fmt.Printf("  Strategy: %s\n", strategy)
	if len(accounts) > 0 {
		fmt.Printf("  Accounts: %v\n", accounts)
	}
	if chromeProfile != "" {
		fmt.Printf("  Chrome profile: %s\n", chromeProfile)
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

func runMultiAgent(cmd *cobra.Command, logger *slog.Logger, config agent.MultiConfig) error {
	ma := agent.NewMulti(config)

	ma.OnAuthStart = func(coord, url, account string) {
		acc := account
		if acc == "" {
			acc = "(auto)"
		}
		fmt.Printf("[%s] %s: Starting auth for %s\n",
			time.Now().Format("15:04:05"), coord, acc)
	}

	ma.OnAuthComplete = func(coord, account, code string) {
		fmt.Printf("[%s] %s: Auth completed for %s (code: %s...)\n",
			time.Now().Format("15:04:05"), coord, account, truncateCode(code))
	}

	ma.OnAuthFailed = func(coord, account string, err error) {
		fmt.Printf("[%s] %s: Auth FAILED for %s: %v\n",
			time.Now().Format("15:04:05"), coord, account, err)
	}

	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	if err := ma.Start(ctx); err != nil {
		return fmt.Errorf("start agent: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	fmt.Printf("Auth agent started (multi-coordinator)\n")
	fmt.Printf("  API: http://localhost:%d\n", config.Port)
	fmt.Printf("  Coordinators: %d\n", len(config.Coordinators))
	fmt.Printf("  Strategy: %s\n", config.AccountStrategy)
	if len(config.Accounts) > 0 {
		fmt.Printf("  Accounts: %v\n", config.Accounts)
	}
	if config.ChromeUserDataDir != "" {
		fmt.Printf("  Chrome profile: %s\n", config.ChromeUserDataDir)
	}
	fmt.Println("\nWaiting for auth requests...")
	fmt.Println("Press Ctrl+C to stop.")

	select {
	case <-sigCh:
		fmt.Println("\nShutting down...")
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()

	if err := ma.Stop(shutdownCtx); err != nil {
		logger.Warn("agent stop error", "error", err)
	}

	fmt.Println("Agent stopped.")
	return nil
}

type agentFileConfig struct {
	Port             int                          `json:"port"`
	CoordinatorURL   string                       `json:"coordinator_url"`
	Coordinator      string                       `json:"coordinator"`
	CoordinatorToken string                       `json:"coordinator_token"`
	PollInterval     string                       `json:"poll_interval"`
	ChromeProfile    string                       `json:"chrome_profile"`
	Headless         bool                         `json:"headless"`
	Strategy         string                       `json:"strategy"`
	Accounts         []string                     `json:"accounts"`
	Coordinators     []*agent.CoordinatorEndpoint `json:"coordinators"`
	ChromeUserData   string                       `json:"chrome_user_data_dir"`
	ChromeProfileDir string                       `json:"chrome_profile_dir"`
}

func loadAgentConfig(path string) (bool, agent.Config, agent.MultiConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, agent.Config{}, agent.MultiConfig{}, fmt.Errorf("read config: %w", err)
	}

	var raw agentFileConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return false, agent.Config{}, agent.MultiConfig{}, fmt.Errorf("parse config: %w", err)
	}

	useMulti := len(raw.Coordinators) > 0
	pollInterval, err := parseOptionalDuration(raw.PollInterval)
	if err != nil {
		return false, agent.Config{}, agent.MultiConfig{}, err
	}
	if useMulti {
		cfg := agent.DefaultMultiConfig()
		if raw.Port != 0 {
			cfg.Port = raw.Port
		}
		if pollInterval != 0 {
			cfg.PollInterval = pollInterval
		}
		cfg.ChromeUserDataDir = firstNonEmpty(raw.ChromeProfile, raw.ChromeUserData, raw.ChromeProfileDir)
		cfg.Headless = raw.Headless
		if raw.Strategy != "" {
			strategy, err := parseStrategy(raw.Strategy)
			if err != nil {
				return false, agent.Config{}, agent.MultiConfig{}, err
			}
			cfg.AccountStrategy = strategy
		}
		cfg.Accounts = raw.Accounts
		cfg.Coordinators = raw.Coordinators
		return true, agent.Config{}, cfg, nil
	}

	cfg := agent.DefaultConfig()
	if raw.Port != 0 {
		cfg.Port = raw.Port
	}
	if pollInterval != 0 {
		cfg.PollInterval = pollInterval
	}
	cfg.ChromeUserDataDir = firstNonEmpty(raw.ChromeProfile, raw.ChromeUserData, raw.ChromeProfileDir)
	cfg.Headless = raw.Headless
	if raw.Strategy != "" {
		strategy, err := parseStrategy(raw.Strategy)
		if err != nil {
			return false, agent.Config{}, agent.MultiConfig{}, err
		}
		cfg.AccountStrategy = strategy
	}
	cfg.Accounts = raw.Accounts
	cfg.CoordinatorURL = firstNonEmpty(raw.CoordinatorURL, raw.Coordinator)
	cfg.CoordinatorToken = raw.CoordinatorToken

	return false, cfg, agent.MultiConfig{}, nil
}

func parseStrategy(value string) (agent.AccountStrategy, error) {
	switch value {
	case "lru":
		return agent.StrategyLRU, nil
	case "round_robin":
		return agent.StrategyRoundRobin, nil
	case "random":
		return agent.StrategyRandom, nil
	default:
		return "", fmt.Errorf("unknown strategy: %s", value)
	}
}

func parseOptionalDuration(value string) (time.Duration, error) {
	if strings.TrimSpace(value) == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("parse poll_interval: %w", err)
	}
	return d, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
