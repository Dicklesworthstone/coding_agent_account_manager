// Package coordinator implements the auth recovery coordinator daemon.
package coordinator

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
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
	_ PaneClient = (*autoPaneClient)(nil)
)

// TypedText prepares text for typing into a pane. Callers end text with "\n"
// to mean "press Enter"; the Enter key sends a carriage return, which is
// what programs in raw mode (Claude Code's input among them) submit on. A
// line feed is Ctrl+J, which Claude Code takes as "insert a newline", so a
// trailing "\n" would leave the command unsubmitted in the input box.
// Newlines inside the text stay line feeds, keeping a multi-line prompt in
// one message.
func TypedText(text string) string {
	if trimmed, ok := strings.CutSuffix(text, "\n"); ok {
		return strings.TrimSuffix(trimmed, "\r") + "\r"
	}
	return text
}

// Control keys, each sent on its own: a lone Esc is told apart from the start
// of an escape sequence by the pause after it.
const (
	KeyEscape    = "\x1b"
	KeyEndOfLine = "\x05" // Ctrl+E
	KeyKillLine  = "\x15" // Ctrl+U: delete to the start of the line
)

// KeyGap is the pause between keys sent separately, long enough for Claude
// Code to handle each one (and for a dismissed menu to hand focus back to the
// prompt) before the next arrives.
const KeyGap = 300 * time.Millisecond

// LoginKeys returns the keys, to be sent one at a time KeyGap apart, that run
// /login in a Claude Code pane whose screen shows output.
//
// At a usage limit Claude Code opens a "What do you want to do?" menu. Typed
// into it, "/login" is ignored and its Enter picks the highlighted option,
// which can be paid extra usage ("Switch to usage credits") or a plan
// upgrade. An open menu is therefore closed with Esc, which Claude Code
// treats like "Stop and wait for limit to reset". Ctrl+E Ctrl+U then empty
// the prompt line: Claude Code can prefill "continue" there, and text left in
// front of "/login" would be sent to the model as a message instead of
// running the command. (Ctrl+Y brings back what was cleared.)
func LoginKeys(output string) []string {
	var keys []string
	if RateLimitMenuOpen(output) {
		keys = append(keys, KeyEscape)
	}
	return append(keys, KeyEndOfLine, KeyKillLine, "/login\n")
}

// PromptKeys returns the keys, sent one at a time KeyGap apart, that submit
// prompt in an emptied prompt line: text left there (Claude Code's
// "continue" prefill, say) would otherwise be sent in front of it.
func PromptKeys(prompt string) []string {
	return []string{KeyEndOfLine, KeyKillLine, prompt}
}

// RateLimitMenuOpen reports whether Claude Code's usage-limit menu is on
// screen in output.
func RateLimitMenuOpen(output string) bool {
	return atBottom(output, Patterns.RateLimitMenu)
}

// sendKeys types keys into a pane one at a time, KeyGap apart.
func sendKeys(ctx context.Context, client PaneClient, paneID int, keys []string) error {
	for i, key := range keys {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(KeyGap):
			}
		}
		if err := client.SendText(ctx, paneID, key, true); err != nil {
			return err
		}
	}
	return nil
}

// autoPaneClient follows whichever multiplexer is answering, in preference
// order. A coordinator started before the user's multiplexer (by systemd at
// boot, say) would otherwise stay bound to whatever a one-time probe found.
// The backend changes only when the current one stops answering, so panes
// are never interleaved from two backends.
type autoPaneClient struct {
	clients []PaneClient
	logger  *slog.Logger

	mu      sync.Mutex
	current PaneClient
}

func newAutoPaneClient(ctx context.Context, logger *slog.Logger, clients ...PaneClient) *autoPaneClient {
	a := &autoPaneClient{clients: clients, logger: logger, current: clients[0]}
	for _, c := range clients {
		if c.IsAvailable(ctx) {
			a.current = c
			break
		}
	}
	return a
}

func (a *autoPaneClient) active() PaneClient {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.current
}

// ListPanes lists the current backend's panes, switching to the first other
// backend that answers when the current one fails.
func (a *autoPaneClient) ListPanes(ctx context.Context) ([]Pane, error) {
	current := a.active()
	panes, err := current.ListPanes(ctx)
	if err == nil {
		return panes, nil
	}
	for _, c := range a.clients {
		if c == current {
			continue
		}
		if other, otherErr := c.ListPanes(ctx); otherErr == nil {
			a.mu.Lock()
			a.current = c
			a.mu.Unlock()
			a.logger.Info("terminal multiplexer backend changed",
				"from", current.Backend(),
				"to", c.Backend(),
				"reason", err.Error())
			return other, nil
		}
	}
	return nil, err
}

func (a *autoPaneClient) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	return a.active().GetText(ctx, paneID, startLine)
}

func (a *autoPaneClient) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	return a.active().SendText(ctx, paneID, text, noPaste)
}

func (a *autoPaneClient) IsAvailable(ctx context.Context) bool {
	for _, c := range a.clients {
		if c.IsAvailable(ctx) {
			return true
		}
	}
	return false
}

func (a *autoPaneClient) Backend() string {
	return a.active().Backend()
}
