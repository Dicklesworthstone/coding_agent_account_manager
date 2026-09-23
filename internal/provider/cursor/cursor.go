// Package cursor implements the provider adapter for Cursor CLI.
//
// Authentication mechanics:
//   - cli-config.json (authInfo metadata) lives in $CURSOR_CONFIG_DIR, else
//     $XDG_CONFIG_HOME/cursor, else ~/.cursor.
//   - File-backed credentials (auth.json) live in $XDG_CONFIG_HOME/cursor on
//     Linux (default ~/.config/cursor), ~/.cursor on macOS (where the login
//     keychain is preferred) and %APPDATA%\Cursor on Windows.
//     See authfile.ResolveCursorPaths.
//   - Binary name: cursor
//
// Auth file swapping (PRIMARY use case):
// - Backup auth files after logging in with each account
// - Restore to instantly switch accounts without browser login flows
package cursor

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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
	return "Cursor"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "cursor"
}

// SupportedAuthModes returns the authentication modes supported by Cursor.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,
		provider.AuthModeAPIKey,
	}
}

// goos is the platform whose Cursor path layout is used. Tests override it.
var goos = runtime.GOOS

// userPaths returns the Cursor paths for the current user's real home.
func userPaths() authfile.CursorPaths {
	homeDir, _ := os.UserHomeDir()
	return authfile.ResolveCursorPaths(homeDir, goos, os.Getenv)
}

// profileEnv pins every variable cursor-agent uses to locate its config and
// credentials, so an XDG_CONFIG_HOME or CURSOR_CONFIG_DIR inherited from the
// caller cannot point a profile at the machine-global login. The pinned
// values equal cursor-agent's defaults for that HOME, so existing profile
// layouts keep working.
func profileEnv(prof *profile.Profile) map[string]string {
	home := prof.HomePath()
	env := map[string]string{
		"HOME":              home,
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"CURSOR_CONFIG_DIR": filepath.Join(home, ".cursor"),
	}
	if goos == "windows" {
		// Windows credentials live under %APPDATA%, which HOME does not move.
		env["APPDATA"] = filepath.Join(home, "AppData", "Roaming")
	}
	return env
}

// profilePaths returns the Cursor paths cursor-agent uses inside a profile.
func profilePaths(prof *profile.Profile) authfile.CursorPaths {
	env := profileEnv(prof)
	lookup := func(k string) string {
		return env[k]
	}
	return authfile.ResolveCursorPaths(prof.HomePath(), goos, lookup)
}

// legacyAuthPath is where older caam versions and older cursor-agent
// releases kept auth.json inside a profile.
func legacyAuthPath(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".cursor", "auth.json")
}

// AuthFiles returns the auth file specifications for Cursor, resolved the
// way cursor-agent resolves them (see authfile.ResolveCursorPaths).
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	set := authfile.CursorAuthFiles()
	specs := make([]provider.AuthFileSpec, 0, len(set.Files))
	for _, f := range set.Files {
		specs = append(specs, provider.AuthFileSpec{
			Path:        f.Path,
			Description: f.Description,
			Required:    f.Required,
		})
	}
	return specs
}

// hasAuthInfo reports whether the file at path is a Cursor cli-config.json that
// contains a non-empty "authInfo" object. The cli-config.json file also holds
// non-secret permission/config data, so merely being valid JSON is not enough
// to consider the account "logged in".
func hasAuthInfo(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(data, &parsed); err != nil {
		return false
	}
	raw, ok := parsed["authInfo"]
	if !ok {
		return false
	}
	var authInfo map[string]json.RawMessage
	if err := json.Unmarshal(raw, &authInfo); err != nil {
		return false
	}
	return len(authInfo) > 0
}

// PrepareProfile sets up the profile directory structure.
func (p *Provider) PrepareProfile(ctx context.Context, prof *profile.Profile) error {
	homePath := prof.HomePath()
	if err := os.MkdirAll(homePath, 0700); err != nil {
		return fmt.Errorf("create home: %w", err)
	}
	return nil
}

// Env returns the environment variables for running Cursor in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	return profileEnv(prof), nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	cmd := exec.CommandContext(ctx, "cursor")
	cmd.Env = os.Environ()
	for k, v := range profileEnv(prof) {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	fmt.Println("Starting Cursor login flow...")
	fmt.Println("Complete the login in the interactive session.")

	return cmd.Run()
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	for _, authPath := range uniquePaths(profilePaths(prof).AuthFile, legacyAuthPath(prof)) {
		if err := os.Remove(authPath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", authPath, err)
		}
	}
	return nil
}

// uniquePaths returns the non-empty paths in order without duplicates.
func uniquePaths(paths ...string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// Status checks the current authentication state.
func (p *Provider) Status(ctx context.Context, prof *profile.Profile) (*provider.ProfileStatus, error) {
	status := &provider.ProfileStatus{
		HasLockFile: prof.IsLocked(),
	}

	paths := profilePaths(prof)

	// Primary: cli-config.json with a non-empty authInfo object.
	if hasAuthInfo(filepath.Join(paths.ConfigDir, "cli-config.json")) {
		status.LoggedIn = true
		return status, nil
	}

	// File-backed credentials: the platform location, then the legacy one.
	for _, authPath := range uniquePaths(paths.AuthFile, legacyAuthPath(prof)) {
		if _, err := os.Stat(authPath); err == nil {
			status.LoggedIn = true
			break
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

// DetectExistingAuth detects existing Cursor authentication files.
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	paths := userPaths()

	// Primary: cli-config.json. "Logged in" requires a non-empty authInfo
	// object (the file also holds non-secret permission/config data).
	cliConfigPath := filepath.Join(paths.ConfigDir, "cli-config.json")
	cliLoc := provider.AuthLocation{
		Path:        cliConfigPath,
		Description: "Cursor CLI auth (authInfo)",
	}
	if info, err := os.Stat(cliConfigPath); err != nil {
		if !os.IsNotExist(err) {
			cliLoc.ValidationError = fmt.Sprintf("stat error: %v", err)
		}
	} else {
		cliLoc.Exists = true
		cliLoc.LastModified = info.ModTime()
		cliLoc.FileSize = info.Size()
		if hasAuthInfo(cliConfigPath) {
			cliLoc.IsValid = true
		} else {
			cliLoc.ValidationError = "no non-empty authInfo object"
		}
	}
	detection.Locations = append(detection.Locations, cliLoc)
	if cliLoc.Exists && cliLoc.IsValid && detection.Primary == nil {
		detection.Found = true
		locCopy := cliLoc
		detection.Primary = &locCopy
	}

	// File-backed credentials: auth.json (presence + valid JSON).
	authPath := paths.AuthFile
	authLoc := provider.AuthLocation{
		Path:        authPath,
		Description: "Cursor CLI auth credentials",
	}
	if info, err := os.Stat(authPath); err != nil {
		if !os.IsNotExist(err) {
			authLoc.ValidationError = fmt.Sprintf("stat error: %v", err)
		}
	} else {
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
	}
	detection.Locations = append(detection.Locations, authLoc)
	if authLoc.Exists && authLoc.IsValid && detection.Primary == nil {
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

	// Place each file where cursor-agent reads it inside the profile.
	paths := profilePaths(prof)
	basename := filepath.Base(sourcePath)
	var targetPath string
	switch basename {
	case "auth.json":
		targetPath = paths.AuthFile
	case "cli-config.json":
		targetPath = filepath.Join(paths.ConfigDir, basename)
	default:
		targetPath = filepath.Join(prof.HomePath(), ".cursor", basename)
	}
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

	paths := profilePaths(prof)

	// Primary: cli-config.json must contain a non-empty authInfo object.
	if hasAuthInfo(filepath.Join(paths.ConfigDir, "cli-config.json")) {
		result.Valid = true
		return result, nil
	}

	// File-backed credentials: the platform location, else the legacy one.
	authPath := paths.AuthFile
	if _, err := os.Stat(authPath); os.IsNotExist(err) {
		authPath = legacyAuthPath(prof)
	}
	if _, err := os.Stat(authPath); os.IsNotExist(err) {
		result.Valid = false
		result.Error = "no Cursor auth found (cli-config.json authInfo empty/missing and auth.json not found)"
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
