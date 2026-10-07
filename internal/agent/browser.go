package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/chromedp/chromedp"
)

// BrowserConfig configures the browser automation.
type BrowserConfig struct {
	// UserDataDir is the Chrome profile directory.
	// If empty, DefaultChromeUserDataDir is used.
	UserDataDir string

	// ExecPath is the Chrome executable. If empty, it is auto-detected.
	ExecPath string

	// Headless runs Chrome without UI.
	// Note: Google OAuth may require visible browser.
	Headless bool

	// Logger for structured logging.
	Logger *slog.Logger
}

// DefaultChromeUserDataDir is the agent's own persistent Chrome profile.
// Google and Claude sign-ins made there (see 'caam auth-agent signin') carry
// over to every OAuth flow. Chrome refuses automation of the user's everyday
// profile and locks a profile while it is open, so the agent keeps its own.
func DefaultChromeUserDataDir() string {
	return filepath.Join(config.DefaultDataPath(), "auth-agent-chrome")
}

// SignInURLs are opened by 'caam auth-agent signin': adding Google accounts
// and signing in to Claude.
var SignInURLs = []string{
	"https://accounts.google.com/AddSession",
	"https://claude.ai/login",
}

// Browser handles Chrome automation for OAuth flows.
type Browser struct {
	config     BrowserConfig
	logger     *slog.Logger
	allocCtx   context.Context
	cancelFunc context.CancelFunc

	// stepDelay is the pause after each navigation or click for redirects
	// and rendering to settle; flowTimeout bounds a whole OAuth flow.
	stepDelay   time.Duration
	flowTimeout time.Duration
	// extraOpts are additional Chrome allocator options (tests route
	// Chrome through a fixture proxy with them).
	extraOpts []chromedp.ExecAllocatorOption
}

// NewBrowser creates a new browser automation instance.
func NewBrowser(config BrowserConfig) *Browser {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	config.UserDataDir = ResolveChromeUserDataDir(config.UserDataDir)

	return &Browser{
		config:      config,
		logger:      config.Logger,
		stepDelay:   2 * time.Second,
		flowTimeout: 90 * time.Second,
	}
}

// ResolveChromeUserDataDir resolves an empty profile directory to
// DefaultChromeUserDataDir and a leading "~/" to the home directory (config
// files are not shell-expanded).
func ResolveChromeUserDataDir(dir string) string {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return DefaultChromeUserDataDir()
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(dir[1:], "/"))
		}
	}
	return dir
}

// UserDataDir returns the Chrome profile directory this browser uses.
func (b *Browser) UserDataDir() string {
	return b.config.UserDataDir
}

func (b *Browser) execPath() string {
	if b.config.ExecPath != "" {
		return b.config.ExecPath
	}
	return findChrome()
}

// ensureProfileDir creates the profile directory owner-only: it holds
// Google and Claude session cookies.
func (b *Browser) ensureProfileDir() error {
	if err := os.MkdirAll(b.config.UserDataDir, 0o700); err != nil {
		return fmt.Errorf("create chrome profile dir: %w", err)
	}
	return nil
}

// OpenForSignIn opens a visible Chrome window on the agent's profile at urls
// and returns once the user closes it, so sign-ins persist for later OAuth
// flows. If Chrome already has this profile open, the URLs open there and
// OpenForSignIn returns immediately.
func (b *Browser) OpenForSignIn(ctx context.Context, urls ...string) error {
	chromePath := b.execPath()
	if chromePath == "" {
		return errors.New("Chrome not found: install Google Chrome or Chromium")
	}
	if err := b.ensureProfileDir(); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, chromePath, signInArgs(b.config.UserDataDir, urls)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("chrome exited: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func signInArgs(userDataDir string, urls []string) []string {
	args := []string{
		"--user-data-dir=" + userDataDir,
		"--no-first-run",
		"--no-default-browser-check",
	}
	return append(args, urls...)
}

// Close releases browser resources.
func (b *Browser) Close() {
	if b.cancelFunc != nil {
		b.cancelFunc()
	}
}

// CompleteOAuth navigates to the OAuth URL and extracts the challenge code.
// If preferredAccount is set, it will try to select that Google account.
// Returns the code, the account actually used, and any error.
func (b *Browser) CompleteOAuth(ctx context.Context, oauthURL, preferredAccount string) (string, string, error) {
	// Only log URL details at debug level to avoid exposing tokens
	b.logger.Debug("starting OAuth flow",
		"url_prefix", truncateURL(oauthURL, 60),
		"preferred_account", preferredAccount)
	b.logger.Info("starting OAuth flow",
		"has_preferred_account", preferredAccount != "")

	if err := b.ensureProfileDir(); err != nil {
		return "", "", err
	}

	// Create browser context with options
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
		chromedp.UserDataDir(b.config.UserDataDir),
	}

	if b.config.Headless {
		opts = append(opts, chromedp.Headless)
	} else {
		// Ensure visible window
		opts = append(opts,
			chromedp.Flag("headless", false),
			chromedp.WindowSize(1280, 900),
		)
	}

	// Find Chrome executable
	chromePath := b.execPath()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}
	opts = append(opts, b.extraOpts...)

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {
			b.logger.Debug(fmt.Sprintf(format, args...))
		}),
	)
	defer cancelTask()

	// Set timeout for entire flow
	taskCtx, cancelTimeout := context.WithTimeout(taskCtx, b.flowTimeout)
	defer cancelTimeout()

	var code string
	var usedAccount string

	err := chromedp.Run(taskCtx,
		// Navigate to OAuth URL
		chromedp.Navigate(oauthURL),
		chromedp.WaitReady("body"),
	)
	if err != nil {
		return "", "", fmt.Errorf("navigate: %w", err)
	}

	// Wait a moment for redirects
	time.Sleep(b.stepDelay)

	// Check current state and handle accordingly
	var lastURL string
	for attempt := 0; attempt < 10; attempt++ {
		var currentURL string
		var pageHTML string

		err = chromedp.Run(taskCtx,
			chromedp.Location(&currentURL),
			chromedp.OuterHTML("html", &pageHTML),
		)
		if err != nil {
			return "", "", fmt.Errorf("get page state: %w", err)
		}
		lastURL = currentURL

		b.logger.Debug("page state",
			"attempt", attempt,
			"url", truncateURL(currentURL, 80))

		// The OAuth callback carries the code in its URL; that is
		// authoritative and what Claude Code expects pasted ("code#state").
		if code = codeFromCallbackURL(currentURL); code != "" {
			b.logger.Info("extracted authorization code from callback URL")
			return code, usedAccount, nil
		}
		// Otherwise read the code from an Anthropic/Claude code page. Other
		// pages (Google sign-in, the consent page) are never scraped: their
		// markup is full of tokens that look like codes.
		if onCodePage(currentURL) {
			if code = extractChallengeCode(pageHTML); code != "" {
				b.logger.Info("extracted challenge code from code page")
				return code, usedAccount, nil
			}
		}

		// Claude login (the profile's Claude session is missing or expired):
		// sign in with Google, which leads to the account chooser below.
		// Checked before the consent heuristics: this page's return URL
		// mentions "authorize", and its submit button is the email form.
		if onClaudeLogin(currentURL) {
			if err := b.clickByText(taskCtx, "button", "Continue with Google"); err == nil {
				b.logger.Debug("continuing Claude login with Google")
			} else {
				b.logger.Debug("Claude login: no Google sign-in button", "error", err)
			}
			time.Sleep(b.stepDelay)
			continue
		}

		// Google account chooser: pick the preferred account, else the first.
		if strings.Contains(currentURL, "accounts.google.com") {
			if preferredAccount != "" {
				if _, _, err := b.clickFirstVisible(taskCtx, preferredAccountSelectors(preferredAccount)); err == nil {
					usedAccount = preferredAccount
					b.logger.Debug("selected preferred account")
					time.Sleep(b.stepDelay)
					continue
				}
				b.logger.Debug("preferred account not offered, trying any account")
			}
			// Report the account actually chosen so usage tracking stays
			// truthful when the preferred one is not signed in.
			if _, identity, err := b.clickFirstVisible(taskCtx, anyAccountSelectors); err == nil {
				usedAccount = identity
				b.logger.Debug("selected first offered account")
			} else {
				b.logger.Debug("account selection failed", "error", err)
			}
			time.Sleep(b.stepDelay)
			continue
		}

		// Check if on consent page
		if strings.Contains(pageHTML, "consent") || strings.Contains(pageHTML, "Allow") ||
			strings.Contains(pageHTML, "permission") || strings.Contains(pageHTML, "authorize") {
			if selector, _, err := b.clickFirstVisible(taskCtx, consentSelectors); err == nil {
				b.logger.Debug("clicked consent button", "selector", selector)
			} else {
				b.logger.Debug("consent click failed", "error", err)
			}
			time.Sleep(b.stepDelay)
			continue
		}

		// Wait and retry
		time.Sleep(b.stepDelay)
	}

	return "", "", stuckFlowError(lastURL)
}

// stuckFlowError explains where a flow stopped without a code. Only the host
// and path are reported: queries carry OAuth state.
func stuckFlowError(lastURL string) error {
	u, err := url.Parse(lastURL)
	if err != nil || u.Host == "" {
		return errors.New("could not complete OAuth flow: no authorization code found")
	}
	page := u.Host + u.Path
	host := strings.ToLower(u.Hostname())
	if onClaudeLogin(lastURL) || host == "accounts.google.com" {
		return fmt.Errorf("could not complete OAuth flow: stuck at sign-in page %s; sign in to the agent's Chrome profile with 'caam auth-agent signin'", page)
	}
	return fmt.Errorf("could not complete OAuth flow: no authorization code found (last page %s)", page)
}

// onClaudeLogin reports whether rawURL is the Claude web login page.
func onClaudeLogin(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return (host == "claude.ai" || host == "claude.com" || strings.HasSuffix(host, ".claude.ai") || strings.HasSuffix(host, ".claude.com")) &&
		strings.HasPrefix(u.Path, "/login")
}

// clickByText clicks the first visible tag element whose text contains text.
func (b *Browser) clickByText(ctx context.Context, tag, text string) error {
	args, err := json.Marshal([]string{tag, text})
	if err != nil {
		return err
	}
	var marked bool
	script := fmt.Sprintf(`(([tag, text]) => {
	for (const el of document.querySelectorAll(tag)) {
		if (el.textContent.includes(text) && el.getClientRects().length > 0) {
			el.setAttribute("data-caam-target", "1");
			return true;
		}
	}
	return false;
})(%s)`, args)
	if err := chromedp.Run(ctx, chromedp.Evaluate(script, &marked)); err != nil {
		return err
	}
	if !marked {
		return errNoVisibleMatch
	}
	clickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return chromedp.Run(clickCtx, chromedp.Click(`[data-caam-target="1"]`, chromedp.ByQuery, chromedp.NodeVisible))
}

// preferredAccountSelectors match one account in Google's account chooser
// (which marks accounts with data-identifier) or similar pickers.
func preferredAccountSelectors(email string) []string {
	return []string{
		fmt.Sprintf(`[data-identifier=%q]`, email),
		fmt.Sprintf(`[data-email=%q]`, email),
	}
}

// anyAccountSelectors match any offered account.
var anyAccountSelectors = []string{
	`div[data-identifier]`,
	`li[data-identifier]`,
	`[role="listitem"][data-email]`,
	`button[data-email]`,
	`div[data-email]`,
}

// consentSelectors match approve buttons on OAuth consent pages.
var consentSelectors = []string{
	// Standard form submissions
	`button[type="submit"]`,
	`input[type="submit"]`,
	// Google consent buttons
	`#submit_approve_access`,
	`button[data-idom-class="nCP5yc"]`, // Google's "Allow" button
	`div[role="button"][data-value="approve"]`,
	// Text-based fallbacks
	`button[aria-label*="Allow"]`,
	`button[aria-label*="Continue"]`,
	`button[aria-label*="Accept"]`,
	// Generic button patterns
	`button.primary`,
	`button.submit`,
	`input[value="Allow"]`,
	`input[value="Continue"]`,
	`input[value="Accept"]`,
}

// errNoVisibleMatch reports that none of the selectors matched a visible
// element.
var errNoVisibleMatch = errors.New("no visible element matches")

// visibleMatchScript finds the first selector in a JSON array that matches a
// visible element and returns it with the element's account identifier.
const visibleMatchScript = `((selectors) => {
	for (const s of selectors) {
		let el = null;
		try { el = document.querySelector(s); } catch (e) { continue; }
		if (el && el.getClientRects().length > 0) {
			return {selector: s, identity: el.getAttribute("data-identifier") || el.getAttribute("data-email") || ""};
		}
	}
	return {selector: "", identity: ""};
})(%s)`

// clickFirstVisible clicks the first selector that matches a visible element
// and returns it with that element's data-identifier or data-email. The
// page is checked before clicking because a chromedp query waits until its
// selector matches: clicking an absent selector would stall the whole flow
// until its deadline.
func (b *Browser) clickFirstVisible(ctx context.Context, selectors []string) (selector, identity string, err error) {
	list, err := json.Marshal(selectors)
	if err != nil {
		return "", "", err
	}
	var found struct {
		Selector string `json:"selector"`
		Identity string `json:"identity"`
	}
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(visibleMatchScript, list), &found)); err != nil {
		return "", "", err
	}
	if found.Selector == "" {
		return "", "", errNoVisibleMatch
	}
	clickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(clickCtx, chromedp.Click(found.Selector, chromedp.ByQuery, chromedp.NodeVisible)); err != nil {
		return "", "", fmt.Errorf("click %s: %w", found.Selector, err)
	}
	return found.Selector, found.Identity, nil
}

// codeFromCallbackURL returns the paste-ready authorization code when rawURL
// is an OAuth code callback (".../oauth/code/callback?code=...&state=...").
// Claude Code accepts "code#state" at its paste prompt.
func codeFromCallbackURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || !strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/oauth/code/callback") {
		return ""
	}
	q := u.Query()
	code := strings.TrimSpace(q.Get("code"))
	if code == "" {
		return ""
	}
	if state := strings.TrimSpace(q.Get("state")); state != "" {
		return code + "#" + state
	}
	return code
}

// onCodePage reports whether rawURL is an Anthropic or Claude OAuth code page
// (".../oauth/code/..."), the only pages that display an authorization code.
// Login, consent and app pages are excluded: their markup is full of numbers
// and identifiers that the code patterns would mistake for a code.
func onCodePage(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	anthropic := false
	for _, domain := range []string{"anthropic.com", "claude.ai", "claude.com"} {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			anthropic = true
		}
	}
	return anthropic && strings.Contains(u.Path, "/oauth/code")
}

// pastedCodePattern matches a displayed "code#state" authorization code.
var pastedCodePattern = regexp.MustCompile(`([A-Za-z0-9_-]{16,}#[A-Za-z0-9_-]{8,})`)

// extractChallengeCode finds the challenge code in HTML content.
func extractChallengeCode(html string) string {
	// A full "code#state" string is what the CLI's paste prompt expects.
	if m := pastedCodePattern.FindStringSubmatch(html); len(m) > 1 {
		return m[1]
	}

	// Look for common patterns:
	// 1. Code in a dedicated element (class containing "code", "challenge", etc.)
	// 2. Formatted as XXXX-XXXX or similar
	// 3. In a copy-paste friendly format

	patterns := []*regexp.Regexp{
		// Claude's challenge code format (typically XXXX-XXXX)
		regexp.MustCompile(`(?i)(?:code|challenge)[^>]*>([A-Z0-9]{4,8}-[A-Z0-9]{4,8})<`),
		regexp.MustCompile(`(?i)>([A-Z0-9]{4,8}-[A-Z0-9]{4,8})</`),
		// Alphanumeric code with dashes
		regexp.MustCompile(`\b([A-Z0-9]{4}-[A-Z0-9]{4})\b`),
		// Longer alphanumeric codes
		regexp.MustCompile(`\b([A-Z0-9]{8,16})\b`),
	}

	for _, pattern := range patterns {
		if matches := pattern.FindStringSubmatch(html); len(matches) > 1 {
			code := strings.TrimSpace(matches[1])
			// Validate it looks like a code (not random text)
			if len(code) >= 7 && len(code) <= 20 {
				return code
			}
		}
	}

	return ""
}

// truncateURL shortens a URL for logging.
func truncateURL(url string, maxLen int) string {
	if len(url) <= maxLen {
		return url
	}
	return url[:maxLen-3] + "..."
}

// findChrome locates the Chrome executable on the system.
// Prefers Chrome Canary (newer features) over stable Chrome.
func findChrome() string {
	switch runtime.GOOS {
	case "darwin":
		paths := []string{
			// Prefer Canary for latest features
			"/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			// User-level installations
			os.Getenv("HOME") + "/Applications/Google Chrome Canary.app/Contents/MacOS/Google Chrome Canary",
			os.Getenv("HOME") + "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	case "linux":
		paths := []string{
			"/usr/bin/google-chrome-unstable", // Canary/Dev channel
			"/usr/bin/google-chrome-beta",
			"/usr/bin/google-chrome",
			"/usr/bin/google-chrome-stable",
			"/usr/bin/chromium",
			"/usr/bin/chromium-browser",
			// Snap installations
			"/snap/bin/chromium",
		}
		for _, p := range paths {
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		// Try which
		for _, name := range []string{"google-chrome-unstable", "google-chrome", "chromium"} {
			if path, err := exec.LookPath(name); err == nil {
				return path
			}
		}
	case "windows":
		// Get local app data for Canary
		localAppData := os.Getenv("LOCALAPPDATA")
		paths := []string{
			// Canary (user-level)
			localAppData + `\Google\Chrome SxS\Application\chrome.exe`,
			// Stable (system-level)
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
			// Stable (user-level)
			localAppData + `\Google\Chrome\Application\chrome.exe`,
		}
		for _, p := range paths {
			if p == "" {
				continue
			}
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}

	return "" // Let chromedp find it
}

// IsChromeAvailable checks if Chrome/Chromium is available on the system.
func IsChromeAvailable() bool {
	return findChrome() != ""
}

// GetChromePath returns the detected Chrome path, or empty string if not found.
func GetChromePath() string {
	return findChrome()
}
