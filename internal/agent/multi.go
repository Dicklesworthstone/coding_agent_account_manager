package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	caamsync "github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"golang.org/x/crypto/ssh"
)

// SSHTunnel routes coordinator traffic through an SSH connection the agent
// opens itself, like 'ssh -L', so the coordinator API never listens beyond
// loopback on the remote host.
type SSHTunnel struct {
	Host         string `json:"host"`
	Port         int    `json:"port,omitempty"`
	User         string `json:"user,omitempty"`
	IdentityFile string `json:"identity_file,omitempty"`
}

// CoordinatorEndpoint represents a remote coordinator to poll.
type CoordinatorEndpoint struct {
	Name        string `json:"name"`         // Short name: "csd", "css", "trj"
	URL         string `json:"url"`          // Base URL: http://127.0.0.1:7890 (resolved on the remote side when SSH is set)
	DisplayName string `json:"display_name"` // Human-friendly name
	Token       string `json:"token,omitempty"`

	// SSH, when set, reaches URL through an SSH connection to this host.
	SSH *SSHTunnel `json:"ssh,omitempty"`

	LastCheck time.Time `json:"-"`
	IsHealthy bool      `json:"-"`
	LastError string    `json:"-"`
	mu        sync.RWMutex

	transportMu sync.Mutex
	client      *http.Client
	tunnel      *sshTunnel
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

// Transport describes how the endpoint is reached, for status output.
func (c *CoordinatorEndpoint) Transport() string {
	if c.SSH != nil {
		return "ssh"
	}
	return "direct"
}

// httpClient returns the endpoint's HTTP client, dialing through the SSH
// tunnel when one is configured.
func (c *CoordinatorEndpoint) httpClient() *http.Client {
	c.transportMu.Lock()
	defer c.transportMu.Unlock()
	if c.client != nil {
		return c.client
	}
	if c.SSH == nil {
		c.client = &http.Client{Timeout: 15 * time.Second}
		return c.client
	}
	c.tunnel = newSSHTunnel(c.Name, *c.SSH)
	c.client = &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			DialContext:         c.tunnel.DialContext,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
	}
	return c.client
}

// close releases the endpoint's SSH connection, if any. A later request
// reconnects.
func (c *CoordinatorEndpoint) close() {
	c.transportMu.Lock()
	tunnel := c.tunnel
	c.transportMu.Unlock()
	if tunnel != nil {
		tunnel.Close()
	}
}

// CoordinatorProbe is the result of a one-off coordinator health check.
type CoordinatorProbe struct {
	Healthy      bool
	Latency      time.Duration
	Backend      string
	PaneCount    int
	PendingAuths int
	Error        string
}

// Probe calls the coordinator's authenticated /status through the endpoint's
// transport (including its SSH tunnel), as the agent would reach it.
func (c *CoordinatorEndpoint) Probe(ctx context.Context) CoordinatorProbe {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(c.URL, "/")+"/status", nil)
	if err != nil {
		return CoordinatorProbe{Error: err.Error()}
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.httpClient().Do(req)
	probe := CoordinatorProbe{Latency: time.Since(start)}
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		probe.Error = fmt.Sprintf("status %d: %s", resp.StatusCode, coordinatorErrorMessage(data))
		return probe
	}
	var status struct {
		Backend      string `json:"backend"`
		PaneCount    int    `json:"pane_count"`
		PendingAuths int    `json:"pending_auths"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&status); err != nil {
		probe.Error = fmt.Sprintf("decode status: %v", err)
		return probe
	}
	probe.Healthy = true
	probe.Backend = status.Backend
	probe.PaneCount = status.PaneCount
	probe.PendingAuths = status.PendingAuths
	return probe
}

// Close releases the endpoint's SSH connection, if any.
func (c *CoordinatorEndpoint) Close() {
	c.close()
}

// sshTunnel dials coordinator connections over one shared SSH connection and
// reconnects when that connection breaks.
type sshTunnel struct {
	name        string
	cfg         SSHTunnel
	dialTimeout time.Duration

	mu    sync.Mutex
	owner *caamsync.SSHClient
	conn  *ssh.Client
}

func newSSHTunnel(name string, cfg SSHTunnel) *sshTunnel {
	return &sshTunnel{name: name, cfg: cfg, dialTimeout: 15 * time.Second}
}

// DialContext opens addr from the remote host. A failed dial on an existing
// connection is retried once on a fresh connection.
func (t *sshTunnel) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	reused := t.conn != nil
	conn, err := t.dialLocked(ctx, network, addr)
	if err == nil {
		return conn, nil
	}
	t.closeLocked()
	if !reused || ctx.Err() != nil {
		return nil, fmt.Errorf("ssh tunnel %s: %w", t.cfg.Host, err)
	}
	conn, err = t.dialLocked(ctx, network, addr)
	if err != nil {
		t.closeLocked()
		return nil, fmt.Errorf("ssh tunnel %s: %w", t.cfg.Host, err)
	}
	return conn, nil
}

func (t *sshTunnel) dialLocked(ctx context.Context, network, addr string) (net.Conn, error) {
	if t.conn == nil {
		if err := t.connectLocked(); err != nil {
			return nil, err
		}
	}
	client := t.conn

	// ssh.Client.Dial has no deadline and blocks forever on a half-open
	// connection; closing the client releases it.
	type result struct {
		conn net.Conn
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		c, err := client.Dial(network, addr)
		ch <- result{c, err}
	}()

	timer := time.NewTimer(t.dialTimeout)
	defer timer.Stop()
	var cause error
	select {
	case r := <-ch:
		return r.conn, r.err
	case <-timer.C:
		cause = fmt.Errorf("dial %s timed out after %s", addr, t.dialTimeout)
	case <-ctx.Done():
		cause = ctx.Err()
	}
	client.Close()
	if r := <-ch; r.conn != nil {
		r.conn.Close()
	}
	return nil, cause
}

func (t *sshTunnel) connectLocked() error {
	owner := caamsync.NewSSHClient(&caamsync.Machine{
		Name:       t.name,
		Address:    t.cfg.Host,
		Port:       t.cfg.Port,
		SSHUser:    t.cfg.User,
		SSHKeyPath: t.cfg.IdentityFile,
	})
	if err := owner.Connect(caamsync.ConnectOptions{Timeout: 15 * time.Second, UseAgent: true}); err != nil {
		return err
	}
	t.owner = owner
	t.conn = owner.Conn()
	return nil
}

func (t *sshTunnel) closeLocked() {
	if t.owner != nil {
		t.owner.Disconnect()
	}
	t.owner = nil
	t.conn = nil
}

// Close drops the SSH connection; the next dial reconnects.
func (t *sshTunnel) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.closeLocked()
}

// FileConfig is the on-disk agent configuration. 'caam setup distributed'
// writes it and 'caam auth-agent --config' reads it. A config with
// coordinators runs the multi-coordinator agent; otherwise CoordinatorURL
// selects a single coordinator.
type FileConfig struct {
	Port             int                    `json:"port,omitempty"`
	CoordinatorURL   string                 `json:"coordinator_url,omitempty"`
	Coordinator      string                 `json:"coordinator,omitempty"`
	CoordinatorToken string                 `json:"coordinator_token,omitempty"`
	Coordinators     []*CoordinatorEndpoint `json:"coordinators,omitempty"`
	PollInterval     string                 `json:"poll_interval,omitempty"`
	ChromeProfile    string                 `json:"chrome_profile"`
	ChromeUserData   string                 `json:"chrome_user_data_dir,omitempty"`
	ChromeProfileDir string                 `json:"chrome_profile_dir,omitempty"`
	Headless         bool                   `json:"headless,omitempty"`
	Strategy         string                 `json:"strategy,omitempty"`
	Accounts         []string               `json:"accounts"`
}

// DefaultConfigPath is where 'caam setup distributed' writes the agent
// config and where 'caam auth-agent service' and 'caam serve' look for it.
func DefaultConfigPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		configDir = filepath.Join(os.Getenv("HOME"), ".config")
	}
	return filepath.Join(configDir, "caam", "distributed-agent.json")
}

// LoadFileConfig reads an agent FileConfig from path.
func LoadFileConfig(path string) (FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return FileConfig{}, fmt.Errorf("read config: %w", err)
	}
	var fc FileConfig
	if err := json.Unmarshal(data, &fc); err != nil {
		return FileConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return fc, nil
}

// WriteFileConfig atomically writes an agent FileConfig readable only by its
// owner, since it carries coordinator tokens.
func WriteFileConfig(path string, fc FileConfig) error {
	data, err := json.MarshalIndent(fc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp config file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename

	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod temp config file: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp config file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp config file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp config file: %w", err)
	}
	return nil
}

// ChromeUserDataDir returns the configured Chrome profile directory from any
// of its accepted keys.
func (fc FileConfig) ChromeUserDataDir() string {
	for _, v := range []string{fc.ChromeProfile, fc.ChromeUserData, fc.ChromeProfileDir} {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
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
	oauth        oauthCompleter
	delivery     deliveryPolicy
	accountUsage map[string]*AccountUsage
	usagePath    string
	mu           sync.RWMutex
	cancel       context.CancelFunc
	stopCh       chan struct{}
	doneCh       chan struct{}
	running      bool

	// Track which requests we're already processing
	processing map[string]bool
	procMu     sync.Mutex
	inflight   sync.WaitGroup

	// oauthMu serializes browser flows (see runOAuth).
	oauthMu sync.Mutex

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
		delivery:     defaultDeliveryPolicy,
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
	if a.oauth == nil {
		a.oauth = a.browser
	}

	// Stop cancels in-flight OAuth flows and deliveries.
	ctx, a.cancel = context.WithCancel(ctx)

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.HandleFunc("GET /coordinators", a.handleCoordinators)
	mux.HandleFunc("GET /accounts", a.handleAccounts)
	mux.HandleFunc("POST /auth", a.handleAuth)

	addr := fmt.Sprintf("127.0.0.1:%d", a.config.Port)
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		a.cancel()
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

// Stop halts the agent, cancelling in-flight OAuth flows and deliveries.
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
	if a.cancel != nil {
		a.cancel()
	}

	if a.server != nil {
		if err := a.server.Shutdown(ctx); err != nil {
			a.logger.Warn("HTTP server shutdown error", "error", err)
		}
	}

	// Wait for pollLoop and in-flight requests to finish
	<-a.doneCh
	a.inflight.Wait()

	if a.browser != nil {
		a.browser.Close()
	}
	for _, coord := range a.GetCoordinators() {
		coord.close()
	}

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

	for _, coord := range a.GetCoordinators() {
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
	pending, err := fetchPending(ctx, coord.httpClient(), coord.URL, coord.Token)
	if err != nil {
		coord.SetHealth(false, err.Error())
		a.logger.Debug("failed to poll coordinator",
			"coordinator", coord.Name,
			"transport", coord.Transport(),
			"error", err)
		return
	}
	coord.SetHealth(true, "")

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
		a.inflight.Add(1)
		go func(requestID, authURL string) {
			defer a.inflight.Done()
			a.processAuthRequest(ctx, coord, requestID, authURL)

			// Mark as no longer processing after completion
			a.procMu.Lock()
			delete(a.processing, requestID)
			a.procMu.Unlock()
		}(p.ID, p.URL)
	}
}

// processAuthRequest completes one auth request from a coordinator and
// delivers the result. Success callbacks and usage records happen only after
// the coordinator acknowledges the code.
func (a *MultiAgent) processAuthRequest(ctx context.Context, coord *CoordinatorEndpoint, requestID, authURL string) {
	a.logger.Info("processing auth request",
		"coordinator", coord.Name,
		"request_id", requestID)

	account, code, usedAccount, err := a.runOAuth(ctx, authURL, "", func(account string) {
		if a.OnAuthStart != nil {
			a.OnAuthStart(coord.Name, authURL, account)
		}
	})
	if err != nil {
		a.logger.Error("OAuth failed",
			"coordinator", coord.Name,
			"request_id", requestID,
			"error", err)
		a.recordUsage(account, "failed")

		// Report the failure so the pane stops waiting for a code.
		if derr := deliverCompletion(ctx, coord.httpClient(), coord.URL, coord.Token,
			completion{RequestID: requestID, Error: err.Error()}, a.delivery, a.logger.With("coordinator", coord.Name)); derr != nil {
			a.logger.Warn("failed to report OAuth failure",
				"coordinator", coord.Name,
				"request_id", requestID,
				"error", derr)
		}

		if a.OnAuthFailed != nil {
			a.OnAuthFailed(coord.Name, account, err)
		}
		return
	}

	a.logger.Info("OAuth completed",
		"coordinator", coord.Name,
		"request_id", requestID,
		"account", usedAccount)

	if err := deliverCompletion(ctx, coord.httpClient(), coord.URL, coord.Token,
		completion{RequestID: requestID, Code: code, Account: usedAccount}, a.delivery, a.logger.With("coordinator", coord.Name)); err != nil {
		a.logger.Error("auth code not delivered",
			"coordinator", coord.Name,
			"request_id", requestID,
			"account", usedAccount,
			"error", err)
		a.recordUsage(usedAccount, "undelivered")
		if a.OnAuthFailed != nil {
			a.OnAuthFailed(coord.Name, usedAccount, fmt.Errorf("deliver code for request %s: %w", requestID, err))
		}
		return
	}

	a.recordUsage(usedAccount, "success")
	if a.OnAuthComplete != nil {
		a.OnAuthComplete(coord.Name, usedAccount, code)
	}
}

// runOAuth selects an account (unless one is requested) and completes one
// OAuth flow. Flows are serialized: they share one Chrome profile directory,
// where a second concurrent Chrome fails, and each selection must see the
// account the previous flow just used so concurrent rate limits spread
// across accounts.
func (a *MultiAgent) runOAuth(ctx context.Context, authURL, requested string, onStart func(account string)) (account, code, usedAccount string, err error) {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()

	account = requested
	if account == "" {
		account = a.selectAccount()
	}
	if onStart != nil {
		onStart(account)
	}
	code, usedAccount, err = a.oauth.CompleteOAuth(ctx, authURL, account)
	if usedAccount != "" {
		a.touchAccount(usedAccount)
	} else {
		a.touchAccount(account)
	}
	return account, code, usedAccount, err
}

// touchAccount marks an account as just used without recording an outcome.
func (a *MultiAgent) touchAccount(email string) {
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
	case StrategyRandom:
		return accounts[rand.IntN(len(accounts))]
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
		copied := *u
		usages = append(usages, &copied)
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
	running := a.running
	a.mu.RUnlock()

	coords := a.GetCoordinators()
	healthyCount := 0
	for _, coord := range coords {
		healthy, _, _ := coord.GetHealth()
		if healthy {
			healthyCount++
		}
	}

	status := map[string]interface{}{
		"running":              running,
		"coordinator_count":    len(coords),
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
		Transport   string    `json:"transport"`
		SSHHost     string    `json:"ssh_host,omitempty"`
		IsHealthy   bool      `json:"is_healthy"`
		LastCheck   time.Time `json:"last_check"`
		LastError   string    `json:"last_error,omitempty"`
	}

	coords := a.GetCoordinators()
	statuses := make([]coordStatus, 0, len(coords))
	for _, c := range coords {
		healthy, errMsg, lastCheck := c.GetHealth()
		status := coordStatus{
			Name:        c.Name,
			URL:         c.URL,
			DisplayName: c.DisplayName,
			Transport:   c.Transport(),
			IsHealthy:   healthy,
			LastCheck:   lastCheck,
			LastError:   errMsg,
		}
		if c.SSH != nil {
			status.SSHHost = c.SSH.Host
		}
		statuses = append(statuses, status)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statuses)
}

func (a *MultiAgent) handleAccounts(w http.ResponseWriter, r *http.Request) {
	a.mu.RLock()
	usages := make([]*AccountUsage, 0, len(a.accountUsage))
	for _, u := range a.accountUsage {
		copied := *u
		usages = append(usages, &copied)
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

	account, code, usedAccount, err := a.runOAuth(r.Context(), req.URL, req.Account, nil)
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

// GetCoordinators returns a snapshot of the configured coordinators.
func (a *MultiAgent) GetCoordinators() []*CoordinatorEndpoint {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]*CoordinatorEndpoint(nil), a.config.Coordinators...)
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
				a.config.Coordinators[:i:i],
				a.config.Coordinators[i+1:]...)
			c.close()
			return true
		}
	}
	return false
}
