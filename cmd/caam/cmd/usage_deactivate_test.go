package cmd

import (
	"path/filepath"
	"testing"
	"time"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
)

// TestLogProfileSwitch_EmitsDeactivateForOutgoing verifies issue #31: switching
// from codex/old to codex/new records positive active seconds for the outgoing
// profile (via a duration-bearing deactivate event) and one session/activation
// for the incoming profile.
func TestLogProfileSwitch_EmitsDeactivateForOutgoing(t *testing.T) {
	db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	// Seed a prior activation of codex/old 10 minutes ago so the deactivate has a
	// measurable duration.
	tenMinAgo := time.Now().Add(-10 * time.Minute)
	if err := db.LogEvent(caamdb.Event{
		Type:        caamdb.EventActivate,
		Provider:    "codex",
		ProfileName: "old",
		Timestamp:   tenMinAgo,
	}); err != nil {
		t.Fatalf("seed activation: %v", err)
	}

	// Switch from old -> new.
	logProfileSwitch(db, "codex", "old", "new", map[string]any{"selection_source": "test"})

	oldStats, err := db.GetStats("codex", "old")
	if err != nil {
		t.Fatalf("get old stats: %v", err)
	}
	if oldStats == nil || oldStats.TotalActiveSeconds <= 0 {
		t.Fatalf("expected positive active seconds for outgoing codex/old, got %+v", oldStats)
	}

	newStats, err := db.GetStats("codex", "new")
	if err != nil {
		t.Fatalf("get new stats: %v", err)
	}
	if newStats == nil || newStats.TotalActivations != 1 {
		t.Fatalf("expected exactly one activation for incoming codex/new, got %+v", newStats)
	}
}

// TestLogProfileSwitch_SkipsSystemOutgoing verifies that switching away from a
// system profile (e.g. _original) does not record active seconds for it.
func TestLogProfileSwitch_SkipsSystemOutgoing(t *testing.T) {
	db, err := caamdb.OpenAt(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	if err := db.LogEvent(caamdb.Event{
		Type:        caamdb.EventActivate,
		Provider:    "codex",
		ProfileName: "_original",
		Timestamp:   time.Now().Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("seed activation: %v", err)
	}

	logProfileSwitch(db, "codex", "_original", "new", nil)

	sysStats, err := db.GetStats("codex", "_original")
	if err != nil {
		t.Fatalf("get system stats: %v", err)
	}
	if sysStats != nil && sysStats.TotalActiveSeconds > 0 {
		t.Errorf("system profile should not accrue active seconds, got %+v", sysStats)
	}
}
