package cmd

import (
	"errors"
	"fmt"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
)

// TestExitCode verifies issue #36: a wrapped tool's real exit code is propagated
// to the process exit code, instead of being flattened to 1.
func TestExitCode(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil error", nil, 0},
		{"exit code 127", &exec.ExitCodeError{Code: 127}, 127},
		{"exit code 0 falls back to 1", &exec.ExitCodeError{Code: 0}, 1},
		{"wrapped exit code", fmt.Errorf("run: %w", &exec.ExitCodeError{Code: 42}), 42},
		{"generic error", errors.New("boom"), 1},
	}
	for _, tc := range cases {
		if got := ExitCode(tc.err); got != tc.want {
			t.Errorf("%s: ExitCode = %d, want %d", tc.name, got, tc.want)
		}
	}
}

// TestNonInteractiveExecExample verifies the per-tool non-interactive hint shown
// when an interactive session fails because stdin is not a TTY (issue #36).
func TestNonInteractiveExecExample(t *testing.T) {
	cases := []struct {
		tool    string
		profile string
		args    []string
		want    string
	}{
		{"codex", "me", []string{"PING"}, `caam exec codex me -- exec PING`},
		{"codex", "me", []string{"exec", "PING"}, `caam exec codex me -- exec PING`},
		{"codex", "me", []string{"e", "PING"}, `caam exec codex me -- e PING`},
		{"claude", "home", []string{"fix bug"}, `caam exec claude home -- -p "fix bug"`},
		{"gemini", "g", nil, `caam exec gemini g -- -p`},
		{"agy", "a", []string{"hello"}, `caam exec agy a -- -p hello`},
		{"unknown", "x", []string{"y"}, `caam exec unknown x -- <non-interactive flag> y`},
	}
	for _, tc := range cases {
		got := nonInteractiveExecExample(tc.tool, tc.profile, tc.args)
		if got != tc.want {
			t.Errorf("nonInteractiveExecExample(%q,%q,%v) = %q, want %q", tc.tool, tc.profile, tc.args, got, tc.want)
		}
	}
}
