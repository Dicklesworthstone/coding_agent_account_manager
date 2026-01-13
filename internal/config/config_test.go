// Package config manages global caam configuration.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestMigrateDataToCAAMHomeMerge(t *testing.T) {
	xdgData := t.TempDir()
	caamHome := t.TempDir()

	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("CAAM_HOME", caamHome)

	sourceBase := filepath.Join(xdgData, "caam")
	targetBase := filepath.Join(caamHome, "data")

	legacyPath := filepath.Join(sourceBase, "vault", "claude", "work", "auth.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatalf("MkdirAll legacyPath dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy"), 0600); err != nil {
		t.Fatalf("WriteFile legacyPath: %v", err)
	}

	targetPath := filepath.Join(targetBase, "vault", "claude", "work", "auth.json")
	if err := os.MkdirAll(filepath.Dir(targetPath), 0700); err != nil {
		t.Fatalf("MkdirAll targetPath dir: %v", err)
	}
	if err := os.WriteFile(targetPath, []byte("target"), 0600); err != nil {
		t.Fatalf("WriteFile targetPath: %v", err)
	}

	legacyNewPath := filepath.Join(sourceBase, "vault", "codex", "personal", "auth.json")
	if err := os.MkdirAll(filepath.Dir(legacyNewPath), 0700); err != nil {
		t.Fatalf("MkdirAll legacyNewPath dir: %v", err)
	}
	if err := os.WriteFile(legacyNewPath, []byte("legacy-new"), 0600); err != nil {
		t.Fatalf("WriteFile legacyNewPath: %v", err)
	}

	linkPath := filepath.Join(sourceBase, "vault", "link.json")
	linkTarget := filepath.Join("codex", "personal", "auth.json")
	symlinkCreated := true
	if err := os.Symlink(linkTarget, linkPath); err != nil {
		symlinkCreated = false
	}

	copied, err := MigrateDataToCAAMHome()
	if err != nil {
		t.Fatalf("MigrateDataToCAAMHome() error = %v", err)
	}
	if !copied {
		t.Fatal("MigrateDataToCAAMHome() copied = false, want true")
	}

	data, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile targetPath: %v", err)
	}
	if string(data) != "target" {
		t.Fatalf("targetPath content = %q, want %q", string(data), "target")
	}

	newTargetPath := filepath.Join(targetBase, "vault", "codex", "personal", "auth.json")
	data, err = os.ReadFile(newTargetPath)
	if err != nil {
		t.Fatalf("ReadFile newTargetPath: %v", err)
	}
	if string(data) != "legacy-new" {
		t.Fatalf("newTargetPath content = %q, want %q", string(data), "legacy-new")
	}

	if symlinkCreated {
		linkDest := filepath.Join(targetBase, "vault", "link.json")
		info, err := os.Lstat(linkDest)
		if err != nil {
			t.Fatalf("Lstat linkDest: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("linkDest mode = %v, want symlink", info.Mode())
		}
		destTarget, err := os.Readlink(linkDest)
		if err != nil {
			t.Fatalf("Readlink linkDest: %v", err)
		}
		if destTarget != linkTarget {
			t.Fatalf("linkDest target = %q, want %q", destTarget, linkTarget)
		}
	}
}

func TestMigrateDataToCAAMHomeNoCAAMHome(t *testing.T) {
	t.Setenv("CAAM_HOME", "")
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	copied, err := MigrateDataToCAAMHome()
	if err != nil {
		t.Fatalf("MigrateDataToCAAMHome() error = %v", err)
	}
	if copied {
		t.Fatal("MigrateDataToCAAMHome() copied = true, want false")
	}
}

func TestMigrateDataToCAAMHomeRefusesNestedTarget(t *testing.T) {
	xdgData := t.TempDir()
	caamHome := filepath.Join(xdgData, "caam", "nested")

	t.Setenv("XDG_DATA_HOME", xdgData)
	t.Setenv("CAAM_HOME", caamHome)

	if err := os.MkdirAll(filepath.Join(xdgData, "caam"), 0700); err != nil {
		t.Fatalf("MkdirAll legacy base: %v", err)
	}

	copied, err := MigrateDataToCAAMHome()
	if err == nil {
		t.Fatal("MigrateDataToCAAMHome() error = nil, want error")
	}
	if copied {
		t.Fatal("MigrateDataToCAAMHome() copied = true, want false")
	}
	if !strings.Contains(err.Error(), "refusing to migrate") {
		t.Fatalf("MigrateDataToCAAMHome() error = %q, want refusal", err.Error())
	}
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

func TestProfileKey(t *testing.T) {
	tests := []struct {
		provider string
		profile  string
		want     string
	}{
		{"claude", "work", "claude/work"},
		{"codex", "personal", "codex/personal"},
		{"gemini", "team-1", "gemini/team-1"},
	}

	for _, tt := range tests {
		got := ProfileKey(tt.provider, tt.profile)
		if got != tt.want {
			t.Errorf("ProfileKey(%q, %q) = %q, want %q", tt.provider, tt.profile, got, tt.want)
		}
	}
}

func TestAddAlias(t *testing.T) {
	cfg := DefaultConfig()

	// Add first alias
	cfg.AddAlias("claude", "work-account-1", "work")
	aliases := cfg.GetAliases("claude", "work-account-1")
	if len(aliases) != 1 || aliases[0] != "work" {
		t.Errorf("GetAliases() = %v, want [work]", aliases)
	}

	// Add second alias for same profile
	cfg.AddAlias("claude", "work-account-1", "w")
	aliases = cfg.GetAliases("claude", "work-account-1")
	if len(aliases) != 2 {
		t.Errorf("GetAliases() len = %d, want 2", len(aliases))
	}

	// Add duplicate - should not add
	cfg.AddAlias("claude", "work-account-1", "work")
	aliases = cfg.GetAliases("claude", "work-account-1")
	if len(aliases) != 2 {
		t.Errorf("GetAliases() len after duplicate = %d, want 2", len(aliases))
	}

	// Add alias for different profile
	cfg.AddAlias("claude", "personal", "home")
	aliases = cfg.GetAliases("claude", "personal")
	if len(aliases) != 1 || aliases[0] != "home" {
		t.Errorf("GetAliases(personal) = %v, want [home]", aliases)
	}
}

func TestRemoveAlias(t *testing.T) {
	cfg := &Config{
		Aliases: map[string][]string{
			"claude/work": {"w", "work"},
			"codex/dev":   {"d"},
		},
	}

	// Remove existing alias
	if !cfg.RemoveAlias("w") {
		t.Error("RemoveAlias(w) should return true")
	}

	aliases := cfg.GetAliases("claude", "work")
	if len(aliases) != 1 || aliases[0] != "work" {
		t.Errorf("GetAliases() after remove = %v, want [work]", aliases)
	}

	// Remove last alias - should remove the key
	if !cfg.RemoveAlias("work") {
		t.Error("RemoveAlias(work) should return true")
	}
	if _, exists := cfg.Aliases["claude/work"]; exists {
		t.Error("Alias key should be removed when no aliases remain")
	}

	// Remove non-existent alias
	if cfg.RemoveAlias("nonexistent") {
		t.Error("RemoveAlias(nonexistent) should return false")
	}
}

func TestResolveAlias(t *testing.T) {
	cfg := &Config{
		Aliases: map[string][]string{
			"claude/work-account":   {"work", "w"},
			"codex/dev":             {"d"},
			"gemini/team-unlimited": {"team"},
		},
	}

	tests := []struct {
		alias        string
		wantProvider string
		wantProfile  string
		wantFound    bool
	}{
		{"work", "claude", "work-account", true},
		{"w", "claude", "work-account", true},
		{"d", "codex", "dev", true},
		{"team", "gemini", "team-unlimited", true},
		{"nonexistent", "", "", false},
	}

	for _, tt := range tests {
		provider, profile, found := cfg.ResolveAlias(tt.alias)
		if found != tt.wantFound || provider != tt.wantProvider || profile != tt.wantProfile {
			t.Errorf("ResolveAlias(%q) = (%q, %q, %v), want (%q, %q, %v)",
				tt.alias, provider, profile, found, tt.wantProvider, tt.wantProfile, tt.wantFound)
		}
	}
}

func TestResolveAliasForProvider(t *testing.T) {
	cfg := &Config{
		Aliases: map[string][]string{
			"claude/work-account": {"work", "w"},
			"codex/work-codex":    {"work"},
		},
	}

	// Resolve within provider
	if got := cfg.ResolveAliasForProvider("claude", "work"); got != "work-account" {
		t.Errorf("ResolveAliasForProvider(claude, work) = %q, want work-account", got)
	}

	// Same alias, different provider
	if got := cfg.ResolveAliasForProvider("codex", "work"); got != "work-codex" {
		t.Errorf("ResolveAliasForProvider(codex, work) = %q, want work-codex", got)
	}

	// Non-existent alias
	if got := cfg.ResolveAliasForProvider("claude", "nonexistent"); got != "" {
		t.Errorf("ResolveAliasForProvider(claude, nonexistent) = %q, want empty", got)
	}
}

func TestFavorites(t *testing.T) {
	cfg := DefaultConfig()

	// Initially empty
	if favs := cfg.GetFavorites("claude"); favs != nil {
		t.Errorf("GetFavorites() = %v, want nil", favs)
	}

	// Set favorites
	cfg.SetFavorites("claude", []string{"work", "personal", "backup"})
	favs := cfg.GetFavorites("claude")
	if len(favs) != 3 {
		t.Errorf("GetFavorites() len = %d, want 3", len(favs))
	}
	if favs[0] != "work" || favs[1] != "personal" || favs[2] != "backup" {
		t.Errorf("GetFavorites() = %v, want [work personal backup]", favs)
	}

	// Check IsFavorite
	if !cfg.IsFavorite("claude", "work") {
		t.Error("IsFavorite(claude, work) should be true")
	}
	if !cfg.IsFavorite("claude", "personal") {
		t.Error("IsFavorite(claude, personal) should be true")
	}
	if cfg.IsFavorite("claude", "nonexistent") {
		t.Error("IsFavorite(claude, nonexistent) should be false")
	}
	if cfg.IsFavorite("codex", "work") {
		t.Error("IsFavorite(codex, work) should be false (different provider)")
	}

	// Clear favorites
	cfg.SetFavorites("claude", nil)
	if favs := cfg.GetFavorites("claude"); favs != nil {
		t.Errorf("GetFavorites() after clear = %v, want nil", favs)
	}
}

func TestFuzzyMatch(t *testing.T) {
	profiles := []string{
		"work-account-1",
		"work-account-2",
		"personal-gmail",
		"backup-old",
	}

	cfg := &Config{
		Aliases: map[string][]string{
			"claude/work-account-1": {"work", "w"},
			"claude/personal-gmail": {"home", "h"},
		},
	}

	tests := []struct {
		query     string
		wantLen   int
		wantFirst string
	}{
		// Exact profile name match - returns only that profile
		{"work-account-1", 1, "work-account-1"},
		// Exact alias match - short-circuits, returns only the aliased profile
		// (Aliases are shortcuts; when you type an exact alias, you want that one profile)
		{"work", 1, "work-account-1"},
		{"w", 1, "work-account-1"},
		// Alias prefix match (not exact) + profile prefix match - returns both
		{"wo", 2, "work-account-1"}, // work-account-1 via alias prefix (score 2), work-account-2 via profile prefix (score 3)
		// Profile prefix match
		{"per", 1, "personal-gmail"},
		// Substring match
		{"gmail", 1, "personal-gmail"},
		{"account", 2, "work-account-1"}, // matches work-account-1 and work-account-2
		// No match
		{"nonexistent", 0, ""},
		// Empty query returns all
		{"", 4, "work-account-1"},
	}

	for _, tt := range tests {
		matches := cfg.FuzzyMatch("claude", tt.query, profiles)
		if len(matches) != tt.wantLen {
			t.Errorf("FuzzyMatch(%q) len = %d, want %d (got %v)", tt.query, len(matches), tt.wantLen, matches)
			continue
		}
		if tt.wantLen > 0 && matches[0] != tt.wantFirst {
			t.Errorf("FuzzyMatch(%q)[0] = %q, want %q", tt.query, matches[0], tt.wantFirst)
		}
	}
}
