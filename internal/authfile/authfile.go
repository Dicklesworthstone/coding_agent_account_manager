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
	"encoding/base64"
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
	// AllowOptionalOnly permits auth states that rely solely on optional files
	// (e.g., API key or helper-based auth that doesn't create OAuth artifacts).
	AllowOptionalOnly bool
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
// Claude Code stores OAuth credentials in:
//   - ~/.claude/.credentials.json (primary - contains claudeAiOauth with tokens)
//   - ~/.claude.json (settings file - not auth, but backed up for completeness)
//   - ~/.config/claude-code/auth.json (auth credentials; or $CLAUDE_CONFIG_DIR/auth.json)
//   - ~/.claude/settings.json (user settings)
func ClaudeAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()
	claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
	if claudeConfigDir == "" {
		xdgConfig := os.Getenv("XDG_CONFIG_HOME")
		if xdgConfig == "" {
			xdgConfig = filepath.Join(homeDir, ".config")
		}
		claudeConfigDir = filepath.Join(xdgConfig, "claude-code")
	}

	return AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude", ".credentials.json"),
				Description: "Claude Code OAuth credentials (Claude Max subscription)",
				Required:    true,
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude.json"),
				Description: "Claude Code settings and session state",
				Required:    false, // This is a settings file, not strictly required for auth
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(claudeConfigDir, "auth.json"),
				Description: "Claude Code auth credentials",
				Required:    false,
			},
			{
				Tool:        "claude",
				Path:        filepath.Join(homeDir, ".claude", "settings.json"),
				Description: "Claude Code user settings (apiKeyHelper / API key mode)",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
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
				Path:        filepath.Join(geminiHome, "oauth_creds.json"),
				Description: "Gemini CLI OAuth credentials cache",
				Required:    false,
			},
			{
				Tool:        "gemini",
				Path:        filepath.Join(geminiHome, ".env"),
				Description: "Gemini API key (.env file)",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
	}
}

// OpenCodeAuthFiles returns the auth files for OpenCode.
// OpenCode stores auth in $XDG_DATA_HOME/opencode/auth.json (default ~/.local/share/opencode/auth.json).
func OpenCodeAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" {
		dataHome = filepath.Join(homeDir, ".local", "share")
	}

	return AuthFileSet{
		Tool: "opencode",
		Files: []AuthFileSpec{
			{
				Tool:        "opencode",
				Path:        filepath.Join(dataHome, "opencode", "auth.json"),
				Description: "OpenCode auth credentials",
				Required:    true,
			},
		},
	}
}

// CursorAuthFiles returns the auth files for Cursor CLI.
// Cursor stores config in ~/.cursor/ directory.
func CursorAuthFiles() AuthFileSet {
	homeDir, _ := os.UserHomeDir()

	return AuthFileSet{
		Tool: "cursor",
		Files: []AuthFileSpec{
			{
				Tool:        "cursor",
				Path:        filepath.Join(homeDir, ".cursor", "auth.json"),
				Description: "Cursor CLI auth credentials",
				Required:    false,
			},
			{
				Tool:        "cursor",
				Path:        filepath.Join(homeDir, ".cursor", "settings.json"),
				Description: "Cursor CLI settings",
				Required:    false,
			},
		},
		AllowOptionalOnly: true,
	}
}

// GetAuthFileSet returns the AuthFileSet for the given provider name.
func GetAuthFileSet(provider string) (AuthFileSet, bool) {
	switch strings.ToLower(provider) {
	case "claude":
		return ClaudeAuthFiles(), true
	case "codex":
		return CodexAuthFiles(), true
	case "gemini":
		return GeminiAuthFiles(), true
	case "opencode", "oc":
		return OpenCodeAuthFiles(), true
	case "cursor", "cur":
		return CursorAuthFiles(), true
	default:
		return AuthFileSet{}, false
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
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "data", "vault")
	}
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
	requiredFound := false
	optionalFound := false
	var missingRequired []string
	var originalPaths []string
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			if spec.Required {
				missingRequired = append(missingRequired, spec.Path)
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
		if spec.Required {
			requiredFound = true
		} else {
			optionalFound = true
		}
		originalPaths = append(originalPaths, spec.Path)
	}

	if backedUp == 0 {
		return fmt.Errorf("no auth files found to backup for %s; ensure you're logged in first with '%s' or 'caam add %s'", tool, tool, tool)
	}
	if len(missingRequired) > 0 {
		if !(fileSet.AllowOptionalOnly && !requiredFound && optionalFound) {
			return fmt.Errorf("required auth file not found: %s", missingRequired[0])
		}
	}

	// Write metadata
	metaPath := filepath.Join(profileDir, "meta.json")
	meta := struct {
		Tool          string   `json:"tool"`
		Profile       string   `json:"profile"`
		Description   string   `json:"description,omitempty"` // Free-form notes about profile purpose
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

	// Atomic write: write to temp file, fsync, then rename
	dir := filepath.Dir(metaPath)
	f, err := os.CreateTemp(dir, "meta.json.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp metadata file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

	if _, err := f.Write(raw); err != nil {
		f.Close()
		return fmt.Errorf("write temp metadata file: %w", err)
	}

	if err := f.Chmod(0600); err != nil {
		f.Close()
		return fmt.Errorf("chmod temp metadata file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("sync temp metadata file: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp metadata file: %w", err)
	}

	if err := os.Rename(tmpPath, metaPath); err != nil {
		return fmt.Errorf("rename metadata file: %w", err)
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
	// Only back up when at least one auth file exists.
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

	// Only back up when at least one auth file exists.
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

// MigrateGeminiVaultDir renames oauth_credentials.json to oauth_creds.json in a
// vault profile directory if the old name exists and the new name does not.
// CAAM previously stored "oauth_credentials.json" but Gemini CLI reads "oauth_creds.json".
// This is a no-op if the directory already has the new name or has no OAuth file.
func MigrateGeminiVaultDir(dir string) error {
	oldName := filepath.Join(dir, "oauth_credentials.json")
	newName := filepath.Join(dir, "oauth_creds.json")
	if _, err := os.Stat(oldName); err != nil {
		if os.IsNotExist(err) {
			return nil // old file doesn't exist, nothing to migrate
		}
		return err // permission or I/O error
	}
	if _, err := os.Stat(newName); err == nil {
		// New file already exists; remove legacy file to avoid confusion.
		_ = os.Remove(oldName)
		return nil
	}
	if err := os.Rename(oldName, newName); err != nil {
		// Handle race: another process may have completed the migration.
		if _, statErr := os.Stat(newName); statErr == nil {
			return nil
		}
		return err
	}
	return nil
}

// Restore copies backed-up auth files to their original locations.
func (v *Vault) Restore(fileSet AuthFileSet, profile string) error {
	profileDir, err := v.safeProfileDir(fileSet.Tool, profile)
	if err != nil {
		return err
	}

	if _, err := os.Stat(profileDir); os.IsNotExist(err) {
		return fmt.Errorf("profile %s/%s not found in vault; run 'caam ls %s' to see available profiles", fileSet.Tool, profile, fileSet.Tool)
	}

	// Migrate legacy Gemini OAuth filename in vault.
	if fileSet.Tool == "gemini" {
		if err := MigrateGeminiVaultDir(profileDir); err != nil {
			return fmt.Errorf("vault migration (oauth_credentials.json -> oauth_creds.json): %w", err)
		}
	}

	restored := 0
	requiredFound := false
	optionalFound := false
	var missingRequired []string
	for _, spec := range fileSet.Files {
		filename := filepath.Base(spec.Path)
		srcPath := filepath.Join(profileDir, filename)

		// Check if backup exists
		if _, err := os.Stat(srcPath); os.IsNotExist(err) {
			if spec.Required {
				missingRequired = append(missingRequired, srcPath)
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
		if spec.Required {
			requiredFound = true
		} else {
			optionalFound = true
		}
	}

	if restored == 0 {
		return fmt.Errorf("no auth files restored for %s/%s", fileSet.Tool, profile)
	}
	if len(missingRequired) > 0 {
		if !(fileSet.AllowOptionalOnly && !requiredFound && optionalFound) {
			return fmt.Errorf("required backup not found: %s", missingRequired[0])
		}
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

// CopyProfile creates a copy of a profile with a new name.
// This is a non-destructive operation: the source profile remains unchanged.
// Returns an error if the source doesn't exist or the destination already exists.
func (v *Vault) CopyProfile(tool, srcProfile, dstProfile string) error {
	srcDir, err := v.safeProfileDir(tool, srcProfile)
	if err != nil {
		return fmt.Errorf("invalid source profile: %w", err)
	}
	dstDir, err := v.safeProfileDir(tool, dstProfile)
	if err != nil {
		return fmt.Errorf("invalid destination profile: %w", err)
	}

	// Verify source exists
	if _, err := os.Stat(srcDir); os.IsNotExist(err) {
		return fmt.Errorf("source profile %s/%s not found", tool, srcProfile)
	}

	// Verify destination doesn't exist
	if _, err := os.Stat(dstDir); err == nil {
		return fmt.Errorf("destination profile %s/%s already exists", tool, dstProfile)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("check destination: %w", err)
	}

	// Create destination directory
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	// Copy all files from source to destination
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		os.RemoveAll(dstDir) // Cleanup on failure
		return fmt.Errorf("read source dir: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue // Skip subdirectories
		}

		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		if err := copyFile(srcPath, dstPath); err != nil {
			os.RemoveAll(dstDir) // Cleanup on failure
			return fmt.Errorf("copy %s: %w", entry.Name(), err)
		}
	}

	// Update meta.json with new profile name
	metaPath := filepath.Join(dstDir, "meta.json")
	if _, err := os.Stat(metaPath); err == nil {
		// Read and update meta.json
		data, err := os.ReadFile(metaPath)
		if err == nil {
			var meta map[string]interface{}
			if json.Unmarshal(data, &meta) == nil {
				meta["profile"] = dstProfile
				meta["copied_from"] = srcProfile
				meta["copied_at"] = time.Now().Format(time.RFC3339)
				if updated, err := json.MarshalIndent(meta, "", "  "); err == nil {
					// Atomic write for meta.json
					tmpPath := metaPath + ".tmp"
					if err := os.WriteFile(tmpPath, updated, 0600); err == nil {
						os.Rename(tmpPath, metaPath)
					}
				}
			}
		}
	}

	return nil
}

// ActiveProfile returns which profile is currently active (if any).
// It compares the current auth files with vault backups using stable identity
// hashing. For tools like Claude and Codex, only identity-bearing fields are
// hashed so that volatile metadata (e.g., changelogLastFetched, numStartups)
// does not break profile detection.
func (v *Vault) ActiveProfile(fileSet AuthFileSet) (string, error) {
	profiles, err := v.List(fileSet.Tool)
	if err != nil {
		return "", err
	}

	// Hash the current auth files using stable identity extraction.
	// Prefer required files for matching; optional files can change frequently
	// (e.g., settings/session files) and should not break profile detection.
	currentHashes := make(map[string]string)
	optionalHashes := make(map[string]string)
	requiredFound := false
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); os.IsNotExist(err) {
			continue
		}
		hash, err := stableFileHash(fileSet.Tool, spec.Path)
		if err != nil {
			continue
		}
		base := filepath.Base(spec.Path)
		if spec.Required {
			requiredFound = true
			currentHashes[base] = hash
			continue
		}
		optionalHashes[base] = hash
	}

	if !requiredFound {
		if fileSet.AllowOptionalOnly {
			currentHashes = optionalHashes
		}
	}

	if len(currentHashes) == 0 {
		return "", nil // No relevant auth files present
	}

	// Compare with each profile.
	// Prefer user-named profiles over system profiles (_backup_*, _original,
	// _auto_backup_*). System profiles can share the same identity as a named
	// profile (same account re-authenticated), and because they sort
	// alphabetically before most user names (underscore < lowercase letters),
	// they would otherwise shadow the intended named profile.
	var systemMatch string
	for _, profile := range profiles {
		profileDir := v.ProfilePath(fileSet.Tool, profile)
		matches := true

		for filename, currentHash := range currentHashes {
			backupPath := filepath.Join(profileDir, filename)
			backupHash, err := stableFileHash(fileSet.Tool, backupPath)
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
			if !IsSystemProfile(profile) {
				return profile, nil // Prefer user-named profiles
			}
			if systemMatch == "" {
				systemMatch = profile // Remember first system match as fallback
			}
		}
	}

	return systemMatch, nil // Fall back to system profile, or "" if no match
}

// HasAuthFiles checks if the tool currently has auth files present.
func HasAuthFiles(fileSet AuthFileSet) bool {
	optionalFound := false
	for _, spec := range fileSet.Files {
		if _, err := os.Stat(spec.Path); err == nil {
			if spec.Required {
				return true
			}
			optionalFound = true
		}
	}
	if fileSet.AllowOptionalOnly && optionalFound {
		return true
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
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Create temp file for atomic write using CreateTemp to avoid races
	// Pattern: filename.tmp.RANDOM
	dstFile, err := os.CreateTemp(dir, filepath.Base(dst)+".tmp.*")
	if err != nil {
		return err
	}
	tmpPath := dstFile.Name()

	// Ensure cleanup of temp file if something goes wrong.
	// If rename succeeds, this removal will fail (which is fine).
	defer os.Remove(tmpPath)

	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		return err
	}

	// Enforce 0600 permissions for all auth files
	if err := dstFile.Chmod(0600); err != nil {
		dstFile.Close()
		return err
	}

	if err := dstFile.Sync(); err != nil {
		dstFile.Close()
		return err
	}

	if err := dstFile.Close(); err != nil {
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

// hashBytes returns a SHA-256 hex digest of raw bytes. Used as a fallback
// when identity extraction fails but we already have the file data in memory,
// avoiding a second disk read (TOCTOU race) that hashFile would require.
func hashBytes(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// stableFileHash returns a hash of only the identity-bearing fields in an auth
// file, ignoring volatile metadata that tools write after activation. This
// prevents profile detection from breaking when tools modify non-auth fields.
//
// Falls back to whole-file hashing if identity extraction fails or is not
// implemented for the given tool.
func stableFileHash(tool, path string) (string, error) {
	switch tool {
	case "claude":
		return stableClaudeHash(path)
	case "codex":
		return stableCodexHash(path)
	default:
		return hashFile(path)
	}
}

// stableClaudeHash extracts identity-bearing fields from Claude auth files and
// hashes only those fields. This handles two file types:
//
//   - .credentials.json: contains claudeAiOauth with accessToken and
//     refreshToken (the actual auth identity)
//   - .claude.json: settings file with oauthAccount (identity) mixed with
//     volatile fields like changelogLastFetched, numStartups, tipsHistory
//
// For .credentials.json, we hash the accessToken and refreshToken.
// For .claude.json, we hash the oauthAccount field only.
// For other files (settings.json, auth.json), we fall back to whole-file hash.
func stableClaudeHash(path string) (string, error) {
	base := filepath.Base(path)

	switch base {
	case ".credentials.json":
		return hashClaudeCredentials(path)
	case ".claude.json":
		return hashClaudeSettings(path)
	default:
		return hashFile(path)
	}
}

// hashClaudeCredentials hashes the identity-bearing fields from Claude's
// .credentials.json: the accessToken and refreshToken from claudeAiOauth.
// These tokens uniquely identify the authenticated account. Volatile fields
// like expiresAt are excluded since they change on token refresh without
// changing the account identity.
func hashClaudeCredentials(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		// Not valid JSON; fall back to hashing the bytes we already read
		return hashBytes(data), nil
	}

	oauth, ok := root["claudeAiOauth"].(map[string]interface{})
	if !ok {
		// No claudeAiOauth section; fall back to hashing the bytes we already read
		return hashBytes(data), nil
	}

	// Extract stable identity fields: accessToken and refreshToken uniquely
	// identify the authenticated session/account.
	identityFields := map[string]interface{}{}
	for _, key := range []string{"accessToken", "refreshToken"} {
		if v, exists := oauth[key]; exists {
			identityFields[key] = v
		}
	}

	if len(identityFields) == 0 {
		return hashBytes(data), nil
	}

	// Deterministic JSON serialization for hashing
	canonical, err := json.Marshal(identityFields)
	if err != nil {
		return hashBytes(data), nil
	}

	h := sha256.New()
	h.Write([]byte("claude:credentials:"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashClaudeSettings hashes only the identity-bearing oauthAccount field from
// Claude's .claude.json settings file. This file contains many volatile fields
// (changelogLastFetched, numStartups, tipsHistory, etc.) that change
// frequently and would break profile detection if included in the hash.
func hashClaudeSettings(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var root map[string]interface{}
	if err := json.Unmarshal(data, &root); err != nil {
		return hashBytes(data), nil
	}

	// oauthAccount is the identity-bearing field in .claude.json.
	// All other top-level fields are volatile session/UI state.
	identityFields := map[string]interface{}{}
	for _, key := range []string{"oauthAccount", "userID"} {
		if v, exists := root[key]; exists {
			identityFields[key] = v
		}
	}

	if len(identityFields) == 0 {
		// No identity fields found; the file is purely volatile settings.
		// Return a fixed sentinel hash so all settings-only files match,
		// preventing settings drift from breaking profile detection.
		h := sha256.New()
		h.Write([]byte("claude:settings:no-identity"))
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	canonical, err := json.Marshal(identityFields)
	if err != nil {
		return hashBytes(data), nil
	}

	h := sha256.New()
	h.Write([]byte("claude:settings:"))
	h.Write(canonical)
	return hex.EncodeToString(h.Sum(nil)), nil
}

// stableCodexHash extracts a stable identity from Codex auth files by parsing
// JWT tokens and hashing the identity claims (email, account_id, organization).
// This solves the dedup problem where multiple named profiles with different
// token strings (due to refresh) actually represent the same OpenAI account.
//
// Falls back to whole-file hash if JWT parsing fails.
func stableCodexHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return hashBytes(data), nil
	}

	// Try to extract stable identity from JWT tokens.
	// Codex stores tokens in various fields; check all candidates.
	identity := extractCodexIdentity(auth)
	if identity != "" {
		h := sha256.New()
		h.Write([]byte("codex:identity:"))
		h.Write([]byte(identity))
		return hex.EncodeToString(h.Sum(nil)), nil
	}

	// JWT parsing failed; fall back to hashing bytes we already read
	return hashBytes(data), nil
}

// extractCodexIdentity extracts a stable identity string from Codex auth data
// by decoding JWT tokens and extracting email/account claims. Returns empty
// string if no identity can be determined.
func extractCodexIdentity(auth map[string]interface{}) string {
	// Ordered by preference: id_token has richer claims than access_token
	tokenFields := []string{"id_token", "idToken"}
	nestedTokenFields := []string{"id_token", "idToken", "access_token", "accessToken"}

	// Check top-level token fields
	for _, field := range tokenFields {
		if token := jsonString(auth, field); token != "" {
			if id := identityFromJWT(token); id != "" {
				return id
			}
		}
	}

	// Check nested tokens object
	if tokens, ok := auth["tokens"].(map[string]interface{}); ok {
		for _, field := range nestedTokenFields {
			if token := jsonString(tokens, field); token != "" {
				if id := identityFromJWT(token); id != "" {
					return id
				}
			}
		}
	}

	// Check top-level access tokens as last resort
	for _, field := range []string{"access_token", "accessToken", "token"} {
		if token := jsonString(auth, field); token != "" {
			if id := identityFromJWT(token); id != "" {
				return id
			}
		}
	}

	return ""
}

// identityFromJWT decodes a JWT token (without signature verification) and
// extracts a stable identity string from its claims. Returns empty string if
// the token is not a valid JWT or contains no identity claims.
func identityFromJWT(token string) string {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[1] == "" {
		return ""
	}

	// Decode the payload segment
	payload, err := decodeBase64Segment(parts[1])
	if err != nil {
		return ""
	}

	var claims map[string]interface{}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}

	// Build stable identity from claims, checking known namespaces too.
	// OpenAI/Codex tokens nest some claims under "https://api.openai.com/auth".
	claimMaps := []map[string]interface{}{claims}
	for _, ns := range []string{"https://api.openai.com/auth", "https://api.openai.com/profile"} {
		if nested, ok := claims[ns].(map[string]interface{}); ok {
			claimMaps = append(claimMaps, nested)
		}
	}

	var email, accountID, org string
	for _, m := range claimMaps {
		if email == "" {
			for _, key := range []string{"email", "preferred_username", "upn"} {
				if v := jsonString(m, key); v != "" {
					email = v
					break
				}
			}
		}
		if accountID == "" {
			for _, key := range []string{"sub", "account_id", "accountId", "user_id", "userId"} {
				if v := jsonString(m, key); v != "" {
					accountID = v
					break
				}
			}
		}
		if org == "" {
			for _, key := range []string{"organization", "org", "org_name"} {
				if v := jsonString(m, key); v != "" {
					org = v
					break
				}
			}
		}
	}

	// Build a canonical identity string from whatever we found.
	// At minimum we need email or accountID to have a useful identity.
	if email == "" && accountID == "" {
		return ""
	}

	// Deterministic format: "email|accountID|org"
	return email + "|" + accountID + "|" + org
}

// decodeBase64Segment decodes a base64url-encoded JWT segment, handling
// missing padding.
func decodeBase64Segment(s string) ([]byte, error) {
	// Add padding if needed
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}

	// Try URL encoding first (standard for JWTs), then standard encoding
	if decoded, err := base64DecodeURL(s); err == nil {
		return decoded, nil
	}
	return base64DecodeStd(s)
}

// jsonString extracts a string value from a map, returning empty string if
// the key doesn't exist or isn't a string.
func jsonString(m map[string]interface{}, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

func base64DecodeURL(s string) ([]byte, error) {
	return base64.URLEncoding.DecodeString(s)
}

func base64DecodeStd(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
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
	// Only allow safe characters: alphanumeric, underscore, hyphen, period, and @.
	// This prevents shell injection when profile names are used in shell scripts
	// (e.g., claude.go's setupAPIKeyHelper embeds profile name in bash script).
	// The @ and + characters are safe (no special shell meaning) and useful for email-based profile names.
	// Also prevents filesystem issues and unexpected behavior.
	for _, r := range val {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' || r == '@' || r == '+') {
			return "", fmt.Errorf("invalid %s: %q (only alphanumeric, underscore, hyphen, period, @, and + allowed)", kind, val)
		}
	}
	if filepath.IsAbs(val) || filepath.VolumeName(val) != "" {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}

	return val, nil
}

// ProfileIdentity returns a human-readable identity string for a vault profile
// by reading its auth files and extracting identity-bearing claims (e.g., email,
// account ID). Returns empty string if the identity cannot be determined.
//
// This is used by the doctor command to detect when a named profile and a
// system/backup profile share the same underlying account.
func (v *Vault) ProfileIdentity(tool, profile string) string {
	profileDir := v.ProfilePath(tool, profile)

	switch tool {
	case "codex":
		return v.codexProfileIdentity(profileDir)
	case "claude":
		return v.claudeProfileIdentity(profileDir)
	case "gemini":
		return v.geminiProfileIdentity(profileDir)
	default:
		return ""
	}
}

// codexProfileIdentity extracts identity from a Codex vault profile by parsing
// JWT tokens in auth.json and extracting email/account claims.
func (v *Vault) codexProfileIdentity(profileDir string) string {
	authPath := filepath.Join(profileDir, "auth.json")
	data, err := os.ReadFile(authPath)
	if err != nil {
		return ""
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}

	return extractCodexIdentity(auth)
}

// claudeProfileIdentity extracts identity from a Claude vault profile.
// It checks .claude.json for the oauthAccount field (typically email),
// then falls back to .credentials.json JWT parsing.
func (v *Vault) claudeProfileIdentity(profileDir string) string {
	// Try .claude.json first -- has oauthAccount field
	settingsPath := filepath.Join(profileDir, ".claude.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			if acct := jsonString(root, "oauthAccount"); acct != "" {
				return acct
			}
			if uid := jsonString(root, "userID"); uid != "" {
				return uid
			}
		}
	}

	// Try .credentials.json -- parse JWT from claudeAiOauth.accessToken
	credsPath := filepath.Join(profileDir, ".credentials.json")
	if data, err := os.ReadFile(credsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			if oauth, ok := root["claudeAiOauth"].(map[string]interface{}); ok {
				// Try to extract identity from the access token JWT
				for _, key := range []string{"accessToken", "idToken"} {
					if token := jsonString(oauth, key); token != "" {
						if id := identityFromJWT(token); id != "" {
							return id
						}
					}
				}
			}
		}
	}

	return ""
}

// geminiProfileIdentity extracts identity from a Gemini vault profile by
// reading settings.json or oauth_creds.json for email/account information.
func (v *Vault) geminiProfileIdentity(profileDir string) string {
	// Try settings.json
	settingsPath := filepath.Join(profileDir, "settings.json")
	if data, err := os.ReadFile(settingsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			// Gemini stores identity in various fields depending on version
			for _, key := range []string{"email", "account", "user_email"} {
				if val := jsonString(root, key); val != "" {
					return val
				}
			}
			// Check nested auth object
			if auth, ok := root["auth"].(map[string]interface{}); ok {
				for _, key := range []string{"email", "account"} {
					if val := jsonString(auth, key); val != "" {
						return val
					}
				}
			}
		}
	}

	// Try oauth_creds.json
	credsPath := filepath.Join(profileDir, "oauth_creds.json")
	if data, err := os.ReadFile(credsPath); err == nil {
		var root map[string]interface{}
		if err := json.Unmarshal(data, &root); err == nil {
			for _, key := range []string{"email", "account", "client_email"} {
				if val := jsonString(root, key); val != "" {
					return val
				}
			}
			// Check for JWT id_token
			if token := jsonString(root, "id_token"); token != "" {
				if id := identityFromJWT(token); id != "" {
					return id
				}
			}
		}
	}

	return ""
}
