package cmd

import (
	"testing"
)

// =============================================================================
// cleanup.go Tests
// =============================================================================

func TestCleanupCommand(t *testing.T) {
	if cleanupCmd.Use != "cleanup" {
		t.Errorf("Expected Use 'cleanup', got %q", cleanupCmd.Use)
	}

	if cleanupCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if cleanupCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestCleanupCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"dry-run", "false"},
		{"days", "0"},
		{"quiet", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := cleanupCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Expected flag --%s", tt.name)
				return
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("Expected default %q, got %q", tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestDBStatsCommand(t *testing.T) {
	if dbStatsCmd.Use != "db" {
		t.Errorf("Expected Use 'db', got %q", dbStatsCmd.Use)
	}

	if dbStatsCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

func TestDBStatsShowCommand(t *testing.T) {
	if dbStatsShowCmd.Use != "stats" {
		t.Errorf("Expected Use 'stats', got %q", dbStatsShowCmd.Use)
	}

	if dbStatsShowCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if dbStatsShowCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

// =============================================================================
// defaults.go Tests
// =============================================================================

func TestUseCommand(t *testing.T) {
	if useCmd.Use != "use <provider> <profile>" {
		t.Errorf("Expected Use 'use <provider> <profile>', got %q", useCmd.Use)
	}

	if useCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if useCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestUseCommandArgs(t *testing.T) {
	// useCmd requires exactly 2 args
	err := useCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = useCmd.Args(nil, []string{"codex"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}

	err = useCmd.Args(nil, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = useCmd.Args(nil, []string{"codex", "work", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}
}

func TestWhichCommand(t *testing.T) {
	if whichCmd.Use != "which [provider]" {
		t.Errorf("Expected Use 'which [provider]', got %q", whichCmd.Use)
	}

	if whichCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if whichCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestWhichCommandArgs(t *testing.T) {
	// whichCmd allows 0 or 1 args
	err := whichCmd.Args(nil, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args, got %v", err)
	}

	err = whichCmd.Args(nil, []string{"codex"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = whichCmd.Args(nil, []string{"codex", "extra"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

// =============================================================================
// env.go Tests
// =============================================================================

func TestEnvCmd_Structure(t *testing.T) {
	if envCmd.Use != "env <tool> <profile>" {
		t.Errorf("Expected Use 'env <tool> <profile>', got %q", envCmd.Use)
	}

	if envCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if envCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestEnvCmd_Flags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"unset", "false"},
		{"export-prefix", "export"},
		{"fish", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := envCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Expected flag --%s", tt.name)
				return
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("Expected default %q, got %q", tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestEnvCmd_Args(t *testing.T) {
	// envCmd requires exactly 2 args
	err := envCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = envCmd.Args(nil, []string{"codex"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}

	err = envCmd.Args(nil, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = envCmd.Args(nil, []string{"codex", "work", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}
}
