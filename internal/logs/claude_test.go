package logs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// claudeAssistantLine is an assistant line in Claude Code's transcript shape,
// including the usage fields newer versions add (issue #126).
func claudeAssistantLine(ts, msgID, reqID, model string, in, cacheCreate, cacheRead, out int64) string {
	return fmt.Sprintf(`{"parentUuid":"p-%s","isSidechain":false,"userType":"external","cwd":"/work/proj","sessionId":"sess-1","version":"2.1.0","type":"assistant","message":{"id":%q,"type":"message","role":"assistant","model":%q,"content":[{"type":"text","text":"done"}],"stop_reason":null,"usage":{"input_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d,"output_tokens":%d,"output_tokens_details":{"reasoning_tokens":0},"server_tool_use":{"web_search_requests":0},"service_tier":"standard"}},"requestId":%q,"uuid":"u-%s","timestamp":%q}`,
		msgID, msgID, model, in, cacheCreate, cacheRead, out, reqID, msgID, ts)
}

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestNewClaudeScannerDefaultRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	got := NewClaudeScanner().logDirs
	want := []string{filepath.Join(home, ".config", "claude", "projects"), filepath.Join(home, ".claude", "projects")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("default roots = %q, want %q", got, want)
	}

	config := filepath.Join(home, "isolated")
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	scanner := NewClaudeScanner()
	if len(scanner.logDirs) != 1 || scanner.LogDir() != filepath.Join(config, "projects") {
		t.Fatalf("CLAUDE_CONFIG_DIR roots = %q, want only its projects dir", scanner.logDirs)
	}
}

func TestNewClaudeScannerWithDir(t *testing.T) {
	customDir := "/custom/projects"
	if got := NewClaudeScannerWithDir(customDir).LogDir(); got != customDir {
		t.Errorf("LogDir() = %q, want %q", got, customDir)
	}
}

func TestClaudeScanner_ScanMissingAndEmptyDirectory(t *testing.T) {
	for _, dir := range []string{filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
		result, err := NewClaudeScannerWithDir(dir).Scan(context.Background(), "", time.Time{})
		if err != nil {
			t.Fatalf("Scan(%s) error = %v, want nil", dir, err)
		}
		if result.Provider != "claude" || len(result.Entries) != 0 {
			t.Fatalf("Scan(%s) = %+v, want an empty claude result", dir, result)
		}
	}
}

// The reported symptom: `caam cost tokens claude` returned [] on a host with
// fresh transcripts, because the scanner read a directory Claude Code never
// writes and expected usage at the top level of each line (issue #126).
func TestClaudeScannerReadsCodeTranscripts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	now := time.Now().UTC()
	at := func(ago time.Duration) string { return now.Add(-ago).Format("2006-01-02T15:04:05.000Z") }

	session := filepath.Join(home, ".claude", "projects", "-work-proj", "sess-1.jsonl")
	writeTranscript(t, session,
		`{"type":"summary","summary":"Fix the build","leafUuid":"u-1"}`,
		`{"parentUuid":null,"type":"user","message":{"role":"user","content":"mention usage of the assistant"},"uuid":"u-0","timestamp":"`+at(3*time.Hour)+`"}`,
		// One response written as two lines (thinking, then text): counted once,
		// with the complete output count.
		claudeAssistantLine(at(3*time.Hour), "msg_a", "req_a", "claude-opus-5-5", 10, 1000, 20000, 1),
		claudeAssistantLine(at(3*time.Hour), "msg_a", "req_a", "claude-opus-5-5", 10, 1000, 20000, 400),
		claudeAssistantLine(at(2*time.Hour), "msg_b", "req_b", "claude-haiku-4-5", 5, 0, 300, 50),
		// Claude Code's placeholder for an aborted request carries no usage.
		`{"type":"assistant","message":{"id":"synthetic","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0}},"timestamp":"`+at(time.Hour)+`"}`,
		// Outside the window.
		claudeAssistantLine(at(72*time.Hour), "msg_old", "req_old", "claude-opus-5-5", 999, 0, 0, 999),
	)
	// Subagent transcripts live in a nested directory.
	writeTranscript(t, filepath.Join(home, ".claude", "projects", "-work-proj", "sess-1", "subagents", "agent-1.jsonl"),
		claudeAssistantLine(at(time.Hour), "msg_c", "req_c", "claude-haiku-4-5", 7, 0, 100, 30),
	)
	// The XDG location is read too.
	writeTranscript(t, filepath.Join(home, ".config", "claude", "projects", "-other", "sess-2.jsonl"),
		claudeAssistantLine(at(30*time.Minute), "msg_d", "req_d", "claude-opus-5-5", 3, 0, 0, 5),
	)

	result, err := NewClaudeScanner().Scan(context.Background(), "", now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.ParseErrors != 0 || len(result.Entries) != 4 {
		t.Fatalf("entries=%d parse_errors=%d, want 4 responses and no errors", len(result.Entries), result.ParseErrors)
	}
	usage := result.TokenUsage()
	opus, haiku := usage.ByModel["claude-opus-5-5"], usage.ByModel["claude-haiku-4-5"]
	if opus == nil || haiku == nil {
		t.Fatalf("ByModel = %+v, want opus and haiku", usage.ByModel)
	}
	if opus.InputTokens != 13 || opus.OutputTokens != 405 || opus.TotalTokens != 21418 {
		t.Fatalf("opus = %+v, want input 13, output 405, total 21418", opus)
	}
	if haiku.InputTokens != 12 || haiku.OutputTokens != 80 || haiku.TotalTokens != 492 {
		t.Fatalf("haiku = %+v, want input 12, output 80, total 492", haiku)
	}
	if usage.CacheReadTokens != 20400 || usage.CacheCreateTokens != 1000 {
		t.Fatalf("cache read=%d create=%d, want 20400 and 1000", usage.CacheReadTokens, usage.CacheCreateTokens)
	}
	for _, entry := range result.Entries {
		if entry.Timestamp.IsZero() || entry.ConversationID != "sess-1" || entry.MessageID == "" {
			t.Fatalf("entry lost its identity or time: %+v", entry)
		}
	}
}

func TestClaudeScannerHonorsConfigDirAndSymlinkedRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	ts := time.Now().UTC().Format(time.RFC3339Nano)

	// ~/.config/claude is a symlink to ~/.claude: one store, counted once.
	writeTranscript(t, filepath.Join(home, ".claude", "projects", "p", "s.jsonl"),
		claudeAssistantLine(ts, "msg_1", "req_1", "claude-opus-5-5", 1, 0, 0, 1))
	if err := os.MkdirAll(filepath.Join(home, ".config"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, ".claude"), filepath.Join(home, ".config", "claude")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	result, err := NewClaudeScanner().Scan(context.Background(), "", time.Time{})
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("symlinked default roots: entries=%v err=%v, want 1", len(result.Entries), err)
	}

	// An explicit CLAUDE_CONFIG_DIR is the only store, as for Claude Code.
	isolated := filepath.Join(home, "isolated")
	writeTranscript(t, filepath.Join(isolated, "projects", "p", "s.jsonl"),
		claudeAssistantLine(ts, "msg_2", "req_2", "claude-opus-5-5", 2, 0, 0, 2),
		claudeAssistantLine(ts, "msg_3", "req_3", "claude-opus-5-5", 3, 0, 0, 3))
	t.Setenv("CLAUDE_CONFIG_DIR", isolated)
	result, err = NewClaudeScanner().Scan(context.Background(), "", time.Time{})
	if err != nil || len(result.Entries) != 2 {
		t.Fatalf("CLAUDE_CONFIG_DIR: entries=%v err=%v, want 2", len(result.Entries), err)
	}
}

// A transcript line far beyond bufio.Scanner's old 1 MiB limit (inline tool
// output, pasted images) used to end the scan of its whole file.
func TestClaudeScannerSurvivesLongAndMalformedLines(t *testing.T) {
	dir := t.TempDir()
	ts := time.Now().UTC().Format(time.RFC3339Nano)
	big := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", 3<<20) + `"}}`
	writeTranscript(t, filepath.Join(dir, "p", "s.jsonl"),
		big,
		`{"type":"assistant","message":{"usage":`, // truncated write
		claudeAssistantLine(ts, "msg_1", "req_1", "claude-opus-5-5", 1, 0, 0, 2),
	)
	result, err := NewClaudeScannerWithDir(dir).Scan(context.Background(), "", time.Time{})
	if err != nil || len(result.Entries) != 1 || result.ParseErrors != 1 || result.TotalEntries != 3 {
		t.Fatalf("entries=%d parse_errors=%d total=%d err=%v, want 1/1/3", len(result.Entries), result.ParseErrors, result.TotalEntries, err)
	}

	// Past the in-memory bound the line is skipped and counted, not fatal.
	old := maxTranscriptLineBytes
	maxTranscriptLineBytes = 1 << 20
	t.Cleanup(func() { maxTranscriptLineBytes = old })
	result, err = NewClaudeScannerWithDir(dir).Scan(context.Background(), "", time.Time{})
	if err != nil || len(result.Entries) != 1 || result.ParseErrors != 2 || result.TotalEntries != 2 {
		t.Fatalf("over the bound: entries=%d parse_errors=%d total=%d err=%v, want 1/2/2", len(result.Entries), result.ParseErrors, result.TotalEntries, err)
	}
}

func TestClaudeScannerFiltersByTimeAndIgnoresOtherFiles(t *testing.T) {
	dir := t.TempDir()
	cutoff := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)
	writeTranscript(t, filepath.Join(dir, "p", "s.jsonl"),
		claudeAssistantLine("2026-10-15T11:59:59.999Z", "msg_1", "req_1", "m", 1, 0, 0, 1),
		claudeAssistantLine("2026-10-15T12:00:00.000Z", "msg_2", "req_2", "m", 1, 0, 0, 1),
		claudeAssistantLine("not-a-time", "msg_3", "req_3", "m", 1, 0, 0, 1),
	)
	writeTranscript(t, filepath.Join(dir, "p", "notes.json"), claudeAssistantLine("2026-10-15T13:00:00Z", "msg_4", "req_4", "m", 1, 0, 0, 1))
	result, err := NewClaudeScannerWithDir(dir).Scan(context.Background(), "", cutoff)
	if err != nil || len(result.Entries) != 1 || result.Entries[0].MessageID != "msg_2" {
		t.Fatalf("Scan since %s = %+v, %v; want only msg_2", cutoff, result.Entries, err)
	}
}

func TestClaudeScanner_ScanWithCanceledContext(t *testing.T) {
	dir := t.TempDir()
	writeTranscript(t, filepath.Join(dir, "p", "s.jsonl"), claudeAssistantLine("2026-10-15T12:00:00Z", "m", "r", "m", 1, 0, 0, 1))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := NewClaudeScannerWithDir(dir).Scan(ctx, "", time.Time{})
	if !errors.Is(err, context.Canceled) || result == nil {
		t.Fatalf("Scan() with canceled context = %v, %v; want a partial result and context.Canceled", result, err)
	}
}

func TestClaudeScanner_CustomLogDirOverridesDefaults(t *testing.T) {
	custom := t.TempDir()
	writeTranscript(t, filepath.Join(custom, "p", "s.jsonl"), claudeAssistantLine("2026-10-15T12:00:00Z", "m", "r", "m", 1, 0, 0, 1))
	result, err := NewClaudeScannerWithDir("/wrong/default").Scan(context.Background(), custom, time.Time{})
	if err != nil || len(result.Entries) != 1 {
		t.Fatalf("Scan(custom) = %d entries, %v; want 1", len(result.Entries), err)
	}
}

func TestClaudeScannerImplementsScanner(t *testing.T) {
	var _ Scanner = (*ClaudeScanner)(nil)
}
