package keychain_test

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestMain runs this package's tests under an isolated HOME, which also
// switches the keychain bridge off by default. Tests that need it opt in via
// testutil.FakeKeychain, which points `security` at a stub. See
// testutil.IsolatedMain.
func TestMain(m *testing.M) {
	os.Exit(testutil.IsolatedMain(m))
}
