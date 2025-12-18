package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// CooldownEvent records a provider/profile limit hit and its enforced cooldown.
type CooldownEvent struct {
	ID            int64
	Provider      string
	ProfileName   string
	HitAt         time.Time
	CooldownUntil time.Time
	Notes         string
}

// SetCooldown records a limit hit and cooldown duration for a provider/profile.
// It inserts a new limit_events row (keeping history).
func (d *DB) SetCooldown(provider, profile string, hitAt time.Time, duration time.Duration, notes string) (*CooldownEvent, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	profile = strings.TrimSpace(profile)
	notes = strings.TrimSpace(notes)

	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}
	if profile == "" {
		return nil, fmt.Errorf("profile name is required")
	}
	if duration <= 0 {
		return nil, fmt.Errorf("duration must be > 0")
	}

	if hitAt.IsZero() {
		hitAt = time.Now().UTC()
	} else {
		hitAt = hitAt.UTC()
	}

	cooldownUntil := hitAt.Add(duration)
	if cooldownUntil.Before(hitAt) {
		return nil, fmt.Errorf("cooldown_until is before hit_at")
	}

	var notesStr sql.NullString
	if notes != "" {
		notesStr = sql.NullString{String: notes, Valid: true}
	}

	res, err := d.conn.Exec(
		`INSERT INTO limit_events (provider, profile_name, hit_at, cooldown_until, notes) VALUES (?, ?, ?, ?, ?)`,
		provider,
		profile,
		formatSQLiteTime(hitAt),
		formatSQLiteTime(cooldownUntil),
		notesStr,
	)
	if err != nil {
		return nil, fmt.Errorf("insert limit_events: %w", err)
	}

	id, _ := res.LastInsertId()
	return &CooldownEvent{
		ID:            id,
		Provider:      provider,
		ProfileName:   profile,
		HitAt:         hitAt,
		CooldownUntil: cooldownUntil,
		Notes:         notes,
	}, nil
}

// ActiveCooldown returns the most recent active cooldown for the given provider/profile.
// Returns (nil, nil) if there is no active cooldown.
func (d *DB) ActiveCooldown(provider, profile string, now time.Time) (*CooldownEvent, error) {
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

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}

	var (
		ev               CooldownEvent
		hitAtStr         string
		cooldownUntilStr string
		notes            sql.NullString
	)
	err := d.conn.QueryRow(
		`SELECT id, provider, profile_name, hit_at, cooldown_until, notes
		   FROM limit_events
		  WHERE provider = ? AND profile_name = ? AND datetime(cooldown_until) > datetime(?)
		  ORDER BY datetime(cooldown_until) DESC, datetime(hit_at) DESC, id DESC
		  LIMIT 1`,
		provider,
		profile,
		formatSQLiteTime(now),
	).Scan(&ev.ID, &ev.Provider, &ev.ProfileName, &hitAtStr, &cooldownUntilStr, &notes)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query limit_events: %w", err)
	}

	hitAt, err := parseSQLiteTime(hitAtStr)
	if err != nil {
		return nil, fmt.Errorf("parse hit_at %q: %w", hitAtStr, err)
	}
	cooldownUntil, err := parseSQLiteTime(cooldownUntilStr)
	if err != nil {
		return nil, fmt.Errorf("parse cooldown_until %q: %w", cooldownUntilStr, err)
	}

	ev.HitAt = hitAt
	ev.CooldownUntil = cooldownUntil
	if notes.Valid {
		ev.Notes = notes.String
	}

	return &ev, nil
}

// ListActiveCooldowns returns the newest active cooldown entry for each provider/profile.
func (d *DB) ListActiveCooldowns(now time.Time) ([]CooldownEvent, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	nowStr := formatSQLiteTime(now)

	rows, err := d.conn.Query(
		`SELECT le.id, le.provider, le.profile_name, le.hit_at, le.cooldown_until, le.notes
		   FROM limit_events le
		  WHERE datetime(le.cooldown_until) > datetime(?)
		    AND le.id = (
		      SELECT le2.id
		        FROM limit_events le2
		       WHERE le2.provider = le.provider
		         AND le2.profile_name = le.profile_name
		         AND datetime(le2.cooldown_until) > datetime(?)
		       ORDER BY datetime(le2.cooldown_until) DESC, datetime(le2.hit_at) DESC, le2.id DESC
		       LIMIT 1
		    )
		  ORDER BY le.provider ASC, le.profile_name ASC`,
		nowStr,
		nowStr,
	)
	if err != nil {
		return nil, fmt.Errorf("query limit_events: %w", err)
	}
	defer rows.Close()

	var out []CooldownEvent
	for rows.Next() {
		var (
			ev               CooldownEvent
			hitAtStr         string
			cooldownUntilStr string
			notes            sql.NullString
		)
		if err := rows.Scan(&ev.ID, &ev.Provider, &ev.ProfileName, &hitAtStr, &cooldownUntilStr, &notes); err != nil {
			return nil, fmt.Errorf("scan limit_events: %w", err)
		}
		hitAt, err := parseSQLiteTime(hitAtStr)
		if err != nil {
			return nil, fmt.Errorf("parse hit_at %q: %w", hitAtStr, err)
		}
		cooldownUntil, err := parseSQLiteTime(cooldownUntilStr)
		if err != nil {
			return nil, fmt.Errorf("parse cooldown_until %q: %w", cooldownUntilStr, err)
		}
		ev.HitAt = hitAt
		ev.CooldownUntil = cooldownUntil
		if notes.Valid {
			ev.Notes = notes.String
		}
		out = append(out, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate limit_events: %w", err)
	}
	return out, nil
}

// ClearCooldown deletes cooldown history for a specific provider/profile.
func (d *DB) ClearCooldown(provider, profile string) (int64, error) {
	if d == nil || d.conn == nil {
		return 0, fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	profile = strings.TrimSpace(profile)
	if provider == "" {
		return 0, fmt.Errorf("provider is required")
	}
	if profile == "" {
		return 0, fmt.Errorf("profile name is required")
	}

	res, err := d.conn.Exec(`DELETE FROM limit_events WHERE provider = ? AND profile_name = ?`, provider, profile)
	if err != nil {
		return 0, fmt.Errorf("delete limit_events: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}

// ClearAllCooldowns deletes all cooldown entries (all providers/profiles).
func (d *DB) ClearAllCooldowns() (int64, error) {
	if d == nil || d.conn == nil {
		return 0, fmt.Errorf("db is not open")
	}

	res, err := d.conn.Exec(`DELETE FROM limit_events`)
	if err != nil {
		return 0, fmt.Errorf("delete limit_events: %w", err)
	}
	affected, _ := res.RowsAffected()
	return affected, nil
}
