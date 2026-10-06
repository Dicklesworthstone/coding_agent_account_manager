package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// =============================================================================
// E2E Tests: Profile Backup and Restore Workflow
// =============================================================================

// TestE2E_FullBackupWorkflow tests the complete backup workflow:
// - Create temp HOME directory
// - Place mock auth files for a provider
// - Run backup command
// - Verify profile directory structure created
// - Verify auth files copied correctly
func TestE2E_FullBackupWorkflow(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	// Create mock home directory structure
	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	// Create Codex auth file
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authContent := map[string]interface{}{
		"access_token":  "test-access-token-12345",
		"refresh_token": "test-refresh-token-67890",
		"token_type":    "Bearer",
		"expires_in":    3600,
	}
	authJSON, _ := json.MarshalIndent(authContent, "", "  ")
	authPath := filepath.Join(codexHome, "auth.json")
	if err := os.WriteFile(authPath, authJSON, 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	h.Log.Info("Created mock auth file", map[string]interface{}{
		"path": authPath,
	})

	// Create custom AuthFileSet pointing to our temp directory
	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{
				Tool:        "codex",
				Path:        authPath,
				Description: "Test Codex auth",
				Required:    true,
			},
		},
	}

	h.Log.SetStep("backup")
	startTime := time.Now()

	// Create vault and perform backup
	vault := NewVault(vaultDir)
	err := vault.Backup(fileSet, "work-account")

	backupDuration := time.Since(startTime)
	h.Log.Info("Backup completed", map[string]interface{}{
		"duration_ms": backupDuration.Milliseconds(),
	})

	if err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	h.Log.SetStep("verify")

	// Verify profile directory was created
	profileDir := vault.ProfilePath("codex", "work-account")
	if !h.DirExists(profileDir) {
		t.Fatalf("Profile directory not created")
	}

	// Verify auth file was copied
	backupPath := vault.BackupPath("codex", "work-account", "auth.json")
	if !h.FileExists(backupPath) {
		t.Fatalf("Auth file not backed up")
	}

	// Verify content matches
	if !h.JSONContains(backupPath, "access_token", "test-access-token-12345") {
		t.Errorf("Backed up auth file has incorrect content")
	}

	// Verify metadata file was created
	metaPath := filepath.Join(profileDir, "meta.json")
	if !h.FileExists(metaPath) {
		t.Fatalf("Metadata file not created")
	}

	if !h.JSONContains(metaPath, "tool", "codex") {
		t.Errorf("Metadata has incorrect tool")
	}
	if !h.JSONContains(metaPath, "profile", "work-account") {
		t.Errorf("Metadata has incorrect profile")
	}

	// Verify file permissions are secure
	if !h.FilePermissions(backupPath, 0600) {
		t.Errorf("Auth file should have 0600 permissions")
	}

	h.Log.Info("Backup verification complete", map[string]interface{}{
		"profile_dir": profileDir,
		"backup_path": backupPath,
	})
}

// TestE2E_FullRestoreWorkflow tests the complete restore workflow:
// - Start with empty auth state
// - Run restore command
// - Verify auth files restored to correct locations
func TestE2E_FullRestoreWorkflow(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	// Create directories
	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	// Create pre-existing backup in vault
	codexProfileDir := filepath.Join(vaultDir, "codex", "personal-account")
	if err := os.MkdirAll(codexProfileDir, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Write backed up auth file
	backupContent := map[string]interface{}{
		"access_token":  "restored-access-token",
		"refresh_token": "restored-refresh-token",
		"token_type":    "Bearer",
		"expires_in":    7200,
	}
	backupJSON, _ := json.MarshalIndent(backupContent, "", "  ")
	backupPath := filepath.Join(codexProfileDir, "auth.json")
	if err := os.WriteFile(backupPath, backupJSON, 0600); err != nil {
		t.Fatalf("Failed to write backup: %v", err)
	}

	h.Log.Info("Created pre-existing backup", map[string]interface{}{
		"path": backupPath,
	})

	// Define where auth file should be restored
	codexHome := filepath.Join(homeDir, ".codex")
	restorePath := filepath.Join(codexHome, "auth.json")

	// Verify auth file does NOT exist before restore
	if _, err := os.Stat(restorePath); err == nil {
		t.Fatalf("Auth file already exists before restore")
	}

	// Create custom AuthFileSet pointing to our temp directory
	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{
				Tool:        "codex",
				Path:        restorePath,
				Description: "Test Codex auth",
				Required:    true,
			},
		},
	}

	h.Log.SetStep("restore")
	startTime := time.Now()

	// Perform restore
	vault := NewVault(vaultDir)
	err := vault.Restore(fileSet, "personal-account")

	restoreDuration := time.Since(startTime)
	h.Log.Info("Restore completed", map[string]interface{}{
		"duration_ms": restoreDuration.Milliseconds(),
	})

	if err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	h.Log.SetStep("verify")

	// Verify auth file was restored
	if !h.FileExists(restorePath) {
		t.Fatalf("Auth file not restored")
	}

	// Verify content matches backup
	if !h.JSONContains(restorePath, "access_token", "restored-access-token") {
		t.Errorf("Restored auth file has incorrect access_token")
	}
	if !h.JSONContains(restorePath, "refresh_token", "restored-refresh-token") {
		t.Errorf("Restored auth file has incorrect refresh_token")
	}

	// Verify file permissions are secure
	if !h.FilePermissions(restorePath, 0600) {
		t.Errorf("Restored file should have 0600 permissions")
	}

	// Verify parent directory was created with correct permissions
	if !h.DirExists(codexHome) {
		t.Errorf("Parent directory should exist")
	}

	h.Log.Info("Restore verification complete", map[string]interface{}{
		"restore_path": restorePath,
	})
}

// TestE2E_BackupOverwriteProtection tests that backup doesn't overwrite without explicit flag
func TestE2E_BackupOverwriteProtection(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	// Create initial auth file
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	originalContent := `{"access_token": "original-token", "token_type": "Bearer"}`
	if err := os.WriteFile(authPath, []byte(originalContent), 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	vault := NewVault(vaultDir)

	h.Log.SetStep("first_backup")

	// First backup
	if err := vault.Backup(fileSet, "test-profile"); err != nil {
		t.Fatalf("First backup failed: %v", err)
	}

	// Verify original backup exists
	backupPath := vault.BackupPath("codex", "test-profile", "auth.json")
	if !h.FileContains(backupPath, "original-token") {
		t.Errorf("First backup should contain original token")
	}

	h.Log.SetStep("modify_auth")

	// Modify the auth file
	newContent := `{"access_token": "new-token", "token_type": "Bearer"}`
	if err := os.WriteFile(authPath, []byte(newContent), 0600); err != nil {
		t.Fatalf("Failed to update auth file: %v", err)
	}

	h.Log.SetStep("second_backup")

	// Second backup to same profile (should overwrite)
	if err := vault.Backup(fileSet, "test-profile"); err != nil {
		t.Fatalf("Second backup failed: %v", err)
	}

	// Verify backup now has new content
	if !h.FileContains(backupPath, "new-token") {
		t.Errorf("Backup should have been updated with new token")
	}

	h.Log.Info("Overwrite test complete")
}

// TestE2E_CrossProviderWorkflows tests multiple providers don't interfere
func TestE2E_CrossProviderWorkflows(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")
	xdgConfig := filepath.Join(homeDir, ".config")

	// Create Codex auth
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}
	codexAuthPath := filepath.Join(codexHome, "auth.json")
	codexContent := `{"access_token": "codex-token-123", "provider": "codex"}`
	if err := os.WriteFile(codexAuthPath, []byte(codexContent), 0600); err != nil {
		t.Fatalf("Failed to write codex auth: %v", err)
	}

	// Create Claude auth
	claudeAuthPath := filepath.Join(homeDir, ".claude.json")
	claudeContent := `{"sessionKey": "claude-session-456", "provider": "claude"}`
	if err := os.WriteFile(claudeAuthPath, []byte(claudeContent), 0600); err != nil {
		t.Fatalf("Failed to write claude auth: %v", err)
	}

	// Create Claude Code config auth (optional)
	claudeConfigDir := filepath.Join(xdgConfig, "claude-code")
	if err := os.MkdirAll(claudeConfigDir, 0700); err != nil {
		t.Fatalf("Failed to create claude config dir: %v", err)
	}
	claudeConfigAuthPath := filepath.Join(claudeConfigDir, "auth.json")
	claudeConfigContent := `{"access_token": "claude-access-789", "refresh_token": "claude-refresh-789"}`
	if err := os.WriteFile(claudeConfigAuthPath, []byte(claudeConfigContent), 0600); err != nil {
		t.Fatalf("Failed to write claude config auth: %v", err)
	}

	h.Log.Info("Created auth files for multiple providers", map[string]interface{}{
		"codex_path":         codexAuthPath,
		"claude_path":        claudeAuthPath,
		"claude_config_path": claudeConfigAuthPath,
	})

	// Create file sets for each provider
	codexFileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: codexAuthPath, Required: true},
		},
	}

	claudeFileSet := AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: claudeAuthPath, Required: true},
			{Tool: "claude", Path: claudeConfigAuthPath, Required: false},
		},
	}

	vault := NewVault(vaultDir)

	h.Log.SetStep("backup_codex")

	// Backup Codex profile
	if err := vault.Backup(codexFileSet, "work"); err != nil {
		t.Fatalf("Codex backup failed: %v", err)
	}

	h.Log.SetStep("backup_claude")

	// Backup Claude profile
	if err := vault.Backup(claudeFileSet, "personal"); err != nil {
		t.Fatalf("Claude backup failed: %v", err)
	}

	h.Log.SetStep("verify_isolation")

	// Verify Codex backup exists and has correct content
	codexBackup := vault.BackupPath("codex", "work", "auth.json")
	if !h.FileExists(codexBackup) {
		t.Errorf("Codex backup not found")
	}
	if !h.FileContains(codexBackup, "codex-token-123") {
		t.Errorf("Codex backup has wrong content")
	}

	// Verify Claude backup exists and has correct content
	claudeBackup := vault.BackupPath("claude", "personal", ".claude.json")
	if !h.FileExists(claudeBackup) {
		t.Errorf("Claude backup not found")
	}
	if !h.FileContains(claudeBackup, "claude-session-456") {
		t.Errorf("Claude backup has wrong content")
	}

	// Verify optional Claude config was also backed up
	claudeConfigBackup := vault.BackupPath("claude", "personal", "auth.json")
	if !h.FileExists(claudeConfigBackup) {
		t.Errorf("Claude config backup not found")
	}

	// Verify profiles are isolated in vault structure
	codexProfileDir := vault.ProfilePath("codex", "work")
	claudeProfileDir := vault.ProfilePath("claude", "personal")

	if !h.DirExists(codexProfileDir) {
		t.Errorf("Codex profile directory not found")
	}
	if !h.DirExists(claudeProfileDir) {
		t.Errorf("Claude profile directory not found")
	}

	// Verify they are different directories
	if codexProfileDir == claudeProfileDir {
		t.Errorf("Provider profiles should be in different directories")
	}

	h.Log.SetStep("test_active_profile")

	// Test ActiveProfile detection for Codex
	activeCodex, err := vault.ActiveProfile(codexFileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}
	if activeCodex != "work" {
		t.Errorf("Expected active profile 'work', got '%s'", activeCodex)
	}

	// Test ActiveProfile detection for Claude
	activeClaude, err := vault.ActiveProfile(claudeFileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}
	if activeClaude != "personal" {
		t.Errorf("Expected active profile 'personal', got '%s'", activeClaude)
	}

	h.Log.SetStep("test_list")

	// Test List returns correct profiles per tool
	codexProfiles, err := vault.List("codex")
	if err != nil {
		t.Fatalf("List codex failed: %v", err)
	}
	if len(codexProfiles) != 1 || codexProfiles[0] != "work" {
		t.Errorf("Expected [work], got %v", codexProfiles)
	}

	claudeProfiles, err := vault.List("claude")
	if err != nil {
		t.Fatalf("List claude failed: %v", err)
	}
	if len(claudeProfiles) != 1 || claudeProfiles[0] != "personal" {
		t.Errorf("Expected [personal], got %v", claudeProfiles)
	}

	// Test ListAll returns both
	allProfiles, err := vault.ListAll()
	if err != nil {
		t.Fatalf("ListAll failed: %v", err)
	}
	if len(allProfiles) != 2 {
		t.Errorf("Expected 2 tools, got %d", len(allProfiles))
	}

	h.Log.Info("Cross-provider isolation verified", map[string]interface{}{
		"codex_profiles":  codexProfiles,
		"claude_profiles": claudeProfiles,
	})
}

// TestE2E_BackupRestoreRoundTrip tests complete backup-clear-restore cycle
func TestE2E_BackupRestoreRoundTrip(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	// Create auth file
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	originalContent := map[string]interface{}{
		"access_token":  "round-trip-token",
		"refresh_token": "round-trip-refresh",
		"token_type":    "Bearer",
		"expires_in":    3600,
	}
	originalJSON, _ := json.MarshalIndent(originalContent, "", "  ")
	if err := os.WriteFile(authPath, originalJSON, 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	vault := NewVault(vaultDir)

	h.Log.SetStep("backup")

	// Step 1: Backup
	if err := vault.Backup(fileSet, "roundtrip"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	h.Log.SetStep("verify_backup")

	// Verify backup exists
	backupPath := vault.BackupPath("codex", "roundtrip", "auth.json")
	if !h.FileExists(backupPath) {
		t.Fatalf("Backup file not created")
	}

	h.Log.SetStep("clear")

	// Step 2: Clear auth files (simulate logout)
	if err := ClearAuthFiles(fileSet); err != nil {
		t.Fatalf("ClearAuthFiles failed: %v", err)
	}

	// Verify auth file is gone
	if h.FileNotExists(authPath) {
		h.Log.Info("Auth file cleared successfully")
	}

	// Verify HasAuthFiles returns false
	if HasAuthFiles(fileSet) {
		t.Errorf("HasAuthFiles should return false after clear")
	}

	h.Log.SetStep("restore")

	// Step 3: Restore
	if err := vault.Restore(fileSet, "roundtrip"); err != nil {
		t.Fatalf("Restore failed: %v", err)
	}

	h.Log.SetStep("verify_restore")

	// Verify auth file was restored
	if !h.FileExists(authPath) {
		t.Fatalf("Auth file not restored")
	}

	// Verify content matches original
	if !h.JSONContains(authPath, "access_token", "round-trip-token") {
		t.Errorf("Restored content doesn't match original")
	}

	// Verify HasAuthFiles returns true
	if !HasAuthFiles(fileSet) {
		t.Errorf("HasAuthFiles should return true after restore")
	}

	// Verify ActiveProfile detects it
	active, err := vault.ActiveProfile(fileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}
	if active != "roundtrip" {
		t.Errorf("Expected active profile 'roundtrip', got '%s'", active)
	}

	h.Log.Info("Round-trip test complete")
}

// TestE2E_MultipleProfilesSameProvider tests managing multiple profiles for one provider
func TestE2E_MultipleProfilesSameProvider(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	authPath := filepath.Join(codexHome, "auth.json")
	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	vault := NewVault(vaultDir)

	profiles := []struct {
		name  string
		token string
	}{
		{"work", "work-token-111"},
		{"personal", "personal-token-222"},
		{"client-a", "client-a-token-333"},
	}

	h.Log.SetStep("create_profiles")

	// Create multiple profiles
	for _, p := range profiles {
		content := `{"access_token": "` + p.token + `", "token_type": "Bearer"}`
		if err := os.WriteFile(authPath, []byte(content), 0600); err != nil {
			t.Fatalf("Failed to write auth for %s: %v", p.name, err)
		}

		if err := vault.Backup(fileSet, p.name); err != nil {
			t.Fatalf("Backup failed for %s: %v", p.name, err)
		}

		h.Log.Info("Created profile", map[string]interface{}{
			"name": p.name,
		})
	}

	h.Log.SetStep("verify_list")

	// Verify all profiles exist
	listed, err := vault.List("codex")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(listed) != 3 {
		t.Errorf("Expected 3 profiles, got %d", len(listed))
	}

	h.Log.SetStep("test_switching")

	// Test switching between profiles
	for _, p := range profiles {
		// Restore this profile
		if err := vault.Restore(fileSet, p.name); err != nil {
			t.Fatalf("Restore failed for %s: %v", p.name, err)
		}

		// Verify content
		if !h.FileContains(authPath, p.token) {
			t.Errorf("After restoring %s, expected token %s not found", p.name, p.token)
		}

		// Verify ActiveProfile detects correct one
		active, err := vault.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile failed: %v", err)
		}
		if active != p.name {
			t.Errorf("Expected active '%s', got '%s'", p.name, active)
		}

		h.Log.Info("Switched to profile", map[string]interface{}{
			"name":   p.name,
			"active": active,
		})
	}

	h.Log.SetStep("test_delete")

	// Test deleting a profile
	if err := vault.Delete("codex", "client-a"); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify it's gone
	listed, _ = vault.List("codex")
	if len(listed) != 2 {
		t.Errorf("Expected 2 profiles after delete, got %d", len(listed))
	}

	// Verify other profiles still work
	if err := vault.Restore(fileSet, "work"); err != nil {
		t.Fatalf("Restore 'work' after delete failed: %v", err)
	}

	h.Log.Info("Multiple profiles test complete")
}

// TestE2E_RestoreNonExistentProfile tests error handling for missing profiles
func TestE2E_RestoreNonExistentProfile(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	codexHome := filepath.Join(homeDir, ".codex")
	authPath := filepath.Join(codexHome, "auth.json")

	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	vault := NewVault(vaultDir)

	h.Log.SetStep("restore_missing")

	// Try to restore non-existent profile
	err := vault.Restore(fileSet, "does-not-exist")
	if err == nil {
		t.Errorf("Expected error when restoring non-existent profile")
	}

	h.Log.Info("Got expected error", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_BackupMissingRequiredFile tests error handling for missing required auth files
func TestE2E_BackupMissingRequiredFile(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	// Path to non-existent auth file
	authPath := filepath.Join(homeDir, ".codex", "auth.json")

	fileSet := AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{Tool: "codex", Path: authPath, Required: true},
		},
	}

	vault := NewVault(vaultDir)

	h.Log.SetStep("backup_missing")

	// Try to backup when required file doesn't exist
	err := vault.Backup(fileSet, "test")
	if err == nil {
		t.Errorf("Expected error when backing up missing required file")
	}

	h.Log.Info("Got expected error", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_OptionalFilesHandling tests proper handling of optional auth files
func TestE2E_OptionalFilesHandling(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	// Create required file only
	claudeAuth := filepath.Join(homeDir, ".claude.json")
	if err := os.WriteFile(claudeAuth, []byte(`{"sessionKey": "test"}`), 0600); err != nil {
		t.Fatalf("Failed to write claude auth: %v", err)
	}

	// Optional file path (doesn't exist)
	optionalAuth := filepath.Join(homeDir, ".config", "claude-code", "auth.json")

	fileSet := AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: claudeAuth, Required: true},
			{Tool: "claude", Path: optionalAuth, Required: false}, // Optional, doesn't exist
		},
	}

	vault := NewVault(vaultDir)

	h.Log.SetStep("backup")

	// Backup should succeed even though optional file doesn't exist
	err := vault.Backup(fileSet, "test")
	if err != nil {
		t.Fatalf("Backup should succeed with missing optional file: %v", err)
	}

	h.Log.SetStep("verify")

	// Verify required file was backed up
	requiredBackup := vault.BackupPath("claude", "test", ".claude.json")
	if !h.FileExists(requiredBackup) {
		t.Errorf("Required file not backed up")
	}

	// Verify optional file was NOT backed up (doesn't exist)
	optionalBackup := vault.BackupPath("claude", "test", "auth.json")
	if !h.FileNotExists(optionalBackup) {
		t.Errorf("Optional file should not have been backed up")
	}

	h.Log.SetStep("restore")

	// Clear and restore
	if err := ClearAuthFiles(fileSet); err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	// Restore should succeed
	err = vault.Restore(fileSet, "test")
	if err != nil {
		t.Fatalf("Restore should succeed: %v", err)
	}

	// Verify required file was restored
	if !h.FileExists(claudeAuth) {
		t.Errorf("Required file not restored")
	}

	h.Log.Info("Optional files handling verified")
}
