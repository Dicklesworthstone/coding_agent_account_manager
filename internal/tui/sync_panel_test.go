package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSyncPanel_View_Empty(t *testing.T) {
	p := NewSyncPanel()
	p.SetSize(120, 40)

	out := p.View()
	if out == "" {
		t.Fatalf("View() returned empty")
	}
	if want := "No machines"; !strings.Contains(out, want) {
		t.Fatalf("View() output missing %q, got: %s", want, out)
	}
}

func TestSyncPanel_Toggle(t *testing.T) {
	p := NewSyncPanel()

	if p.Visible() {
		t.Fatalf("panel should not be visible initially")
	}

	p.Toggle()
	if !p.Visible() {
		t.Fatalf("panel should be visible after toggle")
	}

	p.Toggle()
	if p.Visible() {
		t.Fatalf("panel should not be visible after second toggle")
	}
}

func TestSyncPanel_Navigation(t *testing.T) {
	p := NewSyncPanel()

	// Can't move up/down with no machines
	p.MoveUp()
	p.MoveDown()
	// Should not panic
}

func TestModel_SyncPanel_ToggleWithKey(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("CAAM_HOME", tmpDir)

	m := New()
	m.width = 120
	m.height = 40

	// Toggle on with 'S'
	model, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("S")})
	m = model.(Model)
	if m.syncPanel == nil || !m.syncPanel.Visible() {
		t.Fatalf("sync panel not visible after toggle")
	}

	// Close with esc
	model, _ = m.Update(tea.KeyMsg{Type: tea.KeyEscape})
	m = model.(Model)
	if m.syncPanel.Visible() {
		t.Fatalf("sync panel still visible after esc")
	}
}

func TestSyncPanel_SetLoading(t *testing.T) {
	p := NewSyncPanel()
	p.SetSize(120, 40)
	p.SetLoading(true)

	out := p.View()
	if !strings.Contains(out, "Loading") {
		t.Fatalf("View() should show loading state, got: %s", out)
	}

	p.SetLoading(false)
	out = p.View()
	if strings.Contains(out, "Loading") {
		t.Fatalf("View() should not show loading after SetLoading(false)")
	}
}

func TestSyncPanel_SetSyncing(t *testing.T) {
	p := NewSyncPanel()
	p.SetSyncing(true)
	// Should not panic

	// Test nil receiver
	var nilPanel *SyncPanel
	nilPanel.SetSyncing(true)
	// Should not panic
}

func TestSyncPanel_SetState(t *testing.T) {
	p := NewSyncPanel()
	p.SetSize(120, 40)

	// Set nil state
	p.SetState(nil)
	if p.State() != nil {
		t.Fatal("State should be nil after SetState(nil)")
	}

	// Test nil receiver
	var nilPanel *SyncPanel
	nilPanel.SetState(nil)
	// Should not panic
}

func TestSyncPanel_State(t *testing.T) {
	p := NewSyncPanel()
	if p.State() != nil {
		t.Fatal("State should be nil initially")
	}

	// Test nil receiver
	var nilPanel *SyncPanel
	if nilPanel.State() != nil {
		t.Fatal("State on nil receiver should return nil")
	}
}

func TestSyncPanel_SelectedMachine(t *testing.T) {
	p := NewSyncPanel()

	// No machines - should return nil
	if m := p.SelectedMachine(); m != nil {
		t.Fatal("SelectedMachine should return nil with no machines")
	}

	// Test nil receiver
	var nilPanel *SyncPanel
	if m := nilPanel.SelectedMachine(); m != nil {
		t.Fatal("SelectedMachine on nil receiver should return nil")
	}
}

// Note: TestGetStatusIcon, TestFormatTimeAgo, TestTruncateString, TestToMachineInfo
// are defined in sync_test.go
