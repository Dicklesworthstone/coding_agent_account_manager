package tui

import "github.com/charmbracelet/bubbles/key"

// keyMap defines all keybindings for the TUI.
type keyMap struct {
	// Navigation
	Up    key.Binding
	Down  key.Binding
	Left  key.Binding
	Right key.Binding
	Tab   key.Binding

	// Actions
	Enter   key.Binding
	Backup  key.Binding
	Delete  key.Binding
	Edit    key.Binding
	Login   key.Binding
	Open    key.Binding
	Search  key.Binding
	Project key.Binding
	Usage   key.Binding
	Sync    key.Binding
	Export  key.Binding
	Import  key.Binding
	Palette key.Binding

	// Confirmation
	Confirm key.Binding
	Cancel  key.Binding

	// General
	Help key.Binding
	Quit key.Binding
}

// defaultKeyMap returns the default keybindings.
func defaultKeyMap() keyMap {
	return keyMap{
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/k", "move up"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
			key.WithHelp("↓/j", "move down"),
		),
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/h", "previous provider"),
		),
		Right: key.NewBinding(
			key.WithKeys("right"),
			key.WithHelp("→", "next provider"),
		),
		Tab: key.NewBinding(
			key.WithKeys("tab"),
			key.WithHelp("tab", "cycle providers"),
		),
		Enter: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "activate profile"),
		),
		Backup: key.NewBinding(
			key.WithKeys("b"),
			key.WithHelp("b", "backup current auth"),
		),
		Delete: key.NewBinding(
			key.WithKeys("d"),
			key.WithHelp("d", "delete profile"),
		),
		Edit: key.NewBinding(
			key.WithKeys("e"),
			key.WithHelp("e", "edit profile"),
		),
		Login: key.NewBinding(
			key.WithKeys("l"),
			key.WithHelp("l", "login/refresh"),
		),
		Open: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "open in browser"),
		),
		Search: key.NewBinding(
			key.WithKeys("/"),
			key.WithHelp("/", "search profiles"),
		),
		Project: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "set project association"),
		),
		Usage: key.NewBinding(
			key.WithKeys("u"),
			key.WithHelp("u", "usage stats"),
		),
		Sync: key.NewBinding(
			key.WithKeys("S"),
			key.WithHelp("S", "sync pool"),
		),
		Export: key.NewBinding(
			key.WithKeys("E"),
			key.WithHelp("E", "export vault"),
		),
		Import: key.NewBinding(
			key.WithKeys("I"),
			key.WithHelp("I", "import bundle"),
		),
		Palette: key.NewBinding(
			key.WithKeys("ctrl+p"),
			key.WithHelp("ctrl+p", "command palette"),
		),
		Confirm: key.NewBinding(
			key.WithKeys("y", "enter"),
			key.WithHelp("y/enter", "confirm"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("n", "esc"),
			key.WithHelp("n/esc", "cancel"),
		),
		Help: key.NewBinding(
			key.WithKeys("?"),
			key.WithHelp("?", "toggle help"),
		),
		Quit: key.NewBinding(
			key.WithKeys("q", "esc", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
	}
}

// ShortHelp returns keybindings for the short help view.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Help, k.Quit}
}

// FullHelp returns keybindings for the full help view.
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Up, k.Down, k.Left, k.Right},
		{k.Enter, k.Backup, k.Delete, k.Edit},
		{k.Login, k.Open, k.Search, k.Project, k.Usage},
		{k.Sync, k.Export, k.Import},
		{k.Help, k.Quit},
	}
}
