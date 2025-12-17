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
	"fmt"
	"os"
	"path/filepath"
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

	// CreatedAt is when this profile was created.
	CreatedAt time.Time `json:"created_at"`

	// LastUsedAt is when this profile was last used.
	LastUsedAt time.Time `json:"last_used_at,omitempty"`

	// Metadata stores provider-specific configuration.
	Metadata map[string]string `json:"metadata,omitempty"`
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

// IsLocked checks if the profile is currently locked (in use).
func (p *Profile) IsLocked() bool {
	_, err := os.Stat(p.LockPath())
	return err == nil
}

// Lock creates a lock file to indicate the profile is in use.
func (p *Profile) Lock() error {
	lockPath := p.LockPath()

	// Check if already locked
	if p.IsLocked() {
		return fmt.Errorf("profile %s is already locked", p.Name)
	}

	// Create lock file with PID
	content := fmt.Sprintf(`{"pid": %d, "locked_at": %q}`, os.Getpid(), time.Now().Format(time.RFC3339))
	return os.WriteFile(lockPath, []byte(content), 0600)
}

// Unlock removes the lock file.
func (p *Profile) Unlock() error {
	lockPath := p.LockPath()
	if err := os.Remove(lockPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
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

	return os.WriteFile(p.MetaPath(), data, 0600)
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
func DefaultStorePath() string {
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "caam", "profiles")
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".local", "share", "caam", "profiles")
}

// ProfilePath returns the path to a specific profile.
func (s *Store) ProfilePath(provider, name string) string {
	return filepath.Join(s.basePath, provider, name)
}

// Create creates a new profile.
func (s *Store) Create(provider, name, authMode string) (*Profile, error) {
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
	profilePath := s.ProfilePath(provider, name)
	_, err := os.Stat(profilePath)
	return err == nil
}
