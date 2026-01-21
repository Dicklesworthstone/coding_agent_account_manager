package tui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/lipgloss"
)

// =============================================================================
// styles.go Tests
// =============================================================================

func TestDefaultStyles(t *testing.T) {
	styles := DefaultStyles()

	// Verify all style fields are initialized (not zero values)
	t.Run("Header style initialized", func(t *testing.T) {
		// Render with test string to ensure style is usable
		result := styles.Header.Render("Test Header")
		if result == "" {
			t.Error("Header style should render non-empty output")
		}
	})

	t.Run("Tab styles initialized", func(t *testing.T) {
		result := styles.Tab.Render("Tab")
		if result == "" {
			t.Error("Tab style should render non-empty output")
		}

		result = styles.ActiveTab.Render("Active Tab")
		if result == "" {
			t.Error("ActiveTab style should render non-empty output")
		}
	})

	t.Run("List item styles initialized", func(t *testing.T) {
		result := styles.Item.Render("Item")
		if result == "" {
			t.Error("Item style should render non-empty output")
		}

		result = styles.SelectedItem.Render("Selected")
		if result == "" {
			t.Error("SelectedItem style should render non-empty output")
		}

		result = styles.Active.Render("Active")
		if result == "" {
			t.Error("Active style should render non-empty output")
		}
	})

	t.Run("Status bar styles initialized", func(t *testing.T) {
		result := styles.StatusBar.Render("Status")
		if result == "" {
			t.Error("StatusBar style should render non-empty output")
		}

		result = styles.StatusKey.Render("Key")
		if result == "" {
			t.Error("StatusKey style should render non-empty output")
		}

		result = styles.StatusText.Render("Text")
		if result == "" {
			t.Error("StatusText style should render non-empty output")
		}

		result = styles.StatusSuccess.Render("Success")
		if result == "" {
			t.Error("StatusSuccess style should render non-empty output")
		}

		result = styles.StatusWarning.Render("Warning")
		if result == "" {
			t.Error("StatusWarning style should render non-empty output")
		}

		result = styles.StatusError.Render("Error")
		if result == "" {
			t.Error("StatusError style should render non-empty output")
		}
	})

	t.Run("Empty state style initialized", func(t *testing.T) {
		result := styles.Empty.Render("No items")
		if result == "" {
			t.Error("Empty style should render non-empty output")
		}
	})

	t.Run("Help style initialized", func(t *testing.T) {
		result := styles.Help.Render("Help text")
		if result == "" {
			t.Error("Help style should render non-empty output")
		}
	})

	t.Run("Dialog styles initialized", func(t *testing.T) {
		result := styles.Dialog.Render("Dialog content")
		if result == "" {
			t.Error("Dialog style should render non-empty output")
		}

		result = styles.DialogFocused.Render("Dialog content")
		if result == "" {
			t.Error("DialogFocused style should render non-empty output")
		}

		result = styles.DialogOverlay.Render("Dialog background")
		if result == "" {
			t.Error("DialogOverlay style should render non-empty output")
		}

		result = styles.DialogTitle.Render("Title")
		if result == "" {
			t.Error("DialogTitle style should render non-empty output")
		}

		result = styles.DialogButton.Render("Button")
		if result == "" {
			t.Error("DialogButton style should render non-empty output")
		}

		result = styles.DialogButtonActive.Render("Active Button")
		if result == "" {
			t.Error("DialogButtonActive style should render non-empty output")
		}
	})

	t.Run("Input styles initialized", func(t *testing.T) {
		result := styles.InputCursor.Render("|")
		if result == "" {
			t.Error("InputCursor style should render non-empty output")
		}
	})
}

func TestStatusSeverityStyleMapping(t *testing.T) {
	theme := NewTheme(DefaultThemeOptions())
	styles := NewStyles(theme)

	tests := []struct {
		severity StatusSeverity
		want     interface{}
	}{
		{StatusSuccess, theme.Palette.Success},
		{StatusWarning, theme.Palette.Warning},
		{StatusError, theme.Palette.Danger},
	}

	for _, tt := range tests {
		t.Run(tt.severity.String(), func(t *testing.T) {
			style := styles.StatusSeverityStyle(tt.severity)
			fg := style.GetForeground()
			t.Logf("severity=%s foreground=%v", tt.severity.String(), fg)
			if fg != tt.want {
				t.Fatalf("severity=%s foreground=%v, want %v", tt.severity.String(), fg, tt.want)
			}
		})
	}

	t.Run("info uses StatusText", func(t *testing.T) {
		style := styles.StatusSeverityStyle(StatusInfo)
		fg := style.GetForeground()
		t.Logf("severity=info foreground=%v", fg)
		if fg != styles.StatusText.GetForeground() {
			t.Fatalf("info foreground=%v, want %v", fg, styles.StatusText.GetForeground())
		}
	})

	t.Run("no-color theme leaves severity styles uncolored", func(t *testing.T) {
		theme := NewTheme(ThemeOptions{NoColor: true})
		styles := NewStyles(theme)
		style := styles.StatusSeverityStyle(StatusError)
		fg := style.GetForeground()
		t.Logf("severity=error foreground=%v", fg)
		if fg != (lipgloss.NoColor{}) {
			t.Fatalf("expected no color foreground, got %v", fg)
		}
	})
}

func TestDefaultStylesConsistency(t *testing.T) {
	// Calling DefaultStyles multiple times should return consistent styles
	styles1 := DefaultStyles()
	styles2 := DefaultStyles()

	// Both should render the same output for the same input
	s1 := styles1.Header.Render("Test")
	s2 := styles2.Header.Render("Test")

	if s1 != s2 {
		t.Error("DefaultStyles should return consistent styles")
	}
}

func TestToastStyleMapping(t *testing.T) {
	theme := NewTheme(DefaultThemeOptions())
	styles := NewStyles(theme)

	tests := []struct {
		severity StatusSeverity
		want     interface{}
	}{
		{StatusSuccess, theme.Palette.Success},
		{StatusWarning, theme.Palette.Warning},
		{StatusError, theme.Palette.Danger},
		{StatusInfo, theme.Palette.Info},
	}

	for _, tt := range tests {
		t.Run(tt.severity.String(), func(t *testing.T) {
			style := styles.ToastStyle(tt.severity)
			fg := style.GetForeground()
			t.Logf("toast severity=%s foreground=%v", tt.severity.String(), fg)
			if fg != tt.want {
				t.Fatalf("toast severity=%s foreground=%v, want %v", tt.severity.String(), fg, tt.want)
			}
		})
	}
}

func TestToastStylesInitialized(t *testing.T) {
	styles := DefaultStyles()

	t.Run("ToastSuccess style initialized", func(t *testing.T) {
		result := styles.ToastSuccess.Render("Success!")
		if result == "" {
			t.Error("ToastSuccess style should render non-empty output")
		}
	})

	t.Run("ToastWarning style initialized", func(t *testing.T) {
		result := styles.ToastWarning.Render("Warning!")
		if result == "" {
			t.Error("ToastWarning style should render non-empty output")
		}
	})

	t.Run("ToastError style initialized", func(t *testing.T) {
		result := styles.ToastError.Render("Error!")
		if result == "" {
			t.Error("ToastError style should render non-empty output")
		}
	})

	t.Run("ToastInfo style initialized", func(t *testing.T) {
		result := styles.ToastInfo.Render("Info!")
		if result == "" {
			t.Error("ToastInfo style should render non-empty output")
		}
	})
}

func TestStatusModeStylesInitialized(t *testing.T) {
	styles := DefaultStyles()

	t.Run("StatusModeNormal style initialized", func(t *testing.T) {
		result := styles.StatusModeNormal.Render("NORMAL")
		if result == "" {
			t.Error("StatusModeNormal style should render non-empty output")
		}
	})

	t.Run("StatusModeSearch style initialized", func(t *testing.T) {
		result := styles.StatusModeSearch.Render("SEARCH")
		if result == "" {
			t.Error("StatusModeSearch style should render non-empty output")
		}
	})

	t.Run("StatusModeHelp style initialized", func(t *testing.T) {
		result := styles.StatusModeHelp.Render("HELP")
		if result == "" {
			t.Error("StatusModeHelp style should render non-empty output")
		}
	})
}

// =============================================================================
// keys.go Tests
// =============================================================================

func TestDefaultKeyMap(t *testing.T) {
	km := defaultKeyMap()

	t.Run("Navigation keys initialized", func(t *testing.T) {
		assertKeyBinding(t, km.Up, "Up")
		assertKeyBinding(t, km.Down, "Down")
		assertKeyBinding(t, km.Left, "Left")
		assertKeyBinding(t, km.Right, "Right")
		assertKeyBinding(t, km.Tab, "Tab")
	})

	t.Run("Action keys initialized", func(t *testing.T) {
		assertKeyBinding(t, km.Enter, "Enter")
		assertKeyBinding(t, km.Backup, "Backup")
		assertKeyBinding(t, km.Delete, "Delete")
		assertKeyBinding(t, km.Edit, "Edit")
		assertKeyBinding(t, km.Login, "Login")
		assertKeyBinding(t, km.Open, "Open")
		assertKeyBinding(t, km.Search, "Search")
		assertKeyBinding(t, km.Project, "Project")
		assertKeyBinding(t, km.Usage, "Usage")
		assertKeyBinding(t, km.Sync, "Sync")
		assertKeyBinding(t, km.Export, "Export")
		assertKeyBinding(t, km.Import, "Import")
	})

	t.Run("Confirmation keys initialized", func(t *testing.T) {
		assertKeyBinding(t, km.Confirm, "Confirm")
		assertKeyBinding(t, km.Cancel, "Cancel")
	})

	t.Run("General keys initialized", func(t *testing.T) {
		assertKeyBinding(t, km.Help, "Help")
		assertKeyBinding(t, km.Quit, "Quit")
	})
}

func TestKeyMapShortHelp(t *testing.T) {
	km := defaultKeyMap()
	shortHelp := km.ShortHelp()

	if len(shortHelp) != 2 {
		t.Errorf("ShortHelp should return 2 bindings, got %d", len(shortHelp))
	}

	// Should contain Help and Quit
	found := map[string]bool{"Help": false, "Quit": false}
	for _, b := range shortHelp {
		help := b.Help()
		if help.Desc == "toggle help" {
			found["Help"] = true
		}
		if help.Desc == "quit" {
			found["Quit"] = true
		}
	}

	if !found["Help"] {
		t.Error("ShortHelp should contain Help binding")
	}
	if !found["Quit"] {
		t.Error("ShortHelp should contain Quit binding")
	}
}

func TestKeyMapFullHelp(t *testing.T) {
	km := defaultKeyMap()
	fullHelp := km.FullHelp()

	// Should have 5 groups
	if len(fullHelp) != 5 {
		t.Errorf("FullHelp should return 5 groups, got %d", len(fullHelp))
	}

	// Group 1: Navigation (Up, Down, Left, Right)
	if len(fullHelp[0]) != 4 {
		t.Errorf("Navigation group should have 4 bindings, got %d", len(fullHelp[0]))
	}

	// Group 2: Primary actions (Enter, Backup, Delete, Edit)
	if len(fullHelp[1]) != 4 {
		t.Errorf("Primary actions group should have 4 bindings, got %d", len(fullHelp[1]))
	}

	// Group 3: Secondary actions (Login, Open, Search, Project, Usage)
	if len(fullHelp[2]) != 5 {
		t.Errorf("Secondary actions group should have 5 bindings, got %d", len(fullHelp[2]))
	}

	// Group 4: Advanced (Sync, Export, Import)
	if len(fullHelp[3]) != 3 {
		t.Errorf("Advanced group should have 3 bindings, got %d", len(fullHelp[3]))
	}

	// Group 5: General (Help, Quit)
	if len(fullHelp[4]) != 2 {
		t.Errorf("General group should have 2 bindings, got %d", len(fullHelp[4]))
	}
}

func TestKeyBindingsHaveKeys(t *testing.T) {
	km := defaultKeyMap()

	// Each key binding should have at least one key associated
	tests := []struct {
		name    string
		binding key.Binding
	}{
		{"Up", km.Up},
		{"Down", km.Down},
		{"Left", km.Left},
		{"Right", km.Right},
		{"Tab", km.Tab},
		{"Enter", km.Enter},
		{"Backup", km.Backup},
		{"Delete", km.Delete},
		{"Edit", km.Edit},
		{"Login", km.Login},
		{"Open", km.Open},
		{"Search", km.Search},
		{"Project", km.Project},
		{"Usage", km.Usage},
		{"Sync", km.Sync},
		{"Export", km.Export},
		{"Import", km.Import},
		{"Confirm", km.Confirm},
		{"Cancel", km.Cancel},
		{"Help", km.Help},
		{"Quit", km.Quit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			keys := tt.binding.Keys()
			if len(keys) == 0 {
				t.Errorf("%s binding should have at least one key", tt.name)
			}
		})
	}
}

func TestKeyBindingsHaveHelp(t *testing.T) {
	km := defaultKeyMap()

	tests := []struct {
		name     string
		binding  key.Binding
		wantDesc string
	}{
		{"Up", km.Up, "move up"},
		{"Down", km.Down, "move down"},
		{"Left", km.Left, "previous provider"},
		{"Right", km.Right, "next provider"},
		{"Tab", km.Tab, "cycle providers"},
		{"Enter", km.Enter, "activate profile"},
		{"Backup", km.Backup, "backup current auth"},
		{"Delete", km.Delete, "delete profile"},
		{"Edit", km.Edit, "edit profile"},
		{"Login", km.Login, "login/refresh"},
		{"Open", km.Open, "open in browser"},
		{"Search", km.Search, "search profiles"},
		{"Project", km.Project, "set project association"},
		{"Usage", km.Usage, "usage stats"},
		{"Sync", km.Sync, "sync pool"},
		{"Export", km.Export, "export vault"},
		{"Import", km.Import, "import bundle"},
		{"Confirm", km.Confirm, "confirm"},
		{"Cancel", km.Cancel, "cancel"},
		{"Help", km.Help, "toggle help"},
		{"Quit", km.Quit, "quit"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			help := tt.binding.Help()
			if help.Desc != tt.wantDesc {
				t.Errorf("%s help description = %q, want %q", tt.name, help.Desc, tt.wantDesc)
			}
			if help.Key == "" {
				t.Errorf("%s help key should not be empty", tt.name)
			}
		})
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

func assertKeyBinding(t *testing.T, b key.Binding, name string) {
	t.Helper()
	if len(b.Keys()) == 0 {
		t.Errorf("%s binding should have keys", name)
	}
	help := b.Help()
	if help.Key == "" {
		t.Errorf("%s binding should have help key", name)
	}
	if help.Desc == "" {
		t.Errorf("%s binding should have help description", name)
	}
}
