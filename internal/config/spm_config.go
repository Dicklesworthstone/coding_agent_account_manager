// Package config manages caam configuration including Smart Profile Management settings.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// SPMConfig holds Smart Profile Management configuration.
// This is stored in YAML format at ~/.caam/config.yaml
type SPMConfig struct {
	Version       int                        `yaml:"version"`
	Health        HealthConfig               `yaml:"health"`
	Analytics     AnalyticsConfig            `yaml:"analytics"`
	Runtime       RuntimeConfig              `yaml:"runtime"`
	Project       ProjectConfig              `yaml:"project"`
	Stealth       StealthConfig              `yaml:"stealth"`
	Safety        SafetyConfig               `yaml:"safety"`
	Alerts        AlertConfig                `yaml:"alerts"`
	Handoff       HandoffConfig              `yaml:"handoff"`
	RateLimits    RateLimitPatternsConfig    `yaml:"rate_limits"`
	LoginPatterns LoginPatternsConfig        `yaml:"login_patterns"`
	Subscriptions map[string]SubscriptionConfig `yaml:"subscriptions,omitempty"`
	Daemon        DaemonConfig               `yaml:"daemon"`
	TUI           TUIConfig                  `yaml:"tui"`
}

// TUIConfig holds TUI appearance and behavior preferences.
// Settings can be overridden by environment variables (higher priority)
// or CLI flags (highest priority).
type TUIConfig struct {
	// Theme controls the color scheme: "auto" (default), "dark", or "light".
	// Environment override: CAAM_TUI_THEME
	Theme string `yaml:"theme"`

	// HighContrast enables high-contrast colors for better accessibility.
	// Environment override: CAAM_TUI_CONTRAST=high
	HighContrast bool `yaml:"high_contrast"`

	// ReducedMotion disables animated UI effects like spinners.
	// Environment override: CAAM_TUI_REDUCED_MOTION or REDUCED_MOTION
	ReducedMotion bool `yaml:"reduced_motion"`

	// Toasts enables transient notification messages.
	// Environment override: CAAM_TUI_TOASTS
	Toasts bool `yaml:"toasts"`

	// Mouse enables mouse support in the TUI.
	// Environment override: CAAM_TUI_MOUSE
	Mouse bool `yaml:"mouse"`

	// ShowKeyHints shows keyboard shortcut hints in the status bar.
	// Environment override: CAAM_TUI_KEY_HINTS
	ShowKeyHints bool `yaml:"show_key_hints"`

	// Density controls spacing: "cozy" (default) or "compact".
	// Environment override: CAAM_TUI_DENSITY
	Density string `yaml:"density"`

	// NoTUI disables the TUI entirely, using plain text output instead.
	// Environment override: CAAM_NO_TUI or NO_TUI
	NoTUI bool `yaml:"no_tui"`
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
	Enabled                bool `yaml:"enabled"`
	RetentionDays          int  `yaml:"retention_days"`           // Keep detailed logs
	AggregateRetentionDays int  `yaml:"aggregate_retention_days"` // Keep aggregates longer
	CleanupOnStartup       bool `yaml:"cleanup_on_startup"`
}

// RuntimeConfig contains runtime behavior settings.
type RuntimeConfig struct {
	FileWatching   bool   `yaml:"file_watching"`    // Watch profile directories for changes
	ReloadOnSIGHUP bool   `yaml:"reload_on_sighup"` // Reload config on SIGHUP
	PIDFile        bool   `yaml:"pid_file"`         // Write PID file when running
	PIDFilePath    string `yaml:"pid_file_path"`    // Custom path for PID file
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

// AlertConfig controls alert and notification settings.
type AlertConfig struct {
	Enabled           bool               `yaml:"enabled"`
	WarningThreshold  int                `yaml:"warning_threshold"`  // Percentage of usage before warning (0-100)
	CriticalThreshold int                `yaml:"critical_threshold"` // Percentage of usage before critical (0-100)
	Notifications     NotificationConfig `yaml:"notifications"`
}

// NotificationConfig controls how alerts are delivered.
type NotificationConfig struct {
	Terminal bool   `yaml:"terminal"` // Show alerts in terminal
	Desktop  bool   `yaml:"desktop"`  // Show desktop notifications (if available)
	Webhook  string `yaml:"webhook"`  // Optional webhook URL for alerts
}

// HandoffConfig controls smart session handoff behavior.
type HandoffConfig struct {
	AutoTrigger      bool     `yaml:"auto_trigger"`       // Automatically trigger handoff on rate limit
	DebounceDelay    Duration `yaml:"debounce_delay"`     // Wait before triggering handoff
	MaxRetries       int      `yaml:"max_retries"`        // Max handoff attempts per session
	FallbackToManual bool     `yaml:"fallback_to_manual"` // Show manual instructions on failure
}

// RateLimitPatternsConfig holds rate limit detection patterns per provider.
type RateLimitPatternsConfig struct {
	Claude []string `yaml:"claude,omitempty"`
	Codex  []string `yaml:"codex,omitempty"`
	Gemini []string `yaml:"gemini,omitempty"`
}

// LoginPatternsConfig holds login success/failure patterns per provider.
type LoginPatternsConfig struct {
	Claude ProviderLoginPatterns `yaml:"claude,omitempty"`
	Codex  ProviderLoginPatterns `yaml:"codex,omitempty"`
	Gemini ProviderLoginPatterns `yaml:"gemini,omitempty"`
}

// ProviderLoginPatterns holds success and failure patterns for a provider.
type ProviderLoginPatterns struct {
	Success []string `yaml:"success,omitempty"`
	Failure []string `yaml:"failure,omitempty"`
}

// SubscriptionConfig holds subscription cost information for a provider.
type SubscriptionConfig struct {
	Plan        string  `yaml:"plan"`         // e.g., "max", "pro", "free"
	MonthlyCost float64 `yaml:"monthly_cost"` // Monthly cost in USD
}

// DaemonConfig holds daemon-specific settings.
type DaemonConfig struct {
	AuthPool         AuthPoolConfig `yaml:"auth_pool"`
	CheckInterval    Duration       `yaml:"check_interval"`
	RefreshThreshold Duration       `yaml:"refresh_threshold"`
	Verbose          bool           `yaml:"verbose"`
}

// AuthPoolConfig holds auth pool settings.
type AuthPoolConfig struct {
	Enabled               bool     `yaml:"enabled"`
	MaxConcurrentRefresh  int      `yaml:"max_concurrent_refresh"`
	RefreshRetryDelay     Duration `yaml:"refresh_retry_delay"`
	MaxRefreshRetries     int      `yaml:"max_refresh_retries"`
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

// MarshalJSON converts Duration to a JSON string like "30s" or "5m".
func (d Duration) MarshalJSON() ([]byte, error) {
	return []byte(`"` + time.Duration(d).String() + `"`), nil
}

// UnmarshalJSON parses a duration string like "30s" or "5m".
func (d *Duration) UnmarshalJSON(b []byte) error {
	// Remove quotes
	if len(b) < 2 {
		*d = 0
		return nil
	}
	s := string(b[1 : len(b)-1])
	if s == "" {
		*d = 0
		return nil
	}

	dur, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(dur)
	return nil
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
		Alerts: AlertConfig{
			Enabled:           true,
			WarningThreshold:  70,  // 70% usage triggers warning
			CriticalThreshold: 85,  // 85% usage triggers critical
			Notifications: NotificationConfig{
				Terminal: true,
				Desktop:  true,
				Webhook:  "",
			},
		},
		Handoff: HandoffConfig{
			AutoTrigger:      true,                       // Auto-trigger by default
			DebounceDelay:    Duration(2 * time.Second),  // Wait 2s before triggering
			MaxRetries:       1,                          // One retry per session
			FallbackToManual: true,                       // Show manual instructions on failure
		},
		RateLimits: RateLimitPatternsConfig{
			Claude: []string{
				"rate limit",
				"usage limit reached",
				"too many requests",
				"429",
			},
			Codex: []string{
				"rate limit exceeded",
				"quota exceeded",
				"too many requests",
			},
			Gemini: []string{
				"RESOURCE_EXHAUSTED",
				"quota exceeded",
				"rate limit",
			},
		},
		LoginPatterns: LoginPatternsConfig{
			Claude: ProviderLoginPatterns{
				Success: []string{
					"successfully logged in",
					"authenticated",
					"login complete",
				},
				Failure: []string{
					"authentication failed",
					"invalid token",
					"expired",
					"login failed",
				},
			},
			Codex: ProviderLoginPatterns{
				Success: []string{
					"logged in",
					"authentication successful",
				},
				Failure: []string{
					"authentication failed",
					"login failed",
				},
			},
			Gemini: ProviderLoginPatterns{
				Success: []string{
					"authenticated",
					"logged in",
				},
				Failure: []string{
					"authentication failed",
					"invalid credentials",
				},
			},
		},
		Subscriptions: map[string]SubscriptionConfig{
			"gemini": {Plan: "ultra", MonthlyCost: 275},
		},
		Daemon: DaemonConfig{
			AuthPool: AuthPoolConfig{
				Enabled:              false, // Opt-in
				MaxConcurrentRefresh: 3,
				RefreshRetryDelay:    Duration(30 * time.Second),
				MaxRefreshRetries:    3,
			},
			CheckInterval:    Duration(5 * time.Minute),
			RefreshThreshold: Duration(30 * time.Minute),
			Verbose:          false,
		},
		TUI: TUIConfig{
			Theme:         "auto",
			HighContrast:  false,
			ReducedMotion: false,
			Toasts:        true,
			Mouse:         true,
			ShowKeyHints:  true,
			Density:       "cozy",
			NoTUI:         false,
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

	config.ApplyEnvOverrides()

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
	header := []byte("# caam Smart Profile Management configuration\n# Documentation: https://github.com/Dicklesworthstone/coding_agent_account_manager/blob/main/docs/SMART_PROFILE_MANAGEMENT.md\n\n")
	data = append(header, data...)

	// Atomic write: write to temp file, fsync, then rename
	tmpPath := configPath + ".tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	// Sync to disk before rename to ensure durability
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
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

	// Alerts validation
	if c.Alerts.WarningThreshold < 0 || c.Alerts.WarningThreshold > 100 {
		return fmt.Errorf("alerts.warning_threshold must be between 0 and 100")
	}
	if c.Alerts.CriticalThreshold < 0 || c.Alerts.CriticalThreshold > 100 {
		return fmt.Errorf("alerts.critical_threshold must be between 0 and 100")
	}
	if c.Alerts.WarningThreshold > c.Alerts.CriticalThreshold {
		return fmt.Errorf("alerts.warning_threshold should be <= critical_threshold")
	}

	// Handoff validation
	if c.Handoff.DebounceDelay.Duration() < 0 {
		return fmt.Errorf("handoff.debounce_delay cannot be negative")
	}
	if c.Handoff.MaxRetries < 0 {
		return fmt.Errorf("handoff.max_retries cannot be negative")
	}

	// Daemon validation
	if c.Daemon.CheckInterval.Duration() < 0 {
		return fmt.Errorf("daemon.check_interval cannot be negative")
	}
	if c.Daemon.RefreshThreshold.Duration() < 0 {
		return fmt.Errorf("daemon.refresh_threshold cannot be negative")
	}
	if c.Daemon.AuthPool.MaxConcurrentRefresh < 0 {
		return fmt.Errorf("daemon.auth_pool.max_concurrent_refresh cannot be negative")
	}
	if c.Daemon.AuthPool.RefreshRetryDelay.Duration() < 0 {
		return fmt.Errorf("daemon.auth_pool.refresh_retry_delay cannot be negative")
	}
	if c.Daemon.AuthPool.MaxRefreshRetries < 0 {
		return fmt.Errorf("daemon.auth_pool.max_refresh_retries cannot be negative")
	}

	// Subscription validation
	for name, sub := range c.Subscriptions {
		if sub.MonthlyCost < 0 {
			return fmt.Errorf("subscriptions.%s.monthly_cost cannot be negative", name)
		}
	}

	// Pattern validation
	if err := validatePatterns("rate_limits.claude", c.RateLimits.Claude); err != nil {
		return err
	}
	if err := validatePatterns("rate_limits.codex", c.RateLimits.Codex); err != nil {
		return err
	}
	if err := validatePatterns("rate_limits.gemini", c.RateLimits.Gemini); err != nil {
		return err
	}

	if err := validateLoginPatterns("login_patterns.claude", c.LoginPatterns.Claude); err != nil {
		return err
	}
	if err := validateLoginPatterns("login_patterns.codex", c.LoginPatterns.Codex); err != nil {
		return err
	}
	if err := validateLoginPatterns("login_patterns.gemini", c.LoginPatterns.Gemini); err != nil {
		return err
	}

	// TUI validation
	validThemes := map[string]bool{"auto": true, "dark": true, "light": true, "": true}
	if !validThemes[c.TUI.Theme] {
		return fmt.Errorf("tui.theme must be one of: auto, dark, light")
	}
	validDensities := map[string]bool{"cozy": true, "compact": true, "": true}
	if !validDensities[c.TUI.Density] {
		return fmt.Errorf("tui.density must be one of: cozy, compact")
	}

	return nil
}

func validatePatterns(path string, patterns []string) error {
	for i, p := range patterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("invalid regex in %s[%d]: %q: %w", path, i, p, err)
		}
	}
	return nil
}

func validateLoginPatterns(path string, p ProviderLoginPatterns) error {
	if err := validatePatterns(path+".success", p.Success); err != nil {
		return err
	}
	if err := validatePatterns(path+".failure", p.Failure); err != nil {
		return err
	}
	return nil
}

// ApplyEnvOverrides updates the config with environment variables.
func (c *SPMConfig) ApplyEnvOverrides() {
	// Alerts
	if v := os.Getenv("CAAM_ALERTS_ENABLED"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.Alerts.Enabled = b
		}
	}
	if v := os.Getenv("CAAM_ALERTS_WARNING_THRESHOLD"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			c.Alerts.WarningThreshold = i
		}
	}
	if v := os.Getenv("CAAM_ALERTS_CRITICAL_THRESHOLD"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			c.Alerts.CriticalThreshold = i
		}
	}
	if v := os.Getenv("CAAM_ALERTS_NOTIFICATIONS_TERMINAL"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.Alerts.Notifications.Terminal = b
		}
	}
	if v := os.Getenv("CAAM_ALERTS_NOTIFICATIONS_DESKTOP"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.Alerts.Notifications.Desktop = b
		}
	}
	if v := os.Getenv("CAAM_ALERTS_NOTIFICATIONS_WEBHOOK"); v != "" {
		c.Alerts.Notifications.Webhook = v
	}

	// Handoff
	if v := os.Getenv("CAAM_HANDOFF_AUTO_TRIGGER"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.Handoff.AutoTrigger = b
		}
	}
	if v := os.Getenv("CAAM_HANDOFF_DEBOUNCE_DELAY"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Handoff.DebounceDelay = Duration(d)
		}
	}
	if v := os.Getenv("CAAM_HANDOFF_MAX_RETRIES"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			c.Handoff.MaxRetries = i
		}
	}
	if v := os.Getenv("CAAM_HANDOFF_FALLBACK_TO_MANUAL"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.Handoff.FallbackToManual = b
		}
	}

	// Daemon
	if v := os.Getenv("CAAM_DAEMON_CHECK_INTERVAL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Daemon.CheckInterval = Duration(d)
		}
	}
	if v := os.Getenv("CAAM_DAEMON_REFRESH_THRESHOLD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Daemon.RefreshThreshold = Duration(d)
		}
	}
	if v := os.Getenv("CAAM_DAEMON_VERBOSE"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.Daemon.Verbose = b
		}
	}
	
	// Health
	if v := os.Getenv("CAAM_HEALTH_REFRESH_THRESHOLD"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			c.Health.RefreshThreshold = Duration(d)
		}
	}

	// TUI
	if v := os.Getenv("CAAM_TUI_THEME"); v != "" {
		theme := strings.ToLower(strings.TrimSpace(v))
		switch theme {
		case "auto", "dark", "light":
			c.TUI.Theme = theme
		}
	}
	if v := os.Getenv("CAAM_TUI_CONTRAST"); v != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "high", "hc", "1", "true":
			c.TUI.HighContrast = true
		case "normal", "0", "false":
			c.TUI.HighContrast = false
		}
	}
	if v := os.Getenv("CAAM_TUI_REDUCED_MOTION"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.ReducedMotion = b
		}
	} else if v := os.Getenv("CAAM_REDUCED_MOTION"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.ReducedMotion = b
		}
	} else if v := os.Getenv("REDUCED_MOTION"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.ReducedMotion = b
		}
	}
	if v := os.Getenv("CAAM_TUI_TOASTS"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.Toasts = b
		}
	}
	if v := os.Getenv("CAAM_TUI_MOUSE"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.Mouse = b
		}
	}
	if v := os.Getenv("CAAM_TUI_KEY_HINTS"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.ShowKeyHints = b
		}
	}
	if v := os.Getenv("CAAM_TUI_DENSITY"); v != "" {
		density := strings.ToLower(strings.TrimSpace(v))
		switch density {
		case "cozy", "compact":
			c.TUI.Density = density
		}
	}
	if v := os.Getenv("CAAM_NO_TUI"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.NoTUI = b
		}
	} else if v := os.Getenv("NO_TUI"); v != "" {
		if b, err := parseBool(v); err == nil {
			c.TUI.NoTUI = b
		}
	}
}

// parseBool parses various boolean representations.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "true", "yes", "1", "on":
		return true, nil
	case "false", "no", "0", "off":
		return false, nil
	default:
		return false, fmt.Errorf("invalid boolean: %s (use true/false, yes/no, 1/0)", s)
	}
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
