package gemini

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if p.ID() != "gemini" {
		t.Errorf("ID() = %q, want %q", p.ID(), "gemini")
	}
}

func TestProviderDisplayName(t *testing.T) {
	p := New()
	expected := "Gemini CLI (Google Gemini Ultra)"
	if p.DisplayName() != expected {
		t.Errorf("DisplayName() = %q, want %q", p.DisplayName(), expected)
	}
}

func TestProviderDefaultBin(t *testing.T) {
	p := New()
	if p.DefaultBin() != "gemini" {
		t.Errorf("DefaultBin() = %q, want %q", p.DefaultBin(), "gemini")
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
	hasAPIKey := false
	hasVertexADC := false
	for _, mode := range modes {
		switch mode {
		case provider.AuthModeOAuth:
			hasOAuth = true
		case provider.AuthModeAPIKey:
			hasAPIKey = true
		case provider.AuthModeVertexADC:
			hasVertexADC = true
		}
	}

	if !hasOAuth {
		t.Error("SupportedAuthModes() should include OAuth")
	}
	if !hasAPIKey {
		t.Error("SupportedAuthModes() should include APIKey")
	}
	if !hasVertexADC {
		t.Error("SupportedAuthModes() should include VertexADC")
	}
}

// =============================================================================
// Auth Files Tests
// =============================================================================

func TestAuthFiles(t *testing.T) {
	t.Run("returns three auth file specs", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		if len(files) != 3 {
			t.Fatalf("AuthFiles() returned %d files, want 3", len(files))
		}
	})

	t.Run("first file is settings.json and required", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[0]
		if !strings.HasSuffix(file.Path, "settings.json") {
			t.Errorf("AuthFiles()[0].Path = %q, should end with settings.json", file.Path)
		}
		if !file.Required {
			t.Error("settings.json should be required")
		}
	})

	t.Run("second file is oauth_creds.json and optional", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[1]
		if !strings.HasSuffix(file.Path, "oauth_creds.json") {
			t.Errorf("AuthFiles()[1].Path = %q, should end with oauth_creds.json", file.Path)
		}
		if file.Required {
			t.Error("oauth_creds.json should be optional")
		}
	})

	t.Run("third file is .env and optional", func(t *testing.T) {
		p := New()
		files := p.AuthFiles()

		file := files[2]
		if !strings.HasSuffix(file.Path, filepath.Join(".gemini", ".env")) {
			t.Errorf("AuthFiles()[2].Path = %q, should end with .gemini/.env", file.Path)
		}
		if file.Required {
			t.Error(".env should be optional")
		}
	})

	t.Run("uses GEMINI_HOME if set", func(t *testing.T) {
		originalHome := os.Getenv("GEMINI_HOME")
		defer os.Setenv("GEMINI_HOME", originalHome)

		os.Setenv("GEMINI_HOME", "/custom/gemini/home")
		p := New()
		files := p.AuthFiles()

		expected := "/custom/gemini/home/settings.json"
		if files[0].Path != expected {
			t.Errorf("AuthFiles()[0].Path = %q, want %q", files[0].Path, expected)
		}
	})

	t.Run("uses default .gemini if GEMINI_HOME not set", func(t *testing.T) {
		originalHome := os.Getenv("GEMINI_HOME")
		defer os.Setenv("GEMINI_HOME", originalHome)

		os.Unsetenv("GEMINI_HOME")
		p := New()
		files := p.AuthFiles()

		homeDir, _ := os.UserHomeDir()
		expected := filepath.Join(homeDir, ".gemini", "settings.json")
		if files[0].Path != expected {
			t.Errorf("AuthFiles()[0].Path = %q, want %q", files[0].Path, expected)
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
			Provider: "gemini",
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

	t.Run("creates .gemini directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		geminiDir := filepath.Join(prof.HomePath(), ".gemini")
		info, err := os.Stat(geminiDir)
		if err != nil {
			t.Fatalf(".gemini dir not created: %v", err)
		}
		if !info.IsDir() {
			t.Error(".gemini should be a directory")
		}
	})

	t.Run("sets secure permissions", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		homePath := prof.HomePath()
		info, _ := os.Stat(homePath)
		if info.Mode().Perm() != 0700 {
			t.Errorf("home permissions = %o, want 0700", info.Mode().Perm())
		}
	})

	t.Run("idempotent - can be called multiple times", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
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
// Vertex ADC Mode Tests
// =============================================================================

func TestPrepareProfileWithVertexADC(t *testing.T) {
	t.Run("creates gcloud directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		if err := p.PrepareProfile(context.Background(), prof); err != nil {
			t.Fatalf("PrepareProfile() error = %v", err)
		}

		gcloudDir := filepath.Join(tmpDir, "gcloud")
		info, err := os.Stat(gcloudDir)
		if err != nil {
			t.Fatalf("gcloud dir not created: %v", err)
		}
		if !info.IsDir() {
			t.Error("gcloud should be a directory")
		}
	})

	t.Run("does not create gcloud directory for OAuth mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeOAuth),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		gcloudDir := filepath.Join(tmpDir, "gcloud")
		if _, err := os.Stat(gcloudDir); !os.IsNotExist(err) {
			t.Error("gcloud dir should not be created for OAuth mode")
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
			Provider: "gemini",
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

	t.Run("returns only HOME for OAuth mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeOAuth),
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		if len(env) != 1 {
			t.Errorf("Env() returned %d vars for OAuth, want 1", len(env))
		}
	})

	t.Run("sets CLOUDSDK_CONFIG for VertexADC mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		cloudsdk, ok := env["CLOUDSDK_CONFIG"]
		if !ok {
			t.Fatal("CLOUDSDK_CONFIG not set for VertexADC mode")
		}

		expected := filepath.Join(tmpDir, "gcloud")
		if cloudsdk != expected {
			t.Errorf("CLOUDSDK_CONFIG = %q, want %q", cloudsdk, expected)
		}
	})

	t.Run("returns two vars for VertexADC mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		if len(env) != 2 {
			t.Errorf("Env() returned %d vars for VertexADC, want 2", len(env))
		}
	})
}

// =============================================================================
// Logout Tests
// =============================================================================

func TestLogout(t *testing.T) {
	t.Run("removes .env file", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create .env file
		envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")
		if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=test"), 0600); err != nil {
			t.Fatal(err)
		}

		// Logout
		if err := p.Logout(context.Background(), prof); err != nil {
			t.Fatalf("Logout() error = %v", err)
		}

		// Verify removed
		if _, err := os.Stat(envPath); !os.IsNotExist(err) {
			t.Error(".env should be removed after Logout")
		}
	})

	t.Run("handles non-existent .env file", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Don't create .env, just logout
		if err := p.Logout(context.Background(), prof); err != nil {
			t.Errorf("Logout() error = %v, should handle missing file", err)
		}
	})
}

// =============================================================================
// Status Tests
// =============================================================================

func TestStatus(t *testing.T) {
	t.Run("API key mode: logged in when .env exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeAPIKey),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create .env file
		envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")
		if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=test"), 0600); err != nil {
			t.Fatal(err)
		}

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true for API key mode when .env exists")
		}
	})

	t.Run("API key mode: not logged in when .env missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeAPIKey),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.LoggedIn {
			t.Error("LoggedIn should be false for API key mode when .env missing")
		}
	})

	t.Run("VertexADC mode: logged in when ADC credentials exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create ADC credentials file
		adcPath := filepath.Join(tmpDir, "gcloud", "application_default_credentials.json")
		if err := os.WriteFile(adcPath, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true for VertexADC when ADC exists")
		}
	})

	t.Run("VertexADC mode: not logged in when ADC missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.LoggedIn {
			t.Error("LoggedIn should be false for VertexADC when ADC missing")
		}
	})

	t.Run("OAuth mode: logged in when settings.json exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeOAuth),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Create settings.json
		geminiDir := filepath.Join(prof.HomePath(), ".gemini")
		if err := os.MkdirAll(geminiDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(`{"oauth": {}}`), 0600); err != nil {
			t.Fatal(err)
		}

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true for OAuth when settings.json exists")
		}
	})

	t.Run("OAuth mode: not logged in when settings.json missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeOAuth),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.LoggedIn {
			t.Error("LoggedIn should be false for OAuth when settings.json missing")
		}
	})

	t.Run("reports lock file status", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
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
	t.Run("valid when home exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
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
			Provider: "gemini",
			BasePath: tmpDir,
		}

		p := New()
		// Don't call PrepareProfile

		err := p.ValidateProfile(context.Background(), prof)
		if err == nil {
			t.Error("ValidateProfile() should error when home missing")
		}
	})

	t.Run("VertexADC: valid when gcloud dir exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		p.PrepareProfile(context.Background(), prof)

		if err := p.ValidateProfile(context.Background(), prof); err != nil {
			t.Errorf("ValidateProfile() error = %v", err)
		}
	})

	t.Run("VertexADC: invalid when gcloud dir missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		// Create home but not gcloud dir
		os.MkdirAll(prof.HomePath(), 0700)

		p := New()
		err := p.ValidateProfile(context.Background(), prof)
		if err == nil {
			t.Error("ValidateProfile() should error when gcloud dir missing for VertexADC")
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
// geminiHome Helper Tests
// =============================================================================

func TestGeminiHome(t *testing.T) {
	t.Run("respects GEMINI_HOME env var", func(t *testing.T) {
		original := os.Getenv("GEMINI_HOME")
		defer os.Setenv("GEMINI_HOME", original)

		os.Setenv("GEMINI_HOME", "/test/gemini")
		result := geminiHome()
		if result != "/test/gemini" {
			t.Errorf("geminiHome() = %q, want /test/gemini", result)
		}
	})

	t.Run("falls back to ~/.gemini", func(t *testing.T) {
		original := os.Getenv("GEMINI_HOME")
		defer os.Setenv("GEMINI_HOME", original)

		os.Unsetenv("GEMINI_HOME")
		result := geminiHome()
		homeDir, _ := os.UserHomeDir()
		expected := filepath.Join(homeDir, ".gemini")
		if result != expected {
			t.Errorf("geminiHome() = %q, want %s", result, expected)
		}
	})
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestFullOAuthLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "oauth-test",
		Provider: "gemini",
		AuthMode: string(provider.AuthModeOAuth),
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

	// Simulate login by creating settings.json
	geminiDir := filepath.Join(prof.HomePath(), ".gemini")
	if err := os.MkdirAll(geminiDir, 0700); err != nil {
		t.Fatalf("mkdir .gemini: %v", err)
	}
	if err := os.WriteFile(filepath.Join(geminiDir, "settings.json"), []byte(`{"oauth": {}}`), 0600); err != nil {
		t.Fatalf("write settings.json: %v", err)
	}

	// Status (now logged in)
	status, _ = p.Status(context.Background(), prof)
	if !status.LoggedIn {
		t.Error("should be logged in after settings.json created")
	}

	// Get env
	env, _ := p.Env(context.Background(), prof)
	if env["HOME"] == "" {
		t.Error("HOME should be set")
	}

	// Logout (cleans up cached auth files)
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}

	// settings.json should be removed after logout
	if _, err := os.Stat(filepath.Join(geminiDir, "settings.json")); !os.IsNotExist(err) {
		t.Error("settings.json should be removed after logout")
	}
}

func TestFullAPIKeyLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "apikey-test",
		Provider: "gemini",
		AuthMode: string(provider.AuthModeAPIKey),
		BasePath: tmpDir,
	}

	p := New()

	// Prepare
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile() error = %v", err)
	}

	// Validate (should pass)
	if err := p.ValidateProfile(context.Background(), prof); err != nil {
		t.Fatalf("ValidateProfile() error = %v", err)
	}

	// Status (not logged in yet)
	status, _ := p.Status(context.Background(), prof)
	if status.LoggedIn {
		t.Error("should not be logged in before API key set")
	}

	// Simulate login by creating .env file
	envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")
	os.WriteFile(envPath, []byte("GEMINI_API_KEY=test-key-12345"), 0600)

	// Status (now logged in)
	status, _ = p.Status(context.Background(), prof)
	if !status.LoggedIn {
		t.Error("should be logged in after .env created")
	}

	// Passive validation should pass
	result, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if !result.Valid {
		t.Errorf("ValidateToken() should be valid, got error: %s", result.Error)
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

func TestOAuthValidateTokenIgnoresAPIKeyEnv(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "oauth-validate",
		Provider: "gemini",
		AuthMode: string(provider.AuthModeOAuth),
		BasePath: tmpDir,
	}

	p := New()

	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile() error = %v", err)
	}

	// Create .env without OAuth files
	envPath := filepath.Join(prof.HomePath(), ".gemini", ".env")
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=test-key-12345"), 0600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	result, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("ValidateToken() error = %v", err)
	}
	if result.Valid {
		t.Error("ValidateToken() should be invalid for OAuth mode when only API key is configured")
	}
}

func TestFullVertexADCLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	prof := &profile.Profile{
		Name:     "vertex-test",
		Provider: "gemini",
		AuthMode: string(provider.AuthModeVertexADC),
		BasePath: tmpDir,
	}

	p := New()

	// Prepare (should create gcloud directory)
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile() error = %v", err)
	}

	// Verify gcloud directory exists
	gcloudDir := filepath.Join(tmpDir, "gcloud")
	if _, err := os.Stat(gcloudDir); err != nil {
		t.Fatalf("gcloud dir should exist: %v", err)
	}

	// Validate (should pass)
	if err := p.ValidateProfile(context.Background(), prof); err != nil {
		t.Fatalf("ValidateProfile() error = %v", err)
	}

	// Status (not logged in yet)
	status, _ := p.Status(context.Background(), prof)
	if status.LoggedIn {
		t.Error("should not be logged in before ADC credentials")
	}

	// Simulate login by creating ADC credentials
	adcPath := filepath.Join(gcloudDir, "application_default_credentials.json")
	os.WriteFile(adcPath, []byte(`{"type":"authorized_user"}`), 0600)

	// Status (now logged in)
	status, _ = p.Status(context.Background(), prof)
	if !status.LoggedIn {
		t.Error("should be logged in after ADC created")
	}

	// Env should include CLOUDSDK_CONFIG
	env, _ := p.Env(context.Background(), prof)
	if env["CLOUDSDK_CONFIG"] != gcloudDir {
		t.Errorf("CLOUDSDK_CONFIG = %q, want %q", env["CLOUDSDK_CONFIG"], gcloudDir)
	}
}


// =============================================================================
// DetectExistingAuth Tests
// =============================================================================

func TestDetectExistingAuth(t *testing.T) {
	setupEnv := func(t *testing.T) (string, string) {
		home := t.TempDir()
		xdg := filepath.Join(home, ".config")
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", xdg)
		return home, xdg
	}

	t.Run("detects settings.json (OAuth)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		geminiDir := filepath.Join(home, ".gemini")
		os.MkdirAll(geminiDir, 0700)
		path := filepath.Join(geminiDir, "settings.json")
		writeJSON(t, path, map[string]interface{}{"oauth": map[string]interface{}{}})

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary path = %v, want %v", detection.Primary.Path, path)
		}
	})

	t.Run("detects oauth_creds.json", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		geminiDir := filepath.Join(home, ".gemini")
		os.MkdirAll(geminiDir, 0700)
		path := filepath.Join(geminiDir, "oauth_creds.json")
		writeJSON(t, path, map[string]interface{}{"access_token": "valid"})

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary path = %v, want %v", detection.Primary.Path, path)
		}
	})

	t.Run("detects .env (API Key)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		geminiDir := filepath.Join(home, ".gemini")
		os.MkdirAll(geminiDir, 0700)
		path := filepath.Join(geminiDir, ".env")
		os.WriteFile(path, []byte("GEMINI_API_KEY=test"), 0600)

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary path = %v, want %v", detection.Primary.Path, path)
		}
	})

	t.Run("detects ADC credentials (Vertex)", func(t *testing.T) {
		_, xdg := setupEnv(t)
		p := New()

		gcloudDir := filepath.Join(xdg, "gcloud")
		os.MkdirAll(gcloudDir, 0700)
		path := filepath.Join(gcloudDir, "application_default_credentials.json")
		writeJSON(t, path, map[string]interface{}{"client_id": "test", "type": "authorized_user"})

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary path = %v, want %v", detection.Primary.Path, path)
		}
	})

	t.Run("detects GEMINI_HOME locations", func(t *testing.T) {
		setupEnv(t)
		customHome := t.TempDir()
		t.Setenv("GEMINI_HOME", customHome)
		p := New()

		path := filepath.Join(customHome, "settings.json")
		writeJSON(t, path, map[string]interface{}{"oauth": map[string]interface{}{}})

		detection, err := p.DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}

		if !detection.Found {
			t.Error("Should have found auth")
		}
		if detection.Primary.Path != path {
			t.Errorf("Primary path = %v, want %v", detection.Primary.Path, path)
		}
	})

	t.Run("validates file content", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()
		geminiDir := filepath.Join(home, ".gemini")
		os.MkdirAll(geminiDir, 0700)

		// Invalid JSON settings
		path := filepath.Join(geminiDir, "settings.json")
		os.WriteFile(path, []byte("{invalid"), 0600)
		detection, _ := p.DetectExistingAuth()
		if detection.Locations[0].IsValid {
			t.Error("Should be invalid settings.json")
		}

		// Invalid .env
		envPath := filepath.Join(geminiDir, ".env")
		os.WriteFile(envPath, []byte("FOO=BAR"), 0600)
		detection, _ = p.DetectExistingAuth()
		// .env location index depends on list order, checking all
		for _, loc := range detection.Locations {
			if filepath.Base(loc.Path) == ".env" && loc.IsValid {
				t.Error("Should be invalid .env")
			}
		}
	})
}

// =============================================================================
// ImportAuth Tests
// =============================================================================

func TestImportAuth(t *testing.T) {
	t.Run("imports settings.json", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Source from .gemini structure
		srcDir := t.TempDir()
		srcGemini := filepath.Join(srcDir, ".gemini")
		os.MkdirAll(srcGemini, 0700)
		srcPath := filepath.Join(srcGemini, "settings.json")
		writeJSON(t, srcPath, map[string]interface{}{"oauth": true})

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth error: %v", err)
		}

		expected := filepath.Join(prof.HomePath(), ".gemini", "settings.json")
		if copied[0] != expected {
			t.Errorf("Copied to %s, want %s", copied[0], expected)
		}
	})

	t.Run("imports ADC credentials", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: "vertex_adc", // Important for PrepareProfile to create gcloud dir
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Source from gcloud structure
		srcDir := t.TempDir()
		srcGcloud := filepath.Join(srcDir, "gcloud")
		os.MkdirAll(srcGcloud, 0700)
		srcPath := filepath.Join(srcGcloud, "application_default_credentials.json")
		writeJSON(t, srcPath, map[string]interface{}{"type": "authorized_user"})

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth error: %v", err)
		}

		expected := filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json")
		if copied[0] != expected {
			t.Errorf("Copied to %s, want %s", copied[0], expected)
		}
	})

	t.Run("imports .env", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		srcDir := t.TempDir()
		srcGemini := filepath.Join(srcDir, ".gemini")
		os.MkdirAll(srcGemini, 0700)
		srcPath := filepath.Join(srcGemini, ".env")
		os.WriteFile(srcPath, []byte("KEY=val"), 0600)

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth error: %v", err)
		}

		expected := filepath.Join(prof.HomePath(), ".gemini", ".env")
		if copied[0] != expected {
			t.Errorf("Copied to %s, want %s", copied[0], expected)
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
