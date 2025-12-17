// Package authfile manages auth file backup/restore for instant account switching.
//
// The core insight: AI coding tools store OAuth tokens in specific files.
// Instead of logging in/out (slow, requires browser), we can:
//  1. Backup the auth file after logging in once
//  2. Label it with the account name
//  3. Restore it instantly when we need to switch
//
// This enables sub-second account switching for "all you can eat" subscriptions
// like GPT Pro, Claude Max, and Gemini Ultra when hitting usage limits.
package authfile

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// AuthFileSpec defines where a tool stores its auth credentials.
type AuthFileSpec struct {
	// Tool is the tool identifier (codex, claude, gemini).
	Tool string

	// Path is the absolute path to the auth file.
	Path string

	// Description is a human-readable description.
	Description string

	// Required indicates if this file must exist for auth to work.
	Required bool
}

// AuthFileSet is a collection of auth files that together represent
// a complete authentication state for a tool.
type AuthFileSet struct {
	Tool  string
	Files []AuthFileSpec
}

// CodexAuthFiles returns the auth files for Codex CLI.
// Codex stores auth in $CODEX_HOME/auth.json (default ~/.codex/auth.json).
func CodexAuthFiles() AuthFileSet {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		homeDir, _ := os.UserHomeDir()
		home = filepath.Join(homeDir, ".codex")
	}

	return AuthFileSet{
		Tool: "codex",
		Files: []AuthFileSpec{
			{
				Tool:        "codex",
				Path:        filepath.Join(home, "auth.json"),
				Description: "Codex CLI OAuth token (GPT Pro subscription)",
				Required:    true,
			},
		},
	}
}

// ClaudeAuthFiles returns the auth files for Claude Code.
// Claude Code stores auth in multiple locations:
//   - ~/.claude.json (main OAuth session state)
//   - ~/.config/claude-code/auth.json (auth credentials)
func ClaudeAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	// XDG config path
	xdgConfig := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfig == "" {
		xdgConfig = filepath.Join(homeDir, ".config")
	}

	return AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude.json"),
				Description: "Claude Code OAuth session state (Claude Max subscription)",
				Required:    true,
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(xdgConfig, "claude-code", "auth.json"),
				Description: "Claude Code auth credentials",
				Required:    false, // May not exist in all setups
			},
		},
	}
}

// GeminiAuthFiles returns the auth files for Gemini CLI.
// Gemini CLI stores Google OAuth tokens in ~/.gemini/ directory.
func GeminiAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	// Check for GEMINI_HOME override
	geminiHome := os.Getenv("GEMINI_HOME")
	if geminiHome == "" {
		geminiHome = filepath.Join(homeDir, ".gemini")
	}

	return AuthFileSet{
		Tool: "gemini",
		Files: []AuthFileSpec{
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, "settings.json"),
				Description: "Gemini CLI settings with Google OAuth state (Gemini Ultra subscription)",
				Required:    true,
			},
			// Additional auth files that may store tokens
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, "oauth_credentials.json"),
				Description: "Gemini CLI OAuth credentials cache",
				Required:    false,
			},
		},
	}
}

// Vault manages stored auth file backups.
type Vault struct {
	basePath string // ~/.local/share/accx/vault
}

// NewVault creates a new vault at the given path.
func NewVault(basePath string) *Vault {
	return &Vault{basePath: basePath}
}

// BasePath returns the on-disk path to the vault root directory.
func (v *Vault) BasePath() string {
	return v.basePath
}

// DefaultVaultPath returns the default vault location.
// Falls back to current directory if home directory cannot be determined.
func DefaultVaultPath() string {
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "caam", "vault")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory - unusual but handles edge cases
		return filepath.Join(".local", "share", "caam", "vault")
	}
	return filepath.Join(homeDir, ".local", "share", "caam", "vault")
}

// ProfilePath returns the path to a profile's backup directory.
// Structure: vault/<tool>/<profile>/
func (v *Vault) ProfilePath(tool, profile string) string {
	return filepath.Join(v.basePath, tool, profile)
}

// BackupPath returns the path where a specific auth file is backed up.
// Structure: vault/<tool>/<profile>/<filename>
func (v *Vault) BackupPath(tool, profile, filename string) string {
	return filepath.Join(v.ProfilePath(tool, profile), filename)
}

// Backup saves the current auth files to the vault.
func (v *Vault) Backup(fileSet AuthFileSet, profile string) error {
	profileDir := v.ProfilePath(fileSet.Tool, profile)

	// Create profile directory
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	backedUp := 0
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			if spec.Required {
				return fmt.Errorf("required auth file not found: %s", spec.Path)
			}
			continue // Skip optional files that don't exist
		}

		// Copy file to vault
		filename := filepath.Base(spec.Path)
		destPath := v.BackupPath(fileSet.Tool, profile, filename)

		if err := copyFile(spec.Path, destPath); err != nil {
			return fmt.Errorf("backup %s: %w", spec.Path, err)
		}
		backedUp++
	}

	if backedUp == 0 {
		return fmt.Errorf("no auth files found to backup for %s", fileSet.Tool)
	}

	// Write metadata
	metaPath := filepath.Join(profileDir, "meta.json")
	meta := fmt.Sprintf(`{"tool": %q, "profile": %q, "backed_up_at": %q, "files": %d}`,
		fileSet.Tool, profile, time.Now().Format(time.RFC3339), backedUp)
	if err := os.WriteFile(metaPath, []byte(meta), 0600); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	return nil
}

// Restore copies backed-up auth files to their original locations.
func (v *Vault) Restore(fileSet AuthFileSet, profile string) error {
	profileDir := v.ProfilePath(fileSet.Tool, profile)

	if _, err := os.Stat(profileDir); os.IsNotExist(err) {
		return fmt.Errorf("profile %s/%s not found in vault", fileSet.Tool, profile)
	}

	restored := 0
	for _, spec := range fileSet.Files {
		filename := filepath.Base(spec.Path)
		srcPath := v.BackupPath(fileSet.Tool, profile, filename)

		// Check if backup exists
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			if spec.Required {
				return fmt.Errorf("required backup not found: %s", srcPath)
			}
			continue // Skip optional files
		}

		// Ensure parent directory exists
		if err := os.MkdirAll(filepath.Dir(spec.Path), 0700); err != nil {
			return fmt.Errorf("create parent dir for %s: %w", spec.Path, err)
		}

		// Copy from vault to original location
		if err := copyFile(srcPath, spec.Path); err != nil {
			return fmt.Errorf("restore %s: %w", spec.Path, err)
		}
		restored++
	}

	if restored == 0 {
		return fmt.Errorf("no auth files restored for %s/%s", fileSet.Tool, profile)
	}

	return nil
}

// List returns all profiles stored for a tool.
func (v *Vault) List(tool string) ([]string, error) {
	toolDir := filepath.Join(v.basePath, tool)
	entries, err := os.ReadDir(toolDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var profiles []string
	for _, e := range entries {
		if e.IsDir() {
			profiles = append(profiles, e.Name())
		}
	}
	return profiles, nil
}

// ListAll returns all profiles for all tools.
func (v *Vault) ListAll() (map[string][]string, error) {
	result := make(map[string][]string)

	entries, err := os.ReadDir(v.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	for _, e := range entries {
		if e.IsDir() {
			profiles, err := v.List(e.Name())
			if err != nil {
				continue
			}
			result[e.Name()] = profiles
		}
	}

	return result, nil
}

// Delete removes a profile from the vault.
func (v *Vault) Delete(tool, profile string) error {
	profileDir := v.ProfilePath(tool, profile)
	return os.RemoveAll(profileDir)
}

// ActiveProfile returns which profile is currently active (if any).
// It compares the current auth files with vault backups using content hashing.
func (v *Vault) ActiveProfile(fileSet AuthFileSet) (string, error) {
	profiles, err := v.List(fileSet.Tool)
	if err != nil {
		return "", err
	}

	// Hash the current auth files
	currentHashes := make(map[string]string)
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			continue
		}
		hash, err := hashFile(spec.Path)
		if err != nil {
			continue
		}
		currentHashes[filepath.Base(spec.Path)] = hash
	}

	if len(currentHashes) == 0 {
		return "", nil // No auth files present
	}

	// Compare with each profile
	for _, profile := range profiles {
		profileDir := v.ProfilePath(fileSet.Tool, profile)
		matches := true

		for filename, currentHash := range currentHashes {
			backupPath := filepath.Join(profileDir, filename)
			backupHash, err := hashFile(backupPath)
			if err != nil {
				matches = false
				break
			}
			if currentHash != backupHash {
				matches = false
				break
			}
		}

		if matches {
			return profile, nil
		}
	}

	return "", nil // No matching profile found
}

// HasAuthFiles checks if the tool currently has auth files present.
func HasAuthFiles(fileSet AuthFileSet) bool {
	for _, spec := range fileSet.Files {
		if spec.Required {
			if _, err := os.Stat(spec.Path); err == nil {
				return true
			}
		}
	}
	return false
}

// ClearAuthFiles removes all auth files for a tool (logout).
func ClearAuthFiles(fileSet AuthFileSet) error {
	for _, spec := range fileSet.Files {
		if err := os.Remove(spec.Path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", spec.Path, err)
		}
	}
	return nil
}

// Helper functions

func copyFile(src, dst string) error {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Create temp file for atomic write
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

	if err := dstFile.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Atomic rename
	return os.Rename(tmpPath, dst)
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
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
