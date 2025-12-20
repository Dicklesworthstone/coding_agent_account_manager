package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
)

// =============================================================================
// sync_model.go Tests
// =============================================================================

func TestDefaultSyncPanelStyles(t *testing.T) {
	styles := DefaultSyncPanelStyles()

	// Verify all styles render non-empty output
	tests := []struct {
		name  string
		style func(...string) string
	}{
		{"Title", styles.Title.Render},
		{"StatusEnabled", styles.StatusEnabled.Render},
		{"StatusDisabled", styles.StatusDisabled.Render},
		{"Machine", styles.Machine.Render},
		{"SelectedMachine", styles.SelectedMachine.Render},
		{"StatusOnline", styles.StatusOnline.Render},
		{"StatusOffline", styles.StatusOffline.Render},
		{"StatusSyncing", styles.StatusSyncing.Render},
		{"StatusError", styles.StatusError.Render},
		{"KeyHint", styles.KeyHint.Render},
		{"Border", styles.Border.Render},
		{"Empty", styles.Empty.Render},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.style("test")
			if result == "" {
				t.Errorf("%s style should render non-empty output", tt.name)
			}
		})
	}
}

func TestNewSyncPanel(t *testing.T) {
	panel := NewSyncPanel()

	if panel == nil {
		t.Fatal("NewSyncPanel returned nil")
	}
	if panel.visible {
		t.Error("New sync panel should not be visible")
	}
	if panel.selectedIdx != 0 {
		t.Errorf("selectedIdx = %d, want 0", panel.selectedIdx)
	}
	if panel.loading {
		t.Error("New sync panel should not be loading")
	}
	if panel.syncing {
		t.Error("New sync panel should not be syncing")
	}
}

func TestSyncPanelToggle(t *testing.T) {
	panel := NewSyncPanel()

	if panel.Visible() {
		t.Error("Initial state should be not visible")
	}

	panel.Toggle()
	if !panel.Visible() {
		t.Error("After first toggle, should be visible")
	}

	panel.Toggle()
	if panel.Visible() {
		t.Error("After second toggle, should not be visible")
	}
}

func TestSyncPanelToggleNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.Toggle()
	if panel.Visible() {
		t.Error("Nil panel Visible() should return false")
	}
}

func TestSyncPanelSetSize(t *testing.T) {
	panel := NewSyncPanel()

	panel.SetSize(100, 50)

	if panel.width != 100 {
		t.Errorf("width = %d, want 100", panel.width)
	}
	if panel.height != 50 {
		t.Errorf("height = %d, want 50", panel.height)
	}
}

func TestSyncPanelSetSizeNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.SetSize(100, 50)
}

func TestSyncPanelSetLoading(t *testing.T) {
	panel := NewSyncPanel()

	if panel.loading {
		t.Error("Initial loading should be false")
	}

	panel.SetLoading(true)
	if !panel.loading {
		t.Error("After SetLoading(true), loading should be true")
	}

	panel.SetLoading(false)
	if panel.loading {
		t.Error("After SetLoading(false), loading should be false")
	}
}

func TestSyncPanelSetLoadingNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.SetLoading(true)
}

func TestSyncPanelSetSyncing(t *testing.T) {
	panel := NewSyncPanel()

	if panel.syncing {
		t.Error("Initial syncing should be false")
	}

	panel.SetSyncing(true)
	if !panel.syncing {
		t.Error("After SetSyncing(true), syncing should be true")
	}

	panel.SetSyncing(false)
	if panel.syncing {
		t.Error("After SetSyncing(false), syncing should be false")
	}
}

func TestSyncPanelSetSyncingNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.SetSyncing(true)
}

func TestSyncPanelSetState(t *testing.T) {
	panel := NewSyncPanel()

	// Test with nil state
	panel.SetState(nil)
	if panel.state != nil {
		t.Error("State should be nil after SetState(nil)")
	}
	if len(panel.machines) != 0 {
		t.Error("machines should be empty after SetState(nil)")
	}

	// Test with valid state and pool
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled:  true,
			AutoSync: true,
		},
	}
	// Add a machine to the pool
	machine := sync.NewMachine("test-machine", "192.168.1.100")
	state.Pool.AddMachine(machine)

	panel.SetState(state)
	if panel.state != state {
		t.Error("State should be set")
	}
	if len(panel.machines) != 1 {
		t.Errorf("machines length = %d, want 1", len(panel.machines))
	}
	if panel.loading {
		t.Error("loading should be false after SetState")
	}
}

func TestSyncPanelSetStateNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.SetState(&sync.SyncState{})
}

func TestSyncPanelSetStateClampIndex(t *testing.T) {
	panel := NewSyncPanel()
	panel.selectedIdx = 10 // Set to a high index

	// Set state with no machines - should clamp to 0
	panel.SetState(&sync.SyncState{Pool: &sync.MachinePool{}})

	if panel.selectedIdx != 0 {
		t.Errorf("selectedIdx = %d, want 0 (clamped)", panel.selectedIdx)
	}
}

func TestSyncPanelState(t *testing.T) {
	panel := NewSyncPanel()

	if panel.State() != nil {
		t.Error("Initial state should be nil")
	}

	state := &sync.SyncState{}
	panel.state = state

	if panel.State() != state {
		t.Error("State() should return the set state")
	}
}

func TestSyncPanelStateNil(t *testing.T) {
	var panel *SyncPanel
	if panel.State() != nil {
		t.Error("Nil panel State() should return nil")
	}
}

func TestSyncPanelSelectedMachine(t *testing.T) {
	panel := NewSyncPanel()

	// No machines - should return nil
	if panel.SelectedMachine() != nil {
		t.Error("SelectedMachine should be nil when no machines")
	}

	// Add machines
	state := &sync.SyncState{
		Pool: &sync.MachinePool{},
	}
	m1 := sync.NewMachine("machine1", "192.168.1.1")
	m2 := sync.NewMachine("machine2", "192.168.1.2")
	state.Pool.AddMachine(m1)
	state.Pool.AddMachine(m2)
	panel.SetState(state)

	selected := panel.SelectedMachine()
	if selected == nil {
		t.Fatal("SelectedMachine should not be nil")
	}
	if selected.Name != "machine1" {
		t.Errorf("SelectedMachine name = %q, want machine1", selected.Name)
	}

	// Move down and check again
	panel.MoveDown()
	selected = panel.SelectedMachine()
	if selected == nil {
		t.Fatal("SelectedMachine should not be nil after MoveDown")
	}
	if selected.Name != "machine2" {
		t.Errorf("SelectedMachine name = %q, want machine2", selected.Name)
	}
}

func TestSyncPanelSelectedMachineNil(t *testing.T) {
	var panel *SyncPanel
	if panel.SelectedMachine() != nil {
		t.Error("Nil panel SelectedMachine() should return nil")
	}
}

func TestSyncPanelMoveUp(t *testing.T) {
	panel := NewSyncPanel()

	// Setup with multiple machines
	state := &sync.SyncState{
		Pool: &sync.MachinePool{},
	}
	state.Pool.AddMachine(sync.NewMachine("m1", "1.1.1.1"))
	state.Pool.AddMachine(sync.NewMachine("m2", "2.2.2.2"))
	state.Pool.AddMachine(sync.NewMachine("m3", "3.3.3.3"))
	panel.SetState(state)

	panel.selectedIdx = 2 // Start at bottom

	panel.MoveUp()
	if panel.selectedIdx != 1 {
		t.Errorf("After MoveUp from 2, selectedIdx = %d, want 1", panel.selectedIdx)
	}

	panel.MoveUp()
	if panel.selectedIdx != 0 {
		t.Errorf("After MoveUp from 1, selectedIdx = %d, want 0", panel.selectedIdx)
	}

	// Should not go below 0
	panel.MoveUp()
	if panel.selectedIdx != 0 {
		t.Errorf("MoveUp at 0 should stay at 0, got %d", panel.selectedIdx)
	}
}

func TestSyncPanelMoveUpNoMachines(t *testing.T) {
	panel := NewSyncPanel()
	// Should not panic
	panel.MoveUp()
}

func TestSyncPanelMoveUpNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.MoveUp()
}

func TestSyncPanelMoveDown(t *testing.T) {
	panel := NewSyncPanel()

	// Setup with multiple machines
	state := &sync.SyncState{
		Pool: &sync.MachinePool{},
	}
	state.Pool.AddMachine(sync.NewMachine("m1", "1.1.1.1"))
	state.Pool.AddMachine(sync.NewMachine("m2", "2.2.2.2"))
	state.Pool.AddMachine(sync.NewMachine("m3", "3.3.3.3"))
	panel.SetState(state)

	panel.selectedIdx = 0 // Start at top

	panel.MoveDown()
	if panel.selectedIdx != 1 {
		t.Errorf("After MoveDown from 0, selectedIdx = %d, want 1", panel.selectedIdx)
	}

	panel.MoveDown()
	if panel.selectedIdx != 2 {
		t.Errorf("After MoveDown from 1, selectedIdx = %d, want 2", panel.selectedIdx)
	}

	// Should not go past last index
	panel.MoveDown()
	if panel.selectedIdx != 2 {
		t.Errorf("MoveDown at last should stay at 2, got %d", panel.selectedIdx)
	}
}

func TestSyncPanelMoveDownNoMachines(t *testing.T) {
	panel := NewSyncPanel()
	// Should not panic
	panel.MoveDown()
}

func TestSyncPanelMoveDownNil(t *testing.T) {
	var panel *SyncPanel
	// Should not panic
	panel.MoveDown()
}

func TestToMachineInfo(t *testing.T) {
	t.Run("nil machine", func(t *testing.T) {
		info := ToMachineInfo(nil)
		if info.ID != "" {
			t.Error("nil machine should produce empty info")
		}
	})

	t.Run("valid machine", func(t *testing.T) {
		now := time.Now()
		machine := &sync.Machine{
			ID:        "test-id",
			Name:      "test-name",
			Address:   "192.168.1.100",
			Port:      22,
			Status:    sync.StatusOnline,
			LastSync:  now.Add(-5 * time.Minute),
			LastError: "some error",
		}

		info := ToMachineInfo(machine)

		if info.ID != "test-id" {
			t.Errorf("ID = %q, want test-id", info.ID)
		}
		if info.Name != "test-name" {
			t.Errorf("Name = %q, want test-name", info.Name)
		}
		if info.Address != "192.168.1.100" {
			t.Errorf("Address = %q, want 192.168.1.100", info.Address)
		}
		if info.Port != 22 {
			t.Errorf("Port = %d, want 22", info.Port)
		}
		if info.Status != sync.StatusOnline {
			t.Errorf("Status = %q, want online", info.Status)
		}
		if info.StatusIcon != "🟢" {
			t.Errorf("StatusIcon = %q, want 🟢", info.StatusIcon)
		}
		if info.LastError != "some error" {
			t.Errorf("LastError = %q, want 'some error'", info.LastError)
		}
		if !strings.Contains(info.LastSync, "min") {
			t.Errorf("LastSync = %q, want to contain 'min'", info.LastSync)
		}
	})
}

func TestGetStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{sync.StatusOnline, "🟢"},
		{sync.StatusOffline, "🔴"},
		{sync.StatusSyncing, "🔄"},
		{sync.StatusError, "⚠️"},
		{"unknown", "⚪"},
		{"", "⚪"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := getStatusIcon(tt.status)
			if got != tt.want {
				t.Errorf("getStatusIcon(%q) = %q, want %q", tt.status, got, tt.want)
			}
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name    string
		time    time.Time
		wantSub string
	}{
		{"zero time", time.Time{}, "never"},
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"1 min ago", now.Add(-90 * time.Second), "1 min ago"},
		{"5 mins ago", now.Add(-5 * time.Minute), "mins ago"},
		{"1 hour ago", now.Add(-90 * time.Minute), "1 hour ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "hours ago"},
		{"1 day ago", now.Add(-36 * time.Hour), "1 day ago"},
		{"5 days ago", now.Add(-5 * 24 * time.Hour), "days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTimeAgo(tt.time)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("formatTimeAgo() = %q, want to contain %q", got, tt.wantSub)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		input  string
		maxLen int
		want   string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long string", 10, "this is..."},
		{"ab", 3, "ab"},
		{"abcd", 3, "abc"},
		{"abcdef", 4, "a..."},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

// =============================================================================
// sync_view.go Tests
// =============================================================================

func TestSyncPanelViewNil(t *testing.T) {
	var panel *SyncPanel
	if panel.View() != "" {
		t.Error("Nil panel View() should return empty string")
	}
}

func TestSyncPanelViewLoading(t *testing.T) {
	panel := NewSyncPanel()
	panel.SetLoading(true)

	view := panel.View()

	if !strings.Contains(view, "Sync Pool") {
		t.Error("View should contain title 'Sync Pool'")
	}
	if !strings.Contains(view, "Loading") {
		t.Error("View should contain 'Loading' when loading")
	}
}

func TestSyncPanelViewSyncing(t *testing.T) {
	panel := NewSyncPanel()
	panel.SetSyncing(true)

	view := panel.View()

	if !strings.Contains(view, "Syncing") {
		t.Error("View should contain 'Syncing' when syncing")
	}
}

func TestSyncPanelViewNoState(t *testing.T) {
	panel := NewSyncPanel()

	view := panel.View()

	if !strings.Contains(view, "Not configured") {
		t.Error("View should show 'Not configured' when no state")
	}
}

func TestSyncPanelViewEnabled(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled:  true,
			AutoSync: true,
		},
	}
	panel.SetState(state)

	view := panel.View()

	if !strings.Contains(view, "Enabled") {
		t.Error("View should show 'Enabled' when pool is enabled")
	}
	if !strings.Contains(view, "Auto-sync") {
		t.Error("View should show 'Auto-sync' when auto-sync is on")
	}
}

func TestSyncPanelViewDisabled(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled:  false,
			AutoSync: false,
		},
	}
	panel.SetState(state)

	view := panel.View()

	if !strings.Contains(view, "Disabled") {
		t.Error("View should show 'Disabled' when pool is disabled")
	}
}

func TestSyncPanelViewNoMachines(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	panel.SetState(state)

	view := panel.View()

	if !strings.Contains(view, "No machines") {
		t.Error("View should show 'No machines' when none configured")
	}
}

func TestSyncPanelViewWithMachines(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	m := sync.NewMachine("test-server", "192.168.1.100")
	m.Status = sync.StatusOnline
	state.Pool.AddMachine(m)
	panel.SetState(state)

	view := panel.View()

	if !strings.Contains(view, "test-server") {
		t.Error("View should contain machine name")
	}
	if !strings.Contains(view, "192.168.1.100") {
		t.Error("View should contain machine address")
	}
	if !strings.Contains(view, "🟢") {
		t.Error("View should contain online status icon")
	}
}

func TestSyncPanelViewWithSelectedMachine(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	m1 := sync.NewMachine("server1", "1.1.1.1")
	m2 := sync.NewMachine("server2", "2.2.2.2")
	state.Pool.AddMachine(m1)
	state.Pool.AddMachine(m2)
	panel.SetState(state)

	view := panel.View()

	// First machine should be selected (indicated by ">")
	if !strings.Contains(view, ">") {
		t.Error("View should contain selection indicator '>'")
	}
}

func TestSyncPanelViewWithError(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	m := sync.NewMachine("error-server", "192.168.1.100")
	m.Status = sync.StatusError
	m.LastError = "Connection refused"
	state.Pool.AddMachine(m)
	panel.SetState(state)

	view := panel.View()

	if !strings.Contains(view, "⚠️") {
		t.Error("View should contain error status icon")
	}
	if !strings.Contains(view, "Connection refused") {
		t.Error("View should contain error message")
	}
}

func TestSyncPanelViewWithSize(t *testing.T) {
	panel := NewSyncPanel()
	panel.SetSize(80, 24)
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	panel.SetState(state)

	view := panel.View()

	// Just verify it renders without panic
	if view == "" {
		t.Error("View should not be empty")
	}
}

func TestSyncPanelViewKeyHints(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	m := sync.NewMachine("server", "1.1.1.1")
	state.Pool.AddMachine(m)
	panel.SetState(state)

	view := panel.View()

	// Check key hints are present
	if !strings.Contains(view, "[a]dd") {
		t.Error("View should contain [a]dd key hint")
	}
	if !strings.Contains(view, "[s]ync") {
		t.Error("View should contain [s]ync key hint")
	}
	if !strings.Contains(view, "[esc]") {
		t.Error("View should contain [esc] key hint")
	}
}

func TestSyncPanelViewWithNonDefaultPort(t *testing.T) {
	panel := NewSyncPanel()
	state := &sync.SyncState{
		Pool: &sync.MachinePool{
			Enabled: true,
		},
	}
	m := sync.NewMachine("server", "192.168.1.100")
	m.Port = 2222 // Non-default SSH port
	state.Pool.AddMachine(m)
	panel.SetState(state)

	view := panel.View()

	if !strings.Contains(view, "2222") {
		t.Error("View should show non-default port number")
	}
}

// =============================================================================
// sync_update.go Tests
// =============================================================================

func TestSyncStateLoadedMsg(t *testing.T) {
	msg := syncStateLoadedMsg{
		state: &sync.SyncState{},
		err:   nil,
	}

	if msg.state == nil {
		t.Error("state should not be nil")
	}
	if msg.err != nil {
		t.Error("err should be nil")
	}
}

func TestSyncMachineAddedMsg(t *testing.T) {
	machine := sync.NewMachine("test", "1.1.1.1")
	msg := syncMachineAddedMsg{
		machine: machine,
		err:     nil,
	}

	if msg.machine == nil {
		t.Error("machine should not be nil")
	}
	if msg.err != nil {
		t.Error("err should be nil")
	}
}

func TestSyncMachineRemovedMsg(t *testing.T) {
	msg := syncMachineRemovedMsg{
		machineID: "test-id",
		err:       nil,
	}

	if msg.machineID != "test-id" {
		t.Errorf("machineID = %q, want test-id", msg.machineID)
	}
}

func TestSyncMachineUpdatedMsg(t *testing.T) {
	machine := sync.NewMachine("updated", "2.2.2.2")
	msg := syncMachineUpdatedMsg{
		machine: machine,
		err:     nil,
	}

	if msg.machine == nil {
		t.Error("machine should not be nil")
	}
}

func TestSyncTestResultMsg(t *testing.T) {
	msg := syncTestResultMsg{
		machineID: "test-id",
		success:   true,
		message:   "Connected successfully",
		err:       nil,
	}

	if msg.machineID != "test-id" {
		t.Errorf("machineID = %q, want test-id", msg.machineID)
	}
	if !msg.success {
		t.Error("success should be true")
	}
	if msg.message != "Connected successfully" {
		t.Errorf("message = %q, want 'Connected successfully'", msg.message)
	}
}

func TestSyncStartedMsg(t *testing.T) {
	msg := syncStartedMsg{
		machineID: "test-id",
	}

	if msg.machineID != "test-id" {
		t.Errorf("machineID = %q, want test-id", msg.machineID)
	}
}

func TestSyncCompletedMsg(t *testing.T) {
	msg := syncCompletedMsg{
		machineID: "test-id",
		err:       nil,
	}

	if msg.machineID != "test-id" {
		t.Errorf("machineID = %q, want test-id", msg.machineID)
	}
	if msg.err != nil {
		t.Error("err should be nil")
	}
}

func TestLoadSyncStateCmd(t *testing.T) {
	cmd := LoadSyncStateCmd()

	if cmd == nil {
		t.Fatal("LoadSyncStateCmd returned nil")
	}

	// Execute the command - it will try to load state (which may not exist)
	// Just ensure it returns a valid message type
	msg := cmd()

	if _, ok := msg.(syncStateLoadedMsg); !ok {
		t.Errorf("Command returned %T, want syncStateLoadedMsg", msg)
	}
}
