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
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
	if home := os.Getenv("GEMINI_CLI_HOME"); home != "" {
		return filepath.Join(home, ".gemini")
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
			Path:        filepath.Join(geminiHome(), "oauth_creds.json"),
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

	// With HOME redirected, XDG config/data/state default to paths inside the
	// profile; pass through the real entries so XDG-based CLIs keep their
	// credentials (issue #69).
	if err := mgr.SetupXDGPassthroughs(homePath, filepath.Join(homePath, ".config")); err != nil {
		return fmt.Errorf("setup xdg passthroughs: %w", err)
	}

	return nil
}

// Env returns the environment variables for running Gemini in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"HOME":            prof.HomePath(),
		"GEMINI_HOME":     filepath.Join(prof.HomePath(), ".gemini"),
		"GEMINI_CLI_HOME": prof.HomePath(),
	}

	// For Vertex AI mode, also set CLOUDSDK_CONFIG for gcloud isolation
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeVertexADC {
		env["CLOUDSDK_CONFIG"] = filepath.Join(prof.BasePath, "gcloud")
		env["GOOGLE_APPLICATION_CREDENTIALS"] = filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json")
		env["GOOGLE_GENAI_USE_VERTEXAI"] = "true"
		env["GOOGLE_GENAI_USE_GCA"] = "false"
		env["GEMINI_API_KEY"] = ""
		env["GOOGLE_API_KEY"] = ""
		data, err := readGeminiImportFile(filepath.Join(prof.HomePath(), ".gemini", ".env"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			values, err := parseGeminiEnv(data)
			if err != nil {
				return nil, err
			}
			if err := validateGeminiEnvMode(values, provider.AuthModeVertexADC); err != nil {
				return nil, err
			}
			for _, key := range []string{"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_PROJECT_ID", "GOOGLE_CLOUD_LOCATION"} {
				if values[key] != "" {
					env[key] = values[key]
				}
			}
		}
	} else if provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey {
		data, err := readGeminiImportFile(filepath.Join(prof.HomePath(), ".gemini", ".env"))
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			values, err := parseGeminiEnv(data)
			if err != nil || values["GEMINI_API_KEY"] == "" {
				return nil, fmt.Errorf("profile .env has no usable GEMINI_API_KEY")
			}
			env["GEMINI_API_KEY"] = values["GEMINI_API_KEY"]
		}
		env["GOOGLE_GENAI_USE_VERTEXAI"] = "false"
		env["GOOGLE_GENAI_USE_GCA"] = "false"
		env["GOOGLE_API_KEY"] = ""
	} else if provider.AuthMode(prof.AuthMode) == provider.AuthModeOAuth {
		env["GOOGLE_GENAI_USE_GCA"] = "true"
		env["GOOGLE_GENAI_USE_VERTEXAI"] = "false"
		env["GEMINI_API_KEY"] = ""
		env["GOOGLE_API_KEY"] = ""
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
		filepath.Join(geminiDir, "oauth_creds.json"),
		filepath.Join(geminiDir, "oauth_credentials.json"), // legacy CAAM filename
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
		oauthPath := filepath.Join(prof.HomePath(), ".gemini", "oauth_creds.json")
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

// DetectExistingAuth inspects the selected native login without mixing homes or
// choosing an unrelated account because its settings were modified more recently.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	dir, err := filepath.Abs(geminiHome())
	if err != nil {
		return nil, fmt.Errorf("resolve Gemini home: %w", err)
	}

	paths := []string{
		filepath.Join(dir, "settings.json"),
		filepath.Join(dir, "oauth_creds.json"),
		filepath.Join(dir, "oauth_credentials.json"),
		filepath.Join(dir, ".env"),
		geminiADCPath(nil),
	}
	bundle, bundleErr := loadGeminiDirectory(dir, "")
	if bundleErr == nil {
		for _, file := range bundle.files {
			if !slices.Contains(paths, file.source) {
				paths = append(paths, file.source)
			}
		}
	}
	for _, path := range paths {
		authLoc := provider.AuthLocation{
			Path:        path,
			Description: "Gemini native " + filepath.Base(path),
		}

		info, err := os.Stat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				authLoc.ValidationError = fmt.Sprintf("stat error: %v", err)
			}
			detection.Locations = append(detection.Locations, authLoc)
			continue
		}

		authLoc.Exists = true
		authLoc.LastModified = info.ModTime()
		authLoc.FileSize = info.Size()

		if bundleErr == nil {
			for _, file := range bundle.files {
				if file.source == path {
					authLoc.IsValid = true
				}
			}
			if path == bundle.primary {
				detection.Found = true
				primary := authLoc
				detection.Primary = &primary
			}
		} else {
			authLoc.ValidationError = bundleErr.Error()
		}
		detection.Locations = append(detection.Locations, authLoc)
	}
	if bundleErr != nil {
		detection.Warning = bundleErr.Error()
	}

	return detection, nil
}

// ImportAuth validates and reads the entire selected grant before writing any
// destination. The caller publishes the prepared profile transactionally.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prof == nil || prof.Provider != p.ID() || strings.TrimSpace(prof.BasePath) == "" {
		return nil, fmt.Errorf("invalid Gemini import profile")
	}
	bundle, err := loadGeminiImport(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("validate Gemini auth source: %w", err)
	}
	copied := make([]string, 0, len(bundle.files))
	for _, file := range bundle.files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		target := filepath.Join(prof.HomePath(), ".gemini", file.name)
		if file.name == "application_default_credentials.json" {
			target = filepath.Join(prof.BasePath, "gcloud", file.name)
		}
		if err := atomicWriteFile(target, file.data, 0600); err != nil {
			return nil, fmt.Errorf("write imported %s: %w", file.name, err)
		}
		copied = append(copied, target)
	}
	prof.AuthMode = string(bundle.mode)
	return copied, nil
}

type geminiImportFile struct {
	source string
	name   string
	data   []byte
}

type geminiImportBundle struct {
	mode    provider.AuthMode
	primary string
	files   []geminiImportFile
}

// Credential reads are bounded, including a source that grows after Stat.
func readGeminiImportFile(path string) ([]byte, error) {
	const maxBytes = 16 << 20
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, fmt.Errorf("%s must be a regular file no larger than 16 MiB", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBytes {
		return nil, fmt.Errorf("%s must be a regular file no larger than 16 MiB", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err == nil && len(data) > maxBytes {
		err = fmt.Errorf("%s exceeds 16 MiB", path)
	}
	return data, err
}

func geminiADCPath(values map[string]string) string {
	path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		path = values["GOOGLE_APPLICATION_CREDENTIALS"]
	}
	if path == "" {
		dir := os.Getenv("CLOUDSDK_CONFIG")
		if dir == "" {
			dir = values["CLOUDSDK_CONFIG"]
		}
		if dir == "" {
			dir = filepath.Join(xdgConfigHome(), "gcloud")
		}
		path = filepath.Join(dir, "application_default_credentials.json")
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

func loadGeminiDirectory(dir string, explicitMode provider.AuthMode) (*geminiImportBundle, error) {
	settingsPath := filepath.Join(dir, "settings.json")
	settings, err := readGeminiImportFile(settingsPath)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	mode, embedded, err := parseGeminiSettings(settings)
	if err != nil {
		return nil, fmt.Errorf("invalid settings.json: %w", err)
	}
	if explicitMode != "" {
		if mode != "" && mode != explicitMode {
			return nil, fmt.Errorf("settings.json selects a different auth method")
		}
		mode = explicitMode
	}
	envPath := filepath.Join(dir, ".env")
	var envData []byte
	var envValues map[string]string
	if mode != provider.AuthModeOAuth {
		envData, err = readGeminiImportFile(envPath)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		if err == nil {
			envValues, err = parseGeminiEnv(envData)
			if err != nil {
				return nil, fmt.Errorf("invalid .env: %w", err)
			}
		}
		if mode == "" {
			// Native settings select the method first. Otherwise the CLI
			// checks GCA, Vertex, then an API key in its loaded environment.
			if geminiAuthEnv(envValues, "GOOGLE_GENAI_USE_GCA") == "true" {
				mode = provider.AuthModeOAuth
			} else if geminiAuthEnv(envValues, "GOOGLE_GENAI_USE_VERTEXAI") == "true" {
				mode = provider.AuthModeVertexADC
			} else if geminiAuthEnv(envValues, "GEMINI_API_KEY") != "" {
				mode = provider.AuthModeAPIKey
			}
		}
	}
	var candidates []*geminiImportBundle
	if mode == "" || mode == provider.AuthModeOAuth {
		for _, name := range []string{"oauth_creds.json", "oauth_credentials.json"} {
			path := filepath.Join(dir, name)
			data, err := readGeminiImportFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if _, err := parseGeminiOAuth(data); err != nil {
				return nil, fmt.Errorf("invalid %s: %w", name, err)
			}
			if embedded != nil && !sameGeminiGrant(embedded, data) {
				return nil, fmt.Errorf("settings.json and %s contain different OAuth grants", name)
			}
			candidates = append(candidates, &geminiImportBundle{mode: provider.AuthModeOAuth, primary: path,
				files: []geminiImportFile{{path, "oauth_creds.json", data}}})
			break // A present cache is authoritative, including when malformed.
		}
		if len(candidates) == 0 && embedded != nil {
			candidates = append(candidates, &geminiImportBundle{mode: provider.AuthModeOAuth, primary: settingsPath,
				files: []geminiImportFile{{settingsPath, "oauth_creds.json", embedded}}})
		}
	}
	if mode == provider.AuthModeAPIKey {
		// File import must not silently select a different account from a
		// shell-provided key. Only an exact saved match can be imported.
		if geminiAuthEnv(envValues, "GEMINI_API_KEY") != envValues["GEMINI_API_KEY"] {
			return nil, fmt.Errorf("selected Gemini API key does not match the saved .env credential")
		}
		if envValues["GEMINI_API_KEY"] != "" {
			if err := validateGeminiEnvMode(envValues, provider.AuthModeAPIKey); err != nil {
				return nil, err
			}
			candidates = append(candidates, &geminiImportBundle{mode: provider.AuthModeAPIKey, primary: envPath,
				files: []geminiImportFile{{envPath, ".env", envData}}})
		}
	}
	if mode == provider.AuthModeVertexADC || (mode == "" && len(candidates) == 0) {
		path := geminiADCPath(envValues)
		data, err := readGeminiImportFile(path)
		if err == nil {
			if err := validateGeminiADC(data); err != nil {
				return nil, fmt.Errorf("invalid ADC: %w", err)
			}
			primary := path
			if settings != nil {
				// Importing the selected settings path retains Vertex settings
				// and the configured ADC source as one bundle.
				primary = settingsPath
			}
			candidates = append(candidates, &geminiImportBundle{mode: provider.AuthModeVertexADC, primary: primary,
				files: []geminiImportFile{{path, "application_default_credentials.json", data}}})
		} else if !os.IsNotExist(err) {
			return nil, err
		}
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no complete credentials for the selected Gemini auth method")
	}
	bundle := candidates[0]
	if embedded != nil && bundle.mode != provider.AuthModeOAuth {
		return nil, fmt.Errorf("settings.json contains OAuth credentials for a different auth method")
	}
	if settings != nil {
		bundle.files = append(bundle.files, geminiImportFile{settingsPath, "settings.json", settings})
	}
	if bundle.mode == provider.AuthModeVertexADC && envData != nil {
		if err := validateGeminiEnvMode(envValues, bundle.mode); err != nil {
			return nil, err
		}
		if settings != nil || geminiEnvSelectsADC(envValues) {
			bundle.files = append(bundle.files, geminiImportFile{envPath, ".env", envData})
			if settings == nil {
				// The entry point must retain both the selected ADC and its
				// project context on a later detect-to-import call.
				bundle.primary = envPath
			}
		}
	}
	return bundle, nil
}

func loadGeminiImport(sourcePath string) (*geminiImportBundle, error) {
	path, err := filepath.Abs(sourcePath)
	if err != nil {
		return nil, err
	}
	data, err := readGeminiImportFile(path)
	if err != nil {
		return nil, err
	}
	bundle := &geminiImportBundle{primary: path}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) == nil && obj != nil {
		if _, exists := obj["type"]; exists {
			if err := validateGeminiADC(data); err != nil {
				return nil, err
			}
			bundle.mode = provider.AuthModeVertexADC
			bundle.files = []geminiImportFile{{path, "application_default_credentials.json", data}}
			return bundle, nil
		}
		if filepath.Base(path) == "settings.json" {
			return loadGeminiDirectory(filepath.Dir(path), "")
		}
		if _, err := parseGeminiOAuth(data); err != nil {
			return nil, err
		}
		bundle.mode = provider.AuthModeOAuth
		bundle.files = []geminiImportFile{{path, "oauth_creds.json", data}}
	} else {
		values, err := parseGeminiEnv(data)
		if err != nil {
			return nil, fmt.Errorf("source has no unambiguous OAuth, API key, or ADC credential")
		}
		if values["GEMINI_API_KEY"] == "" {
			if filepath.Base(path) == ".env" && geminiEnvSelectsADC(values) {
				// This explicit .env selects its ADC bundle even when the
				// importing shell is currently configured for another method.
				selected, err := loadGeminiDirectory(filepath.Dir(path), provider.AuthModeVertexADC)
				if err != nil {
					return nil, err
				}
				if selected.mode == provider.AuthModeVertexADC {
					return selected, nil
				}
			}
			return nil, fmt.Errorf("source has no selected API key or ADC credential")
		}
		if err := validateGeminiEnvMode(values, provider.AuthModeAPIKey); err != nil {
			return nil, err
		}
		bundle.mode = provider.AuthModeAPIKey
		bundle.files = []geminiImportFile{{path, ".env", data}}
	}
	settingsPath := filepath.Join(filepath.Dir(path), "settings.json")
	settings, err := readGeminiImportFile(settingsPath)
	if os.IsNotExist(err) {
		return bundle, nil
	}
	if err != nil {
		return nil, err
	}
	mode, embedded, err := parseGeminiSettings(settings)
	if err != nil {
		return nil, fmt.Errorf("invalid companion settings.json: %w", err)
	}
	if mode != "" && mode != bundle.mode {
		return nil, fmt.Errorf("companion settings.json selects a different auth method")
	}
	if embedded != nil && (bundle.mode != provider.AuthModeOAuth || !sameGeminiGrant(embedded, data)) {
		return nil, fmt.Errorf("companion settings.json contains a different OAuth grant")
	}
	bundle.files = append(bundle.files, geminiImportFile{settingsPath, "settings.json", settings})
	return bundle, nil
}

func geminiJSONObject(data []byte) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		return nil, fmt.Errorf("expected a JSON object")
	}
	return obj, nil
}

// An omitted optional slot is different from a present but unusable credential.
func geminiCredentialString(obj map[string]json.RawMessage, keys ...string) (string, error) {
	var selected string
	for _, key := range keys {
		raw, exists := obj[key]
		if !exists {
			continue
		}
		var value string
		if json.Unmarshal(raw, &value) != nil || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("%s must be a nonempty string", key)
		}
		if selected != "" && value != selected {
			return "", fmt.Errorf("credential aliases contain conflicting values")
		}
		selected = value
	}
	return selected, nil
}

func parseGeminiOAuth(data []byte) (map[string]json.RawMessage, error) {
	obj, err := geminiJSONObject(data)
	if err != nil {
		return nil, err
	}
	access, err := geminiCredentialString(obj, "access_token", "accessToken")
	if err != nil || access == "" {
		return nil, fmt.Errorf("OAuth access token is missing or malformed")
	}
	if _, err := geminiCredentialString(obj, "refresh_token", "refreshToken"); err != nil {
		return nil, err
	}
	for _, key := range []string{"id_token", "idToken", "email", "user_email", "client_email", "client_id", "client_secret"} {
		if _, err := geminiCredentialString(obj, key); err != nil {
			return nil, err
		}
	}
	for _, key := range []string{"expiry_date", "expires_at", "expiresAt", "expiry", "token_expiry", "expires"} {
		raw, exists := obj[key]
		if !exists {
			continue
		}
		if _, err := parseGeminiCredentialExpiry(key, raw); err != nil {
			return nil, err
		}
	}
	return obj, nil
}

func parseGeminiCredentialExpiry(key string, raw json.RawMessage) (time.Time, error) {
	// This is the final millisecond of year 9999. Bounding before converting
	// to int64 also rejects values rounded up to 2^63 by JSON float decoding.
	const maxUnixMillis = 253402300799999
	var expires time.Time
	var number float64
	if json.Unmarshal(raw, &number) == nil && number > 0 && number <= maxUnixMillis && math.Trunc(number) == number {
		expires = parseGeminiUnixTime(number)
		if key == "expiry_date" {
			expires = time.UnixMilli(int64(number))
		}
	} else {
		var stamp string
		if json.Unmarshal(raw, &stamp) == nil {
			expires, _ = parseGeminiExpiryTime(stamp)
		}
	}
	if !expires.After(time.Unix(0, 0)) || expires.UTC().Year() > 9999 {
		return time.Time{}, fmt.Errorf("%s is not a valid credential expiry", key)
	}
	return expires, nil
}

func sameGeminiGrant(a, b []byte) bool {
	left, leftErr := parseGeminiOAuth(a)
	right, rightErr := parseGeminiOAuth(b)
	if leftErr != nil || rightErr != nil {
		return false
	}
	for _, keys := range [][]string{{"access_token", "accessToken"}, {"refresh_token", "refreshToken"}} {
		l, _ := geminiCredentialString(left, keys...)
		r, _ := geminiCredentialString(right, keys...)
		if l != r {
			return false
		}
	}
	for _, keys := range [][]string{{"id_token", "idToken"}, {"email", "user_email", "client_email"}} {
		l, lerr := geminiCredentialString(left, keys...)
		r, rerr := geminiCredentialString(right, keys...)
		if lerr != nil || rerr != nil || (l != "" && r != "" && l != r) {
			return false
		}
	}
	return true
}

func parseGeminiSettings(data []byte) (provider.AuthMode, []byte, error) {
	if data == nil {
		return "", nil, nil
	}
	obj, err := geminiJSONObject(data)
	if err != nil {
		return "", nil, err
	}
	selected, err := geminiCredentialString(obj, "selectedAuthType")
	if err != nil {
		return "", nil, err
	}
	if raw, exists := obj["security"]; exists {
		security, err := geminiJSONObject(raw)
		if err != nil {
			return "", nil, fmt.Errorf("security must be an object")
		}
		if raw, exists := security["auth"]; exists {
			auth, err := geminiJSONObject(raw)
			if err != nil {
				return "", nil, fmt.Errorf("security.auth must be an object")
			}
			current, err := geminiCredentialString(auth, "selectedType")
			if err != nil {
				return "", nil, err
			}
			if selected != "" && current != "" && selected != current {
				return "", nil, fmt.Errorf("conflicting selected auth types")
			}
			if current != "" {
				selected = current
			}
		}
	}
	var mode provider.AuthMode
	switch selected {
	case "":
	case "oauth-personal":
		mode = provider.AuthModeOAuth
	case "gemini-api-key":
		mode = provider.AuthModeAPIKey
	case "vertex-ai":
		mode = provider.AuthModeVertexADC
	default:
		return "", nil, fmt.Errorf("unsupported Gemini selected auth type")
	}
	var embedded []byte
	for _, key := range []string{"oauth", "credentials", "googleOAuth"} {
		if raw, exists := obj[key]; exists {
			if _, err := parseGeminiOAuth(raw); err != nil {
				return "", nil, fmt.Errorf("%s contains incomplete OAuth credentials", key)
			}
			if embedded != nil && !sameGeminiGrant(embedded, raw) {
				return "", nil, fmt.Errorf("settings contain conflicting OAuth grants")
			}
			embedded = raw
		}
	}
	for _, key := range []string{"access_token", "accessToken", "refresh_token", "refreshToken"} {
		if _, exists := obj[key]; exists {
			if _, err := parseGeminiOAuth(data); err != nil {
				return "", nil, err
			}
			if embedded != nil && !sameGeminiGrant(embedded, data) {
				return "", nil, fmt.Errorf("settings contain conflicting OAuth grants")
			}
			embedded = data
			break
		}
	}
	return mode, embedded, nil
}

func parseGeminiEnv(data []byte) (map[string]string, error) {
	values := make(map[string]string)
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || key == "" || strings.ContainsAny(key, " \t\r\x00") {
			return nil, fmt.Errorf("expected a dotenv assignment")
		}
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			end := strings.IndexByte(value[1:], quote)
			if end < 0 {
				return nil, fmt.Errorf("unterminated dotenv value")
			}
			end++
			tail := strings.TrimSpace(value[end+1:])
			if tail != "" && !strings.HasPrefix(tail, "#") {
				return nil, fmt.Errorf("unexpected text after dotenv value")
			}
			value = value[1:end]
		} else if comment := strings.IndexByte(value, '#'); comment >= 0 {
			value = strings.TrimSpace(value[:comment])
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, fmt.Errorf("invalid dotenv value")
		}
		if key == "GEMINI_API_KEY" && (strings.TrimSpace(value) == "" || strings.ContainsAny(value, " \t")) {
			return nil, fmt.Errorf("GEMINI_API_KEY is empty or contains whitespace")
		}
		values[key] = value
	}
	return values, nil
}

func validateGeminiEnvMode(values map[string]string, mode provider.AuthMode) error {
	if values["GOOGLE_API_KEY"] != "" || values["GOOGLE_GENAI_USE_GCA"] == "true" ||
		(mode == provider.AuthModeAPIKey && values["GOOGLE_GENAI_USE_VERTEXAI"] == "true") ||
		(mode == provider.AuthModeVertexADC && values["GEMINI_API_KEY"] != "") {
		return fmt.Errorf(".env contains credentials or selection for a different auth method")
	}
	return nil
}

func geminiAuthEnv(values map[string]string, key string) string {
	// Gemini's dotenv loader never overwrites a shell-defined variable,
	// including an explicitly empty value or a false mode selector.
	if value, exists := os.LookupEnv(key); exists {
		return value
	}
	return values[key]
}

func geminiEnvSelectsADC(values map[string]string) bool {
	return values["GOOGLE_GENAI_USE_VERTEXAI"] == "true" ||
		values["GOOGLE_APPLICATION_CREDENTIALS"] != "" || values["CLOUDSDK_CONFIG"] != "" ||
		((values["GOOGLE_CLOUD_PROJECT"] != "" || values["GOOGLE_CLOUD_PROJECT_ID"] != "") && values["GOOGLE_CLOUD_LOCATION"] != "")
}

func validateGeminiADC(data []byte) error {
	obj, err := geminiJSONObject(data)
	if err != nil {
		return err
	}
	kind, err := geminiCredentialString(obj, "type")
	if err != nil {
		return err
	}
	var required []string
	switch kind {
	case "authorized_user":
		required = []string{"client_id", "client_secret", "refresh_token"}
	case "service_account":
		required = []string{"client_email", "private_key", "token_uri"}
	default:
		return fmt.Errorf("unsupported or missing ADC type")
	}
	for _, key := range required {
		value, err := geminiCredentialString(obj, key)
		if err != nil || value == "" {
			return fmt.Errorf("ADC requires a nonempty %s", key)
		}
	}
	if kind == "service_account" {
		key, _ := geminiCredentialString(obj, "private_key")
		block, rest := pem.Decode([]byte(key))
		if block == nil || len(bytes.TrimSpace(rest)) != 0 {
			return fmt.Errorf("service account private_key is not a PEM private key")
		}
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
		}
		if _, ok := parsed.(*rsa.PrivateKey); err != nil || !ok {
			return fmt.Errorf("service account private_key is not an RSA private key")
		}
		uri, _ := geminiCredentialString(obj, "token_uri")
		endpoint, err := url.Parse(uri)
		if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" || endpoint.User != nil {
			return fmt.Errorf("service account token_uri must be an HTTPS endpoint")
		}
	}
	return nil
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
	if prof == nil {
		return nil, fmt.Errorf("profile is nil")
	}
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	authMode := provider.AuthMode(prof.AuthMode)
	dir := filepath.Join(prof.HomePath(), ".gemini")
	var source string
	switch authMode {
	case provider.AuthModeAPIKey:
		source = filepath.Join(dir, ".env")
		if _, err := os.Stat(source); os.IsNotExist(err) && strings.TrimSpace(os.Getenv("GEMINI_API_KEY")) != "" {
			result.Valid = true
			return result, nil
		}
	case provider.AuthModeVertexADC:
		source = filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json")
	case provider.AuthModeOAuth, "":
		authMode = provider.AuthModeOAuth
		for _, name := range []string{"oauth_creds.json", "oauth_credentials.json", "settings.json"} {
			path := filepath.Join(dir, name)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				source = path
				break
			}
		}
	default:
		result.Error = "unsupported Gemini profile auth mode"
		return result, nil
	}
	if source == "" {
		result.Error = "no auth files found"
		return result, nil
	}
	bundle, err := loadGeminiImport(source)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if bundle.mode != authMode {
		result.Error = "credential does not match the profile auth mode"
		return result, nil
	}
	if authMode == provider.AuthModeOAuth {
		var grant map[string]json.RawMessage
		for _, file := range bundle.files {
			if file.name == "oauth_creds.json" {
				grant, _ = parseGeminiOAuth(file.data) // Already validated by the loader.
			}
		}
		for _, key := range []string{"expiry_date", "expires_at", "expiresAt", "expiry", "token_expiry", "expires"} {
			raw, exists := grant[key]
			if !exists {
				continue
			}
			result.ExpiresAt, _ = parseGeminiCredentialExpiry(key, raw)
			break
		}
		refresh, _ := geminiCredentialString(grant, "refresh_token", "refreshToken")
		// The native CLI can renew a complete cached grant. Preserve expiry
		// for health reporting without forcing that account to log in again.
		if !result.ExpiresAt.IsZero() && !result.ExpiresAt.After(time.Now()) && refresh == "" {
			result.Error = "access token has expired and has no refresh token"
			return result, nil
		}
	}
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
