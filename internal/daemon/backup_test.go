package daemon

import (
	"fmt"
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

	oldCaamHome := os.Getenv("CAAM_HOME")
	oldXDGData := os.Getenv("XDG_DATA_HOME")
	os.Setenv("CAAM_HOME", tmpDir)
	os.Unsetenv("XDG_DATA_HOME")
	defer func() {
		os.Setenv("CAAM_HOME", oldCaamHome)
		os.Setenv("XDG_DATA_HOME", oldXDGData)
	}()

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

func TestBackupScheduler_RecordError(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.BackupConfig{
		Enabled:  true,
		Location: filepath.Join(tmpDir, "backups"),
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	// Record an error
	testErr := os.ErrPermission
	scheduler.recordError(testErr)

	state := scheduler.GetState()
	if state.LastError == "" {
		t.Error("LastError should be set after recordError")
	}
	if state.LastErrorTime.IsZero() {
		t.Error("LastErrorTime should be set after recordError")
	}
}

func TestBackupScheduler_CreateBackup_NotDue(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.BackupConfig{
		Enabled:  true,
		Interval: config.Duration(24 * time.Hour),
		Location: filepath.Join(tmpDir, "backups"),
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	// Set last backup to recent time so it's not due
	scheduler.state.LastBackup = time.Now().Add(-1 * time.Hour)

	path, err := scheduler.CreateBackup()
	if err != nil {
		t.Fatalf("CreateBackup() error = %v", err)
	}
	if path != "" {
		t.Error("CreateBackup() should return empty path when not due")
	}
}

func TestBackupScheduler_CreateBackup_Disabled(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.BackupConfig{
		Enabled: false,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	path, err := scheduler.CreateBackup()
	if err != nil {
		t.Fatalf("CreateBackup() error = %v", err)
	}
	if path != "" {
		t.Error("CreateBackup() should return empty path when disabled")
	}
}

func TestBackupScheduler_LoadState_NoFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.BackupConfig{
		Enabled: true,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	// Load state when no file exists - should succeed with empty state
	err := scheduler.LoadState()
	if err != nil {
		t.Errorf("LoadState() error = %v, want nil", err)
	}

	state := scheduler.GetState()
	if state.BackupCount != 0 {
		t.Errorf("BackupCount = %d, want 0", state.BackupCount)
	}
}

func TestBackupScheduler_LoadState_InvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	cfg := &config.BackupConfig{
		Enabled: true,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	// Create invalid JSON file
	t.Setenv("CAAM_HOME", "")
	stateDir := filepath.Join(config.DefaultDataPath())
	os.MkdirAll(stateDir, 0700)
	statePath := filepath.Join(stateDir, "backup_state.json")
	os.WriteFile(statePath, []byte("{invalid json"), 0600)
	defer os.Remove(statePath)

	err := scheduler.LoadState()
	if err == nil {
		t.Error("LoadState() should error on invalid JSON")
	}
}

func TestBackupScheduler_ListBackups_NoDir(t *testing.T) {
	cfg := &config.BackupConfig{
		Enabled:  true,
		Location: "/nonexistent/path",
	}

	scheduler := NewBackupScheduler(cfg, t.TempDir(), newTestLogger())

	backups, err := scheduler.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups() error = %v", err)
	}
	if len(backups) != 0 {
		t.Errorf("len(backups) = %d, want 0", len(backups))
	}
}

func TestBackupScheduler_RotateBackups_DefaultKeepLast(t *testing.T) {
	tmpDir := t.TempDir()
	backupDir := filepath.Join(tmpDir, "backups")
	os.MkdirAll(backupDir, 0700)

	// Create test backup files with proper names
	for i := 1; i <= 10; i++ {
		name := filepath.Join(backupDir, fmt.Sprintf("caam_export_2025-02-%02d_1200.zip", i))
		os.WriteFile(name, []byte("test"), 0600)
	}

	cfg := &config.BackupConfig{
		Enabled:  true,
		KeepLast: 0, // 0 means use default (5)
		Location: backupDir,
	}

	scheduler := NewBackupScheduler(cfg, filepath.Join(tmpDir, "vault"), newTestLogger())

	// Should delete older backups, keeping default (5)
	if err := scheduler.RotateBackups(); err != nil {
		t.Fatalf("RotateBackups() error = %v", err)
	}

	backups, _ := scheduler.ListBackups()
	// GetKeepLast() returns 5 when KeepLast is 0 (default)
	if len(backups) != 5 {
		t.Errorf("len(backups) = %d, want 5", len(backups))
	}
}

func TestBackupScheduler_RotateBackups_NoDir(t *testing.T) {
	cfg := &config.BackupConfig{
		Enabled:  true,
		KeepLast: 5,
		Location: "/nonexistent/path",
	}

	scheduler := NewBackupScheduler(cfg, t.TempDir(), newTestLogger())

	// Should not error on nonexistent directory
	err := scheduler.RotateBackups()
	if err != nil {
		t.Errorf("RotateBackups() error = %v, want nil", err)
	}
}

func TestReadBackupStatus(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CAAM_HOME", tmpDir)

	cfg := config.BackupConfig{Enabled: true, Interval: config.Duration(24 * time.Hour), KeepLast: 3, Location: filepath.Join(tmpDir, "backups")}

	// No state yet: enabled, never run, due immediately.
	st, err := ReadBackupStatus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.IntervalText != "1d" || st.IntervalSeconds != 86400 || st.KeepLast != 3 || !st.LastBackup.IsZero() || st.NextBackup.IsZero() {
		t.Fatalf("fresh status = %+v", st)
	}

	last := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	s := NewBackupScheduler(&cfg, filepath.Join(tmpDir, "vault"), newTestLogger())
	s.state = BackupState{LastBackup: last, LastBackupPath: "/b/1.tar.gz", BackupCount: 4, LastError: "disk full", LastErrorTime: last}
	if err := s.SaveState(); err != nil {
		t.Fatal(err)
	}

	st, err = ReadBackupStatus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !st.LastBackup.Equal(last) || st.BackupCount != 4 || st.LastError != "disk full" || !st.NextBackup.Equal(last.Add(24*time.Hour)) {
		t.Fatalf("status = %+v", st)
	}

	cfg.Enabled = false
	st, err = ReadBackupStatus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if st.Enabled || !st.NextBackup.IsZero() || st.BackupCount != 4 {
		t.Fatalf("disabled status = %+v", st)
	}
}

func TestFormatBackupInterval(t *testing.T) {
	for d, want := range map[time.Duration]string{
		7 * 24 * time.Hour: "7d",
		12 * time.Hour:     "12h",
		90 * time.Minute:   "1h30m0s",
	} {
		if got := formatBackupInterval(d); got != want {
			t.Errorf("formatBackupInterval(%v) = %q, want %q", d, got, want)
		}
	}
}
