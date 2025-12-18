package watcher

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"
)

func TestWatcher_TranslateEvent_IgnoresProviderRootFiles(t *testing.T) {
	tmpDir := t.TempDir()
	providerDir := filepath.Join(tmpDir, "codex")
	if err := os.MkdirAll(providerDir, 0700); err != nil {
		t.Fatalf("mkdir provider: %v", err)
	}

	dsStorePath := filepath.Join(providerDir, ".DS_Store")
	if err := os.WriteFile(dsStorePath, []byte("x"), 0600); err != nil {
		t.Fatalf("write .DS_Store: %v", err)
	}

	w := &Watcher{profilesDir: tmpDir}
	if got := w.translateEvent(fsnotify.Event{Name: dsStorePath, Op: fsnotify.Create}); got != nil {
		t.Fatalf("translateEvent() = %+v, want nil", *got)
	}
}

func TestWatcher_TranslateEvent_ParsesProviderAndProfile(t *testing.T) {
	tmpDir := t.TempDir()
	profileDir := filepath.Join(tmpDir, "codex", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("mkdir profile: %v", err)
	}

	profileJSON := filepath.Join(profileDir, "profile.json")
	if err := os.WriteFile(profileJSON, []byte("{}"), 0600); err != nil {
		t.Fatalf("write profile.json: %v", err)
	}

	w := &Watcher{profilesDir: tmpDir, debouncer: nil}
	got := w.translateEvent(fsnotify.Event{Name: profileJSON, Op: fsnotify.Write})
	if got == nil {
		t.Fatalf("translateEvent() = nil, want event")
	}
	if got.Provider != "codex" {
		t.Fatalf("Provider = %q, want %q", got.Provider, "codex")
	}
	if got.Profile != "work" {
		t.Fatalf("Profile = %q, want %q", got.Profile, "work")
	}
	if got.Type != EventProfileModified {
		t.Fatalf("Type = %v, want %v", got.Type, EventProfileModified)
	}
}

func TestWatcher_EndToEnd_ProfileLifecycle(t *testing.T) {
	tmpDir := t.TempDir()

	w, err := NewWithDebounceDelay(tmpDir, 25*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWithDebounceDelay() error = %v", err)
	}
	defer w.Close()

	profileDir := filepath.Join(tmpDir, "codex", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("mkdir profile: %v", err)
	}

	// We should receive an "added" event even when provider and profile directories
	// are created in rapid succession (common on first-run).
	waitForEvent(t, w.Events(), func(e Event) bool {
		return e.Type == EventProfileAdded && e.Provider == "codex" && e.Profile == "work"
	})

	profileJSON := filepath.Join(profileDir, "profile.json")
	if err := os.WriteFile(profileJSON, []byte("{}"), 0600); err != nil {
		t.Fatalf("write profile.json: %v", err)
	}

	waitForEvent(t, w.Events(), func(e Event) bool {
		return e.Type == EventProfileModified && e.Provider == "codex" && e.Profile == "work"
	})

	if err := os.RemoveAll(profileDir); err != nil {
		t.Fatalf("remove profile: %v", err)
	}

	waitForEvent(t, w.Events(), func(e Event) bool {
		return e.Type == EventProfileDeleted && e.Provider == "codex" && e.Profile == "work"
	})
}

func waitForEvent(t *testing.T, ch <-chan Event, match func(Event) bool) Event {
	t.Helper()

	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()

	for {
		select {
		case e, ok := <-ch:
			if !ok {
				t.Fatalf("events channel closed while waiting")
			}
			if match(e) {
				return e
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for event")
		}
	}
}
