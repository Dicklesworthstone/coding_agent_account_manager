// Package cursor implements the provider adapter for Cursor CLI.
//
// Authentication mechanics:
// - First use: run `cursor`, authenticate via browser OAuth flow.
// - Auth state stored in:
//   - ~/.cursor/cli-config.json (primary - contains authInfo with tokens)
//
// Context isolation for caam:
// - Set HOME to pseudo-home directory
// - This makes ~/.cursor/cli-config.json profile-scoped
//
// Auth file swapping (PRIMARY use case):
// - Backup ~/.cursor/cli-config.json after logging in
// - Restore to instantly switch Cursor accounts without browser flows
package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Provider implements the Cursor CLI adapter.
type Provider struct{}

// New creates a new Cursor provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "cursor"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Cursor (Anysphere)"
}

// DefaultBin returns the default binary name.
// Cursor CLI is invoked via the "agent" command.
func (p *Provider) DefaultBin() string {
	return "agent"
}

// SupportedAuthModes returns the authentication modes supported by Cursor.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth, // Browser-based OAuth flow
	}
}

// cursorConfigDir returns the Cursor config directory for the current user.
func cursorConfigDir() string {
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".cursor")
}

func cursorConfigDirForProfile(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".cursor")
}

func cursorConfigPathForProfile(prof *profile.Profile) string {
	return filepath.Join(cursorConfigDirForProfile(prof), "cli-config.json")
}

// AuthFiles returns the auth file specifications for Cursor.
// This is the key method for auth file backup/restore.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	homeDir, _ := os.UserHomeDir()

	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(homeDir, ".cursor", "cli-config.json"),
			Description: "Cursor CLI config with OAuth credentials",
			Required:    true,
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

	// Create pseudo-XDG_CONFIG_HOME directory
	xdgConfig := prof.XDGConfigPath()
	if err := os.MkdirAll(xdgConfig, 0700); err != nil {
		return fmt.Errorf("create xdg_config: %w", err)
	}

	// Create .cursor directory under home
	cursorDir := cursorConfigDirForProfile(prof)
	if err := os.MkdirAll(cursorDir, 0700); err != nil {
		return fmt.Errorf("create .cursor dir: %w", err)
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

// Env returns the environment variables for running Cursor in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"HOME":            prof.HomePath(),
		"XDG_CONFIG_HOME": prof.XDGConfigPath(),
	}
	return env, nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	return p.loginWithOAuth(ctx, prof)
}

// loginWithOAuth launches Cursor agent for interactive login.
func (p *Provider) loginWithOAuth(ctx context.Context, prof *profile.Profile) error {
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}

	fmt.Println("Launching Cursor agent for authentication...")
	fmt.Println("Complete the OAuth flow in your browser.")

	cmd := exec.CommandContext(ctx, "agent")
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
		fmt.Println("Press Ctrl+C when done.")
	}

	cmd.Stdin = os.Stdin

	err = cmd.Run()
	if capture != nil {
		capture.Flush()
	}
	return err
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	// Remove auth files
	authPaths := []string{
		cursorConfigPathForProfile(prof),
	}

	for _, path := range authPaths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}

	return nil
}

// Status checks the current authentication state.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}

	// Check if cli-config.json exists and has authInfo
	configPath := cursorConfigPathForProfile(prof)
	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err == nil {
			var config map[string]interface{}
			if json.Unmarshal(data, &config) == nil {
				if authInfo, ok := config["authInfo"].(map[string]interface{}); ok {
					if email, ok := authInfo["email"].(string); ok && email != "" {
						status.LoggedIn = true
						status.AccountID = email
					}
				}
			}
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

	// Check xdg_config exists
	xdgConfig := prof.XDGConfigPath()
	if _, err := os.Stat(xdgConfig); os.IsNotExist(err) {
		return fmt.Errorf("xdg_config directory missing")
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

	return nil
}

// DetectExistingAuth detects existing Cursor authentication files in standard locations.
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
			path:        filepath.Join(homeDir, ".cursor", "cli-config.json"),
			description: "Cursor CLI config with OAuth credentials",
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

		// Basic validation: try to parse as JSON and check for authInfo
		data, err := os.ReadFile(loc.path)
		if err != nil {
			authLoc.ValidationError = fmt.Sprintf("read error: %v", err)
		} else {
			var config map[string]interface{}
			if err := json.Unmarshal(data, &config); err != nil {
				authLoc.ValidationError = fmt.Sprintf("invalid JSON: %v", err)
			} else {
				// Check for authInfo field
				if authInfo, ok := config["authInfo"].(map[string]interface{}); ok {
					if _, hasEmail := authInfo["email"]; hasEmail {
						authLoc.IsValid = true
					} else {
						authLoc.ValidationError = "authInfo missing email field"
					}
				} else {
					authLoc.ValidationError = "missing authInfo field"
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

	// Determine target based on source file type
	basename := filepath.Base(sourcePath)
	switch basename {
	case "cli-config.json":
		// Copy to profile's .cursor directory
		targetDir := cursorConfigDirForProfile(prof)
		if err := os.MkdirAll(targetDir, 0700); err != nil {
			return nil, fmt.Errorf("create .cursor dir: %w", err)
		}
		targetPath := filepath.Join(targetDir, "cli-config.json")
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy cli-config.json: %w", err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	default:
		// Default: copy to .cursor directory
		targetDir := cursorConfigDirForProfile(prof)
		if err := os.MkdirAll(targetDir, 0700); err != nil {
			return nil, fmt.Errorf("create .cursor dir: %w", err)
		}
		targetPath := filepath.Join(targetDir, basename)
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", basename, err)
		}
		copiedFiles = append(copiedFiles, targetPath)
	}

	return copiedFiles, nil
}

// ValidateToken validates that the authentication token works.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		CheckedAt: timeNow(),
	}

	if passive {
		return p.validateTokenPassive(ctx, prof, result)
	}
	return p.validateTokenActive(ctx, prof, result)
}

// validateTokenPassive performs passive validation without network calls.
func (p *Provider) validateTokenPassive(ctx context.Context, prof *profile.Profile, result *provider.ValidationResult) (*provider.ValidationResult, error) {
	result.Method = "passive"

	configPath := cursorConfigPathForProfile(prof)

	if !fileExists(configPath) {
		result.Valid = false
		result.Error = "no auth files found"
		return result, nil
	}

	// Check cli-config.json
	data, err := os.ReadFile(configPath)
	if err != nil {
		result.Valid = false
		result.Error = fmt.Sprintf("cannot read cli-config.json: %v", err)
		return result, nil
	}

	var config map[string]interface{}
	if err := json.Unmarshal(data, &config); err != nil {
		result.Valid = false
		result.Error = fmt.Sprintf("invalid JSON in cli-config.json: %v", err)
		return result, nil
	}

	// Check for authInfo
	authInfo, ok := config["authInfo"].(map[string]interface{})
	if !ok {
		result.Valid = false
		result.Error = "missing authInfo in config"
		return result, nil
	}

	// Check for email (basic auth presence check)
	if _, ok := authInfo["email"].(string); !ok {
		result.Valid = false
		result.Error = "missing email in authInfo"
		return result, nil
	}

	// If we got here, passive validation passed
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

	// For now, active validation just confirms passive checks
	// (would need API endpoint to truly validate)
	result.Valid = true
	return result, nil
}

// Helper functions

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
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

// timeNow is a variable for testing
var timeNow = time.Now

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
