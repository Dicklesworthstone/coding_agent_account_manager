package bundle

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

// ExportOptions configures the export operation.
type ExportOptions struct {
	// OutputDir is the directory to write the bundle to.
	OutputDir string

	// VerboseFilename uses the long descriptive filename format.
	VerboseFilename bool

	// Encrypt enables AES-256-GCM encryption with Argon2id key derivation.
	Encrypt bool

	// Password is the encryption password (required if Encrypt is true).
	Password string

	// IncludeConfig includes config.yaml in the bundle.
	IncludeConfig bool

	// IncludeProjects includes projects.json in the bundle.
	IncludeProjects bool

	// IncludeHealth includes health data in the bundle.
	IncludeHealth bool

	// IncludeDatabase includes the SQLite database in the bundle.
	IncludeDatabase bool

	// IncludeSyncConfig includes sync pool configuration in the bundle.
	IncludeSyncConfig bool

	// ProviderFilter limits export to specific providers (empty = all).
	ProviderFilter []string

	// ProfileFilter limits export to specific profile patterns (empty = all).
	ProfileFilter []string

	// DryRun shows what would be exported without creating a file.
	DryRun bool
}

// DefaultExportOptions returns sensible defaults for export.
func DefaultExportOptions() *ExportOptions {
	return &ExportOptions{
		IncludeConfig:     true,
		IncludeProjects:   true,
		IncludeHealth:     true,
		IncludeDatabase:   false, // Can be large
		IncludeSyncConfig: true,
	}
}

// ExportResult contains the results of an export operation.
type ExportResult struct {
	// OutputPath is the path to the created bundle file.
	OutputPath string

	// Manifest is the bundle manifest.
	Manifest *ManifestV1

	// Encrypted indicates if the bundle was encrypted.
	Encrypted bool

	// TotalFiles is the total number of files in the bundle.
	TotalFiles int

	// TotalSize is the total uncompressed size in bytes.
	TotalSize int64

	// CompressedSize is the final bundle size in bytes.
	CompressedSize int64
}

// VaultExporter handles exporting vault contents to a bundle.
type VaultExporter struct {
	// VaultPath is the path to the vault directory.
	VaultPath string

	// DataPath is the base caam data path (contains config, health, etc.).
	DataPath string

	// ConfigPath is the path to config.yaml.
	ConfigPath string

	// ProjectsPath is the path to projects.json.
	ProjectsPath string

	// HealthPath is the path to health data (file or directory).
	HealthPath string

	// DatabasePath is the path to caam.db.
	DatabasePath string

	// SyncPath is the path to sync configuration directory.
	SyncPath string

	afterCopyFile func(relativePath string)
}

// Export creates a vault bundle with the given options.
func (e *VaultExporter) Export(opts *ExportOptions) (*ExportResult, error) {
	if opts == nil {
		opts = DefaultExportOptions()
	}

	if opts.Encrypt && opts.Password == "" {
		return nil, fmt.Errorf("encryption enabled but no password provided")
	}
	if opts.DryRun {
		return e.export(opts)
	}
	// Do not create an empty vault merely to report that it cannot be exported.
	if err := validateRestorePath(e.VaultPath, true, false); err != nil {
		return nil, fmt.Errorf("read vault: %w", err)
	}
	var result *ExportResult
	err := authfile.NewVault(e.VaultPath).WithMutationLock(func() error {
		var err error
		result, err = e.export(opts)
		return err
	})
	return result, err
}

func (e *VaultExporter) export(opts *ExportOptions) (*ExportResult, error) {
	// Create manifest
	manifest := NewManifest()
	if err := e.populateSourceInfo(manifest); err != nil {
		return nil, fmt.Errorf("populate source info: %w", err)
	}

	// Collect files to include
	files, err := e.collectFiles(opts, manifest)
	if err != nil {
		return nil, fmt.Errorf("collect files: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no files to export")
	}

	// Generate output path
	outputPath := e.generateOutputPath(opts)

	// If dry run, return early
	if opts.DryRun {
		if err := checkExportGenerations(files); err != nil {
			return nil, err
		}
		return &ExportResult{
			OutputPath: outputPath,
			Manifest:   manifest,
			Encrypted:  opts.Encrypt,
			TotalFiles: len(files),
		}, nil
	}

	// Create temp directory for bundle assembly
	tempDir, err := os.MkdirTemp("", "caam-export-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Copy files to temp directory and compute checksums
	var totalSize int64
	for _, f := range files {
		destPath := filepath.Join(tempDir, f.RelPath)
		if err := os.MkdirAll(filepath.Dir(destPath), 0700); err != nil {
			return nil, fmt.Errorf("create dir for %s: %w", f.RelPath, err)
		}

		if f.generation != nil {
			data, _, err := f.generation.ReadFile(f.profileRelativePath)
			if err != nil {
				return nil, fmt.Errorf("copy %s: %w", f.RelPath, err)
			}
			if err := atomicWriteBytes(destPath, data, 0600); err != nil {
				return nil, fmt.Errorf("copy %s: %w", f.RelPath, err)
			}
		} else if err := copyFileForExport(f.SrcPath, destPath); err != nil {
			return nil, fmt.Errorf("copy %s: %w", f.RelPath, err)
		}
		if e.afterCopyFile != nil {
			e.afterCopyFile(f.RelPath)
		}

		// Compute checksum
		checksum, err := ComputeFileChecksum(destPath, DefaultAlgorithm)
		if err != nil {
			return nil, fmt.Errorf("checksum %s: %w", f.RelPath, err)
		}
		manifest.AddChecksum(NormalizePath(f.RelPath), checksum)

		info, _ := os.Stat(destPath)
		if info != nil {
			totalSize += info.Size()
		}
	}
	if err := checkExportGenerations(files); err != nil {
		return nil, err
	}

	// Write manifest
	if err := SaveManifest(tempDir, manifest); err != nil {
		return nil, fmt.Errorf("save manifest: %w", err)
	}

	// Create zip file
	zipPath := filepath.Join(tempDir, "bundle.zip")
	if err := createZipFromDir(tempDir, zipPath); err != nil {
		return nil, fmt.Errorf("create zip: %w", err)
	}
	if err := checkExportGenerations(files); err != nil {
		return nil, err
	}

	// Handle encryption if requested
	var finalPath string
	if opts.Encrypt {
		encPath, err := e.encryptBundle(zipPath, opts.Password, outputPath)
		if err != nil {
			return nil, fmt.Errorf("encrypt bundle: %w", err)
		}
		finalPath = encPath
	} else {
		// Move zip to output path
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return nil, fmt.Errorf("create output dir: %w", err)
		}
		if err := copyFileForExport(zipPath, outputPath); err != nil {
			return nil, fmt.Errorf("copy to output: %w", err)
		}
		finalPath = outputPath
	}

	// Get final file size
	info, err := os.Stat(finalPath)
	if err != nil {
		return nil, fmt.Errorf("stat final bundle: %w", err)
	}

	return &ExportResult{
		OutputPath:     finalPath,
		Manifest:       manifest,
		Encrypted:      opts.Encrypt,
		TotalFiles:     len(files) + 1, // +1 for manifest
		TotalSize:      totalSize,
		CompressedSize: info.Size(),
	}, nil
}

// fileEntry represents a file to include in the bundle.
type fileEntry struct {
	SrcPath string
	RelPath string

	generation          *vaulttransfer.Snapshot
	profileRelativePath string
}

func checkExportGenerations(files []fileEntry) error {
	checked := make(map[*vaulttransfer.Snapshot]bool)
	for _, file := range files {
		if file.generation == nil || checked[file.generation] {
			continue
		}
		if err := file.generation.Check(); err != nil {
			return fmt.Errorf("export refused: %w", err)
		}
		checked[file.generation] = true
	}
	return nil
}

// collectFiles gathers all files to include in the bundle.
func (e *VaultExporter) collectFiles(opts *ExportOptions, manifest *ManifestV1) ([]fileEntry, error) {
	var files []fileEntry

	// Collect vault profiles
	vaultFiles, err := e.collectVaultFiles(opts, manifest)
	if err != nil {
		return nil, fmt.Errorf("collect vault files: %w", err)
	}
	files = append(files, vaultFiles...)

	// Collect optional files
	if opts.IncludeConfig {
		if configFiles, err := e.collectConfigFiles(manifest); err == nil {
			files = append(files, configFiles...)
		}
	} else {
		manifest.SetConfig(false, "")
	}

	if opts.IncludeProjects {
		if projectFiles, err := e.collectProjectFiles(manifest); err == nil {
			files = append(files, projectFiles...)
		}
	} else {
		manifest.SetProjects(false, "", 0)
	}

	if opts.IncludeHealth {
		if healthFiles, err := e.collectHealthFiles(manifest); err == nil {
			files = append(files, healthFiles...)
		}
	} else {
		manifest.SetHealth(false, "")
	}

	if opts.IncludeDatabase {
		if dbFiles, err := e.collectDatabaseFiles(manifest); err == nil {
			files = append(files, dbFiles...)
		}
	} else {
		manifest.SetDatabase(false, "")
	}

	if opts.IncludeSyncConfig {
		if syncFiles, err := e.collectSyncFiles(manifest); err == nil {
			files = append(files, syncFiles...)
		}
	} else {
		manifest.SetSyncConfig(false, "")
	}

	return files, nil
}

// collectVaultFiles collects profile files from the vault.
func (e *VaultExporter) collectVaultFiles(opts *ExportOptions, manifest *ManifestV1) ([]fileEntry, error) {
	var files []fileEntry

	// Read vault directory
	entries, err := os.ReadDir(e.VaultPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("vault not found at %s", e.VaultPath)
		}
		return nil, err
	}

	for _, providerEntry := range entries {
		if !providerEntry.IsDir() {
			continue
		}

		provider := providerEntry.Name()
		if authfile.IsPrivateVaultEntry(provider) {
			continue
		}

		// Check provider filter
		if len(opts.ProviderFilter) > 0 && !contains(opts.ProviderFilter, provider) {
			continue
		}

		providerPath := filepath.Join(e.VaultPath, provider)
		profiles, err := os.ReadDir(providerPath)
		if err != nil {
			return nil, fmt.Errorf("read provider %s: %w", provider, err)
		}

		for _, profileEntry := range profiles {
			if !profileEntry.IsDir() {
				continue
			}

			profile := profileEntry.Name()

			// Check profile filter
			if len(opts.ProfileFilter) > 0 && !matchesAny(profile, opts.ProfileFilter) {
				continue
			}

			// Skip system profiles
			if authfile.IsSystemProfile(profile) || strings.HasPrefix(profile, ".caam_import_") {
				continue
			}

			profilePath := filepath.Join(providerPath, profile)
			profileFiles, err := e.collectProfileFiles(profilePath, provider, profile)
			if err != nil {
				return nil, fmt.Errorf("profile %s/%s: %w", provider, profile, err)
			}

			if len(profileFiles) > 0 {
				files = append(files, profileFiles...)
				manifest.AddProfile(provider, profile)
			}
		}
	}

	return files, nil
}

// collectProfileFiles collects all files from a profile directory.
func (e *VaultExporter) collectProfileFiles(profilePath, provider, profile string) ([]fileEntry, error) {
	var files []fileEntry
	generation, err := vaulttransfer.Capture(profilePath)
	if err != nil {
		return nil, err
	}
	if !generation.Exists() {
		return nil, fmt.Errorf("profile disappeared during export")
	}
	omittedFile := ""
	if strings.EqualFold(provider, "claude") {
		claude, err := vaulttransfer.InspectClaude(generation)
		if err != nil {
			return nil, err
		}
		if claude.BindingFile == "meta.json" {
			omittedFile = identity.ClaudeIdentityCacheFile
		}
	}
	for _, name := range generation.Files() {
		if name == omittedFile {
			continue
		}
		bundlePath := filepath.Join("vault", provider, profile, name)
		files = append(files, fileEntry{
			SrcPath:             filepath.Join(profilePath, name),
			RelPath:             NormalizePath(bundlePath),
			generation:          generation,
			profileRelativePath: name,
		})
	}
	return files, nil
}

// collectConfigFiles collects config files.
func (e *VaultExporter) collectConfigFiles(manifest *ManifestV1) ([]fileEntry, error) {
	if e.ConfigPath == "" {
		manifest.SetConfig(false, "")
		return nil, nil
	}

	if _, err := os.Stat(e.ConfigPath); os.IsNotExist(err) {
		manifest.SetConfig(false, "")
		return nil, nil
	}

	relPath := "config.yaml"
	manifest.SetConfig(true, relPath)

	return []fileEntry{{
		SrcPath: e.ConfigPath,
		RelPath: relPath,
	}}, nil
}

// collectProjectFiles collects project association files.
func (e *VaultExporter) collectProjectFiles(manifest *ManifestV1) ([]fileEntry, error) {
	if e.ProjectsPath == "" {
		manifest.SetProjects(false, "", 0)
		return nil, nil
	}

	if _, err := os.Stat(e.ProjectsPath); os.IsNotExist(err) {
		manifest.SetProjects(false, "", 0)
		return nil, nil
	}

	// Count associations
	count := 0
	if data, err := os.ReadFile(e.ProjectsPath); err == nil {
		var projects struct {
			Associations map[string]map[string]string `json:"associations"`
		}
		if json.Unmarshal(data, &projects) == nil {
			count = len(projects.Associations)
		}
	}

	relPath := "projects.json"
	manifest.SetProjects(true, relPath, count)

	return []fileEntry{{
		SrcPath: e.ProjectsPath,
		RelPath: relPath,
	}}, nil
}

// collectHealthFiles collects health data files.
func (e *VaultExporter) collectHealthFiles(manifest *ManifestV1) ([]fileEntry, error) {
	if e.HealthPath == "" {
		manifest.SetHealth(false, "")
		return nil, nil
	}

	info, err := os.Stat(e.HealthPath)
	if os.IsNotExist(err) {
		manifest.SetHealth(false, "")
		return nil, nil
	}
	if err != nil {
		manifest.SetHealth(false, "")
		return nil, err
	}

	if !info.IsDir() {
		relPath := "health.json"
		manifest.SetHealth(true, relPath)
		return []fileEntry{{
			SrcPath: e.HealthPath,
			RelPath: relPath,
		}}, nil
	}

	var files []fileEntry
	err = filepath.WalkDir(e.HealthPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}

		relToHealth, err := filepath.Rel(e.HealthPath, path)
		if err != nil {
			return err
		}

		bundlePath := filepath.Join("health", relToHealth)
		files = append(files, fileEntry{
			SrcPath: path,
			RelPath: NormalizePath(bundlePath),
		})
		return nil
	})

	if err != nil || len(files) == 0 {
		manifest.SetHealth(false, "")
		return nil, err
	}

	manifest.SetHealth(true, "health/")
	return files, nil
}

// collectDatabaseFiles collects the SQLite database.
func (e *VaultExporter) collectDatabaseFiles(manifest *ManifestV1) ([]fileEntry, error) {
	if e.DatabasePath == "" {
		manifest.SetDatabase(false, "")
		return nil, nil
	}

	if _, err := os.Stat(e.DatabasePath); os.IsNotExist(err) {
		manifest.SetDatabase(false, "")
		return nil, nil
	}

	relPath := "caam.db"
	manifest.SetDatabase(true, relPath)
	if err := caamdb.Checkpoint(e.DatabasePath); err != nil {
		manifest.Contents.Database.Note = fmt.Sprintf("wal checkpoint failed: %v", err)
	}

	return []fileEntry{{
		SrcPath: e.DatabasePath,
		RelPath: relPath,
	}}, nil
}

// collectSyncFiles collects sync configuration files.
func (e *VaultExporter) collectSyncFiles(manifest *ManifestV1) ([]fileEntry, error) {
	if e.SyncPath == "" {
		manifest.SetSyncConfig(false, "")
		return nil, nil
	}

	if _, err := os.Stat(e.SyncPath); os.IsNotExist(err) {
		manifest.SetSyncConfig(false, "")
		return nil, nil
	}

	var files []fileEntry
	err := filepath.WalkDir(e.SyncPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !d.Type().IsRegular() {
			return err
		}

		relToSync, err := filepath.Rel(e.SyncPath, path)
		if err != nil {
			return err
		}

		bundlePath := filepath.Join("sync", relToSync)
		files = append(files, fileEntry{
			SrcPath: path,
			RelPath: NormalizePath(bundlePath),
		})
		return nil
	})

	if err != nil || len(files) == 0 {
		manifest.SetSyncConfig(false, "")
		return nil, err
	}

	manifest.SetSyncConfig(true, "sync/")
	return files, nil
}

// populateSourceInfo fills in the manifest's source information.
func (e *VaultExporter) populateSourceInfo(manifest *ManifestV1) error {
	hostname, _ := os.Hostname()
	manifest.Source.Hostname = hostname
	manifest.Source.Platform = runtime.GOOS
	manifest.Source.Arch = runtime.GOARCH
	manifest.Source.CAAMDataPath = e.DataPath

	if u, err := user.Current(); err == nil {
		manifest.Source.Username = u.Username
	}

	return nil
}

// generateOutputPath creates the output file path based on options.
func (e *VaultExporter) generateOutputPath(opts *ExportOptions) string {
	now := time.Now()

	var filename string
	if opts.VerboseFilename {
		// Exported_Coding_Agent_Account_Auth_Info__As_of__12_19_2025__02_29_PM
		filename = fmt.Sprintf("Exported_Coding_Agent_Account_Auth_Info__As_of__%s",
			now.Format("01_02_2006__03_04_PM"))
	} else {
		// caam_export_2025-12-19_1429
		filename = fmt.Sprintf("caam_export_%s", now.Format("2006-01-02_1504"))
	}

	if opts.Encrypt {
		filename += EncryptedBundleMarker
	}
	filename += BundleFileExtension

	outputDir := opts.OutputDir
	if outputDir == "" {
		outputDir = "."
	}

	return filepath.Join(outputDir, filename)
}

// encryptBundle encrypts a zip file and saves it to the output path.
func (e *VaultExporter) encryptBundle(zipPath, password, outputPath string) (string, error) {
	// Read the zip file
	plainData, err := os.ReadFile(zipPath)
	if err != nil {
		return "", fmt.Errorf("read zip: %w", err)
	}

	// Encrypt
	ciphertext, meta, err := EncryptBundle(plainData, password)
	if err != nil {
		return "", fmt.Errorf("encrypt: %w", err)
	}

	// Create output directory
	outDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return "", fmt.Errorf("create output dir: %w", err)
	}

	// Write encrypted file atomically
	if err := atomicWriteBytes(outputPath, ciphertext, 0600); err != nil {
		return "", fmt.Errorf("write encrypted bundle: %w", err)
	}

	// Write metadata alongside (or embed in bundle)
	metaPath := outputPath + ".meta"
	metaData, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		os.Remove(outputPath)
		return "", fmt.Errorf("marshal metadata: %w", err)
	}
	if err := atomicWriteBytes(metaPath, metaData, 0600); err != nil {
		os.Remove(outputPath)
		return "", fmt.Errorf("write metadata: %w", err)
	}

	return outputPath, nil
}

// atomicWriteBytes writes data to a file atomically using temp file + fsync + rename.
func atomicWriteBytes(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, filepath.Base(path)+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on error; no-op after successful rename

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Chmod(perm); err != nil {
		tmpFile.Close()
		return fmt.Errorf("chmod temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// createZipFromDir creates a zip archive from a directory.
func createZipFromDir(srcDir, zipPath string) error {
	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	return filepath.WalkDir(srcDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip the zip file itself
		if path == zipPath {
			return nil
		}

		relPath, err := filepath.Rel(srcDir, path)
		if err != nil {
			return err
		}

		// Skip root
		if relPath == "." {
			return nil
		}

		// Normalize to forward slashes for zip
		relPath = NormalizePath(relPath)

		if d.IsDir() {
			// Add directory entry
			_, err := zipWriter.Create(relPath + "/")
			return err
		}

		// Add file
		info, err := d.Info()
		if err != nil {
			return err
		}

		header, err := zip.FileInfoHeader(info)
		if err != nil {
			return err
		}
		header.Name = relPath
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			return err
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(writer, file)
		if closeErr := file.Close(); closeErr != nil && copyErr == nil {
			return closeErr
		}
		return copyErr
	})
}

// copyFileForExport copies a file for export (with fsync for durability).
func copyFileForExport(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	tmpPath := dst + ".tmp"
	dstFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		os.Remove(tmpPath)
		return err
	}

	if err := dstFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	return os.Rename(tmpPath, dst)
}

// contains checks if a slice contains a string.
func contains(slice []string, s string) bool {
	for _, item := range slice {
		if strings.EqualFold(item, s) {
			return true
		}
	}
	return false
}

// matchesAny checks if a string matches any of the patterns.
func matchesAny(s string, patterns []string) bool {
	for _, p := range patterns {
		// Simple prefix/suffix/contains matching
		if strings.Contains(strings.ToLower(s), strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// FormatSize returns a human-readable size string.
func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}
