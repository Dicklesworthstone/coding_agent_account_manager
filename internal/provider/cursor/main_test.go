package cursor

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(testutil.IsolatedMain(m))
}
