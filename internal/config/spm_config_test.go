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

func TestLoadSPMConfigEnvOverrides(t *testing.T) {
	origCaamHome, hadCaamHome := os.LookupEnv("CAAM_HOME")
	origRefresh, hadRefresh := os.LookupEnv("CAAM_HEALTH_REFRESH_THRESHOLD")
	defer func() {
		if hadCaamHome {
			_ = os.Setenv("CAAM_HOME", origCaamHome)
		} else {
			_ = os.Unsetenv("CAAM_HOME")
		}
		if hadRefresh {
			_ = os.Setenv("CAAM_HEALTH_REFRESH_THRESHOLD", origRefresh)
		} else {
			_ = os.Unsetenv("CAAM_HEALTH_REFRESH_THRESHOLD")
		}
	}()

	tmpDir := t.TempDir()
	if err := os.Setenv("CAAM_HOME", tmpDir); err != nil {
		t.Fatalf("Setenv(CAAM_HOME) error = %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	if err := os.Setenv("CAAM_HEALTH_REFRESH_THRESHOLD", "15m"); err != nil {
		t.Fatalf("Setenv(CAAM_HEALTH_REFRESH_THRESHOLD) error = %v", err)
	}

	cfg, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() error = %v", err)
	}

	if cfg.Health.RefreshThreshold.Duration() != 15*time.Minute {
		t.Errorf("RefreshThreshold = %v, want 15m", cfg.Health.RefreshThreshold)
	}
}

func TestLoadSPMConfigEnvOverridesInvalid(t *testing.T) {
	origCaamHome, hadCaamHome := os.LookupEnv("CAAM_HOME")
	origRefresh, hadRefresh := os.LookupEnv("CAAM_HEALTH_REFRESH_THRESHOLD")
	defer func() {
		if hadCaamHome {
			_ = os.Setenv("CAAM_HOME", origCaamHome)
		} else {
			_ = os.Unsetenv("CAAM_HOME")
		}
		if hadRefresh {
			_ = os.Setenv("CAAM_HEALTH_REFRESH_THRESHOLD", origRefresh)
		} else {
			_ = os.Unsetenv("CAAM_HEALTH_REFRESH_THRESHOLD")
		}
	}()

	tmpDir := t.TempDir()
	if err := os.Setenv("CAAM_HOME", tmpDir); err != nil {
		t.Fatalf("Setenv(CAAM_HOME) error = %v", err)
	}

	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0600); err != nil {
		t.Fatalf("WriteFile(config.yaml) error = %v", err)
	}

	if err := os.Setenv("CAAM_HEALTH_REFRESH_THRESHOLD", "-5m"); err != nil {
		t.Fatalf("Setenv(CAAM_HEALTH_REFRESH_THRESHOLD) error = %v", err)
	}

	if _, err := LoadSPMConfig(); err == nil {
		t.Fatalf("LoadSPMConfig() expected error for invalid env override")
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
			name:    "version 0",
			yaml:    `version: 0`,
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

func TestTUIConfigDefaults(t *testing.T) {
	cfg := DefaultSPMConfig()

	if cfg.TUI.Theme != "auto" {
		t.Errorf("TUI.Theme = %q, want %q", cfg.TUI.Theme, "auto")
	}
	if cfg.TUI.HighContrast {
		t.Error("TUI.HighContrast should be false by default")
	}
	if cfg.TUI.ReducedMotion {
		t.Error("TUI.ReducedMotion should be false by default")
	}
	if !cfg.TUI.Toasts {
		t.Error("TUI.Toasts should be true by default")
	}
	if !cfg.TUI.Mouse {
		t.Error("TUI.Mouse should be true by default")
	}
	if !cfg.TUI.ShowKeyHints {
		t.Error("TUI.ShowKeyHints should be true by default")
	}
	if cfg.TUI.Density != "cozy" {
		t.Errorf("TUI.Density = %q, want %q", cfg.TUI.Density, "cozy")
	}
	if cfg.TUI.NoTUI {
		t.Error("TUI.NoTUI should be false by default")
	}
}

func TestTUIConfigEnvOverrides(t *testing.T) {
	// Helper to save and restore env vars
	saveEnv := func(key string) (restore func()) {
		orig, had := os.LookupEnv(key)
		return func() {
			if had {
				_ = os.Setenv(key, orig)
			} else {
				_ = os.Unsetenv(key)
			}
		}
	}

	// Create temp config dir
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)
	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	// Create minimal config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("version: 1\n"), 0600); err != nil {
		t.Fatalf("WriteFile error = %v", err)
	}

	t.Run("CAAM_TUI_THEME", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_THEME")
		defer restore()

		os.Setenv("CAAM_TUI_THEME", "dark")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if cfg.TUI.Theme != "dark" {
			t.Errorf("TUI.Theme = %q, want %q", cfg.TUI.Theme, "dark")
		}
	})

	t.Run("CAAM_TUI_CONTRAST", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_CONTRAST")
		defer restore()

		for _, v := range []string{"high", "hc", "1", "true"} {
			os.Setenv("CAAM_TUI_CONTRAST", v)
			cfg, err := LoadSPMConfig()
			if err != nil {
				t.Fatalf("LoadSPMConfig() error = %v", err)
			}
			if !cfg.TUI.HighContrast {
				t.Errorf("CAAM_TUI_CONTRAST=%q should set HighContrast=true", v)
			}
		}
	})

	t.Run("CAAM_TUI_REDUCED_MOTION", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_REDUCED_MOTION")
		defer restore()

		os.Setenv("CAAM_TUI_REDUCED_MOTION", "true")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if !cfg.TUI.ReducedMotion {
			t.Error("TUI.ReducedMotion should be true")
		}
	})

	t.Run("REDUCED_MOTION fallback", func(t *testing.T) {
		restore := saveEnv("REDUCED_MOTION")
		defer restore()

		os.Setenv("REDUCED_MOTION", "1")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if !cfg.TUI.ReducedMotion {
			t.Error("TUI.ReducedMotion should be true from REDUCED_MOTION")
		}
	})

	t.Run("CAAM_TUI_TOASTS", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_TOASTS")
		defer restore()

		os.Setenv("CAAM_TUI_TOASTS", "false")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if cfg.TUI.Toasts {
			t.Error("TUI.Toasts should be false")
		}
	})

	t.Run("CAAM_TUI_MOUSE", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_MOUSE")
		defer restore()

		os.Setenv("CAAM_TUI_MOUSE", "0")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if cfg.TUI.Mouse {
			t.Error("TUI.Mouse should be false")
		}
	})

	t.Run("CAAM_TUI_KEY_HINTS", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_KEY_HINTS")
		defer restore()

		os.Setenv("CAAM_TUI_KEY_HINTS", "no")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if cfg.TUI.ShowKeyHints {
			t.Error("TUI.ShowKeyHints should be false")
		}
	})

	t.Run("CAAM_TUI_DENSITY", func(t *testing.T) {
		restore := saveEnv("CAAM_TUI_DENSITY")
		defer restore()

		os.Setenv("CAAM_TUI_DENSITY", "compact")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if cfg.TUI.Density != "compact" {
			t.Errorf("TUI.Density = %q, want %q", cfg.TUI.Density, "compact")
		}
	})

	t.Run("CAAM_NO_TUI", func(t *testing.T) {
		restore := saveEnv("CAAM_NO_TUI")
		defer restore()

		os.Setenv("CAAM_NO_TUI", "1")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if !cfg.TUI.NoTUI {
			t.Error("TUI.NoTUI should be true")
		}
	})

	t.Run("NO_TUI fallback", func(t *testing.T) {
		restore := saveEnv("NO_TUI")
		defer restore()

		os.Setenv("NO_TUI", "true")
		cfg, err := LoadSPMConfig()
		if err != nil {
			t.Fatalf("LoadSPMConfig() error = %v", err)
		}
		if !cfg.TUI.NoTUI {
			t.Error("TUI.NoTUI should be true from NO_TUI")
		}
	})
}

func TestTUIConfigValidation(t *testing.T) {
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
			name: "invalid theme",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
tui:
  theme: neon
`,
			wantErr: "tui.theme must be one of: auto, dark, light",
		},
		{
			name: "invalid density",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
tui:
  density: spacious
`,
			wantErr: "tui.density must be one of: cozy, compact",
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

func TestTUIConfigSaveAndLoad(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	cfg := DefaultSPMConfig()
	cfg.TUI.Theme = "light"
	cfg.TUI.HighContrast = true
	cfg.TUI.ReducedMotion = true
	cfg.TUI.Toasts = false
	cfg.TUI.Mouse = false
	cfg.TUI.ShowKeyHints = false
	cfg.TUI.Density = "compact"
	cfg.TUI.NoTUI = true

	// Save
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	loaded, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() error = %v", err)
	}

	// Verify
	if loaded.TUI.Theme != "light" {
		t.Errorf("Loaded TUI.Theme = %q, want %q", loaded.TUI.Theme, "light")
	}
	if !loaded.TUI.HighContrast {
		t.Error("Loaded TUI.HighContrast should be true")
	}
	if !loaded.TUI.ReducedMotion {
		t.Error("Loaded TUI.ReducedMotion should be true")
	}
	if loaded.TUI.Toasts {
		t.Error("Loaded TUI.Toasts should be false")
	}
	if loaded.TUI.Mouse {
		t.Error("Loaded TUI.Mouse should be false")
	}
	if loaded.TUI.ShowKeyHints {
		t.Error("Loaded TUI.ShowKeyHints should be false")
	}
	if loaded.TUI.Density != "compact" {
		t.Errorf("Loaded TUI.Density = %q, want %q", loaded.TUI.Density, "compact")
	}
	if !loaded.TUI.NoTUI {
		t.Error("Loaded TUI.NoTUI should be true")
	}
}

func TestNewConfigSectionDefaults(t *testing.T) {
	cfg := DefaultSPMConfig()

	// Test alerts defaults
	t.Run("Alerts", func(t *testing.T) {
		if !cfg.Alerts.Enabled {
			t.Error("Alerts.Enabled should be true by default")
		}
		if cfg.Alerts.WarningThreshold != 70 {
			t.Errorf("Alerts.WarningThreshold = %d, want 70", cfg.Alerts.WarningThreshold)
		}
		if cfg.Alerts.CriticalThreshold != 85 {
			t.Errorf("Alerts.CriticalThreshold = %d, want 85", cfg.Alerts.CriticalThreshold)
		}
		if !cfg.Alerts.Notifications.Terminal {
			t.Error("Alerts.Notifications.Terminal should be true by default")
		}
		if !cfg.Alerts.Notifications.Desktop {
			t.Error("Alerts.Notifications.Desktop should be true by default")
		}
		if cfg.Alerts.Notifications.Webhook != "" {
			t.Errorf("Alerts.Notifications.Webhook should be empty by default, got %q", cfg.Alerts.Notifications.Webhook)
		}
	})

	// Test handoff defaults
	t.Run("Handoff", func(t *testing.T) {
		if !cfg.Handoff.AutoTrigger {
			t.Error("Handoff.AutoTrigger should be true by default")
		}
		if cfg.Handoff.DebounceDelay.Duration() != 2*time.Second {
			t.Errorf("Handoff.DebounceDelay = %v, want 2s", cfg.Handoff.DebounceDelay)
		}
		if cfg.Handoff.MaxRetries != 1 {
			t.Errorf("Handoff.MaxRetries = %d, want 1", cfg.Handoff.MaxRetries)
		}
		if !cfg.Handoff.FallbackToManual {
			t.Error("Handoff.FallbackToManual should be true by default")
		}
	})

	// Test rate limits defaults
	t.Run("RateLimits", func(t *testing.T) {
		if len(cfg.RateLimits.Claude) == 0 {
			t.Error("RateLimits.Claude should have default patterns")
		}
		if len(cfg.RateLimits.Codex) == 0 {
			t.Error("RateLimits.Codex should have default patterns")
		}
		if len(cfg.RateLimits.Gemini) == 0 {
			t.Error("RateLimits.Gemini should have default patterns")
		}
	})

	// Test login patterns defaults
	t.Run("LoginPatterns", func(t *testing.T) {
		if len(cfg.LoginPatterns.Claude.Success) == 0 {
			t.Error("LoginPatterns.Claude.Success should have default patterns")
		}
		if len(cfg.LoginPatterns.Claude.Failure) == 0 {
			t.Error("LoginPatterns.Claude.Failure should have default patterns")
		}
	})

	// Test daemon defaults
	t.Run("Daemon", func(t *testing.T) {
		if cfg.Daemon.AuthPool.Enabled {
			t.Error("Daemon.AuthPool.Enabled should be false by default")
		}
		if cfg.Daemon.AuthPool.MaxConcurrentRefresh != 3 {
			t.Errorf("Daemon.AuthPool.MaxConcurrentRefresh = %d, want 3", cfg.Daemon.AuthPool.MaxConcurrentRefresh)
		}
		if cfg.Daemon.CheckInterval.Duration() != 5*time.Minute {
			t.Errorf("Daemon.CheckInterval = %v, want 5m", cfg.Daemon.CheckInterval)
		}
		if cfg.Daemon.RefreshThreshold.Duration() != 30*time.Minute {
			t.Errorf("Daemon.RefreshThreshold = %v, want 30m", cfg.Daemon.RefreshThreshold)
		}
	})

	// Test subscriptions defaults
	t.Run("Subscriptions", func(t *testing.T) {
		if cfg.Subscriptions == nil {
			t.Fatal("Subscriptions should not be nil by default")
		}
		sub, ok := cfg.Subscriptions["gemini"]
		if !ok {
			t.Fatal("Subscriptions should include gemini by default")
		}
		if sub.Plan != "ultra" {
			t.Errorf("Subscriptions[gemini].Plan = %q, want ultra", sub.Plan)
		}
		if sub.MonthlyCost != 275 {
			t.Errorf("Subscriptions[gemini].MonthlyCost = %v, want 275", sub.MonthlyCost)
		}
	})
}

func TestNewConfigSectionValidation(t *testing.T) {
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
			name: "alerts warning > critical",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
alerts:
  warning_threshold: 90
  critical_threshold: 70
`,
			wantErr: "alerts.warning_threshold should be <= critical_threshold",
		},
		{
			name: "alerts warning out of range",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
alerts:
  warning_threshold: 150
`,
			wantErr: "alerts.warning_threshold must be between 0 and 100",
		},
		{
			name: "negative handoff max_retries",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
handoff:
  max_retries: -1
`,
			wantErr: "handoff.max_retries cannot be negative",
		},
		{
			name: "negative subscription cost",
			yaml: `
version: 1
health:
  refresh_threshold: 10m
  warning_threshold: 1h
  penalty_decay_rate: 0.8
  penalty_decay_interval: 5m
subscriptions:
  claude:
    plan: max
    monthly_cost: -100
`,
			wantErr: "monthly_cost cannot be negative",
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

func TestNewConfigSectionLoadAndSave(t *testing.T) {
	// Save original env
	origCaamHome := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	// Create config with custom values
	cfg := DefaultSPMConfig()
	cfg.Alerts.WarningThreshold = 60
	cfg.Alerts.CriticalThreshold = 80
	cfg.Alerts.Notifications.Webhook = "https://example.com/webhook"
	cfg.Handoff.DebounceDelay = Duration(5 * time.Second)
	cfg.Handoff.MaxRetries = 3
	cfg.Daemon.AuthPool.Enabled = true
	cfg.Daemon.AuthPool.MaxConcurrentRefresh = 5
	cfg.Subscriptions = map[string]SubscriptionConfig{
		"claude": {Plan: "max", MonthlyCost: 200},
		"codex":  {Plan: "pro", MonthlyCost: 20},
	}

	// Save
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load
	loaded, err := LoadSPMConfig()
	if err != nil {
		t.Fatalf("LoadSPMConfig() error = %v", err)
	}

	// Verify new sections
	if loaded.Alerts.WarningThreshold != 60 {
		t.Errorf("Loaded Alerts.WarningThreshold = %d, want 60", loaded.Alerts.WarningThreshold)
	}
	if loaded.Alerts.Notifications.Webhook != "https://example.com/webhook" {
		t.Errorf("Loaded Alerts.Notifications.Webhook = %q, want webhook URL", loaded.Alerts.Notifications.Webhook)
	}
	if loaded.Handoff.DebounceDelay.Duration() != 5*time.Second {
		t.Errorf("Loaded Handoff.DebounceDelay = %v, want 5s", loaded.Handoff.DebounceDelay)
	}
	if loaded.Handoff.MaxRetries != 3 {
		t.Errorf("Loaded Handoff.MaxRetries = %d, want 3", loaded.Handoff.MaxRetries)
	}
	if !loaded.Daemon.AuthPool.Enabled {
		t.Error("Loaded Daemon.AuthPool.Enabled should be true")
	}
	if loaded.Daemon.AuthPool.MaxConcurrentRefresh != 5 {
		t.Errorf("Loaded Daemon.AuthPool.MaxConcurrentRefresh = %d, want 5", loaded.Daemon.AuthPool.MaxConcurrentRefresh)
	}
	if len(loaded.Subscriptions) != 3 {
		t.Errorf("Loaded Subscriptions has %d entries, want 3", len(loaded.Subscriptions))
	}
	if sub, ok := loaded.Subscriptions["claude"]; !ok || sub.MonthlyCost != 200 {
		t.Errorf("Loaded Subscriptions[claude] = %+v, want monthly_cost 200", sub)
	}
	if sub, ok := loaded.Subscriptions["gemini"]; !ok || sub.MonthlyCost != 275 {
		t.Errorf("Loaded Subscriptions[gemini] = %+v, want monthly_cost 275", sub)
	}
}
