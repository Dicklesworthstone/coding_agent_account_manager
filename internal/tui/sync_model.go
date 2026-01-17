package tui

import (
	"fmt"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/charmbracelet/lipgloss"
)

// SyncPanel manages the sync view state and machine list display.
type SyncPanel struct {
	visible bool
	loading bool
	syncing bool

	// State
	state    *sync.SyncState
	machines []*sync.Machine

	// Selection
	selectedIdx int

	// Panel dimensions
	width  int
	height int

	// Styles
	styles SyncPanelStyles
}

// MachineInfo is a display-friendly representation of a machine.
type MachineInfo struct {
	ID         string
	Name       string
	Address    string
	Port       int
	Status     string
	LastSync   string
	LastError  string
	IsLocal    bool
	StatusIcon string
}

// SyncPanelStyles contains styles for the sync panel.
type SyncPanelStyles struct {
	Title           lipgloss.Style
	StatusEnabled   lipgloss.Style
	StatusDisabled  lipgloss.Style
	Machine         lipgloss.Style
	SelectedMachine lipgloss.Style
	StatusOnline    lipgloss.Style
	StatusOffline   lipgloss.Style
	StatusSyncing   lipgloss.Style
	StatusError     lipgloss.Style
	KeyHint         lipgloss.Style
	Border          lipgloss.Style
	Empty           lipgloss.Style
}

// DefaultSyncPanelStyles returns the default styles for the sync panel.
func DefaultSyncPanelStyles() SyncPanelStyles {
	return NewSyncPanelStyles(DefaultTheme())
}

// NewSyncPanelStyles returns themed styles for the sync panel.
func NewSyncPanelStyles(theme Theme) SyncPanelStyles {
	p := theme.Palette

	return SyncPanelStyles{
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent),
		StatusEnabled: lipgloss.NewStyle().
			Foreground(p.Success).
			Bold(true),
		StatusDisabled: lipgloss.NewStyle().
			Foreground(p.Danger),
		Machine: lipgloss.NewStyle().
			Foreground(p.Text),
		SelectedMachine: lipgloss.NewStyle().
			Foreground(p.Info).
			Bold(true),
		StatusOnline: lipgloss.NewStyle().
			Foreground(p.Success),
		StatusOffline: lipgloss.NewStyle().
			Foreground(p.Muted),
		StatusSyncing: lipgloss.NewStyle().
			Foreground(p.Warning),
		StatusError: lipgloss.NewStyle().
			Foreground(p.Danger),
		KeyHint: lipgloss.NewStyle().
			Foreground(p.Muted),
		Border: lipgloss.NewStyle().
			Border(theme.Border).
			BorderForeground(p.BorderMuted).
			Background(p.Surface).
			Padding(1, 2),
		Empty: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true),
	}
}

// NewSyncPanel creates a new sync panel.
func NewSyncPanel() *SyncPanel {
	return NewSyncPanelWithTheme(DefaultTheme())
}

// NewSyncPanelWithTheme creates a new sync panel using a theme.
func NewSyncPanelWithTheme(theme Theme) *SyncPanel {
	return &SyncPanel{
		visible:     false,
		selectedIdx: 0,
		styles:      NewSyncPanelStyles(theme),
	}
}

// Toggle toggles the visibility of the sync panel.
func (p *SyncPanel) Toggle() {
	if p == nil {
		return
	}
	p.visible = !p.visible
}

// Visible returns whether the sync panel is visible.
func (p *SyncPanel) Visible() bool {
	if p == nil {
		return false
	}
	return p.visible
}

// SetSize sets the panel dimensions.
func (p *SyncPanel) SetSize(width, height int) {
	if p == nil {
		return
	}
	p.width = width
	p.height = height
}

// SetLoading sets the loading state.
func (p *SyncPanel) SetLoading(loading bool) {
	if p == nil {
		return
	}
	p.loading = loading
}

// SetSyncing sets the syncing state.
func (p *SyncPanel) SetSyncing(syncing bool) {
	if p == nil {
		return
	}
	p.syncing = syncing
}

// SetState sets the sync state and updates the machine list.
func (p *SyncPanel) SetState(state *sync.SyncState) {
	if p == nil {
		return
	}
	p.state = state
	p.loading = false
	if state != nil && state.Pool != nil {
		p.machines = state.Pool.ListMachines()
	} else {
		p.machines = nil
	}
	// Clamp selected index
	if p.selectedIdx >= len(p.machines) {
		p.selectedIdx = len(p.machines) - 1
	}
	if p.selectedIdx < 0 {
		p.selectedIdx = 0
	}
}

// State returns the current sync state.
func (p *SyncPanel) State() *sync.SyncState {
	if p == nil {
		return nil
	}
	return p.state
}

// SelectedMachine returns the currently selected machine, or nil if none.
func (p *SyncPanel) SelectedMachine() *sync.Machine {
	if p == nil || len(p.machines) == 0 || p.selectedIdx < 0 || p.selectedIdx >= len(p.machines) {
		return nil
	}
	return p.machines[p.selectedIdx]
}

// MoveUp moves the selection up.
func (p *SyncPanel) MoveUp() {
	if p == nil || len(p.machines) == 0 {
		return
	}
	if p.selectedIdx > 0 {
		p.selectedIdx--
	}
}

// MoveDown moves the selection down.
func (p *SyncPanel) MoveDown() {
	if p == nil || len(p.machines) == 0 {
		return
	}
	if p.selectedIdx < len(p.machines)-1 {
		p.selectedIdx++
	}
}

// ToMachineInfo converts a sync.Machine to a MachineInfo for display.
func ToMachineInfo(m *sync.Machine) MachineInfo {
	if m == nil {
		return MachineInfo{}
	}
	return MachineInfo{
		ID:         m.ID,
		Name:       m.Name,
		Address:    m.Address,
		Port:       m.Port,
		Status:     m.Status,
		LastSync:   formatTimeAgo(m.LastSync),
		LastError:  m.LastError,
		StatusIcon: getStatusIcon(m.Status),
	}
}

// getStatusIcon returns an emoji icon for a machine status.
func getStatusIcon(status string) string {
	switch status {
	case sync.StatusOnline:
		return "🟢"
	case sync.StatusOffline:
		return "🔴"
	case sync.StatusSyncing:
		return "🔄"
	case sync.StatusError:
		return "⚠️"
	default:
		return "⚪"
	}
}

// formatTimeAgo formats a time as a relative "X ago" string.
func formatTimeAgo(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	d := time.Since(t)

	if d < time.Minute {
		return "just now"
	}
	if d < 2*time.Minute {
		return "1 min ago"
	}
	if d < time.Hour {
		return fmt.Sprintf("%d mins ago", int(d.Minutes()))
	}
	if d < 2*time.Hour {
		return "1 hour ago"
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	if d < 48*time.Hour {
		return "1 day ago"
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// truncateString truncates a string to maxLen with ellipsis.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-3] + "..."
}
