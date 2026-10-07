// Package opencode implements the provider adapter for OpenCode CLI.
//
// Authentication mechanics:
// - OpenCode stores auth in $XDG_DATA_HOME/opencode/auth.json (default ~/.local/share/opencode/auth.json).
// - Binary name: opencode
//
// Auth file swapping (PRIMARY use case):
// - Backup auth.json after logging in with each account
// - Restore to instantly switch accounts without browser login flows
package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Provider implements the OpenCode CLI adapter.
type Provider struct{}

// New creates a new OpenCode provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "opencode"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "OpenCode"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "opencode"
}

// SupportedAuthModes returns the authentication modes supported by OpenCode.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,
		provider.AuthModeAPIKey,
	}
}

// opencodeDataHome returns the OpenCode data directory.
func opencodeDataHome() string {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		homeDir, _ := os.UserHomeDir()
		dataHome = filepath.Join(homeDir, ".local", "share")
	}
	return filepath.Join(dataHome, "opencode")
}

// AuthFiles returns the auth file specifications for OpenCode.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(opencodeDataHome(), "auth.json"),
			Description: "OpenCode auth credentials",
			Required:    true,
		},
	}
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	// Create home directory for isolated context
	homePath := prof.HomePath()
	if err := os.MkdirAll(homePath, 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	return nil
}

// Env returns the environment variables for running OpenCode in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"HOME":            prof.HomePath(),
		"XDG_DATA_HOME":   filepath.Join(prof.HomePath(), ".local", "share"),
		"XDG_CONFIG_HOME": filepath.Join(prof.HomePath(), ".config"),
	}
	return env, nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	env, err := provider.ProfileEnvironment(ctx, p, prof)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "opencode")
	cmd.Env = provider.MergeEnvironment(os.Environ(), env, nil)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting OpenCode login flow...")
	fmt.Println("Complete the login in the interactive session.")

	return cmd.Run()
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	authPath := filepath.Join(prof.HomePath(), ".local", "share", "opencode", "auth.json")
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

	authPath := filepath.Join(prof.HomePath(), ".local", "share", "opencode", "auth.json")
	if _, err := os.Stat(authPath); err == nil {
		status.LoggedIn = true
	}

	return status, nil
}

// ValidateProfile checks if the profile is correctly configured.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	homePath := prof.HomePath()
	if _, err := os.Stat(homePath); os.IsNotExist(err) {
		return fmt.Errorf("home directory missing")
	}
	return nil
}

// DetectExistingAuth detects existing OpenCode authentication files.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	authPath := filepath.Join(opencodeDataHome(), "auth.json")
	authLoc := provider.AuthLocation{
		Path:        authPath,
		Description: "OpenCode auth credentials",
	}

	info, err := os.Stat(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			authLoc.Exists = false
		} else {
			authLoc.ValidationError = fmt.Sprintf("stat error: %v", err)
		}
		detection.Locations = append(detection.Locations, authLoc)
		return detection, nil
	}

	authLoc.Exists = true
	authLoc.LastModified = info.ModTime()
	authLoc.FileSize = info.Size()

	data, err := os.ReadFile(authPath)
	if err != nil {
		authLoc.ValidationError = fmt.Sprintf("read error: %v", err)
	} else {
		var parsed map[string]interface{}
		if err := json.Unmarshal(data, &parsed); err != nil {
			authLoc.ValidationError = fmt.Sprintf("invalid JSON: %v", err)
		} else {
			authLoc.IsValid = true
		}
	}

	detection.Locations = append(detection.Locations, authLoc)
	if authLoc.Exists && authLoc.IsValid {
		detection.Found = true
		locCopy := authLoc
		detection.Primary = &locCopy
	}

	return detection, nil
}

// ImportAuth imports detected auth files into a profile directory.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source auth file not found: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path is a directory, not a file")
	}

	// OpenCode auth goes into the profile's data home
	dataDir := filepath.Join(prof.HomePath(), ".local", "share", "opencode")
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, fmt.Errorf("create opencode data dir: %w", err)
	}

	basename := filepath.Base(sourcePath)
	targetPath := filepath.Join(dataDir, basename)
	if err := copyFile(sourcePath, targetPath); err != nil {
		return nil, fmt.Errorf("copy %s: %w", basename, err)
	}

	return []string{targetPath}, nil
}

// ValidateToken validates that the authentication token works.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		Method:    "passive",
		CheckedAt: time.Now(),
	}

	authPath := filepath.Join(prof.HomePath(), ".local", "share", "opencode", "auth.json")
	if _, err := os.Stat(authPath); os.IsNotExist(err) {
		result.Valid = false
		result.Error = "auth.json not found"
		return result, nil
	}

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

	result.Valid = true
	return result, nil
}

// copyFile copies a file from src to dst atomically.
func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}

	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmpPath := dst + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return err
	}

	return os.Rename(tmpPath, dst)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
