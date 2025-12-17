// Package cmd implements the CLI commands for caam.
package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// Test helpers

// captureOutput captures stdout and stderr from a command execution.
func captureOutput(t *testing.T, cmd *cobra.Command, args []string) (stdout, stderr string, err error) {
	t.Helper()

	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(args)

	err = cmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

// createTestCmd creates a fresh root command for testing.
func createTestCmd() *cobra.Command {
	return rootCmd
}

// TestRootCommand tests the root command exists and has correct metadata.
func TestRootCommand(t *testing.T) {
	cmd := createTestCmd()

	if cmd.Use != "caam" {
		t.Errorf("Expected Use 'caam', got %q", cmd.Use)
	}

	if cmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if cmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}

	// Check that RunE is set (launches TUI when no args)
	if cmd.RunE == nil {
		t.Error("Expected RunE to be set")
	}

	// Check PersistentPreRunE is set (initializes globals)
	if cmd.PersistentPreRunE == nil {
		t.Error("Expected PersistentPreRunE to be set")
	}
}

// TestSubcommandRegistration tests that all expected subcommands are registered.
func TestSubcommandRegistration(t *testing.T) {
	cmd := createTestCmd()

	expectedCommands := []string{
		"version",
		"backup",
		"activate",
		"status",
		"ls",
		"delete",
		"paths",
		"clear",
		"profile",
		"login",
		"exec",
		"doctor",
		"sessions",
		"env",
		"init",
		"open",
	}

	commands := cmd.Commands()
	cmdMap := make(map[string]bool)
	for _, c := range commands {
		cmdMap[c.Use] = true
		// Also map by first word of Use (e.g., "backup <tool> <profile>" -> "backup")
		parts := strings.Fields(c.Use)
		if len(parts) > 0 {
			cmdMap[parts[0]] = true
		}
	}

	for _, expected := range expectedCommands {
		if !cmdMap[expected] {
			t.Errorf("Expected subcommand %q to be registered", expected)
		}
	}
}

// TestVersionCommand tests the version command output.
func TestVersionCommand(t *testing.T) {
	if versionCmd.Use != "version" {
		t.Errorf("Expected Use 'version', got %q", versionCmd.Use)
	}

	if versionCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	// Run is set (not RunE)
	if versionCmd.Run == nil {
		t.Error("Expected Run to be set")
	}
}

// TestBackupCommandFlags tests the backup command has correct arg requirements.
func TestBackupCommandFlags(t *testing.T) {
	if backupCmd.Use != "backup <tool> <profile-name>" {
		t.Errorf("Unexpected Use: %q", backupCmd.Use)
	}

	// Check Args expects exactly 2 args
	if backupCmd.Args == nil {
		t.Error("Expected Args validator to be set")
	}

	// Test that Args function rejects wrong number of arguments
	err := backupCmd.Args(backupCmd, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = backupCmd.Args(backupCmd, []string{"codex"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}

	err = backupCmd.Args(backupCmd, []string{"codex", "profile", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}

	err = backupCmd.Args(backupCmd, []string{"codex", "profile"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}
}

// TestActivateCommandFlags tests the activate command flags and aliases.
func TestActivateCommandFlags(t *testing.T) {
	if activateCmd.Use != "activate <tool> <profile-name>" {
		t.Errorf("Unexpected Use: %q", activateCmd.Use)
	}

	// Check aliases
	expectedAliases := []string{"switch", "use"}
	for _, alias := range expectedAliases {
		found := false
		for _, a := range activateCmd.Aliases {
			if a == alias {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected alias %q not found", alias)
		}
	}

	// Check --backup-current flag exists
	flag := activateCmd.Flags().Lookup("backup-current")
	if flag == nil {
		t.Error("Expected --backup-current flag")
	} else if flag.DefValue != "false" {
		t.Errorf("Expected default false, got %q", flag.DefValue)
	}
}

// TestStatusCommandArgs tests the status command accepts optional tool argument.
func TestStatusCommandArgs(t *testing.T) {
	if statusCmd.Use != "status [tool]" {
		t.Errorf("Unexpected Use: %q", statusCmd.Use)
	}

	// Should accept 0 or 1 args
	err := statusCmd.Args(statusCmd, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args, got %v", err)
	}

	err = statusCmd.Args(statusCmd, []string{"claude"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = statusCmd.Args(statusCmd, []string{"claude", "extra"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

// TestLsCommandAliases tests the ls command has the list alias.
func TestLsCommandAliases(t *testing.T) {
	if lsCmd.Use != "ls [tool]" {
		t.Errorf("Unexpected Use: %q", lsCmd.Use)
	}

	found := false
	for _, alias := range lsCmd.Aliases {
		if alias == "list" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'list' alias")
	}
}

// TestDeleteCommandFlags tests the delete command flags.
func TestDeleteCommandFlags(t *testing.T) {
	if deleteCmd.Use != "delete <tool> <profile-name>" {
		t.Errorf("Unexpected Use: %q", deleteCmd.Use)
	}

	// Check aliases
	found := false
	for _, alias := range deleteCmd.Aliases {
		if alias == "rm" || alias == "remove" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected 'rm' or 'remove' alias")
	}

	// Check --force flag exists
	flag := deleteCmd.Flags().Lookup("force")
	if flag == nil {
		t.Error("Expected --force flag")
	}
}

// TestClearCommandFlags tests the clear command flags.
func TestClearCommandFlags(t *testing.T) {
	if clearCmd.Use != "clear <tool>" {
		t.Errorf("Unexpected Use: %q", clearCmd.Use)
	}

	// Check --force flag exists
	flag := clearCmd.Flags().Lookup("force")
	if flag == nil {
		t.Error("Expected --force flag")
	}
}

// TestPathsCommandArgs tests the paths command args.
func TestPathsCommandArgs(t *testing.T) {
	if pathsCmd.Use != "paths [tool]" {
		t.Errorf("Unexpected Use: %q", pathsCmd.Use)
	}

	// Should accept 0 or 1 args
	err := pathsCmd.Args(pathsCmd, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args, got %v", err)
	}
}

// TestProfileCommand tests the profile parent command and subcommands.
func TestProfileCommand(t *testing.T) {
	if profileCmd.Use != "profile" {
		t.Errorf("Unexpected Use: %q", profileCmd.Use)
	}

	// Check subcommands are registered
	commands := profileCmd.Commands()
	cmdMap := make(map[string]bool)
	for _, c := range commands {
		parts := strings.Fields(c.Use)
		if len(parts) > 0 {
			cmdMap[parts[0]] = true
		}
	}

	expectedSubcommands := []string{"add", "ls", "delete", "status", "unlock"}
	for _, expected := range expectedSubcommands {
		if !cmdMap[expected] {
			t.Errorf("Expected profile subcommand %q", expected)
		}
	}
}

// TestProfileAddFlags tests the profile add command flags.
func TestProfileAddFlags(t *testing.T) {
	expectedFlags := []string{"auth-mode", "browser", "browser-profile", "browser-name"}

	for _, name := range expectedFlags {
		flag := profileAddCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Expected --%s flag", name)
		}
	}
}

// TestProfileDeleteFlags tests the profile delete command flags.
func TestProfileDeleteFlags(t *testing.T) {
	flag := profileDeleteCmd.Flags().Lookup("force")
	if flag == nil {
		t.Error("Expected --force flag")
	}
}

// TestProfileUnlockFlags tests the profile unlock command flags.
func TestProfileUnlockFlags(t *testing.T) {
	flag := profileUnlockCmd.Flags().Lookup("force")
	if flag == nil {
		t.Error("Expected --force flag")
	}

	// Check shorthand
	if flag.Shorthand != "f" {
		t.Errorf("Expected shorthand 'f', got %q", flag.Shorthand)
	}
}

// TestLoginCommandArgs tests the login command requires 2 args.
func TestLoginCommandArgs(t *testing.T) {
	if loginCmd.Use != "login <tool> <profile>" {
		t.Errorf("Unexpected Use: %q", loginCmd.Use)
	}

	err := loginCmd.Args(loginCmd, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = loginCmd.Args(loginCmd, []string{"codex"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}
}

// TestExecCommandFlags tests the exec command flags.
func TestExecCommandFlags(t *testing.T) {
	flag := execCmd.Flags().Lookup("no-lock")
	if flag == nil {
		t.Error("Expected --no-lock flag")
	}
}

// TestToolsMap verifies the tools map contains expected providers.
func TestToolsMap(t *testing.T) {
	expectedTools := []string{"codex", "claude", "gemini"}

	for _, tool := range expectedTools {
		if _, ok := tools[tool]; !ok {
			t.Errorf("Expected tool %q in tools map", tool)
		}
	}

	// Verify each tool returns auth files
	for tool, getFileSet := range tools {
		fileSet := getFileSet()
		if fileSet.Tool == "" {
			t.Errorf("Expected Tool for tool %q", tool)
		}
		if len(fileSet.Files) == 0 {
			t.Errorf("Expected Files for tool %q", tool)
		}
	}
}

// TestCommandDescriptions verifies all commands have descriptions.
func TestCommandDescriptions(t *testing.T) {
	cmd := createTestCmd()

	// Walk all commands and check descriptions
	var checkCmd func(*cobra.Command)
	checkCmd = func(c *cobra.Command) {
		if c.Short == "" {
			t.Errorf("Command %q missing Short description", c.Use)
		}
		for _, sub := range c.Commands() {
			checkCmd(sub)
		}
	}

	for _, sub := range cmd.Commands() {
		checkCmd(sub)
	}
}

// TestWithTempDir runs a test with a temporary directory for data.
func TestWithTempDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Set XDG_DATA_HOME to temp dir
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	// Create necessary subdirectories
	vaultDir := filepath.Join(tmpDir, "caam", "vault")
	profilesDir := filepath.Join(tmpDir, "caam", "profiles")

	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		t.Fatalf("Failed to create vault dir: %v", err)
	}
	if err := os.MkdirAll(profilesDir, 0700); err != nil {
		t.Fatalf("Failed to create profiles dir: %v", err)
	}

	// Test that directories exist
	if _, err := os.Stat(vaultDir); err != nil {
		t.Errorf("Vault dir not created: %v", err)
	}
	if _, err := os.Stat(profilesDir); err != nil {
		t.Errorf("Profiles dir not created: %v", err)
	}
}

// TestValidToolNames verifies tool name validation works.
func TestValidToolNames(t *testing.T) {
	validTools := []string{"codex", "claude", "gemini", "CODEX", "Claude", "GEMINI"}

	for _, tool := range validTools {
		normalized := strings.ToLower(tool)
		if _, ok := tools[normalized]; !ok {
			t.Errorf("Expected normalized tool %q to be valid", normalized)
		}
	}

	invalidTools := []string{"invalid", "foo", "bar", ""}
	for _, tool := range invalidTools {
		if _, ok := tools[tool]; ok {
			t.Errorf("Expected tool %q to be invalid", tool)
		}
	}
}

// TestCommandUsageStrings verifies usage strings are properly formatted.
func TestCommandUsageStrings(t *testing.T) {
	commands := []struct {
		cmd      *cobra.Command
		expected string
	}{
		{backupCmd, "backup <tool> <profile-name>"},
		{activateCmd, "activate <tool> <profile-name>"},
		{statusCmd, "status [tool]"},
		{lsCmd, "ls [tool]"},
		{deleteCmd, "delete <tool> <profile-name>"},
		{pathsCmd, "paths [tool]"},
		{clearCmd, "clear <tool>"},
		{loginCmd, "login <tool> <profile>"},
	}

	for _, tc := range commands {
		if tc.cmd.Use != tc.expected {
			t.Errorf("Command Use mismatch: expected %q, got %q", tc.expected, tc.cmd.Use)
		}
	}
}
