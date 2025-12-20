package cmd

import (
	"testing"
)

// =============================================================================
// export.go Command Tests
// =============================================================================

func TestExportCommand(t *testing.T) {
	if exportCmd.Use != "export [tool/profile] [tool profile]" {
		t.Errorf("Expected Use 'export [tool/profile] [tool profile]', got %q", exportCmd.Use)
	}

	if exportCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if exportCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestExportCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"all", "false"},
		{"output", ""},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := exportCmd.Flags().Lookup(tt.name)
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

func TestExportCommandArgs(t *testing.T) {
	// exportCmd accepts 0-2 args
	err := exportCmd.Args(nil, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args, got %v", err)
	}

	err = exportCmd.Args(nil, []string{"codex/work"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = exportCmd.Args(nil, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = exportCmd.Args(nil, []string{"codex", "work", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}
}

// =============================================================================
// import.go Command Tests
// =============================================================================

func TestImportCommand(t *testing.T) {
	if importCmd.Use != "import <archive.tar.gz|->" {
		t.Errorf("Expected Use 'import <archive.tar.gz|->', got %q", importCmd.Use)
	}

	if importCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if importCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestImportCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"as", ""},
		{"force", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := importCmd.Flags().Lookup(tt.name)
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

func TestImportCommandArgs(t *testing.T) {
	// importCmd requires exactly 1 arg
	err := importCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = importCmd.Args(nil, []string{"archive.tar.gz"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = importCmd.Args(nil, []string{"archive.tar.gz", "extra"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

// =============================================================================
// parseToolProfileArg Tests
// =============================================================================

func TestParseToolProfileArg(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantTool    string
		wantProfile string
		wantErr     bool
	}{
		{
			name:        "valid codex/work",
			input:       "codex/work",
			wantTool:    "codex",
			wantProfile: "work",
			wantErr:     false,
		},
		{
			name:        "valid claude/personal",
			input:       "claude/personal",
			wantTool:    "claude",
			wantProfile: "personal",
			wantErr:     false,
		},
		{
			name:        "uppercase tool normalized",
			input:       "CODEX/Work",
			wantTool:    "codex",
			wantProfile: "Work",
			wantErr:     false,
		},
		{
			name:        "with whitespace",
			input:       "  codex / work  ",
			wantTool:    "codex",
			wantProfile: "work",
			wantErr:     false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no separator",
			input:   "codexwork",
			wantErr: true,
		},
		{
			name:    "empty tool",
			input:   "/work",
			wantErr: true,
		},
		{
			name:    "empty profile",
			input:   "codex/",
			wantErr: true,
		},
		{
			name:    "only separator",
			input:   "/",
			wantErr: true,
		},
		{
			name:    "multiple separators",
			input:   "codex/work/extra",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, profile, err := parseToolProfileArg(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseToolProfileArg(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if tool != tt.wantTool {
					t.Errorf("parseToolProfileArg(%q) tool = %q, want %q", tt.input, tool, tt.wantTool)
				}
				if profile != tt.wantProfile {
					t.Errorf("parseToolProfileArg(%q) profile = %q, want %q", tt.input, profile, tt.wantProfile)
				}
			}
		})
	}
}
