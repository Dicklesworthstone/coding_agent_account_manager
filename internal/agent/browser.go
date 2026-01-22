package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// BrowserConfig configures the browser automation.
type BrowserConfig struct {
	// UserDataDir is the Chrome profile directory.
	// If empty, uses a temporary profile.
	UserDataDir string

	// Headless runs Chrome without UI.
	// Note: Google OAuth may require visible browser.
	Headless bool

	// Logger for structured logging.
	Logger *slog.Logger
}

// Browser handles Chrome automation for OAuth flows.
type Browser struct {
	config     BrowserConfig
	logger     *slog.Logger
	allocCtx   context.Context
	cancelFunc context.CancelFunc
}

// NewBrowser creates a new browser automation instance.
func NewBrowser(config BrowserConfig) *Browser {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	return &Browser{
		config: config,
		logger: config.Logger,
	}
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

	// Create browser context with options
	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.DisableGPU,
	}

	if b.config.UserDataDir != "" {
		opts = append(opts, chromedp.UserDataDir(b.config.UserDataDir))
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
	chromePath := findChrome()
	if chromePath != "" {
		opts = append(opts, chromedp.ExecPath(chromePath))
	}

	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()

	taskCtx, cancelTask := chromedp.NewContext(allocCtx,
		chromedp.WithLogf(func(format string, args ...interface{}) {
			b.logger.Debug(fmt.Sprintf(format, args...))
		}),
	)
	defer cancelTask()

	// Set timeout for entire flow
	taskCtx, cancelTimeout := context.WithTimeout(taskCtx, 90*time.Second)
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
	time.Sleep(2 * time.Second)

	// Check current state and handle accordingly
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

		b.logger.Debug("page state",
			"attempt", attempt,
			"url", truncateURL(currentURL, 80))

		// Check if we have a challenge code
		if code = extractChallengeCode(pageHTML); code != "" {
			b.logger.Info("extracted challenge code")
			return code, usedAccount, nil
		}

		// Check if on Google account selection page
		if strings.Contains(currentURL, "accounts.google.com") {
			if preferredAccount != "" {
				b.logger.Debug("attempting to select account", "account", preferredAccount)
				usedAccount = preferredAccount

				// Try multiple selector strategies for account selection
				accountSelectors := []string{
					fmt.Sprintf(`div[data-email="%s"]`, preferredAccount),
					fmt.Sprintf(`li[data-email="%s"]`, preferredAccount),
					fmt.Sprintf(`[data-identifier="%s"]`, preferredAccount),
					// Anthropic/Claude-specific selectors
					fmt.Sprintf(`button[data-email="%s"]`, preferredAccount),
					fmt.Sprintf(`a[data-email="%s"]`, preferredAccount),
				}

				selected := false
				for _, selector := range accountSelectors {
					err = chromedp.Run(taskCtx,
						chromedp.Click(selector,
							chromedp.ByQuery,
							chromedp.NodeVisible),
					)
					if err == nil {
						selected = true
						break
					}
				}

				if !selected {
					b.logger.Debug("could not click preferred account, trying generic selectors")
					// Fallback: try clicking any visible account
					fallbackSelectors := []string{
						`div[data-identifier]`,
						`li[data-identifier]`,
						`[role="listitem"][data-email]`,
						`button[data-email]`,
					}
					for _, selector := range fallbackSelectors {
						err = chromedp.Run(taskCtx,
							chromedp.Click(selector,
								chromedp.ByQuery,
								chromedp.NodeVisible),
						)
						if err == nil {
							break
						}
					}
				}
			} else {
				// No preferred account - click first available
				fallbackSelectors := []string{
					`div[data-identifier]`,
					`li[data-identifier]`,
					`[role="listitem"][data-email]`,
					`button[data-email]`,
					`div[data-email]`,
				}
				for _, selector := range fallbackSelectors {
					err = chromedp.Run(taskCtx,
						chromedp.Click(selector,
							chromedp.ByQuery,
							chromedp.NodeVisible),
					)
					if err == nil {
						break
					}
				}
			}
			if err != nil {
				b.logger.Debug("account selection failed", "error", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}

		// Check if on consent page
		if strings.Contains(pageHTML, "consent") || strings.Contains(pageHTML, "Allow") ||
			strings.Contains(pageHTML, "permission") || strings.Contains(pageHTML, "authorize") {
			b.logger.Debug("handling consent page")

			// Try multiple selector strategies for consent buttons
			consentSelectors := []string{
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

			for _, selector := range consentSelectors {
				err = chromedp.Run(taskCtx,
					chromedp.Click(selector,
						chromedp.ByQuery,
						chromedp.NodeVisible),
				)
				if err == nil {
					b.logger.Debug("clicked consent button", "selector", selector)
					break
				}
			}
			if err != nil {
				b.logger.Debug("consent click failed with all selectors", "last_error", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}

		// Check if on Claude's code display page
		if strings.Contains(currentURL, "claude.ai") || strings.Contains(currentURL, "anthropic.com") {
			// Look for code display
			if code = extractChallengeCode(pageHTML); code != "" {
				b.logger.Info("found challenge code on Claude page")
				return code, usedAccount, nil
			}
		}

		// Wait and retry
		time.Sleep(2 * time.Second)
	}

	return "", "", fmt.Errorf("could not complete OAuth flow - no challenge code found")
}

// extractChallengeCode finds the challenge code in HTML content.
func extractChallengeCode(html string) string {
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
