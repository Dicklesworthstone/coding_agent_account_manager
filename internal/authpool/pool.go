package authpool

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// AuthPool manages a pool of authentication profiles and their states.
// It tracks token freshness, handles refresh coordination, and provides
// profile selection for rotation.
type AuthPool struct {
	mu       sync.RWMutex
	profiles map[string]*PooledProfile // key: "provider:profile"

	// Configuration
	refreshThreshold time.Duration // Refresh when this close to expiry
	cooldownDuration time.Duration // Default cooldown after rate limit
	maxRetries       int           // Max consecutive errors before marking error

	// Dependencies
	vault *authfile.Vault

	// Callbacks
	onStateChange func(profile *PooledProfile, oldStatus, newStatus PoolStatus)
}

// PoolOption configures an AuthPool.
type PoolOption func(*AuthPool)

// WithRefreshThreshold sets how early to refresh before expiry.
func WithRefreshThreshold(d time.Duration) PoolOption {
	return func(p *AuthPool) {
		p.refreshThreshold = d
	}
}

// WithCooldownDuration sets the default rate limit cooldown.
func WithCooldownDuration(d time.Duration) PoolOption {
	return func(p *AuthPool) {
		p.cooldownDuration = d
	}
}

// WithMaxRetries sets the maximum consecutive errors before marking error state.
func WithMaxRetries(n int) PoolOption {
	return func(p *AuthPool) {
		p.maxRetries = n
	}
}

// WithVault sets the auth vault for profile data.
func WithVault(v *authfile.Vault) PoolOption {
	return func(p *AuthPool) {
		p.vault = v
	}
}

// WithOnStateChange sets a callback for profile state changes.
func WithOnStateChange(fn func(profile *PooledProfile, oldStatus, newStatus PoolStatus)) PoolOption {
	return func(p *AuthPool) {
		p.onStateChange = fn
	}
}

// NewAuthPool creates a new authentication pool.
func NewAuthPool(opts ...PoolOption) *AuthPool {
	p := &AuthPool{
		profiles:         make(map[string]*PooledProfile),
		refreshThreshold: 5 * time.Minute,   // Default: refresh 5 min before expiry
		cooldownDuration: 5 * time.Minute,   // Default: 5 min cooldown
		maxRetries:       3,                 // Default: 3 retries
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// profileKey generates the map key for a provider:profile pair.
func profileKey(provider, name string) string {
	return provider + ":" + name
}

// AddProfile adds a profile to the pool or updates if exists.
func (p *AuthPool) AddProfile(provider, name string) *PooledProfile {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	if existing, ok := p.profiles[key]; ok {
		return existing.Clone()
	}

	profile := &PooledProfile{
		Provider:    provider,
		ProfileName: name,
		Status:      PoolStatusUnknown,
		LastCheck:   time.Now(),
	}

	p.profiles[key] = profile
	return profile.Clone()
}

// RemoveProfile removes a profile from the pool.
func (p *AuthPool) RemoveProfile(provider, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	delete(p.profiles, key)
}

// GetProfile returns a copy of a profile's state.
// Returns nil if the profile is not in the pool.
func (p *AuthPool) GetProfile(provider, name string) *PooledProfile {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := profileKey(provider, name)
	if profile, ok := p.profiles[key]; ok {
		return profile.Clone()
	}
	return nil
}

// GetStatus returns the status of a profile.
// Returns PoolStatusUnknown if the profile is not in the pool.
func (p *AuthPool) GetStatus(provider, name string) PoolStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()

	key := profileKey(provider, name)
	if profile, ok := p.profiles[key]; ok {
		return profile.Status
	}
	return PoolStatusUnknown
}

// SetStatus updates a profile's status.
func (p *AuthPool) SetStatus(provider, name string, status PoolStatus) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		return fmt.Errorf("profile %s not found in pool", key)
	}

	oldStatus := profile.Status
	profile.Status = status
	profile.LastCheck = time.Now()

	// Clear error on success
	if status == PoolStatusReady {
		profile.ErrorCount = 0
		profile.ErrorMessage = ""
	}

	// Fire callback outside lock
	if p.onStateChange != nil && oldStatus != status {
		clone := profile.Clone()
		go p.onStateChange(clone, oldStatus, status)
	}

	return nil
}

// SetError records an error for a profile.
func (p *AuthPool) SetError(provider, name string, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		return
	}

	profile.ErrorCount++
	profile.ErrorMessage = err.Error()
	profile.LastCheck = time.Now()

	if profile.ErrorCount >= p.maxRetries {
		profile.Status = PoolStatusError
	}
}

// SetCooldown puts a profile into cooldown state.
func (p *AuthPool) SetCooldown(provider, name string, duration time.Duration) {
	if duration == 0 {
		duration = p.cooldownDuration
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		return
	}

	oldStatus := profile.Status
	profile.Status = PoolStatusCooldown
	profile.CooldownUntil = time.Now().Add(duration)
	profile.LastCheck = time.Now()

	if p.onStateChange != nil && oldStatus != PoolStatusCooldown {
		clone := profile.Clone()
		go p.onStateChange(clone, oldStatus, PoolStatusCooldown)
	}
}

// ClearCooldown removes cooldown status from a profile.
func (p *AuthPool) ClearCooldown(provider, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		return
	}

	if profile.Status == PoolStatusCooldown {
		profile.Status = PoolStatusReady
		profile.CooldownUntil = time.Time{}
	}
}

// UpdateTokenExpiry updates the token expiry time for a profile.
func (p *AuthPool) UpdateTokenExpiry(provider, name string, expiry time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	if profile, ok := p.profiles[key]; ok {
		profile.TokenExpiry = expiry
		profile.LastCheck = time.Now()

		// Update status based on expiry
		if profile.IsExpired() {
			profile.Status = PoolStatusExpired
		} else if profile.Status == PoolStatusExpired {
			profile.Status = PoolStatusReady
		}
	}
}

// MarkRefreshed marks a profile as successfully refreshed.
func (p *AuthPool) MarkRefreshed(provider, name string, newExpiry time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	if profile, ok := p.profiles[key]; ok {
		profile.Status = PoolStatusReady
		profile.TokenExpiry = newExpiry
		profile.LastRefresh = time.Now()
		profile.LastCheck = time.Now()
		profile.ErrorCount = 0
		profile.ErrorMessage = ""
	}
}

// MarkUsed updates the last used timestamp for a profile.
func (p *AuthPool) MarkUsed(provider, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	if profile, ok := p.profiles[key]; ok {
		profile.LastUsed = time.Now()
	}
}

// GetReadyProfiles returns all profiles that are ready to use.
func (p *AuthPool) GetReadyProfiles(provider string) []*PooledProfile {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*PooledProfile
	for _, profile := range p.profiles {
		if provider != "" && profile.Provider != provider {
			continue
		}
		if profile.Status == PoolStatusReady {
			result = append(result, profile.Clone())
		}
	}

	// Sort by priority (descending), then by last used (ascending, prefer least recently used)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Priority != result[j].Priority {
			return result[i].Priority > result[j].Priority
		}
		return result[i].LastUsed.Before(result[j].LastUsed)
	})

	return result
}

// GetProfilesNeedingRefresh returns profiles that need token refresh.
func (p *AuthPool) GetProfilesNeedingRefresh(provider string) []*PooledProfile {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*PooledProfile
	for _, profile := range p.profiles {
		if provider != "" && profile.Provider != provider {
			continue
		}
		if profile.Status.NeedsRefresh() || profile.IsExpiringSoon(p.refreshThreshold) {
			result = append(result, profile.Clone())
		}
	}

	return result
}

// GetProfilesInCooldown returns profiles currently in rate limit cooldown.
func (p *AuthPool) GetProfilesInCooldown(provider string) []*PooledProfile {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*PooledProfile
	for _, profile := range p.profiles {
		if provider != "" && profile.Provider != provider {
			continue
		}
		if profile.Status == PoolStatusCooldown && profile.IsInCooldown() {
			result = append(result, profile.Clone())
		}
	}

	return result
}

// GetAllProfiles returns all tracked profiles.
func (p *AuthPool) GetAllProfiles(provider string) []*PooledProfile {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*PooledProfile
	for _, profile := range p.profiles {
		if provider != "" && profile.Provider != provider {
			continue
		}
		result = append(result, profile.Clone())
	}

	return result
}

// SelectBest returns the best available profile for use.
// Returns nil if no profile is ready.
func (p *AuthPool) SelectBest(provider string) *PooledProfile {
	ready := p.GetReadyProfiles(provider)
	if len(ready) == 0 {
		return nil
	}
	return ready[0]
}

// Count returns the number of profiles in the pool.
func (p *AuthPool) Count() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.profiles)
}

// CountByStatus returns counts grouped by status.
func (p *AuthPool) CountByStatus() map[PoolStatus]int {
	p.mu.RLock()
	defer p.mu.RUnlock()

	counts := make(map[PoolStatus]int)
	for _, profile := range p.profiles {
		counts[profile.Status]++
	}
	return counts
}

// CheckAndUpdateCooldowns checks profiles in cooldown and clears expired cooldowns.
func (p *AuthPool) CheckAndUpdateCooldowns() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	cleared := 0
	now := time.Now()

	for _, profile := range p.profiles {
		if profile.Status == PoolStatusCooldown && !profile.CooldownUntil.IsZero() {
			if now.After(profile.CooldownUntil) {
				profile.Status = PoolStatusReady
				profile.CooldownUntil = time.Time{}
				cleared++
			}
		}
	}

	return cleared
}

// LoadFromVault populates the pool from the vault's profiles.
// Profiles are added with unknown status - callers should validate
// and update status separately.
func (p *AuthPool) LoadFromVault(ctx context.Context) error {
	if p.vault == nil {
		return fmt.Errorf("vault not configured")
	}

	// Use ListAll to get all providers and profiles at once
	allProfiles, err := p.vault.ListAll()
	if err != nil {
		return fmt.Errorf("listing vault profiles: %w", err)
	}

	for provider, profiles := range allProfiles {
		for _, name := range profiles {
			p.AddProfile(provider, name)
		}
	}

	return nil
}

// Summary returns a summary of pool state.
type PoolSummary struct {
	TotalProfiles int            `json:"total_profiles"`
	ByStatus      map[string]int `json:"by_status"`
	ByProvider    map[string]int `json:"by_provider"`
	ReadyCount    int            `json:"ready_count"`
	CooldownCount int            `json:"cooldown_count"`
	ErrorCount    int            `json:"error_count"`
}

// Summary returns a summary of the pool state.
func (p *AuthPool) Summary() *PoolSummary {
	p.mu.RLock()
	defer p.mu.RUnlock()

	summary := &PoolSummary{
		TotalProfiles: len(p.profiles),
		ByStatus:      make(map[string]int),
		ByProvider:    make(map[string]int),
	}

	for _, profile := range p.profiles {
		summary.ByStatus[profile.Status.String()]++
		summary.ByProvider[profile.Provider]++

		switch profile.Status {
		case PoolStatusReady:
			summary.ReadyCount++
		case PoolStatusCooldown:
			summary.CooldownCount++
		case PoolStatusError:
			summary.ErrorCount++
		}
	}

	return summary
}
