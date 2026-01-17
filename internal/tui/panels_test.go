package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// =============================================================================
// provider_panel.go Tests
// =============================================================================

func TestDefaultProviderPanelStyles(t *testing.T) {
	styles := DefaultProviderPanelStyles()

	// All style fields should render non-empty output
	tests := []struct {
		name  string
		style func() string
	}{
		{"Border", func() string { return styles.Border.Render("test") }},
		{"Title", func() string { return styles.Title.Render("test") }},
		{"Item", func() string { return styles.Item.Render("test") }},
		{"SelectedItem", func() string { return styles.SelectedItem.Render("test") }},
		{"Count", func() string { return styles.Count.Render("test") }},
		{"ActiveIndicator", func() string { return styles.ActiveIndicator.Render("test") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.style()
			if result == "" {
				t.Errorf("%s style should render non-empty output", tt.name)
			}
		})
	}
}

func TestNewProviderPanel(t *testing.T) {
	providers := []string{"claude", "codex", "gemini"}
	panel := NewProviderPanel(providers)

	if panel == nil {
		t.Fatal("NewProviderPanel returned nil")
	}

	if len(panel.providers) != 3 {
		t.Errorf("Expected 3 providers, got %d", len(panel.providers))
	}

	if panel.profileCounts == nil {
		t.Error("profileCounts should be initialized")
	}
}

func TestProviderPanel_SetActiveProvider(t *testing.T) {
	panel := NewProviderPanel([]string{"claude", "codex", "gemini"})

	// Valid indices
	panel.SetActiveProvider(1)
	if panel.activeProvider != 1 {
		t.Errorf("Expected activeProvider=1, got %d", panel.activeProvider)
	}

	panel.SetActiveProvider(2)
	if panel.activeProvider != 2 {
		t.Errorf("Expected activeProvider=2, got %d", panel.activeProvider)
	}

	// Invalid indices should not change the value
	panel.SetActiveProvider(-1)
	if panel.activeProvider != 2 {
		t.Errorf("Negative index should not change activeProvider")
	}

	panel.SetActiveProvider(10)
	if panel.activeProvider != 2 {
		t.Errorf("Out of bounds index should not change activeProvider")
	}
}

func TestProviderPanel_SetProfileCounts(t *testing.T) {
	panel := NewProviderPanel([]string{"claude", "codex", "gemini"})

	counts := map[string]int{
		"claude": 5,
		"codex":  3,
		"gemini": 2,
	}
	panel.SetProfileCounts(counts)

	if panel.profileCounts["claude"] != 5 {
		t.Errorf("Expected claude count=5, got %d", panel.profileCounts["claude"])
	}
}

func TestProviderPanel_SetSize(t *testing.T) {
	panel := NewProviderPanel([]string{"claude"})
	panel.SetSize(100, 50)

	if panel.width != 100 {
		t.Errorf("Expected width=100, got %d", panel.width)
	}
	if panel.height != 50 {
		t.Errorf("Expected height=50, got %d", panel.height)
	}
}

func TestProviderPanel_View(t *testing.T) {
	panel := NewProviderPanel([]string{"claude", "codex", "gemini"})
	panel.SetProfileCounts(map[string]int{
		"claude": 5,
		"codex":  3,
		"gemini": 2,
	})

	view := panel.View()

	// Should contain provider names (capitalized)
	if !strings.Contains(view, "Claude") {
		t.Error("View should contain 'Claude'")
	}
	if !strings.Contains(view, "Codex") {
		t.Error("View should contain 'Codex'")
	}
	if !strings.Contains(view, "Gemini") {
		t.Error("View should contain 'Gemini'")
	}

	// Should contain counts
	if !strings.Contains(view, "(5)") {
		t.Error("View should contain claude count '(5)'")
	}
}

func TestProviderPanel_View_WithWidth(t *testing.T) {
	panel := NewProviderPanel([]string{"claude"})
	panel.SetSize(40, 20)

	view := panel.View()
	if view == "" {
		t.Error("View with width should render non-empty output")
	}
}

func TestCapitalizeFirst_ProviderPanel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"claude", "Claude"},
		{"codex", "Codex"},
		{"gemini", "Gemini"},
		{"", ""},
		{"a", "A"},
		{"ABC", "ABC"},
		{"über", "Über"}, // Unicode test
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := capitalizeFirst(tt.input)
			if got != tt.want {
				t.Errorf("capitalizeFirst(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// =============================================================================
// detail_panel.go Tests
// =============================================================================

func TestDefaultDetailPanelStyles(t *testing.T) {
	styles := DefaultDetailPanelStyles()

	tests := []struct {
		name  string
		style func() string
	}{
		{"Border", func() string { return styles.Border.Render("test") }},
		{"Title", func() string { return styles.Title.Render("test") }},
		{"Label", func() string { return styles.Label.Render("test") }},
		{"Value", func() string { return styles.Value.Render("test") }},
		{"StatusOK", func() string { return styles.StatusOK.Render("test") }},
		{"StatusWarn", func() string { return styles.StatusWarn.Render("test") }},
		{"StatusBad", func() string { return styles.StatusBad.Render("test") }},
		{"StatusMuted", func() string { return styles.StatusMuted.Render("test") }},
		{"LockIcon", func() string { return styles.LockIcon.Render("test") }},
		{"Divider", func() string { return styles.Divider.Render("test") }},
		{"ActionHeader", func() string { return styles.ActionHeader.Render("test") }},
		{"ActionKey", func() string { return styles.ActionKey.Render("test") }},
		{"ActionDesc", func() string { return styles.ActionDesc.Render("test") }},
		{"Empty", func() string { return styles.Empty.Render("test") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.style()
			if result == "" {
				t.Errorf("%s style should render non-empty output", tt.name)
			}
		})
	}
}

func TestNewDetailPanel(t *testing.T) {
	panel := NewDetailPanel()
	if panel == nil {
		t.Fatal("NewDetailPanel returned nil")
	}
}

func TestDetailPanel_SetProfile(t *testing.T) {
	panel := NewDetailPanel()

	profile := &DetailInfo{
		Name:     "test@example.com",
		Provider: "claude",
		AuthMode: "OAuth",
	}
	panel.SetProfile(profile)

	if panel.profile != profile {
		t.Error("SetProfile should set the profile")
	}
}

func TestDetailPanel_SetSize(t *testing.T) {
	panel := NewDetailPanel()
	panel.SetSize(80, 40)

	if panel.width != 80 {
		t.Errorf("Expected width=80, got %d", panel.width)
	}
	if panel.height != 40 {
		t.Errorf("Expected height=40, got %d", panel.height)
	}
}

func TestDetailPanel_View_NoProfile(t *testing.T) {
	panel := NewDetailPanel()
	view := panel.View()

	if !strings.Contains(view, "Select a profile") {
		t.Error("Empty detail panel should show 'Select a profile' message")
	}
}

func TestDetailPanel_View_WithProfile(t *testing.T) {
	panel := NewDetailPanel()
	panel.SetSize(80, 40)

	profile := &DetailInfo{
		Name:         "test@example.com",
		Provider:     "claude",
		AuthMode:     "OAuth",
		HealthStatus: health.StatusHealthy,
		TokenExpiry:  time.Now().Add(2 * time.Hour),
		ErrorCount:   0,
		Path:         "/home/user/.claude/auth.json",
		CreatedAt:    time.Now().Add(-24 * time.Hour),
		LastUsedAt:   time.Now().Add(-1 * time.Hour),
		Account:      "user@example.com",
	}
	panel.SetProfile(profile)

	view := panel.View()

	// Should contain profile name
	if !strings.Contains(view, "test@example.com") {
		t.Error("View should contain profile name")
	}

	// Should contain provider
	if !strings.Contains(view, "Claude") {
		t.Error("View should contain provider name")
	}

	// Should contain actions
	if !strings.Contains(view, "Actions") {
		t.Error("View should contain Actions section")
	}
}

func TestDetailPanel_View_WithErrors(t *testing.T) {
	panel := NewDetailPanel()
	panel.SetSize(80, 40)

	profile := &DetailInfo{
		Name:         "test@example.com",
		Provider:     "codex",
		AuthMode:     "API Key",
		HealthStatus: health.StatusWarning,
		ErrorCount:   5,
	}
	panel.SetProfile(profile)

	view := panel.View()

	// Should show error count
	if !strings.Contains(view, "5 in last hour") {
		t.Error("View should show error count")
	}
}

func TestDetailPanel_View_Locked(t *testing.T) {
	panel := NewDetailPanel()
	panel.SetSize(80, 40)

	profile := &DetailInfo{
		Name:     "locked@example.com",
		Provider: "claude",
		Locked:   true,
	}
	panel.SetProfile(profile)

	view := panel.View()

	// Should show lock indicator
	if !strings.Contains(view, "Locked") {
		t.Error("View should show locked status")
	}
}

func TestFormatDurationFull(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Second, "less than a minute"},
		{5 * time.Minute, "5 minutes"},
		{1 * time.Hour, "1 hours 0 minutes"},
		{2*time.Hour + 30*time.Minute, "2 hours 30 minutes"},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := formatDurationFull(tt.duration)
			if got != tt.want {
				t.Errorf("formatDurationFull(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

// =============================================================================
// profiles_panel.go Tests
// =============================================================================

func TestDefaultProfilesPanelStyles(t *testing.T) {
	styles := DefaultProfilesPanelStyles()

	tests := []struct {
		name  string
		style func() string
	}{
		{"Border", func() string { return styles.Border.Render("test") }},
		{"Title", func() string { return styles.Title.Render("test") }},
		{"Header", func() string { return styles.Header.Render("test") }},
		{"Row", func() string { return styles.Row.Render("test") }},
		{"SelectedRow", func() string { return styles.SelectedRow.Render("test") }},
		{"ActiveIndicator", func() string { return styles.ActiveIndicator.Render("test") }},
		{"StatusOK", func() string { return styles.StatusOK.Render("test") }},
		{"StatusWarn", func() string { return styles.StatusWarn.Render("test") }},
		{"StatusBad", func() string { return styles.StatusBad.Render("test") }},
		{"StatusMuted", func() string { return styles.StatusMuted.Render("test") }},
		{"LockIcon", func() string { return styles.LockIcon.Render("test") }},
		{"ProjectBadge", func() string { return styles.ProjectBadge.Render("test") }},
		{"Empty", func() string { return styles.Empty.Render("test") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.style()
			if result == "" {
				t.Errorf("%s style should render non-empty output", tt.name)
			}
		})
	}
}

func TestProfilesPanelStyles_StatusStyle(t *testing.T) {
	styles := DefaultProfilesPanelStyles()

	tests := []struct {
		status health.HealthStatus
	}{
		{health.StatusHealthy},
		{health.StatusWarning},
		{health.StatusCritical},
		{health.StatusUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.status.String(), func(t *testing.T) {
			style := styles.StatusStyle(tt.status)
			result := style.Render("test")
			if result == "" {
				t.Errorf("StatusStyle(%v) should render non-empty output", tt.status)
			}
		})
	}
}

func TestNewProfilesPanel(t *testing.T) {
	panel := NewProfilesPanel()
	if panel == nil {
		t.Fatal("NewProfilesPanel returned nil")
	}
	if panel.profiles == nil {
		t.Error("profiles should be initialized")
	}
}

func TestProfilesPanel_SetProvider(t *testing.T) {
	panel := NewProfilesPanel()
	panel.SetProvider("claude")

	if panel.provider != "claude" {
		t.Errorf("Expected provider='claude', got %q", panel.provider)
	}
}

func TestProfilesPanel_SetProfiles(t *testing.T) {
	panel := NewProfilesPanel()

	now := time.Now()
	profiles := []ProfileInfo{
		{Name: "newer", LastUsed: now.Add(-1 * time.Hour)},
		{Name: "older", LastUsed: now.Add(-2 * time.Hour)},
		{Name: "oldest", LastUsed: now.Add(-3 * time.Hour)},
	}

	panel.SetProfiles(profiles)

	// Should be sorted by last used (most recent first)
	if panel.profiles[0].Name != "newer" {
		t.Errorf("Expected first profile='newer', got %q", panel.profiles[0].Name)
	}
	if panel.profiles[2].Name != "oldest" {
		t.Errorf("Expected last profile='oldest', got %q", panel.profiles[2].Name)
	}
}

func TestProfilesPanel_SetProfiles_ResetsSelection(t *testing.T) {
	panel := NewProfilesPanel()
	panel.selected = 5 // Set to out-of-bounds value

	profiles := []ProfileInfo{
		{Name: "profile1"},
		{Name: "profile2"},
	}
	panel.SetProfiles(profiles)

	// Selection should be reset to valid range
	if panel.selected >= len(profiles) {
		t.Errorf("Selection should be reset to valid range, got %d", panel.selected)
	}
}

func TestProfilesPanel_Selection(t *testing.T) {
	panel := NewProfilesPanel()
	panel.SetProfiles([]ProfileInfo{
		{Name: "p1"},
		{Name: "p2"},
		{Name: "p3"},
	})

	// Initial selection
	if panel.GetSelected() != 0 {
		t.Errorf("Initial selection should be 0, got %d", panel.GetSelected())
	}

	// Set selection
	panel.SetSelected(1)
	if panel.GetSelected() != 1 {
		t.Errorf("Expected selection=1, got %d", panel.GetSelected())
	}

	// Invalid selection should not change
	panel.SetSelected(-1)
	if panel.GetSelected() != 1 {
		t.Error("Negative index should not change selection")
	}

	panel.SetSelected(10)
	if panel.GetSelected() != 1 {
		t.Error("Out of bounds index should not change selection")
	}
}

func TestProfilesPanel_GetSelectedProfile(t *testing.T) {
	panel := NewProfilesPanel()

	// Empty panel
	if panel.GetSelectedProfile() != nil {
		t.Error("Empty panel should return nil")
	}

	// With profiles
	panel.SetProfiles([]ProfileInfo{
		{Name: "p1"},
		{Name: "p2"},
	})
	panel.SetSelected(1)

	profile := panel.GetSelectedProfile()
	if profile == nil {
		t.Fatal("GetSelectedProfile should return a profile")
	}
	if profile.Name != "p2" {
		t.Errorf("Expected profile 'p2', got %q", profile.Name)
	}
}

func TestProfilesPanel_MoveUpDown(t *testing.T) {
	panel := NewProfilesPanel()
	panel.SetProfiles([]ProfileInfo{
		{Name: "p1"},
		{Name: "p2"},
		{Name: "p3"},
	})

	// Move down
	panel.MoveDown()
	if panel.GetSelected() != 1 {
		t.Errorf("Expected selection=1 after MoveDown, got %d", panel.GetSelected())
	}

	panel.MoveDown()
	if panel.GetSelected() != 2 {
		t.Errorf("Expected selection=2 after second MoveDown, got %d", panel.GetSelected())
	}

	// Cannot move down past last
	panel.MoveDown()
	if panel.GetSelected() != 2 {
		t.Errorf("Should not move past last item, got %d", panel.GetSelected())
	}

	// Move up
	panel.MoveUp()
	if panel.GetSelected() != 1 {
		t.Errorf("Expected selection=1 after MoveUp, got %d", panel.GetSelected())
	}

	// Cannot move up past first
	panel.SetSelected(0)
	panel.MoveUp()
	if panel.GetSelected() != 0 {
		t.Errorf("Should not move past first item, got %d", panel.GetSelected())
	}
}

func TestProfilesPanel_SetSize(t *testing.T) {
	panel := NewProfilesPanel()
	panel.SetSize(100, 50)

	if panel.width != 100 {
		t.Errorf("Expected width=100, got %d", panel.width)
	}
	if panel.height != 50 {
		t.Errorf("Expected height=50, got %d", panel.height)
	}
}

func TestProfilesPanel_View_Empty(t *testing.T) {
	panel := NewProfilesPanel()
	panel.SetProvider("claude")

	view := panel.View()

	if !strings.Contains(view, "No profiles saved") {
		t.Error("Empty panel should show 'No profiles saved' message")
	}
}

func TestProfilesPanel_View_WithProfiles(t *testing.T) {
	panel := NewProfilesPanel()
	panel.SetProvider("claude")
	panel.SetSize(100, 50)
	panel.SetProfiles([]ProfileInfo{
		{
			Name:         "test@example.com",
			AuthMode:     "OAuth",
			HealthStatus: health.StatusHealthy,
			TokenExpiry:  time.Now().Add(2 * time.Hour),
			LastUsed:     time.Now().Add(-1 * time.Hour),
			Account:      "user@example.com",
			IsActive:     true,
		},
	})

	view := panel.View()

	// Should contain profile info (may be truncated due to column width)
	if !strings.Contains(view, "test@") {
		t.Error("View should contain profile name prefix")
	}
	if !strings.Contains(view, "OAuth") {
		t.Error("View should contain auth mode")
	}
}

func TestFormatRelativeTime_ProfilesPanel(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		time time.Time
		want string
	}{
		{"zero time", time.Time{}, "never"},
		{"just now", now.Add(-30 * time.Second), "now"},
		{"minutes ago", now.Add(-5 * time.Minute), "5m ago"},
		{"hours ago", now.Add(-3 * time.Hour), "3h ago"},
		{"days ago", now.Add(-2 * 24 * time.Hour), "2d ago"},
		{"weeks ago", now.Add(-14 * 24 * time.Hour), "2w ago"},
		{"months ago", now.Add(-60 * 24 * time.Hour), "2mo ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRelativeTime(tt.time)
			if got != tt.want {
				t.Errorf("formatRelativeTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		duration time.Duration
		want     string
	}{
		{30 * time.Minute, "30m left"},
		{2 * time.Hour, "2h left"},
		{48 * time.Hour, "2d left"},
	}

	for _, tt := range tests {
		t.Run(tt.duration.String(), func(t *testing.T) {
			got := formatDuration(tt.duration)
			if got != tt.want {
				t.Errorf("formatDuration(%v) = %q, want %q", tt.duration, got, tt.want)
			}
		})
	}
}

func TestPadRight(t *testing.T) {
	tests := []struct {
		input string
		width int
		want  string
	}{
		{"hello", 10, "hello     "},
		{"hello", 5, "hello"},
		{"hello", 3, "hello"}, // Don't truncate
		{"", 5, "     "},
		{"über", 6, "über  "}, // Unicode test
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := padRight(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("padRight(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		width int
		want  string
	}{
		{"hello world", 5, "he..."},
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hi", 3, "hi"},
		{"hi", 2, "hi"},
		{"hello", 3, "hel"},        // width <= 3, no ellipsis
		{"über test", 6, "übe..."}, // Unicode test
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncate(tt.input, tt.width)
			if got != tt.want {
				t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.width, got, tt.want)
			}
		})
	}
}

func TestFormatNameWithBadge(t *testing.T) {
	tests := []struct {
		name  string
		badge string
		width int
		want  string
	}{
		{"test", "", 10, "test"},
		{"test", "🔒", 10, "test 🔒"},
		{"longname", "🔒", 8, "lon... 🔒"}, // Truncates name to fit width with ellipsis
		{"", "🔒", 5, " 🔒"},               // Space before badge when name is empty
		{"test", "badge", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name+"/"+tt.badge, func(t *testing.T) {
			got := formatNameWithBadge(tt.name, tt.badge, tt.width)
			if got != tt.want {
				t.Errorf("formatNameWithBadge(%q, %q, %d) = %q, want %q",
					tt.name, tt.badge, tt.width, got, tt.want)
			}
		})
	}
}

func TestFormatTUIStatus(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		profile ProfileInfo
		wantSub string // Substring that should be in the result
	}{
		{
			name:    "unknown expiry",
			profile: ProfileInfo{HealthStatus: health.StatusUnknown},
			wantSub: "Unknown",
		},
		{
			name: "expired",
			profile: ProfileInfo{
				HealthStatus: health.StatusCritical,
				TokenExpiry:  now.Add(-1 * time.Hour),
			},
			wantSub: "Expired",
		},
		{
			name: "hours left",
			profile: ProfileInfo{
				HealthStatus: health.StatusHealthy,
				TokenExpiry:  now.Add(3 * time.Hour),
			},
			wantSub: "h left", // Just check for "h left" to avoid timing issues
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTUIStatus(&tt.profile)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("formatTUIStatus() = %q, want substring %q", got, tt.wantSub)
			}
		})
	}
}
