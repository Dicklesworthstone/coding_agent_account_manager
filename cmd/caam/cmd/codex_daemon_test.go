package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// checkCodexDaemon must never warn for non-codex tools, regardless of what is
// running on the host (claude/gemini swap different files and have no shared
// in-process daemon model here).
func TestCheckCodexDaemon_SkipsNonCodexTools(t *testing.T) {
	for _, tool := range []string{"claude", "gemini", "agy", "CLAUDE", ""} {
		w := checkCodexDaemon(tool, false)
		if w.Detected {
			t.Errorf("tool %q: expected no detection for non-codex tool, got %+v", tool, w)
		}
		if w.Message != "" {
			t.Errorf("tool %q: expected empty message, got %q", tool, w.Message)
		}
	}
}

// printCodexDaemonWarning is a no-op when nothing was detected, and writes a
// "Warning:" line to the supplied writer when a daemon was found.
func TestPrintCodexDaemonWarning(t *testing.T) {
	var buf bytes.Buffer

	// No detection -> nothing written.
	printCodexDaemonWarning(&buf, codexDaemonWarning{})
	if buf.Len() != 0 {
		t.Fatalf("expected no output for empty warning, got %q", buf.String())
	}

	// Detected, not reloaded -> "Warning:" prefix.
	buf.Reset()
	printCodexDaemonWarning(&buf, codexDaemonWarning{
		Detected: true,
		PIDs:     []int{4321},
		Message:  "a Codex daemon (app-server, pid 4321) is running",
	})
	got := buf.String()
	if !strings.HasPrefix(got, "Warning:") {
		t.Fatalf("expected 'Warning:' prefix, got %q", got)
	}
	if !strings.Contains(got, "4321") {
		t.Fatalf("expected pid in message, got %q", got)
	}

	// Reloaded -> "Codex daemon:" prefix (informational, not a warning).
	buf.Reset()
	printCodexDaemonWarning(&buf, codexDaemonWarning{
		Detected: true,
		Reloaded: true,
		Message:  "reloaded Codex daemon",
	})
	if got := buf.String(); !strings.HasPrefix(got, "Codex daemon:") {
		t.Fatalf("expected 'Codex daemon:' prefix when reloaded, got %q", got)
	}
}
