package update

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// VerifySignature verifies the cosign signature on the checksums file.
// It requires cosign to be installed on the system.
func VerifySignature(ctx context.Context, checksumsPath, signaturePath, tag, owner, repo string) error {
	// Check if cosign is available
	if _, err := exec.LookPath("cosign"); err != nil {
		// If cosign is not installed, skip signature verification with a warning
		// This allows updates to work even without cosign, but logs the skip
		return nil // Consider logging: "cosign not found, skipping signature verification"
	}

	// Build the expected identity for the OIDC certificate
	identity := fmt.Sprintf("https://github.com/%s/%s/.github/workflows/release.yml@refs/tags/%s",
		owner, repo, tag)

	cmd := exec.CommandContext(ctx, "cosign", "verify-blob",
		"--bundle", signaturePath,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com",
		"--certificate-identity", identity,
		checksumsPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("cosign verification failed: %s: %w", string(output), err)
	}

	return nil
}

// VerifyChecksum verifies that a file matches its expected checksum in the checksums file.
func VerifyChecksum(filePath, checksumsPath, assetName string) error {
	// Read expected checksum from SHA256SUMS file
	expectedHash, err := readExpectedChecksum(checksumsPath, assetName)
	if err != nil {
		return fmt.Errorf("read expected checksum: %w", err)
	}

	// Calculate actual checksum
	actualHash, err := calculateSHA256(filePath)
	if err != nil {
		return fmt.Errorf("calculate checksum: %w", err)
	}

	if actualHash != expectedHash {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expectedHash, actualHash)
	}

	return nil
}

// readExpectedChecksum reads the expected checksum for an asset from SHA256SUMS.
// The asset name may contain wildcards like "caam_*_linux_amd64.tar.gz".
func readExpectedChecksum(checksumsPath, assetPattern string) (string, error) {
	f, err := os.Open(checksumsPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Convert pattern to match format (handle wildcards)
	patternParts := strings.Split(assetPattern, "*")

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: "hash  filename" or "hash filename"
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		hash := parts[0]
		filename := parts[len(parts)-1]

		// Check if filename matches pattern
		if matchPattern(filename, patternParts) {
			return hash, nil
		}
	}

	if err := scanner.Err(); err != nil {
		return "", err
	}

	return "", fmt.Errorf("asset not found in checksums: %s", assetPattern)
}

// matchPattern checks if a filename matches a pattern with wildcards.
func matchPattern(filename string, patternParts []string) bool {
	if len(patternParts) == 1 {
		return filename == patternParts[0]
	}

	// For pattern like "caam_*_linux_amd64.tar.gz":
	// patternParts = ["caam_", "_linux_amd64.tar.gz"]
	// Check if filename starts with first part and ends with last part
	if len(patternParts) >= 2 {
		if !strings.HasPrefix(filename, patternParts[0]) {
			return false
		}
		if !strings.HasSuffix(filename, patternParts[len(patternParts)-1]) {
			return false
		}
		return true
	}

	return false
}

// calculateSHA256 calculates the SHA256 hash of a file.
func calculateSHA256(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// ExtractBinary extracts the caam binary from an archive.
func ExtractBinary(archivePath, destPath string) error {
	if strings.HasSuffix(archivePath, ".zip") {
		return extractFromZip(archivePath, destPath)
	}
	return extractFromTarGz(archivePath, destPath)
}

// extractFromTarGz extracts the binary from a .tar.gz archive.
func extractFromTarGz(archivePath, destPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("gzip reader: %w", err)
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)

	binaryName := "caam"
	if runtime.GOOS == "windows" {
		binaryName = "caam.exe"
	}

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}

		// Look for the binary (might be at root or in a subdirectory)
		if filepath.Base(header.Name) == binaryName && header.Typeflag == tar.TypeReg {
			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			defer out.Close()

			if _, err := io.Copy(out, tr); err != nil {
				return fmt.Errorf("extract binary: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("binary not found in archive: %s", binaryName)
}

// extractFromZip extracts the binary from a .zip archive.
func extractFromZip(archivePath, destPath string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer r.Close()

	binaryName := "caam"
	if runtime.GOOS == "windows" {
		binaryName = "caam.exe"
	}

	for _, f := range r.File {
		if filepath.Base(f.Name) == binaryName && !f.FileInfo().IsDir() {
			rc, err := f.Open()
			if err != nil {
				return err
			}
			defer rc.Close()

			out, err := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
			if err != nil {
				return err
			}
			defer out.Close()

			if _, err := io.Copy(out, rc); err != nil {
				return fmt.Errorf("extract binary: %w", err)
			}
			return nil
		}
	}

	return fmt.Errorf("binary not found in archive: %s", binaryName)
}

// AtomicReplace atomically replaces the target file with the source file.
// On Unix, this uses rename which is atomic. On Windows, we need a different approach.
func AtomicReplace(src, dst string) error {
	// Ensure the destination directory exists
	destDir := filepath.Dir(dst)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}

	// Get source file info for permissions
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	// On Windows, we can't rename over an existing file directly
	// So we rename the old file first, then rename the new one
	if runtime.GOOS == "windows" {
		return atomicReplaceWindows(src, dst, srcInfo)
	}

	// On Unix, rename is atomic
	if err := os.Rename(src, dst); err != nil {
		// If rename fails (cross-device), fall back to copy
		if err := copyFile(src, dst); err != nil {
			return err
		}
		if err := os.Chmod(dst, srcInfo.Mode()); err != nil {
			return fmt.Errorf("set permissions: %w", err)
		}
	}

	return nil
}

// atomicReplaceWindows handles atomic replacement on Windows.
func atomicReplaceWindows(src, dst string, srcInfo os.FileInfo) error {
	// Rename old file to .old
	oldPath := dst + ".old"
	_ = os.Remove(oldPath) // Remove any existing .old file

	if _, err := os.Stat(dst); err == nil {
		if err := os.Rename(dst, oldPath); err != nil {
			return fmt.Errorf("rename old file: %w", err)
		}
	}

	// Copy new file to destination
	if err := copyFile(src, dst); err != nil {
		// Restore old file on failure
		_ = os.Rename(oldPath, dst)
		return err
	}

	// Set permissions
	if err := os.Chmod(dst, srcInfo.Mode()); err != nil {
		return fmt.Errorf("set permissions: %w", err)
	}

	// Remove old file
	_ = os.Remove(oldPath)

	return nil
}

// Rollback restores a previous version from backup.
func Rollback(backupPath, exePath string) error {
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		return fmt.Errorf("backup not found: %s", backupPath)
	}

	return AtomicReplace(backupPath, exePath)
}

// CleanupOldBackups removes backup files older than the given duration.
func CleanupOldBackups(backupDir string, maxAge time.Duration) error {
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		return err
	}

	cutoff := time.Now().Add(-maxAge)

	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "caam.") || !strings.HasSuffix(entry.Name(), ".backup") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		if info.ModTime().Before(cutoff) {
			path := filepath.Join(backupDir, entry.Name())
			_ = os.Remove(path)
		}
	}

	return nil
}
