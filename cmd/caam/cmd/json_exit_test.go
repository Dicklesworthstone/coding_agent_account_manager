package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// runRunEForJSON invokes a command's RunE directly (not via rootCmd.Execute, to
// avoid PersistentPreRunE side effects that pollute shared global state for
// other tests), with the --json flag set, capturing the command's stdout via
// SetOut. It returns the captured output and the error RunE returned (which is
// what determines the process exit code).
func runRunEForJSON(t *testing.T, cmd *cobra.Command, run func(*cobra.Command, []string) error, args []string) (string, error) {
	t.Helper()

	// Reset any silence flags a prior run may have set on the shared cmd.
	cmd.SilenceUsage = false
	cmd.SilenceErrors = false
	t.Cleanup(func() {
		cmd.SilenceUsage = false
		cmd.SilenceErrors = false
		_ = cmd.Flags().Set("json", "false")
	})

	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json flag: %v", err)
	}

	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := run(cmd, args)
	return out.String(), err
}

// TestJSONMode_ActivateFailureReturnsError verifies issue #24: a --json
// activate failure must emit a JSON payload AND return a non-zero exit
// (i.e. a non-nil error from RunE), not exit 0.
func TestJSONMode_ActivateFailureReturnsError(t *testing.T) {
	out, err := runRunEForJSON(t, activateCmd, runActivate, []string{"notatool", "nope"})
	if err == nil {
		t.Fatalf("expected non-nil error (non-zero exit) for failed --json activate, got nil; output=%q", out)
	}

	var payload struct {
		Success bool   `json:"success"`
		Error   string `json:"error"`
	}
	if jerr := json.Unmarshal([]byte(out), &payload); jerr != nil {
		t.Fatalf("activate --json output is not valid JSON: %v; output=%q", jerr, out)
	}
	if payload.Success {
		t.Errorf("expected success=false in JSON payload, got true")
	}
	if payload.Error == "" {
		t.Errorf("expected non-empty error in JSON payload")
	}
	// The usage block must not be dumped on a runtime JSON error.
	if strings.Contains(out, "Usage:") {
		t.Errorf("did not expect usage text in JSON error output; output=%q", out)
	}
}

// TestJSONMode_BackupFailureReturnsError verifies issue #24 for backup.
func TestJSONMode_BackupFailureReturnsError(t *testing.T) {
	out, err := runRunEForJSON(t, backupCmd, runBackup, []string{"notatool", "nope"})
	if err == nil {
		t.Fatalf("expected non-nil error (non-zero exit) for failed --json backup, got nil; output=%q", out)
	}

	var payload struct {
		Success bool `json:"success"`
	}
	if jerr := json.Unmarshal([]byte(out), &payload); jerr != nil {
		t.Fatalf("backup --json output is not valid JSON: %v; output=%q", jerr, out)
	}
	if payload.Success {
		t.Errorf("expected success=false in JSON payload, got true")
	}
}

// TestSupportedToolsList verifies issue #30: the shared helper enumerates every
// auth-swap provider, so error/help text no longer drifts to the stale
// "codex, claude, gemini" subset.
func TestSupportedToolsList(t *testing.T) {
	got := supportedToolsList()
	for _, want := range []string{"agy", "claude", "codex", "cursor", "gemini", "grok", "opencode"} {
		if !strings.Contains(got, want) {
			t.Errorf("supportedToolsList() = %q, missing %q", got, want)
		}
	}
}
