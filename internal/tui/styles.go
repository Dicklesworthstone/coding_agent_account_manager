package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ThemeMode controls how the palette is resolved.
type ThemeMode string

const (
	ThemeAuto  ThemeMode = "auto"
	ThemeDark  ThemeMode = "dark"
	ThemeLight ThemeMode = "light"
)

// ThemeContrast controls high-contrast overrides.
type ThemeContrast string

const (
	ContrastNormal ThemeContrast = "normal"
	ContrastHigh   ThemeContrast = "high"
)

// ThemeOptions configures palette selection and rendering preferences.
type ThemeOptions struct {
	Mode     ThemeMode
	Contrast ThemeContrast
	NoColor  bool
	// ReducedMotion disables animated UI effects (e.g., spinners).
	ReducedMotion bool
}

// Theme defines the active palette and rendering characteristics.
type Theme struct {
	Mode     ThemeMode
	Contrast ThemeContrast
	NoColor  bool
	// ReducedMotion disables animated UI effects (e.g., spinners).
	ReducedMotion bool
	Palette  Palette
	Border   lipgloss.Border
}

// Palette holds the color tokens used throughout the TUI.
type Palette struct {
	Accent       lipgloss.TerminalColor
	AccentAlt    lipgloss.TerminalColor
	Success      lipgloss.TerminalColor
	Warning      lipgloss.TerminalColor
	Info         lipgloss.TerminalColor
	Danger       lipgloss.TerminalColor
	Text         lipgloss.TerminalColor
	Muted        lipgloss.TerminalColor
	Background   lipgloss.TerminalColor
	Surface      lipgloss.TerminalColor
	SurfaceMuted lipgloss.TerminalColor
	Border       lipgloss.TerminalColor
	BorderMuted  lipgloss.TerminalColor
	Selection    lipgloss.TerminalColor
	KeycapBg     lipgloss.TerminalColor
	KeycapBorder lipgloss.TerminalColor
	KeycapText   lipgloss.TerminalColor
}

// DefaultTheme returns the theme derived from the environment.
func DefaultTheme() Theme {
	return NewTheme(ThemeOptionsFromEnv())
}

// DefaultThemeOptions provides baseline options.
func DefaultThemeOptions() ThemeOptions {
	return ThemeOptions{
		Mode:     ThemeAuto,
		Contrast: ContrastNormal,
	}
}

// ThemeOptionsFromEnv derives theme options from environment variables.
// Supported variables:
// - NO_COLOR (any value) disables color output.
// - TERM=dumb disables color output.
// - CAAM_TUI_THEME=auto|dark|light|high-contrast
// - CAAM_TUI_CONTRAST=normal|high
// - CAAM_TUI_REDUCED_MOTION=1 (or true/yes/on) disables animation
// - CAAM_REDUCED_MOTION=1 (alias)
// - REDUCED_MOTION=1 (generic)
func ThemeOptionsFromEnv() ThemeOptions {
	opts := DefaultThemeOptions()

	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		opts.NoColor = true
	}

	term := strings.TrimSpace(strings.ToLower(os.Getenv("TERM")))
	if term == "dumb" {
		opts.NoColor = true
	}

	if envBool("CAAM_TUI_REDUCED_MOTION") || envBool("CAAM_REDUCED_MOTION") || envBool("REDUCED_MOTION") {
		opts.ReducedMotion = true
	}

	theme := strings.TrimSpace(strings.ToLower(os.Getenv("CAAM_TUI_THEME")))
	switch theme {
	case "light", "lite":
		opts.Mode = ThemeLight
	case "dark":
		opts.Mode = ThemeDark
	case "auto", "system":
		opts.Mode = ThemeAuto
	}
	if strings.Contains(theme, "contrast") {
		opts.Contrast = ContrastHigh
	}

	contrast := strings.TrimSpace(strings.ToLower(os.Getenv("CAAM_TUI_CONTRAST")))
	switch contrast {
	case "high", "hc", "1", "true":
		opts.Contrast = ContrastHigh
	case "normal", "0", "false":
		opts.Contrast = ContrastNormal
	}

	return opts
}

// NewTheme builds a theme from options.
func NewTheme(opts ThemeOptions) Theme {
	if opts.Mode == "" {
		opts.Mode = ThemeAuto
	}
	if opts.Contrast == "" {
		opts.Contrast = ContrastNormal
	}

	palette := paletteFor(opts)
	border := lipgloss.RoundedBorder()
	if opts.NoColor {
		border = lipgloss.HiddenBorder()
	}

	return Theme{
		Mode:     opts.Mode,
		Contrast: opts.Contrast,
		NoColor:  opts.NoColor,
		ReducedMotion: opts.ReducedMotion,
		Palette:  palette,
		Border:   border,
	}
}

func paletteFor(opts ThemeOptions) Palette {
	if opts.NoColor {
		return noColorPalette()
	}
	if opts.Contrast == ContrastHigh {
		return highContrastPalette(opts)
	}
	return defaultPalette(opts)
}

func defaultPalette(opts ThemeOptions) Palette {
	return Palette{
		Accent:       resolveColor("#1d4ed8", "#4f8cff", opts),
		AccentAlt:    resolveColor("#db2777", "#f472b6", opts),
		Success:      resolveColor("#15803d", "#2fd576", opts),
		Warning:      resolveColor("#b45309", "#f2c94c", opts),
		Info:         resolveColor("#0e7490", "#5ad1e9", opts),
		Danger:       resolveColor("#b91c1c", "#ff6b6b", opts),
		Text:         resolveColor("#0f172a", "#e6edf3", opts),
		Muted:        resolveColor("#475569", "#9aa4b2", opts),
		Background:   resolveColor("#f8fafc", "#0b1220", opts),
		Surface:      resolveColor("#ffffff", "#111827", opts),
		SurfaceMuted: resolveColor("#e2e8f0", "#1f2937", opts),
		Border:       resolveColor("#cbd5e1", "#1f2937", opts),
		BorderMuted:  resolveColor("#e2e8f0", "#374151", opts),
		Selection:    resolveColor("#e2e8f0", "#1f2937", opts),
		KeycapBg:     resolveColor("#e2e8f0", "#1f2937", opts),
		KeycapBorder: resolveColor("#94a3b8", "#374151", opts),
		KeycapText:   resolveColor("#0f172a", "#e6edf3", opts),
	}
}

func highContrastPalette(opts ThemeOptions) Palette {
	return Palette{
		Accent:       resolveColor("#1e40af", "#6ea8ff", opts),
		AccentAlt:    resolveColor("#be185d", "#ff86cc", opts),
		Success:      resolveColor("#166534", "#38e27d", opts),
		Warning:      resolveColor("#92400e", "#ffd166", opts),
		Info:         resolveColor("#155e75", "#7fe3f5", opts),
		Danger:       resolveColor("#991b1b", "#ff7b7b", opts),
		Text:         resolveColor("#0b1120", "#f8fafc", opts),
		Muted:        resolveColor("#334155", "#c7d0df", opts),
		Background:   resolveColor("#ffffff", "#05070f", opts),
		Surface:      resolveColor("#f8fafc", "#0b1220", opts),
		SurfaceMuted: resolveColor("#e5e7eb", "#111827", opts),
		Border:       resolveColor("#94a3b8", "#334155", opts),
		BorderMuted:  resolveColor("#cbd5e1", "#475569", opts),
		Selection:    resolveColor("#dbeafe", "#1e293b", opts),
		KeycapBg:     resolveColor("#dbeafe", "#1e293b", opts),
		KeycapBorder: resolveColor("#2563eb", "#3b82f6", opts),
		KeycapText:   resolveColor("#0f172a", "#f8fafc", opts),
	}
}

func noColorPalette() Palette {
	no := lipgloss.NoColor{}
	return Palette{
		Accent:       no,
		AccentAlt:    no,
		Success:      no,
		Warning:      no,
		Info:         no,
		Danger:       no,
		Text:         no,
		Muted:        no,
		Background:   no,
		Surface:      no,
		SurfaceMuted: no,
		Border:       no,
		BorderMuted:  no,
		Selection:    no,
		KeycapBg:     no,
		KeycapBorder: no,
		KeycapText:   no,
	}
}

func resolveColor(light, dark string, opts ThemeOptions) lipgloss.TerminalColor {
	if opts.NoColor {
		return lipgloss.NoColor{}
	}
	switch opts.Mode {
	case ThemeLight:
		return lipgloss.Color(light)
	case ThemeDark:
		return lipgloss.Color(dark)
	default:
		return lipgloss.AdaptiveColor{Light: light, Dark: dark}
	}
}

func keycapStyle(theme Theme, compact bool) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	if !theme.NoColor {
		style = style.Foreground(theme.Palette.KeycapText).
			Background(theme.Palette.KeycapBg)
	}
	if !compact {
		style = style.Padding(0, 1)
	}
	return style
}

func envBool(key string) bool {
	val, ok := os.LookupEnv(key)
	if !ok {
		return false
	}
	val = strings.TrimSpace(strings.ToLower(val))
	if val == "" {
		return true
	}
	switch val {
	case "1", "true", "yes", "y", "on":
		return true
	case "0", "false", "no", "n", "off":
		return false
	default:
		return true
	}
}

// spinnerStyle returns the centralized spinner styling for the current theme.
func spinnerStyle(theme Theme) lipgloss.Style {
	style := lipgloss.NewStyle()
	if theme.NoColor {
		return style
	}
	return style.Foreground(theme.Palette.Accent)
}

func spinnerEnabled(theme Theme) bool {
	return !theme.ReducedMotion
}

func spinnerMessageStyle(theme Theme) lipgloss.Style {
	style := lipgloss.NewStyle()
	if theme.NoColor {
		return style
	}
	return style.Foreground(theme.Palette.Muted)
}

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
	DialogFocused      lipgloss.Style
	DialogOverlay      lipgloss.Style
	DialogTitle        lipgloss.Style
	DialogButton       lipgloss.Style
	DialogButtonActive lipgloss.Style

	// Input styles
	InputCursor lipgloss.Style
}

// DefaultStyles returns the default style configuration.
func DefaultStyles() Styles {
	return NewStyles(DefaultTheme())
}

// NewStyles returns styles for the provided theme.
func NewStyles(theme Theme) Styles {
	p := theme.Palette
	keycap := keycapStyle(theme, false).MarginRight(1)
	dialogBorder := theme.Border
	dialogFocusBorder := theme.Border
	overlayStyle := lipgloss.NewStyle()
	if !theme.NoColor {
		dialogFocusBorder = lipgloss.DoubleBorder()
		overlayStyle = overlayStyle.Faint(true)
	}

	return Styles{
		Header: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			Background(p.Surface).
			Padding(0, 1).
			MarginBottom(1),

		Tab: lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(p.Muted).
			Border(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderTop(false).
			BorderLeft(false).
			BorderRight(false).
			BorderForeground(p.BorderMuted),

		ActiveTab: lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(p.Text).
			Bold(true).
			Background(p.Surface).
			Border(lipgloss.NormalBorder()).
			BorderBottom(true).
			BorderTop(false).
			BorderLeft(false).
			BorderRight(false).
			BorderForeground(p.Accent),

		Item: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(p.Text),

		SelectedItem: lipgloss.NewStyle().
			Padding(0, 2).
			Foreground(p.Text).
			Bold(true).
			Background(p.Selection),

		Active: lipgloss.NewStyle().
			Foreground(p.Success).
			Bold(true),

		StatusBar: lipgloss.NewStyle().
			Padding(0, 1).
			Background(p.SurfaceMuted).
			Foreground(p.Text),

		StatusKey: keycap,

		StatusText: lipgloss.NewStyle().
			Foreground(p.Muted),

		Empty: lipgloss.NewStyle().
			Foreground(p.Muted).
			Italic(true).
			Padding(2, 4),

		Help: lipgloss.NewStyle().
			Padding(2, 4).
			Foreground(p.Text),

		Dialog: lipgloss.NewStyle().
			Border(dialogBorder).
			BorderForeground(p.Border).
			Background(p.Surface).
			Padding(1, 2),

		DialogFocused: lipgloss.NewStyle().
			Border(dialogFocusBorder).
			BorderForeground(p.Accent).
			Background(p.Surface).
			Padding(1, 2),

		DialogOverlay: overlayStyle,

		DialogTitle: lipgloss.NewStyle().
			Bold(true).
			Foreground(p.Accent).
			MarginBottom(1),

		DialogButton: lipgloss.NewStyle().
			Padding(0, 2).
			Border(theme.Border).
			BorderForeground(p.BorderMuted),

		DialogButtonActive: lipgloss.NewStyle().
			Padding(0, 2).
			Bold(true).
			Foreground(p.Text).
			Background(p.Accent).
			Border(theme.Border).
			BorderForeground(p.Accent),

		InputCursor: lipgloss.NewStyle().
			Foreground(p.Accent).
			Bold(true),
	}
}
