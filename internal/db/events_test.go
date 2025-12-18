package db

import (
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestDB_LogEventAndStats(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(tmpDir + "/caam.db")
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if err := d.LogEvent(Event{
		Type:        EventActivate,
		Provider:    "codex",
		ProfileName: "work",
		Details:     map[string]any{"from": "test"},
	}); err != nil {
		t.Fatalf("LogEvent(activate) error = %v", err)
	}

	if err := d.LogEvent(Event{
		Type:        EventError,
		Provider:    "codex",
		ProfileName: "work",
		Details:     map[string]any{"err": "boom"},
	}); err != nil {
		t.Fatalf("LogEvent(error) error = %v", err)
	}

	if err := d.LogEvent(Event{
		Type:        EventDeactivate,
		Provider:    "codex",
		ProfileName: "work",
		Duration:    90 * time.Second,
	}); err != nil {
		t.Fatalf("LogEvent(deactivate) error = %v", err)
	}

	stats, err := d.GetStats("codex", "work")
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if stats == nil {
		t.Fatalf("GetStats() = nil, want stats")
	}
	if stats.TotalActivations != 1 {
		t.Fatalf("TotalActivations = %d, want 1", stats.TotalActivations)
	}
	if stats.TotalErrors != 1 {
		t.Fatalf("TotalErrors = %d, want 1", stats.TotalErrors)
	}
	if stats.TotalActiveSeconds != 90 {
		t.Fatalf("TotalActiveSeconds = %d, want 90", stats.TotalActiveSeconds)
	}
	if stats.LastActivated.IsZero() {
		t.Fatalf("LastActivated is zero, want non-zero")
	}
	if stats.LastError.IsZero() {
		t.Fatalf("LastError is zero, want non-zero")
	}

	events, err := d.GetEvents("codex", "work", time.Time{}, 10)
	if err != nil {
		t.Fatalf("GetEvents() error = %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("GetEvents() len = %d, want 3", len(events))
	}
}

func TestDB_LogEvent_Concurrent(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(tmpDir + "/caam.db")
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	const (
		goroutines = 10
		perWorker  = 25
	)

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*perWorker)

	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				if err := d.LogEvent(Event{
					Type:        EventActivate,
					Provider:    "claude",
					ProfileName: "work",
					Details:     map[string]any{"worker": worker, "n": j},
				}); err != nil {
					errCh <- err
					return
				}
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Fatalf("concurrent LogEvent error = %v", err)
	}

	stats, err := d.GetStats("claude", "work")
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if stats == nil {
		t.Fatalf("GetStats() = nil, want stats")
	}
	want := goroutines * perWorker
	if stats.TotalActivations != want {
		t.Fatalf("TotalActivations = %d, want %d", stats.TotalActivations, want)
	}
}

func TestDB_LastActivation_UsesStatsAndFallsBackToActivityLog(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(tmpDir + "/caam.db")
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	ts := time.Now().UTC().Truncate(time.Second).Add(-1 * time.Hour)
	if err := d.LogEvent(Event{
		Type:        EventActivate,
		Provider:    "codex",
		ProfileName: "work",
		Timestamp:   ts,
	}); err != nil {
		t.Fatalf("LogEvent(activate) error = %v", err)
	}

	got, err := d.LastActivation("codex", "work")
	if err != nil {
		t.Fatalf("LastActivation(stats) error = %v", err)
	}
	if !got.Equal(ts) {
		t.Fatalf("LastActivation(stats) = %s, want %s", got.Format(time.RFC3339Nano), ts.Format(time.RFC3339Nano))
	}

	// Remove stats row to force fallback behavior.
	if _, err := d.Conn().Exec(`DELETE FROM profile_stats WHERE provider = ? AND profile_name = ?`, "codex", "work"); err != nil {
		t.Fatalf("DELETE profile_stats error = %v", err)
	}

	got, err = d.LastActivation("codex", "work")
	if err != nil {
		t.Fatalf("LastActivation(fallback) error = %v", err)
	}
	if !got.Equal(ts) {
		t.Fatalf("LastActivation(fallback) = %s, want %s", got.Format(time.RFC3339Nano), ts.Format(time.RFC3339Nano))
	}

	empty, err := d.LastActivation("codex", "nope")
	if err != nil {
		t.Fatalf("LastActivation(empty) error = %v", err)
	}
	if !empty.IsZero() {
		t.Fatalf("LastActivation(empty) = %s, want zero", empty.Format(time.RFC3339Nano))
	}
}

func TestDB_ProfileStats_LastErrorIsMonotonic(t *testing.T) {
	tmpDir := t.TempDir()
	d, err := OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	now := time.Now().UTC().Truncate(time.Second)
	newer := now.Add(-10 * time.Minute)
	older := now.Add(-2 * time.Hour)

	if err := d.LogEvent(Event{
		Type:        EventError,
		Provider:    "codex",
		ProfileName: "work",
		Timestamp:   newer,
	}); err != nil {
		t.Fatalf("LogEvent(newer error) error = %v", err)
	}

	// Insert an older error after a newer one; LastError should not move backwards.
	if err := d.LogEvent(Event{
		Type:        EventError,
		Provider:    "codex",
		ProfileName: "work",
		Timestamp:   older,
	}); err != nil {
		t.Fatalf("LogEvent(older error) error = %v", err)
	}

	stats, err := d.GetStats("codex", "work")
	if err != nil {
		t.Fatalf("GetStats() error = %v", err)
	}
	if stats == nil {
		t.Fatalf("GetStats() = nil, want stats")
	}
	if stats.TotalErrors != 2 {
		t.Fatalf("TotalErrors = %d, want 2", stats.TotalErrors)
	}
	if !stats.LastError.Equal(newer) {
		t.Fatalf("LastError = %s, want %s", stats.LastError.Format(time.RFC3339Nano), newer.Format(time.RFC3339Nano))
	}
}
