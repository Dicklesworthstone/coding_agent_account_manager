package usage

import (
	"context"
	"testing"
)

func TestCursorAndGrokFailuresDoNotDropSiblingProfiles(t *testing.T) {
	fetcher := NewMultiProfileFetcher()
	ctx := context.Background()

	cursorRows := fetcher.FetchAllProfiles(ctx, "cursor", map[string]string{
		"one": t.TempDir(),
		"two": t.TempDir(),
	})
	if len(cursorRows) != 2 {
		t.Fatalf("cursor rows = %d, want 2", len(cursorRows))
	}
	for _, row := range cursorRows {
		if row.Usage == nil || row.Usage.QuotaStatus != QuotaUnavailable || row.Usage.Error == "" {
			t.Fatalf("cursor row %+v", row.Usage)
		}
	}

	grokRows := fetcher.FetchAllProfiles(ctx, "grok", map[string]string{"g": t.TempDir()})
	if len(grokRows) != 1 || grokRows[0].Usage == nil || grokRows[0].Usage.Error == "" {
		t.Fatalf("grok row %+v", grokRows)
	}
}
