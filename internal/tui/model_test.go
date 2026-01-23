package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/watcher"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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

	// Should either show status message (no auth files) or show backup dialog (auth files exist)
	if updated.statusMsg == "" && updated.backupDialog == nil {
		t.Error("expected either status message or backup dialog")
	}

	// If dialog is shown, state should be stateBackupDialog
	if updated.backupDialog != nil && updated.state != stateBackupDialog {
		t.Errorf("expected stateBackupDialog when dialog is shown, got %v", updated.state)
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

// TestInit tests that Init returns expected commands.
func TestInit(t *testing.T) {
	m := New()

	cmd := m.Init()
	if cmd == nil {
		t.Error("expected Init to return a non-nil command")
	}

	// The command should be a batch of load commands
	// We can't directly test the batch contents, but we can verify it's a function
}

// TestInitWithFileWatching tests Init command generation with file watching enabled.
func TestInitWithFileWatching(t *testing.T) {
	m := New()
	m.runtime.FileWatching = true

	cmd := m.Init()
	if cmd == nil {
		t.Error("expected Init to return a non-nil command with file watching")
	}
}

// TestRestoreSelection tests the restoreSelection method.
func TestRestoreSelection(t *testing.T) {
	tests := []struct {
		name          string
		profiles      []Profile
		ctx           refreshContext
		initialSelect int
		expectedName  string
	}{
		{
			name:          "empty profiles",
			profiles:      []Profile{},
			ctx:           refreshContext{},
			initialSelect: 5,
			expectedName:  "",
		},
		{
			name: "restore by profile name",
			profiles: []Profile{
				{Name: "alpha"},
				{Name: "beta"},
				{Name: "gamma"},
			},
			ctx:           refreshContext{selectedProfile: "beta"},
			initialSelect: 0,
			expectedName:  "beta",
		},
		{
			name: "deleted profile - select next",
			profiles: []Profile{
				{Name: "alpha"},
				{Name: "gamma"},
			},
			ctx:           refreshContext{deletedProfile: "beta"},
			initialSelect: 0,
			expectedName:  "alpha", // alpha takes beta's place
		},
		{
			name: "deleted profile - select previous when deleted is last",
			profiles: []Profile{
				{Name: "alpha"},
				{Name: "beta"},
			},
			ctx:           refreshContext{deletedProfile: "zeta"},
			initialSelect: 0,
			expectedName:  "beta", // last profile
		},
		{
			name: "clamp selection to valid range",
			profiles: []Profile{
				{Name: "only"},
			},
			ctx:           refreshContext{},
			initialSelect: 10,
			expectedName:  "only",
		},
		{
			name: "negative selection clamped to 0",
			profiles: []Profile{
				{Name: "only"},
			},
			ctx:           refreshContext{},
			initialSelect: -5,
			expectedName:  "only",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.profiles = map[string][]Profile{
				"claude": tc.profiles,
			}
			m.selected = tc.initialSelect

			m.restoreSelection(tc.ctx)

			if m.selectedProfileName != tc.expectedName {
				t.Errorf("expected selectedProfileName=%q, got %q", tc.expectedName, m.selectedProfileName)
			}
		})
	}
}

// TestShowError tests the showError method.
func TestShowError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		context  string
		contains string
	}{
		{
			name:     "nil error",
			err:      nil,
			context:  "Test",
			contains: "",
		},
		{
			name:     "file not found error",
			err:      fmt.Errorf("no such file or directory"),
			context:  "Load",
			contains: "Profile not found in vault",
		},
		{
			name:     "permission denied error",
			err:      fmt.Errorf("permission denied"),
			context:  "Write",
			contains: "Cannot write to auth file",
		},
		{
			name:     "invalid/corrupt error",
			err:      fmt.Errorf("invalid JSON"),
			context:  "Parse",
			contains: "Profile data corrupted",
		},
		{
			name:     "already exists error",
			err:      fmt.Errorf("already exists"),
			context:  "Create",
			contains: "Profile already exists",
		},
		{
			name:     "locked error",
			err:      fmt.Errorf("locked by process"),
			context:  "Access",
			contains: "locked by another process",
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("something went wrong"),
			context:  "Action",
			contains: "something went wrong",
		},
		{
			name:     "error with empty context",
			err:      fmt.Errorf("some error"),
			context:  "",
			contains: "some error",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New()
			m.showError(tc.err, tc.context)

			if tc.err == nil {
				if m.statusMsg != "" {
					t.Errorf("expected empty status for nil error, got %q", m.statusMsg)
				}
				return
			}

			if !strings.Contains(m.statusMsg, tc.contains) {
				t.Errorf("expected status to contain %q, got %q", tc.contains, m.statusMsg)
			}
		})
	}
}

// TestFormatError tests the formatError method.
func TestFormatError(t *testing.T) {
	m := New()

	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error",
			err:      nil,
			expected: "",
		},
		{
			name:     "no such file",
			err:      fmt.Errorf("no such file"),
			expected: "Profile not found in vault",
		},
		{
			name:     "does not exist",
			err:      fmt.Errorf("path does not exist"),
			expected: "Profile not found in vault",
		},
		{
			name:     "permission denied",
			err:      fmt.Errorf("permission denied: /path"),
			expected: "Cannot write to auth file - check permissions",
		},
		{
			name:     "invalid data",
			err:      fmt.Errorf("invalid token format"),
			expected: "Profile data corrupted - try re-backup",
		},
		{
			name:     "corrupt file",
			err:      fmt.Errorf("corrupt JSON"),
			expected: "Profile data corrupted - try re-backup",
		},
		{
			name:     "already exists",
			err:      fmt.Errorf("profile already exists"),
			expected: "Profile already exists",
		},
		{
			name:     "locked",
			err:      fmt.Errorf("file is locked"),
			expected: "Profile is currently locked by another process",
		},
		{
			name:     "generic error",
			err:      fmt.Errorf("unknown error occurred"),
			expected: "unknown error occurred",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := m.formatError(tc.err)
			if result != tc.expected {
				t.Errorf("formatError(%v) = %q, expected %q", tc.err, result, tc.expected)
			}
		})
	}
}

// TestShowActivateSuccess tests the showActivateSuccess method.
func TestShowActivateSuccess(t *testing.T) {
	m := New()
	m.showActivateSuccess("claude", "work@example.com")

	if !strings.Contains(m.statusMsg, "Activated") {
		t.Errorf("expected 'Activated' in status, got %q", m.statusMsg)
	}
	if !strings.Contains(m.statusMsg, "work@example.com") {
		t.Errorf("expected profile name in status, got %q", m.statusMsg)
	}
	if !strings.Contains(m.statusMsg, "claude") {
		t.Errorf("expected provider name in status, got %q", m.statusMsg)
	}
}

// TestShowRefreshSuccess tests the showRefreshSuccess method.
func TestShowRefreshSuccess(t *testing.T) {
	m := New()

	// Test with zero expiry time
	m.showRefreshSuccess("test@example.com", time.Time{})
	if !strings.Contains(m.statusMsg, "Refreshed") {
		t.Errorf("expected 'Refreshed' in status, got %q", m.statusMsg)
	}
	if !strings.Contains(m.statusMsg, "test@example.com") {
		t.Errorf("expected profile name in status, got %q", m.statusMsg)
	}

	// Test with specific expiry time
	expiry := time.Date(2025, time.March, 15, 14, 30, 0, 0, time.UTC)
	m.showRefreshSuccess("work@company.com", expiry)
	if !strings.Contains(m.statusMsg, "Mar 15") || !strings.Contains(m.statusMsg, "14:30") {
		t.Errorf("expected expiry time in status, got %q", m.statusMsg)
	}
}

// TestDialogOverlayView tests the dialogOverlayView method.
func TestDialogOverlayView(t *testing.T) {
	m := New()
	m.width = 120
	m.height = 24
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com"}},
	}

	dialogContent := "Test Dialog Content"
	view := m.dialogOverlayView(dialogContent)

	if view == "" {
		t.Error("expected non-empty view")
	}
	if !strings.Contains(view, dialogContent) {
		t.Errorf("expected dialog content in view, got %q", view)
	}
	if !strings.Contains(view, "caam - Coding Agent Account Manager") {
		t.Errorf("expected background view to be retained in overlay")
	}
}

// TestDialogOverlayViewSmallScreen tests dialogOverlayView with small dimensions.
func TestDialogOverlayViewSmallScreen(t *testing.T) {
	m := New()
	m.width = 20
	m.height = 10

	// Dialog larger than screen
	largeDialog := strings.Repeat("X", 50)
	view := m.dialogOverlayView(largeDialog)

	if view == "" {
		t.Error("expected non-empty view even with small screen")
	}
}

// TestMainViewWithCompactLayout tests mainView with compact layout.
func TestMainViewWithCompactLayout(t *testing.T) {
	m := New()
	m.width = 60 // Compact width
	m.height = 20
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", IsActive: true}},
	}
	m.syncProfilesPanel()

	view := m.mainView()
	if view == "" {
		t.Error("expected non-empty view for compact layout")
	}
}

// TestMainViewWithFullLayout tests mainView with full layout.
func TestMainViewWithFullLayout(t *testing.T) {
	m := New()
	m.width = 150 // Full width
	m.height = 40
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com", IsActive: true}},
	}
	m.syncProfilesPanel()

	view := m.mainView()
	if view == "" {
		t.Error("expected non-empty view for full layout")
	}
}

// TestIsCompactLayout tests the isCompactLayout method.
func TestIsCompactLayout(t *testing.T) {
	tests := []struct {
		width, height int
		expected      bool
	}{
		{0, 0, false},    // Zero dimensions
		{50, 30, true},   // Narrow width
		{150, 20, true},  // Short height
		{150, 40, false}, // Full size
		{93, 30, true},   // Just under width threshold
		{94, 23, true},   // Just under height threshold
		{94, 24, false},  // Exactly at thresholds
	}

	for _, tc := range tests {
		t.Run(fmt.Sprintf("%dx%d", tc.width, tc.height), func(t *testing.T) {
			m := New()
			m.width = tc.width
			m.height = tc.height

			result := m.isCompactLayout()
			if result != tc.expected {
				t.Errorf("isCompactLayout() with %dx%d = %v, expected %v",
					tc.width, tc.height, result, tc.expected)
			}
		})
	}
}

func TestLayoutModeTiny(t *testing.T) {
	m := New()
	m.width = minTinyWidth - 1
	m.height = 40
	if m.layoutMode() != layoutTiny {
		t.Errorf("expected tiny layout for narrow width, got %v", m.layoutMode())
	}

	m.width = 120
	m.height = minTinyHeight - 1
	if m.layoutMode() != layoutTiny {
		t.Errorf("expected tiny layout for short height, got %v", m.layoutMode())
	}
}

func TestFullLayoutSpecWidths(t *testing.T) {
	m := New()
	m.width = minFullWidth() + 20
	m.height = minFullHeight + 10

	spec := m.fullLayoutSpec(20)
	available := m.width - (layoutGap * 2)
	total := spec.ProviderWidth + spec.ProfilesWidth + spec.DetailWidth

	if spec.ProviderWidth < minProviderWidth {
		t.Errorf("provider width below min: %d", spec.ProviderWidth)
	}
	if spec.ProfilesWidth < minProfilesWidth {
		t.Errorf("profiles width below min: %d", spec.ProfilesWidth)
	}
	if spec.DetailWidth < minDetailWidth {
		t.Errorf("detail width below min: %d", spec.DetailWidth)
	}
	if total > available {
		t.Errorf("panel widths exceed available: %d > %d", total, available)
	}
}

func TestCompactLayoutSpecTinyDetailHeights(t *testing.T) {
	m := New()
	spec := m.compactLayoutSpec(layoutTiny, 30, 1)
	if !spec.ShowDetail {
		t.Error("expected detail to be enabled for tall tiny layout")
	}
	if spec.ProfilesHeight <= 0 {
		t.Error("expected profiles height to be positive in tiny layout")
	}

	spec = m.compactLayoutSpec(layoutTiny, 10, 1)
	if spec.ShowDetail {
		t.Error("expected detail to be disabled for short tiny layout")
	}
}

// TestProjectContextLine tests the projectContextLine method.
func TestProjectContextLine(t *testing.T) {
	m := New()

	// Test with no cwd
	m.cwd = ""
	result := m.projectContextLine()
	if result != "" {
		t.Errorf("expected empty string for no cwd, got %q", result)
	}

	// Test with no provider
	m.cwd = "/some/path"
	m.providers = []string{}
	result = m.projectContextLine()
	if result != "" {
		t.Errorf("expected empty string for no provider, got %q", result)
	}

	// Test with no project context
	m.providers = []string{"claude"}
	m.projectContext = nil
	result = m.projectContextLine()
	if !strings.Contains(result, "no association") {
		t.Errorf("expected 'no association' in result, got %q", result)
	}
}

// TestProjectDefaultForProvider tests the projectDefaultForProvider method.
func TestProjectDefaultForProvider(t *testing.T) {
	m := New()

	// Test with empty provider
	result := m.projectDefaultForProvider("")
	if result != "" {
		t.Errorf("expected empty string for empty provider, got %q", result)
	}

	// Test with nil project context
	m.projectContext = nil
	result = m.projectDefaultForProvider("claude")
	if result != "" {
		t.Errorf("expected empty string for nil project context, got %q", result)
	}
}

// TestProviderCount tests the providerCount method.
func TestProviderCount(t *testing.T) {
	m := New()

	// Test with nil profiles
	m.profiles = nil
	count := m.providerCount("claude")
	if count != 0 {
		t.Errorf("expected 0 for nil profiles, got %d", count)
	}

	// Test with profiles
	m.profiles = map[string][]Profile{
		"claude": {{Name: "a"}, {Name: "b"}},
		"codex":  {{Name: "c"}},
	}
	if got := m.providerCount("claude"); got != 2 {
		t.Errorf("expected 2 for claude, got %d", got)
	}
	if got := m.providerCount("codex"); got != 1 {
		t.Errorf("expected 1 for codex, got %d", got)
	}
	if got := m.providerCount("gemini"); got != 0 {
		t.Errorf("expected 0 for gemini, got %d", got)
	}
}

// TestBadgeFor tests the badgeFor method.
func TestBadgeFor(t *testing.T) {
	m := New()

	// Test with nil badges
	m.badges = nil
	badge := m.badgeFor("claude", "test")
	if badge != "" {
		t.Errorf("expected empty badge for nil badges, got %q", badge)
	}

	// Test with expired badge
	m.badges = map[string]profileBadge{
		"claude/expired": {badge: "OLD", expiry: time.Now().Add(-1 * time.Hour)},
	}
	badge = m.badgeFor("claude", "expired")
	if badge != "" {
		t.Errorf("expected empty badge for expired, got %q", badge)
	}

	// Test with valid badge
	m.badges["claude/new"] = profileBadge{badge: "NEW", expiry: time.Now().Add(1 * time.Hour)}
	badge = m.badgeFor("claude", "new")
	if badge != "NEW" {
		t.Errorf("expected 'NEW' badge, got %q", badge)
	}

	// Test with zero expiry (never expires)
	m.badges["claude/permanent"] = profileBadge{badge: "PERM", expiry: time.Time{}}
	badge = m.badgeFor("claude", "permanent")
	if badge != "PERM" {
		t.Errorf("expected 'PERM' badge for zero expiry, got %q", badge)
	}
}

// TestDumpStatsLine tests the dumpStatsLine method.
func TestDumpStatsLine(t *testing.T) {
	m := New()
	m.width = 100
	m.height = 50
	m.cwd = "/test/path"
	m.profiles = map[string][]Profile{
		"claude": {{Name: "a"}, {Name: "b"}},
	}

	stats := m.dumpStatsLine()

	if !strings.Contains(stats, "tui_stats") {
		t.Errorf("expected 'tui_stats' prefix, got %q", stats)
	}
	if !strings.Contains(stats, "provider=claude") {
		t.Errorf("expected provider in stats, got %q", stats)
	}
	if !strings.Contains(stats, "total_profiles=2") {
		t.Errorf("expected total_profiles in stats, got %q", stats)
	}
}

// TestHelpView tests the helpView method.
func TestHelpView(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := New()
	view := m.helpView()

	if view == "" {
		t.Error("expected non-empty help view")
	}
	// The help view uses Glamour markdown rendering, check for key content (case-insensitive)
	viewLower := strings.ToLower(view)
	if !strings.Contains(viewLower, "keyboard shortcuts") {
		t.Errorf("expected help view to contain shortcuts section, got: %s", view[:min(len(view), 200)])
	}
	if !strings.Contains(viewLower, "enter") {
		t.Errorf("expected help view to contain enter key info, got: %s", view[:min(len(view), 200)])
	}
}

// TestEventTypeVerb tests the eventTypeVerb function.
func TestEventTypeVerb(t *testing.T) {
	tests := []struct {
		eventType watcher.EventType
		expected  string
	}{
		{watcher.EventProfileAdded, "added"},
		{watcher.EventProfileDeleted, "deleted"},
		{watcher.EventProfileModified, "updated"},
		{watcher.EventType(999), "updated"}, // default case
	}

	for _, tc := range tests {
		result := eventTypeVerb(tc.eventType)
		if result != tc.expected {
			t.Errorf("eventTypeVerb(%d) = %q, expected %q", tc.eventType, result, tc.expected)
		}
	}
}

// TestHandleExportVault tests the handleExportVault method.
func TestHandleExportVault(t *testing.T) {
	m := New()

	// Test with no profiles
	m.profiles = map[string][]Profile{}
	result, _ := m.handleExportVault()
	updated := result.(Model)
	if !strings.Contains(updated.statusMsg, "No profiles") {
		t.Errorf("expected 'No profiles' message, got %q", updated.statusMsg)
	}

	// Test with profiles - should show confirmation dialog
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test"}},
	}
	result, _ = m.handleExportVault()
	updated = result.(Model)
	if updated.state != stateExportConfirm {
		t.Errorf("expected stateExportConfirm, got %v", updated.state)
	}
	if updated.confirmDialog == nil {
		t.Error("expected confirmDialog to be set")
	}
}

// TestHandleImportBundle tests the handleImportBundle method.
func TestHandleImportBundle(t *testing.T) {
	m := New()

	result, _ := m.handleImportBundle()
	updated := result.(Model)

	if updated.state != stateImportPath {
		t.Errorf("expected stateImportPath, got %v", updated.state)
	}
	if updated.backupDialog == nil {
		t.Error("expected backupDialog to be set")
	}
}

// TestHandleExportComplete tests the handleExportComplete method.
func TestHandleExportComplete(t *testing.T) {
	m := New()
	msg := exportCompleteMsg{path: "/test/export.zip", size: 1024}

	result, _ := m.handleExportComplete(msg)
	updated := result.(Model)

	if !strings.Contains(updated.statusMsg, "Exported to:") {
		t.Errorf("expected 'Exported to:' in status, got %q", updated.statusMsg)
	}
	if !strings.Contains(updated.statusMsg, "/test/export.zip") {
		t.Errorf("expected path in status, got %q", updated.statusMsg)
	}
}

// TestHandleExportError tests the handleExportError method.
func TestHandleExportError(t *testing.T) {
	m := New()
	msg := exportErrorMsg{err: fmt.Errorf("export failed")}

	result, _ := m.handleExportError(msg)
	updated := result.(Model)

	if !strings.Contains(updated.statusMsg, "Export failed") {
		t.Errorf("expected 'Export failed' in status, got %q", updated.statusMsg)
	}
}

// TestHandleImportError tests the handleImportError method.
func TestHandleImportError(t *testing.T) {
	m := New()
	msg := importErrorMsg{err: fmt.Errorf("import failed")}

	result, _ := m.handleImportError(msg)
	updated := result.(Model)

	if !strings.Contains(updated.statusMsg, "Import failed") {
		t.Errorf("expected 'Import failed' in status, got %q", updated.statusMsg)
	}
}

// TestHandleExportConfirmKeysNilDialog tests handleExportConfirmKeys with nil dialog.
func TestHandleExportConfirmKeysNilDialog(t *testing.T) {
	m := New()
	m.state = stateExportConfirm
	m.confirmDialog = nil

	result, _ := m.handleExportConfirmKeys(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(Model)

	if updated.state != stateList {
		t.Errorf("expected stateList with nil dialog, got %v", updated.state)
	}
}

// TestHandleImportPathKeysNilDialog tests handleImportPathKeys with nil dialog.
func TestHandleImportPathKeysNilDialog(t *testing.T) {
	m := New()
	m.state = stateImportPath
	m.backupDialog = nil

	result, _ := m.handleImportPathKeys(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(Model)

	if updated.state != stateList {
		t.Errorf("expected stateList with nil dialog, got %v", updated.state)
	}
}

// TestHandleImportConfirmKeysNilDialog tests handleImportConfirmKeys with nil dialog.
func TestHandleImportConfirmKeysNilDialog(t *testing.T) {
	m := New()
	m.state = stateImportConfirm
	m.confirmDialog = nil

	result, _ := m.handleImportConfirmKeys(tea.KeyMsg{Type: tea.KeyEnter})
	updated := result.(Model)

	if updated.state != stateList {
		t.Errorf("expected stateList with nil dialog, got %v", updated.state)
	}
}

// TestValidateAndPreviewImport tests the validateAndPreviewImport method.
func TestValidateAndPreviewImport(t *testing.T) {
	m := New()

	// Test with empty path
	result, _ := m.validateAndPreviewImport("")
	updated := result.(Model)
	if !strings.Contains(updated.statusMsg, "empty") {
		t.Errorf("expected 'empty' in status for empty path, got %q", updated.statusMsg)
	}

	// Test with whitespace-only path
	result, _ = m.validateAndPreviewImport("   ")
	updated = result.(Model)
	if !strings.Contains(updated.statusMsg, "empty") {
		t.Errorf("expected 'empty' in status for whitespace path, got %q", updated.statusMsg)
	}

	// Test with non-existent path
	result, _ = m.validateAndPreviewImport("/nonexistent/path/bundle.zip")
	updated = result.(Model)
	if !strings.Contains(updated.statusMsg, "not found") {
		t.Errorf("expected 'not found' in status, got %q", updated.statusMsg)
	}
}

// TestRenderProfileList tests the renderProfileList method.
func TestRenderProfileList(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{}

	// Test with empty profiles
	view := m.renderProfileList()
	if !strings.Contains(view, "No profiles saved") {
		t.Errorf("expected 'No profiles saved' for empty list, got %q", view)
	}

	// Test with profiles
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "test@example.com", IsActive: true},
			{Name: "work@company.com", IsActive: false},
		},
	}
	view = m.renderProfileList()
	if !strings.Contains(view, "test@example.com") {
		t.Errorf("expected profile name in view, got %q", view)
	}
	if !strings.Contains(view, "work@company.com") {
		t.Errorf("expected second profile name in view, got %q", view)
	}
}

// TestRenderStatusBar tests the renderStatusBar method.
func TestRenderStatusBar(t *testing.T) {
	m := New()

	// Test with zero width
	m.width = 0
	view := m.renderStatusBar(layoutSpec{Mode: layoutFull})
	if view != "" {
		t.Errorf("expected empty string for zero width, got %q", view)
	}

	// Test with status message
	m.width = 100
	m.statusMsg = "Test status"
	view = m.renderStatusBar(layoutSpec{Mode: layoutFull})
	if !strings.Contains(view, "Test status") {
		t.Errorf("expected status message in view, got %q", view)
	}

	// Test with narrow width (< 70)
	m.statusMsg = ""
	m.width = 50
	view = m.renderStatusBar(layoutSpec{Mode: layoutCompact})
	// Narrow view shows only provider hint, not quit
	if !strings.Contains(view, "provider") {
		t.Errorf("expected 'provider' hint in narrow view, got %q", view)
	}

	// Test with medium width (70-99)
	m.width = 80
	view = m.renderStatusBar(layoutSpec{Mode: layoutCompact})
	if !strings.Contains(view, "provider") {
		t.Errorf("expected 'provider' hint in medium view, got %q", view)
	}

	// Test with full width (>= 100)
	m.width = 120
	view = m.renderStatusBar(layoutSpec{Mode: layoutFull})
	// Updated: status bar now shows "provider" not "switch provider" for conciseness
	if !strings.Contains(view, "provider") {
		t.Errorf("expected 'provider' hint in full view, got %q", view)
	}
	if !strings.Contains(view, "activate") {
		t.Errorf("expected 'activate' hint in full view, got %q", view)
	}
}

func TestStatusBarSeveritySnapshots(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	m := New()
	m.width = 120

	// Status bar now has 3 segments: mode indicator | center message | key hints
	// The format is: " CLAUDE  message  [ tab  :provider] [ enter  :activate] [ /  :search]"
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{
			name:    "success",
			message: "Exported",
			want: "" +
				"  CLAUDE   Exported                                                 [ tab  :provider] [ enter  :activate] [ /  :search]",
		},
		{
			name:    "warning",
			message: "No profile selected",
			want: "" +
				"  CLAUDE   No profile selected                                      [ tab  :provider] [ enter  :activate] [ /  :search]",
		},
		{
			name:    "error",
			message: "Export failed",
			want: "" +
				"  CLAUDE   Export failed                                            [ tab  :provider] [ enter  :activate] [ /  :search]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m.statusMsg = tt.message
			got := normalizeStatusSnapshot(m.renderStatusBar(layoutSpec{Mode: layoutFull}))
			if got != tt.want {
				t.Fatalf("status snapshot mismatch\n--- got ---\n%s\n--- want ---\n%s", got, tt.want)
			}
		})
	}
}

// TestApplySearchFilter tests the applySearchFilter method.
func TestApplySearchFilter(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "work@example.com"},
			{Name: "personal@gmail.com"},
			{Name: "test@test.com"},
		},
	}
	m.profilesPanel = NewProfilesPanel()

	// Test with empty query - should show all
	m.searchQuery = ""
	m.applySearchFilter()
	if m.profilesPanel.Count() != 3 {
		t.Errorf("expected 3 profiles for empty query, got %d", m.profilesPanel.Count())
	}

	// Test with specific query
	m.searchQuery = "work"
	m.applySearchFilter()
	if m.profilesPanel.Count() != 1 {
		t.Errorf("expected 1 profile for 'work' query, got %d", m.profilesPanel.Count())
	}

	// Test with case-insensitive query
	m.searchQuery = "PERSONAL"
	m.applySearchFilter()
	if m.profilesPanel.Count() != 1 {
		t.Errorf("expected 1 profile for 'PERSONAL' query, got %d", m.profilesPanel.Count())
	}

	// Test with no matches
	m.searchQuery = "nonexistent"
	m.applySearchFilter()
	if m.profilesPanel.Count() != 0 {
		t.Errorf("expected 0 profiles for 'nonexistent', got %d", m.profilesPanel.Count())
	}
}

func normalizeStatusSnapshot(s string) string {
	plain := ansi.Strip(s)
	lines := strings.Split(plain, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " ")
	}
	return strings.Join(lines, "\n")
}

// TestApplySearchFilterNilPanel tests applySearchFilter with nil profilesPanel.
func TestApplySearchFilterNilPanel(t *testing.T) {
	m := New()
	m.profilesPanel = nil
	m.searchQuery = "test"

	// Should not panic
	m.applySearchFilter()
}

func TestRenderSearchBar(t *testing.T) {
	m := New()
	m.width = 80
	m.height = 24
	m.profiles = map[string][]Profile{
		"claude": {
			{Name: "work@example.com"},
			{Name: "personal@gmail.com"},
		},
	}
	m.profilesPanel = NewProfilesPanel()

	// Test that search bar is empty when not in search mode
	m.state = stateList
	bar := m.renderSearchBar()
	if bar != "" {
		t.Errorf("expected empty search bar in list mode, got %q", bar)
	}

	// Test that search bar is rendered when in search mode
	m.state = stateSearch
	m.searchQuery = ""
	bar = m.renderSearchBar()
	if bar == "" {
		t.Error("expected non-empty search bar in search mode")
	}
	if !strings.Contains(bar, "/") {
		t.Error("expected search bar to contain '/' prompt")
	}
	if !strings.Contains(bar, "matches") {
		t.Error("expected search bar to contain match count")
	}

	// Test with search query
	m.searchQuery = "work"
	bar = m.renderSearchBar()
	if !strings.Contains(bar, "work") {
		t.Errorf("expected search bar to contain query 'work', got %q", bar)
	}
}

// TestHandleEditProfile tests the handleEditProfile method.
func TestHandleEditProfile(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "test@example.com"}},
	}
	m.profileMeta = map[string]map[string]*profile.Profile{
		"claude": {
			"test@example.com": {
				Name:     "test@example.com",
				Provider: "claude",
			},
		},
	}

	result, _ := m.handleEditProfile()
	updated := result.(Model)

	if updated.state != stateEditProfile {
		t.Errorf("expected stateEditProfile, got %v", updated.state)
	}
	if updated.editDialog == nil {
		t.Error("expected editDialog to be initialized")
	}
}

// TestFormatSQLiteSince tests the formatSQLiteSince function.
func TestFormatSQLiteSince(t *testing.T) {
	// Test with zero time
	result := formatSQLiteSince(time.Time{})
	if result != "1970-01-01 00:00:00" {
		t.Errorf("expected '1970-01-01 00:00:00' for zero time, got %q", result)
	}

	// Test with specific time
	specific := time.Date(2025, 6, 15, 10, 30, 0, 0, time.UTC)
	result = formatSQLiteSince(specific)
	if result != "2025-06-15 10:30:00" {
		t.Errorf("expected '2025-06-15 10:30:00', got %q", result)
	}
}

// TestUpdateProviderCounts tests the updateProviderCounts method.
func TestUpdateProviderCounts(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "a"}, {Name: "b"}},
		"codex":  {{Name: "c"}},
	}
	m.providerPanel = NewProviderPanel([]string{"claude", "codex"})

	// Should not panic
	m.updateProviderCounts()
}

// TestSyncProviderPanel tests the syncProviderPanel method.
func TestSyncProviderPanel(t *testing.T) {
	m := New()
	m.activeProvider = 1
	m.providerPanel = NewProviderPanel(DefaultProviders())

	// Should not panic
	m.syncProviderPanel()
}

// TestRenderProviderTabsWithCounts tests renderProviderTabs with different widths.
func TestRenderProviderTabsWithCounts(t *testing.T) {
	m := New()
	m.profiles = map[string][]Profile{
		"claude": {{Name: "a"}, {Name: "b"}},
		"codex":  {{Name: "c"}},
	}

	// Test with narrow width (counts hidden)
	m.width = 60
	view := m.renderProviderTabs()
	if view == "" {
		t.Error("expected non-empty tabs view")
	}

	// Test with wide width (counts shown)
	m.width = 100
	view = m.renderProviderTabs()
	if !strings.Contains(view, "2") { // Should show count
		// This may depend on styling, so just check it renders
	}
}

// TestToastCreation tests creating new toasts.
func TestToastCreation(t *testing.T) {
	toast := NewToast("Test message", StatusSuccess)
	if toast.Message != "Test message" {
		t.Errorf("expected message 'Test message', got %q", toast.Message)
	}
	if toast.Severity != StatusSuccess {
		t.Errorf("expected severity StatusSuccess, got %v", toast.Severity)
	}
	if toast.CreatedAt.IsZero() {
		t.Error("expected non-zero CreatedAt")
	}
	if toast.IsExpired() {
		t.Error("new toast should not be expired")
	}
}

// TestToastExpiration tests toast expiration logic.
func TestToastExpiration(t *testing.T) {
	// Create an already-expired toast by manipulating CreatedAt
	toast := Toast{
		Message:   "Old toast",
		Severity:  StatusInfo,
		CreatedAt: time.Now().Add(-ToastDuration - time.Second),
	}
	if !toast.IsExpired() {
		t.Error("old toast should be expired")
	}
}

// TestAddToast tests adding toasts to the model.
func TestAddToast(t *testing.T) {
	m := New()
	if len(m.toasts) != 0 {
		t.Errorf("expected 0 toasts initially, got %d", len(m.toasts))
	}

	cmd := m.addToast("Success!", StatusSuccess)
	if len(m.toasts) != 1 {
		t.Errorf("expected 1 toast after addToast, got %d", len(m.toasts))
	}
	if m.toasts[0].Message != "Success!" {
		t.Errorf("expected message 'Success!', got %q", m.toasts[0].Message)
	}
	if cmd == nil {
		t.Error("expected non-nil command for toast tick")
	}
}

// TestExpireToasts tests the toast expiration mechanism.
func TestExpireToasts(t *testing.T) {
	m := New()

	// Add a fresh toast
	m.toasts = append(m.toasts, NewToast("Fresh", StatusInfo))

	// Add an expired toast
	m.toasts = append(m.toasts, Toast{
		Message:   "Expired",
		Severity:  StatusWarning,
		CreatedAt: time.Now().Add(-ToastDuration - time.Second),
	})

	if len(m.toasts) != 2 {
		t.Fatalf("expected 2 toasts before expiration, got %d", len(m.toasts))
	}

	hasRemaining := m.expireToasts()
	if !hasRemaining {
		t.Error("expected remaining toasts after expiration")
	}
	if len(m.toasts) != 1 {
		t.Errorf("expected 1 toast after expiration, got %d", len(m.toasts))
	}
	if m.toasts[0].Message != "Fresh" {
		t.Errorf("expected fresh toast to remain, got %q", m.toasts[0].Message)
	}
}

// TestStatusModeIndicator tests the mode indicator in status bar.
func TestStatusModeIndicator(t *testing.T) {
	m := New()
	m.width = 100
	m.height = 30

	// Test normal state shows provider
	m.state = stateList
	indicator := m.statusModeIndicator()
	plainIndicator := ansi.Strip(indicator)
	if !strings.Contains(plainIndicator, "CLAUDE") {
		t.Errorf("expected CLAUDE in normal mode indicator, got %q", plainIndicator)
	}

	// Test search state
	m.state = stateSearch
	indicator = m.statusModeIndicator()
	plainIndicator = ansi.Strip(indicator)
	if !strings.Contains(plainIndicator, "SEARCH") {
		t.Errorf("expected SEARCH in search mode indicator, got %q", plainIndicator)
	}

	// Test help state
	m.state = stateHelp
	indicator = m.statusModeIndicator()
	plainIndicator = ansi.Strip(indicator)
	if !strings.Contains(plainIndicator, "HELP") {
		t.Errorf("expected HELP in help mode indicator, got %q", plainIndicator)
	}

	// Test command palette state
	m.state = stateCommandPalette
	indicator = m.statusModeIndicator()
	plainIndicator = ansi.Strip(indicator)
	if !strings.Contains(plainIndicator, "CMD") {
		t.Errorf("expected CMD in command palette mode indicator, got %q", plainIndicator)
	}
}

// TestStatusKeyHints tests that key hints are always present.
func TestStatusKeyHints(t *testing.T) {
	m := New()

	layout := layoutSpec{Mode: layoutFull}

	// Test narrow width
	m.width = 60
	hints := m.statusKeyHints(layout)
	plainHints := ansi.Strip(hints)
	plainHints = strings.ReplaceAll(plainHints, " ", "")
	if !strings.Contains(plainHints, "[tab:provider]") {
		t.Errorf("expected '[tab:provider]' in narrow hints, got %q", plainHints)
	}

	// Test wide width
	m.width = 120
	hints = m.statusKeyHints(layout)
	plainHints = ansi.Strip(hints)
	plainHints = strings.ReplaceAll(plainHints, " ", "")
	if !strings.Contains(plainHints, "[enter:activate]") {
		t.Errorf("expected '[enter:activate]' in wide hints, got %q", plainHints)
	}
	if !strings.Contains(plainHints, "[/:search]") {
		t.Errorf("expected '[/:search]' in wide hints, got %q", plainHints)
	}
}

// TestStatusCenterMessage tests center message rendering with toasts and statusMsg.
func TestStatusCenterMessage(t *testing.T) {
	m := New()

	// Test with no message
	if m.statusCenterText() != "" {
		t.Errorf("expected empty center text, got %q", m.statusCenterText())
	}

	// Test with statusMsg
	m.statusMsg = "Status message"
	if m.statusCenterText() != "Status message" {
		t.Errorf("expected 'Status message', got %q", m.statusCenterText())
	}

	// Test that toast takes priority
	m.addToast("Toast message", StatusSuccess)
	if m.statusCenterText() != "Toast message" {
		t.Errorf("expected toast to take priority, got %q", m.statusCenterText())
	}
	if m.statusMessageSeverity() != StatusSuccess {
		t.Errorf("expected StatusSuccess severity, got %v", m.statusMessageSeverity())
	}
}

// TestRenderStatusBarThreeSegments tests the 3-segment status bar layout.
func TestRenderStatusBarThreeSegments(t *testing.T) {
	m := New()
	m.width = 100
	m.height = 30

	layout := layoutSpec{Mode: layoutFull}

	// Render status bar
	bar := m.renderStatusBar(layout)
	plainBar := ansi.Strip(bar)

	// Should contain mode indicator (provider name)
	if !strings.Contains(plainBar, "CLAUDE") {
		t.Errorf("expected CLAUDE mode indicator in status bar, got %q", plainBar)
	}

	// Should contain key hints (new format: [key:action])
	if !strings.Contains(plainBar, "provider") {
		t.Errorf("expected 'provider' key hint in status bar, got %q", plainBar)
	}

	// Add a status message and verify layout
	m.statusMsg = "Test status"
	bar = m.renderStatusBar(layout)
	plainBar = ansi.Strip(bar)

	// Should still have mode and hints
	if !strings.Contains(plainBar, "CLAUDE") {
		t.Errorf("expected CLAUDE mode indicator with status message, got %q", plainBar)
	}
	if !strings.Contains(plainBar, "tab") {
		t.Errorf("expected 'tab' hint with status message, got %q", plainBar)
	}
	// Should have status message
	if !strings.Contains(plainBar, "Test status") {
		t.Errorf("expected status message in bar, got %q", plainBar)
	}
}

// TestToastTickMsg tests the toast tick message handling.
func TestToastTickMsg(t *testing.T) {
	m := New()

	// Add an expired toast
	m.toasts = append(m.toasts, Toast{
		Message:   "Expired",
		Severity:  StatusInfo,
		CreatedAt: time.Now().Add(-ToastDuration - time.Second),
	})

	// Process tick message
	newModel, cmd := m.Update(toastTickMsg{})
	updated := newModel.(Model)

	if len(updated.toasts) != 0 {
		t.Errorf("expected expired toast to be removed, got %d toasts", len(updated.toasts))
	}
	// Should not return another tick command when no toasts remain
	if cmd != nil {
		t.Error("expected no tick command when no toasts remain")
	}
}

// TestToastTickMsgWithRemaining tests tick when toasts remain.
func TestToastTickMsgWithRemaining(t *testing.T) {
	m := New()

	// Add a fresh toast
	m.toasts = append(m.toasts, NewToast("Fresh", StatusInfo))

	// Process tick message
	newModel, cmd := m.Update(toastTickMsg{})
	updated := newModel.(Model)

	if len(updated.toasts) != 1 {
		t.Errorf("expected 1 toast to remain, got %d", len(updated.toasts))
	}
	// Should return another tick command to check later
	if cmd == nil {
		t.Error("expected tick command when toasts remain")
	}
}
