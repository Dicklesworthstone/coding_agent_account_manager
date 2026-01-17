package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var weztermCmd = &cobra.Command{
	Use:   "wezterm",
	Short: "WezTerm helpers for active sessions",
	Long: `Helpers for WezTerm-muxed sessions.

These commands can broadcast actions (like /login) to panes that match
your target tool, reducing manual repetition across many sessions.`,
}

var weztermLoginAllCmd = &cobra.Command{
	Use:   "login-all <tool>",
	Short: "Send /login to all matching WezTerm panes",
	Long: `Send /login to all matching WezTerm panes for the specified tool.

By default, panes are matched by scanning recent output for a tool-specific
pattern. Use --all to broadcast to every pane.

Examples:
  caam wezterm login-all claude
  caam wezterm login-all claude --subscription
  caam wezterm login-all claude --all --yes
`,
	Args: cobra.ExactArgs(1),
	RunE: runWeztermLoginAll,
}

var weztermOAuthReportCmd = &cobra.Command{
	Use:   "oauth-urls <tool>",
	Short: "Report OAuth URLs found in WezTerm panes",
	Long: `Scan WezTerm panes for OAuth URLs and print a copy-friendly report.

The report includes pane id and scan timestamp alongside each URL.`,
	Args: cobra.ExactArgs(1),
	RunE: runWeztermOAuthReport,
}

func init() {
	rootCmd.AddCommand(weztermCmd)
	weztermCmd.AddCommand(weztermLoginAllCmd)
	weztermCmd.AddCommand(weztermOAuthReportCmd)

	weztermLoginAllCmd.Flags().Bool("all", false, "broadcast to all panes (skip matching)")
	weztermLoginAllCmd.Flags().Bool("yes", false, "skip confirmation prompt")
	weztermLoginAllCmd.Flags().Bool("dry-run", false, "show target panes without sending")
	weztermLoginAllCmd.Flags().Bool("subscription", false, "also send '1' to choose subscription login")
	weztermLoginAllCmd.Flags().String("match", "", "regex pattern to match panes (overrides default)")

	weztermOAuthReportCmd.Flags().Bool("all", false, "scan all panes (skip matching)")
	weztermOAuthReportCmd.Flags().String("match", "", "regex pattern to match panes (overrides default)")
}

type weztermPane struct {
	ID    int
	Title string
}

type weztermTarget struct {
	Pane   weztermPane
	Reason string
}

var (
	weztermLookupFunc              = exec.LookPath
	weztermListPanesFunc           = weztermListPanes
	weztermGetTextFunc             = weztermGetText
	weztermSendTextFunc            = weztermSendText
	weztermIsTerminal              = term.IsTerminal
	weztermNow                     = time.Now
	weztermDebugWriter   io.Writer = os.Stderr
)

func runWeztermLoginAll(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(strings.TrimSpace(args[0]))
	switch tool {
	case "claude", "codex", "gemini":
		// ok
	default:
		return fmt.Errorf("unknown tool: %s (supported: claude, codex, gemini)", tool)
	}

	if _, err := weztermLookupFunc("wezterm"); err != nil {
		return fmt.Errorf("wezterm CLI not found in PATH")
	}

	all, _ := cmd.Flags().GetBool("all")
	yes, _ := cmd.Flags().GetBool("yes")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	subscription, _ := cmd.Flags().GetBool("subscription")
	matchOverride, _ := cmd.Flags().GetString("match")

	logger := weztermDebugLogger()

	panes, err := weztermListPanesFunc()
	if err != nil {
		return err
	}
	if len(panes) == 0 {
		return fmt.Errorf("no wezterm panes found")
	}

	var matcher *regexp.Regexp
	if !all && matchOverride != "" {
		matcher, err = regexp.Compile(matchOverride)
		if err != nil {
			return fmt.Errorf("invalid match pattern: %w", err)
		}
	}

	var targets []weztermTarget
	for _, pane := range panes {
		if all {
			targets = append(targets, weztermTarget{Pane: pane, Reason: "all"})
			continue
		}
		text, err := weztermGetTextFunc(pane.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("wezterm pane read failed", "pane_id", pane.ID, "title", pane.Title, "error", err)
			}
			continue
		}
		match := matchWeztermPane(tool, text, matcher)
		if logger != nil {
			logger.Debug("pane scan", "pane_id", pane.ID, "title", pane.Title, "matched", match.Matched, "reason", match.Reason, "tool", tool)
		}
		if match.Matched {
			targets = append(targets, weztermTarget{Pane: pane, Reason: match.Reason})
		}
	}

	if len(targets) == 0 {
		return fmt.Errorf("no panes matched (use --all to force)")
	}

	if dryRun {
		fmt.Fprintf(cmd.OutOrStdout(), "Would send /login to %d pane(s):\n", len(targets))
		for _, target := range targets {
			fmt.Fprintf(cmd.OutOrStdout(), "  pane %d %s (%s)\n", target.Pane.ID, target.Pane.Title, target.Reason)
		}
		return nil
	}

	if !yes && !dryRun {
		if !weztermIsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("non-interactive session: use --yes or --dry-run")
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Send /login to %d pane(s)? [y/N]: ", len(targets))
		var resp string
		fmt.Fscanln(os.Stdin, &resp)
		resp = strings.TrimSpace(strings.ToLower(resp))
		if resp != "y" && resp != "yes" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled")
			return nil
		}
	}

	payload := "/login\n"
	if subscription {
		payload += "1\n"
	}

	successCount := 0
	failCount := 0
	for _, target := range targets {
		if err := weztermSendTextFunc(target.Pane.ID, payload); err != nil {
			failCount++
			fmt.Fprintf(cmd.ErrOrStderr(), "pane %d: %v\n", target.Pane.ID, err)
			continue
		}
		successCount++
	}

	if failCount > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Targeted %d pane(s): %d succeeded, %d failed.\n", len(targets), successCount, failCount)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Targeted %d pane(s): %d succeeded.\n", len(targets), successCount)
	}
	return nil
}

func runWeztermOAuthReport(cmd *cobra.Command, args []string) error {
	tool := strings.ToLower(strings.TrimSpace(args[0]))
	if tool != "claude" {
		return fmt.Errorf("oauth url reporting currently supports only claude")
	}

	if _, err := weztermLookupFunc("wezterm"); err != nil {
		return fmt.Errorf("wezterm CLI not found in PATH")
	}

	all, _ := cmd.Flags().GetBool("all")
	matchOverride, _ := cmd.Flags().GetString("match")

	logger := weztermDebugLogger()

	panes, err := weztermListPanesFunc()
	if err != nil {
		return err
	}
	if len(panes) == 0 {
		return fmt.Errorf("no wezterm panes found")
	}

	var matcher *regexp.Regexp
	if !all && matchOverride != "" {
		matcher, err = regexp.Compile(matchOverride)
		if err != nil {
			return fmt.Errorf("invalid match pattern: %w", err)
		}
	}

	scannedAt := weztermNow().Format(time.RFC3339)
	type paneURL struct {
		Pane weztermPane
		URL  string
	}

	var results []paneURL

	for _, pane := range panes {
		if !all {
			text, err := weztermGetTextFunc(pane.ID)
			if err != nil {
				if logger != nil {
					logger.Warn("wezterm pane read failed", "pane_id", pane.ID, "title", pane.Title, "error", err)
				}
				continue
			}
			match := matchWeztermPane(tool, text, matcher)
			if !match.Matched {
				continue
			}
			urls := extractClaudeOAuthURLs(text)
			if len(urls) == 0 {
				continue
			}
			for _, url := range urls {
				results = append(results, paneURL{Pane: pane, URL: url})
			}
			if logger != nil {
				logger.Debug("oauth urls found", "pane_id", pane.ID, "title", pane.Title, "count", len(urls))
			}
			continue
		}

		text, err := weztermGetTextFunc(pane.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("wezterm pane read failed", "pane_id", pane.ID, "title", pane.Title, "error", err)
			}
			continue
		}
		urls := extractClaudeOAuthURLs(text)
		if len(urls) == 0 {
			continue
		}
		for _, url := range urls {
			results = append(results, paneURL{Pane: pane, URL: url})
		}
		if logger != nil {
			logger.Debug("oauth urls found", "pane_id", pane.ID, "title", pane.Title, "count", len(urls))
		}
	}

	if len(results) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No OAuth URLs found.")
		return nil
	}

	for _, item := range results {
		title := strings.TrimSpace(item.Pane.Title)
		if title == "" {
			title = "-"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%d\t%s\t%s\t# %s\n", item.Pane.ID, scannedAt, item.URL, title)
	}

	return nil
}

type matchResult struct {
	Matched bool
	Reason  string
}

var (
	ansiEscapeRe  = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	oscEscapeRe   = regexp.MustCompile(`\x1b\][^\a]*(\a|\x1b\\)`)
	claudeMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bclaude\b`),
		regexp.MustCompile(`(?i)claude\s+code`),
		regexp.MustCompile(`(?i)anthropic`),
	}
	codexMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bcodex\b`),
		regexp.MustCompile(`(?i)openai`),
	}
	geminiMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)\bgemini\b`),
		regexp.MustCompile(`(?i)google\s+ai`),
	}
	rateLimitMarkers = []*regexp.Regexp{
		regexp.MustCompile(`(?i)you'?ve hit your limit`),
		regexp.MustCompile(`(?i)usage limit`),
		regexp.MustCompile(`(?i)rate limit`),
		regexp.MustCompile(`(?i)too many requests`),
		regexp.MustCompile(`(?i)resource_exhausted`),
		regexp.MustCompile(`(?i)\b429\b`),
	}
)

func matchWeztermPane(tool, text string, override *regexp.Regexp) matchResult {
	normalized := normalizeWeztermText(text)
	if override != nil {
		if override.MatchString(normalized) {
			return matchResult{Matched: true, Reason: "override"}
		}
		return matchResult{Matched: false, Reason: "override_no_match"}
	}
	if matchesAny(rateLimitMarkers, normalized) {
		return matchResult{Matched: true, Reason: "rate_limit"}
	}
	switch tool {
	case "claude":
		if matchesAny(claudeMarkers, normalized) {
			return matchResult{Matched: true, Reason: "tool_marker"}
		}
	case "codex":
		if matchesAny(codexMarkers, normalized) {
			return matchResult{Matched: true, Reason: "tool_marker"}
		}
	case "gemini":
		if matchesAny(geminiMarkers, normalized) {
			return matchResult{Matched: true, Reason: "tool_marker"}
		}
	}
	return matchResult{Matched: false, Reason: "no_match"}
}

func matchesAny(patterns []*regexp.Regexp, text string) bool {
	for _, re := range patterns {
		if re.MatchString(text) {
			return true
		}
	}
	return false
}

func normalizeWeztermText(text string) string {
	cleaned := cleanWeztermText(text)
	fields := strings.Fields(cleaned)
	return strings.Join(fields, " ")
}

func cleanWeztermText(text string) string {
	cleaned := oscEscapeRe.ReplaceAllString(text, "")
	cleaned = ansiEscapeRe.ReplaceAllString(cleaned, "")
	cleaned = stripBoxDrawing(cleaned)
	cleaned = strings.ReplaceAll(cleaned, "\u00a0", " ")
	cleaned = strings.Map(func(r rune) rune {
		if r < 32 && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, cleaned)
	return cleaned
}

func stripBoxDrawing(text string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x2500 && r <= 0x257F: // box drawing
			return ' '
		case r >= 0x2580 && r <= 0x259F: // block elements
			return ' '
		default:
			return r
		}
	}, text)
}

func weztermDebugLogger() *slog.Logger {
	if os.Getenv("CAAM_DEBUG") == "" {
		return nil
	}
	return slog.New(slog.NewJSONHandler(weztermDebugWriter, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

const claudeOAuthBase = "https://claude.ai/oauth/authorize"

func extractClaudeOAuthURLs(text string) []string {
	cleaned := cleanWeztermText(text)
	var urls []string
	seen := make(map[string]struct{})

	start := 0
	for {
		idx := strings.Index(cleaned[start:], claudeOAuthBase)
		if idx == -1 {
			break
		}
		idx += start
		url := readWrappedURL(cleaned[idx:])
		if url != "" {
			if _, ok := seen[url]; !ok {
				seen[url] = struct{}{}
				urls = append(urls, url)
			}
		}
		start = idx + len(claudeOAuthBase)
	}

	return urls
}

func readWrappedURL(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isURLChar(r) {
			b.WriteRune(r)
			continue
		}
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			continue
		}
		break
	}
	return b.String()
}

func isURLChar(r rune) bool {
	if r >= 'a' && r <= 'z' {
		return true
	}
	if r >= 'A' && r <= 'Z' {
		return true
	}
	if r >= '0' && r <= '9' {
		return true
	}
	switch r {
	case '-', '.', '_', '~', ':', '/', '?', '#', '[', ']', '@',
		'!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', '%':
		return true
	default:
		return false
	}
}

func weztermListPanes() ([]weztermPane, error) {
	cmd := exec.Command("wezterm", "cli", "list", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("wezterm cli list failed: %w", err)
	}

	var raw []map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse wezterm list json: %w", err)
	}

	var panes []weztermPane
	for _, item := range raw {
		id := getIntField(item, "pane_id", "paneId")
		if id == 0 {
			continue
		}
		title := getStringField(item, "title", "tab_title", "domain_name", "workspace")
		panes = append(panes, weztermPane{ID: id, Title: title})
	}
	return panes, nil
}

func weztermGetText(paneID int) (string, error) {
	args := []string{"cli", "get-text", "--pane-id", strconv.Itoa(paneID), "--start-line", "-200"}
	cmd := exec.Command("wezterm", args...)
	out, err := cmd.Output()
	if err == nil {
		return string(out), nil
	}
	// Fallback without --start-line for older wezterm.
	cmd = exec.Command("wezterm", "cli", "get-text", "--pane-id", strconv.Itoa(paneID))
	out, err2 := cmd.Output()
	if err2 != nil {
		return "", fmt.Errorf("wezterm get-text failed: %w", err)
	}
	return string(out), nil
}

func weztermSendText(paneID int, text string) error {
	cmd := exec.Command("wezterm", "cli", "send-text", "--pane-id", strconv.Itoa(paneID), "--no-paste", text)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return errors.New(msg)
		}
		return err
	}
	return nil
}

func getIntField(m map[string]any, keys ...string) int {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			switch n := v.(type) {
			case float64:
				return int(n)
			case int:
				return n
			case int64:
				return int(n)
			case json.Number:
				if i, err := n.Int64(); err == nil {
					return int(i)
				}
			case string:
				if i, err := strconv.Atoi(n); err == nil {
					return i
				}
			}
		}
	}
	return 0
}

func getStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}
