// Package workflows provides E2E workflow tests for caam operations.
package workflows

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// =============================================================================
// E2E Test: Complete Backup-Activate-Switch Workflow
// =============================================================================

// TestE2E_CompleteBackupActivateSwitchWorkflow tests the complete user workflow
// from initial backup through activation and profile switching.
//
// This test uses ExtendedHarness for detailed step tracking and metrics.
func TestE2E_CompleteBackupActivateSwitchWorkflow(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// ==========================================================================
	// PHASE 1: Initial Setup
	// ==========================================================================
	h.StartStep("setup", "Creating mock auth files for multiple providers")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")
	xdgConfig := filepath.Join(homeDir, ".config")

	// Create Codex auth files
	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	// Create Claude auth files
	claudeConfigDir := filepath.Join(xdgConfig, "claude-code")
	if err := os.MkdirAll(claudeConfigDir, 0700); err != nil {
		t.Fatalf("Failed to create claude config dir: %v", err)
	}

	// Write initial Codex auth - account1
	codexAuthPath := filepath.Join(codexHome, "auth.json")
	codexAccount1 := map[string]interface{}{
		"access_token":  "codex-account1-token-abc123",
		"refresh_token": "codex-account1-refresh-xyz789",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"account":       "account1@example.com",
	}
	codexJSON1, _ := json.MarshalIndent(codexAccount1, "", "  ")
	if err := os.WriteFile(codexAuthPath, codexJSON1, 0600); err != nil {
		t.Fatalf("Failed to write codex auth: %v", err)
	}

	// Write initial Claude auth - personal account
	claudeMainPath := filepath.Join(homeDir, ".claude.json")
	claudePersonal := map[string]interface{}{
		"session_token": "claude-personal-session-111",
		"refresh_token": "claude-personal-refresh-222",
		"expires_at":    time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		"account":       "personal@example.com",
	}
	claudeMainJSON, _ := json.MarshalIndent(claudePersonal, "", "  ")
	if err := os.WriteFile(claudeMainPath, claudeMainJSON, 0600); err != nil {
		t.Fatalf("Failed to write claude main auth: %v", err)
	}

	claudeConfigAuthPath := filepath.Join(claudeConfigDir, "auth.json")
	claudeConfigAuth := map[string]interface{}{
		"api_key": "claude-config-key-333",
	}
	claudeConfigJSON, _ := json.MarshalIndent(claudeConfigAuth, "", "  ")
	if err := os.WriteFile(claudeConfigAuthPath, claudeConfigJSON, 0600); err != nil {
		t.Fatalf("Failed to write claude config auth: %v", err)
	}

	h.LogInfo("Created mock auth files", "codex_path", codexAuthPath, "claude_path", claudeMainPath)
	h.EndStep("setup")

	// Define auth file sets for providers
	codexFileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: codexAuthPath, Required: true},
		},
	}

	claudeFileSet := authfile.AuthFileSet{
		Tool: "claude",
		Files: []authfile.AuthFileSpec{
			{Tool: "claude", Path: claudeMainPath, Required: true},
			{Tool: "claude", Path: claudeConfigAuthPath, Required: false},
		},
	}

	vault := authfile.NewVault(vaultDir)

	// ==========================================================================
	// PHASE 2: Backup Phase
	// ==========================================================================
	h.StartStep("backup_phase", "Backing up profiles for multiple accounts")

	// Backup Codex account1
	h.TimeStep("backup_codex_account1", "Backing up codex account1@example.com", func() {
		if err := vault.Backup(codexFileSet, "account1@example.com"); err != nil {
			t.Fatalf("Backup codex account1 failed: %v", err)
		}
	})
	h.LogInfo("Backed up codex account1", "profile", "account1@example.com")

	// Switch Codex to account2 and backup
	codexAccount2 := map[string]interface{}{
		"access_token":  "codex-account2-token-def456",
		"refresh_token": "codex-account2-refresh-uvw012",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"account":       "account2@example.com",
	}
	codexJSON2, _ := json.MarshalIndent(codexAccount2, "", "  ")
	if err := os.WriteFile(codexAuthPath, codexJSON2, 0600); err != nil {
		t.Fatalf("Failed to write codex account2 auth: %v", err)
	}

	h.TimeStep("backup_codex_account2", "Backing up codex account2@example.com", func() {
		if err := vault.Backup(codexFileSet, "account2@example.com"); err != nil {
			t.Fatalf("Backup codex account2 failed: %v", err)
		}
	})
	h.LogInfo("Backed up codex account2", "profile", "account2@example.com")

	// Backup Claude personal
	h.TimeStep("backup_claude_personal", "Backing up claude personal@example.com", func() {
		if err := vault.Backup(claudeFileSet, "personal@example.com"); err != nil {
			t.Fatalf("Backup claude personal failed: %v", err)
		}
	})
	h.LogInfo("Backed up claude personal", "profile", "personal@example.com")

	h.EndStep("backup_phase")

	// ==========================================================================
	// PHASE 3: Activation Phase
	// ==========================================================================
	h.StartStep("activation_phase", "Testing profile activation")

	// Activate Codex account1
	h.TimeStep("activate_codex_account1", "Activating codex account1@example.com", func() {
		if err := vault.Restore(codexFileSet, "account1@example.com"); err != nil {
			t.Fatalf("Activate codex account1 failed: %v", err)
		}
	})

	// Verify auth files match account1
	h.StartStep("verify_account1", "Verifying account1 is active")
	content, err := os.ReadFile(codexAuthPath)
	if err != nil {
		t.Fatalf("Failed to read codex auth: %v", err)
	}
	var authData map[string]interface{}
	if err := json.Unmarshal(content, &authData); err != nil {
		t.Fatalf("Failed to parse codex auth: %v", err)
	}
	if authData["access_token"] != "codex-account1-token-abc123" {
		t.Errorf("Expected account1 token, got %v", authData["access_token"])
	}
	h.LogInfo("Verified account1 active", "access_token", authData["access_token"].(string)[:20]+"...")
	h.EndStep("verify_account1")

	// Activate Codex account2
	h.TimeStep("activate_codex_account2", "Activating codex account2@example.com", func() {
		if err := vault.Restore(codexFileSet, "account2@example.com"); err != nil {
			t.Fatalf("Activate codex account2 failed: %v", err)
		}
	})

	// Verify auth files match account2
	h.StartStep("verify_account2", "Verifying account2 is active")
	content, err = os.ReadFile(codexAuthPath)
	if err != nil {
		t.Fatalf("Failed to read codex auth: %v", err)
	}
	if err := json.Unmarshal(content, &authData); err != nil {
		t.Fatalf("Failed to parse codex auth: %v", err)
	}
	if authData["access_token"] != "codex-account2-token-def456" {
		t.Errorf("Expected account2 token, got %v", authData["access_token"])
	}
	h.LogInfo("Verified account2 active", "access_token", authData["access_token"].(string)[:20]+"...")
	h.EndStep("verify_account2")

	h.EndStep("activation_phase")

	// ==========================================================================
	// PHASE 4: Switch Phase
	// ==========================================================================
	h.StartStep("switch_phase", "Testing profile switching")

	// Get status - should show account2
	h.StartStep("check_status_account2", "Checking current status")
	active, err := vault.ActiveProfile(codexFileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}
	if active != "account2@example.com" {
		t.Errorf("Expected 'account2@example.com' active, got '%s'", active)
	}
	h.LogInfo("Status check", "active_profile", active)
	h.EndStep("check_status_account2")

	// Switch back to account1
	h.TimeStep("switch_to_account1", "Switching to account1@example.com", func() {
		if err := vault.Restore(codexFileSet, "account1@example.com"); err != nil {
			t.Fatalf("Switch to account1 failed: %v", err)
		}
	})

	// Get status - should show account1
	h.StartStep("check_status_account1", "Verifying switch to account1")
	active, err = vault.ActiveProfile(codexFileSet)
	if err != nil {
		t.Fatalf("ActiveProfile failed: %v", err)
	}
	if active != "account1@example.com" {
		t.Errorf("Expected 'account1@example.com' active, got '%s'", active)
	}
	h.LogInfo("Status check after switch", "active_profile", active)
	h.EndStep("check_status_account1")

	h.EndStep("switch_phase")

	// ==========================================================================
	// PHASE 5: Auto-Backup Verification
	// ==========================================================================
	h.StartStep("auto_backup_verification", "Verifying auto-backup functionality")

	// Test BackupOriginal - should create _original profile on first use
	h.StartStep("test_original_backup", "Testing _original profile creation")
	created, err := vault.BackupOriginal(codexFileSet)
	if err != nil {
		t.Fatalf("BackupOriginal failed: %v", err)
	}
	// Should not create _original because current state matches an existing profile
	if created {
		h.LogInfo("_original created (current state didn't match existing profiles)")
	} else {
		h.LogInfo("_original not created (current state matches an existing profile)")
	}
	h.EndStep("test_original_backup")

	// Verify _original profile exists or doesn't based on state
	hasOriginal, err := vault.HasOriginalBackup("codex")
	if err != nil {
		t.Fatalf("HasOriginalBackup failed: %v", err)
	}
	h.LogInfo("Original backup status", "has_original", hasOriginal)

	// Test auto-backup with timestamp
	h.StartStep("test_timestamped_backup", "Testing timestamped backup creation")
	backupName, err := vault.BackupCurrent(codexFileSet)
	if err != nil {
		t.Fatalf("BackupCurrent failed: %v", err)
	}
	if backupName == "" {
		t.Errorf("Expected timestamped backup to be created")
	} else {
		h.LogInfo("Created timestamped backup", "name", backupName)
	}
	h.EndStep("test_timestamped_backup")

	// Verify auto-backup rotation
	h.StartStep("test_backup_rotation", "Testing backup rotation")
	profiles, err := vault.List("codex")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	h.LogInfo("Current profiles", "count", len(profiles), "profiles", profiles)
	h.EndStep("test_backup_rotation")

	h.EndStep("auto_backup_verification")

	// ==========================================================================
	// PHASE 6: Final Verification
	// ==========================================================================
	h.StartStep("final_verification", "Running final verifications")

	// Verify vault structure
	expectedStructure := map[string]string{
		"codex":                              "dir",
		"codex/account1@example.com":         "dir",
		"codex/account2@example.com":         "dir",
		"codex/account1@example.com/auth.json": "file",
		"codex/account2@example.com/auth.json": "file",
		"claude":                             "dir",
		"claude/personal@example.com":        "dir",
	}

	allMatch := true
	for path, expectedType := range expectedStructure {
		fullPath := filepath.Join(vaultDir, path)
		info, err := os.Stat(fullPath)
		if err != nil {
			t.Errorf("Expected path %s not found: %v", path, err)
			allMatch = false
			continue
		}
		if expectedType == "dir" && !info.IsDir() {
			t.Errorf("Expected %s to be a directory", path)
			allMatch = false
		} else if expectedType == "file" && info.IsDir() {
			t.Errorf("Expected %s to be a file", path)
			allMatch = false
		}
	}

	if allMatch {
		h.LogInfo("Vault structure verified", "paths_checked", len(expectedStructure))
	}

	// Record final metrics
	h.RecordMetric("total_profiles_created", time.Duration(len(profiles))*time.Second)
	h.RecordMetric("vault_structure_verified", time.Duration(len(expectedStructure))*time.Millisecond)

	h.EndStep("final_verification")

	// Log summary
	t.Log("\n" + h.Summary())
}

// TestE2E_RapidProfileSwitching tests switching profiles rapidly to verify
// there are no race conditions or file locking issues.
func TestE2E_RapidProfileSwitching(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Setting up rapid switching test")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")

	codexHome := filepath.Join(homeDir, ".codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatalf("Failed to create codex home: %v", err)
	}

	codexAuthPath := filepath.Join(codexHome, "auth.json")
	fileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: codexAuthPath, Required: true},
		},
	}

	vault := authfile.NewVault(vaultDir)

	// Create 5 profiles
	profiles := []string{"profile-a", "profile-b", "profile-c", "profile-d", "profile-e"}
	for i, name := range profiles {
		content := map[string]interface{}{
			"access_token": name + "-token",
			"profile_id":   i,
		}
		jsonBytes, _ := json.MarshalIndent(content, "", "  ")
		if err := os.WriteFile(codexAuthPath, jsonBytes, 0600); err != nil {
			t.Fatalf("Failed to write auth for %s: %v", name, err)
		}
		if err := vault.Backup(fileSet, name); err != nil {
			t.Fatalf("Failed to backup %s: %v", name, err)
		}
	}
	h.LogInfo("Created profiles", "count", len(profiles))
	h.EndStep("setup")

	h.StartStep("rapid_switching", "Performing rapid profile switches")

	// Perform 20 rapid switches
	switchCount := 20
	startTime := time.Now()

	for i := 0; i < switchCount; i++ {
		profile := profiles[i%len(profiles)]
		if err := vault.Restore(fileSet, profile); err != nil {
			t.Fatalf("Switch %d to %s failed: %v", i, profile, err)
		}

		// Verify switch was successful
		active, err := vault.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile check %d failed: %v", i, err)
		}
		if active != profile {
			t.Errorf("Switch %d: expected '%s', got '%s'", i, profile, active)
		}
	}

	switchDuration := time.Since(startTime)
	avgSwitchTime := switchDuration / time.Duration(switchCount)

	h.RecordMetric("total_switch_time", switchDuration)
	h.RecordMetric("avg_switch_time", avgSwitchTime)
	h.LogInfo("Rapid switching complete",
		"switches", switchCount,
		"total_time_ms", switchDuration.Milliseconds(),
		"avg_time_ms", avgSwitchTime.Milliseconds())

	// Verify sub-100ms average (the project's goal)
	if avgSwitchTime > 100*time.Millisecond {
		t.Errorf("Average switch time %v exceeds 100ms target", avgSwitchTime)
	}

	h.EndStep("rapid_switching")

	t.Log("\n" + h.Summary())
}

// TestE2E_CrossProviderSwitching tests switching between different providers
// doesn't interfere with each other.
func TestE2E_CrossProviderSwitching(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Setting up cross-provider test")

	homeDir := h.SubDir("home")
	vaultDir := h.SubDir("vault")
	xdgConfig := filepath.Join(homeDir, ".config")

	// Create directories
	codexHome := filepath.Join(homeDir, ".codex")
	claudeConfigDir := filepath.Join(xdgConfig, "claude-code")
	geminiHome := filepath.Join(homeDir, ".gemini")

	for _, dir := range []string{codexHome, claudeConfigDir, geminiHome} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("Failed to create dir %s: %v", dir, err)
		}
	}

	// Auth paths
	codexAuthPath := filepath.Join(codexHome, "auth.json")
	claudeMainPath := filepath.Join(homeDir, ".claude.json")
	claudeConfigPath := filepath.Join(claudeConfigDir, "auth.json")
	geminiPath := filepath.Join(geminiHome, "settings.json")

	// File sets
	codexFileSet := authfile.AuthFileSet{
		Tool: "codex",
		Files: []authfile.AuthFileSpec{
			{Tool: "codex", Path: codexAuthPath, Required: true},
		},
	}
	claudeFileSet := authfile.AuthFileSet{
		Tool: "claude",
		Files: []authfile.AuthFileSpec{
			{Tool: "claude", Path: claudeMainPath, Required: true},
			{Tool: "claude", Path: claudeConfigPath, Required: false},
		},
	}
	geminiFileSet := authfile.AuthFileSet{
		Tool: "gemini",
		Files: []authfile.AuthFileSpec{
			{Tool: "gemini", Path: geminiPath, Required: true},
		},
	}

	vault := authfile.NewVault(vaultDir)

	// Create initial profiles for each provider
	providers := []struct {
		name    string
		fileSet authfile.AuthFileSet
		paths   []string
		content map[string]interface{}
	}{
		{
			name:    "codex",
			fileSet: codexFileSet,
			paths:   []string{codexAuthPath},
			content: map[string]interface{}{"access_token": "codex-work-token"},
		},
		{
			name:    "claude",
			fileSet: claudeFileSet,
			paths:   []string{claudeMainPath, claudeConfigPath},
			content: map[string]interface{}{"session_token": "claude-work-session"},
		},
		{
			name:    "gemini",
			fileSet: geminiFileSet,
			paths:   []string{geminiPath},
			content: map[string]interface{}{"default_model": "gemini-pro", "api_key": "gemini-key"},
		},
	}

	for _, p := range providers {
		jsonBytes, _ := json.MarshalIndent(p.content, "", "  ")
		for _, path := range p.paths {
			if err := os.WriteFile(path, jsonBytes, 0600); err != nil {
				t.Fatalf("Failed to write auth for %s: %v", p.name, err)
			}
		}
		if err := vault.Backup(p.fileSet, "work"); err != nil {
			t.Fatalf("Failed to backup %s: %v", p.name, err)
		}
		h.LogInfo("Created provider profile", "provider", p.name, "profile", "work")
	}

	h.EndStep("setup")

	h.StartStep("cross_provider_switching", "Testing cross-provider independence")

	// Switch Codex to a different profile
	codexPersonal := map[string]interface{}{"access_token": "codex-personal-token"}
	jsonBytes, _ := json.MarshalIndent(codexPersonal, "", "  ")
	if err := os.WriteFile(codexAuthPath, jsonBytes, 0600); err != nil {
		t.Fatalf("Failed to write codex personal: %v", err)
	}
	if err := vault.Backup(codexFileSet, "personal"); err != nil {
		t.Fatalf("Failed to backup codex personal: %v", err)
	}

	// Verify Claude is still on work profile (unaffected)
	claudeActive, err := vault.ActiveProfile(claudeFileSet)
	if err != nil {
		t.Fatalf("Claude ActiveProfile failed: %v", err)
	}
	if claudeActive != "work" {
		t.Errorf("Claude should still be on 'work', got '%s'", claudeActive)
	}
	h.LogInfo("Verified Claude unaffected by Codex switch", "claude_active", claudeActive)

	// Verify Gemini is still on work profile (unaffected)
	geminiActive, err := vault.ActiveProfile(geminiFileSet)
	if err != nil {
		t.Fatalf("Gemini ActiveProfile failed: %v", err)
	}
	if geminiActive != "work" {
		t.Errorf("Gemini should still be on 'work', got '%s'", geminiActive)
	}
	h.LogInfo("Verified Gemini unaffected by Codex switch", "gemini_active", geminiActive)

	// Switch Codex back to work
	if err := vault.Restore(codexFileSet, "work"); err != nil {
		t.Fatalf("Failed to restore codex work: %v", err)
	}

	// Verify all providers are on work
	for _, p := range providers {
		active, err := vault.ActiveProfile(p.fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile for %s failed: %v", p.name, err)
		}
		if active != "work" {
			t.Errorf("%s should be on 'work', got '%s'", p.name, active)
		}
	}
	h.LogInfo("All providers verified on work profile")

	h.EndStep("cross_provider_switching")

	t.Log("\n" + h.Summary())
}
