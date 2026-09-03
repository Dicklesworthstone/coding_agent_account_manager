package keychain_test

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestMain runs this package's tests under an isolated HOME so nothing they
// exercise can reach the developer's real auth files, and with the keychain
// bridge disabled so nothing reaches the real login keychain either. Tests
// that need the bridge opt in through testutil.FakeKeychain.
func TestMain(m *testing.M) {
	os.Exit(testutil.IsolatedMain(m))
}
