package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"gopkg.in/yaml.v3"
)

var spmConfig *config.SPMConfig

// configCmd is the parent command for config management.
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage Smart Profile Management configuration",
	Long: `View and modify Smart Profile Management settings.

Configuration is stored at ~/.caam/config.yaml

Examples:
  caam config show                              # Show current config
  caam config get health.refresh_threshold      # Get specific value
  caam config set health.refresh_threshold 5m   # Set value
  caam config reset                             # Reset to defaults`,
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		// Load SPM config
		var err error
		spmConfig, err = config.LoadSPMConfig()
		if err != nil {
			return fmt.Errorf("load SPM config: %w", err)
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configShowCmd)
	configCmd.AddCommand(configGetCmd)
	configCmd.AddCommand(configSetCmd)
	configCmd.AddCommand(configResetCmd)
	configCmd.AddCommand(configPathCmd)
	configCmd.AddCommand(configTUICmd)
}

// configShowCmd shows the current configuration.
var configShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show current configuration",
	Long: `Displays the current Smart Profile Management configuration.

The output shows all settings with their current values in YAML format.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := yaml.Marshal(spmConfig)
		if err != nil {
			return fmt.Errorf("marshal config: %w", err)
		}

		fmt.Printf("# Configuration file: %s\n\n", config.SPMConfigPath())
		fmt.Println(string(data))
		return nil
	},
}

// configGetCmd gets a specific configuration value.
var configGetCmd = &cobra.Command{
	Use:   "get <key>",
	Short: "Get a configuration value",
	Long: `Get a specific configuration value by its key path.

Key paths use dot notation: section.key

Available keys:
  version                             Config version
  health.refresh_threshold            Token refresh threshold (duration)
  health.warning_threshold            Health warning threshold (duration)
  health.penalty_decay_rate           Penalty decay rate (0-1)
  health.penalty_decay_interval       Penalty decay interval (duration)
  analytics.enabled                   Analytics enabled (bool)
  analytics.retention_days            Detailed log retention (int)
  analytics.aggregate_retention_days  Aggregate retention (int)
  analytics.cleanup_on_startup        Cleanup on startup (bool)
  runtime.file_watching               File watching enabled (bool)
  runtime.reload_on_sighup            Reload on SIGHUP (bool)
  runtime.pid_file                    PID file enabled (bool)
  project.enabled                     Project associations enabled (bool)
  project.auto_activate               Auto-activate by CWD (bool)

Examples:
  caam config get health.refresh_threshold
  caam config get analytics.retention_days`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value, err := getConfigValue(spmConfig, key)
		if err != nil {
			return err
		}
		fmt.Println(value)
		return nil
	},
}

// configSetCmd sets a configuration value.
var configSetCmd = &cobra.Command{
	Use:   "set <key> <value>",
	Short: "Set a configuration value",
	Long: `Set a specific configuration value.

Key paths use dot notation: section.key
Values are parsed based on the key's type.

Duration values: 10m, 1h, 30s, 2h30m
Boolean values: true, false, yes, no, 1, 0
Integer values: 30, 90, 365

Examples:
  caam config set health.refresh_threshold 5m
  caam config set health.penalty_decay_rate 0.9
  caam config set analytics.retention_days 30
  caam config set runtime.file_watching false`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		key := args[0]
		value := args[1]

		if err := setConfigValue(spmConfig, key, value); err != nil {
			return err
		}

		if err := spmConfig.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		// Show updated value
		newValue, _ := getConfigValue(spmConfig, key)
		fmt.Printf("%s = %s\n", key, newValue)
		return nil
	},
}

// configResetCmd resets configuration to defaults.
var configResetCmd = &cobra.Command{
	Use:   "reset",
	Short: "Reset configuration to defaults",
	Long: `Reset all configuration values to their defaults.

This will overwrite your current configuration file.

Examples:
  caam config reset`,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		if !force {
			fmt.Printf("Reset configuration to defaults? [y/N]: ")
			var confirm string
			fmt.Scanln(&confirm)
			if strings.ToLower(confirm) != "y" {
				fmt.Println("Cancelled")
				return nil
			}
		}

		spmConfig = config.DefaultSPMConfig()
		if err := spmConfig.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		fmt.Println("Configuration reset to defaults")
		return nil
	},
}

func init() {
	configResetCmd.Flags().Bool("force", false, "skip confirmation")
}

// configPathCmd shows the configuration file path.
var configPathCmd = &cobra.Command{
	Use:   "path",
	Short: "Show configuration file path",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println(config.SPMConfigPath())
		return nil
	},
}

// getConfigValue retrieves a value from the config by key path.
func getConfigValue(cfg *config.SPMConfig, key string) (string, error) {
	parts := strings.Split(key, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return "", fmt.Errorf("invalid key format: %s (use section.key or section.subsection.key)", key)
	}

	// Handle top-level keys
	if len(parts) == 1 {
		switch parts[0] {
		case "version":
			return strconv.Itoa(cfg.Version), nil
		default:
			return "", fmt.Errorf("unknown key: %s", key)
		}
	}

	section := parts[0]
	field := parts[1]

	// Handle nested sections (3 parts)
	if len(parts) == 3 {
		subfield := parts[2]
		switch section {
		case "alerts":
			if field == "notifications" {
				return getNotificationsValue(&cfg.Alerts.Notifications, subfield)
			}
		case "daemon":
			if field == "auth_pool" {
				return getAuthPoolValue(&cfg.Daemon.AuthPool, subfield)
			}
		}
		return "", fmt.Errorf("unknown nested key: %s", key)
	}

	switch section {
	case "health":
		return getHealthValue(&cfg.Health, field)
	case "analytics":
		return getAnalyticsValue(&cfg.Analytics, field)
	case "runtime":
		return getRuntimeValue(&cfg.Runtime, field)
	case "project":
		return getProjectValue(&cfg.Project, field)
	case "alerts":
		return getAlertsValue(&cfg.Alerts, field)
	case "handoff":
		return getHandoffValue(&cfg.Handoff, field)
	case "daemon":
		return getDaemonValue(&cfg.Daemon, field)
	case "tui":
		return getTUIValue(&cfg.TUI, field)
	default:
		return "", fmt.Errorf("unknown section: %s", section)
	}
}

func getHealthValue(h *config.HealthConfig, field string) (string, error) {
	switch field {
	case "refresh_threshold":
		return h.RefreshThreshold.String(), nil
	case "warning_threshold":
		return h.WarningThreshold.String(), nil
	case "penalty_decay_rate":
		return fmt.Sprintf("%.2f", h.PenaltyDecayRate), nil
	case "penalty_decay_interval":
		return h.PenaltyDecayInterval.String(), nil
	default:
		return "", fmt.Errorf("unknown health field: %s", field)
	}
}

func getAnalyticsValue(a *config.AnalyticsConfig, field string) (string, error) {
	switch field {
	case "enabled":
		return strconv.FormatBool(a.Enabled), nil
	case "retention_days":
		return strconv.Itoa(a.RetentionDays), nil
	case "aggregate_retention_days":
		return strconv.Itoa(a.AggregateRetentionDays), nil
	case "cleanup_on_startup":
		return strconv.FormatBool(a.CleanupOnStartup), nil
	default:
		return "", fmt.Errorf("unknown analytics field: %s", field)
	}
}

func getRuntimeValue(r *config.RuntimeConfig, field string) (string, error) {
	switch field {
	case "file_watching":
		return strconv.FormatBool(r.FileWatching), nil
	case "reload_on_sighup":
		return strconv.FormatBool(r.ReloadOnSIGHUP), nil
	case "pid_file":
		return strconv.FormatBool(r.PIDFile), nil
	default:
		return "", fmt.Errorf("unknown runtime field: %s", field)
	}
}

func getProjectValue(p *config.ProjectConfig, field string) (string, error) {
	switch field {
	case "enabled":
		return strconv.FormatBool(p.Enabled), nil
	case "auto_activate":
		return strconv.FormatBool(p.AutoActivate), nil
	default:
		return "", fmt.Errorf("unknown project field: %s", field)
	}
}

func getAlertsValue(a *config.AlertConfig, field string) (string, error) {
	switch field {
	case "enabled":
		return strconv.FormatBool(a.Enabled), nil
	case "warning_threshold":
		return strconv.Itoa(a.WarningThreshold), nil
	case "critical_threshold":
		return strconv.Itoa(a.CriticalThreshold), nil
	default:
		return "", fmt.Errorf("unknown alerts field: %s", field)
	}
}

func getNotificationsValue(n *config.NotificationConfig, field string) (string, error) {
	switch field {
	case "terminal":
		return strconv.FormatBool(n.Terminal), nil
	case "desktop":
		return strconv.FormatBool(n.Desktop), nil
	case "webhook":
		return n.Webhook, nil
	default:
		return "", fmt.Errorf("unknown notifications field: %s", field)
	}
}

func getHandoffValue(h *config.HandoffConfig, field string) (string, error) {
	switch field {
	case "auto_trigger":
		return strconv.FormatBool(h.AutoTrigger), nil
	case "debounce_delay":
		return h.DebounceDelay.String(), nil
	case "max_retries":
		return strconv.Itoa(h.MaxRetries), nil
	case "fallback_to_manual":
		return strconv.FormatBool(h.FallbackToManual), nil
	default:
		return "", fmt.Errorf("unknown handoff field: %s", field)
	}
}

func getDaemonValue(d *config.DaemonConfig, field string) (string, error) {
	switch field {
	case "check_interval":
		return d.CheckInterval.String(), nil
	case "refresh_threshold":
		return d.RefreshThreshold.String(), nil
	case "verbose":
		return strconv.FormatBool(d.Verbose), nil
	default:
		return "", fmt.Errorf("unknown daemon field: %s", field)
	}
}

func getAuthPoolValue(a *config.AuthPoolConfig, field string) (string, error) {
	switch field {
	case "enabled":
		return strconv.FormatBool(a.Enabled), nil
	case "max_concurrent_refresh":
		return strconv.Itoa(a.MaxConcurrentRefresh), nil
	case "refresh_retry_delay":
		return a.RefreshRetryDelay.String(), nil
	case "max_refresh_retries":
		return strconv.Itoa(a.MaxRefreshRetries), nil
	default:
		return "", fmt.Errorf("unknown auth_pool field: %s", field)
	}
}

// setConfigValue sets a value in the config by key path.
func setConfigValue(cfg *config.SPMConfig, key, value string) error {
	parts := strings.Split(key, ".")
	if len(parts) < 1 || len(parts) > 3 {
		return fmt.Errorf("invalid key format: %s (use section.key or section.subsection.key)", key)
	}

	// Handle top-level keys
	if len(parts) == 1 {
		switch parts[0] {
		case "version":
			v, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid version: %w", err)
			}
			cfg.Version = v
			return nil
		default:
			return fmt.Errorf("unknown key: %s", key)
		}
	}

	section := parts[0]
	field := parts[1]

	// Handle nested sections (3 parts)
	if len(parts) == 3 {
		subfield := parts[2]
		switch section {
		case "alerts":
			if field == "notifications" {
				return setNotificationsValue(&cfg.Alerts.Notifications, subfield, value)
			}
		case "daemon":
			if field == "auth_pool" {
				return setAuthPoolValue(&cfg.Daemon.AuthPool, subfield, value)
			}
		}
		return fmt.Errorf("unknown nested key: %s", key)
	}

	switch section {
	case "health":
		return setHealthValue(&cfg.Health, field, value)
	case "analytics":
		return setAnalyticsValue(&cfg.Analytics, field, value)
	case "runtime":
		return setRuntimeValue(&cfg.Runtime, field, value)
	case "project":
		return setProjectValue(&cfg.Project, field, value)
	case "alerts":
		return setAlertsValue(&cfg.Alerts, field, value)
	case "handoff":
		return setHandoffValue(&cfg.Handoff, field, value)
	case "daemon":
		return setDaemonValue(&cfg.Daemon, field, value)
	case "tui":
		return setTUIValue(&cfg.TUI, field, value)
	default:
		return fmt.Errorf("unknown section: %s", section)
	}
}

func setHealthValue(h *config.HealthConfig, field, value string) error {
	switch field {
	case "refresh_threshold":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		h.RefreshThreshold = config.Duration(d)
	case "warning_threshold":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		h.WarningThreshold = config.Duration(d)
	case "penalty_decay_rate":
		f, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return fmt.Errorf("invalid float: %w", err)
		}
		h.PenaltyDecayRate = f
	case "penalty_decay_interval":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		h.PenaltyDecayInterval = config.Duration(d)
	default:
		return fmt.Errorf("unknown health field: %s", field)
	}
	return nil
}

func setAnalyticsValue(a *config.AnalyticsConfig, field, value string) error {
	switch field {
	case "enabled":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		a.Enabled = b
	case "retention_days":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		a.RetentionDays = i
	case "aggregate_retention_days":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		a.AggregateRetentionDays = i
	case "cleanup_on_startup":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		a.CleanupOnStartup = b
	default:
		return fmt.Errorf("unknown analytics field: %s", field)
	}
	return nil
}

func setRuntimeValue(r *config.RuntimeConfig, field, value string) error {
	switch field {
	case "file_watching":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		r.FileWatching = b
	case "reload_on_sighup":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		r.ReloadOnSIGHUP = b
	case "pid_file":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		r.PIDFile = b
	default:
		return fmt.Errorf("unknown runtime field: %s", field)
	}
	return nil
}

func setProjectValue(p *config.ProjectConfig, field, value string) error {
	switch field {
	case "enabled":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		p.Enabled = b
	case "auto_activate":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		p.AutoActivate = b
	default:
		return fmt.Errorf("unknown project field: %s", field)
	}
	return nil
}

func setAlertsValue(a *config.AlertConfig, field, value string) error {
	switch field {
	case "enabled":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		a.Enabled = b
	case "warning_threshold":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		a.WarningThreshold = i
	case "critical_threshold":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		a.CriticalThreshold = i
	default:
		return fmt.Errorf("unknown alerts field: %s", field)
	}
	return nil
}

func setNotificationsValue(n *config.NotificationConfig, field, value string) error {
	switch field {
	case "terminal":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		n.Terminal = b
	case "desktop":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		n.Desktop = b
	case "webhook":
		n.Webhook = value
	default:
		return fmt.Errorf("unknown notifications field: %s", field)
	}
	return nil
}

func setHandoffValue(h *config.HandoffConfig, field, value string) error {
	switch field {
	case "auto_trigger":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		h.AutoTrigger = b
	case "debounce_delay":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		h.DebounceDelay = config.Duration(d)
	case "max_retries":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		h.MaxRetries = i
	case "fallback_to_manual":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		h.FallbackToManual = b
	default:
		return fmt.Errorf("unknown handoff field: %s", field)
	}
	return nil
}

func setDaemonValue(d *config.DaemonConfig, field, value string) error {
	switch field {
	case "check_interval":
		dur, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		d.CheckInterval = config.Duration(dur)
	case "refresh_threshold":
		dur, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		d.RefreshThreshold = config.Duration(dur)
	case "verbose":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		d.Verbose = b
	default:
		return fmt.Errorf("unknown daemon field: %s", field)
	}
	return nil
}

func setAuthPoolValue(a *config.AuthPoolConfig, field, value string) error {
	switch field {
	case "enabled":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		a.Enabled = b
	case "max_concurrent_refresh":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		a.MaxConcurrentRefresh = i
	case "refresh_retry_delay":
		d, err := time.ParseDuration(value)
		if err != nil {
			return fmt.Errorf("invalid duration: %w", err)
		}
		a.RefreshRetryDelay = config.Duration(d)
	case "max_refresh_retries":
		i, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("invalid integer: %w", err)
		}
		a.MaxRefreshRetries = i
	default:
		return fmt.Errorf("unknown auth_pool field: %s", field)
	}
	return nil
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

func getTUIValue(t *config.TUIConfig, field string) (string, error) {
	switch field {
	case "theme":
		return t.Theme, nil
	case "high_contrast":
		return strconv.FormatBool(t.HighContrast), nil
	case "reduced_motion":
		return strconv.FormatBool(t.ReducedMotion), nil
	case "toasts":
		return strconv.FormatBool(t.Toasts), nil
	case "mouse":
		return strconv.FormatBool(t.Mouse), nil
	case "show_key_hints":
		return strconv.FormatBool(t.ShowKeyHints), nil
	case "density":
		return t.Density, nil
	case "no_tui":
		return strconv.FormatBool(t.NoTUI), nil
	default:
		return "", fmt.Errorf("unknown tui field: %s", field)
	}
}

func setTUIValue(t *config.TUIConfig, field, value string) error {
	switch field {
	case "theme":
		theme := strings.ToLower(strings.TrimSpace(value))
		switch theme {
		case "auto", "dark", "light":
			t.Theme = theme
		default:
			return fmt.Errorf("invalid theme: %s (use auto, dark, or light)", value)
		}
	case "high_contrast":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		t.HighContrast = b
	case "reduced_motion":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		t.ReducedMotion = b
	case "toasts":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		t.Toasts = b
	case "mouse":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		t.Mouse = b
	case "show_key_hints":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		t.ShowKeyHints = b
	case "density":
		density := strings.ToLower(strings.TrimSpace(value))
		switch density {
		case "cozy", "compact":
			t.Density = density
		default:
			return fmt.Errorf("invalid density: %s (use cozy or compact)", value)
		}
	case "no_tui":
		b, err := parseBool(value)
		if err != nil {
			return err
		}
		t.NoTUI = b
	default:
		return fmt.Errorf("unknown tui field: %s", field)
	}
	return nil
}

// configTUICmd shows and manages TUI preferences.
var configTUICmd = &cobra.Command{
	Use:   "tui [key] [value]",
	Short: "View and modify TUI preferences",
	Long: `View and modify TUI appearance and behavior preferences.

When called without arguments, shows all TUI settings.
When called with a key, shows that specific setting.
When called with a key and value, sets that setting.

Available keys:
  theme          Color scheme: auto (default), dark, light
  high_contrast  Enable high-contrast colors (bool)
  reduced_motion Disable animations like spinners (bool)
  toasts         Show transient notifications (bool)
  mouse          Enable mouse support (bool)
  show_key_hints Show keyboard shortcuts in status bar (bool)
  density        Spacing: cozy (default), compact
  no_tui         Disable TUI entirely (bool)

Environment variable overrides (higher priority than config):
  CAAM_TUI_THEME          Theme setting
  CAAM_TUI_CONTRAST       high or normal
  CAAM_TUI_REDUCED_MOTION Disable animations
  CAAM_TUI_TOASTS         Show toasts
  CAAM_TUI_MOUSE          Mouse support
  CAAM_TUI_KEY_HINTS      Key hints
  CAAM_TUI_DENSITY        cozy or compact
  CAAM_NO_TUI / NO_TUI    Disable TUI

Examples:
  caam config tui                          # Show all TUI settings
  caam config tui theme                    # Show theme setting
  caam config tui theme dark               # Set theme to dark
  caam config tui reduced_motion true      # Disable animations
  caam config tui density compact          # Use compact spacing`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			// Show all TUI settings
			fmt.Println("TUI Preferences")
			fmt.Println("───────────────────────────────────────────")
			fmt.Printf("  %-16s %s\n", "theme:", spmConfig.TUI.Theme)
			fmt.Printf("  %-16s %t\n", "high_contrast:", spmConfig.TUI.HighContrast)
			fmt.Printf("  %-16s %t\n", "reduced_motion:", spmConfig.TUI.ReducedMotion)
			fmt.Printf("  %-16s %t\n", "toasts:", spmConfig.TUI.Toasts)
			fmt.Printf("  %-16s %t\n", "mouse:", spmConfig.TUI.Mouse)
			fmt.Printf("  %-16s %t\n", "show_key_hints:", spmConfig.TUI.ShowKeyHints)
			fmt.Printf("  %-16s %s\n", "density:", spmConfig.TUI.Density)
			fmt.Printf("  %-16s %t\n", "no_tui:", spmConfig.TUI.NoTUI)
			fmt.Println()
			fmt.Printf("Config file: %s\n", config.SPMConfigPath())
			return nil
		}

		key := "tui." + args[0]
		if len(args) == 1 {
			// Get specific value
			value, err := getConfigValue(spmConfig, key)
			if err != nil {
				return err
			}
			fmt.Println(value)
			return nil
		}

		// Set value
		value := args[1]
		if err := setConfigValue(spmConfig, key, value); err != nil {
			return err
		}

		if err := spmConfig.Save(); err != nil {
			return fmt.Errorf("save config: %w", err)
		}

		// Show updated value
		newValue, _ := getConfigValue(spmConfig, key)
		fmt.Printf("tui.%s = %s\n", args[0], newValue)
		return nil
	},
}

