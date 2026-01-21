package tui

import (
	"strings"
	"testing"
)

func TestBreadcrumb_View(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := NewTheme(ThemeOptionsFromEnv())

	tests := []struct {
		name        string
		path        []string
		width       int
		wantPath    string
		wantBack    string
		wantNotEmpty bool
	}{
		{
			name:        "home and usage",
			path:        []string{"Profiles", "Usage"},
			width:       80,
			wantPath:    "Profiles > Usage",
			wantBack:    "[Esc] Back",
			wantNotEmpty: true,
		},
		{
			name:        "home and sync",
			path:        []string{"Profiles", "Sync"},
			width:       80,
			wantPath:    "Profiles > Sync",
			wantBack:    "[Esc] Back",
			wantNotEmpty: true,
		},
		{
			name:        "empty path",
			path:        []string{},
			width:       80,
			wantNotEmpty: false,
		},
		{
			name:        "single item",
			path:        []string{"Home"},
			width:       80,
			wantPath:    "Home",
			wantBack:    "[Esc] Back",
			wantNotEmpty: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			bc := NewBreadcrumb(tc.path, theme)
			got := bc.View(tc.width)

			if tc.wantNotEmpty {
				if got == "" {
					t.Errorf("View() returned empty string, expected content")
				}
				if tc.wantPath != "" && !strings.Contains(got, tc.wantPath) {
					t.Errorf("View() missing path %q in output: %s", tc.wantPath, got)
				}
				if tc.wantBack != "" && !strings.Contains(got, tc.wantBack) {
					t.Errorf("View() missing back hint %q in output: %s", tc.wantBack, got)
				}
			} else {
				if got != "" {
					t.Errorf("View() should return empty for empty path, got: %s", got)
				}
			}
		})
	}
}

func TestRenderBreadcrumb(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	theme := NewTheme(ThemeOptionsFromEnv())

	got := RenderBreadcrumb("Usage", theme, 80)

	if !strings.Contains(got, "Profiles > Usage") {
		t.Errorf("RenderBreadcrumb missing path, got: %s", got)
	}
	if !strings.Contains(got, "[Esc] Back") {
		t.Errorf("RenderBreadcrumb missing back hint, got: %s", got)
	}
}

func TestBreadcrumb_NilReceiver(t *testing.T) {
	var bc *Breadcrumb
	got := bc.View(80)
	if got != "" {
		t.Errorf("nil receiver View() should return empty, got: %s", got)
	}
}

func TestBreadcrumbStyles(t *testing.T) {
	theme := DefaultTheme()
	styles := NewBreadcrumbStyles(theme)

	// Just verify styles are created without panic
	_ = styles.Container
	_ = styles.Separator
	_ = styles.Home
	_ = styles.Current
	_ = styles.BackHint
}
