package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func layoutTestProfiles(perProvider int) map[string][]Profile {
	profiles := map[string][]Profile{}
	for _, p := range DefaultProviders() {
		for i := 0; i < perProvider; i++ {
			profiles[p] = append(profiles[p], Profile{
				Name:     fmt.Sprintf("%s-account-with-a-long-name-%02d@example.com", p, i),
				Provider: p,
				IsActive: i == 0,
			})
		}
	}
	return profiles
}

func sizedLayoutModel(t *testing.T, w, h, perProvider int) Model {
	t.Helper()
	t.Setenv("CAAM_HOME", t.TempDir())
	m := New()
	mm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	mm, _ = mm.Update(profilesLoadedMsg{profiles: layoutTestProfiles(perProvider)})
	return mm.(Model)
}

// The main view must fill the terminal exactly, never wider or taller, with
// the status bar on the last row, at the full/compact/tiny breakpoints. The
// profile list and detail panel used to grow past their boxes: at 80x24 the
// view was 61 rows and 92 columns, and the status bar was dropped whenever
// the content overflowed.
func TestMainViewFitsTerminal(t *testing.T) {
	sizes := []struct{ w, h int }{
		{200, 60}, {140, 40}, {120, 40}, {100, 30}, {80, 24}, {70, 20}, {60, 20}, {50, 12},
	}
	for _, sz := range sizes {
		for _, n := range []int{0, 3, 40} {
			t.Run(fmt.Sprintf("%dx%d_%dprofiles", sz.w, sz.h, n), func(t *testing.T) {
				m := sizedLayoutModel(t, sz.w, sz.h, n)
				view := m.View()
				lines := strings.Split(view, "\n")
				if len(lines) != sz.h {
					t.Errorf("view has %d rows, want %d", len(lines), sz.h)
				}
				for i, ln := range lines {
					if w := ansi.StringWidth(ln); w > sz.w {
						t.Errorf("row %d is %d cells wide in a %d-wide terminal", i, w, sz.w)
						break
					}
				}
				mode := strings.TrimSpace(ansi.Strip(m.statusModeIndicator()))
				last := ansi.Strip(lines[len(lines)-1])
				if mode == "" || !strings.Contains(last, mode) {
					t.Errorf("status bar (mode %q) not on the last row: %q", mode, last)
				}
			})
		}
	}
}

// Moving the selection past the rows that fit must scroll the profile list
// so the selected profile stays on screen.
func TestProfilesPanelKeepsSelectionVisible(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{80, 24}, {120, 40}} {
		m := sizedLayoutModel(t, sz.w, sz.h, 40)
		for i := 0; i < 39; i++ {
			mm, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
			m = mm.(Model)
		}
		if got := m.profilesPanel.GetSelected(); got != 39 {
			t.Fatalf("%dx%d: selected %d after 39 downs, want 39", sz.w, sz.h, got)
		}
		view := ansi.Strip(m.View())
		if !strings.Contains(view, "claude-account-wi") {
			t.Fatalf("%dx%d: no profile rows visible:\n%s", sz.w, sz.h, view)
		}
		if strings.Contains(view, "● claude") {
			t.Errorf("%dx%d: list did not scroll; the first (active) profile is still shown:\n%s", sz.w, sz.h, view)
		}
	}
}

func TestClampBoxKeepsBottomBorder(t *testing.T) {
	box := "╭──╮\n│a │\n│b │\n│c │\n╰──╯"
	got := clampBox(box, 10, 3)
	want := "╭──╮\n│a │\n╰──╯"
	if got != want {
		t.Errorf("clampBox = %q, want %q", got, want)
	}
	if got := clampBox(box, 2, 10); got != "╭─\n│a\n│b\n│c\n╰─" {
		t.Errorf("clampBox width cut = %q", got)
	}
}
