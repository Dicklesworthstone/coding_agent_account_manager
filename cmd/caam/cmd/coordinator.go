package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
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

TRANSPORT:
  The API listens on 127.0.0.1 by default. 'caam setup distributed' configures
  the local agent to reach it through its own SSH connection, so nothing is
  exposed on the network. Binding to another address (for example a Tailscale
  IP with --bind) requires --auth-token.

Examples:
  # Start coordinator (auto-detects best backend)
  caam auth-coordinator

  # Force WezTerm backend
  caam auth-coordinator --backend wezterm

  # Force tmux backend (for Ghostty/Alacritty/iTerm2)
  caam auth-coordinator --backend tmux

  # Custom port and verbose logging
  caam auth-coordinator --port 7891 --verbose

  # Listen on a Tailscale address (token required)
  caam auth-coordinator --bind 100.64.0.5 --auth-token "$CAAM_COORDINATOR_TOKEN"

Manual SSH tunnel (run on the local machine; the agent then uses http://localhost:7890):
  ssh -N -L 7890:127.0.0.1:7890 user@remote-server`,
	RunE: runCoordinator,
}

var (
	coordinatorPort         int
	coordinatorBind         string
	coordinatorPollMs       int
	coordinatorResumePrompt string
	coordinatorVerbose      bool
	coordinatorJSONLogs     bool
	coordinatorBackend      string
	coordinatorConfigPath   string
	coordinatorAuthToken    string
)

func init() {
	rootCmd.AddCommand(coordinatorCmd)

	coordinatorCmd.Flags().IntVar(&coordinatorPort, "port", 7890, "API server port")
	coordinatorCmd.Flags().StringVar(&coordinatorBind, "bind", coordinator.DefaultBindAddress,
		"API listen address (non-loopback addresses require --auth-token)")
	coordinatorCmd.Flags().IntVar(&coordinatorPollMs, "poll-interval", 500, "Pane poll interval in milliseconds")
	coordinatorCmd.Flags().StringVar(&coordinatorResumePrompt, "resume-prompt",
		"proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		"Text to inject after successful auth")
	coordinatorCmd.Flags().BoolVar(&coordinatorVerbose, "verbose", false, "Verbose output (debug level)")
	coordinatorCmd.Flags().BoolVar(&coordinatorJSONLogs, "json", false, "Output logs in JSON format")
	coordinatorCmd.Flags().StringVar(&coordinatorBackend, "backend", "auto",
		"Terminal multiplexer backend: wezterm (preferred), tmux, or auto")
	coordinatorCmd.Flags().StringVar(&coordinatorConfigPath, "config", "", "Path to JSON config file")
	coordinatorCmd.Flags().StringVar(&coordinatorAuthToken, "auth-token", "", "Auth token for coordinator API (shared secret)")
}

func runCoordinator(cmd *cobra.Command, args []string) error {
	// Setup logger
	logLevel := slog.LevelInfo
	if coordinatorVerbose {
		logLevel = slog.LevelDebug
	}

	// Choose log format: JSON or text
	var logHandler slog.Handler
	logOpts := &slog.HandlerOptions{Level: logLevel}
	if coordinatorJSONLogs {
		logHandler = slog.NewJSONHandler(os.Stderr, logOpts)
	} else {
		logHandler = slog.NewTextHandler(os.Stderr, logOpts)
	}
	logger := slog.New(logHandler)

	config := coordinator.DefaultConfig()
	listen := coordinatorListen{Bind: coordinator.DefaultBindAddress, Port: coordinatorPort}

	if coordinatorConfigPath != "" {
		loadedConfig, loadedListen, err := loadCoordinatorConfig(coordinatorConfigPath)
		if err != nil {
			return err
		}
		config = loadedConfig
		listen = loadedListen
	}

	if cmd.Flags().Changed("backend") {
		backend, err := parseBackend(coordinatorBackend)
		if err != nil {
			return err
		}
		config.Backend = backend
	}
	if cmd.Flags().Changed("poll-interval") {
		config.PollInterval = time.Duration(coordinatorPollMs) * time.Millisecond
	}
	if cmd.Flags().Changed("resume-prompt") {
		config.ResumePrompt = coordinatorResumePrompt
	}
	if cmd.Flags().Changed("port") {
		listen.Port = coordinatorPort
	}
	if cmd.Flags().Changed("bind") {
		listen.Bind = coordinatorBind
	}
	if cmd.Flags().Changed("auth-token") {
		config.AuthToken = coordinatorAuthToken
	} else if envToken := strings.TrimSpace(os.Getenv("CAAM_COORDINATOR_TOKEN")); envToken != "" {
		config.AuthToken = envToken
	}
	if err := coordinator.ValidateListenSecurity(listen.Bind, config.AuthToken); err != nil {
		return err
	}

	config.Logger = logger

	// Bind before starting the monitor so a taken port or bad address fails fast.
	apiAddr := coordinator.ListenAddress(listen.Bind, listen.Port)
	listener, err := net.Listen("tcp", apiAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", apiAddr, err)
	}

	// Create coordinator
	coord := coordinator.New(config)

	// Set up callbacks
	coord.OnAuthRequest = func(req *coordinator.AuthRequest) {
		fmt.Printf("[%s] AUTH NEEDED pane=%d request=%s url=%s\n",
			time.Now().Format("15:04:05"),
			req.PaneID,
			req.ID,
			coordinator.RedactURL(req.URL))
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
	api := coordinator.NewAPIServer(coord, listen.Bind, listen.Port, logger)

	// Start coordinator
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	if err := coord.Start(ctx); err != nil {
		listener.Close()
		return fmt.Errorf("start coordinator: %w", err)
	}

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start API server in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- api.Serve(listener)
	}()

	fmt.Printf("Auth coordinator started\n")
	fmt.Printf("  Backend: %s\n", coord.Backend())
	fmt.Printf("  API: http://%s\n", apiAddr)
	fmt.Printf("  Poll interval: %dms\n", int(config.PollInterval.Milliseconds()))
	if config.AuthToken != "" {
		fmt.Println("  Auth: token required")
	}
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
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			coord.Stop()
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

// coordinatorListen is where the coordinator API listens.
type coordinatorListen struct {
	Bind string
	Port int
}

func loadCoordinatorConfig(path string) (coordinator.Config, coordinatorListen, error) {
	fc, err := coordinator.LoadFileConfig(path)
	if err != nil {
		return coordinator.Config{}, coordinatorListen{}, err
	}
	cfg, err := fc.Apply(coordinator.DefaultConfig())
	if err != nil {
		return coordinator.Config{}, coordinatorListen{}, fmt.Errorf("config %s: %w", path, err)
	}

	listen := coordinatorListen{Bind: coordinator.DefaultBindAddress, Port: coordinatorPort}
	if strings.TrimSpace(fc.Bind) != "" {
		listen.Bind = strings.TrimSpace(fc.Bind)
	}
	if fc.Port != 0 {
		listen.Port = fc.Port
	}
	return cfg, listen, nil
}

func parseBackend(value string) (coordinator.Backend, error) {
	return coordinator.ParseBackend(value)
}

var (
	coordinatorStatusURL    string
	coordinatorStatusConfig string
	coordinatorStatusToken  string
	coordinatorStatusJSON   bool
)

// coordinatorStatusCmd queries a running coordinator.
var coordinatorStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show auth-coordinator status",
	Long: `Query a running auth-coordinator's /status endpoint.

The address and token come from --url/--auth-token, or from the coordinator
config file (default ~/.config/caam/coordinator.json when present).`,
	Args: cobra.NoArgs,
	RunE: runCoordinatorStatus,
}

func init() {
	coordinatorCmd.AddCommand(coordinatorStatusCmd)
	coordinatorStatusCmd.Flags().StringVar(&coordinatorStatusURL, "url", "", "coordinator base URL (default from config, else http://127.0.0.1:7890)")
	coordinatorStatusCmd.Flags().StringVar(&coordinatorStatusConfig, "config", "", "coordinator config file to read address and token from")
	coordinatorStatusCmd.Flags().StringVar(&coordinatorStatusToken, "auth-token", "", "coordinator API token (default from config or CAAM_COORDINATOR_TOKEN)")
	coordinatorStatusCmd.Flags().BoolVar(&coordinatorStatusJSON, "json", false, "print the raw status JSON")
}

func runCoordinatorStatus(cmd *cobra.Command, args []string) error {
	baseURL, token, err := resolveCoordinatorStatusTarget()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(cmd.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/status", nil)
	if err != nil {
		return fmt.Errorf("build status request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("coordinator unreachable at %s: %w", baseURL, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("read status: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("coordinator at %s returned %s: %s", baseURL, resp.Status, strings.TrimSpace(string(body)))
	}

	out := cmd.OutOrStdout()
	if coordinatorStatusJSON {
		_, err := out.Write(body)
		return err
	}

	var status coordinator.StatusResponse
	if err := json.Unmarshal(body, &status); err != nil {
		return fmt.Errorf("decode status: %w", err)
	}
	fmt.Fprintf(out, "Coordinator: %s (backend %s)\n", baseURL, status.Backend)
	fmt.Fprintf(out, "Panes: %d  Pending auth requests: %d\n", status.PaneCount, status.PendingAuths)
	sort.Slice(status.Panes, func(i, j int) bool { return status.Panes[i].PaneID < status.Panes[j].PaneID })
	for _, p := range status.Panes {
		line := fmt.Sprintf("  pane %-5d %-22s since %s", p.PaneID, p.State, p.StateEntered.Format(time.RFC3339))
		if p.RequestID != "" {
			line += " request=" + p.RequestID
		}
		if p.Account != "" {
			line += " account=" + p.Account
		}
		if p.Error != "" {
			line += " error=" + p.Error
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

// resolveCoordinatorStatusTarget picks the URL and token for 'status' from
// flags, then the coordinator config file, then the environment.
func resolveCoordinatorStatusTarget() (string, string, error) {
	configPath := coordinatorStatusConfig
	explicitConfig := configPath != ""
	if !explicitConfig {
		if dir, err := os.UserConfigDir(); err == nil {
			configPath = filepath.Join(dir, "caam", "coordinator.json")
		}
	}

	var fc coordinator.FileConfig
	if configPath != "" {
		loaded, err := coordinator.LoadFileConfig(configPath)
		switch {
		case err == nil:
			fc = loaded
		case explicitConfig || !errors.Is(err, os.ErrNotExist):
			return "", "", err
		}
	}

	baseURL := coordinatorStatusURL
	if baseURL == "" {
		bind := strings.TrimSpace(fc.Bind)
		if bind == "" || bind == "0.0.0.0" || bind == "::" {
			bind = coordinator.DefaultBindAddress
		}
		port := fc.Port
		if port == 0 {
			port = 7890
		}
		baseURL = "http://" + coordinator.ListenAddress(bind, port)
	}

	token := coordinatorStatusToken
	if token == "" {
		token = fc.AuthToken
	}
	if token == "" {
		token = strings.TrimSpace(os.Getenv("CAAM_COORDINATOR_TOKEN"))
	}
	return baseURL, token, nil
}

// filterClaudePanes returns true for panes likely running Claude Code.
func filterClaudePanes(pane coordinator.Pane) bool {
	title := strings.ToLower(pane.Title)
	return strings.Contains(title, "claude") ||
		strings.Contains(title, "cc") ||
		strings.Contains(title, "anthropic")
}
