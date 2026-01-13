// Package coordinator implements the auth recovery coordinator daemon.
package coordinator

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// TmuxClient wraps the tmux CLI for pane operations.
//
// This is a FALLBACK for terminals without built-in multiplexing (e.g., Ghostty,
// Alacritty, iTerm2). For best experience, use WezTerm with its native mux-server.
//
// Limitations compared to WezTerm:
//   - No domain awareness - tmux doesn't know which pane connects to which machine
//   - Less metadata - no workspace, foreground PID, or cursor position
//   - Extra process layer - terminal -> tmux -> shell adds complexity
//   - Session management - requires tmux server running and proper attach workflow
//
// Tmux pane IDs are strings like "%0", "%1", etc. We strip the "%" prefix
// and use the numeric portion as our integer PaneID for compatibility with
// the existing coordinator infrastructure.
type TmuxClient struct {
	binaryPath string
}

// NewTmuxClient creates a new tmux CLI client.
func NewTmuxClient() *TmuxClient {
	return &TmuxClient{
		binaryPath: "tmux",
	}
}

// ListPanes returns all panes across all sessions.
func (c *TmuxClient) ListPanes(ctx context.Context) ([]Pane, error) {
	// Format string for tmux list-panes:
	// #{pane_id} - unique ID like %0, %1
	// #{session_name} - session name
	// #{window_index} - window number within session
	// #{pane_index} - pane number within window
	// #{pane_title} - pane title
	// #{pane_current_path} - current working directory
	// #{pane_active} - 1 if active, 0 otherwise
	// #{pane_width} - width in columns
	// #{pane_height} - height in rows
	format := "#{pane_id}\t#{session_name}\t#{window_index}\t#{pane_index}\t#{pane_title}\t#{pane_current_path}\t#{pane_active}\t#{pane_width}\t#{pane_height}"

	cmd := exec.CommandContext(ctx, c.binaryPath, "list-panes", "-a", "-F", format)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Check if tmux server is not running
		if strings.Contains(stderr.String(), "no server running") {
			return nil, fmt.Errorf("tmux server not running (start with: tmux new-session -d)")
		}
		return nil, fmt.Errorf("tmux list-panes: %w (stderr: %s)", err, stderr.String())
	}

	var panes []Pane
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")

	for _, line := range lines {
		if line == "" {
			continue
		}

		pane, err := c.parsePaneLine(line)
		if err != nil {
			continue // Skip malformed lines
		}
		panes = append(panes, pane)
	}

	return panes, nil
}

// parsePaneLine parses a single line of tmux list-panes output.
func (c *TmuxClient) parsePaneLine(line string) (Pane, error) {
	parts := strings.Split(line, "\t")
	if len(parts) < 9 {
		return Pane{}, fmt.Errorf("malformed pane line: %s", line)
	}

	// Parse pane ID: "%0" -> 0
	paneIDStr := parts[0]
	if !strings.HasPrefix(paneIDStr, "%") {
		return Pane{}, fmt.Errorf("unexpected pane ID format: %s", paneIDStr)
	}
	paneID, err := strconv.Atoi(paneIDStr[1:])
	if err != nil {
		return Pane{}, fmt.Errorf("parse pane ID: %w", err)
	}

	// Parse window index
	windowIndex, _ := strconv.Atoi(parts[2])

	// Parse active flag
	isActive := parts[6] == "1"

	// Parse dimensions
	cols, _ := strconv.Atoi(parts[7])
	rows, _ := strconv.Atoi(parts[8])

	// Build domain string from session:window.pane for identification
	// This helps users identify which tmux session/window contains a pane
	sessionName := parts[1]
	paneIndex := parts[3]
	domain := fmt.Sprintf("%s:%s.%s", sessionName, parts[2], paneIndex)

	return Pane{
		PaneID:   paneID,
		WindowID: windowIndex, // Map to window index
		TabID:    0,           // tmux doesn't have tabs
		Domain:   domain,      // session:window.pane for identification
		Title:    parts[4],
		CWD:      parts[5],
		IsActive: isActive,
		Cols:     cols,
		Rows:     rows,
		// Note: tmux doesn't provide cursor position or foreground PID easily
	}, nil
}

// GetText retrieves text content from a pane.
// startLine is negative for lines from the end (e.g., -50 for last 50 lines).
func (c *TmuxClient) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	target := fmt.Sprintf("%%%d", paneID)

	args := []string{"capture-pane", "-t", target, "-p"}
	if startLine != 0 {
		args = append(args, "-S", strconv.Itoa(startLine))
	}

	cmd := exec.CommandContext(ctx, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux capture-pane: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// SendText injects text into a pane.
// If noPaste is true, sends as literal keystrokes rather than via paste buffer.
func (c *TmuxClient) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	target := fmt.Sprintf("%%%d", paneID)

	var args []string
	if noPaste {
		// Send as literal text (like typing)
		args = []string{"send-keys", "-t", target, "-l", text}
	} else {
		// Send through paste buffer (bracketed paste mode if terminal supports it)
		args = []string{"send-keys", "-t", target, text}
	}

	cmd := exec.CommandContext(ctx, c.binaryPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// IsAvailable checks if tmux is available and a server is running.
func (c *TmuxClient) IsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, c.binaryPath, "list-sessions")
	return cmd.Run() == nil
}

// Backend returns the backend name.
func (c *TmuxClient) Backend() string {
	return "tmux"
}
