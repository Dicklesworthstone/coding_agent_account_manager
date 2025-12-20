// Package config backup configuration for automatic backup scheduling.
package config

import (
	"os"
	"path/filepath"
	"time"
)

// BackupConfig holds configuration for automatic backup scheduling.
type BackupConfig struct {
	// Enabled controls whether automatic backups are enabled.
	// Default: false (opt-in)
	Enabled bool `json:"enabled"`

	// Interval is how often to create automatic backups.
	// Default: 7 days (168h)
	Interval Duration `json:"interval"`

	// KeepLast is the number of backups to retain.
	// Older backups are deleted when this limit is exceeded.
	// Default: 5
	KeepLast int `json:"keep_last"`

	// Location is the directory where backups are stored.
	// Default: ~/.caam-backups
	Location string `json:"location,omitempty"`
}

// DefaultBackupConfig returns a BackupConfig with sensible defaults.
func DefaultBackupConfig() BackupConfig {
	return BackupConfig{
		Enabled:  false,
		Interval: Duration(7 * 24 * time.Hour), // 7 days
		KeepLast: 5,
		Location: defaultBackupLocation(),
	}
}

// defaultBackupLocation returns the default backup directory.
func defaultBackupLocation() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".caam-backups")
	}
	return filepath.Join(homeDir, ".caam-backups")
}

// GetLocation returns the backup location, using the default if not set.
func (c *BackupConfig) GetLocation() string {
	if c.Location == "" {
		return defaultBackupLocation()
	}
	return c.Location
}

// GetInterval returns the backup interval as time.Duration.
func (c *BackupConfig) GetInterval() time.Duration {
	if c.Interval <= 0 {
		return 7 * 24 * time.Hour // Default 7 days
	}
	return c.Interval.Duration()
}

// GetKeepLast returns the number of backups to keep.
func (c *BackupConfig) GetKeepLast() int {
	if c.KeepLast <= 0 {
		return 5 // Default
	}
	return c.KeepLast
}

// IsEnabled returns whether automatic backups are enabled.
func (c *BackupConfig) IsEnabled() bool {
	return c.Enabled
}
