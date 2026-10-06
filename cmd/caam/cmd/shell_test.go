package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateBashInit(t *testing.T) {
	caamPath := "/usr/local/bin/caam"
	tools := []string{"claude", "codex", "gemini"}

	output := generateBashInit(caamPath, tools, false)

	// Check for wrapper functions
	if !strings.Contains(output, "claude()") {
		t.Error("Missing claude() function")
	}
	if !strings.Contains(output, "codex()") {
		t.Error("Missing codex() function")
	}
	if !strings.Contains(output, "gemini()") {
		t.Error("Missing gemini() function")
	}

	// Check for caam run usage
	if !strings.Contains(output, "caam run claude") {
		t.Error("Missing 'caam run claude' in wrapper")
	}

	// Check for completion
	if !strings.Contains(output, "_caam_completions") {
		t.Error("Missing completion function")
	}
	if !strings.Contains(output, "complete -F _caam_completions caam") {
		t.Error("Missing completion registration")
	}
}

func TestGenerateBashInit_NoWrap(t *testing.T) {
	caamPath := "/usr/local/bin/caam"
	tools := []string{"claude", "codex", "gemini"}

	output := generateBashInit(caamPath, tools, true)

	// Should NOT have wrapper functions
	if strings.Contains(output, "claude()") {
		t.Error("Should not have claude() function when noWrap=true")
	}

	// Should still have completions
	if !strings.Contains(output, "_caam_completions") {
		t.Error("Missing completion function even with noWrap=true")
	}
}

func TestGenerateBashInit_CustomTools(t *testing.T) {
	caamPath := "/usr/local/bin/caam"
	tools := []string{"claude"} // Only claude

	output := generateBashInit(caamPath, tools, false)

	if !strings.Contains(output, "claude()") {
		t.Error("Missing claude() function")
	}
	if strings.Contains(output, "codex()") {
		t.Error("Should not have codex() function")
	}
	if strings.Contains(output, "gemini()") {
		t.Error("Should not have gemini() function")
	}
}

func TestGenerateFishInit(t *testing.T) {
	caamPath := "/usr/local/bin/caam"
	tools := []string{"claude", "codex", "gemini"}

	output := generateFishInit(caamPath, tools, false)

	// Check for fish function syntax
	if !strings.Contains(output, "function claude") {
		t.Error("Missing claude function")
	}
	if !strings.Contains(output, "function codex") {
		t.Error("Missing codex function")
	}
	if !strings.Contains(output, "function gemini") {
		t.Error("Missing gemini function")
	}

	// Check for fish completion syntax
	if !strings.Contains(output, "complete -c caam") {
		t.Error("Missing fish completion")
	}
}

func TestGenerateFishInit_NoWrap(t *testing.T) {
	caamPath := "/usr/local/bin/caam"
	tools := []string{"claude"}

	output := generateFishInit(caamPath, tools, true)

	// Should NOT have wrapper functions
	if strings.Contains(output, "function claude") {
		t.Error("Should not have claude function when noWrap=true")
	}

	// Should still have completions
	if !strings.Contains(output, "complete -c caam") {
		t.Error("Missing fish completion even with noWrap=true")
	}
}

func TestDetectShell(t *testing.T) {
	// This test just ensures detectShell returns a valid shell name
	shell := detectShell()

	validShells := map[string]bool{
		"bash": true,
		"zsh":  true,
		"fish": true,
		"sh":   true,
	}

	if !validShells[shell] {
		t.Errorf("detectShell() returned unexpected value: %s", shell)
	}
}

func TestShellInitCommand(t *testing.T) {
	// Test that the command exists and can be executed
	cmd := shellInitCmd

	if cmd.Use != "init" {
		t.Errorf("shellInitCmd.Use = %s, want 'init'", cmd.Use)
	}

	// Check flags exist
	if cmd.Flags().Lookup("fish") == nil {
		t.Error("Missing --fish flag")
	}
	if cmd.Flags().Lookup("bash") == nil {
		t.Error("Missing --bash flag")
	}
	if cmd.Flags().Lookup("zsh") == nil {
		t.Error("Missing --zsh flag")
	}
	if cmd.Flags().Lookup("no-wrap") == nil {
		t.Error("Missing --no-wrap flag")
	}
	if cmd.Flags().Lookup("tools") == nil {
		t.Error("Missing --tools flag")
	}
}

func TestShellCompletionCommand(t *testing.T) {
	cmd := shellCompletionCmd

	if cmd.Use != "completion [bash|zsh|fish|powershell]" {
		t.Errorf("shellCompletionCmd.Use = %s", cmd.Use)
	}

	// Check valid args
	validArgs := cmd.ValidArgs
	expected := []string{"bash", "zsh", "fish", "powershell"}

	if len(validArgs) != len(expected) {
		t.Errorf("ValidArgs length = %d, want %d", len(validArgs), len(expected))
	}

	for _, arg := range expected {
		found := false
		for _, v := range validArgs {
			if v == arg {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Missing valid arg: %s", arg)
		}
	}
}

func TestShellInitOutput(t *testing.T) {
	// Capture stdout by running the command
	var buf bytes.Buffer
	cmd := shellInitCmd
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Reset flags
	cmd.Flags().Set("tools", "claude")
	cmd.Flags().Set("no-wrap", "false")

	// We can't easily test runShellInit directly since it writes to stdout,
	// but we can test the generators
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple path no quoting needed",
			input:    "/usr/local/bin/caam",
			expected: "/usr/local/bin/caam",
		},
		{
			name:     "path with spaces",
			input:    "/path with spaces/caam",
			expected: "'/path with spaces/caam'",
		},
		{
			name:     "path with single quote",
			input:    "/path'quote/caam",
			expected: "'/path'\"'\"'quote/caam'",
		},
		{
			name:     "path with dollar sign",
			input:    "/path$var/caam",
			expected: "'/path$var/caam'",
		},
		{
			name:     "path with backtick",
			input:    "/path`cmd`/caam",
			expected: "'/path`cmd`/caam'",
		},
		{
			name:     "path with semicolon (injection attempt)",
			input:    "/path; rm -rf /;/caam",
			expected: "'/path; rm -rf /;/caam'",
		},
		{
			name:     "path with pipe (injection attempt)",
			input:    "/path | cat /etc/passwd/caam",
			expected: "'/path | cat /etc/passwd/caam'",
		},
		{
			name:     "path with ampersand",
			input:    "/path && evil/caam",
			expected: "'/path && evil/caam'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.input)
			if got != tt.expected {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestFishQuote(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty argument",
			input:    "",
			expected: "''",
		},
		{
			name:     "backslash before quote",
			input:    "C:\\users\\owner's profile\\",
			expected: "'C:\\\\users\\\\owner\\'s profile\\\\'",
		},
		{
			name:     "consecutive backslashes",
			input:    `\\server\profiles`,
			expected: `'\\\\server\\profiles'`,
		},
		{
			name:     "simple path no quoting needed",
			input:    "/usr/local/bin/caam",
			expected: "/usr/local/bin/caam",
		},
		{
			name:     "path with spaces",
			input:    "/path with spaces/caam",
			expected: "'/path with spaces/caam'",
		},
		{
			name:     "path with single quote",
			input:    "/path'quote/caam",
			expected: "'/path\\'quote/caam'",
		},
		{
			name:     "path with dollar sign",
			input:    "/path$var/caam",
			expected: "'/path$var/caam'",
		},
		{
			name:     "path with semicolon",
			input:    "/path; rm -rf /;/caam",
			expected: "'/path; rm -rf /;/caam'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fishQuote(tt.input)
			if got != tt.expected {
				t.Errorf("fishQuote(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGenerateBashInit_PathWithSpaces(t *testing.T) {
	// This tests that paths with special characters are properly quoted
	caamPath := "/path with spaces/caam"
	tools := []string{"claude"}

	output := generateBashInit(caamPath, tools, false)

	// Should use quoted path
	if !strings.Contains(output, "'/path with spaces/caam'") {
		t.Error("Path with spaces should be single-quoted in output")
	}
}

func TestGenerateFishInit_PathWithSpaces(t *testing.T) {
	// This tests that paths with special characters are properly quoted
	caamPath := "/path with spaces/caam"
	tools := []string{"claude"}

	output := generateFishInit(caamPath, tools, false)

	// Should use quoted path
	if !strings.Contains(output, "'/path with spaces/caam'") {
		t.Error("Path with spaces should be single-quoted in output")
	}
}
