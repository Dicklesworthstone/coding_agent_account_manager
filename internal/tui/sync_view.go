package tui

import (
	"fmt"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/charmbracelet/lipgloss"
)

// View renders the sync panel.
func (p *SyncPanel) View() string {
	if p == nil {
		return ""
	}

	title := p.styles.Title.Render("Sync Pool")

	// Show sync status
	var statusLine string
	if p.state != nil && p.state.Pool != nil {
		if p.state.Pool.Enabled {
			statusLine = p.styles.StatusEnabled.Render("Status: Enabled")
		} else {
			statusLine = p.styles.StatusDisabled.Render("Status: Disabled")
		}
		if p.state.Pool.AutoSync {
			statusLine += "  " + p.styles.KeyHint.Render("[Auto-sync on]")
		}
	} else {
		statusLine = p.styles.StatusDisabled.Render("Status: Not configured")
	}

	if p.loading {
		var body string
		if p.loadingSpinner != nil {
			body = p.loadingSpinner.View()
		} else {
			body = p.styles.Empty.Render("Loading sync state...")
		}
		return p.render(title, statusLine, body)
	}

	if p.syncing {
		var body string
		if p.syncingSpinner != nil {
			body = p.syncingSpinner.View()
		} else {
			body = p.styles.Empty.Render("Syncing...")
		}
		return p.render(title, statusLine, body)
	}

	body := p.renderMachineList()
	return p.render(title, statusLine, body)
}

// renderMachineList renders the machine list.
func (p *SyncPanel) renderMachineList() string {
	if len(p.machines) == 0 {
		return p.styles.Empty.Render(
			"No machines configured.\n\n" +
				"Use 'caam sync add <name> <address>' to add a machine.\n" +
				"Or press [a] to add one interactively.",
		)
	}

	var rows []string
	for i, m := range p.machines {
		row := p.renderMachineRow(m, i == p.selectedIdx)
		rows = append(rows, row)
	}

	keyHints := p.styles.KeyHint.Render("\n[a]dd  [e]dit  [r]emove  [t]est  [s]ync  [esc] close")
	return strings.Join(rows, "\n") + keyHints
}

// renderMachineRow renders a single machine row.
func (p *SyncPanel) renderMachineRow(m *sync.Machine, selected bool) string {
	statusIcon := getStatusIcon(m.Status)
	style := p.styles.Machine
	if selected {
		style = p.styles.SelectedMachine
	}

	// Format: [status] name (address) - last sync time
	name := truncateString(m.Name, 20)
	addr := m.Address
	if m.Port != 0 && m.Port != sync.DefaultSSHPort {
		addr = fmt.Sprintf("%s:%d", m.Address, m.Port)
	}
	addr = truncateString(addr, 25)

	lastSync := "never"
	if !m.LastSync.IsZero() {
		lastSync = formatTimeAgo(m.LastSync)
	}

	selector := "  "
	if selected {
		selector = "> "
	}

	row := fmt.Sprintf("%s%s %-20s  %-25s  %s", selector, statusIcon, name, addr, lastSync)

	// Add error message if present
	if m.Status == sync.StatusError && m.LastError != "" {
		errMsg := truncateString(m.LastError, 40)
		row += "\n      " + p.styles.StatusError.Render(errMsg)
	}

	return style.Render(row)
}

// render renders the full panel with title, status, and body.
func (p *SyncPanel) render(title, status, body string) string {
	// Render breadcrumb for navigation context
	contentWidth := p.width - 6 // Account for border and padding
	if contentWidth < 40 {
		contentWidth = 40
	}
	breadcrumb := RenderBreadcrumb("Sync", p.theme, contentWidth)

	inner := lipgloss.JoinVertical(lipgloss.Left, breadcrumb, title, status, "", body)
	if p.width > 0 {
		return p.styles.Border.Width(p.width - 2).Height(p.height - 2).Render(inner)
	}
	return p.styles.Border.Render(inner)
}
