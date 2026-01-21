package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Breadcrumb represents a navigation breadcrumb for sub-views.
type Breadcrumb struct {
	path   []string
	styles BreadcrumbStyles
}

// BreadcrumbStyles contains styles for breadcrumb rendering.
type BreadcrumbStyles struct {
	Container lipgloss.Style
	Separator lipgloss.Style
	Home      lipgloss.Style
	Current   lipgloss.Style
	BackHint  lipgloss.Style
}

// NewBreadcrumbStyles creates themed breadcrumb styles.
func NewBreadcrumbStyles(theme Theme) BreadcrumbStyles {
	p := theme.Palette

	return BreadcrumbStyles{
		Container: lipgloss.NewStyle().
			MarginBottom(1),
		Separator: lipgloss.NewStyle().
			Foreground(p.Muted).
			SetString(" > "),
		Home: lipgloss.NewStyle().
			Foreground(p.Muted),
		Current: lipgloss.NewStyle().
			Foreground(p.Accent).
			Bold(true),
		BackHint: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true),
	}
}

// NewBreadcrumb creates a new breadcrumb with the given path.
// The path should be from root to current location, e.g., ["Home", "Usage"].
func NewBreadcrumb(path []string, theme Theme) *Breadcrumb {
	return &Breadcrumb{
		path:   path,
		styles: NewBreadcrumbStyles(theme),
	}
}

// View renders the breadcrumb bar.
// Output example: "Home > Usage                              [Esc] Back"
func (b *Breadcrumb) View(width int) string {
	if b == nil || len(b.path) == 0 {
		return ""
	}

	// Build the path portion
	var pathParts []string
	for i, part := range b.path {
		if i == len(b.path)-1 {
			// Current (last) item - highlighted
			pathParts = append(pathParts, b.styles.Current.Render(part))
		} else {
			// Ancestor items - muted
			pathParts = append(pathParts, b.styles.Home.Render(part))
		}
	}
	pathStr := strings.Join(pathParts, b.styles.Separator.String())

	// Back hint
	backHint := b.styles.BackHint.Render("[Esc] Back")

	// Calculate spacing
	pathLen := lipgloss.Width(pathStr)
	backLen := lipgloss.Width(backHint)
	minSpacing := 2

	if width > 0 && width > pathLen+backLen+minSpacing {
		// Right-align the back hint
		spacing := width - pathLen - backLen
		return b.styles.Container.Render(pathStr + strings.Repeat(" ", spacing) + backHint)
	}

	// Fallback: simple join with separator
	return b.styles.Container.Render(pathStr + "  " + backHint)
}

// RenderBreadcrumb is a convenience function for rendering a breadcrumb inline.
// viewName is the current view name (e.g., "Usage", "Sync").
func RenderBreadcrumb(viewName string, theme Theme, width int) string {
	bc := NewBreadcrumb([]string{"Profiles", viewName}, theme)
	return bc.View(width)
}
