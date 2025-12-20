// Package profile manages individual account profiles for AI coding tools.
//
// Each profile represents a complete, isolated authentication context for
// a specific account. Profiles contain:
//   - Pseudo-HOME directory for context isolation
//   - Auth file storage (either in pseudo-HOME or provider-specific location)
//   - Configuration metadata
//   - Lock files for preventing concurrent access
package profile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Profile represents a single account profile for an AI coding tool.
type Profile struct {
	// Name is the unique identifier for this profile (e.g., "work", "personal").
	Name string `json:"name"`

	// Provider is the tool provider ID (codex, claude, gemini).
	Provider string `json:"provider"`

	// AuthMode is the authentication method (oauth, api-key, vertex-adc).
	AuthMode string `json:"auth_mode"`

	// BasePath is the root directory for this profile's data.
	// Structure: ~/.local/share/caam/profiles/<provider>/<name>/
	BasePath string `json:"base_path"`

	// AccountLabel is a human-friendly label (e.g., email address).
	AccountLabel string `json:"account_label,omitempty"`

	// Description is free-form notes about the profile's purpose.
	// Examples: "Client X project", "Free tier for testing", "Team shared account"
	Description string `json:"description,omitempty"`

	// CreatedAt is when this profile was created.
	CreatedAt time.Time `json:"created_at"`

	// LastUsedAt is when this profile was last used.
	LastUsedAt time.Time `json:"last_used_at,omitempty"`

	// LastSessionID is the most recent Codex session ID observed for this profile.
	// This enables 'caam resume codex <profile>' to resume without manually copy/pasting.
	LastSessionID string `json:"last_session_id,omitempty"`

	// LastSessionTS is when LastSessionID was last observed.
	LastSessionTS time.Time `json:"last_session_ts,omitempty"`

	// Metadata stores provider-specific configuration.
	Metadata map[string]string `json:"metadata,omitempty"`

	// BrowserCommand is the browser executable to use for OAuth flows.
	// Examples: "google-chrome", "firefox", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	// If empty, uses system default browser.
	BrowserCommand string `json:"browser_command,omitempty"`

	// BrowserProfileDir is the browser profile directory or name.
	// For Chrome: "Profile 1", "Default", or full path to profile directory
	// For Firefox: profile name as shown in about:profiles
	// If empty, uses browser's default profile.
	BrowserProfileDir string `json:"browser_profile_dir,omitempty"`

	// BrowserProfileName is a human-friendly label for the browser profile.
	// Examples: "Work Google", "Personal GitHub"
	// Used for display purposes only.
	BrowserProfileName string `json:"browser_profile_name,omitempty"`
}

// HomePath returns the pseudo-HOME directory for this profile.
// This is where tools that use HOME for auth storage will look.
func (p *Profile) HomePath() string {
	return filepath.Join(p.BasePath, "home")
}

// XDGConfigPath returns the pseudo-XDG_CONFIG_HOME directory.
// Used by tools like Claude Code that respect XDG conventions.
func (p *Profile) XDGConfigPath() string {
	return filepath.Join(p.BasePath, "xdg_config")
}

// CodexHomePath returns the CODEX_HOME directory for this profile.
// Codex CLI specifically uses this for auth.json.
func (p *Profile) CodexHomePath() string {
	return filepath.Join(p.BasePath, "codex_home")
}

// LockPath returns the path to the lock file.
func (p *Profile) LockPath() string {
	return filepath.Join(p.BasePath, ".lock")
}

// MetaPath returns the path to the profile metadata file.
func (p *Profile) MetaPath() string {
	return filepath.Join(p.BasePath, "profile.json")
}

// HasBrowserConfig returns true if browser configuration is set.
func (p *Profile) HasBrowserConfig() bool {
	return p.BrowserCommand != "" || p.BrowserProfileDir != ""
}

// BrowserDisplayName returns a display name for the browser profile.
// Returns BrowserProfileName if set, otherwise a generated description.
func (p *Profile) BrowserDisplayName() string {
	if p.BrowserProfileName != "" {
		return p.BrowserProfileName
	}
	if p.BrowserCommand != "" && p.BrowserProfileDir != "" {
		return fmt.Sprintf("%s (%s)", p.BrowserCommand, p.BrowserProfileDir)
	}
	if p.BrowserCommand != "" {
		return p.BrowserCommand
	}
	if p.BrowserProfileDir != "" {
		return p.BrowserProfileDir
	}
	return "system default"
}

// IsLocked checks if the profile is currently locked (in use).
func (p *Profile) IsLocked() bool {
	_, err := os.Stat(p.LockPath())
	return err == nil
}

// Lock creates a lock file to indicate the profile is in use.
// Uses O_EXCL for atomic creation to prevent race conditions.
func (p *Profile) Lock() error {
	lockPath := p.LockPath()

	// Use O_EXCL to atomically check and create - prevents TOCTOU race
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("profile %s is already locked", p.Name)
		}
		return fmt.Errorf("create lock file: %w", err)
	}
	defer f.Close()

	// Write lock file with PID
	content := fmt.Sprintf(`{"pid": %d, "locked_at": %q}`, os.Getpid(), time.Now().Format(time.RFC3339))
	if _, err := f.WriteString(content); err != nil {
		// Clean up on failure
		os.Remove(lockPath)
		return fmt.Errorf("write lock file: %w", err)
	}

	return nil
}

// Unlock removes the lock file.
func (p *Profile) Unlock() error {
	lockPath := p.LockPath()
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LockInfo contains information about a lock file.
type LockInfo struct {
	PID      int       `json:"pid"`
	LockedAt time.Time `json:"locked_at"`
}

// GetLockInfo reads and parses the lock file.
// Returns nil, nil if no lock file exists.
func (p *Profile) GetLockInfo() (*LockInfo, error) {
	lockPath := p.LockPath()
	data, err := os.ReadFile(lockPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read lock file: %w", err)
	}

	var info LockInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("parse lock file: %w", err)
	}

	return &info, nil
}

// IsProcessAlive checks if a process with the given PID is still running.
// On Unix, this sends signal 0 to check if the process exists.
func IsProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix, Signal(0) checks if the process exists without actually sending a signal
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}

	// If we get EPERM, the process exists but we can't signal it (it's alive).
	// Only ESRCH means it doesn't exist.
	if errors.Is(err, syscall.EPERM) {
		return true
	}

	return false
}

// IsLockStale checks if the lock file is from a dead process.
// Returns true if the lock exists but the owning process is no longer running.
// Returns false if no lock exists, or if the lock owner is still alive.
func (p *Profile) IsLockStale() (bool, error) {
	info, err := p.GetLockInfo()
	if err != nil {
		return false, err
	}
	if info == nil {
		// No lock file exists
		return false, nil
	}

	// Check if the process is still running
	if IsProcessAlive(info.PID) {
		return false, nil
	}

	return true, nil
}

// CleanStaleLock removes a stale lock file if the owning process is dead.
// Returns true if a stale lock was cleaned, false if no action was taken.
// Returns an error if there's a valid lock or an I/O error.
func (p *Profile) CleanStaleLock() (bool, error) {
	stale, err := p.IsLockStale()
	if err != nil {
		return false, err
	}
	if !stale {
		return false, nil
	}

	// Remove the stale lock
	if err := p.Unlock(); err != nil {
		return false, fmt.Errorf("remove stale lock: %w", err)
	}

	return true, nil
}

// LockWithCleanup attempts to acquire a lock, cleaning stale locks first.
// This is the recommended method for acquiring locks.
func (p *Profile) LockWithCleanup() error {
	// Try to clean any stale locks first
	cleaned, err := p.CleanStaleLock()
	if err != nil {
		return fmt.Errorf("check stale lock: %w", err)
	}
	if cleaned {
		// Log that we cleaned a stale lock (caller can check this if needed)
	}

	// Now try to acquire the lock
	return p.Lock()
}

// Save persists the profile metadata to disk.
func (p *Profile) Save() error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profile: %w", err)
	}

	if err := os.MkdirAll(p.BasePath, 0700); err != nil {
		return fmt.Errorf("create profile dir: %w", err)
	}

	// Atomic write: write to temp file then rename
	tmpPath := p.MetaPath() + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp profile file: %w", err)
	}
	
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp profile file: %w", err)
	}
	
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp profile file: %w", err)
	}
	
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp profile file: %w", err)
	}

	if err := os.Rename(tmpPath, p.MetaPath()); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp profile file: %w", err)
	}

	return nil
}

// UpdateLastUsed updates the last used timestamp and saves.
func (p *Profile) UpdateLastUsed() error {
	p.LastUsedAt = time.Now()
	return p.Save()
}

// Store manages profile storage and retrieval.
type Store struct {
	basePath string // ~/.local/share/caam/profiles
}

// NewStore creates a new profile store.
func NewStore(basePath string) *Store {
	return &Store{basePath: basePath}
}

// DefaultStorePath returns the default profiles directory.
// Falls back to current directory if home directory cannot be determined.
func DefaultStorePath() string {
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "caam", "profiles")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory - unusual but handles edge cases
		return filepath.Join(".local", "share", "caam", "profiles")
	}
	return filepath.Join(homeDir, ".local", "share", "caam", "profiles")
}

// ProfilePath returns the path to a specific profile.
func (s *Store) ProfilePath(provider, name string) string {
	return filepath.Join(s.basePath, provider, name)
}

func validateStoreSegment(kind, val string) (string, error) {
	val = strings.TrimSpace(val)
	if val == "" {
		return "", fmt.Errorf("%s cannot be empty", kind)
	}
	if val == "." || val == ".." {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}
	// Only allow safe characters: alphanumeric, underscore, hyphen, and period.
	// This prevents shell injection when profile names are used in shell scripts
	// (e.g., claude.go's setupAPIKeyHelper embeds profile name in bash script).
	// Also prevents filesystem issues and unexpected behavior.
	for _, r := range val {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			return "", fmt.Errorf("invalid %s: %q (only alphanumeric, underscore, hyphen, and period allowed)", kind, val)
		}
	}
	if filepath.IsAbs(val) || filepath.VolumeName(val) != "" {
		return "", fmt.Errorf("invalid %s: %q", kind, val)
	}
	return val, nil
}

// Create creates a new profile.
func (s *Store) Create(provider, name, authMode string) (*Profile, error) {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return nil, fmt.Errorf("profile store base path is empty")
	}
	var err error
	provider, err = validateStoreSegment("provider", provider)
	if err != nil {
		return nil, err
	}
	name, err = validateStoreSegment("name", name)
	if err != nil {
		return nil, err
	}
	authMode = strings.TrimSpace(authMode)

	profilePath := s.ProfilePath(provider, name)

	// Check if already exists
	if _, err := os.Stat(profilePath); err == nil {
		return nil, fmt.Errorf("profile %s/%s already exists", provider, name)
	}

	profile := &Profile{
		Name:      name,
		Provider:  provider,
		AuthMode:  authMode,
		BasePath:  profilePath,
		CreatedAt: time.Now(),
		Metadata:  make(map[string]string),
	}

	if err := profile.Save(); err != nil {
		return nil, err
	}

	return profile, nil
}

// Load retrieves a profile from disk.
func (s *Store) Load(provider, name string) (*Profile, error) {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return nil, fmt.Errorf("profile store base path is empty")
	}
	var err error
	provider, err = validateStoreSegment("provider", provider)
	if err != nil {
		return nil, err
	}
	name, err = validateStoreSegment("name", name)
	if err != nil {
		return nil, err
	}

	profilePath := s.ProfilePath(provider, name)
	metaPath := filepath.Join(profilePath, "profile.json")

	data, err := os.ReadFile(metaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("profile %s/%s not found", provider, name)
		}
		return nil, fmt.Errorf("read profile: %w", err)
	}

	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, fmt.Errorf("parse profile: %w", err)
	}

	// Ensure BasePath is set (for backwards compatibility)
	if profile.BasePath == "" {
		profile.BasePath = profilePath
	}

	return &profile, nil
}

// Delete removes a profile and all its data.
func (s *Store) Delete(provider, name string) error {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return fmt.Errorf("profile store base path is empty")
	}
	var err error
	provider, err = validateStoreSegment("provider", provider)
	if err != nil {
		return err
	}
	name, err = validateStoreSegment("name", name)
	if err != nil {
		return err
	}

	profilePath := s.ProfilePath(provider, name)

	// Check if profile exists
	if _, err := os.Stat(profilePath); os.IsNotExist(err) {
		return fmt.Errorf("profile %s/%s not found", provider, name)
	}

	// Check if locked
	lockPath := filepath.Join(profilePath, ".lock")
	if _, err := os.Stat(lockPath); err == nil {
		return fmt.Errorf("cannot delete locked profile %s/%s", provider, name)
	}

	return os.RemoveAll(profilePath)
}

// List returns all profiles for a provider.
func (s *Store) List(provider string) ([]*Profile, error) {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return nil, fmt.Errorf("profile store base path is empty")
	}
	var err error
	provider, err = validateStoreSegment("provider", provider)
	if err != nil {
		return nil, err
	}

	providerPath := filepath.Join(s.basePath, provider)

	entries, err := os.ReadDir(providerPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var profiles []*Profile
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		profile, err := s.Load(provider, entry.Name())
		if err != nil {
			continue // Skip invalid profiles
		}
		profiles = append(profiles, profile)
	}

	return profiles, nil
}

// ListAll returns all profiles for all providers.
func (s *Store) ListAll() (map[string][]*Profile, error) {
	result := make(map[string][]*Profile)

	entries, err := os.ReadDir(s.basePath)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		profiles, err := s.List(entry.Name())
		if err != nil {
			continue
		}
		if len(profiles) > 0 {
			result[entry.Name()] = profiles
		}
	}

	return result, nil
}

// Exists checks if a profile exists.
func (s *Store) Exists(provider, name string) bool {
	if s == nil || strings.TrimSpace(s.basePath) == "" {
		return false
	}
	var err error
	provider, err = validateStoreSegment("provider", provider)
	if err != nil {
		return false
	}
	name, err = validateStoreSegment("name", name)
	if err != nil {
		return false
	}

	profilePath := s.ProfilePath(provider, name)
	_, err = os.Stat(profilePath)
	return err == nil
}
