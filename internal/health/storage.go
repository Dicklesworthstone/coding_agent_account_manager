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
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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

	// PlanType is the subscription tier as the provider reports it,
	// lowercased (free, pro, plus, team, max, ultra, premium, enterprise).
	// Scorers rank it through PlanTierOf; it is never collapsed to a single
	// paid spelling, so "max" stays "max" in storage and output.
	PlanType string `json:"plan_type,omitempty"`

	// LastChecked is when the stored expiry was last written. It records a
	// local file read, not a provider check; see LastVerifiedAt for that.
	LastChecked time.Time `json:"last_checked,omitempty"`

	// LastVerifiedAt is when the provider last accepted this profile's
	// credential (issue #108), and VerifiedFingerprint identifies the
	// credential it accepted.
	LastVerifiedAt      time.Time `json:"last_verified_at,omitempty"`
	VerifiedFingerprint string    `json:"verified_fingerprint,omitempty"`

	// ProviderRejectedAt is when the provider last rejected this profile's
	// credential (a 401/403, or a refresh refused as revoked, reused or
	// expired). ProviderRejection is a short non-secret reason code and
	// RejectedFingerprint identifies the rejected credential, so a new login
	// clears the rejection by replacing the credential (issue #108).
	ProviderRejectedAt  time.Time `json:"provider_rejected_at,omitempty"`
	ProviderRejection   string    `json:"provider_rejection,omitempty"`
	RejectedFingerprint string    `json:"rejected_fingerprint,omitempty"`

	// CredentialFingerprint identifies the credential currently on disk for
	// this profile. Set at report time alongside TokenExpiresAt and never
	// persisted; ProviderRejected compares it with RejectedFingerprint.
	CredentialFingerprint string `json:"-"`

	// RateLimitedUntil is the end of an active rate-limit cooldown, filled in
	// at report time from the limit_events table. It is never persisted here:
	// the DB owns cooldown state. When set and in the future, the cap is the
	// operative constraint and must be reported as "rate limited" rather than
	// letting a (possibly stale) TokenExpiresAt masquerade as a token-expiry
	// problem.
	RateLimitedUntil time.Time `json:"-"`

	// SelfRefreshing marks TokenExpiresAt as the expiry of an access token
	// the provider's own CLI renews in place (Claude Code with a refresh
	// token, or Cursor with a stored API key). Set at report time alongside
	// TokenExpiresAt and never persisted: the token's TTL is then informational only and must
	// not lower the verdict, list a reason, or recommend a refresh caam
	// cannot perform (PR #84).
	SelfRefreshing bool `json:"-"`

	// TokenRenewable marks TokenExpiresAt as the expiry of an access token
	// that can be renewed without a human re-authenticating — a refresh token
	// is stored beside it, or the provider's CLI renews it in place. Set at
	// report time alongside TokenExpiresAt and never persisted.
	//
	// It is the "does this account need a re-login?" half of what
	// SelfRefreshing used to answer alone. Codex sets it without setting
	// SelfRefreshing: caam still wants to be told the access token is near
	// expiry (it has a Codex refresher), but a lapsed-yet-refreshable token
	// must not be reported as an expired account (issue #102).
	TokenRenewable bool `json:"-"`

	// ReloginWarningLead is extra warning time for a non-renewable login with a hard
	// deadline, such as a Cursor session. Derived alongside TokenExpiresAt at
	// report time and never persisted, so switching to an API-key credential
	// immediately removes the relogin warning.
	ReloginWarningLead time.Duration `json:"-"`
}

// RateLimited reports whether an active rate-limit cooldown is in effect.
func (h *ProfileHealth) RateLimited(now time.Time) bool {
	return h != nil && !h.RateLimitedUntil.IsZero() && h.RateLimitedUntil.After(now)
}

// CredentialRenewable reports whether the recorded token expiry can be
// resolved without a human logging in again. It is the predicate the
// user-facing verdict keys on: a renewable credential's TTL says nothing
// about whether the account works.
func (h *ProfileHealth) CredentialRenewable() bool {
	return h != nil && (h.SelfRefreshing || h.TokenRenewable)
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
	path      string
	vaultPath string
	mu        sync.RWMutex
}

// NewStorage creates a new health storage manager.
// If path is empty, uses the default path.
func NewStorage(path string) *Storage {
	if path == "" {
		path = DefaultHealthPath()
	}
	return &Storage{path: path}
}

// SetVaultPath binds report-time credential reads to the vault used by the
// caller. An empty path restores the default vault beside health.json. It
// changes neither persisted health metadata nor any credential file.
func (s *Storage) SetVaultPath(root string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.vaultPath = root
}

// vaultPathLocked returns the configured vault root. Caller holds s.mu.
func (s *Storage) vaultPathLocked() string {
	if s.vaultPath != "" {
		return s.vaultPath
	}
	return filepath.Join(filepath.Dir(s.path), "vault")
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

// GetProfile combines persisted health metadata with the current vault
// credential. It returns nil only when neither source knows the profile.
// Cached expiry alone is not credential evidence: its renewal semantics were
// deliberately not persisted, and the credential may have since been replaced.
func (s *Storage) GetProfile(provider, name string) (*ProfileHealth, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	store, err := s.loadLocked()
	if err != nil {
		return nil, err
	}

	key := profileKey(provider, name)
	return hydrateVaultHealth(s.vaultPathLocked(), provider, name, store.Profiles[key]), nil
}

// hydrateVaultHealth never persists its result. Both the expiry and the means
// to renew it belong to the current credential, not to health.json. In
// particular, keeping only a stored expiry would turn an expired renewable
// access token into a hard login deadline after every process restart.
func hydrateVaultHealth(root, provider, name string, stored *ProfileHealth) *ProfileHealth {
	var h *ProfileHealth
	if stored != nil {
		copy := *stored
		h = &copy
	}
	var candidates []string
	switch provider {
	case "claude":
		candidates = []string{".credentials.json", ".claude.json", "auth.json", filepath.Join("claude-code", "auth.json")}
	case "gemini":
		// ParseGeminiExpiry validates the selected source itself. An unused
		// cache must not invalidate an explicitly selected API key.
		candidates = []string{"settings.json"}
	case "codex", "cursor", "grok":
		candidates = []string{"auth.json"}
	default:
		return h
	}
	if h != nil {
		h.TokenExpiresAt = time.Time{}
		h.TokenRenewable = false
		h.SelfRefreshing = false
		h.ReloginWarningLead = 0
		h.CredentialFingerprint = ""
	}
	if !validVaultProfileName(name) {
		return h
	}
	dir := filepath.Join(root, provider, name)
	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return h
	}
	if h == nil {
		h = &ProfileHealth{}
	}
	// Do not open a device or FIFO through a passive status read. Symlinks
	// to regular files are supported, like adopted profile credentials.
	for _, candidate := range candidates {
		path := filepath.Join(dir, candidate)
		fi, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			fi, err = os.Stat(path)
		}
		if err != nil || !fi.Mode().IsRegular() {
			return h
		}
	}
	var info *ExpiryInfo
	switch provider {
	case "claude":
		// A present primary credential is authoritative. Do not revive an
		// older login from an optional state file when the primary is bad.
		primary := filepath.Join(dir, ".credentials.json")
		if _, statErr := os.Stat(primary); statErr == nil {
			info, err = parseClaudeCredentialsFile(primary)
			if info != nil {
				info.Renewable = info.HasRefreshToken
				info.SelfRefreshing = info.HasRefreshToken
			}
		} else {
			info, err = ParseClaudeExpiry(dir)
		}
	case "codex":
		info, err = ParseCodexExpiry(filepath.Join(dir, "auth.json"))
	case "cursor":
		info, err = ParseCursorExpiry(filepath.Join(dir, "auth.json"))
	case "gemini":
		info, err = ParseGeminiExpiry(dir)
	case "grok":
		info, err = ParseGrokExpiry(filepath.Join(dir, "auth.json"))
	}
	// Expiry fields without credential material are not proof of a login.
	// A parse failure is also not evidence that a rejected login changed.
	if err != nil || info == nil || info.Fingerprint == "" {
		return h
	}
	h.TokenExpiresAt = info.ExpiresAt
	h.TokenRenewable = info.Renewable
	h.SelfRefreshing = info.SelfRefreshing
	h.ReloginWarningLead = info.ReloginWarningLead
	h.CredentialFingerprint = info.Fingerprint
	return h
}

func validVaultProfileName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`)
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

// ListProfiles returns current health for persisted profiles and profiles in
// the vault, including credentials never written to health.json.
func (s *Storage) ListProfiles() (map[string]*ProfileHealth, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	store, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	vaultRoot := s.vaultPathLocked()
	for _, provider := range []string{"claude", "codex", "gemini", "grok", "cursor"} {
		entries, err := os.ReadDir(filepath.Join(vaultRoot, provider))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() && validVaultProfileName(entry.Name()) && !authfile.IsSystemProfile(entry.Name()) {
				key := profileKey(provider, entry.Name())
				if _, exists := store.Profiles[key]; !exists {
					store.Profiles[key] = nil
				}
			}
		}
	}
	result := make(map[string]*ProfileHealth, len(store.Profiles))
	for k, v := range store.Profiles {
		if provider, name, ok := strings.Cut(k, "/"); ok {
			if h := hydrateVaultHealth(vaultRoot, provider, name, v); h != nil {
				result[k] = h
			}
		} else if v != nil {
			copy := *v
			result[k] = &copy
		}
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
