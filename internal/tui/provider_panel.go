package tui

import (
	"fmt"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// ProviderPanel renders the left panel showing providers with profile counts.
type ProviderPanel struct {
	providers      []string
	activeProvider int
	profileCounts  map[string]int
	width          int
	height         int
	styles         ProviderPanelStyles
}

// ProviderPanelStyles holds the styles for the provider panel.
type ProviderPanelStyles struct {
	Border       lipgloss.Style
	Title        lipgloss.Style
	Item         lipgloss.Style
	SelectedItem lipgloss.Style
	Count        lipgloss.Style
}

// DefaultProviderPanelStyles returns the default styles for the provider panel.
func DefaultProviderPanelStyles() ProviderPanelStyles {
	return ProviderPanelStyles{
		Border: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDarkGray).
			Padding(0, 1),

		Title: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPurple).
			MarginBottom(1),

		Item: lipgloss.NewStyle().
			Foreground(colorGray).
			PaddingLeft(1),

		SelectedItem: lipgloss.NewStyle().
			Foreground(colorWhite).
			Bold(true).
			Background(colorDarkGray).
			PaddingLeft(1),

		Count: lipgloss.NewStyle().
			Foreground(colorGray).
			Italic(true),
	}
}

// NewProviderPanel creates a new provider panel.
func NewProviderPanel(providers []string) *ProviderPanel {
	return &ProviderPanel{
		providers:     providers,
		profileCounts: make(map[string]int),
		styles:        DefaultProviderPanelStyles(),
	}
}

// SetActiveProvider sets the currently selected provider index.
func (p *ProviderPanel) SetActiveProvider(index int) {
	if index >= 0 && index < len(p.providers) {
		p.activeProvider = index
	}
}

// SetProfileCounts updates the profile counts for each provider.
func (p *ProviderPanel) SetProfileCounts(counts map[string]int) {
	p.profileCounts = counts
}

// SetSize sets the panel dimensions.
func (p *ProviderPanel) SetSize(width, height int) {
	p.width = width
	p.height = height
}

// View renders the provider panel.
func (p *ProviderPanel) View() string {
	// Title
	title := p.styles.Title.Render("Providers")

	// Provider list
	var items []string
	for i, name := range p.providers {
		count := p.profileCounts[name]
		countStr := p.styles.Count.Render(fmt.Sprintf("(%d)", count))

		// Indicator for selected
		indicator := "  "
		style := p.styles.Item
		if i == p.activeProvider {
			indicator = lipgloss.NewStyle().Foreground(colorGreen).Render("▶ ")
			style = p.styles.SelectedItem
		}

		// Capitalize first letter for display
		displayName := capitalizeFirst(name)
		item := fmt.Sprintf("%s%s %s", indicator, displayName, countStr)
		items = append(items, style.Render(item))
	}

	content := lipgloss.JoinVertical(lipgloss.Left, items...)

	// Combine title and content
	inner := lipgloss.JoinVertical(lipgloss.Left, title, content)

	// Apply border
	if p.width > 0 {
		return p.styles.Border.Width(p.width - 2).Render(inner)
	}
	return p.styles.Border.Render(inner)
}

// capitalizeFirst capitalizes the first letter of a string.
// Uses Unicode-aware rune handling.
func capitalizeFirst(s string) string {
	if s == "" {
		return s
	}
	r, size := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[size:]
}
