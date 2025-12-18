package db

import (
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultCleanupConfig(t *testing.T) {
	cfg := DefaultCleanupConfig()

	if cfg.RetentionDays != 90 {
		t.Errorf("RetentionDays = %d, want 90", cfg.RetentionDays)
	}
	if cfg.AggregateRetentionDays != 365 {
		t.Errorf("AggregateRetentionDays = %d, want 365", cfg.AggregateRetentionDays)
	}
}

func TestDB_Cleanup(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cleanup.db")

	db, err := OpenAt(dbPath)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	// Insert some old and new activity logs
	now := time.Now()
	old := now.AddDate(0, 0, -100) // 100 days ago
	recent := now.AddDate(0, 0, -10) // 10 days ago

	// Old event
	oldEvent := Event{
		Timestamp:   old,
		Type:        EventActivate,
		Provider:    "codex",
		ProfileName: "old-profile",
	}
	if err := db.LogEvent(oldEvent); err != nil {
		t.Fatalf("LogEvent (old): %v", err)
	}

	// Recent event
	recentEvent := Event{
		Timestamp:   recent,
		Type:        EventActivate,
		Provider:    "codex",
		ProfileName: "recent-profile",
	}
	if err := db.LogEvent(recentEvent); err != nil {
		t.Fatalf("LogEvent (recent): %v", err)
	}

	// Verify we have 2 entries
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActivityLogCount != 2 {
		t.Fatalf("ActivityLogCount = %d, want 2", stats.ActivityLogCount)
	}

	// Run cleanup with 90-day retention (should delete the 100-day old entry)
	cfg := CleanupConfig{
		RetentionDays:          90,
		AggregateRetentionDays: 365,
	}
	result, err := db.Cleanup(cfg)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	if result.ActivityLogsDeleted != 1 {
		t.Errorf("ActivityLogsDeleted = %d, want 1", result.ActivityLogsDeleted)
	}

	// Verify we have 1 entry left
	stats, err = db.Stats()
	if err != nil {
		t.Fatalf("Stats after cleanup: %v", err)
	}
	if stats.ActivityLogCount != 1 {
		t.Errorf("ActivityLogCount after cleanup = %d, want 1", stats.ActivityLogCount)
	}
}

func TestDB_CleanupDryRun(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cleanup_dryrun.db")

	db, err := OpenAt(dbPath)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	// Insert an old activity log
	old := time.Now().AddDate(0, 0, -100)
	oldEvent := Event{
		Timestamp:   old,
		Type:        EventActivate,
		Provider:    "claude",
		ProfileName: "old-profile",
	}
	if err := db.LogEvent(oldEvent); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	// Dry run should report what would be deleted
	cfg := CleanupConfig{
		RetentionDays:          90,
		AggregateRetentionDays: 365,
	}
	result, err := db.CleanupDryRun(cfg)
	if err != nil {
		t.Fatalf("CleanupDryRun: %v", err)
	}

	if result.ActivityLogsDeleted != 1 {
		t.Errorf("CleanupDryRun ActivityLogsDeleted = %d, want 1", result.ActivityLogsDeleted)
	}

	// Verify nothing was actually deleted
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActivityLogCount != 1 {
		t.Errorf("ActivityLogCount after dry run = %d, want 1 (nothing should be deleted)", stats.ActivityLogCount)
	}
}

func TestDB_Stats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_stats.db")

	db, err := OpenAt(dbPath)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	// Empty database
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActivityLogCount != 0 {
		t.Errorf("ActivityLogCount = %d, want 0", stats.ActivityLogCount)
	}
	if stats.Path != dbPath {
		t.Errorf("Path = %q, want %q", stats.Path, dbPath)
	}

	// Add an entry
	event := Event{
		Type:        EventActivate,
		Provider:    "gemini",
		ProfileName: "test",
	}
	if err := db.LogEvent(event); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	stats, err = db.Stats()
	if err != nil {
		t.Fatalf("Stats after insert: %v", err)
	}
	if stats.ActivityLogCount != 1 {
		t.Errorf("ActivityLogCount = %d, want 1", stats.ActivityLogCount)
	}
	if stats.ProfileStatsCount != 1 {
		t.Errorf("ProfileStatsCount = %d, want 1", stats.ProfileStatsCount)
	}
	if stats.OldestEntry.IsZero() {
		t.Error("OldestEntry should not be zero")
	}
	if stats.NewestEntry.IsZero() {
		t.Error("NewestEntry should not be zero")
	}
}

func TestDB_CleanupNilDB(t *testing.T) {
	var db *DB
	_, err := db.Cleanup(DefaultCleanupConfig())
	if err == nil {
		t.Error("Cleanup on nil DB should return error")
	}

	_, err = db.CleanupDryRun(DefaultCleanupConfig())
	if err == nil {
		t.Error("CleanupDryRun on nil DB should return error")
	}

	_, err = db.Stats()
	if err == nil {
		t.Error("Stats on nil DB should return error")
	}
}

func TestDB_CleanupStaleProfileStats(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cleanup_stats.db")

	db, err := OpenAt(dbPath)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	// Insert an old profile stat with old last_activated
	old := time.Now().AddDate(-2, 0, 0) // 2 years ago
	_, err = db.conn.Exec(`
		INSERT INTO profile_stats (provider, profile_name, total_activations, last_activated)
		VALUES (?, ?, ?, ?)
	`, "codex", "ancient-profile", 5, formatSQLiteTime(old))
	if err != nil {
		t.Fatalf("Insert old profile_stats: %v", err)
	}

	// Insert a recent profile stat
	recent := time.Now().AddDate(0, 0, -30) // 30 days ago
	_, err = db.conn.Exec(`
		INSERT INTO profile_stats (provider, profile_name, total_activations, last_activated)
		VALUES (?, ?, ?, ?)
	`, "claude", "recent-profile", 10, formatSQLiteTime(recent))
	if err != nil {
		t.Fatalf("Insert recent profile_stats: %v", err)
	}

	// Verify we have 2 profile stats
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ProfileStatsCount != 2 {
		t.Fatalf("ProfileStatsCount = %d, want 2", stats.ProfileStatsCount)
	}

	// Run cleanup with 1 year aggregate retention
	cfg := CleanupConfig{
		RetentionDays:          90,
		AggregateRetentionDays: 365,
	}
	result, err := db.Cleanup(cfg)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// The 2-year old profile_stats should be deleted
	if result.StatsEntriesDeleted != 1 {
		t.Errorf("StatsEntriesDeleted = %d, want 1", result.StatsEntriesDeleted)
	}

	// Verify we have 1 profile stat left
	stats, err = db.Stats()
	if err != nil {
		t.Fatalf("Stats after cleanup: %v", err)
	}
	if stats.ProfileStatsCount != 1 {
		t.Errorf("ProfileStatsCount after cleanup = %d, want 1", stats.ProfileStatsCount)
	}
}

func TestDB_CleanupZeroRetention(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test_cleanup_zero.db")

	db, err := OpenAt(dbPath)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer db.Close()

	// Insert an old activity log (would normally be deleted)
	old := time.Now().AddDate(0, 0, -100) // 100 days ago
	oldEvent := Event{
		Timestamp:   old,
		Type:        EventActivate,
		Provider:    "codex",
		ProfileName: "old-profile",
	}
	if err := db.LogEvent(oldEvent); err != nil {
		t.Fatalf("LogEvent: %v", err)
	}

	// Verify we have 1 entry
	stats, err := db.Stats()
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.ActivityLogCount != 1 {
		t.Fatalf("ActivityLogCount = %d, want 1", stats.ActivityLogCount)
	}

	// Run cleanup with zero retention (should skip deletion, treat as "keep forever")
	cfg := CleanupConfig{
		RetentionDays:          0,
		AggregateRetentionDays: 0,
	}
	result, err := db.Cleanup(cfg)
	if err != nil {
		t.Fatalf("Cleanup: %v", err)
	}

	// Nothing should be deleted when retention is 0
	if result.ActivityLogsDeleted != 0 {
		t.Errorf("ActivityLogsDeleted = %d, want 0 (retention=0 means keep forever)", result.ActivityLogsDeleted)
	}
	if result.StatsEntriesDeleted != 0 {
		t.Errorf("StatsEntriesDeleted = %d, want 0 (retention=0 means keep forever)", result.StatsEntriesDeleted)
	}

	// Verify nothing was deleted
	stats, err = db.Stats()
	if err != nil {
		t.Fatalf("Stats after cleanup: %v", err)
	}
	if stats.ActivityLogCount != 1 {
		t.Errorf("ActivityLogCount after cleanup = %d, want 1", stats.ActivityLogCount)
	}

	// Dry run should also return 0
	dryResult, err := db.CleanupDryRun(cfg)
	if err != nil {
		t.Fatalf("CleanupDryRun: %v", err)
	}
	if dryResult.ActivityLogsDeleted != 0 {
		t.Errorf("CleanupDryRun ActivityLogsDeleted = %d, want 0", dryResult.ActivityLogsDeleted)
	}
}
