package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/agent"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/deploy"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:   "auth-agent",
	Short: "Run the local auth agent for automated OAuth completion",
	Long: `Receive OAuth URLs from the remote coordinator and complete authentication
using browser automation.

The auth-agent runs on your local machine (e.g., Mac) and drives its own
Chrome profile; sign in to your Google accounts there once with
'caam auth-agent signin'. It:
1. Receives auth request URLs from the remote coordinator
2. Opens Chrome and navigates to the OAuth URL
3. Selects the appropriate Google account (using LRU strategy by default)
4. Extracts the challenge code
5. Sends the code back to the coordinator

The coordinator then injects this code into the waiting Claude Code session.
Codes are redelivered until the coordinator acknowledges them, so a dropped
connection does not lose a completed login.

TRANSPORT:
  'caam setup distributed' writes a config whose coordinators carry an "ssh"
  block: the agent opens its own SSH connection to each host and reaches the
  loopback-only coordinator API through it, with a per-host token.

  With --coordinator, forward the port yourself (run on this machine):
    ssh -N -L 7890:127.0.0.1:7890 user@remote-server

Examples:
  # Start agent with default settings
  caam auth-agent

  # Specify coordinator URL and accounts
  caam auth-agent --coordinator http://localhost:7890 \
    --accounts alice@gmail.com,bob@gmail.com

  # Sign in to the Google accounts the agent should use (once per machine)
  caam auth-agent signin

  # Use a different Chrome user data directory
  caam auth-agent --chrome-profile ~/caam-chrome

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
		"Chrome user data directory (default: the agent's persistent profile)")
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
	fmt.Printf("  Chrome profile: %s\n", agent.ResolveChromeUserDataDir(chromeProfile))
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
	for _, c := range config.Coordinators {
		via := c.URL
		if c.SSH != nil {
			via = fmt.Sprintf("%s via ssh %s", c.URL, c.SSH.Host)
		}
		auth := "no token"
		if c.Token != "" {
			auth = "token"
		}
		fmt.Printf("    - %s: %s (%s)\n", c.Name, via, auth)
	}
	fmt.Printf("  Strategy: %s\n", config.AccountStrategy)
	if len(config.Accounts) > 0 {
		fmt.Printf("  Accounts: %v\n", config.Accounts)
	}
	fmt.Printf("  Chrome profile: %s\n", agent.ResolveChromeUserDataDir(config.ChromeUserDataDir))
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

func loadAgentConfig(path string) (bool, agent.Config, agent.MultiConfig, error) {
	raw, err := agent.LoadFileConfig(path)
	if err != nil {
		return false, agent.Config{}, agent.MultiConfig{}, err
	}

	useMulti := len(raw.Coordinators) > 0
	pollInterval, err := parseOptionalDuration(raw.PollInterval)
	if err != nil {
		return false, agent.Config{}, agent.MultiConfig{}, err
	}
	if useMulti {
		for i, c := range raw.Coordinators {
			if c == nil || strings.TrimSpace(c.URL) == "" {
				return false, agent.Config{}, agent.MultiConfig{}, fmt.Errorf("config %s: coordinator %d has no url", path, i+1)
			}
			if c.SSH != nil && strings.TrimSpace(c.SSH.Host) == "" {
				return false, agent.Config{}, agent.MultiConfig{}, fmt.Errorf("config %s: coordinator %q has ssh without host", path, c.Name)
			}
		}
		cfg := agent.DefaultMultiConfig()
		if raw.Port != 0 {
			cfg.Port = raw.Port
		}
		if pollInterval != 0 {
			cfg.PollInterval = pollInterval
		}
		cfg.ChromeUserDataDir = raw.ChromeUserDataDir()
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
	cfg.ChromeUserDataDir = raw.ChromeUserDataDir()
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
		code, account, err := browser.CompleteOAuth(cmd.Context(), url, nil)
		if err != nil {
			return fmt.Errorf("OAuth failed: %w", err)
		}

		fmt.Printf("\nSuccess!\n")
		fmt.Printf("  Code: %s\n", code)
		fmt.Printf("  Account: %s\n", account)
		return nil
	},
}

var (
	agentSignInConfig  string
	agentSignInProfile string
)

// agentSignInCmd opens the agent's Chrome profile so the user can sign in to
// the Google accounts (and Claude) that unattended OAuth flows will use.
var agentSignInCmd = &cobra.Command{
	Use:   "signin",
	Short: "Sign in to Google accounts in the auth agent's Chrome profile",
	Long: `Open a Chrome window on the auth agent's own profile with tabs for adding
Google accounts and signing in to Claude. Sign in to every Google account the
agent should rotate through, then close the window.

The agent reuses these sessions for every OAuth flow, so this is needed once
per machine (and again only when Google signs a session out). The profile is
the config's chrome_profile, or by default <caam data dir>/auth-agent-chrome
(~/.local/share/caam/auth-agent-chrome, or $CAAM_HOME/data/auth-agent-chrome).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		profile, err := signInProfileDir(cmd)
		if err != nil {
			return err
		}
		browser := agent.NewBrowser(agent.BrowserConfig{UserDataDir: profile})
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Opening Chrome with profile %s\n", browser.UserDataDir())
		fmt.Fprintln(out, "Sign in to each Google account the agent should use (and to Claude), then close the window.")
		if err := browser.OpenForSignIn(cmd.Context(), agent.SignInURLs...); err != nil {
			return err
		}
		fmt.Fprintln(out, "Done. Running auth agents pick up the sign-ins on their next OAuth flow.")
		cfgPath := agentSignInConfig
		if cfgPath == "" {
			cfgPath = agent.DefaultConfigPath()
		}
		fmt.Fprintf(out, "Add the accounts to rotate through (they go into %s):\n", cfgPath)
		fmt.Fprintln(out, "  caam auth-agent accounts add <email>...")
		fmt.Fprintln(out, "Without them every recovery reuses whichever account is offered first. Check with: caam doctor")
		return nil
	},
}

// signInProfileDir picks the profile: --chrome-profile, then the agent
// config's chrome_profile, then the default.
func signInProfileDir(cmd *cobra.Command) (string, error) {
	if cmd.Flags().Changed("chrome-profile") {
		return agentSignInProfile, nil
	}
	path := agentSignInConfig
	if path == "" {
		path = agent.DefaultConfigPath()
	}
	fc, err := agent.LoadFileConfig(path)
	switch {
	case err == nil:
		return fc.ChromeUserDataDir(), nil
	case errors.Is(err, os.ErrNotExist) && !cmd.Flags().Changed("config"):
		return "", nil
	default:
		return "", fmt.Errorf("agent config %s: %w", path, err)
	}
}

var (
	agentServiceConfig string
	agentServiceJSON   bool
)

// agentServiceCmd manages the auth-agent as a per-user background service.
var agentServiceCmd = &cobra.Command{
	Use:   "service",
	Short: "Run the auth agent as a login service (launchd on macOS, systemd on Linux)",
	Long: `Install, remove, or inspect a per-user service that runs
'caam auth-agent --config <path>' at login and restarts it if it crashes.

macOS:  ~/Library/LaunchAgents/com.dicklesworthstone.caam.auth-agent.plist
        (logs: ~/Library/Logs/caam-auth-agent.log)
Linux:  ~/.config/systemd/user/caam-auth-agent.service
        (logs: journalctl --user -u caam-auth-agent)

The config defaults to the one written by 'caam setup distributed'.`,
}

func newAgentService() (*deploy.AgentService, error) {
	configPath := agentServiceConfig
	if configPath == "" {
		configPath = agent.DefaultConfigPath()
	}
	abs, err := filepath.Abs(configPath)
	if err != nil {
		return nil, fmt.Errorf("resolve config path: %w", err)
	}
	return deploy.NewAgentService(abs)
}

var agentServiceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install and start the auth-agent service",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newAgentService()
		if err != nil {
			return err
		}
		if _, err := loadAgentConfigForService(svc.ConfigPath); err != nil {
			return err
		}
		path, err := svc.Install(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		fmt.Fprintf(out, "Installed auth-agent service: %s\n", path)
		fmt.Fprintf(out, "  Runs: %s auth-agent --config %s\n", svc.Executable, svc.ConfigPath)
		if logPath := svc.LogPath(); logPath != "" {
			fmt.Fprintf(out, "  Logs: %s\n", logPath)
		} else {
			fmt.Fprintln(out, "  Logs: journalctl --user -u caam-auth-agent -f")
		}
		return nil
	},
}

var agentServiceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and remove the auth-agent service",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newAgentService()
		if err != nil {
			return err
		}
		path, err := svc.Uninstall(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Removed auth-agent service: %s\n", path)
		return nil
	},
}

var agentServiceStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether the auth-agent service is installed and running",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		svc, err := newAgentService()
		if err != nil {
			return err
		}
		st, err := svc.Status(cmd.Context())
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if agentServiceJSON {
			enc := json.NewEncoder(out)
			enc.SetIndent("", "  ")
			return enc.Encode(st)
		}
		fmt.Fprintf(out, "Unit:      %s\n", st.UnitPath)
		fmt.Fprintf(out, "Installed: %t\n", st.Installed)
		fmt.Fprintf(out, "Running:   %t\n", st.Running)
		if st.Detail != "" {
			fmt.Fprintf(out, "Detail:    %s\n", st.Detail)
		}
		if st.LogPath != "" {
			fmt.Fprintf(out, "Logs:      %s\n", st.LogPath)
		}
		return nil
	},
}

// loadAgentConfigForService validates the config before a service is
// installed that would crash-loop on it.
func loadAgentConfigForService(path string) (bool, error) {
	useMulti, _, _, err := loadAgentConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("agent config %s not found: run 'caam setup distributed' or pass --config", path)
	}
	if err != nil {
		return false, fmt.Errorf("agent config %s: %w", path, err)
	}
	return useMulti, nil
}

func init() {
	agentCmd.AddCommand(agentServiceCmd)
	agentServiceCmd.AddCommand(agentServiceInstallCmd, agentServiceUninstallCmd, agentServiceStatusCmd)
	agentServiceCmd.PersistentFlags().StringVar(&agentServiceConfig, "config", "",
		"agent config file (default: the 'caam setup distributed' config)")
	agentServiceStatusCmd.Flags().BoolVar(&agentServiceJSON, "json", false, "print status as JSON")
}

func init() {
	agentCmd.AddCommand(agentSignInCmd)
	agentSignInCmd.Flags().StringVar(&agentSignInConfig, "config", "",
		"agent config whose chrome_profile to use (default: the 'caam setup distributed' config)")
	agentSignInCmd.Flags().StringVar(&agentSignInProfile, "chrome-profile", "",
		"Chrome user data directory (overrides the config)")
}

var agentAccountsConfig string

// agentAccountsCmd lists and edits the Google accounts the agent rotates
// through (the config's "accounts").
var agentAccountsCmd = &cobra.Command{
	Use:   "accounts",
	Short: "List the accounts the auth agent rotates through",
	Long: `List the Google accounts the auth agent signs panes in with, in order, with
when each was last used and any hold: an account that hit its usage limit is
passed over until the limit resets.

Add or remove accounts with 'caam auth-agent accounts add|remove <email>...'.
Sign in to them first in the agent's Chrome profile: caam auth-agent signin.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		path, fc, err := loadAgentAccountsConfig()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		if len(fc.Accounts) == 0 {
			fmt.Fprintf(out, "No accounts in %s; every recovery reuses whichever account is offered first.\n", path)
			fmt.Fprintln(out, "Add some with: caam auth-agent accounts add <email>...")
			return nil
		}
		held, err := agent.HeldAccounts(agent.UsagePath(), time.Now())
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %v\n", err)
		}
		lastUsed := accountLastUsed(agent.UsagePath())
		fmt.Fprintf(out, "Accounts (%s, strategy %s):\n", path, firstNonEmpty(fc.Strategy, "lru"))
		for i, acc := range fc.Accounts {
			line := fmt.Sprintf("  %d. %s", i+1, acc)
			if t, ok := lastUsed[strings.ToLower(acc)]; ok && !t.IsZero() {
				line += "  last used " + t.Local().Format("Jan 2 15:04")
			}
			for email, until := range held {
				if strings.EqualFold(email, acc) {
					line += "  at its limit until " + until.Local().Format("Jan 2 15:04")
				}
			}
			fmt.Fprintln(out, line)
		}
		return nil
	},
}

var agentAccountsAddCmd = &cobra.Command{
	Use:   "add <email>...",
	Short: "Add accounts for the auth agent to rotate through",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editAgentAccounts(cmd, func(accounts []string) ([]string, []string, error) {
			var changed []string
			for _, email := range args {
				email = strings.TrimSpace(email)
				if !strings.Contains(email, "@") || strings.ContainsAny(email, " \t,") {
					return nil, nil, fmt.Errorf("%q is not an email address", email)
				}
				if !containsFold(accounts, email) {
					accounts = append(accounts, email)
					changed = append(changed, email)
				}
			}
			return accounts, changed, nil
		}, "Added")
	},
}

var agentAccountsRemoveCmd = &cobra.Command{
	Use:   "remove <email>...",
	Short: "Stop the auth agent from using accounts",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return editAgentAccounts(cmd, func(accounts []string) ([]string, []string, error) {
			var kept, changed []string
			for _, acc := range accounts {
				if containsFold(args, acc) {
					changed = append(changed, acc)
				} else {
					kept = append(kept, acc)
				}
			}
			if kept == nil {
				kept = []string{}
			}
			return kept, changed, nil
		}, "Removed")
	},
}

func loadAgentAccountsConfig() (string, agent.FileConfig, error) {
	path := agentAccountsConfig
	if path == "" {
		path = agent.DefaultConfigPath()
	}
	fc, err := agent.LoadFileConfig(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, fc, fmt.Errorf("agent config %s not found: run 'caam setup distributed' or pass --config", path)
	}
	if err != nil {
		return path, fc, fmt.Errorf("agent config %s: %w", path, err)
	}
	return path, fc, nil
}

// editAgentAccounts applies edit to the config's accounts, saves the config,
// and restarts an installed agent service so it uses them.
func editAgentAccounts(cmd *cobra.Command, edit func([]string) (accounts, changed []string, err error), verb string) error {
	path, fc, err := loadAgentAccountsConfig()
	if err != nil {
		return err
	}
	accounts, changed, err := edit(append([]string(nil), fc.Accounts...))
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if len(changed) == 0 {
		fmt.Fprintln(out, "Nothing to change.")
		return nil
	}
	fc.Accounts = accounts
	if err := agent.WriteFileConfig(path, fc); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s %s in %s.\n", verb, strings.Join(changed, ", "), path)
	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if restartInstalledAgentService(ctx, path) {
		fmt.Fprintln(out, "Restarted the auth-agent service.")
	}
	return nil
}

// accountLastUsed reads when each account was last used, by lowercased email.
func accountLastUsed(usagePath string) map[string]time.Time {
	data, err := os.ReadFile(usagePath)
	if err != nil {
		return nil
	}
	var usages []*agent.AccountUsage
	if json.Unmarshal(data, &usages) != nil {
		return nil
	}
	out := make(map[string]time.Time, len(usages))
	for _, u := range usages {
		if u != nil {
			out[strings.ToLower(u.Email)] = u.LastUsed
		}
	}
	return out
}

func containsFold(list []string, s string) bool {
	for _, item := range list {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

func init() {
	agentCmd.AddCommand(agentAccountsCmd)
	agentAccountsCmd.AddCommand(agentAccountsAddCmd, agentAccountsRemoveCmd)
	agentAccountsCmd.PersistentFlags().StringVar(&agentAccountsConfig, "config", "",
		"agent config file (default: the 'caam setup distributed' config)")
}

func init() {
	agentCmd.AddCommand(testAuthCmd)
	testAuthCmd.Flags().StringVar(&agentChromeProfile, "chrome-profile", "",
		"Chrome user data directory")
	testAuthCmd.Flags().BoolVar(&agentHeadless, "headless", false,
		"Run Chrome in headless mode")
}
