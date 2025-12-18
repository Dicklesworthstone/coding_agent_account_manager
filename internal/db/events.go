package db

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const (
	EventActivate    = "activate"
	EventLogin       = "login"
	EventRefresh     = "refresh"
	EventError       = "error"
	EventSwitch      = "switch"
	EventDeactivate  = "deactivate"
	sqliteTimeLayout = "2006-01-02 15:04:05"
)

type Event struct {
	Timestamp   time.Time
	Type        string
	Provider    string
	ProfileName string
	Details     map[string]any
	Duration    time.Duration
}

type ProfileStats struct {
	Provider           string
	ProfileName        string
	TotalActivations   int
	TotalErrors        int
	TotalActiveSeconds int
	LastActivated      time.Time
	LastError          time.Time
}

type EventLogger interface {
	Log(event Event) error
	GetEvents(provider, profile string, since time.Time, limit int) ([]Event, error)
	GetStats(provider, profile string) (*ProfileStats, error)
}

func (d *DB) Log(event Event) error {
	return d.LogEvent(event)
}

func (d *DB) LogEvent(event Event) error {
	if d == nil || d.conn == nil {
		return fmt.Errorf("db is not open")
	}

	eventType := strings.TrimSpace(event.Type)
	provider := strings.TrimSpace(event.Provider)
	profile := strings.TrimSpace(event.ProfileName)

	if eventType == "" {
		return fmt.Errorf("event type is required")
	}
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if profile == "" {
		return fmt.Errorf("profile name is required")
	}

	ts := event.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	tsStr := formatSQLiteTime(ts)

	var detailsStr sql.NullString
	if event.Details != nil {
		b, err := json.Marshal(event.Details)
		if err != nil {
			return fmt.Errorf("marshal details: %w", err)
		}
		detailsStr = sql.NullString{String: string(b), Valid: true}
	}

	durationSeconds := int64(event.Duration / time.Second)
	if durationSeconds < 0 {
		durationSeconds = 0
	}

	tx, err := d.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	if _, err := tx.Exec(
		`INSERT INTO activity_log (timestamp, event_type, provider, profile_name, details, duration_seconds) VALUES (?, ?, ?, ?, ?, ?)`,
		tsStr,
		eventType,
		provider,
		profile,
		detailsStr,
		durationSeconds,
	); err != nil {
		return fmt.Errorf("insert activity_log: %w", err)
	}

	if err := updateProfileStats(tx, eventType, provider, profile, tsStr, durationSeconds); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

func (d *DB) GetEvents(provider, profile string, since time.Time, limit int) ([]Event, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	profile = strings.TrimSpace(profile)
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if profile == "" {
		return nil, fmt.Errorf("profile name is required")
	}

	if limit <= 0 {
		limit = 100
	}
	sinceStr := formatSQLiteTime(since)

	rows, err := d.conn.Query(
		`SELECT timestamp, event_type, provider, profile_name, details, duration_seconds
		 FROM activity_log
		 WHERE provider = ? AND profile_name = ? AND datetime(timestamp) >= datetime(?)
		 ORDER BY timestamp DESC
		 LIMIT ?`,
		provider,
		profile,
		sinceStr,
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query activity_log: %w", err)
	}
	defer rows.Close()

	var out []Event
	for rows.Next() {
		var tsStr string
		var e Event
		var details sql.NullString
		var durationSeconds sql.NullInt64
		if err := rows.Scan(&tsStr, &e.Type, &e.Provider, &e.ProfileName, &details, &durationSeconds); err != nil {
			return nil, fmt.Errorf("scan activity_log: %w", err)
		}

		ts, err := parseSQLiteTime(tsStr)
		if err != nil {
			return nil, fmt.Errorf("parse timestamp %q: %w", tsStr, err)
		}
		e.Timestamp = ts

		if details.Valid && details.String != "" {
			var m map[string]any
			if err := json.Unmarshal([]byte(details.String), &m); err == nil {
				e.Details = m
			}
		}

		if durationSeconds.Valid && durationSeconds.Int64 > 0 {
			e.Duration = time.Duration(durationSeconds.Int64) * time.Second
		}

		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activity_log: %w", err)
	}
	return out, nil
}

func (d *DB) GetStats(provider, profile string) (*ProfileStats, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	profile = strings.TrimSpace(profile)
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if profile == "" {
		return nil, fmt.Errorf("profile name is required")
	}

	var stats ProfileStats
	var lastActivated sql.NullString
	var lastError sql.NullString

	err := d.conn.QueryRow(
		`SELECT provider, profile_name, total_activations, total_errors, total_active_seconds, last_activated, last_error
		 FROM profile_stats
		 WHERE provider = ? AND profile_name = ?`,
		provider,
		profile,
	).Scan(
		&stats.Provider,
		&stats.ProfileName,
		&stats.TotalActivations,
		&stats.TotalErrors,
		&stats.TotalActiveSeconds,
		&lastActivated,
		&lastError,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query profile_stats: %w", err)
	}

	if lastActivated.Valid && lastActivated.String != "" {
		if ts, err := parseSQLiteTime(lastActivated.String); err == nil {
			stats.LastActivated = ts
		}
	}
	if lastError.Valid && lastError.String != "" {
		if ts, err := parseSQLiteTime(lastError.String); err == nil {
			stats.LastError = ts
		}
	}

	return &stats, nil
}

// LastActivation returns the most recent activation timestamp for a provider/profile.
// Returns zero time if no activation event exists.
//
// Prefer the aggregated profile_stats table, but fall back to querying activity_log
// for robustness (e.g., older databases without backfilled stats).
func (d *DB) LastActivation(provider, profile string) (time.Time, error) {
	if d == nil || d.conn == nil {
		return time.Time{}, fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	profile = strings.TrimSpace(profile)
	if provider == "" {
		return time.Time{}, fmt.Errorf("provider is required")
	}
	if profile == "" {
		return time.Time{}, fmt.Errorf("profile name is required")
	}

	stats, err := d.GetStats(provider, profile)
	if err != nil {
		return time.Time{}, err
	}
	if stats != nil && !stats.LastActivated.IsZero() {
		return stats.LastActivated, nil
	}

	var tsStr string
	err = d.conn.QueryRow(
		`SELECT timestamp
		 FROM activity_log
		 WHERE provider = ? AND profile_name = ? AND event_type = ?
		 ORDER BY timestamp DESC
		 LIMIT 1`,
		provider,
		profile,
		EventActivate,
	).Scan(&tsStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return time.Time{}, nil
		}
		return time.Time{}, fmt.Errorf("query last activation: %w", err)
	}

	ts, err := parseSQLiteTime(tsStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse timestamp %q: %w", tsStr, err)
	}
	return ts, nil
}

func updateProfileStats(tx *sql.Tx, eventType, provider, profile, ts string, durationSeconds int64) error {
	switch eventType {
	case EventActivate:
		_, err := tx.Exec(
			`INSERT INTO profile_stats (provider, profile_name, total_activations, last_activated)
			 VALUES (?, ?, 1, ?)
			 ON CONFLICT(provider, profile_name) DO UPDATE SET
			   total_activations = total_activations + 1,
			   last_activated = MAX(COALESCE(last_activated, excluded.last_activated), excluded.last_activated)`,
			provider,
			profile,
			ts,
		)
		if err != nil {
			return fmt.Errorf("update profile_stats activate: %w", err)
		}
	case EventError:
		_, err := tx.Exec(
			`INSERT INTO profile_stats (provider, profile_name, total_errors, last_error)
			 VALUES (?, ?, 1, ?)
			 ON CONFLICT(provider, profile_name) DO UPDATE SET
			   total_errors = total_errors + 1,
			   last_error = MAX(COALESCE(last_error, excluded.last_error), excluded.last_error)`,
			provider,
			profile,
			ts,
		)
		if err != nil {
			return fmt.Errorf("update profile_stats error: %w", err)
		}
	case EventDeactivate:
		if durationSeconds <= 0 {
			return nil
		}
		_, err := tx.Exec(
			`INSERT INTO profile_stats (provider, profile_name, total_active_seconds)
			 VALUES (?, ?, ?)
			 ON CONFLICT(provider, profile_name) DO UPDATE SET
			   total_active_seconds = total_active_seconds + excluded.total_active_seconds`,
			provider,
			profile,
			durationSeconds,
		)
		if err != nil {
			return fmt.Errorf("update profile_stats deactivate: %w", err)
		}
	}

	return nil
}

func formatSQLiteTime(t time.Time) string {
	if t.IsZero() {
		// This makes "since" queries behave like "since the beginning of time".
		return "1970-01-01 00:00:00"
	}
	return t.UTC().Format(sqliteTimeLayout)
}

func parseSQLiteTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty time")
	}
	if ts, err := time.ParseInLocation(sqliteTimeLayout, s, time.UTC); err == nil {
		return ts, nil
	}
	if ts, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return ts.UTC(), nil
	}
	if ts, err := time.Parse(time.RFC3339, s); err == nil {
		return ts.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time format")
}
