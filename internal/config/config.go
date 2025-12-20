// Package config manages global caam configuration.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
}

// DefaultConfig returns the default configuration.
func DefaultConfig() *Config {
	return &Config{
		DefaultProvider: "codex",
		DefaultProfiles: make(map[string]string),
		AutoLock:        true,
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
