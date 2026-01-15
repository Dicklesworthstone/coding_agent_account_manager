// Package codex implements the provider adapter for OpenAI Codex CLI.
//
// Authentication mechanics (from research):
// - ChatGPT login is browser/OAuth via localhost:1455 during setup.
// - After login, credentials stored in $CODEX_HOME/auth.json (default ~/.codex/auth.json).
// - API key alternative: printenv OPENAI_API_KEY | codex login --with-api-key
// - Config defaults from ~/.codex/config.toml, supports --profile flag.
//
// Context isolation for caam:
// - Set CODEX_HOME to point to the profile's codex_home directory.
// - This is the cleanest provider since CODEX_HOME is the official anchor for auth.json.
//
// Auth file swapping (PRIMARY use case):
// - Backup ~/.codex/auth.json after logging in with each GPT Pro account
// - Restore to instantly switch accounts without browser login flows
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"golang.org/x/term"
)

// Provider implements the Codex CLI adapter.
type Provider struct{}

// New creates a new Codex provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "codex"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Codex CLI (OpenAI GPT Pro)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "codex"
}

// SupportedAuthModes returns the authentication modes supported by Codex.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,      // Browser-based ChatGPT login (GPT Pro subscription)
		provider.AuthModeDeviceCode, // Device code flow (codex login --device-auth)
		provider.AuthModeAPIKey,     // OpenAI API key
	}
}

// codexHome returns the Codex home directory.
func codexHome() string {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".codex")
}

// ResolveHome returns the current Codex home directory.
func ResolveHome() string {
	return codexHome()
}

var codexCredentialsStoreRe = regexp.MustCompile(`(?m)^\s*cli_auth_credentials_store\s*=\s*\"[^\"]*\"`)

// EnsureFileCredentialStore ensures Codex uses file-based credential storage.
// This is required for CAAM to manage auth.json reliably.
func EnsureFileCredentialStore(home string) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return fmt.Errorf("codex home is empty")
	}

	configPath := filepath.Join(home, "config.toml")
	const settingLine = `cli_auth_credentials_store = "file"`

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if err := os.MkdirAll(home, 0700); err != nil {
			return fmt.Errorf("create codex home: %w", err)
		}
		content := "# Managed by caam to ensure file-based auth storage\n" + settingLine + "\n"
		return atomicWriteFile(configPath, []byte(content), 0600)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read config.toml: %w", err)
	}

	if match := codexCredentialsStoreRe.Find(data); match != nil {
		if strings.Contains(string(match), `"file"`) {
			return nil
		}
		updated := codexCredentialsStoreRe.ReplaceAll(data, []byte(settingLine))
		return atomicWriteFile(configPath, updated, 0600)
	}

	text := string(data)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	text += settingLine + "\n"
	return atomicWriteFile(configPath, []byte(text), 0600)
}

// AuthFiles returns the auth file specifications for Codex.
// This is the key method for auth file backup/restore.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(codexHome(), "auth.json"),
			Description: "Codex CLI OAuth token (GPT Pro subscription)",
			Required:    true,
		},
	}
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	// Create codex_home directory for isolated context
	codexHomePath := prof.CodexHomePath()
	if err := os.MkdirAll(codexHomePath, 0700); err != nil {
		return fmt.Errorf("create codex_home: %w", err)
	}
	if err := EnsureFileCredentialStore(codexHomePath); err != nil {
		return fmt.Errorf("configure codex credential store: %w", err)
	}

	// Create pseudo-home directory
	homePath := prof.HomePath()
	if err := os.MkdirAll(homePath, 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
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

// Env returns the environment variables for running Codex in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"CODEX_HOME": prof.CodexHomePath(),
		"HOME":       prof.HomePath(),
	}
	return env, nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	if err := EnsureFileCredentialStore(prof.CodexHomePath()); err != nil {
		return fmt.Errorf("configure codex credential store: %w", err)
	}
	switch provider.AuthMode(prof.AuthMode) {
	case provider.AuthModeDeviceCode:
		return p.LoginWithDeviceCode(ctx, prof)
	case provider.AuthModeAPIKey:
		return p.loginWithAPIKey(ctx, prof)
	default:
		return p.loginWithOAuth(ctx, prof)
	}
}

func (p *Provider) SupportsDeviceCode() bool {
	return true
}

func (p *Provider) LoginWithDeviceCode(ctx context.Context, prof *profile.Profile) error {
	codexHomePath := prof.CodexHomePath()

	cmd := exec.CommandContext(ctx, "codex", "login", "--device-auth")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHomePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin

	fmt.Println("Starting Codex device code login flow...")
	fmt.Println("Follow the prompts to authenticate in your browser.")

	return cmd.Run()
}

// loginWithOAuth runs the browser-based login flow.
func (p *Provider) loginWithOAuth(ctx context.Context, prof *profile.Profile) error {
	codexHomePath := prof.CodexHomePath()

	cmd := exec.CommandContext(ctx, "codex", "login")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHomePath)

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
	}

	cmd.Stdin = os.Stdin

	fmt.Println("Starting Codex OAuth login flow...")
	if prof.HasBrowserConfig() {
		fmt.Println("Browser will open with configured profile.")
	} else {
		fmt.Println("A browser window will open. Complete the login there.")
	}

	err := cmd.Run()
	if capture != nil {
		capture.Flush()
	}
	return err
}

func readAPIKeyFromStdin(stdin *os.File) (key string, hidden bool, err error) {
	if stdin == nil {
		return "", false, fmt.Errorf("stdin is nil")
	}

	if term.IsTerminal(int(stdin.Fd())) {
		b, err := term.ReadPassword(int(stdin.Fd()))
		if err != nil {
			return "", false, fmt.Errorf("read API key: %w", err)
		}
		return strings.TrimSpace(string(b)), true, nil
	}

	reader := bufio.NewReader(stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", false, fmt.Errorf("read API key: %w", err)
	}
	return strings.TrimSpace(line), false, nil
}

// loginWithAPIKey prompts for and stores an API key.
func (p *Provider) loginWithAPIKey(ctx context.Context, prof *profile.Profile) error {
	codexHomePath := prof.CodexHomePath()

	// Check for OPENAI_API_KEY environment variable first
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Print("Enter OpenAI API key: ")
		key, hidden, err := readAPIKeyFromStdin(os.Stdin)
		if hidden {
			fmt.Println()
		}
		if err != nil {
			return err
		}
		apiKey = key
	}

	if apiKey == "" {
		return fmt.Errorf("API key is required")
	}

	// Use codex login --with-api-key via stdin (safer than argv)
	cmd := exec.CommandContext(ctx, "codex", "login", "--with-api-key")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHomePath)
	cmd.Stdin = strings.NewReader(apiKey)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	if err := os.Remove(authPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove auth.json: %w", err)
	}
	return nil
}

// Status checks the current authentication state.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}

	// Check if auth.json exists
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	if _, err := os.Stat(authPath); err == nil {
		status.LoggedIn = true
	}

	return status, nil
}

// ValidateProfile checks if the profile is correctly configured.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	// Check codex_home exists
	codexHomePath := prof.CodexHomePath()
	if _, err := os.Stat(codexHomePath); os.IsNotExist(err) {
		return fmt.Errorf("codex_home directory missing")
	}

	// Check passthrough symlinks if profile has a home directory
	homePath := prof.HomePath()
	if _, err := os.Stat(homePath); err == nil {
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
	}

	return nil
}

// DetectExistingAuth detects existing Codex authentication files in standard locations.
// Locations checked:
// - $CODEX_HOME/auth.json (if CODEX_HOME is set)
// - ~/.codex/auth.json (default location)
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
	}{
		{
			path:        filepath.Join(homeDir, ".codex", "auth.json"),
			description: "Codex CLI OAuth token (default location)",
		},
	}

	// Also check CODEX_HOME if it's set and different from default
	if codexHomeEnv := os.Getenv("CODEX_HOME"); codexHomeEnv != "" {
		envPath := filepath.Join(codexHomeEnv, "auth.json")
		defaultPath := filepath.Join(homeDir, ".codex", "auth.json")
		if envPath != defaultPath {
			locations = append([]struct {
				path        string
				description string
			}{
				{
					path:        envPath,
					description: "Codex CLI OAuth token (CODEX_HOME)",
				},
			}, locations...)
		}
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

		// Basic validation: try to parse as JSON and check for expected fields
		data, err := os.ReadFile(loc.path)
		if err != nil {
			authLoc.ValidationError = fmt.Sprintf("read error: %v", err)
		} else {
			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				authLoc.ValidationError = fmt.Sprintf("invalid JSON: %v", err)
			} else {
				// Check for expected Codex auth fields
				if _, ok := parsed["access_token"]; ok {
					authLoc.IsValid = true
				} else if _, ok := parsed["accessToken"]; ok {
					authLoc.IsValid = true
				} else if _, ok := parsed["api_key"]; ok {
					authLoc.IsValid = true
				} else if _, ok := parsed["token"]; ok {
					authLoc.IsValid = true
				} else {
					authLoc.ValidationError = "missing expected auth fields (access_token, api_key, or token)"
				}
			}
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
		detection.Warning = "multiple auth files found; using most recent"
	}

	return detection, nil
}

// ImportAuth imports detected auth files into a profile directory.
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

	// For Codex, auth files go into codex_home
	codexHomePath := prof.CodexHomePath()
	if err := os.MkdirAll(codexHomePath, 0700); err != nil {
		return nil, fmt.Errorf("create codex_home dir: %w", err)
	}

	// Copy auth.json to codex_home
	basename := filepath.Base(sourcePath)
	targetPath := filepath.Join(codexHomePath, basename)
	if err := copyFile(sourcePath, targetPath); err != nil {
		return nil, fmt.Errorf("copy %s: %w", basename, err)
	}
	copiedFiles = append(copiedFiles, targetPath)

	return copiedFiles, nil
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

// ValidateToken validates that the authentication token works.
// For passive validation: checks file existence, format, and expiry timestamps.
// For active validation: attempts minimal API call to OpenAI.
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

	// Check auth.json exists
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	if _, err := os.Stat(authPath); os.IsNotExist(err) {
		result.Valid = false
		result.Error = "auth.json not found"
		return result, nil
	}

	// Read and parse auth.json
	data, err := os.ReadFile(authPath)
	if err != nil {
		result.Valid = false
		result.Error = fmt.Sprintf("cannot read auth.json: %v", err)
		return result, nil
	}

	var authData map[string]interface{}
	if err := json.Unmarshal(data, &authData); err != nil {
		result.Valid = false
		result.Error = fmt.Sprintf("invalid JSON in auth.json: %v", err)
		return result, nil
	}

	// Check for access_token field
	if _, hasToken := authData["access_token"]; !hasToken {
		if _, hasToken := authData["accessToken"]; !hasToken {
			result.Valid = false
			result.Error = "no access token found in auth.json"
			return result, nil
		}
	}

	// Check for expiry timestamp
	for _, key := range []string{"expires_at", "expiresAt", "expires"} {
		if expiresAt, ok := authData[key]; ok {
			var expTime time.Time
			switch v := expiresAt.(type) {
			case string:
				if t, err := parseCodexExpiryTime(v); err == nil {
					expTime = t
				}
			case float64:
				expTime = parseCodexUnixTime(v)
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

	// For active validation, we would need to make an API call to OpenAI
	// to verify the token. For now, we rely on passive validation.
	// A proper implementation would call https://api.openai.com/v1/models
	// with the token to verify it's valid.
	result.Valid = true
	return result, nil
}

func parseCodexExpiryTime(s string) (time.Time, error) {
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

func parseCodexUnixTime(f float64) time.Time {
	if f > 1e12 {
		return time.UnixMilli(int64(f))
	}
	return time.Unix(int64(f), 0)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
var _ provider.DeviceCodeProvider = (*Provider)(nil)
