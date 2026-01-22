// Package agent implements the local auth-agent that completes OAuth flows.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Config configures the auth agent.
type Config struct {
	// Port for HTTP server
	Port int

	// CoordinatorURL is the URL of the remote coordinator.
	CoordinatorURL string

	// PollInterval is how often to poll for pending requests.
	PollInterval time.Duration

	// ChromeUserDataDir is the Chrome profile directory to use.
	// If empty, uses a temporary profile.
	ChromeUserDataDir string

	// Headless controls whether Chrome runs headless.
	// Note: Google OAuth may not work in headless mode.
	Headless bool

	// AccountStrategy determines how to select accounts.
	AccountStrategy AccountStrategy

	// Accounts is the list of account emails to cycle through.
	Accounts []string

	// Logger for structured logging.
	Logger *slog.Logger
}

// AccountStrategy determines how accounts are selected.
type AccountStrategy string

const (
	// StrategyLRU selects the least recently used account.
	StrategyLRU AccountStrategy = "lru"
	// StrategyRoundRobin cycles through accounts in order.
	StrategyRoundRobin AccountStrategy = "round_robin"
	// StrategyRandom selects randomly.
	StrategyRandom AccountStrategy = "random"
)

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Port:            7891,
		CoordinatorURL:  "http://localhost:7890",
		PollInterval:    2 * time.Second,
		Headless:        false, // Google OAuth requires visible browser
		AccountStrategy: StrategyLRU,
	}
}

// AccountUsage tracks when each account was last used.
type AccountUsage struct {
	Email      string    `json:"email"`
	LastUsed   time.Time `json:"last_used"`
	UseCount   int       `json:"use_count"`
	LastResult string    `json:"last_result"` // success, failed
}

// Agent handles OAuth completion for the coordinator.
type Agent struct {
	config        Config
	logger        *slog.Logger
	server        *http.Server
	browser       *Browser
	accountUsage  map[string]*AccountUsage
	usagePath     string
	mu            sync.RWMutex
	stopCh        chan struct{}
	doneCh        chan struct{}
	running       bool

	// Callbacks
	OnAuthStart    func(url, account string)
	OnAuthComplete func(account, code string)
	OnAuthFailed   func(account string, err error)
}

// New creates a new auth agent.
func New(config Config) *Agent {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	// Determine usage storage path
	configDir, _ := os.UserConfigDir()
	usagePath := filepath.Join(configDir, "caam", "account_usage.json")

	agent := &Agent{
		config:       config,
		logger:       config.Logger,
		accountUsage: make(map[string]*AccountUsage),
		usagePath:    usagePath,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}

	// Load existing usage data
	agent.loadUsage()

	return agent
}

// Start begins the agent.
func (a *Agent) Start(ctx context.Context) error {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return fmt.Errorf("agent already running")
	}
	a.running = true
	// Recreate channels for this run (in case of restart after Stop)
	a.stopCh = make(chan struct{})
	a.doneCh = make(chan struct{})
	a.mu.Unlock()

	// Pre-flight check: ensure Chrome is available
	if !IsChromeAvailable() {
		return fmt.Errorf("Chrome/Chromium not found. Install Chrome or run 'caam doctor --auto' for guided installation")
	}

	chromePath := GetChromePath()
	a.logger.Info("using Chrome", "path", chromePath)

	// Initialize browser
	a.browser = NewBrowser(BrowserConfig{
		UserDataDir: a.config.ChromeUserDataDir,
		Headless:    a.config.Headless,
		Logger:      a.logger,
	})

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.HandleFunc("POST /auth", a.handleAuth)
	mux.HandleFunc("GET /accounts", a.handleAccounts)

	a.server = &http.Server{
		Addr:         fmt.Sprintf(":%d", a.config.Port),
		Handler:      a.withLogging(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 120 * time.Second, // Long timeout for OAuth
	}

	// Start polling for requests if coordinator URL is set
	if a.config.CoordinatorURL != "" {
		go a.pollLoop(ctx)
	} else {
		// Close doneCh immediately since pollLoop won't be started to close it
		close(a.doneCh)
	}

	// Start HTTP server
	go func() {
		a.logger.Info("starting agent HTTP server", "port", a.config.Port)
		if err := a.server.ListenAndServe(); err != http.ErrServerClosed {
			a.logger.Error("HTTP server error", "error", err)
		}
	}()

	return nil
}

// Stop halts the agent.
func (a *Agent) Stop(ctx context.Context) error {
	a.mu.Lock()
	if !a.running {
		a.mu.Unlock()
		return nil
	}
	a.running = false
	a.mu.Unlock()

	// Close stopCh only once (safe since we checked running flag under lock)
	select {
	case <-a.stopCh:
		// Already closed
	default:
		close(a.stopCh)
	}

	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			a.logger.Warn("HTTP server shutdown error", "error", err)
		}
	}

	if a.browser != nil {
		a.browser.Close()
	}

	// Wait for pollLoop to finish (or immediately if it wasn't started)
	<-a.doneCh

	// Save usage data
	a.saveUsage()

	return nil
}

// pollLoop polls the coordinator for pending requests.
func (a *Agent) pollLoop(ctx context.Context) {
	defer close(a.doneCh)

	ticker := time.NewTicker(a.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-a.stopCh:
			return
		case <-ticker.C:
			a.checkPendingRequests(ctx)
		}
	}
}

// checkPendingRequests fetches and processes pending auth requests.
func (a *Agent) checkPendingRequests(ctx context.Context) {
	url := a.config.CoordinatorURL + "/auth/pending"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.logger.Debug("failed to reach coordinator", "error", err)
		return
	}
	defer resp.Body.Close()

	var pending []struct {
		ID        string    `json:"id"`
		PaneID    int       `json:"pane_id"`
		URL       string    `json:"url"`
		CreatedAt time.Time `json:"created_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&pending); err != nil {
		a.logger.Debug("failed to decode pending requests", "error", err)
		return
	}

	for _, p := range pending {
		a.processAuthRequest(ctx, p.ID, p.URL)
	}
}

// processAuthRequest handles a single auth request.
func (a *Agent) processAuthRequest(ctx context.Context, requestID, authURL string) {
	a.logger.Info("processing auth request",
		"request_id", requestID,
		"url_prefix", truncate(authURL, 50))

	// Select account
	account := a.selectAccount()
	if a.OnAuthStart != nil {
		a.OnAuthStart(authURL, account)
	}

	// Complete OAuth
	code, usedAccount, err := a.browser.CompleteOAuth(ctx, authURL, account)
	if err != nil {
		a.logger.Error("OAuth failed",
			"request_id", requestID,
			"error", err)
		a.recordUsage(account, "failed")

		if a.OnAuthFailed != nil {
			a.OnAuthFailed(account, err)
		}

		// Send error to coordinator
		a.sendAuthComplete(ctx, requestID, "", "", err.Error())
		return
	}

	a.logger.Info("OAuth completed",
		"request_id", requestID,
		"account", usedAccount)
	a.recordUsage(usedAccount, "success")

	if a.OnAuthComplete != nil {
		a.OnAuthComplete(usedAccount, code)
	}

	// Send success to coordinator
	a.sendAuthComplete(ctx, requestID, code, usedAccount, "")
}

// sendAuthComplete sends the auth result to the coordinator.
func (a *Agent) sendAuthComplete(ctx context.Context, requestID, code, account, errMsg string) {
	url := a.config.CoordinatorURL + "/auth/complete"

	body := map[string]string{
		"request_id": requestID,
		"code":       code,
		"account":    account,
	}
	if errMsg != "" {
		body["error"] = errMsg
	}

	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url,
		jsonReader(bodyJSON))
	if err != nil {
		a.logger.Error("failed to create request", "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.logger.Error("failed to send auth complete", "error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.logger.Warn("coordinator returned error", "status", resp.StatusCode)
	}
}

// selectAccount chooses which account to use based on strategy.
func (a *Agent) selectAccount() string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	accounts := a.config.Accounts
	if len(accounts) == 0 {
		return "" // Will use whatever account is currently logged in
	}

	switch a.config.AccountStrategy {
	case StrategyLRU:
		return a.selectLRU(accounts)
	case StrategyRoundRobin:
		return a.selectRoundRobin(accounts)
	default:
		return accounts[0]
	}
}

func (a *Agent) selectLRU(accounts []string) string {
	var oldest string
	var oldestTime time.Time

	for _, acc := range accounts {
		usage, ok := a.accountUsage[acc]
		if !ok {
			// Never used - perfect candidate
			return acc
		}
		if oldest == "" || usage.LastUsed.Before(oldestTime) {
			oldest = acc
			oldestTime = usage.LastUsed
		}
	}

	return oldest
}

func (a *Agent) selectRoundRobin(accounts []string) string {
	// Find most recently used, return next in list
	var mostRecent string
	var mostRecentTime time.Time

	for _, acc := range accounts {
		usage, ok := a.accountUsage[acc]
		if ok && usage.LastUsed.After(mostRecentTime) {
			mostRecent = acc
			mostRecentTime = usage.LastUsed
		}
	}

	if mostRecent == "" {
		return accounts[0]
	}

	for i, acc := range accounts {
		if acc == mostRecent {
			return accounts[(i+1)%len(accounts)]
		}
	}

	return accounts[0]
}

func (a *Agent) recordUsage(email, result string) {
	if email == "" {
		return
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	usage, ok := a.accountUsage[email]
	if !ok {
		usage = &AccountUsage{Email: email}
		a.accountUsage[email] = usage
	}

	usage.LastUsed = time.Now()
	usage.UseCount++
	usage.LastResult = result

	// Save asynchronously
	go a.saveUsage()
}

func (a *Agent) loadUsage() {
	data, err := os.ReadFile(a.usagePath)
	if err != nil {
		return // File doesn't exist yet
	}

	var usages []*AccountUsage
	if err := json.Unmarshal(data, &usages); err != nil {
		a.logger.Warn("failed to parse usage file", "error", err)
		return
	}

	for _, u := range usages {
		a.accountUsage[u.Email] = u
	}
}

func (a *Agent) saveUsage() {
	a.mu.RLock()
	usages := make([]*AccountUsage, 0, len(a.accountUsage))
	for _, u := range a.accountUsage {
		usages = append(usages, u)
	}
	a.mu.RUnlock()

	data, err := json.MarshalIndent(usages, "", "  ")
	if err != nil {
		return
	}

	dir := filepath.Dir(a.usagePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		a.logger.Warn("failed to create usage dir", "error", err)
		return
	}

	// Atomic write: temp file + fsync + rename
	tmpFile, err := os.CreateTemp(dir, "account_usage.*.tmp")
	if err != nil {
		a.logger.Warn("failed to create temp usage file", "error", err)
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath) // Clean up on error; no-op after successful rename

	if _, err := tmpFile.Write(data); err != nil {
		tmpFile.Close()
		return
	}

	if err := tmpFile.Sync(); err != nil {
		tmpFile.Close()
		return
	}

	if err := tmpFile.Close(); err != nil {
		return
	}

	if err := os.Rename(tmpPath, a.usagePath); err != nil {
		a.logger.Warn("failed to rename usage file", "error", err)
	}
}

func (a *Agent) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		a.logger.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"duration", time.Since(start))
	})
}

// HTTP Handlers

func (a *Agent) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	accountCount := len(a.accountUsage)
	a.mu.RUnlock()

	status := map[string]interface{}{
		"running":       a.running,
		"coordinator":   a.config.CoordinatorURL,
		"account_count": accountCount,
		"strategy":      a.config.AccountStrategy,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *Agent) handleAccounts(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	usages := make([]*AccountUsage, 0, len(a.accountUsage))
	for _, u := range a.accountUsage {
		usages = append(usages, u)
	}
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usages)
}

// AuthRequest is the request body for manual auth.
type AuthRequest struct {
	URL     string `json:"url"`
	Account string `json:"account,omitempty"`
}

// AuthResult is the response from auth endpoint.
type AuthResult struct {
	Code    string `json:"code,omitempty"`
	Account string `json:"account,omitempty"`
	Error   string `json:"error,omitempty"`
}

func (a *Agent) handleAuth(w http.ResponseWriter, r *http.Request) {
	var req AuthRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}

	if req.URL == "" {
		http.Error(w, "url required", http.StatusBadRequest)
		return
	}

	account := req.Account
	if account == "" {
		account = a.selectAccount()
	}

	code, usedAccount, err := a.browser.CompleteOAuth(r.Context(), req.URL, account)
	if err != nil {
		a.recordUsage(account, "failed")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(AuthResult{Error: err.Error()})
		return
	}

	a.recordUsage(usedAccount, "success")
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(AuthResult{
		Code:    code,
		Account: usedAccount,
	})
}

// Helpers

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

type jsonReaderWrapper struct {
	data []byte
	pos  int
}

func jsonReader(data []byte) *jsonReaderWrapper {
	return &jsonReaderWrapper{data: data}
}

func (r *jsonReaderWrapper) Read(p []byte) (n int, err error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n = copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
