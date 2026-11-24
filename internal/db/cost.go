package db

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// WrapSession represents a recorded wrap session.
type WrapSession struct {
	ID                 int
	Provider           string
	ProfileName        string
	StartedAt          time.Time
	EndedAt            time.Time
	DurationSeconds    int
	ExitCode           int
	RateLimitHit       bool
	EstimatedCostCents int
	Notes              string
}

// CostRate represents the cost rate configuration for a provider.
type CostRate struct {
	Provider        string
	CentsPerMinute  int
	CentsPerSession int
	UpdatedAt       time.Time
}

// CostSummary provides aggregated cost information.
type CostSummary struct {
	Provider           string
	TotalSessions      int
	TotalDurationSecs  int
	TotalCostCents     int
	RateLimitHits      int
	AverageDurationSec float64
}

// RecordWrapSession records a completed wrap session to the database.
func (d *DB) RecordWrapSession(session WrapSession) error {
	if d == nil || d.conn == nil {
		return fmt.Errorf("db is not open")
	}

	provider := strings.TrimSpace(session.Provider)
	profile := strings.TrimSpace(session.ProfileName)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if profile == "" {
		return fmt.Errorf("profile name is required")
	}

	startedAt := session.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now()
	}

	endedAt := session.EndedAt
	if endedAt.IsZero() {
		endedAt = time.Now()
	}

	// Calculate duration if not provided
	duration := session.DurationSeconds
	if duration <= 0 && !endedAt.Before(startedAt) {
		duration = int(endedAt.Sub(startedAt).Seconds())
	}

	// Calculate estimated cost if not provided
	estimatedCost := session.EstimatedCostCents
	if estimatedCost <= 0 {
		rate, err := d.GetCostRate(provider)
		if err == nil && rate != nil {
			// Cost = per-session + (per-minute * minutes)
			minutes := float64(duration) / 60.0
			estimatedCost = rate.CentsPerSession + int(float64(rate.CentsPerMinute)*minutes)
		}
	}

	rateLimitHit := 0
	if session.RateLimitHit {
		rateLimitHit = 1
	}

	_, err := d.conn.Exec(
		`INSERT INTO wrap_sessions (provider, profile_name, started_at, ended_at, duration_seconds, exit_code, rate_limit_hit, estimated_cost_cents, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		provider,
		profile,
		formatSQLiteTime(startedAt),
		formatSQLiteTime(endedAt),
		duration,
		session.ExitCode,
		rateLimitHit,
		estimatedCost,
		session.Notes,
	)
	if err != nil {
		return fmt.Errorf("insert wrap_sessions: %w", err)
	}

	return nil
}

// GetCostRate returns the cost rate configuration for a provider.
func (d *DB) GetCostRate(provider string) (*CostRate, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	if provider == "" {
		return nil, fmt.Errorf("provider is required")
	}

	var rate CostRate
	var updatedAtStr string

	err := d.conn.QueryRow(
		`SELECT provider, cents_per_minute, cents_per_session, updated_at
		 FROM cost_rates
		 WHERE provider = ?`,
		provider,
	).Scan(&rate.Provider, &rate.CentsPerMinute, &rate.CentsPerSession, &updatedAtStr)

	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query cost_rates: %w", err)
	}

	if ts, err := parseSQLiteTime(updatedAtStr); err == nil {
		rate.UpdatedAt = ts
	}

	return &rate, nil
}

// SetCostRate sets or updates the cost rate for a provider.
func (d *DB) SetCostRate(provider string, centsPerMinute, centsPerSession int) error {
	if d == nil || d.conn == nil {
		return fmt.Errorf("db is not open")
	}

	provider = strings.TrimSpace(provider)
	if provider == "" {
		return fmt.Errorf("provider is required")
	}

	_, err := d.conn.Exec(
		`INSERT INTO cost_rates (provider, cents_per_minute, cents_per_session, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(provider) DO UPDATE SET
		   cents_per_minute = excluded.cents_per_minute,
		   cents_per_session = excluded.cents_per_session,
		   updated_at = excluded.updated_at`,
		provider,
		centsPerMinute,
		centsPerSession,
		formatSQLiteTime(time.Now()),
	)
	if err != nil {
		return fmt.Errorf("upsert cost_rates: %w", err)
	}

	return nil
}

// GetAllCostRates returns all configured cost rates.
func (d *DB) GetAllCostRates() ([]CostRate, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	rows, err := d.conn.Query(
		`SELECT provider, cents_per_minute, cents_per_session, updated_at
		 FROM cost_rates
		 ORDER BY provider`,
	)
	if err != nil {
		return nil, fmt.Errorf("query cost_rates: %w", err)
	}
	defer rows.Close()

	var rates []CostRate
	for rows.Next() {
		var rate CostRate
		var updatedAtStr string
		if err := rows.Scan(&rate.Provider, &rate.CentsPerMinute, &rate.CentsPerSession, &updatedAtStr); err != nil {
			return nil, fmt.Errorf("scan cost_rates: %w", err)
		}
		if ts, err := parseSQLiteTime(updatedAtStr); err == nil {
			rate.UpdatedAt = ts
		}
		rates = append(rates, rate)
	}

	return rates, rows.Err()
}

// GetWrapSessions returns wrap sessions, optionally filtered by provider and time range.
func (d *DB) GetWrapSessions(provider string, since time.Time, limit int) ([]WrapSession, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	if limit <= 0 {
		limit = 100
	}

	var rows *sql.Rows
	var err error

	provider = strings.TrimSpace(provider)
	sinceStr := formatSQLiteTime(since)

	if provider != "" {
		rows, err = d.conn.Query(
			`SELECT id, provider, profile_name, started_at, ended_at, duration_seconds, exit_code, rate_limit_hit, estimated_cost_cents, notes
			 FROM wrap_sessions
			 WHERE provider = ? AND datetime(started_at) >= datetime(?)
			 ORDER BY started_at DESC
			 LIMIT ?`,
			provider, sinceStr, limit,
		)
	} else {
		rows, err = d.conn.Query(
			`SELECT id, provider, profile_name, started_at, ended_at, duration_seconds, exit_code, rate_limit_hit, estimated_cost_cents, notes
			 FROM wrap_sessions
			 WHERE datetime(started_at) >= datetime(?)
			 ORDER BY started_at DESC
			 LIMIT ?`,
			sinceStr, limit,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("query wrap_sessions: %w", err)
	}
	defer rows.Close()

	var sessions []WrapSession
	for rows.Next() {
		var s WrapSession
		var startedAtStr, endedAtStr string
		var rateLimitHit int
		var notes sql.NullString

		if err := rows.Scan(&s.ID, &s.Provider, &s.ProfileName, &startedAtStr, &endedAtStr,
			&s.DurationSeconds, &s.ExitCode, &rateLimitHit, &s.EstimatedCostCents, &notes); err != nil {
			return nil, fmt.Errorf("scan wrap_sessions: %w", err)
		}

		if ts, err := parseSQLiteTime(startedAtStr); err == nil {
			s.StartedAt = ts
		}
		if ts, err := parseSQLiteTime(endedAtStr); err == nil {
			s.EndedAt = ts
		}
		s.RateLimitHit = rateLimitHit != 0
		if notes.Valid {
			s.Notes = notes.String
		}

		sessions = append(sessions, s)
	}

	return sessions, rows.Err()
}

// GetCostSummary returns aggregated cost summary, optionally filtered by provider and time range.
func (d *DB) GetCostSummary(provider string, since time.Time) ([]CostSummary, error) {
	if d == nil || d.conn == nil {
		return nil, fmt.Errorf("db is not open")
	}

	sinceStr := formatSQLiteTime(since)

	var rows *sql.Rows
	var err error

	provider = strings.TrimSpace(provider)

	if provider != "" {
		rows, err = d.conn.Query(
			`SELECT provider,
			        COUNT(*) as total_sessions,
			        COALESCE(SUM(duration_seconds), 0) as total_duration,
			        COALESCE(SUM(estimated_cost_cents), 0) as total_cost,
			        COALESCE(SUM(rate_limit_hit), 0) as rate_limit_hits,
			        COALESCE(AVG(duration_seconds), 0) as avg_duration
			 FROM wrap_sessions
			 WHERE provider = ? AND datetime(started_at) >= datetime(?)
			 GROUP BY provider
			 ORDER BY total_cost DESC`,
			provider, sinceStr,
		)
	} else {
		rows, err = d.conn.Query(
			`SELECT provider,
			        COUNT(*) as total_sessions,
			        COALESCE(SUM(duration_seconds), 0) as total_duration,
			        COALESCE(SUM(estimated_cost_cents), 0) as total_cost,
			        COALESCE(SUM(rate_limit_hit), 0) as rate_limit_hits,
			        COALESCE(AVG(duration_seconds), 0) as avg_duration
			 FROM wrap_sessions
			 WHERE datetime(started_at) >= datetime(?)
			 GROUP BY provider
			 ORDER BY total_cost DESC`,
			sinceStr,
		)
	}

	if err != nil {
		return nil, fmt.Errorf("query wrap_sessions summary: %w", err)
	}
	defer rows.Close()

	var summaries []CostSummary
	for rows.Next() {
		var s CostSummary
		if err := rows.Scan(&s.Provider, &s.TotalSessions, &s.TotalDurationSecs,
			&s.TotalCostCents, &s.RateLimitHits, &s.AverageDurationSec); err != nil {
			return nil, fmt.Errorf("scan wrap_sessions summary: %w", err)
		}
		summaries = append(summaries, s)
	}

	return summaries, rows.Err()
}

// GetTotalCost returns the total estimated cost in cents for all providers since a given time.
func (d *DB) GetTotalCost(since time.Time) (int, error) {
	if d == nil || d.conn == nil {
		return 0, fmt.Errorf("db is not open")
	}

	sinceStr := formatSQLiteTime(since)

	var total int
	err := d.conn.QueryRow(
		`SELECT COALESCE(SUM(estimated_cost_cents), 0)
		 FROM wrap_sessions
		 WHERE datetime(started_at) >= datetime(?)`,
		sinceStr,
	).Scan(&total)

	if err != nil {
		return 0, fmt.Errorf("query total cost: %w", err)
	}

	return total, nil
}
