package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CoordinatorEndpoint represents a remote coordinator to poll.
type CoordinatorEndpoint struct {
	Name        string    `json:"name"`         // Short name: "csd", "css", "trj"
	URL         string    `json:"url"`          // Base URL: http://100.x.x.x:7890
	DisplayName string    `json:"display_name"` // Human-friendly name
	Token       string    `json:"token,omitempty"`
	LastCheck   time.Time `json:"-"`
	IsHealthy   bool      `json:"-"`
	LastError   string    `json:"-"`
	mu          sync.RWMutex
}

// SetHealth updates the health status thread-safely.
func (c *CoordinatorEndpoint) SetHealth(healthy bool, err string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.IsHealthy = healthy
	c.LastError = err
	c.LastCheck = time.Now()
}

// GetHealth returns the health status thread-safely.
func (c *CoordinatorEndpoint) GetHealth() (bool, string, time.Time) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.IsHealthy, c.LastError, c.LastCheck
}

// MultiConfig configures the multi-coordinator agent.
type MultiConfig struct {
	// Port for HTTP server
	Port int `json:"port"`

	// Coordinators is the list of coordinator endpoints to poll.
	Coordinators []*CoordinatorEndpoint `json:"coordinators"`

	// PollInterval is how often to poll for pending requests.
	PollInterval time.Duration `json:"poll_interval"`

	// ChromeUserDataDir is the Chrome profile directory to use.
	ChromeUserDataDir string `json:"chrome_profile"`

	// Headless controls whether Chrome runs headless.
	Headless bool `json:"headless"`

	// AccountStrategy determines how to select accounts.
	AccountStrategy AccountStrategy `json:"strategy"`

	// Accounts is the list of account emails to cycle through.
	Accounts []string `json:"accounts"`

	// Logger for structured logging.
	Logger *slog.Logger `json:"-"`
}

// DefaultMultiConfig returns a MultiConfig with sensible defaults.
func DefaultMultiConfig() MultiConfig {
	return MultiConfig{
		Port:            7891,
		PollInterval:    2 * time.Second,
		Headless:        false,
		AccountStrategy: StrategyLRU,
	}
}

// MultiAgent handles OAuth completion for multiple coordinators.
type MultiAgent struct {
	config       MultiConfig
	logger       *slog.Logger
	server       *http.Server
	browser      *Browser
	accountUsage map[string]*AccountUsage
	usagePath    string
	mu           sync.RWMutex
	stopCh       chan struct{}
	doneCh       chan struct{}
	running      bool

	// Track which requests we're already processing
	processing map[string]bool
	procMu     sync.Mutex

	// Callbacks
	OnAuthStart    func(coordinator, url, account string)
	OnAuthComplete func(coordinator, account, code string)
	OnAuthFailed   func(coordinator, account string, err error)
}

// NewMulti creates a new multi-coordinator auth agent.
func NewMulti(config MultiConfig) *MultiAgent {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	// Determine usage storage path
	configDir, _ := os.UserConfigDir()
	usagePath := filepath.Join(configDir, "caam", "account_usage.json")

	agent := &MultiAgent{
		config:       config,
		logger:       config.Logger,
		accountUsage: make(map[string]*AccountUsage),
		usagePath:    usagePath,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
		processing:   make(map[string]bool),
	}

	// Load existing usage data
	agent.loadUsage()

	return agent
}

// Start begins the multi-coordinator agent.
func (a *MultiAgent) Start(ctx context.Context) error {
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
		a.mu.Lock()
		a.running = false
		close(a.doneCh)
		a.mu.Unlock()
		return fmt.Errorf("Chrome/Chromium not found. Install Chrome or run 'caam doctor --auto' for guided installation")
	}

	// Initialize browser
	a.browser = NewBrowser(BrowserConfig{
		UserDataDir: a.config.ChromeUserDataDir,
		Headless:    a.config.Headless,
		Logger:      a.logger,
	})

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.HandleFunc("GET /coordinators", a.handleCoordinators)
	mux.HandleFunc("GET /accounts", a.handleAccounts)
	mux.HandleFunc("POST /auth", a.handleAuth)

	addr := fmt.Sprintf("127.0.0.1:%d", a.config.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		a.mu.Lock()
		a.running = false
		close(a.doneCh)
		a.mu.Unlock()
		return fmt.Errorf("listen %s: %w", addr, err)
	}

	a.server = &http.Server{
		Addr:         addr,
		Handler:      a.withLogging(mux),
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	// Start polling all coordinators
	go a.pollLoop(ctx)

	// Start HTTP server
	go func() {
		a.logger.Info("starting multi-coordinator agent",
			"addr", addr,
			"coordinators", len(a.config.Coordinators))
		if err := a.server.Serve(listener); err != http.ErrServerClosed {
			a.logger.Error("HTTP server error", "error", err)
		}
	}()

	return nil
}

// Stop halts the agent.
func (a *MultiAgent) Stop(ctx context.Context) error {
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

	// Wait for pollLoop to finish
	<-a.doneCh

	// Save usage data
	a.saveUsage()

	return nil
}

// pollLoop polls all coordinators for pending requests.
func (a *MultiAgent) pollLoop(ctx context.Context) {
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
			a.pollAllCoordinators(ctx)
		}
	}
}

// pollAllCoordinators fans out to check all coordinators concurrently.
func (a *MultiAgent) pollAllCoordinators(ctx context.Context) {
	var wg sync.WaitGroup

	for _, coord := range a.config.Coordinators {
		wg.Add(1)
		go func(c *CoordinatorEndpoint) {
			defer wg.Done()
			a.checkCoordinator(ctx, c)
		}(coord)
	}

	wg.Wait()
}

// checkCoordinator polls a single coordinator for pending requests.
func (a *MultiAgent) checkCoordinator(ctx context.Context, coord *CoordinatorEndpoint) {
	url := coord.URL + "/auth/pending"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		coord.SetHealth(false, err.Error())
		return
	}
	if coord.Token != "" {
		req.Header.Set("Authorization", "Bearer "+coord.Token)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		coord.SetHealth(false, err.Error())
		a.logger.Debug("failed to reach coordinator",
			"coordinator", coord.Name,
			"error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		coord.SetHealth(false, fmt.Sprintf("status %d", resp.StatusCode))
		a.logger.Debug("coordinator returned non-200",
			"coordinator", coord.Name,
			"status", resp.StatusCode)
		return
	}

	coord.SetHealth(true, "")

	var pending []struct {
		ID        string    `json:"id"`
		PaneID    int       `json:"pane_id"`
		URL       string    `json:"url"`
		CreatedAt time.Time `json:"created_at"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&pending); err != nil {
		coord.SetHealth(false, err.Error())
		a.logger.Debug("failed to decode pending requests",
			"coordinator", coord.Name,
			"error", err)
		return
	}

	for _, p := range pending {
		// Avoid processing same request multiple times
		a.procMu.Lock()
		if a.processing[p.ID] {
			a.procMu.Unlock()
			continue
		}
		a.processing[p.ID] = true
		a.procMu.Unlock()

		// Process in goroutine to not block other coordinators
		go func(requestID, authURL string) {
			a.processAuthRequest(ctx, coord, requestID, authURL)

			// Mark as no longer processing after completion
			a.procMu.Lock()
			delete(a.processing, requestID)
			a.procMu.Unlock()
		}(p.ID, p.URL)
	}
}

// processAuthRequest handles a single auth request from a coordinator.
func (a *MultiAgent) processAuthRequest(ctx context.Context, coord *CoordinatorEndpoint, requestID, authURL string) {
	a.logger.Info("processing auth request",
		"coordinator", coord.Name,
		"request_id", requestID,
		"url_prefix", truncate(authURL, 50))

	// Select account
	account := a.selectAccount()
	if a.OnAuthStart != nil {
		a.OnAuthStart(coord.Name, authURL, account)
	}

	// Complete OAuth
	code, usedAccount, err := a.browser.CompleteOAuth(ctx, authURL, account)
	if err != nil {
		a.logger.Error("OAuth failed",
			"coordinator", coord.Name,
			"request_id", requestID,
			"error", err)
		a.recordUsage(account, "failed")

		if a.OnAuthFailed != nil {
			a.OnAuthFailed(coord.Name, account, err)
		}

		// Send error to coordinator
		a.sendAuthComplete(ctx, coord, requestID, "", "", err.Error())
		return
	}

	a.logger.Info("OAuth completed",
		"coordinator", coord.Name,
		"request_id", requestID,
		"account", usedAccount)
	a.recordUsage(usedAccount, "success")

	if a.OnAuthComplete != nil {
		a.OnAuthComplete(coord.Name, usedAccount, code)
	}

	// Send success to coordinator
	a.sendAuthComplete(ctx, coord, requestID, code, usedAccount, "")
}

// sendAuthComplete sends the auth result to a specific coordinator.
func (a *MultiAgent) sendAuthComplete(ctx context.Context, coord *CoordinatorEndpoint, requestID, code, account, errMsg string) {
	url := coord.URL + "/auth/complete"

	body := map[string]string{
		"request_id": requestID,
		"code":       code,
		"account":    account,
	}
	if errMsg != "" {
		body["error"] = errMsg
	}

	bodyJSON, _ := json.Marshal(body)

	req, err := http.NewRequestWithContext(ctx, "POST", url, jsonReader(bodyJSON))
	if err != nil {
		a.logger.Error("failed to create request",
			"coordinator", coord.Name,
			"error", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if coord.Token != "" {
		req.Header.Set("Authorization", "Bearer "+coord.Token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		a.logger.Error("failed to send auth complete",
			"coordinator", coord.Name,
			"error", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		a.logger.Warn("coordinator returned error",
			"coordinator", coord.Name,
			"status", resp.StatusCode)
	}
}

// selectAccount chooses which account to use based on strategy.
func (a *MultiAgent) selectAccount() string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	accounts := a.config.Accounts
	if len(accounts) == 0 {
		return ""
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

func (a *MultiAgent) selectLRU(accounts []string) string {
	var oldest string
	var oldestTime time.Time

	for _, acc := range accounts {
		usage, ok := a.accountUsage[acc]
		if !ok {
			return acc
		}
		if oldest == "" || usage.LastUsed.Before(oldestTime) {
			oldest = acc
			oldestTime = usage.LastUsed
		}
	}

	return oldest
}

func (a *MultiAgent) selectRoundRobin(accounts []string) string {
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

func (a *MultiAgent) recordUsage(email, result string) {
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

	go a.saveUsage()
}

func (a *MultiAgent) loadUsage() {
	data, err := os.ReadFile(a.usagePath)
	if err != nil {
		return
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

func (a *MultiAgent) saveUsage() {
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

func (a *MultiAgent) withLogging(next http.Handler) http.Handler {
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

func (a *MultiAgent) handleStatus(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	accountCount := len(a.accountUsage)
	a.mu.RUnlock()

	healthyCount := 0
	for _, coord := range a.config.Coordinators {
		healthy, _, _ := coord.GetHealth()
		if healthy {
			healthyCount++
		}
	}

	status := map[string]interface{}{
		"running":              a.running,
		"coordinator_count":    len(a.config.Coordinators),
		"healthy_coordinators": healthyCount,
		"account_count":        accountCount,
		"strategy":             a.config.AccountStrategy,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (a *MultiAgent) handleCoordinators(w http.ResponseWriter, r *http.Request) {
	type coordStatus struct {
		Name        string    `json:"name"`
		URL         string    `json:"url"`
		DisplayName string    `json:"display_name"`
		IsHealthy   bool      `json:"is_healthy"`
		LastCheck   time.Time `json:"last_check"`
		LastError   string    `json:"last_error,omitempty"`
	}

	statuses := make([]coordStatus, 0, len(a.config.Coordinators))
	for _, c := range a.config.Coordinators {
		healthy, errMsg, lastCheck := c.GetHealth()
		statuses = append(statuses, coordStatus{
			Name:        c.Name,
			URL:         c.URL,
			DisplayName: c.DisplayName,
			IsHealthy:   healthy,
			LastCheck:   lastCheck,
			LastError:   errMsg,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

func (a *MultiAgent) handleAccounts(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	usages := make([]*AccountUsage, 0, len(a.accountUsage))
	for _, u := range a.accountUsage {
		usages = append(usages, u)
	}
	a.mu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(usages)
}

func (a *MultiAgent) handleAuth(w http.ResponseWriter, r *http.Request) {
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

// GetCoordinators returns the list of configured coordinators.
func (a *MultiAgent) GetCoordinators() []*CoordinatorEndpoint {
	return a.config.Coordinators
}

// AddCoordinator adds a new coordinator endpoint.
func (a *MultiAgent) AddCoordinator(coord *CoordinatorEndpoint) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.config.Coordinators = append(a.config.Coordinators, coord)
}

// RemoveCoordinator removes a coordinator by name.
func (a *MultiAgent) RemoveCoordinator(name string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i, c := range a.config.Coordinators {
		if c.Name == name {
			a.config.Coordinators = append(
				a.config.Coordinators[:i],
				a.config.Coordinators[i+1:]...)
			return true
		}
	}
	return false
}
