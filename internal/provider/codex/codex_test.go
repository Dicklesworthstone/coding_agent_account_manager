package codex

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

func TestLoginNativeEnvironmentHonorsSelectedProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture; Windows environment merging is tested in provider")
	}
	for _, mode := range []provider.AuthMode{provider.AuthModeOAuth, provider.AuthModeDeviceCode, provider.AuthModeAPIKey} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			prof := &profile.Profile{Name: "selected", Provider: "codex", AuthMode: string(mode), BasePath: filepath.Join(root, "profile")}
			binDir := filepath.Join(root, "bin")
			if err := os.MkdirAll(binDir, 0700); err != nil {
				t.Fatal(err)
			}
			script := `#!/bin/sh
test "$HOME" = "$CAAM_TEST_SELECTED_HOME" || exit 11
test "$CODEX_HOME" = "$CAAM_TEST_SELECTED_CODEX_HOME" || exit 12
test "$ANTHROPIC_API_KEY" = unrelated || exit 13
if [ "$2" = --with-api-key ]; then
  test "$(cat)" = synthetic-api-input || exit 14
else
  test -z "$OPENAI_API_KEY$CODEX_API_KEY" || exit 15
fi
printf success > "$CAAM_TEST_LOGIN_MARKER"
`
			if err := os.WriteFile(filepath.Join(binDir, "codex"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("OPENAI_API_KEY", "synthetic-api-input")
			t.Setenv("CODEX_API_KEY", "synthetic-other-input")
			t.Setenv("ANTHROPIC_API_KEY", "unrelated")
			t.Setenv("CODEX_HOME", filepath.Join(root, "ignored-host"))
			t.Setenv("CAAM_TEST_SELECTED_HOME", prof.HomePath())
			t.Setenv("CAAM_TEST_SELECTED_CODEX_HOME", prof.CodexHomePath())
			marker := filepath.Join(root, "login-ran")
			t.Setenv("CAAM_TEST_LOGIN_MARKER", marker)
			if err := New().Login(context.Background(), prof); err != nil {
				t.Fatal(err)
			}
			if content, err := os.ReadFile(marker); err != nil || string(content) != "success" {
				t.Fatalf("native login did not use selected environment: %q, %v", content, err)
			}
		})
	}
}

func TestImportedAPIKeyOwnsMergedCodexEnvironment(t *testing.T) {
	for _, alias := range []string{"OPENAI_API_KEY", "api_key", "apiKey"} {
		t.Run(alias, func(t *testing.T) {
			root := t.TempDir()
			prof := &profile.Profile{Name: "selected", Provider: "codex", AuthMode: "oauth", BasePath: filepath.Join(root, "profile")}
			source := filepath.Join(root, "selected-auth.json")
			data, err := json.Marshal(map[string]string{alias: "synthetic-saved-key"})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, data, 0600); err != nil {
				t.Fatal(err)
			}
			p := New()
			if _, err := p.ImportAuth(context.Background(), source, prof); err != nil {
				t.Fatal(err)
			}
			changes, err := provider.ProfileEnvironment(context.Background(), p, prof)
			if err != nil {
				t.Fatal(err)
			}
			ambient := []string{"OPENAI_API_KEY=synthetic-other-interactive", "CODEX_API_KEY=synthetic-other-exec", "ANTHROPIC_API_KEY=unrelated"}
			merged := provider.MergeEnvironment(ambient, changes, nil)
			if !slices.Contains(merged, "OPENAI_API_KEY=synthetic-saved-key") || !slices.Contains(merged, "CODEX_API_KEY=synthetic-saved-key") {
				t.Fatal("imported key did not take precedence over both native ambient inputs")
			}
			if !slices.Contains(merged, "ANTHROPIC_API_KEY=unrelated") {
				t.Fatal("unrelated provider key was removed")
			}
			explicit := provider.MergeEnvironment(ambient, changes, map[string]string{"CODEX_API_KEY": "synthetic-explicit"})
			if !slices.Contains(explicit, "CODEX_API_KEY=synthetic-explicit") {
				t.Fatal("explicit caller credential lost its documented priority")
			}
			stored, err := os.ReadFile(filepath.Join(prof.CodexHomePath(), "auth.json"))
			if err != nil || !bytes.Equal(stored, data) {
				t.Fatal("environment resolution changed the imported credential")
			}
		})
	}

	t.Run("new profile retains ambient enrollment input", func(t *testing.T) {
		prof := &profile.Profile{Name: "new", Provider: "codex", AuthMode: "api-key", BasePath: t.TempDir()}
		changes, err := provider.ProfileEnvironment(context.Background(), New(), prof)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(provider.MergeEnvironment([]string{"OPENAI_API_KEY=synthetic-enrollment"}, changes, nil), "OPENAI_API_KEY=synthetic-enrollment") {
			t.Fatal("new profile lost its ambient enrollment key")
		}
	})

	t.Run("native OAuth login supersedes API-key metadata", func(t *testing.T) {
		prof := &profile.Profile{Name: "changed", Provider: "codex", AuthMode: "api-key", BasePath: t.TempDir()}
		if err := os.MkdirAll(prof.CodexHomePath(), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prof.CodexHomePath(), "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"synthetic-oauth","refresh_token":"synthetic-renewal"}}`), 0600); err != nil {
			t.Fatal(err)
		}
		changes, err := provider.ProfileEnvironment(context.Background(), New(), prof)
		if err != nil {
			t.Fatal(err)
		}
		ambient := []string{"OPENAI_API_KEY=synthetic-other", "CODEX_API_KEY=synthetic-other", "OPENAI_BASE_URL=https://other.invalid"}
		merged := provider.MergeEnvironment(ambient, changes, nil)
		for _, key := range []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"} {
			if !slices.Contains(merged, key+"=") {
				t.Fatalf("saved OAuth grant retained inherited override %s", key)
			}
		}
		if !slices.Contains(provider.MergeEnvironment(ambient, changes, map[string]string{"CODEX_API_KEY": "synthetic-explicit"}), "CODEX_API_KEY=synthetic-explicit") {
			t.Fatal("explicit caller credential lost priority over native OAuth selection")
		}
	})

	t.Run("invalid saved credential does not fall back to ambient account", func(t *testing.T) {
		prof := &profile.Profile{Name: "invalid", Provider: "codex", AuthMode: "api-key", BasePath: t.TempDir()}
		if err := os.MkdirAll(prof.CodexHomePath(), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(prof.CodexHomePath(), "auth.json"), []byte(`{"OPENAI_API_KEY":42}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.ProfileEnvironment(context.Background(), New(), prof); err == nil {
			t.Fatal("malformed selected key was ignored")
		}
	})
}

// =============================================================================
// Provider Factory Tests
// =============================================================================

func TestNew(t *testing.T) {
	p := New()
	if p == nil {
		t.Fatal("New() returned nil")
	}
}

func TestReadAPIKeyFromStdin_NonTTY(t *testing.T) {
	tmpDir := t.TempDir()
	keyPath := filepath.Join(tmpDir, "key.txt")
	if err := os.WriteFile(keyPath, []byte("sk-test-123\n"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	f, err := os.Open(keyPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()

	key, hidden, err := readAPIKeyFromStdin(f)
	if err != nil {
		t.Fatalf("readAPIKeyFromStdin: %v", err)
	}
	if hidden {
		t.Fatal("expected non-tty read to not be hidden")
	}
	if key != "sk-test-123" {
		t.Fatalf("key=%q, want %q", key, "sk-test-123")
	}
}

// =============================================================================
// Provider Identity Tests
// =============================================================================

func TestProviderID(t *testing.T) {
	p := New()
	if p.ID() != "codex" {
		t.Errorf("ID() = %q, want %q", p.ID(), "codex")
	}
}

func TestProviderDisplayName(t *testing.T) {
	p := New()
	expected := "Codex CLI (OpenAI GPT Pro)"
	if p.DisplayName() != expected {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), expected)
	}
}

func TestProviderDefaultBin(t *testing.T) {
	p := New()
	if p.DefaultBin() != "codex" {
		t.Errorf("DefaultBin() = %q, want %q", p.DefaultBin(), "codex")
	}
}

// =============================================================================
// Auth Mode Tests
// =============================================================================

func TestSupportedAuthModes(t *testing.T) {
	p := New()
	modes := p.SupportedAuthModes()

	if len(modes) != 3 {
		t.Fatalf("SupportedAuthModes() returned %d modes, want 3", len(modes))
	}

	hasOAuth := false
	hasDeviceCode := false
	hasAPIKey := false
	for _, mode := range modes {
		if mode == provider.AuthModeOAuth {
			hasOAuth = true
		}
		if mode == provider.AuthModeDeviceCode {
			hasDeviceCode = true
		}
		if mode == provider.AuthModeAPIKey {
			hasAPIKey = true
		}
	}

	if !hasOAuth {
		t.Error("SupportedAuthModes() should include OAuth")
	}
	if !hasDeviceCode {
		t.Error("SupportedAuthModes() should include DeviceCode")
	}
	if !hasAPIKey {
		t.Error("SupportedAuthModes() should include APIKey")
	}
}

// =============================================================================
// Auth Files Tests
// =============================================================================

func TestAuthFiles(t *testing.T) {
	t.Run("returns auth.json spec", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		if len(files) != 1 {
			t.Fatalf("AuthFiles() returned %d files, want 1", len(files))
		}

		file := files[0]
		if !filepath.IsAbs(file.Path) {
			// May not be absolute if HOME is set oddly, just check it ends with auth.json
			if filepath.Base(file.Path) != "auth.json" {
				t.Errorf("AuthFiles()[0].Path = %q, should end with auth.json", file.Path)
			}
		}
		if !file.Required {
			t.Error("auth.json should be required")
		}
	})

	t.Run("uses CODEX_HOME if set", func(t *testing.T) {
		originalHome := os.Getenv("CODEX_HOME")
		defer os.Setenv("CODEX_HOME", originalHome)

		os.Setenv("CODEX_HOME", "/custom/codex/home")
		p := New()
		files := p.AuthFiles()

		if len(files) != 1 {
			t.Fatal("expected 1 auth file")
		}
		expected := "/custom/codex/home/auth.json"
		if files[0].Path != expected {
			t.Errorf("AuthFiles()[0].Path = %q, want %q", files[0].Path, expected)
		}
	})

	t.Run("uses default .codex if CODEX_HOME not set", func(t *testing.T) {
		originalHome := os.Getenv("CODEX_HOME")
		defer os.Setenv("CODEX_HOME", originalHome)

		os.Unsetenv("CODEX_HOME")
		p := New()
		files := p.AuthFiles()

		if len(files) != 1 {
			t.Fatal("expected 1 auth file")
		}
		homeDir, _ := os.UserHomeDir()
		expected := filepath.Join(homeDir, ".codex", "auth.json")
		if files[0].Path != expected {
			t.Errorf("AuthFiles()[0].Path = %q, want %q", files[0].Path, expected)
		}
	})
}

// =============================================================================
// PrepareProfile Tests
// =============================================================================

func TestPrepareProfile(t *testing.T) {
	t.Run("creates directories", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		if err := p.PrepareProfile(context.Background(), prof); err != nil {
			t.Fatalf("PrepareProfile() error = %v", err)
		}

		// Check codex_home
		codexHomePath := prof.CodexHomePath()
		info, err := os.Stat(codexHomePath)
		if err != nil {
			t.Fatalf("codex_home not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("codex_home should be a directory")
		}

		// Check home (pseudo-home for passthroughs)
		homePath := prof.HomePath()
		info, err = os.Stat(homePath)
		if err != nil {
			t.Fatalf("home not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("home should be a directory")
		}
	})

	t.Run("sets secure permissions", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		codexHomePath := prof.CodexHomePath()
		info, err := os.Stat(codexHomePath)
		if err != nil {
			t.Fatal(err)
		}

		// Should be 0700 (user only)
		if info.Mode().Perm() != 0700 {
			t.Errorf("codex_home permissions = %o, want 0700", info.Mode().Perm())
		}
	})

	t.Run("idempotent - can be called multiple times", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
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
// Env Tests
// =============================================================================

func TestEnv(t *testing.T) {
	t.Run("sets env vars", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		env, err := p.Env(context.Background(), prof)
		if err != nil {
			t.Fatalf("Env() error = %v", err)
		}

		if len(env) != 2 {
			t.Errorf("Env() returned %d vars, want 2", len(env))
		}

		// Check CODEX_HOME
		codexHome, ok := env["CODEX_HOME"]
		if !ok {
			t.Error("CODEX_HOME not set in env")
		}
		expectedCodexHome := prof.CodexHomePath()
		if codexHome != expectedCodexHome {
			t.Errorf("CODEX_HOME = %q, want %q", codexHome, expectedCodexHome)
		}

		// Check HOME
		home, ok := env["HOME"]
		if !ok {
			t.Error("HOME not set in env")
		}
		expectedHome := prof.HomePath()
		if home != expectedHome {
			t.Errorf("HOME = %q, want %q", home, expectedHome)
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
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create auth.json
		authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
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

	t.Run("handles non-existent auth.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Don't create auth.json, just logout
		if err := p.Logout(context.Background(), prof); err != nil {
			t.Errorf("Logout() error = %v, should handle missing file", err)
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
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create auth.json
		authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
		if err := os.WriteFile(authPath, []byte(`{}`), 0600); err != nil {
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

	t.Run("not logged in when auth.json missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.LoggedIn {
			t.Error("LoggedIn should be false when auth.json missing")
		}
	})

	t.Run("reports lock file status", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()

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
	t.Run("valid when codex_home exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		if err := p.ValidateProfile(context.Background(), prof); err != nil {
			t.Errorf("ValidateProfile() error = %v", err)
		}
	})

	t.Run("invalid when codex_home missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}

		p := New()
		// Don't call PrepareProfile

		err := p.ValidateProfile(context.Background(), prof)
		if err == nil {
			t.Error("ValidateProfile() should error when codex_home missing")
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
// codexHome Helper Tests (via AuthFiles)
// =============================================================================

func TestCodexHomeHelper(t *testing.T) {
	// Test CODEX_HOME environment variable override
	originalHome := os.Getenv("CODEX_HOME")
	defer os.Setenv("CODEX_HOME", originalHome)

	t.Run("respects CODEX_HOME env var", func(t *testing.T) {
		os.Setenv("CODEX_HOME", "/test/codex")
		p := New()
		files := p.AuthFiles()
		if !hasPrefix(files[0].Path, "/test/codex") {
			t.Errorf("Path %q should use CODEX_HOME=/test/codex", files[0].Path)
		}
	})

	t.Run("falls back to ~/.codex", func(t *testing.T) {
		os.Unsetenv("CODEX_HOME")
		p := New()
		files := p.AuthFiles()
		homeDir, _ := os.UserHomeDir()
		expected := filepath.Join(homeDir, ".codex")
		if !hasPrefix(files[0].Path, expected) {
			t.Errorf("Path %q should use %s", files[0].Path, expected)
		}
	})
}

// hasPrefix checks if path starts with prefix.
func hasPrefix(path, prefix string) bool {
	return len(path) >= len(prefix) && path[:len(prefix)] == prefix
}

// =============================================================================
// Integration Test
// =============================================================================

func TestFullProfileLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "lifecycle-test",
		Provider: "codex",
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

	// Simulate login by creating auth.json
	authPath := filepath.Join(prof.CodexHomePath(), "auth.json")
	os.WriteFile(authPath, []byte(`{"token":"test"}`), 0600)

	// Status (now logged in)
	status, _ = p.Status(context.Background(), prof)
	if !status.LoggedIn {
		t.Error("should be logged in after auth.json created")
	}

	// Get env
	env, _ := p.Env(context.Background(), prof)
	if env["CODEX_HOME"] == "" {
		t.Error("CODEX_HOME should be set")
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
// DetectExistingAuth Tests
// =============================================================================

func TestDetectExistingAuth(t *testing.T) {
	setupEnv := func(t *testing.T) string {
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("CODEX_HOME", "")
		return home
	}

	t.Run("detects ~/.codex/auth.json (default)", func(t *testing.T) {
		home := setupEnv(t)
		p := New()

		codexDir := filepath.Join(home, ".codex")
		os.MkdirAll(codexDir, 0700)
		authPath := filepath.Join(codexDir, "auth.json")

		data := map[string]interface{}{"access_token": "valid"}
		writeJSON(t, authPath, data)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary == nil || detection.Primary.Path != authPath {
			t.Errorf("Primary path = %v, want %v", detection.Primary, authPath)
		}
	})

	t.Run("detects CODEX_HOME/auth.json", func(t *testing.T) {
		setupEnv(t) // just to be safe
		customHome := t.TempDir()
		t.Setenv("CODEX_HOME", customHome)
		p := New()

		authPath := filepath.Join(customHome, "auth.json")
		data := map[string]interface{}{"access_token": "valid"}
		writeJSON(t, authPath, data)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != authPath {
			t.Errorf("Primary path = %v, want %v", detection.Primary.Path, authPath)
		}
	})

	t.Run("validates JSON fields", func(t *testing.T) {
		home := setupEnv(t)
		p := New()

		codexDir := filepath.Join(home, ".codex")
		os.MkdirAll(codexDir, 0700)
		authPath := filepath.Join(codexDir, "auth.json")

		// Invalid JSON
		os.WriteFile(authPath, []byte("{invalid"), 0600)
		detection, _ := p.DetectExistingAuth()
		if detection.Locations[0].IsValid {
			t.Error("Should be invalid (json error)")
		}

		// Valid JSON but missing token
		writeJSON(t, authPath, map[string]interface{}{"foo": "bar"})
		detection, _ = p.DetectExistingAuth()
		if detection.Locations[0].IsValid {
			t.Error("Should be invalid (missing token)")
		}

		// Valid fields
		validFields := []string{"access_token", "accessToken", "api_key", "token"}
		for _, field := range validFields {
			writeJSON(t, authPath, map[string]interface{}{field: "val"})
			detection, _ = p.DetectExistingAuth()
			if !detection.Locations[0].IsValid {
				t.Errorf("Should be valid with field %s", field)
			}
		}
	})

	t.Run("configured home wins over a newer default login", func(t *testing.T) {
		home := setupEnv(t)
		customHome := t.TempDir()
		t.Setenv("CODEX_HOME", customHome)
		p := New()

		// A different account in the default home must never override CODEX_HOME.
		defaultDir := filepath.Join(home, ".codex")
		os.MkdirAll(defaultDir, 0700)
		defaultPath := filepath.Join(defaultDir, "auth.json")
		writeJSON(t, defaultPath, map[string]interface{}{"token": "other-account"})
		os.Chtimes(defaultPath, time.Now(), time.Now())

		// The configured account is authoritative even when its file is older.
		customPath := filepath.Join(customHome, "auth.json")
		writeJSON(t, customPath, map[string]interface{}{"token": "configured-account"})
		os.Chtimes(customPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatal(err)
		}
		if detection.Primary == nil || detection.Primary.Path != customPath {
			t.Fatalf("primary=%+v, want configured home %s", detection.Primary, customPath)
		}
		if len(detection.Locations) != 1 || detection.Locations[0].Path != customPath {
			t.Fatalf("unconfigured account was scanned: %+v", detection.Locations)
		}
	})
}

func TestCodexDetectionDoesNotFallbackFromInvalidConfiguredHome(t *testing.T) {
	for _, body := range []string{"missing", `{`, `{"tokens":null}`, `{"access_token":false}`, "directory"} {
		t.Run(body, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			configured := t.TempDir()
			t.Setenv("CODEX_HOME", configured)
			defaultDir := filepath.Join(home, ".codex")
			if err := os.MkdirAll(defaultDir, 0700); err != nil {
				t.Fatal(err)
			}
			writeJSON(t, filepath.Join(defaultDir, "auth.json"), map[string]any{"access_token": "other-account"})
			configuredPath := filepath.Join(configured, "auth.json")
			if body == "directory" {
				if err := os.Mkdir(configuredPath, 0700); err != nil {
					t.Fatal(err)
				}
			} else if body != "missing" {
				if err := os.WriteFile(configuredPath, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			detection, err := New().DetectExistingAuth()
			if err != nil {
				t.Fatal(err)
			}
			if detection.Found || detection.Primary != nil {
				t.Fatalf("unconfigured account rescued invalid CODEX_HOME: %+v", detection)
			}
			if len(detection.Locations) != 1 || detection.Locations[0].Path != configuredPath {
				t.Fatalf("unexpected locations: %+v", detection.Locations)
			}
		})
	}
}

// =============================================================================
// ImportAuth Tests
// =============================================================================

func TestImportAuth(t *testing.T) {
	t.Run("imports auth.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "codex",
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		srcDir := t.TempDir()
		srcPath := filepath.Join(srcDir, "auth.json")
		writeJSON(t, srcPath, map[string]interface{}{"token": "val"})

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth error: %v", err)
		}

		if len(copied) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(copied))
		}
		expected := filepath.Join(prof.CodexHomePath(), "auth.json")
		if copied[0] != expected {
			t.Errorf("Copied to %s, want %s", copied[0], expected)
		}
		if _, err := os.Stat(expected); os.IsNotExist(err) {
			t.Error("Target file not created")
		}
	})

	t.Run("fails if source missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{Name: "test", BasePath: tmpDir}
		p := New()
		_, err := p.ImportAuth(context.Background(), "/missing", prof)
		if err == nil {
			t.Error("Should fail")
		}
	})
}

func TestCodexCredentialIntake(t *testing.T) {
	cases := []struct {
		name string
		body string
		mode provider.AuthMode
	}{
		{"modern nested OAuth", `{"auth_mode":"chatgpt","OPENAI_API_KEY":null,"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","id_token":"synthetic-id","account_id":"workspace"},"last_refresh":"2026-10-06T12:00:00Z"}`, provider.AuthModeOAuth},
		{"native API key", `{"auth_mode":"apikey","OPENAI_API_KEY":"synthetic-key","tokens":null,"last_refresh":null}`, provider.AuthModeAPIKey},
		{"flat access token", `{"access_token":"synthetic-access"}`, provider.AuthModeOAuth},
		{"flat camel case token", `{"accessToken":"synthetic-access"}`, provider.AuthModeOAuth},
		{"flat token alias", `{"token":"synthetic-access"}`, provider.AuthModeOAuth},
		{"flat API key", `{"api_key":"synthetic-key"}`, provider.AuthModeAPIKey},
		{"camel case API key", `{"apiKey":"synthetic-key"}`, provider.AuthModeAPIKey},
		{"matching access aliases", `{"access_token":"synthetic-access","accessToken":"synthetic-access"}`, provider.AuthModeOAuth},
		{"empty optional API key with OAuth", `{"OPENAI_API_KEY":"","tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`, provider.AuthModeOAuth},
		{"invalid JSON", `{`, ""},
		{"null document", `null`, ""},
		{"array document", `[]`, ""},
		{"empty object", `{}`, ""},
		{"settings only", `{"model":"gpt-5"}`, ""},
		{"null access", `{"access_token":null}`, ""},
		{"numeric access", `{"access_token":12}`, ""},
		{"empty access", `{"access_token":"  "}`, ""},
		{"refresh only", `{"refresh_token":"synthetic-refresh"}`, ""},
		{"identity only", `{"id_token":"synthetic-id"}`, ""},
		{"null API key only", `{"OPENAI_API_KEY":null}`, ""},
		{"numeric API key", `{"OPENAI_API_KEY":12}`, ""},
		{"null tokens only", `{"tokens":null}`, ""},
		{"array tokens", `{"tokens":[]}`, ""},
		{"string tokens", `{"tokens":"synthetic-access"}`, ""},
		{"empty tokens", `{"tokens":{}}`, ""},
		{"nested null access", `{"tokens":{"access_token":null,"refresh_token":"synthetic-refresh"}}`, ""},
		{"nested refresh only", `{"tokens":{"refresh_token":"synthetic-refresh"}}`, ""},
		{"null refresh", `{"access_token":"synthetic-access","refresh_token":null}`, ""},
		{"empty refresh", `{"access_token":"synthetic-access","refresh_token":""}`, ""},
		{"wrong refresh type", `{"access_token":"synthetic-access","refresh_token":{}}`, ""},
		{"conflicting access aliases", `{"access_token":"first","accessToken":"second"}`, ""},
		{"conflicting credential layouts", `{"access_token":"first","tokens":{"access_token":"second"}}`, ""},
		{"malformed flat token with valid nested login", `{"access_token":false,"tokens":{"access_token":"synthetic-access"}}`, ""},
		{"malformed identity", `{"tokens":{"access_token":"synthetic-access","account_id":[]}}`, ""},
		{"wrong nested fields with valid flat token", `{"access_token":"synthetic-access","tokens":{"access_token":false}}`, ""},
		{"wrong API key type with OAuth", `{"OPENAI_API_KEY":false,"tokens":{"access_token":"synthetic-access"}}`, ""},
		{"bad expiry", `{"access_token":"synthetic-access","expires_at":"tomorrow"}`, ""},
		{"null expiry", `{"access_token":"synthetic-access","expires_at":null}`, ""},
		{"out of range expiry", `{"access_token":"synthetic-access","expires_at":1e30}`, ""},
		{"bad last refresh", `{"tokens":{"access_token":"synthetic-access"},"last_refresh":false}`, ""},
		{"bad auth mode", `{"auth_mode":true,"access_token":"synthetic-access"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("CODEX_HOME", "")
			source := filepath.Join(home, ".codex", "auth.json")
			if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(source, []byte(tc.body), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(source)
			if err != nil {
				t.Fatal(err)
			}
			p := New()
			detection, err := p.DetectExistingAuth()
			if err != nil {
				t.Fatal(err)
			}
			valid := tc.mode != ""
			if detection.Found != valid || (detection.Primary != nil) != valid {
				t.Errorf("detection found=%v primary=%v, want valid=%v", detection.Found, detection.Primary != nil, valid)
			}
			if len(detection.Locations) != 1 || detection.Locations[0].IsValid != valid {
				t.Fatalf("unexpected detection locations: %+v", detection.Locations)
			}
			prof := &profile.Profile{Name: "imported", Provider: "codex", AuthMode: "unchanged", BasePath: filepath.Join(t.TempDir(), "target")}
			copied, err := p.ImportAuth(context.Background(), source, prof)
			if !valid {
				if err == nil {
					t.Fatal("malformed credential imported successfully")
				}
				if _, err := os.Lstat(prof.BasePath); !os.IsNotExist(err) {
					t.Fatalf("invalid import created a profile directory: %v", err)
				}
				if prof.AuthMode != "unchanged" {
					t.Fatalf("invalid import changed auth mode: %s", prof.AuthMode)
				}
				return
			}
			if err != nil {
				t.Fatalf("ImportAuth: %v", err)
			}
			target := filepath.Join(prof.CodexHomePath(), "auth.json")
			if len(copied) != 1 || copied[0] != target {
				t.Fatalf("copied=%v, want %s", copied, target)
			}
			data, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(data, []byte(tc.body)) {
				t.Fatalf("import did not preserve credential bytes: %v", err)
			}
			if prof.AuthMode != string(tc.mode) {
				t.Errorf("auth mode=%s, want %s", prof.AuthMode, tc.mode)
			}
			validation, err := p.ValidateToken(context.Background(), prof, true)
			if err != nil || !validation.Valid {
				t.Errorf("imported credential fails passive validation: %+v, %v", validation, err)
			}
			after, err := os.Stat(source)
			if err != nil || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("native source metadata changed: %v", err)
			}
			data, err = os.ReadFile(source)
			if err != nil || !bytes.Equal(data, []byte(tc.body)) {
				t.Fatalf("native source changed: %v", err)
			}
		})
	}
}

func TestCodexImportUsesCanonicalPathAndPreservesTargetOnFailure(t *testing.T) {
	source := filepath.Join(t.TempDir(), "exported-credentials.json")
	const native = `{"tokens":{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}}`
	if err := os.WriteFile(source, []byte(native), 0644); err != nil {
		t.Fatal(err)
	}
	prof := &profile.Profile{Name: "work", Provider: "codex", BasePath: t.TempDir()}
	target := filepath.Join(prof.CodexHomePath(), "auth.json")
	if _, err := New().ImportAuth(context.Background(), source, prof); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != native {
		t.Fatalf("canonical credential missing: %v", err)
	}
	if info, err := os.Stat(target); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("imported credential permissions are not private: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(prof.CodexHomePath(), filepath.Base(source))); !os.IsNotExist(err) {
		t.Fatalf("source basename was published as a credential path: %v", err)
	}
	if err := os.WriteFile(source, []byte(`{"tokens":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New().ImportAuth(context.Background(), source, prof); err == nil {
		t.Fatal("invalid replacement succeeded")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != native {
		t.Fatalf("failed replacement changed working credentials: %v", err)
	}
}

func TestCodexPassiveValidationUsesAccessExpiry(t *testing.T) {
	jwt := func(exp time.Time) string {
		claims, err := json.Marshal(map[string]any{"exp": exp.Unix()})
		if err != nil {
			t.Fatal(err)
		}
		return "e30." + base64.RawURLEncoding.EncodeToString(claims) + ".synthetic"
	}
	now := time.Now()
	prof := &profile.Profile{Name: "work", Provider: "codex", BasePath: t.TempDir()}
	if err := os.MkdirAll(prof.CodexHomePath(), 0700); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(prof.CodexHomePath(), "auth.json"), map[string]any{"tokens": map[string]any{
		"access_token": jwt(now.Add(time.Hour)), "id_token": jwt(now.Add(-time.Hour)), "refresh_token": "synthetic-refresh",
	}})
	result, err := New().ValidateToken(context.Background(), prof, true)
	if err != nil || !result.Valid || result.ExpiresAt.Unix() != now.Add(time.Hour).Unix() {
		t.Fatalf("access token did not control passive expiry: %+v, %v", result, err)
	}
	for _, refreshable := range []bool{false, true} {
		tokens := map[string]any{"access_token": jwt(now.Add(-time.Hour))}
		if refreshable {
			tokens["refresh_token"] = "synthetic-refresh"
		}
		writeJSON(t, filepath.Join(prof.CodexHomePath(), "auth.json"), map[string]any{"tokens": tokens})
		result, err = New().ValidateToken(context.Background(), prof, true)
		if err != nil || result.Valid != refreshable {
			t.Fatalf("expired access token with renewable=%v: %+v, %v", refreshable, result, err)
		}
	}
}

func TestCodexImportRejectsUnusableSourcesBeforeWriting(t *testing.T) {
	for _, kind := range []string{"directory", "oversized", "canceled"} {
		t.Run(kind, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source")
			if kind == "directory" {
				if err := os.Mkdir(source, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				f, err := os.Create(source)
				if err != nil {
					t.Fatal(err)
				}
				if kind == "oversized" {
					err = f.Truncate(16<<20 + 1)
				} else {
					_, err = f.WriteString(`{"OPENAI_API_KEY":"synthetic-key"}`)
				}
				closeErr := f.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("create source: %v, %v", err, closeErr)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if kind == "canceled" {
				cancel()
			}
			prof := &profile.Profile{Name: "work", Provider: "codex", BasePath: filepath.Join(t.TempDir(), "target")}
			if _, err := New().ImportAuth(ctx, source, prof); err == nil {
				t.Fatal("unusable source imported successfully")
			}
			if _, err := os.Lstat(prof.BasePath); !os.IsNotExist(err) {
				t.Fatalf("invalid import modified target: %v", err)
			}
		})
	}
}

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

// TestEnsureFileCredentialStore covers the #46 regression: when the config has
// no top-level cli_auth_credentials_store but DOES contain table sections (e.g.
// [mcp_servers.*]), the enforced key must land as a TOP-LEVEL key, not nested
// under the last table. Appending at end-of-file (the old behavior) would make
// it mcp_servers.<last>.cli_auth_credentials_store, silently failing to enforce
// the file store.
func TestEnsureFileCredentialStorePreservesTOML(t *testing.T) {
	const setting = "cli_auth_credentials_store = \"file\"\n"
	for _, tc := range []struct {
		name, input, want string
	}{
		{
			name:  "literal root value",
			input: "cli_auth_credentials_store = 'keychain' # native store\nmodel = 'synthetic-model'\n",
			want:  "cli_auth_credentials_store = \"file\" # native store\nmodel = 'synthetic-model'\n",
		},
		{
			name:  "quoted root key and literal value",
			input: "  \"cli_auth_credentials_store\"\t=\t'keychain'\r\n[features]\r\nfast = true\r\n",
			want:  "  \"cli_auth_credentials_store\"\t=\t\"file\"\r\n[features]\r\nfast = true\r\n",
		},
		{
			name:  "literal root key",
			input: "'cli_auth_credentials_store' = \"keychain\"",
			want:  "'cli_auth_credentials_store' = \"file\"",
		},
		{
			name:  "escaped root key",
			input: "\"cli_auth_credentials_stor\\u0065\" = \"keychain\"\n",
			want:  "\"cli_auth_credentials_stor\\u0065\" = \"file\"\n",
		},
		{
			name:  "already literal file",
			input: "cli_auth_credentials_store = 'file' # keep spelling\n",
			want:  "cli_auth_credentials_store = 'file' # keep spelling\n",
		},
		{
			name:  "already escaped file",
			input: "cli_auth_credentials_store = \"fi\\u006ce\"\n",
			want:  "cli_auth_credentials_store = \"fi\\u006ce\"\n",
		},
		{
			name:  "nested file value does not select root",
			input: "[profiles.work]\ncli_auth_credentials_store = \"file\"\n",
			want:  setting + "[profiles.work]\ncli_auth_credentials_store = \"file\"\n",
		},
		{
			name:  "nested keychain is not rewritten",
			input: "cli_auth_credentials_store = 'keychain'\n[profiles.work]\ncli_auth_credentials_store = \"keychain\"\n",
			want:  "cli_auth_credentials_store = \"file\"\n[profiles.work]\ncli_auth_credentials_store = \"keychain\"\n",
		},
		{
			name:  "dotted unrelated key",
			input: "profiles.work.cli_auth_credentials_store = 'keychain'\n",
			want:  setting + "profiles.work.cli_auth_credentials_store = 'keychain'\n",
		},
		{
			name:  "multiline literal string decoy",
			input: "instructions = '''\n[not_a_table]\ncli_auth_credentials_store = \"keychain\"\n'''\n",
			want:  setting + "instructions = '''\n[not_a_table]\ncli_auth_credentials_store = \"keychain\"\n'''\n",
		},
		{
			name:  "multiline basic escaped delimiter decoy",
			input: "instructions = \"\"\"\n\\\"\"\"\ncli_auth_credentials_store = \"keychain\"\n\"\"\"\n",
			want:  setting + "instructions = \"\"\"\n\\\"\"\"\ncli_auth_credentials_store = \"keychain\"\n\"\"\"\n",
		},
		{
			name:  "multiline target string",
			input: "cli_auth_credentials_store = '''keychain''' # select file\n",
			want:  "cli_auth_credentials_store = \"file\" # select file\n",
		},
		{
			name:  "array and inline table decoys",
			input: "mcp = [\n  { cli_auth_credentials_store = 'keychain' }, # keep\n  { arguments = [']', '#', '='] },\n]\n",
			want:  setting + "mcp = [\n  { cli_auth_credentials_store = 'keychain' }, # keep\n  { arguments = [']', '#', '='] },\n]\n",
		},
		{
			name:  "quoted table path",
			input: "[mcp_servers.\"odd]#=name\"]\ncommand = 'synthetic-command'\n",
			want:  setting + "[mcp_servers.\"odd]#=name\"]\ncommand = 'synthetic-command'\n",
		},
		{
			name:  "array table is not root",
			input: "[[profiles]]\ncli_auth_credentials_store = 'keychain'\n",
			want:  setting + "[[profiles]]\ncli_auth_credentials_store = 'keychain'\n",
		},
		{
			name:  "leading comments retained",
			input: "# native config\n\n[features]\nfast = true\n",
			want:  "# native config\n\n" + setting + "[features]\nfast = true\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			if err := os.WriteFile(path, []byte(tc.input), 0600); err != nil {
				t.Fatal(err)
			}
			if err := EnsureFileCredentialStore(home); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("configuration mismatch\ngot:  %q\nwant: %q", got, tc.want)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := EnsureFileCredentialStore(home); err != nil {
				t.Fatalf("repeat enforcement: %v", err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatal("idempotent enforcement rewrote the native config")
			}
			got, err = os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("repeat enforcement changed contents: %q, %v", got, err)
			}
		})
	}
}

func TestEnsureFileCredentialStoreRejectsAmbiguousSource(t *testing.T) {
	for _, input := range []string{
		"cli_auth_credentials_store = 'keychain'\ncli_auth_credentials_store = \"file\"\n",
		"cli_auth_credentials_store = 'keychain'\n\"cli_auth_credentials_stor\\u0065\" = 'file'\n",
		"cli_auth_credentials_store =\n",
		"cli_auth_credentials_store = true\n",
		"cli_auth_credentials_store = 'keychain' trailing\n",
		"cli_auth_credentials_store = \"keychain\n",
		"cli_auth_credentials_store = '''keychain\n",
		"cli_auth_credentials_store = \"bad\\q\"\n",
		"cli_auth_credentials_store = \"bad\\uD800\"\n",
		"cli_auth_credentials_store = \"\"\"key\\ chain\"\"\"\n",
		"cli_auth_credentials_store.nested = 'keychain'\n",
		"[cli_auth_credentials_store]\nmode = 'keychain'\n",
		"cli_auth_credentials_store = 'file'\n[broken\n",
		"cli_auth_credentials_store = 'file'\n[broken] trailing\n",
		"cli_auth_credentials_store = 'file'\nargs = ['unfinished'\n",
		"cli_auth_credentials_store = 'file'\nargs = [}\n",
		"cli_auth_credentials_store = 'file'\nmodel =\n",
		"cli_auth_credentials_store = 'file'\nnot an assignment\n",
	} {
		t.Run(input, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "config.toml")
			if err := os.WriteFile(path, []byte(input), 0640); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := EnsureFileCredentialStore(home); err == nil {
				t.Fatal("malformed or ambiguous configuration accepted")
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != input {
				t.Fatalf("rejected configuration changed: %q, %v", got, err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
				t.Fatal("rejected configuration was replaced or its metadata changed")
			}
		})
	}
}

func TestEnsureFileCredentialStore(t *testing.T) {
	const settingLine = `cli_auth_credentials_store = "file"`

	// firstTableIdx returns the byte offset of the first TOML table header, or -1.
	firstTableIdx := func(s string) int {
		offset := 0
		for _, line := range strings.Split(s, "\n") {
			if t := strings.TrimSpace(line); strings.HasPrefix(t, "[") {
				return offset
			}
			offset += len(line) + 1
		}
		return -1
	}

	write := func(t *testing.T, home, cfg string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(cfg), 0600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(t *testing.T, home string) string {
		t.Helper()
		got, err := os.ReadFile(filepath.Join(home, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		return string(got)
	}

	t.Run("inserts top-level key before mcp_servers table", func(t *testing.T) {
		home := t.TempDir()
		write(t, home, "[mcp_servers.tavily]\ncommand = \"npx\"\nargs = [\"-y\", \"tavily-mcp\"]\n")
		if err := EnsureFileCredentialStore(home); err != nil {
			t.Fatalf("EnsureFileCredentialStore error: %v", err)
		}
		out := read(t, home)
		settingIdx := strings.Index(out, settingLine)
		if settingIdx < 0 {
			t.Fatalf("setting line not written; got:\n%s", out)
		}
		tableIdx := firstTableIdx(out)
		if tableIdx < 0 {
			t.Fatalf("mcp_servers table lost; got:\n%s", out)
		}
		// The setting must appear BEFORE the first table header (top-level scope).
		if settingIdx > tableIdx {
			t.Errorf("setting nested under a table (idx %d > first table idx %d); got:\n%s", settingIdx, tableIdx, out)
		}
		if !strings.Contains(out, "[mcp_servers.tavily]") || !strings.Contains(out, `command = "npx"`) {
			t.Errorf("MCP config not preserved; got:\n%s", out)
		}
	})

	t.Run("replaces existing keychain value in place", func(t *testing.T) {
		home := t.TempDir()
		write(t, home, "cli_auth_credentials_store = \"keychain\"\n[mcp_servers.x]\ncommand = \"y\"\n")
		if err := EnsureFileCredentialStore(home); err != nil {
			t.Fatalf("EnsureFileCredentialStore error: %v", err)
		}
		out := read(t, home)
		if strings.Contains(out, "keychain") {
			t.Errorf("keychain not replaced; got:\n%s", out)
		}
		if !strings.Contains(out, settingLine) {
			t.Errorf("file store not set; got:\n%s", out)
		}
	})

	t.Run("preserves leading comment block", func(t *testing.T) {
		home := t.TempDir()
		write(t, home, "# my codex config\n\n[mcp_servers.x]\ncommand = \"y\"\n")
		if err := EnsureFileCredentialStore(home); err != nil {
			t.Fatalf("EnsureFileCredentialStore error: %v", err)
		}
		out := read(t, home)
		if strings.Index(out, settingLine) > firstTableIdx(out) {
			t.Errorf("setting not top-level; got:\n%s", out)
		}
		if !strings.Contains(out, "# my codex config") {
			t.Errorf("leading comment lost; got:\n%s", out)
		}
	})
}
