// Package daemon backup provides automatic vault backup scheduling.
package daemon

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	syncstate "github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
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

	// OwnedBackups records archives successfully published by this scheduler.
	// Legacy archives without a record are never eligible for retention.
	OwnedBackups []BackupRecord `json:"owned_backups,omitempty"`
}

// BackupRecord binds automatic-backup ownership to exact, verified file bytes.
type BackupRecord struct {
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	Size      int64     `json:"size"`
	SHA256    string    `json:"sha256"`
}

const automaticBackupPrefix = "caam_auto_backup_"
const automaticBackupTimeFormat = "20060102T150405.000000000Z"

// BackupScheduler manages automatic backup scheduling.
type BackupScheduler struct {
	config *config.BackupConfig
	vault  string // Path to the vault to backup
	state  BackupState
	mu     sync.RWMutex // Protects state
	// Serialize creation and retention so concurrent calls cannot prune the
	// recovery copy while another backup is still being prepared.
	operationMu sync.Mutex
	logger      interface {
		Printf(format string, v ...interface{})
		Println(v ...interface{})
	}
}

// NewBackupScheduler creates a new backup scheduler.
func NewBackupScheduler(cfg *config.BackupConfig, vaultPath string, logger interface {
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}) *BackupScheduler {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &BackupScheduler{
		config: cfg,
		vault:  vaultPath,
		logger: logger,
	}
}

// BackupStatus summarizes scheduled backups for display outside the daemon.
type BackupStatus struct {
	Enabled         bool          `json:"enabled"`
	Interval        time.Duration `json:"-"`
	IntervalText    string        `json:"interval"`
	IntervalSeconds int64         `json:"interval_seconds"`
	KeepLast        int           `json:"keep_last"`
	Location        string        `json:"location"`
	LastBackup      time.Time     `json:"last_backup,omitzero"`
	LastBackupPath  string        `json:"last_backup_path,omitempty"`
	BackupCount     int64         `json:"backup_count"`
	NextBackup      time.Time     `json:"next_backup,omitzero"`
	LastError       string        `json:"last_error,omitempty"`
	LastErrorTime   time.Time     `json:"last_error_time,omitzero"`
}

// formatBackupInterval renders whole days or hours compactly ("7d", "12h").
func formatBackupInterval(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	default:
		return d.String()
	}
}

// ReadBackupStatus reports the backup schedule from cfg and the state the
// daemon persisted, without needing a running daemon.
func ReadBackupStatus(cfg config.BackupConfig) (BackupStatus, error) {
	s := NewBackupScheduler(&cfg, "", nil)
	err := s.LoadState()
	state := s.GetState()
	status := BackupStatus{
		Enabled:         cfg.IsEnabled(),
		Interval:        cfg.GetInterval(),
		IntervalText:    formatBackupInterval(cfg.GetInterval()),
		IntervalSeconds: int64(cfg.GetInterval() / time.Second),
		KeepLast:        cfg.GetKeepLast(),
		Location:        cfg.GetLocation(),
		LastBackup:      state.LastBackup,
		LastBackupPath:  state.LastBackupPath,
		BackupCount:     state.BackupCount,
		LastError:       state.LastError,
		LastErrorTime:   state.LastErrorTime,
	}
	if status.Enabled {
		status.NextBackup = s.NextBackupTime()
	}
	return status, err
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

	var state BackupState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parse backup state: %w", err)
	}
	s.mu.Lock()
	s.state = state
	s.mu.Unlock()

	return nil
}

// SaveState persists the backup state to disk.
func (s *BackupScheduler) SaveState() error {
	statePath := s.statePath()

	// Ensure directory exists
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		return fmt.Errorf("create backup state dir: %w", err)
	}

	s.mu.RLock()
	data, err := json.MarshalIndent(s.state, "", "  ")
	s.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal backup state: %w", err)
	}

	// Atomic write
	f, err := os.CreateTemp(filepath.Dir(statePath), ".backup_state_*")
	if err != nil {
		return fmt.Errorf("create temp backup state file: %w", err)
	}
	tmpPath := f.Name()
	defer os.Remove(tmpPath)

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

	return syncBackupDirectory(filepath.Dir(statePath))
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

	s.mu.RLock()
	lastBackup := s.state.LastBackup
	s.mu.RUnlock()

	// No previous backup - should create one
	if lastBackup.IsZero() {
		return true
	}

	// Check if interval has elapsed
	elapsed := time.Since(lastBackup)
	return elapsed >= s.config.GetInterval()
}

// NextBackupTime returns when the next backup is scheduled.
// Returns zero time if backups are disabled.
func (s *BackupScheduler) NextBackupTime() time.Time {
	if !s.config.IsEnabled() {
		return time.Time{}
	}

	s.mu.RLock()
	lastBackup := s.state.LastBackup
	s.mu.RUnlock()

	if lastBackup.IsZero() {
		return time.Now() // Immediately
	}

	return lastBackup.Add(s.config.GetInterval())
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
	s.operationMu.Lock()
	defer s.operationMu.Unlock()

	if !s.ShouldBackup() {
		return "", nil
	}

	location := s.config.GetLocation()

	// Ensure backup directory exists
	if err := os.MkdirAll(location, 0700); err != nil {
		s.recordError(fmt.Errorf("create backup dir: %w", err))
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	location, err := filepath.EvalSymlinks(location)
	if err == nil {
		location, err = filepath.Abs(location)
	}
	if err != nil {
		s.recordError(fmt.Errorf("resolve backup dir: %w", err))
		return "", fmt.Errorf("resolve backup dir: %w", err)
	}

	// The exporter retains its ordinary manual-export naming and overwrite
	// semantics only inside this private directory. Nothing in the user's
	// backup directory can be replaced by export or failed assembly.
	staging, err := os.MkdirTemp(location, ".caam-auto-backup-*")
	if err != nil {
		s.recordError(fmt.Errorf("create backup staging dir: %w", err))
		return "", fmt.Errorf("create backup staging dir: %w", err)
	}
	defer os.RemoveAll(staging)

	// Create the backup using the bundle package
	exporter := &bundle.VaultExporter{
		VaultPath:    s.vault,
		DataPath:     config.DefaultDataPath(),
		ConfigPath:   config.ConfigPath(),
		ProjectsPath: project.DefaultPath(),
		HealthPath:   health.DefaultHealthPath(),
		DatabasePath: caamdb.DefaultPath(),
		SyncPath:     syncstate.SyncDataDir(),
	}

	opts := bundle.DefaultExportOptions()
	opts.OutputDir = staging
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

	// Exercise the same manifest, checksum, and path validation as recovery
	// before this archive can displace an older recovery copy. The destinations
	// are private and dry-run never publishes imported credentials.
	if err := verifyBackupRecovery(result.OutputPath, staging); err != nil {
		s.recordError(fmt.Errorf("verify backup: %w", err))
		return "", fmt.Errorf("verify backup: %w", err)
	}

	checksum, err := bundle.ComputeFileChecksum(result.OutputPath, bundle.AlgorithmSHA256)
	if err != nil {
		s.recordError(fmt.Errorf("checksum backup: %w", err))
		return "", fmt.Errorf("checksum backup: %w", err)
	}
	created := time.Now().UTC()
	backupPath, err := publishScheduledBackup(result.OutputPath, location, created, rand.Reader)
	if err != nil {
		s.recordError(fmt.Errorf("publish backup: %w", err))
		return "", fmt.Errorf("publish backup: %w", err)
	}
	record := BackupRecord{Path: backupPath, CreatedAt: created, Size: result.CompressedSize, SHA256: checksum}

	// Update state
	s.mu.Lock()
	previous := s.state
	s.state.LastBackup = created
	s.state.LastBackupPath = backupPath
	s.state.BackupCount++
	s.state.LastError = ""
	s.state.LastErrorTime = time.Time{}
	s.state.OwnedBackups = append(s.state.OwnedBackups, record)
	s.mu.Unlock()

	if err := s.SaveState(); err != nil {
		// Keep the published archive, but never prune after a failed ownership
		// commit. A lost state file leaves extra archives, not missing recovery.
		s.mu.Lock()
		s.state = previous
		s.mu.Unlock()
		s.recordError(fmt.Errorf("save backup ownership: %w", err))
		return backupPath, fmt.Errorf("save backup ownership: %w", err)
	}

	s.logger.Printf("Created automatic backup: %s", backupPath)

	// Rotate old backups
	if err := s.rotateBackups(); err != nil {
		s.recordError(fmt.Errorf("rotate backups: %w", err))
		s.logger.Printf("Warning: failed to rotate backups: %v", err)
	}

	return backupPath, nil
}

// publishScheduledBackup atomically creates a new name for a completed archive.
// A hard link is an exclusive publication operation: unlike Rename, it cannot
// replace an existing file, directory, or symlink. Staging is on the destination
// filesystem; unsupported filesystems fail closed without pruning old backups.
func publishScheduledBackup(stagedPath, location string, created time.Time, entropy io.Reader) (string, error) {
	for attempt := 0; attempt < 16; attempt++ {
		var id [16]byte
		if _, err := io.ReadFull(entropy, id[:]); err != nil {
			return "", fmt.Errorf("generate backup name: %w", err)
		}
		name := automaticBackupPrefix + created.UTC().Format(automaticBackupTimeFormat) + "_" + hex.EncodeToString(id[:]) + ".zip"
		path := filepath.Join(location, name)
		if err := os.Link(stagedPath, path); err != nil {
			if os.IsExist(err) {
				continue
			}
			return "", err
		}
		if err := syncBackupDirectory(location); err != nil {
			// This complete but unrecorded file is deliberately left untouched.
			return "", fmt.Errorf("sync published backup: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf("could not allocate an unused automatic backup name")
}

func syncBackupDirectory(path string) error {
	// Windows does not support Sync on directory handles. The completed
	// archive and state files have already been synced before publication.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func verifyBackupRecovery(path, staging string) error {
	_, err := (&bundle.VaultImporter{BundlePath: path}).Import(&bundle.ImportOptions{
		DryRun:       true,
		Mode:         bundle.ImportModeReplace,
		VaultPath:    filepath.Join(staging, "verify", "vault"),
		ConfigPath:   filepath.Join(staging, "verify", "config.json"),
		ProjectsPath: filepath.Join(staging, "verify", "projects.json"),
		HealthPath:   filepath.Join(staging, "verify", "health.json"),
		DatabasePath: filepath.Join(staging, "verify", "caam.db"),
		SyncPath:     filepath.Join(staging, "verify", "sync"),
	})
	return err
}

// recordError records an error in the backup state.
func (s *BackupScheduler) recordError(err error) {
	s.mu.Lock()
	s.state.LastError = err.Error()
	s.state.LastErrorTime = time.Now()
	s.mu.Unlock()
	if saveErr := s.SaveState(); saveErr != nil {
		s.logger.Printf("Warning: failed to save backup state: %v", saveErr)
	}
}

// RotateBackups removes only unchanged, recorded automatic backups. Archives
// without trustworthy ownership records do not count toward keep_last.
func (s *BackupScheduler) RotateBackups() error {
	s.operationMu.Lock()
	defer s.operationMu.Unlock()
	return s.rotateBackups()
}

func (s *BackupScheduler) rotateBackups() error {
	keepLast := s.config.GetKeepLast()
	backups, err := s.verifiedBackups()
	if err != nil {
		return err
	}
	if len(backups) <= keepLast {
		return nil
	}

	removed := make(map[string]bool)
	var failures []error
	for i := len(backups) - 1; i >= keepLast; i-- {
		backup := backups[i]
		current, err := os.Lstat(backup.record.Path)
		if err != nil || !sameBackupFile(backup.info, current) {
			// A missing or replaced path is no longer ours to remove.
			continue
		}
		if err := os.Remove(backup.record.Path); err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", filepath.Base(backup.record.Path), err))
			continue
		}
		removed[backup.record.Path] = true
		s.logger.Printf("Deleted old automatic backup: %s", filepath.Base(backup.record.Path))
	}

	if len(removed) > 0 {
		s.mu.Lock()
		retained := make([]BackupRecord, 0, len(s.state.OwnedBackups))
		for _, record := range s.state.OwnedBackups {
			if !removed[record.Path] {
				retained = append(retained, record)
			}
		}
		s.state.OwnedBackups = retained
		s.mu.Unlock()
		if err := s.SaveState(); err != nil {
			failures = append(failures, fmt.Errorf("save retained backups: %w", err))
		}
	}
	return errors.Join(failures...)
}

// GetState returns a copy of the current backup state.
func (s *BackupScheduler) GetState() BackupState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.state
	state.OwnedBackups = append([]BackupRecord(nil), s.state.OwnedBackups...)
	return state
}

// ListBackups returns verified automatic backups owned by this scheduler.
// Legacy and manual archives remain on disk but are outside retention.
func (s *BackupScheduler) ListBackups() ([]BackupInfo, error) {
	verified, err := s.verifiedBackups()
	if err != nil {
		return nil, err
	}
	backups := make([]BackupInfo, 0, len(verified))
	for _, backup := range verified {
		backups = append(backups, BackupInfo{
			Name:      filepath.Base(backup.record.Path),
			Path:      backup.record.Path,
			Size:      backup.record.Size,
			CreatedAt: backup.record.CreatedAt,
		})
	}
	return backups, nil
}

type verifiedBackup struct {
	record BackupRecord
	info   os.FileInfo
}

func (s *BackupScheduler) verifiedBackups() ([]verifiedBackup, error) {
	location, err := filepath.EvalSymlinks(s.config.GetLocation())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve backup directory: %w", err)
	}
	location, err = filepath.Abs(location)
	if err != nil {
		return nil, err
	}

	records := s.GetState().OwnedBackups
	counts := make(map[string]int, len(records))
	for _, record := range records {
		counts[record.Path]++
	}
	var backups []verifiedBackup
	// Records are appended only after successful publication. Use that durable
	// order even when clock resolution produces ties or the clock moves back.
	for index := len(records) - 1; index >= 0; index-- {
		record := records[index]
		// Duplicate or conflicting ownership records are ambiguous.
		if counts[record.Path] != 1 || !validBackupRecord(record, location) {
			continue
		}
		info, err := verifyBackupRecord(record)
		if err != nil {
			s.logger.Printf("Preserving unverified automatic backup %s: %v", filepath.Base(record.Path), err)
			continue
		}
		backups = append(backups, verifiedBackup{record: record, info: info})
	}

	return backups, nil
}

func validBackupRecord(record BackupRecord, location string) bool {
	if !filepath.IsAbs(record.Path) || filepath.Clean(record.Path) != record.Path || filepath.Dir(record.Path) != location || record.Size <= 0 || record.CreatedAt.IsZero() {
		return false
	}
	name := filepath.Base(record.Path)
	if !strings.HasPrefix(name, automaticBackupPrefix) || !strings.HasSuffix(name, ".zip") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, automaticBackupPrefix), ".zip"), "_")
	if len(parts) != 2 || len(parts[1]) != 32 {
		return false
	}
	created, err := time.Parse(automaticBackupTimeFormat, parts[0])
	if err != nil || !created.Equal(record.CreatedAt) {
		return false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return false
	}
	checksum, err := hex.DecodeString(record.SHA256)
	return err == nil && len(checksum) == sha256.Size
}

func verifyBackupRecord(record BackupRecord) (os.FileInfo, error) {
	before, err := os.Lstat(record.Path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() != record.Size {
		return nil, fmt.Errorf("file type or size changed")
	}
	f, err := os.Open(record.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !sameBackupFile(before, opened) {
		return nil, fmt.Errorf("file changed before verification")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return nil, err
	}
	if hex.EncodeToString(hash.Sum(nil)) != record.SHA256 {
		return nil, fmt.Errorf("file changed since backup publication")
	}
	staging, err := os.MkdirTemp("", "caam-backup-verify-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)
	if err := verifyBackupRecovery(record.Path, staging); err != nil {
		return nil, fmt.Errorf("archive is not recoverable: %w", err)
	}
	after, err := os.Lstat(record.Path)
	if err != nil {
		return nil, err
	}
	if !sameBackupFile(before, after) {
		return nil, fmt.Errorf("file changed since backup publication")
	}
	return after, nil
}

func sameBackupFile(before, after os.FileInfo) bool {
	return before != nil && after != nil && after.Mode().IsRegular() && os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}

// BackupInfo contains information about a backup file.
type BackupInfo struct {
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	Size      int64     `json:"size"`
	CreatedAt time.Time `json:"created_at"`
}
