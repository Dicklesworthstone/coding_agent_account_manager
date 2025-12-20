// Browser detection and profile discovery for caam init wizard.

package browser

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// DetectedBrowser represents a browser found on the system.
type DetectedBrowser struct {
	// Type is the browser type (chrome, firefox, edge, brave, chromium).
	Type BrowserType

	// Name is the human-readable browser name.
	Name string

	// Command is the path to the browser executable.
	Command string

	// Profiles are the discovered profiles within this browser.
	Profiles []BrowserProfile

	// DataDir is the browser's user data directory.
	DataDir string
}

// BrowserProfile represents a profile within a browser.
type BrowserProfile struct {
	// Name is the profile display name.
	Name string

	// ID is the profile directory name (e.g., "Profile 1", "Default").
	ID string

	// Email is the associated email if detected (from browser sync).
	Email string

	// IsDefault is true if this is the default profile.
	IsDefault bool
}

// Extended browser types for detection.
const (
	BrowserEdge     BrowserType = "edge"
	BrowserBrave    BrowserType = "brave"
	BrowserChromium BrowserType = "chromium"
)

// DetectBrowsers finds all installed browsers and their profiles.
func DetectBrowsers() []DetectedBrowser {
	var browsers []DetectedBrowser

	// Detect Chromium-based browsers
	if b := detectChrome(); b != nil {
		browsers = append(browsers, *b)
	}
	if b := detectChromium(); b != nil {
		browsers = append(browsers, *b)
	}
	if b := detectBrave(); b != nil {
		browsers = append(browsers, *b)
	}
	if b := detectEdge(); b != nil {
		browsers = append(browsers, *b)
	}

	// Detect Firefox
	if b := detectFirefoxBrowser(); b != nil {
		browsers = append(browsers, *b)
	}

	return browsers
}

// detectChrome finds Google Chrome and its profiles.
func detectChrome() *DetectedBrowser {
	var command, dataDir string

	switch runtime.GOOS {
	case "darwin":
		command = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		dataDir = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Google", "Chrome")
	case "linux":
		for _, name := range []string{"google-chrome", "google-chrome-stable"} {
			if path := findInPath(name); path != "" {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("HOME"), ".config", "google-chrome")
	case "windows":
		for _, path := range []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Google", "Chrome", "Application", "chrome.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "Application", "chrome.exe"),
		} {
			if fileExists(path) {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Google", "Chrome", "User Data")
	}

	if command == "" || !fileExists(command) {
		return nil
	}

	browser := &DetectedBrowser{
		Type:    BrowserChrome,
		Name:    "Google Chrome",
		Command: command,
		DataDir: dataDir,
	}

	if dataDir != "" {
		browser.Profiles = discoverChromiumProfiles(dataDir)
	}

	return browser
}

// detectChromium finds Chromium and its profiles.
func detectChromium() *DetectedBrowser {
	var command, dataDir string

	switch runtime.GOOS {
	case "darwin":
		command = "/Applications/Chromium.app/Contents/MacOS/Chromium"
		dataDir = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Chromium")
	case "linux":
		for _, name := range []string{"chromium", "chromium-browser"} {
			if path := findInPath(name); path != "" {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("HOME"), ".config", "chromium")
	case "windows":
		dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Chromium", "User Data")
		if path := findInPath("chromium"); path != "" {
			command = path
		}
	}

	if command == "" || !fileExists(command) {
		return nil
	}

	browser := &DetectedBrowser{
		Type:    BrowserChromium,
		Name:    "Chromium",
		Command: command,
		DataDir: dataDir,
	}

	if dataDir != "" {
		browser.Profiles = discoverChromiumProfiles(dataDir)
	}

	return browser
}

// detectBrave finds Brave Browser and its profiles.
func detectBrave() *DetectedBrowser {
	var command, dataDir string

	switch runtime.GOOS {
	case "darwin":
		command = "/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"
		dataDir = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "BraveSoftware", "Brave-Browser")
	case "linux":
		for _, name := range []string{"brave-browser", "brave"} {
			if path := findInPath(name); path != "" {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("HOME"), ".config", "BraveSoftware", "Brave-Browser")
	case "windows":
		for _, path := range []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
			filepath.Join(os.Getenv("LOCALAPPDATA"), "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
		} {
			if fileExists(path) {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "BraveSoftware", "Brave-Browser", "User Data")
	}

	if command == "" || !fileExists(command) {
		return nil
	}

	browser := &DetectedBrowser{
		Type:    BrowserBrave,
		Name:    "Brave",
		Command: command,
		DataDir: dataDir,
	}

	if dataDir != "" {
		browser.Profiles = discoverChromiumProfiles(dataDir)
	}

	return browser
}

// detectEdge finds Microsoft Edge and its profiles.
func detectEdge() *DetectedBrowser {
	var command, dataDir string

	switch runtime.GOOS {
	case "darwin":
		command = "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge"
		dataDir = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Microsoft Edge")
	case "linux":
		for _, name := range []string{"microsoft-edge", "microsoft-edge-stable"} {
			if path := findInPath(name); path != "" {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("HOME"), ".config", "microsoft-edge")
	case "windows":
		for _, path := range []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		} {
			if fileExists(path) {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("LOCALAPPDATA"), "Microsoft", "Edge", "User Data")
	}

	if command == "" || !fileExists(command) {
		return nil
	}

	browser := &DetectedBrowser{
		Type:    BrowserEdge,
		Name:    "Microsoft Edge",
		Command: command,
		DataDir: dataDir,
	}

	if dataDir != "" {
		browser.Profiles = discoverChromiumProfiles(dataDir)
	}

	return browser
}

// detectFirefoxBrowser finds Firefox and its profiles.
func detectFirefoxBrowser() *DetectedBrowser {
	var command, dataDir string

	switch runtime.GOOS {
	case "darwin":
		for _, path := range []string{
			"/Applications/Firefox.app/Contents/MacOS/firefox",
			"/Applications/Firefox Developer Edition.app/Contents/MacOS/firefox",
		} {
			if fileExists(path) {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("HOME"), "Library", "Application Support", "Firefox")
	case "linux":
		for _, name := range []string{"firefox", "firefox-esr"} {
			if path := findInPath(name); path != "" {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("HOME"), ".mozilla", "firefox")
	case "windows":
		for _, path := range []string{
			filepath.Join(os.Getenv("PROGRAMFILES"), "Mozilla Firefox", "firefox.exe"),
			filepath.Join(os.Getenv("PROGRAMFILES(X86)"), "Mozilla Firefox", "firefox.exe"),
		} {
			if fileExists(path) {
				command = path
				break
			}
		}
		dataDir = filepath.Join(os.Getenv("APPDATA"), "Mozilla", "Firefox")
	}

	if command == "" {
		return nil
	}

	browser := &DetectedBrowser{
		Type:    BrowserFirefox,
		Name:    "Firefox",
		Command: command,
		DataDir: dataDir,
	}

	if dataDir != "" {
		browser.Profiles = discoverFirefoxProfiles(dataDir)
	}

	return browser
}

// discoverChromiumProfiles reads profiles from a Chromium-based browser's data directory.
func discoverChromiumProfiles(dataDir string) []BrowserProfile {
	var profiles []BrowserProfile

	// Check for Local State file which contains profile info
	localStatePath := filepath.Join(dataDir, "Local State")
	localStateData, err := os.ReadFile(localStatePath)
	if err != nil {
		// Fall back to directory scanning
		return discoverChromiumProfilesByDir(dataDir)
	}

	var localState struct {
		Profile struct {
			InfoCache map[string]struct {
				Name      string `json:"name"`
				UserName  string `json:"user_name"`
				GaiaName  string `json:"gaia_name"`
				IsDefault bool   `json:"is_using_default_name"`
			} `json:"info_cache"`
		} `json:"profile"`
	}

	if err := json.Unmarshal(localStateData, &localState); err != nil {
		return discoverChromiumProfilesByDir(dataDir)
	}

	for profileID, info := range localState.Profile.InfoCache {
		displayName := info.Name
		if displayName == "" {
			displayName = info.GaiaName
		}
		if displayName == "" {
			displayName = profileID
		}

		profiles = append(profiles, BrowserProfile{
			Name:      displayName,
			ID:        profileID,
			Email:     info.UserName,
			IsDefault: profileID == "Default",
		})
	}

	return profiles
}

// discoverChromiumProfilesByDir discovers profiles by scanning directories.
func discoverChromiumProfilesByDir(dataDir string) []BrowserProfile {
	var profiles []BrowserProfile

	entries, err := os.ReadDir(dataDir)
	if err != nil {
		return nil
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		name := entry.Name()
		// Check if it looks like a profile directory
		if name == "Default" || strings.HasPrefix(name, "Profile ") {
			profile := BrowserProfile{
				Name:      name,
				ID:        name,
				IsDefault: name == "Default",
			}

			// Try to read profile name from Preferences
			prefsPath := filepath.Join(dataDir, name, "Preferences")
			if data, err := os.ReadFile(prefsPath); err == nil {
				var prefs struct {
					Profile struct {
						Name string `json:"name"`
					} `json:"profile"`
					AccountInfo []struct {
						Email string `json:"email"`
					} `json:"account_info"`
				}
				if err := json.Unmarshal(data, &prefs); err == nil {
					if prefs.Profile.Name != "" {
						profile.Name = prefs.Profile.Name
					}
					if len(prefs.AccountInfo) > 0 {
						profile.Email = prefs.AccountInfo[0].Email
					}
				}
			}

			profiles = append(profiles, profile)
		}
	}

	return profiles
}

// discoverFirefoxProfiles reads Firefox profiles from profiles.ini.
func discoverFirefoxProfiles(dataDir string) []BrowserProfile {
	var profiles []BrowserProfile

	// Read profiles.ini
	iniPath := filepath.Join(dataDir, "profiles.ini")
	data, err := os.ReadFile(iniPath)
	if err != nil {
		return nil
	}

	// Simple INI parser for Firefox profiles.ini
	var currentProfile *BrowserProfile
	lines := strings.Split(string(data), "\n")

	for _, line := range lines {
		line = strings.TrimSpace(line)

		if strings.HasPrefix(line, "[Profile") {
			if currentProfile != nil {
				profiles = append(profiles, *currentProfile)
			}
			currentProfile = &BrowserProfile{}
			continue
		}

		if currentProfile == nil {
			continue
		}

		if strings.HasPrefix(line, "Name=") {
			currentProfile.Name = strings.TrimPrefix(line, "Name=")
			currentProfile.ID = currentProfile.Name
		} else if strings.HasPrefix(line, "Path=") {
			path := strings.TrimPrefix(line, "Path=")
			if currentProfile.ID == "" {
				currentProfile.ID = path
			}
		} else if strings.HasPrefix(line, "Default=1") {
			currentProfile.IsDefault = true
		}
	}

	if currentProfile != nil {
		profiles = append(profiles, *currentProfile)
	}

	return profiles
}

// findInPath looks for an executable in PATH.
func findInPath(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return path
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
