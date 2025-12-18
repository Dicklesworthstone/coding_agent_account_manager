package watcher

import (
	"testing"
	"time"
)

func TestDebouncer_ShouldEmit(t *testing.T) {
	d := newDebouncer(100 * time.Millisecond)

	if got := d.ShouldEmit("codex/work"); !got {
		t.Fatalf("first ShouldEmit() = false, want true")
	}
	if got := d.ShouldEmit("codex/work"); got {
		t.Fatalf("second ShouldEmit() = true, want false")
	}

	time.Sleep(120 * time.Millisecond)
	if got := d.ShouldEmit("codex/work"); !got {
		t.Fatalf("after delay ShouldEmit() = false, want true")
	}
}
