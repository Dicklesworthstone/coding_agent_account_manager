package daemon

import (
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

type testLogger struct {
	*log.Logger
}

func newTestLogger() *testLogger {
	return &testLogger{log.New(os.Stdout, "[test] ", 0)}
}

func TestBackupScheduler_ShouldBackup(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		lastBackup time.Time
		interval   time.Duration
		want       bool
	}{
		{
			name:    "disabled",
			enabled: false,
			want:    false,
		},
		{
			name:       "enabled, no previous backup",
			enabled:    true,
			lastBackup: time.Time{},
			interval:   24 * time.Hour,
			want:       true,
		},
		{
			name:       "enabled, backup due",
			enabled:    true,
			lastBackup: time.Now().Add(-25 * time.Hour),
			interval:   24 * time.Hour,
			want:       true,
		},
		{
			name:       "enabled, backup not due",
			enabled:    true,
			lastBackup: time.Now().Add(-1 * time.Hour),
			interval:   24 * time.Hour,
			want:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.BackupConfig{
				Enabled:  tc.enabled,
				Interval: config.Duration(tc.interval),
			}

			scheduler := NewBackupScheduler(cfg, t.TempDir(), newTestLogger())
			scheduler.state.LastBackup = tc.lastBackup

			if got := scheduler.ShouldBackup(); got != tc.want {
				t.Errorf("ShouldBackup() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBackupScheduler_NextBackupTime(t *testing.T) {
	tests := []struct {
		name       string
		enabled    bool
		lastBackup time.Time
		interval   time.Duration
		wantZero   bool
	}{
		{
			name:     "disabled returns zero",
			enabled:  false,
			wantZero: true,
		},
		{
			name:       "no previous backup returns now",
			enabled:    true,
			lastBackup: time.Time{},
			interval:   24 * time.Hour,
			wantZero:   false,
		},
		{
			name:       "with previous backup",
			enabled:    true,
			lastBackup: time.Now().Add(-12 * time.Hour),
			interval:   24 * time.Hour,
			wantZero:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.BackupConfig{
				Enabled:  tc.enabled,
				Interval: config.Duration(tc.interval),
			}

			scheduler := NewBackupScheduler(cfg, t.TempDir(), newTestLogger())
			scheduler.state.LastBackup = tc.lastBackup

			got := scheduler.NextBackupTime()
			if tc.wantZero && !got.IsZero() {
				t.Errorf("NextBackupTime() = %v, want zero time", got)
			}
			if !tc.wantZero && got.IsZero() {
				t.Error("NextBackupTime() = zero, want non-zero time")
			}
		})
	}
}

func TestBackupScheduler_TimeUntilNextBackup(t *testing.T) {
	cfg := &config.BackupConfig{
		Enabled:  true,
		Interval: config.Duration(24 * time.Hour),
	}

	scheduler := NewBackupScheduler(cfg, t.TempDir(), newTestLogger())

	// No previous backup - should be 0 (due now)
	if got := scheduler.TimeUntilNextBackup(); got != 0 {
		t.Errorf("TimeUntilNextBackup() with no previous = %v, want 0", got)
	}

	// Set last backup to 12 hours ago
	scheduler.state.LastBackup = time.Now().Add(-12 * time.Hour)
	remaining := scheduler.TimeUntilNextBackup()
	if remaining < 11*time.Hour || remaining > 13*time.Hour {
		t.Errorf("TimeUntilNextBackup() = %v, expected ~12h", remaining)
	}

	// Disabled returns -1
	cfg.Enabled = false
	if got := scheduler.TimeUntilNextBackup(); got != -1 {
		t.Errorf("TimeUntilNextBackup() when disabled = %v, want -1", got)
	}
}

func TestBackupScheduler_StatePersistence(t *testing.T) {
	tmpDir := t.TempDir()

	cfg := &config.BackupConfig{
		Enabled:  true,
		Interval: config.Duration(24 * time.Hour),
		Location: filepath.Join(tmpDir, "backups"),
	}

	// Create scheduler and set state
	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	scheduler.state.LastBackup = time.Now().Add(-1 * time.Hour)
	scheduler.state.LastBackupPath = "/test/backup.tar.gz"
	scheduler.state.BackupCount = 5

	// Override state path for testing
	statePath := filepath.Join(tmpDir, "backup_state.json")
	os.MkdirAll(filepath.Dir(statePath), 0700)

	// Save state
	if err := scheduler.SaveState(); err != nil {
		t.Fatalf("SaveState() error = %v", err)
	}

	// Create new scheduler and load state
	scheduler2 := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	if err := scheduler2.LoadState(); err != nil {
		t.Fatalf("LoadState() error = %v", err)
	}

	// State should be loaded
	state := scheduler2.GetState()
	if state.BackupCount != 5 {
		t.Errorf("BackupCount = %d, want 5", state.BackupCount)
	}
}

func TestBackupScheduler_RotateBackups(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(backupDir, 0700)

	// Create test backup files
	for i := 1; i <= 7; i++ {
		name := filepath.Join(backupDir, "caam_export_2025-01-0"+string(rune('0'+i))+"_1200.zip")
		if err := os.WriteFile(name, []byte("test"), 0600); err != nil {
			t.Fatalf("failed to create test backup: %v", err)
		}
	}

	cfg := &config.BackupConfig{
		Enabled:  true,
		KeepLast: 5,
		Location: backupDir,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	// Rotate should delete 2 oldest backups
	if err := scheduler.RotateBackups(); err != nil {
		t.Fatalf("RotateBackups() error = %v", err)
	}

	// List remaining backups
	backups, err := scheduler.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups() error = %v", err)
	}

	if len(backups) != 5 {
		t.Errorf("len(backups) = %d, want 5", len(backups))
	}

	// Verify oldest backups were deleted
	for _, b := range backups {
		if b.Name == "caam_export_2025-01-01_1200.zip" || b.Name == "caam_export_2025-01-02_1200.zip" {
			t.Errorf("backup %s should have been deleted", b.Name)
		}
	}
}

func TestBackupScheduler_ListBackups(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(backupDir, 0700)

	// Create test backup files with different timestamps
	// Pattern matches VaultExporter output: caam_export_YYYY-MM-DD_HHMM.zip
	names := []string{
		"caam_export_2025-01-15_1200.zip",
		"caam_export_2025-01-10_1200.zip",
		"caam_export_2025-01-20_1200.zip",
	}
	for i, name := range names {
		path := filepath.Join(backupDir, name)
		if err := os.WriteFile(path, []byte("test data"), 0600); err != nil {
			t.Fatalf("failed to create test backup: %v", err)
		}
		// Set different mod times so sorting works
		modTime := time.Now().Add(time.Duration(-i) * time.Hour)
		os.Chtimes(path, modTime, modTime)
	}

	// Create a non-backup file
	os.WriteFile(filepath.Join(backupDir, "other.txt"), []byte("other"), 0600)

	cfg := &config.BackupConfig{
		Enabled:  true,
		Location: backupDir,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	backups, err := scheduler.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups() error = %v", err)
	}

	if len(backups) != 3 {
		t.Errorf("len(backups) = %d, want 3", len(backups))
	}

	// Should only include backup files
	for _, b := range backups {
		if b.Name == "other.txt" {
			t.Errorf("non-backup file included: %s", b.Name)
		}
	}
}

func TestBackupScheduler_GetState(t *testing.T) {
	cfg := &config.BackupConfig{
		Enabled: true,
	}

	scheduler := NewBackupScheduler(cfg, t.TempDir(), newTestLogger())
	scheduler.state.LastBackup = time.Now()
	scheduler.state.BackupCount = 10
	scheduler.state.LastBackupPath = "/test/backup.tar.gz"

	state := scheduler.GetState()

	if state.BackupCount != 10 {
		t.Errorf("BackupCount = %d, want 10", state.BackupCount)
	}
	if state.LastBackupPath != "/test/backup.tar.gz" {
		t.Errorf("LastBackupPath = %s, want /test/backup.tar.gz", state.LastBackupPath)
	}
}
