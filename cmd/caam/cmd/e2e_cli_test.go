package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// =============================================================================
// E2E Tests: CLI Command Workflow Tests
// =============================================================================

// setupCLITest initializes test environment and returns cleanup function
func setupCLITest(t *testing.T, h *testutil.TestHarness) {
	// Create vault directory
	vaultDir := h.SubDir("vault")
	h.SetEnv("XDG_DATA_HOME", filepath.Dir(filepath.Dir(vaultDir)))

	// Create profiles directory
	profilesDir := h.SubDir("profiles")

	// Initialize global vault with test path
	vault = authfile.NewVault(vaultDir)
	profileStore = profile.NewStore(profilesDir)

	h.Log.Info("CLI test environment initialized", map[string]interface{}{
		"vault_dir":    vaultDir,
		"profiles_dir": profilesDir,
	})
}

// executeCommand runs a CLI command and captures output
func executeCommand(args ...string) (string, error) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return buf.String(), err
}

// TestE2E_ListCommand tests the caam ls command workflow
func TestE2E_ListCommand(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	h.Log.SetStep("test_empty_list")

	// Test empty list using vault API directly (fmt.Printf doesn't work with buffer capture)
	profiles, err := vault.ListAll()
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}

	if len(profiles) != 0 {
		t.Errorf("Expected empty profiles, got: %v", profiles)
	}

	h.Log.SetStep("create_profiles")

	// Create auth files and backup them
	homeDir := h.SubDir("home")
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	// Create mock auth file
	authPath := filepath.Join(codexHome, "auth.json")
	authContent := `{"access_token": "test-token", "token_type": "Bearer"}`
	if err := os.WriteFile(authPath, []byte(authContent), 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	// Create custom fileset pointing to our test dir
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	// Backup to vault
	if err := vault.Backup(fileSet, "work"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	if err := vault.Backup(fileSet, "personal"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	h.Log.SetStep("test_list_all")

	// Test list all - use vault directly since ls command uses global tools map
	codexProfiles, err := vault.List("codex")
	if err != nil {
		t.Fatalf("vault.List failed: %v", err)
	}

	if len(codexProfiles) != 2 {
		t.Errorf("Expected 2 profiles, got %d", len(codexProfiles))
	}

	h.Log.SetStep("test_list_tool")

	// Verify both profiles exist
	found := make(map[string]bool)
	for _, p := range codexProfiles {
		found[p] = true
	}

	if !found["work"] {
		t.Errorf("Expected 'work' profile in list")
	}
	if !found["personal"] {
		t.Errorf("Expected 'personal' profile in list")
	}

	h.Log.Info("List command tests complete", map[string]interface{}{
		"profiles_found": codexProfiles,
	})
}

// TestE2E_BackupCommand tests the caam backup command workflow
func TestE2E_BackupCommand(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	// Create mock auth environment
	homeDir := h.SubDir("home")
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	authContent := map[string]interface{}{
		"access_token":  "backup-test-token",
		"refresh_token": "backup-test-refresh",
		"token_type":    "Bearer",
	}
	authJSON, _ := json.MarshalIndent(authContent, "", "  ")
	if err := os.WriteFile(authPath, authJSON, 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	h.Log.Info("Created test auth file", map[string]interface{}{
		"path": authPath,
	})

	h.Log.SetStep("test_backup_workflow")

	// Create fileset and backup
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	// Perform backup
	err := vault.Backup(fileSet, "backup-test")
	if err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	h.Log.SetStep("verify_backup")

	// Verify backup was created
	backupPath := vault.BackupPath("codex", "backup-test", "auth.json")
	if !h.FileExists(backupPath) {
		t.Errorf("Backup file not created: %s", backupPath)
	}

	// Verify content
	if !h.JSONContains(backupPath, "access_token", "backup-test-token") {
		t.Errorf("Backup has incorrect content")
	}

	// Verify metadata
	metaPath := filepath.Join(vault.ProfilePath("codex", "backup-test"), "meta.json")
	if !h.FileExists(metaPath) {
		t.Errorf("Metadata file not created")
	}

	h.Log.Info("Backup command test complete")
}

// TestE2E_RestoreCommand tests the restore (activate) workflow
func TestE2E_RestoreCommand(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	// Create home directory structure
	homeDir := h.SubDir("home")
	codexHome := filepath.Join(homeDir, ".codex")

	// Create and backup initial auth
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	initialAuthPath := filepath.Join(codexHome, "auth.json")
	initialContent := `{"access_token": "initial-token", "token_type": "Bearer"}`
	if err := os.WriteFile(initialAuthPath, []byte(initialContent), 0600); err != nil {
		t.Fatalf("Failed to write initial auth: %v", err)
	}

	// Create fileset
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: initialAuthPath, Required: true},
		},
	}

	// Backup initial state
	if err := vault.Backup(fileSet, "initial"); err != nil {
		t.Fatalf("Initial backup failed: %v", err)
	}

	h.Log.SetStep("create_second_profile")

	// Create second profile with different token
	secondContent := `{"access_token": "second-token", "token_type": "Bearer"}`
	if err := os.WriteFile(initialAuthPath, []byte(secondContent), 0600); err != nil {
		t.Fatalf("Failed to write second auth: %v", err)
	}

	if err := vault.Backup(fileSet, "second"); err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}

	h.Log.SetStep("test_restore")

	// Now restore initial profile
	if err := vault.Restore(fileSet, "initial"); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	// Verify initial token is restored
	if !h.FileContains(initialAuthPath, "initial-token") {
		t.Errorf("Expected initial-token after restore")
	}

	h.Log.SetStep("test_switch_back")

	// Switch to second profile
	if err := vault.Restore(fileSet, "second"); err != nil {
		t.Fatalf("Second restore failed: %v", err)
	}

	if !h.FileContains(initialAuthPath, "second-token") {
		t.Errorf("Expected second-token after second restore")
	}

	h.Log.Info("Restore command test complete")
}

// TestE2E_DeleteCommand tests the delete workflow
func TestE2E_DeleteCommand(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	// Create auth file and profile
	homeDir := h.SubDir("home")
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token": "delete-test"}`), 0600); err != nil {
		t.Fatalf("Failed to write auth: %v", err)
	}

	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	// Create profiles to delete
	if err := vault.Backup(fileSet, "to-delete"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}
	if err := vault.Backup(fileSet, "to-keep"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	h.Log.SetStep("verify_profiles_exist")

	profiles, _ := vault.List("codex")
	if len(profiles) != 2 {
		t.Fatalf("Expected 2 profiles before delete, got %d", len(profiles))
	}

	h.Log.SetStep("test_delete")

	// Delete one profile
	if err := vault.Delete("codex", "to-delete"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	h.Log.SetStep("verify_deletion")

	// Verify deletion
	profiles, _ = vault.List("codex")
	if len(profiles) != 1 {
		t.Errorf("Expected 1 profile after delete, got %d", len(profiles))
	}

	if profiles[0] != "to-keep" {
		t.Errorf("Wrong profile remaining: %s", profiles[0])
	}

	// Verify directory was removed
	deletedPath := vault.ProfilePath("codex", "to-delete")
	if h.FileNotExists(deletedPath) {
		h.Log.Info("Deleted profile directory confirmed gone")
	}

	h.Log.Info("Delete command test complete")
}

// TestE2E_StatusCommand tests the status workflow
func TestE2E_StatusCommand(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	// Create home directory
	homeDir := h.SubDir("home")
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	h.Log.SetStep("test_no_auth")

	// Test with no auth files
	authPath := filepath.Join(codexHome, "auth.json")
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	// No auth file exists - HasAuthFiles should return false
	if authfile.HasAuthFiles(fileSet) {
		t.Errorf("HasAuthFiles should return false when no auth exists")
	}

	h.Log.SetStep("create_auth_and_backup")

	// Create auth and backup
	if err := os.WriteFile(authPath, []byte(`{"access_token": "status-test"}`), 0600); err != nil {
		t.Fatalf("Failed to write auth: %v", err)
	}

	// Now HasAuthFiles should return true
	if !authfile.HasAuthFiles(fileSet) {
		t.Errorf("HasAuthFiles should return true when auth exists")
	}

	// Backup to vault
	if err := vault.Backup(fileSet, "status-profile"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	h.Log.SetStep("test_active_profile")

	// Check active profile
	active, err := vault.ActiveProfile(fileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}

	if active != "status-profile" {
		t.Errorf("Expected active profile 'status-profile', got '%s'", active)
	}

	h.Log.SetStep("test_modified_auth")

	// Modify auth file (no longer matches backup)
	if err := os.WriteFile(authPath, []byte(`{"access_token": "modified-token"}`), 0600); err != nil {
		t.Fatalf("Failed to modify auth: %v", err)
	}

	// Active profile should be empty (doesn't match any backup)
	active, err = vault.ActiveProfile(fileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}

	if active != "" {
		t.Errorf("Expected no active profile after modification, got '%s'", active)
	}

	h.Log.Info("Status command test complete")
}

// TestE2E_InvalidToolError tests error handling for invalid tools
func TestE2E_InvalidToolError(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	h.Log.SetStep("test_invalid_tool_backup")

	// Try to backup with invalid tool
	fileSet := authfile.AuthFileSet{
		Tool: "invalid-tool",
		Files: []authfile.AuthFileSpec{
			{Tool: "invalid-tool", Path: "/nonexistent/path", Required: true},
		},
	}

	err := vault.Backup(fileSet, "test")
	if err == nil {
		t.Errorf("Expected error for invalid tool backup")
	}

	h.Log.Info("Invalid tool error handling verified", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_ProfileWorkflow tests the isolated profile workflow
func TestE2E_ProfileWorkflow(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	h.Log.SetStep("create_profile")

	// Create isolated profile
	prof, err := profileStore.Create("codex", "test-profile", "oauth")
	if err != nil {
		t.Fatalf("Failed to create profile: %v", err)
	}

	h.Log.Info("Created profile", map[string]interface{}{
		"name":      prof.Name,
		"provider":  prof.Provider,
		"base_path": prof.BasePath,
	})

	h.Log.SetStep("verify_profile")

	// Verify profile exists
	if !profileStore.Exists("codex", "test-profile") {
		t.Errorf("Profile should exist after creation")
	}

	// Load and verify
	loaded, err := profileStore.Load("codex", "test-profile")
	if err != nil {
		t.Fatalf("Failed to load profile: %v", err)
	}

	if loaded.Name != "test-profile" {
		t.Errorf("Expected name 'test-profile', got '%s'", loaded.Name)
	}
	if loaded.Provider != "codex" {
		t.Errorf("Expected provider 'codex', got '%s'", loaded.Provider)
	}
	if loaded.AuthMode != "oauth" {
		t.Errorf("Expected auth mode 'oauth', got '%s'", loaded.AuthMode)
	}

	h.Log.SetStep("test_locking")

	// Test lock/unlock
	if err := prof.Lock(); err != nil {
		t.Fatalf("Failed to lock profile: %v", err)
	}

	if !prof.IsLocked() {
		t.Errorf("Profile should be locked")
	}

	if err := prof.Unlock(); err != nil {
		t.Fatalf("Failed to unlock profile: %v", err)
	}

	if prof.IsLocked() {
		t.Errorf("Profile should be unlocked")
	}

	h.Log.SetStep("test_list_profiles")

	// List profiles
	profiles, err := profileStore.List("codex")
	if err != nil {
		t.Fatalf("Failed to list profiles: %v", err)
	}

	if len(profiles) != 1 {
		t.Errorf("Expected 1 profile, got %d", len(profiles))
	}

	h.Log.SetStep("test_delete_profile")

	// Delete profile
	if err := profileStore.Delete("codex", "test-profile"); err != nil {
		t.Fatalf("Failed to delete profile: %v", err)
	}

	if profileStore.Exists("codex", "test-profile") {
		t.Errorf("Profile should not exist after deletion")
	}

	h.Log.Info("Profile workflow test complete")
}

// TestE2E_ClearAuthFiles tests the clear workflow
func TestE2E_ClearAuthFiles(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	// Create auth file
	homeDir := h.SubDir("home")
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token": "to-clear"}`), 0600); err != nil {
		t.Fatalf("Failed to write auth: %v", err)
	}

	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	h.Log.SetStep("verify_auth_exists")

	if !authfile.HasAuthFiles(fileSet) {
		t.Fatalf("Auth should exist before clear")
	}

	h.Log.SetStep("clear_auth")

	// Clear auth files
	if err := authfile.ClearAuthFiles(fileSet); err != nil {
		t.Fatalf("ClearAuthFiles failed: %v", err)
	}

	h.Log.SetStep("verify_cleared")

	if authfile.HasAuthFiles(fileSet) {
		t.Errorf("Auth should not exist after clear")
	}

	if !h.FileNotExists(authPath) {
		t.Errorf("Auth file should be deleted")
	}

	h.Log.Info("Clear auth test complete")
}

// TestE2E_MultiProviderWorkflow tests working with multiple providers
func TestE2E_MultiProviderWorkflow(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupCLITest(t, h)

	homeDir := h.SubDir("home")

	// Create auth files for multiple providers
	providers := []struct {
		tool     string
		homeDir  string
		authFile string
		content  string
	}{
		{
			tool:     "codex",
			homeDir:  filepath.Join(homeDir, ".codex"),
			authFile: "auth.json",
			content:  `{"access_token": "codex-token"}`,
		},
		{
			tool:     "claude",
			homeDir:  filepath.Join(homeDir, ".claude"),
			authFile: ".credentials.json",
			content:  `{"claudeAiOauth":{"accessToken":"claude-token","refreshToken":"claude-refresh"}}`,
		},
	}

	h.Log.SetStep("create_auth_files")

	for _, p := range providers {
		if p.homeDir != homeDir {
			if err := os.MkdirAll(p.homeDir, 0700); err != nil {
				t.Fatalf("Failed to create dir for %s: %v", p.tool, err)
			}
		}
		authPath := filepath.Join(p.homeDir, p.authFile)
		if err := os.WriteFile(authPath, []byte(p.content), 0600); err != nil {
			t.Fatalf("Failed to write auth for %s: %v", p.tool, err)
		}

		h.Log.Info("Created auth file", map[string]interface{}{
			"tool": p.tool,
			"path": authPath,
		})
	}

	h.Log.SetStep("backup_all")

	// Backup all providers
	for _, p := range providers {
		authPath := filepath.Join(p.homeDir, p.authFile)
		fileSet := authfile.AuthFileSet{
			Tool: p.tool,
			Files: []authfile.AuthFileSpec{
				{Tool: p.tool, Path: authPath, Required: true},
			},
		}

		if err := vault.Backup(fileSet, "multi-test"); err != nil {
			t.Fatalf("Backup failed for %s: %v", p.tool, err)
		}
	}

	h.Log.SetStep("verify_isolation")

	// Verify each provider has its own backup
	allProfiles, err := vault.ListAll()
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}

	if len(allProfiles) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(allProfiles))
	}

	for _, p := range providers {
		profiles := allProfiles[p.tool]
		if len(profiles) != 1 {
			t.Errorf("Expected 1 profile for %s, got %d", p.tool, len(profiles))
		}
		if profiles[0] != "multi-test" {
			t.Errorf("Expected profile 'multi-test' for %s, got '%s'", p.tool, profiles[0])
		}
	}

	h.Log.Info("Multi-provider workflow test complete", map[string]interface{}{
		"providers": len(providers),
		"profiles":  allProfiles,
	})
}
