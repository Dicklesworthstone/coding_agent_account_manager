// Package tui provides the terminal user interface for caam.
package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/browser"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/project"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/signals"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/watcher"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// providerAccountURLs maps provider names to their account management URLs.
var providerAccountURLs = map[string]string{
	"claude": "https://console.anthropic.com/",
	"codex":  "https://platform.openai.com/",
	"gemini": "https://aistudio.google.com/",
}

// viewState represents the current view/mode of the TUI.
type viewState int

const (
	stateList viewState = iota
	stateDetail
	stateConfirm
	stateSearch
	stateHelp
	stateBackupDialog
	stateConfirmOverwrite
	stateExportConfirm
	stateImportPath
	stateImportConfirm
	stateEditProfile
	stateSyncAdd
	stateSyncEdit
	stateCommandPalette
)

type layoutMode int

const (
	layoutFull layoutMode = iota
	layoutCompact
	layoutTiny
)

type layoutSpec struct {
	Mode           layoutMode
	ProviderWidth  int
	ProfilesWidth  int
	DetailWidth    int
	Gap            int
	ContentHeight  int
	ProfilesHeight int
	DetailHeight   int
	ShowDetail     bool
}

const (
	layoutGap                 = 2
	minProviderWidth          = 18
	maxProviderWidth          = 26
	minProfilesWidth          = 40
	maxProfilesWidth          = 90
	minDetailWidth            = 32
	maxDetailWidth            = 48
	minFullHeight             = 24
	minTinyWidth              = 64
	minTinyHeight             = 16
	minCompactDetailHeight    = 14
	minCompactProfilesHeight  = 6
	minCompactDetailMinHeight = 7
	dialogMinWidth            = 24
	dialogMargin              = 4
)

// confirmAction represents the action being confirmed.
type confirmAction int

const (
	confirmNone confirmAction = iota
	confirmDelete
	confirmActivate
)

// Profile represents a saved auth profile for display.
type Profile struct {
	Name     string
	Provider string
	IsActive bool
}

type vaultProfileMeta struct {
	Description string
	Account     string
}

// Model is the main Bubble Tea model for the caam TUI.
type Model struct {
	// Provider state
	providers      []string // codex, claude, gemini
	activeProvider int      // Currently selected provider index

	// Profile state
	profiles            map[string][]Profile // Profiles by provider
	selected            int                  // Currently selected profile index
	selectedProfileName string               // Selected profile name (source of truth for actions)
	profileStore        *profile.Store
	profileMeta         map[string]map[string]*profile.Profile
	vaultMeta           map[string]map[string]vaultProfileMeta

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
	syncPanel     *SyncPanel

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

	// Health storage for profile health data
	healthStorage *health.Storage

	// Confirmation state
	pendingAction confirmAction
	searchQuery   string

	// Dialog state for backup flow
	backupDialog   *TextInputDialog
	confirmDialog  *ConfirmDialog
	pendingProfile string // Profile name pending overwrite confirmation
	editDialog     *MultiFieldDialog

	// Sync panel dialogs
	syncAddDialog       *MultiFieldDialog
	syncEditDialog      *MultiFieldDialog
	pendingSyncMachine  string
	pendingEditProvider string
	pendingEditProfile  string

	// Command palette dialog
	commandPalette *CommandPaletteDialog

	// Help renderer with Glamour markdown support and caching
	helpRenderer *HelpRenderer
	theme        Theme

	// Toast notifications
	toasts []Toast
}

// DefaultProviders returns the default list of provider names.
func DefaultProviders() []string {
	return []string{"claude", "codex", "gemini"}
}

// New creates a new TUI model with default settings.
func New() Model {
	return NewWithProviders(DefaultProviders())
}

// NewWithConfig creates a new TUI model using the provided SPM config.
// This applies all TUI preferences from the config file (theme, contrast, etc.)
// with environment variable overrides already applied.
func NewWithConfig(cfg *config.SPMConfig) Model {
	return NewWithProvidersAndConfig(DefaultProviders(), cfg)
}

// NewWithProviders creates a new TUI model with the specified providers.
func NewWithProviders(providers []string) Model {
	return NewWithProvidersAndConfig(providers, nil)
}

// NewWithProvidersAndConfig creates a new TUI model with specified providers and SPM config.
// If cfg is nil, defaults are used. Otherwise, TUI preferences are loaded from cfg.
func NewWithProvidersAndConfig(providers []string, cfg *config.SPMConfig) Model {
	cwd, _ := os.Getwd()

	// Load TUI preferences from config (with env overrides) or use defaults
	var prefs TUIPreferences
	if cfg != nil {
		prefs = TUIPreferencesFromConfig(cfg)
	} else {
		prefs = LoadTUIPreferences()
	}

	// Create theme from preferences
	theme := NewTheme(prefs.ThemeOptions)

	profilesPanel := NewProfilesPanelWithTheme(theme)
	if len(providers) > 0 {
		profilesPanel.SetProvider(providers[0])
	}

	// Use runtime config from SPM config if provided
	var runtime config.RuntimeConfig
	if cfg != nil {
		runtime = cfg.Runtime
	} else {
		runtime = config.DefaultSPMConfig().Runtime
	}

	return Model{
		providers:      providers,
		activeProvider: 0,
		profiles:       make(map[string][]Profile),
		selected:       0,
		state:          stateList,
		keys:           defaultKeyMap(),
		styles:         NewStyles(theme),
		providerPanel:  NewProviderPanelWithTheme(providers, theme),
		profilesPanel:  profilesPanel,
		detailPanel:    NewDetailPanelWithTheme(theme),
		usagePanel:     NewUsagePanelWithTheme(theme),
		syncPanel:      NewSyncPanelWithTheme(theme),
		vaultPath:      authfile.DefaultVaultPath(),
		badges:         make(map[string]profileBadge),
		runtime:        runtime,
		cwd:            cwd,
		profileStore:   profile.NewStore(profile.DefaultStorePath()),
		profileMeta:    make(map[string]map[string]*profile.Profile),
		vaultMeta:      make(map[string]map[string]vaultProfileMeta),
		projectStore:   project.NewStore(""),
		healthStorage:  health.NewStorage(""),
		helpRenderer:   NewHelpRenderer(theme),
		theme:          theme,
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
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
	meta := make(map[string]map[string]*profile.Profile)
	vaultMeta := make(map[string]map[string]vaultProfileMeta)

	store := m.profileStore
	if store == nil {
		store = profile.NewStore(profile.DefaultStorePath())
	}

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
		meta[name] = make(map[string]*profile.Profile)
		vaultMeta[name] = make(map[string]vaultProfileMeta)
		for _, prof := range names {
			ps = append(ps, Profile{
				Name:     prof,
				Provider: name,
				IsActive: prof == active,
			})
			if store != nil {
				if loaded, err := store.Load(name, prof); err == nil && loaded != nil {
					meta[name][prof] = loaded
				}
			}
			vaultMeta[name][prof] = loadVaultProfileMeta(vault, name, prof)
		}
		profiles[name] = ps
	}

	return profilesLoadedMsg{profiles: profiles, meta: meta, vaultMeta: vaultMeta}
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
	profiles  map[string][]Profile
	meta      map[string]map[string]*profile.Profile
	vaultMeta map[string]map[string]vaultProfileMeta
}

// errMsg is sent when an error occurs.
type errMsg struct {
	err error
}

// refreshResultMsg is sent when a token refresh operation completes.
type refreshResultMsg struct {
	provider string
	profile  string
	err      error
}

// activateResultMsg is sent when a profile activation completes.
type activateResultMsg struct {
	provider string
	profile  string
	err      error
}

// toastTickMsg is sent to check for expired toasts.
type toastTickMsg struct{}

// toastTick returns a command that ticks after the toast duration.
func toastTick() tea.Cmd {
	return tea.Tick(ToastDuration, func(t time.Time) tea.Msg {
		return toastTickMsg{}
	})
}

// addToast adds a new toast notification and returns a command to schedule expiration.
func (m *Model) addToast(message string, severity StatusSeverity) tea.Cmd {
	m.toasts = append(m.toasts, NewToast(message, severity))
	return toastTick()
}

// expireToasts removes expired toasts and returns true if any remain.
func (m *Model) expireToasts() bool {
	var active []Toast
	for _, t := range m.toasts {
		if !t.IsExpired() {
			active = append(active, t)
		}
	}
	m.toasts = active
	return len(m.toasts) > 0
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
			cmds = append(cmds, m.usagePanel.SetLoading(true))
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

	case toastTickMsg:
		if m.expireToasts() {
			return m, toastTick()
		}
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

	case syncStateLoadedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to load sync state: " + msg.err.Error()
			if m.syncPanel != nil {
				m.syncPanel.SetLoading(false)
			}
			return m, nil
		}
		if m.syncPanel != nil {
			m.syncPanel.SetState(msg.state)
		}
		return m, nil

	case syncMachineAddedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to add machine: " + msg.err.Error()
		} else {
			m.statusMsg = "Machine added: " + msg.machine.Name
		}
		return m, m.loadSyncState()

	case syncMachineUpdatedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to update machine: " + msg.err.Error()
		} else if msg.machine != nil {
			m.statusMsg = "Machine updated: " + msg.machine.Name
		} else {
			m.statusMsg = "Machine updated"
		}
		return m, m.loadSyncState()

	case syncMachineRemovedMsg:
		if msg.err != nil {
			m.statusMsg = "Failed to remove machine: " + msg.err.Error()
		} else {
			m.statusMsg = "Machine removed"
		}
		return m, m.loadSyncState()

	case syncTestResultMsg:
		if msg.err != nil {
			m.statusMsg = "Connection test failed: " + msg.err.Error()
		} else if msg.success {
			m.statusMsg = "Connection test: " + msg.message
		} else {
			m.statusMsg = "Connection test failed: " + msg.message
		}
		return m, nil

	case syncStartedMsg:
		var spinnerCmd tea.Cmd
		if m.syncPanel != nil {
			spinnerCmd = m.syncPanel.SetSyncing(true)
		}
		if msg.machineName != "" {
			m.statusMsg = "Syncing " + msg.machineName + "..."
		} else {
			m.statusMsg = "Syncing..."
		}
		return m, spinnerCmd

	case syncCompletedMsg:
		if m.syncPanel != nil {
			m.syncPanel.SetSyncing(false)
		}
		if msg.err != nil {
			m.statusMsg = "Sync failed: " + msg.err.Error()
		} else {
			name := msg.machineName
			if name == "" {
				name = "machine"
			}
			stats := msg.stats
			m.statusMsg = fmt.Sprintf(
				"Sync complete (%s): %d pushed, %d pulled, %d skipped, %d failed",
				name,
				stats.Pushed,
				stats.Pulled,
				stats.Skipped,
				stats.Failed,
			)
		}
		return m, m.loadSyncState()

	case spinner.TickMsg:
		// Forward spinner tick messages to panels with active spinners.
		var cmds []tea.Cmd
		if m.usagePanel != nil && m.usagePanel.loading && m.usagePanel.Visible() {
			_, cmd := m.usagePanel.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if m.syncPanel != nil && (m.syncPanel.loading || m.syncPanel.syncing) && m.syncPanel.Visible() {
			_, cmd := m.syncPanel.Update(msg)
			if cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if len(cmds) > 0 {
			return m, tea.Batch(cmds...)
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKeyPress(msg)

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.clampDialogWidths()
		return m, nil

	case profilesLoadedMsg:
		m.profiles = msg.profiles
		if msg.meta != nil {
			m.profileMeta = msg.meta
		} else {
			m.profileMeta = make(map[string]map[string]*profile.Profile)
		}
		if msg.vaultMeta != nil {
			m.vaultMeta = msg.vaultMeta
		} else {
			m.vaultMeta = make(map[string]map[string]vaultProfileMeta)
		}
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

	case profilesRefreshedMsg:
		if msg.err != nil {
			m.showError(msg.err, "Refresh profiles")
			return m, nil
		}
		m.profiles = msg.profiles
		if msg.meta != nil {
			m.profileMeta = msg.meta
		}
		if msg.vaultMeta != nil {
			m.vaultMeta = msg.vaultMeta
		}
		// Restore selection intelligently based on context
		m.restoreSelection(msg.ctx)
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

	case activateResultMsg:
		if msg.err != nil {
			m.showError(msg.err, "Activate")
			return m, nil
		}
		m.showActivateSuccess(msg.provider, msg.profile)
		// Refresh profiles to update active state
		ctx := refreshContext{
			provider:        msg.provider,
			selectedProfile: msg.profile,
		}
		return m, m.refreshProfiles(ctx)

	case refreshResultMsg:
		if msg.err != nil {
			m.showError(msg.err, "Refresh")
			return m, nil
		}
		m.showRefreshSuccess(msg.profile, time.Time{}) // TODO: pass actual expiry time
		// Refresh profiles to update any changed state
		ctx := refreshContext{
			provider:        msg.provider,
			selectedProfile: msg.profile,
		}
		return m, m.refreshProfiles(ctx)

	case errMsg:
		m.err = msg.err
		m.statusMsg = msg.err.Error()
		if m.watcher != nil {
			return m, m.watchProfiles()
		}
		return m, nil

	case exportCompleteMsg:
		return m.handleExportComplete(msg)

	case exportErrorMsg:
		return m.handleExportError(msg)

	case importPreviewMsg:
		return m.handleImportPreview(msg)

	case importCompleteMsg:
		return m.handleImportComplete(msg)

	case importErrorMsg:
		return m.handleImportError(msg)
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
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		case "2":
			m.usagePanel.SetTimeRange(7)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		case "3":
			m.usagePanel.SetTimeRange(30)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		case "4":
			m.usagePanel.SetTimeRange(0)
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		}
	}

	// Sync panel overlay gets keys when visible.
	if m.syncPanel != nil && m.syncPanel.Visible() {
		return m.handleSyncPanelKeys(msg)
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
	case stateBackupDialog:
		return m.handleBackupDialogKeys(msg)
	case stateConfirmOverwrite:
		return m.handleConfirmOverwriteKeys(msg)
	case stateExportConfirm:
		return m.handleExportConfirmKeys(msg)
	case stateImportPath:
		return m.handleImportPathKeys(msg)
	case stateImportConfirm:
		return m.handleImportConfirmKeys(msg)
	case stateEditProfile:
		return m.handleEditProfileKeys(msg)
	case stateSyncAdd:
		return m.handleSyncAddKeys(msg)
	case stateSyncEdit:
		return m.handleSyncEditKeys(msg)
	case stateCommandPalette:
		return m.handleCommandPaletteKeys(msg)
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
		if m.profilesPanel != nil {
			m.profilesPanel.MoveUp()
			m.selected = m.profilesPanel.GetSelected()
			if info := m.profilesPanel.GetSelectedProfile(); info != nil {
				m.selectedProfileName = info.Name
			}
		} else if m.selected > 0 {
			m.selected--
			if name := m.selectedProfileNameValue(); name != "" {
				m.selectedProfileName = name
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Down):
		if m.profilesPanel != nil {
			m.profilesPanel.MoveDown()
			m.selected = m.profilesPanel.GetSelected()
			if info := m.profilesPanel.GetSelectedProfile(); info != nil {
				m.selectedProfileName = info.Name
			}
		} else {
			profiles := m.currentProfiles()
			if m.selected < len(profiles)-1 {
				m.selected++
				if name := m.selectedProfileNameValue(); name != "" {
					m.selectedProfileName = name
				}
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Left):
		if m.activeProvider > 0 {
			m.activeProvider--
			m.selected = 0
			m.selectedProfileName = ""
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Right):
		if m.activeProvider < len(m.providers)-1 {
			m.activeProvider++
			m.selected = 0
			m.selectedProfileName = ""
			m.syncProfilesPanel()
		}
		return m, nil

	case key.Matches(msg, m.keys.Enter):
		return m.handleActivateProfile()

	case key.Matches(msg, m.keys.Tab):
		// Cycle through providers
		m.activeProvider = (m.activeProvider + 1) % len(m.providers)
		m.selected = 0
		m.selectedProfileName = ""
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
			spinnerCmd := m.usagePanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadUsageStats())
		}
		return m, nil

	case key.Matches(msg, m.keys.Sync):
		if m.syncPanel == nil {
			return m, nil
		}
		m.syncPanel.Toggle()
		if m.syncPanel.Visible() {
			spinnerCmd := m.syncPanel.SetLoading(true)
			return m, tea.Batch(spinnerCmd, m.loadSyncState())
		}
		return m, nil

	case key.Matches(msg, m.keys.Export):
		return m.handleExportVault()

	case key.Matches(msg, m.keys.Import):
		return m.handleImportBundle()

	case key.Matches(msg, m.keys.Palette):
		return m.handleOpenCommandPalette()
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
		info := m.buildProfileInfo(provider, p, projectDefault)
		if profileMatchesQuery(info, query) {
			filtered = append(filtered, info)
		}
	}

	m.profilesPanel.SetProfiles(filtered)
	m.selected = 0
	m.profilesPanel.SetSelected(0)
	if info := m.profilesPanel.GetSelectedProfile(); info != nil {
		m.selectedProfileName = info.Name
	} else {
		m.selectedProfileName = ""
	}
	// Note: statusMsg not set here; search bar shows match count
}

// handleActivateProfile initiates profile activation with confirmation.
// Confirmation is required because activation replaces current auth files,
// which could be lost if not backed up.
func (m Model) handleActivateProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}

	// Check if this profile is already active (no-op)
	if info.IsActive {
		m.statusMsg = fmt.Sprintf("'%s' is already active", info.Name)
		return m, nil
	}

	// Enter confirmation state
	m.state = stateConfirm
	m.pendingAction = confirmActivate
	m.statusMsg = fmt.Sprintf("Activate '%s'? Current auth will be replaced. (y/n)", info.Name)
	return m, nil
}

// handleDeleteProfile initiates profile deletion with confirmation.
func (m Model) handleDeleteProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	m.state = stateConfirm
	m.pendingAction = confirmDelete
	m.statusMsg = fmt.Sprintf("Delete '%s'? (y/n)", info.Name)
	return m, nil
}

// handleBackupProfile initiates backup of the current auth state to a named profile.
func (m Model) handleBackupProfile() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	if provider == "" {
		m.statusMsg = "No provider selected"
		return m, nil
	}

	// Check if auth files exist for this provider
	fileSet, ok := authFileSetForProvider(provider)
	if !ok {
		m.statusMsg = fmt.Sprintf("Unknown provider: %s", provider)
		return m, nil
	}

	if !authfile.HasAuthFiles(fileSet) {
		m.statusMsg = fmt.Sprintf("No auth files found for %s - nothing to backup", provider)
		return m, nil
	}

	// Create text input dialog for profile name
	m.backupDialog = NewTextInputDialog(
		fmt.Sprintf("Backup %s Auth", provider),
		"Enter profile name (alphanumeric, underscore, hyphen, or period):",
	)
	m.backupDialog.SetStyles(m.styles)
	m.backupDialog.SetPlaceholder("work-main")
	m.backupDialog.SetWidth(m.dialogWidth(50))
	m.state = stateBackupDialog
	m.statusMsg = ""
	return m, nil
}

// handleBackupDialogKeys handles key input for the backup dialog.
func (m Model) handleBackupDialogKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.backupDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.backupDialog, cmd = m.backupDialog.Update(msg)

	// Check dialog result
	switch m.backupDialog.Result() {
	case DialogResultSubmit:
		profileName := m.backupDialog.Value()
		return m.processBackupSubmit(profileName)

	case DialogResultCancel:
		m.backupDialog = nil
		m.state = stateList
		m.statusMsg = "Backup cancelled"
		return m, nil
	}

	return m, cmd
}

// processBackupSubmit validates the profile name and initiates backup.
func (m Model) processBackupSubmit(profileName string) (tea.Model, tea.Cmd) {
	provider := m.currentProvider()

	// Validate profile name
	profileName = strings.TrimSpace(profileName)
	if profileName == "" {
		m.statusMsg = "Profile name cannot be empty"
		m.backupDialog.Reset()
		return m, nil
	}

	// Check for reserved names
	if profileName == "." || profileName == ".." {
		m.statusMsg = "Profile name cannot be '.' or '..'"
		m.backupDialog.Reset()
		return m, nil
	}

	// Only allow alphanumeric, underscore, hyphen, and period
	// This matches the vault validation in authfile.go and profile.go
	// to prevent shell injection and filesystem issues
	for _, r := range profileName {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.') {
			m.statusMsg = "Profile name can only contain letters, numbers, underscore, hyphen, and period"
			m.backupDialog.Reset()
			return m, nil
		}
	}

	// Check if profile already exists
	vault := authfile.NewVault(m.vaultPath)
	profiles, err := vault.List(provider)
	if err != nil {
		m.statusMsg = fmt.Sprintf("Error listing profiles: %v", err)
		m.backupDialog = nil
		m.state = stateList
		return m, nil
	}

	profileExists := false
	for _, p := range profiles {
		if p == profileName {
			profileExists = true
			break
		}
	}

	if profileExists {
		// Show overwrite confirmation dialog
		m.backupDialog = nil
		m.pendingProfile = profileName
		m.confirmDialog = NewConfirmDialog(
			"Profile Exists",
			fmt.Sprintf("Profile '%s' already exists. Overwrite?", profileName),
		)
		m.confirmDialog.SetStyles(m.styles)
		m.confirmDialog.SetLabels("Overwrite", "Cancel")
		m.confirmDialog.SetWidth(m.dialogWidth(50))
		m.state = stateConfirmOverwrite
		return m, nil
	}

	// Execute backup
	return m.executeBackup(profileName)
}

// handleConfirmOverwriteKeys handles key input for the overwrite confirmation dialog.
func (m Model) handleConfirmOverwriteKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.confirmDialog == nil {
		m.state = stateList
		return m, nil
	}

	// Update the dialog with the key press
	var cmd tea.Cmd
	m.confirmDialog, cmd = m.confirmDialog.Update(msg)

	// Check dialog result
	switch m.confirmDialog.Result() {
	case DialogResultSubmit:
		if m.confirmDialog.Confirmed() {
			profileName := m.pendingProfile
			m.confirmDialog = nil
			m.pendingProfile = ""
			return m.executeBackup(profileName)
		}
		// User selected "No" - cancel overwrite
		m.confirmDialog = nil
		m.pendingProfile = ""
		m.state = stateList
		m.statusMsg = "Backup cancelled"
		return m, nil

	case DialogResultCancel:
		m.confirmDialog = nil
		m.pendingProfile = ""
		m.state = stateList
		m.statusMsg = "Backup cancelled"
		return m, nil
	}

	return m, cmd
}

// handleSyncPanelKeys handles keys when the sync panel is visible.
func (m Model) handleSyncPanelKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.syncPanel == nil {
		return m, nil
	}

	switch msg.String() {
	case "esc", "S":
		m.syncPanel.Toggle()
		return m, nil

	case "up", "k":
		m.syncPanel.MoveUp()
		return m, nil

	case "down", "j":
		m.syncPanel.MoveDown()
		return m, nil

	case "a":
		m.syncAddDialog = newSyncMachineDialog("Add Sync Machine", nil)
		m.syncAddDialog.SetStyles(m.styles)
		m.syncAddDialog.SetWidth(m.dialogWidth(m.syncAddDialog.width))
		m.state = stateSyncAdd
		m.statusMsg = ""
		return m, nil

	case "r":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			return m, m.removeSyncMachine(machine.ID)
		}
		return m, nil

	case "e":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			m.pendingSyncMachine = machine.ID
			m.syncEditDialog = newSyncMachineDialog("Edit Sync Machine", machine)
			m.syncEditDialog.SetStyles(m.styles)
			m.syncEditDialog.SetWidth(m.dialogWidth(m.syncEditDialog.width))
			m.state = stateSyncEdit
			m.statusMsg = ""
			return m, nil
		}
		return m, nil

	case "t":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			m.statusMsg = "Testing connection to " + machine.Name + "..."
			return m, m.testSyncMachine(machine.ID)
		}
		return m, nil

	case "s":
		if machine := m.syncPanel.SelectedMachine(); machine != nil {
			m.statusMsg = "Syncing " + machine.Name + "..."
			spinnerCmd := m.syncPanel.SetSyncing(true)
			return m, tea.Batch(spinnerCmd, m.syncWithMachine(machine.ID))
		}
		return m, nil

	case "l":
		m.statusMsg = "View sync history via CLI: caam sync log"
		return m, nil
	}

	return m, nil
}

// executeBackup performs the actual backup operation.
func (m Model) executeBackup(profileName string) (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	fileSet, ok := authFileSetForProvider(provider)
	if !ok {
		m.state = stateList
		m.statusMsg = fmt.Sprintf("Unknown provider: %s", provider)
		return m, nil
	}

	vault := authfile.NewVault(m.vaultPath)
	if err := vault.Backup(fileSet, profileName); err != nil {
		m.state = stateList
		m.statusMsg = fmt.Sprintf("Backup failed: %v", err)
		return m, nil
	}

	m.state = stateList
	m.statusMsg = fmt.Sprintf("Backed up %s auth to '%s'", provider, profileName)

	// Reload profiles to show the new backup
	return m, m.loadProfiles
}

// handleLoginProfile initiates login/refresh for the selected profile.
func (m Model) handleLoginProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	provider := m.currentProvider()

	m.statusMsg = fmt.Sprintf("Refreshing %s token...", info.Name)

	// Return a command that performs the async refresh
	return m, m.doRefreshProfile(provider, info.Name)
}

// doRefreshProfile returns a tea.Cmd that performs the token refresh.
func (m Model) doRefreshProfile(provider, profile string) tea.Cmd {
	return func() tea.Msg {
		vault := authfile.NewVault(m.vaultPath)

		// Get health storage for updating health data after refresh
		store := health.NewStorage("")

		// Perform the refresh
		ctx := context.Background()
		err := refresh.RefreshProfile(ctx, provider, profile, vault, store)

		return refreshResultMsg{
			provider: provider,
			profile:  profile,
			err:      err,
		}
	}
}

// doActivateProfile returns a tea.Cmd that performs the profile activation.
func (m Model) doActivateProfile(provider, profile string) tea.Cmd {
	return func() tea.Msg {
		fileSet, ok := authFileSetForProvider(provider)
		if !ok {
			return activateResultMsg{
				provider: provider,
				profile:  profile,
				err:      fmt.Errorf("unknown provider: %s", provider),
			}
		}

		vault := authfile.NewVault(m.vaultPath)
		if err := vault.Restore(fileSet, profile); err != nil {
			return activateResultMsg{
				provider: provider,
				profile:  profile,
				err:      err,
			}
		}

		return activateResultMsg{
			provider: provider,
			profile:  profile,
			err:      nil,
		}
	}
}

// handleOpenInBrowser opens the account page in browser.
func (m Model) handleOpenInBrowser() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	url, ok := providerAccountURLs[provider]
	if !ok {
		m.statusMsg = fmt.Sprintf("No account URL for %s", provider)
		return m, nil
	}

	launcher := &browser.DefaultLauncher{}
	if err := launcher.Open(url); err != nil {
		// If browser launch fails, show the URL so user can copy it
		m.statusMsg = fmt.Sprintf("Open in browser: %s", url)
		return m, nil
	}

	m.statusMsg = fmt.Sprintf("Opened %s account page in browser", strings.ToUpper(provider[:1])+provider[1:])
	return m, nil
}

// handleEditProfile opens the edit view for the selected profile.
func (m Model) handleEditProfile() (tea.Model, tea.Cmd) {
	info := m.selectedProfileInfo()
	if info == nil {
		m.statusMsg = "No profile selected"
		return m, nil
	}
	provider := m.currentProvider()

	meta := m.profileMetaFor(provider, info.Name)
	if meta == nil {
		m.statusMsg = fmt.Sprintf("Profile metadata not found. Create with: caam profile add %s %s", provider, info.Name)
		return m, nil
	}

	fields := []FieldDefinition{
		{Label: "Description", Placeholder: "Notes about this profile", Value: meta.Description, Required: false},
		{Label: "Account Label", Placeholder: "user@example.com", Value: meta.AccountLabel, Required: false},
		{Label: "Browser Command", Placeholder: "chrome / firefox", Value: meta.BrowserCommand, Required: false},
		{Label: "Browser Profile", Placeholder: "Profile 1", Value: meta.BrowserProfileDir, Required: false},
		{Label: "Browser Name", Placeholder: "Work Chrome", Value: meta.BrowserProfileName, Required: false},
	}

	m.editDialog = NewMultiFieldDialog("Edit Profile", fields)
	m.editDialog.SetStyles(m.styles)
	m.editDialog.SetWidth(m.dialogWidth(64))
	m.pendingEditProvider = provider
	m.pendingEditProfile = info.Name
	m.state = stateEditProfile
	m.statusMsg = ""
	return m, nil
}

func (m Model) handleEditProfileKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.editDialog == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.editDialog, cmd = m.editDialog.Update(msg)

	switch m.editDialog.Result() {
	case DialogResultSubmit:
		provider := m.pendingEditProvider
		name := m.pendingEditProfile
		meta := m.profileMetaFor(provider, name)
		if meta == nil {
			m.editDialog = nil
			m.state = stateList
			m.pendingEditProvider = ""
			m.pendingEditProfile = ""
			m.statusMsg = "Profile metadata not found"
			return m, nil
		}

		values := m.editDialog.ValueMap()
		meta.Description = strings.TrimSpace(values["Description"])
		meta.AccountLabel = strings.TrimSpace(values["Account Label"])
		meta.BrowserCommand = strings.TrimSpace(values["Browser Command"])
		meta.BrowserProfileDir = strings.TrimSpace(values["Browser Profile"])
		meta.BrowserProfileName = strings.TrimSpace(values["Browser Name"])

		if err := meta.Save(); err != nil {
			m.editDialog = nil
			m.state = stateList
			m.statusMsg = fmt.Sprintf("Failed to save profile: %v", err)
			return m, nil
		}

		m.editDialog = nil
		m.state = stateList
		m.pendingEditProvider = ""
		m.pendingEditProfile = ""
		m.statusMsg = "Profile updated"
		m.syncProfilesPanel()
		m.syncDetailPanel()
		return m, nil

	case DialogResultCancel:
		m.editDialog = nil
		m.state = stateList
		m.pendingEditProvider = ""
		m.pendingEditProfile = ""
		m.statusMsg = "Edit cancelled"
		return m, nil
	}

	return m, cmd
}

type syncMachineDialogValues struct {
	Name    string
	Address string
	Port    string
	User    string
	KeyPath string
}

func syncDialogValuesFromMachine(machine *sync.Machine) syncMachineDialogValues {
	values := syncMachineDialogValues{}
	if machine == nil {
		return values
	}
	values.Name = machine.Name
	values.Address = machine.Address
	if machine.Port > 0 {
		values.Port = fmt.Sprintf("%d", machine.Port)
	}
	values.User = machine.SSHUser
	values.KeyPath = machine.SSHKeyPath
	return values
}

func syncDialogValuesFromMap(values map[string]string) syncMachineDialogValues {
	return syncMachineDialogValues{
		Name:    strings.TrimSpace(values["Name"]),
		Address: strings.TrimSpace(values["Address"]),
		Port:    strings.TrimSpace(values["Port"]),
		User:    strings.TrimSpace(values["User"]),
		KeyPath: strings.TrimSpace(values["Key Path"]),
	}
}

func newSyncMachineDialogWithValues(title string, values syncMachineDialogValues) *MultiFieldDialog {
	fields := []FieldDefinition{
		{Label: "Name", Placeholder: "work-laptop", Value: values.Name, Required: false},
		{Label: "Address", Placeholder: "192.168.1.100", Value: values.Address, Required: false},
		{Label: "Port", Placeholder: "22", Value: values.Port, Required: false},
		{Label: "User", Placeholder: "ssh user (optional)", Value: values.User, Required: false},
		{Label: "Key Path", Placeholder: "~/.ssh/id_rsa (optional)", Value: values.KeyPath, Required: false},
	}
	dialog := NewMultiFieldDialog(title, fields)
	dialog.SetWidth(64)
	return dialog
}

func newSyncMachineDialog(title string, machine *sync.Machine) *MultiFieldDialog {
	return newSyncMachineDialogWithValues(title, syncDialogValuesFromMachine(machine))
}

func (m Model) handleSyncAddKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.syncAddDialog == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.syncAddDialog, cmd = m.syncAddDialog.Update(msg)

	switch m.syncAddDialog.Result() {
	case DialogResultSubmit:
		values := syncDialogValuesFromMap(m.syncAddDialog.ValueMap())
		if values.Name == "" || values.Address == "" {
			m.statusMsg = "Name and address are required"
			m.syncAddDialog = newSyncMachineDialogWithValues("Add Sync Machine", values)
			m.syncAddDialog.SetStyles(m.styles)
			m.syncAddDialog.SetWidth(m.dialogWidth(m.syncAddDialog.width))
			m.state = stateSyncAdd
			return m, nil
		}
		m.syncAddDialog = nil
		m.state = stateList
		m.statusMsg = "Adding machine..."
		return m, m.addSyncMachine(values.Name, values.Address, values.Port, values.User, values.KeyPath)

	case DialogResultCancel:
		m.syncAddDialog = nil
		m.state = stateList
		m.statusMsg = "Add cancelled"
		return m, nil
	}

	return m, cmd
}

func (m Model) handleSyncEditKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.syncEditDialog == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.syncEditDialog, cmd = m.syncEditDialog.Update(msg)

	switch m.syncEditDialog.Result() {
	case DialogResultSubmit:
		values := syncDialogValuesFromMap(m.syncEditDialog.ValueMap())
		if values.Name == "" || values.Address == "" {
			m.statusMsg = "Name and address are required"
			m.syncEditDialog = newSyncMachineDialogWithValues("Edit Sync Machine", values)
			m.syncEditDialog.SetStyles(m.styles)
			m.syncEditDialog.SetWidth(m.dialogWidth(m.syncEditDialog.width))
			m.state = stateSyncEdit
			return m, nil
		}
		machineID := m.pendingSyncMachine
		m.syncEditDialog = nil
		m.state = stateList
		m.pendingSyncMachine = ""
		m.statusMsg = "Updating machine..."
		return m, m.updateSyncMachine(machineID, values.Name, values.Address, values.Port, values.User, values.KeyPath)

	case DialogResultCancel:
		m.syncEditDialog = nil
		m.state = stateList
		m.pendingSyncMachine = ""
		m.statusMsg = "Edit cancelled"
		return m, nil
	}

	return m, cmd
}

// handleEnterSearchMode enters search/filter mode.
func (m Model) handleEnterSearchMode() (tea.Model, tea.Cmd) {
	m.state = stateSearch
	m.searchQuery = ""
	m.statusMsg = "" // Search bar shows all search info
	return m, nil
}

// handleOpenCommandPalette opens the command palette overlay.
func (m Model) handleOpenCommandPalette() (tea.Model, tea.Cmd) {
	m.commandPalette = NewCommandPaletteDialog("Command Palette", DefaultCommands())
	m.commandPalette.SetStyles(m.styles)
	m.commandPalette.SetWidth(60)
	m.state = stateCommandPalette
	m.statusMsg = ""
	return m, nil
}

// handleCommandPaletteAction executes the selected command palette action.
func (m Model) handleCommandPaletteAction(action string) (tea.Model, tea.Cmd) {
	m.state = stateList
	m.commandPalette = nil

	switch action {
	case "activate":
		return m.handleActivateProfile()
	case "backup":
		return m.handleBackupProfile()
	case "delete":
		return m.handleDeleteProfile()
	case "edit":
		return m.handleEditProfile()
	case "login":
		return m.handleLoginProfile()
	case "open":
		return m.handleOpenInBrowser()
	case "project":
		return m.handleSetProjectAssociation()
	case "usage":
		if m.usagePanel != nil {
			m.usagePanel.Toggle()
			if m.usagePanel.Visible() {
				spinnerCmd := m.usagePanel.SetLoading(true)
				return m, tea.Batch(spinnerCmd, m.loadUsageStats())
			}
		}
	case "sync":
		if m.syncPanel != nil {
			m.syncPanel.Toggle()
			if m.syncPanel.Visible() {
				spinnerCmd := m.syncPanel.SetLoading(true)
				return m, tea.Batch(spinnerCmd, m.loadSyncState())
			}
		}
	case "export":
		return m.handleExportVault()
	case "import":
		return m.handleImportBundle()
	case "help":
		m.state = stateHelp
	}

	return m, nil
}

// handleCommandPaletteKeys handles keys when the command palette is open.
func (m Model) handleCommandPaletteKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.commandPalette == nil {
		m.state = stateList
		return m, nil
	}

	var cmd tea.Cmd
	m.commandPalette, cmd = m.commandPalette.Update(msg)

	switch m.commandPalette.Result() {
	case DialogResultSubmit:
		if chosen := m.commandPalette.ChosenCommand(); chosen != nil {
			return m.handleCommandPaletteAction(chosen.Action)
		}
		m.state = stateList
		m.commandPalette = nil
	case DialogResultCancel:
		m.state = stateList
		m.commandPalette = nil
	}

	return m, cmd
}

func (m Model) handleSetProjectAssociation() (tea.Model, tea.Cmd) {
	provider := m.currentProvider()
	info := m.selectedProfileInfo()
	if provider == "" || info == nil {
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

	profileName := info.Name
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
	case confirmActivate:
		info := m.selectedProfileInfo()
		if info != nil {
			provider := m.currentProvider()

			m.statusMsg = fmt.Sprintf("Activating %s...", info.Name)
			m.state = stateList
			m.pendingAction = confirmNone

			return m, m.doActivateProfile(provider, info.Name)
		}

	case confirmDelete:
		info := m.selectedProfileInfo()
		if info != nil {
			provider := m.currentProvider()

			// Perform the deletion via vault
			vault := authfile.NewVault(m.vaultPath)
			if err := vault.Delete(provider, info.Name); err != nil {
				m.showError(err, fmt.Sprintf("Delete %s", info.Name))
				m.state = stateList
				m.pendingAction = confirmNone
				return m, nil
			}

			m.showDeleteSuccess(info.Name)
			m.state = stateList
			m.pendingAction = confirmNone

			// Refresh profiles with context for intelligent selection restoration
			ctx := refreshContext{
				provider:       provider,
				deletedProfile: info.Name,
			}
			return m, m.refreshProfiles(ctx)
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

func (m Model) selectedProfileInfo() *ProfileInfo {
	if m.profilesPanel != nil {
		if info := m.profilesPanel.GetSelectedProfile(); info != nil {
			return info
		}
	}
	profiles := m.currentProfiles()
	if m.selected >= 0 && m.selected < len(profiles) {
		provider := m.currentProvider()
		projectDefault := m.projectDefaultForProvider(provider)
		info := m.buildProfileInfo(provider, profiles[m.selected], projectDefault)
		return &info
	}
	return nil
}

func (m Model) selectedProfileNameValue() string {
	if info := m.selectedProfileInfo(); info != nil {
		return info.Name
	}
	if m.selectedProfileName != "" {
		return m.selectedProfileName
	}
	profiles := m.currentProfiles()
	if m.selected >= 0 && m.selected < len(profiles) {
		return profiles[m.selected].Name
	}
	return ""
}

func (m Model) profileMetaFor(provider, name string) *profile.Profile {
	if m.profileMeta == nil {
		return nil
	}
	byProvider, ok := m.profileMeta[provider]
	if !ok {
		return nil
	}
	return byProvider[name]
}

func (m Model) vaultMetaFor(provider, name string) vaultProfileMeta {
	if m.vaultMeta == nil {
		return vaultProfileMeta{}
	}
	byProvider, ok := m.vaultMeta[provider]
	if !ok {
		return vaultProfileMeta{}
	}
	return byProvider[name]
}

func loadVaultProfileMeta(vault *authfile.Vault, provider, name string) vaultProfileMeta {
	meta := vaultProfileMeta{}
	if vault == nil || provider == "" || name == "" {
		return meta
	}

	profileDir := vault.ProfilePath(provider, name)
	metaPath := filepath.Join(profileDir, "meta.json")
	if raw, err := os.ReadFile(metaPath); err == nil {
		var stored struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(raw, &stored); err == nil {
			meta.Description = strings.TrimSpace(stored.Description)
		}
	}

	meta.Account = vaultIdentityEmail(provider, profileDir)
	return meta
}

func vaultIdentityEmail(provider, profileDir string) string {
	var id *identity.Identity
	switch provider {
	case "codex":
		id, _ = identity.ExtractFromCodexAuth(filepath.Join(profileDir, "auth.json"))
	case "claude":
		id, _ = identity.ExtractFromClaudeCredentials(filepath.Join(profileDir, ".credentials.json"))
	case "gemini":
		id, _ = identity.ExtractFromGeminiConfig(filepath.Join(profileDir, "settings.json"))
		if id == nil {
			id, _ = identity.ExtractFromGeminiConfig(filepath.Join(profileDir, "oauth_credentials.json"))
		}
	}
	if id == nil {
		return ""
	}
	return strings.TrimSpace(id.Email)
}

func profileAccountLabel(meta *profile.Profile) string {
	if meta == nil {
		return ""
	}
	if meta.AccountLabel != "" {
		return meta.AccountLabel
	}
	if meta.Identity != nil && meta.Identity.Email != "" {
		return meta.Identity.Email
	}
	return ""
}

func profileMatchesQuery(info ProfileInfo, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(strings.ToLower(info.Name), query) {
		return true
	}
	if info.Account != "" && strings.Contains(strings.ToLower(info.Account), query) {
		return true
	}
	if info.Description != "" && strings.Contains(strings.ToLower(info.Description), query) {
		return true
	}
	return false
}

func (m Model) buildProfileInfo(provider string, p Profile, projectDefault string) ProfileInfo {
	authMode := "oauth"
	account := ""
	description := ""
	lastUsed := time.Time{}
	locked := false

	meta := m.profileMetaFor(provider, p.Name)
	if meta != nil {
		if meta.AuthMode != "" {
			authMode = meta.AuthMode
		}
		account = profileAccountLabel(meta)
		description = meta.Description
		lastUsed = meta.LastUsedAt
		locked = meta.IsLocked()
	}

	vmeta := m.vaultMetaFor(provider, p.Name)
	if account == "" {
		account = vmeta.Account
	}
	if description == "" {
		description = vmeta.Description
	}

	healthStatus := health.StatusUnknown
	errorCount := 0
	penalty := float64(0)
	var tokenExpiry time.Time

	if m.healthStorage != nil {
		if h, err := m.healthStorage.GetProfile(provider, p.Name); err == nil && h != nil {
			healthStatus = health.CalculateStatus(h)
			errorCount = h.ErrorCount1h
			penalty = h.Penalty
			tokenExpiry = h.TokenExpiresAt
		}
	}

	return ProfileInfo{
		Name:           p.Name,
		Badge:          m.badgeFor(provider, p.Name),
		ProjectDefault: projectDefault != "" && p.Name == projectDefault,
		AuthMode:       authMode,
		LoggedIn:       true,
		Locked:         locked,
		LastUsed:       lastUsed,
		Account:        account,
		Description:    description,
		IsActive:       p.IsActive,
		HealthStatus:   healthStatus,
		TokenExpiry:    tokenExpiry,
		ErrorCount:     errorCount,
		Penalty:        penalty,
	}
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
func (m *Model) syncProfilesPanel() {
	if m.profilesPanel == nil {
		return
	}
	provider := m.currentProvider()
	m.profilesPanel.SetProvider(provider)

	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)
	infos := make([]ProfileInfo, 0, len(profiles))
	for _, p := range profiles {
		infos = append(infos, m.buildProfileInfo(provider, p, projectDefault))
	}
	m.profilesPanel.SetProfiles(infos)

	if len(infos) == 0 {
		m.selected = 0
		m.selectedProfileName = ""
		return
	}

	selectedIndex := m.selected
	if m.selectedProfileName != "" {
		if m.profilesPanel.SetSelectedByName(m.selectedProfileName) {
			selectedIndex = m.profilesPanel.GetSelected()
		} else {
			if selectedIndex < 0 {
				selectedIndex = 0
			}
			if selectedIndex >= len(infos) {
				selectedIndex = len(infos) - 1
			}
			m.profilesPanel.SetSelected(selectedIndex)
		}
	} else {
		if selectedIndex < 0 || selectedIndex >= len(infos) {
			selectedIndex = 0
		}
		m.profilesPanel.SetSelected(selectedIndex)
	}

	m.selected = selectedIndex
	if info := m.profilesPanel.GetSelectedProfile(); info != nil {
		m.selectedProfileName = info.Name
	}
}

// syncDetailPanel syncs the detail panel with the currently selected profile.
func (m Model) syncDetailPanel() {
	if m.detailPanel == nil {
		return
	}

	// Get the selected profile
	info := m.selectedProfileInfo()
	if info == nil {
		m.detailPanel.SetProfile(nil)
		return
	}

	provider := m.currentProvider()
	profileName := info.Name

	// Default values for health data
	healthStatus := health.StatusUnknown
	errorCount := 0
	penalty := float64(0)
	var tokenExpiry time.Time

	// Fetch real health data if available
	if m.healthStorage != nil {
		if h, err := m.healthStorage.GetProfile(provider, profileName); err == nil && h != nil {
			healthStatus = health.CalculateStatus(h)
			errorCount = h.ErrorCount1h
			penalty = h.Penalty
			tokenExpiry = h.TokenExpiresAt
		}
	}

	authMode := "oauth"
	account := ""
	description := ""
	path := ""
	createdAt := time.Time{}
	lastUsedAt := time.Time{}
	browserCmd := ""
	browserProf := ""
	locked := false

	meta := m.profileMetaFor(provider, profileName)
	if meta != nil {
		if meta.AuthMode != "" {
			authMode = meta.AuthMode
		}
		account = profileAccountLabel(meta)
		description = meta.Description
		createdAt = meta.CreatedAt
		lastUsedAt = meta.LastUsedAt
		browserCmd = meta.BrowserCommand
		if meta.BrowserProfileName != "" {
			browserProf = meta.BrowserProfileName
		} else {
			browserProf = meta.BrowserProfileDir
		}
		path = meta.BasePath
		locked = meta.IsLocked()
	}

	vmeta := m.vaultMetaFor(provider, profileName)
	if account == "" {
		account = vmeta.Account
	}
	if description == "" {
		description = vmeta.Description
	}

	if path == "" {
		vault := authfile.NewVault(m.vaultPath)
		path = vault.ProfilePath(provider, profileName)
	}

	detail := &DetailInfo{
		Name:         profileName,
		Provider:     provider,
		AuthMode:     authMode,
		LoggedIn:     true,
		Locked:       locked,
		Path:         path,
		CreatedAt:    createdAt,
		LastUsedAt:   lastUsedAt,
		Account:      account,
		Description:  description,
		BrowserCmd:   browserCmd,
		BrowserProf:  browserProf,
		HealthStatus: healthStatus,
		TokenExpiry:  tokenExpiry,
		ErrorCount:   errorCount,
		Penalty:      penalty,
	}
	m.detailPanel.SetProfile(detail)
}

// View implements tea.Model.
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	switch m.state {
	case stateHelp:
		return m.helpView()
	case stateBackupDialog:
		return m.dialogOverlayView(m.backupDialog.View())
	case stateConfirmOverwrite:
		return m.dialogOverlayView(m.confirmDialog.View())
	case stateExportConfirm:
		if m.confirmDialog != nil {
			return m.dialogOverlayView(m.confirmDialog.View())
		}
		return m.mainView()
	case stateImportPath:
		if m.backupDialog != nil {
			return m.dialogOverlayView(m.backupDialog.View())
		}
		return m.mainView()
	case stateImportConfirm:
		if m.confirmDialog != nil {
			return m.dialogOverlayView(m.confirmDialog.View())
		}
		return m.mainView()
	case stateEditProfile:
		if m.editDialog != nil {
			return m.dialogOverlayView(m.editDialog.View())
		}
		return m.mainView()
	case stateSyncAdd:
		if m.syncAddDialog != nil {
			return m.dialogOverlayView(m.syncAddDialog.View())
		}
		return m.mainView()
	case stateSyncEdit:
		if m.syncEditDialog != nil {
			return m.dialogOverlayView(m.syncEditDialog.View())
		}
		return m.mainView()
	case stateCommandPalette:
		if m.commandPalette != nil {
			return m.dialogOverlayView(m.commandPalette.View())
		}
		return m.mainView()
	default:
		if m.usagePanel != nil && m.usagePanel.Visible() {
			m.usagePanel.SetSize(m.width, m.height)
			return m.usagePanel.View()
		}
		if m.syncPanel != nil && m.syncPanel.Visible() {
			m.syncPanel.SetSize(m.width, m.height)
			return m.syncPanel.View()
		}
		return m.mainView()
	}
}

// dialogOverlayView renders the main view with a dialog overlay centered on top.
func (m Model) dialogOverlayView(dialogContent string) string {
	if m.width <= 0 || m.height <= 0 {
		return dialogContent
	}

	mainView := m.mainView()
	background := lipgloss.Place(m.width, m.height, lipgloss.Left, lipgloss.Top, mainView)
	background = m.styles.DialogOverlay.Render(background)

	dialogWidth := lipgloss.Width(dialogContent)
	dialogHeight := lipgloss.Height(dialogContent)
	if dialogWidth < 0 {
		dialogWidth = 0
	}
	if dialogHeight < 0 {
		dialogHeight = 0
	}
	if dialogWidth > m.width {
		dialogWidth = m.width
	}
	if dialogHeight > m.height {
		dialogHeight = m.height
	}

	x := (m.width - dialogWidth) / 2
	y := (m.height - dialogHeight) / 2
	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}

	bgLines := padOverlayLines(background, m.width, m.height)
	overlayLines := padOverlayLines(dialogContent, dialogWidth, dialogHeight)

	for i := 0; i < dialogHeight; i++ {
		target := y + i
		if target < 0 || target >= len(bgLines) {
			continue
		}
		left := cutANSI(bgLines[target], 0, x)
		right := cutANSI(bgLines[target], x+dialogWidth, m.width)
		overlay := overlayLines[i]
		if ansi.StringWidth(overlay) > dialogWidth {
			overlay = cutANSI(overlay, 0, dialogWidth)
		}
		bgLines[target] = left + overlay + right
	}

	return strings.Join(bgLines, "\n")
}

func padOverlayLines(content string, width, height int) []string {
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	st := lipgloss.NewStyle().Width(width)
	for i, line := range lines {
		cut := cutANSI(line, 0, width)
		lines[i] = st.Render(cut)
	}
	return lines
}

func cutANSI(s string, left, right int) string {
	if right <= left {
		return ""
	}
	if right < 0 {
		return ""
	}
	if left < 0 {
		left = 0
	}
	truncated := ansi.Truncate(s, right, "")
	if left == 0 {
		return truncated
	}
	return trimLeftANSI(truncated, left)
}

func trimLeftANSI(s string, left int) string {
	if left <= 0 {
		return s
	}
	state := ansi.NormalState
	width := 0
	var out strings.Builder

	for len(s) > 0 {
		seq, w, n, newState := ansi.DecodeSequence(s, state, nil)
		state = newState
		if n == 0 {
			break
		}
		if w == 0 {
			out.WriteString(seq)
			s = s[n:]
			continue
		}
		if width+w <= left {
			width += w
			s = s[n:]
			continue
		}
		out.WriteString(seq)
		out.WriteString(s[n:])
		return out.String()
	}
	return ""
}

// mainView renders the main list view.
func (m Model) mainView() string {
	// Header
	headerLines := []string{m.styles.Header.Render("caam - Coding Agent Account Manager")}
	if projectLine := m.projectContextLine(); projectLine != "" {
		headerLines = append(headerLines, m.styles.StatusText.Render(projectLine))
	}
	header := lipgloss.JoinVertical(lipgloss.Left, headerLines...)

	// Search bar (rendered when in search mode)
	searchBar := m.renderSearchBar()
	searchBarHeight := 0
	if searchBar != "" {
		searchBarHeight = lipgloss.Height(searchBar)
	}

	headerHeight := lipgloss.Height(header)
	contentHeight := m.height - headerHeight - searchBarHeight - 2
	if contentHeight < 0 {
		contentHeight = 0
	}

	var panels string
	layoutMode := m.layoutMode()
	var layout layoutSpec

	if layoutMode != layoutFull {
		tabs := m.renderProviderTabs()
		tabsHeight := lipgloss.Height(tabs)
		layout = m.compactLayoutSpec(layoutMode, contentHeight, tabsHeight)

		var profilesPanelView string
		if m.profilesPanel != nil {
			m.profilesPanel.SetSize(m.width, layout.ProfilesHeight)
			profilesPanelView = m.profilesPanel.View()
		} else {
			profilesPanelView = m.renderProfileList()
		}

		var detailPanelView string
		if m.detailPanel != nil && layout.ShowDetail {
			m.syncDetailPanel()
			m.detailPanel.SetSize(m.width, layout.DetailHeight)
			detailPanelView = m.detailPanel.View()
		}

		if detailPanelView != "" {
			panels = lipgloss.JoinVertical(lipgloss.Left, tabs, profilesPanelView, "", detailPanelView)
		} else {
			panels = lipgloss.JoinVertical(lipgloss.Left, tabs, profilesPanelView)
		}
	} else {
		layout = m.fullLayoutSpec(contentHeight)

		// Sync and render provider panel
		m.providerPanel.SetActiveProvider(m.activeProvider)
		m.providerPanel.SetSize(layout.ProviderWidth, contentHeight)
		providerPanelView := m.providerPanel.View()

		// Sync and render profiles panel (center panel)
		var profilesPanelView string
		if m.profilesPanel != nil {
			m.profilesPanel.SetSize(layout.ProfilesWidth, contentHeight)
			profilesPanelView = m.profilesPanel.View()
		} else {
			profilesPanelView = m.renderProfileList()
		}

		// Sync and render detail panel (right panel)
		var detailPanelView string
		if m.detailPanel != nil {
			m.syncDetailPanel()
			m.detailPanel.SetSize(layout.DetailWidth, contentHeight)
			detailPanelView = m.detailPanel.View()
		}

		// Create panels side by side
		panels = lipgloss.JoinHorizontal(
			lipgloss.Top,
			providerPanelView,
			strings.Repeat(" ", layout.Gap),
			profilesPanelView,
			strings.Repeat(" ", layout.Gap),
			detailPanelView,
		)
	}

	// Status bar
	status := m.renderStatusBar(layout)

	// Combine header, search bar (if active), panels, and status
	var content string
	if searchBar != "" {
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			searchBar,
			panels,
		)
	} else {
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			header,
			"",
			panels,
		)
	}

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

func (m Model) isCompactLayout() bool {
	return m.layoutMode() != layoutFull
}

func (m Model) layoutMode() layoutMode {
	if m.width <= 0 || m.height <= 0 {
		return layoutFull
	}
	if m.width < minFullWidth() || m.height < minFullHeight {
		if m.width < minTinyWidth || m.height < minTinyHeight {
			return layoutTiny
		}
		return layoutCompact
	}
	return layoutFull
}

func minFullWidth() int {
	return minProviderWidth + minProfilesWidth + minDetailWidth + (layoutGap * 2)
}

func (m Model) fullLayoutSpec(contentHeight int) layoutSpec {
	spec := layoutSpec{
		Mode:          layoutFull,
		Gap:           layoutGap,
		ContentHeight: contentHeight,
	}

	if m.width <= 0 {
		return spec
	}

	available := m.width - (layoutGap * 2)
	if available < 0 {
		available = 0
	}

	provider := minProviderWidth
	detail := minDetailWidth
	profiles := minProfilesWidth
	extra := available - (provider + detail + profiles)
	if extra < 0 {
		extra = 0
	}

	// Give most extra width to profiles, then detail, then provider.
	profilesBoost := min(extra, maxProfilesWidth-profiles)
	profiles += profilesBoost
	extra -= profilesBoost

	detailBoost := min(extra, maxDetailWidth-detail)
	detail += detailBoost
	extra -= detailBoost

	providerBoost := min(extra, maxProviderWidth-provider)
	provider += providerBoost
	extra -= providerBoost

	profiles += extra

	if provider < minProviderWidth {
		provider = minProviderWidth
	}
	if detail < minDetailWidth {
		detail = minDetailWidth
	}
	if profiles < minProfilesWidth {
		profiles = minProfilesWidth
	}

	// Final safety check to avoid overflow.
	total := provider + detail + profiles
	if total > available && available > 0 {
		overflow := total - available
		if profiles-overflow >= minProfilesWidth {
			profiles -= overflow
		} else if detail-overflow >= minDetailWidth {
			detail -= overflow
		}
	}

	spec.ProviderWidth = provider
	spec.DetailWidth = detail
	spec.ProfilesWidth = max(0, available-provider-detail)
	return spec
}

func (m Model) compactLayoutSpec(mode layoutMode, contentHeight, tabsHeight int) layoutSpec {
	spec := layoutSpec{
		Mode:          mode,
		Gap:           layoutGap,
		ContentHeight: contentHeight,
	}
	remainingHeight := contentHeight - tabsHeight - 1
	if remainingHeight < 0 {
		remainingHeight = 0
	}

	showDetail := remainingHeight >= minCompactDetailHeight
	profilesHeight := remainingHeight
	detailHeight := 0

	if showDetail {
		profilesHeight = remainingHeight * 6 / 10
		if profilesHeight < minCompactProfilesHeight {
			profilesHeight = minCompactProfilesHeight
		}
		detailHeight = remainingHeight - profilesHeight - 1
		if detailHeight < minCompactDetailMinHeight {
			detailHeight = minCompactDetailMinHeight
			profilesHeight = remainingHeight - detailHeight - 1
			if profilesHeight < minCompactProfilesHeight {
				profilesHeight = minCompactProfilesHeight
				if profilesHeight+detailHeight+1 > remainingHeight {
					detailHeight = remainingHeight - profilesHeight - 1
					if detailHeight < 0 {
						detailHeight = 0
					}
				}
			}
		}
	}

	spec.ProfilesHeight = profilesHeight
	spec.DetailHeight = detailHeight
	spec.ShowDetail = showDetail && detailHeight > 0
	return spec
}

func (m Model) layoutDebugString(spec layoutSpec) string {
	if spec.Mode == layoutFull {
		return fmt.Sprintf("layout=full w=%d h=%d p=%d pr=%d d=%d", m.width, m.height, spec.ProviderWidth, spec.ProfilesWidth, spec.DetailWidth)
	}
	mode := "compact"
	if spec.Mode == layoutTiny {
		mode = "tiny"
	}
	return fmt.Sprintf("layout=%s w=%d h=%d ph=%d dh=%d", mode, m.width, m.height, spec.ProfilesHeight, spec.DetailHeight)
}

func (m Model) debugEnabled() bool {
	return os.Getenv("CAAM_DEBUG") != ""
}

func (m Model) dialogWidth(preferred int) int {
	if preferred <= 0 {
		preferred = dialogMinWidth
	}
	if m.width <= 0 {
		return preferred
	}
	maxWidth := m.width - dialogMargin
	if maxWidth <= 0 {
		return preferred
	}
	if maxWidth < dialogMinWidth {
		return maxWidth
	}
	if preferred > maxWidth {
		return maxWidth
	}
	return preferred
}

func (m *Model) clampDialogWidths() {
	if m.backupDialog != nil {
		m.backupDialog.SetWidth(m.dialogWidth(m.backupDialog.width))
	}
	if m.confirmDialog != nil {
		m.confirmDialog.SetWidth(m.dialogWidth(m.confirmDialog.width))
	}
	if m.editDialog != nil {
		m.editDialog.SetWidth(m.dialogWidth(m.editDialog.width))
	}
	if m.syncAddDialog != nil {
		m.syncAddDialog.SetWidth(m.dialogWidth(m.syncAddDialog.width))
	}
	if m.syncEditDialog != nil {
		m.syncEditDialog.SetWidth(m.dialogWidth(m.syncEditDialog.width))
	}
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

func (m Model) providerCount(provider string) int {
	if m.profiles == nil {
		return 0
	}
	return len(m.profiles[provider])
}

// renderProviderTabs renders the provider selection tabs.
func (m Model) renderProviderTabs() string {
	var tabs []string
	for i, p := range m.providers {
		label := capitalizeFirst(p)
		if m.width >= 80 {
			if count := m.providerCount(p); count > 0 {
				label = fmt.Sprintf("%s %d", label, count)
			}
		}
		style := m.styles.Tab
		if i == m.activeProvider {
			style = m.styles.ActiveTab
		}
		tabs = append(tabs, style.Render(label))
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

// renderSearchBar renders a visible search bar when in search mode.
func (m Model) renderSearchBar() string {
	if m.state != stateSearch {
		return ""
	}

	// Calculate match count for display
	provider := m.currentProvider()
	profiles := m.profiles[provider]
	projectDefault := m.projectDefaultForProvider(provider)
	query := strings.ToLower(m.searchQuery)
	matchCount := 0
	for _, p := range profiles {
		info := m.buildProfileInfo(provider, p, projectDefault)
		if profileMatchesQuery(info, query) {
			matchCount++
		}
	}

	// Build search bar content
	prompt := m.styles.SearchPrompt.Render("/")
	queryText := m.styles.SearchQuery.Render(m.searchQuery)
	cursor := m.styles.SearchCursor.Render("█")
	matchInfo := m.styles.SearchMatchInfo.Render(fmt.Sprintf(" (%d matches)", matchCount))

	// Hints for search mode
	hints := m.styles.StatusKey.Render("Enter") +
		m.styles.StatusText.Render(" accept  ") +
		m.styles.StatusKey.Render("Esc") +
		m.styles.StatusText.Render(" cancel")

	// Calculate available width for search bar content
	barWidth := m.width - 4 // Account for border padding
	if barWidth < 20 {
		barWidth = 20
	}

	// Left side: prompt + query + cursor + match info
	left := prompt + queryText + cursor + matchInfo
	leftWidth := lipgloss.Width(left)
	hintsWidth := lipgloss.Width(hints)

	// Calculate gap between left and hints
	gap := barWidth - leftWidth - hintsWidth
	if gap < 1 {
		gap = 1
	}

	content := left + strings.Repeat(" ", gap) + hints
	return m.styles.SearchBar.Width(m.width - 2).Render(content)
}

// renderStatusBar renders the bottom status bar with 3 segments:
// left (mode indicator), center (status/toast message), right (key hints).
func (m Model) renderStatusBar(layout layoutSpec) string {
	if m.width <= 0 {
		return ""
	}
	contentWidth := m.width - 2
	if contentWidth < 1 {
		contentWidth = m.width
	}

	// Left segment: mode indicator
	left := m.statusModeIndicator()

	// Right segment: key hints (always visible)
	right := m.statusKeyHints(layout)

	// Center segment: status message or toast
	center := m.statusCenterMessage()

	// Calculate widths
	leftWidth := lipgloss.Width(left)
	rightWidth := lipgloss.Width(right)
	centerWidth := lipgloss.Width(center)

	// Minimum gap between segments
	minGap := 2
	availableForCenter := contentWidth - leftWidth - rightWidth - (2 * minGap)

	// Truncate center if needed
	if center != "" && centerWidth > availableForCenter && availableForCenter > 4 {
		severity := m.statusMessageSeverity()
		truncated := truncateString(m.statusCenterText(), availableForCenter)
		center = m.styles.StatusSeverityStyle(severity).Render(truncated)
		centerWidth = lipgloss.Width(center)
	}

	// Build the line with proper spacing
	if center == "" {
		// No center message: left + gap + right
		gap := contentWidth - leftWidth - rightWidth
		if gap < 1 {
			gap = 1
		}
		line := left + strings.Repeat(" ", gap) + right
		return m.styles.StatusBar.Width(m.width).Render(line)
	}

	// With center message: left + gap + center + gap + right
	leftGap := minGap
	totalUsed := leftWidth + leftGap + centerWidth + minGap + rightWidth
	if totalUsed > contentWidth {
		// Compress gaps evenly
		leftGap = 1
	}
	rightGap := contentWidth - leftWidth - leftGap - centerWidth - rightWidth
	if rightGap < 1 {
		rightGap = 1
	}

	line := left + strings.Repeat(" ", leftGap) + center + strings.Repeat(" ", rightGap) + right
	return m.styles.StatusBar.Width(m.width).Render(line)
}

// statusModeIndicator returns the rendered mode indicator for the status bar.
func (m Model) statusModeIndicator() string {
	switch m.state {
	case stateSearch:
		return m.styles.StatusModeSearch.Render("SEARCH")
	case stateHelp:
		return m.styles.StatusModeHelp.Render("HELP")
	case stateCommandPalette:
		return m.styles.StatusModeSearch.Render("CMD")
	default:
		// Show current provider as context
		if len(m.providers) > 0 && m.activeProvider >= 0 && m.activeProvider < len(m.providers) {
			provider := strings.ToUpper(m.providers[m.activeProvider])
			return m.styles.StatusModeNormal.Render(provider)
		}
		return m.styles.StatusModeNormal.Render("NORMAL")
	}
}

// statusKeyHints returns the key hints for the status bar right segment.
func (m Model) statusKeyHints(layout layoutSpec) string {
	hint := func(key, action string) string {
		return m.styles.StatusText.Render("[") +
			m.styles.StatusKey.Render(key) +
			m.styles.StatusText.Render(":"+action+"]")
	}

	var hints string
	switch {
	case m.width < 70:
		hints = hint("tab", "provider")
	case m.width < 100:
		hints = hint("tab", "provider") + " " + hint("/", "search")
	default:
		hints = hint("tab", "provider") + " " + hint("enter", "activate") + " " + hint("/", "search")
	}

	if m.debugEnabled() {
		debugLine := m.layoutDebugString(layout)
		if debugLine != "" {
			hints += "  " + m.styles.StatusText.Render(debugLine)
		}
	}

	return hints
}

// statusCenterText returns the raw text for the center status message.
func (m Model) statusCenterText() string {
	// Toasts take priority over statusMsg
	if len(m.toasts) > 0 {
		return m.toasts[len(m.toasts)-1].Message
	}
	return m.statusMsg
}

// statusMessageSeverity returns the severity for the current center message.
func (m Model) statusMessageSeverity() StatusSeverity {
	if len(m.toasts) > 0 {
		return m.toasts[len(m.toasts)-1].Severity
	}
	return statusSeverityFromMessage(m.statusMsg)
}

// statusCenterMessage returns the rendered center message for the status bar.
func (m Model) statusCenterMessage() string {
	text := m.statusCenterText()
	if text == "" {
		return ""
	}
	severity := m.statusMessageSeverity()
	return m.styles.StatusSeverityStyle(severity).Render(text)
}

func statusSeverityFromMessage(msg string) StatusSeverity {
	msg = strings.TrimSpace(strings.ToLower(msg))
	if msg == "" {
		return StatusInfo
	}

	errorMarkers := []string{
		"error",
		"failed",
		"cannot",
		"can't",
		"unable",
		"invalid",
		"not found",
		"denied",
		"forbidden",
		"expired",
		"corrupt",
		"locked",
	}
	for _, marker := range errorMarkers {
		if strings.Contains(msg, marker) {
			return StatusError
		}
	}

	warnMarkers := []string{
		"warning",
		"warn",
		"cancelled",
		"canceled",
		"not configured",
		"no profile",
		"no profiles",
		"no auth",
		"missing",
		"ignored",
	}
	for _, marker := range warnMarkers {
		if strings.Contains(msg, marker) {
			return StatusWarning
		}
	}

	successMarkers := []string{
		"success",
		"completed",
		"complete",
		"activated",
		"added",
		"updated",
		"deleted",
		"removed",
		"backed up",
		"refreshed",
		"exported",
		"imported",
		"saved",
		"associated",
		"synced",
		"connection test:",
	}
	for _, marker := range successMarkers {
		if strings.Contains(msg, marker) {
			return StatusSuccess
		}
	}

	return StatusInfo
}

// helpView renders the help screen with Glamour markdown rendering.
func (m Model) helpView() string {
	if m.helpRenderer == nil {
		// Fallback to plain text if renderer not initialized
		return m.styles.Help.Render(MainHelpMarkdown())
	}

	// Update renderer width for proper word wrap
	contentWidth := m.width - 8 // Account for padding
	if contentWidth < 60 {
		contentWidth = 60
	}
	m.helpRenderer.SetWidth(contentWidth)

	rendered := m.helpRenderer.Render(MainHelpMarkdown())
	return m.styles.Help.Render(rendered)
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

	// Log resolved TUI config for debugging (no sensitive data to redact)
	prefs := TUIPreferencesFromConfig(spmCfg)
	slog.Debug("resolved TUI config",
		slog.String("theme", string(prefs.Mode)),
		slog.String("contrast", string(prefs.Contrast)),
		slog.Bool("no_color", prefs.NoColor),
		slog.Bool("reduced_motion", prefs.ReducedMotion),
		slog.Bool("toasts", prefs.Toasts),
		slog.Bool("mouse", prefs.Mouse),
		slog.Bool("show_key_hints", prefs.ShowKeyHints),
		slog.String("density", prefs.Density),
		slog.Bool("no_tui", prefs.NoTUI),
	)

	m := NewWithConfig(spmCfg)

	pidPath := signals.DefaultPIDFilePath()
	pidWritten := false
	if spmCfg.Runtime.PIDFile {
		// Create PID file directly
		if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
			return fmt.Errorf("create pid dir: %w", err)
		}
		if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0600); err != nil {
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

// refreshContext holds state to preserve across profile refresh operations.
type refreshContext struct {
	provider        string // Provider being modified
	selectedProfile string // Profile name that was selected before refresh
	deletedProfile  string // Profile name that was deleted (if any)
}

// profilesRefreshedMsg is sent when profiles are reloaded after a mutation.
type profilesRefreshedMsg struct {
	profiles  map[string][]Profile
	meta      map[string]map[string]*profile.Profile
	vaultMeta map[string]map[string]vaultProfileMeta
	ctx       refreshContext
	err       error
}

// refreshProfiles returns a tea.Cmd that reloads profiles from the vault
// while preserving selection context for intelligent index restoration.
func (m Model) refreshProfiles(ctx refreshContext) tea.Cmd {
	return func() tea.Msg {
		vault := authfile.NewVault(m.vaultPath)
		profiles := make(map[string][]Profile)
		meta := make(map[string]map[string]*profile.Profile)
		vaultMeta := make(map[string]map[string]vaultProfileMeta)

		store := m.profileStore
		if store == nil {
			store = profile.NewStore(profile.DefaultStorePath())
		}

		for _, name := range m.providers {
			names, err := vault.List(name)
			if err != nil {
				return profilesRefreshedMsg{
					err: fmt.Errorf("list vault profiles for %s: %w", name, err),
					ctx: ctx,
				}
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
			meta[name] = make(map[string]*profile.Profile)
			vaultMeta[name] = make(map[string]vaultProfileMeta)
			for _, prof := range names {
				ps = append(ps, Profile{
					Name:     prof,
					Provider: name,
					IsActive: prof == active,
				})
				if store != nil {
					if loaded, err := store.Load(name, prof); err == nil && loaded != nil {
						meta[name][prof] = loaded
					}
				}
				vaultMeta[name][prof] = loadVaultProfileMeta(vault, name, prof)
			}
			profiles[name] = ps
		}

		return profilesRefreshedMsg{profiles: profiles, meta: meta, vaultMeta: vaultMeta, ctx: ctx}
	}
}

// refreshProfilesSimple returns a tea.Cmd that reloads profiles preserving
// current selection by profile name.
func (m Model) refreshProfilesSimple() tea.Cmd {
	ctx := refreshContext{
		provider: m.currentProvider(),
	}
	if name := m.selectedProfileNameValue(); name != "" {
		ctx.selectedProfile = name
	}
	return m.refreshProfiles(ctx)
}

// restoreSelection finds the appropriate selection index after a refresh.
// It tries to maintain selection on the same profile, or adjusts intelligently
// if the profile was deleted.
func (m *Model) restoreSelection(ctx refreshContext) {
	profiles := m.currentProfiles()
	if len(profiles) == 0 {
		m.selected = 0
		m.selectedProfileName = ""
		return
	}

	indexByName := func(name string) int {
		for i, p := range profiles {
			if p.Name == name {
				return i
			}
		}
		return -1
	}

	// If a profile was deleted, try to select the next one in the list
	if ctx.deletedProfile != "" {
		// Find position where deleted profile was (profiles are sorted)
		for i, p := range profiles {
			if p.Name > ctx.deletedProfile {
				// Select the profile that took its place (prefer previous if possible)
				selected := i
				if selected > 0 {
					selected--
				}
				m.selectedProfileName = profiles[selected].Name
				m.selected = selected
				return
			}
		}
		// Deleted profile was last, select new last
		m.selectedProfileName = profiles[len(profiles)-1].Name
		m.selected = len(profiles) - 1
		return
	}

	// Try to find the previously selected profile by name
	if ctx.selectedProfile != "" {
		m.selectedProfileName = ctx.selectedProfile
		if idx := indexByName(ctx.selectedProfile); idx >= 0 {
			m.selected = idx
		} else {
			m.selected = 0
		}
		return
	}

	// Fallback: keep current profile name if available, otherwise derive from index
	if m.selectedProfileName == "" {
		if m.selected >= 0 && m.selected < len(profiles) {
			m.selectedProfileName = profiles[m.selected].Name
		} else {
			m.selectedProfileName = profiles[0].Name
		}
	}
	if idx := indexByName(m.selectedProfileName); idx >= 0 {
		m.selected = idx
	} else {
		m.selected = 0
		m.selectedProfileName = profiles[0].Name
	}
}

// showError sets the status message with a consistent error format.
// It maps common error types to user-friendly messages.
func (m *Model) showError(err error, context string) {
	if err == nil {
		return
	}

	msg := err.Error()

	// Map common errors to user-friendly messages
	switch {
	case strings.Contains(msg, "no such file") || strings.Contains(msg, "does not exist"):
		msg = "Profile not found in vault"
	case strings.Contains(msg, "permission denied"):
		msg = "Cannot write to auth file - check permissions"
	case strings.Contains(msg, "invalid") || strings.Contains(msg, "corrupt"):
		msg = "Profile data corrupted - try re-backup"
	case strings.Contains(msg, "already exists"):
		msg = "Profile already exists"
	case strings.Contains(msg, "locked"):
		msg = "Profile is currently locked by another process"
	}

	if context != "" {
		m.statusMsg = fmt.Sprintf("%s: %s", context, msg)
	} else {
		m.statusMsg = msg
	}
}

// showSuccess sets the status message with a success notification.
func (m *Model) showSuccess(format string, args ...interface{}) {
	m.statusMsg = fmt.Sprintf(format, args...)
}

// showActivateSuccess shows a success message for profile activation.
func (m *Model) showActivateSuccess(provider, profile string) {
	m.showSuccess("Activated %s for %s", profile, provider)
}

// showDeleteSuccess shows a success message for profile deletion.
func (m *Model) showDeleteSuccess(profile string) {
	m.showSuccess("Deleted %s", profile)
}

// showRefreshSuccess shows a success message for token refresh.
func (m *Model) showRefreshSuccess(profile string, expiresAt time.Time) {
	if expiresAt.IsZero() {
		m.showSuccess("Refreshed %s", profile)
	} else {
		m.showSuccess("Refreshed %s - new token valid until %s", profile, expiresAt.Format("Jan 2 15:04"))
	}
}

// formatError returns a user-friendly error message.
// It maps common error types to human-readable messages.
func (m Model) formatError(err error) string {
	if err == nil {
		return ""
	}

	msg := err.Error()

	// Map common errors to user-friendly messages
	switch {
	case strings.Contains(msg, "no such file") || strings.Contains(msg, "does not exist"):
		return "Profile not found in vault"
	case strings.Contains(msg, "permission denied"):
		return "Cannot write to auth file - check permissions"
	case strings.Contains(msg, "invalid") || strings.Contains(msg, "corrupt"):
		return "Profile data corrupted - try re-backup"
	case strings.Contains(msg, "already exists"):
		return "Profile already exists"
	case strings.Contains(msg, "locked"):
		return "Profile is currently locked by another process"
	}

	return msg
}

// refreshProfilesWithIndex returns a tea.Cmd that reloads profiles and
// sets the selection to the specified index after refresh.
func (m Model) refreshProfilesWithIndex(provider string, index int) tea.Cmd {
	return func() tea.Msg {
		vault := authfile.NewVault(m.vaultPath)
		profiles := make(map[string][]Profile)

		for _, name := range m.providers {
			names, err := vault.List(name)
			if err != nil {
				return profilesRefreshedMsg{
					err: fmt.Errorf("list vault profiles for %s: %w", name, err),
					ctx: refreshContext{provider: provider},
				}
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

		// Create context that will set the selection index after refresh
		ctx := refreshContext{
			provider: provider,
		}

		// Set the selected profile name based on the index
		if providerProfiles := profiles[provider]; index >= 0 && index < len(providerProfiles) {
			ctx.selectedProfile = providerProfiles[index].Name
		}

		return profilesRefreshedMsg{profiles: profiles, ctx: ctx}
	}
}
