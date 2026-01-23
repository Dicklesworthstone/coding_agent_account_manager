package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/api"
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the local HTTP API server",
	Long: `Start a localhost-only HTTP server that exposes caam state and actions via a REST API.

The API enables web UIs and automation tools to interact with caam without shelling out.

ENDPOINTS:
  GET  /health                  Health check (no auth)
  GET  /api/v1/status           Overall status for all tools
  GET  /api/v1/profiles         List all profiles
  GET  /api/v1/profiles?tool=X  List profiles for a specific tool
  GET  /api/v1/profiles/X/Y     Get profile details
  DELETE /api/v1/profiles/X/Y   Delete a profile
  GET  /api/v1/usage            Usage statistics
  GET  /api/v1/coordinators     Coordinator status
  POST /api/v1/actions/activate Activate a profile
  POST /api/v1/actions/backup   Backup current auth to a profile
  GET  /api/v1/events           SSE stream for live updates

AUTHENTICATION:
  All endpoints except /health require Bearer token authentication.
  The token is auto-generated and stored at ~/.config/caam/.api_token
  (or $CAAM_HOME/.api_token if CAAM_HOME is set).

  Include the header: Authorization: Bearer <token>

SECURITY:
  - Server binds to 127.0.0.1 only (localhost)
  - CORS allows only localhost origins
  - All responses redact sensitive tokens
  - Token file has 0600 permissions

Examples:
  caam serve                        # Start on default port 7891
  caam serve --port 8080            # Use custom port
  caam serve --verbose              # Debug logging
  caam serve --show-token           # Print the API token

Querying the API:
  TOKEN=$(cat ~/.config/caam/.api_token)
  curl -H "Authorization: Bearer $TOKEN" http://localhost:7891/api/v1/status`,
	RunE: runServe,
}

var (
	servePort      int
	serveVerbose   bool
	serveShowToken bool
	serveJSONLogs  bool
)

func init() {
	rootCmd.AddCommand(serveCmd)

	serveCmd.Flags().IntVar(&servePort, "port", 7891, "API server port")
	serveCmd.Flags().BoolVar(&serveVerbose, "verbose", false, "Enable debug logging")
	serveCmd.Flags().BoolVar(&serveShowToken, "show-token", false, "Print API token and exit")
	serveCmd.Flags().BoolVar(&serveJSONLogs, "json", false, "Output logs in JSON format")
}

func runServe(cmd *cobra.Command, args []string) error {
	// Setup logger
	logLevel := slog.LevelInfo
	if serveVerbose {
		logLevel = slog.LevelDebug
	}

	var logHandler slog.Handler
	logOpts := &slog.HandlerOptions{Level: logLevel}
	if serveJSONLogs {
		logHandler = slog.NewJSONHandler(os.Stderr, logOpts)
	} else {
		logHandler = slog.NewTextHandler(os.Stderr, logOpts)
	}
	logger := slog.New(logHandler)

	// Create handlers with dependencies from root command
	db, err := getDB()
	if err != nil {
		logger.Warn("database unavailable, some features disabled", "error", err)
	}

	handlers := api.NewHandlers(vault, healthStore, db)

	// Create server config
	serverCfg := api.DefaultConfig()
	serverCfg.Port = servePort
	serverCfg.Logger = logger

	// Create server
	server, err := api.NewServer(serverCfg, handlers)
	if err != nil {
		return fmt.Errorf("create server: %w", err)
	}

	// If --show-token, just print and exit
	if serveShowToken {
		fmt.Println(server.Token())
		return nil
	}

	// Setup graceful shutdown
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	// Start server in background
	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Start()
	}()

	// Print startup info
	fmt.Printf("caam API server started\n")
	fmt.Printf("  Address: http://127.0.0.1:%d\n", server.Port())
	fmt.Printf("  Token:   %s\n", server.Token()[:8]+"...")
	fmt.Println()
	fmt.Println("Endpoints:")
	fmt.Println("  GET  /health              - Health check")
	fmt.Println("  GET  /api/v1/status       - Overall status")
	fmt.Println("  GET  /api/v1/profiles     - List profiles")
	fmt.Println("  GET  /api/v1/usage        - Usage statistics")
	fmt.Println("  GET  /api/v1/events       - SSE live updates")
	fmt.Println("  POST /api/v1/actions/*    - Actions (activate, backup)")
	fmt.Println()
	fmt.Println("Press Ctrl+C to stop.")

	// Wait for signal or error
	select {
	case <-sigCh:
		fmt.Println("\nShutting down...")
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("server error: %w", err)
		}
	case <-ctx.Done():
	}

	// Graceful shutdown with timeout
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()

	if err := server.Stop(shutdownCtx); err != nil {
		logger.Error("shutdown error", "error", err)
	}

	fmt.Println("Server stopped.")
	return nil
}
