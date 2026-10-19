package logs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func codexTokenCount(ts string, last, total *codexTokenUsage) string {
	usage := func(u *codexTokenUsage) string {
		if u == nil {
			return "null"
		}
		return fmt.Sprintf(`{"input_tokens":%d,"cached_input_tokens":%d,"cache_write_input_tokens":%d,"output_tokens":%d,"reasoning_output_tokens":0,"total_tokens":%d}`,
			u.InputTokens, u.CachedInputTokens, u.CacheWriteInputTokens, u.OutputTokens, u.TotalTokens)
	}
	return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":%s,"last_token_usage":%s,"model_context_window":272000},"rate_limits":{"primary":{"used_percent":9.0}}}}`,
		ts, usage(total), usage(last))
}

func TestNewCodexScannerDefaultRoots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	got := NewCodexScanner().logDirs
	want := []string{filepath.Join(home, ".codex", "sessions"), filepath.Join(home, ".codex", "archived_sessions")}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("default roots = %q, want %q", got, want)
	}
	t.Setenv("CODEX_HOME", filepath.Join(home, "codex-alt"))
	if got := NewCodexScanner().LogDir(); got != filepath.Join(home, "codex-alt", "sessions") {
		t.Fatalf("CODEX_HOME root = %q", got)
	}
}

func TestNewCodexScannerWithDir(t *testing.T) {
	if got := NewCodexScannerWithDir("/custom/codex").LogDir(); got != "/custom/codex" {
		t.Errorf("LogDir() = %q", got)
	}
}

func TestCodexScanner_ScanMissingAndEmptyDirectory(t *testing.T) {
	for _, dir := range []string{filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
		result, err := NewCodexScannerWithDir(dir).Scan(context.Background(), "", time.Time{})
		if err != nil || result.Provider != "codex" || len(result.Entries) != 0 {
			t.Fatalf("Scan(%s) = %+v, %v; want an empty codex result", dir, result, err)
		}
	}
}

// The reported symptom: `caam cost tokens codex` returned [] because the
// scanner read $CODEX_HOME/logs, while Codex records usage in session
// rollouts under $CODEX_HOME/sessions (issue #126).
func TestCodexScannerReadsSessionRollouts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", "")
	now := time.Now().UTC()
	at := func(ago time.Duration) string { return now.Add(-ago).Format("2006-01-02T15:04:05.000Z") }

	first := &codexTokenUsage{InputTokens: 1000, CachedInputTokens: 600, CacheWriteInputTokens: 100, OutputTokens: 50, TotalTokens: 1050}
	second := &codexTokenUsage{InputTokens: 2000, CachedInputTokens: 1500, OutputTokens: 80, TotalTokens: 2080}
	running := &codexTokenUsage{InputTokens: 3000, CachedInputTokens: 2100, CacheWriteInputTokens: 100, OutputTokens: 130, TotalTokens: 3130}
	third := &codexTokenUsage{InputTokens: 500, OutputTokens: 20, TotalTokens: 520}
	finalTotal := &codexTokenUsage{InputTokens: 3500, CachedInputTokens: 2100, CacheWriteInputTokens: 100, OutputTokens: 150, TotalTokens: 3650}

	rollout := filepath.Join(home, ".codex", "sessions", now.Format("2006"), now.Format("01"), now.Format("02"), "rollout-2026-10-15T16-38-00-abc.jsonl")
	writeTranscript(t, rollout,
		`{"timestamp":"`+at(3*time.Hour)+`","type":"session_meta","payload":{"id":"sess-codex","cwd":"/work/proj","cli_version":"0.48.0","instructions":"..."}}`,
		`{"timestamp":"`+at(3*time.Hour)+`","type":"turn_context","payload":{"cwd":"/work/proj","approval_policy":"on-request","model":"gpt-5-codex","effort":"high"}}`,
		`{"timestamp":"`+at(3*time.Hour)+`","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"token_count please"}]}}`,
		// A rate-limit-only update carries no usage.
		`{"timestamp":"`+at(3*time.Hour)+`","type":"event_msg","payload":{"type":"token_count","info":null,"rate_limits":{"primary":{"used_percent":8.0}}}}`,
		codexTokenCount(at(3*time.Hour), first, first),
		codexTokenCount(at(2*time.Hour), second, running),
		// Codex re-emits the same running total: not new usage.
		codexTokenCount(at(2*time.Hour), second, running),
		`{"timestamp":"`+at(time.Hour)+`","type":"turn_context","payload":{"model":"gpt-5-mini"}}`,
		// Older rollouts may omit last_token_usage: use the running-total delta.
		codexTokenCount(at(time.Hour), nil, finalTotal),
	)
	// Archived sessions count too; this one is outside the window.
	writeTranscript(t, filepath.Join(home, ".codex", "archived_sessions", "rollout-old.jsonl"),
		`{"timestamp":"`+at(72*time.Hour)+`","type":"turn_context","payload":{"model":"gpt-5-codex"}}`,
		codexTokenCount(at(72*time.Hour), third, third),
		`{"timestamp":"`+at(10*time.Minute)+`","type":"turn_context","payload":{"model":"gpt-5-codex"}}`,
		codexTokenCount(at(10*time.Minute), third, &codexTokenUsage{InputTokens: 1000, OutputTokens: 40, TotalTokens: 1040}),
	)

	result, err := NewCodexScanner().Scan(context.Background(), "", now.Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.ParseErrors != 0 || len(result.Entries) != 4 {
		t.Fatalf("entries=%d parse_errors=%d, want 4 responses and no errors", len(result.Entries), result.ParseErrors)
	}
	usage := result.TokenUsage()
	codex, mini := usage.ByModel["gpt-5-codex"], usage.ByModel["gpt-5-mini"]
	if codex == nil || mini == nil {
		t.Fatalf("ByModel = %+v, want gpt-5-codex and gpt-5-mini", usage.ByModel)
	}
	// Codex's own totals are input + output; cached and cache-write input are
	// part of input_tokens and must not be counted twice.
	if codex.TotalTokens != 1050+2080+520 || mini.TotalTokens != 3650-3130 {
		t.Fatalf("totals codex=%d mini=%d, want %d and %d", codex.TotalTokens, mini.TotalTokens, 1050+2080+520, 3650-3130)
	}
	if usage.CacheReadTokens != 600+1500 || usage.CacheCreateTokens != 100 {
		t.Fatalf("cache read=%d write=%d, want 2100 and 100", usage.CacheReadTokens, usage.CacheCreateTokens)
	}
	if codex.InputTokens != 300+500+500 || codex.OutputTokens != 50+80+20 {
		t.Fatalf("codex uncached input=%d output=%d, want 1300 and 150", codex.InputTokens, codex.OutputTokens)
	}
	if result.Entries[0].ConversationID != "sess-codex" || result.Entries[0].Timestamp.IsZero() {
		t.Fatalf("entry lost its session or time: %+v", result.Entries[0])
	}
}

func TestCodexScannerSurvivesMalformedLines(t *testing.T) {
	dir := t.TempDir()
	u := &codexTokenUsage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}
	writeTranscript(t, filepath.Join(dir, "2026", "10", "15", "rollout-x.jsonl"),
		`{"timestamp":"2026-10-15T12:00:00Z","type":"event_msg","payload":{"type":"token_count","info":`, // truncated write
		`{"timestamp":"2026-10-15T12:00:01Z","type":"response_item","payload":{"type":"function_call_output","output":"`+strings.Repeat("y", 2<<20)+`"}}`,
		codexTokenCount("2026-10-15T12:00:02Z", u, u),
	)
	result, err := NewCodexScannerWithDir(dir).Scan(context.Background(), "", time.Time{})
	if err != nil || len(result.Entries) != 1 || result.ParseErrors != 1 || result.TotalEntries != 3 {
		t.Fatalf("entries=%d parse_errors=%d total=%d err=%v, want 1/1/3", len(result.Entries), result.ParseErrors, result.TotalEntries, err)
	}
	if result.Entries[0].Model != "" || result.Entries[0].TotalTokens != 15 {
		t.Fatalf("entry = %+v, want 15 tokens and no model before any turn_context", result.Entries[0])
	}
}

func TestCodexScannerImplementsScanner(t *testing.T) {
	var _ Scanner = (*CodexScanner)(nil)
}
