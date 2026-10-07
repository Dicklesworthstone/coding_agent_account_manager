package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/version"
)

// ValidationError represents a bundle validation error.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("%s: %s", e.Field, e.Message)
	}
	return e.Message
}

// ValidateManifest checks that a manifest is well-formed.
// It returns an error if any required fields are missing or invalid.
func ValidateManifest(m *ManifestV1) error {
	if m == nil {
		return &ValidationError{Message: "manifest is nil"}
	}

	// Check schema version
	if m.SchemaVersion < 1 {
		return &ValidationError{
			Field:   "schema_version",
			Message: "must be >= 1",
		}
	}

	// Check caam version
	if m.CAAMVersion == "" {
		return &ValidationError{
			Field:   "caam_version",
			Message: "is required",
		}
	}

	// Check export timestamp
	if m.ExportTimestamp.IsZero() {
		return &ValidationError{
			Field:   "export_timestamp",
			Message: "is required",
		}
	}

	// Validate source info
	if err := validateSourceInfo(&m.Source); err != nil {
		return err
	}

	// Validate checksums
	if err := validateChecksumInfo(&m.Checksums); err != nil {
		return err
	}
	providers := make(map[string]bool)
	for provider, profiles := range m.Contents.Vault.Profiles {
		if err := validatePathSegment(provider); err != nil {
			return fmt.Errorf("invalid vault provider %q: %w", provider, err)
		}
		key := strings.ToLower(provider)
		if providers[key] {
			return fmt.Errorf("duplicate vault provider %q", provider)
		}
		providers[key] = true
		seen := make(map[string]bool)
		for _, profile := range profiles {
			if err := validatePathSegment(profile); err != nil {
				return fmt.Errorf("invalid vault profile %q: %w", profile, err)
			}
			key := strings.ToLower(profile)
			if seen[key] {
				return fmt.Errorf("duplicate vault profile %q for %s", profile, provider)
			}
			seen[key] = true
		}
	}
	for _, content := range []OptionalContent{m.Contents.Config, m.Contents.Projects, m.Contents.Health, m.Contents.Database, m.Contents.SyncConfig} {
		if content.Included {
			if name := strings.TrimSuffix(content.Path, "/"); strings.EqualFold(name, ManifestFileName) || strings.EqualFold(name, EncryptionMarkerFile) {
				return fmt.Errorf("reserved bundle metadata cannot be restored as content: %s", content.Path)
			}
			if err := validateRelativePath(content.Path); err != nil {
				return fmt.Errorf("invalid optional content path %q: %w", content.Path, err)
			}
		}
	}

	return nil
}

// validateSourceInfo validates the source information.
func validateSourceInfo(s *SourceInfo) error {
	if s.Hostname == "" {
		return &ValidationError{
			Field:   "source.hostname",
			Message: "is required",
		}
	}

	validPlatforms := map[string]bool{
		"darwin":  true,
		"linux":   true,
		"windows": true,
		"":        true, // Allow empty for legacy/unknown
	}
	if !validPlatforms[s.Platform] {
		return &ValidationError{
			Field:   "source.platform",
			Message: fmt.Sprintf("invalid platform %q", s.Platform),
		}
	}

	validArchs := map[string]bool{
		"amd64": true,
		"arm64": true,
		"386":   true,
		"arm":   true,
		"":      true, // Allow empty for legacy/unknown
	}
	if !validArchs[s.Arch] {
		return &ValidationError{
			Field:   "source.arch",
			Message: fmt.Sprintf("invalid architecture %q", s.Arch),
		}
	}

	return nil
}

// validateChecksumInfo validates the checksum information.
func validateChecksumInfo(c *ChecksumInfo) error {
	for path := range c.Files {
		if err := validateRelativePath(path); err != nil {
			return fmt.Errorf("invalid checksum path %q: %w", path, err)
		}
	}
	validAlgorithms := map[string]bool{
		"sha256": true,
		"sha512": true,
		"":       true, // Allow empty (no checksums)
	}
	if !validAlgorithms[c.Algorithm] {
		return &ValidationError{
			Field:   "checksums.algorithm",
			Message: fmt.Sprintf("unsupported algorithm %q", c.Algorithm),
		}
	}

	// Validate checksum format (hex string of correct length)
	if c.Algorithm != "" && len(c.Files) > 0 {
		expectedLen := 64 // sha256 produces 64 hex chars
		if c.Algorithm == "sha512" {
			expectedLen = 128
		}

		for path, hash := range c.Files {
			if len(hash) != expectedLen {
				return &ValidationError{
					Field:   fmt.Sprintf("checksums.files[%q]", path),
					Message: fmt.Sprintf("invalid hash length: expected %d, got %d", expectedLen, len(hash)),
				}
			}
			// Validate hex characters
			for _, r := range hash {
				if !isHexChar(r) {
					return &ValidationError{
						Field:   fmt.Sprintf("checksums.files[%q]", path),
						Message: "contains non-hex characters",
					}
				}
			}
		}
	}

	return nil
}

// isHexChar returns true if r is a valid hexadecimal character.
func isHexChar(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
}

// ValidateManifestPath checks that a path from the manifest is safe and stays
// within the bundle directory. Prevents path traversal attacks.
func ValidateManifestPath(basePath, relPath string) (string, error) {
	if err := validateRelativePath(relPath); err != nil {
		return "", err
	}
	relPath = strings.TrimSuffix(relPath, "/")

	// Join and clean the path
	fullPath := filepath.Join(basePath, filepath.FromSlash(relPath))

	// Verify the resolved path is within basePath
	cleanBase := filepath.Clean(basePath)
	cleanFull := filepath.Clean(fullPath)

	// Ensure the full path starts with the base path
	if !strings.HasPrefix(cleanFull, cleanBase+string(filepath.Separator)) &&
		cleanFull != cleanBase {
		return "", &ValidationError{
			Field:   relPath,
			Message: "path escapes bundle directory",
		}
	}

	// The lexical check alone does not detect a symlink inside the root.
	current := basePath
	for _, part := range strings.Split(relPath, "/") {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			break
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink in bundle path: %s", current)
		}
	}
	return fullPath, nil
}

// Bundle names must mean the same thing on Unix and Windows, regardless of
// which platform performs validation. In particular, reject drive names, ADS,
// device aliases, and either path separator before joining any manifest input.
func validatePathSegment(name string) error {
	if name == "" || name == "." || name == ".." || strings.TrimSpace(name) != name || strings.HasSuffix(name, ".") {
		return fmt.Errorf("expected a nonempty portable path segment")
	}
	for _, r := range name {
		if r < 32 || r == 127 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return fmt.Errorf("invalid character in path segment")
		}
	}
	stem := strings.ToUpper(strings.SplitN(name, ".", 2)[0])
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
		return fmt.Errorf("reserved device name")
	}
	if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
		suffix := strings.TrimPrefix(strings.TrimPrefix(stem, "COM"), "LPT")
		if len([]rune(suffix)) == 1 && strings.ContainsAny(suffix, "123456789¹²³") {
			return fmt.Errorf("reserved device name")
		}
	}
	return nil
}

func validateRelativePath(path string) error {
	for _, segment := range strings.Split(strings.TrimSuffix(path, "/"), "/") {
		if err := validatePathSegment(segment); err != nil {
			return fmt.Errorf("invalid relative path %q: %w", path, err)
		}
	}
	return nil
}

// IsCompatibleVersion checks if a manifest was created by a compatible caam version.
// Returns nil if compatible, or an error describing the incompatibility.
func IsCompatibleVersion(m *ManifestV1) error {
	if m == nil {
		return &ValidationError{Message: "manifest is nil"}
	}

	// Check schema version compatibility
	if m.SchemaVersion > CurrentSchemaVersion {
		return &ValidationError{
			Field: "schema_version",
			Message: fmt.Sprintf(
				"bundle uses schema v%d but this caam only supports up to v%d; please upgrade caam",
				m.SchemaVersion, CurrentSchemaVersion,
			),
		}
	}

	// Schema version 1 is always compatible with current implementation
	if m.SchemaVersion < 1 {
		return &ValidationError{
			Field:   "schema_version",
			Message: fmt.Sprintf("unknown schema version %d", m.SchemaVersion),
		}
	}

	// Parse and compare caam versions for warnings (not errors)
	// We allow importing from any caam version, but may warn about mismatches
	_ = version.Short() // Reference for future version comparison

	return nil
}

// ValidateEncryptionMetadata validates encryption metadata.
func ValidateEncryptionMetadata(e *EncryptionMetadata) error {
	if e == nil {
		return &ValidationError{Message: "encryption metadata is nil"}
	}

	if e.Version < 1 {
		return &ValidationError{
			Field:   "version",
			Message: "must be >= 1",
		}
	}

	if e.Algorithm != "aes-256-gcm" {
		return &ValidationError{
			Field:   "algorithm",
			Message: fmt.Sprintf("unsupported algorithm %q; only aes-256-gcm is supported", e.Algorithm),
		}
	}

	if e.KDF != "argon2id" {
		return &ValidationError{
			Field:   "kdf",
			Message: fmt.Sprintf("unsupported KDF %q; only argon2id is supported", e.KDF),
		}
	}

	if e.Salt == "" {
		return &ValidationError{
			Field:   "salt",
			Message: "is required",
		}
	}

	if e.Nonce == "" {
		return &ValidationError{
			Field:   "nonce",
			Message: "is required",
		}
	}

	if e.Argon2Params != nil {
		if err := validateArgon2Params(e.Argon2Params); err != nil {
			return err
		}
	}

	return nil
}

// validateArgon2Params validates Argon2 parameters.
func validateArgon2Params(p *Argon2Params) error {
	if p.Time < 1 {
		return &ValidationError{
			Field:   "argon2_params.time",
			Message: "must be >= 1",
		}
	}

	if p.Memory < 1024 { // Minimum 1 MiB
		return &ValidationError{
			Field:   "argon2_params.memory",
			Message: "must be >= 1024 (1 MiB)",
		}
	}

	if p.Threads < 1 {
		return &ValidationError{
			Field:   "argon2_params.threads",
			Message: "must be >= 1",
		}
	}

	if p.KeyLen < 16 || p.KeyLen > 64 {
		return &ValidationError{
			Field:   "argon2_params.key_len",
			Message: "must be between 16 and 64 bytes",
		}
	}

	return nil
}

// LoadManifest reads and validates a manifest from a bundle directory.
func LoadManifest(bundleDir string) (*ManifestV1, error) {
	manifestPath := filepath.Join(bundleDir, ManifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	var m ManifestV1
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	if err := ValidateManifest(&m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return &m, nil
}

// SaveManifest writes a manifest to a bundle directory.
func SaveManifest(bundleDir string, m *ManifestV1) error {
	if err := ValidateManifest(m); err != nil {
		return fmt.Errorf("manifest validation failed: %w", err)
	}

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal manifest: %w", err)
	}

	manifestPath := filepath.Join(bundleDir, ManifestFileName)

	// Ensure directory exists
	if err := os.MkdirAll(bundleDir, 0700); err != nil {
		return fmt.Errorf("create bundle directory: %w", err)
	}

	// Atomic write
	tmpPath := manifestPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp manifest file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp manifest file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp manifest file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp manifest file: %w", err)
	}

	if err := os.Rename(tmpPath, manifestPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp manifest file: %w", err)
	}

	return nil
}

// NormalizePath converts a path to use forward slashes for portability.
// This ensures bundles created on Windows work on Unix and vice versa.
func NormalizePath(path string) string {
	return strings.ReplaceAll(filepath.ToSlash(path), "\\", "/")
}

// DenormalizePath converts a normalized path to the OS-specific format.
func DenormalizePath(path string) string {
	return filepath.FromSlash(path)
}
