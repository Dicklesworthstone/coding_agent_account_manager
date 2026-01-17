package cmd

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestNormalizeWeztermText(t *testing.T) {
	input := "\x1b[31mYou've hit your limit\x1b[0m\n" +
		"Conversation compacted · ctrl+o for history\n" +
		"═══════════════════════════════════════════\n"

	got := normalizeWeztermText(input)
	if strings.Contains(got, "\x1b") {
		t.Fatalf("expected ANSI escapes removed, got: %q", got)
	}
	if strings.Contains(got, "═") {
		t.Fatalf("expected box-drawing removed, got: %q", got)
	}
	if !strings.Contains(strings.ToLower(got), "you've hit your limit") {
		t.Fatalf("expected normalized text to contain rate limit message, got: %q", got)
	}
}

func TestMatchWeztermPaneOverride(t *testing.T) {
	override := regexp.MustCompile(`foo\s+bar`)
	match := matchWeztermPane("claude", "foo bar", override)
	if !match.Matched || match.Reason != "override" {
		t.Fatalf("expected override match, got: %+v", match)
	}

	noMatch := matchWeztermPane("claude", "baz", override)
	if noMatch.Matched || noMatch.Reason != "override_no_match" {
		t.Fatalf("expected override no match, got: %+v", noMatch)
	}
}

func TestMatchWeztermPaneRateLimit(t *testing.T) {
	match := matchWeztermPane("claude", "You've hit your limit · resets 2pm", nil)
	if !match.Matched || match.Reason != "rate_limit" {
		t.Fatalf("expected rate limit match, got: %+v", match)
	}
}

func TestMatchWeztermPaneToolMarker(t *testing.T) {
	match := matchWeztermPane("claude", "Claude Code session", nil)
	if !match.Matched || match.Reason != "tool_marker" {
		t.Fatalf("expected tool marker match, got: %+v", match)
	}
}

func TestRunWeztermLoginAllDryRun(t *testing.T) {
	savedLookup := weztermLookupFunc
	savedList := weztermListPanesFunc
	savedGet := weztermGetTextFunc
	savedSend := weztermSendTextFunc
	savedIsTerminal := weztermIsTerminal
	defer func() {
		weztermLookupFunc = savedLookup
		weztermListPanesFunc = savedList
		weztermGetTextFunc = savedGet
		weztermSendTextFunc = savedSend
		weztermIsTerminal = savedIsTerminal
	}()

	weztermLookupFunc = func(string) (string, error) { return "wezterm", nil }
	weztermListPanesFunc = func() ([]weztermPane, error) {
		return []weztermPane{{ID: 1, Title: "one"}, {ID: 2, Title: "two"}}, nil
	}
	weztermGetTextFunc = func(paneID int) (string, error) {
		if paneID == 1 {
			return "You've hit your limit · resets soon", nil
		}
		return "bash prompt", nil
	}
	weztermSendTextFunc = func(int, string) error { return errors.New("unexpected send") }
	weztermIsTerminal = func(int) bool { return false }

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().Bool("yes", true, "")
	cmd.Flags().Bool("dry-run", true, "")
	cmd.Flags().Bool("subscription", false, "")
	cmd.Flags().String("match", "", "")

	if err := runWeztermLoginAll(cmd, []string{"claude"}); err != nil {
		t.Fatalf("runWeztermLoginAll error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "pane 1") {
		t.Fatalf("expected pane 1 in output, got: %s", out)
	}
	if strings.Contains(out, "pane 2") {
		t.Fatalf("did not expect pane 2 in output, got: %s", out)
	}
	if !strings.Contains(out, "rate_limit") {
		t.Fatalf("expected reason in output, got: %s", out)
	}
}

func TestRunWeztermLoginAllNonInteractiveRequiresYes(t *testing.T) {
	savedLookup := weztermLookupFunc
	savedList := weztermListPanesFunc
	savedGet := weztermGetTextFunc
	savedSend := weztermSendTextFunc
	savedIsTerminal := weztermIsTerminal
	defer func() {
		weztermLookupFunc = savedLookup
		weztermListPanesFunc = savedList
		weztermGetTextFunc = savedGet
		weztermSendTextFunc = savedSend
		weztermIsTerminal = savedIsTerminal
	}()

	weztermLookupFunc = func(string) (string, error) { return "wezterm", nil }
	weztermListPanesFunc = func() ([]weztermPane, error) {
		return []weztermPane{{ID: 1, Title: "one"}}, nil
	}
	weztermGetTextFunc = func(int) (string, error) { return "You've hit your limit", nil }
	weztermSendTextFunc = func(int, string) error { return nil }
	weztermIsTerminal = func(int) bool { return false }

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().Bool("yes", false, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("subscription", false, "")
	cmd.Flags().String("match", "", "")

	if err := runWeztermLoginAll(cmd, []string{"claude"}); err == nil {
		t.Fatal("expected error for non-interactive session without --yes or --dry-run")
	}
}

func TestRunWeztermLoginAllSendSummary(t *testing.T) {
	savedLookup := weztermLookupFunc
	savedList := weztermListPanesFunc
	savedGet := weztermGetTextFunc
	savedSend := weztermSendTextFunc
	savedIsTerminal := weztermIsTerminal
	defer func() {
		weztermLookupFunc = savedLookup
		weztermListPanesFunc = savedList
		weztermGetTextFunc = savedGet
		weztermSendTextFunc = savedSend
		weztermIsTerminal = savedIsTerminal
	}()

	var sentPayloads []string
	weztermLookupFunc = func(string) (string, error) { return "wezterm", nil }
	weztermListPanesFunc = func() ([]weztermPane, error) {
		return []weztermPane{{ID: 1, Title: "one"}, {ID: 2, Title: "two"}}, nil
	}
	weztermGetTextFunc = func(int) (string, error) { return "You've hit your limit", nil }
	weztermSendTextFunc = func(paneID int, payload string) error {
		sentPayloads = append(sentPayloads, payload)
		if paneID == 2 {
			return errors.New("send failed")
		}
		return nil
	}
	weztermIsTerminal = func(int) bool { return false }

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().Bool("yes", true, "")
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("subscription", true, "")
	cmd.Flags().String("match", "", "")

	if err := runWeztermLoginAll(cmd, []string{"claude"}); err != nil {
		t.Fatalf("runWeztermLoginAll error: %v", err)
	}

	if len(sentPayloads) != 2 {
		t.Fatalf("expected 2 payloads, got %d", len(sentPayloads))
	}
	if sentPayloads[0] != "/login\n1\n" {
		t.Fatalf("expected subscription payload, got %q", sentPayloads[0])
	}

	out := buf.String()
	if !strings.Contains(out, "Targeted 2 pane(s):") {
		t.Fatalf("expected summary output, got: %s", out)
	}
	if !strings.Contains(out, "1 succeeded") || !strings.Contains(out, "1 failed") {
		t.Fatalf("expected success/failure counts, got: %s", out)
	}
}

func TestExtractClaudeOAuthURLsWrapped(t *testing.T) {
	raw := "Browser didn't open? Use the url below:\n" +
		"https://claude.ai/oauth/authorize?code=true&client_id=abc\n" +
		"&response_type=code&redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback\n" +
		"&scope=org%3Acreate_api_key+user%3Aprofile\n" +
		"Paste code here if prompted >"

	urls := extractClaudeOAuthURLs(raw)
	if len(urls) != 1 {
		t.Fatalf("expected 1 url, got %d", len(urls))
	}
	if strings.Contains(urls[0], "\n") || strings.Contains(urls[0], " ") {
		t.Fatalf("expected wrapped url to be reconstructed, got: %q", urls[0])
	}
	if !strings.HasPrefix(urls[0], claudeOAuthBase) {
		t.Fatalf("expected oauth base, got: %q", urls[0])
	}
}

func TestRunWeztermOAuthReport(t *testing.T) {
	savedLookup := weztermLookupFunc
	savedList := weztermListPanesFunc
	savedGet := weztermGetTextFunc
	savedNow := weztermNow
	defer func() {
		weztermLookupFunc = savedLookup
		weztermListPanesFunc = savedList
		weztermGetTextFunc = savedGet
		weztermNow = savedNow
	}()

	weztermLookupFunc = func(string) (string, error) { return "wezterm", nil }
	weztermListPanesFunc = func() ([]weztermPane, error) {
		return []weztermPane{{ID: 10, Title: "claude"}, {ID: 20, Title: "other"}}, nil
	}
	weztermGetTextFunc = func(paneID int) (string, error) {
		if paneID == 10 {
			return "Claude Code\n" + claudeOAuthBase + "?code=true&client_id=abc", nil
		}
		return "bash prompt", nil
	}
	weztermNow = func() time.Time {
		return time.Date(2026, 1, 17, 4, 0, 0, 0, time.UTC)
	}

	buf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(buf)
	cmd.SetErr(buf)
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("match", "", "")

	if err := runWeztermOAuthReport(cmd, []string{"claude"}); err != nil {
		t.Fatalf("runWeztermOAuthReport error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "10") {
		t.Fatalf("expected pane id in output, got: %s", out)
	}
	if !strings.Contains(out, "2026-01-17T04:00:00Z") {
		t.Fatalf("expected timestamp in output, got: %s", out)
	}
	if !strings.Contains(out, claudeOAuthBase) {
		t.Fatalf("expected oauth url in output, got: %s", out)
	}
	if strings.Contains(out, "\n20\t") || strings.HasPrefix(out, "20\t") {
		t.Fatalf("did not expect pane 20 in output, got: %s", out)
	}
}

func TestWeztermOAuthReportRedactsLogs(t *testing.T) {
	savedLookup := weztermLookupFunc
	savedList := weztermListPanesFunc
	savedGet := weztermGetTextFunc
	savedNow := weztermNow
	savedWriter := weztermDebugWriter
	savedEnv := os.Getenv("CAAM_DEBUG")
	defer func() {
		weztermLookupFunc = savedLookup
		weztermListPanesFunc = savedList
		weztermGetTextFunc = savedGet
		weztermNow = savedNow
		weztermDebugWriter = savedWriter
		if savedEnv == "" {
			_ = os.Unsetenv("CAAM_DEBUG")
		} else {
			_ = os.Setenv("CAAM_DEBUG", savedEnv)
		}
	}()

	_ = os.Setenv("CAAM_DEBUG", "true")
	logBuf := &bytes.Buffer{}
	weztermDebugWriter = logBuf

	weztermLookupFunc = func(string) (string, error) { return "wezterm", nil }
	weztermListPanesFunc = func() ([]weztermPane, error) {
		return []weztermPane{{ID: 1, Title: "claude"}}, nil
	}
	weztermGetTextFunc = func(int) (string, error) {
		return claudeOAuthBase + "?code=true&client_id=abc", nil
	}
	weztermNow = func() time.Time { return time.Unix(0, 0).UTC() }

	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Bool("all", true, "")
	cmd.Flags().String("match", "", "")

	if err := runWeztermOAuthReport(cmd, []string{"claude"}); err != nil {
		t.Fatalf("runWeztermOAuthReport error: %v", err)
	}

	logs := logBuf.String()
	if logs == "" {
		t.Fatal("expected debug logs to be captured")
	}
	if strings.Contains(logs, claudeOAuthBase) {
		t.Fatalf("expected logs to redact urls, got: %s", logs)
	}
}
