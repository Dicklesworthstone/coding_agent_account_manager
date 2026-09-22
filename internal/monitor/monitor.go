package monitor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// ProfileFetcher fetches usage data for multiple profiles.
type ProfileFetcher interface {
	FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []usage.ProfileUsage
}

// Monitor manages real-time usage monitoring across profiles.
type Monitor struct {
	interval  time.Duration
	providers []string
	fetcher   ProfileFetcher
	vault     *authfile.Vault
	health    *health.Storage
	db        *caamdb.DB
	pool      *authpool.AuthPool

	mu    sync.RWMutex
	state *MonitorState
}

// MonitorOption configures a Monitor.
type MonitorOption func(*Monitor)

func WithInterval(d time.Duration) MonitorOption {
	return func(m *Monitor) {
		if d > 0 {
			m.interval = d
		}
	}
}

func WithProviders(providers []string) MonitorOption {
	return func(m *Monitor) {
		if len(providers) > 0 {
			m.providers = append([]string(nil), providers...)
		}
	}
}

func WithFetcher(fetcher ProfileFetcher) MonitorOption {
	return func(m *Monitor) {
		m.fetcher = fetcher
	}
}

func WithVault(v *authfile.Vault) MonitorOption {
	return func(m *Monitor) {
		m.vault = v
	}
}

func WithHealthStore(store *health.Storage) MonitorOption {
	return func(m *Monitor) {
		m.health = store
	}
}

func WithDB(db *caamdb.DB) MonitorOption {
	return func(m *Monitor) {
		m.db = db
	}
}

func WithAuthPool(pool *authpool.AuthPool) MonitorOption {
	return func(m *Monitor) {
		m.pool = pool
	}
}

// NewMonitor creates a new monitor with default settings.
func NewMonitor(opts ...MonitorOption) *Monitor {
	m := &Monitor{
		interval:  30 * time.Second,
		providers: []string{"claude", "codex", "gemini", "grok", "opencode", "cursor"},
		fetcher:   usage.NewMultiProfileFetcher(),
		vault:     authfile.NewVault(authfile.DefaultVaultPath()),
		health:    health.NewStorage(""),
		state: &MonitorState{
			Profiles: make(map[string]*ProfileState),
		},
	}

	for _, opt := range opts {
		opt(m)
	}

	if m.state == nil {
		m.state = &MonitorState{Profiles: make(map[string]*ProfileState)}
	}
	if m.interval <= 0 {
		m.interval = 30 * time.Second
	}

	return m
}

// Start runs the monitor refresh loop until ctx is cancelled.
func (m *Monitor) Start(ctx context.Context) error {
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()

	for {
		_ = m.Refresh(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Refresh performs a one-shot update of monitor state.
func (m *Monitor) Refresh(ctx context.Context) error {
	state := &MonitorState{
		Profiles:  make(map[string]*ProfileState),
		UpdatedAt: time.Now(),
	}

	var errs []error
	if m.vault == nil {
		errs = append(errs, fmt.Errorf("vault is nil"))
		m.setState(state, errs)
		return errors.Join(errs...)
	}

	profilesByProvider, err := m.vault.ListAll()
	if err != nil {
		errs = append(errs, err)
		m.setState(state, errs)
		return errors.Join(errs...)
	}

	cooldowns, cdErrs := m.loadCooldowns()
	if len(cdErrs) > 0 {
		errs = append(errs, cdErrs...)
	}

	type providerResult struct {
		provider string
		results  []usage.ProfileUsage
	}

	var wg sync.WaitGroup
	resultsCh := make(chan providerResult, len(m.providers))

	for _, provider := range m.providers {
		profiles := profilesByProvider[provider]
		if len(profiles) == 0 {
			continue
		}

		tokens := make(map[string]string)
		for _, name := range profiles {
			if authfile.IsSystemProfile(name) {
				continue
			}
			token, err := m.readAccessToken(provider, name)
			if err != nil {
				state.Profiles[profileKey(provider, name)] = m.buildProfileState(provider, name, &usage.UsageInfo{
					Provider:    provider,
					ProfileName: name,
					Error:       err.Error(),
					FetchedAt:   time.Now(),
				}, cooldowns)
				continue
			}
			if token == "" {
				state.Profiles[profileKey(provider, name)] = m.buildProfileState(provider, name, &usage.UsageInfo{
					Provider:    provider,
					ProfileName: name,
					Error:       "missing access token",
					FetchedAt:   time.Now(),
				}, cooldowns)
				continue
			}
			tokens[name] = token
		}

		if len(tokens) == 0 || m.fetcher == nil {
			continue
		}

		wg.Add(1)
		go func(provider string, tokens map[string]string) {
			defer wg.Done()
			results := m.fetcher.FetchAllProfiles(ctx, provider, tokens)
			resultsCh <- providerResult{provider: provider, results: results}
		}(provider, tokens)
	}

	wg.Wait()
	close(resultsCh)

	for res := range resultsCh {
		for _, item := range res.results {
			info := item.Usage
			if info == nil {
				info = &usage.UsageInfo{
					Provider:    res.provider,
					ProfileName: item.ProfileName,
					Error:       "usage fetch returned nil",
					FetchedAt:   time.Now(),
				}
			}
			if info.ProfileName == "" {
				info.ProfileName = item.ProfileName
			}
			state.Profiles[profileKey(res.provider, item.ProfileName)] = m.buildProfileState(res.provider, item.ProfileName, info, cooldowns)
		}
	}

	m.setState(state, errs)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// GetState returns a snapshot of the current monitor state.
func (m *Monitor) GetState() *MonitorState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state.Clone()
}

// GetProfile returns a snapshot of a specific profile state.
func (m *Monitor) GetProfile(provider, name string) *ProfileState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.state == nil || m.state.Profiles == nil {
		return nil
	}
	if profile, ok := m.state.Profiles[profileKey(provider, name)]; ok && profile != nil {
		cp := *profile
		return &cp
	}
	return nil
}

func (m *Monitor) setState(state *MonitorState, errs []error) {
	if state == nil {
		return
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		state.Errors = append(state.Errors, err.Error())
	}

	m.mu.Lock()
	m.state = state
	m.mu.Unlock()
}

func (m *Monitor) loadCooldowns() (map[string]time.Time, []error) {
	out := make(map[string]time.Time)
	if m.db == nil {
		return out, nil
	}

	events, err := m.db.ListActiveCooldowns(time.Now())
	if err != nil {
		return out, []error{err}
	}
	for _, ev := range events {
		out[profileKey(ev.Provider, ev.ProfileName)] = ev.CooldownUntil
	}
	return out, nil
}

func (m *Monitor) readAccessToken(provider, name string) (string, error) {
	if m.vault == nil {
		return "", fmt.Errorf("vault is nil")
	}

	switch provider {
	case "claude":
		profilePath := m.vault.ProfilePath(provider, name)
		creds := filepath.Join(profilePath, ".credentials.json")
		token, _, err := usage.ReadClaudeCredentials(creds)
		if err != nil {
			oldPath := filepath.Join(profilePath, ".claude.json")
			token, _, err = usage.ReadClaudeCredentials(oldPath)
			if err != nil {
				authPath := filepath.Join(profilePath, "auth.json")
				token, _, err = usage.ReadClaudeCredentials(authPath)
			}
		}
		return token, err
	case "codex":
		authPath := filepath.Join(m.vault.ProfilePath(provider, name), "auth.json")
		token, _, err := usage.ReadCodexCredentials(authPath)
		return token, err
	case "grok":
		authPath := filepath.Join(m.vault.ProfilePath(provider, name), "auth.json")
		if _, err := os.Stat(authPath); err != nil {
			return "", fmt.Errorf("grok auth.json not found")
		}
		return "grok-home:" + m.vault.ProfilePath(provider, name), nil
	case "opencode", "cursor":
		return "", fmt.Errorf("usage fetch not yet supported for provider %s", provider)
	default:
		return "", fmt.Errorf("usage fetch unsupported for provider %s", provider)
	}
}

func (m *Monitor) buildProfileState(provider, name string, info *usage.UsageInfo, cooldowns map[string]time.Time) *ProfileState {
	state := &ProfileState{
		Provider:    provider,
		ProfileName: name,
		Usage:       info,
		Health:      health.StatusUnknown,
		PoolStatus:  authpool.PoolStatusUnknown,
	}

	if m.health != nil {
		if h, err := m.health.GetProfile(provider, name); err == nil && h != nil {
			state.Health = health.CalculateStatus(h)
		}
	}

	if m.pool != nil {
		state.PoolStatus = m.pool.GetStatus(provider, name)
	}

	if until, ok := cooldowns[profileKey(provider, name)]; ok {
		state.InCooldown = true
		state.CooldownUntil = &until
	}

	state.Alert = evaluateAlert(info, time.Now())
	return state
}
