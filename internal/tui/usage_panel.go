package tui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

type UsagePanel struct {
	visible   bool
	timeRange int // 1=24h, 7=week, 30=month, 0=all
	loading   bool

	stats []ProfileUsage

	width  int
	height int

	styles UsagePanelStyles
}

type ProfileUsage struct {
	Provider     string
	ProfileName  string
	SessionCount int
	TotalHours   float64
	Percentage   float64
}

type UsagePanelStyles struct {
	Border  lipgloss.Style
	Title   lipgloss.Style
	Row     lipgloss.Style
	BarFill lipgloss.Style
	Empty   lipgloss.Style
	Footer  lipgloss.Style
}

func DefaultUsagePanelStyles() UsagePanelStyles {
	return NewUsagePanelStyles(DefaultTheme())
}

// NewUsagePanelStyles returns themed styles for the usage panel.
func NewUsagePanelStyles(theme Theme) UsagePanelStyles {
	p := theme.Palette

	return UsagePanelStyles{
		Border: lipgloss.NewStyle().
			Border(theme.Border).
			BorderForeground(p.BorderMuted).
			Background(p.Surface).
			Padding(1, 2),
		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent),
		Row: lipgloss.NewStyle().
			Foreground(p.Text),
		BarFill: lipgloss.NewStyle().
			Foreground(p.Info),
		Empty: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true),
		Footer: lipgloss.NewStyle().
			Foreground(p.Muted),
	}
}

func NewUsagePanel() *UsagePanel {
	return NewUsagePanelWithTheme(DefaultTheme())
}

// NewUsagePanelWithTheme creates a new usage panel using a theme.
func NewUsagePanelWithTheme(theme Theme) *UsagePanel {
	return &UsagePanel{
		timeRange: 7,
		styles:    NewUsagePanelStyles(theme),
	}
}

func (u *UsagePanel) Toggle() {
	if u == nil {
		return
	}
	u.visible = !u.visible
}

func (u *UsagePanel) Visible() bool {
	if u == nil {
		return false
	}
	return u.visible
}

func (u *UsagePanel) SetTimeRange(days int) {
	if u == nil {
		return
	}
	u.timeRange = days
}

func (u *UsagePanel) TimeRange() int {
	if u == nil {
		return 7
	}
	return u.timeRange
}

func (u *UsagePanel) SetLoading(loading bool) {
	if u == nil {
		return
	}
	u.loading = loading
}

func (u *UsagePanel) SetSize(width, height int) {
	if u == nil {
		return
	}
	u.width = width
	u.height = height
}

func (u *UsagePanel) SetStats(stats []ProfileUsage) {
	if u == nil {
		return
	}

	u.loading = false

	copied := make([]ProfileUsage, len(stats))
	copy(copied, stats)
	sort.Slice(copied, func(i, j int) bool {
		if copied[i].TotalHours == copied[j].TotalHours {
			if copied[i].SessionCount == copied[j].SessionCount {
				return copied[i].Provider+"/"+copied[i].ProfileName < copied[j].Provider+"/"+copied[j].ProfileName
			}
			return copied[i].SessionCount > copied[j].SessionCount
		}
		return copied[i].TotalHours > copied[j].TotalHours
	})

	maxHours := 0.0
	for _, s := range copied {
		if s.TotalHours > maxHours {
			maxHours = s.TotalHours
		}
	}

	for i := range copied {
		if maxHours > 0 {
			copied[i].Percentage = copied[i].TotalHours / maxHours
		}
	}

	u.stats = copied
}

func (u *UsagePanel) View() string {
	if u == nil {
		return ""
	}

	title := u.styles.Title.Render("Usage Statistics")
	timeRange := u.timeRangeLabel()

	if u.loading {
		body := u.styles.Empty.Render("Loading usage stats…")
		return u.render(title, timeRange, body)
	}

	if len(u.stats) == 0 {
		body := u.styles.Empty.Render("No usage data found.\n\nTip: usage data is recorded when caam logs activation/deactivation events.")
		return u.render(title, timeRange, body)
	}

	barWidth := 20
	if u.width > 0 {
		if w := (u.width / 4); w >= 10 && w <= 30 {
			barWidth = w
		}
	}

	var totalSessions int
	var totalHours float64

	var rows []string
	for _, s := range u.stats {
		totalSessions += s.SessionCount
		totalHours += s.TotalHours

		label := fmt.Sprintf("%s/%s", s.Provider, s.ProfileName)
		bar := u.renderBar(s.Percentage, barWidth)
		rows = append(rows, u.styles.Row.Render(fmt.Sprintf("%-22s  %s  %3d sess  %5.1fh", label, bar, s.SessionCount, s.TotalHours)))
	}

	footer := u.styles.Footer.Render(fmt.Sprintf("\nTotal: %d sessions, %.1f hours\n\nPress [u] to toggle, [1-4] for time range, [esc] to close", totalSessions, totalHours))
	body := strings.Join(rows, "\n") + footer

	return u.render(title, timeRange, body)
}

func (u *UsagePanel) render(title, timeRange, body string) string {
	inner := lipgloss.JoinVertical(lipgloss.Left, title, timeRange, "", body)
	if u.width > 0 {
		return u.styles.Border.Width(u.width - 2).Height(u.height - 2).Render(inner)
	}
	return u.styles.Border.Render(inner)
}

func (u *UsagePanel) timeRangeLabel() string {
	switch u.timeRange {
	case 1:
		return "Last 24 hours"
	case 7:
		return "Last 7 days"
	case 30:
		return "Last 30 days"
	case 0:
		return "All time"
	default:
		return fmt.Sprintf("Last %d days", u.timeRange)
	}
}

func (u *UsagePanel) renderBar(percentage float64, width int) string {
	if width <= 0 {
		return ""
	}
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 1 {
		percentage = 1
	}

	filled := int(percentage * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	return u.styles.BarFill.Render(strings.Repeat("█", filled)) + strings.Repeat(" ", width-filled)
}
