package authpool

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// StateVersion is the current state file format version.
const StateVersion = 1

// DefaultStatePath returns the default path for the state file.
func DefaultStatePath() string {
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		home, _ := os.UserHomeDir()
		dataDir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dataDir, "caam", "auth_pool_state.json")
}

// PersistedPoolState represents the serializable state of an AuthPool.
type PersistedPoolState struct {
	Version   int                           `json:"version"`
	UpdatedAt time.Time                     `json:"updated_at"`
	Profiles  map[string]*PersistedProfile  `json:"profiles"`
}

// PersistedProfile represents a serializable profile state.
type PersistedProfile struct {
	Provider      string     `json:"provider"`
	ProfileName   string     `json:"profile_name"`
	TokenExpiry   time.Time  `json:"token_expiry,omitempty"`
	LastRefresh   time.Time  `json:"last_refresh,omitempty"`
	LastCheck     time.Time  `json:"last_check,omitempty"`
	LastUsed      time.Time  `json:"last_used,omitempty"`
	CooldownUntil time.Time  `json:"cooldown_until,omitempty"`
	Status        PoolStatus `json:"status"`
	ErrorCount    int        `json:"error_count,omitempty"`
	ErrorMessage  string     `json:"error_message,omitempty"`
	Priority      int        `json:"priority,omitempty"`
}

// PersistOptions configures persistence behavior.
type PersistOptions struct {
	// StatePath is the path to the state file.
	// If empty, uses DefaultStatePath().
	StatePath string
}

// Save persists the auth pool state to disk atomically.
// It writes to a temp file and then renames to ensure no partial writes.
func (p *AuthPool) Save(opts PersistOptions) error {
	statePath := opts.StatePath
	if statePath == "" {
		statePath = DefaultStatePath()
	}

	// Ensure directory exists
	dir := filepath.Dir(statePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	// Collect current state
	state := p.toPersistedState()

	// Marshal to JSON
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling state: %w", err)
	}

	// Write to temp file
	tmpFile, err := os.CreateTemp(dir, "auth_pool_state.*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmpFile.Name()

	// Clean up temp file on error
	success := false
	defer func() {
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Write data
	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return fmt.Errorf("writing state: %w", err)
	}

	// Sync to disk
	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return fmt.Errorf("syncing state: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tmpPath, statePath); err != nil {
		return fmt.Errorf("renaming state file: %w", err)
	}

	success = true
	return nil
}

// Load restores the auth pool state from disk.
// If the state file doesn't exist, it returns nil (no error).
// If the state file is corrupted, it returns an error.
func (p *AuthPool) Load(opts PersistOptions) error {
	statePath := opts.StatePath
	if statePath == "" {
		statePath = DefaultStatePath()
	}

	// Read state file
	file, err := os.Open(statePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No state file is OK (first run)
		}
		return fmt.Errorf("opening state file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("reading state file: %w", err)
	}

	// Parse JSON
	var state PersistedPoolState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parsing state file: %w", err)
	}

	// Check version compatibility
	if state.Version > StateVersion {
		return fmt.Errorf("state file version %d is newer than supported version %d", state.Version, StateVersion)
	}

	// Restore state
	p.fromPersistedState(&state)

	return nil
}

// LoadFromReader restores the auth pool state from an io.Reader.
// This is useful for testing or loading from non-file sources.
func (p *AuthPool) LoadFromReader(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("reading state: %w", err)
	}

	var state PersistedPoolState
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("parsing state: %w", err)
	}

	if state.Version > StateVersion {
		return fmt.Errorf("state version %d is newer than supported version %d", state.Version, StateVersion)
	}

	p.fromPersistedState(&state)
	return nil
}

// toPersistedState converts the AuthPool to a PersistedPoolState.
func (p *AuthPool) toPersistedState() *PersistedPoolState {
	p.mu.RLock()
	defer p.mu.RUnlock()

	state := &PersistedPoolState{
		Version:   StateVersion,
		UpdatedAt: time.Now(),
		Profiles:  make(map[string]*PersistedProfile),
	}

	for key, profile := range p.profiles {
		state.Profiles[key] = &PersistedProfile{
			Provider:      profile.Provider,
			ProfileName:   profile.ProfileName,
			TokenExpiry:   profile.TokenExpiry,
			LastRefresh:   profile.LastRefresh,
			LastCheck:     profile.LastCheck,
			LastUsed:      profile.LastUsed,
			CooldownUntil: profile.CooldownUntil,
			Status:        profile.Status,
			ErrorCount:    profile.ErrorCount,
			ErrorMessage:  profile.ErrorMessage,
			Priority:      profile.Priority,
		}
	}

	return state
}

// fromPersistedState restores the AuthPool from a PersistedPoolState.
func (p *AuthPool) fromPersistedState(state *PersistedPoolState) {
	p.mu.Lock()
	defer p.mu.Unlock()

	for key, persisted := range state.Profiles {
		profile := &PooledProfile{
			Provider:      persisted.Provider,
			ProfileName:   persisted.ProfileName,
			TokenExpiry:   persisted.TokenExpiry,
			LastRefresh:   persisted.LastRefresh,
			LastCheck:     persisted.LastCheck,
			LastUsed:      persisted.LastUsed,
			CooldownUntil: persisted.CooldownUntil,
			Status:        persisted.Status,
			ErrorCount:    persisted.ErrorCount,
			ErrorMessage:  persisted.ErrorMessage,
			Priority:      persisted.Priority,
		}
		p.profiles[key] = profile
	}
}

// StateExists checks if a state file exists at the given path.
func StateExists(opts PersistOptions) bool {
	statePath := opts.StatePath
	if statePath == "" {
		statePath = DefaultStatePath()
	}
	_, err := os.Stat(statePath)
	return err == nil
}

// RemoveState deletes the state file.
func RemoveState(opts PersistOptions) error {
	statePath := opts.StatePath
	if statePath == "" {
		statePath = DefaultStatePath()
	}
	err := os.Remove(statePath)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// MarshalJSON implements json.Marshaler for PoolStatus.
func (s PoolStatus) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// UnmarshalJSON implements json.Unmarshaler for PoolStatus.
func (s *PoolStatus) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}

	switch str {
	case "ready":
		*s = PoolStatusReady
	case "refreshing":
		*s = PoolStatusRefreshing
	case "expired":
		*s = PoolStatusExpired
	case "cooldown":
		*s = PoolStatusCooldown
	case "error":
		*s = PoolStatusError
	default:
		*s = PoolStatusUnknown
	}
	return nil
}
