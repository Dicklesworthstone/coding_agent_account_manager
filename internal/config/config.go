// Package config manages global caam configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config holds the global caam configuration.
type Config struct {
	// DefaultProvider is the provider to use when not specified.
	DefaultProvider string `json:"default_provider,omitempty"`

	// DefaultProfiles maps providers to their default profile names.
	DefaultProfiles map[string]string `json:"default_profiles,omitempty"`

	// Passthroughs are additional paths to symlink from real HOME.
	Passthroughs []string `json:"passthroughs,omitempty"`

	// AutoLock enables automatic profile locking during exec.
	AutoLock bool `json:"auto_lock"`

	// BrowserProfile specifies a browser profile for OAuth flows (deprecated).
	BrowserProfile string `json:"browser_profile,omitempty"`

	// BrowserCommand is the browser executable to use for OAuth flows.
	// Examples: "google-chrome", "firefox", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
	// If empty, uses system default browser.
	BrowserCommand string `json:"browser_command,omitempty"`

	// BrowserProfileDir is the browser profile directory or name.
	// For Chrome: "Profile 1", "Default", or full path to profile directory.
	// For Firefox: profile name as shown in about:profiles.
	// If empty, uses browser's default profile.
	BrowserProfileDir string `json:"browser_profile_dir,omitempty"`

	// BrowserProfileName is a human-friendly label for the browser profile.
	// Used for display purposes only.
	BrowserProfileName string `json:"browser_profile_name,omitempty"`

	// Aliases maps profile keys (provider/profile) to their short aliases.
	// Example: {"claude/work-account-1": ["work", "w"]}
	Aliases map[string][]string `json:"aliases,omitempty"`

	// Favorites maps providers to their favorite profile names (in priority order).
	// Example: {"claude": ["work", "personal"]}
	Favorites map[string][]string `json:"favorites,omitempty"`

	// Workspaces maps workspace names to provider-profile mappings.
	// Example: {"work": {"claude": "work-claude", "codex": "work-codex"}}
	Workspaces map[string]map[string]string `json:"workspaces,omitempty"`

	// CurrentWorkspace is the name of the currently active workspace.
	CurrentWorkspace string `json:"current_workspace,omitempty"`

	// Wrap configures retry and backoff behavior for the wrap command.
	Wrap WrapConfig `json:"wrap,omitempty"`

	// Backup configures automatic backup scheduling.
	Backup BackupConfig `json:"backup,omitempty"`
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		DefaultProvider: "codex",
		DefaultProfiles: make(map[string]string),
		AutoLock:        true,
		Wrap:            DefaultWrapConfig(),
		Backup:          DefaultBackupConfig(),
	}
}

// ConfigPath returns the path to the config file.
// Falls back to current directory if home directory cannot be determined.
func ConfigPath() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "caam", "config.json")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory - unusual but handles edge cases
		return filepath.Join(".config", "caam", "config.json")
	}
	return filepath.Join(homeDir, ".config", "caam", "config.json")
}

// DefaultDataPath returns the base caam data directory path.
// This follows XDG Base Directory Specification.
func DefaultDataPath() string {
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "caam")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to current directory - unusual but handles edge cases
		return filepath.Join(".local", "share", "caam")
	}
	return filepath.Join(homeDir, ".local", "share", "caam")
}

// Load reads the configuration from disk.
func Load() (*Config, error) {
	configPath := ConfigPath()

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	config := DefaultConfig()
	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return config, nil
}

// Save writes the configuration to disk.
func (c *Config) Save() error {
	configPath := ConfigPath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Atomic write: write to temp file then rename
	tmpPath := configPath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp config file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp config file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp config file: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("rename temp config file: %w", err)
	}

	return nil
}

// SetDefault sets the default profile for a provider.
func (c *Config) SetDefault(provider, profile string) {
	if c.DefaultProfiles == nil {
		c.DefaultProfiles = make(map[string]string)
	}
	c.DefaultProfiles[provider] = profile
}

// GetDefault returns the default profile for a provider.
func (c *Config) GetDefault(provider string) string {
	if c.DefaultProfiles == nil {
		return ""
	}
	return c.DefaultProfiles[provider]
}

// AddPassthrough adds a passthrough path.
func (c *Config) AddPassthrough(path string) {
	for _, p := range c.Passthroughs {
		if p == path {
			return
		}
	}
	c.Passthroughs = append(c.Passthroughs, path)
}

// RemovePassthrough removes a passthrough path.
func (c *Config) RemovePassthrough(path string) {
	for i, p := range c.Passthroughs {
		if p == path {
			c.Passthroughs = append(c.Passthroughs[:i], c.Passthroughs[i+1:]...)
			return
		}
	}
}

// ProfileKey returns the key used for alias storage (provider/profile).
func ProfileKey(provider, profile string) string {
	return provider + "/" + profile
}

// AddAlias adds an alias for a profile.
func (c *Config) AddAlias(provider, profile, alias string) {
	if c.Aliases == nil {
		c.Aliases = make(map[string][]string)
	}
	key := ProfileKey(provider, profile)
	// Check if alias already exists
	for _, a := range c.Aliases[key] {
		if a == alias {
			return
		}
	}
	c.Aliases[key] = append(c.Aliases[key], alias)
}

// RemoveAlias removes an alias.
func (c *Config) RemoveAlias(alias string) bool {
	if c.Aliases == nil {
		return false
	}
	for key, aliases := range c.Aliases {
		for i, a := range aliases {
			if a == alias {
				c.Aliases[key] = append(aliases[:i], aliases[i+1:]...)
				if len(c.Aliases[key]) == 0 {
					delete(c.Aliases, key)
				}
				return true
			}
		}
	}
	return false
}

// GetAliases returns all aliases for a profile.
func (c *Config) GetAliases(provider, profile string) []string {
	if c.Aliases == nil {
		return nil
	}
	return c.Aliases[ProfileKey(provider, profile)]
}

// ResolveAlias resolves an alias to its profile key.
// Returns provider, profile, found.
func (c *Config) ResolveAlias(alias string) (string, string, bool) {
	if c.Aliases == nil {
		return "", "", false
	}
	for key, aliases := range c.Aliases {
		for _, a := range aliases {
			if a == alias {
				// Parse key "provider/profile"
				for i := 0; i < len(key); i++ {
					if key[i] == '/' {
						return key[:i], key[i+1:], true
					}
				}
			}
		}
	}
	return "", "", false
}

// ResolveAliasForProvider resolves an alias within a specific provider.
// Returns profile name if found, empty string otherwise.
func (c *Config) ResolveAliasForProvider(provider, alias string) string {
	if c.Aliases == nil {
		return ""
	}
	for key, aliases := range c.Aliases {
		// Check if key starts with provider/
		prefix := provider + "/"
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			for _, a := range aliases {
				if a == alias {
					return key[len(prefix):]
				}
			}
		}
	}
	return ""
}

// SetFavorites sets the favorite profiles for a provider.
func (c *Config) SetFavorites(provider string, profiles []string) {
	if c.Favorites == nil {
		c.Favorites = make(map[string][]string)
	}
	c.Favorites[provider] = profiles
}

// GetFavorites returns the favorite profiles for a provider.
func (c *Config) GetFavorites(provider string) []string {
	if c.Favorites == nil {
		return nil
	}
	return c.Favorites[provider]
}

// IsFavorite checks if a profile is marked as favorite.
func (c *Config) IsFavorite(provider, profile string) bool {
	favorites := c.GetFavorites(provider)
	for _, f := range favorites {
		if f == profile {
			return true
		}
	}
	return false
}

// CreateWorkspace creates or updates a workspace with the given profile mappings.
func (c *Config) CreateWorkspace(name string, profiles map[string]string) {
	if c.Workspaces == nil {
		c.Workspaces = make(map[string]map[string]string)
	}
	c.Workspaces[name] = profiles
}

// DeleteWorkspace removes a workspace.
func (c *Config) DeleteWorkspace(name string) bool {
	if c.Workspaces == nil {
		return false
	}
	if _, exists := c.Workspaces[name]; !exists {
		return false
	}
	delete(c.Workspaces, name)
	// Clear current workspace if it was deleted
	if c.CurrentWorkspace == name {
		c.CurrentWorkspace = ""
	}
	return true
}

// GetWorkspace returns the profile mappings for a workspace.
func (c *Config) GetWorkspace(name string) map[string]string {
	if c.Workspaces == nil {
		return nil
	}
	return c.Workspaces[name]
}

// ListWorkspaces returns all workspace names.
func (c *Config) ListWorkspaces() []string {
	if c.Workspaces == nil {
		return nil
	}
	names := make([]string, 0, len(c.Workspaces))
	for name := range c.Workspaces {
		names = append(names, name)
	}
	// Sort for consistent ordering
	for i := 0; i < len(names)-1; i++ {
		for j := i + 1; j < len(names); j++ {
			if names[j] < names[i] {
				names[i], names[j] = names[j], names[i]
			}
		}
	}
	return names
}

// SetCurrentWorkspace sets the active workspace.
func (c *Config) SetCurrentWorkspace(name string) {
	c.CurrentWorkspace = name
}

// GetCurrentWorkspace returns the name of the active workspace.
func (c *Config) GetCurrentWorkspace() string {
	return c.CurrentWorkspace
}

// FuzzyMatch finds profiles that match the given query using fuzzy matching.
// It checks aliases first, then profile name prefixes, then substring matches.
// Returns matching profile names sorted by match quality.
// If there's an exact match (profile name or alias), returns only that profile.
func (c *Config) FuzzyMatch(provider, query string, profiles []string) []string {
	if query == "" {
		return profiles
	}

	queryLower := strings.ToLower(query)

	// First pass: check for exact matches (profile name or alias)
	// If found, return immediately with just that profile
	for _, profile := range profiles {
		profileLower := strings.ToLower(profile)

		// Exact profile name match
		if profileLower == queryLower {
			return []string{profile}
		}

		// Exact alias match
		aliases := c.GetAliases(provider, profile)
		for _, alias := range aliases {
			if strings.ToLower(alias) == queryLower {
				return []string{profile}
			}
		}
	}

	// Second pass: fuzzy matching
	type match struct {
		profile string
		score   int // Lower is better
	}
	var matches []match

	for _, profile := range profiles {
		profileLower := strings.ToLower(profile)

		// Check aliases for this profile (prefix match)
		aliases := c.GetAliases(provider, profile)
		aliasMatch := false
		for _, alias := range aliases {
			aliasLower := strings.ToLower(alias)
			if strings.HasPrefix(aliasLower, queryLower) {
				matches = append(matches, match{profile, 2})
				aliasMatch = true
				break
			}
		}
		if aliasMatch {
			continue
		}

		// Prefix match on profile name
		if strings.HasPrefix(profileLower, queryLower) {
			matches = append(matches, match{profile, 3})
			continue
		}

		// Substring match on profile name
		if strings.Contains(profileLower, queryLower) {
			matches = append(matches, match{profile, 4})
			continue
		}
	}

	// Sort by score
	for i := 0; i < len(matches)-1; i++ {
		for j := i + 1; j < len(matches); j++ {
			if matches[j].score < matches[i].score {
				matches[i], matches[j] = matches[j], matches[i]
			}
		}
	}

	// Extract profile names
	result := make([]string, len(matches))
	for i, m := range matches {
		result[i] = m.profile
	}
	return result
}
