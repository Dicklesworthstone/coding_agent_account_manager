// Package config manages caam configuration including Smart Profile Management settings.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// SPMConfig holds Smart Profile Management configuration.
// This is stored in YAML format at ~/.caam/config.yaml
type SPMConfig struct {
	Version   int             `yaml:"version"`
	Health    HealthConfig    `yaml:"health"`
	Analytics AnalyticsConfig `yaml:"analytics"`
	Runtime   RuntimeConfig   `yaml:"runtime"`
	Project   ProjectConfig   `yaml:"project"`
	Stealth   StealthConfig   `yaml:"stealth"`
	Safety    SafetyConfig    `yaml:"safety"`
}

// HealthConfig contains health and refresh settings.
type HealthConfig struct {
	RefreshThreshold     Duration `yaml:"refresh_threshold"`      // Refresh tokens expiring within this time
	WarningThreshold     Duration `yaml:"warning_threshold"`      // Yellow status below this TTL
	PenaltyDecayRate     float64  `yaml:"penalty_decay_rate"`     // Decay multiplier (0.8 = 20% decay)
	PenaltyDecayInterval Duration `yaml:"penalty_decay_interval"` // How often to apply decay
}

// AnalyticsConfig contains activity tracking settings.
type AnalyticsConfig struct {
	Enabled                 bool `yaml:"enabled"`
	RetentionDays           int  `yaml:"retention_days"`           // Keep detailed logs
	AggregateRetentionDays  int  `yaml:"aggregate_retention_days"` // Keep aggregates longer
	CleanupOnStartup        bool `yaml:"cleanup_on_startup"`
}

// RuntimeConfig contains runtime behavior settings.
type RuntimeConfig struct {
	FileWatching   bool `yaml:"file_watching"`    // Watch profile directories for changes
	ReloadOnSIGHUP bool `yaml:"reload_on_sighup"` // Reload config on SIGHUP
	PIDFile        bool `yaml:"pid_file"`         // Write PID file when running
}

// ProjectConfig contains project-profile association settings.
type ProjectConfig struct {
	Enabled      bool `yaml:"enabled"`       // Enable project associations
	AutoActivate bool `yaml:"auto_activate"` // Auto-activate based on CWD
}

// StealthConfig contains detection mitigation settings.
// All features are opt-in (disabled by default) for power users who want speed.
type StealthConfig struct {
	SwitchDelay SwitchDelayConfig `yaml:"switch_delay"`
	Cooldown    CooldownConfig    `yaml:"cooldown"`
	Rotation    RotationConfig    `yaml:"rotation"`
}

// SwitchDelayConfig controls delays before profile switches complete.
// Adds random wait time to make switching look less automated.
type SwitchDelayConfig struct {
	Enabled       bool `yaml:"enabled"`        // Master switch for delay feature
	MinSeconds    int  `yaml:"min_seconds"`    // Minimum delay before switch
	MaxSeconds    int  `yaml:"max_seconds"`    // Maximum delay (random between min-max)
	ShowCountdown bool `yaml:"show_countdown"` // Display countdown during delay
}

// CooldownConfig controls waiting periods after accounts hit rate limits.
// Prevents suspicious pattern of immediate reuse after limit hits.
type CooldownConfig struct {
	Enabled        bool `yaml:"enabled"`          // Master switch for cooldown feature
	DefaultMinutes int  `yaml:"default_minutes"`  // Default cooldown duration
	TrackLimitHits bool `yaml:"track_limit_hits"` // Auto-track when limits are detected
}

// RotationConfig controls smart profile selection algorithms.
// Varies which accounts are used and when to reduce predictable patterns.
type RotationConfig struct {
	Enabled   bool   `yaml:"enabled"`   // Master switch for rotation feature
	Algorithm string `yaml:"algorithm"` // "smart" | "round_robin" | "random"
}

// SafetyConfig contains data safety and recovery settings.
// Ensures users can never lose their original authentication state.
type SafetyConfig struct {
	// AutoBackupBeforeSwitch controls when backups are made before profile switches.
	// "always": Backup before every switch
	// "smart": Backup only if current state doesn't match any vault profile (default)
	// "never": No automatic backups (not recommended)
	AutoBackupBeforeSwitch string `yaml:"auto_backup_before_switch"`

	// MaxAutoBackups limits the number of timestamped auto-backups to keep.
	// Older backups beyond this limit are automatically rotated out.
	// Set to 0 to keep unlimited backups.
	MaxAutoBackups int `yaml:"max_auto_backups"`
}

// Duration is a time.Duration that supports YAML marshaling/unmarshaling
// with human-readable formats like "10m", "1h", "30s".
type Duration time.Duration

// MarshalYAML implements yaml.Marshaler.
func (d Duration) MarshalYAML() (interface{}, error) {
	return time.Duration(d).String(), nil
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(value *yaml.Node) error {
	var s string
	if err := value.Decode(&s); err != nil {
		return err
	}

	dur, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", s, err)
	}

	if dur < 0 {
		return fmt.Errorf("duration cannot be negative: %s", s)
	}

	*d = Duration(dur)
	return nil
}

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}

// String returns the string representation of the duration.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// DefaultSPMConfig returns sensible defaults for Smart Profile Management.
func DefaultSPMConfig() *SPMConfig {
	return &SPMConfig{
		Version: 1,
		Health: HealthConfig{
			RefreshThreshold:     Duration(10 * time.Minute), // Refresh tokens expiring within 10 minutes
			WarningThreshold:     Duration(1 * time.Hour),    // Yellow status below 1 hour
			PenaltyDecayRate:     0.8,                        // 20% decay per interval
			PenaltyDecayInterval: Duration(5 * time.Minute),  // Every 5 minutes
		},
		Analytics: AnalyticsConfig{
			Enabled:                true,
			RetentionDays:          90,
			AggregateRetentionDays: 365,
			CleanupOnStartup:       true,
		},
		Runtime: RuntimeConfig{
			FileWatching:   true,
			ReloadOnSIGHUP: true,
			PIDFile:        true,
		},
		Project: ProjectConfig{
			Enabled:      true,
			AutoActivate: false, // Disabled by default - explicit is better
		},
		Stealth: StealthConfig{
			SwitchDelay: SwitchDelayConfig{
				Enabled:       false, // Opt-in - power users want speed
				MinSeconds:    5,
				MaxSeconds:    30,
				ShowCountdown: true,
			},
			Cooldown: CooldownConfig{
				Enabled:        false, // Opt-in
				DefaultMinutes: 60,
				TrackLimitHits: true,
			},
			Rotation: RotationConfig{
				Enabled:   false, // Opt-in
				Algorithm: "smart",
			},
		},
		Safety: SafetyConfig{
			AutoBackupBeforeSwitch: "smart", // Backup if state doesn't match any profile
			MaxAutoBackups:         5,       // Keep last 5 auto-backups
		},
	}
}

// SPMConfigPath returns the path to the SPM config file.
// Uses ~/.caam/config.yaml by default.
func SPMConfigPath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "config.yaml")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory
		return filepath.Join(".caam", "config.yaml")
	}
	return filepath.Join(homeDir, ".caam", "config.yaml")
}

// LoadSPMConfig reads the SPM configuration from disk.
// Returns defaults if the file doesn't exist.
func LoadSPMConfig() (*SPMConfig, error) {
	configPath := SPMConfigPath()

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultSPMConfig(), nil
		}
		return nil, fmt.Errorf("read SPM config: %w", err)
	}

	config := DefaultSPMConfig() // Start with defaults
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parse SPM config: %w", err)
	}

	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid SPM config: %w", err)
	}

	return config, nil
}

// Save writes the SPM configuration to disk.
func (c *SPMConfig) Save() error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}

	configPath := SPMConfigPath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal SPM config: %w", err)
	}

	// Add header comment
	header := []byte("# caam Smart Profile Management configuration\n# Documentation: https://github.com/your/caam#smart-profile-management\n\n")
	data = append(header, data...)

	// Atomic write: write to temp file then rename
	tmpPath := configPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// Validate checks that all configuration values are valid.
func (c *SPMConfig) Validate() error {
	if c.Version < 1 {
		return fmt.Errorf("version must be >= 1")
	}

	// Health validation
	if c.Health.RefreshThreshold.Duration() < 0 {
		return fmt.Errorf("health.refresh_threshold cannot be negative")
	}
	if c.Health.WarningThreshold.Duration() < 0 {
		return fmt.Errorf("health.warning_threshold cannot be negative")
	}
	if c.Health.PenaltyDecayRate < 0 || c.Health.PenaltyDecayRate > 1 {
		return fmt.Errorf("health.penalty_decay_rate must be between 0 and 1")
	}
	if c.Health.PenaltyDecayInterval.Duration() < time.Minute {
		return fmt.Errorf("health.penalty_decay_interval must be at least 1 minute")
	}

	// Analytics validation
	if c.Analytics.RetentionDays < 0 {
		return fmt.Errorf("analytics.retention_days cannot be negative")
	}
	if c.Analytics.AggregateRetentionDays < 0 {
		return fmt.Errorf("analytics.aggregate_retention_days cannot be negative")
	}
	if c.Analytics.AggregateRetentionDays < c.Analytics.RetentionDays {
		return fmt.Errorf("analytics.aggregate_retention_days should be >= retention_days")
	}

	// Stealth validation
	if c.Stealth.SwitchDelay.MinSeconds < 0 {
		return fmt.Errorf("stealth.switch_delay.min_seconds cannot be negative")
	}
	if c.Stealth.SwitchDelay.MaxSeconds < 0 {
		return fmt.Errorf("stealth.switch_delay.max_seconds cannot be negative")
	}
	if c.Stealth.SwitchDelay.MinSeconds > c.Stealth.SwitchDelay.MaxSeconds {
		return fmt.Errorf("stealth.switch_delay.min_seconds cannot be greater than max_seconds")
	}
	if c.Stealth.Cooldown.DefaultMinutes < 0 {
		return fmt.Errorf("stealth.cooldown.default_minutes cannot be negative")
	}
	validAlgorithms := map[string]bool{"smart": true, "round_robin": true, "random": true}
	if c.Stealth.Rotation.Algorithm != "" && !validAlgorithms[c.Stealth.Rotation.Algorithm] {
		return fmt.Errorf("stealth.rotation.algorithm must be one of: smart, round_robin, random")
	}

	// Safety validation
	validBackupModes := map[string]bool{"always": true, "smart": true, "never": true}
	if c.Safety.AutoBackupBeforeSwitch != "" && !validBackupModes[c.Safety.AutoBackupBeforeSwitch] {
		return fmt.Errorf("safety.auto_backup_before_switch must be one of: always, smart, never")
	}
	if c.Safety.MaxAutoBackups < 0 {
		return fmt.Errorf("safety.max_auto_backups cannot be negative")
	}

	return nil
}

// GetRefreshThreshold returns the token refresh threshold.
func (c *SPMConfig) GetRefreshThreshold() time.Duration {
	return c.Health.RefreshThreshold.Duration()
}

// GetWarningThreshold returns the health warning threshold.
func (c *SPMConfig) GetWarningThreshold() time.Duration {
	return c.Health.WarningThreshold.Duration()
}

// ShouldRefresh returns true if a token expiring at the given time needs refresh.
func (c *SPMConfig) ShouldRefresh(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return time.Until(expiresAt) < c.GetRefreshThreshold()
}

// NeedsWarning returns true if a token expiring at the given time should show warning status.
func (c *SPMConfig) NeedsWarning(expiresAt time.Time) bool {
	if expiresAt.IsZero() {
		return false
	}
	return time.Until(expiresAt) < c.GetWarningThreshold()
}
