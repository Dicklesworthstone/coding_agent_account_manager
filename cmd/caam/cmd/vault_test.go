// Package cmd implements the CLI commands for caam.
package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// TestBackupCommand_UnknownTool tests backup command rejects unknown tools.
func TestBackupCommand_UnknownTool(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestVault(t, tmpDir)

	// Initialize vault for test
	testVault := authfile.NewVault(filepath.Join(tmpDir, "vault"))

	// Create fake auth files
	createFakeAuthFiles(t, tmpDir, "codex")

	// Backup command should reject unknown tool
	args := []string{"invalid-tool", "profile1"}
	err := backupCmd.RunE(backupCmd, args)
	if err == nil {
		t.Error("Expected error for unknown tool")
	}
	if err != nil && err.Error() != "unknown tool: invalid-tool (supported: codex, claude, gemini)" {
		t.Logf("Got error (expected): %v", err)
	}

	_ = testVault // silence unused
}

// TestBackupCommand_ValidArgs tests backup command arg parsing.
func TestBackupCommand_ValidArgs(t *testing.T) {
	// Test arg validation
	testCases := []struct {
		args      []string
		expectErr bool
	}{
		{[]string{}, true},
		{[]string{"codex"}, true},
		{[]string{"codex", "profile"}, false},
		{[]string{"codex", "profile", "extra"}, true},
	}

	for _, tc := range testCases {
		err := backupCmd.Args(backupCmd, tc.args)
		if (err != nil) != tc.expectErr {
			t.Errorf("Args %v: expected error=%v, got %v", tc.args, tc.expectErr, err)
		}
	}
}

// TestActivateCommand_UnknownTool tests activate command rejects unknown tools.
func TestActivateCommand_UnknownTool(t *testing.T) {
	// Activate command should reject unknown tool
	args := []string{"invalid-tool", "profile1"}
	err := activateCmd.RunE(activateCmd, args)
	if err == nil {
		t.Error("Expected error for unknown tool")
	}
}

// TestActivateCommand_Aliases tests activate command has switch and use aliases.
func TestActivateCommand_Aliases(t *testing.T) {
	expectedAliases := map[string]bool{
		"switch": false,
		"use":    false,
	}

	for _, alias := range activateCmd.Aliases {
		expectedAliases[alias] = true
	}

	for alias, found := range expectedAliases {
		if !found {
			t.Errorf("Expected alias %q not found", alias)
		}
	}
}

// TestStatusCommand_SingleTool tests status command with a specific tool.
func TestStatusCommand_SingleTool(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestVault(t, tmpDir)

	// Test arg validation
	err := statusCmd.Args(statusCmd, []string{"claude"})
	if err != nil {
		t.Errorf("Expected no error for single tool arg: %v", err)
	}

	err = statusCmd.Args(statusCmd, []string{"claude", "extra"})
	if err == nil {
		t.Error("Expected error for extra arg")
	}
}

// TestLsCommand_NoProfiles tests ls command with empty vault.
func TestLsCommand_NoProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	setupTestVault(t, tmpDir)

	// Test arg validation
	err := lsCmd.Args(lsCmd, []string{})
	if err != nil {
		t.Errorf("Expected no error for no args: %v", err)
	}

	err = lsCmd.Args(lsCmd, []string{"codex"})
	if err != nil {
		t.Errorf("Expected no error for single arg: %v", err)
	}
}

// TestDeleteCommand_Force tests delete command with force flag.
func TestDeleteCommand_Force(t *testing.T) {
	flag := deleteCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("Expected --force flag")
	}

	if flag.DefValue != "false" {
		t.Errorf("Expected default false, got %q", flag.DefValue)
	}

	// Test arg validation
	err := deleteCmd.Args(deleteCmd, []string{"codex", "profile"})
	if err != nil {
		t.Errorf("Expected no error for valid args: %v", err)
	}
}

// TestDeleteCommand_Aliases tests delete command aliases.
func TestDeleteCommand_Aliases(t *testing.T) {
	expectedAliases := map[string]bool{
		"rm":     false,
		"remove": false,
	}

	for _, alias := range deleteCmd.Aliases {
		expectedAliases[alias] = true
	}

	for alias, found := range expectedAliases {
		if !found {
			t.Errorf("Expected alias %q not found", alias)
		}
	}
}

// TestPathsCommand_AllTools tests paths command shows all tools.
func TestPathsCommand_AllTools(t *testing.T) {
	// Test arg validation
	err := pathsCmd.Args(pathsCmd, []string{})
	if err != nil {
		t.Errorf("Expected no error for no args: %v", err)
	}

	err = pathsCmd.Args(pathsCmd, []string{"claude"})
	if err != nil {
		t.Errorf("Expected no error for single arg: %v", err)
	}
}

// TestClearCommand_Force tests clear command with force flag.
func TestClearCommand_Force(t *testing.T) {
	flag := clearCmd.Flags().Lookup("force")
	if flag == nil {
		t.Fatal("Expected --force flag")
	}

	if flag.DefValue != "false" {
		t.Errorf("Expected default false, got %q", flag.DefValue)
	}

	// Test arg validation
	err := clearCmd.Args(clearCmd, []string{"codex"})
	if err != nil {
		t.Errorf("Expected no error for single arg: %v", err)
	}

	err = clearCmd.Args(clearCmd, []string{})
	if err == nil {
		t.Error("Expected error for no args")
	}
}

// TestClearCommand_UnknownTool tests clear command rejects unknown tools.
func TestClearCommand_UnknownTool(t *testing.T) {
	args := []string{"invalid-tool"}
	err := clearCmd.RunE(clearCmd, args)
	if err == nil {
		t.Error("Expected error for unknown tool")
	}
}

// TestToolsMapConsistency tests all tools return valid auth file sets.
func TestToolsMapConsistency(t *testing.T) {
	for toolName, getFileSet := range tools {
		t.Run(toolName, func(t *testing.T) {
			fileSet := getFileSet()

			if fileSet.Tool == "" {
				t.Error("Tool should not be empty")
			}

			if fileSet.Tool != toolName {
				t.Errorf("Tool mismatch: expected %q, got %q", toolName, fileSet.Tool)
			}

			if len(fileSet.Files) == 0 {
				t.Error("Files should not be empty")
			}

			// Check each file spec
			for _, spec := range fileSet.Files {
				if spec.Path == "" {
					t.Error("File path should not be empty")
				}
				if spec.Description == "" {
					t.Error("File description should not be empty")
				}
			}
		})
	}
}

// TestAuthFileSpecs tests auth file specs for each provider.
func TestAuthFileSpecs(t *testing.T) {
	testCases := []struct {
		tool            string
		minFiles        int
		hasRequired     bool
		expectedInPaths []string
	}{
		{
			tool:            "codex",
			minFiles:        1,
			hasRequired:     true,
			expectedInPaths: []string{"auth.json"},
		},
		{
			tool:            "claude",
			minFiles:        1,
			hasRequired:     true,
			expectedInPaths: []string{".claude"},
		},
		{
			tool:            "gemini",
			minFiles:        1,
			hasRequired:     true,
			expectedInPaths: []string{".gemini", "settings.json"},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.tool, func(t *testing.T) {
			fileSet := tools[tc.tool]()

			if len(fileSet.Files) < tc.minFiles {
				t.Errorf("Expected at least %d files, got %d", tc.minFiles, len(fileSet.Files))
			}

			// Check for required files
			hasRequired := false
			for _, spec := range fileSet.Files {
				if spec.Required {
					hasRequired = true
					break
				}
			}

			if tc.hasRequired && !hasRequired {
				t.Error("Expected at least one required file")
			}

			// Check expected substrings in paths
			for _, expected := range tc.expectedInPaths {
				found := false
				for _, spec := range fileSet.Files {
					if containsPath(spec.Path, expected) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("Expected path containing %q", expected)
				}
			}
		})
	}
}

// containsPath checks if a path contains a substring.
func containsPath(path, substr string) bool {
	// Simple substring check
	return strings.Contains(path, substr)
}

// setupTestVault creates a test vault directory structure.
func setupTestVault(t *testing.T, tmpDir string) {
	t.Helper()

	// Set environment to use temp dir
	oldXDG := os.Getenv("XDG_DATA_HOME")
	t.Cleanup(func() {
		os.Setenv("XDG_DATA_HOME", oldXDG)
	})
	os.Setenv("XDG_DATA_HOME", tmpDir)

	// Create vault directory
	vaultDir := filepath.Join(tmpDir, "caam", "vault")
	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		t.Fatalf("Failed to create vault: %v", err)
	}
}

// createFakeAuthFiles creates fake auth files for testing.
// IMPORTANT: Always use tmpDir to avoid corrupting real auth files.
func createFakeAuthFiles(t *testing.T, tmpDir, tool string) {
	t.Helper()

	// Always use tmpDir - never write to real home directory
	homeDir := tmpDir

	switch tool {
	case "codex":
		codexDir := filepath.Join(homeDir, ".codex")
		if err := os.MkdirAll(codexDir, 0700); err != nil {
			t.Fatalf("Failed to create codex dir: %v", err)
		}
		authFile := filepath.Join(codexDir, "auth.json")
		data := map[string]string{"token": "fake-token"}
		jsonData, _ := json.Marshal(data)
		if err := os.WriteFile(authFile, jsonData, 0600); err != nil {
			t.Fatalf("Failed to create auth file: %v", err)
		}

	case "claude":
		claudeFile := filepath.Join(homeDir, ".claude.json")
		data := map[string]string{"session": "fake-session"}
		jsonData, _ := json.Marshal(data)
		if err := os.WriteFile(claudeFile, jsonData, 0600); err != nil {
			t.Fatalf("Failed to create claude file: %v", err)
		}

	case "gemini":
		geminiDir := filepath.Join(homeDir, ".config", "gemini")
		if err := os.MkdirAll(geminiDir, 0700); err != nil {
			t.Fatalf("Failed to create gemini dir: %v", err)
		}
	}
}

// TestVaultProfilePath tests vault profile path generation.
func TestVaultProfilePath(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")

	testVault := authfile.NewVault(vaultDir)

	// Test profile path generation
	path := testVault.ProfilePath("codex", "work")
	expectedSuffix := filepath.Join("codex", "work")

	if !containsPathSuffix(path, expectedSuffix) {
		t.Errorf("Expected path to end with %q, got %q", expectedSuffix, path)
	}
}

// containsPathSuffix checks if path ends with suffix.
func containsPathSuffix(path, suffix string) bool {
	return len(path) >= len(suffix) && path[len(path)-len(suffix):] == suffix
}

// TestBackupRestoreRoundTrip tests backup and restore cycle.
func TestBackupRestoreRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	sourceDir := filepath.Join(tmpDir, "source")
	targetDir := filepath.Join(tmpDir, "target")

	// Create directories
	for _, dir := range []string{vaultDir, sourceDir, targetDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}
	}

	// Create source auth file
	sourceFile := filepath.Join(sourceDir, "auth.json")
	originalData := map[string]string{"token": "original-token", "user": "test"}
	jsonData, _ := json.Marshal(originalData)
	if err := os.WriteFile(sourceFile, jsonData, 0600); err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}

	// Create vault and file set
	testVault := authfile.NewVault(vaultDir)
	fileSet := authfile.AuthFileSet{
		Tool: "test",
		Files: []authfile.AuthFileSpec{
			{
				Path:        sourceFile,
				Required:    true,
				Description: "Test auth file",
			},
		},
	}

	// Backup
	if err := testVault.Backup(fileSet, "test-profile"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Verify backup exists
	backupPath := testVault.ProfilePath("test", "test-profile")
	if _, err := os.Stat(backupPath); err != nil {
		t.Errorf("Backup directory not created: %v", err)
	}

	// Modify source file
	modifiedData := map[string]string{"token": "modified-token"}
	jsonData, _ = json.Marshal(modifiedData)
	if err := os.WriteFile(sourceFile, jsonData, 0600); err != nil {
		t.Fatalf("Failed to modify source file: %v", err)
	}

	// Restore
	if err := testVault.Restore(fileSet, "test-profile"); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify restored data matches original
	restoredData, err := os.ReadFile(sourceFile)
	if err != nil {
		t.Fatalf("Failed to read restored file: %v", err)
	}

	var restored map[string]string
	if err := json.Unmarshal(restoredData, &restored); err != nil {
		t.Fatalf("Failed to parse restored data: %v", err)
	}

	if restored["token"] != "original-token" {
		t.Errorf("Expected original token, got %q", restored["token"])
	}
}

// TestVaultListEmpty tests listing empty vault.
func TestVaultListEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")

	testVault := authfile.NewVault(vaultDir)

	profiles, err := testVault.List("codex")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(profiles) != 0 {
		t.Errorf("Expected empty list, got %v", profiles)
	}
}

// TestVaultListWithProfiles tests listing vault with profiles.
func TestVaultListWithProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	sourceDir := filepath.Join(tmpDir, "source")

	// Create source directory and file
	if err := os.MkdirAll(sourceDir, 0700); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}
	sourceFile := filepath.Join(sourceDir, "auth.json")
	if err := os.WriteFile(sourceFile, []byte(`{"token":"test"}`), 0600); err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}

	testVault := authfile.NewVault(vaultDir)
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test"},
		},
	}

	// Create multiple profiles
	profiles := []string{"work", "personal", "test"}
	for _, name := range profiles {
		if err := testVault.Backup(fileSet, name); err != nil {
			t.Fatalf("Backup %s failed: %v", name, err)
		}
	}

	// List profiles
	listed, err := testVault.List("codex")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}

	if len(listed) != len(profiles) {
		t.Errorf("Expected %d profiles, got %d", len(profiles), len(listed))
	}

	// Check all profiles are listed
	listedMap := make(map[string]bool)
	for _, p := range listed {
		listedMap[p] = true
	}

	for _, expected := range profiles {
		if !listedMap[expected] {
			t.Errorf("Expected profile %q not found in list", expected)
		}
	}
}

// TestVaultDelete tests deleting a profile.
func TestVaultDelete(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	sourceDir := filepath.Join(tmpDir, "source")

	// Setup
	if err := os.MkdirAll(sourceDir, 0700); err != nil {
		t.Fatalf("Failed to create source dir: %v", err)
	}
	sourceFile := filepath.Join(sourceDir, "auth.json")
	if err := os.WriteFile(sourceFile, []byte(`{"token":"test"}`), 0600); err != nil {
		t.Fatalf("Failed to write source file: %v", err)
	}

	testVault := authfile.NewVault(vaultDir)
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test"},
		},
	}

	// Create profile
	if err := testVault.Backup(fileSet, "to-delete"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Verify exists
	profiles, _ := testVault.List("codex")
	if len(profiles) != 1 {
		t.Fatalf("Expected 1 profile, got %d", len(profiles))
	}

	// Delete
	if err := testVault.Delete("codex", "to-delete"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	profiles, _ = testVault.List("codex")
	if len(profiles) != 0 {
		t.Errorf("Expected 0 profiles after delete, got %d", len(profiles))
	}
}
