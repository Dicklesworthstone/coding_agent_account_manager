// Package tui provides the terminal user interface for caam.
package tui

import (
	"fmt"

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
	keys           keyMap
	styles         Styles
	providerPanel  *ProviderPanel
	profilesPanel  *ProfilesPanel
	detailPanel    *DetailPanel

	// Status message
	statusMsg string

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
	profilesPanel := NewProfilesPanel()
	if len(providers) > 0 {
		profilesPanel.SetProvider(providers[0])
	}
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
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		tea.EnterAltScreen,
		m.loadProfiles,
	)
}

// loadProfiles loads profiles for all providers.
func (m Model) loadProfiles() tea.Msg {
	profiles := make(map[string][]Profile)

	for _, name := range m.providers {
		// TODO: Load actual profiles from vault
		// For now, return empty list
		profiles[name] = []Profile{}
	}

	return profilesLoadedMsg{profiles: profiles}
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
		return m, nil
	}

	return m, nil
}

// handleKeyPress processes keyboard input.
func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle state-specific key handling
	switch m.state {
	case stateConfirm:
		return m.handleConfirmKeys(msg)
	case stateHelp:
		// Any key returns to list
		m.state = stateList
		return m, nil
	}

	// Normal list view key handling
	switch {
	case key.Matches(msg, m.keys.Quit):
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
	infos := make([]ProfileInfo, len(profiles))
	for i, p := range profiles {
		infos[i] = ProfileInfo{
			Name:     p.Name,
			AuthMode: "oauth", // Default, TODO: get from actual profile
			LoggedIn: true,    // TODO: get actual status
			Locked:   false,   // TODO: get actual lock status
			IsActive: p.IsActive,
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
		Name:     prof.Name,
		Provider: m.currentProvider(),
		AuthMode: "oauth",  // TODO: get from actual profile
		LoggedIn: true,     // TODO: get actual status
		Locked:   false,    // TODO: get actual lock status
		Path:     "",       // TODO: get from actual profile
		Account:  "",       // TODO: get from actual profile
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
	default:
		return m.mainView()
	}
}

// mainView renders the main list view.
func (m Model) mainView() string {
	// Header
	header := m.styles.Header.Render("caam - Coding Agent Account Manager")

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
  →/l     Next provider
  tab     Cycle providers

Profile Actions
  enter   Activate selected profile
  l       Login/refresh auth
  o       Open account page in browser
  d       Delete profile (with confirmation)

Other Actions
  b       Backup current auth state

General
  ?       Toggle help
  q/esc   Quit

Press any key to return...
`
	return m.styles.Help.Render(help)
}

// Run starts the TUI application.
func Run() error {
	p := tea.NewProgram(New(), tea.WithAltScreen())
	_, err := p.Run()
	return err
}
