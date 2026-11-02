package bundle

import (
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// ChecksumAlgorithm represents a supported checksum algorithm.
type ChecksumAlgorithm string

const (
	// AlgorithmSHA256 is the SHA-256 hash algorithm.
	AlgorithmSHA256 ChecksumAlgorithm = "sha256"

	// AlgorithmSHA512 is the SHA-512 hash algorithm.
	AlgorithmSHA512 ChecksumAlgorithm = "sha512"
)

// DefaultAlgorithm is the default checksum algorithm.
const DefaultAlgorithm = AlgorithmSHA256

// ComputeFileChecksum computes the checksum of a single file.
func ComputeFileChecksum(path string, algorithm ChecksumAlgorithm) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	h, err := newHasher(algorithm)
	if err != nil {
		return "", err
	}

	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("compute hash: %w", err)
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ComputeDataChecksum computes the checksum of a byte slice.
func ComputeDataChecksum(data []byte, algorithm ChecksumAlgorithm) (string, error) {
	h, err := newHasher(algorithm)
	if err != nil {
		return "", err
	}

	h.Write(data)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// newHasher returns a new hash.Hash for the given algorithm.
func newHasher(algorithm ChecksumAlgorithm) (hash.Hash, error) {
	switch algorithm {
	case AlgorithmSHA256, "":
		return sha256.New(), nil
	case AlgorithmSHA512:
		return sha512.New(), nil
	default:
		return nil, fmt.Errorf("unsupported algorithm: %s", algorithm)
	}
}

// ComputeDirectoryChecksums computes checksums for all files in a directory.
// Returns a map of relative paths to their checksums.
func ComputeDirectoryChecksums(baseDir string, algorithm ChecksumAlgorithm) (map[string]string, error) {
	checksums := make(map[string]string)

	err := filepath.Walk(baseDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip directories
		if info.IsDir() {
			return nil
		}

		// Compute relative path
		relPath, err := filepath.Rel(baseDir, path)
		if err != nil {
			return fmt.Errorf("compute relative path: %w", err)
		}

		// Normalize path for cross-platform compatibility
		relPath = NormalizePath(relPath)

		// Skip the manifest file itself
		if relPath == ManifestFileName {
			return nil
		}

		// Compute checksum
		checksum, err := ComputeFileChecksum(path, algorithm)
		if err != nil {
			return fmt.Errorf("checksum %s: %w", relPath, err)
		}

		checksums[relPath] = checksum
		return nil
	})

	if err != nil {
		return nil, err
	}

	return checksums, nil
}

// VerifyChecksums verifies all checksums in a manifest against files in a directory.
// Returns a VerificationResult with details about any mismatches.
func VerifyChecksums(bundleDir string, manifest *ManifestV1) (*VerificationResult, error) {
	if manifest == nil {
		return nil, fmt.Errorf("manifest is nil")
	}

	result := &VerificationResult{
		Valid:    true,
		Verified: make([]string, 0),
		Missing:  make([]string, 0),
		Mismatch: make([]ChecksumMismatch, 0),
		Extra:    make([]string, 0),
	}

	algorithm := ChecksumAlgorithm(manifest.Checksums.Algorithm)
	if algorithm == "" {
		algorithm = DefaultAlgorithm
	}

	// Build set of expected files
	expectedFiles := make(map[string]string)
	for path, checksum := range manifest.Checksums.Files {
		expectedFiles[path] = checksum
	}

	// Walk the bundle directory
	foundFiles := make(map[string]bool)

	err := filepath.Walk(bundleDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(bundleDir, path)
		if err != nil {
			return err
		}

		relPath = NormalizePath(relPath)

		// Skip manifest
		if relPath == ManifestFileName {
			return nil
		}

		// Skip encryption marker file
		if relPath == EncryptionMarkerFile {
			return nil
		}

		foundFiles[relPath] = true

		expectedChecksum, exists := expectedFiles[relPath]
		if !exists {
			result.Valid = false
			result.Extra = append(result.Extra, relPath)
			return nil
		}

		// Compute actual checksum
		actualChecksum, err := ComputeFileChecksum(path, algorithm)
		if err != nil {
			return fmt.Errorf("checksum %s: %w", relPath, err)
		}

		if actualChecksum != expectedChecksum {
			result.Valid = false
			result.Mismatch = append(result.Mismatch, ChecksumMismatch{
				Path:     relPath,
				Expected: expectedChecksum,
				Actual:   actualChecksum,
			})
		} else {
			result.Verified = append(result.Verified, relPath)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Check for missing files
	for path := range expectedFiles {
		if !foundFiles[path] {
			result.Valid = false
			result.Missing = append(result.Missing, path)
		}
	}

	// Sort results for deterministic output
	sort.Strings(result.Verified)
	sort.Strings(result.Missing)
	sort.Strings(result.Extra)
	sort.Slice(result.Mismatch, func(i, j int) bool {
		return result.Mismatch[i].Path < result.Mismatch[j].Path
	})

	return result, nil
}

// VerificationResult contains the results of checksum verification.
type VerificationResult struct {
	// Valid is true if every payload file has a matching checksum and no
	// declared files are missing.
	Valid bool

	// Verified is the list of files that passed verification.
	Verified []string

	// Missing is the list of files expected but not found.
	Missing []string

	// Mismatch is the list of files with incorrect checksums.
	Mismatch []ChecksumMismatch

	// Extra is the list of unexpected files in the bundle.
	Extra []string
}

// ChecksumMismatch describes a file with an incorrect checksum.
type ChecksumMismatch struct {
	Path     string
	Expected string
	Actual   string
}

// Summary returns a human-readable summary of the verification result.
func (r *VerificationResult) Summary() string {
	if r.Valid {
		return fmt.Sprintf("Verified %d files, all checksums match", len(r.Verified))
	}

	parts := make([]string, 0, 3)
	if len(r.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("%d missing", len(r.Missing)))
	}
	if len(r.Mismatch) > 0 {
		parts = append(parts, fmt.Sprintf("%d corrupted", len(r.Mismatch)))
	}
	if len(r.Extra) > 0 {
		parts = append(parts, fmt.Sprintf("%d extra", len(r.Extra)))
	}

	return fmt.Sprintf("Verification failed: %s", joinWithComma(parts))
}

// joinWithComma joins strings with commas and "and" for the last item.
func joinWithComma(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	default:
		result := ""
		for i, p := range parts[:len(parts)-1] {
			if i > 0 {
				result += ", "
			}
			result += p
		}
		result += ", and " + parts[len(parts)-1]
		return result
	}
}
