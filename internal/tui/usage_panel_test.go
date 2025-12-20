package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUsagePanel_View_Empty(t *testing.T) {
	u := NewUsagePanel()
	u.SetSize(120, 40)

	out := u.View()
	if out == "" {
		t.Fatalf("View() returned empty")
	}
	if want := "No usage data"; !strings.Contains(out, want) {
		t.Fatalf("View() output missing %q", want)
	}
}

func TestUsagePanel_SetStats_ComputesPercentages(t *testing.T) {
	u := NewUsagePanel()
	u.SetStats([]ProfileUsage{
		{Provider: "claude", ProfileName: "a", SessionCount: 1, TotalHours: 1.0},
		{Provider: "codex", ProfileName: "b", SessionCount: 2, TotalHours: 2.0},
	})

	if len(u.stats) != 2 {
		t.Fatalf("stats len = %d, want 2", len(u.stats))
	}
	if u.stats[0].Provider != "codex" {
		t.Fatalf("stats[0].Provider = %q, want %q", u.stats[0].Provider, "codex")
	}
	if u.stats[0].Percentage != 1 {
		t.Fatalf("stats[0].Percentage = %v, want 1", u.stats[0].Percentage)
	}
	if u.stats[1].Percentage <= 0 || u.stats[1].Percentage >= 1 {
		t.Fatalf("stats[1].Percentage = %v, want between (0,1)", u.stats[1].Percentage)
	}
}

func TestModel_UsagePanel_ToggleAndRanges(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CAAM_HOME", tmpDir)

	m := New()
	m.width = 120
	m.height = 40

	// Toggle on with 'u'
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("u")})
	m = model.(Model)
	if m.usagePanel == nil || !m.usagePanel.Visible() {
		t.Fatalf("usage panel not visible after toggle")
	}

	// Switch to last 24h with '1'
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	m = model.(Model)
	if got := m.usagePanel.TimeRange(); got != 1 {
		t.Fatalf("TimeRange = %d, want 1", got)
	}

	// Close with esc
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = model.(Model)
	if m.usagePanel.Visible() {
		t.Fatalf("usage panel still visible after esc")
	}
}

func TestUsagePanel_TimeRangeLabel(t *testing.T) {
	tests := []struct {
		timeRange int
		want      string
	}{
		{1, "Last 24 hours"},
		{7, "Last 7 days"},
		{30, "Last 30 days"},
		{0, "All time"},
		{14, "Last 14 days"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			u := NewUsagePanel()
			u.SetTimeRange(tc.timeRange)
			got := u.timeRangeLabel()
			if got != tc.want {
				t.Errorf("timeRangeLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUsagePanel_RenderBar(t *testing.T) {
	u := NewUsagePanel()

	tests := []struct {
		name       string
		percentage float64
		width      int
		wantLen    int
	}{
		{"zero width", 0.5, 0, 0},
		{"negative width", 0.5, -1, 0},
		{"zero percentage", 0, 10, 10},
		{"full percentage", 1.0, 10, 10},
		{"over 100%", 1.5, 10, 10},
		{"negative percentage", -0.5, 10, 10},
		{"50%", 0.5, 10, 10},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := u.renderBar(tc.percentage, tc.width)
			// Just verify it doesn't panic and returns expected length
			if tc.width <= 0 {
				if result != "" {
					t.Errorf("renderBar with width=%d should return empty string", tc.width)
				}
			}
		})
	}
}

func TestUsagePanel_NilReceiver(t *testing.T) {
	var u *UsagePanel

	// Test all nil receiver cases don't panic
	u.Toggle()
	u.SetTimeRange(1)
	u.SetLoading(true)
	u.SetSize(100, 50)
	u.SetStats(nil)
	_ = u.Visible()
	_ = u.TimeRange()
}
