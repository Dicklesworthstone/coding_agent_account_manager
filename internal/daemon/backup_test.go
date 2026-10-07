package daemon

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
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
	scheduler, authPath := newScheduledBackupFixture(t, 10)
	manualOptions := bundle.DefaultExportOptions()
	manualOptions.OutputDir = scheduler.config.Location
	manual, err := (&bundle.VaultExporter{VaultPath: scheduler.vault}).Export(manualOptions)
	if err != nil {
		t.Fatal(err)
	}
	manualBytes := readBackupTestFile(t, manual.OutputPath)
	var paths []string
	for i := 0; i < 7; i++ {
		writeBackupTestFile(t, authPath, []byte(fmt.Sprintf(`{"OPENAI_API_KEY":"synthetic-account-%d"}`, i)))
		paths = append(paths, createDueBackup(t, scheduler))
	}

	// Ownership survives restart. Retention must ignore the older manual
	// export and preserve the newest five actual, recoverable archives.
	restarted := NewBackupScheduler(scheduler.config, scheduler.vault, newTestLogger())
	if err := restarted.LoadState(); err != nil {
		t.Fatal(err)
	}
	scheduler = restarted
	scheduler.config.KeepLast = 5
	if err := scheduler.RotateBackups(); err != nil {
		t.Fatalf("RotateBackups() error = %v", err)
	}
	backups, err := scheduler.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups() error = %v", err)
	}

	if len(backups) != 5 {
		t.Errorf("len(backups) = %d, want 5", len(backups))
	}

	for i, path := range paths {
		_, err := os.Stat(path)
		if i < 2 && !os.IsNotExist(err) {
			t.Errorf("old automatic backup remains: %s (%v)", path, err)
		}
		if i >= 2 && err != nil {
			t.Errorf("retained automatic backup missing: %s (%v)", path, err)
		}
	}
	if !bytes.Equal(readBackupTestFile(t, manual.OutputPath), manualBytes) {
		t.Fatal("retention changed the manual export")
	}
	if backups[0].Path != paths[len(paths)-1] {
		t.Fatalf("newest backup = %s, want %s", backups[0].Path, paths[len(paths)-1])
	}

	restoreRoot := t.TempDir()
	result, err := (&bundle.VaultImporter{BundlePath: backups[0].Path}).Import(&bundle.ImportOptions{
		Mode:         bundle.ImportModeReplace,
		VaultPath:    filepath.Join(restoreRoot, "vault"),
		ConfigPath:   filepath.Join(restoreRoot, "config.json"),
		SkipProjects: true,
		SkipHealth:   true,
		SkipDatabase: true,
		SkipSync:     true,
	})
	if err != nil {
		t.Fatalf("restore scheduled backup: %v", err)
	}
	if result.NewProfiles != 1 || !result.VerificationResult.Valid || len(result.Errors) != 0 {
		t.Fatalf("restore result = %+v", result)
	}
	if got := readBackupTestFile(t, filepath.Join(restoreRoot, "vault", "codex", "work", "auth.json")); !bytes.Equal(got, readBackupTestFile(t, authPath)) {
		t.Fatal("restored credentials differ from the backed-up account")
	}
	if got := readBackupTestFile(t, filepath.Join(restoreRoot, "config.json")); !bytes.Equal(got, readBackupTestFile(t, config.ConfigPath())) {
		t.Fatal("restored configuration differs from the backup source")
	}
}

func TestBackupScheduler_ListBackups(t *testing.T) {
	scheduler, _ := newScheduledBackupFixture(t, 10)
	first := createDueBackup(t, scheduler)
	second := createDueBackup(t, scheduler)
	third := createDueBackup(t, scheduler)
	// Filesystem modification time must not change publication order.
	future := time.Now().Add(24 * time.Hour)
	if err := os.Chtimes(first, future, future); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"caam_export_2025-01-01_1200.zip", "caam_export_notes.zip", "other.txt", "caam_auto_backup_unfinished.zip"} {
		writeBackupTestFile(t, filepath.Join(scheduler.config.Location, name), []byte("preserve this file"))
	}
	backups, err := scheduler.ListBackups()
	if err != nil {
		t.Fatalf("ListBackups() error = %v", err)
	}

	if len(backups) != 3 || backups[0].Path != third || backups[1].Path != second || backups[2].Path != first {
		t.Fatalf("listed backups = %+v", backups)
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
	scheduler, _ := newScheduledBackupFixture(t, 0)
	for i := 0; i < 7; i++ {
		createDueBackup(t, scheduler)
	}
	backups, err := scheduler.ListBackups()
	if err != nil {
		t.Fatal(err)
	}
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

func newScheduledBackupFixture(t *testing.T, keep int) (*BackupScheduler, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	vault := filepath.Join(root, "vault")
	authPath := filepath.Join(vault, "codex", "work", "auth.json")
	writeBackupTestFile(t, authPath, []byte(`{"OPENAI_API_KEY":"synthetic-backup-account"}`))
	writeBackupTestFile(t, config.ConfigPath(), []byte(`{"default_provider":"codex"}`))
	cfg := &config.BackupConfig{
		Enabled:  true,
		Interval: config.Duration(24 * time.Hour),
		KeepLast: keep,
		Location: filepath.Join(root, "backups"),
	}
	return NewBackupScheduler(cfg, vault, newTestLogger()), authPath
}

func writeBackupTestFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readBackupTestFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func createDueBackup(t *testing.T, scheduler *BackupScheduler) string {
	t.Helper()
	scheduler.mu.Lock()
	scheduler.state.LastBackup = time.Time{}
	scheduler.mu.Unlock()
	path, err := scheduler.CreateBackup()
	if err != nil {
		t.Fatalf("CreateBackup() error = %v", err)
	}
	if path == "" || !strings.HasPrefix(filepath.Base(path), automaticBackupPrefix) {
		t.Fatalf("automatic backup path = %q", path)
	}
	return path
}

func TestBackupScheduler_RetentionPreservesUntrustedEntries(t *testing.T) {
	for _, kind := range []string{
		"unrecorded", "legacy name", "invalid name", "invalid timestamp", "zero timestamp",
		"relative path", "unclean path", "outside location", "invalid checksum", "checksum mismatch",
		"size mismatch", "changed archive", "malformed archive with matching checksum", "duplicate record",
		"incomplete record", "symlink", "directory",
	} {
		t.Run(kind, func(t *testing.T) {
			scheduler, _ := newScheduledBackupFixture(t, 10)
			protected := createDueBackup(t, scheduler)
			older := createDueBackup(t, scheduler)
			newest := createDueBackup(t, scheduler)
			record := &scheduler.state.OwnedBackups[0]
			switch kind {
			case "unrecorded":
				scheduler.state.OwnedBackups = scheduler.state.OwnedBackups[1:]
			case "legacy name", "invalid name":
				name := "caam_export_2000-01-01_0000.zip"
				if kind == "invalid name" {
					name = automaticBackupPrefix + "unfinished.zip"
				}
				path := filepath.Join(scheduler.config.Location, name)
				if err := os.Rename(protected, path); err != nil {
					t.Fatal(err)
				}
				protected, record.Path = path, path
			case "invalid timestamp":
				record.CreatedAt = record.CreatedAt.Add(time.Hour)
			case "zero timestamp":
				record.CreatedAt = time.Time{}
			case "relative path":
				record.Path = filepath.Base(record.Path)
			case "unclean path":
				record.Path = filepath.Dir(record.Path) + string(os.PathSeparator) + "." + string(os.PathSeparator) + filepath.Base(record.Path)
			case "outside location":
				path := filepath.Join(t.TempDir(), filepath.Base(protected))
				writeBackupTestFile(t, path, readBackupTestFile(t, protected))
				protected, record.Path = path, path
			case "invalid checksum":
				record.SHA256 = "not-a-checksum"
			case "checksum mismatch":
				record.SHA256 = strings.Repeat("0", 64)
			case "size mismatch":
				record.Size++
			case "changed archive":
				data := readBackupTestFile(t, protected)
				data[0] ^= 0xff
				writeBackupTestFile(t, protected, data)
			case "malformed archive with matching checksum":
				data := []byte("incomplete archive")
				writeBackupTestFile(t, protected, data)
				record.Size = int64(len(data))
				checksum, err := bundle.ComputeDataChecksum(data, bundle.AlgorithmSHA256)
				if err != nil {
					t.Fatal(err)
				}
				record.SHA256 = checksum
			case "duplicate record":
				scheduler.state.OwnedBackups = append(scheduler.state.OwnedBackups, *record)
			case "incomplete record":
				*record = BackupRecord{Path: protected}
			case "symlink", "directory":
				saved := protected + ".preserved"
				if err := os.Rename(protected, saved); err != nil {
					t.Fatal(err)
				}
				if kind == "symlink" {
					if err := os.Symlink(saved, protected); err != nil {
						t.Skipf("symlink unavailable: %v", err)
					}
				} else if err := os.Mkdir(protected, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.Lstat(protected)
			if err != nil {
				t.Fatal(err)
			}
			var beforeBytes []byte
			if !before.IsDir() {
				beforeBytes = readBackupTestFile(t, protected)
			}
			if err := scheduler.SaveState(); err != nil {
				t.Fatal(err)
			}
			restarted := NewBackupScheduler(scheduler.config, scheduler.vault, newTestLogger())
			if err := restarted.LoadState(); err != nil {
				t.Fatal(err)
			}
			restarted.config.KeepLast = 1
			if err := restarted.RotateBackups(); err != nil {
				t.Fatal(err)
			}
			after, err := os.Lstat(protected)
			if err != nil || !os.SameFile(before, after) || before.Mode().Type() != after.Mode().Type() {
				t.Fatalf("untrusted entry was removed or replaced: %v", err)
			}
			if beforeBytes != nil && !bytes.Equal(beforeBytes, readBackupTestFile(t, protected)) {
				t.Fatal("untrusted entry contents changed")
			}
			if _, err := os.Stat(older); !os.IsNotExist(err) {
				t.Fatalf("older verified backup was not pruned: %v", err)
			}
			backups, err := restarted.ListBackups()
			if err != nil || len(backups) != 1 || backups[0].Path != newest {
				t.Fatalf("verified retained backups = %+v, error = %v", backups, err)
			}
		})
	}
}

func TestBackupScheduler_LostOrMalformedOwnershipPreservesArchives(t *testing.T) {
	for _, data := range []string{"", `{invalid`, `{"backup_count":10}`, `{"owned_backups":[{"path":"not-an-owned-backup"}]}`} {
		t.Run(fmt.Sprintf("state-%q", data), func(t *testing.T) {
			scheduler, _ := newScheduledBackupFixture(t, 10)
			paths := []string{createDueBackup(t, scheduler), createDueBackup(t, scheduler)}
			before := [][]byte{readBackupTestFile(t, paths[0]), readBackupTestFile(t, paths[1])}
			if data == "" {
				if err := os.Rename(scheduler.statePath(), scheduler.statePath()+".preserved"); err != nil {
					t.Fatal(err)
				}
			} else {
				writeBackupTestFile(t, scheduler.statePath(), []byte(data))
			}
			restarted := NewBackupScheduler(scheduler.config, scheduler.vault, newTestLogger())
			loadErr := restarted.LoadState()
			if (data == "{invalid") != (loadErr != nil) {
				t.Fatalf("LoadState() = %v", loadErr)
			}
			restarted.config.KeepLast = 1
			if err := restarted.RotateBackups(); err != nil {
				t.Fatal(err)
			}
			for i, path := range paths {
				if !bytes.Equal(before[i], readBackupTestFile(t, path)) {
					t.Fatalf("archive changed without valid ownership: %s", path)
				}
			}
		})
	}
}

func TestBackupScheduler_FailedBackupNeverPrunes(t *testing.T) {
	for _, failure := range []string{"export", "ownership save"} {
		t.Run(failure, func(t *testing.T) {
			scheduler, _ := newScheduledBackupFixture(t, 10)
			paths := []string{createDueBackup(t, scheduler), createDueBackup(t, scheduler)}
			manualPath := filepath.Join(scheduler.config.Location, "caam_export_2000-01-01_0000.zip")
			writeBackupTestFile(t, manualPath, []byte("manual archive"))
			paths = append(paths, manualPath)
			before := make(map[string][]byte)
			for _, path := range paths {
				before[path] = readBackupTestFile(t, path)
			}
			if failure == "export" {
				scheduler.vault = filepath.Join(t.TempDir(), "missing-vault")
			} else {
				if err := os.Rename(scheduler.statePath(), scheduler.statePath()+".preserved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(scheduler.statePath(), 0700); err != nil {
					t.Fatal(err)
				}
			}
			scheduler.config.KeepLast = 1
			scheduler.state.LastBackup = time.Time{}
			published, err := scheduler.CreateBackup()
			if err == nil {
				t.Fatal("expected backup failure")
			}
			if failure == "export" && published != "" {
				t.Fatalf("failed export published %s", published)
			}
			if failure == "ownership save" && published == "" {
				t.Fatal("complete archive should remain after ownership save failure")
			}
			state := scheduler.GetState()
			if state.BackupCount != 2 || len(state.OwnedBackups) != 2 {
				t.Fatalf("failed backup acquired retention ownership: %+v", state)
			}
			for _, record := range state.OwnedBackups {
				if record.Path == published {
					t.Fatal("uncommitted archive counted toward retention")
				}
			}
			for path, data := range before {
				if !bytes.Equal(data, readBackupTestFile(t, path)) {
					t.Fatalf("failed backup changed prior archive %s", path)
				}
			}
			entries, err := os.ReadDir(scheduler.config.Location)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".caam-auto-backup-") {
					t.Errorf("staging directory leaked: %s", entry.Name())
				}
			}
			if scheduler.GetState().LastError == "" {
				t.Fatal("backup failure missing from status")
			}
		})
	}
}

func TestPublishScheduledBackupDoesNotOverwriteCollisions(t *testing.T) {
	for _, existing := range []string{"file", "directory", "symlink"} {
		t.Run(existing, func(t *testing.T) {
			root := t.TempDir()
			staged := filepath.Join(root, "staged.zip")
			writeBackupTestFile(t, staged, []byte("completed staged archive"))
			created := time.Date(2026, time.October, 7, 12, 30, 0, 0, time.UTC)
			collision := filepath.Join(root, automaticBackupPrefix+created.Format(automaticBackupTimeFormat)+"_"+strings.Repeat("0", 32)+".zip")
			switch existing {
			case "file":
				writeBackupTestFile(t, collision, []byte("preserve the existing archive"))
			case "directory":
				if err := os.Mkdir(collision, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				target := filepath.Join(root, "manual.zip")
				writeBackupTestFile(t, target, []byte("preserve manual target"))
				if err := os.Symlink(target, collision); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			before, err := os.Lstat(collision)
			if err != nil {
				t.Fatal(err)
			}
			var original []byte
			if !before.IsDir() {
				original = readBackupTestFile(t, collision)
			}
			// Force the first candidate to collide, then provide a different ID
			// at exactly the same timestamp. Both operations use the real filesystem.
			entropy := append(make([]byte, 16), bytes.Repeat([]byte{1}, 16)...)
			published, err := publishScheduledBackup(staged, root, created, bytes.NewReader(entropy))
			if err != nil || published == collision {
				t.Fatalf("publish = %q, error = %v", published, err)
			}
			if !bytes.Equal(readBackupTestFile(t, published), readBackupTestFile(t, staged)) {
				t.Fatal("published archive differs from completed staging")
			}
			if path, err := publishScheduledBackup(staged, root, created, bytes.NewReader(make([]byte, 16*16))); err == nil || path != "" {
				t.Fatalf("exhausted collisions = %q, %v", path, err)
			}
			after, err := os.Lstat(collision)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("colliding entry replaced: %v", err)
			}
			if original != nil && !bytes.Equal(original, readBackupTestFile(t, collision)) {
				t.Fatal("colliding entry content changed")
			}
		})
	}
}

func TestBackupScheduler_ConcurrentCreationPublishesOnce(t *testing.T) {
	scheduler, _ := newScheduledBackupFixture(t, 1)
	var wg sync.WaitGroup
	type outcome struct {
		path string
		err  error
	}
	results := make(chan outcome, 6)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			path, err := scheduler.CreateBackup()
			results <- outcome{path: path, err: err}
		}()
	}
	wg.Wait()
	close(results)
	created := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.path != "" {
			created++
		}
	}
	if created != 1 || scheduler.GetState().BackupCount != 1 {
		t.Fatalf("concurrent calls created %d backups, state = %+v", created, scheduler.GetState())
	}
	state := scheduler.GetState()
	state.OwnedBackups[0].SHA256 = "changed snapshot"
	if scheduler.GetState().OwnedBackups[0].SHA256 == "changed snapshot" {
		t.Fatal("GetState leaked mutable ownership records")
	}
	var saved BackupState
	if err := json.Unmarshal(readBackupTestFile(t, scheduler.statePath()), &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.OwnedBackups) != 1 || saved.OwnedBackups[0].Path != state.OwnedBackups[0].Path {
		t.Fatalf("persisted ownership = %+v", saved)
	}
}

func TestBackupScheduler_RetentionUsesPublicationOrder(t *testing.T) {
	for _, backwards := range []bool{false, true} {
		t.Run(fmt.Sprintf("clock-moved-backwards-%t", backwards), func(t *testing.T) {
			scheduler, _ := newScheduledBackupFixture(t, 10)
			source := createDueBackup(t, scheduler)
			data := readBackupTestFile(t, source)
			template := scheduler.GetState().OwnedBackups[0]
			created := time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)
			var publications []string
			for _, id := range []byte{0xff, 0x01} {
				// The older archive sorts later by random ID; publication order
				// still determines retention when timestamps collide or go back.
				if backwards && len(publications) > 0 {
					created = created.Add(-time.Hour)
				}
				path, err := publishScheduledBackup(source, scheduler.config.Location, created, bytes.NewReader(bytes.Repeat([]byte{id}, 16)))
				if err != nil {
					t.Fatal(err)
				}
				record := template
				record.Path = path
				record.CreatedAt = created
				scheduler.state.OwnedBackups = append(scheduler.state.OwnedBackups, record)
				publications = append(publications, path)
			}
			if err := scheduler.SaveState(); err != nil {
				t.Fatal(err)
			}
			scheduler.config.KeepLast = 1
			if err := scheduler.RotateBackups(); err != nil {
				t.Fatal(err)
			}
			for _, old := range []string{source, publications[0]} {
				if _, err := os.Stat(old); !os.IsNotExist(err) {
					t.Fatalf("older publication remains: %s (%v)", old, err)
				}
			}
			if !bytes.Equal(data, readBackupTestFile(t, publications[1])) {
				t.Fatal("newest publication was not retained")
			}
		})
	}
}
