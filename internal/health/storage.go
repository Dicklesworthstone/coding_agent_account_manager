// Package health manages profile health metadata for smart profile management.
//
// Health data includes token expiry times, error counts, penalties, and plan types.
// This information enables intelligent profile recommendations and proactive token refresh.
//
// Inspired by codex-pool's sophisticated account scoring system.
package health

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ProfileHealth holds health metadata for a single profile.
type ProfileHealth struct {
	// TokenExpiresAt is when the OAuth token expires.
	TokenExpiresAt time.Time `json:"token_expires_at,omitempty"`

	// LastError is when the last error occurred.
	LastError time.Time `json:"last_error,omitempty"`

	// ErrorCount1h is the number of errors in the last hour.
	ErrorCount1h int `json:"error_count_1h"`

	// Penalty is the current penalty score (decays over time).
	// Higher penalty = less desirable profile.
	Penalty float64 `json:"penalty"`

	// PenaltyUpdatedAt is when the penalty was last updated.
	PenaltyUpdatedAt time.Time `json:"penalty_updated_at,omitempty"`

	// PlanType is the subscription tier as the provider reports it, lowercased
	// (free, pro, plus, team, max, ultra, premium, enterprise). Ranked by
	// PlanTierOf; never collapsed to a single paid spelling.
	PlanType string `json:"plan_type,omitempty"`

	// LastChecked is when health was last verified.
	LastChecked time.Time `json:"last_checked,omitempty"`

	// RateLimitedUntil is the end of an active rate-limit cooldown, filled in
	// at report time from the limit_events table. It is never persisted here:
	// the DB owns cooldown state. When set and in the future, the cap is the
	// operative constraint and must be reported as "rate limited" rather than
	// letting a (possibly stale) TokenExpiresAt masquerade as a token-expiry
	// problem.
	RateLimitedUntil time.Time `json:"-"`
}

// RateLimited reports whether an active rate-limit cooldown is in effect.
func (h *ProfileHealth) RateLimited(now time.Time) bool {
	return h != nil && !h.RateLimitedUntil.IsZero() && h.RateLimitedUntil.After(now)
}

// HealthStore holds health data for all profiles.
type HealthStore struct {
	// Version is the schema version for future migrations.
	Version int `json:"version"`

	// Profiles maps "provider/name" to health data.
	Profiles map[string]*ProfileHealth `json:"profiles"`

	// UpdatedAt is when the store was last modified.
	UpdatedAt time.Time `json:"updated_at"`
}

// Storage manages health metadata persistence.
type Storage struct {
	path string
	mu   sync.RWMutex
}

// NewStorage creates a new health storage manager.
// If path is empty, uses the default path.
func NewStorage(path string) *Storage {
	if path == "" {
		path = DefaultHealthPath()
	}
	return &Storage{path: path}
}

// DefaultHealthPath returns the default health file location.
func DefaultHealthPath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "data", "health.json")
	}
	if xdgData := os.Getenv("XDG_DATA_HOME"); xdgData != "" {
		return filepath.Join(xdgData, "caam", "health.json")
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "share", "caam", "health.json")
	}
	return filepath.Join(homeDir, ".local", "share", "caam", "health.json")
}

// profileKey generates the map key for a provider/profile combination.
func profileKey(provider, name string) string {
	return provider + "/" + name
}

// Load reads health data from disk.
// Returns an empty store if the file doesn't exist.
func (s *Storage) Load() (*HealthStore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.loadLocked()
}

// loadLocked reads health data without acquiring a lock.
// Caller must hold at least a read lock.
func (s *Storage) loadLocked() (*HealthStore, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return newHealthStore(), nil
		}
		return nil, fmt.Errorf("read health file: %w", err)
	}

	store := newHealthStore()
	if err := json.Unmarshal(data, store); err != nil {
		// Log warning about corrupted file but continue with empty store
		// to allow recovery. The corrupted file will be overwritten on next save.
		slog.Warn("health file corrupted, starting fresh",
			"path", s.path,
			"error", err)
		return newHealthStore(), nil
	}

	// Ensure profiles map is initialized
	if store.Profiles == nil {
		store.Profiles = make(map[string]*ProfileHealth)
	}

	return store, nil
}

// Save writes health data to disk atomically.
func (s *Storage) Save(store *HealthStore) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.saveLocked(store)
}

// saveLocked writes health data without acquiring a lock.
// Caller must hold a write lock.
func (s *Storage) saveLocked(store *HealthStore) error {
	// Ensure directory exists
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create health dir: %w", err)
	}

	store.UpdatedAt = time.Now()

	data, err := json.MarshalIndent(store, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal health data: %w", err)
	}

	// Atomic write: write to temp file, fsync, then rename
	tmpPath := s.path + ".tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp file: %w", err)
	}

	// Sync to disk before rename to ensure durability
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, s.path); err != nil {
		os.Remove(tmpPath) // Clean up on failure
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// GetProfile returns health data for a specific profile.
// Returns nil if the profile has no health data.
func (s *Storage) GetProfile(provider, name string) (*ProfileHealth, error) {
	store, err := s.Load()
	if err != nil {
		return nil, err
	}

	key := profileKey(provider, name)
	return store.Profiles[key], nil
}

// UpdateProfile updates or creates health data for a profile.
func (s *Storage) UpdateProfile(provider, name string, health *ProfileHealth) error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	store.Profiles[key] = health

	return s.saveLocked(store)
}

// DeleteProfile removes health data for a profile.
func (s *Storage) DeleteProfile(provider, name string) error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	delete(store.Profiles, key)

	return s.saveLocked(store)
}

// RecordError increments the error count for a profile and applies a penalty.
func (s *Storage) RecordError(provider, name string, errCause error) error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	health := store.Profiles[key]
	if health == nil {
		health = &ProfileHealth{}
		store.Profiles[key] = health
	}

	health.ErrorCount1h++
	health.LastError = time.Now()

	// Apply penalty for errors
	penaltyAmount := PenaltyForError(errCause)
	health.AddPenalty(penaltyAmount, time.Now())

	return s.saveLocked(store)
}

// ClearErrors resets the error count for a profile.
func (s *Storage) ClearErrors(provider, name string) error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	health := store.Profiles[key]
	if health == nil {
		return nil // Nothing to clear
	}

	health.ErrorCount1h = 0
	health.LastError = time.Time{}

	return s.saveLocked(store)
}

// SetTokenExpiry updates the token expiry time for a profile.
func (s *Storage) SetTokenExpiry(provider, name string, expiresAt time.Time) error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	health := store.Profiles[key]
	if health == nil {
		health = &ProfileHealth{}
		store.Profiles[key] = health
	}

	health.TokenExpiresAt = expiresAt
	health.LastChecked = time.Now()

	return s.saveLocked(store)
}

// SetPlanType updates the plan type for a profile.
func (s *Storage) SetPlanType(provider, name, planType string) error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	health := store.Profiles[key]
	if health == nil {
		health = &ProfileHealth{}
		store.Profiles[key] = health
	}

	health.PlanType = planType

	return s.saveLocked(store)
}

// DecayPenalties applies penalty decay to all profiles.
// Call this periodically (e.g., every 5 minutes).
func (s *Storage) DecayPenalties() error {
	// Hold lock for entire read-modify-write cycle to prevent TOCTOU race
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	now := time.Now()
	modified := false

	for _, health := range store.Profiles {
		oldPenalty := health.Penalty
		health.DecayPenalty(now)
		if health.Penalty != oldPenalty {
			modified = true
		}
	}

	if modified {
		return s.saveLocked(store)
	}
	return nil
}

// GetStatus calculates the overall health status for a profile.
func (s *Storage) GetStatus(provider, name string) (HealthStatus, error) {
	health, err := s.GetProfile(provider, name)
	if err != nil {
		return StatusUnknown, err
	}
	if health == nil {
		return StatusUnknown, nil
	}

	return CalculateStatus(health), nil
}

// ListProfiles returns a copy of all profiles with health data.
func (s *Storage) ListProfiles() (map[string]*ProfileHealth, error) {
	store, err := s.Load()
	if err != nil {
		return nil, err
	}
	// Return a copy to prevent external modification of internal state
	result := make(map[string]*ProfileHealth, len(store.Profiles))
	for k, v := range store.Profiles {
		// Deep copy the ProfileHealth struct
		copy := *v
		result[k] = &copy
	}
	return result, nil
}

// Path returns the storage file path.
func (s *Storage) Path() string {
	return s.path
}

func (s *Storage) acquireFileLock() (*os.File, error) {
	lockPath := s.path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := LockFile(f); err != nil {
		f.Close()
		return nil, fmt.Errorf("lock file: %w", err)
	}
	return f, nil
}

func (s *Storage) releaseFileLock(f *os.File) {
	if f != nil {
		UnlockFile(f)
		f.Close()
	}
}

// newHealthStore creates an initialized HealthStore.
func newHealthStore() *HealthStore {
	return &HealthStore{
		Version:  1,
		Profiles: make(map[string]*ProfileHealth),
	}
}
