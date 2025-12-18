package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestDefaultSPMConfig(t *testing.T) {
	cfg := DefaultSPMConfig()

	if cfg == nil {
		t.Fatal("DefaultSPMConfig() returned nil")
	}

	// Check version
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}

	// Check health defaults
	if cfg.Health.RefreshThreshold.Duration() != 10*time.Minute {
		t.Errorf("RefreshThreshold = %v, want 10m", cfg.Health.RefreshThreshold)
	}
	if cfg.Health.WarningThreshold.Duration() != time.Hour {
		t.Errorf("WarningThreshold = %v, want 1h", cfg.Health.WarningThreshold)
	}
	if cfg.Health.PenaltyDecayRate != 0.8 {
		t.Errorf("PenaltyDecayRate = %v, want 0.8", cfg.Health.PenaltyDecayRate)
	}
	if cfg.Health.PenaltyDecayInterval.Duration() != 5*time.Minute {
		t.Errorf("PenaltyDecayInterval = %v, want 5m", cfg.Health.PenaltyDecayInterval)
	}

	// Check analytics defaults
	if !cfg.Analytics.Enabled {
		t.Error("Analytics.Enabled should be true by default")
	}
	if cfg.Analytics.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", cfg.Analytics.RetentionDays)
	}
	if cfg.Analytics.AggregateRetentionDays != 365 {
		t.Errorf("AggregateRetentionDays = %d, want 365", cfg.Analytics.AggregateRetentionDays)
	}
	if !cfg.Analytics.CleanupOnStartup {
		t.Error("CleanupOnStartup should be true by default")
	}

	// Check runtime defaults
	if !cfg.Runtime.FileWatching {
		t.Error("FileWatching should be true by default")
	}
	if !cfg.Runtime.ReloadOnSIGHUP {
		t.Error("ReloadOnSIGHUP should be true by default")
	}
	if !cfg.Runtime.PIDFile {
		t.Error("PIDFile should be true by default")
	}

	// Check project defaults
	if !cfg.Project.Enabled {
		t.Error("Project.Enabled should be true by default")
	}
	if cfg.Project.AutoActivate {
		t.Error("Project.AutoActivate should be false by default")
	}

	// Check stealth defaults - all features disabled by default (opt-in)
	if cfg.Stealth.SwitchDelay.Enabled {
		t.Error("Stealth.SwitchDelay.Enabled should be false by default")
	}
	if cfg.Stealth.SwitchDelay.MinSeconds != 5 {
		t.Errorf("Stealth.SwitchDelay.MinSeconds = %d, want 5", cfg.Stealth.SwitchDelay.MinSeconds)
	}
	if cfg.Stealth.SwitchDelay.MaxSeconds != 30 {
		t.Errorf("Stealth.SwitchDelay.MaxSeconds = %d, want 30", cfg.Stealth.SwitchDelay.MaxSeconds)
	}
	if !cfg.Stealth.SwitchDelay.ShowCountdown {
		t.Error("Stealth.SwitchDelay.ShowCountdown should be true by default")
	}

	if cfg.Stealth.Cooldown.Enabled {
		t.Error("Stealth.Cooldown.Enabled should be false by default")
	}
	if cfg.Stealth.Cooldown.DefaultMinutes != 60 {
		t.Errorf("Stealth.Cooldown.DefaultMinutes = %d, want 60", cfg.Stealth.Cooldown.DefaultMinutes)
	}
	if !cfg.Stealth.Cooldown.TrackLimitHits {
		t.Error("Stealth.Cooldown.TrackLimitHits should be true by default")
	}

	if cfg.Stealth.Rotation.Enabled {
		t.Error("Stealth.Rotation.Enabled should be false by default")
	}
	if cfg.Stealth.Rotation.Algorithm != "smart" {
		t.Errorf("Stealth.Rotation.Algorithm = %q, want %q", cfg.Stealth.Rotation.Algorithm, "smart")
	}

	// Check safety defaults
	if cfg.Safety.AutoBackupBeforeSwitch != "smart" {
		t.Errorf("Safety.AutoBackupBeforeSwitch = %q, want %q", cfg.Safety.AutoBackupBeforeSwitch, "smart")
	}
	if cfg.Safety.MaxAutoBackups != 5 {
		t.Errorf("Safety.MaxAutoBackups = %d, want 5", cfg.Safety.MaxAutoBackups)
	}
}

func TestSPMConfigPath(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	t.Run("with CAAM_HOME set", func(t *testing.T) {
		tmpDir := t.TempDir()
		os.Setenv("CAAM_HOME", tmpDir)

		path := SPMConfigPath()
		expected := filepath.Join(tmpDir, "config.yaml")

		if path != expected {
			t.Errorf("SPMConfigPath() = %q, want %q", path, expected)
		}
	})

	t.Run("without CAAM_HOME", func(t *testing.T) {
		os.Setenv("CAAM_HOME", "")

		path := SPMConfigPath()

		// Should contain .caam/config.yaml
		if !filepath.IsAbs(path) {
			// Fallback case
			if filepath.Base(path) != "config.yaml" {
				t.Errorf("SPMConfigPath() should end with config.yaml, got %q", path)
			}
		} else {
			if !contains(path, filepath.Join(".caam", "config.yaml")) {
				t.Errorf("SPMConfigPath() should contain .caam/config.yaml, got %q", path)
			}
		}
	})
}

func TestLoadSPMConfigNonExistent(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	// Load from non-existent file should return default config
	cfg, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() error = %v, want nil", err)
	}

	if cfg == nil {
		t.Fatal("LoadSPMConfig() returned nil config")
	}

	// Should match defaults
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
}

func TestLoadSPMConfigValidYAML(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	// Create config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	yamlContent := `
version: 1
health:
  refresh_threshold: 5m
  warning_threshold: 30m
  penalty_decay_rate: 0.9
  penalty_decay_interval: 10m
analytics:
  enabled: false
  retention_days: 30
  aggregate_retention_days: 180
  cleanup_on_startup: false
runtime:
  file_watching: false
  reload_on_sighup: false
  pid_file: false
project:
  enabled: false
  auto_activate: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Load config
	cfg, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() error = %v", err)
	}

	// Verify all fields
	if cfg.Health.RefreshThreshold.Duration() != 5*time.Minute {
		t.Errorf("RefreshThreshold = %v, want 5m", cfg.Health.RefreshThreshold)
	}
	if cfg.Health.WarningThreshold.Duration() != 30*time.Minute {
		t.Errorf("WarningThreshold = %v, want 30m", cfg.Health.WarningThreshold)
	}
	if cfg.Health.PenaltyDecayRate != 0.9 {
		t.Errorf("PenaltyDecayRate = %v, want 0.9", cfg.Health.PenaltyDecayRate)
	}
	if cfg.Health.PenaltyDecayInterval.Duration() != 10*time.Minute {
		t.Errorf("PenaltyDecayInterval = %v, want 10m", cfg.Health.PenaltyDecayInterval)
	}

	if cfg.Analytics.Enabled {
		t.Error("Analytics.Enabled should be false")
	}
	if cfg.Analytics.RetentionDays != 30 {
		t.Errorf("RetentionDays = %d, want 30", cfg.Analytics.RetentionDays)
	}
	if cfg.Analytics.AggregateRetentionDays != 180 {
		t.Errorf("AggregateRetentionDays = %d, want 180", cfg.Analytics.AggregateRetentionDays)
	}
	if cfg.Analytics.CleanupOnStartup {
		t.Error("CleanupOnStartup should be false")
	}

	if cfg.Runtime.FileWatching {
		t.Error("FileWatching should be false")
	}
	if cfg.Runtime.ReloadOnSIGHUP {
		t.Error("ReloadOnSIGHUP should be false")
	}
	if cfg.Runtime.PIDFile {
		t.Error("PIDFile should be false")
	}

	if cfg.Project.Enabled {
		t.Error("Project.Enabled should be false")
	}
	if !cfg.Project.AutoActivate {
		t.Error("Project.AutoActivate should be true")
	}
}

func TestLoadSPMConfigInvalidYAML(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	// Create invalid config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("invalid yaml {{{}"), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Load should fail
	_, err := LoadSPMConfig()
	if err == nil {
		t.Error("LoadSPMConfig() should return error for invalid YAML")
	}
}

func TestLoadSPMConfigInvalidValues(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{
			name: "negative decay rate",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: -0.5
  penalty_decay_interval: 5m
`,
			wantErr: "penalty_decay_rate must be between 0 and 1",
		},
		{
			name: "decay rate > 1",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 1.5
  penalty_decay_interval: 5m
`,
			wantErr: "penalty_decay_rate must be between 0 and 1",
		},
		{
			name: "decay interval too short",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 30s
`,
			wantErr: "penalty_decay_interval must be at least 1 minute",
		},
		{
			name: "aggregate retention < retention",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
analytics:
  retention_days: 90
  aggregate_retention_days: 30
`,
			wantErr: "aggregate_retention_days should be >= retention_days",
		},
		{
			name: "version 0",
			yaml: `version: 0`,
			wantErr: "version must be >= 1",
		},
		{
			name: "negative switch delay min",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
stealth:
  switch_delay:
    min_seconds: -5
`,
			wantErr: "stealth.switch_delay.min_seconds cannot be negative",
		},
		{
			name: "negative switch delay max",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
stealth:
  switch_delay:
    max_seconds: -10
`,
			wantErr: "stealth.switch_delay.max_seconds cannot be negative",
		},
		{
			name: "min > max switch delay",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
stealth:
  switch_delay:
    min_seconds: 60
    max_seconds: 30
`,
			wantErr: "stealth.switch_delay.min_seconds cannot be greater than max_seconds",
		},
		{
			name: "negative cooldown minutes",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
stealth:
  cooldown:
    default_minutes: -30
`,
			wantErr: "stealth.cooldown.default_minutes cannot be negative",
		},
		{
			name: "invalid rotation algorithm",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
stealth:
  rotation:
    algorithm: invalid_algo
`,
			wantErr: "stealth.rotation.algorithm must be one of: smart, round_robin, random",
		},
		{
			name: "invalid backup mode",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
safety:
  auto_backup_before_switch: invalid_mode
`,
			wantErr: "safety.auto_backup_before_switch must be one of: always, smart, never",
		},
		{
			name: "negative max auto backups",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
safety:
  max_auto_backups: -5
`,
			wantErr: "safety.max_auto_backups cannot be negative",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			configPath := filepath.Join(tmpDir, "config.yaml")
			if err := os.WriteFile(configPath, []byte(tc.yaml), 0600); err != nil {
				t.Fatalf("Failed to write config file: %v", err)
			}

			_, err := LoadSPMConfig()
			if err == nil {
				t.Errorf("LoadSPMConfig() should return error")
			} else if !contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestSPMConfigSave(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	cfg := &SPMConfig{
		Version: 1,
		Health: HealthConfig{
			RefreshThreshold:     Duration(15 * time.Minute),
			WarningThreshold:     Duration(2 * time.Hour),
			PenaltyDecayRate:     0.75,
			PenaltyDecayInterval: Duration(10 * time.Minute),
		},
		Analytics: AnalyticsConfig{
			Enabled:                false,
			RetentionDays:          60,
			AggregateRetentionDays: 120,
			CleanupOnStartup:       false,
		},
		Runtime: RuntimeConfig{
			FileWatching:   false,
			ReloadOnSIGHUP: true,
			PIDFile:        false,
		},
		Project: ProjectConfig{
			Enabled:      true,
			AutoActivate: true,
		},
		Stealth: StealthConfig{
			SwitchDelay: SwitchDelayConfig{
				Enabled:       true,
				MinSeconds:    10,
				MaxSeconds:    60,
				ShowCountdown: false,
			},
			Cooldown: CooldownConfig{
				Enabled:        true,
				DefaultMinutes: 120,
				TrackLimitHits: false,
			},
			Rotation: RotationConfig{
				Enabled:   true,
				Algorithm: "round_robin",
			},
		},
		Safety: SafetyConfig{
			AutoBackupBeforeSwitch: "always",
			MaxAutoBackups:         10,
		},
	}

	// Save config
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Verify file exists
	configPath := filepath.Join(tmpDir, "config.yaml")
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
	loaded, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() after Save() error = %v", err)
	}

	if loaded.Health.RefreshThreshold.Duration() != 15*time.Minute {
		t.Errorf("Loaded RefreshThreshold = %v, want 15m", loaded.Health.RefreshThreshold)
	}
	if loaded.Health.PenaltyDecayRate != 0.75 {
		t.Errorf("Loaded PenaltyDecayRate = %v, want 0.75", loaded.Health.PenaltyDecayRate)
	}
	if loaded.Analytics.RetentionDays != 60 {
		t.Errorf("Loaded RetentionDays = %d, want 60", loaded.Analytics.RetentionDays)
	}
	if loaded.Project.AutoActivate != true {
		t.Error("Loaded AutoActivate should be true")
	}

	// Verify stealth config was saved and loaded correctly
	if !loaded.Stealth.SwitchDelay.Enabled {
		t.Error("Loaded Stealth.SwitchDelay.Enabled should be true")
	}
	if loaded.Stealth.SwitchDelay.MinSeconds != 10 {
		t.Errorf("Loaded Stealth.SwitchDelay.MinSeconds = %d, want 10", loaded.Stealth.SwitchDelay.MinSeconds)
	}
	if loaded.Stealth.SwitchDelay.MaxSeconds != 60 {
		t.Errorf("Loaded Stealth.SwitchDelay.MaxSeconds = %d, want 60", loaded.Stealth.SwitchDelay.MaxSeconds)
	}
	if loaded.Stealth.SwitchDelay.ShowCountdown {
		t.Error("Loaded Stealth.SwitchDelay.ShowCountdown should be false")
	}
	if !loaded.Stealth.Cooldown.Enabled {
		t.Error("Loaded Stealth.Cooldown.Enabled should be true")
	}
	if loaded.Stealth.Cooldown.DefaultMinutes != 120 {
		t.Errorf("Loaded Stealth.Cooldown.DefaultMinutes = %d, want 120", loaded.Stealth.Cooldown.DefaultMinutes)
	}
	if loaded.Stealth.Cooldown.TrackLimitHits {
		t.Error("Loaded Stealth.Cooldown.TrackLimitHits should be false")
	}
	if !loaded.Stealth.Rotation.Enabled {
		t.Error("Loaded Stealth.Rotation.Enabled should be true")
	}
	if loaded.Stealth.Rotation.Algorithm != "round_robin" {
		t.Errorf("Loaded Stealth.Rotation.Algorithm = %q, want %q", loaded.Stealth.Rotation.Algorithm, "round_robin")
	}

	// Verify safety config was saved and loaded correctly
	if loaded.Safety.AutoBackupBeforeSwitch != "always" {
		t.Errorf("Loaded Safety.AutoBackupBeforeSwitch = %q, want %q", loaded.Safety.AutoBackupBeforeSwitch, "always")
	}
	if loaded.Safety.MaxAutoBackups != 10 {
		t.Errorf("Loaded Safety.MaxAutoBackups = %d, want 10", loaded.Safety.MaxAutoBackups)
	}
}

func TestSPMConfigSaveValidation(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	cfg := &SPMConfig{
		Version: 1,
		Health: HealthConfig{
			RefreshThreshold:     Duration(10 * time.Minute),
			WarningThreshold:     Duration(1 * time.Hour),
			PenaltyDecayRate:     1.5, // Invalid
			PenaltyDecayInterval: Duration(5 * time.Minute),
		},
	}

	err := cfg.Save()
	if err == nil {
		t.Error("Save() should return validation error")
	}
}

func TestDurationMarshalUnmarshal(t *testing.T) {
	tests := []struct {
		name     string
		duration Duration
		yamlStr  string
	}{
		{"zero", Duration(0), "0s"},
		{"seconds", Duration(30 * time.Second), "30s"},
		{"minutes", Duration(5 * time.Minute), "5m0s"},
		{"hours", Duration(2 * time.Hour), "2h0m0s"},
		{"complex", Duration(1*time.Hour + 30*time.Minute), "1h30m0s"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Marshal
			data, err := yaml.Marshal(tc.duration)
			if err != nil {
				t.Fatalf("Marshal error: %v", err)
			}

			// Unmarshal
			var result Duration
			if err := yaml.Unmarshal(data, &result); err != nil {
				t.Fatalf("Unmarshal error: %v", err)
			}

			if result != tc.duration {
				t.Errorf("Roundtrip: got %v, want %v", result, tc.duration)
			}
		})
	}
}

func TestDurationUnmarshalInvalid(t *testing.T) {
	tests := []struct {
		name    string
		yaml    string
		wantErr bool
	}{
		{"invalid format", "invalid", true},
		{"negative", "-5m", true},
		// Note: empty string parses to "0s" which is valid
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var d Duration
			err := yaml.Unmarshal([]byte(tc.yaml), &d)
			if (err != nil) != tc.wantErr {
				t.Errorf("Unmarshal(%q) error = %v, wantErr = %v", tc.yaml, err, tc.wantErr)
			}
		})
	}
}

func TestSPMConfigHelpers(t *testing.T) {
	cfg := &SPMConfig{
		Version: 1,
		Health: HealthConfig{
			RefreshThreshold: Duration(10 * time.Minute),
			WarningThreshold: Duration(1 * time.Hour),
		},
	}

	t.Run("GetRefreshThreshold", func(t *testing.T) {
		if cfg.GetRefreshThreshold() != 10*time.Minute {
			t.Errorf("GetRefreshThreshold() = %v, want 10m", cfg.GetRefreshThreshold())
		}
	})

	t.Run("GetWarningThreshold", func(t *testing.T) {
		if cfg.GetWarningThreshold() != time.Hour {
			t.Errorf("GetWarningThreshold() = %v, want 1h", cfg.GetWarningThreshold())
		}
	})

	t.Run("ShouldRefresh", func(t *testing.T) {
		// Expiring in 5 minutes - should refresh
		expiresIn5m := time.Now().Add(5 * time.Minute)
		if !cfg.ShouldRefresh(expiresIn5m) {
			t.Error("ShouldRefresh(5m) should be true")
		}

		// Expiring in 15 minutes - should not refresh
		expiresIn15m := time.Now().Add(15 * time.Minute)
		if cfg.ShouldRefresh(expiresIn15m) {
			t.Error("ShouldRefresh(15m) should be false")
		}

		// Zero time - should not refresh
		if cfg.ShouldRefresh(time.Time{}) {
			t.Error("ShouldRefresh(zero) should be false")
		}
	})

	t.Run("NeedsWarning", func(t *testing.T) {
		// Expiring in 30 minutes - should warn
		expiresIn30m := time.Now().Add(30 * time.Minute)
		if !cfg.NeedsWarning(expiresIn30m) {
			t.Error("NeedsWarning(30m) should be true")
		}

		// Expiring in 2 hours - should not warn
		expiresIn2h := time.Now().Add(2 * time.Hour)
		if cfg.NeedsWarning(expiresIn2h) {
			t.Error("NeedsWarning(2h) should be false")
		}

		// Zero time - should not warn
		if cfg.NeedsWarning(time.Time{}) {
			t.Error("NeedsWarning(zero) should be false")
		}
	})
}

func TestSPMConfigForwardCompatibility(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	// Create config file with unknown fields (simulating future version)
	configPath := filepath.Join(tmpDir, "config.yaml")
	yamlContent := `
version: 1
health:
  refresh_threshold: 5m
  warning_threshold: 30m
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
  future_field: some_value
analytics:
  enabled: true
  retention_days: 90
  aggregate_retention_days: 365
  cleanup_on_startup: true
future_section:
  unknown_setting: true
`
	if err := os.WriteFile(configPath, []byte(yamlContent), 0600); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	// Load should succeed, ignoring unknown fields
	cfg, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() should succeed with unknown fields, got error: %v", err)
	}

	// Known fields should be parsed correctly
	if cfg.Health.RefreshThreshold.Duration() != 5*time.Minute {
		t.Errorf("RefreshThreshold = %v, want 5m", cfg.Health.RefreshThreshold)
	}
}
