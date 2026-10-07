package agent

import (
	"context"
	"database/sql"
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
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	_ "modernc.org/sqlite" // database/sql driver for reading Chrome's cookie store
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

// ProfileSessions reports whether a Chrome profile directory holds a signed-in
// Google session and a Claude session (what 'caam auth-agent signin'
// establishes). It reads only cookie host names and names from Chrome's
// cookie database, opened read-only so a running Chrome is not disturbed;
// cookie values stay encrypted and are never read.
func ProfileSessions(userDataDir string) (google, claude bool, err error) {
	var dbPath string
	for _, rel := range []string{filepath.Join("Default", "Network", "Cookies"), filepath.Join("Default", "Cookies")} {
		if info, statErr := os.Stat(filepath.Join(userDataDir, rel)); statErr == nil && info.Mode().IsRegular() {
			dbPath = filepath.Join(userDataDir, rel)
			break
		}
	}
	if dbPath == "" {
		return false, false, nil // the profile has never been used
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return false, false, fmt.Errorf("open chrome cookies: %w", err)
	}
	defer db.Close()
	// SID-family cookies exist only while a Google account is signed in;
	// claude.ai's session cookie is sessionKey.
	err = db.QueryRow(`SELECT
		EXISTS(SELECT 1 FROM cookies WHERE (host_key = '.google.com' OR host_key LIKE '%.google.com')
			AND name IN ('SID', '__Secure-1PSID', '__Secure-3PSID')),
		EXISTS(SELECT 1 FROM cookies WHERE (host_key LIKE '%claude.ai' OR host_key LIKE '%claude.com')
			AND name = 'sessionKey')`).Scan(&google, &claude)
	if err != nil {
		return false, false, fmt.Errorf("read chrome cookies: %w", err)
	}
	return google, claude, nil
}

// SignInURLs are opened by 'caam auth-agent signin': adding Google accounts
// and signing in to Claude.
var SignInURLs = []string{
	"https://accounts.google.com/AddSession",
	"https://claude.ai/login",
}

// browserCloseTimeout bounds waiting for Chrome to close (and save its
// state) after a flow.
const browserCloseTimeout = 5 * time.Second

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
// accounts are the Google accounts it may sign in with, most preferred
// first; it never uses another one. With no accounts, whichever account is
// offered first is used. Returns the code, the account actually used, and
// any error.
func (b *Browser) CompleteOAuth(ctx context.Context, oauthURL string, accounts []string) (string, string, error) {
	preferredAccount := ""
	if len(accounts) > 0 {
		preferredAccount = accounts[0]
	}
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
		// No crash-reporter helper per flow (chromedp's defaults do the
		// same). Keychain and password-store flags stay at Chrome's
		// defaults: cookies must stay readable by the plain Chrome that
		// 'caam auth-agent signin' runs on this profile.
		chromedp.Flag("disable-breakpad", true),
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

	browserCtx, cancelTask := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {
			b.logger.Debug(fmt.Sprintf(format, args...))
		}),
	)
	defer cancelTask()
	// Close Chrome gracefully so it writes its cookie store. Cancelling the
	// context kills it, losing cookies set or rotated during the flow: a new
	// Claude session after an account switch, and Google's frequently
	// rotated session cookies, without which the profile drifts toward
	// being signed out.
	defer func() {
		closeCtx, cancel := context.WithTimeout(browserCtx, browserCloseTimeout)
		defer cancel()
		if err := chromedp.Cancel(closeCtx); err != nil {
			b.logger.Debug("graceful browser close failed", "error", err)
		}
	}()

	// Start Chrome on browserCtx: the context of the first Run owns the
	// browser, and ending the flow's timeout context below must not kill it
	// before the graceful close above.
	if err := chromedp.Run(browserCtx); err != nil {
		return "", "", fmt.Errorf("start browser: %w", err)
	}

	// Set timeout for entire flow
	taskCtx, cancelTimeout := context.WithTimeout(browserCtx, b.flowTimeout)
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
	switchedClaudeAccount := false
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

		// Claude's authorize page approves whichever Claude account the
		// profile is signed in to, without asking Google. When that is not
		// the account the strategy chose, drop the Claude session (Google
		// sessions stay) and sign in again through Google, once per flow.
		if onClaudeAuthorize(currentURL) {
			shown := b.pageAccounts(taskCtx)
			if preferredAccount != "" && usedAccount == "" && !switchedClaudeAccount &&
				len(shown) > 0 && !containsFold(shown, preferredAccount) {
				err := b.signOutOf(taskCtx, currentURL)
				if err == nil {
					switchedClaudeAccount = true
					b.logger.Info("Claude is signed in to another account; signing in again with the selected account")
					if err := chromedp.Run(taskCtx, chromedp.Navigate(oauthURL)); err != nil {
						return "", "", fmt.Errorf("navigate: %w", err)
					}
					time.Sleep(b.stepDelay)
					continue
				}
				b.logger.Debug("could not sign out of Claude", "error", err)
			}
			// Never approve a Claude account outside the configured list.
			if len(accounts) > 0 && len(shown) > 0 && !anyContainsFold(shown, accounts) {
				return "", "", fmt.Errorf("claude is signed in as %s, which is not among the configured accounts", shown[0])
			}
			if usedAccount == "" && len(shown) == 1 {
				usedAccount = shown[0]
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

		// Google account chooser: the first allowed account it offers, in
		// preference order; never an account outside the list.
		if onGoogleSignIn(currentURL) && !onGoogleConsent(currentURL) {
			if len(accounts) > 0 {
				picked := ""
				for _, account := range accounts {
					if _, _, err := b.clickFirstVisible(taskCtx, preferredAccountSelectors(account)); err == nil {
						picked = account
						break
					}
				}
				if picked != "" {
					usedAccount = picked
					b.logger.Debug("selected account", "preferred", picked == preferredAccount)
					time.Sleep(b.stepDelay)
					continue
				}
				// Accounts are listed, none of them ours: stop rather than
				// sign in with an account the user did not choose.
				if _, _, err := b.firstVisible(taskCtx, anyAccountSelectors); err == nil {
					return "", "", fmt.Errorf("none of the configured accounts (%s) is signed in to the agent's Chrome profile; add them with 'caam auth-agent signin'",
						strings.Join(accounts, ", "))
				}
				time.Sleep(b.stepDelay) // still loading, or a sign-in form
				continue
			}
			// No accounts configured: whichever account is offered first,
			// reported as the account used.
			if _, identity, err := b.clickFirstVisible(taskCtx, anyAccountSelectors); err == nil {
				usedAccount = identity
				b.logger.Debug("selected first offered account")
			} else {
				b.logger.Debug("account selection failed", "error", err)
			}
			time.Sleep(b.stepDelay)
			continue
		}

		// Consent: only Claude's authorize page and Google's OAuth consent
		// page. Clicking submit-like buttons on any page that merely
		// mentions "authorize" or "allow" could click through a sign-up or
		// onboarding page for an account the user never meant to use.
		if onClaudeAuthorize(currentURL) || onGoogleConsent(currentURL) {
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

// onClaudeAuthorize reports whether rawURL is Claude's OAuth authorize
// (consent) page.
func onClaudeAuthorize(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isClaudeHost(u.Hostname()) && strings.Contains(u.Path, "/oauth/authorize")
}

// onGoogleSignIn reports whether rawURL is on Google's account sign-in host.
func onGoogleSignIn(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(u.Hostname(), "accounts.google.com")
}

// onGoogleConsent reports whether rawURL is Google's OAuth consent page
// ("Sign in to claude.ai … Continue").
func onGoogleConsent(rawURL string) bool {
	u, err := url.Parse(rawURL)
	return err == nil && strings.EqualFold(u.Hostname(), "accounts.google.com") &&
		(strings.Contains(u.Path, "/consent") || strings.Contains(u.Path, "/oauth/id"))
}

func isClaudeHost(host string) bool {
	host = strings.ToLower(host)
	return host == "claude.ai" || host == "claude.com" || strings.HasSuffix(host, ".claude.ai") || strings.HasSuffix(host, ".claude.com")
}

// emailPattern matches an email address in page text.
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

// shownAccounts returns the distinct account emails in page text, ignoring
// Anthropic's own addresses (support and legal links).
func shownAccounts(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range emailPattern.FindAllString(text, -1) {
		email := strings.ToLower(m)
		domain := email[strings.LastIndex(email, "@")+1:]
		if domain == "anthropic.com" || strings.HasSuffix(domain, ".anthropic.com") || isClaudeHost(domain) {
			continue
		}
		if !seen[email] {
			seen[email] = true
			out = append(out, email)
		}
	}
	return out
}

// anyContainsFold reports whether any of values is in list (case-insensitive).
func anyContainsFold(values, list []string) bool {
	for _, v := range values {
		if containsFold(list, v) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}

// pageAccounts returns the account emails visible on the current page.
func (b *Browser) pageAccounts(ctx context.Context) []string {
	var text string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.body ? document.body.innerText : ""`, &text)); err != nil {
		return nil
	}
	return shownAccounts(text)
}

// signOutOf deletes the cookies of rawURL's site (the Claude session), leaving
// every other site's sessions, Google's included, in place.
func (b *Browser) signOutOf(ctx context.Context, rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	origin := u.Scheme + "://" + u.Host + "/"
	return chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		cookies, err := network.GetCookies().WithURLs([]string{origin}).Do(ctx)
		if err != nil {
			return err
		}
		for _, c := range cookies {
			if err := network.DeleteCookies(c.Name).WithDomain(c.Domain).WithPath(c.Path).Do(ctx); err != nil {
				return err
			}
		}
		return nil
	}))
}

// onClaudeLogin reports whether rawURL is the Claude web login page.
func onClaudeLogin(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return isClaudeHost(u.Hostname()) && strings.HasPrefix(u.Path, "/login")
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
	selector, identity, err = b.firstVisible(ctx, selectors)
	if err != nil {
		return "", "", err
	}
	clickCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := chromedp.Run(clickCtx, chromedp.Click(selector, chromedp.ByQuery, chromedp.NodeVisible)); err != nil {
		return "", "", fmt.Errorf("click %s: %w", selector, err)
	}
	return selector, identity, nil
}

// firstVisible returns the first selector matching a visible element, with
// that element's data-identifier or data-email, without clicking it.
func (b *Browser) firstVisible(ctx context.Context, selectors []string) (selector, identity string, err error) {
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
