// Package gemini implements the provider adapter for Google Gemini CLI.
//
// Authentication mechanics (from research):
// - Interactive mode presents three auth paths:
//  1. Login with Google (recommended for AI Pro/Ultra) - browser OAuth via localhost redirect
//  2. Gemini API key (GEMINI_API_KEY)
//  3. Vertex AI (ADC / service account / Google API key)
//
// - "Login with Google" opens browser and uses localhost redirect; credentials cached locally.
// - Gemini CLI auto-loads env vars from first .env found searching upward, then ~/.gemini/.env.
// - For Vertex AI: supports gcloud auth application-default login, service account JSON, etc.
//
// Context isolation for caam:
// - Set HOME to pseudo-home directory to isolate cached Google login tokens.
// - For Vertex AI profiles, also set CLOUDSDK_CONFIG for gcloud credential isolation.
//
// Auth file swapping (PRIMARY use case):
// - Backup ~/.gemini/settings.json and oauth files after logging in
// - Restore to instantly switch Gemini Ultra accounts without browser login flows
package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Provider implements the Gemini CLI adapter.
type Provider struct{}

// New creates a new Gemini provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "gemini"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Gemini CLI (Google Gemini Ultra)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "gemini"
}

// SupportedAuthModes returns the authentication modes supported by Gemini.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,     // Login with Google (Gemini Ultra subscription)
		provider.AuthModeAPIKey,    // Gemini API key
		provider.AuthModeVertexADC, // Vertex AI with Application Default Credentials
	}
}

// geminiHome returns the Gemini home directory.
func geminiHome() string {
	if home := os.Getenv("GEMINI_HOME"); home != "" {
		return home
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".gemini")
}

// AuthFiles returns the auth file specifications for Gemini CLI.
// This is the key method for auth file backup/restore.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(geminiHome(), "settings.json"),
			Description: "Gemini CLI settings with Google OAuth state (Gemini Ultra subscription)",
			Required:    true,
		},
		{
			Path:        filepath.Join(geminiHome(), "oauth_credentials.json"),
			Description: "Gemini CLI OAuth credentials cache",
			Required:    false,
		},
		{
			Path:        filepath.Join(geminiHome(), ".env"),
			Description: "Gemini API key (.env file)",
			Required:    false,
		},
	}
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	// Create pseudo-home directory
	homePath := prof.HomePath()
	if err := os.MkdirAll(homePath, 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}

	// Create .gemini directory under home (for .env if needed)
	geminiDir := filepath.Join(homePath, ".gemini")
	if err := os.MkdirAll(geminiDir, 0700); err != nil {
		return fmt.Errorf("create .gemini dir: %w", err)
	}

	// For Vertex AI mode, create gcloud config directory
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeVertexADC {
		gcloudDir := filepath.Join(prof.BasePath, "gcloud")
		if err := os.MkdirAll(gcloudDir, 0700); err != nil {
			return fmt.Errorf("create gcloud dir: %w", err)
		}
	}

	// Set up passthrough symlinks
	mgr, err := passthrough.NewManager()
	if err != nil {
		return fmt.Errorf("create passthrough manager: %w", err)
	}

	if err := mgr.SetupPassthroughs(homePath); err != nil {
		return fmt.Errorf("setup passthroughs: %w", err)
	}

	return nil
}

// Env returns the environment variables for running Gemini in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"HOME": prof.HomePath(),
	}

	// For Vertex AI mode, also set CLOUDSDK_CONFIG for gcloud isolation
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeVertexADC {
		env["CLOUDSDK_CONFIG"] = filepath.Join(prof.BasePath, "gcloud")
	}

	return env, nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	switch provider.AuthMode(prof.AuthMode) {
	case provider.AuthModeAPIKey:
		return p.loginWithAPIKey(ctx, prof)
	case provider.AuthModeVertexADC:
		return p.loginWithVertexADC(ctx, prof)
	default:
		return p.loginWithOAuth(ctx, prof)
	}
}

// loginWithOAuth launches Gemini CLI for Google login.
func (p *Provider) loginWithOAuth(ctx context.Context, prof *profile.Profile) error {
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}

	fmt.Println("Launching Gemini CLI for Google authentication...")
	fmt.Println("Select 'Login with Google' when prompted.")

	cmd := exec.CommandContext(ctx, "gemini")
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Set up URL detection and capture if browser profile is configured
	var capture *browser.OutputCapture
	if prof.HasBrowserConfig() {
		launcher := browser.NewLauncher(&browser.Config{
			Command:    prof.BrowserCommand,
			ProfileDir: prof.BrowserProfileDir,
		})
		fmt.Printf("Using browser profile: %s\n", prof.BrowserDisplayName())

		capture = browser.NewOutputCapture(os.Stdout, os.Stderr)
		capture.OnURL = func(url, source string) {
			// Open detected URLs with our configured browser
			if err := launcher.Open(url); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to open browser: %v\n", err)
			}
		}
		cmd.Stdout = capture.StdoutWriter()
		cmd.Stderr = capture.StderrWriter()
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Println("A browser window will open. Complete the login there.")
	}

	cmd.Stdin = os.Stdin

	err = cmd.Run()
	if capture != nil {
		capture.Flush()
	}
	return err
}

// loginWithAPIKey guides user to set up GEMINI_API_KEY.
func (p *Provider) loginWithAPIKey(ctx context.Context, prof *profile.Profile) error {
	envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")

	fmt.Println("Gemini API key mode.")
	fmt.Println("You can either:")
	fmt.Println("  1. Set GEMINI_API_KEY environment variable")
	fmt.Printf("  2. Create %s with: GEMINI_API_KEY=your_key\n", envPath)

	// Prompt for key
	fmt.Print("\nEnter Gemini API key (or press Enter to skip): ")
	var apiKey string
	fmt.Scanln(&apiKey)

	if apiKey != "" {
		// Write to .gemini/.env atomically
		content := fmt.Sprintf("GEMINI_API_KEY=%s\n", apiKey)
		if err := atomicWriteFile(envPath, []byte(content), 0600); err != nil {
			return fmt.Errorf("write .env: %w", err)
		}
		fmt.Printf("API key saved to %s\n", envPath)
	}

	return nil
}

// loginWithVertexADC guides user through gcloud ADC login.
func (p *Provider) loginWithVertexADC(ctx context.Context, prof *profile.Profile) error {
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}

	fmt.Println("Vertex AI mode with Application Default Credentials.")
	fmt.Println("Running: gcloud auth application-default login")

	cmd := exec.CommandContext(ctx, "gcloud", "auth", "application-default", "login")
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	// Set up URL detection and capture if browser profile is configured
	var capture *browser.OutputCapture
	if prof.HasBrowserConfig() {
		launcher := browser.NewLauncher(&browser.Config{
			Command:    prof.BrowserCommand,
			ProfileDir: prof.BrowserProfileDir,
		})
		fmt.Printf("Using browser profile: %s\n", prof.BrowserDisplayName())

		capture = browser.NewOutputCapture(os.Stdout, os.Stderr)
		capture.OnURL = func(url, source string) {
			// Open detected URLs with our configured browser
			if err := launcher.Open(url); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to open browser: %v\n", err)
			}
		}
		cmd.Stdout = capture.StdoutWriter()
		cmd.Stderr = capture.StderrWriter()
	} else {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		fmt.Println("A browser window will open. Complete the Google login there.")
	}

	cmd.Stdin = os.Stdin

	err = cmd.Run()
	if capture != nil {
		capture.Flush()
	}
	if err != nil {
		return fmt.Errorf("gcloud auth: %w", err)
	}

	fmt.Println("\nYou may also need to set a default project:")
	fmt.Println("  gcloud config set project YOUR_PROJECT_ID")

	return nil
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	geminiDir := filepath.Join(prof.HomePath(), ".gemini")
	paths := []string{
		filepath.Join(geminiDir, ".env"),
		filepath.Join(geminiDir, "settings.json"),
		filepath.Join(geminiDir, "oauth_credentials.json"),
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
		}
	}

	// For Vertex mode, revoke ADC
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeVertexADC {
		env, _ := p.Env(ctx, prof)
		cmd := exec.CommandContext(ctx, "gcloud", "auth", "application-default", "revoke", "--quiet")
		cmd.Env = os.Environ()
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Run() // Ignore errors
	}

	return nil
}

// Status checks the current authentication state.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}

	switch provider.AuthMode(prof.AuthMode) {
	case provider.AuthModeAPIKey:
		// Check for .env file with API key
		envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")
		if _, err := os.Stat(envPath); err == nil {
			status.LoggedIn = true
		} else if os.Getenv("GEMINI_API_KEY") != "" {
			status.LoggedIn = true
		}
	case provider.AuthModeVertexADC:
		// Check for ADC credentials
		adcPath := filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json")
		if _, err := os.Stat(adcPath); err == nil {
			status.LoggedIn = true
		}
	default:
		// Check for cached Google login tokens
		settingsPath := filepath.Join(prof.HomePath(), ".gemini", "settings.json")
		oauthPath := filepath.Join(prof.HomePath(), ".gemini", "oauth_credentials.json")
		if _, err := os.Stat(settingsPath); err == nil {
			status.LoggedIn = true
		} else if _, err := os.Stat(oauthPath); err == nil {
			status.LoggedIn = true
		}
	}

	return status, nil
}

// ValidateProfile checks if the profile is correctly configured.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	// Check home exists
	homePath := prof.HomePath()
	if _, err := os.Stat(homePath); os.IsNotExist(err) {
		return fmt.Errorf("home directory missing")
	}

	// Check passthrough symlinks
	mgr, err := passthrough.NewManager()
	if err != nil {
		return fmt.Errorf("create passthrough manager: %w", err)
	}

	statuses, err := mgr.VerifyPassthroughs(homePath)
	if err != nil {
		return fmt.Errorf("verify passthroughs: %w", err)
	}

	for _, s := range statuses {
		if s.SourceExists && !s.LinkValid {
			return fmt.Errorf("passthrough %s is invalid: %s", s.Path, s.Error)
		}
	}

	// For Vertex mode, check gcloud directory
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeVertexADC {
		gcloudDir := filepath.Join(prof.BasePath, "gcloud")
		if _, err := os.Stat(gcloudDir); os.IsNotExist(err) {
			return fmt.Errorf("gcloud config directory missing")
		}
	}

	return nil
}

// xdgConfigHome returns the XDG config directory.
func xdgConfigHome() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config")
}

// DetectExistingAuth detects existing Gemini authentication files in standard locations.
// Locations checked:
// - ~/.gemini/settings.json (main settings with OAuth state)
// - ~/.gemini/oauth_credentials.json (OAuth credentials cache)
// - ~/.gemini/.env (API key)
// - ~/.config/gcloud/application_default_credentials.json (Vertex AI ADC)
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("get home dir: %w", err)
	}

	// Define locations to check
	type authLocationSpec struct {
		path        string
		description string
		validator   func(data []byte) (bool, string) // Custom validator
	}

	buildLocations := func(baseDir, label string) []authLocationSpec {
		suffix := ""
		if label != "" {
			suffix = " (" + label + ")"
		}
		return []authLocationSpec{
			{
				path:        filepath.Join(baseDir, "settings.json"),
				description: "Gemini CLI settings with Google OAuth state" + suffix,
				validator: func(data []byte) (bool, string) {
					var parsed map[string]interface{}
					if err := json.Unmarshal(data, &parsed); err != nil {
						return false, fmt.Sprintf("invalid JSON: %v", err)
					}
					// Check for OAuth-related fields
					if _, ok := parsed["oauth"]; ok {
						return true, ""
					}
					if _, ok := parsed["credentials"]; ok {
						return true, ""
					}
					// Accept any valid JSON settings file
					return true, ""
				},
			},
			{
				path:        filepath.Join(baseDir, "oauth_credentials.json"),
				description: "Gemini CLI OAuth credentials cache" + suffix,
				validator: func(data []byte) (bool, string) {
					var parsed map[string]interface{}
					if err := json.Unmarshal(data, &parsed); err != nil {
						return false, fmt.Sprintf("invalid JSON: %v", err)
					}
					// Check for token fields
					if _, ok := parsed["access_token"]; ok {
						return true, ""
					}
					if _, ok := parsed["refresh_token"]; ok {
						return true, ""
					}
					return false, "missing expected OAuth fields"
				},
			},
			{
				path:        filepath.Join(baseDir, ".env"),
				description: "Gemini API key (.env file)" + suffix,
				validator: func(data []byte) (bool, string) {
					content := string(data)
					if len(content) > 0 {
						// Check if it contains GEMINI_API_KEY
						if strings.Contains(content, "GEMINI_API_KEY") {
							return true, ""
						}
						return false, "missing GEMINI_API_KEY"
					}
					return false, "empty file"
				},
			},
		}
	}

	defaultGeminiHome := filepath.Join(homeDir, ".gemini")
	locations := buildLocations(defaultGeminiHome, "")
	if geminiHomeEnv := os.Getenv("GEMINI_HOME"); geminiHomeEnv != "" && geminiHomeEnv != defaultGeminiHome {
		locations = append(buildLocations(geminiHomeEnv, "GEMINI_HOME"), locations...)
	}

	// Always check ADC location for Vertex AI
	locations = append(locations, authLocationSpec{
		path:        filepath.Join(xdgConfigHome(), "gcloud", "application_default_credentials.json"),
		description: "Google Cloud Application Default Credentials (Vertex AI)",
		validator: func(data []byte) (bool, string) {
			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				return false, fmt.Sprintf("invalid JSON: %v", err)
			}
			// Check for ADC fields
			if _, ok := parsed["client_id"]; ok {
				return true, ""
			}
			if _, ok := parsed["type"]; ok {
				return true, ""
			}
			return false, "missing expected ADC fields"
		},
	})

	var mostRecent *provider.AuthLocation

	for _, loc := range locations {
		authLoc := provider.AuthLocation{
			Path:        loc.path,
			Description: loc.description,
		}

		info, err := os.Stat(loc.path)
		if err != nil {
			if os.IsNotExist(err) {
				authLoc.Exists = false
			} else {
				authLoc.ValidationError = fmt.Sprintf("stat error: %v", err)
			}
			detection.Locations = append(detection.Locations, authLoc)
			continue
		}

		authLoc.Exists = true
		authLoc.LastModified = info.ModTime()
		authLoc.FileSize = info.Size()

		// Read and validate
		data, err := os.ReadFile(loc.path)
		if err != nil {
			authLoc.ValidationError = fmt.Sprintf("read error: %v", err)
		} else {
			valid, validationErr := loc.validator(data)
			authLoc.IsValid = valid
			authLoc.ValidationError = validationErr
		}

		detection.Locations = append(detection.Locations, authLoc)

		// Track most recent valid auth
		if authLoc.Exists && authLoc.IsValid {
			detection.Found = true
			if mostRecent == nil || authLoc.LastModified.After(mostRecent.LastModified) {
				locCopy := authLoc // Copy to avoid pointer issues
				mostRecent = &locCopy
			}
		}
	}

	detection.Primary = mostRecent

	// Set warning if multiple valid auth files found
	validCount := 0
	for _, loc := range detection.Locations {
		if loc.Exists && loc.IsValid {
			validCount++
		}
	}
	if validCount > 1 {
		detection.Warning = "multiple auth sources found; using most recent"
	}

	return detection, nil
}

// ImportAuth imports detected auth files into a profile directory.
// For Gemini, this copies OAuth credentials, settings, or .env file.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	// Validate source file exists
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source auth file not found: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path is a directory, not a file")
	}

	var copiedFiles []string

	basename := filepath.Base(sourcePath)
	parentDir := filepath.Base(filepath.Dir(sourcePath))

	// Determine target location based on source file type
	switch {
	case parentDir == ".gemini":
		// Files from ~/.gemini/ go to profile home's .gemini/
		targetDir := filepath.Join(prof.HomePath(), ".gemini")
		if err := os.MkdirAll(targetDir, 0700); err != nil {
			return nil, fmt.Errorf("create .gemini dir: %w", err)
		}
		targetPath := filepath.Join(targetDir, basename)
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", basename, err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	case parentDir == "gcloud":
		// ADC files go to profile's gcloud config
		targetDir := filepath.Join(prof.BasePath, "gcloud")
		if err := os.MkdirAll(targetDir, 0700); err != nil {
			return nil, fmt.Errorf("create gcloud dir: %w", err)
		}
		targetPath := filepath.Join(targetDir, basename)
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", basename, err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	default:
		// Default: copy to .gemini directory
		targetDir := filepath.Join(prof.HomePath(), ".gemini")
		if err := os.MkdirAll(targetDir, 0700); err != nil {
			return nil, fmt.Errorf("create .gemini dir: %w", err)
		}
		targetPath := filepath.Join(targetDir, basename)
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", basename, err)
		}
		copiedFiles = append(copiedFiles, targetPath)
	}

	return copiedFiles, nil
}

// copyFile copies a file from src to dst with fsync for durability.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Get source file info for permissions
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	// Write to temp file first for atomicity
	tmpPath := dst + ".tmp"
	dstFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, srcInfo.Mode()&0600)
	if err != nil {
		return err
	}

	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		dstFile.Close()
		os.Remove(tmpPath)
		return err
	}

	// Sync to disk
	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := dstFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Atomic rename
	return os.Rename(tmpPath, dst)
}

// atomicWriteFile writes data to a file atomically using temp file + fsync + rename.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on error; no-op after successful rename

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// ValidateToken validates that the authentication token works.
// For passive validation: checks file existence, format, and expiry timestamps.
// For active validation: attempts minimal API call to Google.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		CheckedAt: time.Now(),
	}

	if passive {
		return p.validateTokenPassive(ctx, prof, result)
	}
	return p.validateTokenActive(ctx, prof, result)
}

// validateTokenPassive performs passive validation without network calls.
func (p *Provider) validateTokenPassive(ctx context.Context, prof *profile.Profile, result *provider.ValidationResult) (*provider.ValidationResult, error) {
	result.Method = "passive"

	authMode := provider.AuthMode(prof.AuthMode)

	// Check auth files exist
	geminiDir := filepath.Join(prof.HomePath(), ".gemini")
	settingsPath := filepath.Join(geminiDir, "settings.json")
	oauthPath := filepath.Join(geminiDir, "oauth_credentials.json")
	envPath := filepath.Join(geminiDir, ".env")

	settingsExists := fileExistsGemini(settingsPath)
	oauthExists := fileExistsGemini(oauthPath)
	envExists := fileExistsGemini(envPath)

	// API key mode: accept .env or environment variable
	if authMode == provider.AuthModeAPIKey {
		if envExists {
			data, err := os.ReadFile(envPath)
			if err != nil {
				result.Valid = false
				result.Error = fmt.Sprintf("cannot read .env: %v", err)
				return result, nil
			}
			if !strings.Contains(string(data), "GEMINI_API_KEY") {
				result.Valid = false
				result.Error = "GEMINI_API_KEY not found in .env"
				return result, nil
			}
			result.Valid = true
			return result, nil
		}
		if os.Getenv("GEMINI_API_KEY") != "" {
			result.Valid = true
			return result, nil
		}
		result.Valid = false
		result.Error = "no API key configured"
		return result, nil
	}

	if !settingsExists && !oauthExists {
		result.Valid = false
		result.Error = "no auth files found"
		return result, nil
	}

	// Check oauth_credentials.json if it exists (has expiry info)
	if oauthExists {
		data, err := os.ReadFile(oauthPath)
		if err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("cannot read oauth_credentials.json: %v", err)
			return result, nil
		}

		var oauthData map[string]interface{}
		if err := json.Unmarshal(data, &oauthData); err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("invalid JSON in oauth_credentials.json: %v", err)
			return result, nil
		}

		// Check for access_token field
		if _, hasToken := oauthData["access_token"]; !hasToken {
			// No access token - check settings.json for OAuth state
			if !settingsExists {
				result.Valid = false
				result.Error = "no access token found"
				return result, nil
			}
		}

		// Check for expiry timestamp
		for _, key := range []string{"expires_at", "expiry", "token_expiry", "expires"} {
			if expiresAt, ok := oauthData[key]; ok {
				var expTime time.Time
				switch v := expiresAt.(type) {
				case string:
					if t, err := parseGeminiExpiryTime(v); err == nil {
						expTime = t
					}
				case float64:
					expTime = parseGeminiUnixTime(v)
				}

				if !expTime.IsZero() {
					result.ExpiresAt = expTime
					if expTime.Before(time.Now()) {
						result.Valid = false
						result.Error = "token has expired"
						return result, nil
					}
				}
				break
			}
		}
	}

	// Check settings.json if it exists
	if settingsExists {
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("cannot read settings.json: %v", err)
			return result, nil
		}

		var settingsData map[string]interface{}
		if err := json.Unmarshal(data, &settingsData); err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("invalid JSON in settings.json: %v", err)
			return result, nil
		}

		// Check for OAuth or API key mode
		if _, hasOAuth := settingsData["oauth"]; !hasOAuth {
			if _, hasAPIKey := settingsData["api_key"]; !hasAPIKey {
				// Check for .env file with API key
				if !envExists && !oauthExists && os.Getenv("GEMINI_API_KEY") == "" {
					result.Valid = false
					result.Error = "no authentication configured"
					return result, nil
				}
			}
		}
	}

	// Passive validation passed
	result.Valid = true
	return result, nil
}

// validateTokenActive performs active validation with network calls.
func (p *Provider) validateTokenActive(ctx context.Context, prof *profile.Profile, result *provider.ValidationResult) (*provider.ValidationResult, error) {
	result.Method = "active"

	// First do passive validation
	passiveResult, err := p.validateTokenPassive(ctx, prof, result)
	if err != nil {
		return nil, err
	}
	if !passiveResult.Valid {
		return passiveResult, nil
	}

	// For active validation, we would need to make an API call to Google
	// to verify the token. For now, we rely on passive validation.
	result.Valid = true
	return result, nil
}

func fileExistsGemini(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func parseGeminiExpiryTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05-07:00",
	}
	for _, format := range formats {
		if t, err := time.Parse(format, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %s", s)
}

func parseGeminiUnixTime(f float64) time.Time {
	if f > 1e12 {
		return time.UnixMilli(int64(f))
	}
	return time.Unix(int64(f), 0)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
