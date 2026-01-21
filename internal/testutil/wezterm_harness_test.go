package testutil

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWezTermMockHarness_ListPanes(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "pane one")
	h.AddPaneSimple(2, "pane two")

	panes, err := h.ListPanes(context.Background())
	if err != nil {
		t.Fatalf("ListPanes error: %v", err)
	}
	if len(panes) != 2 {
		t.Fatalf("expected 2 panes, got %d", len(panes))
	}
	if panes[0].PaneID != 1 || panes[0].Title != "pane one" {
		t.Errorf("pane[0] mismatch: %+v", panes[0])
	}
	if panes[1].PaneID != 2 || panes[1].Title != "pane two" {
		t.Errorf("pane[1] mismatch: %+v", panes[1])
	}

	if h.GetListCallCount() != 1 {
		t.Errorf("expected 1 list call, got %d", h.GetListCallCount())
	}
}

func TestWezTermMockHarness_ListPanesError(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.SetListError(errors.New("mock list error"))

	_, err := h.ListPanes(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "mock list error") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWezTermMockHarness_GetText(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test pane")
	h.SetPaneText(1, "Hello, World!")

	text, err := h.GetText(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("GetText error: %v", err)
	}
	if text != "Hello, World!" {
		t.Errorf("expected 'Hello, World!', got %q", text)
	}

	calls := h.GetGetTextCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 get-text call, got %d", len(calls))
	}
	if calls[0].PaneID != 1 {
		t.Errorf("expected pane_id 1, got %d", calls[0].PaneID)
	}
}

func TestWezTermMockHarness_GetTextError(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test pane")
	h.SetGetTextError(1, errors.New("mock get-text error"))

	_, err := h.GetText(context.Background(), 1, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "mock get-text error") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWezTermMockHarness_GetTextNotFound(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	_, err := h.GetText(context.Background(), 999, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "pane 999 not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWezTermMockHarness_Sequence(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test pane")
	h.SetPaneSequence(1, []string{"first", "second", "third"})

	ctx := context.Background()

	text1, _ := h.GetText(ctx, 1, 0)
	if text1 != "first" {
		t.Errorf("expected 'first', got %q", text1)
	}

	text2, _ := h.GetText(ctx, 1, 0)
	if text2 != "second" {
		t.Errorf("expected 'second', got %q", text2)
	}

	text3, _ := h.GetText(ctx, 1, 0)
	if text3 != "third" {
		t.Errorf("expected 'third', got %q", text3)
	}

	// After exhaustion, should return last item
	text4, _ := h.GetText(ctx, 1, 0)
	if text4 != "third" {
		t.Errorf("expected 'third' (last item), got %q", text4)
	}
}

func TestWezTermMockHarness_SendText(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test pane")

	err := h.SendText(context.Background(), 1, "/login\n", true)
	if err != nil {
		t.Fatalf("SendText error: %v", err)
	}

	calls := h.GetSendTextCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 send-text call, got %d", len(calls))
	}
	if calls[0].PaneID != 1 {
		t.Errorf("expected pane_id 1, got %d", calls[0].PaneID)
	}
	if calls[0].Text != "/login\n" {
		t.Errorf("expected '/login\\n', got %q", calls[0].Text)
	}
	if !calls[0].NoPaste {
		t.Errorf("expected NoPaste=true")
	}
}

func TestWezTermMockHarness_SendTextError(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test pane")
	h.SetSendError(1, errors.New("mock send error"))

	err := h.SendText(context.Background(), 1, "test", false)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "mock send error") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestWezTermMockHarness_Assertions(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "pane one")
	h.AddPaneSimple(2, "pane two")

	ctx := context.Background()
	h.SendText(ctx, 1, "/login\n", true)
	h.SendText(ctx, 1, "1\n", true)

	// AssertSendTextCount
	if !h.AssertSendTextCount(2) {
		t.Error("AssertSendTextCount failed")
	}

	// AssertSendTextToPane
	if !h.AssertSendTextToPane(1, "/login\n") {
		t.Error("AssertSendTextToPane failed")
	}

	// AssertSendTextContains
	if !h.AssertSendTextContains("login") {
		t.Error("AssertSendTextContains failed")
	}

	// AssertNoSendTextToPane
	if !h.AssertNoSendTextToPane(2) {
		t.Error("AssertNoSendTextToPane failed")
	}
}

func TestWezTermMockHarness_ClearCalls(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test")
	h.SetPaneText(1, "text")

	ctx := context.Background()
	h.ListPanes(ctx)
	h.GetText(ctx, 1, 0)
	h.SendText(ctx, 1, "test", false)

	h.ClearCalls()

	if h.GetListCallCount() != 0 {
		t.Errorf("expected 0 list calls, got %d", h.GetListCallCount())
	}
	if len(h.GetGetTextCalls()) != 0 {
		t.Errorf("expected 0 get-text calls, got %d", len(h.GetGetTextCalls()))
	}
	if len(h.GetSendTextCalls()) != 0 {
		t.Errorf("expected 0 send-text calls, got %d", len(h.GetSendTextCalls()))
	}
}

func TestWezTermMockHarness_FixtureHelpers(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.CreateRateLimitedPane(1, "claude-limited")
	h.CreateOAuthPane(2, "claude-oauth", "https://claude.ai/oauth/authorize?code=abc")
	h.CreateClaudePane(3, "claude-session")
	h.CreateBashPane(4, "bash")

	ctx := context.Background()

	// Rate limited pane should contain rate limit text
	text1, _ := h.GetText(ctx, 1, 0)
	if !strings.Contains(text1, "hit your limit") {
		t.Errorf("rate limited pane missing expected text: %q", text1)
	}

	// OAuth pane should contain URL
	text2, _ := h.GetText(ctx, 2, 0)
	if !strings.Contains(text2, "claude.ai/oauth") {
		t.Errorf("oauth pane missing expected URL: %q", text2)
	}

	// Claude pane should contain Claude marker
	text3, _ := h.GetText(ctx, 3, 0)
	if !strings.Contains(text3, "Claude Code") {
		t.Errorf("claude pane missing expected marker: %q", text3)
	}

	// Bash pane should contain prompt
	text4, _ := h.GetText(ctx, 4, 0)
	if !strings.Contains(text4, "user@host") {
		t.Errorf("bash pane missing expected prompt: %q", text4)
	}
}

func TestWezTermMockHarness_MockCLI(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	h.AddPaneSimple(1, "test pane")
	h.SetPaneText(1, "Hello from mock!")

	binDir := h.SetupMockCLI()

	// Test that mock binary exists and is executable
	mockPath := filepath.Join(binDir, "wezterm")
	info, err := os.Stat(mockPath)
	if err != nil {
		t.Fatalf("Mock binary not found: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Error("Mock binary is not executable")
	}

	// Test wezterm cli list --format json
	cmd := exec.Command(mockPath, "cli", "list", "--format", "json")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("Mock list failed: %v", err)
	}
	if !strings.Contains(string(out), "pane_id") {
		t.Errorf("Mock list output missing pane_id: %s", out)
	}
	if !strings.Contains(string(out), "test pane") {
		t.Errorf("Mock list output missing title: %s", out)
	}

	// Test wezterm cli get-text --pane-id 1
	cmd = exec.Command(mockPath, "cli", "get-text", "--pane-id", "1")
	out, err = cmd.Output()
	if err != nil {
		t.Fatalf("Mock get-text failed: %v", err)
	}
	if string(out) != "Hello from mock!" {
		t.Errorf("Mock get-text output mismatch: %q", out)
	}

	// Test wezterm cli send-text --pane-id 1 "test message"
	cmd = exec.Command(mockPath, "cli", "send-text", "--pane-id", "1", "test message")
	if err := cmd.Run(); err != nil {
		t.Fatalf("Mock send-text failed: %v", err)
	}

	// Verify send-text was logged
	calls := h.ReadSendTextLog()
	if len(calls) != 1 {
		t.Fatalf("Expected 1 send call logged, got %d", len(calls))
	}
	if calls[0].PaneID != 1 {
		t.Errorf("Expected pane_id 1, got %d", calls[0].PaneID)
	}
	if calls[0].Text != "test message" {
		t.Errorf("Expected text 'test message', got %q", calls[0].Text)
	}
}

func TestWezTermMockHarness_MockCLI_GetTextNotFound(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	binDir := h.SetupMockCLI()
	mockPath := filepath.Join(binDir, "wezterm")

	// Test get-text for non-existent pane
	cmd := exec.Command(mockPath, "cli", "get-text", "--pane-id", "999")
	_, err := cmd.Output()
	if err == nil {
		t.Fatal("Expected error for non-existent pane")
	}
}

func TestWezTermMockHarness_IsAvailable(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	if !h.IsAvailable(context.Background()) {
		t.Error("Mock should always be available")
	}
}

func TestWezTermMockHarness_Backend(t *testing.T) {
	h := NewWezTermMockHarness(t)
	defer h.Close()

	if h.Backend() != "wezterm-mock" {
		t.Errorf("Expected backend 'wezterm-mock', got %q", h.Backend())
	}
}
