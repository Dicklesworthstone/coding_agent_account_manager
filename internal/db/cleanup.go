package db

import (
	"fmt"
	"os"
	"time"
)

// CleanupConfig holds retention settings for database cleanup.
type CleanupConfig struct {
	// RetentionDays is how long to keep detailed activity logs.
	RetentionDays int
	// AggregateRetentionDays is how long to keep profile_stats entries.
	// Should be >= RetentionDays.
	AggregateRetentionDays int
}

// DefaultCleanupConfig returns sensible defaults.
func DefaultCleanupConfig() CleanupConfig {
	return CleanupConfig{
		RetentionDays:          90,  // 3 months of detailed logs
		AggregateRetentionDays: 365, // 1 year of aggregates
	}
}

// CleanupResult contains information about what was cleaned up.
type CleanupResult struct {
	ActivityLogsDeleted int
	StatsEntriesDeleted int
	VacuumRan           bool
}

// Cleanup removes old records based on the retention configuration.
// It deletes activity_log entries older than RetentionDays and
// profile_stats entries for profiles with no recent activity.
// If RetentionDays or AggregateRetentionDays is <= 0, that cleanup is skipped
// (treated as "keep forever").
func (d *DB) Cleanup(cfg CleanupConfig) (*CleanupResult, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	result := &CleanupResult{}
	now := time.Now()

	// Delete old activity logs (skip if retention <= 0, meaning "keep forever")
	if cfg.RetentionDays > 0 {
		activityCutoff := now.AddDate(0, 0, -cfg.RetentionDays)
		activityResult, err := d.conn.Exec(`
			DELETE FROM activity_log
			WHERE datetime(timestamp) < datetime(?)
		`, formatSQLiteTime(activityCutoff))
		if err != nil {
			return nil, fmt.Errorf("delete old activity logs: %w", err)
		}
		deleted, _ := activityResult.RowsAffected()
		result.ActivityLogsDeleted = int(deleted)
	}

	// Delete stale profile_stats (skip if aggregate retention <= 0)
	// Only delete if last_activated is old AND there are no recent logs for this profile
	if cfg.AggregateRetentionDays > 0 {
		statsCutoff := now.AddDate(0, 0, -cfg.AggregateRetentionDays)
		statsResult, err := d.conn.Exec(`
			DELETE FROM profile_stats
			WHERE (last_activated IS NULL OR datetime(last_activated) < datetime(?))
			  AND NOT EXISTS (
				SELECT 1 FROM activity_log
				WHERE activity_log.provider = profile_stats.provider
				  AND activity_log.profile_name = profile_stats.profile_name
				  AND datetime(activity_log.timestamp) >= datetime(?)
			  )
		`, formatSQLiteTime(statsCutoff), formatSQLiteTime(statsCutoff))
		if err != nil {
			return nil, fmt.Errorf("delete stale profile stats: %w", err)
		}
		statsDeleted, _ := statsResult.RowsAffected()
		result.StatsEntriesDeleted = int(statsDeleted)
	}

	// Run VACUUM to reclaim space if significant deletions occurred
	if result.ActivityLogsDeleted > 1000 || result.StatsEntriesDeleted > 100 {
		if _, err := d.conn.Exec("VACUUM"); err != nil {
			// VACUUM failure is not critical - log but don't fail
			// In a real app, we'd log this
		} else {
			result.VacuumRan = true
		}
	}

	return result, nil
}

// CleanupDryRun returns what would be deleted without actually deleting.
// If RetentionDays or AggregateRetentionDays is <= 0, that count is skipped (returns 0).
func (d *DB) CleanupDryRun(cfg CleanupConfig) (*CleanupResult, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	result := &CleanupResult{}
	now := time.Now()

	// Count activity logs that would be deleted (skip if retention <= 0)
	if cfg.RetentionDays > 0 {
		activityCutoff := now.AddDate(0, 0, -cfg.RetentionDays)
		var activityCount int
		err := d.conn.QueryRow(`
			SELECT COUNT(*) FROM activity_log
			WHERE datetime(timestamp) < datetime(?)
		`, formatSQLiteTime(activityCutoff)).Scan(&activityCount)
		if err != nil {
			return nil, fmt.Errorf("count old activity logs: %w", err)
		}
		result.ActivityLogsDeleted = activityCount
	}

	// Count profile_stats that would be deleted (skip if aggregate retention <= 0)
	if cfg.AggregateRetentionDays > 0 {
		statsCutoff := now.AddDate(0, 0, -cfg.AggregateRetentionDays)
		var statsCount int
		err := d.conn.QueryRow(`
			SELECT COUNT(*) FROM profile_stats
			WHERE (last_activated IS NULL OR datetime(last_activated) < datetime(?))
			  AND NOT EXISTS (
				SELECT 1 FROM activity_log
				WHERE activity_log.provider = profile_stats.provider
				  AND activity_log.profile_name = profile_stats.profile_name
				  AND datetime(activity_log.timestamp) >= datetime(?)
			  )
		`, formatSQLiteTime(statsCutoff), formatSQLiteTime(statsCutoff)).Scan(&statsCount)
		if err != nil {
			return nil, fmt.Errorf("count stale profile stats: %w", err)
		}
		result.StatsEntriesDeleted = statsCount
	}

	// Would VACUUM run?
	result.VacuumRan = result.ActivityLogsDeleted > 1000 || result.StatsEntriesDeleted > 100

	return result, nil
}

// DatabaseStats returns statistics about the database.
type DatabaseStats struct {
	Path             string
	SizeBytes        int64
	ActivityLogCount int
	ProfileStatsCount int
	OldestEntry      time.Time
	NewestEntry      time.Time
}

// Stats returns statistics about the database.
func (d *DB) Stats() (*DatabaseStats, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	stats := &DatabaseStats{
		Path: d.path,
	}

	// Get file size
	if info, err := fileSize(d.path); err == nil {
		stats.SizeBytes = info
	}

	// Count activity logs
	err := d.conn.QueryRow(`SELECT COUNT(*) FROM activity_log`).Scan(&stats.ActivityLogCount)
	if err != nil {
		return nil, fmt.Errorf("count activity logs: %w", err)
	}

	// Count profile stats
	err = d.conn.QueryRow(`SELECT COUNT(*) FROM profile_stats`).Scan(&stats.ProfileStatsCount)
	if err != nil {
		return nil, fmt.Errorf("count profile stats: %w", err)
	}

	// Get oldest and newest entries
	var oldestStr, newestStr *string
	err = d.conn.QueryRow(`SELECT MIN(timestamp), MAX(timestamp) FROM activity_log`).Scan(&oldestStr, &newestStr)
	if err != nil {
		return nil, fmt.Errorf("get timestamp range: %w", err)
	}
	if oldestStr != nil {
		if ts, err := parseSQLiteTime(*oldestStr); err == nil {
			stats.OldestEntry = ts
		}
	}
	if newestStr != nil {
		if ts, err := parseSQLiteTime(*newestStr); err == nil {
			stats.NewestEntry = ts
		}
	}

	return stats, nil
}

// fileSize returns the size of a file in bytes.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
