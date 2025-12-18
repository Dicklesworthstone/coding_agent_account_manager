package watcher

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// EventType represents the kind of profile-level change detected.
type EventType int

const (
	EventProfileAdded EventType = iota
	EventProfileModified
	EventProfileDeleted
)

func (t EventType) String() string {
	switch t {
	case EventProfileAdded:
		return "profile_added"
	case EventProfileModified:
		return "profile_modified"
	case EventProfileDeleted:
		return "profile_deleted"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// Event represents a file system change attributed to a specific provider/profile.
type Event struct {
	Type     EventType
	Provider string
	Profile  string
	Path     string
}

// Watcher monitors the profile store directory for changes and emits debounced
// profile-level events.
type Watcher struct {
	profilesDir string

	fsWatcher *fsnotify.Watcher
	events    chan Event
	errors    chan error
	done      chan struct{}

	debouncer *debouncer

	mu      sync.Mutex
	watched map[string]struct{}

	wg sync.WaitGroup
}

const (
	defaultDebounceDelay = 100 * time.Millisecond
	defaultEventsBuffer  = 100
	defaultErrorsBuffer  = 10
)

// New creates a new profile watcher using the default debounce delay (100ms).
func New(profilesDir string) (*Watcher, error) {
	return NewWithDebounceDelay(profilesDir, defaultDebounceDelay)
}

// NewWithDebounceDelay creates a new profile watcher with a configurable debounce delay.
func NewWithDebounceDelay(profilesDir string, delay time.Duration) (*Watcher, error) {
	if profilesDir == "" {
		return nil, fmt.Errorf("profilesDir is required")
	}

	absProfilesDir, err := filepath.Abs(profilesDir)
	if err != nil {
		return nil, fmt.Errorf("abs profilesDir: %w", err)
	}

	if err := os.MkdirAll(absProfilesDir, 0700); err != nil {
		return nil, fmt.Errorf("ensure profilesDir exists: %w", err)
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	w := &Watcher{
		profilesDir: absProfilesDir,
		fsWatcher:   fsw,
		events:      make(chan Event, defaultEventsBuffer),
		errors:      make(chan error, defaultErrorsBuffer),
		done:        make(chan struct{}),
		debouncer:   newDebouncer(delay),
		watched:     make(map[string]struct{}),
	}

	if err := w.addRecursive(absProfilesDir); err != nil {
		_ = fsw.Close()
		return nil, err
	}

	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run()
	}()

	return w, nil
}

func (w *Watcher) run() {
	defer close(w.events)
	defer close(w.errors)

	for {
		select {
		case <-w.done:
			return
		case evt, ok := <-w.fsWatcher.Events:
			if !ok {
				return
			}

			// If new directories appear under the watched tree, start watching them too.
			if evt.Op&fsnotify.Create != 0 {
				if isDirNoSymlink(evt.Name) {
					_ = w.addRecursive(evt.Name)
					w.maybeEmitProviderBootstrapEvents(evt.Name)
				}
			}

			if translated := w.translateEvent(evt); translated != nil {
				w.emitEvent(*translated)
			}
		case err, ok := <-w.fsWatcher.Errors:
			if !ok {
				return
			}
			w.emitError(err)
		}
	}
}

// Events returns a channel of debounced profile-level events.
func (w *Watcher) Events() <-chan Event { return w.events }

// Errors returns a channel of watcher errors.
func (w *Watcher) Errors() <-chan error { return w.errors }

// Close stops the watcher and releases OS resources.
func (w *Watcher) Close() error {
	if w == nil {
		return nil
	}

	select {
	case <-w.done:
		// already closed
	default:
		close(w.done)
	}

	// Closing the underlying watcher unblocks the run loop.
	err := w.fsWatcher.Close()
	w.wg.Wait()
	return err
}

func (w *Watcher) emitEvent(e Event) {
	select {
	case w.events <- e:
	default:
		// Best-effort: drop if consumer is stalled.
	}
}

func (w *Watcher) emitError(err error) {
	select {
	case w.errors <- err:
	default:
		// Best-effort: drop if consumer is stalled.
	}
}

func (w *Watcher) addRecursive(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// Best effort: paths may race with deletes.
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}

		// Never watch or traverse symlinks (isolated profiles contain passthrough symlinks).
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		if !d.IsDir() {
			return nil
		}

		name := d.Name()
		if name == ".git" {
			return filepath.SkipDir
		}

		return w.addDir(path)
	})
}

func (w *Watcher) addDir(path string) error {
	clean := filepath.Clean(path)

	w.mu.Lock()
	if _, ok := w.watched[clean]; ok {
		w.mu.Unlock()
		return nil
	}
	w.watched[clean] = struct{}{}
	w.mu.Unlock()

	if err := w.fsWatcher.Add(clean); err != nil {
		// Directory may disappear between discovery and Add.
		w.mu.Lock()
		delete(w.watched, clean)
		w.mu.Unlock()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("watch %s: %w", clean, err)
	}
	return nil
}

func (w *Watcher) translateEvent(e fsnotify.Event) *Event {
	if w == nil {
		return nil
	}
	if e.Name == "" {
		return nil
	}

	cleanPath := filepath.Clean(e.Name)
	rel, err := filepath.Rel(w.profilesDir, cleanPath)
	if err != nil {
		return nil
	}
	if rel == "." || rel == "" {
		return nil
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if shouldIgnoreParts(parts) {
		return nil
	}
	if len(parts) < 2 {
		return nil
	}

	prov := parts[0]
	prof := parts[1]
	if prov == "" || prof == "" {
		return nil
	}

	etype := eventTypeForPath(cleanPath, parts, e.Op)
	if etype == nil {
		return nil
	}

	// Debounce only "modified" events, keyed at provider/profile.
	if *etype == EventProfileModified {
		key := prov + "/" + prof
		if !w.debouncer.ShouldEmit(key) {
			return nil
		}
	}

	return &Event{
		Type:     *etype,
		Provider: prov,
		Profile:  prof,
		Path:     cleanPath,
	}
}

func shouldIgnoreParts(parts []string) bool {
	if len(parts) == 0 {
		return true
	}

	base := parts[len(parts)-1]
	switch base {
	case ".DS_Store", "Thumbs.db", "desktop.ini":
		return true
	}

	for _, part := range parts {
		if part == ".git" {
			return true
		}
	}

	return false
}

func (w *Watcher) maybeEmitProviderBootstrapEvents(dir string) {
	if w == nil {
		return
	}
	if dir == "" {
		return
	}

	clean := filepath.Clean(dir)
	rel, err := filepath.Rel(w.profilesDir, clean)
	if err != nil {
		return
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == "." || rel == "" {
		return
	}

	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 1 {
		return
	}

	provider := parts[0]
	if provider == "" {
		return
	}

	entries, err := os.ReadDir(clean)
	if err != nil {
		w.emitError(err)
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		name := entry.Name()
		if shouldIgnoreParts([]string{provider, name}) {
			continue
		}

		w.emitEvent(Event{
			Type:     EventProfileAdded,
			Provider: provider,
			Profile:  name,
			Path:     filepath.Join(clean, name),
		})
	}
}

func eventTypeForPath(fullPath string, relParts []string, op fsnotify.Op) *EventType {
	// Provider directory only: ignore.
	if len(relParts) < 2 {
		return nil
	}

	// Directly under provider (e.g. profiles/codex/<name>).
	// Only treat as a profile event if it's clearly a profile directory event.
	if len(relParts) == 2 {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			t := EventProfileDeleted
			return &t
		}

		if op&fsnotify.Create != 0 {
			if isDirNoSymlink(fullPath) {
				t := EventProfileAdded
				return &t
			}
			// Provider-level files (.DS_Store, etc.) are ignored.
			return nil
		}

		// Directory metadata changes (chmod) are treated as modifications; files are ignored.
		if op&(fsnotify.Write|fsnotify.Chmod) != 0 {
			if isDirNoSymlink(fullPath) {
				t := EventProfileModified
				return &t
			}
			return nil
		}

		return nil
	}

	// Anything inside a profile directory counts as a modification.
	if op&(fsnotify.Create|fsnotify.Write|fsnotify.Chmod|fsnotify.Remove|fsnotify.Rename) != 0 {
		t := EventProfileModified
		return &t
	}

	return nil
}

func isDirNoSymlink(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	return info.IsDir()
}
