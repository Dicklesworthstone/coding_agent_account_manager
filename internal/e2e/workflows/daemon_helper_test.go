package workflows

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/cmd/caam/cmd"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// TestDaemonHelper is used to run the daemon in a subprocess.
// It is invoked by other tests with -test.run=TestDaemonHelper.
func TestDaemonHelper(t *testing.T) {
	if os.Getenv("GO_WANT_DAEMON_HELPER") != "1" {
		return
	}

	// Set up args to simulate "caam daemon start --fg"
	// We need to use cmd.Execute() but we can't easily pass args to it directly if it uses os.Args?
	// cobra uses os.Args[1:] by default.
	// But we can set args on the root command if we had access to it.
	// cmd.Execute() uses rootCmd.
	
	// Since we can't modify rootCmd args easily from here without exposing it,
	// we will rely on os.Args being set by the caller?
	// No, the caller calls the test binary.
	
	// We can set os.Args manually.
	if extraArgs := os.Getenv("CAAM_DAEMON_ARGS"); extraArgs != "" {
		// Simple splitting by space (doesn't handle quotes but enough for our tests)
		parts := strings.Split(extraArgs, " ")
		os.Args = append([]string{"caam", "daemon", "start", "--fg", "--verbose"}, parts...)
	} else {
		os.Args = []string{"caam", "daemon", "start", "--fg", "--verbose"}
	}
	
	if os.Getenv("MOCK_REFRESH_CLAUDE") == "1" {
		refresh.RefreshClaudeToken = func(ctx context.Context, refreshToken string) (*refresh.TokenResponse, error) {
			return &refresh.TokenResponse{
				AccessToken: "new-mock-access-token",
				ExpiresIn:   3600,
			}, nil
		}
	}
	
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
