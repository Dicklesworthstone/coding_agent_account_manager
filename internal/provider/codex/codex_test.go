package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

	t.Run("prioritizes recent file", func(t *testing.T) {
		home := setupEnv(t)
		customHome := t.TempDir()
		t.Setenv("CODEX_HOME", customHome)
		p := New()

		// Old file in default location
		defaultDir := filepath.Join(home, ".codex")
		os.MkdirAll(defaultDir, 0700)
		defaultPath := filepath.Join(defaultDir, "auth.json")
		writeJSON(t, defaultPath, map[string]interface{}{"token": "old"})
		os.Chtimes(defaultPath, time.Now().Add(-time.Hour), time.Now().Add(-time.Hour))

		// New file in custom location
		customPath := filepath.Join(customHome, "auth.json")
		writeJSON(t, customPath, map[string]interface{}{"token": "new"})
		os.Chtimes(customPath, time.Now(), time.Now())

		detection, _ := p.DetectExistingAuth()
		if detection.Primary.Path != customPath {
			t.Errorf("Should pick custom path (newer). Got %s", detection.Primary.Path)
		}
		if detection.Warning == "" {
			t.Error("Should warn about multiple files")
		}
	})
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
