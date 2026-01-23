package tui

import (
	"log/slog"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// ValidationFunc is a function that validates a field value.
// Returns an error message if invalid, or empty string if valid.
type ValidationFunc func(value string) string

// DialogResult represents the outcome of a dialog interaction.
type DialogResult int

const (
	// DialogResultNone indicates no result yet (dialog still open).
	DialogResultNone DialogResult = iota
	// DialogResultSubmit indicates the user submitted/confirmed.
	DialogResultSubmit
	// DialogResultCancel indicates the user cancelled.
	DialogResultCancel
)

// DialogKeyMap defines the keybindings for dialogs.
type DialogKeyMap struct {
	Submit key.Binding
	Cancel key.Binding
	Tab    key.Binding
}

// DefaultDialogKeyMap returns the default dialog keybindings.
func DefaultDialogKeyMap() DialogKeyMap {
	return DialogKeyMap{
		Submit: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "submit"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "cancel"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "next field"),
		),
	}
}

// TextInputDialog is a single-field text input dialog.
type TextInputDialog struct {
	title       string
	prompt      string
	placeholder string
	hint        string         // Optional hint text shown below input
	validate    ValidationFunc // Optional validation function
	error       string         // Current validation error
	input       textinput.Model
	result      DialogResult
	keys        DialogKeyMap
	styles      Styles
	width       int
	height      int
	focused     bool
	debug       bool // Enable debug logging
}

// NewTextInputDialog creates a new text input dialog.
func NewTextInputDialog(title, prompt string) *TextInputDialog {
	ti := textinput.New()
	ti.Focus()
	ti.CharLimit = 256
	ti.Width = 40

	return &TextInputDialog{
		title:   title,
		prompt:  prompt,
		input:   ti,
		result:  DialogResultNone,
		keys:    DefaultDialogKeyMap(),
		styles:  DefaultStyles(),
		width:   50,
		height:  7,
		focused: true,
	}
}

// SetPlaceholder sets the placeholder text for the input field.
func (d *TextInputDialog) SetPlaceholder(placeholder string) {
	d.placeholder = placeholder
	d.input.Placeholder = placeholder
}

// SetValue sets the initial value of the input field.
func (d *TextInputDialog) SetValue(value string) {
	d.input.SetValue(value)
}

// SetWidth sets the width of the input field.
func (d *TextInputDialog) SetWidth(width int) {
	d.width = width
	d.input.Width = width - 8 // Account for padding/borders
}

// SetStyles sets the styles for the dialog.
func (d *TextInputDialog) SetStyles(styles Styles) {
	d.styles = styles
	d.input.Cursor.Style = styles.InputCursor
}

// SetHint sets the hint text displayed below the input.
func (d *TextInputDialog) SetHint(hint string) {
	d.hint = hint
}

// SetValidation sets a custom validation function.
func (d *TextInputDialog) SetValidation(validate ValidationFunc) {
	d.validate = validate
}

// SetDebug enables or disables debug logging.
func (d *TextInputDialog) SetDebug(debug bool) {
	d.debug = debug
}

// Validate runs validation and returns true if valid.
func (d *TextInputDialog) Validate() bool {
	d.error = ""
	if d.validate != nil {
		d.error = d.validate(d.input.Value())
		if d.error != "" && d.debug {
			slog.Debug("text input validation failed",
				"prompt", d.prompt,
				"error", d.error)
		}
	}
	return d.error == ""
}

// GetError returns the current validation error.
func (d *TextInputDialog) GetError() string {
	return d.error
}

// Focus focuses the dialog input.
func (d *TextInputDialog) Focus() {
	d.focused = true
	d.input.Focus()
}

// Blur blurs the dialog input.
func (d *TextInputDialog) Blur() {
	d.focused = false
	d.input.Blur()
}

// Value returns the current input value.
func (d *TextInputDialog) Value() string {
	return d.input.Value()
}

// Result returns the dialog result.
func (d *TextInputDialog) Result() DialogResult {
	return d.result
}

// Reset resets the dialog to its initial state.
func (d *TextInputDialog) Reset() {
	d.input.Reset()
	d.result = DialogResultNone
	d.error = ""
	d.input.Focus()
}

// Update handles messages for the dialog.
func (d *TextInputDialog) Update(msg tea.Msg) (*TextInputDialog, tea.Cmd) {
	if !d.focused {
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, d.keys.Cancel):
			d.result = DialogResultCancel
			return d, nil
		case key.Matches(msg, d.keys.Submit):
			// Validate before submitting
			if d.validate != nil && !d.Validate() {
				// Stay open with validation error shown
				return d, nil
			}
			d.result = DialogResultSubmit
			return d, nil
		}
	}

	// Clear error when user types (live validation feedback)
	if _, ok := msg.(tea.KeyMsg); ok {
		d.error = ""
	}

	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	return d, cmd
}

// View renders the dialog.
func (d *TextInputDialog) View() string {
	// Build dialog content
	var content strings.Builder

	// Title
	if d.title != "" {
		content.WriteString(d.styles.DialogTitle.Render(d.title))
		content.WriteString("\n\n")
	}

	// Prompt with focus styling
	if d.prompt != "" {
		if d.focused {
			content.WriteString(d.styles.FieldLabelFocused.Render(d.prompt))
		} else {
			content.WriteString(d.styles.FieldLabel.Render(d.prompt))
		}
		content.WriteString("\n\n")
	}

	// Input field with focus indicator
	if d.focused {
		content.WriteString("▸ ")
	} else {
		content.WriteString("  ")
	}
	content.WriteString(d.input.View())

	// Show inline validation error or hint
	if d.error != "" {
		content.WriteString("\n  ")
		content.WriteString(d.styles.FieldError.Render("⚠ " + d.error))
	} else if d.hint != "" && d.focused {
		content.WriteString("\n  ")
		content.WriteString(d.styles.FieldHint.Render(d.hint))
	}

	content.WriteString("\n\n")

	// Help text
	help := d.styles.StatusKey.Render("enter") + " submit  " +
		d.styles.StatusKey.Render("esc") + " cancel"
	content.WriteString(help)

	style := d.styles.Dialog
	if d.focused {
		style = d.styles.DialogFocused
	}

	// Wrap in dialog box
	return style.
		Width(d.width).
		Render(content.String())
}

// ConfirmDialog is a yes/no confirmation dialog.
type ConfirmDialog struct {
	title       string
	message     string
	yesLabel    string
	noLabel     string
	destructive bool // If true, styles "yes" as a destructive/danger action
	selected    int  // 0 = no, 1 = yes
	result      DialogResult
	keys        DialogKeyMap
	styles      Styles
	width       int
	focused     bool
}

// NewConfirmDialog creates a new confirmation dialog.
func NewConfirmDialog(title, message string) *ConfirmDialog {
	return &ConfirmDialog{
		title:    title,
		message:  message,
		yesLabel: "Yes",
		noLabel:  "No",
		selected: 0, // Default to "No" for safety
		result:   DialogResultNone,
		keys:     DefaultDialogKeyMap(),
		styles:   DefaultStyles(),
		width:    50,
		focused:  true,
	}
}

// SetLabels sets custom labels for yes/no buttons.
func (d *ConfirmDialog) SetLabels(yes, no string) {
	d.yesLabel = yes
	d.noLabel = no
}

// SetDestructive marks this as a destructive confirmation (styles affirmative button as danger).
func (d *ConfirmDialog) SetDestructive(destructive bool) {
	d.destructive = destructive
}

// SetWidth sets the dialog width.
func (d *ConfirmDialog) SetWidth(width int) {
	d.width = width
}

// SetStyles sets the styles for the dialog.
func (d *ConfirmDialog) SetStyles(styles Styles) {
	d.styles = styles
}

// Focus focuses the dialog.
func (d *ConfirmDialog) Focus() {
	d.focused = true
}

// Blur blurs the dialog.
func (d *ConfirmDialog) Blur() {
	d.focused = false
}

// Result returns the dialog result.
func (d *ConfirmDialog) Result() DialogResult {
	return d.result
}

// Confirmed returns true if the user confirmed (selected yes).
func (d *ConfirmDialog) Confirmed() bool {
	return d.result == DialogResultSubmit && d.selected == 1
}

// Reset resets the dialog to its initial state.
func (d *ConfirmDialog) Reset() {
	d.selected = 0
	d.result = DialogResultNone
}

// Update handles messages for the dialog.
func (d *ConfirmDialog) Update(msg tea.Msg) (*ConfirmDialog, tea.Cmd) {
	if !d.focused {
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, d.keys.Cancel):
			d.result = DialogResultCancel
			return d, nil
		case key.Matches(msg, d.keys.Submit):
			d.result = DialogResultSubmit
			return d, nil
		case msg.String() == "y" || msg.String() == "Y":
			d.selected = 1
			d.result = DialogResultSubmit
			return d, nil
		case msg.String() == "n" || msg.String() == "N":
			d.selected = 0
			d.result = DialogResultCancel
			return d, nil
		case msg.String() == "left" || msg.String() == "h":
			if d.selected > 0 {
				d.selected--
			}
			return d, nil
		case msg.String() == "right" || msg.String() == "l":
			if d.selected < 1 {
				d.selected++
			}
			return d, nil
		case key.Matches(msg, d.keys.Tab):
			d.selected = (d.selected + 1) % 2
			return d, nil
		}
	}

	return d, nil
}

// View renders the dialog.
func (d *ConfirmDialog) View() string {
	var content strings.Builder

	// Title
	if d.title != "" {
		content.WriteString(d.styles.DialogTitle.Render(d.title))
		content.WriteString("\n\n")
	}

	// Message
	if d.message != "" {
		content.WriteString(d.message)
		content.WriteString("\n\n")
	}

	// Buttons
	noStyle := d.styles.DialogButton
	yesStyle := d.styles.DialogButton
	if d.selected == 0 {
		noStyle = d.styles.DialogButtonActive
	} else {
		yesStyle = d.styles.DialogButtonActive
	}

	buttons := lipgloss.JoinHorizontal(
		lipgloss.Center,
		noStyle.Render("  "+d.noLabel+"  "),
		"  ",
		yesStyle.Render("  "+d.yesLabel+"  "),
	)
	content.WriteString(buttons)
	content.WriteString("\n\n")

	// Help text
	help := d.styles.StatusKey.Render("y") + " yes  " +
		d.styles.StatusKey.Render("n") + " no  " +
		d.styles.StatusKey.Render("←/→") + " select  " +
		d.styles.StatusKey.Render("enter") + " confirm"
	content.WriteString(help)

	style := d.styles.Dialog
	if d.focused {
		style = d.styles.DialogFocused
	}

	// Wrap in dialog box
	return style.
		Width(d.width).
		Render(content.String())
}

// FieldDefinition defines a single field in a multi-field dialog.
type FieldDefinition struct {
	Label       string
	Placeholder string
	Value       string
	Required    bool
	Hint        string         // Optional help text shown below field
	Validate    ValidationFunc // Optional validation function
}

// MultiFieldDialog is a dialog with multiple input fields.
type MultiFieldDialog struct {
	title     string
	fields    []FieldDefinition
	inputs    []textinput.Model
	errors    []string // Validation errors for each field
	focused   int      // Currently focused field index
	result    DialogResult
	keys      DialogKeyMap
	styles    Styles
	width     int
	isFocused bool
	debug     bool // Enable debug logging for validation
}

// NewMultiFieldDialog creates a new multi-field dialog.
func NewMultiFieldDialog(title string, fields []FieldDefinition) *MultiFieldDialog {
	inputs := make([]textinput.Model, len(fields))
	errors := make([]string, len(fields))
	for i, field := range fields {
		ti := textinput.New()
		ti.Placeholder = field.Placeholder
		ti.SetValue(field.Value)
		ti.CharLimit = 256
		ti.Width = 40
		if i == 0 {
			ti.Focus()
		}
		inputs[i] = ti
	}

	return &MultiFieldDialog{
		title:     title,
		fields:    fields,
		inputs:    inputs,
		errors:    errors,
		focused:   0,
		result:    DialogResultNone,
		keys:      DefaultDialogKeyMap(),
		styles:    DefaultStyles(),
		width:     60,
		isFocused: true,
		debug:     false,
	}
}

// SetDebug enables or disables debug logging for validation.
func (d *MultiFieldDialog) SetDebug(debug bool) {
	d.debug = debug
}

// SetWidth sets the dialog width.
func (d *MultiFieldDialog) SetWidth(width int) {
	d.width = width
	inputWidth := width - 8 // Account for padding/borders
	for i := range d.inputs {
		d.inputs[i].Width = inputWidth
	}
}

// SetStyles sets the styles for the dialog.
func (d *MultiFieldDialog) SetStyles(styles Styles) {
	d.styles = styles
	for i := range d.inputs {
		d.inputs[i].Cursor.Style = styles.InputCursor
	}
}

// Focus focuses the dialog.
func (d *MultiFieldDialog) Focus() {
	d.isFocused = true
	if d.focused >= 0 && d.focused < len(d.inputs) {
		d.inputs[d.focused].Focus()
	}
}

// Blur blurs the dialog.
func (d *MultiFieldDialog) Blur() {
	d.isFocused = false
	for i := range d.inputs {
		d.inputs[i].Blur()
	}
}

// Values returns all field values as a slice.
func (d *MultiFieldDialog) Values() []string {
	values := make([]string, len(d.inputs))
	for i, input := range d.inputs {
		values[i] = input.Value()
	}
	return values
}

// ValueMap returns all field values as a map keyed by label.
func (d *MultiFieldDialog) ValueMap() map[string]string {
	result := make(map[string]string)
	for i, field := range d.fields {
		result[field.Label] = d.inputs[i].Value()
	}
	return result
}

// Result returns the dialog result.
func (d *MultiFieldDialog) Result() DialogResult {
	return d.result
}

// Reset resets the dialog to its initial state.
func (d *MultiFieldDialog) Reset() {
	for i := range d.inputs {
		d.inputs[i].Reset()
		d.inputs[i].SetValue(d.fields[i].Value)
		d.inputs[i].Blur()
	}
	d.ClearErrors()
	d.focused = 0
	d.result = DialogResultNone
	if len(d.inputs) > 0 {
		d.inputs[0].Focus()
	}
}

// Validate checks if all required fields have values and runs custom validators.
// Returns true if all validations pass, false otherwise.
// Updates the errors slice with any validation error messages.
func (d *MultiFieldDialog) Validate() bool {
	valid := true
	for i, field := range d.fields {
		d.errors[i] = ""
		value := d.inputs[i].Value()

		// Check required field
		if field.Required && strings.TrimSpace(value) == "" {
			d.errors[i] = "This field is required"
			valid = false
			if d.debug {
				slog.Debug("validation failed: required field empty",
					"field", field.Label,
					"index", i)
			}
			continue
		}

		// Run custom validator if present
		if field.Validate != nil {
			if errMsg := field.Validate(value); errMsg != "" {
				d.errors[i] = errMsg
				valid = false
				if d.debug {
					slog.Debug("validation failed: custom validator",
						"field", field.Label,
						"index", i,
						"error", errMsg)
				}
			}
		}
	}
	return valid
}

// ValidateField validates a single field by index and updates its error state.
func (d *MultiFieldDialog) ValidateField(index int) bool {
	if index < 0 || index >= len(d.fields) {
		return true
	}

	field := d.fields[index]
	value := d.inputs[index].Value()
	d.errors[index] = ""

	// Check required field
	if field.Required && strings.TrimSpace(value) == "" {
		d.errors[index] = "This field is required"
		if d.debug {
			slog.Debug("field validation failed: required field empty",
				"field", field.Label,
				"index", index)
		}
		return false
	}

	// Run custom validator if present
	if field.Validate != nil {
		if errMsg := field.Validate(value); errMsg != "" {
			d.errors[index] = errMsg
			if d.debug {
				slog.Debug("field validation failed: custom validator",
					"field", field.Label,
					"index", index,
					"error", errMsg)
			}
			return false
		}
	}

	return true
}

// GetError returns the validation error for a field by index.
func (d *MultiFieldDialog) GetError(index int) string {
	if index < 0 || index >= len(d.errors) {
		return ""
	}
	return d.errors[index]
}

// ClearErrors clears all validation errors.
func (d *MultiFieldDialog) ClearErrors() {
	for i := range d.errors {
		d.errors[i] = ""
	}
}

// focusField focuses a specific field by index.
// Validates the current field before moving to the next.
func (d *MultiFieldDialog) focusField(index int) {
	if index < 0 || index >= len(d.inputs) {
		return
	}

	// Validate and blur current field
	if d.focused >= 0 && d.focused < len(d.inputs) {
		d.ValidateField(d.focused)
		d.inputs[d.focused].Blur()
	}

	// Focus new field
	d.focused = index
	d.inputs[d.focused].Focus()
}

// Update handles messages for the dialog.
func (d *MultiFieldDialog) Update(msg tea.Msg) (*MultiFieldDialog, tea.Cmd) {
	if !d.isFocused {
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case key.Matches(msg, d.keys.Cancel):
			d.result = DialogResultCancel
			return d, nil
		case key.Matches(msg, d.keys.Submit):
			// Only submit if we're on the last field or all fields are filled
			if d.focused == len(d.inputs)-1 || d.Validate() {
				d.result = DialogResultSubmit
				return d, nil
			}
			// Otherwise move to next field
			d.focusField(d.focused + 1)
			return d, nil
		case key.Matches(msg, d.keys.Tab):
			// Move to next field
			next := (d.focused + 1) % len(d.inputs)
			d.focusField(next)
			return d, nil
		case msg.String() == "shift+tab":
			// Move to previous field
			prev := d.focused - 1
			if prev < 0 {
				prev = len(d.inputs) - 1
			}
			d.focusField(prev)
			return d, nil
		case msg.String() == "up":
			// Move to previous field
			if d.focused > 0 {
				d.focusField(d.focused - 1)
			}
			return d, nil
		case msg.String() == "down":
			// Move to next field
			if d.focused < len(d.inputs)-1 {
				d.focusField(d.focused + 1)
			}
			return d, nil
		}
	}

	// Update the focused input
	var cmd tea.Cmd
	if d.focused >= 0 && d.focused < len(d.inputs) {
		d.inputs[d.focused], cmd = d.inputs[d.focused].Update(msg)
	}
	return d, cmd
}

// View renders the dialog.
func (d *MultiFieldDialog) View() string {
	var content strings.Builder

	// Title
	if d.title != "" {
		content.WriteString(d.styles.DialogTitle.Render(d.title))
		content.WriteString("\n\n")
	}

	// Fields
	for i, field := range d.fields {
		isFocused := i == d.focused && d.isFocused
		hasError := d.errors[i] != ""

		// Build label with styling
		var labelBuilder strings.Builder
		if isFocused {
			labelBuilder.WriteString(d.styles.FieldLabelFocused.Render(field.Label))
		} else {
			labelBuilder.WriteString(d.styles.FieldLabel.Render(field.Label))
		}

		// Add required indicator with red color
		if field.Required {
			labelBuilder.WriteString(" ")
			labelBuilder.WriteString(d.styles.FieldRequired.Render("*"))
		}

		content.WriteString(labelBuilder.String() + "\n")

		// Input field with focus indicator
		if isFocused {
			content.WriteString("▸ ")
		} else {
			content.WriteString("  ")
		}
		content.WriteString(d.inputs[i].View())

		// Show inline validation error below field
		if hasError {
			content.WriteString("\n  ")
			content.WriteString(d.styles.FieldError.Render("⚠ " + d.errors[i]))
		} else if field.Hint != "" && isFocused {
			// Show hint only when focused and no error
			content.WriteString("\n  ")
			content.WriteString(d.styles.FieldHint.Render(field.Hint))
		}

		if i < len(d.fields)-1 {
			content.WriteString("\n\n")
		}
	}
	content.WriteString("\n\n")

	// Help text
	help := d.styles.StatusKey.Render("tab") + " next field  " +
		d.styles.StatusKey.Render("↑↓") + " navigate  " +
		d.styles.StatusKey.Render("enter") + " submit  " +
		d.styles.StatusKey.Render("esc") + " cancel"
	content.WriteString(help)

	style := d.styles.Dialog
	if d.isFocused {
		style = d.styles.DialogFocused
	}

	// Wrap in dialog box
	return style.
		Width(d.width).
		Render(content.String())
}

// CommandAction represents an executable command in the palette.
type CommandAction struct {
	Name        string
	Description string
	Shortcut    string
	Action      string // Action identifier for handling
}

// CommandPaletteDialog is a searchable command palette overlay.
type CommandPaletteDialog struct {
	title    string
	input    textinput.Model
	commands []CommandAction
	filtered []CommandAction
	selected int
	result   DialogResult
	chosen   *CommandAction
	keys     DialogKeyMap
	styles   Styles
	width    int
	height   int
	focused  bool
}

// NewCommandPaletteDialog creates a new command palette dialog.
func NewCommandPaletteDialog(title string, commands []CommandAction) *CommandPaletteDialog {
	ti := textinput.New()
	ti.Focus()
	ti.CharLimit = 64
	ti.Width = 40
	ti.Placeholder = "Type to filter commands..."

	return &CommandPaletteDialog{
		title:    title,
		input:    ti,
		commands: commands,
		filtered: commands,
		selected: 0,
		result:   DialogResultNone,
		keys:     DefaultDialogKeyMap(),
		styles:   DefaultStyles(),
		width:    50,
		height:   20,
		focused:  true,
	}
}

// DefaultCommands returns the default set of palette commands.
func DefaultCommands() []CommandAction {
	return []CommandAction{
		{Name: "Activate Profile", Description: "Switch to the selected profile", Shortcut: "enter", Action: "activate"},
		{Name: "Backup Current Auth", Description: "Create a backup of current auth", Shortcut: "b", Action: "backup"},
		{Name: "Delete Profile", Description: "Delete the selected profile", Shortcut: "d", Action: "delete"},
		{Name: "Edit Profile", Description: "Edit profile details", Shortcut: "e", Action: "edit"},
		{Name: "Login/Refresh", Description: "Refresh authentication", Shortcut: "l", Action: "login"},
		{Name: "Open in Browser", Description: "Open provider in browser", Shortcut: "o", Action: "open"},
		{Name: "Set Project Association", Description: "Link profile to current project", Shortcut: "p", Action: "project"},
		{Name: "Usage Statistics", Description: "View usage stats", Shortcut: "u", Action: "usage"},
		{Name: "Sync Pool", Description: "Manage sync pool", Shortcut: "S", Action: "sync"},
		{Name: "Export Vault", Description: "Export profiles to vault", Shortcut: "E", Action: "export"},
		{Name: "Import Bundle", Description: "Import profiles from bundle", Shortcut: "I", Action: "import"},
		{Name: "Help", Description: "Show help screen", Shortcut: "?", Action: "help"},
	}
}

// SetStyles sets the styles for the dialog.
func (d *CommandPaletteDialog) SetStyles(styles Styles) {
	d.styles = styles
	d.input.Cursor.Style = styles.InputCursor
}

// Focus focuses the dialog.
func (d *CommandPaletteDialog) Focus() {
	d.focused = true
	d.input.Focus()
}

// Blur blurs the dialog.
func (d *CommandPaletteDialog) Blur() {
	d.focused = false
	d.input.Blur()
}

// Result returns the dialog result.
func (d *CommandPaletteDialog) Result() DialogResult {
	return d.result
}

// ChosenCommand returns the selected command (if any).
func (d *CommandPaletteDialog) ChosenCommand() *CommandAction {
	return d.chosen
}

// Reset resets the dialog to its initial state.
func (d *CommandPaletteDialog) Reset() {
	d.input.Reset()
	d.result = DialogResultNone
	d.chosen = nil
	d.selected = 0
	d.filtered = d.commands
	d.input.Focus()
}

// SetWidth sets the dialog width.
func (d *CommandPaletteDialog) SetWidth(width int) {
	d.width = width
	d.input.Width = width - 8
}

// filterCommands filters commands based on the current input.
func (d *CommandPaletteDialog) filterCommands() {
	query := strings.ToLower(d.input.Value())
	if query == "" {
		d.filtered = d.commands
		return
	}

	var filtered []CommandAction
	for _, cmd := range d.commands {
		name := strings.ToLower(cmd.Name)
		desc := strings.ToLower(cmd.Description)
		shortcut := strings.ToLower(cmd.Shortcut)
		if strings.Contains(name, query) ||
			strings.Contains(desc, query) ||
			strings.Contains(shortcut, query) {
			filtered = append(filtered, cmd)
		}
	}
	d.filtered = filtered

	// Reset selection if out of bounds
	if d.selected >= len(d.filtered) {
		d.selected = 0
	}
}

// Update handles messages for the dialog.
func (d *CommandPaletteDialog) Update(msg tea.Msg) (*CommandPaletteDialog, tea.Cmd) {
	if !d.focused {
		return d, nil
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyEscape:
			d.result = DialogResultCancel
			return d, nil
		case tea.KeyEnter:
			if len(d.filtered) > 0 && d.selected < len(d.filtered) {
				d.chosen = &d.filtered[d.selected]
				d.result = DialogResultSubmit
			}
			return d, nil
		case tea.KeyUp, tea.KeyCtrlP:
			if d.selected > 0 {
				d.selected--
			}
			return d, nil
		case tea.KeyDown, tea.KeyCtrlN:
			if d.selected < len(d.filtered)-1 {
				d.selected++
			}
			return d, nil
		}
	}

	// Update input and filter
	var cmd tea.Cmd
	d.input, cmd = d.input.Update(msg)
	d.filterCommands()
	return d, cmd
}

// View renders the dialog.
func (d *CommandPaletteDialog) View() string {
	var content strings.Builder

	// Title
	if d.title != "" {
		content.WriteString(d.styles.DialogTitle.Render(d.title))
		content.WriteString("\n\n")
	}

	// Search input
	content.WriteString(d.input.View())
	content.WriteString("\n\n")

	// Command list
	maxVisible := 8
	if len(d.filtered) < maxVisible {
		maxVisible = len(d.filtered)
	}

	// Calculate scroll offset
	scrollOffset := 0
	if d.selected >= maxVisible {
		scrollOffset = d.selected - maxVisible + 1
	}

	for i := scrollOffset; i < scrollOffset+maxVisible && i < len(d.filtered); i++ {
		cmd := d.filtered[i]
		isSelected := i == d.selected

		// Format: [shortcut] Name - Description
		shortcutStyle := d.styles.StatusKey
		nameStyle := d.styles.Item
		if isSelected {
			nameStyle = d.styles.SelectedItem
		}

		shortcut := shortcutStyle.Render("[" + cmd.Shortcut + "]")
		name := nameStyle.Render(" " + cmd.Name)
		desc := d.styles.StatusText.Render(" - " + cmd.Description)

		line := shortcut + name + desc
		if isSelected {
			line = "▸ " + line
		} else {
			line = "  " + line
		}

		content.WriteString(line)
		if i < scrollOffset+maxVisible-1 && i < len(d.filtered)-1 {
			content.WriteString("\n")
		}
	}

	if len(d.filtered) == 0 {
		content.WriteString(d.styles.Empty.Render("No matching commands"))
	}

	content.WriteString("\n\n")

	// Help text
	help := d.styles.StatusKey.Render("↑↓") + " navigate  " +
		d.styles.StatusKey.Render("enter") + " select  " +
		d.styles.StatusKey.Render("esc") + " cancel"
	content.WriteString(help)

	style := d.styles.Dialog
	if d.focused {
		style = d.styles.DialogFocused
	}

	// Wrap in dialog box
	return style.
		Width(d.width).
		Render(content.String())
}
