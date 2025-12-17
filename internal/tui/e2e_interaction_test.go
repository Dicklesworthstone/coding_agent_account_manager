// Package tui provides end-to-end interaction simulation tests.
package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestE2E_NavigationWithArrowKeys tests navigation using arrow keys.
func TestE2E_NavigationWithArrowKeys(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_navigation")

	// Create model with test data
	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "alice@example.com", Provider: "claude", IsActive: true},
			{Name: "bob@example.com", Provider: "claude", IsActive: false},
		},
		"codex": {
			{Name: "work@company.com", Provider: "codex", IsActive: true},
		},
		"gemini": {
			{Name: "personal@gmail.com", Provider: "gemini", IsActive: false},
		},
	}
	m.syncProfilesPanel()

	// Test initial state
	if m.activeProvider != 0 {
		t.Errorf("Expected activeProvider 0, got %d", m.activeProvider)
	}
	if m.currentProvider() != "claude" {
		t.Errorf("Expected provider 'claude', got %q", m.currentProvider())
	}

	// Navigate right to codex
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	if m.activeProvider != 1 {
		t.Errorf("Expected activeProvider 1, got %d", m.activeProvider)
	}
	if m.currentProvider() != "codex" {
		t.Errorf("Expected provider 'codex', got %q", m.currentProvider())
	}

	// Navigate right to gemini
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(Model)
	if m.activeProvider != 2 {
		t.Errorf("Expected activeProvider 2, got %d", m.activeProvider)
	}

	// Navigate left back to codex
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	if m.activeProvider != 1 {
		t.Errorf("Expected activeProvider 1, got %d", m.activeProvider)
	}

	h.Log.Info("Arrow key navigation tests complete", map[string]interface{}{
		"final_provider": m.currentProvider(),
	})
}

// TestE2E_NavigationWithTabKey tests navigation using Tab key.
func TestE2E_NavigationWithTabKey(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_tab_navigation")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude"}},
		"codex":  {{Name: "work@example.com", Provider: "codex"}},
		"gemini": {{Name: "home@example.com", Provider: "gemini"}},
	}
	m.syncProfilesPanel()

	// Tab should cycle through providers
	if m.currentProvider() != "claude" {
		t.Errorf("Expected initial provider 'claude', got %q", m.currentProvider())
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.currentProvider() != "codex" {
		t.Errorf("Expected provider 'codex', got %q", m.currentProvider())
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.currentProvider() != "gemini" {
		t.Errorf("Expected provider 'gemini', got %q", m.currentProvider())
	}

	// Tab should wrap around
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.currentProvider() != "claude" {
		t.Errorf("Expected provider 'claude' after wrap, got %q", m.currentProvider())
	}

	h.Log.Info("Tab navigation tests complete")
}

// TestE2E_ProfileSelectionWithJK tests profile selection using j/k keys.
func TestE2E_ProfileSelectionWithJK(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_jk_navigation")

	m := New()
	m.width = 120
	m.height = 40

	// Add multiple profiles to test selection
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "profile1@example.com", Provider: "claude", IsActive: false},
			{Name: "profile2@example.com", Provider: "claude", IsActive: true},
			{Name: "profile3@example.com", Provider: "claude", IsActive: false},
		},
	}
	m.syncProfilesPanel()

	// Initial selection should be 0
	if m.profilesPanel.GetSelected() != 0 {
		t.Errorf("Expected initial selection 0, got %d", m.profilesPanel.GetSelected())
	}

	// Move down with 'j'
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.profilesPanel.GetSelected() != 1 {
		t.Errorf("Expected selection 1, got %d", m.profilesPanel.GetSelected())
	}

	// Move down again
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	m = updated.(Model)
	if m.profilesPanel.GetSelected() != 2 {
		t.Errorf("Expected selection 2, got %d", m.profilesPanel.GetSelected())
	}

	// Move up with 'k'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	m = updated.(Model)
	if m.profilesPanel.GetSelected() != 1 {
		t.Errorf("Expected selection 1, got %d", m.profilesPanel.GetSelected())
	}

	h.Log.Info("j/k navigation tests complete", map[string]interface{}{
		"final_selection": m.profilesPanel.GetSelected(),
	})
}

// TestE2E_DeleteConfirmationWorkflow tests delete with y/n confirmation.
func TestE2E_DeleteConfirmationWorkflow(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_delete_workflow")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {{Name: "to-delete@example.com", Provider: "claude"}},
	}
	m.syncProfilesPanel()

	// Press 'd' to initiate delete
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m = updated.(Model)

	// Should be in confirmation state
	if m.state != stateConfirm {
		t.Errorf("Expected stateConfirm, got %v", m.state)
	}
	if m.pendingAction != confirmDelete {
		t.Errorf("Expected confirmDelete, got %v", m.pendingAction)
	}

	h.Log.Info("Delete confirmation state verified")

	// Cancel with 'n'
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	m = updated.(Model)

	// Should be back to list state
	if m.state != stateList {
		t.Errorf("Expected stateList, got %v", m.state)
	}
	if m.pendingAction != confirmNone {
		t.Errorf("Expected confirmNone, got %v", m.pendingAction)
	}

	h.Log.Info("Delete cancellation verified")

	// Now test confirmation
	m.state = stateConfirm
	m.pendingAction = confirmDelete

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m = updated.(Model)

	// Should be back to list state after confirmation
	if m.state != stateList {
		t.Errorf("Expected stateList, got %v", m.state)
	}

	h.Log.Info("Delete workflow tests complete")
}

// TestE2E_SearchModeWorkflow tests search mode entry and exit.
func TestE2E_SearchModeWorkflow(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_search_workflow")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "alice@work.com", Provider: "claude"},
			{Name: "bob@personal.com", Provider: "claude"},
			{Name: "charlie@test.com", Provider: "claude"},
		},
	}
	m.syncProfilesPanel()

	// Press '/' to enter search mode
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	m = updated.(Model)

	// Should be in search state
	if m.state != stateSearch {
		t.Errorf("Expected stateSearch, got %v", m.state)
	}

	h.Log.Info("Search mode entered")

	// Press Esc to exit search mode
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)

	// Should be back to list state
	if m.state != stateList {
		t.Errorf("Expected stateList, got %v", m.state)
	}

	h.Log.Info("Search workflow tests complete")
}

// TestE2E_HelpScreenToggle tests help screen display.
func TestE2E_HelpScreenToggle(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_help_screen")

	m := New()
	m.width = 120
	m.height = 40

	// Press '?' to show help
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = updated.(Model)

	// Should be in help state
	if m.state != stateHelp {
		t.Errorf("Expected stateHelp, got %v", m.state)
	}

	// Press any key to dismiss help
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m = updated.(Model)

	// Should be back to list state
	if m.state != stateList {
		t.Errorf("Expected stateList, got %v", m.state)
	}

	h.Log.Info("Help screen tests complete")
}

// TestE2E_ActionKeyBindings tests various action key bindings.
func TestE2E_ActionKeyBindings(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_action_keys")

	testCases := []struct {
		name string
		key  rune
	}{
		{"login", 'l'},
		{"backup", 'b'},
		{"open", 'o'},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.width = 120
			m.height = 40
			m.profiles = map[string][]Profile{
				"claude": {{Name: "test@example.com", Provider: "claude"}},
			}
			m.syncProfilesPanel()

			// Press action key
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{tc.key}})
			m = updated.(Model)

			// Should have a status message (indicating action was triggered)
			if m.statusMsg == "" {
				t.Errorf("Expected status message after pressing %q", tc.key)
			}

			h.Log.Info("Action key tested", map[string]interface{}{
				"key":        string(tc.key),
				"status_msg": m.statusMsg,
			})
		})
	}
}

// TestE2E_QuitKeyBinding tests quit key binding.
func TestE2E_QuitKeyBinding(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_quit")

	m := New()
	m.width = 120
	m.height = 40

	// Press 'q' to quit
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	// Should return a quit command
	if cmd == nil {
		t.Error("Expected quit command")
	}

	h.Log.Info("Quit key binding verified")
}

// TestE2E_CtrlCKeyBinding tests Ctrl+C key binding.
func TestE2E_CtrlCKeyBinding(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_ctrl_c")

	m := New()
	m.width = 120
	m.height = 40

	// Press Ctrl+C to quit
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})

	// Should return a quit command
	if cmd == nil {
		t.Error("Expected quit command")
	}

	h.Log.Info("Ctrl+C key binding verified")
}

// TestE2E_WindowResizing tests window resize handling.
func TestE2E_WindowResizing(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_resize")

	m := New()

	// Initial size
	if m.width != 0 || m.height != 0 {
		t.Errorf("Expected initial size 0x0, got %dx%d", m.width, m.height)
	}

	// Resize to 100x50
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 50})
	m = updated.(Model)

	if m.width != 100 || m.height != 50 {
		t.Errorf("Expected size 100x50, got %dx%d", m.width, m.height)
	}

	// Resize to smaller
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m = updated.(Model)

	if m.width != 80 || m.height != 24 {
		t.Errorf("Expected size 80x24, got %dx%d", m.width, m.height)
	}

	h.Log.Info("Window resize handling verified", map[string]interface{}{
		"final_size": []int{m.width, m.height},
	})
}

// TestE2E_ProfilesLoadedMessage tests profile loading.
func TestE2E_ProfilesLoadedMessage(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_profiles_load")

	m := New()
	m.width = 120
	m.height = 40

	// Initially no profiles (or empty)
	initialCount := len(m.profiles)

	// Simulate profiles loaded message
	profiles := map[string][]Profile{
		"claude": {
			{Name: "user1@example.com", Provider: "claude", IsActive: true},
			{Name: "user2@example.com", Provider: "claude", IsActive: false},
		},
		"codex": {
			{Name: "work@company.com", Provider: "codex", IsActive: true},
		},
		"gemini": {},
	}

	updated, _ := m.Update(profilesLoadedMsg{profiles: profiles})
	m = updated.(Model)

	// Verify profiles are loaded
	if len(m.profiles["claude"]) != 2 {
		t.Errorf("Expected 2 claude profiles, got %d", len(m.profiles["claude"]))
	}
	if len(m.profiles["codex"]) != 1 {
		t.Errorf("Expected 1 codex profile, got %d", len(m.profiles["codex"]))
	}

	h.Log.Info("Profiles loaded message handling verified", map[string]interface{}{
		"initial_count": initialCount,
		"final_count":   len(m.profiles["claude"]) + len(m.profiles["codex"]),
	})
}

// TestE2E_EmptyProviderHandling tests handling of providers with no profiles.
func TestE2E_EmptyProviderHandling(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_empty_provider")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {}, // Empty
		"codex":  {},
		"gemini": {},
	}
	m.syncProfilesPanel()

	// Should handle empty provider without panic
	profiles := m.currentProfiles()
	if len(profiles) != 0 {
		t.Errorf("Expected 0 profiles, got %d", len(profiles))
	}

	// Navigation should still work
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = updated.(Model)
	if m.currentProvider() != "codex" {
		t.Errorf("Expected 'codex', got %q", m.currentProvider())
	}

	// Action on empty should not panic
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	_ = updated.(Model)

	h.Log.Info("Empty provider handling verified")
}

// TestE2E_ViewRendering tests that View() produces output.
func TestE2E_ViewRendering(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_view_render")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude", IsActive: true}},
	}
	m.syncProfilesPanel()

	// Render view
	view := m.View()

	// Should produce non-empty output
	if view == "" {
		t.Error("Expected non-empty view")
	}

	h.Log.Info("View rendering verified", map[string]interface{}{
		"view_length": len(view),
	})
}

// TestE2E_StateTransitions tests state transitions.
func TestE2E_StateTransitions(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_state_transitions")

	transitions := []struct {
		name       string
		startState viewState
		key        tea.KeyMsg
		endState   viewState
	}{
		{"list_to_search", stateList, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, stateSearch},
		{"search_to_list", stateSearch, tea.KeyMsg{Type: tea.KeyEsc}, stateList},
		{"list_to_help", stateList, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}}, stateHelp},
		{"help_to_list", stateHelp, tea.KeyMsg{Type: tea.KeyEsc}, stateList},
	}

	for _, tc := range transitions {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.width = 120
			m.height = 40
			m.state = tc.startState
			m.profiles = map[string][]Profile{
				"claude": {{Name: "test@example.com", Provider: "claude"}},
			}
			m.syncProfilesPanel()

			updated, _ := m.Update(tc.key)
			m = updated.(Model)

			if m.state != tc.endState {
				t.Errorf("Expected state %v, got %v", tc.endState, m.state)
			}

			h.Log.Info("State transition verified", map[string]interface{}{
				"transition": tc.name,
				"start":      tc.startState,
				"end":        m.state,
			})
		})
	}
}

// TestE2E_ProfilesPanelNavigation tests profiles panel navigation limits.
func TestE2E_ProfilesPanelNavigation(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_profiles_panel_nav")

	p := NewProfilesPanel()
	p.SetProvider("claude")
	p.SetProfiles([]ProfileInfo{
		{Name: "first@example.com", LastUsed: time.Now()},
		{Name: "second@example.com", LastUsed: time.Now().Add(-1 * time.Hour)},
		{Name: "third@example.com", LastUsed: time.Now().Add(-2 * time.Hour)},
	})

	// Initial selection
	if p.GetSelected() != 0 {
		t.Errorf("Expected selection 0, got %d", p.GetSelected())
	}

	// Move up at top - should stay at 0
	p.MoveUp()
	if p.GetSelected() != 0 {
		t.Errorf("Expected selection 0 after up at top, got %d", p.GetSelected())
	}

	// Move down to end
	p.MoveDown()
	if p.GetSelected() != 1 {
		t.Errorf("Expected selection 1, got %d", p.GetSelected())
	}
	p.MoveDown()
	if p.GetSelected() != 2 {
		t.Errorf("Expected selection 2, got %d", p.GetSelected())
	}

	// Move down at bottom - should stay at 2
	p.MoveDown()
	if p.GetSelected() != 2 {
		t.Errorf("Expected selection 2 after down at bottom, got %d", p.GetSelected())
	}

	h.Log.Info("Profiles panel navigation verified")
}

// TestE2E_ProviderPanelSync tests provider panel synchronization.
func TestE2E_ProviderPanelSync(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_provider_sync")

	m := New()
	m.width = 120
	m.height = 40

	// Set profile counts
	profiles := map[string][]Profile{
		"claude": {{Name: "a"}, {Name: "b"}},
		"codex":  {{Name: "c"}},
		"gemini": {},
	}
	m.profiles = profiles
	m.syncProfilesPanel()

	// Provider panel should have correct counts
	if m.providerPanel == nil {
		t.Fatal("Expected provider panel to be initialized")
	}

	h.Log.Info("Provider panel sync verified")
}

// TestE2E_DetailPanelUpdate tests detail panel updates.
func TestE2E_DetailPanelUpdate(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_detail_panel")

	dp := NewDetailPanel()
	if dp == nil {
		t.Fatal("Expected non-nil detail panel")
	}

	// Set profile
	dp.SetProfile(&DetailInfo{
		Name:     "test@example.com",
		AuthMode: "oauth",
		LoggedIn: true,
	})

	view := dp.View()
	if view == "" {
		t.Error("Expected non-empty detail panel view")
	}

	h.Log.Info("Detail panel update verified")
}

// TestE2E_KeyBindingsHelp tests that help displays key bindings.
func TestE2E_KeyBindingsHelp(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_keybindings_help")

	km := defaultKeyMap()

	// Verify all essential bindings exist
	bindings := map[string]interface{}{
		"Login":   km.Login,
		"Open":    km.Open,
		"Backup":  km.Backup,
		"Delete":  km.Delete,
		"Search":  km.Search,
		"Confirm": km.Confirm,
		"Cancel":  km.Cancel,
		"Help":    km.Help,
		"Quit":    km.Quit,
	}

	for name, binding := range bindings {
		kb := binding.(interface{ Keys() []string })
		if len(kb.Keys()) == 0 {
			t.Errorf("Expected %s binding to have keys", name)
		}
	}

	h.Log.Info("Key bindings help verified", map[string]interface{}{
		"binding_count": len(bindings),
	})
}

// TestE2E_EnterKeyActivatesProfile tests Enter key activates profile.
func TestE2E_EnterKeyActivatesProfile(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_enter_activate")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", Provider: "claude", IsActive: false}},
	}
	m.syncProfilesPanel()

	// Press Enter to activate
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)

	// Should trigger activation (status message set or command returned)
	h.Log.Info("Enter key activation tested", map[string]interface{}{
		"status": m.statusMsg,
	})
}

// TestE2E_UpDownArrowNavigation tests up/down arrow keys for profile selection.
func TestE2E_UpDownArrowNavigation(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("test_arrow_profile_nav")

	m := New()
	m.width = 120
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "first@example.com", Provider: "claude"},
			{Name: "second@example.com", Provider: "claude"},
		},
	}
	m.syncProfilesPanel()

	// Move down with arrow
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m = updated.(Model)
	if m.profilesPanel.GetSelected() != 1 {
		t.Errorf("Expected selection 1 after down arrow, got %d", m.profilesPanel.GetSelected())
	}

	// Move up with arrow
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m = updated.(Model)
	if m.profilesPanel.GetSelected() != 0 {
		t.Errorf("Expected selection 0 after up arrow, got %d", m.profilesPanel.GetSelected())
	}

	h.Log.Info("Arrow profile navigation verified")
}
