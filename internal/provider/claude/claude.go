// Package claude implements the provider adapter for Anthropic Claude Code CLI.
//
// Authentication mechanics (from research):
// - First use: run `claude`, then authenticate via `/login` inside the interactive session.
// - `/login` is also the documented way to switch accounts.
// - Auth state stored in:
//   - ~/.claude/.credentials.json (primary OAuth credentials)
//   - ~/.claude.json (legacy OAuth session state)
//   - ~/.config/claude-code/auth.json (auth credentials; or $CLAUDE_CONFIG_DIR/auth.json)
//   - ~/.claude/settings.json (user settings)
//   - Project .claude/* files
//
// Context isolation for caam:
// - Set HOME to pseudo-home directory
// - Set XDG_CONFIG_HOME to pseudo-xdg_config directory
// - Set CLAUDE_CONFIG_DIR to profile-scoped claude-code config dir
// - This makes these become profile-scoped:
//   - ${XDG_CONFIG_HOME}/claude-code/auth.json
//   - ${CLAUDE_CONFIG_DIR}/auth.json
//   - ${CLAUDE_CONFIG_DIR}/.credentials.json (XDG-aware builds; issue #70)
//   - ${HOME}/.claude.json
//   - ${HOME}/.claude/settings.json
//
// Auth file swapping (PRIMARY use case):
// - Backup ~/.claude.json and ~/.config/claude-code/auth.json (or $CLAUDE_CONFIG_DIR/auth.json) after logging in
// - Restore to instantly switch Claude Max accounts without /login flows
//
// API key mode (secondary):
// - Supports apiKeyHelper hook in settings.json that returns auth value
// - Claude Code sends this as X-Api-Key and Authorization: Bearer headers
package claude

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
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// Provider implements the Claude Code CLI adapter.
type Provider struct{}

// New creates a new Claude provider.
func New() *Provider {
	return &Provider{}
}

// ID returns the provider identifier.
func (p *Provider) ID() string {
	return "claude"
}

// DisplayName returns the human-friendly name.
func (p *Provider) DisplayName() string {
	return "Claude Code (Anthropic Claude Max)"
}

// DefaultBin returns the default binary name.
func (p *Provider) DefaultBin() string {
	return "claude"
}

// SupportedAuthModes returns the authentication modes supported by Claude.
func (p *Provider) SupportedAuthModes() []provider.AuthMode {
	return []provider.AuthMode{
		provider.AuthModeOAuth,  // Browser-based login via /login (Claude Max subscription)
		provider.AuthModeAPIKey, // API key via apiKeyHelper
	}
}

// xdgConfigHome returns the XDG config directory.
func xdgConfigHome() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return xdg
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config")
}

// claudeConfigDir returns the Claude config directory for the current user.
// If CLAUDE_CONFIG_DIR is set, it takes precedence; otherwise fallback to
// XDG_CONFIG_HOME/claude-code.
func claudeConfigDir() string {
	if cfg := os.Getenv("CLAUDE_CONFIG_DIR"); cfg != "" {
		return cfg
	}
	return filepath.Join(xdgConfigHome(), "claude-code")
}

func claudeConfigDirForProfile(prof *profile.Profile) string {
	return filepath.Join(prof.XDGConfigPath(), "claude-code")
}

func claudeAuthPathForProfile(prof *profile.Profile) string {
	return filepath.Join(claudeConfigDirForProfile(prof), "auth.json")
}

// claudeXDGCredentialsPathForProfile returns the XDG-side credentials file for
// a profile. XDG-aware Claude Code builds honor the XDG_CONFIG_HOME /
// CLAUDE_CONFIG_DIR the profile env sets and write .credentials.json under
// xdg_config/claude-code/ instead of the legacy home/.claude/ — every probe of
// the legacy path must also probe this one, or a working, authenticated
// profile reads as logged out (issue #70).
func claudeXDGCredentialsPathForProfile(prof *profile.Profile) string {
	return filepath.Join(claudeConfigDirForProfile(prof), ".credentials.json")
}

// claudeLegacyDirForProfile returns the legacy home/.claude directory, which
// is where Claude Code builds that ignore CLAUDE_CONFIG_DIR keep everything.
func claudeLegacyDirForProfile(prof *profile.Profile) string {
	return filepath.Join(prof.HomePath(), ".claude")
}

// claudeConfigDirsForProfile returns every directory a Claude Code build may
// treat as its user config dir inside the profile: the legacy home/.claude
// and the CLAUDE_CONFIG_DIR the profile env sets (xdg_config/claude-code).
// Modern builds resolve settings, skills, plugins, commands and agents from
// CLAUDE_CONFIG_DIR only, so anything caam lays down for the CLI to read has
// to land in both (issues #70, #90).
func claudeConfigDirsForProfile(prof *profile.Profile) []string {
	return []string{
		claudeLegacyDirForProfile(prof),
		claudeConfigDirForProfile(prof),
	}
}

// claudeSettingsPathsForProfile returns the profile-scoped settings.json
// locations, one per config dir candidate.
func claudeSettingsPathsForProfile(prof *profile.Profile) []string {
	dirs := claudeConfigDirsForProfile(prof)
	paths := make([]string, 0, len(dirs))
	for _, dir := range dirs {
		paths = append(paths, filepath.Join(dir, "settings.json"))
	}
	return paths
}

// Existing XDG authentication is authoritative because that is the directory
// modern isolated profiles launch with. Legacy-only profiles keep their native
// store; a newer or healthier ignored token must never decide which account runs.
func effectiveClaudeConfigDir(prof *profile.Profile) (string, error) {
	xdg, legacy := claudeConfigDirForProfile(prof), claudeLegacyDirForProfile(prof)
	xdgAuth, err := claudeDirHasAuth(xdg, filepath.Join(xdg, ".claude.json"))
	if err != nil {
		return "", err
	}
	legacyAuth, err := claudeDirHasAuth(legacy, legacyClaudeStatePath(prof))
	if err != nil {
		return "", err
	}
	if xdgAuth && legacyAuth {
		xdgID, legacyID := claudeDirAccountID(xdg), claudeDirAccountID(legacy)
		if xdgID != "" && legacyID != "" && xdgID != legacyID {
			return "", fmt.Errorf("Claude profile contains conflicting accounts in legacy and XDG config directories")
		}
	}
	if xdgAuth || !legacyAuth {
		return xdg, nil
	}
	return legacy, nil
}

func legacyClaudeStatePath(prof *profile.Profile) string {
	path := filepath.Join(claudeLegacyDirForProfile(prof), ".claude.json")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		return path
	}
	return filepath.Join(prof.HomePath(), ".claude.json")
}

func claudeDirHasAuth(dir, statePath string) (bool, error) {
	for _, filename := range []string{".credentials.json", "auth.json"} {
		if _, err := os.Stat(filepath.Join(dir, filename)); err == nil {
			// Even malformed or expired credentials select this store. Their
			// error must not be hidden by another directory's valid login.
			return true, nil
		} else if !os.IsNotExist(err) {
			return false, fmt.Errorf("inspect Claude auth: %w", err)
		}
	}
	for _, path := range []string{filepath.Join(dir, "settings.json"), statePath} {
		hasAuth, err := claudeDocumentHasAuth(path)
		if err != nil || hasAuth {
			return true, nil // A malformed selected document fails validation.
		}
	}
	return false, nil
}

func claudeDirAccountID(dir string) string {
	if data, err := os.ReadFile(filepath.Join(dir, ".credentials.json")); err == nil {
		if creds, err := parseClaudeCredentials(data); err == nil && creds.ClaudeAiOauth != nil && strings.TrimSpace(creds.ClaudeAiOauth.AccountID) != "" {
			return strings.TrimSpace(creds.ClaudeAiOauth.AccountID)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude.json"))
	if err != nil && filepath.Base(dir) == ".claude" {
		data, err = os.ReadFile(filepath.Join(filepath.Dir(dir), ".claude.json"))
	}
	if err != nil {
		return ""
	}
	var state struct {
		Account struct {
			ID string `json:"accountUuid"`
		} `json:"oauthAccount"`
	}
	if json.Unmarshal(data, &state) != nil {
		return ""
	}
	return strings.TrimSpace(state.Account.ID)
}

// AuthFiles returns the auth file specifications for Claude Code.
// This is the key method for auth file backup/restore.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	homeDir, _ := os.UserHomeDir()
	settingsPath, statePath := claudesettings.SharedPaths(homeDir)
	credentialsPath := filepath.Join(filepath.Dir(settingsPath), ".credentials.json")

	return []provider.AuthFileSpec{
		{
			Path:        credentialsPath,
			Description: "Claude Code OAuth credentials (Claude Max subscription)",
			Required:    true,
		},
		{
			Path:        statePath,
			Description: "Claude Code OAuth session state (legacy location)",
			Required:    false,
		},
		{
			Path:        filepath.Join(claudeConfigDir(), "auth.json"),
			Description: "Claude Code auth credentials (CLAUDE_CONFIG_DIR or XDG_CONFIG_HOME)",
			Required:    false, // May not exist in all setups
		},
		{
			Path:        settingsPath,
			Description: "Claude Code settings (apiKeyHelper / API key mode)",
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

	// Create pseudo-XDG_CONFIG_HOME directory
	xdgConfig := prof.XDGConfigPath()
	if err := os.MkdirAll(xdgConfig, 0700); err != nil {
		return fmt.Errorf("create xdg_config: %w", err)
	}

	// Create claude-code directory under xdg_config
	claudeCodeDir := claudeConfigDirForProfile(prof)
	if err := os.MkdirAll(claudeCodeDir, 0700); err != nil {
		return fmt.Errorf("create claude-code dir: %w", err)
	}

	// Create .claude directory under home
	claudeDir := filepath.Join(homePath, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		return fmt.Errorf("create .claude dir: %w", err)
	}

	// Set up passthrough symlinks
	mgr, err := passthrough.NewManager()
	if err != nil {
		return fmt.Errorf("create passthrough manager: %w", err)
	}

	if err := mgr.SetupPassthroughs(homePath); err != nil {
		return fmt.Errorf("setup passthroughs: %w", err)
	}

	// Pass through XDG config/data/state entries so XDG-based CLIs (gh,
	// vercel, atuin, ...) keep their credentials inside the profile. Provider
	// auth dirs are deny-listed and stay isolated (issue #69).
	if err := mgr.SetupXDGPassthroughs(homePath, prof.XDGConfigPath()); err != nil {
		return fmt.Errorf("setup xdg passthroughs: %w", err)
	}

	// Share non-account Claude assets (skills, plugins, slash commands, agent
	// definitions) from the real ~/.claude into the profile. Isolation is
	// per-account credentials, not the user's tooling: without these links a
	// session inside the profile silently loses every user-level skill and
	// plugin (issue #69 follow-up finding).
	if err := shareClaudeUserAssetsForProfile(mgr.RealHome(), prof); err != nil {
		return err
	}
	if err := p.PrepareRun(ctx, prof); err != nil {
		return err
	}

	// If using API key mode, set up the apiKeyHelper configuration
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey {
		if err := p.setupAPIKeyHelper(prof); err != nil {
			return fmt.Errorf("setup apiKeyHelper: %w", err)
		}
	}

	return nil
}

// sharedClaudeAssetEntries are entries of ~/.claude that carry no account
// state and should be shared into a profile's isolated .claude directory.
// Credentials (.credentials.json/.credentials.lock), settings.json
// (apiKeyHelper may be per-profile), and session state stay isolated.
var sharedClaudeAssetEntries = []string{"skills", "plugins", "commands", "agents"}

// shareClaudeUserAssetsForProfile links the shared assets into every config
// dir a Claude Code build may read inside the profile. The links used to go
// only into the legacy home/.claude, which XDG-aware builds never consult
// because the profile env points CLAUDE_CONFIG_DIR at xdg_config/claude-code;
// every user skill and plugin was invisible there (issue #90).
func shareClaudeUserAssetsForProfile(realHome string, prof *profile.Profile) error {
	for _, dir := range claudeConfigDirsForProfile(prof) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
		if err := shareClaudeUserAssets(realHome, dir); err != nil {
			return fmt.Errorf("share claude user assets into %s: %w", dir, err)
		}
	}
	return nil
}

// RefreshProfile re-syncs the shared, non-account assets of an existing
// profile with the real home. The runner calls it before every isolated run
// (provider.ProfileRefresher) so profiles created before the XDG config dir
// received the links heal in place. It never touches credentials or
// settings.
func (p *Provider) RefreshProfile(ctx context.Context, prof *profile.Profile) error {
	mgr, err := passthrough.NewManager()
	if err != nil {
		return fmt.Errorf("create passthrough manager: %w", err)
	}
	if prof.HomePath() == mgr.RealHome() {
		return nil // Global (non-isolated) profile: nothing to mirror.
	}
	return shareClaudeUserAssetsForProfile(mgr.RealHome(), prof)
}

// shareClaudeUserAssets symlinks non-account Claude assets from the real
// ~/.claude into one profile config directory. An entry that already exists
// in the profile as a real file/dir is left untouched; existing symlinks are
// refreshed.
func shareClaudeUserAssets(realHome, profileClaudeDir string) error {
	realClaude := filepath.Join(realHome, ".claude")
	for _, name := range sharedClaudeAssetEntries {
		realPath := filepath.Join(realClaude, name)
		if _, err := os.Stat(realPath); err != nil {
			continue // Not present in the real home.
		}

		linkPath := filepath.Join(profileClaudeDir, name)
		if info, err := os.Lstat(linkPath); err == nil {
			if info.Mode()&os.ModeSymlink == 0 {
				continue // Profile owns a real copy; leave it alone.
			}
			if current, err := os.Readlink(linkPath); err == nil && current == realPath {
				continue
			}
			if err := os.Remove(linkPath); err != nil {
				return fmt.Errorf("refresh %s symlink: %w", name, err)
			}
		}

		if err := os.Symlink(realPath, linkPath); err != nil {
			return fmt.Errorf("link %s: %w", name, err)
		}
	}
	return nil
}

// setupAPIKeyHelper creates the settings.json with apiKeyHelper configuration.
func (p *Provider) setupAPIKeyHelper(prof *profile.Profile) error {
	// Create a helper script path
	helperPath := filepath.Join(prof.BasePath, "api_key_helper.sh")
	configDir, err := effectiveClaudeConfigDir(prof)
	if err != nil {
		return err
	}
	update, err := claudesettings.PrepareAPIKeyHelper(filepath.Join(configDir, "settings.json"), helperPath)
	if err != nil {
		return fmt.Errorf("prepare API key settings: %w", err)
	}

	// Write the helper script
	helperScript := `#!/bin/bash
# caam apiKeyHelper for Claude Code
# This script retrieves the API key from the keychain or environment

# Try environment variable first
if [ -n "$ANTHROPIC_API_KEY" ]; then
    echo "$ANTHROPIC_API_KEY"
    exit 0
fi

# Try keychain (macOS)
if command -v security &> /dev/null; then
    KEY=$(security find-generic-password -a "caam-claude-` + prof.Name + `" -s "anthropic-api-key" -w 2>/dev/null)
    if [ -n "$KEY" ]; then
        echo "$KEY"
        exit 0
    fi
fi

# Try secret-tool (Linux)
if command -v secret-tool &> /dev/null; then
    KEY=$(secret-tool lookup service caam-claude account ` + prof.Name + ` 2>/dev/null)
    if [ -n "$KEY" ]; then
        echo "$KEY"
        exit 0
    fi
fi

echo "Error: No API key found" >&2
exit 1
`

	if err := atomicWriteFile(helperPath, []byte(helperScript), 0700); err != nil {
		return fmt.Errorf("write helper script: %w", err)
	}

	// Change only the effective store's helper, preserving workflow policy.
	if err := update.Apply(); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	return nil
}

// Env returns the environment variables for running Claude in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	configDir, err := effectiveClaudeConfigDir(prof)
	if err != nil {
		return nil, err
	}
	env := map[string]string{
		"HOME":              prof.HomePath(),
		"XDG_CONFIG_HOME":   prof.XDGConfigPath(),
		"CLAUDE_CONFIG_DIR": configDir,
	}
	return env, nil
}

// Login initiates the authentication flow.
func (p *Provider) Login(ctx context.Context, prof *profile.Profile) error {
	switch provider.AuthMode(prof.AuthMode) {
	case provider.AuthModeAPIKey:
		return p.loginWithAPIKey(ctx, prof)
	default:
		return p.loginWithOAuth(ctx, prof)
	}
}

// loginWithOAuth launches Claude Code for interactive /login.
func (p *Provider) loginWithOAuth(ctx context.Context, prof *profile.Profile) error {
	if err := p.PrepareRun(ctx, prof); err != nil {
		return err
	}
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}

	fmt.Println("Launching Claude Code for authentication...")
	fmt.Println("Once inside, run /login to authenticate.")

	cmd := exec.CommandContext(ctx, "claude")
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

// loginWithAPIKey prompts for API key and stores it.
func (p *Provider) loginWithAPIKey(ctx context.Context, prof *profile.Profile) error {
	fmt.Println("API key mode is configured.")
	fmt.Println("Set ANTHROPIC_API_KEY environment variable or store in system keychain.")
	fmt.Printf("For macOS: security add-generic-password -a \"caam-claude-%s\" -s \"anthropic-api-key\" -w\n", prof.Name)
	fmt.Printf("For Linux: secret-tool store --label \"caam claude %s\" service caam-claude account %s\n", prof.Name, prof.Name)
	return nil
}

// Logout clears authentication credentials.
func (p *Provider) Logout(ctx context.Context, prof *profile.Profile) error {
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return err
	}
	var updates []*claudesettings.Update
	for _, path := range claudeSettingsPathsForProfile(prof) {
		update, err := claudesettings.PrepareClear(path, policy)
		if err != nil {
			return fmt.Errorf("prepare Claude logout: %w", err)
		}
		updates = append(updates, update)
	}
	for _, path := range []string{
		filepath.Join(prof.HomePath(), ".claude.json"),
		filepath.Join(claudeLegacyDirForProfile(prof), ".claude.json"),
		filepath.Join(claudeConfigDirForProfile(prof), ".claude.json"),
	} {
		update, err := claudesettings.PrepareLegacyClear(path, policy)
		if err != nil {
			return fmt.Errorf("prepare Claude session logout: %w", err)
		}
		updates = append(updates, update)
	}
	for _, update := range updates {
		if err := update.Apply(); err != nil {
			return fmt.Errorf("clear Claude authentication settings: %w", err)
		}
	}
	// Credentials are private artifacts; mixed settings retain workflow policy.
	authPaths := []string{
		claudeAuthPathForProfile(prof),
		claudeXDGCredentialsPathForProfile(prof),
		filepath.Join(claudeLegacyDirForProfile(prof), "auth.json"),
		filepath.Join(prof.HomePath(), ".claude", ".credentials.json"),
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

	configDir, err := effectiveClaudeConfigDir(prof)
	if err != nil {
		return nil, err
	}
	statePath := filepath.Join(configDir, ".claude.json")
	if configDir == claudeLegacyDirForProfile(prof) {
		statePath = legacyClaudeStatePath(prof)
	}
	for _, path := range []string{filepath.Join(configDir, ".credentials.json"), filepath.Join(configDir, "auth.json"), statePath, filepath.Join(configDir, "settings.json")} {
		hasAuth, err := claudeDocumentHasAuth(path)
		if err != nil {
			status.LoggedIn = false
			status.Error = fmt.Sprintf("invalid Claude auth: %v", err)
			return status, nil
		}
		status.LoggedIn = status.LoggedIn || hasAuth
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

// DetectExistingAuth detects existing Claude authentication files in standard locations.
// Locations checked:
// - ~/.claude/.credentials.json (primary OAuth credentials)
// - ~/.claude.json (legacy OAuth session state)
// - ~/.config/claude-code/auth.json (current auth credentials; or $CLAUDE_CONFIG_DIR/auth.json)
func (p *Provider) DetectExistingAuth() (*provider.AuthDetection, error) {
	detection := &provider.AuthDetection{
		Provider:  p.ID(),
		Locations: []provider.AuthLocation{},
	}

	// On macOS the OAuth blob lives in the login keychain; ~/.claude/.credentials.json
	// is its mirror. Refresh it or detection reports "no credentials found"
	// for a perfectly good login (issue #98).
	files := p.AuthFiles()
	_, _ = keychain.EnsureMirror(files[0].Path)

	// Define locations to check
	locations := []struct {
		path        string
		description string
	}{
		{
			path:        files[0].Path,
			description: "Claude Code OAuth credentials (primary location)",
		},
		{
			path:        files[1].Path,
			description: "Claude Code OAuth session state (legacy location)",
		},
		{
			path:        files[2].Path,
			description: "Claude Code auth credentials (CLAUDE_CONFIG_DIR or XDG_CONFIG_HOME)",
		},
		{
			path:        files[3].Path,
			description: "Claude Code settings (apiKeyHelper / API key mode)",
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

		hasAuth, err := claudeDocumentHasAuth(loc.path)
		authLoc.IsValid = err == nil && hasAuth
		if err != nil {
			authLoc.ValidationError = fmt.Sprintf("invalid auth: %v", err)
		} else if !hasAuth {
			authLoc.ValidationError = "no credential material in document"
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
	configDir, err := effectiveClaudeConfigDir(prof)
	if err != nil {
		return nil, err
	}
	policy, err := claudesettings.LoadPolicy()
	if err != nil {
		return nil, err
	}
	realHome, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	sharedSettings, sharedState := claudesettings.SharedPaths(realHome)

	var copiedFiles []string

	// Determine target based on source file type
	basename := filepath.Base(sourcePath)
	if basename == ".credentials.json" || basename == "auth.json" || basename == "settings.json" || basename == ".claude.json" {
		hasAuth, err := claudeDocumentHasAuth(sourcePath)
		if err != nil || !hasAuth {
			return nil, fmt.Errorf("source does not contain usable Claude authentication: %s", basename)
		}
		ignoredDir, ignoredState := claudeLegacyDirForProfile(prof), legacyClaudeStatePath(prof)
		if configDir == ignoredDir {
			ignoredDir = claudeConfigDirForProfile(prof)
			ignoredState = filepath.Join(ignoredDir, ".claude.json")
		}
		ignoredAuth, err := claudeDirHasAuth(ignoredDir, ignoredState)
		if err != nil {
			return nil, err
		}
		sourceID, ignoredID := claudeDirAccountID(filepath.Dir(sourcePath)), claudeDirAccountID(ignoredDir)
		if ignoredAuth && sourceID != "" && ignoredID != "" && sourceID != ignoredID {
			return nil, fmt.Errorf("import would create conflicting Claude accounts in legacy and XDG config directories")
		}
	}
	switch basename {
	case ".credentials.json":
		// Import one native credential store. Duplicating a rotating token
		// into the ignored directory would leave another stale generation.
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return nil, fmt.Errorf("create Claude config dir: %w", err)
		}
		targetPath := filepath.Join(configDir, ".credentials.json")
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy .credentials.json: %w", err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	case ".claude.json":
		targetPath := filepath.Join(configDir, ".claude.json")
		update, err := claudesettings.PrepareLegacyImport(sharedState, sourcePath, targetPath, policy)
		if err != nil {
			return nil, fmt.Errorf("prepare imported Claude session settings: %w", err)
		}
		if err := update.Apply(); err != nil {
			return nil, fmt.Errorf("import .claude.json: %w", err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	case "settings.json":
		targetPath := filepath.Join(configDir, "settings.json")
		update, err := claudesettings.PrepareImport(sharedSettings, sourcePath, targetPath, policy)
		if err != nil {
			return nil, fmt.Errorf("prepare imported Claude settings: %w", err)
		}
		if err := update.Apply(); err != nil {
			return nil, fmt.Errorf("import settings.json: %w", err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	case "auth.json":
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return nil, fmt.Errorf("create Claude config dir: %w", err)
		}
		targetPath := filepath.Join(configDir, "auth.json")
		if err := copyFile(sourcePath, targetPath); err != nil {
			return nil, fmt.Errorf("copy auth.json: %w", err)
		}
		copiedFiles = append(copiedFiles, targetPath)

	default:
		// Try to copy to a reasonable location based on the source
		if filepath.Base(filepath.Dir(sourcePath)) == "claude-code" {
			// Source is from claude-code directory
			targetDir := claudeConfigDirForProfile(prof)
			if err := os.MkdirAll(targetDir, 0700); err != nil {
				return nil, fmt.Errorf("create claude-code dir: %w", err)
			}
			targetPath := filepath.Join(targetDir, basename)
			if err := copyFile(sourcePath, targetPath); err != nil {
				return nil, fmt.Errorf("copy %s: %w", basename, err)
			}
			copiedFiles = append(copiedFiles, targetPath)
		} else {
			// Default: copy to home as is
			targetPath := filepath.Join(prof.HomePath(), basename)
			if err := copyFile(sourcePath, targetPath); err != nil {
				return nil, fmt.Errorf("copy %s: %w", basename, err)
			}
			copiedFiles = append(copiedFiles, targetPath)
		}
	}

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
// For active validation: attempts minimal API call (API key mode) or checks OAuth validity.
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
	configDir, err := effectiveClaudeConfigDir(prof)
	if err != nil {
		result.Error = err.Error()
		return result, nil
	}
	claudeJsonPath := filepath.Join(configDir, ".claude.json")
	if configDir == claudeLegacyDirForProfile(prof) {
		claudeJsonPath = legacyClaudeStatePath(prof)
	}
	authJsonPath := filepath.Join(configDir, "auth.json")
	// Validate the exact store selected for native execution. An ignored
	// legacy credential cannot rescue an expired or malformed XDG login.
	credentials := readClaudeCredentialCandidate(filepath.Join(configDir, ".credentials.json"))
	settingsPath := filepath.Join(configDir, "settings.json")
	claudeJsonHasAuth, err := claudeDocumentHasAuth(claudeJsonPath)
	if err != nil {
		result.Error = fmt.Sprintf("invalid .claude.json: %v", err)
		return result, nil
	}
	authJsonHasAuth, err := claudeDocumentHasAuth(authJsonPath)
	if err != nil {
		result.Error = fmt.Sprintf("invalid auth.json: %v", err)
		return result, nil
	}
	authJsonExists := fileExists(authJsonPath)
	credentialsExists := credentials != nil
	settingsExists := fileExists(settingsPath)

	if !claudeJsonHasAuth && !authJsonHasAuth && !credentialsExists {
		if settingsExists || provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey {
			hasKey, err := claudeSettingsHasAPIKey(settingsPath)
			if err != nil && settingsExists {
				result.Valid = false
				result.Error = fmt.Sprintf("invalid settings.json: %v", err)
				return result, nil
			}
			if hasKey {
				result.Valid = true
				return result, nil
			}
			result.Valid = false
			result.Error = "no API key settings found"
			return result, nil
		}

		result.Valid = false
		result.Error = "no auth files found"
		return result, nil
	}

	// Check the selected .credentials.json candidate if any exists.
	if credentialsExists {
		if credentials.parseErr != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("invalid .credentials.json: %v", credentials.parseErr)
			return result, nil
		}
		if credentials.expiresAt != nil {
			result.ExpiresAt = *credentials.expiresAt
			if result.ExpiresAt.Before(timeNow()) {
				result.Valid = false
				result.Error = "token has expired"
				return result, nil
			}
		}
	}

	// Policy-only state is not a login and has no authentication expiry.
	if claudeJsonHasAuth {
		data, err := os.ReadFile(claudeJsonPath)
		if err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("cannot read .claude.json: %v", err)
			return result, nil
		}

		var claudeData map[string]interface{}
		if err := json.Unmarshal(data, &claudeData); err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("invalid JSON in .claude.json: %v", err)
			return result, nil
		}

		// Check for expiry if present
		if expiresAt, ok := claudeData["expiresAt"]; ok {
			if expStr, ok := expiresAt.(string); ok {
				if exp, err := parseExpiryTime(expStr); err == nil {
					result.ExpiresAt = exp
					if exp.Before(timeNow()) {
						result.Valid = false
						result.Error = "token has expired"
						return result, nil
					}
				}
			} else if expFloat, ok := expiresAt.(float64); ok {
				// Unix timestamp in seconds or milliseconds
				exp := parseUnixTime(expFloat)
				result.ExpiresAt = exp
				if exp.Before(timeNow()) {
					result.Valid = false
					result.Error = "token has expired"
					return result, nil
				}
			}
		}

		// No further validation needed; different versions may use different field names.
	}

	// Check auth.json if it exists
	if authJsonExists {
		data, err := os.ReadFile(authJsonPath)
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

		// Check for expiry in auth.json
		if expiresAt, ok := authData["expires_at"]; ok {
			if expStr, ok := expiresAt.(string); ok {
				if exp, err := parseExpiryTime(expStr); err == nil {
					// Use the earliest expiry
					if result.ExpiresAt.IsZero() || exp.Before(result.ExpiresAt) {
						result.ExpiresAt = exp
					}
					if exp.Before(timeNow()) {
						result.Valid = false
						result.Error = "token has expired"
						return result, nil
					}
				}
			}
		}
	}

	// For API key mode, validate settings.json if present
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey && settingsExists {
		hasKey, err := claudeSettingsHasAPIKey(settingsPath)
		if err != nil {
			result.Valid = false
			result.Error = fmt.Sprintf("invalid settings.json: %v", err)
			return result, nil
		}
		if !hasKey {
			result.Valid = false
			result.Error = "no API key configured in settings.json"
			return result, nil
		}
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

	// For API key mode, we could make an actual API call
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey {
		// Try to call the Anthropic API to verify the key
		// For now, we skip active validation for API keys as it requires
		// the key to be available in the environment
		result.Valid = true
		result.Error = "" // Clear any passive error
		return result, nil
	}

	// For OAuth mode, active validation would require running the CLI
	// which is too heavy. Mark as valid based on passive checks.
	result.Valid = true
	return result, nil
}

// Helper functions

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func claudeSettingsHasAPIKey(path string) (bool, error) {
	return claudeDocumentHasAuth(path)
}

// Mixed policy documents are not proof of a login. Accept only supported,
// nonempty credential fields, including settings-based environment credentials.
func claudeDocumentHasAuth(path string) (bool, error) {
	data, err := claudesettings.Read(path)
	if err != nil || data == nil {
		return false, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil || obj == nil {
		return false, fmt.Errorf("%s must contain a JSON object", filepath.Base(path))
	}
	switch filepath.Base(path) {
	case ".credentials.json":
		creds, err := parseClaudeCredentials(data)
		if err != nil {
			return false, err
		}
		return creds.ClaudeAiOauth != nil && strings.TrimSpace(creds.ClaudeAiOauth.AccessToken) != "", nil
	case ".claude.json":
		return nonemptyClaudeFields(obj, "oauthToken", "sessionKey", "apiKey", "api_key", "primaryApiKey")
	case "auth.json":
		return nonemptyClaudeFields(obj, "accessToken", "access_token", "apiKey", "api_key")
	case "settings.json":
		found, err := nonemptyClaudeFields(obj, "apiKeyHelper", "apiKey", "api_key")
		if err != nil {
			return false, err
		}
		if raw, ok := obj["env"]; ok {
			var env map[string]json.RawMessage
			if json.Unmarshal(raw, &env) != nil || env == nil {
				return false, fmt.Errorf("settings env must contain a JSON object")
			}
			envAuth, err := nonemptyClaudeFields(env, "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN")
			return found || envAuth, err
		}
		return found, nil
	}
	return false, nil
}

func nonemptyClaudeFields(obj map[string]json.RawMessage, keys ...string) (bool, error) {
	found := false
	for _, key := range keys {
		if raw, ok := obj[key]; ok {
			var value string
			if string(raw) == "null" || json.Unmarshal(raw, &value) != nil {
				return false, fmt.Errorf("%s must be a string", key)
			}
			found = found || strings.TrimSpace(value) != ""
		}
	}
	return found, nil
}

type claudeCredentials struct {
	ClaudeAiOauth *claudeOAuth `json:"claudeAiOauth"`
}

type claudeOAuth struct {
	AccessToken  string  `json:"accessToken"`
	RefreshToken string  `json:"refreshToken"`
	ExpiresAt    float64 `json:"expiresAt"`
	AccountID    string  `json:"accountId"`
}

func parseClaudeCredentials(data []byte) (*claudeCredentials, error) {
	var creds claudeCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return &creds, nil
}

func (c *claudeCredentials) hasToken() bool {
	if c == nil || c.ClaudeAiOauth == nil {
		return false
	}
	return c.ClaudeAiOauth.AccessToken != "" || c.ClaudeAiOauth.RefreshToken != ""
}

type credentialsInfo struct {
	expiresAt *time.Time
}

func loadClaudeCredentials(path string) (*credentialsInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	creds, err := parseClaudeCredentials(data)
	if err != nil {
		return nil, err
	}
	if creds.ClaudeAiOauth == nil || strings.TrimSpace(creds.ClaudeAiOauth.AccessToken) == "" {
		return nil, fmt.Errorf("no access credential in .credentials.json")
	}

	info := &credentialsInfo{}
	if creds.ClaudeAiOauth != nil && creds.ClaudeAiOauth.ExpiresAt > 0 {
		exp := time.UnixMilli(int64(creds.ClaudeAiOauth.ExpiresAt))
		info.expiresAt = &exp
	}
	return info, nil
}

// claudeCredCandidate is one probed .credentials.json candidate.
type claudeCredCandidate struct {
	path      string
	parseErr  error
	expiresAt *time.Time
}

func readClaudeCredentialCandidate(path string) *claudeCredCandidate {
	if !fileExists(path) {
		return nil
	}
	candidate := &claudeCredCandidate{path: path}
	if info, err := loadClaudeCredentials(path); err != nil {
		candidate.parseErr = err
	} else {
		candidate.expiresAt = info.expiresAt
	}
	return candidate
}

func parseExpiryTime(s string) (time.Time, error) {
	// Try common formats
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

func parseUnixTime(f float64) time.Time {
	// If value is > 1e12, it's likely milliseconds
	if f > 1e12 {
		return time.UnixMilli(int64(f))
	}
	return time.Unix(int64(f), 0)
}

// timeNow is a variable for testing
var timeNow = time.Now

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
