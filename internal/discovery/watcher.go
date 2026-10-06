// Package discovery provides automatic detection of auth file changes
// and auto-discovery of new accounts.
package discovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/claudesettings"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/fsnotify/fsnotify"
)

// WatcherConfig configures the auth file watcher.
type WatcherConfig struct {
	// Providers to watch (e.g., ["claude", "codex", "gemini"]).
	// If empty, watches all known providers.
	Providers []string

	// DebounceInterval is the time to wait after a file change before processing.
	// Multiple rapid changes are coalesced into one event.
	// Default: 500ms
	DebounceInterval time.Duration

	// PollInterval reconciles credentials and repairs directory watches even
	// when filesystem notifications are unavailable. Default: 5 seconds.
	PollInterval time.Duration

	// OnDiscovery is called when a new account is discovered.
	// The callback receives the provider, email, and identity details.
	OnDiscovery func(provider, email string, ident *identity.Identity)

	// OnChange is called when an auth file changes (even if not a new account).
	OnChange func(provider, path string)

	// OnError is called when an error occurs during watching or processing.
	OnError func(err error)

	// Logger for structured logging.
	Logger *slog.Logger
}

// Watcher monitors auth file changes and auto-discovers new accounts.
type Watcher struct {
	vault      *authfile.Vault
	config     WatcherConfig
	logger     *slog.Logger
	mu         sync.Mutex // Serializes complete Start/Stop transitions.
	cancel     context.CancelFunc
	done       chan struct{}
	newBackend func() (*fsnotify.Watcher, error)
}

type pendingChange struct {
	path string
	at   time.Time
}

// NewWatcher creates a new auth file watcher.
func NewWatcher(vault *authfile.Vault, config WatcherConfig) (*Watcher, error) {
	if vault == nil {
		return nil, fmt.Errorf("watcher requires a vault")
	}
	if config.DebounceInterval < 0 || config.PollInterval < 0 {
		return nil, fmt.Errorf("watcher intervals must not be negative")
	}
	if config.DebounceInterval == 0 {
		config.DebounceInterval = 500 * time.Millisecond
	}
	if config.PollInterval == 0 {
		config.PollInterval = 5 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if len(config.Providers) == 0 {
		config.Providers = []string{"claude", "codex", "gemini", "grok", "opencode", "cursor"}
	}

	return &Watcher{
		vault:      vault,
		config:     config,
		logger:     config.Logger,
		newBackend: fsnotify.NewWatcher,
	}, nil
}

// Start begins watching auth files for changes.
// Call Stop() to stop the watcher.
func (w *Watcher) Start(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	if w.done != nil {
		select {
		case <-w.done:
		default:
			return fmt.Errorf("watcher already running")
		}
	}
	sources := make(map[string]authfile.AuthFileSet)
	for _, provider := range w.config.Providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if fileSet, ok := authfile.GetAuthFileSet(provider); ok {
			sources[provider] = fileSet
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel, w.done = cancel, make(chan struct{})
	// Set up watches before returning so the first login cannot race setup.
	backend, watched := w.openBackend(sources)
	go w.run(runCtx, w.done, sources, backend, watched)
	return nil
}

// Stop halts the watcher.
func (w *Watcher) Stop() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cancel != nil {
		w.cancel()
		<-w.done // Includes any backup already in progress and backend close.
		w.cancel, w.done = nil, nil
	}
	return nil
}

func (w *Watcher) openBackend(sources map[string]authfile.AuthFileSet) (*fsnotify.Watcher, map[string]os.FileInfo) {
	backend, err := w.newBackend()
	if err != nil {
		w.logger.Warn("filesystem notifications unavailable; polling auth files", "error", err)
		return nil, nil
	}
	watched := make(map[string]os.FileInfo)
	w.repairWatches(backend, watched, sources)
	return backend, watched
}

func (w *Watcher) repairWatches(backend *fsnotify.Watcher, watched map[string]os.FileInfo, sources map[string]authfile.AuthFileSet) {
	wanted := make(map[string]os.FileInfo)
	for _, fileSet := range sources {
		for _, spec := range fileSet.Files {
			dir := filepath.Dir(spec.Path)
			for {
				info, err := os.Stat(dir)
				if err == nil && info.IsDir() {
					wanted[dir] = info
					break
				}
				parent := filepath.Dir(dir)
				if parent == dir {
					break
				}
				dir = parent
			}
		}
	}
	for dir, old := range watched {
		if current, ok := wanted[dir]; !ok || !os.SameFile(old, current) {
			_ = backend.Remove(dir)
			delete(watched, dir)
		}
	}
	for dir, info := range wanted {
		if _, ok := watched[dir]; ok {
			continue
		}
		if err := backend.Add(dir); err != nil {
			w.logger.Debug("directory watch unavailable; polling auth files", "path", dir, "error", err)
			continue
		}
		watched[dir] = info
	}
}

// One goroutine owns the pending queue, reconciliation and native backend.
// Backups cannot overlap, and a queued event never loses its provider binding.
func (w *Watcher) run(ctx context.Context, done chan<- struct{}, sources map[string]authfile.AuthFileSet, backend *fsnotify.Watcher, watched map[string]os.FileInfo) {
	defer close(done)
	defer func() {
		if backend != nil {
			_ = backend.Close()
		}
	}()
	poll := time.NewTicker(w.config.PollInterval)
	defer poll.Stop()
	debounce := time.NewTicker(min(w.config.DebounceInterval, 100*time.Millisecond))
	defer debounce.Stop()
	pending := make(map[string]pendingChange)
	queueAll := func() {
		for provider := range sources {
			if _, ok := pending[provider]; !ok {
				pending[provider] = pendingChange{at: time.Now()}
			}
		}
	}
	for {
		var events <-chan fsnotify.Event
		var failures <-chan error
		if backend != nil {
			events, failures = backend.Events, backend.Errors
		}
		select {
		case <-ctx.Done():
			return
		case <-poll.C:
			if backend == nil {
				backend, watched = w.openBackend(sources)
			} else {
				w.repairWatches(backend, watched, sources)
			}
			queueAll()
		case <-debounce.C:
			for provider, change := range pending {
				if time.Since(change.at) < w.config.DebounceInterval {
					continue
				}
				delete(pending, provider)
				if ctx.Err() != nil {
					return
				}
				if backend != nil {
					// A replacement directory may have appeared after event-time
					// repair selected its parent, but before that parent watch was
					// attached. Rebind before reporting the reconciled login so
					// subsequent writes in the replacement are observed too.
					w.repairWatches(backend, watched, sources)
				}
				w.processChange(provider, sources[provider], change.path)
			}
		case event, ok := <-events:
			if !ok {
				_ = backend.Close()
				backend = nil
				queueAll()
				continue
			}
			for provider, fileSet := range sources {
				for _, spec := range fileSet.Files {
					if filepath.Clean(event.Name) != filepath.Clean(spec.Path) {
						continue
					}
					if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
						pending[provider] = pendingChange{path: spec.Path, at: time.Now()}
						if w.config.OnChange != nil {
							w.config.OnChange(provider, spec.Path)
						}
					}
				}
			}
			if event.Op&(fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
				_, wasWatchedDirectory := watched[event.Name]
				info, statErr := os.Stat(event.Name)
				isDirectory := statErr == nil && info.IsDir()
				w.repairWatches(backend, watched, sources)
				if wasWatchedDirectory || isDirectory {
					// A new directory can already contain credentials by the
					// time its watch is attached. Reconcile that event gap.
					queueAll()
				}
			}
		case err, ok := <-failures:
			if ok {
				w.logger.Warn("filesystem notifications failed; polling auth files", "error", err)
				if w.config.OnError != nil {
					w.config.OnError(err)
				}
			}
			_ = backend.Close()
			backend = nil
			queueAll()
		}
	}
}

func (w *Watcher) processChange(provider string, fileSet authfile.AuthFileSet, path string) {
	name, ident, err := discoverAccount(w.vault, fileSet)
	if err != nil {
		w.logger.Warn("auth reconciliation failed", "provider", provider, "path", path, "error", err)
		if w.config.OnError != nil {
			w.config.OnError(err)
		}
		return
	}
	if name != "" {
		w.logger.Info("saved discovered credentials", "provider", provider, "profile", name)
		if w.config.OnDiscovery != nil {
			w.config.OnDiscovery(provider, name, ident)
		}
	}
}

// discoverAccount distinguishes an account match from an exact credential
// generation. ActiveProfile intentionally survives native token rotation;
// that must select the existing profile, not suppress its updated snapshot.
func discoverAccount(vault *authfile.Vault, fileSet authfile.AuthFileSet) (string, *identity.Identity, error) {
	if !authfile.HasAuthFiles(fileSet) {
		return "", nil, nil
	}
	specs, err := generationFiles(fileSet)
	if err != nil {
		return "", nil, err
	}
	generation, err := credentialGeneration(fileSet.Tool, specs, "")
	if err != nil || len(generation) == 0 {
		return "", nil, err
	}
	var ident *identity.Identity
	for _, spec := range specs {
		candidate, err := extractIdentity(fileSet.Tool, spec.Path)
		if err == nil && candidate != nil && strings.TrimSpace(candidate.Email) != "" {
			ident = candidate
			break
		}
	}
	name, err := vault.ActiveProfile(fileSet)
	if err != nil {
		return "", nil, err
	}
	if authfile.IsSystemProfile(name) {
		name = ""
	}
	if name == "" && ident != nil {
		name = strings.TrimSpace(ident.Email)
	}
	profiles, err := vault.List(fileSet.Tool)
	if err != nil {
		return "", ident, err
	}
	for _, existing := range profiles {
		if authfile.IsSystemProfile(existing) {
			continue
		}
		// Only a listed snapshot may be read here. A new identity-derived
		// name is validated by Backup before it can become a vault path.
		profileDir := vault.ProfilePath(fileSet.Tool, existing)
		if saved, err := credentialGeneration(fileSet.Tool, specs, profileDir); err == nil && bytes.Equal(generation, saved) {
			return "", ident, nil
		}
	}
	if name == "" || authfile.IsSystemProfile(name) {
		name = autoProfileName(vault, fileSet.Tool)
	}
	if err := vault.Backup(fileSet, name); err != nil {
		return "", ident, fmt.Errorf("backup %s/%s: %w", fileSet.Tool, name, err)
	}
	return name, ident, nil
}

// generationFiles excludes optional workflow files when the primary OAuth
// source exists. API-helper and keychain-only layouts still have a fallback.
func generationFiles(fileSet authfile.AuthFileSet) ([]authfile.AuthFileSpec, error) {
	primary := ""
	switch fileSet.Tool {
	case "claude":
		primary = ".credentials.json"
	case "cursor":
		primary = "auth.json"
	}
	if primary != "" {
		for _, spec := range fileSet.Files {
			if filepath.Base(spec.Path) != primary {
				continue
			}
			if _, err := os.Stat(spec.Path); err == nil {
				return []authfile.AuthFileSpec{spec}, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect %s credentials: %w", fileSet.Tool, err)
			}
		}
	}
	var specs []authfile.AuthFileSpec
	for _, spec := range fileSet.Files {
		if fileSet.Tool == "grok" && filepath.Base(spec.Path) == "config.toml" {
			continue
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

// credentialGeneration is private comparison material, never logged or
// serialized. JSON formatting and optional shared policy are not a login.
func credentialGeneration(provider string, specs []authfile.AuthFileSpec, snapshotDir string) ([]byte, error) {
	parts := make(map[string]json.RawMessage)
	for _, spec := range specs {
		path, filename := spec.Path, filepath.Base(spec.Path)
		if snapshotDir != "" {
			path = filepath.Join(snapshotDir, filename)
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Size() > 8<<20 {
			return nil, fmt.Errorf("%s credential source is not a regular file smaller than 8 MiB", provider)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if filename == ".env" {
			parts[filename], _ = json.Marshal(string(data))
			continue
		}
		var obj map[string]json.RawMessage
		if json.Unmarshal(data, &obj) != nil || obj == nil {
			return nil, fmt.Errorf("%s credential source contains invalid JSON", provider)
		}
		if provider == "claude" {
			switch filename {
			case ".credentials.json":
				var oauth map[string]json.RawMessage
				if json.Unmarshal(obj["claudeAiOauth"], &oauth) != nil || oauth == nil {
					return nil, fmt.Errorf("Claude credential has no OAuth grant")
				}
				obj = map[string]json.RawMessage{"accessToken": oauth["accessToken"], "refreshToken": oauth["refreshToken"]}
			case "settings.json":
				policy, err := claudesettings.LoadPolicy()
				if err != nil {
					return nil, err
				}
				data, err = claudesettings.Identity(data, policy)
				if err != nil {
					return nil, err
				}
				obj = nil
				_ = json.Unmarshal(data, &obj)
			case ".claude.json":
				data, err = claudesettings.LegacyIdentity(data)
				if err != nil {
					return nil, err
				}
				obj = nil
				_ = json.Unmarshal(data, &obj)
			case "config.json":
				for key := range obj {
					if !strings.HasPrefix(key, "oauth:tokenCache") {
						delete(obj, key)
					}
				}
			}
		} else if provider == "cursor" && filename != "auth.json" {
			for key := range obj {
				if key != "authInfo" && key != "apiKey" && key != "accessToken" && key != "refreshToken" {
					delete(obj, key)
				}
			}
		}
		if len(obj) == 0 {
			continue
		}
		data, err = json.Marshal(obj)
		if err != nil {
			return nil, fmt.Errorf("%s credential fields are malformed", provider)
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var canonical interface{}
		if err := decoder.Decode(&canonical); err != nil {
			return nil, fmt.Errorf("%s credential fields are malformed", provider)
		}
		parts[filename], _ = json.Marshal(canonical)
	}
	if len(parts) == 0 {
		return nil, nil
	}
	data, _ := json.Marshal(parts)
	digest := sha256.Sum256(data)
	return digest[:], nil
}

// extractIdentity extracts account identity from an auth file.
func extractIdentity(provider, path string) (*identity.Identity, error) {
	// Check file exists and is readable
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}

	switch provider {
	case "claude":
		// Primary: .credentials.json
		if strings.HasSuffix(path, ".credentials.json") {
			return identity.ExtractFromClaudeCredentials(path)
		}
		return nil, fmt.Errorf("claude credentials not found")

	case "codex":
		if strings.HasSuffix(path, "auth.json") {
			return identity.ExtractFromCodexAuth(path)
		}
		return nil, fmt.Errorf("codex auth not found")

	case "gemini":
		if strings.HasSuffix(path, "settings.json") || strings.HasSuffix(path, "oauth_creds.json") {
			return identity.ExtractFromGeminiConfig(path)
		}
		return nil, fmt.Errorf("gemini config not found")

	case "grok":
		if strings.HasSuffix(path, "auth.json") {
			return identity.ExtractFromGrokAuth(path)
		}
		return nil, fmt.Errorf("grok auth not found")

	case "opencode":
		if strings.HasSuffix(path, "auth.json") {
			return identity.ExtractFromGenericAuth(path)
		}
		return nil, fmt.Errorf("opencode auth not found")

	case "cursor":
		if strings.HasSuffix(path, "auth.json") || strings.HasSuffix(path, "settings.json") {
			return identity.ExtractFromGenericAuth(path)
		}
		return nil, fmt.Errorf("cursor auth not found")

	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// WatchOnce performs a one-time scan of current auth files and saves any new accounts.
// This is useful for discovering accounts that were logged in before the watcher started.
func WatchOnce(vault *authfile.Vault, providers []string, logger *slog.Logger) ([]string, error) {
	if vault == nil {
		return nil, fmt.Errorf("watcher requires a vault")
	}
	if len(providers) == 0 {
		providers = []string{"claude", "codex", "gemini", "grok", "opencode", "cursor"}
	}
	if logger == nil {
		logger = slog.Default()
	}

	var discovered []string
	seen := make(map[string]bool)
	for _, provider := range providers {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if seen[provider] {
			continue
		}
		seen[provider] = true
		fileSet, ok := authfile.GetAuthFileSet(provider)
		if !ok {
			continue
		}

		name, _, err := discoverAccount(vault, fileSet)
		if err != nil {
			logger.Warn("auth reconciliation failed", "provider", provider, "error", err)
			continue
		}
		if name != "" {
			logger.Info("saved discovered credentials", "provider", provider, "profile", name)
			discovered = append(discovered, fmt.Sprintf("%s/%s", provider, name))
		}
	}

	return discovered, nil
}

func autoProfileName(vault *authfile.Vault, provider string) string {
	base := "auto-" + time.Now().Format("20060102-150405")
	if vault == nil {
		return base
	}
	profiles, err := vault.List(provider)
	if err != nil || len(profiles) == 0 {
		return base
	}
	exists := make(map[string]struct{}, len(profiles))
	for _, p := range profiles {
		exists[p] = struct{}{}
	}
	if _, ok := exists[base]; !ok {
		return base
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s-%d", base, i)
		if _, ok := exists[candidate]; !ok {
			return candidate
		}
	}
	return base
}
