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

	// PlanType is the subscription tier (free, pro, enterprise).
	PlanType string `json:"plan_type,omitempty"`

	// LastChecked is when health was last verified.
	LastChecked time.Time `json:"last_checked,omitempty"`
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

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return newHealthStore(), nil
		}
		return nil, fmt.Errorf("read health file: %w", err)
	}

	store := newHealthStore()
	if err := json.Unmarshal(data, store); err != nil {
		// Return empty store on parse error rather than failing
		// This handles corrupted files gracefully
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

	// Atomic write: write to temp file then rename
	tmpPath := s.path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0600); err != nil {
		return fmt.Errorf("write temp file: %w", err)
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
	store, err := s.Load()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	store.Profiles[key] = health

	return s.Save(store)
}

// DeleteProfile removes health data for a profile.
func (s *Storage) DeleteProfile(provider, name string) error {
	store, err := s.Load()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	delete(store.Profiles, key)

	return s.Save(store)
}

// RecordError increments the error count for a profile and applies a penalty.
func (s *Storage) RecordError(provider, name string, errCause error) error {
	store, err := s.Load()
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

	return s.Save(store)
}

// ClearErrors resets the error count for a profile.
func (s *Storage) ClearErrors(provider, name string) error {
	store, err := s.Load()
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

	return s.Save(store)
}

// SetTokenExpiry updates the token expiry time for a profile.
func (s *Storage) SetTokenExpiry(provider, name string, expiresAt time.Time) error {
	store, err := s.Load()
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

	return s.Save(store)
}

// SetPlanType updates the plan type for a profile.
func (s *Storage) SetPlanType(provider, name, planType string) error {
	store, err := s.Load()
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

	return s.Save(store)
}

// DecayPenalties applies penalty decay to all profiles.
// Call this periodically (e.g., every 5 minutes).
func (s *Storage) DecayPenalties() error {
	store, err := s.Load()
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
		return s.Save(store)
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

// ListProfiles returns all profiles with health data.
func (s *Storage) ListProfiles() (map[string]*ProfileHealth, error) {
	store, err := s.Load()
	if err != nil {
		return nil, err
	}
	return store.Profiles, nil
}

// Path returns the storage file path.
func (s *Storage) Path() string {
	return s.path
}

// newHealthStore creates an initialized HealthStore.
func newHealthStore() *HealthStore {
	return &HealthStore{
		Version:  1,
		Profiles: make(map[string]*ProfileHealth),
	}
}
