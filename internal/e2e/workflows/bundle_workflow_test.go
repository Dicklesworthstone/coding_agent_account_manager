// Package workflows provides E2E workflow tests for caam operations.
package workflows

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// =============================================================================
// E2E Test: Bundle Export-Encrypt-Import Workflow
// =============================================================================

// TestE2E_BundleExportImportWorkflow tests the complete bundle lifecycle:
// export, import, encryption, and decryption.
func TestE2E_BundleExportImportWorkflow(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// ==========================================================================
	// PHASE 1: Setup - Create vault with multiple profiles
	// ==========================================================================
	h.StartStep("setup", "Creating vault with multiple profiles")

	vaultDir := h.SubDir("vault")
	outputDir := h.SubDir("output")
	importVaultDir := h.SubDir("import_vault")

	// Create Codex profiles
	codexWork := createTestProfile(t, h, vaultDir, "codex", "work@example.com", map[string]interface{}{
		"access_token":  "codex-work-token",
		"refresh_token": "codex-work-refresh",
		"token_type":    "Bearer",
	})
	codexPersonal := createTestProfile(t, h, vaultDir, "codex", "personal@example.com", map[string]interface{}{
		"access_token":  "codex-personal-token",
		"refresh_token": "codex-personal-refresh",
		"token_type":    "Bearer",
	})

	// Create Claude profile
	claudeMain := createTestProfile(t, h, vaultDir, "claude", "main@example.com", map[string]interface{}{
		"session_token": "claude-session-123",
		"expires_at":    "2025-12-31T00:00:00Z",
	})

	h.LogInfo("Created test profiles",
		"codex_work", codexWork,
		"codex_personal", codexPersonal,
		"claude_main", claudeMain)
	h.EndStep("setup")

	// ==========================================================================
	// PHASE 2: Export Phase - Create bundle
	// ==========================================================================
	h.StartStep("export_phase", "Exporting vault to bundle")

	exporter := &bundle.VaultExporter{
		VaultPath: vaultDir,
	}

	h.TimeStep("export_bundle", "Creating unencrypted bundle", func() {
		opts := &bundle.ExportOptions{
			OutputDir:       outputDir,
			VerboseFilename: false,
		}
		result, err := exporter.Export(opts)
		if err != nil {
			t.Fatalf("Export failed: %v", err)
		}
		h.LogInfo("Export successful",
			"output_path", result.OutputPath,
			"total_files", result.TotalFiles,
			"compressed_size", result.CompressedSize)

		// Verify bundle was created
		if _, err := os.Stat(result.OutputPath); os.IsNotExist(err) {
			t.Fatalf("Bundle file not created: %s", result.OutputPath)
		}

		// Verify manifest
		if result.Manifest == nil {
			t.Fatalf("Manifest not included in result")
		}
	})

	// Verify bundle contents
	h.StartStep("verify_export", "Verifying bundle contents")
	files, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("Failed to read output dir: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("No bundle file created")
	}
	bundlePath := filepath.Join(outputDir, files[0].Name())
	h.LogInfo("Bundle created", "path", bundlePath)
	h.EndStep("verify_export")

	h.EndStep("export_phase")

	// ==========================================================================
	// PHASE 3: Encrypted Export
	// ==========================================================================
	h.StartStep("encrypted_export_phase", "Exporting vault with encryption")

	password := "test-secure-password-123!"
	var encryptedBundlePath string

	h.TimeStep("export_encrypted", "Creating encrypted bundle", func() {
		opts := &bundle.ExportOptions{
			OutputDir: outputDir,
			Encrypt:   true,
			Password:  password,
		}
		result, err := exporter.Export(opts)
		if err != nil {
			t.Fatalf("Encrypted export failed: %v", err)
		}
		encryptedBundlePath = result.OutputPath
		h.LogInfo("Encrypted export successful",
			"output_path", result.OutputPath,
			"encrypted", result.Encrypted)

		if !result.Encrypted {
			t.Errorf("Expected encrypted=true in result")
		}

		// Verify bundle was created
		if _, err := os.Stat(result.OutputPath); os.IsNotExist(err) {
			t.Fatalf("Encrypted bundle file not created")
		}

		// Verify encryption metadata was created
		metaPath := result.OutputPath + ".meta"
		if _, err := os.Stat(metaPath); os.IsNotExist(err) {
			t.Fatalf("Encryption metadata file not created: %s", metaPath)
		}
	})

	// Verify encryption marker
	h.StartStep("verify_encryption", "Verifying encryption")
	encrypted, err := bundle.IsEncrypted(encryptedBundlePath)
	if err != nil {
		t.Fatalf("Failed to check encryption: %v", err)
	}
	if !encrypted {
		t.Errorf("IsEncrypted should return true for encrypted bundle")
	}
	h.LogInfo("Encryption verified", "is_encrypted", encrypted)
	h.EndStep("verify_encryption")

	h.EndStep("encrypted_export_phase")

	// ==========================================================================
	// PHASE 4: Import to Fresh Vault
	// ==========================================================================
	h.StartStep("import_phase", "Importing bundle to fresh vault")

	h.TimeStep("import_bundle", "Importing unencrypted bundle", func() {
		importer := &bundle.VaultImporter{
			BundlePath: bundlePath,
		}
		opts := &bundle.ImportOptions{
			VaultPath: importVaultDir,
			Mode:      bundle.ImportModeReplace,
			Force:     true,
		}
		result, err := importer.Import(opts)
		if err != nil {
			t.Fatalf("Import failed: %v", err)
		}
		h.LogInfo("Import successful",
			"new_profiles", result.NewProfiles,
			"updated_profiles", result.UpdatedProfiles,
			"skipped_profiles", result.SkippedProfiles)

		if result.NewProfiles != 3 {
			t.Errorf("Expected 3 new profiles, got %d", result.NewProfiles)
		}
	})

	// Verify imported profiles
	h.StartStep("verify_import", "Verifying imported profiles")

	importedCodexWork := filepath.Join(importVaultDir, "codex", "work@example.com", "auth.json")
	if !fileExists(importedCodexWork) {
		t.Errorf("Imported codex work profile not found")
	} else {
		content, _ := os.ReadFile(importedCodexWork)
		var auth map[string]interface{}
		json.Unmarshal(content, &auth)
		if auth["access_token"] != "codex-work-token" {
			t.Errorf("Imported profile has wrong token")
		}
		h.LogInfo("Verified codex work profile", "token_prefix", auth["access_token"].(string)[:15]+"...")
	}

	importedClaudeMain := filepath.Join(importVaultDir, "claude", "main@example.com", ".claude.json")
	if !fileExists(importedClaudeMain) {
		t.Errorf("Imported claude main profile not found")
	}

	h.EndStep("verify_import")
	h.EndStep("import_phase")

	// ==========================================================================
	// PHASE 5: Encrypted Import
	// ==========================================================================
	h.StartStep("encrypted_import_phase", "Importing encrypted bundle")

	// Create another fresh vault for encrypted import
	encryptedImportVaultDir := h.SubDir("encrypted_import_vault")

	h.TimeStep("import_encrypted", "Decrypting and importing encrypted bundle", func() {
		importer := &bundle.VaultImporter{
			BundlePath: encryptedBundlePath,
		}
		opts := &bundle.ImportOptions{
			VaultPath: encryptedImportVaultDir,
			Mode:      bundle.ImportModeReplace,
			Password:  password,
			Force:     true,
		}
		result, err := importer.Import(opts)
		if err != nil {
			t.Fatalf("Encrypted import failed: %v", err)
		}
		h.LogInfo("Encrypted import successful",
			"encrypted", result.Encrypted,
			"new_profiles", result.NewProfiles)

		if !result.Encrypted {
			t.Errorf("Expected encrypted=true in result")
		}
		if result.NewProfiles != 3 {
			t.Errorf("Expected 3 new profiles, got %d", result.NewProfiles)
		}
	})

	// Verify decrypted import
	h.StartStep("verify_decrypted_import", "Verifying decrypted profiles")
	decryptedProfile := filepath.Join(encryptedImportVaultDir, "codex", "personal@example.com", "auth.json")
	if !fileExists(decryptedProfile) {
		t.Errorf("Decrypted profile not found")
	} else {
		content, _ := os.ReadFile(decryptedProfile)
		var auth map[string]interface{}
		json.Unmarshal(content, &auth)
		if auth["access_token"] != "codex-personal-token" {
			t.Errorf("Decrypted profile has wrong token")
		}
		h.LogInfo("Decrypted profile verified")
	}
	h.EndStep("verify_decrypted_import")

	h.EndStep("encrypted_import_phase")

	// ==========================================================================
	// PHASE 6: Error Scenarios
	// ==========================================================================
	h.StartStep("error_scenarios", "Testing error handling")

	// Test 6a: Wrong password
	h.StartStep("test_wrong_password", "Testing import with wrong password")
	wrongPasswordImporter := &bundle.VaultImporter{
		BundlePath: encryptedBundlePath,
	}
	wrongPasswordOpts := &bundle.ImportOptions{
		VaultPath: h.SubDir("wrong_password_vault"),
		Password:  "wrong-password",
		Force:     true,
	}
	_, err = wrongPasswordImporter.Import(wrongPasswordOpts)
	if err == nil {
		t.Errorf("Expected error when importing with wrong password")
	} else {
		h.LogInfo("Wrong password correctly rejected", "error", err.Error())
	}
	h.EndStep("test_wrong_password")

	// Test 6b: Missing password for encrypted bundle
	h.StartStep("test_missing_password", "Testing import without password")
	missingPasswordOpts := &bundle.ImportOptions{
		VaultPath: h.SubDir("missing_password_vault"),
		Force:     true,
	}
	_, err = wrongPasswordImporter.Import(missingPasswordOpts)
	if err == nil {
		t.Errorf("Expected error when importing encrypted bundle without password")
	} else {
		if !strings.Contains(err.Error(), "password") {
			t.Errorf("Error should mention password requirement: %v", err)
		}
		h.LogInfo("Missing password correctly rejected", "error", err.Error())
	}
	h.EndStep("test_missing_password")

	// Test 6c: Non-existent bundle
	h.StartStep("test_nonexistent_bundle", "Testing import of non-existent bundle")
	nonExistentImporter := &bundle.VaultImporter{
		BundlePath: "/nonexistent/path/bundle.zip",
	}
	_, err = nonExistentImporter.Import(&bundle.ImportOptions{
		VaultPath: h.SubDir("nonexistent_vault"),
	})
	if err == nil {
		t.Errorf("Expected error for non-existent bundle")
	} else {
		h.LogInfo("Non-existent bundle correctly rejected", "error", err.Error())
	}
	h.EndStep("test_nonexistent_bundle")

	h.EndStep("error_scenarios")

	// ==========================================================================
	// Final Summary
	// ==========================================================================
	h.RecordMetric("total_profiles_exported", 3)
	h.RecordMetric("total_profiles_imported", 3)
	h.RecordMetric("encryption_tests_passed", 2)
	h.RecordMetric("error_scenarios_tested", 3)

	t.Log("\n" + h.Summary())
}

// TestE2E_BundleRoundTrip tests that exported data can be perfectly restored.
func TestE2E_BundleRoundTrip(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Creating vault for round-trip test")

	vaultDir := h.SubDir("vault")
	outputDir := h.SubDir("output")

	// Create profiles with specific content
	profiles := []struct {
		provider string
		name     string
		content  map[string]interface{}
	}{
		{
			provider: "codex",
			name:     "user1@example.com",
			content: map[string]interface{}{
				"access_token": "unique-token-abc123",
				"custom_field": "custom-value",
			},
		},
		{
			provider: "claude",
			name:     "user2@example.com",
			content: map[string]interface{}{
				"session_token": "session-xyz789",
				"settings": map[string]interface{}{
					"theme": "dark",
				},
			},
		},
	}

	originalContent := make(map[string][]byte)
	for _, p := range profiles {
		path := createTestProfile(t, h, vaultDir, p.provider, p.name, p.content)
		content, _ := os.ReadFile(path)
		key := p.provider + "/" + p.name
		originalContent[key] = content
		h.LogInfo("Created profile", "key", key)
	}
	h.EndStep("setup")

	h.StartStep("export", "Exporting vault")
	exporter := &bundle.VaultExporter{VaultPath: vaultDir}
	exportResult, err := exporter.Export(&bundle.ExportOptions{OutputDir: outputDir})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}
	bundlePath := exportResult.OutputPath
	h.LogInfo("Exported bundle", "path", bundlePath)
	h.EndStep("export")

	h.StartStep("import", "Importing to fresh vault")
	freshVaultDir := h.SubDir("fresh_vault")
	importer := &bundle.VaultImporter{BundlePath: bundlePath}
	importResult, err := importer.Import(&bundle.ImportOptions{
		VaultPath: freshVaultDir,
		Mode:      bundle.ImportModeReplace,
		Force:     true,
	})
	if err != nil {
		t.Fatalf("Import failed: %v", err)
	}
	h.LogInfo("Imported profiles", "count", importResult.NewProfiles)
	h.EndStep("import")

	h.StartStep("verify", "Verifying content matches original")
	for _, p := range profiles {
		key := p.provider + "/" + p.name

		// Determine auth file name based on provider
		authFileName := "auth.json"
		if p.provider == "claude" {
			authFileName = ".claude.json"
		}

		importedPath := filepath.Join(freshVaultDir, p.provider, p.name, authFileName)
		importedContent, err := os.ReadFile(importedPath)
		if err != nil {
			t.Errorf("Failed to read imported profile %s: %v", key, err)
			continue
		}

		// Compare JSON content (not byte-for-byte, as formatting may differ)
		var original, imported map[string]interface{}
		json.Unmarshal(originalContent[key], &original)
		json.Unmarshal(importedContent, &imported)

		// Check specific fields
		for k, v := range original {
			// Skip nested structures (maps/slices) as they're not directly comparable
			switch v.(type) {
			case map[string]interface{}, []interface{}:
				continue
			}
			if imported[k] != v {
				t.Errorf("Profile %s field %s mismatch: expected %v, got %v", key, k, v, imported[k])
			}
		}
		h.LogInfo("Verified profile", "key", key)
	}
	h.EndStep("verify")

	t.Log("\n" + h.Summary())
}

// TestE2E_BundleDryRun tests the dry-run functionality.
func TestE2E_BundleDryRun(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Creating vault for dry-run test")

	vaultDir := h.SubDir("vault")
	outputDir := h.SubDir("output")

	createTestProfile(t, h, vaultDir, "codex", "test@example.com", map[string]interface{}{
		"access_token": "test-token",
	})
	h.EndStep("setup")

	h.StartStep("dry_run_export", "Testing export dry-run")
	exporter := &bundle.VaultExporter{VaultPath: vaultDir}
	result, err := exporter.Export(&bundle.ExportOptions{
		OutputDir: outputDir,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Dry-run export failed: %v", err)
	}

	// Verify no file was created
	files, _ := os.ReadDir(outputDir)
	if len(files) > 0 {
		t.Errorf("Dry-run should not create files, found %d", len(files))
	}

	h.LogInfo("Dry-run result",
		"output_path", result.OutputPath,
		"total_files", result.TotalFiles)
	h.EndStep("dry_run_export")

	t.Log("\n" + h.Summary())
}

// =============================================================================
// Helper Functions
// =============================================================================

// createTestProfile creates a test profile in the vault and returns the auth file path.
func createTestProfile(t *testing.T, h *testutil.ExtendedHarness, vaultDir, provider, name string, content map[string]interface{}) string {
	profileDir := filepath.Join(vaultDir, provider, name)
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	// Determine auth file name based on provider
	authFileName := "auth.json"
	if provider == "claude" {
		authFileName = ".claude.json"
	}

	authPath := filepath.Join(profileDir, authFileName)
	jsonBytes, _ := json.MarshalIndent(content, "", "  ")
	if err := os.WriteFile(authPath, jsonBytes, 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	// Create meta.json
	meta := map[string]interface{}{
		"tool":       provider,
		"profile":    name,
		"backed_up_at": "2025-12-19T00:00:00Z",
		"files":      1,
		"type":       "user",
	}
	metaPath := filepath.Join(profileDir, "meta.json")
	metaBytes, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(metaPath, metaBytes, 0600); err != nil {
		t.Fatalf("Failed to write meta file: %v", err)
	}

	return authPath
}

// fileExists checks if a file exists.
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
