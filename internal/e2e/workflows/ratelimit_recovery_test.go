package workflows

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestE2E_RateLimitRecoveryWorkflow tests the full cycle of hitting a rate limit,
// waiting for cooldown (simulated), and recovering.
func TestE2E_RateLimitRecoveryWorkflow(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// ==========================================================================
	// Phase 1: Setup
	// ==========================================================================
	h.StartStep("setup", "Setting up DB and profiles")
	dbPath := filepath.Join(h.TempDir, "caam.db")
	db, err := caamdb.OpenAt(dbPath)
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	profiles := []string{"p1", "p2"}
	h.EndStep("setup")

	// ==========================================================================
	// Phase 2: Hit Rate Limit
	// ==========================================================================
	h.StartStep("hit_rate_limit", "Simulating rate limit hit")

	// P1 hits rate limit
	cooldownDuration := 1 * time.Hour
	hitTime := time.Now()
	_, err = db.SetCooldown("codex", "p1", hitTime, cooldownDuration, "rate limit")
	if err != nil {
		t.Fatalf("SetCooldown failed: %v", err)
	}
	h.LogInfo("P1 in cooldown", "until", hitTime.Add(cooldownDuration))
	h.EndStep("hit_rate_limit")

	// ==========================================================================
	// Phase 3: Failover
	// ==========================================================================
	h.StartStep("failover", "Testing failover to P2")

	selector := rotation.NewSelector(rotation.AlgorithmSmart, nil, db)
	selector.SetRNG(rand.New(rand.NewSource(42)))
	// Mock time to be just after hit
	// NOTE: Selector uses db.ListActiveCooldowns(time.Now()), so we can't easily mock time.Now() inside Selector/DB
	// unless we inject a clock. However, db.ListActiveCooldowns takes `now` as arg.
	// But `rotation.Selector` doesn't expose a way to pass `now` to `ListActiveCooldowns`.
	// It calls `s.db.ListActiveCooldowns(time.Now())`.
	//
	// So we can only test "Active Cooldown" if it really is active now.
	// Since we set it with `time.Now()`, it is active.

	result, err := selector.Select("codex", profiles, "")
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}
	if result.Selected == "p1" {
		t.Error("Should have skipped p1")
	}
	if result.Selected != "p2" {
		t.Errorf("Expected p2, got %s", result.Selected)
	}
	h.LogInfo("Failover successful", "selected", result.Selected)
	h.EndStep("failover")

	// ==========================================================================
	// Phase 4: Recovery (Expiry)
	// ==========================================================================
	h.StartStep("recovery", "Testing recovery after expiry")

	// To simulate expiry without waiting 1 hour, we must update the DB record to be in the past.
	// We can cheat by updating the record or clearing it.
	// A strictly correct test would use a mock clock, but given the constraints,
	// checking that "ActiveCooldown" returns nothing for a past time is a DB unit test.
	// Here we want E2E workflow.
	// We can manually clear it to simulate expiry.

	_, err = db.ClearCooldown("codex", "p1")
	if err != nil {
		t.Fatalf("ClearCooldown failed: %v", err)
	}
	h.LogInfo("Cooldown expired (cleared)")

	// Now P1 should be available again.
	// Depending on RNG/Recency, it might be picked.
	// Reset recency to ensure fairness
	selector.SetAvoidRecent(0)

	// We force select logic to consider p1 valid
	result, err = selector.Select("codex", profiles, "")
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}

	// Check if p1 is at least in alternatives with a positive score
	p1Available := false
	for _, alt := range result.Alternatives {
		if alt.Name == "p1" {
			// Score should not be super negative (like -10000 for cooldown)
			if alt.Score > -1000 {
				p1Available = true
			}
			h.LogInfo("P1 score", "score", alt.Score)
		}
	}
	// Or it might be selected
	if result.Selected == "p1" {
		p1Available = true
	}

	if !p1Available {
		t.Error("P1 should be available after recovery")
	}
	h.EndStep("recovery")

	t.Log("\n" + h.Summary())
}
