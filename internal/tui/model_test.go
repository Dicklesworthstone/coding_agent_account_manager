package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestNew(t *testing.T) {
	m := New()
	if len(m.providers) != 3 {
		t.Errorf("expected 3 providers, got %d", len(m.providers))
	}
	if m.activeProvider != 0 {
		t.Errorf("expected activeProvider 0, got %d", m.activeProvider)
	}
	if m.providerPanel == nil {
		t.Error("expected providerPanel to be initialized")
	}
}

func TestNewWithProviders(t *testing.T) {
	providers := []string{"test1", "test2"}
	m := NewWithProviders(providers)
	if len(m.providers) != 2 {
		t.Errorf("expected 2 providers, got %d", len(m.providers))
	}
}

func TestDefaultProviders(t *testing.T) {
	providers := DefaultProviders()
	expected := []string{"claude", "codex", "gemini"}
	if len(providers) != len(expected) {
		t.Errorf("expected %d providers, got %d", len(expected), len(providers))
	}
	for i, p := range providers {
		if p != expected[i] {
			t.Errorf("expected provider %s at index %d, got %s", expected[i], i, p)
		}
	}
}

func TestModelUpdate(t *testing.T) {
	m := New()

	// Test window size message
	model, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	updated := model.(Model)
	if updated.width != 100 || updated.height != 50 {
		t.Errorf("expected dimensions 100x50, got %dx%d", updated.width, updated.height)
	}

	// Test profiles loaded message
	profiles := map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude", IsActive: true}},
	}
	model, _ = updated.Update(profilesLoadedMsg{profiles: profiles})
	updated = model.(Model)
	if len(updated.profiles["claude"]) != 1 {
		t.Errorf("expected 1 claude profile, got %d", len(updated.profiles["claude"]))
	}
}

func TestCurrentProfiles(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "a@b.com"}},
		"codex":  {{Name: "c@d.com"}, {Name: "e@f.com"}},
	}

	profiles := m.currentProfiles()
	if len(profiles) != 1 {
		t.Errorf("expected 1 profile for claude, got %d", len(profiles))
	}

	m.activeProvider = 1
	profiles = m.currentProfiles()
	if len(profiles) != 2 {
		t.Errorf("expected 2 profiles for codex, got %d", len(profiles))
	}
}

func TestCurrentProvider(t *testing.T) {
	m := New()
	if m.currentProvider() != "claude" {
		t.Errorf("expected claude, got %s", m.currentProvider())
	}
	m.activeProvider = 2
	if m.currentProvider() != "gemini" {
		t.Errorf("expected gemini, got %s", m.currentProvider())
	}
}

func TestProviderPanelView(t *testing.T) {
	p := NewProviderPanel(DefaultProviders())
	p.SetProfileCounts(map[string]int{"claude": 2, "codex": 1, "gemini": 0})
	p.SetActiveProvider(0)

	view := p.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestCapitalizeFirst(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"claude", "Claude"},
		{"Codex", "Codex"},
		{"", ""},
		{"gemini", "Gemini"},
	}

	for _, tc := range tests {
		result := capitalizeFirst(tc.input)
		if result != tc.expected {
			t.Errorf("capitalizeFirst(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestProfilesPanelBasic(t *testing.T) {
	p := NewProfilesPanel()
	if p == nil {
		t.Fatal("expected non-nil profiles panel")
	}

	p.SetProvider("claude")
	view := p.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestProfilesPanelWithProfiles(t *testing.T) {
	p := NewProfilesPanel()
	p.SetProvider("codex")

	// Note: Profiles are sorted by last used (most recent first), then by name
	// Using different LastUsed times to control sort order
	profiles := []ProfileInfo{
		{Name: "work@company.com", AuthMode: "oauth", LoggedIn: true, IsActive: true, LastUsed: time.Now()},
		{Name: "personal@gmail.com", AuthMode: "oauth", LoggedIn: true, Locked: true, LastUsed: time.Now().Add(-1 * time.Hour)},
	}
	p.SetProfiles(profiles)

	view := p.View()
	if view == "" {
		t.Error("expected non-empty view")
	}

	// Test selection
	if p.GetSelected() != 0 {
		t.Errorf("expected initial selection 0, got %d", p.GetSelected())
	}

	p.MoveDown()
	if p.GetSelected() != 1 {
		t.Errorf("expected selection 1 after MoveDown, got %d", p.GetSelected())
	}

	p.MoveUp()
	if p.GetSelected() != 0 {
		t.Errorf("expected selection 0 after MoveUp, got %d", p.GetSelected())
	}

	// Test GetSelectedProfile - should be work@ since it has most recent LastUsed
	selected := p.GetSelectedProfile()
	if selected == nil {
		t.Fatal("expected non-nil selected profile")
	}
	if selected.Name != "work@company.com" {
		t.Errorf("expected work@company.com, got %s", selected.Name)
	}
}

func TestFormatRelativeTime(t *testing.T) {
	// Test zero time
	result := formatRelativeTime(time.Time{})
	if result != "never" {
		t.Errorf("expected 'never' for zero time, got %s", result)
	}

	// Test current time
	result = formatRelativeTime(time.Now())
	if result != "now" {
		t.Errorf("expected 'now' for current time, got %s", result)
	}
}

func TestProfilesPanelIntegration(t *testing.T) {
	m := New()

	// Verify profiles panel is initialized
	if m.profilesPanel == nil {
		t.Fatal("expected profilesPanel to be initialized")
	}

	// Simulate loading profiles
	profiles := map[string][]Profile{
		"claude": {
			{Name: "alice@example.com", Provider: "claude", IsActive: true},
			{Name: "bob@example.com", Provider: "claude", IsActive: false},
		},
	}
	m.profiles = profiles
	m.syncProfilesPanel()

	// Verify profiles panel synced
	selected := m.profilesPanel.GetSelectedProfile()
	if selected == nil {
		t.Fatal("expected non-nil selected profile after sync")
	}
}

func TestDeleteProfileWithConfirmation(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude"}},
	}

	// Try to delete
	result, _ := m.handleDeleteProfile()
	updated := result.(Model)

	// Should be in confirmation state
	if updated.state != stateConfirm {
		t.Errorf("expected stateConfirm, got %v", updated.state)
	}
	if updated.pendingAction != confirmDelete {
		t.Errorf("expected confirmDelete, got %v", updated.pendingAction)
	}
}

func TestDeleteProfileCancel(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude"}},
	}

	// Initiate delete
	m.state = stateConfirm
	m.pendingAction = confirmDelete

	// Cancel with 'n' key
	result, _ := m.handleConfirmKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	updated := result.(Model)

	// Should be back to list state
	if updated.state != stateList {
		t.Errorf("expected stateList, got %v", updated.state)
	}
	if updated.pendingAction != confirmNone {
		t.Errorf("expected confirmNone, got %v", updated.pendingAction)
	}
}

func TestDeleteProfileConfirm(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude"}},
	}
	m.state = stateConfirm
	m.pendingAction = confirmDelete

	// Confirm with 'y' key
	result, _ := m.handleConfirmKeys(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	updated := result.(Model)

	// Should be back to list state
	if updated.state != stateList {
		t.Errorf("expected stateList, got %v", updated.state)
	}
	if updated.pendingAction != confirmNone {
		t.Errorf("expected confirmNone, got %v", updated.pendingAction)
	}
}

func TestHandleLoginProfile(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude"}},
	}

	result, _ := m.handleLoginProfile()
	updated := result.(Model)

	// Should have status message
	if updated.statusMsg == "" {
		t.Error("expected non-empty status message")
	}
}

func TestHandleOpenInBrowser(t *testing.T) {
	m := New()

	result, _ := m.handleOpenInBrowser()
	updated := result.(Model)

	// Should have status message
	if updated.statusMsg == "" {
		t.Error("expected non-empty status message")
	}
}

func TestHandleBackupProfile(t *testing.T) {
	m := New()

	result, _ := m.handleBackupProfile()
	updated := result.(Model)

	// Should have status message
	if updated.statusMsg == "" {
		t.Error("expected non-empty status message")
	}
}

func TestKeyMapBindings(t *testing.T) {
	km := defaultKeyMap()

	// Verify all bindings exist
	if len(km.Login.Keys()) == 0 {
		t.Error("expected Login binding to have keys")
	}
	if len(km.Open.Keys()) == 0 {
		t.Error("expected Open binding to have keys")
	}
	if len(km.Search.Keys()) == 0 {
		t.Error("expected Search binding to have keys")
	}
	if len(km.Confirm.Keys()) == 0 {
		t.Error("expected Confirm binding to have keys")
	}
	if len(km.Cancel.Keys()) == 0 {
		t.Error("expected Cancel binding to have keys")
	}
}
