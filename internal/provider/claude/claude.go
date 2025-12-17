// Package claude implements the provider adapter for Anthropic Claude Code CLI.
//
// Authentication mechanics (from research):
// - First use: run `claude`, then authenticate via `/login` inside the interactive session.
// - `/login` is also the documented way to switch accounts.
// - Auth state stored in:
//   - ~/.claude.json (main OAuth session state)
//   - ~/.config/claude-code/auth.json (auth credentials)
//   - ~/.claude/settings.json (user settings)
//   - Project .claude/* files
//
// Context isolation for caam:
// - Set HOME to pseudo-home directory
// - Set XDG_CONFIG_HOME to pseudo-xdg_config directory
// - This makes these become profile-scoped:
//   - ${XDG_CONFIG_HOME}/claude-code/auth.json
//   - ${HOME}/.claude.json
//   - ${HOME}/.claude/settings.json
//
// Auth file swapping (PRIMARY use case):
// - Backup ~/.claude.json and ~/.config/claude-code/auth.json after logging in
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
	"os"
	"os/exec"
	"path/filepath"

	"github.com/user/caam/internal/passthrough"
	"github.com/user/caam/internal/profile"
	"github.com/user/caam/internal/provider"
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

// AuthFiles returns the auth file specifications for Claude Code.
// This is the key method for auth file backup/restore.
func (p *Provider) AuthFiles() []provider.AuthFileSpec {
	homeDir, _ := os.UserHomeDir()

	return []provider.AuthFileSpec{
		{
			Path:        filepath.Join(homeDir, ".claude.json"),
			Description: "Claude Code OAuth session state (Claude Max subscription)",
			Required:    true,
		},
		{
			Path:        filepath.Join(xdgConfigHome(), "claude-code", "auth.json"),
			Description: "Claude Code auth credentials",
			Required:    false, // May not exist in all setups
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
	claudeCodeDir := filepath.Join(xdgConfig, "claude-code")
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

	if err := mgr.SetupPassthroughs(prof, homePath); err != nil {
		return fmt.Errorf("setup passthroughs: %w", err)
	}

	// If using API key mode, set up the apiKeyHelper configuration
	if provider.AuthMode(prof.AuthMode) == provider.AuthModeAPIKey {
		if err := p.setupAPIKeyHelper(prof); err != nil {
			return fmt.Errorf("setup apiKeyHelper: %w", err)
		}
	}

	return nil
}

// setupAPIKeyHelper creates the settings.json with apiKeyHelper configuration.
func (p *Provider) setupAPIKeyHelper(prof *profile.Profile) error {
	settingsPath := filepath.Join(prof.HomePath(), ".claude", "settings.json")

	// Create a helper script path
	helperPath := filepath.Join(prof.BasePath, "api_key_helper.sh")

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

	if err := os.WriteFile(helperPath, []byte(helperScript), 0700); err != nil {
		return fmt.Errorf("write helper script: %w", err)
	}

	// Write settings.json
	settings := map[string]interface{}{
		"apiKeyHelper": helperPath,
	}

	data, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal settings: %w", err)
	}

	if err := os.WriteFile(settingsPath, data, 0600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}

	return nil
}

// Env returns the environment variables for running Claude in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"HOME":            prof.HomePath(),
		"XDG_CONFIG_HOME": prof.XDGConfigPath(),
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
	env, err := p.Env(ctx, prof)
	if err != nil {
		return err
	}

	fmt.Println("Launching Claude Code for authentication...")
	fmt.Println("Once inside, run /login to authenticate.")
	fmt.Println("Press Ctrl+C when done.")

	cmd := exec.CommandContext(ctx, "claude")
	cmd.Env = os.Environ()
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
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
	// Remove auth files
	authPaths := []string{
		filepath.Join(prof.XDGConfigPath(), "claude-code", "auth.json"),
		filepath.Join(prof.HomePath(), ".claude.json"),
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

	// Check if auth.json exists
	authPath := filepath.Join(prof.XDGConfigPath(), "claude-code", "auth.json")
	if _, err := os.Stat(authPath); err == nil {
		status.LoggedIn = true
	}

	// Also check .claude.json for OAuth state
	claudeJsonPath := filepath.Join(prof.HomePath(), ".claude.json")
	if _, err := os.Stat(claudeJsonPath); err == nil {
		status.LoggedIn = true
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

	statuses, err := mgr.VerifyPassthroughs(prof, homePath)
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

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
