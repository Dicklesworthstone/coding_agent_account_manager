// Package discovery provides automatic detection of auth file changes
// and auto-discovery of new accounts.
package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
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
	vault    *authfile.Vault
	config   WatcherConfig
	watcher  *fsnotify.Watcher
	logger   *slog.Logger
	mu       sync.Mutex
	pending  map[string]time.Time // path -> last change time
	stopCh   chan struct{}
	doneCh   chan struct{}
	watching bool
}

// NewWatcher creates a new auth file watcher.
func NewWatcher(vault *authfile.Vault, config WatcherConfig) (*Watcher, error) {
	if config.DebounceInterval == 0 {
		config.DebounceInterval = 500 * time.Millisecond
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if len(config.Providers) == 0 {
		config.Providers = []string{"claude", "codex", "gemini"}
	}

	fsWatcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	return &Watcher{
		vault:   vault,
		config:  config,
		watcher: fsWatcher,
		logger:  config.Logger,
		pending: make(map[string]time.Time),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}, nil
}

// Start begins watching auth files for changes.
// Call Stop() to stop the watcher.
func (w *Watcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.watching {
		w.mu.Unlock()
		return fmt.Errorf("watcher already running")
	}
	w.watching = true
	// Recreate channels for this run (in case of restart after Stop)
	w.stopCh = make(chan struct{})
	w.doneCh = make(chan struct{})
	w.mu.Unlock()

	// Add watches for all auth file directories
	pathsToWatch := w.getWatchPaths()
	for _, p := range pathsToWatch {
		dir := filepath.Dir(p)
		// Ensure directory exists
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			if err := os.MkdirAll(dir, 0700); err != nil {
				w.logger.Warn("failed to create directory for watching",
					"dir", dir, "error", err)
				continue
			}
		}
		if err := w.watcher.Add(dir); err != nil {
			w.logger.Warn("failed to add watch",
				"path", dir, "error", err)
		} else {
			w.logger.Debug("watching directory", "path", dir)
		}
	}

	// Start event loop
	go w.eventLoop(ctx)
	go w.debounceLoop(ctx)

	return nil
}

// Stop halts the watcher.
func (w *Watcher) Stop() error {
	w.mu.Lock()
	if !w.watching {
		w.mu.Unlock()
		return nil
	}
	w.watching = false
	stopCh := w.stopCh
	doneCh := w.doneCh
	w.mu.Unlock()

	select {
	case <-stopCh:
	default:
		close(stopCh)
	}
	<-doneCh
	return w.watcher.Close()
}

// getWatchPaths returns all auth file paths to monitor.
func (w *Watcher) getWatchPaths() []string {
	var paths []string

	for _, provider := range w.config.Providers {
		fileSet, ok := authfile.GetAuthFileSet(provider)
		if !ok {
			continue
		}
		for _, spec := range fileSet.Files {
			paths = append(paths, spec.Path)
		}
	}

	return paths
}

// eventLoop handles fsnotify events.
func (w *Watcher) eventLoop(ctx context.Context) {
	watchedPaths := make(map[string]string) // path -> provider
	for _, provider := range w.config.Providers {
		fileSet, ok := authfile.GetAuthFileSet(provider)
		if !ok {
			continue
		}
		for _, spec := range fileSet.Files {
			watchedPaths[spec.Path] = provider
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			close(w.doneCh)
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// Check if this is a file we care about
			provider, isWatched := watchedPaths[event.Name]
			if !isWatched {
				// Also check by base name (for files in watched directories)
				for watchPath, prov := range watchedPaths {
					if filepath.Base(event.Name) == filepath.Base(watchPath) &&
						filepath.Dir(event.Name) == filepath.Dir(watchPath) {
						provider = prov
						isWatched = true
						break
					}
				}
			}
			if !isWatched {
				continue
			}

			// Handle create/write events
			if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				w.mu.Lock()
				w.pending[event.Name] = time.Now()
				w.mu.Unlock()

				if w.config.OnChange != nil {
					w.config.OnChange(provider, event.Name)
				}
				w.logger.Debug("auth file changed",
					"provider", provider,
					"path", event.Name,
					"op", event.Op.String())
			}

		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			w.logger.Error("fsnotify error", "error", err)
			if w.config.OnError != nil {
				w.config.OnError(err)
			}
		}
	}
}

// debounceLoop processes pending changes after debounce interval.
func (w *Watcher) debounceLoop(ctx context.Context) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-ticker.C:
			w.processPending()
		}
	}
}

// processPending handles debounced file changes.
func (w *Watcher) processPending() {
	w.mu.Lock()
	now := time.Now()
	var toProcess []string
	for path, lastChange := range w.pending {
		if now.Sub(lastChange) >= w.config.DebounceInterval {
			toProcess = append(toProcess, path)
			delete(w.pending, path)
		}
	}
	w.mu.Unlock()

	for _, path := range toProcess {
		w.processChange(path)
	}
}

// processChange handles a single auth file change.
func (w *Watcher) processChange(path string) {
	// Determine provider from path
	var provider string
	var fileSet authfile.AuthFileSet
	for _, prov := range w.config.Providers {
		fs, ok := authfile.GetAuthFileSet(prov)
		if !ok {
			continue
		}
		for _, spec := range fs.Files {
			if spec.Path == path || filepath.Base(spec.Path) == filepath.Base(path) {
				provider = prov
				fileSet = fs
				break
			}
		}
		if provider != "" {
			break
		}
	}

	if provider == "" {
		w.logger.Warn("could not determine provider for path", "path", path)
		return
	}

	// Extract identity from the auth file
	ident, err := w.extractIdentity(provider, path)
	if err != nil {
		w.logger.Debug("failed to extract identity; falling back to auto profile",
			"provider", provider,
			"path", path,
			"error", err)
		ident = nil
	}

	email := ""
	if ident != nil {
		email = strings.TrimSpace(ident.Email)
	}

	if email == "" {
		if !authfile.HasAuthFiles(fileSet) {
			w.logger.Debug("no auth files found; skipping auto backup",
				"provider", provider,
				"path", path)
			return
		}

		// If this auth state already matches a saved profile, skip.
		if active, err := w.vault.ActiveProfile(fileSet); err == nil && active != "" {
			w.logger.Debug("auth matches existing profile; identity missing",
				"provider", provider,
				"profile", active)
			return
		}

		// No identity available; still back up with an auto-generated name.
		autoName := w.autoProfileName(provider)
		w.logger.Info("identity missing; backing up with auto profile name",
			"provider", provider,
			"profile", autoName)
		if err := w.vault.Backup(fileSet, autoName); err != nil {
			w.logger.Error("failed to backup auto profile",
				"provider", provider,
				"profile", autoName,
				"error", err)
			if w.config.OnError != nil {
				w.config.OnError(fmt.Errorf("backup %s/%s: %w", provider, autoName, err))
			}
			return
		}
		if w.config.OnDiscovery != nil {
			w.config.OnDiscovery(provider, autoName, ident)
		}
		return
	}

	// Check if this profile already exists in vault
	email = strings.TrimSpace(email)
	profiles, err := w.vault.List(provider)
	if err != nil {
		w.logger.Error("failed to list profiles",
			"provider", provider,
			"error", err)
		return
	}

	for _, p := range profiles {
		if p == email {
			// Profile already exists, check if content matches
			active, err := w.vault.ActiveProfile(fileSet)
			if err == nil && active == email {
				w.logger.Debug("profile already active, skipping",
					"provider", provider,
					"email", email)
				return
			}
			// Content differs - update existing profile
			w.logger.Info("updating existing profile",
				"provider", provider,
				"email", email)
			if err := w.vault.Backup(fileSet, email); err != nil {
				w.logger.Error("failed to update profile",
					"provider", provider,
					"email", email,
					"error", err)
				return
			}
			if w.config.OnDiscovery != nil {
				w.config.OnDiscovery(provider, email, ident)
			}
			return
		}
	}

	// New profile - backup it
	w.logger.Info("discovered new account",
		"provider", provider,
		"email", email,
		"plan", ident.PlanType)

	if err := w.vault.Backup(fileSet, email); err != nil {
		w.logger.Error("failed to backup new profile",
			"provider", provider,
			"email", email,
			"error", err)
		if w.config.OnError != nil {
			w.config.OnError(fmt.Errorf("backup %s/%s: %w", provider, email, err))
		}
		return
	}

	if w.config.OnDiscovery != nil {
		w.config.OnDiscovery(provider, email, ident)
	}
}

// autoProfileName generates a unique, user-deletable profile name when identity is missing.
func (w *Watcher) autoProfileName(provider string) string {
	base := "auto-" + time.Now().Format("20060102-150405")
	if w == nil || w.vault == nil {
		return base
	}
	profiles, err := w.vault.List(provider)
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

// extractIdentity extracts account identity from an auth file.
func (w *Watcher) extractIdentity(provider, path string) (*identity.Identity, error) {
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
		// Also check the primary location if this isn't it
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("get home dir: %w", err)
		}
		credPath := filepath.Join(homeDir, ".claude", ".credentials.json")
		if _, err := os.Stat(credPath); err == nil {
			return identity.ExtractFromClaudeCredentials(credPath)
		}
		return nil, fmt.Errorf("claude credentials not found")

	case "codex":
		if strings.HasSuffix(path, "auth.json") {
			return identity.ExtractFromCodexAuth(path)
		}
		// Check default location
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("get home dir: %w", err)
			}
			codexHome = filepath.Join(homeDir, ".codex")
		}
		authPath := filepath.Join(codexHome, "auth.json")
		if _, err := os.Stat(authPath); err == nil {
			return identity.ExtractFromCodexAuth(authPath)
		}
		return nil, fmt.Errorf("codex auth not found")

	case "gemini":
		if strings.HasSuffix(path, "settings.json") || strings.HasSuffix(path, "oauth_credentials.json") {
			return identity.ExtractFromGeminiConfig(path)
		}
		// Check default location
		geminiHome := os.Getenv("GEMINI_HOME")
		if geminiHome == "" {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return nil, fmt.Errorf("get home dir: %w", err)
			}
			geminiHome = filepath.Join(homeDir, ".gemini")
		}
		settingsPath := filepath.Join(geminiHome, "settings.json")
		if _, err := os.Stat(settingsPath); err == nil {
			return identity.ExtractFromGeminiConfig(settingsPath)
		}
		return nil, fmt.Errorf("gemini config not found")

	default:
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}
}

// WatchOnce performs a one-time scan of current auth files and saves any new accounts.
// This is useful for discovering accounts that were logged in before the watcher started.
func WatchOnce(vault *authfile.Vault, providers []string, logger *slog.Logger) ([]string, error) {
	if len(providers) == 0 {
		providers = []string{"claude", "codex", "gemini"}
	}
	if logger == nil {
		logger = slog.Default()
	}

	var discovered []string

	for _, provider := range providers {
		fileSet, ok := authfile.GetAuthFileSet(provider)
		if !ok {
			continue
		}

		// Check if any auth files exist
		if !authfile.HasAuthFiles(fileSet) {
			logger.Debug("no auth files for provider", "provider", provider)
			continue
		}

		// Extract identity
		var ident *identity.Identity
		var err error

		switch provider {
		case "claude":
			homeDir, homeErr := os.UserHomeDir()
			if homeErr != nil {
				logger.Debug("failed to get home dir",
					"provider", provider,
					"error", homeErr)
				continue
			}
			credPath := filepath.Join(homeDir, ".claude", ".credentials.json")
			ident, err = identity.ExtractFromClaudeCredentials(credPath)
		case "codex":
			codexHome := os.Getenv("CODEX_HOME")
			if codexHome == "" {
				homeDir, homeErr := os.UserHomeDir()
				if homeErr != nil {
					logger.Debug("failed to get home dir",
						"provider", provider,
						"error", homeErr)
					continue
				}
				codexHome = filepath.Join(homeDir, ".codex")
			}
			authPath := filepath.Join(codexHome, "auth.json")
			ident, err = identity.ExtractFromCodexAuth(authPath)
		case "gemini":
			geminiHome := os.Getenv("GEMINI_HOME")
			if geminiHome == "" {
				homeDir, homeErr := os.UserHomeDir()
				if homeErr != nil {
					logger.Debug("failed to get home dir",
						"provider", provider,
						"error", homeErr)
					continue
				}
				geminiHome = filepath.Join(homeDir, ".gemini")
			}
			settingsPath := filepath.Join(geminiHome, "settings.json")
			ident, err = identity.ExtractFromGeminiConfig(settingsPath)
		}

		if err != nil {
			logger.Debug("failed to extract identity; falling back to auto profile",
				"provider", provider,
				"error", err)
			ident = nil
		}

		if ident == nil || ident.Email == "" {
			// If current auth already matches a saved profile, skip.
			if active, err := vault.ActiveProfile(fileSet); err == nil && active != "" {
				logger.Debug("auth matches existing profile; identity missing",
					"provider", provider,
					"profile", active)
				continue
			}

			// No identity available; still back up with an auto-generated name.
			autoName := autoProfileName(vault, provider)
			logger.Info("identity missing; backing up with auto profile name",
				"provider", provider,
				"profile", autoName)
			if err := vault.Backup(fileSet, autoName); err != nil {
				logger.Error("failed to backup auto profile",
					"provider", provider,
					"profile", autoName,
					"error", err)
				continue
			}
			discovered = append(discovered, fmt.Sprintf("%s/%s", provider, autoName))
			continue
		}

		email := ident.Email

		// Check if already in vault
		profiles, _ := vault.List(provider)
		alreadyExists := false
		for _, p := range profiles {
			if p == email {
				alreadyExists = true
				break
			}
		}

		if alreadyExists {
			// Check if content matches
			active, err := vault.ActiveProfile(fileSet)
			if err == nil && active == email {
				logger.Debug("profile already exists and matches",
					"provider", provider,
					"email", email)
				continue
			}
			// Update existing
			logger.Info("updating existing profile",
				"provider", provider,
				"email", email)
		} else {
			logger.Info("discovered new account",
				"provider", provider,
				"email", email,
				"plan", ident.PlanType)
		}

		if err := vault.Backup(fileSet, email); err != nil {
			logger.Error("failed to backup profile",
				"provider", provider,
				"email", email,
				"error", err)
			continue
		}

		discovered = append(discovered, fmt.Sprintf("%s/%s", provider, email))
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
