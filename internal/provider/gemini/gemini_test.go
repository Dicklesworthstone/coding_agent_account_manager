package gemini

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
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

	t.Run("isolates native homes and auth selection for OAuth mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeOAuth),
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		if env["GEMINI_HOME"] != filepath.Join(prof.HomePath(), ".gemini") || env["GEMINI_CLI_HOME"] != prof.HomePath() {
			t.Errorf("Env() did not isolate native homes: %#v", env)
		}
		if env["GOOGLE_GENAI_USE_GCA"] != "true" || env["GOOGLE_GENAI_USE_VERTEXAI"] != "false" {
			t.Errorf("Env() did not preserve OAuth selection: %#v", env)
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

	t.Run("pins the ADC source and Vertex auth mode", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeVertexADC),
			BasePath: tmpDir,
		}

		p := New()
		env, _ := p.Env(context.Background(), prof)

		want := filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json")
		if env["GOOGLE_APPLICATION_CREDENTIALS"] != want || env["GOOGLE_GENAI_USE_VERTEXAI"] != "true" {
			t.Errorf("Env() does not select imported ADC: %#v", env)
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
		t.Setenv("GEMINI_HOME", "")
		t.Setenv("GEMINI_CLI_HOME", "")
		t.Setenv("CLOUDSDK_CONFIG", "")
		t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
		return home, xdg
	}

	t.Run("detects settings.json (OAuth)", func(t *testing.T) {
		home, _ := setupEnv(t)
		p := New()

		geminiDir := filepath.Join(home, ".gemini")
		os.MkdirAll(geminiDir, 0700)
		path := filepath.Join(geminiDir, "settings.json")
		writeJSON(t, path, map[string]interface{}{"oauth": map[string]interface{}{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh"}})

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
		writeJSON(t, path, map[string]interface{}{"client_id": "synthetic-client", "client_secret": "synthetic-secret", "refresh_token": "synthetic-refresh", "type": "authorized_user"})

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
		writeJSON(t, path, map[string]interface{}{"oauth": map[string]interface{}{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh"}})

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
		writeJSON(t, srcPath, map[string]interface{}{"oauth": map[string]interface{}{"access_token": "synthetic-access", "refresh_token": "synthetic-refresh"}})

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth error: %v", err)
		}

		expected := filepath.Join(prof.HomePath(), ".gemini", "settings.json")
		if !slices.Contains(copied, expected) {
			t.Errorf("Copied files %v do not include %s", copied, expected)
		}
	})

	t.Run("imports ADC credentials", func(t *testing.T) {
		tmpDir := t.TempDir()
		prof := &profile.Profile{
			Name:     "test",
			Provider: "gemini",
			AuthMode: string(provider.AuthModeOAuth), // Import must infer the actual mode.
			BasePath: tmpDir,
		}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		// Source from gcloud structure
		srcDir := t.TempDir()
		srcGcloud := filepath.Join(srcDir, "gcloud")
		os.MkdirAll(srcGcloud, 0700)
		srcPath := filepath.Join(srcGcloud, "application_default_credentials.json")
		writeJSON(t, srcPath, map[string]interface{}{"type": "authorized_user", "client_id": "synthetic-client", "client_secret": "synthetic-secret", "refresh_token": "synthetic-refresh"})

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
		os.WriteFile(srcPath, []byte("GEMINI_API_KEY=synthetic-key"), 0600)

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

func TestGeminiImportCompleteSelectedBundle(t *testing.T) {
	const oauth = "{\n  \"access_token\": \"synthetic-access\", \"refresh_token\": \"synthetic-refresh\", \"expiry_date\": 1893456000000\n}\n"
	const adc = `{"type":"authorized_user","client_id":"synthetic-client","client_secret":"synthetic-secret","refresh_token":"synthetic-refresh","quota_project_id":"private-project"}`
	const oauthSettings = "{\n \"security\": {\"auth\": {\"selectedType\": \"oauth-personal\"}}, \"mcpServers\": {\"private\": {\"command\": \"private-helper\"}}\n}\n"
	const keySettings = `{"selectedAuthType":"gemini-api-key","theme":"private-theme"}`
	const keyEnv = "# Private account configuration\nexport GEMINI_API_KEY='synthetic-key' # account key\nGOOGLE_CLOUD_PROJECT=private-project\n"
	const embedded = `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}`
	for _, tc := range []struct {
		name     string
		source   string
		files    map[string]string
		mode     provider.AuthMode
		expected map[string]string
	}{
		{
			name: "OAuth cache includes private settings and excludes other auth sources", source: "oauth_creds.json",
			files: map[string]string{"oauth_creds.json": oauth, "settings.json": oauthSettings, ".env": keyEnv, "oauth_credentials.json": `{"access_token":"unrelated-old-account"}`},
			mode:  provider.AuthModeOAuth, expected: map[string]string{"home/.gemini/oauth_creds.json": oauth, "home/.gemini/settings.json": oauthSettings},
		},
		{
			name: "settings entry imports its selected OAuth cache", source: "settings.json",
			files: map[string]string{"oauth_creds.json": oauth, "settings.json": oauthSettings, ".env": keyEnv},
			mode:  provider.AuthModeOAuth, expected: map[string]string{"home/.gemini/oauth_creds.json": oauth, "home/.gemini/settings.json": oauthSettings},
		},
		{
			name: "API key includes settings and excludes unrelated OAuth", source: ".env",
			files: map[string]string{".env": keyEnv, "settings.json": keySettings, "oauth_creds.json": oauth},
			mode:  provider.AuthModeAPIKey, expected: map[string]string{"home/.gemini/.env": keyEnv, "home/.gemini/settings.json": keySettings},
		},
		{
			name: "arbitrary OAuth filename is canonicalized by content", source: "exported-login.backup",
			files: map[string]string{"exported-login.backup": oauth}, mode: provider.AuthModeOAuth,
			expected: map[string]string{"home/.gemini/oauth_creds.json": oauth},
		},
		{
			name: "legacy OAuth filename becomes current cache", source: "oauth_credentials.json",
			files: map[string]string{"oauth_credentials.json": oauth}, mode: provider.AuthModeOAuth,
			expected: map[string]string{"home/.gemini/oauth_creds.json": oauth},
		},
		{
			name: "arbitrary API key filename is canonicalized by content", source: "key.backup",
			files: map[string]string{"key.backup": keyEnv}, mode: provider.AuthModeAPIKey,
			expected: map[string]string{"home/.gemini/.env": keyEnv},
		},
		{
			name: "ADC is imported to the runtime selected gcloud path", source: "google-login.backup",
			files: map[string]string{"google-login.backup": adc}, mode: provider.AuthModeVertexADC,
			expected: map[string]string{"gcloud/application_default_credentials.json": adc},
		},
		{
			name: "ADC named settings is classified by credential content", source: "settings.json",
			files: map[string]string{"settings.json": adc}, mode: provider.AuthModeVertexADC,
			expected: map[string]string{"gcloud/application_default_credentials.json": adc},
		},
		{
			name: "embedded legacy OAuth preserves settings and supplies modern cache", source: "settings.json",
			files: map[string]string{"settings.json": `{"theme":"private","oauth":` + embedded + `}`}, mode: provider.AuthModeOAuth,
			expected: map[string]string{"home/.gemini/settings.json": `{"theme":"private","oauth":` + embedded + `}`, "home/.gemini/oauth_creds.json": embedded},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sourceDir := t.TempDir()
			for name, data := range tc.files {
				writeGeminiFixture(t, filepath.Join(sourceDir, name), data)
			}
			prof := &profile.Profile{Name: "imported", Provider: "gemini", AuthMode: "oauth", BasePath: t.TempDir()}
			copied, err := New().ImportAuth(context.Background(), filepath.Join(sourceDir, tc.source), prof)
			if err != nil {
				t.Fatalf("ImportAuth: %v", err)
			}
			if prof.AuthMode != string(tc.mode) || len(copied) != len(tc.expected) {
				t.Fatalf("import mode/files = %s/%v, want %s/%d files", prof.AuthMode, copied, tc.mode, len(tc.expected))
			}
			validation, err := New().ValidateToken(context.Background(), prof, true)
			if err != nil || validation == nil || !validation.Valid {
				t.Fatalf("imported bundle did not pass passive validation: %+v, %v", validation, err)
			}
			for rel, want := range tc.expected {
				path := filepath.Join(prof.BasePath, rel)
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want || !slices.Contains(copied, path) {
					t.Errorf("import did not preserve expected %s: %v", rel, err)
				}
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Errorf("imported file %s permissions: %v", rel, err)
				}
			}
			for name, want := range tc.files {
				got, err := os.ReadFile(filepath.Join(sourceDir, name))
				if err != nil || string(got) != want {
					t.Errorf("native source %s was changed: %v", name, err)
				}
			}
			for _, rel := range []string{"home/.gemini/.env", "home/.gemini/oauth_creds.json", "home/.gemini/oauth_credentials.json", "home/.gemini/settings.json", "gcloud/application_default_credentials.json"} {
				if _, want := tc.expected[rel]; want {
					continue
				}
				if _, err := os.Stat(filepath.Join(prof.BasePath, rel)); !os.IsNotExist(err) {
					t.Errorf("unexpected imported auth source %s: %v", rel, err)
				}
			}
			t.Setenv("GEMINI_API_KEY", "unrelated-ambient-key")
			t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/unrelated/ambient-adc.json")
			env, err := New().Env(context.Background(), prof)
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == provider.AuthModeAPIKey && env["GEMINI_API_KEY"] != "synthetic-key" {
				t.Error("saved API key was not pinned against ambient credentials")
			}
			if tc.mode == provider.AuthModeVertexADC && env["GOOGLE_APPLICATION_CREDENTIALS"] != filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json") {
				t.Error("runtime does not point at imported ADC")
			}
		})
	}
}

func TestGeminiRejectsMalformedNativeAuth(t *testing.T) {
	for _, tc := range []struct{ name, filename, data string }{
		{"policy settings", "settings.json", `{"theme":"dark"}`},
		{"selector without login", "settings.json", `{"selectedAuthType":"oauth-personal"}`},
		{"empty OAuth settings", "settings.json", `{"oauth":{}}`},
		{"null OAuth settings", "settings.json", `{"oauth":null}`},
		{"boolean OAuth settings", "settings.json", `{"oauth":true}`},
		{"null settings", "settings.json", `null`},
		{"array settings", "settings.json", `[]`},
		{"empty file", "oauth_creds.json", ""},
		{"malformed JSON", "oauth_creds.json", "{"},
		{"null cache", "oauth_creds.json", "null"},
		{"array cache", "oauth_creds.json", "[]"},
		{"null access", "oauth_creds.json", `{"access_token":null}`},
		{"number access", "oauth_creds.json", `{"access_token":42}`},
		{"empty access", "oauth_creds.json", `{"access_token":" "}`},
		{"refresh only", "oauth_creds.json", `{"refresh_token":"synthetic-refresh"}`},
		{"null refresh", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":null}`},
		{"empty refresh", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":""}`},
		{"array refresh", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":[]}`},
		{"conflicting access aliases", "oauth_creds.json", `{"access_token":"synthetic-access","accessToken":"different-account"}`},
		{"invalid identity", "oauth_creds.json", `{"access_token":"synthetic-access","email":[]}`},
		{"null expiry", "oauth_creds.json", `{"access_token":"synthetic-access","expiry_date":null}`},
		{"invalid expiry", "oauth_creds.json", `{"access_token":"synthetic-access","expiry_date":"tomorrow"}`},
		{"negative expiry", "oauth_creds.json", `{"access_token":"synthetic-access","expiry_date":-1}`},
		{"fractional expiry", "oauth_creds.json", `{"access_token":"synthetic-access","expiry_date":1.25}`},
		{"out of range expiry", "oauth_creds.json", `{"access_token":"synthetic-access","expiry_date":1e30}`},
		{"renewable expiry overflows int64", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expiry_date":9223372036854775807}`},
		{"renewable expiry exceeds year 9999", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expiry_date":253402300800000}`},
		{"seconds expiry exceeds year 9999", "oauth_creds.json", `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expires_at":253402300800}`},
		{"comment mentioning key", ".env", "# GEMINI_API_KEY=synthetic-key\nFOO=bar\n"},
		{"unrelated env", ".env", "NOT_GEMINI_API_KEY=synthetic-key\n"},
		{"empty API key", ".env", "GEMINI_API_KEY=\n"},
		{"quoted empty API key", ".env", "GEMINI_API_KEY=' '\n"},
		{"API key containing whitespace", ".env", "GEMINI_API_KEY='not a usable key'\n"},
		{"unterminated API key", ".env", "GEMINI_API_KEY='synthetic-key\n"},
		{"trailing shell text", ".env", "GEMINI_API_KEY='synthetic-key' unexpected\n"},
		{"different auth selection", ".env", "GEMINI_API_KEY=synthetic-key\nGOOGLE_GENAI_USE_VERTEXAI=true\n"},
		{"mixed API keys", ".env", "GEMINI_API_KEY=synthetic-key\nGOOGLE_API_KEY=other-key\n"},
		{"type-only ADC", "application_default_credentials.json", `{"type":"authorized_user"}`},
		{"null ADC type", "application_default_credentials.json", `{"type":null,"client_id":"client","client_secret":"secret","refresh_token":"refresh"}`},
		{"null ADC refresh", "application_default_credentials.json", `{"type":"authorized_user","client_id":"client","client_secret":"secret","refresh_token":null}`},
		{"missing ADC client secret", "application_default_credentials.json", `{"type":"authorized_user","client_id":"client","refresh_token":"refresh"}`},
		{"invalid service account key", "application_default_credentials.json", `{"type":"service_account","client_email":"synthetic@example.com","private_key":"not-a-key","token_uri":"https://oauth2.googleapis.com/token"}`},
		{"unsupported external account", "application_default_credentials.json", `{"type":"external_account","credential_source":{"file":"/other/account"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolateGeminiDetection(t)
			path := filepath.Join(dir, tc.filename)
			writeGeminiFixture(t, path, tc.data)
			if tc.filename == "application_default_credentials.json" {
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", path)
			}
			detection, err := New().DetectExistingAuth()
			if err != nil || detection.Found || detection.Primary != nil {
				t.Fatalf("malformed source detected as a login: %+v, %v", detection, err)
			}
			prof := &profile.Profile{Name: "protected", Provider: "gemini", AuthMode: "existing-mode", BasePath: t.TempDir()}
			oldPath := filepath.Join(prof.HomePath(), ".gemini", "oauth_creds.json")
			writeGeminiFixture(t, oldPath, `{"access_token":"working-existing-account"}`)
			copied, err := New().ImportAuth(context.Background(), path, prof)
			if err == nil || len(copied) != 0 || prof.AuthMode != "existing-mode" {
				t.Fatalf("malformed import changed profile: files=%v mode=%s error=%v", copied, prof.AuthMode, err)
			}
			got, err := os.ReadFile(oldPath)
			if err != nil || string(got) != `{"access_token":"working-existing-account"}` {
				t.Fatal("malformed import overwrote valid credentials")
			}
		})
	}
}

func TestGeminiDetectionUsesSelectedGrant(t *testing.T) {
	const oauth = `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}`
	const key = "GEMINI_API_KEY=synthetic-key\n"
	for _, tc := range []struct {
		name    string
		files   map[string]string
		primary string
		mode    provider.AuthMode
	}{
		{"newer policy does not replace OAuth", map[string]string{"oauth_creds.json": oauth, "settings.json": `{"theme":"newer-private-theme"}`}, "oauth_creds.json", provider.AuthModeOAuth},
		{"legacy filename is usable", map[string]string{"oauth_credentials.json": oauth}, "oauth_credentials.json", provider.AuthModeOAuth},
		{"current cache outranks old account", map[string]string{"oauth_creds.json": oauth, "oauth_credentials.json": `{"access_token":"different-account"}`}, "oauth_creds.json", provider.AuthModeOAuth},
		{"invalid current cache blocks old login", map[string]string{"oauth_creds.json": `null`, "oauth_credentials.json": oauth}, "", ""},
		{"invalid current blocks settings fallback", map[string]string{"oauth_creds.json": `{}`, "settings.json": `{"oauth":` + oauth + `}`}, "", ""},
		{"OAuth selection excludes API key", map[string]string{"settings.json": `{"selectedAuthType":"oauth-personal"}`, "oauth_creds.json": oauth, ".env": key}, "oauth_creds.json", provider.AuthModeOAuth},
		{"current API key selection excludes malformed OAuth", map[string]string{"settings.json": `{"security":{"auth":{"selectedType":"gemini-api-key"}}}`, "oauth_creds.json": `null`, ".env": key}, ".env", provider.AuthModeAPIKey},
		{"API selector imports key bundle through settings", map[string]string{"settings.json": `{"selectedAuthType":"gemini-api-key"}`, "oauth_creds.json": oauth, ".env": key}, ".env", provider.AuthModeAPIKey},
		{"selection missing credential does not fallback", map[string]string{"settings.json": `{"selectedAuthType":"oauth-personal"}`, ".env": key}, "", ""},
		{"API selection missing key does not fallback", map[string]string{"settings.json": `{"selectedAuthType":"gemini-api-key"}`, "oauth_creds.json": oauth}, "", ""},
		{"dotenv API key selects native method", map[string]string{"oauth_creds.json": oauth, ".env": key}, ".env", provider.AuthModeAPIKey},
		{"unknown selection", map[string]string{"settings.json": `{"selectedAuthType":"unknown-mode"}`, "oauth_creds.json": oauth}, "", ""},
		{"null selection", map[string]string{"settings.json": `{"selectedAuthType":null}`, "oauth_creds.json": oauth}, "", ""},
		{"conflicting selectors", map[string]string{"settings.json": `{"selectedAuthType":"oauth-personal","security":{"auth":{"selectedType":"gemini-api-key"}}}`, "oauth_creds.json": oauth, ".env": key}, "", ""},
		{"conflicting embedded account", map[string]string{"settings.json": `{"oauth":{"access_token":"different-account"}}`, "oauth_creds.json": oauth}, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolateGeminiDetection(t)
			for name, data := range tc.files {
				writeGeminiFixture(t, filepath.Join(dir, name), data)
				stamp := time.Unix(1600000000, 0)
				if name == "settings.json" || name == "oauth_credentials.json" {
					stamp = time.Unix(1900000000, 0)
				}
				if err := os.Chtimes(filepath.Join(dir, name), stamp, stamp); err != nil {
					t.Fatal(err)
				}
			}
			detection, err := New().DetectExistingAuth()
			if err != nil {
				t.Fatal(err)
			}
			if tc.primary == "" {
				if detection.Found || detection.Primary != nil {
					t.Fatalf("incorrect auth fallback: %+v", detection)
				}
				return
			}
			want := filepath.Join(dir, tc.primary)
			if !detection.Found || detection.Primary == nil || detection.Primary.Path != want || !detection.Primary.IsValid {
				t.Fatalf("detected %+v, want selected source %s", detection, want)
			}
			prof := &profile.Profile{Name: "detected", Provider: "gemini", BasePath: t.TempDir()}
			if _, err := New().ImportAuth(context.Background(), detection.Primary.Path, prof); err != nil {
				t.Fatalf("detected source cannot be imported: %v", err)
			}
			if prof.AuthMode != string(tc.mode) {
				t.Errorf("import mode %s, want %s", prof.AuthMode, tc.mode)
			}
		})
	}
}

func TestGeminiDetectionRespectsNativeEnvironmentSelection(t *testing.T) {
	const key = "GEMINI_API_KEY=synthetic-key\n"
	const project = "GOOGLE_CLOUD_PROJECT=selected-project\nGOOGLE_CLOUD_LOCATION=us-central1\n"
	for _, tc := range []struct {
		name     string
		env      map[string]string
		dotenv   string
		settings string
		mode     provider.AuthMode
	}{
		{name: "ambient Vertex selects ADC over old OAuth", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"}, mode: provider.AuthModeVertexADC},
		{name: "ambient Vertex retains ADC project bundle", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"}, dotenv: project, mode: provider.AuthModeVertexADC},
		{name: "GCA precedes Vertex and API key", env: map[string]string{"GOOGLE_GENAI_USE_GCA": "true", "GOOGLE_GENAI_USE_VERTEXAI": "true", "GEMINI_API_KEY": "other-key"}, dotenv: key, mode: provider.AuthModeOAuth},
		{name: "Vertex precedes ambient API key", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true", "GEMINI_API_KEY": "other-key"}, mode: provider.AuthModeVertexADC},
		{name: "dotenv GCA precedes ambient Vertex", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"}, dotenv: "GOOGLE_GENAI_USE_GCA=true\n", mode: provider.AuthModeOAuth},
		{name: "shell false overrides dotenv selector", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "false"}, dotenv: "GOOGLE_GENAI_USE_VERTEXAI=true\n", mode: provider.AuthModeOAuth},
		{name: "shell empty overrides dotenv selector", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": ""}, dotenv: "GOOGLE_GENAI_USE_VERTEXAI=true\n", mode: provider.AuthModeOAuth},
		{name: "configured OAuth precedes environment", env: map[string]string{"GOOGLE_GENAI_USE_VERTEXAI": "true"}, settings: `{"security":{"auth":{"selectedType":"oauth-personal"}}}`, mode: provider.AuthModeOAuth},
		{name: "configured Vertex precedes environment", env: map[string]string{"GOOGLE_GENAI_USE_GCA": "true"}, settings: `{"security":{"auth":{"selectedType":"vertex-ai"}}}`, mode: provider.AuthModeVertexADC},
		{name: "shell-only key cannot import old OAuth", env: map[string]string{"GEMINI_API_KEY": "other-key"}},
		{name: "shell key cannot import a different saved key", env: map[string]string{"GEMINI_API_KEY": "other-key"}, dotenv: key},
		{name: "matching saved key is importable", env: map[string]string{"GEMINI_API_KEY": "synthetic-key"}, dotenv: key, mode: provider.AuthModeAPIKey},
		{name: "configured API still requires selected key", env: map[string]string{"GEMINI_API_KEY": "other-key"}, dotenv: key, settings: `{"selectedAuthType":"gemini-api-key"}`},
		{name: "shell empty suppresses configured saved key", env: map[string]string{"GEMINI_API_KEY": ""}, dotenv: key, settings: `{"selectedAuthType":"gemini-api-key"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolateGeminiDetection(t)
			writeGeminiFixture(t, filepath.Join(dir, "oauth_creds.json"), `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}`)
			adcPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gcloud", "application_default_credentials.json")
			writeGeminiFixture(t, adcPath, `{"type":"authorized_user","client_id":"synthetic-client","client_secret":"synthetic-secret","refresh_token":"synthetic-adc"}`)
			if tc.dotenv != "" {
				writeGeminiFixture(t, filepath.Join(dir, ".env"), tc.dotenv)
			}
			if tc.settings != "" {
				writeGeminiFixture(t, filepath.Join(dir, "settings.json"), tc.settings)
			}
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			detection, err := New().DetectExistingAuth()
			if err != nil {
				t.Fatal(err)
			}
			if tc.mode == "" {
				if detection.Found || detection.Primary != nil {
					t.Fatalf("detected a different account from the native selection: %+v", detection)
				}
				return
			}
			if !detection.Found || detection.Primary == nil || !detection.Primary.IsValid {
				t.Fatalf("selected native grant not found: %+v", detection)
			}
			prof := &profile.Profile{Name: "native-selection", Provider: "gemini", BasePath: t.TempDir()}
			if _, err := New().ImportAuth(context.Background(), detection.Primary.Path, prof); err != nil {
				t.Fatalf("detected selection cannot be imported: %v", err)
			}
			if prof.AuthMode != string(tc.mode) {
				t.Fatalf("imported mode %s, want native selection %s", prof.AuthMode, tc.mode)
			}
			result, err := New().ValidateToken(context.Background(), prof, true)
			if err != nil || result == nil || !result.Valid {
				t.Fatalf("selected imported profile is invalid: %+v, %v", result, err)
			}
		})
	}
}

func TestGeminiExplicitCredentialSourceIgnoresAmbientAuthMethod(t *testing.T) {
	for _, tc := range []struct {
		name string
		mode provider.AuthMode
	}{
		{"oauth_creds.json", provider.AuthModeOAuth},
		{".env", provider.AuthModeAPIKey},
		{"application_default_credentials.json", provider.AuthModeVertexADC},
		{"ADC dotenv bundle", provider.AuthModeVertexADC},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := isolateGeminiDetection(t)
			t.Setenv("GOOGLE_GENAI_USE_GCA", "true")
			t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
			t.Setenv("GEMINI_API_KEY", "unrelated-ambient-key")
			path := filepath.Join(dir, tc.name)
			data := `{"access_token":"selected-access","refresh_token":"selected-refresh"}`
			switch tc.mode {
			case provider.AuthModeAPIKey:
				data = "GEMINI_API_KEY=selected-key\n"
			case provider.AuthModeVertexADC:
				data = `{"type":"authorized_user","client_id":"selected-client","client_secret":"selected-secret","refresh_token":"selected-refresh"}`
				if tc.name == "ADC dotenv bundle" {
					adcPath := filepath.Join(t.TempDir(), "application_default_credentials.json")
					writeGeminiFixture(t, adcPath, data)
					path = filepath.Join(dir, ".env")
					data = "GOOGLE_APPLICATION_CREDENTIALS=" + adcPath + "\nGOOGLE_CLOUD_PROJECT=selected-project\nGOOGLE_CLOUD_LOCATION=us-central1\n"
					writeGeminiFixture(t, filepath.Join(dir, "oauth_creds.json"), `{"access_token":"unrelated-old-login"}`)
				}
			}
			writeGeminiFixture(t, path, data)
			prof := &profile.Profile{Name: "explicit-selection", Provider: "gemini", BasePath: t.TempDir()}
			if _, err := New().ImportAuth(context.Background(), path, prof); err != nil || prof.AuthMode != string(tc.mode) {
				t.Fatalf("explicit credential was overridden by ambient auth: mode=%s, %v", prof.AuthMode, err)
			}
		})
	}
}

func TestGeminiDetectionRespectsConfiguredHome(t *testing.T) {
	for _, mode := range []string{"GEMINI_HOME", "GEMINI_CLI_HOME", "GEMINI_HOME wins", "explicit empty home"} {
		t.Run(mode, func(t *testing.T) {
			defaultDir := isolateGeminiDetection(t)
			writeGeminiFixture(t, filepath.Join(defaultDir, "oauth_creds.json"), `{"access_token":"newer-unrelated-default-account"}`)
			custom := t.TempDir()
			selected := custom
			if mode == "GEMINI_CLI_HOME" {
				t.Setenv("GEMINI_CLI_HOME", custom)
				selected = filepath.Join(custom, ".gemini")
			} else {
				t.Setenv("GEMINI_HOME", custom)
				if mode == "GEMINI_HOME wins" {
					t.Setenv("GEMINI_CLI_HOME", t.TempDir())
				}
			}
			if mode != "explicit empty home" {
				path := filepath.Join(selected, "oauth_creds.json")
				writeGeminiFixture(t, path, `{"access_token":"configured-account"}`)
				old := time.Unix(1500000000, 0)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
			}
			detection, err := New().DetectExistingAuth()
			if err != nil {
				t.Fatal(err)
			}
			if mode == "explicit empty home" {
				if detection.Found {
					t.Fatal("explicit empty home fell back to default account")
				}
			} else if !detection.Found || detection.Primary == nil || detection.Primary.Path != filepath.Join(selected, "oauth_creds.json") {
				t.Fatalf("did not select configured home: %+v", detection)
			}
		})
	}
}

func TestGeminiADCSelectionRetainsSettingsAndProject(t *testing.T) {
	dir := isolateGeminiDetection(t)
	settings := `{"security":{"auth":{"selectedType":"vertex-ai"}},"theme":"private-theme"}`
	envData := "GOOGLE_CLOUD_PROJECT=private-project\nGOOGLE_CLOUD_LOCATION=us-central1\n"
	writeGeminiFixture(t, filepath.Join(dir, "settings.json"), settings)
	writeGeminiFixture(t, filepath.Join(dir, ".env"), envData)
	writeGeminiFixture(t, filepath.Join(dir, "oauth_creds.json"), `{"access_token":"unrelated-google-login"}`)
	sdk := t.TempDir()
	t.Setenv("CLOUDSDK_CONFIG", sdk)
	oldADC := `{"type":"authorized_user","client_id":"old-client","client_secret":"old-secret","refresh_token":"old-account"}`
	writeGeminiFixture(t, filepath.Join(sdk, "application_default_credentials.json"), oldADC)
	selected := filepath.Join(t.TempDir(), "chosen-account.backup")
	adc := `{"type":"authorized_user","client_id":"selected-client","client_secret":"selected-secret","refresh_token":"selected-account"}`
	writeGeminiFixture(t, selected, adc)
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", selected)
	detection, err := New().DetectExistingAuth()
	if err != nil || !detection.Found || detection.Primary == nil {
		t.Fatalf("Vertex detection = %+v, %v", detection, err)
	}
	prof := &profile.Profile{Name: "vertex", Provider: "gemini", AuthMode: "oauth", BasePath: t.TempDir()}
	if _, err := New().ImportAuth(context.Background(), detection.Primary.Path, prof); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{"gcloud/application_default_credentials.json": adc, "home/.gemini/settings.json": settings, "home/.gemini/.env": envData} {
		got, err := os.ReadFile(filepath.Join(prof.BasePath, rel))
		if err != nil || string(got) != want {
			t.Errorf("Vertex bundle lost %s: %v", rel, err)
		}
	}
	if prof.AuthMode != string(provider.AuthModeVertexADC) {
		t.Fatalf("ADC mode = %s", prof.AuthMode)
	}
	env, err := New().Env(context.Background(), prof)
	if err != nil || env["GOOGLE_CLOUD_PROJECT"] != "private-project" || env["GOOGLE_CLOUD_LOCATION"] != "us-central1" {
		t.Fatalf("Vertex runtime lost the selected project context: %+v, %v", env, err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing-adc.json"))
	detection, err = New().DetectExistingAuth()
	if err != nil || detection.Found {
		t.Fatalf("missing selected ADC fell back to another account: %+v, %v", detection, err)
	}
}

func TestGeminiADCDetectionWithoutSettingsPreservesSelectedEnvBundle(t *testing.T) {
	for _, selection := range []string{"project context", "dotenv credential path", "ambient credential path", "dotenv gcloud directory"} {
		t.Run(selection, func(t *testing.T) {
			dir := isolateGeminiDetection(t)
			selectedDir := t.TempDir()
			selectedPath := filepath.Join(selectedDir, "application_default_credentials.json")
			selected := `{"type":"authorized_user","client_id":"selected-client","client_secret":"selected-secret","refresh_token":"selected-account"}`
			other := `{"type":"authorized_user","client_id":"other-client","client_secret":"other-secret","refresh_token":"other-account"}`
			writeGeminiFixture(t, selectedPath, selected)
			defaultPath := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "gcloud", "application_default_credentials.json")
			writeGeminiFixture(t, defaultPath, other)
			envData := "GOOGLE_CLOUD_PROJECT=selected-project\nGOOGLE_CLOUD_LOCATION=us-central1\n"
			switch selection {
			case "project context":
				t.Setenv("CLOUDSDK_CONFIG", selectedDir)
			case "dotenv credential path":
				envData += "GOOGLE_GENAI_USE_VERTEXAI=true\nGOOGLE_APPLICATION_CREDENTIALS=" + selectedPath + "\n"
				writeGeminiFixture(t, filepath.Join(dir, "oauth_creds.json"), `{"access_token":"unrelated-oauth-account"}`)
			case "ambient credential path":
				t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", selectedPath)
				envData += "GOOGLE_APPLICATION_CREDENTIALS=" + defaultPath + "\n"
			case "dotenv gcloud directory":
				envData += "CLOUDSDK_CONFIG=" + selectedDir + "\n"
			}
			envPath := filepath.Join(dir, ".env")
			writeGeminiFixture(t, envPath, envData)
			detection, err := New().DetectExistingAuth()
			if err != nil || !detection.Found || detection.Primary == nil || detection.Primary.Path != envPath {
				t.Fatalf("detected entry does not retain ADC context: %+v, %v", detection, err)
			}
			prof := &profile.Profile{Name: "selected-adc", Provider: "gemini", BasePath: t.TempDir()}
			copied, err := New().ImportAuth(context.Background(), detection.Primary.Path, prof)
			if err != nil || len(copied) != 2 || prof.AuthMode != string(provider.AuthModeVertexADC) {
				t.Fatalf("ADC bundle import = %v, mode=%s, %v", copied, prof.AuthMode, err)
			}
			for rel, want := range map[string]string{"gcloud/application_default_credentials.json": selected, "home/.gemini/.env": envData} {
				got, err := os.ReadFile(filepath.Join(prof.BasePath, rel))
				if err != nil || string(got) != want {
					t.Fatalf("selected ADC import lost %s: %v", rel, err)
				}
			}
			env, err := New().Env(context.Background(), prof)
			if err != nil || env["GOOGLE_CLOUD_PROJECT"] != "selected-project" || env["GOOGLE_APPLICATION_CREDENTIALS"] != filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json") {
				t.Fatalf("runtime does not use selected ADC and project: %+v, %v", env, err)
			}
		})
	}
}

func TestGeminiStandaloneADCImportDoesNotAdoptUnrelatedHome(t *testing.T) {
	dir := isolateGeminiDetection(t)
	writeGeminiFixture(t, filepath.Join(dir, "settings.json"), `{"selectedAuthType":"gemini-api-key"}`)
	writeGeminiFixture(t, filepath.Join(dir, ".env"), "GEMINI_API_KEY=unrelated-native-key\nGOOGLE_CLOUD_PROJECT=unrelated-project\n")
	path := filepath.Join(t.TempDir(), "explicit-adc.json")
	writeGeminiFixture(t, path, `{"type":"authorized_user","client_id":"synthetic-client","client_secret":"synthetic-secret","refresh_token":"selected-refresh"}`)
	prof := &profile.Profile{Name: "standalone", Provider: "gemini", BasePath: t.TempDir()}
	copied, err := New().ImportAuth(context.Background(), path, prof)
	if err != nil || len(copied) != 1 || prof.AuthMode != string(provider.AuthModeVertexADC) {
		t.Fatalf("standalone ADC import = %v, mode=%s, %v", copied, prof.AuthMode, err)
	}
	for _, name := range []string{".env", "settings.json"} {
		if _, err := os.Stat(filepath.Join(prof.HomePath(), ".gemini", name)); !os.IsNotExist(err) {
			t.Fatalf("standalone ADC adopted unrelated %s", name)
		}
	}
}

func TestGeminiImportRejectsMalformedCompanionBeforeWriting(t *testing.T) {
	for _, settings := range []string{
		`null`, `{`, `[]`, `{"security":null}`, `{"security":{"auth":[]}}`,
		`{"security":{"auth":{"selectedType":42}}}`, `{"oauth":null}`,
		`{"oauth":{"refresh_token":"synthetic-refresh"}}`,
		`{"selectedAuthType":"gemini-api-key"}`,
		`{"oauth":{"access_token":"different-account"}}`,
	} {
		t.Run(settings, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "oauth_creds.json")
			writeGeminiFixture(t, path, `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}`)
			writeGeminiFixture(t, filepath.Join(dir, "settings.json"), settings)
			prof := &profile.Profile{Name: "protected", Provider: "gemini", AuthMode: "original-mode", BasePath: t.TempDir()}
			target := filepath.Join(prof.HomePath(), ".gemini", "oauth_creds.json")
			original := []byte(`{"access_token":"original-valid-account"}`)
			writeGeminiFixture(t, target, string(original))
			if copied, err := New().ImportAuth(context.Background(), path, prof); err == nil || len(copied) != 0 {
				t.Fatalf("invalid companion accepted: %v, %v", copied, err)
			}
			got, err := os.ReadFile(target)
			if err != nil || !bytes.Equal(got, original) || prof.AuthMode != "original-mode" {
				t.Fatal("failed companion validation changed the existing account")
			}
		})
	}
}

func TestGeminiImportServiceAccount(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]string{
		"type": "service_account", "client_email": "synthetic@private-project.iam.gserviceaccount.com",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"token_uri":   "https://oauth2.googleapis.com/token", "project_id": "private-project",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "service-account.backup")
	writeGeminiFixture(t, path, string(data))
	prof := &profile.Profile{Name: "service", Provider: "gemini", BasePath: t.TempDir()}
	if _, err := New().ImportAuth(context.Background(), path, prof); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(prof.BasePath, "gcloud", "application_default_credentials.json"))
	if err != nil || !bytes.Equal(got, data) || prof.AuthMode != "vertex-adc" {
		t.Fatal("service-account import did not retain its exact usable credential")
	}
}

func TestGeminiImportRevalidatesAndHonorsCancellation(t *testing.T) {
	dir := isolateGeminiDetection(t)
	path := filepath.Join(dir, "oauth_creds.json")
	writeGeminiFixture(t, path, `{"access_token":"synthetic-access"}`)
	detection, err := New().DetectExistingAuth()
	if err != nil || !detection.Found {
		t.Fatalf("detect: %+v, %v", detection, err)
	}
	writeGeminiFixture(t, path, `{"access_token":null}`)
	prof := &profile.Profile{Name: "changed", Provider: "gemini", BasePath: t.TempDir()}
	if _, err := New().ImportAuth(context.Background(), detection.Primary.Path, prof); err == nil {
		t.Fatal("import trusted validation from before the native source changed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	writeGeminiFixture(t, path, `{"access_token":"synthetic-access"}`)
	if _, err := New().ImportAuth(ctx, path, prof); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled import error = %v", err)
	}
	if _, err := os.Stat(prof.HomePath()); !os.IsNotExist(err) {
		t.Fatal("failed or canceled import created destination files")
	}
}

func TestGeminiPassiveValidationRequiresUsableSelectedCredentials(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mode    provider.AuthMode
		files   map[string]string
		valid   bool
		expired bool
	}{
		{
			name: "native CLI can renew expired OAuth", mode: provider.AuthModeOAuth,
			files: map[string]string{"home/.gemini/oauth_creds.json": `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","expiry_date":1600000000000}`},
			valid: true, expired: true,
		},
		{
			name: "expired access-only OAuth is unusable", mode: provider.AuthModeOAuth,
			files:   map[string]string{"home/.gemini/oauth_creds.json": `{"access_token":"synthetic-access","expiry_date":1600000000000}`},
			expired: true,
		},
		{
			name: "null access is not authenticated", mode: provider.AuthModeOAuth,
			files: map[string]string{"home/.gemini/oauth_creds.json": `{"access_token":null}`, "home/.gemini/settings.json": `{"theme":"private"}`},
		},
		{
			name: "null refresh cannot renew expired access", mode: provider.AuthModeOAuth,
			files: map[string]string{"home/.gemini/oauth_creds.json": `{"access_token":"synthetic-access","refresh_token":null,"expiry_date":1600000000000}`},
		},
		{
			name: "settings policy alone is not OAuth", mode: provider.AuthModeOAuth,
			files: map[string]string{"home/.gemini/settings.json": `{"theme":"private"}`},
		},
		{
			name: "API key comment is not authentication", mode: provider.AuthModeAPIKey,
			files: map[string]string{"home/.gemini/.env": "# GEMINI_API_KEY=synthetic-key\n"},
		},
		{
			name: "complete ADC is valid without an OAuth cache", mode: provider.AuthModeVertexADC,
			files: map[string]string{"gcloud/application_default_credentials.json": `{"type":"authorized_user","client_id":"synthetic-client","client_secret":"synthetic-secret","refresh_token":"synthetic-refresh"}`},
			valid: true,
		},
		{
			name: "type-only ADC is not valid", mode: provider.AuthModeVertexADC,
			files: map[string]string{"gcloud/application_default_credentials.json": `{"type":"authorized_user"}`},
		},
		{
			name: "OAuth profile cannot use selected API key", mode: provider.AuthModeOAuth,
			files: map[string]string{"home/.gemini/settings.json": `{"selectedAuthType":"gemini-api-key"}`, "home/.gemini/.env": "GEMINI_API_KEY=synthetic-key\n"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGeminiDetection(t)
			t.Setenv("GEMINI_API_KEY", "")
			prof := &profile.Profile{Name: "passive", Provider: "gemini", AuthMode: string(tc.mode), BasePath: t.TempDir()}
			for name, data := range tc.files {
				writeGeminiFixture(t, filepath.Join(prof.BasePath, name), data)
			}
			result, err := New().ValidateToken(context.Background(), prof, true)
			if err != nil || result == nil || result.Valid != tc.valid {
				t.Fatalf("passive result %+v, %v; want valid=%v", result, err, tc.valid)
			}
			if tc.expired && (result.ExpiresAt.IsZero() || !result.ExpiresAt.Before(time.Now())) {
				t.Fatalf("passive validation lost the expired timestamp: %+v", result)
			}
		})
	}
}

func TestGeminiOAuthExpiryBounds(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		value   string
		expires string
	}{
		{"native milliseconds upper bound", "expiry_date", "253402300799999", "9999-12-31T23:59:59.999Z"},
		{"seconds upper bound", "expires_at", "253402300799", "9999-12-31T23:59:59Z"},
		{"RFC3339 upper bound", "expiresAt", `"9999-12-31T23:59:59Z"`, "9999-12-31T23:59:59Z"},
		{"native milliseconds year 10000", "expiry_date", "253402300800000", ""},
		{"seconds year 10000", "expires_at", "253402300800", ""},
		{"int64 rounded overflow", "expiry_date", "9223372036854775807", ""},
		{"int64 overflow", "expiry_date", "9223372036854775808", ""},
		{"RFC3339 UTC year 10000", "expiresAt", `"9999-12-31T23:59:59-01:00"`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateGeminiDetection(t)
			prof := &profile.Profile{Name: "expiry", Provider: "gemini", AuthMode: "oauth", BasePath: t.TempDir()}
			data := `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","` + tc.key + `":` + tc.value + `}`
			writeGeminiFixture(t, filepath.Join(prof.HomePath(), ".gemini", "oauth_creds.json"), data)
			result, err := New().ValidateToken(context.Background(), prof, true)
			if err != nil || result == nil || result.Valid != (tc.expires != "") {
				t.Fatalf("expiry validation = %+v, %v", result, err)
			}
			if tc.expires != "" && result.ExpiresAt.UTC().Format(time.RFC3339Nano) != tc.expires {
				t.Fatalf("expiry = %s, want %s", result.ExpiresAt, tc.expires)
			}
		})
	}
}

func TestGeminiImportRejectsNonregularAndOversizedSources(t *testing.T) {
	for _, kind := range []string{"directory", "oversized"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "oauth_creds.json")
			if kind == "directory" {
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			} else {
				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}
				err = file.Truncate((16 << 20) + 1)
				closeErr := file.Close()
				if err != nil || closeErr != nil {
					t.Fatalf("prepare oversized fixture: %v, %v", err, closeErr)
				}
			}
			prof := &profile.Profile{Name: "bounded", Provider: "gemini", BasePath: t.TempDir()}
			if _, err := New().ImportAuth(context.Background(), path, prof); err == nil {
				t.Fatal("invalid source was imported")
			}
			if _, err := os.Stat(prof.HomePath()); !os.IsNotExist(err) {
				t.Fatal("invalid source created an account directory")
			}
		})
	}
}

func isolateGeminiDetection(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GEMINI_HOME", "")
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("CLOUDSDK_CONFIG", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")
	for _, key := range []string{"GEMINI_API_KEY", "GOOGLE_GENAI_USE_GCA", "GOOGLE_GENAI_USE_VERTEXAI"} {
		t.Setenv(key, "") // Register restoration before making the value absent.
		if err := os.Unsetenv(key); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(home, ".gemini")
}

func writeGeminiFixture(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
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
