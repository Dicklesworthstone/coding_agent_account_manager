// Package tui provides the terminal user interface for caam.
package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/watcher"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// viewState represents the current view/mode of the TUI.
type viewState int

const (
	stateList viewState = iota
	stateDetail
	stateConfirm
	stateSearch
	stateHelp
)

// confirmAction represents the action being confirmed.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmDelete
)

// Profile represents a saved auth profile for display.
type Profile struct {
	Name     string
	Provider string
	IsActive bool
}

// Model is the main Bubble Tea model for the caam TUI.
type Model struct {
	// Provider state
	providers      []string // codex, claude, gemini
	activeProvider int      // Currently selected provider index

	// Profile state
	profiles map[string][]Profile // Profiles by provider
	selected int                  // Currently selected profile index

	// View state
	width  int
	height int
	state  viewState
	err    error

	// UI components
	keys          keyMap
	styles        Styles
	providerPanel *ProviderPanel
	profilesPanel *ProfilesPanel
	detailPanel   *DetailPanel
	usagePanel    *UsagePanel

	// Status message
	statusMsg string

	// Hot reload watcher
	vaultPath string
	watcher   *watcher.Watcher
	badges    map[string]profileBadge

	// Signal handling
	signals *signals.Handler

	// Runtime configuration
	runtime config.RuntimeConfig

	// Project context
	cwd            string
	projectStore   *project.Store
	projectContext *project.Resolved

	// Confirmation state
	pendingAction confirmAction
	searchQuery   string
}

// DefaultProviders returns the default list of provider names.
func DefaultProviders() []string {
	return []string{"claude", "codex", "gemini"}
}

// New creates a new TUI model with default settings.
func New() Model {
	return NewWithProviders(DefaultProviders())
}

// NewWithProviders creates a new TUI model with the specified providers.
func NewWithProviders(providers []string) Model {
	cwd, _ := os.Getwd()
	profilesPanel := NewProfilesPanel()
	if len(providers) > 0 {
		profilesPanel.SetProvider(providers[0])
	}
	defaultRuntime := config.DefaultSPMConfig().Runtime
	return Model{
		providers:      providers,
		activeProvider: 0,
		profiles:       make(map[string][]Profile),
		selected:       0,
		state:          stateList,
		keys:           defaultKeyMap(),
		styles:         DefaultStyles(),
		providerPanel:  NewProviderPanel(providers),
		profilesPanel:  profilesPanel,
		detailPanel:    NewDetailPanel(),
		usagePanel:     NewUsagePanel(),
		vaultPath:      authfile.DefaultVaultPath(),
		badges:         make(map[string]profileBadge),
		runtime:        defaultRuntime,
		cwd:            cwd,
		projectStore:   project.NewStore(""),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tea.EnterAltScreen,
		m.loadProfiles,
		m.loadProjectContext(),
		m.initSignals(),
	}
	if m.runtime.FileWatching {
		cmds = append(cmds, m.initWatcher())
	}
	return tea.Batch(cmds...)
}

func (m Model) loadProjectContext() tea.Cmd {
	return func() tea.Msg {
		if m.projectStore == nil || m.cwd == "" {
			return projectContextLoadedMsg{}
		}
		resolved, err := m.projectStore.Resolve(m.cwd)
		return projectContextLoadedMsg{cwd: m.cwd, resolved: resolved, err: err}
	}
}

func (m Model) initWatcher() tea.Cmd {
	return func() tea.Msg {
		w, err := watcher.New(m.vaultPath)
		return watcherReadyMsg{watcher: w, err: err}
	}
}

func (m Model) initSignals() tea.Cmd {
	return func() tea.Msg {
		h, err := signals.New()
		return signalsReadyMsg{handler: h, err: err}
	}
}

func (m Model) watchProfiles() tea.Cmd {
	if m.watcher == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case evt, ok := <-m.watcher.Events():
			if !ok {
				return nil
			}
			return profilesChangedMsg{event: evt}
		case err, ok := <-m.watcher.Errors():
			if !ok {
				return nil
			}
			return errMsg{err: err}
		}
	}
}

func (m Model) watchSignals() tea.Cmd {
	if m.signals == nil {
		return nil
	}
	return func() tea.Msg {
		select {
		case <-m.signals.Reload():
			return reloadRequestedMsg{}
		case <-m.signals.DumpStats():
			return dumpStatsMsg{}
		case sig := <-m.signals.Shutdown():
			return shutdownRequestedMsg{sig: sig}
		}
	}
}

func (m Model) loadUsageStats() tea.Cmd {
	if m.usagePanel == nil {
		return nil
	}

	days := m.usagePanel.TimeRange()
	since := time.Time{}
	if days > 0 {
		since = time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	}

	return func() tea.Msg {
		db, err := caamdb.Open()
		if err != nil {
			return usageStatsLoadedMsg{err: err}
		}
		defer db.Close()

		stats, err := queryUsageStats(db, since)
		if err != nil {
			return usageStatsLoadedMsg{err: err}
		}
		return usageStatsLoadedMsg{stats: stats}
	}
}

func queryUsageStats(db *caamdb.DB, since time.Time) ([]ProfileUsage, error) {
	if db == nil || db.Conn() == nil {
		return nil, fmt.Errorf("db not available")
	}

	rows, err := db.Conn().Query(
		`SELECT provider,
		        profile_name,
		        SUM(CASE WHEN event_type = ? THEN 1 ELSE 0 END) AS sessions,
		        SUM(CASE WHEN event_type = ? THEN COALESCE(duration_seconds, 0) ELSE 0 END) AS active_seconds
		   FROM activity_log
		  WHERE datetime(timestamp) >= datetime(?)
		  GROUP BY provider, profile_name
		  ORDER BY active_seconds DESC, sessions DESC, provider ASC, profile_name ASC`,
		caamdb.EventActivate,
		caamdb.EventDeactivate,
		formatSQLiteSince(since),
	)
	if err != nil {
		return nil, fmt.Errorf("query usage stats: %w", err)
	}
	defer rows.Close()

	var out []ProfileUsage
	for rows.Next() {
		var provider, profile string
		var sessions int
		var seconds int64
		if err := rows.Scan(&provider, &profile, &sessions, &seconds); err != nil {
			return nil, fmt.Errorf("scan usage stats: %w", err)
		}
		out = append(out, ProfileUsage{
			Provider:     provider,
			ProfileName:  profile,
			SessionCount: sessions,
			TotalHours:   float64(seconds) / 3600,
		})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate usage stats: %w", err)
	}
	return out, nil
}

func formatSQLiteSince(t time.Time) string {
	if t.IsZero() {
		return "1970-01-01 00:00:00"
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// loadProfiles loads profiles for all providers.
func (m Model) loadProfiles() tea.Msg {
	vault := authfile.NewVault(m.vaultPath)
	profiles := make(map[string][]Profile)

	for _, name := range m.providers {
		names, err := vault.List(name)
		if err != nil {
			return errMsg{err: fmt.Errorf("list vault profiles for %s: %w", name, err)}
		}

		active := ""
		if len(names) > 0 {
			if fileSet, ok := authFileSetForProvider(name); ok {
				if ap, err := vault.ActiveProfile(fileSet); err == nil {
					active = ap
				}
			}
		}

		sort.Strings(names)
		ps := make([]Profile, 0, len(names))
		for _, prof := range names {
			ps = append(ps, Profile{
				Name:     prof,
				Provider: name,
				IsActive: prof == active,
			})
		}
		profiles[name] = ps
	}

	return profilesLoadedMsg{profiles: profiles}
}

func authFileSetForProvider(provider string) (authfile.AuthFileSet, bool) {
	switch provider {
	case "codex":
		return authfile.CodexAuthFiles(), true
	case "claude":
		return authfile.ClaudeAuthFiles(), true
	case "gemini":
		return authfile.GeminiAuthFiles(), true
	default:
		return authfile.AuthFileSet{}, false
	}
}

// profilesLoadedMsg is sent when profiles are loaded.
type profilesLoadedMsg struct {
	profiles map[string][]Profile
}

// errMsg is sent when an error occurs.
type errMsg struct {
	err error
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case signalsReadyMsg:
		if msg.err != nil {
			// Not fatal: leave the TUI usable even if signals are unavailable.
			m.statusMsg = "Signal handling unavailable"
			return m, nil
		}
		m.signals = msg.handler
		return m, m.watchSignals()

	case reloadRequestedMsg:
		if !m.runtime.ReloadOnSIGHUP {
			m.statusMsg = "Reload requested (ignored; runtime.reload_on_sighup=false)"
			return m, m.watchSignals()
		}

		m.statusMsg = "Reload requested"
		cmds := []tea.Cmd{m.loadProfiles, m.loadProjectContext(), m.watchSignals()}
		if m.usagePanel != nil && m.usagePanel.Visible() {
			m.usagePanel.SetLoading(true)
			cmds = append(cmds, m.loadUsageStats())
		}
		return m, tea.Batch(cmds...)

	case dumpStatsMsg:
		if err := signals.AppendLogLine("", m.dumpStatsLine()); err != nil {
			m.statusMsg = fmt.Sprintf("Failed to write stats: %v", err)
		} else {
			m.statusMsg = "Stats written to log"
		}
		return m, m.watchSignals()

	case shutdownRequestedMsg:
		m.statusMsg = fmt.Sprintf("Shutdown requested (%v)", msg.sig)
		return m, tea.Quit

	case projectContextLoadedMsg:
		if msg.err != nil {
			m.statusMsg = msg.err.Error()
			return m, nil
		}
		if msg.cwd != "" {
			m.cwd = msg.cwd
		}
		m.projectContext = msg.resolved
		m.syncProfilesPanel()
		return m, nil

	case watcherReadyMsg:
		if msg.err != nil {
			// Graceful degradation: keep the TUI usable without hot reload.
			m.statusMsg = "Hot reload unavailable (file watching disabled)"
			return m, nil
		}
		m.watcher = msg.watcher
		return m, m.watchProfiles()

	case profilesChangedMsg:
		if msg.event.Type == watcher.EventProfileDeleted {
			delete(m.badges, badgeKey(msg.event.Provider, msg.event.Profile))
		}

		var badgeCmd tea.Cmd
		if msg.event.Type == watcher.EventProfileAdded {
			if m.badges == nil {
				m.badges = make(map[string]profileBadge)
			}
			key := badgeKey(msg.event.Provider, msg.event.Profile)
			m.badges[key] = profileBadge{
				badge:  "NEW",
				expiry: time.Now().Add(5 * time.Second),
			}
			badgeCmd = tea.Tick(5*time.Second, func(time.Time) tea.Msg {
				return badgeExpiredMsg{key: key}
			})
		}

		m.statusMsg = fmt.Sprintf("Profile %s/%s %s", msg.event.Provider, msg.event.Profile, eventTypeVerb(msg.event.Type))
		cmds := []tea.Cmd{m.loadProfiles, m.watchProfiles()}
		if badgeCmd != nil {
			cmds = append(cmds, badgeCmd)
		}
		return m, tea.Batch(cmds...)

	case badgeExpiredMsg:
		delete(m.badges, msg.key)
		m.syncProfilesPanel()
		return m, nil

	case usageStatsLoadedMsg:
		if msg.err != nil {
			m.statusMsg = msg.err.Error()
			if m.usagePanel != nil {
				m.usagePanel.SetLoading(false)
			}
			return m, nil
		}
		if m.usagePanel != nil {
			m.usagePanel.SetStats(msg.stats)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case profilesLoadedMsg:
		m.profiles = msg.profiles
		// Update provider panel counts
		if m.providerPanel != nil {
			counts := make(map[string]int)
			for provider, profiles := range m.profiles {
				counts[provider] = len(profiles)
			}
			m.providerPanel.SetProfileCounts(counts)
		}
		// Update profiles panel with current provider's profiles
		m.syncProfilesPanel()
		return m, nil

	case errMsg:
		m.err = msg.err
		m.statusMsg = msg.err.Error()
		if m.watcher != nil {
			return m, m.watchProfiles()
		}
		return m, nil
	}

	return m, nil
}

// handleKeyPress processes keyboard input.
func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Usage panel overlay gets first crack at keys.
	if m.usagePanel != nil && m.usagePanel.Visible() {
		if msg.Type == tea.KeyEscape {
			m.usagePanel.Toggle()
			return m, nil
		}
		switch msg.String() {
		case "u":
			m.usagePanel.Toggle()
			return m, nil
		case "1":
			m.usagePanel.SetTimeRange(1)
			m.usagePanel.SetLoading(true)
			return m, m.loadUsageStats()
		case "2":
			m.usagePanel.SetTimeRange(7)
			m.usagePanel.SetLoading(true)
			return m, m.loadUsageStats()
		case "3":
			m.usagePanel.SetTimeRange(30)
			m.usagePanel.SetLoading(true)
			return m, m.loadUsageStats()
		case "4":
			m.usagePanel.SetTimeRange(0)
			m.usagePanel.SetLoading(true)
			return m, m.loadUsageStats()
		}
	}

	// Handle state-specific key handling
	switch m.state {
	case stateConfirm:
		return m.handleConfirmKeys(msg)
	case stateSearch:
		return m.handleSearchKeys(msg)
	case stateHelp:
		// Any key returns to list
		m.state = stateList
		return m, nil
	}

	// Normal list view key handling
	switch {
	case key.Matches(msg, m.keys.Quit):
		if m.watcher != nil {
			_ = m.watcher.Close()
			m.watcher = nil
		}
		return m, tea.Quit

	case key.Matches(msg, m.keys.Help):
		m.state = stateHelp
		return m, nil

	case key.Matches(msg, m.keys.Up):
		if m.selected > 0 {
			m.selected--
			if m.profilesPanel != nil {
				m.profilesPanel.SetSelected(m.selected)
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		profiles := m.currentProfiles()
		if m.selected < len(profiles)-1 {
			m.selected++
			if m.profilesPanel != nil {
				m.profilesPanel.SetSelected(m.selected)
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Left):
		if m.activeProvider > 0 {
			m.activeProvider--
			m.selected = 0
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Right):
		if m.activeProvider < len(m.providers)-1 {
			m.activeProvider++
			m.selected = 0
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		return m.handleActivateProfile()

	case key.Matches(msg, m.keys.Tab):
		// Cycle through providers
		m.activeProvider = (m.activeProvider + 1) % len(m.providers)
		m.selected = 0
		m.syncProfilesPanel()
		return m, nil

	case key.Matches(msg, m.keys.Delete):
		return m.handleDeleteProfile()

	case key.Matches(msg, m.keys.Backup):
		return m.handleBackupProfile()

	case key.Matches(msg, m.keys.Login):
		return m.handleLoginProfile()

	case key.Matches(msg, m.keys.Open):
		return m.handleOpenInBrowser()

	case key.Matches(msg, m.keys.Edit):
		return m.handleEditProfile()

	case key.Matches(msg, m.keys.Search):
		return m.handleEnterSearchMode()

	case key.Matches(msg, m.keys.Project):
		return m.handleSetProjectAssociation()

	case key.Matches(msg, m.keys.Usage):
		if m.usagePanel == nil {
			return m, nil
		}
		m.usagePanel.Toggle()
		if m.usagePanel.Visible() {
			m.usagePanel.SetLoading(true)
			return m, m.loadUsageStats()
		}
		return m, nil
	}

	return m, nil
}

// handleConfirmKeys handles keys in confirmation state.
func (m Model) handleConfirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Confirm):
		return m.executeConfirmedAction()
	case key.Matches(msg, m.keys.Cancel):
		m.state = stateList
		m.pendingAction = confirmNone
		m.statusMsg = "Cancelled"
		return m, nil
	}
	return m, nil
}

// handleSearchKeys handles keys in search/filter mode.
func (m Model) handleSearchKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEscape:
		// Cancel search and restore view
		m.state = stateList
		m.searchQuery = ""
		m.statusMsg = ""
		m.syncProfilesPanel() // Restore full list
		return m, nil

	case tea.KeyEnter:
		// Accept current filter and return to list
		m.state = stateList
		if m.searchQuery != "" {
			m.statusMsg = fmt.Sprintf("Filtered by: %s", m.searchQuery)
		} else {
			m.statusMsg = ""
		}
		return m, nil

	case tea.KeyBackspace:
		// Remove last character from search query
		if len(m.searchQuery) > 0 {
			m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
			m.applySearchFilter()
		}
		return m, nil

	case tea.KeyRunes:
		// Add typed characters to search query
		m.searchQuery += string(msg.Runes)
		m.applySearchFilter()
		return m, nil
	}
	return m, nil
}

// applySearchFilter filters the profiles panel based on the search query.
func (m *Model) applySearchFilter() {
	if m.profilesPanel == nil {
		return
	}

	provider := m.currentProvider()
	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)

	// Filter profiles by name (case-insensitive)
	var filtered []ProfileInfo
	query := strings.ToLower(m.searchQuery)

	for _, p := range profiles {
		if query == "" || strings.Contains(strings.ToLower(p.Name), query) {
			filtered = append(filtered, ProfileInfo{
				Name:           p.Name,
				Badge:          m.badgeFor(provider, p.Name),
				ProjectDefault: projectDefault != "" && p.Name == projectDefault,
				AuthMode:       "oauth",
				LoggedIn:       true,
				Locked:         false,
				IsActive:       p.IsActive,
			})
		}
	}

	m.profilesPanel.SetProfiles(filtered)
	m.selected = 0
	m.profilesPanel.SetSelected(0)
	m.statusMsg = fmt.Sprintf("/%s (%d matches)", m.searchQuery, len(filtered))
}

// handleActivateProfile activates the selected profile.
func (m Model) handleActivateProfile() (tea.Model, tea.Cmd) {
	profiles := m.currentProfiles()
	if m.selected < 0 || m.selected >= len(profiles) {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	profile := profiles[m.selected]
	m.statusMsg = fmt.Sprintf("Activating %s... (not yet implemented)", profile.Name)
	return m, nil
}

// handleDeleteProfile initiates profile deletion with confirmation.
func (m Model) handleDeleteProfile() (tea.Model, tea.Cmd) {
	profiles := m.currentProfiles()
	if m.selected < 0 || m.selected >= len(profiles) {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	profile := profiles[m.selected]
	m.state = stateConfirm
	m.pendingAction = confirmDelete
	m.statusMsg = fmt.Sprintf("Delete '%s'? (y/n)", profile.Name)
	return m, nil
}

// handleBackupProfile backs up the current auth state.
func (m Model) handleBackupProfile() (tea.Model, tea.Cmd) {
	m.statusMsg = fmt.Sprintf("Backup %s auth... (not yet implemented)", m.currentProvider())
	return m, nil
}

// handleLoginProfile initiates login/refresh for the selected profile.
func (m Model) handleLoginProfile() (tea.Model, tea.Cmd) {
	profiles := m.currentProfiles()
	if m.selected < 0 || m.selected >= len(profiles) {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	profile := profiles[m.selected]
	m.statusMsg = fmt.Sprintf("Login/refresh %s... (not yet implemented)", profile.Name)
	return m, nil
}

// handleOpenInBrowser opens the account page in browser.
func (m Model) handleOpenInBrowser() (tea.Model, tea.Cmd) {
	m.statusMsg = fmt.Sprintf("Open %s account page... (not yet implemented)", m.currentProvider())
	return m, nil
}

// handleEditProfile opens the edit view for the selected profile.
func (m Model) handleEditProfile() (tea.Model, tea.Cmd) {
	profiles := m.currentProfiles()
	if m.selected < 0 || m.selected >= len(profiles) {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	profile := profiles[m.selected]
	m.statusMsg = fmt.Sprintf("Edit '%s'... (not yet implemented)", profile.Name)
	return m, nil
}

// handleEnterSearchMode enters search/filter mode.
func (m Model) handleEnterSearchMode() (tea.Model, tea.Cmd) {
	m.state = stateSearch
	m.searchQuery = ""
	m.statusMsg = "Type to filter profiles (Esc to cancel)"
	return m, nil
}

func (m Model) handleSetProjectAssociation() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	profiles := m.currentProfiles()
	if provider == "" || m.selected < 0 || m.selected >= len(profiles) {
		m.statusMsg = "No profile selected"
		return m, nil
	}

	if m.cwd == "" {
		if cwd, err := os.Getwd(); err == nil {
			m.cwd = cwd
		}
	}
	if m.cwd == "" {
		m.statusMsg = "Unable to determine current directory"
		return m, nil
	}

	if m.projectStore == nil {
		m.projectStore = project.NewStore("")
	}

	profileName := profiles[m.selected].Name
	if err := m.projectStore.SetAssociation(m.cwd, provider, profileName); err != nil {
		m.statusMsg = err.Error()
		return m, nil
	}

	resolved, err := m.projectStore.Resolve(m.cwd)
	if err != nil {
		m.statusMsg = err.Error()
		return m, nil
	}

	m.projectContext = resolved
	m.syncProfilesPanel()
	m.statusMsg = fmt.Sprintf("Associated %s → %s", provider, profileName)
	return m, nil
}

// executeConfirmedAction executes the pending confirmed action.
func (m Model) executeConfirmedAction() (tea.Model, tea.Cmd) {
	switch m.pendingAction {
	case confirmDelete:
		profiles := m.currentProfiles()
		if m.selected >= 0 && m.selected < len(profiles) {
			profile := profiles[m.selected]
			m.statusMsg = fmt.Sprintf("Deleted '%s' (not yet implemented)", profile.Name)
		}
	}
	m.state = stateList
	m.pendingAction = confirmNone
	return m, nil
}

// currentProfiles returns the profiles for the currently selected provider.
func (m Model) currentProfiles() []Profile {
	if m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
		return m.profiles[m.providers[m.activeProvider]]
	}
	return nil
}

// currentProvider returns the name of the currently selected provider.
func (m Model) currentProvider() string {
	if m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
		return m.providers[m.activeProvider]
	}
	return ""
}

// updateProviderCounts updates the provider panel with current profile counts.
func (m *Model) updateProviderCounts() {
	counts := make(map[string]int)
	for provider, profiles := range m.profiles {
		counts[provider] = len(profiles)
	}
	m.providerPanel.SetProfileCounts(counts)
}

// syncProviderPanel syncs the provider panel state with the model.
func (m *Model) syncProviderPanel() {
	m.providerPanel.SetActiveProvider(m.activeProvider)
}

// syncProfilesPanel syncs the profiles panel with the current provider's profiles.
func (m Model) syncProfilesPanel() {
	if m.profilesPanel == nil {
		return
	}
	provider := m.currentProvider()
	m.profilesPanel.SetProvider(provider)

	// Convert Profile to ProfileInfo
	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)
	infos := make([]ProfileInfo, len(profiles))
	for i, p := range profiles {
		infos[i] = ProfileInfo{
			Name:           p.Name,
			Badge:          m.badgeFor(provider, p.Name),
			ProjectDefault: projectDefault != "" && p.Name == projectDefault,
			AuthMode:       "oauth", // Default, TODO: get from actual profile
			LoggedIn:       true,    // TODO: get actual status
			Locked:         false,   // TODO: get actual lock status
			IsActive:       p.IsActive,
			HealthStatus:   health.StatusUnknown, // TODO: get from health store
			ErrorCount:     0,                    // TODO: get from health store
			Penalty:        0,                    // TODO: get from health store
		}
	}
	m.profilesPanel.SetProfiles(infos)
	m.profilesPanel.SetSelected(m.selected)
}

// syncDetailPanel syncs the detail panel with the currently selected profile.
func (m Model) syncDetailPanel() {
	if m.detailPanel == nil {
		return
	}

	// Get the selected profile
	profiles := m.currentProfiles()
	if m.selected < 0 || m.selected >= len(profiles) {
		m.detailPanel.SetProfile(nil)
		return
	}

	prof := profiles[m.selected]
	detail := &DetailInfo{
		Name:         prof.Name,
		Provider:     m.currentProvider(),
		AuthMode:     "oauth",              // TODO: get from actual profile
		LoggedIn:     true,                 // TODO: get actual status
		Locked:       false,                // TODO: get actual lock status
		Path:         "",                   // TODO: get from actual profile
		Account:      "",                   // TODO: get from actual profile
		HealthStatus: health.StatusUnknown, // TODO: get from health store
		ErrorCount:   0,                    // TODO: get from health store
		Penalty:      0,                    // TODO: get from health store
	}
	m.detailPanel.SetProfile(detail)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	if m.usagePanel != nil && m.usagePanel.Visible() {
		m.usagePanel.SetSize(m.width, m.height)
		return m.usagePanel.View()
	}

	switch m.state {
	case stateHelp:
		return m.helpView()
	default:
		return m.mainView()
	}
}

// mainView renders the main list view.
func (m Model) mainView() string {
	// Header
	headerLines := []string{m.styles.Header.Render("caam - Coding Agent Account Manager")}
	if projectLine := m.projectContextLine(); projectLine != "" {
		headerLines = append(headerLines, m.styles.StatusText.Render(projectLine))
	}
	header := lipgloss.JoinVertical(lipgloss.Left, headerLines...)

	// Calculate panel dimensions
	providerPanelWidth := 18
	detailPanelWidth := 35
	profilesPanelWidth := m.width - providerPanelWidth - detailPanelWidth - 10 // Account for borders and spacing
	if profilesPanelWidth < 40 {
		profilesPanelWidth = 40
	}
	contentHeight := m.height - 5 // Header + status bar

	// Sync and render provider panel
	m.providerPanel.SetActiveProvider(m.activeProvider)
	m.providerPanel.SetSize(providerPanelWidth, contentHeight)
	providerPanelView := m.providerPanel.View()

	// Sync and render profiles panel (center panel)
	var profilesPanelView string
	if m.profilesPanel != nil {
		m.profilesPanel.SetSize(profilesPanelWidth, contentHeight)
		profilesPanelView = m.profilesPanel.View()
	} else {
		profilesPanelView = m.renderProfileList()
	}

	// Sync and render detail panel (right panel)
	var detailPanelView string
	if m.detailPanel != nil {
		m.syncDetailPanel()
		m.detailPanel.SetSize(detailPanelWidth, contentHeight)
		detailPanelView = m.detailPanel.View()
	}

	// Create panels side by side
	panels := lipgloss.JoinHorizontal(
		lipgloss.Top,
		providerPanelView,
		"  ", // Spacing
		profilesPanelView,
		"  ", // Spacing
		detailPanelView,
	)

	// Status bar
	status := m.renderStatusBar()

	// Combine header, panels, and status
	content := lipgloss.JoinVertical(
		lipgloss.Left,
		header,
		"",
		panels,
	)

	// Add status bar at bottom
	availableHeight := m.height - lipgloss.Height(content) - 2
	if availableHeight > 0 {
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			content,
			lipgloss.NewStyle().Height(availableHeight).Render(""),
			status,
		)
	}

	return content
}

func (m Model) projectContextLine() string {
	if m.cwd == "" {
		return ""
	}

	provider := m.currentProvider()
	if provider == "" {
		return ""
	}

	if m.projectContext == nil {
		return fmt.Sprintf("Project: %s (no association)", m.cwd)
	}

	profile := m.projectContext.Profiles[provider]
	source := m.projectContext.Sources[provider]
	if profile == "" || source == "" || source == "<default>" {
		return fmt.Sprintf("Project: %s (no association)", m.cwd)
	}

	return fmt.Sprintf("Project: %s → %s", source, profile)
}

func (m Model) projectDefaultForProvider(provider string) string {
	if provider == "" || m.projectContext == nil {
		return ""
	}

	profile := m.projectContext.Profiles[provider]
	source := m.projectContext.Sources[provider]
	if profile == "" || source == "" || source == "<default>" {
		return ""
	}

	return profile
}

// renderProviderTabs renders the provider selection tabs.
func (m Model) renderProviderTabs() string {
	var tabs []string
	for i, p := range m.providers {
		style := m.styles.Tab
		if i == m.activeProvider {
			style = m.styles.ActiveTab
		}
		tabs = append(tabs, style.Render(p))
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// renderProfileList renders the list of profiles for the current provider.
func (m Model) renderProfileList() string {
	profiles := m.currentProfiles()
	if len(profiles) == 0 {
		return m.styles.Empty.Render(fmt.Sprintf("No profiles saved for %s\n\nUse 'caam backup %s <email>' to save a profile",
			m.currentProvider(), m.currentProvider()))
	}

	var items []string
	for i, p := range profiles {
		style := m.styles.Item
		if i == m.selected {
			style = m.styles.SelectedItem
		}

		indicator := "  "
		if p.IsActive {
			indicator = m.styles.Active.Render("● ")
		}

		items = append(items, style.Render(indicator+p.Name))
	}

	return lipgloss.JoinVertical(lipgloss.Left, items...)
}

// renderStatusBar renders the bottom status bar.
func (m Model) renderStatusBar() string {
	left := m.styles.StatusKey.Render("q") + m.styles.StatusText.Render(" quit  ")
	left += m.styles.StatusKey.Render("?") + m.styles.StatusText.Render(" help  ")
	left += m.styles.StatusKey.Render("tab") + m.styles.StatusText.Render(" switch provider  ")
	left += m.styles.StatusKey.Render("enter") + m.styles.StatusText.Render(" activate")

	if m.statusMsg != "" {
		left = m.styles.StatusText.Render(m.statusMsg)
	}

	return m.styles.StatusBar.Width(m.width).Render(left)
}

// helpView renders the help screen.
func (m Model) helpView() string {
	help := `
Keyboard Shortcuts
==================

Navigation
  ↑/k     Move up
  ↓/j     Move down
  ←/h     Previous provider
  →       Next provider
  tab     Cycle providers
  /       Search/filter profiles

Profile Actions
  enter   Activate selected profile
  l       Login/refresh auth
  e       Edit profile
  o       Open account page in browser
  d       Delete profile (with confirmation)
  p       Set project association (CWD)

Other Actions
  b       Backup current auth state
  u       Toggle usage stats panel (1/2/3/4 ranges)

General
  ?       Toggle help
  q/esc   Quit

Press any key to return...
`
	return m.styles.Help.Render(help)
}

func (m Model) dumpStatsLine() string {
	totalProfiles := 0
	for _, ps := range m.profiles {
		totalProfiles += len(ps)
	}

	activeProvider := ""
	if m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
		activeProvider = m.providers[m.activeProvider]
	}

	usageVisible := false
	if m.usagePanel != nil {
		usageVisible = m.usagePanel.Visible()
	}

	return fmt.Sprintf(
		"tui_stats provider=%s selected=%d total_profiles=%d view_state=%d width=%d height=%d cwd=%q usage_visible=%t",
		activeProvider,
		m.selected,
		totalProfiles,
		m.state,
		m.width,
		m.height,
		m.cwd,
		usageVisible,
	)
}

// Run starts the TUI application.
func Run() error {
	spmCfg, err := config.LoadSPMConfig()
	if err != nil {
		// Keep the TUI usable even with a broken config file.
		spmCfg = config.DefaultSPMConfig()
	}

	// Run cleanup on startup if configured
	if spmCfg.Analytics.CleanupOnStartup {
		runStartupCleanup(spmCfg)
	}

	m := New()
	m.runtime = spmCfg.Runtime

	pidPath := signals.DefaultPIDFilePath()
	pidWritten := false
	if spmCfg.Runtime.PIDFile {
		if err := signals.WritePIDFile(pidPath, os.Getpid()); err != nil {
			return fmt.Errorf("write pid file: %w", err)
		}
		pidWritten = true
	}

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()

	if fm, ok := finalModel.(Model); ok {
		if fm.watcher != nil {
			_ = fm.watcher.Close()
		}
		if fm.signals != nil {
			_ = fm.signals.Close()
		}
	}
	if pidWritten {
		_ = signals.RemovePIDFile(pidPath)
	}
	return err
}

// runStartupCleanup runs database cleanup using the configured retention settings.
// Errors are silently ignored to avoid blocking TUI startup.
func runStartupCleanup(spmCfg *config.SPMConfig) {
	db, err := caamdb.Open()
	if err != nil {
		return
	}
	defer db.Close()

	cfg := caamdb.CleanupConfig{
		RetentionDays:          spmCfg.Analytics.RetentionDays,
		AggregateRetentionDays: spmCfg.Analytics.AggregateRetentionDays,
	}
	_, _ = db.Cleanup(cfg)
}

type profileBadge struct {
	badge  string
	expiry time.Time
}

func badgeKey(provider, profile string) string {
	return provider + "/" + profile
}

func (m Model) badgeFor(provider, profile string) string {
	if m.badges == nil {
		return ""
	}
	key := badgeKey(provider, profile)
	b, ok := m.badges[key]
	if !ok {
		return ""
	}
	if !b.expiry.IsZero() && time.Now().After(b.expiry) {
		return ""
	}
	return b.badge
}
