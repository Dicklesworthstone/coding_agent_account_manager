package tui

import (
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SpinnerStyle defines the visual style of the spinner.
type SpinnerStyle int

const (
	// SpinnerStyleDots uses a dot pattern (default)
	SpinnerStyleDots SpinnerStyle = iota
	// SpinnerStyleLine uses a line pattern
	SpinnerStyleLine
	// SpinnerStyleMiniDots uses smaller dots
	SpinnerStyleMiniDots
	// SpinnerStylePulse uses a pulsing animation
	SpinnerStylePulse
)

// SpinnerOptions configures spinner behavior.
type SpinnerOptions struct {
	// Style determines the spinner animation pattern
	Style SpinnerStyle
	// Message is shown next to the spinner
	Message string
	// Color is the spinner color (ignored if NoColor)
	Color lipgloss.TerminalColor
	// NoColor disables colors (animation still allowed).
	NoColor bool
	// ReduceMotion disables animation (shows static indicator)
	ReduceMotion bool
}

// Spinner wraps the bubbles spinner with theme and accessibility support.
type Spinner struct {
	spinner      spinner.Model
	message      string
	style        lipgloss.Style
	noColor      bool
	reduceMotion bool
}

// SpinnerOptionsFromEnv derives spinner options from environment.
// Respects:
// - NO_COLOR (disables color output)
// - TERM=dumb (disables color output)
// - CAAM_TUI_REDUCED_MOTION / CAAM_REDUCED_MOTION / REDUCED_MOTION (disables animation)
// - CAAM_REDUCE_MOTION / REDUCE_MOTION (legacy aliases)
func SpinnerOptionsFromEnv() SpinnerOptions {
	opts := SpinnerOptions{}

	// Check NO_COLOR
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		opts.NoColor = true
	}

	// Check TERM=dumb
	term := strings.TrimSpace(strings.ToLower(os.Getenv("TERM")))
	if term == "dumb" {
		opts.NoColor = true
	}

	// Check reduced motion preference
	if envBool("CAAM_TUI_REDUCED_MOTION") ||
		envBool("CAAM_REDUCED_MOTION") ||
		envBool("REDUCED_MOTION") ||
		envBool("CAAM_REDUCE_MOTION") ||
		envBool("REDUCE_MOTION") {
		opts.ReduceMotion = true
	}

	return opts
}

// NewSpinner creates a spinner with the given options.
func NewSpinner(opts SpinnerOptions) *Spinner {
	s := spinner.New()

	// Select spinner pattern based on style
	switch opts.Style {
	case SpinnerStyleLine:
		s.Spinner = spinner.Line
	case SpinnerStyleMiniDots:
		s.Spinner = spinner.Line
	case SpinnerStylePulse:
		s.Spinner = spinner.Line
	default:
		s.Spinner = spinner.Line
	}

	// Apply color if not in NoColor mode
	if !opts.NoColor && opts.Color != nil {
		s.Style = lipgloss.NewStyle().Foreground(opts.Color)
	}

	// Create message style
	msgStyle := lipgloss.NewStyle()
	if !opts.NoColor {
		if opts.Color != nil {
			msgStyle = msgStyle.Foreground(opts.Color)
		}
	}

	return &Spinner{
		spinner:      s,
		message:      opts.Message,
		style:        msgStyle,
		noColor:      opts.NoColor,
		reduceMotion: opts.ReduceMotion,
	}
}

// NewSpinnerWithTheme creates a spinner using theme settings.
func NewSpinnerWithTheme(theme Theme, message string) *Spinner {
	envOpts := SpinnerOptionsFromEnv()

	opts := SpinnerOptions{
		Style:        SpinnerStyleLine,
		Message:      message,
		Color:        theme.Palette.Accent,
		NoColor:      theme.NoColor || envOpts.NoColor,
		ReduceMotion: theme.ReducedMotion || envOpts.ReduceMotion,
	}

	s := NewSpinner(opts)
	if s == nil {
		return s
	}
	s.spinner.Style = spinnerStyle(theme)
	s.style = spinnerMessageStyle(theme)
	return s
}

// Init initializes the spinner (required for bubbletea).
func (s *Spinner) Init() tea.Cmd {
	if s == nil || s.reduceMotion {
		return nil
	}
	return s.spinner.Tick
}

// Update handles spinner tick messages.
func (s *Spinner) Update(msg tea.Msg) (*Spinner, tea.Cmd) {
	if s == nil {
		return s, nil
	}

	// If animation is disabled, no need to process tick
	if s.reduceMotion {
		return s, nil
	}

	// Handle spinner tick messages
	if _, ok := msg.(spinner.TickMsg); ok {
		var cmd tea.Cmd
		s.spinner, cmd = s.spinner.Update(msg)
		return s, cmd
	}

	return s, nil
}

// View renders the spinner with its message.
func (s *Spinner) View() string {
	if s == nil {
		return ""
	}

	var indicator string

	if s.reduceMotion {
		// Static indicator for accessibility
		indicator = "[...]"
	} else {
		indicator = s.spinner.View()
	}

	if s.message != "" {
		return indicator + " " + s.style.Render(s.message)
	}
	return indicator
}

// SetMessage updates the spinner message.
func (s *Spinner) SetMessage(msg string) {
	if s == nil {
		return
	}
	s.message = msg
}

// Tick returns the command to trigger a spinner tick.
// Use this to start the spinner animation.
func (s *Spinner) Tick() tea.Cmd {
	if s == nil || s.reduceMotion {
		return nil
	}
	return s.spinner.Tick
}

// IsAnimated returns true if the spinner is animated (not static).
func (s *Spinner) IsAnimated() bool {
	if s == nil {
		return false
	}
	return !s.reduceMotion
}
