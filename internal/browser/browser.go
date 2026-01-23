// Package browser provides abstractions for launching browsers with specific profiles.
//
// This package enables OAuth flows to automatically use the correct browser profile
// (and thus the correct Google/GitHub account), eliminating the need to manually
// switch accounts during login.
package browser

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Launcher opens URLs in a browser, optionally with a specific profile.
type Launcher interface {
	// Open opens the given URL in the browser.
	Open(url string) error

	// Name returns the browser name for display purposes.
	Name() string

	// SupportsProfiles returns true if this launcher supports browser profiles.
	SupportsProfiles() bool
}

// Config holds browser configuration for a launcher.
type Config struct {
	// Command is the browser executable path or name.
	// Examples: "google-chrome", "firefox", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	Command string

	// ProfileDir is the browser profile directory or name.
	// For Chrome: "Profile 1", "Default", or full path
	// For Firefox: profile name as shown in about:profiles
	ProfileDir string
}

// BrowserType represents a known browser type.
type BrowserType string

const (
	BrowserChrome  BrowserType = "chrome"
	BrowserFirefox BrowserType = "firefox"
	BrowserDefault BrowserType = "default"
)

// NewLauncher creates a launcher based on the config.
// If config is nil or empty, returns a DefaultLauncher.
func NewLauncher(cfg *Config) Launcher {
	if cfg == nil || cfg.Command == "" {
		return &DefaultLauncher{}
	}

	browserType := detectBrowserType(cfg.Command)
	switch browserType {
	case BrowserChrome:
		return &ChromeLauncher{config: *cfg}
	case BrowserFirefox:
		return &FirefoxLauncher{config: *cfg}
	default:
		// Unknown browser, treat as generic command
		return &GenericLauncher{config: *cfg}
	}
}

// detectBrowserType determines the browser type from command string.
func detectBrowserType(command string) BrowserType {
	lower := strings.ToLower(command)

	// Chrome variants
	if strings.Contains(lower, "chrome") ||
		strings.Contains(lower, "chromium") ||
		strings.Contains(lower, "brave") ||
		strings.Contains(lower, "edge") {
		return BrowserChrome
	}

	// Firefox variants
	if strings.Contains(lower, "firefox") {
		return BrowserFirefox
	}

	return BrowserDefault
}

// ChromeLauncher opens URLs in Chrome (or Chromium-based browsers) with profile support.
type ChromeLauncher struct {
	config Config
}

// Name returns the browser name.
func (c *ChromeLauncher) Name() string {
	return "Chrome"
}

// SupportsProfiles returns true as Chrome supports profiles.
func (c *ChromeLauncher) SupportsProfiles() bool {
	return true
}

// Open opens a URL in Chrome with the configured profile.
func (c *ChromeLauncher) Open(url string) error {
	chromePath := c.config.Command
	if chromePath == "" {
		chromePath = findChrome()
	}

	if chromePath == "" {
		return fmt.Errorf("chrome not found; install Google Chrome or Chromium from https://google.com/chrome or run 'caam doctor --auto'")
	}

	args := []string{}

	// Add profile directory if specified
	if c.config.ProfileDir != "" {
		args = append(args, fmt.Sprintf("--profile-directory=%s", c.config.ProfileDir))
	}

	args = append(args, url)

	cmd := exec.Command(chromePath, args...)
	return cmd.Start()
}

// findChrome locates Chrome on the system.
func findChrome() string {
	switch runtime.GOOS {
	case "darwin":
		// macOS paths
		paths := []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return p
			}
		}
		// Try to find in PATH
		if path, err := exec.LookPath("google-chrome"); err == nil {
			return path
		}

	case "linux":
		// Linux: check common names in PATH
		names := []string{
			"google-chrome",
			"google-chrome-stable",
			"chromium",
			"chromium-browser",
			"brave-browser",
		}
		for _, name := range names {
			if path, err := exec.LookPath(name); err == nil {
				return path
			}
		}

	case "windows":
		// Windows: common installation paths
		paths := []string{
			`C:\Program Files\Google\Chrome\Application\chrome.exe`,
			`C:\Program Files (x86)\Google\Chrome\Application\chrome.exe`,
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return p
			}
		}
	}

	return ""
}

// FirefoxLauncher opens URLs in Firefox with profile support.
type FirefoxLauncher struct {
	config Config
}

// Name returns the browser name.
func (f *FirefoxLauncher) Name() string {
	return "Firefox"
}

// SupportsProfiles returns true as Firefox supports profiles.
func (f *FirefoxLauncher) SupportsProfiles() bool {
	return true
}

// Open opens a URL in Firefox with the configured profile.
func (f *FirefoxLauncher) Open(url string) error {
	firefoxPath := f.config.Command
	if firefoxPath == "" {
		firefoxPath = findFirefox()
	}

	if firefoxPath == "" {
		return fmt.Errorf("firefox not found; install from https://www.mozilla.org/firefox/")
	}

	args := []string{}

	// Add profile if specified
	// Firefox uses -P for profile name
	if f.config.ProfileDir != "" {
		args = append(args, "-P", f.config.ProfileDir)
	}

	// -new-tab opens in existing window if Firefox is running
	args = append(args, "-new-tab", url)

	cmd := exec.Command(firefoxPath, args...)
	return cmd.Start()
}

// findFirefox locates Firefox on the system.
func findFirefox() string {
	switch runtime.GOOS {
	case "darwin":
		paths := []string{
			"/Applications/Firefox.app/Contents/MacOS/firefox",
			"/Applications/Firefox Developer Edition.app/Contents/MacOS/firefox",
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return p
			}
		}

	case "linux":
		names := []string{
			"firefox",
			"firefox-esr",
		}
		for _, name := range names {
			if path, err := exec.LookPath(name); err == nil {
				return path
			}
		}

	case "windows":
		paths := []string{
			`C:\Program Files\Mozilla Firefox\firefox.exe`,
			`C:\Program Files (x86)\Mozilla Firefox\firefox.exe`,
		}
		for _, p := range paths {
			if _, err := exec.LookPath(p); err == nil {
				return p
			}
		}
	}

	return ""
}

// DefaultLauncher opens URLs using the system default browser.
type DefaultLauncher struct{}

// Name returns the browser name.
func (d *DefaultLauncher) Name() string {
	return "system default"
}

// SupportsProfiles returns false as default launcher doesn't support profiles.
func (d *DefaultLauncher) SupportsProfiles() bool {
	return false
}

// Open opens a URL in the system default browser.
func (d *DefaultLauncher) Open(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		// Try xdg-open first, fall back to common browsers
		if _, err := exec.LookPath("xdg-open"); err == nil {
			cmd = exec.Command("xdg-open", url)
		} else if _, err := exec.LookPath("sensible-browser"); err == nil {
			cmd = exec.Command("sensible-browser", url)
		} else {
			return fmt.Errorf("no browser found: install xdg-utils or set a browser command")
		}
	case "windows":
		// Use rundll32 to open URL, which avoids cmd.exe shell injection vulnerabilities
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}

	return cmd.Start()
}

// GenericLauncher opens URLs using a custom command.
// Used for unknown browsers or custom scripts.
type GenericLauncher struct {
	config Config
}

// Name returns the command as the browser name.
func (g *GenericLauncher) Name() string {
	return g.config.Command
}

// SupportsProfiles returns false as generic launcher doesn't handle profiles.
func (g *GenericLauncher) SupportsProfiles() bool {
	return false
}

// Open opens a URL using the configured command.
func (g *GenericLauncher) Open(url string) error {
	cmd := exec.Command(g.config.Command, url)
	return cmd.Start()
}
