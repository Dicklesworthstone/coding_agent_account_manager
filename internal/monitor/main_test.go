package monitor

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestMain runs this package's tests under an isolated HOME so nothing they
// exercise can reach the developer's real auth files. See testutil.RunIsolated.
func TestMain(m *testing.M) {
	os.Exit(testutil.RunIsolated(m))
}
