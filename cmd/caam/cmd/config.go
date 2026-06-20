package cmd

import (
	"fmt"
	"reflect"
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

Key paths use dot notation and resolve any key that 'caam config show'
emits, including nested sections to arbitrary depth.

Examples of resolvable keys:
  version                             Config version
  health.refresh_threshold            Token refresh threshold (duration)
  analytics.retention_days            Detailed log retention (int)
  runtime.file_watching               File watching enabled (bool)
  project.auto_activate               Auto-activate by CWD (bool)
  stealth.rotation.enabled            Rotation feature enabled (bool)
  stealth.rotation.algorithm          smart | round_robin | random
  stealth.cooldown.default_minutes    Cooldown duration (int)
  safety.auto_backup_before_switch    always | smart | never
  alerts.notifications.terminal       Terminal notifications (bool)
  daemon.auth_pool.max_concurrent_refresh  Max concurrent refresh (int)
  compaction_reminder.cooldown        Reminder cooldown (duration)

Run 'caam config show' to see the full set of available keys. Addressing an
intermediate section (e.g. 'stealth.rotation') prints that subtree as YAML.

Examples:
  caam config get health.refresh_threshold
  caam config get stealth.rotation.enabled
  caam config get stealth.rotation`,
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

// getConfigValue retrieves a value from the config by its dotted key path.
//
// It is driven entirely by reflection over the YAML-tagged SPMConfig struct, so
// it resolves the exact same key space that `config show` (yaml.Marshal of the
// same struct) emits. Previously `get` used a hand-maintained switch that only
// covered a subset of sections (health/analytics/runtime/project/alerts/handoff/
// daemon/tui) and a hardcoded set of nested keys, so paths that `show` printed
// — notably stealth.rotation.* / stealth.cooldown.* / safety.* / rate_limits.* /
// login_patterns.* / subscriptions.* / compaction_reminder.* — failed with
// "unknown nested key" or "unknown section". Reflection keeps the two in lockstep
// permanently: anything `show` serializes, `get` can read (issue #20).
func getConfigValue(cfg *config.SPMConfig, key string) (string, error) {
	if key == "" {
		return "", fmt.Errorf("empty key")
	}
	parts := strings.Split(key, ".")
	v, err := resolveYAMLPath(reflect.ValueOf(cfg), parts, key)
	if err != nil {
		return "", err
	}
	return formatConfigScalar(v, key)
}

// resolveYAMLPath walks a value along a dotted path, matching each path segment
// against the `yaml:"..."` tag of struct fields (and map keys for map-typed
// fields). It returns the addressed value or a descriptive error.
func resolveYAMLPath(v reflect.Value, parts []string, fullKey string) (reflect.Value, error) {
	for i, part := range parts {
		// Deref pointers as we descend.
		for v.Kind() == reflect.Ptr {
			if v.IsNil() {
				return reflect.Value{}, fmt.Errorf("unknown key: %s", fullKey)
			}
			v = v.Elem()
		}

		switch v.Kind() {
		case reflect.Struct:
			// The config.Duration type is a struct-free named int64, but be
			// defensive: only descend into genuine structs by yaml tag.
			field, ok := fieldByYAMLTag(v, part)
			if !ok {
				if i == 0 {
					return reflect.Value{}, fmt.Errorf("unknown section: %s", part)
				}
				return reflect.Value{}, fmt.Errorf("unknown key: %s", fullKey)
			}
			v = field
		case reflect.Map:
			// e.g. subscriptions.<name>.plan — map[string]SubscriptionConfig.
			if v.Type().Key().Kind() != reflect.String {
				return reflect.Value{}, fmt.Errorf("unknown key: %s", fullKey)
			}
			mv := v.MapIndex(reflect.ValueOf(part))
			if !mv.IsValid() {
				return reflect.Value{}, fmt.Errorf("unknown key: %s (no such entry %q)", fullKey, part)
			}
			v = mv
		default:
			// We still have path segments but hit a scalar/slice — the path
			// goes deeper than the schema allows.
			return reflect.Value{}, fmt.Errorf("unknown key: %s", fullKey)
		}
	}
	return v, nil
}

// fieldByYAMLTag returns the struct field whose yaml tag (first comma-separated
// token) equals name. Falls back to a case-insensitive Go field-name match so
// that field name lookups also work.
func fieldByYAMLTag(v reflect.Value, name string) (reflect.Value, bool) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		tag := sf.Tag.Get("yaml")
		if tag != "" {
			if comma := strings.IndexByte(tag, ','); comma >= 0 {
				tag = tag[:comma]
			}
			if tag == "-" {
				continue
			}
			if tag == name {
				return v.Field(i), true
			}
		}
		if strings.EqualFold(sf.Name, name) {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

// formatConfigScalar renders a resolved value to the string form `get` prints.
// Durations, bools, ints, floats and strings keep their historical formatting;
// composite values (structs/maps/slices) are rendered as YAML so that
// addressing an intermediate node (e.g. `stealth.rotation`) yields its subtree
// rather than an error.
func formatConfigScalar(v reflect.Value, key string) (string, error) {
	// config.Duration has a String() method; honor it before generic handling.
	if d, ok := v.Interface().(config.Duration); ok {
		return d.String(), nil
	}

	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		// Preserve the legacy two-decimal rendering for rates like
		// health.penalty_decay_rate so existing output/tests stay stable.
		return fmt.Sprintf("%.2f", v.Float()), nil
	case reflect.String:
		return v.String(), nil
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array, reflect.Ptr:
		// Composite node: serialize the subtree as YAML (same engine `show`
		// uses) so `get <section>` and `get <section>.<sub>` both work.
		data, err := yaml.Marshal(v.Interface())
		if err != nil {
			return "", fmt.Errorf("marshal %s: %w", key, err)
		}
		return strings.TrimRight(string(data), "\n"), nil
	default:
		return fmt.Sprintf("%v", v.Interface()), nil
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

