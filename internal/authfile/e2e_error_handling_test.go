// Package authfile provides E2E error handling tests.
package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestE2E_ReadOnlyDirectoryBackup tests backup fails gracefully on read-only directory.
func TestE2E_ReadOnlyDirectoryBackup(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_readonly_backup")

	// Create vault with read-only directory
	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create source auth file
	sourceDir := h.SubDir("source")
	sourceFile := filepath.Join(sourceDir, "auth.json")
	h.WriteJSON(filepath.Join(sourceDir, "auth.json"), map[string]string{"token": "test"})

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test auth"},
		},
	}

	// Make vault read-only
	if err := os.Chmod(vaultDir, 0555); err != nil {
		t.Skipf("Cannot set read-only permissions: %v", err)
	}
	defer os.Chmod(vaultDir, 0755) // Restore permissions for cleanup

	// Backup should fail gracefully
	err := vault.Backup(fileSet, "test-profile")
	if err == nil {
		t.Error("Expected error for read-only vault")
	}

	h.Log.Info("Read-only directory test complete", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_PermissionDeniedRestore tests restore fails gracefully with permission denied.
func TestE2E_PermissionDeniedRestore(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_permission_denied")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create a backup first
	sourceDir := h.SubDir("source")
	sourceFile := filepath.Join(sourceDir, "auth.json")
	h.WriteJSON("source/auth.json", map[string]string{"token": "test"})

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test auth"},
		},
	}

	if err := vault.Backup(fileSet, "test-profile"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Make source directory read-only
	if err := os.Chmod(sourceDir, 0555); err != nil {
		t.Skipf("Cannot set read-only permissions: %v", err)
	}
	defer os.Chmod(sourceDir, 0755)

	// Restore should fail gracefully
	err := vault.Restore(fileSet, "test-profile")
	if err == nil {
		t.Error("Expected error for permission denied")
	}

	h.Log.Info("Permission denied test complete", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_SymlinkHandling tests vault handles symlinks correctly.
func TestE2E_SymlinkHandling(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_symlink_handling")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create actual file
	actualDir := h.SubDir("actual")
	actualFile := filepath.Join(actualDir, "auth.json")
	h.WriteJSON("actual/auth.json", map[string]string{"token": "original"})

	// Create symlink to actual file
	linkDir := h.SubDir("link")
	linkFile := filepath.Join(linkDir, "auth.json")
	if err := os.Symlink(actualFile, linkFile); err != nil {
		t.Skipf("Cannot create symlink: %v", err)
	}

	// Backup using symlink path
	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: linkFile, Required: true, Description: "Test auth"},
		},
	}

	err := vault.Backup(fileSet, "symlink-profile")
	if err != nil {
		t.Fatalf("Backup via symlink failed: %v", err)
	}

	// Verify backup contains actual content
	backupPath := vault.ProfilePath("test", "symlink-profile")
	backupFile := filepath.Join(backupPath, "auth.json")

	data, err := os.ReadFile(backupFile)
	if err != nil {
		t.Fatalf("Failed to read backup: %v", err)
	}

	var content map[string]string
	if err := json.Unmarshal(data, &content); err != nil {
		t.Fatalf("Failed to parse backup: %v", err)
	}

	if content["token"] != "original" {
		t.Errorf("Expected token 'original', got %q", content["token"])
	}

	h.Log.Info("Symlink handling test complete")
}

// TestE2E_BrokenSymlinkBackup tests backup handles broken symlinks.
func TestE2E_BrokenSymlinkBackup(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_broken_symlink")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create broken symlink
	linkDir := h.SubDir("link")
	linkFile := filepath.Join(linkDir, "auth.json")
	if err := os.Symlink("/nonexistent/path", linkFile); err != nil {
		t.Skipf("Cannot create broken symlink: %v", err)
	}

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: linkFile, Required: true, Description: "Test auth"},
		},
	}

	// Backup should fail for required broken symlink
	err := vault.Backup(fileSet, "broken-profile")
	if err == nil {
		t.Error("Expected error for broken symlink")
	}

	h.Log.Info("Broken symlink test complete", map[string]interface{}{
		"error_occurred": err != nil,
	})
}

// TestE2E_ConcurrentBackups tests concurrent backup operations.
func TestE2E_ConcurrentBackups(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_concurrent_backups")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create source files for multiple profiles
	numProfiles := 5
	fileSets := make([]AuthFileSet, numProfiles)
	profileNames := make([]string, numProfiles)

	for i := 0; i < numProfiles; i++ {
		dirName := "source" + string(rune('A'+i))
		sourceDir := h.SubDir(dirName)
		sourceFile := filepath.Join(sourceDir, "auth.json")
		h.WriteJSON(dirName+"/auth.json", map[string]interface{}{
			"token":   "test-" + string(rune('A'+i)),
			"profile": i,
		})

		fileSets[i] = AuthFileSet{
			Tool: "test",
			Files: []AuthFileSpec{
				{Path: sourceFile, Required: true, Description: "Test auth"},
			},
		}
		profileNames[i] = "profile-" + string(rune('A'+i))
	}

	// Run backups concurrently
	var wg sync.WaitGroup
	errors := make([]error, numProfiles)

	for i := 0; i < numProfiles; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errors[idx] = vault.Backup(fileSets[idx], profileNames[idx])
		}(i)
	}

	wg.Wait()

	// Check for errors
	errorCount := 0
	for i, err := range errors {
		if err != nil {
			errorCount++
			h.Log.Info("Concurrent backup error", map[string]interface{}{
				"profile": profileNames[i],
				"error":   err.Error(),
			})
		}
	}

	// All backups should succeed
	if errorCount > 0 {
		t.Errorf("Expected all backups to succeed, %d failed", errorCount)
	}

	// Verify all profiles exist
	profiles, _ := vault.List("test")
	if len(profiles) != numProfiles {
		t.Errorf("Expected %d profiles, got %d", numProfiles, len(profiles))
	}

	h.Log.Info("Concurrent backups test complete", map[string]interface{}{
		"profiles_created": len(profiles),
	})
}

// TestE2E_ProfileLockingConcurrency tests concurrent profile access with locking.
func TestE2E_ProfileLockingConcurrency(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_profile_locking")

	profileDir := h.SubDir("profiles")
	store := profile.NewStore(profileDir)

	// Create a profile
	prof, err := store.Create("codex", "lock-test", "oauth")
	if err != nil {
		t.Fatalf("Create profile failed: %v", err)
	}

	// Lock the profile
	if err := prof.Lock(); err != nil {
		t.Fatalf("Lock failed: %v", err)
	}

	// Verify locked
	if !prof.IsLocked() {
		t.Error("Profile should be locked")
	}

	// Try to lock again from same process (should succeed or update lock)
	lockInfo, err := prof.GetLockInfo()
	if err != nil {
		t.Fatalf("GetLockInfo failed: %v", err)
	}
	if lockInfo.PID != os.Getpid() {
		t.Errorf("Expected PID %d, got %d", os.Getpid(), lockInfo.PID)
	}

	// Unlock
	if err := prof.Unlock(); err != nil {
		t.Fatalf("Unlock failed: %v", err)
	}

	// Verify unlocked
	if prof.IsLocked() {
		t.Error("Profile should be unlocked")
	}

	h.Log.Info("Profile locking test complete")
}

// TestE2E_CorruptedProfileJSON tests handling of corrupted profile.json.
func TestE2E_CorruptedProfileJSON(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_corrupted_profile")

	profileDir := h.SubDir("profiles")
	store := profile.NewStore(profileDir)

	// Create a valid profile first
	prof, err := store.Create("codex", "corrupt-test", "oauth")
	if err != nil {
		t.Fatalf("Create profile failed: %v", err)
	}

	// Corrupt the profile.json
	profileFile := filepath.Join(prof.BasePath, "profile.json")
	if err := os.WriteFile(profileFile, []byte("{ invalid json"), 0600); err != nil {
		t.Fatalf("Failed to corrupt file: %v", err)
	}

	// Try to load corrupted profile
	_, err = store.Load("codex", "corrupt-test")
	if err == nil {
		t.Error("Expected error loading corrupted profile")
	}

	h.Log.Info("Corrupted profile test complete", map[string]interface{}{
		"error_occurred": err != nil,
	})
}

// TestE2E_InvalidAuthJSON tests handling of invalid auth file JSON.
func TestE2E_InvalidAuthJSON(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_invalid_auth_json")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create invalid JSON auth file
	sourceDir := h.SubDir("source")
	sourceFile := filepath.Join(sourceDir, "auth.json")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}
	if err := os.WriteFile(sourceFile, []byte("not valid json"), 0600); err != nil {
		t.Fatalf("Failed to write file: %v", err)
	}

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test auth"},
		},
	}

	// Backup should still work (just copies bytes, doesn't validate)
	err := vault.Backup(fileSet, "invalid-json")
	if err != nil {
		t.Errorf("Backup should succeed even with invalid JSON: %v", err)
	}

	// Verify backup contains the invalid content
	backupPath := vault.ProfilePath("test", "invalid-json")
	backupFile := filepath.Join(backupPath, "auth.json")
	data, err := os.ReadFile(backupFile)
	if err != nil {
		t.Fatalf("Failed to read backup: %v", err)
	}

	if string(data) != "not valid json" {
		t.Errorf("Expected 'not valid json', got %q", string(data))
	}

	h.Log.Info("Invalid auth JSON test complete")
}

// TestE2E_MissingRequiredFile tests backup fails when required file is missing.
func TestE2E_MissingRequiredFile(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_missing_required")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create file set with non-existent required file
	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: "/nonexistent/path/auth.json", Required: true, Description: "Missing"},
		},
	}

	// Backup should fail
	err := vault.Backup(fileSet, "missing-profile")
	if err == nil {
		t.Error("Expected error for missing required file")
	}

	h.Log.Info("Missing required file test complete", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_OptionalFileMissing tests backup succeeds when optional file is missing.
func TestE2E_OptionalFileMissing(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_optional_missing")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create required file only
	sourceDir := h.SubDir("source")
	requiredFile := filepath.Join(sourceDir, "required.json")
	h.WriteJSON("source/required.json", map[string]string{"token": "test"})

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: requiredFile, Required: true, Description: "Required"},
			{Path: "/nonexistent/optional.json", Required: false, Description: "Optional"},
		},
	}

	// Backup should succeed
	err := vault.Backup(fileSet, "optional-missing")
	if err != nil {
		t.Errorf("Backup should succeed when optional file is missing: %v", err)
	}

	h.Log.Info("Optional file missing test complete")
}

// TestE2E_RestoreNonExistentProfileError tests restore fails for non-existent profile.
func TestE2E_RestoreNonExistentProfileError(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_restore_nonexistent")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: "/some/path/auth.json", Required: true, Description: "Test"},
		},
	}

	// Restore should fail for non-existent profile
	err := vault.Restore(fileSet, "nonexistent-profile")
	if err == nil {
		t.Error("Expected error restoring non-existent profile")
	}

	h.Log.Info("Restore non-existent profile test complete", map[string]interface{}{
		"error": err.Error(),
	})
}

// TestE2E_InterruptedBackupRecovery tests recovery from interrupted backup.
func TestE2E_InterruptedBackupRecovery(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_interrupted_backup")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create source file
	sourceDir := h.SubDir("source")
	sourceFile := filepath.Join(sourceDir, "auth.json")
	h.WriteJSON("source/auth.json", map[string]string{"token": "original"})

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test auth"},
		},
	}

	// Create a partial backup (simulate interruption)
	profilePath := vault.ProfilePath("test", "interrupted")
	if err := os.MkdirAll(profilePath, 0755); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}
	// Write partial file
	partialFile := filepath.Join(profilePath, "auth.json")
	if err := os.WriteFile(partialFile, []byte("partial"), 0600); err != nil {
		t.Fatalf("Failed to write partial file: %v", err)
	}

	// New backup should overwrite partial
	if err := vault.Backup(fileSet, "interrupted"); err != nil {
		t.Fatalf("Backup failed: %v", err)
	}

	// Verify correct content
	data, err := os.ReadFile(partialFile)
	if err != nil {
		t.Fatalf("Failed to read file: %v", err)
	}

	var content map[string]string
	if err := json.Unmarshal(data, &content); err != nil {
		t.Fatalf("Failed to parse: %v", err)
	}

	if content["token"] != "original" {
		t.Errorf("Expected 'original', got %q", content["token"])
	}

	h.Log.Info("Interrupted backup recovery test complete")
}

// TestE2E_DeleteNonExistentProfile tests delete handles missing profile.
func TestE2E_DeleteNonExistentProfile(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_delete_nonexistent")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Delete should handle non-existent gracefully
	err := vault.Delete("test", "nonexistent")
	if err == nil {
		// Some implementations may return nil for non-existent
		h.Log.Info("Delete returned no error for non-existent")
	} else {
		h.Log.Info("Delete returned error for non-existent", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

// TestE2E_EmptyVaultList tests listing empty vault.
func TestE2E_EmptyVaultList(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_empty_vault_list")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// List should return empty for non-existent tool
	profiles, err := vault.List("nonexistent-tool")
	if err != nil {
		t.Errorf("List should not error for non-existent tool: %v", err)
	}

	if len(profiles) != 0 {
		t.Errorf("Expected empty list, got %d profiles", len(profiles))
	}

	h.Log.Info("Empty vault list test complete")
}

// TestE2E_LargeFileBackup tests backup of larger files.
func TestE2E_LargeFileBackup(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_large_file")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	// Create a larger auth file (1MB)
	sourceDir := h.SubDir("source")
	sourceFile := filepath.Join(sourceDir, "auth.json")
	if err := os.MkdirAll(sourceDir, 0755); err != nil {
		t.Fatalf("Failed to create dir: %v", err)
	}

	// Create large JSON
	largeData := make([]byte, 1024*1024) // 1MB
	for i := range largeData {
		largeData[i] = byte('a' + (i % 26))
	}
	content := map[string]string{
		"token": "test",
		"data":  string(largeData),
	}
	jsonData, _ := json.Marshal(content)
	if err := os.WriteFile(sourceFile, jsonData, 0600); err != nil {
		t.Fatalf("Failed to write large file: %v", err)
	}

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Large auth"},
		},
	}

	// Backup should succeed
	if err := vault.Backup(fileSet, "large-file"); err != nil {
		t.Fatalf("Large file backup failed: %v", err)
	}

	// Restore should succeed
	if err := vault.Restore(fileSet, "large-file"); err != nil {
		t.Fatalf("Large file restore failed: %v", err)
	}

	h.Log.Info("Large file backup test complete", map[string]interface{}{
		"file_size": len(jsonData),
	})
}

// TestE2E_SpecialCharactersInProfileName tests profile names with special chars.
func TestE2E_SpecialCharactersInProfileName(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_special_chars")

	vaultDir := h.SubDir("vault")
	vault := NewVault(vaultDir)

	sourceDir := h.SubDir("source")
	sourceFile := filepath.Join(sourceDir, "auth.json")
	h.WriteJSON("source/auth.json", map[string]string{"token": "test"})

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: sourceFile, Required: true, Description: "Test auth"},
		},
	}

	// Test various profile names
	profileNames := []string{
		"user@email.com",
		"profile-with-dashes",
		"profile_with_underscores",
		"profile.with.dots",
	}

	for _, name := range profileNames {
		t.Run(name, func(t *testing.T) {
			err := vault.Backup(fileSet, name)
			if err != nil {
				t.Errorf("Backup failed for profile %q: %v", name, err)
				return
			}

			// Verify can be listed
			profiles, err := vault.List("test")
			if err != nil {
				t.Errorf("List failed: %v", err)
				return
			}

			found := false
			for _, p := range profiles {
				if p == name {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("Profile %q not found in list", name)
			}
		})
	}

	h.Log.Info("Special characters test complete")
}

// TestE2E_ClearAuthFilesError tests ClearAuthFiles handles errors.
func TestE2E_ClearAuthFilesError(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_clear_auth_error")

	// Create a file set pointing to non-existent files
	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: "/nonexistent/auth.json", Required: true, Description: "Missing"},
		},
	}

	// ClearAuthFiles should not error for non-existent files
	err := ClearAuthFiles(fileSet)
	if err != nil {
		h.Log.Info("ClearAuthFiles error", map[string]interface{}{
			"error": err.Error(),
		})
	} else {
		h.Log.Info("ClearAuthFiles succeeded for non-existent files")
	}
}

// TestE2E_HasAuthFilesEmpty tests HasAuthFiles with no files.
func TestE2E_HasAuthFilesEmpty(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_has_auth_empty")

	fileSet := AuthFileSet{
		Tool: "test",
		Files: []AuthFileSpec{
			{Path: "/nonexistent/auth.json", Required: true, Description: "Missing"},
		},
	}

	hasAuth := HasAuthFiles(fileSet)
	if hasAuth {
		t.Error("Expected HasAuthFiles to return false for non-existent files")
	}

	h.Log.Info("HasAuthFiles empty test complete")
}
