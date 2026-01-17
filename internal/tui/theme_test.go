package tui

import (
	"os"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func unsetEnv(t *testing.T, key string) {
	t.Helper()
	orig, ok := os.LookupEnv(key)
	_ = os.Unsetenv(key)
	t.Cleanup(func() {
		if ok {
			_ = os.Setenv(key, orig)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}

func TestThemeOptionsFromEnv_NoColor(t *testing.T) {
	unsetEnv(t, "CAAM_TUI_THEME")
	unsetEnv(t, "CAAM_TUI_CONTRAST")
	unsetEnv(t, "TERM")
	t.Setenv("NO_COLOR", "1")

	opts := ThemeOptionsFromEnv()
	if !opts.NoColor {
		t.Fatal("expected NoColor when NO_COLOR is set")
	}
}

func TestThemeOptionsFromEnv_Overrides(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	unsetEnv(t, "TERM")
	t.Setenv("CAAM_TUI_THEME", "light")
	t.Setenv("CAAM_TUI_CONTRAST", "high")

	opts := ThemeOptionsFromEnv()
	if opts.Mode != ThemeLight {
		t.Fatalf("expected Mode=light, got %q", opts.Mode)
	}
	if opts.Contrast != ContrastHigh {
		t.Fatalf("expected Contrast=high, got %q", opts.Contrast)
	}
	if opts.NoColor {
		t.Fatal("expected NoColor=false when NO_COLOR not set")
	}
}

func TestNewTheme_ModeSelection(t *testing.T) {
	light := NewTheme(ThemeOptions{Mode: ThemeLight, Contrast: ContrastNormal})
	if _, ok := light.Palette.Text.(lipgloss.Color); !ok {
		t.Fatalf("expected Color for light mode, got %T", light.Palette.Text)
	}

	auto := NewTheme(ThemeOptions{Mode: ThemeAuto, Contrast: ContrastNormal})
	if _, ok := auto.Palette.Text.(lipgloss.AdaptiveColor); !ok {
		t.Fatalf("expected AdaptiveColor for auto mode, got %T", auto.Palette.Text)
	}
}

func TestNewTheme_NoColor(t *testing.T) {
	theme := NewTheme(ThemeOptions{Mode: ThemeAuto, Contrast: ContrastNormal, NoColor: true})
	if _, ok := theme.Palette.Text.(lipgloss.NoColor); !ok {
		t.Fatalf("expected NoColor palette, got %T", theme.Palette.Text)
	}
	if theme.Border != lipgloss.HiddenBorder() {
		t.Fatalf("expected hidden border for no-color theme")
	}
}
