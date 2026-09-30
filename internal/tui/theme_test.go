package tui

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
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
	unsetEnv(t, "CAAM_TUI_REDUCED_MOTION")
	unsetEnv(t, "CAAM_REDUCED_MOTION")
	unsetEnv(t, "REDUCED_MOTION")
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

func TestThemeOptionsFromEnv_ReducedMotion(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	unsetEnv(t, "TERM")
	unsetEnv(t, "CAAM_TUI_THEME")
	unsetEnv(t, "CAAM_TUI_CONTRAST")
	t.Setenv("CAAM_TUI_REDUCED_MOTION", "1")

	opts := ThemeOptionsFromEnv()
	if !opts.ReducedMotion {
		t.Fatal("expected ReducedMotion=true when CAAM_TUI_REDUCED_MOTION is set")
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

// TERM=dumb must disable color in both the env-derived and the
// config-derived theme options.
func TestThemeOptions_TermDumbDisablesColor(t *testing.T) {
	unsetEnv(t, "NO_COLOR")
	t.Setenv("TERM", "dumb")
	if !ThemeOptionsFromEnv().NoColor {
		t.Error("ThemeOptionsFromEnv: TERM=dumb should set NoColor")
	}
	t.Setenv("CAAM_HOME", t.TempDir())
	if !TUIPreferencesFromConfig(config.DefaultSPMConfig()).NoColor {
		t.Error("TUIPreferencesFromConfig: TERM=dumb should set NoColor")
	}

	t.Setenv("TERM", "xterm-256color")
	if ThemeOptionsFromEnv().NoColor {
		t.Error("ThemeOptionsFromEnv: xterm-256color without NO_COLOR should keep color")
	}
}

// In every mode and contrast, text must not share its color with the
// backgrounds it is drawn on, and every palette token must be set.
func TestNewTheme_PaletteLegible(t *testing.T) {
	colorOf := func(c lipgloss.TerminalColor, dark bool) string {
		switch v := c.(type) {
		case lipgloss.Color:
			return string(v)
		case lipgloss.AdaptiveColor:
			if dark {
				return v.Dark
			}
			return v.Light
		case nil:
			return ""
		default:
			return fmt.Sprintf("%v", v)
		}
	}
	for _, mode := range []ThemeMode{ThemeLight, ThemeDark, ThemeAuto} {
		for _, contrast := range []ThemeContrast{ContrastNormal, ContrastHigh} {
			th := NewTheme(ThemeOptions{Mode: mode, Contrast: contrast})
			p := th.Palette
			tokens := map[string]lipgloss.TerminalColor{
				"Accent": p.Accent, "Text": p.Text, "Muted": p.Muted, "Background": p.Background,
				"Surface": p.Surface, "Border": p.Border, "Selection": p.Selection,
				"Success": p.Success, "Warning": p.Warning, "Danger": p.Danger,
				"KeycapBg": p.KeycapBg, "KeycapText": p.KeycapText,
			}
			for name, c := range tokens {
				if c == nil {
					t.Errorf("mode=%s contrast=%s: %s is unset", mode, contrast, name)
				}
			}
			for _, dark := range []bool{false, true} {
				text := colorOf(p.Text, dark)
				for _, bg := range []struct {
					name string
					c    lipgloss.TerminalColor
				}{{"Background", p.Background}, {"Surface", p.Surface}, {"Selection", p.Selection}} {
					if text != "" && strings.EqualFold(text, colorOf(bg.c, dark)) {
						t.Errorf("mode=%s contrast=%s dark=%v: Text and %s are both %s", mode, contrast, dark, bg.name, text)
					}
				}
				if kt, kb := colorOf(p.KeycapText, dark), colorOf(p.KeycapBg, dark); kt != "" && strings.EqualFold(kt, kb) {
					t.Errorf("mode=%s contrast=%s dark=%v: KeycapText and KeycapBg are both %s", mode, contrast, dark, kt)
				}
			}
		}
	}
}
