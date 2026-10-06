package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// =============================================================================
// Provider Factory Tests
// =============================================================================

func TestNew(t *testing.T) {
	p := New()
	if p == nil {
		t.Fatal("New() returned nil")
	}
}

// =============================================================================
// Provider Identity Tests
// =============================================================================

func TestProviderID(t *testing.T) {
	p := New()
	if p.ID() != "claude" {
		t.Errorf("ID() = %q, want %q", p.ID(), "claude")
	}
}

func TestProviderDisplayName(t *testing.T) {
	p := New()
	expected := "Claude Code (Anthropic Claude Max)"
	if p.DisplayName() != expected {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), expected)
	}
}

func TestProviderDefaultBin(t *testing.T) {
	p := New()
	if p.DefaultBin() != "claude" {
		t.Errorf("DefaultBin() = %q, want %q", p.DefaultBin(), "claude")
	}
}

// =============================================================================
// Auth Mode Tests
// =============================================================================

func TestSupportedAuthModes(t *testing.T) {
	p := New()
	modes := p.SupportedAuthModes()

	if len(modes) != 2 {
		t.Fatalf("SupportedAuthModes() returned %d modes, want 2", len(modes))
	}

	hasOAuth := false
	hasAPIKey := false
	for _, mode := range modes {
		if mode == provider.AuthModeOAuth {
			hasOAuth = true
		}
		if mode == provider.AuthModeAPIKey {
			hasAPIKey = true
		}
	}

	if !hasOAuth {
		t.Error("SupportedAuthModes() should include OAuth")
	}
	if !hasAPIKey {
		t.Error("SupportedAuthModes() should include APIKey")
	}
}

// =============================================================================
// Auth Files Tests
// =============================================================================

func TestAuthFiles(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Run("returns four auth file specs", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		if len(files) != 4 {
			t.Fatalf("AuthFiles() returned %d files, want 4", len(files))
		}
	})

	t.Run("first file is .credentials.json and required", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[0]
		if !strings.HasSuffix(file.Path, filepath.Join(".claude", ".credentials.json")) {
			t.Errorf("AuthFiles()[0].Path = %q, should end with .claude/.credentials.json", file.Path)
		}
		if !file.Required {
			t.Error(".credentials.json should be required")
		}
	})

	t.Run("second file is .claude.json and optional", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[1]
		if !strings.HasSuffix(file.Path, ".claude.json") {
			t.Errorf("AuthFiles()[1].Path = %q, should end with .claude.json", file.Path)
		}
		if file.Required {
			t.Error(".claude.json should be optional")
		}
	})

	t.Run("third file is auth.json and optional", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[2]
		if !strings.HasSuffix(file.Path, "claude-code/auth.json") {
			t.Errorf("AuthFiles()[2].Path = %q, should end with claude-code/auth.json", file.Path)
		}
		if file.Required {
			t.Error("claude-code/auth.json should be optional")
		}
	})

	t.Run("fourth file is settings.json and optional", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[3]
		if !strings.HasSuffix(file.Path, filepath.Join(".claude", "settings.json")) {
			t.Errorf("AuthFiles()[3].Path = %q, should end with .claude/settings.json", file.Path)
		}
		if file.Required {
			t.Error(".claude/settings.json should be optional")
		}
	})

	t.Run("uses CLAUDE_CONFIG_DIR if set", func(t *testing.T) {
		originalClaude := os.Getenv("CLAUDE_CONFIG_DIR")
		originalXDG := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("CLAUDE_CONFIG_DIR", originalClaude)
		defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

		os.Setenv("CLAUDE_CONFIG_DIR", "/custom/claude")
		os.Unsetenv("XDG_CONFIG_HOME")
		p := New()
		files := p.AuthFiles()

		expected := "/custom/claude/auth.json"
		if files[2].Path != expected {
			t.Errorf("AuthFiles()[2].Path = %q, want %q", files[2].Path, expected)
		}
	})

	t.Run("uses XDG_CONFIG_HOME if set", func(t *testing.T) {
		originalClaude := os.Getenv("CLAUDE_CONFIG_DIR")
		originalXDG := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("CLAUDE_CONFIG_DIR", originalClaude)
		defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

		os.Unsetenv("CLAUDE_CONFIG_DIR")
		os.Setenv("XDG_CONFIG_HOME", "/custom/config")
		p := New()
		files := p.AuthFiles()

		expected := "/custom/config/claude-code/auth.json"
		if files[2].Path != expected {
			t.Errorf("AuthFiles()[2].Path = %q, want %q", files[2].Path, expected)
		}
	})

	t.Run("uses default .config if XDG_CONFIG_HOME not set", func(t *testing.T) {
		originalClaude := os.Getenv("CLAUDE_CONFIG_DIR")
		originalXDG := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("CLAUDE_CONFIG_DIR", originalClaude)
		defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

		os.Unsetenv("CLAUDE_CONFIG_DIR")
		os.Unsetenv("XDG_CONFIG_HOME")
		p := New()
		files := p.AuthFiles()

		homeDir, _ := os.UserHomeDir()
		expected := filepath.Join(homeDir, ".config", "claude-code", "auth.json")
		if files[2].Path != expected {
			t.Errorf("AuthFiles()[2].Path = %q, want %q", files[2].Path, expected)
		}
	})
}

// =============================================================================
// PrepareProfile Tests
// =============================================================================

func TestPrepareProfile(t *testing.T) {
	t.Run("creates home directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		if err := p.PrepareProfile(context.Background(), prof); err != nil {
			t.Fatalf("PrepareProfile() error = %v", err)
		}

		homePath := prof.HomePath()
		info, err := os.Stat(homePath)
		if err != nil {
			t.Fatalf("home not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("home should be a directory")
		}
	})

	t.Run("creates xdg_config directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		xdgPath := prof.XDGConfigPath()
		info, err := os.Stat(xdgPath)
		if err != nil {
			t.Fatalf("xdg_config not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("xdg_config should be a directory")
		}
	})

	t.Run("creates claude-code directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		claudeCodeDir := filepath.Join(prof.XDGConfigPath(), "claude-code")
		info, err := os.Stat(claudeCodeDir)
		if err != nil {
			t.Fatalf("claude-code dir not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("claude-code should be a directory")
		}
	})

	t.Run("creates .claude directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		claudeDir := filepath.Join(prof.HomePath(), ".claude")
		info, err := os.Stat(claudeDir)
		if err != nil {
			t.Fatalf(".claude dir not created: %v", err)
		}
		if !info.IsDir() {
			t.Error(".claude should be a directory")
		}
	})

	t.Run("sets secure permissions", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Check home directory permissions
		homePath := prof.HomePath()
		info, _ := os.Stat(homePath)
		if info.Mode().Perm() != 0700 {
			t.Errorf("home permissions = %o, want 0700", info.Mode().Perm())
		}

		// Check xdg_config permissions
		xdgPath := prof.XDGConfigPath()
		info, _ = os.Stat(xdgPath)
		if info.Mode().Perm() != 0700 {
			t.Errorf("xdg_config permissions = %o, want 0700", info.Mode().Perm())
		}
	})

	t.Run("idempotent - can be called multiple times", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)
		if err := p.PrepareProfile(context.Background(), prof); err != nil {
			t.Errorf("second PrepareProfile() error = %v", err)
		}
	})
}

// =============================================================================
// API Key Helper Tests
// =============================================================================

func TestPrepareProfileWithAPIKey(t *testing.T) {
	t.Run("creates api_key_helper script", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			AuthMode: string(provider.AuthModeAPIKey),
			BasePath: tmpDir,
		}

		p := New()
		if err := p.PrepareProfile(context.Background(), prof); err != nil {
			t.Fatalf("PrepareProfile() error = %v", err)
		}

		helperPath := filepath.Join(tmpDir, "api_key_helper.sh")
		info, err := os.Stat(helperPath)
		if err != nil {
			t.Fatalf("api_key_helper.sh not created: %v", err)
		}
		// Should be executable
		if info.Mode().Perm()&0100 == 0 {
			t.Error("api_key_helper.sh should be executable")
		}
	})

	t.Run("creates settings.json with apiKeyHelper", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			AuthMode: string(provider.AuthModeAPIKey),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		settingsPath := filepath.Join(claudeConfigDirForProfile(prof), "settings.json")
		data, err := os.ReadFile(settingsPath)
		if err != nil {
			t.Fatalf("settings.json not created: %v", err)
		}

		if !strings.Contains(string(data), "apiKeyHelper") {
			t.Error("settings.json should contain apiKeyHelper")
		}
	})

	t.Run("does not create settings.json for OAuth mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			AuthMode: string(provider.AuthModeOAuth),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		settingsPath := filepath.Join(prof.HomePath(), ".claude", "settings.json")
		if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
			t.Error("settings.json should not be created for OAuth mode")
		}
	})
}

// =============================================================================
// Env Tests
// =============================================================================

func TestEnv(t *testing.T) {
	t.Run("sets HOME", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		env, err := p.Env(context.Background(), prof)
		if err != nil {
			t.Fatalf("Env() error = %v", err)
		}

		home, ok := env["HOME"]
		if !ok {
			t.Fatal("HOME not set in env")
		}

		expected := prof.HomePath()
		if home != expected {
			t.Errorf("HOME = %q, want %q", home, expected)
		}
	})

	t.Run("sets XDG_CONFIG_HOME", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		xdg, ok := env["XDG_CONFIG_HOME"]
		if !ok {
			t.Fatal("XDG_CONFIG_HOME not set in env")
		}

		expected := prof.XDGConfigPath()
		if xdg != expected {
			t.Errorf("XDG_CONFIG_HOME = %q, want %q", xdg, expected)
		}
	})

	t.Run("sets CLAUDE_CONFIG_DIR", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		cfg, ok := env["CLAUDE_CONFIG_DIR"]
		if !ok {
			t.Fatal("CLAUDE_CONFIG_DIR not set in env")
		}

		expected := filepath.Join(prof.XDGConfigPath(), "claude-code")
		if cfg != expected {
			t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", cfg, expected)
		}
	})

	t.Run("returns exactly three env vars", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		if len(env) != 3 {
			t.Errorf("Env() returned %d vars, want 3", len(env))
		}
	})
}

// =============================================================================
// Logout Tests
// =============================================================================

func TestLogout(t *testing.T) {
	t.Run("removes auth.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create auth.json
		authDir := filepath.Join(prof.XDGConfigPath(), "claude-code")
		os.MkdirAll(authDir, 0700)
		authPath := filepath.Join(authDir, "auth.json")
		if err := os.WriteFile(authPath, []byte(`{"token":"test"}`), 0600); err != nil {
			t.Fatal(err)
		}

		// Logout
		if err := p.Logout(context.Background(), prof); err != nil {
			t.Fatalf("Logout() error = %v", err)
		}

		// Verify removed
		if _, err := os.Stat(authPath); !os.IsNotExist(err) {
			t.Error("auth.json should be removed after Logout")
		}
	})

	t.Run("scrubs account state from .claude.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create .claude.json
		claudeJsonPath := filepath.Join(prof.HomePath(), ".claude.json")
		if err := os.WriteFile(claudeJsonPath, []byte(`{"sessionKey":"test","mcpServers":{"kept":{}}}`), 0600); err != nil {
			t.Fatal(err)
		}

		// Logout
		if err := p.Logout(context.Background(), prof); err != nil {
			t.Fatalf("Logout() error = %v", err)
		}

		data, err := os.ReadFile(claudeJsonPath)
		if err != nil || strings.Contains(string(data), "sessionKey") || !strings.Contains(string(data), "mcpServers") {
			t.Errorf("logout did not preserve policy and scrub account state: %s, %v", data, err)
		}
	})

	t.Run("removes XDG-side .credentials.json (issue #70)", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// XDG-aware Claude Code builds store credentials here; if Logout
		// leaves the file behind, Status keeps reporting LoggedIn=true.
		credPath := filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json")
		os.MkdirAll(filepath.Dir(credPath), 0700)
		if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"accessToken":"sk-test"}}`), 0600); err != nil {
			t.Fatal(err)
		}

		if err := p.Logout(context.Background(), prof); err != nil {
			t.Fatalf("Logout() error = %v", err)
		}

		if _, err := os.Stat(credPath); !os.IsNotExist(err) {
			t.Error("XDG-side .credentials.json should be removed after Logout")
		}
		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.LoggedIn {
			t.Error("LoggedIn should be false after Logout")
		}
	})

	t.Run("handles non-existent auth files", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Don't create auth files, just logout
		if err := p.Logout(context.Background(), prof); err != nil {
			t.Errorf("Logout() error = %v, should handle missing files", err)
		}
	})
}

// =============================================================================
// Status Tests
// =============================================================================

func TestStatus(t *testing.T) {
	t.Run("logged in when auth.json exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create auth.json
		authDir := filepath.Join(prof.XDGConfigPath(), "claude-code")
		os.MkdirAll(authDir, 0700)
		authPath := filepath.Join(authDir, "auth.json")
		if err := os.WriteFile(authPath, []byte(`{"accessToken":"test-auth"}`), 0600); err != nil {
			t.Fatal(err)
		}

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true when auth.json exists")
		}
	})

	t.Run("logged in when .claude.json contains a credential", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create .claude.json
		claudeJsonPath := filepath.Join(prof.HomePath(), ".claude.json")
		if err := os.WriteFile(claudeJsonPath, []byte(`{"oauthToken":"test-oauth"}`), 0600); err != nil {
			t.Fatal(err)
		}

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true when .claude.json exists")
		}
	})

	t.Run("logged in when XDG-side .credentials.json exists (issue #70)", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// XDG-aware Claude Code builds write credentials under
		// xdg_config/claude-code/ (the CLAUDE_CONFIG_DIR the profile env
		// sets), not the legacy home/.claude/ path.
		credPath := filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json")
		os.MkdirAll(filepath.Dir(credPath), 0700)
		if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"accessToken":"sk-test"}}`), 0600); err != nil {
			t.Fatal(err)
		}

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true when XDG-side .credentials.json exists (issue #70)")
		}
	})

	t.Run("not logged in when no auth files exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.LoggedIn {
			t.Error("LoggedIn should be false when no auth files exist")
		}
	})

	t.Run("reports lock file status", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Initially not locked
		status, _ := p.Status(context.Background(), prof)
		if status.HasLockFile {
			t.Error("HasLockFile should be false initially")
		}

		// Lock the profile
		prof.Lock()
		defer prof.Unlock()

		status, _ = p.Status(context.Background(), prof)
		if !status.HasLockFile {
			t.Error("HasLockFile should be true when locked")
		}
	})
}

// =============================================================================
// ValidateProfile Tests
// =============================================================================

func TestValidateProfile(t *testing.T) {
	t.Run("valid when all directories exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		if err := p.ValidateProfile(context.Background(), prof); err != nil {
			t.Errorf("ValidateProfile() error = %v", err)
		}
	})

	t.Run("invalid when home missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		p := New()
		// Don't call PrepareProfile

		err := p.ValidateProfile(context.Background(), prof)
		if err == nil {
			t.Error("ValidateProfile() should error when home missing")
		}
	})

	t.Run("invalid when xdg_config missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}

		// Create home but not xdg_config
		os.MkdirAll(prof.HomePath(), 0700)

		p := New()
		err := p.ValidateProfile(context.Background(), prof)
		if err == nil {
			t.Error("ValidateProfile() should error when xdg_config missing")
		}
	})
}

// =============================================================================
// Interface Compliance Tests
// =============================================================================

func TestProviderInterface(t *testing.T) {
	// Ensure Provider implements provider.Provider
	var _ provider.Provider = (*Provider)(nil)

	p := New()
	var iface provider.Provider = p

	// Test all interface methods exist
	_ = iface.ID()
	_ = iface.DisplayName()
	_ = iface.DefaultBin()
	_ = iface.SupportedAuthModes()
	_ = iface.AuthFiles()
}

// =============================================================================
// xdgConfigHome Helper Tests
// =============================================================================

func TestXDGConfigHome(t *testing.T) {
	t.Run("respects XDG_CONFIG_HOME env var", func(t *testing.T) {
		original := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("XDG_CONFIG_HOME", original)

		os.Setenv("XDG_CONFIG_HOME", "/test/xdg")
		result := xdgConfigHome()
		if result != "/test/xdg" {
			t.Errorf("xdgConfigHome() = %q, want /test/xdg", result)
		}
	})

	t.Run("falls back to ~/.config", func(t *testing.T) {
		original := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("XDG_CONFIG_HOME", original)

		os.Unsetenv("XDG_CONFIG_HOME")
		result := xdgConfigHome()
		homeDir, _ := os.UserHomeDir()
		expected := filepath.Join(homeDir, ".config")
		if result != expected {
			t.Errorf("xdgConfigHome() = %q, want %s", result, expected)
		}
	})
}

// =============================================================================
// claudeConfigDir Helper Tests
// =============================================================================

func TestClaudeConfigDir(t *testing.T) {
	t.Run("respects CLAUDE_CONFIG_DIR env var", func(t *testing.T) {
		originalClaude := os.Getenv("CLAUDE_CONFIG_DIR")
		originalXDG := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("CLAUDE_CONFIG_DIR", originalClaude)
		defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

		os.Setenv("CLAUDE_CONFIG_DIR", "/test/claude")
		os.Setenv("XDG_CONFIG_HOME", "/ignored")
		result := claudeConfigDir()
		if result != "/test/claude" {
			t.Errorf("claudeConfigDir() = %q, want /test/claude", result)
		}
	})

	t.Run("falls back to XDG_CONFIG_HOME/claude-code", func(t *testing.T) {
		originalClaude := os.Getenv("CLAUDE_CONFIG_DIR")
		originalXDG := os.Getenv("XDG_CONFIG_HOME")
		defer os.Setenv("CLAUDE_CONFIG_DIR", originalClaude)
		defer os.Setenv("XDG_CONFIG_HOME", originalXDG)

		os.Unsetenv("CLAUDE_CONFIG_DIR")
		os.Setenv("XDG_CONFIG_HOME", "/test/xdg")
		result := claudeConfigDir()
		expected := filepath.Join("/test/xdg", "claude-code")
		if result != expected {
			t.Errorf("claudeConfigDir() = %q, want %s", result, expected)
		}
	})
}

// =============================================================================
// Integration Test
// =============================================================================

func TestFullProfileLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "lifecycle-test",
		Provider: "claude",
		AuthMode: "oauth",
		BasePath: tmpDir,
	}

	p := New()

	// Prepare
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile() error = %v", err)
	}

	// Validate (should pass now)
	if err := p.ValidateProfile(context.Background(), prof); err != nil {
		t.Fatalf("ValidateProfile() error = %v", err)
	}

	// Status (not logged in yet)
	status, _ := p.Status(context.Background(), prof)
	if status.LoggedIn {
		t.Error("should not be logged in before login")
	}

	// Simulate login by creating .claude.json
	claudeJsonPath := filepath.Join(prof.HomePath(), ".claude.json")
	os.WriteFile(claudeJsonPath, []byte(`{"sessionKey":"test"}`), 0600)

	// Status (now logged in)
	status, _ = p.Status(context.Background(), prof)
	if !status.LoggedIn {
		t.Error("should be logged in after .claude.json created")
	}

	// Get env
	env, _ := p.Env(context.Background(), prof)
	if env["HOME"] == "" {
		t.Error("HOME should be set")
	}
	if env["XDG_CONFIG_HOME"] == "" {
		t.Error("XDG_CONFIG_HOME should be set")
	}
	if env["CLAUDE_CONFIG_DIR"] == "" {
		t.Error("CLAUDE_CONFIG_DIR should be set")
	}

	// Logout
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}

	// Status (logged out)
	status, _ = p.Status(context.Background(), prof)
	if status.LoggedIn {
		t.Error("should not be logged in after logout")
	}
}

// =============================================================================
// API Key Mode Integration Test
// =============================================================================

func TestAPIKeyModeLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "apikey-test",
		Provider: "claude",
		AuthMode: string(provider.AuthModeAPIKey),
		BasePath: tmpDir,
	}

	p := New()

	// Prepare (should create API key helper)
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile() error = %v", err)
	}

	// Verify helper script exists and is executable
	helperPath := filepath.Join(tmpDir, "api_key_helper.sh")
	info, err := os.Stat(helperPath)
	if err != nil {
		t.Fatalf("api_key_helper.sh not found: %v", err)
	}
	if info.Mode().Perm()&0100 == 0 {
		t.Error("api_key_helper.sh should be executable")
	}

	// Verify settings.json exists
	settingsPath := filepath.Join(claudeConfigDirForProfile(prof), "settings.json")
	settingsData, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("settings.json not found: %v", err)
	}
	if !strings.Contains(string(settingsData), helperPath) {
		t.Error("settings.json should reference the helper script path")
	}

	// Status should report logged in for API key mode
	status, err := p.Status(context.Background(), prof)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if !status.LoggedIn {
		t.Error("Status() should report logged in when apiKeyHelper is configured")
	}

	// Passive validation should pass for API key mode
	result, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("ValidateToken() should be valid, got error: %s", result.Error)
	}

	// Validate should pass
	if err := p.ValidateProfile(context.Background(), prof); err != nil {
		t.Errorf("ValidateProfile() error = %v", err)
	}
}

// =============================================================================
// DetectExistingAuth Tests
// =============================================================================

func TestDetectExistingAuth(t *testing.T) {
	// Helper to set up fake home and config
	setupEnv := func(t *testing.T) (string, string) {
		home := t.TempDir()
		xdg := filepath.Join(home, ".config")
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)
		t.Setenv("CLAUDE_CONFIG_DIR", "")
		return home, xdg
	}

	t.Run("detects .credentials.json (primary)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		// Create .claude directory
		claudeDir := filepath.Join(home, ".claude")
		os.MkdirAll(claudeDir, 0700)

		// Create valid credentials file
		credsPath := filepath.Join(claudeDir, ".credentials.json")
		credsData := map[string]interface{}{
			"claudeAiOauth": map[string]interface{}{
				"accessToken": "valid-token",
				"expiresAt":   time.Now().Add(time.Hour).UnixMilli(),
			},
		}
		writeJSON(t, credsPath, credsData)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary == nil {
			t.Fatal("Primary should not be nil")
		}
		if detection.Primary.Path != credsPath {
			t.Errorf("Primary.Path = %q, want %q", detection.Primary.Path, credsPath)
		}
		if !detection.Primary.IsValid {
			t.Error("Primary should be valid")
		}
	})

	t.Run("detects .claude.json (legacy)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		// Create valid .claude.json
		path := filepath.Join(home, ".claude.json")
		data := map[string]interface{}{
			"oauthToken": "valid-token",
		}
		writeJSON(t, path, data)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary.Path = %q, want %q", detection.Primary.Path, path)
		}
	})

	t.Run("detects auth.json (xdg)", func(t *testing.T) {
		_, xdg := setupEnv(t)
		p := New()

		// Create claude-code directory
		dir := filepath.Join(xdg, "claude-code")
		os.MkdirAll(dir, 0700)

		// Create valid auth.json
		path := filepath.Join(dir, "auth.json")
		data := map[string]interface{}{
			"accessToken": "valid-token",
		}
		writeJSON(t, path, data)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary.Path = %q, want %q", detection.Primary.Path, path)
		}
	})

	t.Run("detects auth.json (CLAUDE_CONFIG_DIR)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		claudeConfigDir := filepath.Join(home, "claude-config")
		t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDir)
		os.MkdirAll(claudeConfigDir, 0700)

		path := filepath.Join(claudeConfigDir, "auth.json")
		data := map[string]interface{}{
			"accessToken": "valid-token",
		}
		writeJSON(t, path, data)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary.Path = %q, want %q", detection.Primary.Path, path)
		}
	})

	t.Run("detects settings.json (api key)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		dir := filepath.Join(home, ".claude")
		os.MkdirAll(dir, 0700)

		path := filepath.Join(dir, "settings.json")
		data := map[string]interface{}{
			"apiKeyHelper": "/path/to/helper",
		}
		writeJSON(t, path, data)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary.Path = %q, want %q", detection.Primary.Path, path)
		}
	})

	t.Run("prioritizes most recent file", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		// Create older .claude.json
		oldPath := filepath.Join(home, ".claude.json")
		writeJSON(t, oldPath, map[string]interface{}{"oauthToken": "old"})
		os.Chtimes(oldPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

		// Create newer .credentials.json
		dir := filepath.Join(home, ".claude")
		os.MkdirAll(dir, 0700)
		newPath := filepath.Join(dir, ".credentials.json")
		writeJSON(t, newPath, map[string]interface{}{
			"claudeAiOauth": map[string]interface{}{"accessToken": "new"},
		})
		// Ensure it's newer
		os.Chtimes(newPath, time.Now(), time.Now())

		detection, _ := p.DetectExistingAuth()
		if detection.Primary.Path != newPath {
			t.Errorf("Should prioritize newer file. Got %q, want %q", detection.Primary.Path, newPath)
		}
		if detection.Warning == "" {
			t.Error("Should have warning about multiple files")
		}
	})

	t.Run("validates JSON structure", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		// Create invalid JSON file
		path := filepath.Join(home, ".claude.json")
		os.WriteFile(path, []byte("{invalid-json"), 0600)

		detection, _ := p.DetectExistingAuth()
		if len(detection.Locations) == 0 {
			t.Fatal("Should detect file existence")
		}

		loc := detection.Locations[1] // .claude.json is 2nd in list
		if loc.IsValid {
			t.Error("Should mark invalid JSON as invalid")
		}
		if loc.ValidationError == "" {
			t.Error("Should have validation error")
		}
	})
}

// =============================================================================
// ImportAuth Tests
// =============================================================================

func TestImportAuth(t *testing.T) {
	t.Run("imports .credentials.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create source file
		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, ".credentials.json")
		writeJSON(t, srcPath, map[string]interface{}{"claudeAiOauth": map[string]string{"accessToken": "selected-token"}})

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth() error = %v", err)
		}

		if len(copied) != 1 {
			t.Fatalf("Expected 1 copied file, got %d", len(copied))
		}

		expectedPath := filepath.Join(claudeConfigDirForProfile(prof), ".credentials.json")
		if copied[0] != expectedPath {
			t.Errorf("Copied path = %q, want %q", copied[0], expectedPath)
		}

		if !fileExists(expectedPath) {
			t.Error("Target file not created")
		}
	})

	t.Run("imports auth.json to xdg location", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "claude",
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "auth.json")
		writeJSON(t, srcPath, map[string]string{"accessToken": "selected-token"})

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth() error = %v", err)
		}

		expectedPath := filepath.Join(prof.XDGConfigPath(), "claude-code", "auth.json")
		if copied[0] != expectedPath {
			t.Errorf("Copied path = %q, want %q", copied[0], expectedPath)
		}
	})

	t.Run("fails if source missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{Name: "test", BasePath: tmpDir}
		p := New()

		_, err := p.ImportAuth(context.Background(), "/non/existent/file", prof)
		if err == nil {
			t.Error("Should fail for missing source")
		}
	})

	t.Run("fails if source is directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{Name: "test", BasePath: tmpDir}
		p := New()

		_, err := p.ImportAuth(context.Background(), tmpDir, prof)
		if err == nil {
			t.Error("Should fail if source is directory")
		}
	})
}

// Helper for writing JSON
func writeJSON(t *testing.T, path string, data interface{}) {
	t.Helper()
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
}

// =============================================================================
// Credential Precedence Tests (issue #72)
// =============================================================================

// writeCredentialsFileAt writes a Claude .credentials.json at path with the
// given expiresAt (epoch millis). expiresAtMillis <= 0 omits the field.
func writeCredentialsFileAt(t *testing.T, path string, expiresAtMillis int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	oauth := map[string]interface{}{
		"accessToken": "sk-test",
	}
	if expiresAtMillis > 0 {
		oauth["expiresAt"] = expiresAtMillis
	}
	data, err := json.Marshal(map[string]interface{}{"claudeAiOauth": oauth})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

// TestValidateTokenPassive_CredentialPrecedence is a regression test for
// issue #72: a stale (expired or corrupt) legacy home/.claude/ credentials
// file must not shadow XDG-side credentials. Validation must describe the
// selected native store even when ignored legacy credentials are fresher.
func TestValidateTokenPassive_CredentialPrecedence(t *testing.T) {
	newProf := func(t *testing.T) *profile.Profile {
		return &profile.Profile{
			Name:     "precedence",
			Provider: "claude",
			BasePath: t.TempDir(),
		}
	}
	legacyPath := func(prof *profile.Profile) string {
		return filepath.Join(prof.HomePath(), ".claude", ".credentials.json")
	}
	xdgPath := func(prof *profile.Profile) string {
		return filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json")
	}
	p := New()
	now := time.Now()
	past := now.Add(-2 * time.Hour).UnixMilli()
	future := now.Add(4 * time.Hour).UnixMilli()
	fresher := now.Add(8 * time.Hour).UnixMilli()

	t.Run("expired legacy falls through to fresh XDG", func(t *testing.T) {
		prof := newProf(t)
		writeCredentialsFileAt(t, legacyPath(prof), past)
		writeCredentialsFileAt(t, xdgPath(prof), future)

		result, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if !result.Valid {
			t.Fatalf("expired legacy file should not shadow fresh XDG credentials (issue #72); got error: %s", result.Error)
		}
		if got := result.ExpiresAt.UnixMilli(); got != future {
			t.Errorf("ExpiresAt = %d, want XDG-side expiry %d", got, future)
		}
	})

	t.Run("corrupt legacy falls through to fresh XDG", func(t *testing.T) {
		prof := newProf(t)
		if err := os.MkdirAll(filepath.Dir(legacyPath(prof)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyPath(prof), []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
		writeCredentialsFileAt(t, xdgPath(prof), future)

		result, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if !result.Valid {
			t.Fatalf("corrupt legacy file should not shadow valid XDG credentials (issue #72); got error: %s", result.Error)
		}
	})

	t.Run("both valid: fresher XDG expiry wins", func(t *testing.T) {
		prof := newProf(t)
		writeCredentialsFileAt(t, legacyPath(prof), future)
		writeCredentialsFileAt(t, xdgPath(prof), fresher)

		result, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if !result.Valid {
			t.Fatalf("expected valid; got error: %s", result.Error)
		}
		if got := result.ExpiresAt.UnixMilli(); got != fresher {
			t.Errorf("ExpiresAt = %d, want fresher XDG expiry %d", got, fresher)
		}
	})

	t.Run("both valid: XDG remains authoritative despite fresher legacy", func(t *testing.T) {
		prof := newProf(t)
		writeCredentialsFileAt(t, legacyPath(prof), fresher)
		writeCredentialsFileAt(t, xdgPath(prof), future)

		result, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if !result.Valid {
			t.Fatalf("expected valid; got error: %s", result.Error)
		}
		if got := result.ExpiresAt.UnixMilli(); got != future {
			t.Errorf("ExpiresAt = %d, want native XDG expiry %d", got, future)
		}
	})

	t.Run("both expired reports the fresher expiry", func(t *testing.T) {
		prof := newProf(t)
		older := now.Add(-4 * time.Hour).UnixMilli()
		writeCredentialsFileAt(t, legacyPath(prof), older)
		writeCredentialsFileAt(t, xdgPath(prof), past)

		result, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if result.Valid {
			t.Fatal("expected invalid when both credential files are expired")
		}
		if result.Error != "token has expired" {
			t.Errorf("Error = %q, want %q", result.Error, "token has expired")
		}
		if got := result.ExpiresAt.UnixMilli(); got != past {
			t.Errorf("ExpiresAt = %d, want fresher (least stale) expiry %d", got, past)
		}
	})

	t.Run("corrupt legacy alone still reports parse error", func(t *testing.T) {
		prof := newProf(t)
		if err := os.MkdirAll(filepath.Dir(legacyPath(prof)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(legacyPath(prof), []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}

		result, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if result.Valid {
			t.Fatal("expected invalid for a lone corrupt credentials file")
		}
		if !strings.Contains(result.Error, "invalid .credentials.json") {
			t.Errorf("Error = %q, want invalid .credentials.json parse error", result.Error)
		}
	})
}

func claudeLifecycleFixture(t *testing.T) (string, *profile.Profile) {
	t.Helper()
	realHome := t.TempDir()
	t.Setenv("HOME", realHome)
	t.Setenv("USERPROFILE", realHome)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(realHome, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CAAM_KEYCHAIN", "0")
	return realHome, &profile.Profile{Name: "isolated", Provider: "claude", AuthMode: "oauth", BasePath: t.TempDir()}
}

func writeClaudeLifecycleJSON(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func readClaudeLifecycleJSON(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var obj map[string]interface{}
	if err := json.Unmarshal(data, &obj); err != nil {
		t.Fatal(err)
	}
	return obj
}

func TestIsolatedClaudeSharedSettingsLifecycle(t *testing.T) {
	realHome, prof := claudeLifecycleFixture(t)
	shared := filepath.Join(realHome, ".claude", "settings.json")
	writeClaudeLifecycleJSON(t, shared, `{"permissions":{"allow":["Read"]},"model":"initial","hooks":{"Stop":[]},"apiKeyHelper":"host-secret","env":{"ANTHROPIC_API_KEY":"host-key"}}`)
	writeClaudeLifecycleJSON(t, filepath.Join(realHome, ".claude.json"), `{"oauthAccount":{"accountUuid":"host"},"mcpServers":{"current":{}}}`)
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	for _, path := range claudeSettingsPathsForProfile(prof) {
		got := readClaudeLifecycleJSON(t, path)
		if got["model"] != "initial" || got["permissions"] == nil || got["apiKeyHelper"] != nil || got["env"] != nil {
			t.Fatalf("new profile lost policy or copied host auth: %#v", got)
		}
	}
	statePath := filepath.Join(claudeConfigDirForProfile(prof), ".claude.json")
	if got := readClaudeLifecycleJSON(t, statePath); got["oauthAccount"] != nil || got["mcpServers"] == nil {
		t.Fatalf("new native state copied the host account or lost MCP policy: %#v", got)
	}
	settingsPath := filepath.Join(claudeConfigDirForProfile(prof), "settings.json")
	writeClaudeLifecycleJSON(t, settingsPath, `{"apiKeyHelper":"profile-helper","env":{"ANTHROPIC_API_KEY":"profile-key"},"model":"old","hooks":{"Stop":[]}}`)
	writeClaudeLifecycleJSON(t, shared, `{"permissions":{"allow":[]},"model":"current","apiKeyHelper":"other-host-secret"}`)
	beforeHost, _ := os.ReadFile(shared)
	if err := p.PrepareRun(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	for _, path := range claudeSettingsPathsForProfile(prof) {
		got := readClaudeLifecycleJSON(t, path)
		if got["model"] != "current" || got["hooks"] != nil || got["permissions"] == nil {
			t.Fatalf("launch retained stale policy or revived a deleted hook: %#v", got)
		}
	}
	got := readClaudeLifecycleJSON(t, settingsPath)
	if got["apiKeyHelper"] != "profile-helper" || got["env"].(map[string]interface{})["ANTHROPIC_API_KEY"] != "profile-key" {
		t.Fatalf("refresh changed the selected account: %#v", got)
	}
	afterHost, _ := os.ReadFile(shared)
	if !bytes.Equal(beforeHost, afterHost) {
		t.Fatal("profile refresh changed canonical host settings")
	}
}

func TestIsolatedClaudeExplicitSharedConfigDirectory(t *testing.T) {
	for _, canonicalExists := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing canonical source", true: "canonical source present"}[canonicalExists], func(t *testing.T) {
			realHome, prof := claudeLifecycleFixture(t)
			writeClaudeLifecycleJSON(t, filepath.Join(realHome, ".claude", "settings.json"), `{"model":"ignored-legacy","apiKeyHelper":"ignored-secret"}`)
			explicit := t.TempDir()
			t.Setenv("CLAUDE_CONFIG_DIR", explicit)
			want := "profile-policy"
			if canonicalExists {
				writeClaudeLifecycleJSON(t, filepath.Join(explicit, "settings.json"), `{"model":"canonical","apiKeyHelper":"host-secret"}`)
				want = "canonical"
			}
			path := filepath.Join(claudeConfigDirForProfile(prof), "settings.json")
			writeClaudeLifecycleJSON(t, path, `{"model":"profile-policy","apiKeyHelper":"profile-helper"}`)
			if err := New().PrepareRun(context.Background(), prof); err != nil {
				t.Fatal(err)
			}
			got := readClaudeLifecycleJSON(t, path)
			if got["model"] != want || got["apiKeyHelper"] != "profile-helper" {
				t.Fatalf("explicit canonical directory was ignored: %#v", got)
			}
		})
	}
}

func TestIsolatedClaudePreparationFailsBeforeChangingSettings(t *testing.T) {
	realHome, prof := claudeLifecycleFixture(t)
	shared := filepath.Join(realHome, ".claude", "settings.json")
	writeClaudeLifecycleJSON(t, shared, `{"model":"new"}`)
	legacy := filepath.Join(claudeLegacyDirForProfile(prof), "settings.json")
	writeClaudeLifecycleJSON(t, legacy, `{"model":"old"}`)
	xdg := filepath.Join(claudeConfigDirForProfile(prof), "settings.json")
	writeClaudeLifecycleJSON(t, xdg, `{broken`)
	before, _ := os.ReadFile(legacy)
	if err := New().PrepareRun(context.Background(), prof); err == nil {
		t.Fatal("malformed destination settings allowed launch preparation")
	}
	after, _ := os.ReadFile(legacy)
	if !bytes.Equal(before, after) {
		t.Fatal("preflight failure partially changed the other settings destination")
	}
}

func TestIsolatedClaudeAPIHelperMergesPolicy(t *testing.T) {
	_, prof := claudeLifecycleFixture(t)
	path := filepath.Join(claudeConfigDirForProfile(prof), "settings.json")
	writeClaudeLifecycleJSON(t, path, `{"permissions":{"allow":["Read"]},"model":"kept","hooks":{"Stop":[]},"env":{"EDITOR":"vim"},"apiKeyHelper":"old"}`)
	if err := New().setupAPIKeyHelper(prof); err != nil {
		t.Fatal(err)
	}
	got := readClaudeLifecycleJSON(t, path)
	if got["apiKeyHelper"] != filepath.Join(prof.BasePath, "api_key_helper.sh") || got["model"] != "kept" || got["permissions"] == nil || got["hooks"] == nil || got["env"] == nil {
		t.Fatalf("API enrollment truncated workflow policy: %#v", got)
	}
}

func TestIsolatedClaudeImportUsesSelectedNativeStoreAndLivePolicy(t *testing.T) {
	realHome, prof := claudeLifecycleFixture(t)
	writeClaudeLifecycleJSON(t, filepath.Join(realHome, ".claude", "settings.json"), `{"model":"current","permissions":{"allow":["Read"]},"apiKeyHelper":"host"}`)
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "settings.json")
	writeClaudeLifecycleJSON(t, source, `{"model":"stale","apiKeyHelper":"selected-account","env":{"ANTHROPIC_API_KEY":"selected-key"}}`)
	paths, err := p.ImportAuth(context.Background(), source, prof)
	if err != nil {
		t.Fatal(err)
	}
	env, err := p.Env(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(env["CLAUDE_CONFIG_DIR"], "settings.json")
	if len(paths) != 1 || paths[0] != wantPath {
		t.Fatalf("import destinations %v do not match native config %s", paths, wantPath)
	}
	got := readClaudeLifecycleJSON(t, wantPath)
	if got["model"] != "current" || got["permissions"] == nil || got["apiKeyHelper"] != "selected-account" {
		t.Fatalf("import reverted policy or selected another account: %#v", got)
	}
	ignored := readClaudeLifecycleJSON(t, filepath.Join(claudeLegacyDirForProfile(prof), "settings.json"))
	if ignored["apiKeyHelper"] != nil || ignored["env"] != nil {
		t.Fatalf("import duplicated authentication into the ignored store: %#v", ignored)
	}
}

func TestIsolatedClaudeLogoutRetainsPolicyWithoutLogin(t *testing.T) {
	_, prof := claudeLifecycleFixture(t)
	prof.AuthMode = "api-key"
	for _, path := range claudeSettingsPathsForProfile(prof) {
		writeClaudeLifecycleJSON(t, path, `{"apiKeyHelper":"account","env":{"ANTHROPIC_API_KEY":"key"},"model":"kept","permissions":{"allow":["Read"]}}`)
	}
	statePath := filepath.Join(claudeConfigDirForProfile(prof), ".claude.json")
	writeClaudeLifecycleJSON(t, statePath, `{"oauthAccount":{"accountUuid":"account"},"oauthToken":"token","mcpServers":{"kept":{}}}`)
	writeCredentialsFileAt(t, claudeXDGCredentialsPathForProfile(prof), time.Now().Add(time.Hour).UnixMilli())
	p := New()
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	for _, path := range claudeSettingsPathsForProfile(prof) {
		got := readClaudeLifecycleJSON(t, path)
		if got["model"] != "kept" || got["permissions"] == nil || got["apiKeyHelper"] != nil || got["env"] != nil {
			t.Fatalf("logout lost policy or retained account settings: %#v", got)
		}
	}
	if got := readClaudeLifecycleJSON(t, statePath); got["mcpServers"] == nil || got["oauthToken"] != nil || got["oauthAccount"] != nil {
		t.Fatalf("logout mishandled native state: %#v", got)
	}
	status, err := p.Status(context.Background(), prof)
	if err != nil || status.LoggedIn {
		t.Fatalf("policy was reported as a login: %+v, %v", status, err)
	}
	validation, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil || validation.Valid {
		t.Fatalf("policy validated as credentials: %+v, %v", validation, err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfigDirForProfile(prof))
	detection, err := p.DetectExistingAuth()
	if err != nil || detection.Found {
		t.Fatalf("retained native settings were discovered as auth: %+v, %v", detection, err)
	}
}

func TestIsolatedClaudePerProfilePolicyAndPureEnv(t *testing.T) {
	realHome, prof := claudeLifecycleFixture(t)
	writeClaudeLifecycleJSON(t, claudesettings.CAAMConfigPath(), `{"claude_settings":{"mode":"per-profile"}}`)
	writeClaudeLifecycleJSON(t, filepath.Join(realHome, ".claude", "settings.json"), `{"model":"host"}`)
	if _, err := New().Env(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(prof.HomePath()); !os.IsNotExist(err) {
		t.Fatalf("Env mutated the profile: %v", err)
	}
	for _, path := range claudeSettingsPathsForProfile(prof) {
		writeClaudeLifecycleJSON(t, path, `{"model":"own","apiKeyHelper":"own-helper"}`)
	}
	if err := New().PrepareRun(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	for _, path := range claudeSettingsPathsForProfile(prof) {
		if got := readClaudeLifecycleJSON(t, path); got["model"] != "own" || got["apiKeyHelper"] != "own-helper" {
			t.Fatalf("per-profile policy was overwritten: %#v", got)
		}
	}
}

func TestIsolatedClaudeNativeReaderAgreesWithValidation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("synthetic native reader uses a POSIX shell")
	}
	for _, scenario := range []string{"legacy-only", "XDG authoritative", "expired XDG", "malformed XDG"} {
		t.Run(scenario, func(t *testing.T) {
			_, prof := claudeLifecycleFixture(t)
			legacy := filepath.Join(claudeLegacyDirForProfile(prof), ".credentials.json")
			xdg := claudeXDGCredentialsPathForProfile(prof)
			writeCredentialsFileAt(t, legacy, time.Now().Add(8*time.Hour).UnixMilli())
			selected := legacy
			if scenario != "legacy-only" {
				selected = xdg
				expiry := time.Now().Add(time.Hour)
				if scenario == "expired XDG" {
					expiry = time.Now().Add(-time.Hour)
				}
				writeCredentialsFileAt(t, xdg, expiry.UnixMilli())
				if scenario == "malformed XDG" {
					writeClaudeLifecycleJSON(t, xdg, `{broken`)
				}
			}
			p := New()
			if err := p.PrepareRun(context.Background(), prof); err != nil {
				t.Fatal(err)
			}
			env, err := p.Env(context.Background(), prof)
			if err != nil {
				t.Fatal(err)
			}
			if env["CLAUDE_CONFIG_DIR"] != filepath.Dir(selected) {
				t.Fatalf("native config = %q, selected path = %q", env["CLAUDE_CONFIG_DIR"], selected)
			}
			command := exec.Command("sh", "-c", `cat "$CLAUDE_CONFIG_DIR/.credentials.json"`)
			command.Env = os.Environ()
			for key, value := range env {
				command.Env = append(command.Env, key+"="+value)
			}
			read, err := command.Output()
			if err != nil {
				t.Fatal(err)
			}
			want, _ := os.ReadFile(selected)
			if !bytes.Equal(read, want) {
				t.Fatal("synthetic native CLI read another credential source")
			}
			validation, err := p.ValidateToken(context.Background(), prof, true)
			wantValid := scenario == "legacy-only" || scenario == "XDG authoritative"
			if err != nil || validation.Valid != wantValid {
				t.Fatalf("native-source validation mismatch: %+v, %v", validation, err)
			}
			if scenario == "XDG authoritative" {
				var token struct {
					OAuth struct {
						Expiry int64 `json:"expiresAt"`
					} `json:"claudeAiOauth"`
				}
				if err := json.Unmarshal(read, &token); err != nil {
					t.Fatal(err)
				}
				if validation.ExpiresAt.UnixMilli() != token.OAuth.Expiry {
					t.Fatal("validation borrowed the ignored legacy token's later expiry")
				}
				ignoredBefore, _ := os.ReadFile(legacy)
				source := filepath.Join(t.TempDir(), ".credentials.json")
				writeCredentialsFileAt(t, source, time.Now().Add(2*time.Hour).UnixMilli())
				imported, err := p.ImportAuth(context.Background(), source, prof)
				if err != nil || len(imported) != 1 || imported[0] != selected {
					t.Fatalf("credential import did not target only the native store: %v, %v", imported, err)
				}
				ignoredAfter, _ := os.ReadFile(legacy)
				if !bytes.Equal(ignoredBefore, ignoredAfter) {
					t.Fatal("credential import duplicated a generation into the ignored store")
				}
				reader := exec.Command("sh", "-c", `cat "$CLAUDE_CONFIG_DIR/.credentials.json"`)
				reader.Env = command.Env
				read, err = reader.Output()
				want, _ = os.ReadFile(source)
				if err != nil || !bytes.Equal(read, want) {
					t.Fatalf("native reader did not see imported credentials: %v", err)
				}
			}
		})
	}
}

func TestIsolatedClaudeRejectsKnownConflictingAccounts(t *testing.T) {
	_, prof := claudeLifecycleFixture(t)
	writeClaudeLifecycleJSON(t, filepath.Join(claudeLegacyDirForProfile(prof), ".credentials.json"), `{"claudeAiOauth":{"accessToken":"legacy","accountId":"alice"}}`)
	writeClaudeLifecycleJSON(t, claudeXDGCredentialsPathForProfile(prof), `{"claudeAiOauth":{"accessToken":"xdg","accountId":"bob"}}`)
	p := New()
	if _, err := p.Env(context.Background(), prof); err == nil {
		t.Fatal("conflicting accounts were assigned a launch environment")
	}
	if err := p.PrepareRun(context.Background(), prof); err == nil {
		t.Fatal("conflicting accounts passed launch preparation")
	}
	if _, err := p.Status(context.Background(), prof); err == nil {
		t.Fatal("conflicting accounts were reported as one profile")
	}
	validation, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil || validation.Valid || !strings.Contains(validation.Error, "conflicting accounts") {
		t.Fatalf("conflict was not retained in validation: %+v, %v", validation, err)
	}
}

func TestIsolatedClaudeLegacyStateMovesToSelectedNativeLocation(t *testing.T) {
	realHome, prof := claudeLifecycleFixture(t)
	writeClaudeLifecycleJSON(t, filepath.Join(realHome, ".claude.json"), `{"oauthAccount":{"accountUuid":"host"},"mcpServers":{"current":{}}}`)
	oldState := filepath.Join(prof.HomePath(), ".claude.json")
	writeClaudeLifecycleJSON(t, oldState, `{"oauthToken":"profile-token","oauthAccount":{"accountUuid":"profile"},"mcpServers":{"old":{}}}`)
	p := New()
	if err := p.PrepareRun(context.Background(), prof); err != nil {
		t.Fatal(err)
	}
	env, err := p.Env(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	if env["CLAUDE_CONFIG_DIR"] != claudeLegacyDirForProfile(prof) {
		t.Fatal("legacy-only profile unexpectedly selected XDG")
	}
	state := readClaudeLifecycleJSON(t, filepath.Join(env["CLAUDE_CONFIG_DIR"], ".claude.json"))
	if state["oauthToken"] != "profile-token" || state["oauthAccount"].(map[string]interface{})["accountUuid"] != "profile" || state["mcpServers"].(map[string]interface{})["current"] == nil {
		t.Fatalf("native state lost its account or current shared policy: %#v", state)
	}
}

func TestIsolatedClaudeImportRejectsPolicyOnlyAndNewAccountConflict(t *testing.T) {
	_, prof := claudeLifecycleFixture(t)
	sourceDir := t.TempDir()
	settingsSource := filepath.Join(sourceDir, "settings.json")
	writeClaudeLifecycleJSON(t, settingsSource, `{"model":"policy-only"}`)
	p := New()
	if _, err := p.ImportAuth(context.Background(), settingsSource, prof); err == nil {
		t.Fatal("policy-only settings imported as a login")
	}
	legacy := filepath.Join(claudeLegacyDirForProfile(prof), ".credentials.json")
	xdg := claudeXDGCredentialsPathForProfile(prof)
	for _, path := range []string{legacy, xdg} {
		writeClaudeLifecycleJSON(t, path, `{"claudeAiOauth":{"accessToken":"current","accountId":"alice"}}`)
	}
	source := filepath.Join(sourceDir, ".credentials.json")
	writeClaudeLifecycleJSON(t, source, `{"claudeAiOauth":{"accessToken":"other","accountId":"bob"}}`)
	before, _ := os.ReadFile(xdg)
	if _, err := p.ImportAuth(context.Background(), source, prof); err == nil {
		t.Fatal("import introduced known conflicting account stores")
	}
	after, _ := os.ReadFile(xdg)
	if !bytes.Equal(before, after) {
		t.Fatal("rejected import changed the selected native credential")
	}
}
