// Package config manages global caam configuration.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg == nil {
		t.Fatal("DefaultConfig() returned nil")
	}

	// Check default values
	if cfg.DefaultProvider != "codex" {
		t.Errorf("DefaultProvider = %q, want %q", cfg.DefaultProvider, "codex")
	}

	if cfg.DefaultProfiles == nil {
		t.Error("DefaultProfiles should not be nil")
	}

	if len(cfg.DefaultProfiles) != 0 {
		t.Errorf("DefaultProfiles should be empty, got %d entries", len(cfg.DefaultProfiles))
	}

	if !cfg.AutoLock {
		t.Error("AutoLock should be true by default")
	}

	if cfg.BrowserProfile != "" {
		t.Errorf("BrowserProfile should be empty, got %q", cfg.BrowserProfile)
	}

	if cfg.Passthroughs != nil && len(cfg.Passthroughs) != 0 {
		t.Errorf("Passthroughs should be nil or empty, got %v", cfg.Passthroughs)
	}
}

func TestConfigPath(t *testing.T) {
	// Save original env
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	t.Run("with XDG_CONFIG_HOME set", func(t *testing.T) {
		tmpDir := t.TempDir()
		os.Setenv("XDG_CONFIG_HOME", tmpDir)

		path := ConfigPath()
		expected := filepath.Join(tmpDir, "caam", "config.json")

		if path != expected {
			t.Errorf("ConfigPath() = %q, want %q", path, expected)
		}
	})

	t.Run("without XDG_CONFIG_HOME", func(t *testing.T) {
		os.Setenv("XDG_CONFIG_HOME", "")

		path := ConfigPath()

		// Should contain .config/caam/config.json
		if !filepath.IsAbs(path) {
			// Fallback case - still valid
			if !contains(path, "config.json") {
				t.Errorf("ConfigPath() should end with config.json, got %q", path)
			}
		} else {
			if !contains(path, filepath.Join(".config", "caam", "config.json")) {
				t.Errorf("ConfigPath() should contain .config/caam/config.json, got %q", path)
			}
		}
	})
}

func TestLoadNonExistent(t *testing.T) {
	// Save original env
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Load from non-existent file should return default config
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}

	if cfg == nil {
		t.Fatal("Load() returned nil config")
	}

	// Should match default values
	if cfg.DefaultProvider != "codex" {
		t.Errorf("DefaultProvider = %q, want %q", cfg.DefaultProvider, "codex")
	}

	if !cfg.AutoLock {
		t.Error("AutoLock should be true")
	}
}

func TestLoadValidConfig(t *testing.T) {
	// Save original env
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create config file
	configDir := filepath.Join(tmpDir, "caam")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	testConfig := Config{
		DefaultProvider: "claude",
		DefaultProfiles: map[string]string{
			"codex":  "work-1",
			"claude": "personal",
		},
		Passthroughs:   []string{".ssh", ".gitconfig"},
		AutoLock:       false,
		BrowserProfile: "Profile 2",
	}

	data, err := json.MarshalIndent(testConfig, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal test config: %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Load config
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify all fields
	if cfg.DefaultProvider != "claude" {
		t.Errorf("DefaultProvider = %q, want %q", cfg.DefaultProvider, "claude")
	}

	if cfg.GetDefault("codex") != "work-1" {
		t.Errorf("GetDefault(codex) = %q, want %q", cfg.GetDefault("codex"), "work-1")
	}

	if cfg.GetDefault("claude") != "personal" {
		t.Errorf("GetDefault(claude) = %q, want %q", cfg.GetDefault("claude"), "personal")
	}

	if len(cfg.Passthroughs) != 2 {
		t.Errorf("Passthroughs len = %d, want 2", len(cfg.Passthroughs))
	}

	if cfg.AutoLock {
		t.Error("AutoLock should be false")
	}

	if cfg.BrowserProfile != "Profile 2" {
		t.Errorf("BrowserProfile = %q, want %q", cfg.BrowserProfile, "Profile 2")
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	// Save original env
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create invalid config file
	configDir := filepath.Join(tmpDir, "caam")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte("invalid json {{{"), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Load should fail
	_, err := Load()
	if err == nil {
		t.Error("Load() should return error for invalid JSON")
	}
}

func TestSave(t *testing.T) {
	// Save original env
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	cfg := &Config{
		DefaultProvider: "gemini",
		DefaultProfiles: map[string]string{
			"gemini": "team-1",
		},
		AutoLock:       true,
		BrowserProfile: "Work",
	}

	// Save config
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	configPath := filepath.Join(tmpDir, "caam", "config.json")
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Config file was not created")
	}

	// Verify file permissions
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("Failed to stat config file: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0600 {
		t.Errorf("Config file permissions = %o, want %o", mode, 0600)
	}

	// Verify content by loading
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() after Save() error = %v", err)
	}

	if loaded.DefaultProvider != "gemini" {
		t.Errorf("Loaded DefaultProvider = %q, want %q", loaded.DefaultProvider, "gemini")
	}

	if loaded.GetDefault("gemini") != "team-1" {
		t.Errorf("Loaded GetDefault(gemini) = %q, want %q", loaded.GetDefault("gemini"), "team-1")
	}
}

func TestSetDefault(t *testing.T) {
	cfg := DefaultConfig()

	// Set default for codex
	cfg.SetDefault("codex", "work-1")
	if cfg.GetDefault("codex") != "work-1" {
		t.Errorf("GetDefault(codex) = %q, want %q", cfg.GetDefault("codex"), "work-1")
	}

	// Override existing default
	cfg.SetDefault("codex", "work-2")
	if cfg.GetDefault("codex") != "work-2" {
		t.Errorf("GetDefault(codex) after override = %q, want %q", cfg.GetDefault("codex"), "work-2")
	}

	// Set default for different provider
	cfg.SetDefault("claude", "personal")
	if cfg.GetDefault("claude") != "personal" {
		t.Errorf("GetDefault(claude) = %q, want %q", cfg.GetDefault("claude"), "personal")
	}

	// Verify original is still set
	if cfg.GetDefault("codex") != "work-2" {
		t.Errorf("GetDefault(codex) should still be work-2, got %q", cfg.GetDefault("codex"))
	}
}

func TestSetDefaultNilMap(t *testing.T) {
	// Test SetDefault with nil DefaultProfiles map
	cfg := &Config{
		DefaultProfiles: nil,
	}

	cfg.SetDefault("codex", "test")

	if cfg.DefaultProfiles == nil {
		t.Error("SetDefault should initialize DefaultProfiles map")
	}

	if cfg.GetDefault("codex") != "test" {
		t.Errorf("GetDefault(codex) = %q, want %q", cfg.GetDefault("codex"), "test")
	}
}

func TestGetDefault(t *testing.T) {
	tests := []struct {
		name     string
		cfg      *Config
		provider string
		want     string
	}{
		{
			name:     "existing provider",
			cfg:      &Config{DefaultProfiles: map[string]string{"codex": "work"}},
			provider: "codex",
			want:     "work",
		},
		{
			name:     "non-existing provider",
			cfg:      &Config{DefaultProfiles: map[string]string{"codex": "work"}},
			provider: "claude",
			want:     "",
		},
		{
			name:     "nil map",
			cfg:      &Config{DefaultProfiles: nil},
			provider: "codex",
			want:     "",
		},
		{
			name:     "empty map",
			cfg:      &Config{DefaultProfiles: map[string]string{}},
			provider: "codex",
			want:     "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.cfg.GetDefault(tt.provider)
			if got != tt.want {
				t.Errorf("GetDefault(%q) = %q, want %q", tt.provider, got, tt.want)
			}
		})
	}
}

func TestAddPassthrough(t *testing.T) {
	cfg := DefaultConfig()

	// Add first passthrough
	cfg.AddPassthrough(".ssh")
	if len(cfg.Passthroughs) != 1 {
		t.Errorf("Passthroughs len = %d, want 1", len(cfg.Passthroughs))
	}
	if cfg.Passthroughs[0] != ".ssh" {
		t.Errorf("Passthroughs[0] = %q, want %q", cfg.Passthroughs[0], ".ssh")
	}

	// Add second passthrough
	cfg.AddPassthrough(".gitconfig")
	if len(cfg.Passthroughs) != 2 {
		t.Errorf("Passthroughs len = %d, want 2", len(cfg.Passthroughs))
	}

	// Add duplicate - should not add
	cfg.AddPassthrough(".ssh")
	if len(cfg.Passthroughs) != 2 {
		t.Errorf("Passthroughs len after duplicate = %d, want 2", len(cfg.Passthroughs))
	}
}

func TestRemovePassthrough(t *testing.T) {
	cfg := &Config{
		Passthroughs: []string{".ssh", ".gitconfig", ".npmrc"},
	}

	// Remove middle element
	cfg.RemovePassthrough(".gitconfig")
	if len(cfg.Passthroughs) != 2 {
		t.Errorf("Passthroughs len = %d, want 2", len(cfg.Passthroughs))
	}

	// Verify remaining elements
	expected := []string{".ssh", ".npmrc"}
	for i, p := range cfg.Passthroughs {
		if p != expected[i] {
			t.Errorf("Passthroughs[%d] = %q, want %q", i, p, expected[i])
		}
	}

	// Remove non-existent - should be no-op
	cfg.RemovePassthrough(".nonexistent")
	if len(cfg.Passthroughs) != 2 {
		t.Errorf("Passthroughs len after removing non-existent = %d, want 2", len(cfg.Passthroughs))
	}

	// Remove first element
	cfg.RemovePassthrough(".ssh")
	if len(cfg.Passthroughs) != 1 {
		t.Errorf("Passthroughs len = %d, want 1", len(cfg.Passthroughs))
	}
	if cfg.Passthroughs[0] != ".npmrc" {
		t.Errorf("Passthroughs[0] = %q, want %q", cfg.Passthroughs[0], ".npmrc")
	}

	// Remove last element
	cfg.RemovePassthrough(".npmrc")
	if len(cfg.Passthroughs) != 0 {
		t.Errorf("Passthroughs len = %d, want 0", len(cfg.Passthroughs))
	}
}

func TestRemovePassthroughNilSlice(t *testing.T) {
	cfg := &Config{
		Passthroughs: nil,
	}

	// Should not panic
	cfg.RemovePassthrough(".ssh")

	if cfg.Passthroughs != nil {
		t.Error("Passthroughs should still be nil")
	}
}

func TestSaveRoundtrip(t *testing.T) {
	// Save original env
	origXDG := os.Getenv("XDG_CONFIG_HOME")
	defer os.Setenv("XDG_CONFIG_HOME", origXDG)

	tmpDir := t.TempDir()
	os.Setenv("XDG_CONFIG_HOME", tmpDir)

	// Create config with all fields set
	original := &Config{
		DefaultProvider: "claude",
		DefaultProfiles: map[string]string{
			"codex":  "work-1",
			"claude": "personal",
			"gemini": "team",
		},
		Passthroughs:   []string{".ssh", ".gitconfig", ".aws"},
		AutoLock:       false,
		BrowserProfile: "Profile 3",
	}

	// Save
	if err := original.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	// Verify all fields match
	if loaded.DefaultProvider != original.DefaultProvider {
		t.Errorf("DefaultProvider = %q, want %q", loaded.DefaultProvider, original.DefaultProvider)
	}

	if loaded.AutoLock != original.AutoLock {
		t.Errorf("AutoLock = %v, want %v", loaded.AutoLock, original.AutoLock)
	}

	if loaded.BrowserProfile != original.BrowserProfile {
		t.Errorf("BrowserProfile = %q, want %q", loaded.BrowserProfile, original.BrowserProfile)
	}

	if len(loaded.DefaultProfiles) != len(original.DefaultProfiles) {
		t.Errorf("DefaultProfiles len = %d, want %d", len(loaded.DefaultProfiles), len(original.DefaultProfiles))
	}

	for k, v := range original.DefaultProfiles {
		if loaded.DefaultProfiles[k] != v {
			t.Errorf("DefaultProfiles[%q] = %q, want %q", k, loaded.DefaultProfiles[k], v)
		}
	}

	if len(loaded.Passthroughs) != len(original.Passthroughs) {
		t.Errorf("Passthroughs len = %d, want %d", len(loaded.Passthroughs), len(original.Passthroughs))
	}

	for i, p := range original.Passthroughs {
		if loaded.Passthroughs[i] != p {
			t.Errorf("Passthroughs[%d] = %q, want %q", i, loaded.Passthroughs[i], p)
		}
	}
}

// Helper function
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr, 0))
}

func containsAt(s, substr string, start int) bool {
	for i := start; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
