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
	"os"
	"os/exec"
	"path/filepath"

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
		// Write to .gemini/.env
		content := fmt.Sprintf("GEMINI_API_KEY=%s\n", apiKey)
		if err := os.WriteFile(envPath, []byte(content), 0600); err != nil {
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
	// For OAuth mode, remove cached credentials
	envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")
	if err := os.Remove(envPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove .env: %w", err)
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
		}
	case provider.AuthModeVertexADC:
		// Check for ADC credentials
		adcPath := filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json")
		if _, err := os.Stat(adcPath); err == nil {
			status.LoggedIn = true
		}
	default:
		// Check for cached Google login tokens
		configPath := filepath.Join(prof.HomePath(), ".config")
		if _, err := os.Stat(configPath); err == nil {
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
	locations := []struct {
		path        string
		description string
		validator   func(data []byte) (bool, string) // Custom validator
	}{
		{
			path:        filepath.Join(homeDir, ".gemini", "settings.json"),
			description: "Gemini CLI settings with Google OAuth state",
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
			path:        filepath.Join(homeDir, ".gemini", "oauth_credentials.json"),
			description: "Gemini CLI OAuth credentials cache",
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
			path:        filepath.Join(homeDir, ".gemini", ".env"),
			description: "Gemini API key (.env file)",
			validator: func(data []byte) (bool, string) {
				content := string(data)
				if len(content) > 0 {
					// Check if it contains GEMINI_API_KEY
					if contains(content, "GEMINI_API_KEY") {
						return true, ""
					}
					return false, "missing GEMINI_API_KEY"
				}
				return false, "empty file"
			},
		},
		{
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
		},
	}

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

// contains checks if s contains substr (simple implementation to avoid importing strings).
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
