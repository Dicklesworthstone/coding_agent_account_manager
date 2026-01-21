// Package testutil provides E2E test infrastructure with detailed logging.
//
// WezTermMockHarness provides a mock WezTerm CLI environment for testing
// pane discovery and recovery logic without requiring a running WezTerm server.
package testutil

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// WezTermMockHarness - Mock WezTerm CLI for testing
// =============================================================================

// WezTermPane represents a mock WezTerm pane for test fixtures.
type WezTermPane struct {
	PaneID      int    `json:"pane_id"`
	WindowID    int    `json:"window_id"`
	TabID       int    `json:"tab_id"`
	Title       string `json:"title"`
	CWD         string `json:"cwd,omitempty"`
	CursorX     int    `json:"cursor_x"`
	CursorY     int    `json:"cursor_y"`
	IsActive    bool   `json:"is_active"`
	IsZoomed    bool   `json:"is_zoomed"`
	Domain      string `json:"domain,omitempty"`
	Workspace   string `json:"workspace,omitempty"`
}

// SendTextCall represents a captured send-text call for assertions.
type SendTextCall struct {
	PaneID    int
	Text      string
	NoPaste   bool
	Timestamp time.Time
}

// GetTextCall represents a captured get-text call for assertions.
type GetTextCall struct {
	PaneID    int
	StartLine int
	Timestamp time.Time
}

// WezTermMockHarness provides a complete mock WezTerm environment for testing.
type WezTermMockHarness struct {
	*TestHarness

	mu sync.Mutex

	// Pane configuration
	panes    []WezTermPane
	paneText map[int]string // pane_id -> text content

	// Scripted sequences for state machine testing
	// Key is pane_id, value is slice of text responses (consumed in order)
	sequences map[int][]string

	// Captured calls for assertions
	sendTextCalls []SendTextCall
	getTextCalls  []GetTextCall
	listCalls     int

	// Error injection
	listError    error
	getTextError map[int]error
	sendError    map[int]error

	// Mock binary path (if using CLI mock)
	mockBinaryPath string
	mockDataDir    string
}

// NewWezTermMockHarness creates a new WezTerm mock harness.
func NewWezTermMockHarness(t *testing.T) *WezTermMockHarness {
	h := NewHarness(t)
	return &WezTermMockHarness{
		TestHarness:  h,
		panes:        make([]WezTermPane, 0),
		paneText:     make(map[int]string),
		sequences:    make(map[int][]string),
		getTextError: make(map[int]error),
		sendError:    make(map[int]error),
	}
}

// =============================================================================
// Pane Configuration Methods
// =============================================================================

// AddPane adds a mock pane to the harness.
func (h *WezTermMockHarness) AddPane(pane WezTermPane) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.panes = append(h.panes, pane)
	h.Log.Debug("Added mock pane", map[string]interface{}{
		"pane_id": pane.PaneID,
		"title":   pane.Title,
	})
}

// AddPaneSimple adds a simple mock pane with minimal configuration.
func (h *WezTermMockHarness) AddPaneSimple(paneID int, title string) {
	h.AddPane(WezTermPane{
		PaneID:   paneID,
		WindowID: 1,
		TabID:    1,
		Title:    title,
	})
}

// SetPaneText sets the text content for a pane.
func (h *WezTermMockHarness) SetPaneText(paneID int, text string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.paneText[paneID] = text
	h.Log.Debug("Set pane text", map[string]interface{}{
		"pane_id": paneID,
		"length":  len(text),
	})
}

// SetPaneSequence sets a sequence of text responses for a pane.
// Each call to GetText will return the next item in the sequence.
// After exhaustion, returns the last item repeatedly.
func (h *WezTermMockHarness) SetPaneSequence(paneID int, sequence []string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sequences[paneID] = sequence
	h.Log.Debug("Set pane sequence", map[string]interface{}{
		"pane_id": paneID,
		"length":  len(sequence),
	})
}

// =============================================================================
// Error Injection Methods
// =============================================================================

// SetListError injects an error for ListPanes calls.
func (h *WezTermMockHarness) SetListError(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.listError = err
}

// SetGetTextError injects an error for GetText calls on a specific pane.
func (h *WezTermMockHarness) SetGetTextError(paneID int, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.getTextError[paneID] = err
}

// SetSendError injects an error for SendText calls on a specific pane.
func (h *WezTermMockHarness) SetSendError(paneID int, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.sendError[paneID] = err
}

// =============================================================================
// PaneClient Interface Implementation (for coordinator package)
// =============================================================================

// ListPanes returns all configured mock panes.
func (h *WezTermMockHarness) ListPanes(ctx context.Context) ([]WezTermPane, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.listCalls++

	if h.listError != nil {
		return nil, h.listError
	}

	// Return a copy to prevent mutation
	result := make([]WezTermPane, len(h.panes))
	copy(result, h.panes)
	return result, nil
}

// GetText returns the configured text for a pane.
func (h *WezTermMockHarness) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.getTextCalls = append(h.getTextCalls, GetTextCall{
		PaneID:    paneID,
		StartLine: startLine,
		Timestamp: time.Now(),
	})

	if err, ok := h.getTextError[paneID]; ok && err != nil {
		return "", err
	}

	// Check for sequence first
	if seq, ok := h.sequences[paneID]; ok && len(seq) > 0 {
		text := seq[0]
		if len(seq) > 1 {
			h.sequences[paneID] = seq[1:]
		}
		return text, nil
	}

	// Fall back to static text
	if text, ok := h.paneText[paneID]; ok {
		return text, nil
	}

	return "", fmt.Errorf("pane %d not found", paneID)
}

// SendText captures the send-text call and returns any injected error.
func (h *WezTermMockHarness) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sendTextCalls = append(h.sendTextCalls, SendTextCall{
		PaneID:    paneID,
		Text:      text,
		NoPaste:   noPaste,
		Timestamp: time.Now(),
	})

	h.Log.Debug("SendText called", map[string]interface{}{
		"pane_id":  paneID,
		"text_len": len(text),
		"no_paste": noPaste,
	})

	if err, ok := h.sendError[paneID]; ok && err != nil {
		return err
	}

	return nil
}

// IsAvailable always returns true for the mock.
func (h *WezTermMockHarness) IsAvailable(ctx context.Context) bool {
	return true
}

// Backend returns "wezterm-mock".
func (h *WezTermMockHarness) Backend() string {
	return "wezterm-mock"
}

// =============================================================================
// Assertion Methods
// =============================================================================

// GetSendTextCalls returns all captured send-text calls.
func (h *WezTermMockHarness) GetSendTextCalls() []SendTextCall {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := make([]SendTextCall, len(h.sendTextCalls))
	copy(result, h.sendTextCalls)
	return result
}

// GetGetTextCalls returns all captured get-text calls.
func (h *WezTermMockHarness) GetGetTextCalls() []GetTextCall {
	h.mu.Lock()
	defer h.mu.Unlock()

	result := make([]GetTextCall, len(h.getTextCalls))
	copy(result, h.getTextCalls)
	return result
}

// GetListCallCount returns the number of ListPanes calls.
func (h *WezTermMockHarness) GetListCallCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.listCalls
}

// AssertSendTextCount asserts the number of send-text calls.
func (h *WezTermMockHarness) AssertSendTextCount(expected int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	if len(h.sendTextCalls) != expected {
		h.T.Errorf("SendText call count: expected %d, got %d", expected, len(h.sendTextCalls))
		return false
	}
	return true
}

// AssertSendTextToPane asserts that send-text was called on a specific pane with expected text.
func (h *WezTermMockHarness) AssertSendTextToPane(paneID int, expectedText string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, call := range h.sendTextCalls {
		if call.PaneID == paneID && call.Text == expectedText {
			return true
		}
	}

	h.T.Errorf("SendText to pane %d with text %q: not found in calls", paneID, expectedText)
	for i, call := range h.sendTextCalls {
		h.T.Logf("  Call %d: pane=%d, text=%q", i, call.PaneID, call.Text)
	}
	return false
}

// AssertSendTextContains asserts that send-text was called with text containing substring.
func (h *WezTermMockHarness) AssertSendTextContains(substring string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, call := range h.sendTextCalls {
		if strings.Contains(call.Text, substring) {
			return true
		}
	}

	h.T.Errorf("SendText containing %q: not found in calls", substring)
	return false
}

// AssertNoSendTextToPane asserts that send-text was NOT called on a specific pane.
func (h *WezTermMockHarness) AssertNoSendTextToPane(paneID int) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	for _, call := range h.sendTextCalls {
		if call.PaneID == paneID {
			h.T.Errorf("SendText to pane %d: unexpected call with text %q", paneID, call.Text)
			return false
		}
	}
	return true
}

// ClearCalls resets all captured calls.
func (h *WezTermMockHarness) ClearCalls() {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.sendTextCalls = h.sendTextCalls[:0]
	h.getTextCalls = h.getTextCalls[:0]
	h.listCalls = 0
}

// =============================================================================
// Mock CLI Binary Generation (for cmd package integration tests)
// =============================================================================

// SetupMockCLI creates a mock wezterm CLI script and configures the environment.
// Returns the path to the mock binary directory (should be prepended to PATH).
func (h *WezTermMockHarness) SetupMockCLI() string {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Create directories
	h.mockDataDir = h.SubDir("wezterm-mock-data")
	binDir := h.SubDir("wezterm-mock-bin")
	h.mockBinaryPath = filepath.Join(binDir, "wezterm")

	// Write the mock script
	script := h.generateMockScript()
	if err := os.WriteFile(h.mockBinaryPath, []byte(script), 0755); err != nil {
		h.T.Fatalf("Failed to write mock wezterm script: %v", err)
	}

	// Write initial fixture data
	h.writeMockData()

	h.Log.Info("Mock CLI setup complete", map[string]interface{}{
		"binary_path": h.mockBinaryPath,
		"data_dir":    h.mockDataDir,
	})

	return binDir
}

// generateMockScript generates a bash script that emulates wezterm CLI.
func (h *WezTermMockHarness) generateMockScript() string {
	return fmt.Sprintf(`#!/bin/bash
# Mock wezterm CLI generated by WezTermMockHarness

DATA_DIR="%s"
SEND_LOG="$DATA_DIR/send_text.log"

if [[ "$1" != "cli" ]]; then
    echo "Usage: wezterm cli <command>" >&2
    exit 1
fi

shift

case "$1" in
    list)
        if [[ "$2" == "--format" && "$3" == "json" ]]; then
            cat "$DATA_DIR/panes.json"
        else
            echo "list requires --format json" >&2
            exit 1
        fi
        ;;
    get-text)
        PANE_ID=""
        START_LINE=""
        shift
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --pane-id)
                    PANE_ID="$2"
                    shift 2
                    ;;
                --start-line)
                    START_LINE="$2"
                    shift 2
                    ;;
                *)
                    shift
                    ;;
            esac
        done
        if [[ -z "$PANE_ID" ]]; then
            echo "get-text requires --pane-id" >&2
            exit 1
        fi
        TEXT_FILE="$DATA_DIR/pane_${PANE_ID}_text.txt"
        SEQ_FILE="$DATA_DIR/pane_${PANE_ID}_seq.txt"

        # Check for sequence file first
        if [[ -f "$SEQ_FILE" ]]; then
            # Read first line and remove it
            head -n1 "$SEQ_FILE"
            tail -n +2 "$SEQ_FILE" > "$SEQ_FILE.tmp" && mv "$SEQ_FILE.tmp" "$SEQ_FILE"
        elif [[ -f "$TEXT_FILE" ]]; then
            cat "$TEXT_FILE"
        else
            echo "pane $PANE_ID not found" >&2
            exit 1
        fi
        ;;
    send-text)
        PANE_ID=""
        NO_PASTE=""
        TEXT=""
        shift
        while [[ $# -gt 0 ]]; do
            case "$1" in
                --pane-id)
                    PANE_ID="$2"
                    shift 2
                    ;;
                --no-paste)
                    NO_PASTE="true"
                    shift
                    ;;
                *)
                    TEXT="$1"
                    shift
                    ;;
            esac
        done
        # Log the call
        echo "$(date -Iseconds)|$PANE_ID|$NO_PASTE|$TEXT" >> "$SEND_LOG"

        # Check for error injection
        ERR_FILE="$DATA_DIR/pane_${PANE_ID}_send_error.txt"
        if [[ -f "$ERR_FILE" ]]; then
            cat "$ERR_FILE" >&2
            exit 1
        fi
        ;;
    activate-pane)
        # No-op for mock
        ;;
    *)
        echo "Unknown command: $1" >&2
        exit 1
        ;;
esac
`, h.mockDataDir)
}

// writeMockData writes the current pane configuration to mock data files.
func (h *WezTermMockHarness) writeMockData() {
	// Write panes.json
	panesJSON, err := json.MarshalIndent(h.panes, "", "  ")
	if err != nil {
		h.T.Fatalf("Failed to marshal panes: %v", err)
	}
	panesPath := filepath.Join(h.mockDataDir, "panes.json")
	if err := os.WriteFile(panesPath, panesJSON, 0644); err != nil {
		h.T.Fatalf("Failed to write panes.json: %v", err)
	}

	// Write individual pane text files
	for paneID, text := range h.paneText {
		textPath := filepath.Join(h.mockDataDir, fmt.Sprintf("pane_%d_text.txt", paneID))
		if err := os.WriteFile(textPath, []byte(text), 0644); err != nil {
			h.T.Fatalf("Failed to write pane text: %v", err)
		}
	}

	// Write sequence files
	for paneID, seq := range h.sequences {
		seqPath := filepath.Join(h.mockDataDir, fmt.Sprintf("pane_%d_seq.txt", paneID))
		content := strings.Join(seq, "\n---SEQUENCE_SEPARATOR---\n")
		if err := os.WriteFile(seqPath, []byte(content), 0644); err != nil {
			h.T.Fatalf("Failed to write pane sequence: %v", err)
		}
	}

	// Write error injection files
	for paneID, err := range h.sendError {
		if err != nil {
			errPath := filepath.Join(h.mockDataDir, fmt.Sprintf("pane_%d_send_error.txt", paneID))
			if err := os.WriteFile(errPath, []byte(err.Error()), 0644); err != nil {
				h.T.Fatalf("Failed to write send error: %v", err)
			}
		}
	}
}

// UpdateMockData updates the mock data files after configuration changes.
// Call this after modifying panes, text, or errors to update the mock CLI.
func (h *WezTermMockHarness) UpdateMockData() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.mockDataDir == "" {
		return // Mock CLI not set up
	}
	h.writeMockData()
}

// ReadSendTextLog reads the send-text calls logged by the mock CLI.
func (h *WezTermMockHarness) ReadSendTextLog() []SendTextCall {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.mockDataDir == "" {
		return nil
	}

	logPath := filepath.Join(h.mockDataDir, "send_text.log")
	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		h.T.Logf("Warning: could not read send_text.log: %v", err)
		return nil
	}

	var calls []SendTextCall
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		timestamp, _ := time.Parse(time.RFC3339, parts[0])
		paneID, _ := strconv.Atoi(parts[1])
		noPaste := parts[2] == "true"
		text := parts[3]
		calls = append(calls, SendTextCall{
			PaneID:    paneID,
			Text:      text,
			NoPaste:   noPaste,
			Timestamp: timestamp,
		})
	}
	return calls
}

// =============================================================================
// Function Variable Helpers (for cmd package tests)
// =============================================================================

// FuncListPanes returns a function suitable for weztermListPanesFunc.
func (h *WezTermMockHarness) FuncListPanes() func() ([]struct{ ID int; Title string }, error) {
	return func() ([]struct{ ID int; Title string }, error) {
		h.mu.Lock()
		defer h.mu.Unlock()

		h.listCalls++

		if h.listError != nil {
			return nil, h.listError
		}

		result := make([]struct{ ID int; Title string }, len(h.panes))
		for i, p := range h.panes {
			result[i] = struct{ ID int; Title string }{ID: p.PaneID, Title: p.Title}
		}
		return result, nil
	}
}

// FuncGetText returns a function suitable for weztermGetTextFunc.
func (h *WezTermMockHarness) FuncGetText() func(paneID int) (string, error) {
	return func(paneID int) (string, error) {
		return h.GetText(context.Background(), paneID, 0)
	}
}

// FuncSendText returns a function suitable for weztermSendTextFunc.
func (h *WezTermMockHarness) FuncSendText() func(paneID int, text string) error {
	return func(paneID int, text string) error {
		return h.SendText(context.Background(), paneID, text, true)
	}
}

// =============================================================================
// Fixture Helpers
// =============================================================================

// CreateRateLimitedPane creates a pane showing a Claude rate limit message.
func (h *WezTermMockHarness) CreateRateLimitedPane(paneID int, title string) {
	h.AddPaneSimple(paneID, title)
	h.SetPaneText(paneID, `Claude Code session
═══════════════════════════════════════════
You've hit your limit · resets 3:00 PM

Rate limit reached. Please wait or upgrade.
`)
}

// CreateOAuthPane creates a pane showing a Claude OAuth URL.
func (h *WezTermMockHarness) CreateOAuthPane(paneID int, title string, oauthURL string) {
	h.AddPaneSimple(paneID, title)
	h.SetPaneText(paneID, fmt.Sprintf(`Claude Code session
═══════════════════════════════════════════
Browser didn't open? Use the url below:
%s

Paste code here if prompted >
`, oauthURL))
}

// CreateClaudePane creates a pane showing a normal Claude session.
func (h *WezTermMockHarness) CreateClaudePane(paneID int, title string) {
	h.AddPaneSimple(paneID, title)
	h.SetPaneText(paneID, `Claude Code session
═══════════════════════════════════════════
> How can I help you today?

I'm ready to assist with your coding tasks.
`)
}

// CreateBashPane creates a pane showing a bash prompt.
func (h *WezTermMockHarness) CreateBashPane(paneID int, title string) {
	h.AddPaneSimple(paneID, title)
	h.SetPaneText(paneID, `Last login: Mon Jan 20 10:00:00 on ttys000
user@host ~ %`)
}
