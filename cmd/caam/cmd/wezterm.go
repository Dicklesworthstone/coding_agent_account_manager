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

var weztermRecoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Interactive batch recovery for rate-limited Claude sessions",
	Long: `Monitor WezTerm panes for rate limits and coordinate authentication recovery.

This command scans all WezTerm panes, detects their current state in the
authentication flow, and provides interactive controls for batch recovery.

State Machine (per pane):
  IDLE            - normal output, no action needed
  RATE_LIMITED    - rate limit detected → can inject /login
  AWAITING_SELECT - login prompt shown → can select subscription (1)
  AWAITING_URL    - OAuth URL detected → waiting for code
  CODE_READY      - code available → can inject code
  RESUMING        - login success → can inject resume prompt
  FAILED          - error occurred → can retry

Interactive Controls:
  r  - refresh pane states
  l  - inject /login to rate-limited panes
  s  - select subscription (1) on awaiting panes
  c  - inject codes to code-ready panes
  p  - inject resume prompt to resuming panes
  a  - auto-advance all panes one step
  q  - quit

Examples:
  # Interactive recovery session
  caam wezterm recover

  # One-shot status display
  caam wezterm recover --status

  # Auto-advance all panes without interaction
  caam wezterm recover --auto --yes
`,
	RunE: runWeztermRecover,
}

func init() {
	rootCmd.AddCommand(weztermCmd)
	weztermCmd.AddCommand(weztermLoginAllCmd)
	weztermCmd.AddCommand(weztermOAuthReportCmd)
	weztermCmd.AddCommand(weztermRecoverCmd)

	weztermLoginAllCmd.Flags().Bool("all", false, "broadcast to all panes (skip matching)")
	weztermLoginAllCmd.Flags().Bool("force", false, "skip confirmation prompt")
	weztermLoginAllCmd.Flags().Bool("yes", false, "skip confirmation prompt (alias for --force)")
	weztermLoginAllCmd.Flags().Bool("dry-run", false, "show target panes without sending")
	weztermLoginAllCmd.Flags().Bool("subscription", false, "also send '1' to choose subscription login")
	weztermLoginAllCmd.Flags().String("match", "", "regex pattern to match panes (overrides default)")

	weztermOAuthReportCmd.Flags().Bool("all", false, "scan all panes (skip matching)")
	weztermOAuthReportCmd.Flags().String("match", "", "regex pattern to match panes (overrides default)")

	weztermRecoverCmd.Flags().Bool("status", false, "show status and exit (no interaction)")
	weztermRecoverCmd.Flags().Bool("auto", false, "auto-advance all panes one step")
	weztermRecoverCmd.Flags().Bool("force", false, "skip confirmation for auto mode")
	weztermRecoverCmd.Flags().Bool("yes", false, "skip confirmation (alias for --force)")
	weztermRecoverCmd.Flags().Bool("watch", false, "continuously watch and refresh (with --status)")
	weztermRecoverCmd.Flags().Duration("interval", 2*time.Second, "refresh interval for watch mode")
	weztermRecoverCmd.Flags().String("resume-prompt", "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n", "prompt to inject after successful auth")
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
		return fmt.Errorf("wezterm CLI not found in PATH; install from https://wezfurlong.org/wezterm/install/")
	}

	all, _ := cmd.Flags().GetBool("all")
	yes, _ := cmd.Flags().GetBool("yes")
	force, _ := cmd.Flags().GetBool("force")
	yes = yes || force
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	subscription, _ := cmd.Flags().GetBool("subscription")
	matchOverride, _ := cmd.Flags().GetString("match")

	logger := weztermDebugLogger()

	panes, err := weztermListPanesFunc()
	if err != nil {
		return err
	}
	if len(panes) == 0 {
		return fmt.Errorf("no wezterm panes found; start wezterm first or use 'wezterm cli list-clients' to verify")
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
		return fmt.Errorf("wezterm CLI not found in PATH; install from https://wezfurlong.org/wezterm/install/")
	}

	all, _ := cmd.Flags().GetBool("all")
	matchOverride, _ := cmd.Flags().GetString("match")

	logger := weztermDebugLogger()

	panes, err := weztermListPanesFunc()
	if err != nil {
		return err
	}
	if len(panes) == 0 {
		return fmt.Errorf("no wezterm panes found; start wezterm first or use 'wezterm cli list-clients' to verify")
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

// RecoverState represents the state of a pane in the recovery flow.
type RecoverState int

const (
	RecoverIdle RecoverState = iota
	RecoverRateLimited
	RecoverAwaitingSelect
	RecoverAwaitingURL
	RecoverCodeReady
	RecoverResuming
	RecoverFailed
)

func (s RecoverState) String() string {
	switch s {
	case RecoverIdle:
		return "IDLE"
	case RecoverRateLimited:
		return "RATE_LIMITED"
	case RecoverAwaitingSelect:
		return "AWAITING_SELECT"
	case RecoverAwaitingURL:
		return "AWAITING_URL"
	case RecoverCodeReady:
		return "CODE_READY"
	case RecoverResuming:
		return "RESUMING"
	case RecoverFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// RecoverPaneState tracks the recovery state for a single pane.
type RecoverPaneState struct {
	Pane        weztermPane
	State       RecoverState
	MatchReason string
	OAuthURL    string
	Code        string
	Error       string
	LastAction  time.Time
	Cooldown    time.Duration
}

// IsOnCooldown returns true if the pane is still on cooldown from last action.
func (r *RecoverPaneState) IsOnCooldown() bool {
	if r.Cooldown == 0 {
		return false
	}
	return time.Since(r.LastAction) < r.Cooldown
}

// Recovery state detection patterns (using coordinator patterns).
var recoverPatterns = struct {
	RateLimit    *regexp.Regexp
	SelectMethod *regexp.Regexp
	OAuthURL     *regexp.Regexp
	PastePrompt  *regexp.Regexp
	LoginSuccess *regexp.Regexp
	LoginFailed  *regexp.Regexp
}{
	RateLimit:    regexp.MustCompile(`(?i)you'?ve hit your limit.*resets`),
	SelectMethod: regexp.MustCompile(`(?i)select login method:`),
	OAuthURL:     regexp.MustCompile(`https://claude\.ai/oauth/authorize\?[^\s]+`),
	PastePrompt:  regexp.MustCompile(`(?i)paste code here if prompted`),
	LoginSuccess: regexp.MustCompile(`(?i)(logged in as|successfully authenticated|welcome back)`),
	LoginFailed:  regexp.MustCompile(`(?i)(login failed|authentication error|invalid code|expired|error signing)`),
}

// detectRecoverState analyzes pane output and returns the recovery state.
func detectRecoverState(text string) (RecoverState, string, string) {
	normalized := normalizeWeztermText(text)

	// Check for login success first (highest priority)
	if recoverPatterns.LoginSuccess.MatchString(normalized) {
		return RecoverResuming, "login_success", ""
	}

	// Check for login failure
	if recoverPatterns.LoginFailed.MatchString(normalized) {
		return RecoverFailed, "login_failed", ""
	}

	// Check for OAuth URL
	if url := recoverPatterns.OAuthURL.FindString(normalized); url != "" {
		return RecoverAwaitingURL, "oauth_url", url
	}

	// Check for paste prompt (URL was shown)
	if recoverPatterns.PastePrompt.MatchString(normalized) {
		url := recoverPatterns.OAuthURL.FindString(normalized)
		return RecoverAwaitingURL, "paste_prompt", url
	}

	// Check for method selection prompt
	if recoverPatterns.SelectMethod.MatchString(normalized) {
		return RecoverAwaitingSelect, "select_method", ""
	}

	// Check for rate limit
	if recoverPatterns.RateLimit.MatchString(normalized) {
		return RecoverRateLimited, "rate_limit", ""
	}

	return RecoverIdle, "", ""
}

func runWeztermRecover(cmd *cobra.Command, args []string) error {
	if _, err := weztermLookupFunc("wezterm"); err != nil {
		return fmt.Errorf("wezterm CLI not found in PATH; install from https://wezfurlong.org/wezterm/install/")
	}

	statusOnly, _ := cmd.Flags().GetBool("status")
	autoMode, _ := cmd.Flags().GetBool("auto")
	yes, _ := cmd.Flags().GetBool("yes")
	forceFlag, _ := cmd.Flags().GetBool("force")
	yes = yes || forceFlag
	watchMode, _ := cmd.Flags().GetBool("watch")
	interval, _ := cmd.Flags().GetDuration("interval")
	resumePrompt, _ := cmd.Flags().GetString("resume-prompt")

	logger := weztermDebugLogger()

	// Scan panes and detect states
	states, err := scanRecoverStates(logger)
	if err != nil {
		return err
	}

	if len(states) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No WezTerm panes found.")
		return nil
	}

	// Status-only mode
	if statusOnly {
		if watchMode {
			return runWatchMode(cmd, interval, logger)
		}
		printRecoverTable(cmd.OutOrStdout(), states)
		return nil
	}

	// Auto mode - advance all panes one step
	if autoMode {
		return runAutoRecover(cmd, states, yes, resumePrompt, logger)
	}

	// Interactive mode
	return runInteractiveRecover(cmd, states, resumePrompt, logger)
}

func scanRecoverStates(logger *slog.Logger) ([]*RecoverPaneState, error) {
	panes, err := weztermListPanesFunc()
	if err != nil {
		return nil, fmt.Errorf("list panes: %w", err)
	}

	var states []*RecoverPaneState
	for _, pane := range panes {
		text, err := weztermGetTextFunc(pane.ID)
		if err != nil {
			if logger != nil {
				logger.Warn("pane read failed", "pane_id", pane.ID, "error", err)
			}
			states = append(states, &RecoverPaneState{
				Pane:  pane,
				State: RecoverFailed,
				Error: err.Error(),
			})
			continue
		}

		state, reason, url := detectRecoverState(text)

		// Also check for tool markers to confirm it's a Claude session
		match := matchWeztermPane("claude", text, nil)
		if !match.Matched && state == RecoverIdle {
			// Skip non-Claude panes in idle state
			continue
		}

		ps := &RecoverPaneState{
			Pane:        pane,
			State:       state,
			MatchReason: reason,
			OAuthURL:    url,
		}

		if match.Matched && ps.MatchReason == "" {
			ps.MatchReason = match.Reason
		}

		states = append(states, ps)
	}

	return states, nil
}

func printRecoverTable(w io.Writer, states []*RecoverPaneState) {
	fmt.Fprintf(w, "\n%-6s  %-20s  %-16s  %-15s  %s\n",
		"PANE", "TITLE", "STATE", "REASON", "URL/ERROR")
	fmt.Fprintf(w, "%s\n", strings.Repeat("-", 100))

	for _, s := range states {
		title := s.Pane.Title
		if len(title) > 20 {
			title = title[:17] + "..."
		}
		if title == "" {
			title = "-"
		}

		extra := ""
		if s.OAuthURL != "" {
			if len(s.OAuthURL) > 40 {
				extra = s.OAuthURL[:37] + "..."
			} else {
				extra = s.OAuthURL
			}
		} else if s.Error != "" {
			extra = "ERR: " + s.Error
		}

		fmt.Fprintf(w, "%-6d  %-20s  %-16s  %-15s  %s\n",
			s.Pane.ID, title, s.State.String(), s.MatchReason, extra)
	}
	fmt.Fprintln(w)
}

func printRecoverSummary(w io.Writer, states []*RecoverPaneState) {
	counts := make(map[RecoverState]int)
	for _, s := range states {
		counts[s.State]++
	}

	fmt.Fprintf(w, "Summary: %d panes (", len(states))
	parts := []string{}
	if n := counts[RecoverRateLimited]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d rate-limited", n))
	}
	if n := counts[RecoverAwaitingSelect]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d awaiting-select", n))
	}
	if n := counts[RecoverAwaitingURL]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d awaiting-url", n))
	}
	if n := counts[RecoverResuming]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d resuming", n))
	}
	if n := counts[RecoverFailed]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", n))
	}
	if n := counts[RecoverIdle]; n > 0 {
		parts = append(parts, fmt.Sprintf("%d idle", n))
	}
	if len(parts) == 0 {
		parts = append(parts, "all idle")
	}
	fmt.Fprintf(w, "%s)\n", strings.Join(parts, ", "))
}

func runWatchMode(cmd *cobra.Command, interval time.Duration, logger *slog.Logger) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Watching panes (refresh every %v, Ctrl+C to stop)...\n", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// Clear screen and move cursor to top
		fmt.Fprint(cmd.OutOrStdout(), "\033[2J\033[H")
		fmt.Fprintf(cmd.OutOrStdout(), "WezTerm Recovery Status [%s]\n", time.Now().Format("15:04:05"))

		states, err := scanRecoverStates(logger)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
		} else {
			printRecoverTable(cmd.OutOrStdout(), states)
			printRecoverSummary(cmd.OutOrStdout(), states)
		}

		fmt.Fprintln(cmd.OutOrStdout(), "\nPress Ctrl+C to stop watching.")

		select {
		case <-ticker.C:
			continue
		case <-cmd.Context().Done():
			return nil
		}
	}
}

func runAutoRecover(cmd *cobra.Command, states []*RecoverPaneState, yes bool, resumePrompt string, logger *slog.Logger) error {
	// Count actionable panes
	var actionable []*RecoverPaneState
	for _, s := range states {
		switch s.State {
		case RecoverRateLimited, RecoverAwaitingSelect, RecoverResuming:
			actionable = append(actionable, s)
		}
	}

	if len(actionable) == 0 {
		printRecoverTable(cmd.OutOrStdout(), states)
		fmt.Fprintln(cmd.OutOrStdout(), "No panes need action.")
		return nil
	}

	printRecoverTable(cmd.OutOrStdout(), states)
	fmt.Fprintf(cmd.OutOrStdout(), "Will auto-advance %d pane(s):\n", len(actionable))
	for _, s := range actionable {
		action := ""
		switch s.State {
		case RecoverRateLimited:
			action = "inject /login"
		case RecoverAwaitingSelect:
			action = "select subscription (1)"
		case RecoverResuming:
			action = "inject resume prompt"
		}
		fmt.Fprintf(cmd.OutOrStdout(), "  pane %d: %s -> %s\n", s.Pane.ID, s.State, action)
	}

	if !yes {
		if !weztermIsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("non-interactive: use --yes to confirm")
		}
		fmt.Fprint(cmd.OutOrStdout(), "\nProceed? [y/N]: ")
		var resp string
		fmt.Fscanln(os.Stdin, &resp)
		if strings.ToLower(strings.TrimSpace(resp)) != "y" {
			fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
			return nil
		}
	}

	// Execute actions
	success, fail := 0, 0
	for _, s := range actionable {
		var err error
		switch s.State {
		case RecoverRateLimited:
			err = weztermSendTextFunc(s.Pane.ID, "/login\n")
			if err == nil && logger != nil {
				logger.Debug("injected /login", "pane_id", s.Pane.ID)
			}
		case RecoverAwaitingSelect:
			time.Sleep(200 * time.Millisecond)
			err = weztermSendTextFunc(s.Pane.ID, "1\n")
			if err == nil && logger != nil {
				logger.Debug("injected subscription select", "pane_id", s.Pane.ID)
			}
		case RecoverResuming:
			time.Sleep(500 * time.Millisecond)
			err = weztermSendTextFunc(s.Pane.ID, resumePrompt)
			if err == nil && logger != nil {
				logger.Debug("injected resume prompt", "pane_id", s.Pane.ID)
			}
		}
		if err != nil {
			fail++
			fmt.Fprintf(cmd.ErrOrStderr(), "  pane %d: FAILED - %v\n", s.Pane.ID, err)
		} else {
			success++
			fmt.Fprintf(cmd.OutOrStdout(), "  pane %d: OK\n", s.Pane.ID)
		}
	}

	fmt.Fprintf(cmd.OutOrStdout(), "\nDone: %d succeeded, %d failed.\n", success, fail)
	return nil
}

func runInteractiveRecover(cmd *cobra.Command, states []*RecoverPaneState, resumePrompt string, logger *slog.Logger) error {
	if !weztermIsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("interactive mode requires a terminal (use --status or --auto)")
	}

	fmt.Fprintln(cmd.OutOrStdout(), "WezTerm Recovery - Interactive Mode")
	fmt.Fprintln(cmd.OutOrStdout(), "Commands: (r)efresh (l)ogin (s)elect (p)rompt (a)uto (q)uit")
	fmt.Fprintln(cmd.OutOrStdout())

	printRecoverTable(cmd.OutOrStdout(), states)
	printRecoverSummary(cmd.OutOrStdout(), states)

	// Set terminal to raw mode for single-char input
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	buf := make([]byte, 1)
	for {
		fmt.Fprint(cmd.OutOrStdout(), "\r> ")
		_, err := os.Stdin.Read(buf)
		if err != nil {
			break
		}

		// Restore terminal for output
		term.Restore(int(os.Stdin.Fd()), oldState)

		switch buf[0] {
		case 'q', 'Q', 3: // q or Ctrl+C
			fmt.Fprintln(cmd.OutOrStdout(), "\nExiting.")
			return nil

		case 'r', 'R':
			fmt.Fprintln(cmd.OutOrStdout(), "\nRefreshing...")
			states, err = scanRecoverStates(logger)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: %v\n", err)
			} else {
				printRecoverTable(cmd.OutOrStdout(), states)
				printRecoverSummary(cmd.OutOrStdout(), states)
			}

		case 'l', 'L':
			fmt.Fprintln(cmd.OutOrStdout(), "\nInjecting /login to rate-limited panes...")
			injectToState(cmd, states, RecoverRateLimited, "/login\n", logger)

		case 's', 'S':
			fmt.Fprintln(cmd.OutOrStdout(), "\nSelecting subscription on awaiting panes...")
			time.Sleep(200 * time.Millisecond)
			injectToState(cmd, states, RecoverAwaitingSelect, "1\n", logger)

		case 'p', 'P':
			fmt.Fprintln(cmd.OutOrStdout(), "\nInjecting resume prompt to resuming panes...")
			time.Sleep(500 * time.Millisecond)
			injectToState(cmd, states, RecoverResuming, resumePrompt, logger)

		case 'a', 'A':
			fmt.Fprintln(cmd.OutOrStdout(), "\nAuto-advancing all panes...")
			runAutoRecover(cmd, states, true, resumePrompt, logger)
			// Refresh after auto
			states, _ = scanRecoverStates(logger)
			printRecoverTable(cmd.OutOrStdout(), states)
			printRecoverSummary(cmd.OutOrStdout(), states)

		default:
			fmt.Fprintf(cmd.OutOrStdout(), "\nUnknown command: %c\n", buf[0])
			fmt.Fprintln(cmd.OutOrStdout(), "Commands: (r)efresh (l)ogin (s)elect (p)rompt (a)uto (q)uit")
		}

		// Re-enable raw mode for next input
		oldState, _ = term.MakeRaw(int(os.Stdin.Fd()))
	}

	return nil
}

func injectToState(cmd *cobra.Command, states []*RecoverPaneState, targetState RecoverState, text string, logger *slog.Logger) {
	count := 0
	for _, s := range states {
		if s.State != targetState {
			continue
		}
		if err := weztermSendTextFunc(s.Pane.ID, text); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  pane %d: FAILED - %v\n", s.Pane.ID, err)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "  pane %d: OK\n", s.Pane.ID)
			count++
		}
	}
	if count == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "  (no panes in target state)")
	}
}
