package tui

import (
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

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
	input       textinput.Model
	result      DialogResult
	keys        DialogKeyMap
	styles      Styles
	width       int
	height      int
	focused     bool
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
			d.result = DialogResultSubmit
			return d, nil
		}
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

	// Prompt
	if d.prompt != "" {
		content.WriteString(d.prompt)
		content.WriteString("\n\n")
	}

	// Input field
	content.WriteString(d.input.View())
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
	title    string
	message  string
	yesLabel string
	noLabel  string
	selected int // 0 = no, 1 = yes
	result   DialogResult
	keys     DialogKeyMap
	styles   Styles
	width    int
	focused  bool
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
}

// MultiFieldDialog is a dialog with multiple input fields.
type MultiFieldDialog struct {
	title     string
	fields    []FieldDefinition
	inputs    []textinput.Model
	focused   int // Currently focused field index
	result    DialogResult
	keys      DialogKeyMap
	styles    Styles
	width     int
	isFocused bool
}

// NewMultiFieldDialog creates a new multi-field dialog.
func NewMultiFieldDialog(title string, fields []FieldDefinition) *MultiFieldDialog {
	inputs := make([]textinput.Model, len(fields))
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
		focused:   0,
		result:    DialogResultNone,
		keys:      DefaultDialogKeyMap(),
		styles:    DefaultStyles(),
		width:     60,
		isFocused: true,
	}
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
	d.focused = 0
	d.result = DialogResultNone
	if len(d.inputs) > 0 {
		d.inputs[0].Focus()
	}
}

// Validate checks if all required fields have values.
func (d *MultiFieldDialog) Validate() bool {
	for i, field := range d.fields {
		if field.Required && strings.TrimSpace(d.inputs[i].Value()) == "" {
			return false
		}
	}
	return true
}

// focusField focuses a specific field by index.
func (d *MultiFieldDialog) focusField(index int) {
	if index < 0 || index >= len(d.inputs) {
		return
	}

	// Blur current field
	if d.focused >= 0 && d.focused < len(d.inputs) {
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
		label := field.Label
		if field.Required {
			label += " *"
		}
		content.WriteString(label + "\n")
		content.WriteString(d.inputs[i].View())
		if i < len(d.fields)-1 {
			content.WriteString("\n\n")
		}
	}
	content.WriteString("\n\n")

	// Help text
	help := d.styles.StatusKey.Render("tab") + " next field  " +
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
