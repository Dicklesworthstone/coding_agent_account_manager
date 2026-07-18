// Package grok implements the provider adapter for xAI's official Grok CLI
// ("Grok Build").
//
// Authentication mechanics (confirmed from the official installer at
// https://x.ai/cli/install.sh and the CLI's bundled documentation):
//   - `grok login` writes the account credential to $GROK_HOME/auth.json
//     (default ~/.grok/auth.json).
//   - config.toml in the same directory carries CLI configuration.
//   - GROK_HOME overrides the config directory (default ~/.grok), which makes
//     per-profile isolation possible.
//   - Binary name: grok (installed to ~/.grok/bin/grok).
//
// Auth file swapping (PRIMARY use case):
//   - Backup auth.json (+ config.toml) after logging in with each account
//   - Restore to instantly switch accounts without browser login flows
//
// Note: the enterprise GROK_DEPLOYMENT_KEY environment variable takes
// precedence over auth.json in the official installer's auth flow; when it is
// set, a swapped auth.json is ignored by the CLI.
//
// Disambiguation: the unaffiliated community CLI superagent-ai/grok-cli also
// uses ~/.grok/ (grok.db, user-settings.json); this adapter touches only the
// official Grok Build files (auth.json, config.toml).
package grok

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Provider implements the Grok Build CLI adapter.
type Provider struct{}

// New creates a new Grok provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "grok"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Grok Build (xAI)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "grok"
}

// SupportedAuthModes returns the authentication modes supported by Grok Build.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,
	}
}

// grokHome returns the Grok config directory ($GROK_HOME, default ~/.grok).
func grokHome() string {
	if home := os.Getenv("GROK_HOME"); home != "" {
		return home
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".grok")
}

// profileGrokHome returns the Grok config directory inside a profile's
// isolated HOME.
func profileGrokHome(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".grok")
}

// AuthFiles returns the auth file specifications for Grok Build.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	home := grokHome()
	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(home, "auth.json"),
			Description: "Grok Build CLI login credential (written by 'grok login')",
			Required:    true,
		},
		{
			Path:        filepath.Join(home, "config.toml"),
			Description: "Grok Build CLI configuration",
			Required:    false,
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
	if err := os.MkdirAll(profileGrokHome(prof), 0700); err != nil {
		return fmt.Errorf("create grok home: %w", err)
	}
	return nil
}

// Env returns the environment variables for running Grok Build in this
// profile's context. GROK_HOME is pinned explicitly (documented CLI override
// for the config directory) in addition to HOME.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"HOME":      prof.HomePath(),
		"GROK_HOME": profileGrokHome(prof),
	}
	return env, nil
}

// Login initiates the authentication flow (`grok login`, browser OIDC).
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	cmd := exec.CommandContext(ctx, "grok", "login")
	cmd.Env = append(os.Environ(),
		"HOME="+prof.HomePath(),
		"GROK_HOME="+profileGrokHome(prof),
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting Grok Build login flow...")
	fmt.Println("Complete the login in the browser window that opens.")

	return cmd.Run()
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	authPath := filepath.Join(profileGrokHome(prof), "auth.json")
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

	authPath := filepath.Join(profileGrokHome(prof), "auth.json")
	if _, err := os.Stat(authPath); err == nil {
		status.LoggedIn = true
		// Best-effort identity extraction for display.
		if id, err := identity.ExtractFromGrokAuth(authPath); err == nil && id != nil {
			status.AccountID = id.Email
		}
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

// DetectExistingAuth detects existing Grok Build authentication files.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	authPath := filepath.Join(grokHome(), "auth.json")
	authLoc := provider.AuthLocation{
		Path:        authPath,
		Description: "Grok Build CLI login credential",
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

	// Grok auth goes into the profile's grok home
	grokDir := profileGrokHome(prof)
	if err := os.MkdirAll(grokDir, 0700); err != nil {
		return nil, fmt.Errorf("create grok home dir: %w", err)
	}

	basename := filepath.Base(sourcePath)
	targetPath := filepath.Join(grokDir, basename)
	if err := copyFile(sourcePath, targetPath); err != nil {
		return nil, fmt.Errorf("copy %s: %w", basename, err)
	}

	return []string{targetPath}, nil
}

// ValidateToken validates that the authentication token works.
// Passive validation checks file presence and JSON shape only; the internal
// schema of auth.json is not part of any public contract, so no field-level
// checks are made.
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		Method:    "passive",
		CheckedAt: time.Now(),
	}

	authPath := filepath.Join(profileGrokHome(prof), "auth.json")
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
