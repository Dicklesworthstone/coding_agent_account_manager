package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTextInputDialog_Creation(t *testing.T) {
	d := NewTextInputDialog("Test Title", "Enter value:")

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone, got %v", d.Result())
	}

	if d.Value() != "" {
		t.Errorf("expected empty value, got %q", d.Value())
	}
}

func TestTextInputDialog_SetValue(t *testing.T) {
	d := NewTextInputDialog("Test", "Prompt")
	d.SetValue("initial value")

	if d.Value() != "initial value" {
		t.Errorf("expected 'initial value', got %q", d.Value())
	}
}

func TestTextInputDialog_SetPlaceholder(t *testing.T) {
	d := NewTextInputDialog("Test", "Prompt")
	d.SetPlaceholder("enter here...")

	// Can't directly check placeholder, but verify no panic
	view := d.View()
	if view == "" {
		t.Error("expected non-empty view")
	}
}

func TestTextInputDialog_Submit(t *testing.T) {
	d := NewTextInputDialog("Test", "Prompt")
	d.SetValue("test input")

	// Simulate Enter key
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit, got %v", d.Result())
	}

	if d.Value() != "test input" {
		t.Errorf("expected 'test input', got %q", d.Value())
	}
}

func TestTextInputDialog_Cancel(t *testing.T) {
	d := NewTextInputDialog("Test", "Prompt")
	d.SetValue("test input")

	// Simulate Escape key
	msg := tea.KeyMsg{Type: tea.KeyEsc}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultCancel {
		t.Errorf("expected DialogResultCancel, got %v", d.Result())
	}
}

func TestTextInputDialog_Reset(t *testing.T) {
	d := NewTextInputDialog("Test", "Prompt")
	d.SetValue("test input")

	// Submit first
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit after enter, got %v", d.Result())
	}

	// Reset
	d.Reset()

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone after reset, got %v", d.Result())
	}
}

func TestTextInputDialog_View(t *testing.T) {
	d := NewTextInputDialog("Test Title", "Enter your name:")

	view := d.View()

	// Check title is rendered
	if !strings.Contains(view, "Test Title") {
		t.Error("expected view to contain title")
	}

	// Check prompt is rendered
	if !strings.Contains(view, "Enter your name:") {
		t.Error("expected view to contain prompt")
	}

	// Check help text is rendered
	if !strings.Contains(view, "enter") || !strings.Contains(view, "esc") {
		t.Error("expected view to contain help text")
	}
}

func TestTextInputDialog_Focus(t *testing.T) {
	d := NewTextInputDialog("Test", "Prompt")

	// Initially focused
	d.Blur()

	// When blurred, updates should be ignored
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	// Result should remain None since we're blurred
	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone when blurred, got %v", d.Result())
	}

	// Focus and try again
	d.Focus()
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit when focused, got %v", d.Result())
	}
}

func TestConfirmDialog_Creation(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Are you sure?")

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone, got %v", d.Result())
	}

	if d.Confirmed() {
		t.Error("expected Confirmed() to be false initially")
	}
}

func TestConfirmDialog_ConfirmWithY(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Delete this?")

	// Press 'y'
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit, got %v", d.Result())
	}

	if !d.Confirmed() {
		t.Error("expected Confirmed() to be true after 'y'")
	}
}

func TestConfirmDialog_CancelWithN(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Delete this?")

	// Press 'n'
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultCancel {
		t.Errorf("expected DialogResultCancel, got %v", d.Result())
	}

	if d.Confirmed() {
		t.Error("expected Confirmed() to be false after 'n'")
	}
}

func TestConfirmDialog_CancelWithEsc(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Delete this?")

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultCancel {
		t.Errorf("expected DialogResultCancel, got %v", d.Result())
	}
}

func TestConfirmDialog_Navigation(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Delete?")

	// Initial selection is 0 (No)
	// Navigate right to Yes
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'l'}}
	d, _ = d.Update(msg)

	// Press Enter to confirm
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if !d.Confirmed() {
		t.Error("expected Confirmed() to be true after navigating to Yes and pressing Enter")
	}
}

func TestConfirmDialog_TabNavigation(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Delete?")

	// Tab to switch from No to Yes
	msg := tea.KeyMsg{Type: tea.KeyTab}
	d, _ = d.Update(msg)

	// Press Enter
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if !d.Confirmed() {
		t.Error("expected Confirmed() to be true after Tab + Enter")
	}
}

func TestConfirmDialog_SetLabels(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Proceed?")
	d.SetLabels("Proceed", "Cancel")

	view := d.View()

	if !strings.Contains(view, "Proceed") {
		t.Error("expected view to contain 'Proceed' label")
	}

	if !strings.Contains(view, "Cancel") {
		t.Error("expected view to contain 'Cancel' label")
	}
}

func TestConfirmDialog_Reset(t *testing.T) {
	d := NewConfirmDialog("Confirm", "Delete?")

	// Confirm
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit, got %v", d.Result())
	}

	// Reset
	d.Reset()

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone after reset, got %v", d.Result())
	}
}

func TestConfirmDialog_View(t *testing.T) {
	d := NewConfirmDialog("Delete Profile", "Are you sure you want to delete?")

	view := d.View()

	// Check title is rendered
	if !strings.Contains(view, "Delete Profile") {
		t.Error("expected view to contain title")
	}

	// Check message is rendered
	if !strings.Contains(view, "Are you sure") {
		t.Error("expected view to contain message")
	}

	// Check buttons are rendered
	if !strings.Contains(view, "Yes") || !strings.Contains(view, "No") {
		t.Error("expected view to contain Yes/No buttons")
	}
}

func TestMultiFieldDialog_Creation(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Name", Placeholder: "Enter name", Required: true},
		{Label: "Email", Placeholder: "Enter email"},
	}
	d := NewMultiFieldDialog("User Info", fields)

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone, got %v", d.Result())
	}

	values := d.Values()
	if len(values) != 2 {
		t.Errorf("expected 2 values, got %d", len(values))
	}
}

func TestMultiFieldDialog_Values(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "First", Value: "initial1"},
		{Label: "Second", Value: "initial2"},
	}
	d := NewMultiFieldDialog("Test", fields)

	values := d.Values()
	if values[0] != "initial1" || values[1] != "initial2" {
		t.Errorf("expected initial values, got %v", values)
	}

	valueMap := d.ValueMap()
	if valueMap["First"] != "initial1" || valueMap["Second"] != "initial2" {
		t.Errorf("expected value map with initial values, got %v", valueMap)
	}
}

func TestMultiFieldDialog_Validate(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Required", Required: true},
		{Label: "Optional"},
	}
	d := NewMultiFieldDialog("Test", fields)

	// Should fail validation - required field is empty
	if d.Validate() {
		t.Error("expected Validate() to return false when required field is empty")
	}

	// Type in required field
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	d, _ = d.Update(msg)

	// Now should pass validation
	if !d.Validate() {
		t.Error("expected Validate() to return true after filling required field")
	}
}

func TestMultiFieldDialog_TabNavigation(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Field1"},
		{Label: "Field2"},
		{Label: "Field3"},
	}
	d := NewMultiFieldDialog("Test", fields)

	// First field is focused initially
	// Type in first field
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	d, _ = d.Update(msg)

	// Tab to second field
	msg = tea.KeyMsg{Type: tea.KeyTab}
	d, _ = d.Update(msg)

	// Type in second field
	msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	d, _ = d.Update(msg)

	values := d.Values()
	if values[0] != "a" {
		t.Errorf("expected first field to be 'a', got %q", values[0])
	}
	if values[1] != "b" {
		t.Errorf("expected second field to be 'b', got %q", values[1])
	}
}

func TestMultiFieldDialog_Cancel(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Field1"},
	}
	d := NewMultiFieldDialog("Test", fields)

	msg := tea.KeyMsg{Type: tea.KeyEsc}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultCancel {
		t.Errorf("expected DialogResultCancel, got %v", d.Result())
	}
}

func TestMultiFieldDialog_Submit(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Field1", Value: "value1"},
	}
	d := NewMultiFieldDialog("Test", fields)

	msg := tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit, got %v", d.Result())
	}
}

func TestMultiFieldDialog_Reset(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Field1", Value: "initial"},
	}
	d := NewMultiFieldDialog("Test", fields)

	// Type something
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}}
	d, _ = d.Update(msg)

	// Submit
	msg = tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit, got %v", d.Result())
	}

	// Reset
	d.Reset()

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone after reset, got %v", d.Result())
	}

	values := d.Values()
	if values[0] != "initial" {
		t.Errorf("expected value to be reset to 'initial', got %q", values[0])
	}
}

func TestMultiFieldDialog_View(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Name", Required: true},
		{Label: "Email"},
	}
	d := NewMultiFieldDialog("User Details", fields)

	view := d.View()

	// Check title is rendered
	if !strings.Contains(view, "User Details") {
		t.Error("expected view to contain title")
	}

	// Check labels are rendered
	if !strings.Contains(view, "Name") {
		t.Error("expected view to contain 'Name' label")
	}

	if !strings.Contains(view, "Email") {
		t.Error("expected view to contain 'Email' label")
	}

	// Check required indicator
	if !strings.Contains(view, "*") {
		t.Error("expected view to contain required indicator '*'")
	}

	// Check help text
	if !strings.Contains(view, "tab") {
		t.Error("expected view to contain help text about tab")
	}
}

func TestMultiFieldDialog_Focus(t *testing.T) {
	fields := []FieldDefinition{
		{Label: "Field1"},
	}
	d := NewMultiFieldDialog("Test", fields)

	// Blur the dialog
	d.Blur()

	// Updates should be ignored
	msg := tea.KeyMsg{Type: tea.KeyEnter}
	d, _ = d.Update(msg)

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone when blurred, got %v", d.Result())
	}

	// Focus and try again
	d.Focus()
	d, _ = d.Update(msg)

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit when focused, got %v", d.Result())
	}
}

func TestDialogKeyMap(t *testing.T) {
	keys := DefaultDialogKeyMap()

	// Just verify the keybindings exist
	if len(keys.Submit.Keys()) == 0 {
		t.Error("expected Submit keybinding to have keys")
	}

	if len(keys.Cancel.Keys()) == 0 {
		t.Error("expected Cancel keybinding to have keys")
	}

	if len(keys.Tab.Keys()) == 0 {
		t.Error("expected Tab keybinding to have keys")
	}
}

func TestCommandPaletteDialog_Creation(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone, got %v", d.Result())
	}

	if d.ChosenCommand() != nil {
		t.Error("expected nil chosen command on creation")
	}
}

func TestCommandPaletteDialog_Filter(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)

	// Filter by typing "back"
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	d.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})

	// The filter should narrow down to backup command
	view := d.View()
	if !strings.Contains(view, "Backup") {
		t.Error("expected filter to show backup command")
	}
}

func TestCommandPaletteDialog_Navigation(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)

	// Move down
	d.Update(tea.KeyMsg{Type: tea.KeyDown})
	d.Update(tea.KeyMsg{Type: tea.KeyDown})

	// Move up
	d.Update(tea.KeyMsg{Type: tea.KeyUp})

	// Result should still be none
	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone, got %v", d.Result())
	}
}

func TestCommandPaletteDialog_Select(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)

	// Press Enter to select first command
	d.Update(tea.KeyMsg{Type: tea.KeyEnter})

	if d.Result() != DialogResultSubmit {
		t.Errorf("expected DialogResultSubmit, got %v", d.Result())
	}

	chosen := d.ChosenCommand()
	if chosen == nil {
		t.Fatal("expected a chosen command")
	}
	if chosen.Action != "activate" {
		t.Errorf("expected first command (activate), got %s", chosen.Action)
	}
}

func TestCommandPaletteDialog_Cancel(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)

	// Press Escape to cancel
	d.Update(tea.KeyMsg{Type: tea.KeyEscape})

	if d.Result() != DialogResultCancel {
		t.Errorf("expected DialogResultCancel, got %v", d.Result())
	}
}

func TestCommandPaletteDialog_Reset(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)

	// Select a command
	d.Update(tea.KeyMsg{Type: tea.KeyEnter})

	// Reset
	d.Reset()

	if d.Result() != DialogResultNone {
		t.Errorf("expected DialogResultNone after reset, got %v", d.Result())
	}
	if d.ChosenCommand() != nil {
		t.Error("expected nil chosen command after reset")
	}
}

func TestCommandPaletteDialog_View(t *testing.T) {
	commands := DefaultCommands()
	d := NewCommandPaletteDialog("Test Palette", commands)
	d.SetStyles(DefaultStyles())

	view := d.View()

	// Should contain the title
	if !strings.Contains(view, "Test Palette") {
		t.Error("expected view to contain title")
	}

	// Should contain help text
	if !strings.Contains(view, "navigate") {
		t.Error("expected view to contain navigation help")
	}

	// Should contain at least one command
	if !strings.Contains(view, "Activate") {
		t.Error("expected view to contain Activate command")
	}
}

func TestDefaultCommands(t *testing.T) {
	commands := DefaultCommands()

	if len(commands) == 0 {
		t.Error("expected at least one default command")
	}

	// Check that all commands have required fields
	for i, cmd := range commands {
		if cmd.Name == "" {
			t.Errorf("command %d has empty name", i)
		}
		if cmd.Action == "" {
			t.Errorf("command %d has empty action", i)
		}
		if cmd.Shortcut == "" {
			t.Errorf("command %d has empty shortcut", i)
		}
	}
}
