package tui

import (
	"github.com/charmbracelet/lipgloss"
)

// Color palette - Dracula theme inspired.
var (
	colorPurple    = lipgloss.Color("#bd93f9")
	colorPink      = lipgloss.Color("#ff79c6")
	colorGreen     = lipgloss.Color("#50fa7b")
	colorYellow    = lipgloss.Color("#f1fa8c")
	colorCyan      = lipgloss.Color("#8be9fd")
	colorOrange    = lipgloss.Color("#ffb86c")
	colorRed       = lipgloss.Color("#ff5555")
	colorWhite     = lipgloss.Color("#f8f8f2")
	colorGray      = lipgloss.Color("#6272a4")
	colorDarkGray  = lipgloss.Color("#44475a")
	colorBackground = lipgloss.Color("#282a36")
)

// Styles holds all the lipgloss styles for the TUI.
type Styles struct {
	// Header styles
	Header lipgloss.Style

	// Tab styles
	Tab       lipgloss.Style
	ActiveTab lipgloss.Style

	// List item styles
	Item         lipgloss.Style
	SelectedItem lipgloss.Style
	Active       lipgloss.Style

	// Status bar styles
	StatusBar  lipgloss.Style
	StatusKey  lipgloss.Style
	StatusText lipgloss.Style

	// Empty state
	Empty lipgloss.Style

	// Help screen
	Help lipgloss.Style

	// Dialog styles
	Dialog             lipgloss.Style
	DialogTitle        lipgloss.Style
	DialogButton       lipgloss.Style
	DialogButtonActive lipgloss.Style
}

// DefaultStyles returns the default style configuration.
func DefaultStyles() Styles {
	return Styles{
		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPurple).
			MarginBottom(1),

		Tab: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorGray).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorDarkGray),

		ActiveTab: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorWhite).
			Bold(true).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPurple),

		Item: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorWhite),

		SelectedItem: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(colorPurple).
			Bold(true).
			Background(colorDarkGray),

		Active: lipgloss.NewStyle().
			Foreground(colorGreen).
			Bold(true),

		StatusBar: lipgloss.NewStyle().
			Padding(0, 1).
			Background(colorDarkGray).
			Foreground(colorWhite),

		StatusKey: lipgloss.NewStyle().
			Foreground(colorPurple).
			Bold(true),

		StatusText: lipgloss.NewStyle().
			Foreground(colorGray),

		Empty: lipgloss.NewStyle().
			Foreground(colorGray).
			Italic(true).
			Padding(2, 4),

		Help: lipgloss.NewStyle().
			Padding(2, 4).
			Foreground(colorWhite),

		Dialog: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPurple).
			Padding(1, 2),

		DialogTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(colorPurple).
			MarginBottom(1),

		DialogButton: lipgloss.NewStyle().
			Padding(0, 2).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorGray),

		DialogButtonActive: lipgloss.NewStyle().
			Padding(0, 2).
			Bold(true).
			Foreground(colorWhite).
			Background(colorPurple).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorPurple),
	}
}
