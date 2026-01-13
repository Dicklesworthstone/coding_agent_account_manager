// Package coordinator implements the auth recovery coordinator daemon.
package coordinator

import (
	"context"
)

// PaneClient is the interface for terminal multiplexer backends.
// Implementations include WezTermClient (preferred) and TmuxClient (fallback).
//
// WezTerm is the PREFERRED backend because:
//   - Integrated multiplexer - panes ARE your terminal panes, no extra layer
//   - Domain-aware - ssh_domains config maps panes to remote machines
//   - Richer metadata - window/tab/workspace info, cursor position, foreground PID
//   - Seamless setup - auto-connects to remote mux-servers on startup
//
// Tmux is supported as a FALLBACK for terminals without built-in multiplexing
// (e.g., Ghostty, Alacritty, iTerm2). Drawbacks compared to WezTerm:
//   - Extra process layer - terminal -> tmux -> shell
//   - No machine context - can't automatically know which pane connects where
//   - Session management - requires tmux server running, attach/detach workflow
//   - Less metadata - no equivalent of WezTerm's domain/workspace concepts
type PaneClient interface {
	// ListPanes returns all panes across all windows/sessions.
	ListPanes(ctx context.Context) ([]Pane, error)

	// GetText retrieves text content from a pane.
	// startLine is negative for lines from the end (e.g., -50 for last 50 lines).
	GetText(ctx context.Context, paneID int, startLine int) (string, error)

	// SendText injects text into a pane.
	// If noPaste is true, sends as keystrokes rather than bracketed paste.
	SendText(ctx context.Context, paneID int, text string, noPaste bool) error

	// IsAvailable checks if the backend is available and functional.
	IsAvailable(ctx context.Context) bool

	// Backend returns the name of this backend ("wezterm" or "tmux").
	Backend() string
}

// Ensure implementations satisfy the interface.
var (
	_ PaneClient = (*WezTermClient)(nil)
	_ PaneClient = (*TmuxClient)(nil)
)
