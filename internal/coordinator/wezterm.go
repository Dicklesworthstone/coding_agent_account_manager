// Package coordinator implements the auth recovery coordinator daemon
// that monitors WezTerm panes for rate limits and orchestrates authentication.
package coordinator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// WezTermClient wraps the wezterm CLI for pane operations.
type WezTermClient struct {
	binaryPath string
}

// NewWezTermClient creates a new WezTerm CLI client.
func NewWezTermClient() *WezTermClient {
	return &WezTermClient{
		binaryPath: "wezterm",
	}
}

// Pane represents a WezTerm pane.
type Pane struct {
	PaneID       int    `json:"pane_id"`
	WindowID     int    `json:"window_id"`
	TabID        int    `json:"tab_id"`
	WorkspaceID  string `json:"workspace,omitempty"`
	Domain       string `json:"domain,omitempty"`
	Title        string `json:"title"`
	CWD          string `json:"cwd,omitempty"`
	CursorX      int    `json:"cursor_x"`
	CursorY      int    `json:"cursor_y"`
	IsActive     bool   `json:"is_active"`
	IsZoomed     bool   `json:"is_zoomed"`
	Rows         int    `json:"size,omitempty"`
	Cols         int    `json:"cols,omitempty"`
	ForegroundPID int   `json:"foreground_process_id,omitempty"`
}

// ListPanes returns all panes across all windows.
func (c *WezTermClient) ListPanes(ctx context.Context) ([]Pane, error) {
	cmd := exec.CommandContext(ctx, c.binaryPath, "cli", "list", "--format", "json")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("wezterm cli list: %w (stderr: %s)", err, stderr.String())
	}

	var panes []Pane
	if err := json.Unmarshal(stdout.Bytes(), &panes); err != nil {
		return nil, fmt.Errorf("parse pane list: %w", err)
	}

	return panes, nil
}

// GetText retrieves text content from a pane.
// startLine is negative for lines from the end (e.g., -50 for last 50 lines).
func (c *WezTermClient) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	args := []string{"cli", "get-text", "--pane-id", strconv.Itoa(paneID)}
	if startLine != 0 {
		args = append(args, "--start-line", strconv.Itoa(startLine))
	}

	cmd := exec.CommandContext(ctx, c.binaryPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("wezterm cli get-text: %w (stderr: %s)", err, stderr.String())
	}

	return stdout.String(), nil
}

// SendText injects text into a pane.
// If noPaste is true, sends as keystrokes rather than bracketed paste.
func (c *WezTermClient) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	args := []string{"cli", "send-text", "--pane-id", strconv.Itoa(paneID)}
	if noPaste {
		args = append(args, "--no-paste")
	}
	args = append(args, text)

	cmd := exec.CommandContext(ctx, c.binaryPath, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wezterm cli send-text: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// SendKeys sends key events to a pane.
// Keys should be in WezTerm key notation (e.g., "Enter", "Escape").
func (c *WezTermClient) SendKeys(ctx context.Context, paneID int, keys ...string) error {
	for _, key := range keys {
		// Map common key names
		mapped := mapKey(key)
		if err := c.SendText(ctx, paneID, mapped, true); err != nil {
			return fmt.Errorf("send key %s: %w", key, err)
		}
		// Small delay between keys
		time.Sleep(50 * time.Millisecond)
	}
	return nil
}

// mapKey converts common key names to their actual characters/sequences.
func mapKey(key string) string {
	switch strings.ToLower(key) {
	case "enter", "return":
		return "\n"
	case "tab":
		return "\t"
	case "escape", "esc":
		return "\x1b"
	case "space":
		return " "
	case "backspace":
		return "\x7f"
	default:
		return key
	}
}

// ActivatePane brings a pane to focus.
func (c *WezTermClient) ActivatePane(ctx context.Context, paneID int) error {
	cmd := exec.CommandContext(ctx, c.binaryPath, "cli", "activate-pane", "--pane-id", strconv.Itoa(paneID))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("wezterm cli activate-pane: %w (stderr: %s)", err, stderr.String())
	}

	return nil
}

// IsAvailable checks if the wezterm CLI is available.
func (c *WezTermClient) IsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, c.binaryPath, "cli", "list", "--format", "json")
	return cmd.Run() == nil
}

// Backend returns the backend name.
func (c *WezTermClient) Backend() string {
	return "wezterm"
}
