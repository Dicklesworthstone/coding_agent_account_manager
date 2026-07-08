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
	Long: `Set a specific configuration value by its key path.

Key paths use dot notation and address any key that 'caam config show'
emits, to arbitrary depth. This is the exact same key space that
'caam config get' reads, so every value shown can be written back
(issue #54).

Values are parsed based on the addressed field's type:
  Duration values: 10m, 1h, 30s, 2h30m
  Boolean values:  true, false, yes, no, 1, 0, on, off
  Integer values:  30, 90, 365
  Float values:    0.8, 275
  String values:   used verbatim
  List values:     comma-separated (e.g. "429,rate limit"); address a
                   single element by index to preserve commas, e.g.
                   'set rate_limits.claude.0 "a{2,3}"'

Examples:
  caam config set health.refresh_threshold 5m
  caam config set health.penalty_decay_rate 0.9
  caam config set analytics.retention_days 30
  caam config set runtime.file_watching false
  caam config set safety.auto_backup_before_switch always
  caam config set stealth.rotation.enabled true
  caam config set stealth.rotation.algorithm round_robin
  caam config set subscriptions.gemini.plan pro`,
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
		case reflect.Slice, reflect.Array:
			// e.g. rate_limits.claude.0 — index into a list leaf.
			idx, err := strconv.Atoi(part)
			if err != nil {
				return reflect.Value{}, fmt.Errorf("unknown key: %s", fullKey)
			}
			if idx < 0 || idx >= v.Len() {
				return reflect.Value{}, fmt.Errorf("index out of range for %s: %d (len %d)", fullKey, idx, v.Len())
			}
			v = v.Index(idx)
		default:
			// We still have path segments but hit a scalar — the path goes
			// deeper than the schema allows.
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

// setConfigValue sets a value in the config by its dotted key path.
//
// Like getConfigValue, it is driven entirely by reflection over the YAML-tagged
// SPMConfig struct so that show/get/set share a single source of truth: every
// scalar leaf that `config show` emits (and `config get` can read) can also be
// written here. Previously `set` used a hand-maintained switch that only knew
// about version/health/analytics/runtime/project/alerts/handoff/daemon/tui (and
// two nested cases), so every other section `show` displayed —
// stealth.* / safety.* / rate_limits.* / login_patterns.* / subscriptions.* /
// compaction_reminder.* — was rejected as "unknown section" / "unknown nested
// key" even though `show` printed it and `get` could read it (issue #54).
// Reflection keeps the three commands in lockstep permanently.
//
// The resulting value is not semantically validated here; SPMConfig.Validate()
// (invoked by Save) enforces enum membership, ranges and regex compilation and
// returns a descriptive error to the user.
func setConfigValue(cfg *config.SPMConfig, key, value string) error {
	if key == "" {
		return fmt.Errorf("empty key")
	}
	parts := strings.Split(key, ".")
	return assignYAMLPath(reflect.ValueOf(cfg), parts, key, value, 0)
}

// assignYAMLPath walks a value along a dotted path (matching yaml tags for
// struct fields, string keys for maps and integer indices for slices) and writes
// raw into the addressed leaf. Map entries are not addressable in Go, so the
// entry is copied into an addressable temporary, mutated, and stored back with
// SetMapIndex. depth distinguishes a bad top-level segment ("unknown section")
// from a bad nested one ("unknown key"), mirroring resolveYAMLPath's messages.
func assignYAMLPath(v reflect.Value, parts []string, fullKey, raw string, depth int) error {
	// Deref pointers as we descend.
	for v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return fmt.Errorf("unknown key: %s", fullKey)
		}
		v = v.Elem()
	}

	part := parts[0]
	rest := parts[1:]

	switch v.Kind() {
	case reflect.Struct:
		field, ok := fieldByYAMLTag(v, part)
		if !ok {
			if depth == 0 {
				return fmt.Errorf("unknown section: %s", part)
			}
			return fmt.Errorf("unknown key: %s", fullKey)
		}
		if len(rest) == 0 {
			return setScalarValue(field, fullKey, raw)
		}
		return assignYAMLPath(field, rest, fullKey, raw, depth+1)

	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return fmt.Errorf("unknown key: %s", fullKey)
		}
		keyVal := reflect.ValueOf(part)
		elem := v.MapIndex(keyVal)
		if !elem.IsValid() {
			return fmt.Errorf("unknown key: %s (no such entry %q)", fullKey, part)
		}
		// Map values are not addressable; mutate an addressable copy and store
		// it back.
		tmp := reflect.New(v.Type().Elem()).Elem()
		tmp.Set(elem)
		if len(rest) == 0 {
			if err := setScalarValue(tmp, fullKey, raw); err != nil {
				return err
			}
		} else if err := assignYAMLPath(tmp, rest, fullKey, raw, depth+1); err != nil {
			return err
		}
		v.SetMapIndex(keyVal, tmp)
		return nil

	case reflect.Slice, reflect.Array:
		idx, err := strconv.Atoi(part)
		if err != nil {
			return fmt.Errorf("unknown key: %s", fullKey)
		}
		if idx < 0 || idx >= v.Len() {
			return fmt.Errorf("index out of range for %s: %d (len %d)", fullKey, idx, v.Len())
		}
		if len(rest) == 0 {
			return setScalarValue(v.Index(idx), fullKey, raw)
		}
		return assignYAMLPath(v.Index(idx), rest, fullKey, raw, depth+1)

	default:
		return fmt.Errorf("unknown key: %s", fullKey)
	}
}

// setScalarValue parses raw according to the concrete type of the addressed leaf
// and assigns it. It is the write-side counterpart of formatConfigScalar and
// supports every leaf type the schema uses: config.Duration, bool, the int/uint
// families, floats, strings and string lists (comma-separated).
func setScalarValue(v reflect.Value, fullKey, raw string) error {
	if !v.CanSet() {
		return fmt.Errorf("cannot set %s", fullKey)
	}

	// config.Duration is a named int64; parse human-readable durations before
	// the generic int handling would treat it as a raw nanosecond count.
	if v.Type() == reflect.TypeOf(config.Duration(0)) {
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid duration for %s: %w", fullKey, err)
		}
		v.SetInt(int64(d))
		return nil
	}

	switch v.Kind() {
	case reflect.Bool:
		b, err := parseBool(raw)
		if err != nil {
			return err
		}
		v.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid integer for %s: %w", fullKey, err)
		}
		if v.OverflowInt(n) {
			return fmt.Errorf("value out of range for %s: %s", fullKey, raw)
		}
		v.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid unsigned integer for %s: %w", fullKey, err)
		}
		if v.OverflowUint(n) {
			return fmt.Errorf("value out of range for %s: %s", fullKey, raw)
		}
		v.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("invalid number for %s: %w", fullKey, err)
		}
		if v.OverflowFloat(f) {
			return fmt.Errorf("value out of range for %s: %s", fullKey, raw)
		}
		v.SetFloat(f)
	case reflect.String:
		v.SetString(raw)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("cannot set %s: unsupported list element type %s", fullKey, v.Type().Elem().Kind())
		}
		items := parseStringList(raw)
		s := reflect.MakeSlice(v.Type(), len(items), len(items))
		for i, it := range items {
			s.Index(i).SetString(it)
		}
		v.Set(s)
	default:
		return fmt.Errorf("cannot set %s: unsupported type %s", fullKey, v.Kind())
	}
	return nil
}

// parseStringList splits a comma-separated value into a trimmed string slice,
// dropping empty entries. An empty (or whitespace-only) input clears the list.
// To set a single element that itself contains a comma (e.g. a regex like
// "a{2,3}"), address that element by index instead: set rate_limits.claude.0.
func parseStringList(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
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
