package workflows

import (
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/cmd/caam/cmd"
)

// TestCLIHelper acts as the entry point for subprocess CLI calls.
// Usage: exec.Command(os.Executable(), "-test.run=^TestCLIHelper$", "--", "arg1", "arg2")
func TestCLIHelper(t *testing.T) {
	if os.Getenv("GO_WANT_CLI_HELPER") != "1" {
		return
	}

	// Find where the real args start (after "--")
	args := []string{}
	for i, arg := range os.Args {
		if arg == "--" {
			if i+1 < len(os.Args) {
				args = os.Args[i+1:]
			}
			break
		}
	}

	if len(args) == 0 {
		// Fallback to env var if needed, or just exit
		if extra := os.Getenv("CAAM_CLI_ARGS"); extra != "" {
			args = strings.Split(extra, " ")
		}
	}

	if len(args) == 0 {
		return
	}

	// Inject "caam" as the first arg if missing (cobra expects app name + args)
	if args[0] != "caam" {
		args = append([]string{"caam"}, args...)
	}

	os.Args = args

	// Execute and exit with appropriate code
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
