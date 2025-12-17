package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ProfileInfo represents a profile with all displayable information.
type ProfileInfo struct {
	Name      string
	AuthMode  string
	LoggedIn  bool
	Locked    bool
	LastUsed  time.Time
	Account   string
	IsActive  bool
}

// ProfilesPanel renders the center panel showing profiles for the selected provider.
type ProfilesPanel struct {
	provider string
	profiles []ProfileInfo
	selected int
	width    int
	height   int
	styles   ProfilesPanelStyles
}

// ProfilesPanelStyles holds the styles for the profiles panel.
type ProfilesPanelStyles struct {
	Border         lipgloss.Style
	Title          lipgloss.Style
	Header         lipgloss.Style
	Row            lipgloss.Style
	SelectedRow    lipgloss.Style
	ActiveIndicator lipgloss.Style
	StatusOK       lipgloss.Style
	StatusBad      lipgloss.Style
	LockIcon       lipgloss.Style
	Empty          lipgloss.Style
}

// DefaultProfilesPanelStyles returns the default styles for the profiles panel.
func DefaultProfilesPanelStyles() ProfilesPanelStyles {
	return ProfilesPanelStyles{
		Border: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDarkGray).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPurple).
			MarginBottom(1),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorGray).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(colorDarkGray),

		Row: lipgloss.NewStyle().
			Foreground(colorWhite),

		SelectedRow: lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true).
			Background(colorDarkGray),

		ActiveIndicator: lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true),

		StatusOK: lipgloss.NewStyle().
			Foreground(colorGreen),

		StatusBad: lipgloss.NewStyle().
			Foreground(colorRed),

		LockIcon: lipgloss.NewStyle().
			Foreground(colorYellow),

		Empty: lipgloss.NewStyle().
			Foreground(colorGray).
			Italic(true).
			Padding(2, 2),
	}
}

// NewProfilesPanel creates a new profiles panel.
func NewProfilesPanel() *ProfilesPanel {
	return &ProfilesPanel{
		profiles: []ProfileInfo{},
		styles:   DefaultProfilesPanelStyles(),
	}
}

// SetProvider sets the currently displayed provider.
func (p *ProfilesPanel) SetProvider(provider string) {
	p.provider = provider
}

// SetProfiles sets the profiles to display, sorted by last used.
func (p *ProfilesPanel) SetProfiles(profiles []ProfileInfo) {
	// Sort by last used (most recent first), then by name
	sorted := make([]ProfileInfo, len(profiles))
	copy(sorted, profiles)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].LastUsed.Equal(sorted[j].LastUsed) {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].LastUsed.After(sorted[j].LastUsed)
	})
	p.profiles = sorted

	// Reset selection if out of bounds
	if p.selected >= len(p.profiles) {
		p.selected = max(0, len(p.profiles)-1)
	}
}

// SetSelected sets the currently selected profile index.
func (p *ProfilesPanel) SetSelected(index int) {
	if index >= 0 && index < len(p.profiles) {
		p.selected = index
	}
}

// GetSelected returns the currently selected profile index.
func (p *ProfilesPanel) GetSelected() int {
	return p.selected
}

// GetSelectedProfile returns the currently selected profile, or nil if none.
func (p *ProfilesPanel) GetSelectedProfile() *ProfileInfo {
	if p.selected >= 0 && p.selected < len(p.profiles) {
		return &p.profiles[p.selected]
	}
	return nil
}

// MoveUp moves selection up.
func (p *ProfilesPanel) MoveUp() {
	if p.selected > 0 {
		p.selected--
	}
}

// MoveDown moves selection down.
func (p *ProfilesPanel) MoveDown() {
	if p.selected < len(p.profiles)-1 {
		p.selected++
	}
}

// SetSize sets the panel dimensions.
func (p *ProfilesPanel) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// View renders the profiles panel.
func (p *ProfilesPanel) View() string {
	// Title
	title := p.styles.Title.Render(capitalizeFirst(p.provider) + " Profiles")

	if len(p.profiles) == 0 {
		empty := p.styles.Empty.Render(
			fmt.Sprintf("No profiles saved for %s\n\nUse 'caam backup %s <email>' to save a profile",
				p.provider, p.provider))
		inner := lipgloss.JoinVertical(lipgloss.Left, title, empty)
		if p.width > 0 {
			return p.styles.Border.Width(p.width - 2).Render(inner)
		}
		return p.styles.Border.Render(inner)
	}

	// Define column widths
	colWidths := struct {
		name     int
		auth     int
		status   int
		lastUsed int
		account  int
	}{
		name:     16,
		auth:     8,
		status:   10,
		lastUsed: 12,
		account:  16,
	}

	// Header row
	headerCells := []string{
		padRight("Name", colWidths.name),
		padRight("Auth", colWidths.auth),
		padRight("Status", colWidths.status),
		padRight("Last Used", colWidths.lastUsed),
		padRight("Account", colWidths.account),
	}
	header := p.styles.Header.Render(strings.Join(headerCells, " "))

	// Profile rows
	var rows []string
	for i, prof := range p.profiles {
		// Indicator for selected and active
		indicator := "  "
		if prof.IsActive {
			indicator = p.styles.ActiveIndicator.Render("● ")
		}

		// Status icons
		var statusStr string
		if prof.LoggedIn {
			statusStr = p.styles.StatusOK.Render("✓")
		} else {
			statusStr = p.styles.StatusBad.Render("✗")
		}
		if prof.Locked {
			statusStr += " " + p.styles.LockIcon.Render("🔒")
		}

		// Last used - relative time
		lastUsed := formatRelativeTime(prof.LastUsed)

		// Account (truncate if needed)
		account := prof.Account
		if account == "" {
			account = "-"
		}
		if len(account) > colWidths.account {
			account = account[:colWidths.account-3] + "..."
		}

		// Build row
		cells := []string{
			indicator + padRight(truncate(prof.Name, colWidths.name-2), colWidths.name-2),
			padRight(prof.AuthMode, colWidths.auth),
			padRight(statusStr, colWidths.status),
			padRight(lastUsed, colWidths.lastUsed),
			padRight(account, colWidths.account),
		}
		row := strings.Join(cells, " ")

		// Apply row style
		style := p.styles.Row
		if i == p.selected {
			style = p.styles.SelectedRow
		}
		rows = append(rows, style.Render(row))
	}

	// Combine header and rows
	content := lipgloss.JoinVertical(lipgloss.Left, append([]string{header}, rows...)...)

	// Combine title and content
	inner := lipgloss.JoinVertical(lipgloss.Left, title, content)

	// Apply border
	if p.width > 0 {
		return p.styles.Border.Width(p.width - 2).Render(inner)
	}
	return p.styles.Border.Render(inner)
}

// formatRelativeTime formats a time as a relative string (e.g., "2h ago", "1d ago").
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "never"
	}

	duration := time.Since(t)

	switch {
	case duration < time.Minute:
		return "now"
	case duration < time.Hour:
		mins := int(duration.Minutes())
		return fmt.Sprintf("%dm ago", mins)
	case duration < 24*time.Hour:
		hours := int(duration.Hours())
		return fmt.Sprintf("%dh ago", hours)
	case duration < 7*24*time.Hour:
		days := int(duration.Hours() / 24)
		return fmt.Sprintf("%dd ago", days)
	case duration < 30*24*time.Hour:
		weeks := int(duration.Hours() / (24 * 7))
		return fmt.Sprintf("%dw ago", weeks)
	default:
		months := int(duration.Hours() / (24 * 30))
		if months == 0 {
			months = 1
		}
		return fmt.Sprintf("%dmo ago", months)
	}
}

// padRight pads a string to the right with spaces.
// Uses rune count for proper Unicode handling.
func padRight(s string, width int) string {
	runeCount := utf8.RuneCountInString(s)
	if runeCount >= width {
		return s
	}
	return s + strings.Repeat(" ", width-runeCount)
}

// truncate truncates a string to the given width in runes.
// Uses rune handling for proper Unicode support.
func truncate(s string, width int) string {
	runes := []rune(s)
	if len(runes) <= width {
		return s
	}
	if width <= 3 {
		return string(runes[:width])
	}
	return string(runes[:width-3]) + "..."
}
