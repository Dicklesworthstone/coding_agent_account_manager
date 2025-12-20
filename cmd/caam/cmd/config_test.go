package cmd

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
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
