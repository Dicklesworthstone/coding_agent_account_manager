// Package daemon backup provides automatic vault backup scheduling.
package daemon

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

// BackupState tracks the state of automatic backups.
type BackupState struct {
	// LastBackup is when the last automatic backup was created.
	LastBackup time.Time `json:"last_backup,omitempty"`

	// LastBackupPath is the path to the last backup created.
	LastBackupPath string `json:"last_backup_path,omitempty"`

	// BackupCount is the total number of backups created.
	BackupCount int64 `json:"backup_count"`

	// LastError is the last error encountered during backup.
	LastError string `json:"last_error,omitempty"`

	// LastErrorTime is when the last error occurred.
	LastErrorTime time.Time `json:"last_error_time,omitempty"`
}

// BackupScheduler manages automatic backup scheduling.
type BackupScheduler struct {
	config *config.BackupConfig
	vault  string // Path to the vault to backup
	state  BackupState
	logger interface {
		Printf(format string, v ...interface{})
		Println(v ...interface{})
	}
}

// NewBackupScheduler creates a new backup scheduler.
func NewBackupScheduler(cfg *config.BackupConfig, vaultPath string, logger interface {
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}) *BackupScheduler {
	return &BackupScheduler{
		config: cfg,
		vault:  vaultPath,
		logger: logger,
	}
}

// LoadState loads the backup state from disk.
func (s *BackupScheduler) LoadState() error {
	statePath := s.statePath()
	data, err := os.ReadFile(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file yet, start fresh
		}
		return fmt.Errorf("read backup state: %w", err)
	}

	if err := json.Unmarshal(data, &s.state); err != nil {
		return fmt.Errorf("parse backup state: %w", err)
	}

	return nil
}

// SaveState persists the backup state to disk.
func (s *BackupScheduler) SaveState() error {
	statePath := s.statePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return fmt.Errorf("create backup state dir: %w", err)
	}

	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal backup state: %w", err)
	}

	// Atomic write
	tmpPath := statePath + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp backup state file: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp backup state file: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp backup state file: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp backup state file: %w", err)
	}

	if err := os.Rename(tmpPath, statePath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename backup state file: %w", err)
	}

	return nil
}

// statePath returns the path to the state file.
func (s *BackupScheduler) statePath() string {
	return filepath.Join(config.DefaultDataPath(), "backup_state.json")
}

// ShouldBackup returns true if a backup should be created now.
func (s *BackupScheduler) ShouldBackup() bool {
	if !s.config.IsEnabled() {
		return false
	}

	// No previous backup - should create one
	if s.state.LastBackup.IsZero() {
		return true
	}

	// Check if interval has elapsed
	elapsed := time.Since(s.state.LastBackup)
	return elapsed >= s.config.GetInterval()
}

// NextBackupTime returns when the next backup is scheduled.
// Returns zero time if backups are disabled.
func (s *BackupScheduler) NextBackupTime() time.Time {
	if !s.config.IsEnabled() {
		return time.Time{}
	}

	if s.state.LastBackup.IsZero() {
		return time.Now() // Immediately
	}

	return s.state.LastBackup.Add(s.config.GetInterval())
}

// TimeUntilNextBackup returns the duration until the next backup.
// Returns 0 if a backup should happen now.
// Returns -1 if backups are disabled.
func (s *BackupScheduler) TimeUntilNextBackup() time.Duration {
	if !s.config.IsEnabled() {
		return -1
	}

	next := s.NextBackupTime()
	if next.IsZero() {
		return 0
	}

	remaining := time.Until(next)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// CreateBackup creates a new backup if needed.
// Returns the backup path if created, empty string if not needed.
func (s *BackupScheduler) CreateBackup() (string, error) {
	if !s.ShouldBackup() {
		return "", nil
	}

	location := s.config.GetLocation()

	// Ensure backup directory exists
	if err := os.MkdirAll(location, 0700); err != nil {
		s.recordError(fmt.Errorf("create backup dir: %w", err))
		return "", fmt.Errorf("create backup dir: %w", err)
	}

	// Create the backup using the bundle package
	exporter := &bundle.VaultExporter{
		VaultPath:    s.vault,
		DataPath:     config.DefaultDataPath(),
		ConfigPath:   config.ConfigPath(),
		ProjectsPath: filepath.Join(config.DefaultDataPath(), "projects.json"),
		HealthPath:   filepath.Join(config.DefaultDataPath(), "health"),
		DatabasePath: filepath.Join(config.DefaultDataPath(), "caam.db"),
		SyncPath:     filepath.Join(config.DefaultDataPath(), "sync"),
	}

	opts := bundle.DefaultExportOptions()
	opts.OutputDir = location
	opts.IncludeConfig = true
	opts.IncludeProjects = true
	opts.IncludeHealth = true
	opts.IncludeDatabase = true
	opts.IncludeSyncConfig = true

	result, err := exporter.Export(opts)
	if err != nil {
		s.recordError(fmt.Errorf("create backup: %w", err))
		return "", fmt.Errorf("create backup: %w", err)
	}

	backupPath := result.OutputPath

	// Update state
	s.state.LastBackup = time.Now()
	s.state.LastBackupPath = backupPath
	s.state.BackupCount++
	s.state.LastError = ""
	s.state.LastErrorTime = time.Time{}

	if err := s.SaveState(); err != nil {
		s.logger.Printf("Warning: failed to save backup state: %v", err)
	}

	s.logger.Printf("Created automatic backup: %s", backupPath)

	// Rotate old backups
	if err := s.RotateBackups(); err != nil {
		s.logger.Printf("Warning: failed to rotate backups: %v", err)
	}

	return backupPath, nil
}

// recordError records an error in the backup state.
func (s *BackupScheduler) recordError(err error) {
	s.state.LastError = err.Error()
	s.state.LastErrorTime = time.Now()
	if saveErr := s.SaveState(); saveErr != nil {
		s.logger.Printf("Warning: failed to save backup state: %v", saveErr)
	}
}

// RotateBackups removes old backups to stay within the keep_last limit.
func (s *BackupScheduler) RotateBackups() error {
	keepLast := s.config.GetKeepLast()
	if keepLast <= 0 {
		return nil // Unlimited
	}

	location := s.config.GetLocation()

	entries, err := os.ReadDir(location)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("list backups: %w", err)
	}

	// Filter to caam backup files only
	// VaultExporter creates files with pattern: caam_export_YYYY-MM-DD_HHMM.zip
	var backups []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, "caam_export_") && strings.HasSuffix(name, ".zip") {
			backups = append(backups, name)
		}
	}

	// Already within limit?
	if len(backups) <= keepLast {
		return nil
	}

	// Sort by name (which includes timestamp, so oldest first)
	sort.Strings(backups)

	// Delete oldest until we're within limit
	toDelete := len(backups) - keepLast
	for i := 0; i < toDelete; i++ {
		backupPath := filepath.Join(location, backups[i])
		if err := os.Remove(backupPath); err != nil {
			s.logger.Printf("Warning: failed to delete old backup %s: %v", backups[i], err)
		} else {
			s.logger.Printf("Deleted old backup: %s", backups[i])
		}
	}

	return nil
}

// GetState returns a copy of the current backup state.
func (s *BackupScheduler) GetState() BackupState {
	return s.state
}

// ListBackups returns all backup files in the backup location.
func (s *BackupScheduler) ListBackups() ([]BackupInfo, error) {
	location := s.config.GetLocation()

	entries, err := os.ReadDir(location)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list backups: %w", err)
	}

	// VaultExporter creates files with pattern: caam_export_YYYY-MM-DD_HHMM.zip
	var backups []BackupInfo
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasPrefix(name, "caam_export_") || !strings.HasSuffix(name, ".zip") {
			continue
		}

		info, err := e.Info()
		if err != nil {
			continue
		}

		backups = append(backups, BackupInfo{
			Name:      name,
			Path:      filepath.Join(location, name),
			Size:      info.Size(),
			CreatedAt: info.ModTime(),
		})
	}

	// Sort by creation time, newest first
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})

	return backups, nil
}

// BackupInfo contains information about a backup file.
type BackupInfo struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}
