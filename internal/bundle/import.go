package bundle

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
)

// ImportMode defines how to handle conflicts during import.
type ImportMode string

const (
	// ImportModeSmart compares token freshness for conflicts.
	// New profiles are added, existing profiles keep the fresher token.
	ImportModeSmart ImportMode = "smart"

	// ImportModeMerge adds new profiles but skips existing ones.
	ImportModeMerge ImportMode = "merge"

	// ImportModeReplace overwrites existing profiles from the bundle.
	ImportModeReplace ImportMode = "replace"
)

// ImportOptions configures the import operation.
type ImportOptions struct {
	// Mode determines how to handle conflicts.
	Mode ImportMode

	// Password is the decryption password for encrypted bundles.
	Password string

	// DryRun shows what would be imported without making changes.
	DryRun bool

	// SkipConfig excludes configuration file from import.
	SkipConfig bool

	// SkipProjects excludes project associations from import.
	SkipProjects bool

	// SkipHealth excludes health metadata from import.
	SkipHealth bool

	// SkipDatabase excludes activity database from import.
	SkipDatabase bool

	// SkipSync excludes sync configuration from import.
	SkipSync bool

	// ProviderFilter limits import to specific providers (empty = all).
	ProviderFilter []string

	// ProfileFilter limits import to specific profile patterns (empty = all).
	ProfileFilter []string

	// Force skips confirmation prompts.
	Force bool

	// VaultPath is the local vault path to import into.
	VaultPath string

	// ConfigPath is the local config path.
	ConfigPath string

	// ProjectsPath is the local projects path.
	ProjectsPath string

	// HealthPath is the local health data path.
	HealthPath string

	// DatabasePath is the local database path.
	DatabasePath string

	// SyncPath is the local sync configuration path.
	SyncPath string
}

// DefaultImportOptions returns sensible defaults for import.
func DefaultImportOptions() *ImportOptions {
	return &ImportOptions{
		Mode: ImportModeSmart,
	}
}

// ImportResult contains the results of an import operation.
type ImportResult struct {
	// Manifest is the bundle manifest.
	Manifest *ManifestV1

	// Encrypted indicates if the bundle was encrypted.
	Encrypted bool

	// VerificationResult contains checksum verification results.
	VerificationResult *VerificationResult

	// ProfileActions lists what happened to each profile.
	ProfileActions []ProfileAction

	// OptionalActions lists what happened to optional files.
	OptionalActions []OptionalAction

	// Summary statistics
	NewProfiles     int
	UpdatedProfiles int
	SkippedProfiles int
	Errors          []string
}

// ProfileAction describes what happened to a single profile during import.
type ProfileAction struct {
	Provider string
	Profile  string
	Action   string // "add", "update", "skip", "error"
	Reason   string
	LocalExpiry  *time.Time
	BundleExpiry *time.Time
}

// OptionalAction describes what happened to an optional file during import.
type OptionalAction struct {
	Name    string // "config", "projects", "health", "database", "sync"
	Action  string // "import", "merge", "skip", "error"
	Reason  string
	Details string
}

// VaultImporter handles importing vault contents from a bundle.
type VaultImporter struct {
	// BundlePath is the path to the bundle file.
	BundlePath string
}

// Import restores vault from a bundle with the given options.
func (i *VaultImporter) Import(opts *ImportOptions) (*ImportResult, error) {
	if opts == nil {
		opts = DefaultImportOptions()
	}

	result := &ImportResult{
		ProfileActions:  make([]ProfileAction, 0),
		OptionalActions: make([]OptionalAction, 0),
		Errors:          make([]string, 0),
	}

	// Check bundle exists
	if _, err := os.Stat(i.BundlePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("bundle not found: %s", i.BundlePath)
	}

	// Check if encrypted
	encrypted, err := IsEncrypted(i.BundlePath)
	if err != nil {
		return nil, fmt.Errorf("check encryption: %w", err)
	}
	result.Encrypted = encrypted

	if encrypted && opts.Password == "" {
		return nil, fmt.Errorf("encrypted bundle requires password")
	}

	// Extract to temp directory
	tempDir, err := os.MkdirTemp("", "caam-import-*")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	// Extract bundle
	if encrypted {
		if err := i.extractEncryptedBundle(tempDir, opts.Password); err != nil {
			return nil, fmt.Errorf("extract encrypted bundle: %w", err)
		}
	} else {
		if err := i.extractBundle(tempDir); err != nil {
			return nil, fmt.Errorf("extract bundle: %w", err)
		}
	}

	// Load and validate manifest
	manifest, err := LoadManifest(tempDir)
	if err != nil {
		return nil, fmt.Errorf("load manifest: %w", err)
	}
	result.Manifest = manifest

	// Check version compatibility
	if err := IsCompatibleVersion(manifest); err != nil {
		return nil, fmt.Errorf("version incompatible: %w", err)
	}

	// Verify checksums
	verifyResult, err := VerifyChecksums(tempDir, manifest)
	if err != nil {
		return nil, fmt.Errorf("verify checksums: %w", err)
	}
	result.VerificationResult = verifyResult

	if !verifyResult.Valid && !opts.Force {
		return result, fmt.Errorf("checksum verification failed: %s", verifyResult.Summary())
	}

	// If dry run, determine what would happen without doing it
	if opts.DryRun {
		i.previewImport(tempDir, manifest, opts, result)
		return result, nil
	}

	// Import vault profiles
	if err := i.importVault(tempDir, manifest, opts, result); err != nil {
		return result, fmt.Errorf("import vault: %w", err)
	}

	// Import optional files
	i.importOptionalFiles(tempDir, manifest, opts, result)

	return result, nil
}

// extractBundle extracts a regular (unencrypted) zip bundle.
func (i *VaultImporter) extractBundle(destDir string) error {
	r, err := zip.OpenReader(i.BundlePath)
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if err := extractZipFile(f, destDir); err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
	}

	return nil
}

// extractEncryptedBundle decrypts and extracts an encrypted bundle.
func (i *VaultImporter) extractEncryptedBundle(destDir, password string) error {
	// Read encrypted data
	ciphertext, err := os.ReadFile(i.BundlePath)
	if err != nil {
		return fmt.Errorf("read encrypted bundle: %w", err)
	}

	// Load encryption metadata
	metaPath := i.BundlePath + ".meta"
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		return fmt.Errorf("read encryption metadata: %w", err)
	}

	var meta EncryptionMetadata
	if err := json.Unmarshal(metaData, &meta); err != nil {
		return fmt.Errorf("parse encryption metadata: %w", err)
	}

	// Decrypt
	plainData, err := DecryptBundle(ciphertext, &meta, password)
	if err != nil {
		return fmt.Errorf("decrypt: %w", err)
	}

	// Create zip reader from memory
	r, err := zip.NewReader(bytes.NewReader(plainData), int64(len(plainData)))
	if err != nil {
		return fmt.Errorf("open decrypted zip: %w", err)
	}

	for _, f := range r.File {
		if err := extractZipFile(f, destDir); err != nil {
			return fmt.Errorf("extract %s: %w", f.Name, err)
		}
	}

	return nil
}

// extractZipFile extracts a single file from a zip archive.
func extractZipFile(f *zip.File, destDir string) error {
	// Sanitize path to prevent directory traversal (Zip Slip attack)
	// First, clean the zip entry name to normalize any path components
	cleanName := filepath.Clean(DenormalizePath(f.Name))

	// Reject any path that starts with .. or is absolute
	if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
		return fmt.Errorf("invalid file path (path traversal attempt): %s", f.Name)
	}

	// Construct the target path
	path := filepath.Join(destDir, cleanName)

	// Double-check: use filepath.Rel to verify the path is within destDir
	// This catches edge cases like symlinks or unusual path normalization
	relPath, err := filepath.Rel(destDir, path)
	if err != nil || strings.HasPrefix(relPath, "..") || filepath.IsAbs(relPath) {
		return fmt.Errorf("invalid file path (escapes destination): %s", f.Name)
	}

	// Final prefix check after all cleaning
	cleanDest := filepath.Clean(destDir) + string(filepath.Separator)
	cleanPath := filepath.Clean(path)
	if !strings.HasPrefix(cleanPath, cleanDest) && cleanPath != filepath.Clean(destDir) {
		return fmt.Errorf("invalid file path: %s", f.Name)
	}

	if f.FileInfo().IsDir() {
		return os.MkdirAll(path, 0700)
	}

	// Check uncompressed size to prevent zip bombs (100MB limit per file)
	const maxExtractedFileSize = 100 * 1024 * 1024
	if f.UncompressedSize64 > maxExtractedFileSize {
		return fmt.Errorf("file %s too large: %d bytes (max %d)", f.Name, f.UncompressedSize64, maxExtractedFileSize)
	}

	// Create parent directories
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}

	// Open source
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	// Create destination
	dest, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer dest.Close()

	// Use LimitReader as defense in depth against zip bombs with fake size headers
	limited := io.LimitReader(rc, maxExtractedFileSize+1)
	written, err := io.Copy(dest, limited)
	if err != nil {
		return err
	}
	if written > maxExtractedFileSize {
		return fmt.Errorf("file %s exceeded size limit during extraction", f.Name)
	}
	return nil
}

// previewImport determines what would happen during import without making changes.
func (i *VaultImporter) previewImport(bundleDir string, manifest *ManifestV1, opts *ImportOptions, result *ImportResult) {
	// Preview vault profiles
	for provider, profiles := range manifest.Contents.Vault.Profiles {
		// Check provider filter
		if len(opts.ProviderFilter) > 0 && !containsIgnoreCase(opts.ProviderFilter, provider) {
			continue
		}

		for _, profile := range profiles {
			// Check profile filter
			if len(opts.ProfileFilter) > 0 && !matchesAnyPattern(profile, opts.ProfileFilter) {
				continue
			}

			action := i.determineProfileAction(bundleDir, opts, provider, profile)
			result.ProfileActions = append(result.ProfileActions, action)

			switch action.Action {
			case "add":
				result.NewProfiles++
			case "update":
				result.UpdatedProfiles++
			case "skip":
				result.SkippedProfiles++
			}
		}
	}

	// Preview optional files
	i.previewOptionalFiles(manifest, opts, result)
}

// determineProfileAction determines what action to take for a profile.
func (i *VaultImporter) determineProfileAction(bundleDir string, opts *ImportOptions, provider, profile string) ProfileAction {
	action := ProfileAction{
		Provider: provider,
		Profile:  profile,
	}

	// Check if profile exists locally
	localProfilePath := filepath.Join(opts.VaultPath, provider, profile)
	localExists := directoryExists(localProfilePath)

	if !localExists {
		action.Action = "add"
		action.Reason = "new profile"
		return action
	}

	// Profile exists locally - determine action based on mode
	switch opts.Mode {
	case ImportModeMerge:
		action.Action = "skip"
		action.Reason = "profile exists (merge mode)"
		return action

	case ImportModeReplace:
		action.Action = "update"
		action.Reason = "overwriting (replace mode)"
		return action

	case ImportModeSmart:
		// Compare freshness
		bundleProfilePath := filepath.Join(bundleDir, "vault", provider, profile)
		localFresh, bundleFresh := i.compareFreshness(provider, profile, localProfilePath, bundleProfilePath)

		if localFresh != nil {
			action.LocalExpiry = &localFresh.ExpiresAt
		}
		if bundleFresh != nil {
			action.BundleExpiry = &bundleFresh.ExpiresAt
		}

		if bundleFresh == nil {
			action.Action = "skip"
			action.Reason = "cannot determine bundle freshness"
			return action
		}

		if localFresh == nil {
			action.Action = "update"
			action.Reason = "cannot determine local freshness, importing bundle"
			return action
		}

		if sync.CompareFreshness(bundleFresh, localFresh) {
			action.Action = "update"
			action.Reason = fmt.Sprintf("bundle token fresher (expires %s vs %s)",
				bundleFresh.ExpiresAt.Format("2006-01-02 15:04"),
				localFresh.ExpiresAt.Format("2006-01-02 15:04"))
		} else {
			action.Action = "skip"
			action.Reason = "local token fresher or equal"
		}
		return action

	default:
		action.Action = "skip"
		action.Reason = "unknown import mode"
		return action
	}
}

// compareFreshness extracts freshness info from local and bundle profiles.
func (i *VaultImporter) compareFreshness(provider, profile, localPath, bundlePath string) (*sync.TokenFreshness, *sync.TokenFreshness) {
	// Get auth files for this provider
	var authFileNames []string
	switch provider {
	case "claude":
		authFileNames = []string{".claude.json", "auth.json"}
	case "codex":
		authFileNames = []string{"auth.json"}
	case "gemini":
		authFileNames = []string{"settings.json", "oauth_creds.json", "oauth_credentials.json"}
	default:
		return nil, nil
	}

	// Extract local freshness
	localFiles := make(map[string][]byte)
	for _, name := range authFileNames {
		path := filepath.Join(localPath, name)
		if data, err := os.ReadFile(path); err == nil {
			localFiles[path] = data
		}
	}
	localFresh, _ := sync.ExtractFreshnessFromBytes(provider, profile, localFiles)

	// Extract bundle freshness
	bundleFiles := make(map[string][]byte)
	for _, name := range authFileNames {
		path := filepath.Join(bundlePath, name)
		if data, err := os.ReadFile(path); err == nil {
			bundleFiles[path] = data
		}
	}
	bundleFresh, _ := sync.ExtractFreshnessFromBytes(provider, profile, bundleFiles)

	return localFresh, bundleFresh
}

// previewOptionalFiles determines what would happen to optional files.
func (i *VaultImporter) previewOptionalFiles(manifest *ManifestV1, opts *ImportOptions, result *ImportResult) {
	// Config
	if manifest.Contents.Config.Included && !opts.SkipConfig {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "config",
			Action: "import",
			Reason: "will be imported",
		})
	} else if opts.SkipConfig {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "config",
			Action: "skip",
			Reason: "user opted out",
		})
	}

	// Projects
	if manifest.Contents.Projects.Included && !opts.SkipProjects {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:    "projects",
			Action:  "merge",
			Reason:  "will be merged with local",
			Details: fmt.Sprintf("%d associations", manifest.Contents.Projects.Count),
		})
	} else if opts.SkipProjects {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "projects",
			Action: "skip",
			Reason: "user opted out",
		})
	}

	// Health
	if manifest.Contents.Health.Included && !opts.SkipHealth {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "health",
			Action: "import",
			Reason: "will be imported",
		})
	} else if opts.SkipHealth {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "health",
			Action: "skip",
			Reason: "user opted out",
		})
	}

	// Database
	if manifest.Contents.Database.Included && !opts.SkipDatabase {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "database",
			Action: "import",
			Reason: "will be imported",
		})
	} else if opts.SkipDatabase || !manifest.Contents.Database.Included {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "database",
			Action: "skip",
			Reason: func() string {
				if !manifest.Contents.Database.Included {
					return "not in bundle"
				}
				return "user opted out"
			}(),
		})
	}

	// Sync
	if manifest.Contents.SyncConfig.Included && !opts.SkipSync {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "sync",
			Action: "merge",
			Reason: "will be merged with local",
		})
	} else if opts.SkipSync {
		result.OptionalActions = append(result.OptionalActions, OptionalAction{
			Name:   "sync",
			Action: "skip",
			Reason: "user opted out",
		})
	}
}

// importVault imports vault profiles from the bundle.
func (i *VaultImporter) importVault(bundleDir string, manifest *ManifestV1, opts *ImportOptions, result *ImportResult) error {
	for provider, profiles := range manifest.Contents.Vault.Profiles {
		// Check provider filter
		if len(opts.ProviderFilter) > 0 && !containsIgnoreCase(opts.ProviderFilter, provider) {
			continue
		}

		for _, profile := range profiles {
			// Check profile filter
			if len(opts.ProfileFilter) > 0 && !matchesAnyPattern(profile, opts.ProfileFilter) {
				continue
			}

			action := i.determineProfileAction(bundleDir, opts, provider, profile)
			result.ProfileActions = append(result.ProfileActions, action)

			if action.Action == "skip" {
				result.SkippedProfiles++
				continue
			}

			// Import the profile
			bundleProfilePath := filepath.Join(bundleDir, "vault", provider, profile)
			localProfilePath := filepath.Join(opts.VaultPath, provider, profile)

			if err := copyProfileDirectory(bundleProfilePath, localProfilePath); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s/%s: %v", provider, profile, err))
				continue
			}

			switch action.Action {
			case "add":
				result.NewProfiles++
			case "update":
				result.UpdatedProfiles++
			}
		}
	}

	return nil
}

// copyProfileDirectory copies a profile directory from bundle to vault atomically.
// Uses a temporary directory to ensure the original is preserved if the copy fails.
func copyProfileDirectory(src, dst string) error {
	// Ensure parent directory exists
	parentDir := filepath.Dir(dst)
	if err := os.MkdirAll(parentDir, 0700); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	// Create a temporary directory in the same parent to ensure atomic rename works
	tmpDir, err := os.MkdirTemp(parentDir, ".caam_import_*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}

	// Clean up temp dir on failure
	success := false
	defer func() {
		if !success {
			os.RemoveAll(tmpDir)
		}
	}()

	// Copy to temporary directory
	if err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(tmpDir, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0700)
		}

		return copyFile(path, dstPath)
	}); err != nil {
		return fmt.Errorf("copy files: %w", err)
	}

	// Check if destination exists
	_, statErr := os.Stat(dst)
	dstExists := statErr == nil

	// If destination exists, rename it to backup first
	backupPath := dst + ".bak"
	if dstExists {
		// Remove any existing backup
		os.RemoveAll(backupPath)
		if err := os.Rename(dst, backupPath); err != nil {
			return fmt.Errorf("backup existing: %w", err)
		}
	}

	// Rename temp directory to destination (atomic on same filesystem)
	if err := os.Rename(tmpDir, dst); err != nil {
		// Restore backup if rename failed
		if dstExists {
			os.Rename(backupPath, dst)
		}
		return fmt.Errorf("rename to destination: %w", err)
	}

	// Success - remove backup and mark success to prevent temp cleanup
	success = true
	if dstExists {
		os.RemoveAll(backupPath)
	}

	return nil
}

// copyFile copies a single file.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		return err
	}

	return dstFile.Sync()
}

// importOptionalFiles imports optional files from the bundle.
func (i *VaultImporter) importOptionalFiles(bundleDir string, manifest *ManifestV1, opts *ImportOptions, result *ImportResult) {
	// Config
	if manifest.Contents.Config.Included && !opts.SkipConfig && opts.ConfigPath != "" {
		srcPath, err := ValidateManifestPath(bundleDir, manifest.Contents.Config.Path)
		if err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "config",
				Action: "error",
				Reason: fmt.Sprintf("invalid path: %v", err),
			})
		} else if err := copyFile(srcPath, opts.ConfigPath); err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "config",
				Action: "error",
				Reason: err.Error(),
			})
		} else {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "config",
				Action: "import",
				Reason: "imported successfully",
			})
		}
	}

	// Projects (merge)
	if manifest.Contents.Projects.Included && !opts.SkipProjects && opts.ProjectsPath != "" {
		srcPath, err := ValidateManifestPath(bundleDir, manifest.Contents.Projects.Path)
		if err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "projects",
				Action: "error",
				Reason: fmt.Sprintf("invalid path: %v", err),
			})
		} else if err := mergeJSONFile(srcPath, opts.ProjectsPath); err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "projects",
				Action: "error",
				Reason: err.Error(),
			})
		} else {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "projects",
				Action: "merge",
				Reason: "merged successfully",
			})
		}
	}

	// Health
	if manifest.Contents.Health.Included && !opts.SkipHealth && opts.HealthPath != "" {
		srcPath, err := ValidateManifestPath(bundleDir, manifest.Contents.Health.Path)
		if err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "health",
				Action: "error",
				Reason: fmt.Sprintf("invalid path: %v", err),
			})
		} else if info, err := os.Stat(srcPath); err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "health",
				Action: "error",
				Reason: err.Error(),
			})
		} else if info.IsDir() {
			destLooksLikeFile := strings.EqualFold(filepath.Ext(opts.HealthPath), ".json")
			if destLooksLikeFile {
				expected := filepath.Join(srcPath, "health.json")
				if _, err := os.Stat(expected); err != nil {
					result.OptionalActions = append(result.OptionalActions, OptionalAction{
						Name:   "health",
						Action: "error",
						Reason: fmt.Sprintf("expected health.json in bundle health directory: %v", err),
					})
				} else if err := copyFile(expected, opts.HealthPath); err != nil {
					result.OptionalActions = append(result.OptionalActions, OptionalAction{
						Name:   "health",
						Action: "error",
						Reason: err.Error(),
					})
				} else {
					result.OptionalActions = append(result.OptionalActions, OptionalAction{
						Name:   "health",
						Action: "import",
						Reason: "imported successfully",
					})
				}
			} else if err := copyDirectory(srcPath, opts.HealthPath); err != nil {
				result.OptionalActions = append(result.OptionalActions, OptionalAction{
					Name:   "health",
					Action: "error",
					Reason: err.Error(),
				})
			} else {
				result.OptionalActions = append(result.OptionalActions, OptionalAction{
					Name:   "health",
					Action: "import",
					Reason: "imported successfully",
				})
			}
		} else if err := copyFile(srcPath, opts.HealthPath); err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "health",
				Action: "error",
				Reason: err.Error(),
			})
		} else {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "health",
				Action: "import",
				Reason: "imported successfully",
			})
		}
	}

	// Database
	if manifest.Contents.Database.Included && !opts.SkipDatabase && opts.DatabasePath != "" {
		srcPath, err := ValidateManifestPath(bundleDir, manifest.Contents.Database.Path)
		if err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "database",
				Action: "error",
				Reason: fmt.Sprintf("invalid path: %v", err),
			})
		} else if err := copyFile(srcPath, opts.DatabasePath); err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "database",
				Action: "error",
				Reason: err.Error(),
			})
		} else {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "database",
				Action: "import",
				Reason: "imported successfully",
			})
		}
	}

	// Sync config (merge)
	if manifest.Contents.SyncConfig.Included && !opts.SkipSync && opts.SyncPath != "" {
		srcPath, err := ValidateManifestPath(bundleDir, manifest.Contents.SyncConfig.Path)
		if err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "sync",
				Action: "error",
				Reason: fmt.Sprintf("invalid path: %v", err),
			})
		} else if err := copyDirectory(srcPath, opts.SyncPath); err != nil {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "sync",
				Action: "error",
				Reason: err.Error(),
			})
		} else {
			result.OptionalActions = append(result.OptionalActions, OptionalAction{
				Name:   "sync",
				Action: "import",
				Reason: "imported successfully",
			})
		}
	}
}

// mergeJSONFile merges a JSON file from bundle into an existing local file.
// Uses atomic write (temp + fsync + rename) to prevent corruption.
func mergeJSONFile(srcPath, dstPath string) error {
	// Read source
	srcData, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read source: %w", err)
	}

	var srcMap map[string]interface{}
	if err := json.Unmarshal(srcData, &srcMap); err != nil {
		return fmt.Errorf("parse source: %w", err)
	}

	// Read destination (if exists)
	dstMap := make(map[string]interface{})
	if dstData, err := os.ReadFile(dstPath); err == nil {
		if err := json.Unmarshal(dstData, &dstMap); err != nil {
			// If destination is corrupt, just use source
			dstMap = make(map[string]interface{})
		}
	}

	// Merge: source values overwrite destination
	for k, v := range srcMap {
		dstMap[k] = v
	}

	// Write merged result
	merged, err := json.MarshalIndent(dstMap, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal merged: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(dstPath), 0700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// Atomic write: write to temp file, fsync, then rename
	tmpPath := dstPath + ".tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := tmpFile.Write(merged); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, dstPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// copyDirectory copies an entire directory.
func copyDirectory(src, dst string) error {
	if err := os.MkdirAll(dst, 0700); err != nil {
		return err
	}

	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}

		dstPath := filepath.Join(dst, relPath)

		if d.IsDir() {
			return os.MkdirAll(dstPath, 0700)
		}

		return copyFile(path, dstPath)
	})
}

// Helper functions

func containsIgnoreCase(slice []string, s string) bool {
	s = strings.ToLower(s)
	for _, item := range slice {
		if strings.ToLower(item) == s {
			return true
		}
	}
	return false
}

func matchesAnyPattern(s string, patterns []string) bool {
	s = strings.ToLower(s)
	for _, p := range patterns {
		if strings.Contains(s, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
