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

	// BrowserProfile specifies a browser profile for OAuth flows.
	BrowserProfile string `json:"browser_profile,omitempty"`
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
func ConfigPath() string {
	if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
		return filepath.Join(xdgConfig, "caam", "config.json")
	}
	homeDir, _ := os.UserHomeDir()
	return filepath.Join(homeDir, ".config", "caam", "config.json")
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

	return os.WriteFile(configPath, data, 0600)
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
