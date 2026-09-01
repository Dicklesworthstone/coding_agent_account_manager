package testutil

import (
	"os"
	"testing"
)

// TestMain runs this package's tests under an isolated HOME. See RunIsolated.
func TestMain(m *testing.M) {
	os.Exit(RunIsolated(m))
}
