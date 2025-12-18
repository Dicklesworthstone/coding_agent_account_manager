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
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	basePath string // ~/.local/share/caam/vault
}

const originalProfileName = "_original"

// IsSystemProfile reports whether a profile name is reserved for system-managed
// profiles (created automatically by caam safety features).
//
// Convention: profile names starting with '_' are system profiles.
func IsSystemProfile(name string) bool {
	return strings.HasPrefix(strings.TrimSpace(name), "_")
}

var errProtectedSystemProfile = fmt.Errorf("protected system profile")

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
	profileDir, err := v.safeProfileDir(fileSet.Tool, profile)
	if err != nil {
		return err
	}

	tool := strings.TrimSpace(fileSet.Tool)
	profile = strings.TrimSpace(profile)

	// System profiles are immutable safety artifacts; never overwrite them.
	if IsSystemProfile(profile) {
		st, err := os.Stat(profileDir)
		if err == nil {
			if st.IsDir() {
				return fmt.Errorf("%w: refusing to overwrite %s/%s", errProtectedSystemProfile, tool, profile)
			}
			return fmt.Errorf("profile path exists and is not a directory: %s", profileDir)
		}
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("stat profile dir: %w", err)
		}
	}

	// Create profile directory
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	backedUp := 0
	var originalPaths []string
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			if spec.Required {
				return fmt.Errorf("required auth file not found: %s", spec.Path)
			}
			continue // Skip optional files that don't exist
		}

		// Copy file to vault
		filename := filepath.Base(spec.Path)
		destPath := filepath.Join(profileDir, filename)

		if err := copyFile(spec.Path, destPath); err != nil {
			return fmt.Errorf("backup %s: %w", spec.Path, err)
		}
		backedUp++
		originalPaths = append(originalPaths, spec.Path)
	}

	if backedUp == 0 {
		return fmt.Errorf("no auth files found to backup for %s", tool)
	}

	// Write metadata
	metaPath := filepath.Join(profileDir, "meta.json")
	meta := struct {
		Tool          string   `json:"tool"`
		Profile       string   `json:"profile"`
		BackedUpAt    string   `json:"backed_up_at"`
		Files         int      `json:"files"`
		Type          string   `json:"type,omitempty"`       // user|system
		CreatedBy     string   `json:"created_by,omitempty"` // user|auto|first-activate
		OriginalPaths []string `json:"original_paths,omitempty"`
	}{
		Tool:          tool,
		Profile:       profile,
		BackedUpAt:    time.Now().Format(time.RFC3339),
		Files:         backedUp,
		Type:          "user",
		CreatedBy:     "user",
		OriginalPaths: originalPaths,
	}
	if IsSystemProfile(profile) {
		meta.Type = "system"
		meta.CreatedBy = "auto"
		if profile == originalProfileName {
			meta.CreatedBy = "first-activate"
		}
	}
	raw, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if err := os.WriteFile(metaPath, raw, 0600); err != nil {
		return fmt.Errorf("write metadata: %w", err)
	}

	return nil
}

// HasOriginalBackup reports whether the system-managed `_original` profile exists
// for the given tool.
func (v *Vault) HasOriginalBackup(tool string) (bool, error) {
	profileDir, err := v.safeProfileDir(tool, originalProfileName)
	if err != nil {
		return false, err
	}
	st, err := os.Stat(profileDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat original profile dir: %w", err)
	}
	if !st.IsDir() {
		return false, fmt.Errorf("original profile path is not a directory: %s", profileDir)
	}
	return true, nil
}

// BackupCurrent creates a timestamped backup of the current auth state.
// Returns the backup profile name (e.g., "_backup_20251217_143022") if created,
// or empty string if there was nothing to back up.
func (v *Vault) BackupCurrent(fileSet AuthFileSet) (string, error) {
	// Only back up when at least one required auth file exists.
	if !HasAuthFiles(fileSet) {
		return "", nil
	}

	// Generate timestamped backup name
	timestamp := time.Now().Format("20060102_150405")
	backupName := "_backup_" + timestamp

	if err := v.Backup(fileSet, backupName); err != nil {
		return "", fmt.Errorf("backup current: %w", err)
	}

	return backupName, nil
}

// RotateAutoBackups removes old auto-backup profiles to stay within the limit.
// Backups are sorted by timestamp (oldest first) and oldest are deleted.
// A maxBackups of 0 means unlimited (no rotation).
func (v *Vault) RotateAutoBackups(tool string, maxBackups int) error {
	if maxBackups <= 0 {
		return nil // Unlimited
	}

	profiles, err := v.List(tool)
	if err != nil {
		return fmt.Errorf("list profiles: %w", err)
	}

	// Filter to auto-backup profiles only
	var backups []string
	for _, p := range profiles {
		if strings.HasPrefix(p, "_backup_") {
			backups = append(backups, p)
		}
	}

	// Already within limit?
	if len(backups) <= maxBackups {
		return nil
	}

	// Sort by name (which includes timestamp, so oldest first)
	// _backup_20251217_143022 sorts lexicographically by date/time
	sort.Strings(backups)

	// Delete oldest until we're within limit
	toDelete := len(backups) - maxBackups
	for i := 0; i < toDelete; i++ {
		if err := v.DeleteForce(tool, backups[i]); err != nil {
			return fmt.Errorf("delete old backup %s: %w", backups[i], err)
		}
	}

	return nil
}

// BackupOriginal creates the system-managed `_original` profile for a tool if
// needed. This is intended to preserve a user's pre-caam auth state.
//
// Behavior:
// - No-op if `_original` already exists
// - No-op if no current auth files exist
// - No-op if current auth already matches an existing vault profile
// - Otherwise backups current auth as `_original`
//
// It returns true if a backup was created.
func (v *Vault) BackupOriginal(fileSet AuthFileSet) (bool, error) {
	exists, err := v.HasOriginalBackup(fileSet.Tool)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}

	// Only back up when at least one required auth file exists.
	if !HasAuthFiles(fileSet) {
		return false, nil
	}

	active, err := v.ActiveProfile(fileSet)
	if err != nil {
		return false, fmt.Errorf("detect active profile: %w", err)
	}
	if active != "" {
		return false, nil
	}

	if err := v.Backup(fileSet, originalProfileName); err != nil {
		return false, err
	}
	return true, nil
}

// Restore copies backed-up auth files to their original locations.
func (v *Vault) Restore(fileSet AuthFileSet, profile string) error {
	profileDir, err := v.safeProfileDir(fileSet.Tool, profile)
	if err != nil {
		return err
	}

	if _, err := os.Stat(profileDir); os.IsNotExist(err) {
		return fmt.Errorf("profile %s/%s not found in vault", fileSet.Tool, profile)
	}

	restored := 0
	for _, spec := range fileSet.Files {
		filename := filepath.Base(spec.Path)
		srcPath := filepath.Join(profileDir, filename)

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
	toolDir, err := v.safeToolDir(tool)
	if err != nil {
		return nil, err
	}
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
	if IsSystemProfile(profile) {
		return fmt.Errorf("%w: refusing to delete %s/%s without force", errProtectedSystemProfile, tool, profile)
	}
	return v.DeleteForce(tool, profile)
}

// DeleteForce removes a profile from the vault, including system profiles.
// Prefer Delete unless the caller has an explicit reason to remove protected
// profiles.
func (v *Vault) DeleteForce(tool, profile string) error {
	profileDir, err := v.safeProfileDir(tool, profile)
	if err != nil {
		return err
	}
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

	if err := dstFile.Sync(); err != nil {
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

func (v *Vault) safeToolDir(tool string) (string, error) {
	if v == nil || strings.TrimSpace(v.basePath) == "" {
		return "", fmt.Errorf("vault base path is empty")
	}
	tool, err := validateVaultSegment("tool", tool)
	if err != nil {
		return "", err
	}

	baseAbs, err := filepath.Abs(v.basePath)
	if err != nil {
		return "", fmt.Errorf("vault base absolute path: %w", err)
	}

	return filepath.Join(baseAbs, tool), nil
}

func (v *Vault) safeProfileDir(tool, profile string) (string, error) {
	if v == nil || strings.TrimSpace(v.basePath) == "" {
		return "", fmt.Errorf("vault base path is empty")
	}
	tool, err := validateVaultSegment("tool", tool)
	if err != nil {
		return "", err
	}
	profile, err = validateVaultSegment("profile", profile)
	if err != nil {
		return "", err
	}

	baseAbs, err := filepath.Abs(v.basePath)
	if err != nil {
		return "", fmt.Errorf("vault base absolute path: %w", err)
	}

	full := filepath.Join(baseAbs, tool, profile)
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("vault profile absolute path: %w", err)
	}

	baseAbs = filepath.Clean(baseAbs)
	if fullAbs != baseAbs && !strings.HasPrefix(fullAbs, baseAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("vault profile path escapes base directory")
	}

	return fullAbs, nil
}

func validateVaultSegment(kind, val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("%s cannot be empty", kind)
	}
	if val == "." || val == ".." {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}
	if strings.ContainsRune(val, 0) {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}
	if strings.ContainsAny(val, "/\\") {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}
	if filepath.IsAbs(val) || filepath.VolumeName(val) != "" {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}

	return val, nil
}
