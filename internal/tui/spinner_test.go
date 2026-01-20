package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSpinnerOptionsFromEnv(t *testing.T) {
	envKeys := []string{
		"NO_COLOR",
		"TERM",
		"REDUCED_MOTION",
		"CAAM_TUI_REDUCED_MOTION",
		"CAAM_REDUCED_MOTION",
		"REDUCE_MOTION",
		"CAAM_REDUCE_MOTION",
	}
	orig := make(map[string]string)
	origSet := make(map[string]bool)
	for _, key := range envKeys {
		if val, ok := os.LookupEnv(key); ok {
			orig[key] = val
			origSet[key] = true
		}
	}
	defer func() {
		for _, key := range envKeys {
			if origSet[key] {
				os.Setenv(key, orig[key])
			} else {
				os.Unsetenv(key)
			}
		}
	}()

	tests := []struct {
		name               string
		envVars            map[string]string
		expectNoColor      bool
		expectReduceMotion bool
	}{
		{
			name:               "default (no env vars)",
			envVars:            map[string]string{},
			expectNoColor:      false,
			expectReduceMotion: false,
		},
		{
			name:               "NO_COLOR set",
			envVars:            map[string]string{"NO_COLOR": "1"},
			expectNoColor:      true,
			expectReduceMotion: false,
		},
		{
			name:               "TERM=dumb",
			envVars:            map[string]string{"TERM": "dumb"},
			expectNoColor:      true,
			expectReduceMotion: false,
		},
		{
			name:               "REDUCE_MOTION set",
			envVars:            map[string]string{"REDUCE_MOTION": "1"},
			expectNoColor:      false,
			expectReduceMotion: true,
		},
		{
			name:               "REDUCED_MOTION set",
			envVars:            map[string]string{"REDUCED_MOTION": "1"},
			expectNoColor:      false,
			expectReduceMotion: true,
		},
		{
			name:               "CAAM_REDUCE_MOTION set",
			envVars:            map[string]string{"CAAM_REDUCE_MOTION": "true"},
			expectNoColor:      false,
			expectReduceMotion: true,
		},
		{
			name:               "CAAM_TUI_REDUCED_MOTION set",
			envVars:            map[string]string{"CAAM_TUI_REDUCED_MOTION": "true"},
			expectNoColor:      false,
			expectReduceMotion: true,
		},
		{
			name:               "both NO_COLOR and REDUCE_MOTION",
			envVars:            map[string]string{"NO_COLOR": "1", "REDUCE_MOTION": "1"},
			expectNoColor:      true,
			expectReduceMotion: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear all relevant env vars
			for _, key := range envKeys {
				os.Unsetenv(key)
			}

			// Set test env vars
			for k, v := range tt.envVars {
				os.Setenv(k, v)
			}

			opts := SpinnerOptionsFromEnv()
			assert.Equal(t, tt.expectNoColor, opts.NoColor, "NoColor mismatch")
			assert.Equal(t, tt.expectReduceMotion, opts.ReduceMotion, "ReduceMotion mismatch")
		})
	}
}

func TestNewSpinner(t *testing.T) {
	tests := []struct {
		name     string
		opts     SpinnerOptions
		checkFn  func(t *testing.T, s *Spinner)
	}{
		{
			name: "default spinner with message",
			opts: SpinnerOptions{
				Style:   SpinnerStyleDots,
				Message: "Loading...",
			},
			checkFn: func(t *testing.T, s *Spinner) {
				require.NotNil(t, s)
				assert.Equal(t, "Loading...", s.message)
				assert.False(t, s.noColor)
				assert.False(t, s.reduceMotion)
				assert.True(t, s.IsAnimated())
			},
		},
		{
			name: "spinner with NoColor",
			opts: SpinnerOptions{
				Style:   SpinnerStyleDots,
				Message: "Loading...",
				NoColor: true,
			},
			checkFn: func(t *testing.T, s *Spinner) {
				require.NotNil(t, s)
				assert.True(t, s.noColor)
				assert.True(t, s.IsAnimated())
			},
		},
		{
			name: "spinner with ReduceMotion",
			opts: SpinnerOptions{
				Style:        SpinnerStyleDots,
				Message:      "Loading...",
				ReduceMotion: true,
			},
			checkFn: func(t *testing.T, s *Spinner) {
				require.NotNil(t, s)
				assert.True(t, s.reduceMotion)
				assert.False(t, s.IsAnimated())
			},
		},
		{
			name: "spinner with custom color",
			opts: SpinnerOptions{
				Style:   SpinnerStyleLine,
				Message: "Syncing...",
				Color:   lipgloss.Color("#ff0000"),
			},
			checkFn: func(t *testing.T, s *Spinner) {
				require.NotNil(t, s)
				assert.Equal(t, "Syncing...", s.message)
				assert.True(t, s.IsAnimated())
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSpinner(tt.opts)
			tt.checkFn(t, s)
		})
	}
}

func TestNewSpinnerWithTheme(t *testing.T) {
	tests := []struct {
		name        string
		themeOpts   ThemeOptions
		message     string
		expectAnim  bool
	}{
		{
			name:        "default theme",
			themeOpts:   DefaultThemeOptions(),
			message:     "Loading...",
			expectAnim:  true,
		},
		{
			name:        "NoColor theme",
			themeOpts:   ThemeOptions{NoColor: true},
			message:     "Loading...",
			expectAnim:  true,
		},
		{
			name:        "Reduced motion theme",
			themeOpts:   ThemeOptions{ReducedMotion: true},
			message:     "Loading...",
			expectAnim:  false,
		},
		{
			name:        "high contrast theme",
			themeOpts:   ThemeOptions{Contrast: ContrastHigh},
			message:     "Processing...",
			expectAnim:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			theme := NewTheme(tt.themeOpts)
			s := NewSpinnerWithTheme(theme, tt.message)

			require.NotNil(t, s)
			assert.Equal(t, tt.message, s.message)
			assert.Equal(t, tt.expectAnim, s.IsAnimated())
		})
	}
}

func TestSpinnerView(t *testing.T) {
	tests := []struct {
		name       string
		opts       SpinnerOptions
		contains   []string
		notContain []string
	}{
		{
			name: "animated spinner includes message",
			opts: SpinnerOptions{
				Message: "Loading data...",
			},
			contains: []string{"Loading data..."},
		},
		{
			name: "NoColor spinner shows static indicator",
			opts: SpinnerOptions{
				Message: "Loading...",
				NoColor: true,
			},
			contains: []string{"Loading..."},
			notContain: []string{"[...]"},
		},
		{
			name: "ReduceMotion spinner shows static indicator",
			opts: SpinnerOptions{
				Message:      "Loading...",
				ReduceMotion: true,
			},
			contains: []string{"[...]", "Loading..."},
		},
		{
			name: "empty message",
			opts: SpinnerOptions{
				Message: "",
			},
			notContain: []string{"Loading"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSpinner(tt.opts)
			view := s.View()

			for _, substr := range tt.contains {
				assert.Contains(t, view, substr, "expected %q in view", substr)
			}
			for _, substr := range tt.notContain {
				assert.NotContains(t, view, substr, "unexpected %q in view", substr)
			}
		})
	}
}

func TestSpinnerSetMessage(t *testing.T) {
	s := NewSpinner(SpinnerOptions{Message: "Initial"})
	assert.Contains(t, s.View(), "Initial")

	s.SetMessage("Updated")
	assert.Contains(t, s.View(), "Updated")
	assert.NotContains(t, s.View(), "Initial")
}

func TestSpinnerInit(t *testing.T) {
	tests := []struct {
		name      string
		opts      SpinnerOptions
		expectCmd bool
	}{
		{
			name:      "animated spinner returns tick command",
			opts:      SpinnerOptions{Message: "Loading"},
			expectCmd: true,
		},
		{
			name:      "NoColor spinner returns nil",
			opts:      SpinnerOptions{Message: "Loading", NoColor: true},
			expectCmd: true,
		},
		{
			name:      "ReduceMotion spinner returns nil",
			opts:      SpinnerOptions{Message: "Loading", ReduceMotion: true},
			expectCmd: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSpinner(tt.opts)
			cmd := s.Init()

			if tt.expectCmd {
				assert.NotNil(t, cmd, "expected Init to return a command")
			} else {
				assert.Nil(t, cmd, "expected Init to return nil")
			}
		})
	}
}

func TestSpinnerTick(t *testing.T) {
	tests := []struct {
		name      string
		opts      SpinnerOptions
		expectCmd bool
	}{
		{
			name:      "animated spinner returns tick command",
			opts:      SpinnerOptions{Message: "Loading"},
			expectCmd: true,
		},
		{
			name:      "NoColor spinner returns nil",
			opts:      SpinnerOptions{Message: "Loading", NoColor: true},
			expectCmd: true,
		},
		{
			name:      "ReduceMotion spinner returns nil",
			opts:      SpinnerOptions{Message: "Loading", ReduceMotion: true},
			expectCmd: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewSpinner(tt.opts)
			cmd := s.Tick()

			if tt.expectCmd {
				assert.NotNil(t, cmd, "expected Tick to return a command")
			} else {
				assert.Nil(t, cmd, "expected Tick to return nil")
			}
		})
	}
}

func TestSpinnerUpdate(t *testing.T) {
	t.Run("animated spinner processes tick messages", func(t *testing.T) {
		s := NewSpinner(SpinnerOptions{Message: "Loading"})
		require.NotNil(t, s)

		// Create a spinner tick message
		tickMsg := spinner.TickMsg{}

		newS, cmd := s.Update(tickMsg)
		assert.NotNil(t, newS)
		// Animated spinner should return a new tick command
		assert.NotNil(t, cmd)
	})

	t.Run("NoColor spinner processes tick messages", func(t *testing.T) {
		s := NewSpinner(SpinnerOptions{Message: "Loading", NoColor: true})
		require.NotNil(t, s)

		tickMsg := spinner.TickMsg{}

		newS, cmd := s.Update(tickMsg)
		assert.NotNil(t, newS)
		// NoColor spinner should still return a command (no color, but animated)
		assert.NotNil(t, cmd)
	})

	t.Run("ReduceMotion spinner ignores tick messages", func(t *testing.T) {
		s := NewSpinner(SpinnerOptions{Message: "Loading", ReduceMotion: true})
		require.NotNil(t, s)

		tickMsg := spinner.TickMsg{}

		newS, cmd := s.Update(tickMsg)
		assert.NotNil(t, newS)
		// ReduceMotion spinner should not return a command
		assert.Nil(t, cmd)
	})
}

func TestSpinnerNilSafety(t *testing.T) {
	var s *Spinner

	// All methods should be nil-safe
	assert.NotPanics(t, func() { s.Init() })
	assert.NotPanics(t, func() { s.Update(nil) })
	assert.NotPanics(t, func() { s.View() })
	assert.NotPanics(t, func() { s.SetMessage("test") })
	assert.NotPanics(t, func() { s.Tick() })
	assert.NotPanics(t, func() { s.IsAnimated() })

	assert.Equal(t, "", s.View())
	assert.False(t, s.IsAnimated())
	assert.Nil(t, s.Init())
	assert.Nil(t, s.Tick())

	newS, cmd := s.Update(nil)
	assert.Nil(t, newS)
	assert.Nil(t, cmd)
}

func TestSpinnerStyles(t *testing.T) {
	// Test different spinner styles
	styles := []SpinnerStyle{
		SpinnerStyleDots,
		SpinnerStyleLine,
		SpinnerStyleMiniDots,
		SpinnerStylePulse,
	}

	for _, style := range styles {
		t.Run("style_"+string(rune('0'+int(style))), func(t *testing.T) {
			s := NewSpinner(SpinnerOptions{
				Style:   style,
				Message: "Testing",
			})
			require.NotNil(t, s)
			assert.Contains(t, s.View(), "Testing")
		})
	}
}
