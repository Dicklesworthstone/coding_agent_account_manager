// Package agy implements the provider adapter for the Antigravity CLI (`agy`),
// Google's successor to the retired Gemini CLI (`gmi`).
//
// Authentication mechanics (verified against a live, authenticated agy install):
//   - agy is authenticated entirely by an on-disk OAuth token file:
//     ~/.gemini/antigravity-cli/antigravity-oauth-token (mode 0600, ~500 bytes,
//     JSON of the form {"auth_method": ..., "token": ...}).
//     Copying ONLY this file to another machine authenticates agy there; the
//     token is NOT device-bound. This file is therefore the authoritative,
//     required auth artifact.
//   - The active Google account identity is recorded in
//     ~/.gemini/google_accounts.json: {"active": "<email>", "old": [<emails>...]}.
//     This file is shared with the legacy Gemini CLI (gmi) and identifies which
//     Google account agy is operating as.
//   - ~/.gemini/oauth_creds.json is the shared Google OAuth credentials cache
//     (also used by gmi).
//   - ~/.gemini/antigravity-cli/settings.json holds the default model
//     (e.g. "Gemini 3.1 Pro (High)") and telemetry preference.
//
// Keyring finding (Linux): agy does NOT use the OS keyring (libsecret) on Linux.
// `secret-tool` is not required and no antigravity/agy keyring entry exists; the
// antigravity-oauth-token file is the sole, authoritative credential store. On
// platforms that use an OS keychain, agy still writes this token file, so caam
// treats the file as authoritative across platforms.
//
// Auth file swapping (PRIMARY use case for caam):
//   - Backup the antigravity-oauth-token + google_accounts.json + oauth_creds.json
//     after authenticating once.
//   - Restore to instantly switch the active Antigravity (Google) account without
//     re-running the browser OAuth flow.
//
// Relationship to the gemini provider: the legacy `gemini`/`gmi` provider is kept
// intact as a separate adapter. `agy` is additive and operates on the
// Antigravity-specific token file plus the shared Google account files.
package agy

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

// Provider implements the Antigravity CLI (agy) adapter.
type Provider struct{}

// New creates a new Antigravity (agy) provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "agy"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Antigravity CLI (Google)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "agy"
}

// SupportedAuthModes returns the authentication modes supported by agy.
// agy authenticates via Google OAuth (the antigravity-oauth-token file).
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,
	}
}

// geminiHome returns the base ~/.gemini directory that agy shares with the
// legacy Gemini CLI. Honors GEMINI_HOME for parity with the gemini provider.
func geminiHome() string {
	if home := os.Getenv("GEMINI_HOME"); home != "" {
		return home
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".gemini")
}

// antigravityDir returns the antigravity-cli subdirectory under ~/.gemini.
func antigravityDir() string {
	return filepath.Join(geminiHome(), "antigravity-cli")
}

// TokenPath returns the absolute path to the authoritative agy OAuth token file.
func TokenPath() string {
	return filepath.Join(antigravityDir(), "antigravity-oauth-token")
}

// AccountsPath returns the absolute path to the shared Google accounts file.
func AccountsPath() string {
	return filepath.Join(geminiHome(), "google_accounts.json")
}

// OAuthCredsPath returns the absolute path to the shared Google OAuth creds cache.
func OAuthCredsPath() string {
	return filepath.Join(geminiHome(), "oauth_creds.json")
}

// SettingsPath returns the absolute path to the antigravity-cli settings file
// (default model / telemetry).
func SettingsPath() string {
	return filepath.Join(antigravityDir(), "settings.json")
}

// AuthFiles returns the auth file specifications for the Antigravity CLI.
// This is the key method for auth file backup/restore.
//
// The antigravity-oauth-token is the only REQUIRED file: it alone authenticates
// agy. google_accounts.json / oauth_creds.json / settings.json are backed up so
// that a restored snapshot reproduces the full account context (active email,
// shared Google creds cache, default model), but their absence does not break a
// restore.
//
// NOTE: every basename here is unique (antigravity-oauth-token,
// google_accounts.json, oauth_creds.json, settings.json) so they never collide
// in the vault, even though they live in two different directories.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	return []provider.AuthFileSpec{
		{
			Path:        TokenPath(),
			Description: "Antigravity CLI OAuth token (authoritative agy credential)",
			Required:    true,
		},
		{
			Path:        AccountsPath(),
			Description: "Active Google account for Antigravity/Gemini (google_accounts.json)",
			Required:    false,
		},
		{
			Path:        OAuthCredsPath(),
			Description: "Shared Google OAuth credentials cache (oauth_creds.json)",
			Required:    false,
		},
		{
			Path:        SettingsPath(),
			Description: "Antigravity CLI settings (default model / telemetry)",
			Required:    false,
		},
	}
}

// PrepareProfile sets up the profile directory structure for an isolated agy run.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	homePath := prof.HomePath()
	if err := os.MkdirAll(homePath, 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}

	// agy stores its token under ~/.gemini/antigravity-cli and shared files
	// under ~/.gemini; pre-create both inside the isolated home.
	if err := os.MkdirAll(filepath.Join(homePath, ".gemini", "antigravity-cli"), 0700); err != nil {
		return fmt.Errorf("create .gemini/antigravity-cli dir: %w", err)
	}

	mgr, err := passthrough.NewManager()
	if err != nil {
		return fmt.Errorf("create passthrough manager: %w", err)
	}
	if err := mgr.SetupPassthroughs(homePath); err != nil {
		return fmt.Errorf("setup passthroughs: %w", err)
	}

	return nil
}

// Env returns the environment variables for running agy in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return map[string]string{
		"HOME": prof.HomePath(),
	}, nil
}

// Login initiates the Antigravity authentication flow (browser OAuth).
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}

	fmt.Println("Launching Antigravity CLI (agy) for Google authentication...")
	fmt.Println("Complete the Google login when prompted.")

	cmd := exec.CommandContext(ctx, p.DefaultBin())
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}

	var capture *browser.OutputCapture
	if prof.HasBrowserConfig() {
		launcher := browser.NewLauncher(&browser.Config{
			Command:    prof.BrowserCommand,
			ProfileDir: prof.BrowserProfileDir,
		})
		fmt.Printf("Using browser profile: %s\n", prof.BrowserDisplayName())

		capture = browser.NewOutputCapture(os.Stdout, os.Stderr)
		capture.OnURL = func(url, source string) {
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

// Logout clears agy authentication credentials from the profile.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	geminiDir := filepath.Join(prof.HomePath(), ".gemini")
	paths := []string{
		filepath.Join(geminiDir, "antigravity-cli", "antigravity-oauth-token"),
		filepath.Join(geminiDir, "google_accounts.json"),
		filepath.Join(geminiDir, "oauth_creds.json"),
	}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", filepath.Base(path), err)
		}
	}
	return nil
}

// Status checks the current authentication state of the profile.
// agy is considered logged in when its token file is present. The active Google
// account email (if available) is reported as AccountID.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}

	geminiDir := filepath.Join(prof.HomePath(), ".gemini")
	tokenPath := filepath.Join(geminiDir, "antigravity-cli", "antigravity-oauth-token")
	accountsPath := filepath.Join(geminiDir, "google_accounts.json")

	if fileExists(tokenPath) {
		status.LoggedIn = true
	}

	if email := activeAccountEmail(accountsPath); email != "" {
		status.AccountID = email
	}

	return status, nil
}

// ValidateProfile checks if a profile is correctly configured.
func (p *Provider) ValidateProfile(ctx context.Context, prof *profile.Profile) error {
	homePath := prof.HomePath()
	if _, err := os.Stat(homePath); os.IsNotExist(err) {
		return fmt.Errorf("home directory missing")
	}

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

// DetectExistingAuth detects existing Antigravity authentication in the standard
// system locations. Detection is read-only and never modifies original files,
// and never reads or exposes the raw token bytes beyond size/validity metadata.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	type spec struct {
		path        string
		description string
		validator   func(data []byte) (bool, string)
	}

	specs := []spec{
		{
			path:        TokenPath(),
			description: "Antigravity CLI OAuth token (authoritative agy credential)",
			validator: func(data []byte) (bool, string) {
				if len(data) == 0 {
					return false, "empty token file"
				}
				// The token file is JSON of the form {"auth_method":..,"token":..}.
				// Validate structure (keys only) without exposing the secret.
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					// Some versions may store a bare token string; accept any
					// non-empty payload as a valid credential.
					return true, ""
				}
				if _, ok := parsed["token"]; ok {
					return true, ""
				}
				// Non-empty JSON without a "token" key is still a credential blob.
				return true, ""
			},
		},
		{
			path:        AccountsPath(),
			description: "Active Google account (google_accounts.json)",
			validator: func(data []byte) (bool, string) {
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					return false, fmt.Sprintf("invalid JSON: %v", err)
				}
				if active, ok := parsed["active"].(string); ok && active != "" {
					return true, ""
				}
				return false, "no active account"
			},
		},
		{
			path:        OAuthCredsPath(),
			description: "Shared Google OAuth credentials cache (oauth_creds.json)",
			validator: func(data []byte) (bool, string) {
				var parsed map[string]interface{}
				if err := json.Unmarshal(data, &parsed); err != nil {
					return false, fmt.Sprintf("invalid JSON: %v", err)
				}
				if _, ok := parsed["access_token"]; ok {
					return true, ""
				}
				if _, ok := parsed["refresh_token"]; ok {
					return true, ""
				}
				return false, "missing expected OAuth fields"
			},
		},
	}

	var mostRecent *provider.AuthLocation
	for _, s := range specs {
		authLoc := provider.AuthLocation{
			Path:        s.path,
			Description: s.description,
		}

		info, err := os.Stat(s.path)
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

		data, err := os.ReadFile(s.path)
		if err != nil {
			authLoc.ValidationError = fmt.Sprintf("read error: %v", err)
		} else {
			valid, verr := s.validator(data)
			authLoc.IsValid = valid
			authLoc.ValidationError = verr
		}

		detection.Locations = append(detection.Locations, authLoc)

		if authLoc.Exists && authLoc.IsValid {
			detection.Found = true
			// Prefer the token file as primary; otherwise most recent valid.
			if filepath.Base(authLoc.Path) == "antigravity-oauth-token" {
				locCopy := authLoc
				mostRecent = &locCopy
			} else if mostRecent == nil || (filepath.Base(mostRecent.Path) != "antigravity-oauth-token" && authLoc.LastModified.After(mostRecent.LastModified)) {
				locCopy := authLoc
				mostRecent = &locCopy
			}
		}
	}

	detection.Primary = mostRecent
	return detection, nil
}

// ImportAuth imports a detected agy auth file into a profile directory.
// Files keep their original relative location under the profile's pseudo-home so
// that a subsequent agy run (with HOME pointed at the profile) finds them.
func (p *Provider) ImportAuth(ctx context.Context, sourcePath string, prof *profile.Profile) ([]string, error) {
	info, err := os.Stat(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("source auth file not found: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("source path is a directory, not a file")
	}

	basename := filepath.Base(sourcePath)
	parentDir := filepath.Base(filepath.Dir(sourcePath))

	var targetPath string
	switch basename {
	case "antigravity-oauth-token":
		targetPath = filepath.Join(prof.HomePath(), ".gemini", "antigravity-cli", basename)
	case "settings.json":
		if parentDir == "antigravity-cli" {
			targetPath = filepath.Join(prof.HomePath(), ".gemini", "antigravity-cli", basename)
		} else {
			targetPath = filepath.Join(prof.HomePath(), ".gemini", basename)
		}
	default:
		// google_accounts.json, oauth_creds.json, etc. live in ~/.gemini.
		targetPath = filepath.Join(prof.HomePath(), ".gemini", basename)
	}

	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		return nil, fmt.Errorf("create target dir: %w", err)
	}
	if err := copyFile(sourcePath, targetPath); err != nil {
		return nil, fmt.Errorf("copy %s: %w", basename, err)
	}

	return []string{targetPath}, nil
}

// ValidateToken validates that the agy authentication token is usable.
// Passive validation checks the token file exists and is non-empty (it never
// reads token bytes beyond confirming presence/size). Active validation falls
// back to passive (no network probe is performed to avoid side effects/cost).
func (p *Provider) ValidateToken(ctx context.Context, prof *profile.Profile, passive bool) (*provider.ValidationResult, error) {
	result := &provider.ValidationResult{
		Provider:  p.ID(),
		Profile:   prof.Name,
		CheckedAt: time.Now(),
	}
	if passive {
		result.Method = "passive"
	} else {
		result.Method = "active"
	}

	tokenPath := filepath.Join(prof.HomePath(), ".gemini", "antigravity-cli", "antigravity-oauth-token")
	info, err := os.Stat(tokenPath)
	if err != nil {
		result.Valid = false
		result.Error = "no Antigravity token found"
		return result, nil
	}
	if info.Size() == 0 {
		result.Valid = false
		result.Error = "Antigravity token file is empty"
		return result, nil
	}

	result.Valid = true
	return result, nil
}

// activeAccountEmail reads google_accounts.json and returns the active account
// email, or "" if it cannot be determined. It never returns token material.
func activeAccountEmail(accountsPath string) string {
	data, err := os.ReadFile(accountsPath)
	if err != nil {
		return ""
	}
	var parsed struct {
		Active string `json:"active"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return parsed.Active
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// copyFile copies a file from src to dst atomically with fsync, enforcing 0600.
func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	tmpFile, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := io.Copy(tmpFile, srcFile); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Chmod(0600); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, dst)
}

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
