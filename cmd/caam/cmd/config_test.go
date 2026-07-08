package cmd

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"gopkg.in/yaml.v3"
)

// =============================================================================
// config.go Command Tests
// =============================================================================

func TestConfigCommand(t *testing.T) {
	if configCmd.Use != "config" {
		t.Errorf("Expected Use 'config', got %q", configCmd.Use)
	}

	if configCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if configCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestConfigShowCommand(t *testing.T) {
	if configShowCmd.Use != "show" {
		t.Errorf("Expected Use 'show', got %q", configShowCmd.Use)
	}

	if configShowCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

func TestConfigGetCommand(t *testing.T) {
	if configGetCmd.Use != "get <key>" {
		t.Errorf("Expected Use 'get <key>', got %q", configGetCmd.Use)
	}

	if configGetCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	// Should require exactly 1 arg
	err := configGetCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = configGetCmd.Args(nil, []string{"key"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = configGetCmd.Args(nil, []string{"key", "extra"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

func TestConfigSetCommand(t *testing.T) {
	if configSetCmd.Use != "set <key> <value>" {
		t.Errorf("Expected Use 'set <key> <value>', got %q", configSetCmd.Use)
	}

	if configSetCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	// Should require exactly 2 args
	err := configSetCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = configSetCmd.Args(nil, []string{"key"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}

	err = configSetCmd.Args(nil, []string{"key", "value"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}
}

func TestConfigResetCommand(t *testing.T) {
	if configResetCmd.Use != "reset" {
		t.Errorf("Expected Use 'reset', got %q", configResetCmd.Use)
	}

	if configResetCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	// Check --force flag
	forceFlag := configResetCmd.Flags().Lookup("force")
	if forceFlag == nil {
		t.Error("Expected --force flag")
	}
	if forceFlag.DefValue != "false" {
		t.Errorf("Expected force default false, got %q", forceFlag.DefValue)
	}
}

func TestConfigPathCommand(t *testing.T) {
	if configPathCmd.Use != "path" {
		t.Errorf("Expected Use 'path', got %q", configPathCmd.Use)
	}

	if configPathCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

// =============================================================================
// getConfigValue Tests
// =============================================================================

func TestGetConfigValue_TopLevel(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	// Test version
	val, err := getConfigValue(cfg, "version")
	if err != nil {
		t.Errorf("getConfigValue(version) error: %v", err)
	}
	if val != "1" {
		t.Errorf("Expected version '1', got %q", val)
	}
}

func TestGetConfigValue_Health(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{"health.refresh_threshold", false},
		{"health.warning_threshold", false},
		{"health.penalty_decay_rate", false},
		{"health.penalty_decay_interval", false},
		{"health.unknown_field", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := getConfigValue(cfg, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("getConfigValue(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestGetConfigValue_Analytics(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{"analytics.enabled", false},
		{"analytics.retention_days", false},
		{"analytics.aggregate_retention_days", false},
		{"analytics.cleanup_on_startup", false},
		{"analytics.unknown_field", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := getConfigValue(cfg, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("getConfigValue(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestGetConfigValue_Runtime(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{"runtime.file_watching", false},
		{"runtime.reload_on_sighup", false},
		{"runtime.pid_file", false},
		{"runtime.unknown_field", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := getConfigValue(cfg, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("getConfigValue(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestGetConfigValue_Project(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{"project.enabled", false},
		{"project.auto_activate", false},
		{"project.unknown_field", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := getConfigValue(cfg, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("getConfigValue(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

func TestGetConfigValue_InvalidKeys(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key     string
		wantErr bool
	}{
		{"unknown_key", true},
		{"unknown.section.key", true},
		{"invalid_section.key", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			_, err := getConfigValue(cfg, tt.key)
			if (err != nil) != tt.wantErr {
				t.Errorf("getConfigValue(%q) error = %v, wantErr %v", tt.key, err, tt.wantErr)
			}
		})
	}
}

// =============================================================================
// setConfigValue Tests
// =============================================================================

func TestSetConfigValue_Version(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	err := setConfigValue(cfg, "version", "2")
	if err != nil {
		t.Errorf("setConfigValue(version) error: %v", err)
	}
	if cfg.Version != 2 {
		t.Errorf("Expected version 2, got %d", cfg.Version)
	}

	// Test invalid version
	err = setConfigValue(cfg, "version", "not-a-number")
	if err == nil {
		t.Error("Expected error for invalid version")
	}
}

func TestSetConfigValue_Health(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	// Test refresh_threshold
	err := setConfigValue(cfg, "health.refresh_threshold", "10m")
	if err != nil {
		t.Errorf("setConfigValue(health.refresh_threshold) error: %v", err)
	}
	if time.Duration(cfg.Health.RefreshThreshold) != 10*time.Minute {
		t.Errorf("Expected 10m, got %v", cfg.Health.RefreshThreshold)
	}

	// Test penalty_decay_rate
	err = setConfigValue(cfg, "health.penalty_decay_rate", "0.85")
	if err != nil {
		t.Errorf("setConfigValue(health.penalty_decay_rate) error: %v", err)
	}
	if cfg.Health.PenaltyDecayRate != 0.85 {
		t.Errorf("Expected 0.85, got %f", cfg.Health.PenaltyDecayRate)
	}

	// Test invalid duration
	err = setConfigValue(cfg, "health.refresh_threshold", "not-a-duration")
	if err == nil {
		t.Error("Expected error for invalid duration")
	}
}

func TestSetConfigValue_Analytics(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	// Test enabled
	err := setConfigValue(cfg, "analytics.enabled", "false")
	if err != nil {
		t.Errorf("setConfigValue(analytics.enabled) error: %v", err)
	}
	if cfg.Analytics.Enabled {
		t.Error("Expected enabled=false")
	}

	// Test retention_days
	err = setConfigValue(cfg, "analytics.retention_days", "60")
	if err != nil {
		t.Errorf("setConfigValue(analytics.retention_days) error: %v", err)
	}
	if cfg.Analytics.RetentionDays != 60 {
		t.Errorf("Expected 60, got %d", cfg.Analytics.RetentionDays)
	}

	// Test invalid integer
	err = setConfigValue(cfg, "analytics.retention_days", "not-a-number")
	if err == nil {
		t.Error("Expected error for invalid integer")
	}
}

func TestSetConfigValue_Runtime(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	// Test file_watching
	err := setConfigValue(cfg, "runtime.file_watching", "true")
	if err != nil {
		t.Errorf("setConfigValue(runtime.file_watching) error: %v", err)
	}
	if !cfg.Runtime.FileWatching {
		t.Error("Expected file_watching=true")
	}

	// Test pid_file
	err = setConfigValue(cfg, "runtime.pid_file", "yes")
	if err != nil {
		t.Errorf("setConfigValue(runtime.pid_file) error: %v", err)
	}
	if !cfg.Runtime.PIDFile {
		t.Error("Expected pid_file=true")
	}
}

func TestSetConfigValue_Project(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	// Test enabled
	err := setConfigValue(cfg, "project.enabled", "1")
	if err != nil {
		t.Errorf("setConfigValue(project.enabled) error: %v", err)
	}
	if !cfg.Project.Enabled {
		t.Error("Expected enabled=true")
	}

	// Test auto_activate
	err = setConfigValue(cfg, "project.auto_activate", "0")
	if err != nil {
		t.Errorf("setConfigValue(project.auto_activate) error: %v", err)
	}
	if cfg.Project.AutoActivate {
		t.Error("Expected auto_activate=false")
	}
}

func TestSetConfigValue_InvalidKeys(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key   string
		value string
	}{
		{"unknown_key", "value"},
		{"unknown.section.key", "value"},
		{"invalid_section.key", "value"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			err := setConfigValue(cfg, tt.key, tt.value)
			if err == nil {
				t.Errorf("setConfigValue(%q) expected error", tt.key)
			}
		})
	}
}

// =============================================================================
// parseBool Tests
// =============================================================================

func TestParseBool(t *testing.T) {
	tests := []struct {
		input   string
		want    bool
		wantErr bool
	}{
		{"true", true, false},
		{"True", true, false},
		{"TRUE", true, false},
		{"yes", true, false},
		{"Yes", true, false},
		{"1", true, false},
		{"on", true, false},
		{"false", false, false},
		{"False", false, false},
		{"FALSE", false, false},
		{"no", false, false},
		{"No", false, false},
		{"0", false, false},
		{"off", false, false},
		{"invalid", false, true},
		{"maybe", false, true},
		{"2", false, true},
		{"", false, true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseBool(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseBool(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr && got != tt.want {
				t.Errorf("parseBool(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// =============================================================================
// Issue #20: `config get` must resolve any nested key `config show` emits.
// =============================================================================

// TestGetConfigValue_NestedStealthKeys asserts that representative nested keys
// emitted by `config show` (e.g. stealth.rotation.*) resolve via `config get`.
// These previously failed with "unknown nested key" because get used a partial
// hardcoded switch that didn't include the stealth section.
func TestGetConfigValue_NestedStealthKeys(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	tests := []struct {
		key  string
		want string
	}{
		{"stealth.rotation.enabled", "false"},
		{"stealth.rotation.algorithm", "smart"},
		{"stealth.cooldown.enabled", "false"},
		{"stealth.cooldown.default_minutes", "60"},
		{"stealth.switch_delay.min_seconds", "5"},
		{"stealth.switch_delay.max_seconds", "30"},
		{"safety.auto_backup_before_switch", "smart"},
		{"safety.max_auto_backups", "5"},
		{"compaction_reminder.enabled", "false"},
		{"compaction_reminder.cooldown", "10m0s"},
		{"alerts.notifications.terminal", "true"},
		{"daemon.auth_pool.max_concurrent_refresh", "3"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			got, err := getConfigValue(cfg, tt.key)
			if err != nil {
				t.Fatalf("getConfigValue(%q) unexpected error: %v", tt.key, err)
			}
			if got != tt.want {
				t.Errorf("getConfigValue(%q) = %q, want %q", tt.key, got, tt.want)
			}
		})
	}
}

// TestGetConfigValue_ResolvesEverythingShowEmits is a drift guard: every leaf
// key that `config show` (yaml.Marshal of *SPMConfig) emits must be resolvable
// by `config get`. This prevents the two commands from ever diverging again.
func TestGetConfigValue_ResolvesEverythingShowEmits(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	for _, key := range leafKeys("", root) {
		if _, err := getConfigValue(cfg, key); err != nil {
			t.Errorf("config show emits %q but config get cannot resolve it: %v", key, err)
		}
	}
}

// leafKeys walks a decoded YAML map and returns every scalar leaf's dotted path.
// Sequences (slices) are treated as leaves addressable by their parent key.
func leafKeys(prefix string, v interface{}) []string {
	var keys []string
	m, ok := v.(map[string]interface{})
	if !ok {
		if prefix != "" {
			keys = append(keys, prefix)
		}
		return keys
	}
	for k, child := range m {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		switch cv := child.(type) {
		case map[string]interface{}:
			keys = append(keys, leafKeys(path, cv)...)
		default:
			keys = append(keys, path)
		}
	}
	return keys
}

// TestGetConfigValue_UnknownStillErrors ensures the reflection resolver keeps
// rejecting genuinely unknown keys (so error behavior didn't regress).
func TestGetConfigValue_UnknownStillErrors(t *testing.T) {
	cfg := config.DefaultSPMConfig()
	for _, key := range []string{"unknown.section.key", "invalid_section.key", "stealth.rotation.bogus", "stealth.bogus", ""} {
		if _, err := getConfigValue(cfg, key); err == nil {
			t.Errorf("getConfigValue(%q) = nil error, want error", key)
		}
	}
}

// TestGetConfigValue_CompositeNodeYAML verifies that addressing an intermediate
// (non-leaf) node returns its subtree as YAML rather than erroring.
func TestGetConfigValue_CompositeNodeYAML(t *testing.T) {
	cfg := config.DefaultSPMConfig()
	got, err := getConfigValue(cfg, "stealth.rotation")
	if err != nil {
		t.Fatalf("getConfigValue(stealth.rotation) error: %v", err)
	}
	if !strings.Contains(got, "algorithm: smart") || !strings.Contains(got, "enabled: false") {
		t.Errorf("getConfigValue(stealth.rotation) = %q, want YAML subtree with algorithm/enabled", got)
	}
}

// =============================================================================
// Issue #54: `config set` must accept every key `config show` emits.
// =============================================================================

// TestSetConfigValue_NestedSections asserts that representative keys which
// `config show` displays — and which `config get` already resolved — are now
// writable via `config set`. Before the reflection unification these were
// rejected with "unknown section" / "unknown nested key" even though `show`
// printed them, so users could see values they could never change (issue #54).
// Each case sets a value, re-reads it, and confirms the config still validates.
func TestSetConfigValue_NestedSections(t *testing.T) {
	tests := []struct {
		key   string
		value string
		want  string
	}{
		{"safety.auto_backup_before_switch", "always", "always"},
		{"safety.max_auto_backups", "10", "10"},
		{"stealth.rotation.enabled", "true", "true"},
		{"stealth.rotation.algorithm", "round_robin", "round_robin"},
		{"stealth.cooldown.enabled", "true", "true"},
		{"stealth.cooldown.default_minutes", "30", "30"},
		{"stealth.switch_delay.min_seconds", "3", "3"},
		{"stealth.switch_delay.max_seconds", "40", "40"},
		{"compaction_reminder.enabled", "true", "true"},
		{"compaction_reminder.cooldown", "15m", "15m0s"},
		{"compaction_reminder.prompt", "reread it", "reread it"},
		{"subscriptions.gemini.plan", "pro", "pro"},
		{"subscriptions.gemini.monthly_cost", "42", "42.00"},
		{"rate_limits.claude.0", "429 detected", "429 detected"},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			cfg := config.DefaultSPMConfig()
			if err := setConfigValue(cfg, tt.key, tt.value); err != nil {
				t.Fatalf("config show emits %q but config set rejects it: %v", tt.key, err)
			}
			got, err := getConfigValue(cfg, tt.key)
			if err != nil {
				t.Fatalf("getConfigValue(%q) after set: %v", tt.key, err)
			}
			if got != tt.want {
				t.Errorf("after set %q=%q, get = %q, want %q", tt.key, tt.value, got, tt.want)
			}
			if err := cfg.Validate(); err != nil {
				t.Errorf("config invalid after setting %q=%q: %v", tt.key, tt.value, err)
			}
		})
	}
}

// TestSetConfigValue_SetsEverythingShowEmits is the write-side drift guard that
// permanently locks show/get/set to one source of truth: every scalar leaf key
// that `config show` (yaml.Marshal of *SPMConfig) emits must be writable via
// `config set`, and the write must be observable via `config get`. This is the
// end-to-end assertion for issue #54 and prevents the commands from diverging
// again the next time a config field is added.
func TestSetConfigValue_SetsEverythingShowEmits(t *testing.T) {
	cfg := config.DefaultSPMConfig()

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	var root map[string]interface{}
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	for _, key := range leafKeys("", root) {
		t.Run(key, func(t *testing.T) {
			before, err := getConfigValue(cfg, key)
			if err != nil {
				t.Fatalf("config show emits %q but config get cannot read it: %v", key, err)
			}

			// Resolve the leaf's concrete kind to choose a valid write value.
			v, err := resolveYAMLPath(reflect.ValueOf(cfg), strings.Split(key, "."), key)
			if err != nil {
				t.Fatalf("resolve %q: %v", key, err)
			}
			for v.Kind() == reflect.Ptr {
				v = v.Elem()
			}

			if v.Kind() == reflect.Slice {
				// List leaves (rate_limits.*, login_patterns.*.*) accept a
				// comma-separated value; verify it is written, not rejected.
				if err := setConfigValue(cfg, key, "alpha,beta"); err != nil {
					t.Fatalf("config show emits list %q but config set rejects it: %v", key, err)
				}
				got, err := getConfigValue(cfg, key)
				if err != nil {
					t.Fatalf("getConfigValue(%q) after set: %v", key, err)
				}
				if !strings.Contains(got, "alpha") || !strings.Contains(got, "beta") {
					t.Errorf("set list %q did not take effect: %q", key, got)
				}
				return
			}

			// Scalar leaves must round-trip: writing back the value `get`
			// reported must reproduce it exactly.
			if err := setConfigValue(cfg, key, before); err != nil {
				t.Fatalf("config show emits %q but config set rejects it: %v", key, err)
			}
			after, err := getConfigValue(cfg, key)
			if err != nil {
				t.Fatalf("getConfigValue(%q) after set: %v", key, err)
			}
			if after != before {
				t.Errorf("round-trip mismatch for %q: before=%q after=%q", key, before, after)
			}
		})
	}
}

// TestSetConfigValue_UnknownStillErrors ensures the reflection writer keeps
// rejecting genuinely unknown keys so error behavior did not regress.
func TestSetConfigValue_UnknownStillErrors(t *testing.T) {
	cfg := config.DefaultSPMConfig()
	for _, key := range []string{"unknown_key", "unknown.section.key", "invalid_section.key", "stealth.rotation.bogus", "stealth.bogus", ""} {
		if err := setConfigValue(cfg, key, "x"); err == nil {
			t.Errorf("setConfigValue(%q) = nil error, want error", key)
		}
	}
}
