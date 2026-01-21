package coordinator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	if cfg.AuthTimeout != 60*time.Second {
		t.Errorf("expected 60s auth timeout, got %v", cfg.AuthTimeout)
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

	if err == nil {
		t.Error("expected error for missing tracker")
	}
	if !strings.Contains(err.Error(), "no tracker") {
		t.Errorf("expected 'no tracker' error, got: %v", err)
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

	api := NewAPIServer(coord, 0, nil)

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

	api := NewAPIServer(coord, 0, nil)

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

	api := NewAPIServer(coord, 0, nil)

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

	api := NewAPIServer(coord, 0, nil)

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

	api := NewAPIServer(coord, 0, nil)

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

	api := NewAPIServer(coord, 0, nil)

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

	// Check /login was sent
	sent := client.sentText()
	if len(sent) < 1 || sent[0] != "/login\n" {
		t.Errorf("expected /login to be sent, got %v", sent)
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
	sent := client.sentText()
	if len(sent) != 1 {
		t.Fatalf("expected 1 send, got %d", len(sent))
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
	sent = client.sentText()
	if len(sent) != 1 {
		t.Errorf("expected cooldown to prevent second injection, got %d sends", len(sent))
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
	sent = client.sentText()
	if len(sent) != 2 {
		t.Errorf("expected injection after cooldown, got %d sends", len(sent))
	}
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
	coord.requests["req-1"] = &AuthRequest{ID: "req-1", Status: "pending"}

	ctx := context.Background()
	coord.pollPanes(ctx)

	if tracker.GetState() != StateFailed {
		t.Errorf("expected StateFailed after timeout, got %v", tracker.GetState())
	}
	if failedPaneID != 1 {
		t.Errorf("expected OnAuthFailed to be called with pane 1, got %d", failedPaneID)
	}
}
