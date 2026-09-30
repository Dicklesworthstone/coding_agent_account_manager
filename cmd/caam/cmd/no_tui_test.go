package cmd

import (
	"strings"
	"testing"
)

// Bare 'caam' must not start the full-screen TUI when it is disabled or when
// there is no terminal (pipes, CI); it prints the status table instead.
func TestTUIUnavailableReason(t *testing.T) {
	t.Setenv("CAAM_HOME", t.TempDir())
	t.Setenv("CAAM_NO_TUI", "")

	t.Setenv("NO_TUI", "1")
	if got := tuiUnavailableReason(); !strings.Contains(got, "NO_TUI") {
		t.Errorf("NO_TUI=1: reason = %q, want it to name NO_TUI", got)
	}

	t.Setenv("NO_TUI", "")
	t.Setenv("CAAM_NO_TUI", "true")
	if got := tuiUnavailableReason(); !strings.Contains(got, "NO_TUI") {
		t.Errorf("CAAM_NO_TUI=true: reason = %q, want it to name NO_TUI", got)
	}

	// Under 'go test' stdin/stdout are not terminals.
	t.Setenv("CAAM_NO_TUI", "false")
	if got := tuiUnavailableReason(); !strings.Contains(got, "not a terminal") {
		t.Errorf("non-terminal: reason = %q, want a not-a-terminal reason", got)
	}
}
