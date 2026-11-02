package vaulttransfer

import (
	"os"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestMain isolates native credentials and environment for every transfer test.
func TestMain(m *testing.M) {
	os.Exit(testutil.IsolatedMain(m))
}
