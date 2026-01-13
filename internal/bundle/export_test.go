package bundle

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultExportOptions(t *testing.T) {
	opts := DefaultExportOptions()

	if !opts.IncludeConfig {
		t.Error("IncludeConfig should default to true")
	}
	if !opts.IncludeProjects {
		t.Error("IncludeProjects should default to true")
	}
	if !opts.IncludeHealth {
		t.Error("IncludeHealth should default to true")
	}
	if opts.IncludeDatabase {
		t.Error("IncludeDatabase should default to false")
	}
	if !opts.IncludeSyncConfig {
		t.Error("IncludeSyncConfig should default to true")
	}
	if opts.Encrypt {
		t.Error("Encrypt should default to false")
	}
}

func TestVaultExporter_Export_DryRun(t *testing.T) {
	// Create test vault structure
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	claudeDir := filepath.Join(vaultDir, "claude", "alice@gmail.com")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "auth.json"), []byte(`{"token":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	opts := &ExportOptions{
		OutputDir: tmpDir,
		DryRun:    true,
	}

	result, err := exporter.Export(opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if result.OutputPath == "" {
		t.Error("OutputPath should be set")
	}

	if result.Manifest == nil {
		t.Error("Manifest should be set")
	}

	// Verify no file was created in dry run
	if _, err := os.Stat(result.OutputPath); !os.IsNotExist(err) {
		t.Error("File should not be created in dry run mode")
	}

	// Check manifest has the profile
	if len(result.Manifest.Contents.Vault.Profiles) == 0 {
		t.Error("Vault profiles should be populated")
	}

	profiles := result.Manifest.Contents.Vault.Profiles["claude"]
	if len(profiles) != 1 || profiles[0] != "alice@gmail.com" {
		t.Errorf("Expected claude/alice@gmail.com, got %v", profiles)
	}
}

func TestVaultExporter_Export_Full(t *testing.T) {
	// Create test vault structure
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	claudeDir := filepath.Join(vaultDir, "claude", "alice@gmail.com")
	codexDir := filepath.Join(vaultDir, "codex", "work@company.com")

	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codexDir, 0700); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(claudeDir, "auth.json"), []byte(`{"token":"claude_token"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexDir, "auth.json"), []byte(`{"token":"codex_token"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Create config file
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("default_provider: claude"), 0600); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath:  vaultDir,
		DataPath:   tmpDir,
		ConfigPath: configPath,
	}

	opts := &ExportOptions{
		OutputDir:     outputDir,
		IncludeConfig: true,
	}

	result, err := exporter.Export(opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(result.OutputPath); err != nil {
		t.Errorf("Output file should exist: %v", err)
	}

	// Verify it's a valid zip
	reader, err := zip.OpenReader(result.OutputPath)
	if err != nil {
		t.Fatalf("Should be valid zip: %v", err)
	}
	defer reader.Close()

	// Check expected files are in the zip
	fileNames := make(map[string]bool)
	for _, f := range reader.File {
		fileNames[f.Name] = true
	}

	if !fileNames["manifest.json"] {
		t.Error("manifest.json should be in bundle")
	}
	if !fileNames["vault/claude/alice@gmail.com/auth.json"] {
		t.Error("claude auth should be in bundle")
	}
	if !fileNames["vault/codex/work@company.com/auth.json"] {
		t.Error("codex auth should be in bundle")
	}
	if !fileNames["config.yaml"] {
		t.Error("config should be in bundle")
	}

	// Verify manifest content
	if result.Manifest.Contents.Vault.TotalProfiles != 2 {
		t.Errorf("TotalProfiles = %d, want 2", result.Manifest.Contents.Vault.TotalProfiles)
	}
}

func TestVaultExporter_Export_WithEncryption(t *testing.T) {
	// Create test vault structure
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	claudeDir := filepath.Join(vaultDir, "claude", "test@gmail.com")

	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "auth.json"), []byte(`{"token":"secret"}`), 0600); err != nil {
		t.Fatal(err)
	}

	outputDir := filepath.Join(tmpDir, "output")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	password := "test-password-123"
	opts := &ExportOptions{
		OutputDir: outputDir,
		Encrypt:   true,
		Password:  password,
	}

	result, err := exporter.Export(opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	if !result.Encrypted {
		t.Error("Result should be marked as encrypted")
	}

	// Verify file exists
	if _, err := os.Stat(result.OutputPath); err != nil {
		t.Errorf("Output file should exist: %v", err)
	}

	// Verify metadata file exists
	metaPath := result.OutputPath + ".meta"
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("Metadata file should exist: %v", err)
	}

	var meta EncryptionMetadata
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("Metadata should be valid JSON: %v", err)
	}

	if meta.Algorithm != "aes-256-gcm" {
		t.Errorf("Algorithm = %q, want aes-256-gcm", meta.Algorithm)
	}

	// The encrypted file should NOT be a valid zip directly
	if _, err := zip.OpenReader(result.OutputPath); err == nil {
		t.Error("Encrypted file should not be readable as plain zip")
	}
}

func TestVaultExporter_Export_ProviderFilter(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")

	// Create profiles for multiple providers
	for _, provider := range []string{"claude", "codex", "gemini"} {
		dir := filepath.Join(vaultDir, provider, "profile@test.com")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	opts := &ExportOptions{
		OutputDir:      filepath.Join(tmpDir, "output"),
		ProviderFilter: []string{"claude"},
		DryRun:         true,
	}

	result, err := exporter.Export(opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Should only have claude
	if len(result.Manifest.Contents.Vault.Profiles) != 1 {
		t.Errorf("Should have 1 provider, got %d", len(result.Manifest.Contents.Vault.Profiles))
	}

	if _, ok := result.Manifest.Contents.Vault.Profiles["claude"]; !ok {
		t.Error("Should have claude provider")
	}
}

func TestVaultExporter_Export_SkipsSystemProfiles(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")

	// Create user profile
	userDir := filepath.Join(vaultDir, "claude", "user@gmail.com")
	if err := os.MkdirAll(userDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(userDir, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create system profile (starts with _)
	systemDir := filepath.Join(vaultDir, "claude", "_original")
	if err := os.MkdirAll(systemDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(systemDir, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	result, err := exporter.Export(&ExportOptions{
		OutputDir: filepath.Join(tmpDir, "output"),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Should only have user profile
	profiles := result.Manifest.Contents.Vault.Profiles["claude"]
	if len(profiles) != 1 || profiles[0] != "user@gmail.com" {
		t.Errorf("Should only include user profile, got %v", profiles)
	}
}

func TestVaultExporter_Export_VerboseFilename(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	claudeDir := filepath.Join(vaultDir, "claude", "test@gmail.com")

	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	opts := &ExportOptions{
		OutputDir:       filepath.Join(tmpDir, "output"),
		VerboseFilename: true,
		DryRun:          true,
	}

	result, err := exporter.Export(opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	filename := filepath.Base(result.OutputPath)
	if !strings.HasPrefix(filename, "Exported_Coding_Agent_Account_Auth_Info") {
		t.Errorf("Verbose filename should start with expected prefix, got %s", filename)
	}
}

func TestVaultExporter_Export_NoVault(t *testing.T) {
	tmpDir := t.TempDir()

	exporter := &VaultExporter{
		VaultPath: filepath.Join(tmpDir, "nonexistent"),
		DataPath:  tmpDir,
	}

	_, err := exporter.Export(&ExportOptions{
		OutputDir: tmpDir,
	})

	if err == nil {
		t.Error("Should fail with nonexistent vault")
	}
}

func TestVaultExporter_Export_EmptyVault(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")

	if err := os.MkdirAll(vaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	_, err := exporter.Export(&ExportOptions{
		OutputDir: tmpDir,
	})

	if err == nil {
		t.Error("Should fail with empty vault")
	}
}

func TestVaultExporter_Export_EncryptionRequiresPassword(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	claudeDir := filepath.Join(vaultDir, "claude", "test@gmail.com")

	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tmpDir,
	}

	_, err := exporter.Export(&ExportOptions{
		OutputDir: tmpDir,
		Encrypt:   true,
		Password:  "", // No password!
	})

	if err == nil {
		t.Error("Should fail when encryption enabled without password")
	}
}

func TestFormatSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{100, "100 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		got := FormatSize(tt.bytes)
		if got != tt.want {
			t.Errorf("FormatSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

func TestContains(t *testing.T) {
	slice := []string{"claude", "codex", "gemini"}

	if !contains(slice, "claude") {
		t.Error("Should find claude")
	}
	if !contains(slice, "CLAUDE") {
		t.Error("Should find CLAUDE (case insensitive)")
	}
	if contains(slice, "unknown") {
		t.Error("Should not find unknown")
	}
}

func TestMatchesAny(t *testing.T) {
	patterns := []string{"alice", "bob"}

	if !matchesAny("alice@gmail.com", patterns) {
		t.Error("Should match alice pattern")
	}
	if !matchesAny("ALICE@gmail.com", patterns) {
		t.Error("Should match ALICE pattern (case insensitive)")
	}
	if matchesAny("charlie@gmail.com", patterns) {
		t.Error("Should not match charlie")
	}
}

func TestCreateZipFromDir(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some files
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.MkdirAll(subDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("content2"), 0600); err != nil {
		t.Fatal(err)
	}

	zipPath := filepath.Join(tmpDir, "test.zip")
	if err := createZipFromDir(tmpDir, zipPath); err != nil {
		t.Fatalf("createZipFromDir failed: %v", err)
	}

	// Verify zip contents
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("Failed to open zip: %v", err)
	}
	defer reader.Close()

	fileNames := make(map[string]bool)
	for _, f := range reader.File {
		fileNames[f.Name] = true
	}

	if !fileNames["file1.txt"] {
		t.Error("file1.txt should be in zip")
	}
	if !fileNames["subdir/file2.txt"] {
		t.Error("subdir/file2.txt should be in zip")
	}
}

// Tests for collectProjectFiles
func TestCollectProjectFiles(t *testing.T) {
	t.Run("no projects path", func(t *testing.T) {
		exporter := &VaultExporter{
			ProjectsPath: "",
		}
		manifest := NewManifest()

		files, err := exporter.collectProjectFiles(manifest)
		if err != nil {
			t.Fatalf("collectProjectFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Projects.Included {
			t.Error("Projects should not be included")
		}
	})

	t.Run("projects file does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		exporter := &VaultExporter{
			ProjectsPath: filepath.Join(tmpDir, "nonexistent.json"),
		}
		manifest := NewManifest()

		files, err := exporter.collectProjectFiles(manifest)
		if err != nil {
			t.Fatalf("collectProjectFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Projects.Included {
			t.Error("Projects should not be included")
		}
	})

	t.Run("projects file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		projectsPath := filepath.Join(tmpDir, "projects.json")

		projectsData := `{"associations": {"/path/to/project1": {"claude": "work"}, "/path/to/project2": {"codex": "personal"}}}`
		if err := os.WriteFile(projectsPath, []byte(projectsData), 0600); err != nil {
			t.Fatal(err)
		}

		exporter := &VaultExporter{
			ProjectsPath: projectsPath,
		}
		manifest := NewManifest()

		files, err := exporter.collectProjectFiles(manifest)
		if err != nil {
			t.Fatalf("collectProjectFiles error = %v", err)
		}

		if len(files) != 1 {
			t.Errorf("Expected 1 file, got %d", len(files))
		}
		if !manifest.Contents.Projects.Included {
			t.Error("Projects should be included")
		}
		if manifest.Contents.Projects.Count != 2 {
			t.Errorf("Count = %d, want 2", manifest.Contents.Projects.Count)
		}
	})
}

// Tests for collectHealthFiles
func TestCollectHealthFiles(t *testing.T) {
	t.Run("no health path", func(t *testing.T) {
		exporter := &VaultExporter{
			HealthPath: "",
		}
		manifest := NewManifest()

		files, err := exporter.collectHealthFiles(manifest)
		if err != nil {
			t.Fatalf("collectHealthFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Health.Included {
			t.Error("Health should not be included")
		}
	})

	t.Run("health path does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		exporter := &VaultExporter{
			HealthPath: filepath.Join(tmpDir, "nonexistent"),
		}
		manifest := NewManifest()

		files, err := exporter.collectHealthFiles(manifest)
		if err != nil {
			t.Fatalf("collectHealthFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Health.Included {
			t.Error("Health should not be included")
		}
	})

	t.Run("health directory with files", func(t *testing.T) {
		tmpDir := t.TempDir()
		healthPath := filepath.Join(tmpDir, "health")

		// Create health directory with some files
		if err := os.MkdirAll(healthPath, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(healthPath, "health.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}

		exporter := &VaultExporter{
			HealthPath: healthPath,
		}
		manifest := NewManifest()

		files, err := exporter.collectHealthFiles(manifest)
		if err != nil {
			t.Fatalf("collectHealthFiles error = %v", err)
		}

		if len(files) != 1 {
			t.Errorf("Expected 1 file, got %d", len(files))
		}
		if !manifest.Contents.Health.Included {
			t.Error("Health should be included")
		}
	})

	t.Run("empty health directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		healthPath := filepath.Join(tmpDir, "health")

		if err := os.MkdirAll(healthPath, 0700); err != nil {
			t.Fatal(err)
		}

		exporter := &VaultExporter{
			HealthPath: healthPath,
		}
		manifest := NewManifest()

		files, err := exporter.collectHealthFiles(manifest)
		if err != nil {
			t.Fatalf("collectHealthFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Health.Included {
			t.Error("Health should not be included for empty dir")
		}
	})
}

// Tests for collectDatabaseFiles
func TestCollectDatabaseFiles(t *testing.T) {
	t.Run("no database path", func(t *testing.T) {
		exporter := &VaultExporter{
			DatabasePath: "",
		}
		manifest := NewManifest()

		files, err := exporter.collectDatabaseFiles(manifest)
		if err != nil {
			t.Fatalf("collectDatabaseFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Database.Included {
			t.Error("Database should not be included")
		}
	})

	t.Run("database does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		exporter := &VaultExporter{
			DatabasePath: filepath.Join(tmpDir, "nonexistent.db"),
		}
		manifest := NewManifest()

		files, err := exporter.collectDatabaseFiles(manifest)
		if err != nil {
			t.Fatalf("collectDatabaseFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.Database.Included {
			t.Error("Database should not be included")
		}
	})

	t.Run("database exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		dbPath := filepath.Join(tmpDir, "caam.db")

		if err := os.WriteFile(dbPath, []byte("sqlite db data"), 0600); err != nil {
			t.Fatal(err)
		}

		exporter := &VaultExporter{
			DatabasePath: dbPath,
		}
		manifest := NewManifest()

		files, err := exporter.collectDatabaseFiles(manifest)
		if err != nil {
			t.Fatalf("collectDatabaseFiles error = %v", err)
		}

		if len(files) != 1 {
			t.Errorf("Expected 1 file, got %d", len(files))
		}
		if !manifest.Contents.Database.Included {
			t.Error("Database should be included")
		}
		if manifest.Contents.Database.Path != "caam.db" {
			t.Errorf("Path = %q, want caam.db", manifest.Contents.Database.Path)
		}
	})
}

// Tests for collectSyncFiles
func TestCollectSyncFiles(t *testing.T) {
	t.Run("no sync path", func(t *testing.T) {
		exporter := &VaultExporter{
			SyncPath: "",
		}
		manifest := NewManifest()

		files, err := exporter.collectSyncFiles(manifest)
		if err != nil {
			t.Fatalf("collectSyncFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.SyncConfig.Included {
			t.Error("SyncConfig should not be included")
		}
	})

	t.Run("sync path does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		exporter := &VaultExporter{
			SyncPath: filepath.Join(tmpDir, "nonexistent"),
		}
		manifest := NewManifest()

		files, err := exporter.collectSyncFiles(manifest)
		if err != nil {
			t.Fatalf("collectSyncFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.SyncConfig.Included {
			t.Error("SyncConfig should not be included")
		}
	})

	t.Run("sync directory with files", func(t *testing.T) {
		tmpDir := t.TempDir()
		syncPath := filepath.Join(tmpDir, "sync")

		// Create sync directory with config files
		if err := os.MkdirAll(syncPath, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(syncPath, "pool.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(syncPath, "machines.json"), []byte("[]"), 0600); err != nil {
			t.Fatal(err)
		}

		exporter := &VaultExporter{
			SyncPath: syncPath,
		}
		manifest := NewManifest()

		files, err := exporter.collectSyncFiles(manifest)
		if err != nil {
			t.Fatalf("collectSyncFiles error = %v", err)
		}

		if len(files) != 2 {
			t.Errorf("Expected 2 files, got %d", len(files))
		}
		if !manifest.Contents.SyncConfig.Included {
			t.Error("SyncConfig should be included")
		}
	})

	t.Run("empty sync directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		syncPath := filepath.Join(tmpDir, "sync")

		if err := os.MkdirAll(syncPath, 0700); err != nil {
			t.Fatal(err)
		}

		exporter := &VaultExporter{
			SyncPath: syncPath,
		}
		manifest := NewManifest()

		files, err := exporter.collectSyncFiles(manifest)
		if err != nil {
			t.Fatalf("collectSyncFiles error = %v", err)
		}

		if len(files) != 0 {
			t.Errorf("Expected no files, got %d", len(files))
		}
		if manifest.Contents.SyncConfig.Included {
			t.Error("SyncConfig should not be included for empty dir")
		}
	})
}

// Tests for Export with optional content
func TestVaultExporter_Export_WithOptionalContent(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	outputDir := filepath.Join(tmpDir, "output")

	// Create vault with profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create projects file
	projectsPath := filepath.Join(tmpDir, "projects.json")
	if err := os.WriteFile(projectsPath, []byte(`{"/proj1": {}}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Create health directory
	healthPath := filepath.Join(tmpDir, "health")
	if err := os.MkdirAll(healthPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(healthPath, "health.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create database
	dbPath := filepath.Join(tmpDir, "caam.db")
	if err := os.WriteFile(dbPath, []byte("db data"), 0600); err != nil {
		t.Fatal(err)
	}

	// Create sync directory
	syncPath := filepath.Join(tmpDir, "sync")
	if err := os.MkdirAll(syncPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(syncPath, "pool.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	exporter := &VaultExporter{
		VaultPath:    vaultDir,
		DataPath:     tmpDir,
		ProjectsPath: projectsPath,
		HealthPath:   healthPath,
		DatabasePath: dbPath,
		SyncPath:     syncPath,
	}

	opts := DefaultExportOptions()
	opts.OutputDir = outputDir
	opts.IncludeProjects = true
	opts.IncludeHealth = true
	opts.IncludeDatabase = true
	opts.IncludeSyncConfig = true

	result, err := exporter.Export(opts)
	if err != nil {
		t.Fatalf("Export failed: %v", err)
	}

	// Verify manifest includes all content
	if !result.Manifest.Contents.Projects.Included {
		t.Error("Projects should be included")
	}
	if !result.Manifest.Contents.Health.Included {
		t.Error("Health should be included")
	}
	if !result.Manifest.Contents.Database.Included {
		t.Error("Database should be included")
	}
	if !result.Manifest.Contents.SyncConfig.Included {
		t.Error("SyncConfig should be included")
	}

	// Verify files are in the zip
	reader, err := zip.OpenReader(result.OutputPath)
	if err != nil {
		t.Fatalf("Failed to open zip: %v", err)
	}
	defer reader.Close()

	fileNames := make(map[string]bool)
	for _, f := range reader.File {
		fileNames[f.Name] = true
	}

	if !fileNames["projects.json"] {
		t.Error("projects.json should be in bundle")
	}
	if !fileNames["health/health.json"] {
		t.Error("health/health.json should be in bundle")
	}
	if !fileNames["caam.db"] {
		t.Error("caam.db should be in bundle")
	}
	if !fileNames["sync/pool.json"] {
		t.Error("sync/pool.json should be in bundle")
	}
}
