// Package authwatch monitors auth files for external changes.
//
// This package detects when users login to AI tools directly (outside of caam),
// potentially overwriting saved auth states. It can:
//   - Track content hashes of auth files
//   - Detect when auth doesn't match any saved profile
//   - Trigger automatic backups or user prompts
package authwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// ChangeType represents the kind of auth file change detected.
type ChangeType int

const (
	// ChangeNone means no change detected.
	ChangeNone ChangeType = iota
	// ChangeNew means auth files exist that weren't there before.
	ChangeNew
	// ChangeModified means auth file content changed.
	ChangeModified
	// ChangeRemoved means auth files were removed.
	ChangeRemoved
)

func (c ChangeType) String() string {
	switch c {
	case ChangeNone:
		return "none"
	case ChangeNew:
		return "new"
	case ChangeModified:
		return "modified"
	case ChangeRemoved:
		return "removed"
	default:
		return fmt.Sprintf("unknown(%d)", int(c))
	}
}

// AuthState represents the current state of auth files for a provider.
type AuthState struct {
	Provider     string            // claude, codex, gemini
	Exists       bool              // Whether auth files exist
	ContentHash  string            // Combined hash of all auth file contents
	FileHashes   map[string]string // Individual file hashes
	LastModified time.Time         // Most recent modification time
	CheckedAt    time.Time         // When this state was captured
}

// Change represents a detected change in auth state.
type Change struct {
	Provider    string
	Type        ChangeType
	OldState    *AuthState
	NewState    *AuthState
	Description string
}

// Tracker monitors auth file states and detects changes.
type Tracker struct {
	vault  *authfile.Vault
	states map[string]*AuthState // provider -> state
	mu     sync.RWMutex
}

// NewTracker creates a new auth state tracker.
func NewTracker(vault *authfile.Vault) *Tracker {
	return &Tracker{
		vault:  vault,
		states: make(map[string]*AuthState),
	}
}

// Capture captures the current auth state for a provider.
func (t *Tracker) Capture(provider string) (*AuthState, error) {
	fileSet := getFileSet(provider)
	if fileSet.Tool == "" {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	state := &AuthState{
		Provider:   provider,
		FileHashes: make(map[string]string),
		CheckedAt:  time.Now(),
	}

	var hasher = sha256.New()
	var latestMod time.Time

	for _, spec := range fileSet.Files {
		info, err := os.Stat(spec.Path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", spec.Path, err)
		}

		if info.ModTime().After(latestMod) {
			latestMod = info.ModTime()
		}

		content, err := os.ReadFile(spec.Path)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", spec.Path, err)
		}

		fileHash := sha256.Sum256(content)
		hashStr := hex.EncodeToString(fileHash[:])
		state.FileHashes[spec.Path] = hashStr

		// Add to combined hash with filename + length delimiters to avoid ambiguity.
		writeHashComponent(hasher, filepath.Base(spec.Path), content)
		state.Exists = true
	}

	if state.Exists {
		state.ContentHash = hex.EncodeToString(hasher.Sum(nil))
		state.LastModified = latestMod
	}

	t.mu.Lock()
	t.states[provider] = state
	t.mu.Unlock()

	return state, nil
}

// CaptureAll captures auth states for all providers.
func (t *Tracker) CaptureAll() (map[string]*AuthState, error) {
	providers := []string{"claude", "codex", "gemini"}
	results := make(map[string]*AuthState)

	for _, p := range providers {
		state, err := t.Capture(p)
		if err != nil {
			// Non-fatal: continue with other providers
			continue
		}
		results[p] = state
	}

	return results, nil
}

// GetState returns the last captured state for a provider.
func (t *Tracker) GetState(provider string) *AuthState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.states[provider]
}

// DetectChange compares current auth state against the last captured state.
func (t *Tracker) DetectChange(provider string) (*Change, error) {
	oldState := t.GetState(provider)
	newState, err := t.Capture(provider)
	if err != nil {
		return nil, err
	}

	change := &Change{
		Provider: provider,
		OldState: oldState,
		NewState: newState,
	}

	// Determine change type
	if oldState == nil {
		if newState.Exists {
			change.Type = ChangeNew
			change.Description = "Auth files appeared"
		}
		return change, nil
	}

	if oldState.Exists && !newState.Exists {
		change.Type = ChangeRemoved
		change.Description = "Auth files were removed"
		return change, nil
	}

	if !oldState.Exists && newState.Exists {
		change.Type = ChangeNew
		change.Description = "Auth files appeared"
		return change, nil
	}

	if oldState.ContentHash != newState.ContentHash {
		change.Type = ChangeModified
		change.Description = "Auth files were modified"
		return change, nil
	}

	change.Type = ChangeNone
	return change, nil
}

// DetectAllChanges checks all providers for changes.
func (t *Tracker) DetectAllChanges() ([]Change, error) {
	providers := []string{"claude", "codex", "gemini"}
	var changes []Change

	for _, p := range providers {
		change, err := t.DetectChange(p)
		if err != nil {
			continue
		}
		if change.Type != ChangeNone {
			changes = append(changes, *change)
		}
	}

	return changes, nil
}

// MatchesProfile checks if current auth matches a saved profile.
func (t *Tracker) MatchesProfile(provider, profile string) (bool, error) {
	if t.vault == nil {
		return false, fmt.Errorf("vault not configured")
	}

	currentState, err := t.Capture(provider)
	if err != nil {
		return false, err
	}

	if !currentState.Exists {
		return false, nil
	}

	// Get the profile's auth hash
	profileHash, err := t.getProfileHash(provider, profile)
	if err != nil {
		return false, err
	}

	return currentState.ContentHash == profileHash, nil
}

// FindMatchingProfile finds which saved profile (if any) matches current auth.
func (t *Tracker) FindMatchingProfile(provider string) (string, error) {
	if t.vault == nil {
		return "", fmt.Errorf("vault not configured")
	}

	currentState, err := t.Capture(provider)
	if err != nil {
		return "", err
	}

	if !currentState.Exists {
		return "", nil
	}

	profiles, err := t.vault.List(provider)
	if err != nil {
		return "", err
	}

	for _, profile := range profiles {
		profileHash, err := t.getProfileHash(provider, profile)
		if err != nil {
			continue
		}

		if currentState.ContentHash == profileHash {
			return profile, nil
		}
	}

	return "", nil
}

// getProfileHash computes the content hash of a saved profile.
func (t *Tracker) getProfileHash(provider, profile string) (string, error) {
	profilePath := t.vault.ProfilePath(provider, profile)
	fileSet := getFileSet(provider)

	hasher := sha256.New()
	requiredFound := false
	optionalFound := false
	var missingRequired []string

	for _, spec := range fileSet.Files {
		// Map source path to profile path
		fileName := filepath.Base(spec.Path)
		profileFilePath := filepath.Join(profilePath, fileName)

		content, err := os.ReadFile(profileFilePath)
		if err != nil {
			if os.IsNotExist(err) {
				if spec.Required {
					missingRequired = append(missingRequired, profileFilePath)
				}
				continue
			}
			return "", err
		}

		writeHashComponent(hasher, fileName, content)
		if spec.Required {
			requiredFound = true
		} else {
			optionalFound = true
		}
	}

	if !requiredFound && !optionalFound {
		return "", fmt.Errorf("no auth files found for %s/%s", provider, profile)
	}
	if len(missingRequired) > 0 {
		if !(fileSet.AllowOptionalOnly && !requiredFound && optionalFound) {
			return "", fmt.Errorf("required backup not found: %s", missingRequired[0])
		}
	}

	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// AuthStatus represents the overall auth status for a provider.
type AuthStatus struct {
	Provider        string
	HasAuth         bool   // Auth files exist
	MatchedProfile  string // Name of matching profile (empty if no match)
	IsUnsaved       bool   // Auth exists but doesn't match any profile
	ContentHash     string
	LastModified    time.Time
	SuggestedAction string // Suggested action for the user
}

// GetStatus returns the complete auth status for a provider.
func (t *Tracker) GetStatus(provider string) (*AuthStatus, error) {
	state, err := t.Capture(provider)
	if err != nil {
		return nil, err
	}

	status := &AuthStatus{
		Provider:     provider,
		HasAuth:      state.Exists,
		ContentHash:  state.ContentHash,
		LastModified: state.LastModified,
	}

	if !state.Exists {
		return status, nil
	}

	matchedProfile, err := t.FindMatchingProfile(provider)
	if err != nil {
		return status, nil
	}

	status.MatchedProfile = matchedProfile
	status.IsUnsaved = matchedProfile == ""

	if status.IsUnsaved {
		status.SuggestedAction = fmt.Sprintf("caam backup %s <profile-name>", provider)
	}

	return status, nil
}

// GetAllStatuses returns auth status for all providers.
func (t *Tracker) GetAllStatuses() ([]*AuthStatus, error) {
	providers := []string{"claude", "codex", "gemini"}
	var statuses []*AuthStatus

	for _, p := range providers {
		status, err := t.GetStatus(p)
		if err != nil {
			continue
		}
		statuses = append(statuses, status)
	}

	return statuses, nil
}

// StateFile represents the persistent state file for tracking auth changes.
type StateFile struct {
	States    map[string]*AuthState `json:"states"`
	UpdatedAt time.Time             `json:"updated_at"`
}

// StatePath returns the path to the state file.
func StatePath() string {
	if caamHome := os.Getenv("CAAM_HOME"); caamHome != "" {
		return filepath.Join(caamHome, "data", "auth_state.json")
	}
	homeDir, _ := os.UserHomeDir()
	xdgData := os.Getenv("XDG_DATA_HOME")
	if xdgData == "" {
		xdgData = filepath.Join(homeDir, ".local", "share")
	}
	return filepath.Join(xdgData, "caam", "auth_state.json")
}

// SaveState persists the current tracker state to disk.
func (t *Tracker) SaveState() error {
	t.mu.RLock()
	stateFile := &StateFile{
		States:    t.states,
		UpdatedAt: time.Now(),
	}
	data, err := json.MarshalIndent(stateFile, "", "  ")
	t.mu.RUnlock()
	if err != nil {
		return fmt.Errorf("marshal state: %w", err)
	}

	path := StatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	// Atomic write with fsync for durability
	tmpPath := path + ".tmp"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create temp state: %w", err)
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("write temp state: %w", err)
	}

	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("sync temp state: %w", err)
	}

	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("close temp state: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename state file: %w", err)
	}

	return nil
}

// LoadState loads the tracker state from disk.
func (t *Tracker) LoadState() error {
	path := StatePath()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No saved state yet
		}
		return fmt.Errorf("read state: %w", err)
	}

	var stateFile StateFile
	if err := json.Unmarshal(data, &stateFile); err != nil {
		return fmt.Errorf("parse state: %w", err)
	}

	t.mu.Lock()
	t.states = stateFile.States
	if t.states == nil {
		t.states = make(map[string]*AuthState)
	}
	t.mu.Unlock()

	return nil
}

// getFileSet returns the auth file set for a provider.
func getFileSet(provider string) authfile.AuthFileSet {
	switch strings.ToLower(provider) {
	case "claude":
		return authfile.ClaudeAuthFiles()
	case "codex":
		return authfile.CodexAuthFiles()
	case "gemini":
		return authfile.GeminiAuthFiles()
	default:
		return authfile.AuthFileSet{}
	}
}

// CheckUnsavedAuth is a convenience function that checks for unsaved auth.
// It returns a list of providers with unsaved auth states.
func CheckUnsavedAuth(vault *authfile.Vault) ([]string, error) {
	tracker := NewTracker(vault)

	var unsaved []string
	providers := []string{"claude", "codex", "gemini"}

	for _, p := range providers {
		status, err := tracker.GetStatus(p)
		if err != nil {
			continue
		}

		if status.IsUnsaved {
			unsaved = append(unsaved, p)
		}
	}

	return unsaved, nil
}

// FormatUnsavedWarning formats a warning message about unsaved auth.
func FormatUnsavedWarning(providers []string) string {
	if len(providers) == 0 {
		return ""
	}

	var buf strings.Builder
	buf.WriteString("Warning: Unsaved auth detected for: ")
	buf.WriteString(strings.Join(providers, ", "))
	buf.WriteString("\n")
	buf.WriteString("These auth states don't match any saved profile and could be lost.\n")
	buf.WriteString("Run 'caam backup <tool> <profile-name>' to save them.\n")

	return buf.String()
}

func writeHashComponent(hasher hash.Hash, name string, content []byte) {
	if hasher == nil {
		return
	}

	hasher.Write([]byte(name))
	hasher.Write([]byte{0})

	var lenBuf [20]byte
	lenBytes := strconv.AppendInt(lenBuf[:0], int64(len(content)), 10)
	hasher.Write(lenBytes)
	hasher.Write([]byte{0})

	hasher.Write(content)
	hasher.Write([]byte{0})
}

// Watcher provides real-time monitoring of auth file changes.
// It wraps fsnotify to watch auth file paths directly.
type Watcher struct {
	tracker  *Tracker
	onChange func(Change)
	done     chan struct{}
	mu       sync.Mutex
	running  bool
	stopOnce sync.Once // Ensures done channel is only closed once
}

// NewWatcher creates a new auth file watcher.
func NewWatcher(vault *authfile.Vault, onChange func(Change)) *Watcher {
	return &Watcher{
		tracker:  NewTracker(vault),
		onChange: onChange,
		done:     make(chan struct{}),
	}
}

// Start begins watching auth files for changes.
// This is a blocking call - run in a goroutine.
func (w *Watcher) Start() error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return fmt.Errorf("watcher already running")
	}
	w.running = true
	// Recreate done channel in case watcher was previously stopped
	w.done = make(chan struct{})
	w.stopOnce = sync.Once{} // Reset for new Start cycle
	w.mu.Unlock()

	// Capture initial state
	if _, err := w.tracker.CaptureAll(); err != nil {
		// Reset running state on failure so Start() can be called again
		w.mu.Lock()
		w.running = false
		w.mu.Unlock()
		return err
	}

	// Poll for changes (simpler than fsnotify for cross-platform auth files)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-w.done:
			return nil
		case <-ticker.C:
			changes, _ := w.tracker.DetectAllChanges()
			for _, change := range changes {
				if w.onChange != nil {
					w.onChange(change)
				}
			}
		}
	}
}

// Stop stops the watcher.
func (w *Watcher) Stop() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.running {
		return
	}

	w.running = false
	// Use sync.Once to ensure done channel is only closed once, preventing panic
	// if Stop() is called concurrently or multiple times.
	w.stopOnce.Do(func() {
		close(w.done)
	})
}
