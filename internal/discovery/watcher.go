// Package discovery provides automatic detection of auth file changes
// and auto-discovery of new accounts.
package discovery

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
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

	// PollInterval reconciles credential contents even when filesystem events
	// are unavailable or lost. It also controls retries after a failed capture
	// and attempts to restore unavailable filesystem notifications.
	// Default: 2s
	PollInterval time.Duration

	// WatchOptionalFiles includes settings that do not carry credentials.
	// Credential alternatives and identity files are always monitored.
	WatchOptionalFiles bool

	// OnDiscovery is called when a new account is discovered.
	// The callback receives the provider, email, and identity details.
	// Callbacks run serially; cancel the context to stop from a callback.
	OnDiscovery func(provider, email string, ident *identity.Identity)

	// OnChange is called once per changed path after its provider's changes
	// have settled, even when the credential already matches a saved account.
	OnChange func(provider, path string)

	// OnError is called when an error occurs during watching or processing.
	OnError func(err error)

	// Logger for structured logging.
	Logger *slog.Logger
}

// Watcher monitors auth file changes and auto-discovers new accounts.
type Watcher struct {
	vault          *authfile.Vault
	config         WatcherConfig
	logger         *slog.Logger
	fileSets       []authfile.AuthFileSet
	paths          map[string][]string // full path -> canonical provider IDs
	newEventSource func() (watchEventSource, error)
	mu             sync.Mutex
	run            *watchRun
}

// Each run owns its notifier and mutable state. A new run never shares events,
// pending captures, or completion channels with the run that preceded it.
type watchRun struct {
	cancel   context.CancelFunc
	done     chan struct{}
	closeErr error
	source   watchEventSource
	dirs     map[string]os.FileInfo
	seen     map[string]watchFingerprint
	pending  map[string]*pendingDiscovery
	failures map[string]string
}

type pendingDiscovery struct {
	changedAt time.Time
	retryAt   time.Time
	paths     map[string]struct{}
	lastError string
}

type watchFingerprint struct {
	digest [sha256.Size]byte
	state  string
}

// An instance-local factory lets tests exercise unavailable notifiers and lost
// event streams without changing process-global state.
type watchEventSource interface {
	Add(string) error
	Remove(string) error
	Close() error
	EventsChan() <-chan fsnotify.Event
	ErrorsChan() <-chan error
}

type fsnotifySource struct {
	*fsnotify.Watcher
}

func (s *fsnotifySource) EventsChan() <-chan fsnotify.Event { return s.Events }
func (s *fsnotifySource) ErrorsChan() <-chan error          { return s.Errors }

func newFSNotifySource() (watchEventSource, error) {
	source, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &fsnotifySource{source}, nil
}

// NewWatcher creates a new auth file watcher without allocating OS resources.
func NewWatcher(vault *authfile.Vault, config WatcherConfig) (*Watcher, error) {
	if vault == nil {
		return nil, fmt.Errorf("discovery watcher requires a vault")
	}
	if config.DebounceInterval < 0 || config.PollInterval < 0 {
		return nil, fmt.Errorf("watch intervals must be positive")
	}
	if config.DebounceInterval == 0 {
		config.DebounceInterval = 500 * time.Millisecond
	}
	if config.PollInterval == 0 {
		config.PollInterval = 2 * time.Second
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if len(config.Providers) == 0 {
		config.Providers = []string{"claude", "codex", "gemini", "agy", "grok", "opencode", "cursor"}
	}

	w := &Watcher{
		vault:          vault,
		config:         config,
		logger:         config.Logger,
		paths:          make(map[string][]string),
		newEventSource: newFSNotifySource,
	}
	w.config.Providers = nil
	seenProviders := make(map[string]bool)
	for _, provider := range config.Providers {
		fileSet, ok := authfile.GetAuthFileSet(strings.TrimSpace(provider))
		if !ok {
			return nil, fmt.Errorf("unknown discovery provider %q", provider)
		}
		if seenProviders[fileSet.Tool] {
			continue
		}
		seenProviders[fileSet.Tool] = true
		w.config.Providers = append(w.config.Providers, fileSet.Tool)
		for i := range fileSet.Files {
			spec := &fileSet.Files[i]
			path, err := filepath.Abs(spec.Path)
			if err != nil {
				return nil, fmt.Errorf("resolve %s auth path: %w", fileSet.Tool, err)
			}
			spec.Path = filepath.Clean(path)
			if config.WatchOptionalFiles || discoveryCredentialFile(fileSet.Tool, *spec) {
				w.paths[spec.Path] = append(w.paths[spec.Path], fileSet.Tool)
			}
		}
		w.fileSets = append(w.fileSets, fileSet)
	}
	return w, nil
}

// Start begins watching auth files for changes.
// Existing credentials are reconciled as well, closing the gap between an
// initial scan and registering filesystem watches. No native directory is
// created merely to watch it. Call Stop() to wait for all work to finish.
func (w *Watcher) Start(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("discovery watcher requires a context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.run != nil {
		select {
		case <-w.run.done:
		default:
			return fmt.Errorf("watcher already running")
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	run := &watchRun{
		cancel:   cancel,
		done:     make(chan struct{}),
		dirs:     make(map[string]os.FileInfo),
		seen:     make(map[string]watchFingerprint),
		pending:  make(map[string]*pendingDiscovery),
		failures: make(map[string]string),
	}
	w.run = run
	go w.eventLoop(runCtx, run)
	return nil
}

// Stop cancels the current run and waits for its callbacks and vault writes.
// Concurrent callers wait on the same completion barrier.
func (w *Watcher) Stop() error {
	w.mu.Lock()
	run := w.run
	if run != nil {
		run.cancel()
	}
	w.mu.Unlock()
	if run == nil {
		return nil
	}
	<-run.done
	return run.closeErr
}

// Done closes after the current run has released its resources. Before Start,
// it returns an already closed channel.
func (w *Watcher) Done() <-chan struct{} {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.run != nil {
		return w.run.done
	}
	done := make(chan struct{})
	close(done)
	return done
}

func discoveryCredentialFile(provider string, spec authfile.AuthFileSpec) bool {
	if spec.Required {
		return true
	}
	switch provider {
	case "claude":
		// Claude's settings and Desktop cache can contain credentials or the
		// paired OAuth account identity. Capture compares credential contents
		// before saving, so unrelated settings edits cannot create profiles.
		return true
	case "gemini":
		return filepath.Base(spec.Path) == "oauth_creds.json" || filepath.Base(spec.Path) == ".env"
	case "agy":
		return filepath.Base(spec.Path) == "google_accounts.json" || filepath.Base(spec.Path) == "oauth_creds.json"
	case "cursor":
		return filepath.Base(spec.Path) == "auth.json" || filepath.Base(spec.Path) == "cli-config.json"
	default:
		return false
	}
}

// eventLoop serializes event delivery, polling, debounce, and captures. Polling
// remains active even with a working notifier: atomic directory replacement,
// lost events, and an unavailable notifier all converge through the same path.
func (w *Watcher) eventLoop(ctx context.Context, run *watchRun) {
	defer close(run.done)
	defer run.cancel()
	defer run.closeSource()
	if ctx.Err() != nil {
		return
	}
	w.openSource(run)
	w.reconcile(run, time.Now())
	poll := time.NewTicker(w.config.PollInterval)
	defer poll.Stop()
	debounce := time.NewTicker(min(w.config.DebounceInterval, 100*time.Millisecond))
	defer debounce.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		var events <-chan fsnotify.Event
		var eventErrors <-chan error
		if run.source != nil {
			events = run.source.EventsChan()
			eventErrors = run.source.ErrorsChan()
		}
		select {
		case <-ctx.Done():
			return
		case event, ok := <-events:
			if !ok {
				run.closeSource()
				w.reportWatchError(fmt.Errorf("filesystem event stream closed; continuing with polling"))
				w.reconcile(run, time.Now())
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Remove|fsnotify.Rename|fsnotify.Chmod) == 0 {
				continue
			}
			path := filepath.Clean(event.Name)
			if _, ok := w.paths[path]; ok {
				w.observePath(run, path, time.Now())
			}
			if event.Op&(fsnotify.Create|fsnotify.Remove|fsnotify.Rename) != 0 && w.affectsWatchDirectory(run, path) {
				w.refreshWatches(run)
				// A replacement directory may already contain credentials by
				// the time its notification watch is attached.
				w.reconcile(run, time.Now())
			}
		case err, ok := <-eventErrors:
			if ok {
				w.reportWatchError(fmt.Errorf("filesystem event error; reconciling credentials: %w", err))
			} else {
				w.reportWatchError(fmt.Errorf("filesystem error stream closed; continuing with polling"))
			}
			run.closeSource()
			w.reconcile(run, time.Now())
		case <-poll.C:
			if run.source == nil {
				w.openSource(run)
			} else {
				w.refreshWatches(run)
			}
			now := time.Now()
			w.reconcile(run, now)
			w.processPending(ctx, run, now)
		case <-debounce.C:
			w.processPending(ctx, run, time.Now())
		}
	}
}

func (run *watchRun) closeSource() {
	if run.source != nil {
		run.closeErr = errors.Join(run.closeErr, run.source.Close())
		run.source = nil
		clear(run.dirs)
	}
}

func (w *Watcher) openSource(run *watchRun) {
	source, err := w.newEventSource()
	if err == nil && source == nil {
		err = fmt.Errorf("filesystem notifier returned no event source")
	}
	if err != nil {
		if source != nil {
			run.closeErr = errors.Join(run.closeErr, source.Close())
		}
		if run.failures["notifier"] != err.Error() {
			run.failures["notifier"] = err.Error()
			w.reportWatchError(fmt.Errorf("filesystem events unavailable; continuing with polling: %w", err))
		}
		return
	}
	delete(run.failures, "notifier")
	run.source = source
	w.refreshWatches(run)
}

func (w *Watcher) affectsWatchDirectory(run *watchRun, path string) bool {
	if _, ok := run.dirs[path]; ok {
		return true
	}
	for watched := range w.paths {
		dir := filepath.Dir(watched)
		if dir == path || strings.HasPrefix(dir, path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func (w *Watcher) refreshWatches(run *watchRun) {
	if run.source == nil {
		return
	}
	wanted := make(map[string]os.FileInfo)
	for path := range w.paths {
		// Watch the nearest existing ancestor while the native CLI has not
		// yet created its configuration directory. No directories are made.
		for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
			info, err := os.Stat(dir)
			if err == nil && info.IsDir() {
				wanted[dir] = info
				break
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	for dir, previous := range run.dirs {
		if info, ok := wanted[dir]; !ok || !os.SameFile(previous, info) {
			_ = run.source.Remove(dir)
			delete(run.dirs, dir)
		}
	}
	for dir, info := range wanted {
		if _, watching := run.dirs[dir]; watching {
			continue
		}
		if err := run.source.Add(dir); err != nil {
			if run.failures[dir] != err.Error() {
				run.failures[dir] = err.Error()
				w.reportWatchError(fmt.Errorf("watch directory %s; using polling: %w", dir, err))
			}
			continue
		}
		delete(run.failures, dir)
		run.dirs[dir] = info
		w.logger.Debug("watching auth directory", "path", dir)
	}
}

func (w *Watcher) reconcile(run *watchRun, now time.Time) {
	for path := range w.paths {
		w.observePath(run, path, now)
	}
	// The login keychain can change without touching any mirrored auth file.
	if keychain.Enabled() {
		for _, fileSet := range w.fileSets {
			if fileSet.Tool == "claude" && run.pending[fileSet.Tool] == nil {
				run.pending[fileSet.Tool] = &pendingDiscovery{changedAt: now}
			}
		}
	}
}

func (w *Watcher) observePath(run *watchRun, path string, now time.Time) {
	current := w.fingerprintWatchFile(path)
	previous, observed := run.seen[path]
	run.seen[path] = current
	if current == previous || (!observed && current.state == "missing") {
		return
	}
	for _, provider := range w.paths[path] {
		pending := run.pending[provider]
		if pending == nil {
			pending = &pendingDiscovery{}
			run.pending[provider] = pending
		}
		pending.changedAt = now
		pending.retryAt = time.Time{}
		pending.lastError = ""
		if pending.paths == nil {
			pending.paths = make(map[string]struct{})
		}
		pending.paths[path] = struct{}{}
	}
}

func (w *Watcher) fingerprintWatchFile(path string) watchFingerprint {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return watchFingerprint{state: "missing"}
	}
	if err != nil {
		return watchFingerprint{state: err.Error()}
	}
	if !info.Mode().IsRegular() {
		return watchFingerprint{state: "not a regular file"}
	}
	// Use the capture reader's bound so polling and one-shot discovery have
	// the same behavior for unexpectedly large native credential files.
	if info.Size() > authfile.MaxDiscoveryFileBytes {
		return watchFingerprint{state: "auth file exceeds watch size limit"}
	}
	file, err := os.Open(path)
	if err != nil {
		return watchFingerprint{state: err.Error()}
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, authfile.MaxDiscoveryFileBytes+1))
	if err != nil {
		return watchFingerprint{state: err.Error()}
	}
	if int64(len(data)) > authfile.MaxDiscoveryFileBytes {
		return watchFingerprint{state: "auth file exceeds watch size limit"}
	}
	if w.config.WatchOptionalFiles {
		return watchFingerprint{digest: sha256.Sum256(data)}
	}
	// Project mixed settings onto the same credential fields capture uses.
	// Continuous UI/history writes cannot keep postponing a real login.
	for _, provider := range w.paths[path] {
		if hash := authfile.DiscoveryFileFingerprint(provider, path, data); hash != "" {
			return watchFingerprint{state: "credential:" + hash}
		}
	}
	return watchFingerprint{state: "missing"}
}

func (w *Watcher) reportWatchError(err error) {
	w.logger.Warn("auth discovery watcher error", "error", err)
	if w.config.OnError != nil {
		w.config.OnError(err)
	}
}

// processPending processes an entire frozen provider fileset once its changes
// settle. Failed captures remain pending: an unchanged credential must still
// be saved after a transient vault failure is repaired.
func (w *Watcher) processPending(ctx context.Context, run *watchRun, now time.Time) {
	for _, fileSet := range w.fileSets {
		if ctx.Err() != nil {
			return
		}
		pending := run.pending[fileSet.Tool]
		if pending == nil || now.Sub(pending.changedAt) < w.config.DebounceInterval || now.Before(pending.retryAt) {
			continue
		}
		// Event-time repair may have watched a parent just before the native
		// replacement directory appeared. Rebind before callbacks announce a
		// captured login so its next credential write is observed immediately.
		w.refreshWatches(run)
		paths := make([]string, 0, len(pending.paths))
		for path := range pending.paths {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			if ctx.Err() != nil {
				return
			}
			w.logger.Debug("auth file changed", "provider", fileSet.Tool, "path", path)
			if w.config.OnChange != nil {
				w.config.OnChange(fileSet.Tool, path)
			}
		}
		pending.paths = nil
		if ctx.Err() != nil {
			return
		}
		if err := w.processProvider(fileSet); err != nil {
			pending.retryAt = time.Now().Add(w.config.PollInterval)
			if pending.lastError != err.Error() {
				pending.lastError = err.Error()
				w.reportWatchError(err)
			}
			continue
		}
		delete(run.pending, fileSet.Tool)
	}
}

// processProvider captures one complete provider state. The same operation is
// used by initial scans and the continuous watcher so both paths apply the
// same credential validation, ownership, and freshness rules.
func (w *Watcher) processProvider(fileSet authfile.AuthFileSet) error {
	result, err := w.vault.CaptureDiscovery(fileSet)
	if errors.Is(err, authfile.ErrNoCredentials) {
		w.logger.Debug("no credentials to discover", "provider", fileSet.Tool)
		return nil
	}
	if err != nil {
		return fmt.Errorf("capture %s credentials: %w", fileSet.Tool, err)
	}
	if result.Unchanged || result.KeptNewer {
		w.logger.Debug("saved credentials already current",
			"provider", fileSet.Tool,
			"profile", result.Profile,
			"kept_newer", result.KeptNewer)
		return nil
	}
	w.logger.Info("captured discovered credentials",
		"provider", fileSet.Tool,
		"profile", result.Profile,
		"created", result.Created,
		"updated", result.Updated)
	if w.config.OnDiscovery != nil {
		w.config.OnDiscovery(fileSet.Tool, result.Profile, result.Identity)
	}
	return nil
}

// WatchOnce performs a one-time scan of current auth files and saves any new accounts.
// This is useful for discovering accounts that were logged in before the watcher started.
// Successful discoveries are returned even when another provider fails. Missing
// credentials are normal; malformed credentials and failed vault writes are errors.
func WatchOnce(vault *authfile.Vault, providers []string, logger *slog.Logger) ([]string, error) {
	var discovered []string
	w, err := NewWatcher(vault, WatcherConfig{
		Providers: providers,
		Logger:    logger,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discovered = append(discovered, fmt.Sprintf("%s/%s", provider, profile))
		},
	})
	if err != nil {
		return nil, err
	}
	var scanErrors []error
	for _, fileSet := range w.fileSets {
		if err := w.processProvider(fileSet); err != nil {
			w.logger.Error("credential discovery failed", "provider", fileSet.Tool, "error", err)
			scanErrors = append(scanErrors, err)
		}
	}
	return discovered, errors.Join(scanErrors...)
}
