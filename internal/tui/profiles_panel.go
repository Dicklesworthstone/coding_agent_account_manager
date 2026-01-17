package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/charmbracelet/lipgloss"
)

// ProfileInfo represents a profile with all displayable information.
type ProfileInfo struct {
	Name           string
	Badge          string
	ProjectDefault bool
	AuthMode       string
	LoggedIn       bool
	Locked         bool
	LastUsed       time.Time
	Account        string
	Description    string // Free-form notes about this profile's purpose
	IsActive       bool
	HealthStatus   health.HealthStatus
	TokenExpiry    time.Time
	ErrorCount     int
	Penalty        float64
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
	Border          lipgloss.Style
	Title           lipgloss.Style
	Header          lipgloss.Style
	Row             lipgloss.Style
	SelectedRow     lipgloss.Style
	ActiveIndicator lipgloss.Style
	StatusOK        lipgloss.Style
	StatusWarn      lipgloss.Style
	StatusBad       lipgloss.Style
	StatusMuted     lipgloss.Style
	LockIcon        lipgloss.Style
	ProjectBadge    lipgloss.Style
	Empty           lipgloss.Style
}

// DefaultProfilesPanelStyles returns the default styles for the profiles panel.
func DefaultProfilesPanelStyles() ProfilesPanelStyles {
	return NewProfilesPanelStyles(DefaultTheme())
}

// NewProfilesPanelStyles returns themed styles for the profiles panel.
func NewProfilesPanelStyles(theme Theme) ProfilesPanelStyles {
	p := theme.Palette

	return ProfilesPanelStyles{
		Border: lipgloss.NewStyle().
			Border(theme.Border).
			BorderForeground(p.BorderMuted).
			Background(p.Surface).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			MarginBottom(1),

		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Muted).
			BorderStyle(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderForeground(p.BorderMuted),

		Row: lipgloss.NewStyle().
			Foreground(p.Text),

		SelectedRow: lipgloss.NewStyle().
			Foreground(p.Text).
			Bold(true).
			Background(p.Selection),

		ActiveIndicator: lipgloss.NewStyle().
			Foreground(p.Success).
			Bold(true),

		StatusOK: lipgloss.NewStyle().
			Foreground(p.Success),

		StatusWarn: lipgloss.NewStyle().
			Foreground(p.Warning),

		StatusBad: lipgloss.NewStyle().
			Foreground(p.Danger),

		StatusMuted: lipgloss.NewStyle().
			Foreground(p.Muted),

		LockIcon: lipgloss.NewStyle().
			Foreground(p.Warning),

		ProjectBadge: lipgloss.NewStyle().
			Foreground(p.Info).
			Bold(true),

		Empty: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true).
			Padding(2, 2),
	}
}

// StatusStyle returns the style for a given health status.
func (s ProfilesPanelStyles) StatusStyle(status health.HealthStatus) lipgloss.Style {
	switch status {
	case health.StatusHealthy:
		return s.StatusOK
	case health.StatusWarning:
		return s.StatusWarn
	case health.StatusCritical:
		return s.StatusBad
	default:
		return s.StatusMuted
	}
}

// formatTUIStatus formats the health status string.
func formatTUIStatus(pi *ProfileInfo) string {
	icon := pi.HealthStatus.Icon()

	if pi.TokenExpiry.IsZero() {
		return icon + " " + formatStatusLabel(pi.HealthStatus)
	}

	ttl := time.Until(pi.TokenExpiry)
	if ttl <= 0 {
		return icon + " Expired"
	}

	return icon + " " + formatDuration(ttl)
}

func formatStatusLabel(status health.HealthStatus) string {
	label := status.String()
	if label == "" {
		return "Unknown"
	}
	return strings.ToUpper(label[:1]) + label[1:]
}

// formatDuration formats a duration concisely for TUI.
func formatDuration(d time.Duration) string {
	if d < time.Hour {
		return fmt.Sprintf("%dm left", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh left", int(d.Hours()))
	}
	return fmt.Sprintf("%dd left", int(d.Hours()/24))
}

// NewProfilesPanel creates a new profiles panel.
func NewProfilesPanel() *ProfilesPanel {
	return NewProfilesPanelWithTheme(DefaultTheme())
}

// NewProfilesPanelWithTheme creates a new profiles panel using a theme.
func NewProfilesPanelWithTheme(theme Theme) *ProfilesPanel {
	return &ProfilesPanel{
		profiles: []ProfileInfo{},
		styles:   NewProfilesPanelStyles(theme),
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

// SetSelectedByName sets the selected profile by name.
// Returns true if the profile was found.
func (p *ProfilesPanel) SetSelectedByName(name string) bool {
	for i := range p.profiles {
		if p.profiles[i].Name == name {
			p.selected = i
			return true
		}
	}
	return false
}

// Count returns the number of profiles in the panel.
func (p *ProfilesPanel) Count() int {
	return len(p.profiles)
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

	availableWidth := p.width
	if availableWidth > 0 {
		availableWidth = availableWidth - 4
	}

	layout := "full"
	if availableWidth > 0 {
		switch {
		case availableWidth < 56:
			layout = "narrow"
		case availableWidth < 80:
			layout = "compact"
		}
	}

	colWidths := struct {
		name     int
		auth     int
		status   int
		lastUsed int
		account  int
	}{
		name:     18,
		auth:     8,
		status:   14,
		lastUsed: 12,
		account:  16,
	}

	switch layout {
	case "compact":
		colWidths.name = 22
		colWidths.status = 14
		colWidths.lastUsed = 12
		colWidths.auth = 0
		colWidths.account = 0
	case "narrow":
		colWidths.name = 26
		colWidths.status = 12
		colWidths.lastUsed = 0
		colWidths.auth = 0
		colWidths.account = 0
	}

	columnCount := 2
	if layout == "full" {
		columnCount = 5
	} else if layout == "compact" {
		columnCount = 3
	}

	sumWidths := colWidths.name + colWidths.status
	if layout == "full" {
		sumWidths += colWidths.auth + colWidths.lastUsed + colWidths.account
	} else if layout == "compact" {
		sumWidths += colWidths.lastUsed
	}
	sumWidths += columnCount - 1
	if availableWidth > 0 && sumWidths > availableWidth {
		reduce := sumWidths - availableWidth
		minName := 12
		if layout == "full" {
			minName = 10
		}
		if colWidths.name-reduce < minName {
			colWidths.name = minName
		} else {
			colWidths.name -= reduce
		}
	}

	// Header row
	headerCells := []string{padRight("Name", colWidths.name)}
	if layout == "full" {
		headerCells = append(headerCells, padRight("Auth", colWidths.auth))
	}
	headerCells = append(headerCells, padRight("Status", colWidths.status))
	if layout != "narrow" {
		headerCells = append(headerCells, padRight("Last Used", colWidths.lastUsed))
	}
	if layout == "full" {
		headerCells = append(headerCells, padRight("Account", colWidths.account))
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

		// Status display
		statusText := formatTUIStatus(&prof)
		if prof.Locked && layout == "full" {
			statusText += " " + p.styles.LockIcon.Render("🔒")
		}
		statusStyle := p.styles.StatusStyle(prof.HealthStatus)

		// Last used - relative time
		lastUsed := formatRelativeTime(prof.LastUsed)

		// Account (truncate if needed)
		account := prof.Account
		if account == "" {
			account = "-"
		}
		if len(account) > colWidths.account && colWidths.account > 3 {
			account = account[:colWidths.account-3] + "..."
		} else if len(account) > colWidths.account {
			account = account[:colWidths.account]
		}

		// Build row cells with proper padding
		paddedName := padRight(formatNameWithBadge(prof.Name, prof.Badge, colWidths.name-2), colWidths.name-2)
		paddedStatusText := padRight(statusText, colWidths.status)
		renderedStatus := statusStyle.Render(paddedStatusText)

		rowParts := []string{indicator + paddedName}
		if layout == "full" {
			rowParts = append(rowParts, padRight(prof.AuthMode, colWidths.auth))
		}
		rowParts = append(rowParts, renderedStatus)
		if layout != "narrow" {
			rowParts = append(rowParts, padRight(lastUsed, colWidths.lastUsed))
		}
		if layout == "full" {
			rowParts = append(rowParts, padRight(account, colWidths.account))
		}

		rowStr := strings.Join(rowParts, " ")
		if prof.ProjectDefault && layout == "full" {
			rowStr += " " + p.styles.ProjectBadge.Render("[PROJECT DEFAULT]")
		}

		// Apply row style
		style := p.styles.Row
		if i == p.selected {
			style = p.styles.SelectedRow
		}
		rows = append(rows, style.Render(rowStr))
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
// Uses lipgloss.Width for proper visual width handling (emojis, CJK).
func padRight(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
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

func formatNameWithBadge(name, badge string, width int) string {
	if badge == "" {
		return truncate(name, width)
	}
	if width <= 0 {
		return ""
	}

	badgeRunes := utf8.RuneCountInString(badge)
	if badgeRunes >= width {
		return truncate(badge, width)
	}

	nameWidth := width - 1 - badgeRunes
	if nameWidth < 0 {
		nameWidth = 0
	}

	return truncate(name, nameWidth) + " " + badge
}
