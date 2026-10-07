package authpool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// AuthPool manages a pool of authentication profiles and their states.
// It tracks token freshness, handles refresh coordination, and provides
// profile selection for rotation.
type AuthPool struct {
	mu              sync.RWMutex
	profiles        map[string]*PooledProfile // key: "provider:profile"
	nextReservation uint64

	// Configuration
	refreshThreshold time.Duration // Refresh when this close to expiry
	cooldownDuration time.Duration // Default cooldown after rate limit
	maxRetries       int           // Max consecutive errors before marking error

	// Dependencies
	vault       *authfile.Vault
	healthStore *health.Storage

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

// WithHealthStorage supplies provider verdicts and current credential health.
// The storage is bound to the pool's vault after all options are applied.
func WithHealthStorage(storage *health.Storage) PoolOption {
	return func(p *AuthPool) { p.healthStore = storage }
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
		refreshThreshold: 5 * time.Minute, // Default: refresh 5 min before expiry
		cooldownDuration: 5 * time.Minute, // Default: 5 min cooldown
		maxRetries:       3,               // Default: 3 retries
	}

	for _, opt := range opts {
		opt(p)
	}
	if p.vault != nil {
		if p.healthStore == nil {
			p.healthStore = health.NewStorage("")
		}
		p.healthStore.SetVaultPath(p.vault.BasePath())
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

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		p.mu.Unlock()
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

	var shouldCallback bool
	var clone *PooledProfile
	if p.onStateChange != nil && oldStatus != status {
		shouldCallback = true
		clone = profile.Clone()
	}
	p.mu.Unlock()

	// Fire callback outside lock
	if shouldCallback {
		go p.onStateChange(clone, oldStatus, status)
	}

	return nil
}

// TryMarkRefreshing marks a profile as refreshing if it is not already.
// Returns the previous status and true if reserved, or false if the profile
// does not exist, is already refreshing, or is no longer eligible. Capture the
// status under the same lock so queued work cannot bypass a new cooldown.
func (p *AuthPool) TryMarkRefreshing(provider, name string) (PoolStatus, bool) {
	previous, _, reserved := p.reserveRefresh(provider, name)
	return previous, reserved
}

func (p *AuthPool) reserveRefresh(provider, name string) (PoolStatus, uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		return PoolStatusUnknown, 0, false
	}
	if profile.inFlight || profile.Status == PoolStatusRefreshing {
		return profile.Status, 0, false
	}
	if profile.refreshEligibilityError() != nil {
		return profile.Status, 0, false
	}

	previous := profile.Status
	profile.Status = PoolStatusRefreshing
	profile.inFlight = true
	p.nextReservation++
	profile.reservation = p.nextReservation
	profile.LastCheck = time.Now()
	return previous, profile.reservation, true
}

// refreshEligibilityError distinguishes invalid credentials from supported
// sources that renew natively or have no automatic renewal. Callers holding
// the pool lock use the same check immediately before reserving a worker.
func (p *PooledProfile) refreshEligibilityError() error {
	if p.vaultChecked && p.credentialStatus == PoolStatusError && !p.refreshable {
		return fmt.Errorf("%s/%s: %s", p.Provider, p.ProfileName, p.ErrorMessage)
	}
	if p.IsInCooldown() {
		return fmt.Errorf("%w: %s/%s is in cooldown", refresh.ErrUnsupported, p.Provider, p.ProfileName)
	}
	if p.Provider == "cursor" || (p.vaultChecked && !p.refreshable) {
		return fmt.Errorf("%w: automatic refresh is unavailable for %s/%s", refresh.ErrUnsupported, p.Provider, p.ProfileName)
	}
	return nil
}

// finishRefresh publishes only into the entry this worker reserved. A deleted
// and re-created profile can have the same name but belongs to another worker.
func (p *AuthPool) finishRefresh(provider, name string, reservation uint64, previous PoolStatus, expiry time.Time, attempted bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	profile := p.profiles[profileKey(provider, name)]
	if profile == nil || !profile.inFlight || profile.reservation != reservation {
		return
	}
	old := profile.Status
	profile.inFlight = false
	if profile.Status == PoolStatusRefreshing {
		profile.Status = previous
	}
	if attempted && (err == nil || refresh.IsDeliveryIncomplete(err)) {
		profile.Status = PoolStatusReady
		profile.TokenExpiry = expiry
		profile.LastRefresh = time.Now()
		profile.ErrorCount = 0
		profile.ErrorMessage = ""
	} else if attempted && !refresh.IsSkipped(err) && !errors.Is(err, context.Canceled) {
		profile.ErrorCount++
		profile.ErrorMessage = err.Error()
		if profile.ErrorCount >= p.maxRetries {
			profile.Status = PoolStatusError
		}
	}
	profile.LastCheck = time.Now()
	if profile.IsInCooldown() {
		profile.Status = PoolStatusCooldown
	}
	if p.onStateChange != nil && old != profile.Status {
		go p.onStateChange(profile.Clone(), old, profile.Status)
	}
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

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		p.mu.Unlock()
		return
	}

	oldStatus := profile.Status
	profile.Status = PoolStatusCooldown
	profile.CooldownUntil = time.Now().Add(duration)
	profile.LastCheck = time.Now()

	var shouldCallback bool
	var clone *PooledProfile
	if p.onStateChange != nil && oldStatus != PoolStatusCooldown {
		shouldCallback = true
		clone = profile.Clone()
	}
	p.mu.Unlock()

	// Fire callback outside lock
	if shouldCallback {
		go p.onStateChange(clone, oldStatus, PoolStatusCooldown)
	}
}

// ClearCooldown removes cooldown status from a profile.
func (p *AuthPool) ClearCooldown(provider, name string) {
	p.mu.Lock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		p.mu.Unlock()
		return
	}

	var shouldCallback bool
	var clone *PooledProfile
	if profile.Status == PoolStatusCooldown {
		profile.Status = PoolStatusReady
		if profile.vaultChecked {
			profile.Status = profile.credentialStatus
		}
		profile.CooldownUntil = time.Time{}
		if p.onStateChange != nil {
			shouldCallback = true
			clone = profile.Clone()
		}
	}
	p.mu.Unlock()

	// Fire callback outside lock
	if shouldCallback {
		go p.onStateChange(clone, PoolStatusCooldown, clone.Status)
	}
}

// UpdateTokenExpiry updates the token expiry time for a profile.
func (p *AuthPool) UpdateTokenExpiry(provider, name string, expiry time.Time) {
	p.mu.Lock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		p.mu.Unlock()
		return
	}

	oldStatus := profile.Status
	profile.TokenExpiry = expiry
	profile.LastCheck = time.Now()

	// Update status based on expiry
	if profile.IsExpired() {
		profile.Status = PoolStatusExpired
	} else if profile.Status == PoolStatusExpired {
		profile.Status = PoolStatusReady
	}

	var shouldCallback bool
	var clone *PooledProfile
	if p.onStateChange != nil && oldStatus != profile.Status {
		shouldCallback = true
		clone = profile.Clone()
	}
	newStatus := profile.Status
	p.mu.Unlock()

	// Fire callback outside lock
	if shouldCallback {
		go p.onStateChange(clone, oldStatus, newStatus)
	}
}

// MarkRefreshed marks a profile as successfully refreshed.
func (p *AuthPool) MarkRefreshed(provider, name string, newExpiry time.Time) {
	p.mu.Lock()

	key := profileKey(provider, name)
	profile, ok := p.profiles[key]
	if !ok {
		p.mu.Unlock()
		return
	}

	oldStatus := profile.Status
	profile.Status = PoolStatusReady
	if profile.IsInCooldown() {
		profile.Status = PoolStatusCooldown
	}
	profile.inFlight = false
	profile.TokenExpiry = newExpiry
	profile.LastRefresh = time.Now()
	profile.LastCheck = time.Now()
	profile.ErrorCount = 0
	profile.ErrorMessage = ""

	var shouldCallback bool
	var clone *PooledProfile
	if p.onStateChange != nil && oldStatus != profile.Status {
		shouldCallback = true
		clone = profile.Clone()
	}
	newStatus := profile.Status
	p.mu.Unlock()

	// Fire callback outside lock
	if shouldCallback {
		go p.onStateChange(clone, oldStatus, newStatus)
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
		if profile.inFlight || profile.IsInCooldown() || (profile.vaultChecked && !profile.refreshable) {
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

	cleared := 0
	now := time.Now()

	// Collect profiles that need callback (fire outside lock)
	var callbackProfiles []*PooledProfile

	for _, profile := range p.profiles {
		if profile.Status == PoolStatusCooldown && !profile.CooldownUntil.IsZero() {
			if now.After(profile.CooldownUntil) {
				profile.Status = PoolStatusReady
				if profile.vaultChecked {
					profile.Status = profile.credentialStatus
				}
				profile.CooldownUntil = time.Time{}
				cleared++
				if p.onStateChange != nil {
					callbackProfiles = append(callbackProfiles, profile.Clone())
				}
			}
		}
	}

	p.mu.Unlock()

	// Fire callbacks outside lock
	for _, clone := range callbackProfiles {
		go p.onStateChange(clone, PoolStatusCooldown, clone.Status)
	}

	return cleared
}

// LoadFromVault reconciles membership and current credential health. Operational
// cooldowns, priorities and in-flight reservations survive a reconciliation;
// stale persisted credential facts do not. This method never modifies the vault.
func (p *AuthPool) LoadFromVault(ctx context.Context) error {
	if p.vault == nil {
		return fmt.Errorf("vault not configured")
	}

	// Serialize local observations with reservations/results so an observation
	// begun before a refresh cannot overwrite its completion afterward.
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	allProfiles, err := p.vault.ListAll()
	if err != nil {
		return fmt.Errorf("listing vault profiles: %w", err)
	}

	present := make(map[string]bool)
	for provider, profiles := range allProfiles {
		fileSet, supported := authfile.GetAuthFileSet(provider)
		if !supported || fileSet.Tool != provider {
			continue
		}
		for _, name := range profiles {
			if err := ctx.Err(); err != nil {
				return err
			}
			if authfile.IsSystemProfile(name) {
				continue
			}
			key := profileKey(provider, name)
			present[key] = true
			profile := p.profiles[key]
			if profile == nil {
				profile = &PooledProfile{Provider: provider, ProfileName: name}
				p.profiles[key] = profile
			}
			if profile.inFlight {
				continue
			}
			oldStatus := profile.Status
			generation, credentialErr := p.vaultCredentialGeneration(fileSet, name)
			current, healthErr := p.healthStore.GetProfile(provider, name)
			if credentialErr == nil && healthErr != nil {
				credentialErr = fmt.Errorf("credential health unavailable")
			}
			if current != nil && current.CredentialFingerprint != "" {
				generation = current.CredentialFingerprint
			}
			changed := !profile.vaultChecked || (generation != "" && generation != profile.generation)
			profile.vaultChecked = true
			profile.generation = generation
			profile.LastCheck = time.Now()
			profile.TokenExpiry = time.Time{}
			profile.refreshable = false
			profile.credentialStatus = PoolStatusReady
			if changed {
				profile.ErrorCount = 0
				profile.ErrorMessage = ""
			}
			if credentialErr != nil {
				profile.credentialStatus = PoolStatusError
				profile.ErrorMessage = "saved credentials are missing or invalid"
			} else if current != nil {
				profile.TokenExpiry = current.TokenExpiresAt
				profile.refreshable = (provider == "codex" || provider == "gemini") && current.TokenRenewable && !current.SelfRefreshing && !current.ProviderRejected()
				signals := health.CredentialSignals(current, health.DefaultHealthConfig())
				if current.ProviderRejected() {
					profile.credentialStatus = PoolStatusError
					profile.ErrorMessage = "provider rejected these credentials; log in again"
				} else if (signals.LoginRequired != nil && *signals.LoginRequired) || (profile.refreshable && profile.IsExpired()) {
					profile.credentialStatus = PoolStatusExpired
				}
			}
			profile.Status = profile.credentialStatus
			if !changed && profile.ErrorCount >= p.maxRetries {
				profile.Status = PoolStatusError
			}
			if profile.IsInCooldown() {
				profile.Status = PoolStatusCooldown
			} else {
				profile.CooldownUntil = time.Time{}
			}
			if p.onStateChange != nil && oldStatus != profile.Status {
				go p.onStateChange(profile.Clone(), oldStatus, profile.Status)
			}
		}
	}
	for key := range p.profiles {
		if !present[key] {
			delete(p.profiles, key)
		}
	}

	return nil
}

// vaultCredentialGeneration verifies actual access material as well as file-set
// completeness. Expiry parsers intentionally accept absent timing information;
// that alone must not turn a malformed or policy-only snapshot into ready auth.
func (p *AuthPool) vaultCredentialGeneration(fileSet authfile.AuthFileSet, name string) (string, error) {
	dir := filepath.Join(p.vault.BasePath(), fileSet.Tool, name)
	files := fileSet.Files
	if fileSet.Tool == "cursor" {
		path := filepath.Join(dir, "auth.json")
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			files = []authfile.AuthFileSpec{{Path: path}}
		}
	}
	if fileSet.Tool == "gemini" {
		for _, candidate := range []string{"oauth_creds.json", "oauth_credentials.json"} {
			path := filepath.Join(dir, candidate)
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				files = []authfile.AuthFileSpec{{Path: path}}
				break
			}
		}
	}
	hash := sha256.New()
	found := false
	for _, spec := range files {
		filename := filepath.Base(spec.Path)
		path := filepath.Join(dir, filename)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Size() > authfile.MaxDiscoveryFileBytes {
			return "", fmt.Errorf("invalid saved auth source")
		}
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("unreadable saved auth source")
		}
		data, readErr := io.ReadAll(io.LimitReader(f, authfile.MaxDiscoveryFileBytes+1))
		closeErr := f.Close()
		if readErr != nil || closeErr != nil || int64(len(data)) > authfile.MaxDiscoveryFileBytes {
			return "", fmt.Errorf("unreadable saved auth source")
		}
		if filename == "oauth_credentials.json" {
			filename = "oauth_creds.json"
		}
		if fileSet.Tool == "cursor" && filename == "cli-config.json" {
			hasAuth, err := authfile.CursorConfigAuthInfo(data)
			if err != nil {
				return "", fmt.Errorf("invalid saved Cursor credentials")
			}
			if hasAuth {
				found = true
				_, _ = hash.Write(data)
			}
			continue
		}
		if err := authfile.ValidateCredentialData(fileSet.Tool, filename, data); err != nil && !(fileSet.Tool == "gemini" && refreshOnlyGeminiADC(data)) {
			if errors.Is(err, authfile.ErrNoCredentials) && filename != "auth.json" && filename != ".credentials.json" && filename != "oauth_creds.json" {
				continue
			}
			return "", fmt.Errorf("invalid saved auth material")
		}
		found = true
		_, _ = hash.Write(data)
		// Canonical Cursor auth owns the login; unrelated cli-config churn is
		// not another grant and cannot override its validity or generation.
		if fileSet.Tool == "cursor" && filename == "auth.json" {
			break
		}
	}
	if !found {
		return "", fmt.Errorf("saved access credential missing")
	}
	if err := p.vault.ValidateProfileCredentials(fileSet, name); err != nil {
		return "", fmt.Errorf("incomplete saved auth source")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// A complete Google ADC grant authenticates by exchanging its refresh token;
// it need not contain an access token yet. Other incomplete OAuth snapshots
// still require the normal credential validator.
func refreshOnlyGeminiADC(data []byte) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return false
	}
	for _, key := range []string{"access_token", "accessToken"} {
		if _, present := fields[key]; present {
			return false
		}
	}
	var adc refresh.ADC
	return json.Unmarshal(data, &adc) == nil && strings.TrimSpace(adc.ClientID) != "" &&
		strings.TrimSpace(adc.ClientSecret) != "" && strings.TrimSpace(adc.RefreshToken) != ""
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
