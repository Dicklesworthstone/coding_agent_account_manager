package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestCooldownBasics tests basic cooldown functionality.
func TestCooldownBasics(t *testing.T) {
	tracker := NewPaneTracker(1)

	// Initially not on cooldown
	if tracker.IsOnCooldown("login") {
		t.Error("expected no cooldown initially")
	}

	// Set a cooldown
	tracker.SetCooldown("login", 100*time.Millisecond)
	if !tracker.IsOnCooldown("login") {
		t.Error("expected cooldown to be active")
	}

	// Different action should not be on cooldown
	if tracker.IsOnCooldown("method_select") {
		t.Error("expected different action to not be on cooldown")
	}

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)
	if tracker.IsOnCooldown("login") {
		t.Error("expected cooldown to have expired")
	}
}

// TestCooldownRemaining tests CooldownRemaining functionality.
func TestCooldownRemaining(t *testing.T) {
	tracker := NewPaneTracker(1)

	// No cooldown returns 0
	if remaining := tracker.CooldownRemaining("login"); remaining != 0 {
		t.Errorf("expected 0 remaining, got %v", remaining)
	}

	// Set cooldown and check remaining
	tracker.SetCooldown("login", 100*time.Millisecond)
	remaining := tracker.CooldownRemaining("login")
	if remaining < 50*time.Millisecond || remaining > 100*time.Millisecond {
		t.Errorf("expected remaining between 50ms and 100ms, got %v", remaining)
	}

	// After expiry, should be 0
	time.Sleep(150 * time.Millisecond)
	if remaining := tracker.CooldownRemaining("login"); remaining != 0 {
		t.Errorf("expected 0 after expiry, got %v", remaining)
	}
}

// TestCooldownClear tests clearing cooldowns.
func TestCooldownClear(t *testing.T) {
	tracker := NewPaneTracker(1)

	tracker.SetCooldown("login", 10*time.Second)
	tracker.SetCooldown("method_select", 10*time.Second)

	if !tracker.IsOnCooldown("login") {
		t.Error("expected login cooldown")
	}

	// Clear specific cooldown
	tracker.ClearCooldown("login")
	if tracker.IsOnCooldown("login") {
		t.Error("expected login cooldown to be cleared")
	}
	if !tracker.IsOnCooldown("method_select") {
		t.Error("expected method_select cooldown to remain")
	}

	// Clear all cooldowns
	tracker.SetCooldown("login", 10*time.Second)
	tracker.ClearAllCooldowns()
	if tracker.IsOnCooldown("login") {
		t.Error("expected all cooldowns to be cleared")
	}
	if tracker.IsOnCooldown("method_select") {
		t.Error("expected all cooldowns to be cleared")
	}
}

// TestCooldownResetOnTrackerReset tests cooldowns are cleared on Reset().
func TestCooldownResetOnTrackerReset(t *testing.T) {
	tracker := NewPaneTracker(1)

	tracker.SetCooldown("login", 10*time.Second)
	tracker.Reset()

	if tracker.IsOnCooldown("login") {
		t.Error("expected cooldown to be cleared on Reset")
	}
}

// TestStateTransitionTiming tests state timing functionality.
func TestStateTransitionTiming(t *testing.T) {
	tracker := NewPaneTracker(1)

	// Initial state entered should be recent
	if tracker.TimeSinceStateChange() > time.Second {
		t.Error("expected state change to be recent")
	}

	// Transition to new state
	time.Sleep(10 * time.Millisecond)
	tracker.SetState(StateRateLimited)

	// New state should have recent timestamp
	if tracker.TimeSinceStateChange() > 5*time.Millisecond {
		t.Error("expected state change timestamp to be updated")
	}
}

// TestCoordinatorConfig tests default configuration values.
func TestCoordinatorConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.Backend != BackendAuto {
		t.Errorf("expected BackendAuto, got %v", cfg.Backend)
	}
	if cfg.PollInterval != 500*time.Millisecond {
		t.Errorf("expected 500ms poll interval, got %v", cfg.PollInterval)
	}
	if cfg.AuthTimeout != 5*time.Minute {
		t.Errorf("expected 5m auth timeout, got %v", cfg.AuthTimeout)
	}
	if cfg.StateTimeout != 30*time.Second {
		t.Errorf("expected 30s state timeout, got %v", cfg.StateTimeout)
	}
	if cfg.OutputLines != 100 {
		t.Errorf("expected 100 output lines, got %d", cfg.OutputLines)
	}
	if cfg.LoginCooldown != 5*time.Second {
		t.Errorf("expected 5s login cooldown, got %v", cfg.LoginCooldown)
	}
	if cfg.MethodSelectCooldown != 2*time.Second {
		t.Errorf("expected 2s method select cooldown, got %v", cfg.MethodSelectCooldown)
	}
}

// TestCoordinatorStartStop tests starting and stopping the coordinator.
func TestCoordinatorStartStop(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = &fakePaneClient{panes: []Pane{}}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start should succeed
	if err := coord.Start(ctx); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Double start should fail
	if err := coord.Start(ctx); err == nil {
		t.Error("expected error on double start")
	}

	// Stop should succeed
	if err := coord.Stop(); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	// Can restart after stop
	if err := coord.Start(ctx); err != nil {
		t.Fatalf("Restart failed: %v", err)
	}
	coord.Stop()
}

// TestCoordinatorGetStatus tests GetStatus functionality.
func TestCoordinatorGetStatus(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	// Add some trackers
	coord.trackers[1] = NewPaneTracker(1)
	coord.trackers[2] = NewPaneTracker(2)
	coord.trackers[2].SetState(StateRateLimited)

	status := coord.GetStatus()

	if len(status) != 2 {
		t.Errorf("expected 2 panes in status, got %d", len(status))
	}
	if status[1] != StateIdle {
		t.Errorf("expected pane 1 to be IDLE, got %v", status[1])
	}
	if status[2] != StateRateLimited {
		t.Errorf("expected pane 2 to be RATE_LIMITED, got %v", status[2])
	}
}

// TestCoordinatorGetTrackers tests GetTrackers functionality.
func TestCoordinatorGetTrackers(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	coord.trackers[1] = NewPaneTracker(1)
	coord.trackers[2] = NewPaneTracker(2)

	trackers := coord.GetTrackers()

	if len(trackers) != 2 {
		t.Errorf("expected 2 trackers, got %d", len(trackers))
	}
}

// TestCoordinatorGetPendingRequests tests GetPendingRequests functionality.
func TestCoordinatorGetPendingRequests(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	// Add pending and non-pending requests
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", Status: "pending"}
	coord.requests["req-2"] = &AuthRequest{ID: "req-2", Status: "processing"}
	coord.requests["req-3"] = &AuthRequest{ID: "req-3", Status: "pending"}

	pending := coord.GetPendingRequests()

	if len(pending) != 2 {
		t.Errorf("expected 2 pending requests, got %d", len(pending))
	}
}

// TestCoordinatorReceiveAuthResponseUnknown tests error handling for unknown request.
func TestCoordinatorReceiveAuthResponseUnknown(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	err := coord.ReceiveAuthResponse(AuthResponse{
		RequestID: "unknown-request",
		Code:      "ABC123",
		Account:   "test@example.com",
	})

	if err == nil {
		t.Error("expected error for unknown request")
	}
	if !strings.Contains(err.Error(), "unknown request") {
		t.Errorf("expected 'unknown request' error, got: %v", err)
	}
}

// TestCoordinatorReceiveAuthResponseNoTracker tests error handling for missing tracker.
func TestCoordinatorReceiveAuthResponseNoTracker(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	// Add request but no tracker
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", Status: "pending"}

	err := coord.ReceiveAuthResponse(AuthResponse{
		RequestID: "req-1",
		Code:      "ABC123",
		Account:   "test@example.com",
	})

	if !errors.Is(err, ErrAuthRequestClosed) {
		t.Fatalf("expected ErrAuthRequestClosed for missing tracker, got: %v", err)
	}
	if len(coord.GetPendingRequests()) != 0 {
		t.Error("request without a pane must stop being offered to agents")
	}
}

// TestCoordinatorReceiveAuthResponseWithError tests error response handling.
func TestCoordinatorReceiveAuthResponseWithError(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.SetRequestID("req-1")
	tracker.SetState(StateAuthPending)
	coord.trackers[1] = tracker
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", Status: "pending", PaneID: 1}

	var failedPaneID int
	coord.OnAuthFailed = func(paneID int, err error) {
		failedPaneID = paneID
	}

	err := coord.ReceiveAuthResponse(AuthResponse{
		RequestID: "req-1",
		Error:     "auth failed",
	})

	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if tracker.GetState() != StateFailed {
		t.Errorf("expected StateFailed, got %v", tracker.GetState())
	}
	if failedPaneID != 1 {
		t.Errorf("expected OnAuthFailed to be called with pane 1, got %d", failedPaneID)
	}
}

// API Tests

// TestAPIHealthEndpoint tests the /health endpoint.
func TestAPIHealthEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = &fakePaneClient{}

	api := NewAPIServer(coord, "", 0, nil)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	api.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if resp.Status != "ok" {
		t.Errorf("expected status 'ok', got %q", resp.Status)
	}
	if resp.Backend != "fake" {
		t.Errorf("expected backend 'fake', got %q", resp.Backend)
	}
	if resp.Timestamp.IsZero() {
		t.Error("expected non-zero timestamp")
	}
}

// TestAPIStatusEndpoint tests the /status endpoint.
func TestAPIStatusEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = &fakePaneClient{}

	// Add some trackers
	tracker := NewPaneTracker(1)
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID("req-1")
	coord.trackers[1] = tracker
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", PaneID: 1, Status: "pending"}

	api := NewAPIServer(coord, "", 0, nil)

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()

	api.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var resp StatusResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if !resp.Running {
		t.Error("expected running to be true")
	}
	if resp.PaneCount != 1 {
		t.Errorf("expected 1 pane, got %d", resp.PaneCount)
	}
	if resp.PendingAuths != 1 {
		t.Errorf("expected 1 pending auth, got %d", resp.PendingAuths)
	}
	if len(resp.Panes) != 1 {
		t.Errorf("expected 1 pane in list, got %d", len(resp.Panes))
	}
	if resp.Panes[0].State != "AUTH_PENDING" {
		t.Errorf("expected AUTH_PENDING state, got %q", resp.Panes[0].State)
	}
}

func TestAPITokenAuth(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AuthToken = "secret-token"
	coord := New(cfg)
	coord.paneClient = &fakePaneClient{}

	api := NewAPIServer(coord, "", 0, nil)
	handler := api.authMiddleware(api.handleStatus)

	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected status 401, got %d", w.Code)
	}

	req = httptest.NewRequest("GET", "/status", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	w = httptest.NewRecorder()
	handler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}
}

// TestAPIGetPendingEndpoint tests the /auth/pending endpoint.
func TestAPIGetPendingEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	coord.requests["req-1"] = &AuthRequest{
		ID:     "req-1",
		PaneID: 1,
		URL:    "https://claude.ai/oauth/authorize?code_challenge=abc",
		Status: "pending",
	}
	coord.requests["req-2"] = &AuthRequest{
		ID:     "req-2",
		PaneID: 2,
		Status: "processing", // Not pending
	}

	api := NewAPIServer(coord, "", 0, nil)

	req := httptest.NewRequest("GET", "/auth/pending", nil)
	w := httptest.NewRecorder()

	api.handleGetPending(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var pending []*AuthRequest
	if err := json.Unmarshal(w.Body.Bytes(), &pending); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(pending) != 1 {
		t.Errorf("expected 1 pending request, got %d", len(pending))
	}
	if pending[0].ID != "req-1" {
		t.Errorf("expected req-1, got %q", pending[0].ID)
	}
}

// TestAPICompleteEndpoint tests the /auth/complete endpoint.
func TestAPICompleteEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = &fakePaneClient{}

	tracker := NewPaneTracker(1)
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID("req-1")
	coord.trackers[1] = tracker
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", PaneID: 1, Status: "pending"}

	api := NewAPIServer(coord, "", 0, nil)

	body := strings.NewReader(`{"request_id":"req-1","code":"ABC123","account":"test@example.com"}`)
	req := httptest.NewRequest("POST", "/auth/complete", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	api.handleComplete(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	// Check tracker received the code
	if tracker.GetReceivedCode() != "ABC123" {
		t.Errorf("expected code ABC123, got %q", tracker.GetReceivedCode())
	}
	if tracker.GetUsedAccount() != "test@example.com" {
		t.Errorf("expected account test@example.com, got %q", tracker.GetUsedAccount())
	}
}

// TestAPICompleteEndpointBadRequest tests error handling for invalid requests.
func TestAPICompleteEndpointBadRequest(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)

	api := NewAPIServer(coord, "", 0, nil)

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{
			name:     "invalid JSON",
			body:     `{invalid}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing request_id",
			body:     `{"code":"ABC123"}`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "unknown request_id",
			body:     `{"request_id":"unknown","code":"ABC123"}`,
			wantCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/auth/complete", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()

			api.handleComplete(w, req)

			if w.Code != tt.wantCode {
				t.Errorf("expected status %d, got %d", tt.wantCode, w.Code)
			}
		})
	}
}

// TestAPIListPanesEndpoint tests the /panes endpoint.
func TestAPIListPanesEndpoint(t *testing.T) {
	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = &fakePaneClient{
		panes: []Pane{
			{PaneID: 1, Title: "pane-1"},
			{PaneID: 2, Title: "pane-2"},
		},
	}

	api := NewAPIServer(coord, "", 0, nil)

	req := httptest.NewRequest("GET", "/panes", nil)
	w := httptest.NewRecorder()

	api.handleListPanes(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	var panes []Pane
	if err := json.Unmarshal(w.Body.Bytes(), &panes); err != nil {
		t.Fatalf("failed to unmarshal response: %v", err)
	}

	if len(panes) != 2 {
		t.Errorf("expected 2 panes, got %d", len(panes))
	}
}

// E2E style tests with mocked pane client

// TestE2ERateLimitToAuthComplete tests the full flow from rate limit to auth complete.
func TestE2ERateLimitToAuthComplete(t *testing.T) {
	client := &fakePaneClient{
		panes: []Pane{{PaneID: 1, Title: "claude-code"}},
	}

	cfg := DefaultConfig()
	cfg.LoginCooldown = 10 * time.Millisecond
	cfg.MethodSelectCooldown = 10 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Phase 1: Rate limit detected
	client.output = "You've hit your limit on Claude usage today. This resets 2pm"
	coord.pollPanes(ctx)

	if len(coord.trackers) != 1 {
		t.Fatalf("expected 1 tracker, got %d", len(coord.trackers))
	}
	tracker := coord.trackers[1]
	if tracker.GetState() != StateRateLimited {
		t.Errorf("expected StateRateLimited, got %v", tracker.GetState())
	}

	// Check /login was typed into an emptied prompt line
	sent := client.sentText()
	if want := []string{KeyEndOfLine, KeyKillLine, "/login\n"}; !slices.Equal(sent, want) {
		t.Errorf("expected %q to be sent, got %q", want, sent)
	}

	// Phase 2: Method selection appears
	client.output = "Select login method:\n1. Claude account with subscription\n2. API key"
	time.Sleep(15 * time.Millisecond) // Wait for cooldown
	coord.pollPanes(ctx)

	if tracker.GetState() != StateAwaitingMethodSelect {
		t.Errorf("expected StateAwaitingMethodSelect, got %v", tracker.GetState())
	}

	// Phase 3: OAuth URL appears
	client.output = "Open https://claude.ai/oauth/authorize?code_challenge=xyz in your browser\nPaste code here if prompted >"
	time.Sleep(15 * time.Millisecond) // Wait for cooldown
	coord.pollPanes(ctx)

	// First poll transitions to AwaitingURL
	if tracker.GetState() != StateAwaitingURL {
		t.Errorf("expected StateAwaitingURL, got %v", tracker.GetState())
	}

	// Second poll processes AwaitingURL and creates auth request
	coord.pollPanes(ctx)

	if tracker.GetState() != StateAuthPending {
		t.Errorf("expected StateAuthPending, got %v", tracker.GetState())
	}

	// Check request was created
	pending := coord.GetPendingRequests()
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending request, got %d", len(pending))
	}

	requestID := pending[0].ID

	// Phase 4: Auth response received
	err := coord.ReceiveAuthResponse(AuthResponse{
		RequestID: requestID,
		Code:      "AUTH-CODE-123",
		Account:   "user@example.com",
	})
	if err != nil {
		t.Fatalf("ReceiveAuthResponse error: %v", err)
	}

	// Process to inject code
	coord.pollPanes(ctx)
	if tracker.GetState() != StateCodeReceived {
		t.Errorf("expected StateCodeReceived, got %v", tracker.GetState())
	}

	coord.pollPanes(ctx)
	if tracker.GetState() != StateAwaitingConfirm {
		t.Errorf("expected StateAwaitingConfirm, got %v", tracker.GetState())
	}

	// Check code was injected
	sent = client.sentText()
	var codeInjected bool
	for _, s := range sent {
		if s == "AUTH-CODE-123\n" {
			codeInjected = true
			break
		}
	}
	if !codeInjected {
		t.Errorf("expected auth code to be injected, sent: %v", sent)
	}

	// Phase 5: Login success
	client.output = "Logged in as user@example.com"
	coord.pollPanes(ctx)

	if tracker.GetState() != StateResuming {
		t.Errorf("expected StateResuming, got %v", tracker.GetState())
	}

	// Process resuming state
	coord.pollPanes(ctx)

	// Tracker should be reset
	if tracker.GetState() != StateIdle {
		t.Errorf("expected StateIdle after resume, got %v", tracker.GetState())
	}
}

// TestE2ECooldownPreventsRapidInjection tests that cooldowns prevent rapid injections.
func TestE2ECooldownPreventsRapidInjection(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "You've hit your limit on Claude usage today. This resets 2pm",
	}

	cfg := DefaultConfig()
	cfg.LoginCooldown = 100 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// First poll should inject /login and transition to RATE_LIMITED
	coord.pollPanes(ctx)
	if n := countLogins(client.sentText()); n != 1 {
		t.Fatalf("expected 1 /login, got %d", n)
	}
	tracker := coord.trackers[1]
	if tracker.GetState() != StateRateLimited {
		t.Fatalf("expected RATE_LIMITED state, got %v", tracker.GetState())
	}

	// Simulate tracker reset (e.g., after timeout or manual intervention)
	// The cooldown should prevent immediate re-injection
	tracker.mu.Lock()
	tracker.State = StateIdle
	tracker.StateEntered = time.Now()
	tracker.LastOutput = "" // Clear to trigger reprocessing
	// Keep the cooldown - don't clear it
	tracker.mu.Unlock()

	// Poll again - rate limit still detected but cooldown should prevent injection
	coord.pollPanes(ctx)
	if n := countLogins(client.sentText()); n != 1 {
		t.Errorf("expected cooldown to prevent second injection, got %d /login", n)
	}

	// After cooldown expires, should inject again
	time.Sleep(150 * time.Millisecond)
	// Reset to idle again
	tracker.mu.Lock()
	tracker.State = StateIdle
	tracker.StateEntered = time.Now()
	tracker.LastOutput = "" // Clear to trigger reprocessing
	tracker.mu.Unlock()

	coord.pollPanes(ctx)
	if n := countLogins(client.sentText()); n != 2 {
		t.Errorf("expected injection after cooldown, got %d /login", n)
	}
}

// countLogins counts the /login commands submitted among sent texts.
func countLogins(sent []string) int {
	n := 0
	for _, s := range sent {
		if s == "/login\n" {
			n++
		}
	}
	return n
}

// TestE2EPaneDisappears tests cleanup when a pane disappears.
func TestE2EPaneDisappears(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}, {PaneID: 2}},
		output: "Normal output",
	}

	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Initial poll creates trackers
	coord.pollPanes(ctx)
	if len(coord.trackers) != 2 {
		t.Fatalf("expected 2 trackers, got %d", len(coord.trackers))
	}

	// Pane 2 disappears
	client.panes = []Pane{{PaneID: 1}}
	coord.pollPanes(ctx)

	if len(coord.trackers) != 1 {
		t.Errorf("expected 1 tracker after pane removal, got %d", len(coord.trackers))
	}
	if _, exists := coord.trackers[1]; !exists {
		t.Error("expected tracker 1 to remain")
	}
	if _, exists := coord.trackers[2]; exists {
		t.Error("expected tracker 2 to be removed")
	}
}

// TestE2EAuthTimeout tests handling of auth timeout.
func TestE2EAuthTimeout(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Paste code here if prompted >",
	}

	cfg := DefaultConfig()
	cfg.AuthTimeout = 50 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	var failedPaneID int
	coord.OnAuthFailed = func(paneID int, err error) {
		failedPaneID = paneID
	}

	// Setup tracker in auth pending state
	tracker := NewPaneTracker(1)
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID("req-1")
	// Set state entered to be in the past to trigger timeout
	tracker.mu.Lock()
	tracker.StateEntered = time.Now().Add(-100 * time.Millisecond)
	tracker.mu.Unlock()

	coord.trackers[1] = tracker
	// An agent fetched the request and then never answered.
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", Status: "pending", ClaimedAt: time.Now().Add(-100 * time.Millisecond)}

	ctx := context.Background()
	coord.pollPanes(ctx)

	if tracker.GetState() != StateFailed {
		t.Errorf("expected StateFailed after timeout, got %v", tracker.GetState())
	}
	if failedPaneID != 1 {
		t.Errorf("expected OnAuthFailed to be called with pane 1, got %d", failedPaneID)
	}
}

// TestUnclaimedRequestWaitsForAgent: while no agent has fetched a request
// (the agent's machine is asleep, say), the pane keeps waiting at its paste
// prompt instead of timing out and spending its retries; once an agent
// fetches it, AuthTimeout applies from then.
func TestUnclaimedRequestWaitsForAgent(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}, output: "Paste code here if prompted >"}
	cfg := DefaultConfig()
	cfg.AuthTimeout = 50 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client
	failures := 0
	coord.OnAuthFailed = func(int, error) { failures++ }

	tracker := NewPaneTracker(1)
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID("req-1")
	tracker.mu.Lock()
	tracker.StateEntered = time.Now().Add(-time.Hour) // asleep for an hour
	tracker.mu.Unlock()
	coord.trackers[1] = tracker
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", Status: RequestPending}

	ctx := context.Background()
	coord.pollPanes(ctx)
	coord.pollPanes(ctx)
	if tracker.GetState() != StateAuthPending || failures != 0 {
		t.Fatalf("unclaimed request: state=%v failures=%d, want still pending", tracker.GetState(), failures)
	}
	if got := coord.GetPendingRequests(); len(got) != 1 || !got[0].ClaimedAt.IsZero() {
		t.Fatalf("pending = %+v; status reads must not claim", got)
	}

	// The agent wakes up and fetches it: the clock starts now.
	claimed := coord.ClaimPendingRequests()
	if len(claimed) != 1 || claimed[0].ClaimedAt.IsZero() {
		t.Fatalf("claimed = %+v", claimed)
	}
	first := claimed[0].ClaimedAt
	if again := coord.ClaimPendingRequests(); !again[0].ClaimedAt.Equal(first) {
		t.Fatal("a second fetch moved the claim time")
	}
	coord.pollPanes(ctx)
	if tracker.GetState() != StateAuthPending {
		t.Fatalf("state = %v right after the claim, want pending", tracker.GetState())
	}

	time.Sleep(60 * time.Millisecond)
	coord.pollPanes(ctx)
	if tracker.GetState() != StateFailed || failures != 1 {
		t.Fatalf("after the claimed request timed out: state=%v failures=%d", tracker.GetState(), failures)
	}
}

// =============================================================================
// Compaction Reminder Injection Tests (caam-6dqi)
// =============================================================================

// TestCompactionReminderInjection tests that reminder is injected when enabled and compaction detected.
func TestCompactionReminderInjection(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1, Title: "claude-code"}},
		output: "Some output\nConversation compacted · ctrl+o for history\nMore output",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Reread AGENTS.md so it's still fresh in your mind."
	cfg.CompactionReminderCooldown = 100 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should detect compaction and inject reminder
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "AGENTS.md") {
		t.Errorf("expected reminder to contain 'AGENTS.md', got %q", sent[0])
	}
}

// TestCompactionReminderDisabled tests no injection when feature is disabled.
func TestCompactionReminderDisabled(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1, Title: "claude-code"}},
		output: "Some output\nConversation compacted · ctrl+o for history\nMore output",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = false // Explicitly disabled (default)
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should NOT inject reminder when disabled
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 0 {
		t.Errorf("expected no sends when disabled, got %d: %v", len(sent), sent)
	}
}

// TestCompactionReminderCooldown tests that cooldown prevents duplicate injections.
func TestCompactionReminderCooldown(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1, Title: "claude-code"}},
		output: "Conversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Reread AGENTS.md"
	cfg.CompactionReminderCooldown = 100 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// First poll should inject
	coord.pollPanes(ctx)
	sent := client.sentText()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send on first poll, got %d", len(sent))
	}

	// Reset output to trigger re-detection (simulate new compaction event)
	tracker := coord.trackers[1]
	tracker.mu.Lock()
	tracker.LastOutput = "" // Force re-evaluation
	tracker.mu.Unlock()

	// Second poll within cooldown should NOT inject again
	coord.pollPanes(ctx)
	sent = client.sentText()
	if len(sent) != 1 {
		t.Errorf("expected cooldown to prevent second injection, got %d sends", len(sent))
	}

	// Wait for cooldown to expire
	time.Sleep(150 * time.Millisecond)

	// Reset output again
	tracker.mu.Lock()
	tracker.LastOutput = ""
	tracker.mu.Unlock()

	// Third poll after cooldown should inject again
	coord.pollPanes(ctx)
	sent = client.sentText()
	if len(sent) != 2 {
		t.Errorf("expected injection after cooldown, got %d sends", len(sent))
	}
}

// TestCompactionReminderAlreadyPresent tests no injection if reminder text already in output.
func TestCompactionReminderAlreadyPresent(t *testing.T) {
	reminderText := "Reread AGENTS.md so it's still fresh in your mind."
	client := &fakePaneClient{
		panes: []Pane{{PaneID: 1, Title: "claude-code"}},
		// Output already contains the reminder text
		output: "Some output\nConversation compacted · ctrl+o for history\n" + reminderText + "\nMore output",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = reminderText
	cfg.CompactionReminderCooldown = 10 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should NOT inject because reminder already present in output
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 0 {
		t.Errorf("expected no sends when reminder already present, got %d: %v", len(sent), sent)
	}
}

// TestCompactionReminderNoCompaction tests no injection when no compaction banner detected.
func TestCompactionReminderNoCompaction(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1, Title: "claude-code"}},
		output: "Normal terminal output without compaction banner",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Reread AGENTS.md"
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should NOT inject because no compaction detected
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 0 {
		t.Errorf("expected no sends without compaction banner, got %d: %v", len(sent), sent)
	}
}

// TestCompactionReminderWithANSI tests injection with ANSI-formatted compaction banner.
func TestCompactionReminderWithANSI(t *testing.T) {
	client := &fakePaneClient{
		panes: []Pane{{PaneID: 1, Title: "claude-code"}},
		// Output with ANSI color codes
		output: "\x1b[36mConversation compacted\x1b[0m · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Reread AGENTS.md"
	cfg.CompactionReminderCooldown = 100 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should detect compaction despite ANSI codes and inject reminder
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send with ANSI-formatted banner, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "AGENTS.md") {
		t.Errorf("expected reminder to contain 'AGENTS.md', got %q", sent[0])
	}
}

// TestCompactionReminderNotInjectedWhenRateLimited tests no compaction injection during rate limit.
func TestCompactionReminderNotInjectedWhenRateLimited(t *testing.T) {
	client := &fakePaneClient{
		panes: []Pane{{PaneID: 1, Title: "claude-code"}},
		// Output with BOTH rate limit AND compaction banner
		output: "You've hit your limit on Claude usage today. This resets 2pm\n" +
			"Conversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Reread AGENTS.md"
	cfg.LoginCooldown = 10 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should detect rate limit and inject /login, NOT the compaction reminder
	coord.pollPanes(ctx)

	sent := client.sentText()
	if want := []string{KeyEndOfLine, KeyKillLine, "/login\n"}; !slices.Equal(sent, want) {
		t.Fatalf("expected only the /login keys %q, got %q", want, sent)
	}
	// Verify no compaction reminder was sent
	for _, s := range sent {
		if strings.Contains(s, "AGENTS.md") {
			t.Errorf("should not inject compaction reminder when rate limited, got %q", s)
		}
	}
}

// TestCompactionReminderCustomPattern tests custom regex pattern for detection.
func TestCompactionReminderCustomPattern(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1, Title: "claude-code"}},
		output: "CUSTOM_COMPACTION_EVENT_12345",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Custom reminder"
	cfg.CompactionReminderCooldown = 100 * time.Millisecond
	cfg.CompactionReminderRegex = regexp.MustCompile(`CUSTOM_COMPACTION_EVENT_\d+`)
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()

	// Poll should detect custom pattern and inject reminder
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send with custom pattern, got %d: %v", len(sent), sent)
	}
	if !strings.Contains(sent[0], "Custom reminder") {
		t.Errorf("expected custom reminder text, got %q", sent[0])
	}
}

// TestCompactionReminderPromptNewline tests that newline is appended to prompt.
func TestCompactionReminderPromptNewline(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1, Title: "claude-code"}},
		output: "Conversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Reminder without newline" // No trailing \n
	cfg.CompactionReminderCooldown = 100 * time.Millisecond
	coord := New(cfg)
	coord.paneClient = client

	ctx := context.Background()
	coord.pollPanes(ctx)

	sent := client.sentText()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(sent))
	}
	// Verify newline was appended
	if !strings.HasSuffix(sent[0], "\n") {
		t.Errorf("expected prompt to end with newline, got %q", sent[0])
	}
}

// =============================================================================
// Replay-safe acceptance (caam-3ezz.8)
// =============================================================================

// newAwaitingCodeCoordinator returns a coordinator whose pane 1 is waiting for
// the code of request "req-1".
func newAwaitingCodeCoordinator(t *testing.T) (*Coordinator, *PaneTracker, *fakePaneClient) {
	t.Helper()
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Paste code here if prompted >",
	}
	cfg := DefaultConfig()
	cfg.PaneClient = client
	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = client.output
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID("req-1")
	coord.trackers[1] = tracker
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", PaneID: 1, Status: RequestPending, CreatedAt: time.Now()}
	return coord, tracker, client
}

func TestReceiveAuthResponseIsReplaySafe(t *testing.T) {
	coord, tracker, _ := newAwaitingCodeCoordinator(t)

	first := AuthResponse{RequestID: "req-1", Code: "CODE-A", Account: "a@example.com"}
	if err := coord.ReceiveAuthResponse(first); err != nil {
		t.Fatalf("first response: %v", err)
	}
	if got := coord.GetPendingRequests(); len(got) != 0 {
		t.Fatalf("accepted request still offered to agents: %+v", got)
	}

	// A redelivery after a lost acknowledgement is acknowledged again.
	if err := coord.ReceiveAuthResponse(first); err != nil {
		t.Fatalf("identical redelivery: %v", err)
	}

	// A different response never overwrites the accepted code.
	err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: "CODE-B", Account: "b@example.com"})
	if !errors.Is(err, ErrAuthResponseConflict) {
		t.Fatalf("conflicting response error = %v, want ErrAuthResponseConflict", err)
	}
	err = coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Error: "late failure"})
	if !errors.Is(err, ErrAuthResponseConflict) {
		t.Fatalf("late error response = %v, want ErrAuthResponseConflict", err)
	}
	if tracker.GetReceivedCode() != "CODE-A" || tracker.GetUsedAccount() != "a@example.com" {
		t.Fatalf("accepted response overwritten: code=%q account=%q", tracker.GetReceivedCode(), tracker.GetUsedAccount())
	}
	if tracker.GetState() != StateAuthPending {
		t.Fatalf("state = %v, want AUTH_PENDING until next poll", tracker.GetState())
	}
}

func TestReceiveAuthResponseRejectsEmptyOrAmbiguous(t *testing.T) {
	coord, tracker, _ := newAwaitingCodeCoordinator(t)

	for _, resp := range []AuthResponse{
		{RequestID: "req-1"},
		{RequestID: "req-1", Code: "   ", Account: "a@example.com"},
		{RequestID: "req-1", Code: "CODE", Error: "boom"},
		{Code: "CODE"},
	} {
		if err := coord.ReceiveAuthResponse(resp); !errors.Is(err, ErrInvalidAuthResponse) {
			t.Errorf("ReceiveAuthResponse(%+v) = %v, want ErrInvalidAuthResponse", resp, err)
		}
	}
	if tracker.GetReceivedCode() != "" || tracker.GetState() != StateAuthPending {
		t.Fatal("invalid responses must not change the pane")
	}
	if len(coord.GetPendingRequests()) != 1 {
		t.Fatal("invalid responses must leave the request pending")
	}
}

func TestReceiveAuthResponseReplayAfterCompletion(t *testing.T) {
	coord, tracker, client := newAwaitingCodeCoordinator(t)
	ctx := context.Background()

	accepted := AuthResponse{RequestID: "req-1", Code: "CODE-A", Account: "a@example.com"}
	if err := coord.ReceiveAuthResponse(accepted); err != nil {
		t.Fatalf("accept: %v", err)
	}
	coord.processPaneState(ctx, client.panes[0]) // -> CODE_RECEIVED
	coord.processPaneState(ctx, client.panes[0]) // inject -> AWAITING_CONFIRM
	client.output = "Logged in as a@example.com"
	coord.processPaneState(ctx, client.panes[0]) // -> RESUMING
	coord.processPaneState(ctx, client.panes[0]) // resume -> IDLE
	if tracker.GetState() != StateIdle {
		t.Fatalf("state = %v, want IDLE after the cycle", tracker.GetState())
	}

	// The agent's acknowledgement was lost; its retry arrives after the cycle.
	if err := coord.ReceiveAuthResponse(accepted); err != nil {
		t.Fatalf("redelivery after completion = %v, want acknowledgement", err)
	}
	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: "CODE-B"}); !errors.Is(err, ErrAuthResponseConflict) {
		t.Fatalf("conflict after completion = %v, want ErrAuthResponseConflict", err)
	}
	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "never-issued", Code: "X"}); !errors.Is(err, ErrUnknownAuthRequest) {
		t.Fatalf("unknown request = %v, want ErrUnknownAuthRequest", err)
	}

	// Injection happened exactly once.
	injected := 0
	for _, s := range client.sentText() {
		if s == "CODE-A\n" {
			injected++
		}
	}
	if injected != 1 {
		t.Fatalf("code injected %d times, want 1", injected)
	}
}

func TestReceiveAuthResponseAfterTimeoutIsClosed(t *testing.T) {
	coord, tracker, client := newAwaitingCodeCoordinator(t)
	coord.config.AuthTimeout = 10 * time.Millisecond
	tracker.mu.Lock()
	tracker.StateEntered = time.Now().Add(-time.Second)
	tracker.mu.Unlock()
	// An agent fetched the request a second ago and never answered.
	coord.mu.Lock()
	coord.requests["req-1"].ClaimedAt = time.Now().Add(-time.Second)
	coord.mu.Unlock()

	coord.processPaneState(context.Background(), client.panes[0])
	if tracker.GetState() != StateFailed {
		t.Fatalf("state = %v, want FAILED after auth timeout", tracker.GetState())
	}

	err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: "LATE"})
	if !errors.Is(err, ErrAuthRequestClosed) {
		t.Fatalf("late code = %v, want ErrAuthRequestClosed", err)
	}
	if tracker.GetReceivedCode() != "" {
		t.Fatal("late code must not be stored")
	}
}

func TestAcceptedResponseWinsOverConcurrentTimeout(t *testing.T) {
	coord, tracker, client := newAwaitingCodeCoordinator(t)
	coord.config.AuthTimeout = 10 * time.Millisecond

	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: "CODE-A"}); err != nil {
		t.Fatalf("accept: %v", err)
	}
	// The poll that observes the code also sees the timeout elapsed.
	tracker.mu.Lock()
	tracker.StateEntered = time.Now().Add(-time.Second)
	tracker.mu.Unlock()

	coord.processPaneState(context.Background(), client.panes[0])
	if tracker.GetState() != StateCodeReceived {
		t.Fatalf("state = %v, want CODE_RECEIVED: an accepted code must not time out", tracker.GetState())
	}
}

func TestErrorResponseClosesRequestAndReplays(t *testing.T) {
	coord, tracker, _ := newAwaitingCodeCoordinator(t)

	failure := AuthResponse{RequestID: "req-1", Error: "browser crashed"}
	if err := coord.ReceiveAuthResponse(failure); err != nil {
		t.Fatalf("error response: %v", err)
	}
	if tracker.GetState() != StateFailed || tracker.GetErrorMessage() != "browser crashed" {
		t.Fatalf("state=%v error=%q", tracker.GetState(), tracker.GetErrorMessage())
	}
	if err := coord.ReceiveAuthResponse(failure); err != nil {
		t.Fatalf("redelivered error response = %v, want acknowledgement", err)
	}
	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: "CODE"}); !errors.Is(err, ErrAuthResponseConflict) {
		t.Fatalf("code after reported failure = %v, want ErrAuthResponseConflict", err)
	}
}

func TestConcurrentResponsesAcceptExactlyOne(t *testing.T) {
	coord, tracker, _ := newAwaitingCodeCoordinator(t)

	const n = 16
	results := make(chan error, n)
	var start sync.WaitGroup
	start.Add(1)
	for i := 0; i < n; i++ {
		go func(i int) {
			start.Wait()
			results <- coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: fmt.Sprintf("CODE-%d", i)})
		}(i)
	}
	start.Done()

	accepted := 0
	for i := 0; i < n; i++ {
		err := <-results
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, ErrAuthResponseConflict):
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d responses, want exactly 1", accepted)
	}
	if !strings.HasPrefix(tracker.GetReceivedCode(), "CODE-") {
		t.Fatalf("stored code = %q", tracker.GetReceivedCode())
	}
}

func TestPaneDisappearanceClosesRequest(t *testing.T) {
	coord, _, client := newAwaitingCodeCoordinator(t)
	client.panes = nil

	coord.pollPanes(context.Background())
	if len(coord.GetPendingRequests()) != 0 {
		t.Fatal("request for a vanished pane must not stay pending")
	}
	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Code: "CODE"}); !errors.Is(err, ErrAuthRequestClosed) {
		t.Fatalf("code for vanished pane = %v, want ErrAuthRequestClosed", err)
	}
}

func TestClosedRequestsPrunedAfterReplayWindow(t *testing.T) {
	coord, _, client := newAwaitingCodeCoordinator(t)
	coord.config.ResponseReplayWindow = time.Millisecond
	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Error: "nope"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	client.output = "idle"
	coord.pollPanes(context.Background())

	if err := coord.ReceiveAuthResponse(AuthResponse{RequestID: "req-1", Error: "nope"}); !errors.Is(err, ErrUnknownAuthRequest) {
		t.Fatalf("after replay window = %v, want ErrUnknownAuthRequest", err)
	}
}

func TestAPICompleteStatusCodes(t *testing.T) {
	coord, _, _ := newAwaitingCodeCoordinator(t)
	api := NewAPIServer(coord, "", 0, nil)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/auth/complete", strings.NewReader(body))
		w := httptest.NewRecorder()
		api.handleComplete(w, req)
		return w
	}

	if w := post(`{"request_id":"req-1"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("empty response status = %d, want 400", w.Code)
	}
	w := post(`{"request_id":"req-1","code":"CODE-A","account":"a@example.com"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("accept status = %d, want 200 (%s)", w.Code, w.Body.String())
	}
	var ack CompleteAck
	if err := json.Unmarshal(w.Body.Bytes(), &ack); err != nil || ack.Status != "accepted" || ack.RequestID != "req-1" {
		t.Fatalf("ack = %+v (%v)", ack, err)
	}
	if w := post(`{"request_id":"req-1","code":"CODE-A","account":"a@example.com"}`); w.Code != http.StatusOK {
		t.Fatalf("duplicate status = %d, want 200", w.Code)
	}
	if w := post(`{"request_id":"req-1","code":"CODE-B"}`); w.Code != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409", w.Code)
	}
	if w := post(`{"request_id":"other","code":"X"}`); w.Code != http.StatusNotFound {
		t.Fatalf("unknown status = %d, want 404", w.Code)
	}

	coord.closeRequest("req-1", RequestExpired)
	coord.mu.Lock()
	coord.closed["req-2"] = &AuthRequest{ID: "req-2", Status: RequestExpired, closedAt: time.Now()}
	coord.mu.Unlock()
	if w := post(`{"request_id":"req-2","code":"X"}`); w.Code != http.StatusGone {
		t.Fatalf("closed status = %d, want 410", w.Code)
	}
}

func TestListenSecurity(t *testing.T) {
	for _, bind := range []string{"", "127.0.0.1", "localhost", "::1", "[::1]", "127.0.0.2"} {
		if !IsLoopbackBind(bind) {
			t.Errorf("IsLoopbackBind(%q) = false", bind)
		}
		if err := ValidateListenSecurity(bind, ""); err != nil {
			t.Errorf("loopback %q without token rejected: %v", bind, err)
		}
	}
	for _, bind := range []string{"0.0.0.0", "::", "100.64.0.5", "example.com"} {
		if IsLoopbackBind(bind) {
			t.Errorf("IsLoopbackBind(%q) = true", bind)
		}
		if err := ValidateListenSecurity(bind, ""); err == nil {
			t.Errorf("%q without token accepted", bind)
		}
		if err := ValidateListenSecurity(bind, "tok"); err != nil {
			t.Errorf("%q with token rejected: %v", bind, err)
		}
	}
	if got := ListenAddress("::1", 7890); got != "[::1]:7890" {
		t.Errorf("ListenAddress(::1) = %q", got)
	}
	if got := ListenAddress("", 7890); got != "127.0.0.1:7890" {
		t.Errorf("ListenAddress(\"\") = %q", got)
	}
}

func TestFileConfigApply(t *testing.T) {
	cfg, err := FileConfig{PollInterval: "1s", ResumeCooldown: "3s", Backend: "WezTerm", AuthToken: "t"}.Apply(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != time.Second || cfg.ResumeCooldown != 3*time.Second || cfg.Backend != BackendWezTerm || cfg.AuthToken != "t" {
		t.Fatalf("applied config = %+v", cfg)
	}
	if cfg.AuthTimeout != DefaultConfig().AuthTimeout {
		t.Fatal("unset fields must keep defaults")
	}
	for _, bad := range []FileConfig{{AuthTimeout: "soon"}, {StateTimeout: "0s"}, {Backend: "screen"}, {OutputLines: -1}} {
		if _, err := bad.Apply(DefaultConfig()); err == nil {
			t.Errorf("Apply(%+v) accepted invalid config", bad)
		}
	}
}

func TestFailedLoginIsRetriedWithinBudget(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}, output: "Paste code here if prompted > abc\nInvalid code"}
	cfg := DefaultConfig()
	cfg.PaneClient = client
	cfg.StateTimeout = time.Millisecond
	cfg.MaxLoginRetries = 2
	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = client.output
	coord.trackers[1] = tracker
	fail := func() {
		tracker.SetState(StateFailed)
		tracker.mu.Lock()
		tracker.StateEntered = time.Now().Add(-time.Second)
		tracker.mu.Unlock()
		coord.processPaneState(context.Background(), client.panes[0])
	}

	for attempt := 1; attempt <= 2; attempt++ {
		fail()
		if tracker.GetState() != StateRateLimited || tracker.GetRetryCount() != attempt {
			t.Fatalf("attempt %d: state=%v retries=%d, want RATE_LIMITED/%d", attempt, tracker.GetState(), tracker.GetRetryCount(), attempt)
		}
	}
	if logins := countLogins(client.sentText()); logins != 2 {
		t.Fatalf("/login injected %d times, want 2", logins)
	}

	// Budget exhausted: the pane is left for a human.
	fail()
	if tracker.GetState() != StateIdle {
		t.Fatalf("state after exhausted retries = %v, want IDLE", tracker.GetState())
	}
	if countLogins(client.sentText()) != 2 {
		t.Fatal("no further /login once retries are exhausted")
	}

	// A fresh rate limit starts a new episode with a full budget.
	client.output = "...\nYou've hit your limit · resets 9pm"
	coord.processPaneState(context.Background(), client.panes[0])
	if tracker.GetState() != StateRateLimited || tracker.GetRetryCount() != 0 {
		t.Fatalf("new episode: state=%v retries=%d", tracker.GetState(), tracker.GetRetryCount())
	}
}

// switchablePane is a multiplexer backend that can be started and stopped.
type switchablePane struct {
	name string
	up   atomic.Bool
	sent atomic.Int32
}

func (p *switchablePane) ListPanes(ctx context.Context) ([]Pane, error) {
	if !p.up.Load() {
		return nil, fmt.Errorf("%s: no server running", p.name)
	}
	return []Pane{{PaneID: 7, Title: p.name}}, nil
}

func (p *switchablePane) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	return p.name + " output", nil
}

func (p *switchablePane) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	p.sent.Add(1)
	return nil
}

func (p *switchablePane) IsAvailable(ctx context.Context) bool { return p.up.Load() }
func (p *switchablePane) Backend() string                      { return p.name }

func TestAutoBackendFollowsTheRunningMultiplexer(t *testing.T) {
	ctx := context.Background()
	wezterm := &switchablePane{name: "wezterm"}
	tmux := &switchablePane{name: "tmux"}

	// Started before any multiplexer (systemd at boot): WezTerm by preference.
	auto := newAutoPaneClient(ctx, slog.Default(), wezterm, tmux)
	if auto.Backend() != "wezterm" || auto.IsAvailable(ctx) {
		t.Fatalf("no multiplexer: backend=%s available=%v", auto.Backend(), auto.IsAvailable(ctx))
	}
	if _, err := auto.ListPanes(ctx); err == nil {
		t.Fatal("ListPanes succeeded with no multiplexer running")
	}

	// The user starts tmux: the next poll finds its panes.
	tmux.up.Store(true)
	panes, err := auto.ListPanes(ctx)
	if err != nil || len(panes) != 1 || panes[0].Title != "tmux" {
		t.Fatalf("after tmux started: panes=%v err=%v", panes, err)
	}
	if auto.Backend() != "tmux" {
		t.Fatalf("backend = %s, want tmux", auto.Backend())
	}
	if err := auto.SendText(ctx, 7, "/login\n", true); err != nil || tmux.sent.Load() != 1 || wezterm.sent.Load() != 0 {
		t.Fatalf("injection went to the wrong backend: tmux=%d wezterm=%d err=%v", tmux.sent.Load(), wezterm.sent.Load(), err)
	}

	// WezTerm appearing while tmux still answers does not flip backends.
	wezterm.up.Store(true)
	if _, err := auto.ListPanes(ctx); err != nil || auto.Backend() != "tmux" {
		t.Fatalf("backend flipped while tmux answers: %s err=%v", auto.Backend(), err)
	}

	// tmux exits: WezTerm takes over.
	tmux.up.Store(false)
	panes, err = auto.ListPanes(ctx)
	if err != nil || panes[0].Title != "wezterm" || auto.Backend() != "wezterm" {
		t.Fatalf("after tmux exited: panes=%v backend=%s err=%v", panes, auto.Backend(), err)
	}
}

func TestAutoBackendPrefersWezTermWhenBothRun(t *testing.T) {
	ctx := context.Background()
	wezterm := &switchablePane{name: "wezterm"}
	tmux := &switchablePane{name: "tmux"}
	wezterm.up.Store(true)
	tmux.up.Store(true)
	if got := newAutoPaneClient(ctx, slog.Default(), wezterm, tmux).Backend(); got != "wezterm" {
		t.Fatalf("backend = %s, want wezterm", got)
	}
}

func TestResumeDismissesPostLoginScreenBeforePrompting(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1, Title: "claude"}}}
	cfg := DefaultConfig()
	cfg.ResumePrompt = "proceed\n"
	coord := New(cfg)
	coord.paneClient = client
	tracker := NewPaneTracker(1)
	tracker.SetState(StateResuming)
	coord.trackers[1] = tracker
	ctx := context.Background()

	// Text typed on this screen would be swallowed; it needs an Enter first.
	client.output = "\x1b[32mLogin successful.\x1b[0m Press Enter to continue…"
	coord.pollPanes(ctx)
	coord.pollPanes(ctx) // settling: nothing more is typed
	if got := client.sentText(); len(got) != 1 || got[0] != "\n" {
		t.Fatalf("sent %q, want a single Enter to dismiss the screen", got)
	}
	if tracker.GetState() != StateResuming {
		t.Fatalf("state = %v, want still resuming", tracker.GetState())
	}

	// Back at the prompt (the dismissed screen may linger in scrollback).
	tracker.SetCooldown("continue", 0)
	client.output = "Login successful. Press Enter to continue…\n\n> "
	coord.pollPanes(ctx)
	if got := client.sentText(); len(got) != 2 || got[1] != "proceed\n" {
		t.Fatalf("sent %q, want Enter then the resume prompt", got)
	}
	if tracker.GetState() != StateIdle {
		t.Fatalf("state = %v, want idle after resuming", tracker.GetState())
	}
}

// TestMethodSelectionIsTheDigitAlone: Claude Code's menus act on a digit at
// once, so an Enter sent with it would reach the OAuth code prompt that
// replaces the menu. Without option 1 highlighted, a lost "1" is resent as
// "1", never as an Enter that would pick another option.
func TestMethodSelectionIsTheDigitAlone(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}}
	cfg := DefaultConfig()
	cfg.MethodSelectCooldown = time.Millisecond
	coord := New(cfg)
	coord.paneClient = client
	tracker := NewPaneTracker(1)
	tracker.SetState(StateRateLimited)
	coord.trackers[1] = tracker
	ctx := context.Background()

	client.output = "Select login method:\n  1. Claude account with subscription\n❯ 2. Anthropic Console account"
	for range 4 {
		coord.pollPanes(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	if got, want := client.sentText(), []string{"1", "1", "1"}; !slices.Equal(got, want) {
		t.Fatalf("selections sent = %q, want %q", got, want)
	}
}

func TestLostMethodSelectionIsResentWithinBound(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}}
	cfg := DefaultConfig()
	cfg.MethodSelectCooldown = time.Millisecond
	coord := New(cfg)
	coord.paneClient = client
	tracker := NewPaneTracker(1)
	tracker.SetState(StateRateLimited)
	coord.trackers[1] = tracker
	ctx := context.Background()

	// The menu never advances: the "1" is lost, and so are the Enters that
	// confirm the highlighted option 1 on later attempts.
	client.output = "Select login method:\n❯ 1. Claude account with subscription\n  2. Anthropic Console account"
	for range 6 {
		coord.pollPanes(ctx)
		time.Sleep(5 * time.Millisecond)
	}
	if got, want := client.sentText(), []string{"1", "\n", "\n"}; !slices.Equal(got, want) {
		t.Fatalf("selections sent = %q, want %q (resent while the menu stays, then bounded)", got, want)
	}

	// Once the URL shows, the flow moves on.
	client.output = "Browse to https://claude.ai/oauth/authorize?code=true&state=x\nPaste code here if prompted >"
	coord.pollPanes(ctx)
	if tracker.GetState() != StateAwaitingURL {
		t.Fatalf("state = %v, want AWAITING_URL", tracker.GetState())
	}
}

// TestRestartedCoordinatorAdoptsLoginInProgress: a coordinator restarted
// mid-flow (an upgrade) finds the pane at its paste prompt with fresh, idle
// trackers and must carry the login through instead of ignoring it.
func TestRestartedCoordinatorAdoptsLoginInProgress(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "You've hit your limit · resets 2pm\n> /login\nBrowse to https://claude.ai/oauth/authorize?code=true&state=s1\nPaste code here if prompted >",
	}
	coord := New(DefaultConfig())
	coord.paneClient = client
	ctx := context.Background()

	coord.pollPanes(ctx) // fresh tracker: adopts
	coord.pollPanes(ctx) // publishes the request
	tracker := coord.trackers[1]
	if tracker.GetState() != StateAuthPending {
		t.Fatalf("state = %v, want AUTH_PENDING after adoption", tracker.GetState())
	}
	pending := coord.GetPendingRequests()
	if len(pending) != 1 || pending[0].URL != "https://claude.ai/oauth/authorize?code=true&state=s1" {
		t.Fatalf("pending = %+v", pending)
	}
	if sent := client.sentText(); len(sent) != 0 {
		t.Fatalf("adoption typed %q into the pane", sent)
	}
}

func TestIdleDoesNotAdoptPromptOnlyInScrollback(t *testing.T) {
	output := "Browse to https://claude.ai/oauth/authorize?code=true&state=old\nPaste code here if prompted >\n"
	for i := 0; i < 20; i++ {
		output += fmt.Sprintf("working on step %d\n", i)
	}
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}, output: output}
	coord := New(DefaultConfig())
	coord.paneClient = client
	coord.pollPanes(context.Background())
	if st := coord.trackers[1].GetState(); st != StateIdle {
		t.Fatalf("state = %v: adopted a prompt that is no longer on screen", st)
	}
}

func TestIdleDoesNotAdoptPaneLeftForManualRecovery(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Browse to https://claude.ai/oauth/authorize?code=true&state=s1\nPaste code here if prompted >",
	}
	coord := New(DefaultConfig())
	coord.paneClient = client
	tracker := NewPaneTracker(1)
	tracker.SetGaveUp(true)
	coord.trackers[1] = tracker
	coord.pollPanes(context.Background())
	if st := tracker.GetState(); st != StateIdle {
		t.Fatalf("state = %v: re-adopted a pane whose retries were spent", st)
	}

	// The next rate-limit episode starts fresh.
	client.output = "You've hit your limit · resets 5pm"
	coord.pollPanes(context.Background())
	if tracker.HasGivenUp() || tracker.GetState() != StateRateLimited {
		t.Fatalf("new episode: gaveUp=%v state=%v", tracker.HasGivenUp(), tracker.GetState())
	}
}

// TestTmuxCaptureJoinsWrappedOAuthURL runs a real tmux server (on a private
// socket) whose 60-column pane prints an OAuth URL several times wider.
func TestTmuxCaptureJoinsWrappedOAuthURL(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	url := "https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&response_type=code&redirect_uri=https%3A%2F%2Fconsole.anthropic.com%2Foauth%2Fcode%2Fcallback&scope=org%3Acreate_api_key+user%3Aprofile+user%3Ainference&code_challenge=abcdefghijklmnopqrstuvwxyz0123456789ABCDEFG&code_challenge_method=S256&state=zyxwvutsrqponmlkjihgfedcba9876543210"
	script := fmt.Sprintf("printf 'Browse to:\\n%%s\\nPaste code here if prompted > ' '%s'; sleep 30", url)
	if out, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-x", "60", "-y", "30", script).CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-server").Run() })

	client := NewTmuxClient()
	ctx := context.Background()
	var got string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		panes, err := client.ListPanes(ctx)
		if err == nil && len(panes) == 1 {
			text, err := client.GetText(ctx, panes[0].PaneID, -50)
			if err == nil && strings.Contains(text, "Paste code here") {
				got = ExtractOAuthURL(text)
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if got != url {
		t.Fatalf("extracted %q\nwant      %q", got, url)
	}
}

func TestTypedText(t *testing.T) {
	for in, want := range map[string]string{
		"/login\n":       "/login\r",
		"code#state\r\n": "code#state\r",
		"line1\nline2\n": "line1\nline2\r", // one message, then submit
		"no newline":     "no newline",
		"":               "",
	} {
		if got := TypedText(in); got != want {
			t.Errorf("TypedText(%q) = %q, want %q", in, got, want)
		}
	}
}

// usageLimitMenu is the screen Claude Code shows at a usage limit when the
// account can buy extra usage: the paid option comes first and is highlighted.
const usageLimitMenu = `⏺ Refactoring the parser…
  ⎿  You've hit your limit · resets 3pm (America/New_York)

▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   What do you want to do?

   ❯ 1. Switch to usage credits
     2. Stop and wait for limit to reset
     3. Wait here, then continue automatically at 3pm
     4. Upgrade your plan

   Esc to cancel
`

func TestLoginKeys(t *testing.T) {
	cases := []struct {
		name   string
		output string
		want   []string
	}{
		{"menu open", usageLimitMenu,
			[]string{KeyEscape, KeyEndOfLine, KeyKillLine, "/login\n"}},
		{"menu closed", "  ⎿  You've hit your limit · resets 3pm (America/New_York)\n\n────\n❯ continue\n────\n",
			[]string{KeyEndOfLine, KeyKillLine, "/login\n"}},
		{"usage-based billing labels its option Stop", "   What do you want to do?\n\n   ❯ 1. Add funds to continue with usage\n     2. Stop\n",
			[]string{KeyEscape, KeyEndOfLine, KeyKillLine, "/login\n"}},
		// A menu long gone from the screen is not answered with Esc, which
		// would interrupt a working session.
		{"menu only in scrollback", usageLimitMenu + strings.Repeat("⏺ more work\n", bottomLines+1),
			[]string{KeyEndOfLine, KeyKillLine, "/login\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LoginKeys(tc.output); !slices.Equal(got, tc.want) {
				t.Errorf("LoginKeys = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestUsageLimitMenuIsClosedNotAnswered: at a usage limit Claude Code's menu
// may highlight paid extra usage. Typing "/login" and Enter there would buy
// it; the coordinator must close the menu with Esc before anything else.
func TestUsageLimitMenuIsClosedNotAnswered(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}, output: usageLimitMenu}
	cfg := DefaultConfig()
	cfg.PaneClient = client
	coord := New(cfg)

	coord.pollPanes(context.Background())

	want := []string{KeyEscape, KeyEndOfLine, KeyKillLine, "/login\n"}
	if sent := client.sentText(); !slices.Equal(sent, want) {
		t.Fatalf("sent %q, want %q", sent, want)
	}
	if got := coord.trackers[1].GetState(); got != StateRateLimited {
		t.Fatalf("state = %v, want RATE_LIMITED", got)
	}
}

func TestDetectStateReadsTheBannersReset(t *testing.T) {
	for _, tc := range []struct{ text, reset string }{
		{"  ⎿  You've hit your session limit · resets 3pm (America/New_York)", "3pm (America/New_York)"},
		{"You've hit your weekly limit · resets Oct 9, 3pm (Europe/Paris) · progress saved", "Oct 9, 3pm (Europe/Paris)"},
		{"Claude usage limit reached. Your limit will reset at 5pm (Europe/Paris).", "5pm (Europe/Paris)"},
		{"You've hit your monthly spend limit · raise it at claude.ai/settings · your session limit resets 3pm", "3pm"},
		// The latest banner counts, not one further up.
		{"You've hit your limit · resets 1pm\n> continue\nYou've hit your session limit · resets 6pm", "6pm"},
	} {
		state, meta := DetectState(tc.text)
		if state != StateRateLimited || meta["reset_text"] != tc.reset {
			t.Errorf("DetectState(%q) = %v, reset %q; want RATE_LIMITED, %q", tc.text, state, meta["reset_text"], tc.reset)
		}
	}
}

// TestAuthRequestNamesTheLimitedAccount: the request tells the agent which
// account hit its limit and when that resets, also after a retried login.
func TestAuthRequestNamesTheLimitedAccount(t *testing.T) {
	client := &fakePaneClient{panes: []Pane{{PaneID: 1}}, output: "  ⎿  You've hit your session limit · resets 3pm (America/New_York)"}
	cfg := DefaultConfig()
	cfg.PaneClient = client
	cfg.LoginCooldown = time.Millisecond
	coord := New(cfg)
	coord.signedInAccount = func() string { return "limited@example.com" }
	ctx := context.Background()

	coord.pollPanes(ctx)
	tracker := coord.trackers[1]
	// A retry resets the tracker; the episode's limit must survive it.
	tracker.Reset()
	tracker.SetState(StateRateLimited)

	client.output = "Browse to https://claude.ai/oauth/authorize?code=true&state=s\nPaste code here if prompted >"
	coord.pollPanes(ctx)
	coord.pollPanes(ctx)

	pending := coord.ClaimPendingRequests()
	if len(pending) != 1 {
		t.Fatalf("pending = %d requests, want 1", len(pending))
	}
	if got := pending[0]; got.LimitedAccount != "limited@example.com" || got.LimitReset != "3pm (America/New_York)" {
		t.Fatalf("request limit = %q / %q", got.LimitedAccount, got.LimitReset)
	}
	data, err := json.Marshal(pending[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"limited_account":"limited@example.com"`) || !strings.Contains(string(data), `"limit_reset":"3pm (America/New_York)"`) {
		t.Fatalf("pending JSON lacks the limit: %s", data)
	}
}

// TestTmuxDeliversLoginKeys: the control keys reach a raw-mode program as the
// bytes the keyboard sends, each in order.
func TestTmuxDeliversLoginKeys(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	want := "\x1b\x05\x15/login\r"
	out := filepath.Join(t.TempDir(), "bytes")
	script := fmt.Sprintf("stty raw -echo; head -c %d > '%s'; sleep 30", len(want), out)
	if b, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-x", "80", "-y", "24", script).CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, b)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-server").Run() })

	client := NewTmuxClient()
	ctx := context.Background()
	var panes []Pane
	deadline := time.Now().Add(10 * time.Second)
	for len(panes) == 0 && time.Now().Before(deadline) {
		panes, _ = client.ListPanes(ctx)
		time.Sleep(20 * time.Millisecond)
	}
	if len(panes) != 1 {
		t.Fatal("tmux pane did not start")
	}
	time.Sleep(200 * time.Millisecond) // let stty take effect
	if err := sendKeys(ctx, client, panes[0].PaneID, LoginKeys(usageLimitMenu)); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(out); len(got) == len(want) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != want {
		t.Fatalf("raw-mode program received %q, want %q", got, want)
	}
}

// TestTmuxTypingPressesEnter: a raw-mode program (like Claude Code's input)
// must receive a carriage return, the byte the Enter key sends, not a line
// feed (Ctrl+J, which Claude Code takes as "insert a newline").
func TestTmuxTypingPressesEnter(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("TMUX_TMPDIR", t.TempDir())
	t.Setenv("TMUX", "")
	out := filepath.Join(t.TempDir(), "bytes")
	script := fmt.Sprintf("stty raw -echo; head -c 7 > '%s'; sleep 30", out)
	if b, err := exec.Command("tmux", "-f", "/dev/null", "new-session", "-d", "-x", "80", "-y", "24", script).CombinedOutput(); err != nil {
		t.Fatalf("start tmux: %v: %s", err, b)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-server").Run() })

	client := NewTmuxClient()
	ctx := context.Background()
	var panes []Pane
	deadline := time.Now().Add(5 * time.Second)
	for len(panes) == 0 && time.Now().Before(deadline) {
		panes, _ = client.ListPanes(ctx)
		time.Sleep(20 * time.Millisecond)
	}
	if len(panes) != 1 {
		t.Fatal("tmux pane did not start")
	}
	time.Sleep(200 * time.Millisecond) // let stty take effect
	if err := client.SendText(ctx, panes[0].PaneID, "/login\n", true); err != nil {
		t.Fatal(err)
	}
	var got []byte
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(out); len(got) == 7 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "/login\r" {
		t.Fatalf("raw-mode program received %q, want %q", got, "/login\r")
	}
}
