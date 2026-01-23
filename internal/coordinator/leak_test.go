package coordinator

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestAuthRequestLeakOnTimeout reproduces the issue where AuthRequests are not
// cleaned up from the coordinator's map when a tracker times out.
func TestAuthRequestLeakOnTimeout(t *testing.T) {
	cfg := DefaultConfig()
	cfg.AuthTimeout = 10 * time.Millisecond
	cfg.StateTimeout = 10 * time.Millisecond
	cfg.PaneClient = &fakePaneClient{
		panes: []Pane{{PaneID: 1}},
	}
	coord := New(cfg)

	// Setup tracker in auth pending state with a request
	reqID := "req-leak-test"
	tracker := NewPaneTracker(1)
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID(reqID)
	// Force state entered to be in the past to trigger timeout
	tracker.mu.Lock()
	tracker.StateEntered = time.Now().Add(-1 * time.Second)
	tracker.mu.Unlock()

	coord.trackers[1] = tracker
	coord.requests[reqID] = &AuthRequest{ID: reqID, Status: "pending"}

	ctx := context.Background()

	// 1. Poll to trigger transition from StateAuthPending -> StateFailed
	coord.pollPanes(ctx)

	if tracker.GetState() != StateFailed {
		t.Errorf("expected transition to StateFailed, got %v", tracker.GetState())
	}

	// The request SHOULD still be there at this point (or maybe marked failed?)
	// But let's see if it's there.
	coord.mu.RLock()
	_, exists := coord.requests[reqID]
	coord.mu.RUnlock()
	if !exists {
		// If it's already gone, maybe I misunderstood the code, or it's already fixed?
		// But in my analysis, it wasn't removed.
		t.Log("Request removed on transition to Failed (unexpected behavior based on analysis)")
	}

	// 2. Force StateFailed timeout -> Reset (Idle)
	tracker.mu.Lock()
	tracker.StateEntered = time.Now().Add(-1 * time.Second)
	tracker.mu.Unlock()

	coord.pollPanes(ctx)

	if tracker.GetState() != StateIdle {
		t.Errorf("expected transition to StateIdle (Reset), got %v", tracker.GetState())
	}

	if tracker.GetRequestID() != "" {
		t.Errorf("expected tracker RequestID to be cleared, got %s", tracker.GetRequestID())
	}

	// 3. Check if request leaked
	coord.mu.RLock()
	_, leaked := coord.requests[reqID]
	coord.mu.RUnlock()

	if leaked {
		os.WriteFile("leak_test_result.txt", []byte("FAIL: Request leaked"), 0644)
		t.Errorf("AuthRequest leaked! It remains in coordinator.requests after tracker reset")
	} else {
		os.WriteFile("leak_test_result.txt", []byte("PASS: Request cleaned up"), 0644)
	}
}
