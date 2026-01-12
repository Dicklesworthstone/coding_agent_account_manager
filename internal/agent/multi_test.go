package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestCoordinatorEndpointHealth(t *testing.T) {
	coord := &CoordinatorEndpoint{
		Name: "test",
		URL:  "http://localhost:7890",
	}

	// Initially unhealthy (never checked)
	healthy, errMsg, lastCheck := coord.GetHealth()
	if healthy {
		t.Error("expected initially unhealthy")
	}
	if lastCheck != (time.Time{}) {
		t.Error("expected zero time for lastCheck")
	}

	// Set healthy
	coord.SetHealth(true, "")
	healthy, errMsg, lastCheck = coord.GetHealth()
	if !healthy {
		t.Error("expected healthy after SetHealth(true)")
	}
	if errMsg != "" {
		t.Errorf("expected empty error, got %q", errMsg)
	}
	if lastCheck.IsZero() {
		t.Error("expected lastCheck to be set")
	}

	// Set unhealthy with error
	coord.SetHealth(false, "connection refused")
	healthy, errMsg, _ = coord.GetHealth()
	if healthy {
		t.Error("expected unhealthy")
	}
	if errMsg != "connection refused" {
		t.Errorf("expected 'connection refused', got %q", errMsg)
	}
}

func TestMultiAgentSelectAccount(t *testing.T) {
	config := DefaultMultiConfig()
	config.Accounts = []string{"a@test.com", "b@test.com", "c@test.com"}
	config.AccountStrategy = StrategyLRU

	agent := NewMulti(config)

	// Clear any loaded usage data to start fresh
	agent.mu.Lock()
	agent.accountUsage = make(map[string]*AccountUsage)
	agent.mu.Unlock()

	// First selection should return first account (none used)
	account := agent.selectAccount()
	if account != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", account)
	}

	// Record usage for first account
	agent.recordUsage("a@test.com", "success")
	time.Sleep(10 * time.Millisecond)

	// Should now return second account
	account = agent.selectAccount()
	if account != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", account)
	}

	// Record usage for second
	agent.recordUsage("b@test.com", "success")
	time.Sleep(10 * time.Millisecond)

	// Should return third
	account = agent.selectAccount()
	if account != "c@test.com" {
		t.Errorf("expected c@test.com, got %s", account)
	}
}

func TestMultiAgentRoundRobin(t *testing.T) {
	config := DefaultMultiConfig()
	config.Accounts = []string{"a@test.com", "b@test.com", "c@test.com"}
	config.AccountStrategy = StrategyRoundRobin

	agent := NewMulti(config)

	// Clear any loaded usage data to start fresh
	agent.mu.Lock()
	agent.accountUsage = make(map[string]*AccountUsage)
	agent.mu.Unlock()

	// First selection should return first (nothing used yet)
	account := agent.selectAccount()
	if account != "a@test.com" {
		t.Errorf("expected a@test.com, got %s", account)
	}

	// Manually set usage times to control order
	now := time.Now()
	agent.mu.Lock()
	agent.accountUsage["a@test.com"] = &AccountUsage{Email: "a@test.com", LastUsed: now}
	agent.mu.Unlock()

	// Next should be second (a was most recent)
	account = agent.selectAccount()
	if account != "b@test.com" {
		t.Errorf("expected b@test.com, got %s", account)
	}

	// Update b as most recent
	agent.mu.Lock()
	agent.accountUsage["b@test.com"] = &AccountUsage{Email: "b@test.com", LastUsed: now.Add(time.Second)}
	agent.mu.Unlock()

	// Next should be third
	account = agent.selectAccount()
	if account != "c@test.com" {
		t.Errorf("expected c@test.com, got %s", account)
	}

	// Update c as most recent
	agent.mu.Lock()
	agent.accountUsage["c@test.com"] = &AccountUsage{Email: "c@test.com", LastUsed: now.Add(2 * time.Second)}
	agent.mu.Unlock()

	// Should wrap to first
	account = agent.selectAccount()
	if account != "a@test.com" {
		t.Errorf("expected wrap to a@test.com, got %s", account)
	}
}

func TestMultiAgentPollCoordinators(t *testing.T) {
	var requestCount atomic.Int32

	// Create test coordinators
	ts1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer ts1.Close()

	ts2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount.Add(1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer ts2.Close()

	config := DefaultMultiConfig()
	config.PollInterval = 50 * time.Millisecond
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "coord1", URL: ts1.URL},
		{Name: "coord2", URL: ts2.URL},
	}

	agent := NewMulti(config)

	// Manually poll once
	ctx := context.Background()
	agent.pollAllCoordinators(ctx)

	// Both should be polled
	count := requestCount.Load()
	if count != 2 {
		t.Errorf("expected 2 requests, got %d", count)
	}

	// Both should be healthy
	for _, coord := range config.Coordinators {
		healthy, _, _ := coord.GetHealth()
		if !healthy {
			t.Errorf("coordinator %s should be healthy", coord.Name)
		}
	}
}

func TestMultiAgentUnhealthyCoordinator(t *testing.T) {
	// One healthy, one unreachable
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]interface{}{})
	}))
	defer ts.Close()

	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "healthy", URL: ts.URL},
		{Name: "unhealthy", URL: "http://127.0.0.1:1"}, // Invalid port
	}

	agent := NewMulti(config)

	ctx := context.Background()
	agent.pollAllCoordinators(ctx)

	// Check health status
	h1, _, _ := config.Coordinators[0].GetHealth()
	h2, err2, _ := config.Coordinators[1].GetHealth()

	if !h1 {
		t.Error("expected first coordinator to be healthy")
	}
	if h2 {
		t.Error("expected second coordinator to be unhealthy")
	}
	if err2 == "" {
		t.Error("expected error message for unhealthy coordinator")
	}
}

func TestMultiAgentStatusEndpoint(t *testing.T) {
	config := DefaultMultiConfig()
	config.Port = 0 // Let OS assign port
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "coord1", URL: "http://localhost:7890"},
		{Name: "coord2", URL: "http://localhost:7891"},
	}
	config.Accounts = []string{"test@example.com"}

	agent := NewMulti(config)

	// Test handler directly
	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()

	agent.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var status map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&status); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if status["coordinator_count"].(float64) != 2 {
		t.Errorf("expected 2 coordinators, got %v", status["coordinator_count"])
	}
}

func TestMultiAgentCoordinatorsEndpoint(t *testing.T) {
	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "csd", URL: "http://100.100.118.85:7890", DisplayName: "Sense Demo"},
		{Name: "css", URL: "http://100.90.148.85:7890", DisplayName: "Super Server"},
	}

	// Mark one as healthy
	config.Coordinators[0].SetHealth(true, "")
	config.Coordinators[1].SetHealth(false, "connection refused")

	agent := NewMulti(config)

	req := httptest.NewRequest("GET", "/coordinators", nil)
	w := httptest.NewRecorder()

	agent.handleCoordinators(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var coords []map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&coords); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if len(coords) != 2 {
		t.Fatalf("expected 2 coordinators, got %d", len(coords))
	}

	// Check first (healthy)
	if coords[0]["name"] != "csd" {
		t.Errorf("expected name=csd, got %v", coords[0]["name"])
	}
	if coords[0]["is_healthy"] != true {
		t.Errorf("expected csd to be healthy")
	}

	// Check second (unhealthy)
	if coords[1]["is_healthy"] != false {
		t.Errorf("expected css to be unhealthy")
	}
	if coords[1]["last_error"] != "connection refused" {
		t.Errorf("expected error message, got %v", coords[1]["last_error"])
	}
}

func TestMultiAgentAddRemoveCoordinator(t *testing.T) {
	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "initial", URL: "http://localhost:7890"},
	}

	agent := NewMulti(config)

	if len(agent.GetCoordinators()) != 1 {
		t.Fatalf("expected 1 coordinator")
	}

	// Add
	agent.AddCoordinator(&CoordinatorEndpoint{
		Name: "new",
		URL:  "http://localhost:7891",
	})

	if len(agent.GetCoordinators()) != 2 {
		t.Errorf("expected 2 coordinators after add")
	}

	// Remove
	removed := agent.RemoveCoordinator("initial")
	if !removed {
		t.Error("expected RemoveCoordinator to return true")
	}

	if len(agent.GetCoordinators()) != 1 {
		t.Errorf("expected 1 coordinator after remove")
	}

	// Try to remove non-existent
	removed = agent.RemoveCoordinator("nonexistent")
	if removed {
		t.Error("expected RemoveCoordinator to return false for non-existent")
	}
}

func TestMultiAgentDuplicateRequestPrevention(t *testing.T) {
	// Test the processing map logic directly without browser
	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "test", URL: "http://localhost:7890"},
	}

	agent := NewMulti(config)

	// Simulate marking a request as processing
	agent.procMu.Lock()
	agent.processing["request-123"] = true
	agent.procMu.Unlock()

	// Check that it's in the map
	agent.procMu.Lock()
	processing := agent.processing["request-123"]
	agent.procMu.Unlock()

	if !processing {
		t.Error("expected request to be marked as processing")
	}

	// Simulate completion (removing from map)
	agent.procMu.Lock()
	delete(agent.processing, "request-123")
	agent.procMu.Unlock()

	// Should no longer be processing
	agent.procMu.Lock()
	processing = agent.processing["request-123"]
	agent.procMu.Unlock()

	if processing {
		t.Error("expected request to no longer be processing after delete")
	}
}

func TestMultiAgentCheckCoordinatorDeduplication(t *testing.T) {
	var pollCount atomic.Int32

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth/pending" {
			pollCount.Add(1)
			w.Header().Set("Content-Type", "application/json")
			// Return same request ID every time
			json.NewEncoder(w).Encode([]map[string]interface{}{
				{
					"id":         "dup-request-456",
					"pane_id":    1,
					"url":        "https://example.com/oauth",
					"created_at": time.Now(),
				},
			})
		}
	}))
	defer ts.Close()

	config := DefaultMultiConfig()
	config.Coordinators = []*CoordinatorEndpoint{
		{Name: "test", URL: ts.URL},
	}

	agent := NewMulti(config)

	// Pre-mark the request as processing
	agent.procMu.Lock()
	agent.processing["dup-request-456"] = true
	agent.procMu.Unlock()

	ctx := context.Background()

	// Poll should skip the already-processing request
	agent.checkCoordinator(ctx, config.Coordinators[0])

	// Give any goroutines a chance to start (they shouldn't)
	time.Sleep(50 * time.Millisecond)

	// Verify we polled but didn't spawn new processing
	if pollCount.Load() != 1 {
		t.Errorf("expected 1 poll, got %d", pollCount.Load())
	}

	// Coordinator should be healthy
	healthy, _, _ := config.Coordinators[0].GetHealth()
	if !healthy {
		t.Error("expected coordinator to be healthy after poll")
	}
}
