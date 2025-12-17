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
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/passthrough"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
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
		provider.AuthModeOAuth,  // Browser-based ChatGPT login (GPT Pro subscription)
		provider.AuthModeAPIKey, // OpenAI API key
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

	return nil
}

// Env returns the environment variables for running Codex in this profile's context.
func (p *Provider) Env(ctx context.Context, prof *profile.Profile) (map[string]string, error) {
	env := map[string]string{
		"CODEX_HOME": prof.CodexHomePath(),
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

// loginWithOAuth runs the browser-based login flow.
func (p *Provider) loginWithOAuth(ctx context.Context, prof *profile.Profile) error {
	codexHomePath := prof.CodexHomePath()

	cmd := exec.CommandContext(ctx, "codex", "login")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHomePath)

	// If browser profile is configured, use it for the OAuth flow
	if prof.HasBrowserConfig() {
		launcher := browser.NewLauncher(&browser.Config{
			Command:    prof.BrowserCommand,
			ProfileDir: prof.BrowserProfileDir,
		})
		fmt.Printf("Using browser profile: %s\n", prof.BrowserDisplayName())

		// Set up URL detection and capture
		capture := browser.NewOutputCapture(os.Stdout, os.Stderr)
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

	return cmd.Run()
}

// loginWithAPIKey prompts for and stores an API key.
func (p *Provider) loginWithAPIKey(ctx context.Context, prof *profile.Profile) error {
	codexHomePath := prof.CodexHomePath()

	// Check for OPENAI_API_KEY environment variable first
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		fmt.Print("Enter OpenAI API key (input hidden): ")
		reader := bufio.NewReader(os.Stdin)
		key, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read API key: %w", err)
		}
		apiKey = strings.TrimSpace(key)
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

		statuses, err := mgr.VerifyPassthroughs(prof, homePath)
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

// Ensure Provider implements the interface.
var _ provider.Provider = (*Provider)(nil)
