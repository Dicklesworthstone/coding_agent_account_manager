// Package agent implements the local auth-agent that completes OAuth flows.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Config configures the auth agent.
type Config struct {
	// Port for HTTP server
	Port int

	// CoordinatorURL is the URL of the remote coordinator.
	CoordinatorURL string

	// CoordinatorToken is an optional shared secret for coordinator API calls.
	CoordinatorToken string

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
	// LimitedUntil is when the account's usage limit lifts; until then it
	// is not signed in again (signing a pane in with it would only hit the
	// limit again).
	LimitedUntil time.Time `json:"limited_until,omitzero"`
}

// defaultLimitHold is how long an account at its usage limit is passed over
// when the limit's reset cannot be read: Claude's five-hour session window.
const defaultLimitHold = 5 * time.Hour

// maxLimitHold caps a hold; the longest limit, the weekly one, lifts within
// a week.
const maxLimitHold = 8 * 24 * time.Hour

var (
	resetZoneRe  = regexp.MustCompile(`\(([A-Za-z]+(?:/[A-Za-z0-9_+-]+)+|UTC|GMT)\)`)
	resetClockRe = regexp.MustCompile(`(?i)\b(\d{1,2})(?::(\d{2}))?\s*([ap])\.?m\b`)
	resetDateRe  = regexp.MustCompile(`(?i)\b(jan|feb|mar|apr|may|jun|jul|aug|sep|oct|nov|dec)[a-z]*\.?\s+(\d{1,2})\b`)
	resetInRe    = regexp.MustCompile(`(?i)^in\s+(?:(\d+)\s*d\w*\s*)?(?:(\d+)\s*h\w*\s*)?(?:(\d+)\s*m\w*)?\s*$`)
)

// limitResetTime returns when a usage limit lifts, from the reset Claude Code
// printed in its banner: "3pm (America/New_York)", "Oct 9, 3:30pm
// (Europe/Paris)", "Oct 9", "in 2h 13m". A time without a zone is read in
// the agent's local zone. Text it cannot read gives now+defaultLimitHold. The
// result is never before now nor more than maxLimitHold after it.
func limitResetTime(reset string, now time.Time) time.Time {
	reset = strings.TrimSpace(reset)
	t, ok := parseLimitReset(reset, now)
	switch {
	case !ok:
		return now.Add(defaultLimitHold)
	case t.Before(now):
		return now
	case t.After(now.Add(maxLimitHold)):
		return now.Add(maxLimitHold)
	}
	return t
}

func parseLimitReset(reset string, now time.Time) (time.Time, bool) {
	if m := resetInRe.FindStringSubmatch(reset); m != nil && (m[1] != "" || m[2] != "" || m[3] != "") {
		d := time.Duration(atoiOr0(m[1]))*24*time.Hour + time.Duration(atoiOr0(m[2]))*time.Hour + time.Duration(atoiOr0(m[3]))*time.Minute
		return now.Add(d), true
	}

	loc := time.Local
	if m := resetZoneRe.FindStringSubmatch(reset); m != nil {
		if l, err := time.LoadLocation(m[1]); err == nil {
			loc = l
		}
	}
	local := now.In(loc)

	clock := resetClockRe.FindStringSubmatch(reset)
	hour, minute := 0, 0
	if clock != nil {
		hour = atoiOr0(clock[1]) % 12
		minute = atoiOr0(clock[2])
		if strings.EqualFold(clock[3], "p") {
			hour += 12
		}
		if minute > 59 {
			return time.Time{}, false
		}
	}

	if date := resetDateRe.FindStringSubmatch(reset); date != nil {
		month, err := time.Parse("Jan", strings.ToUpper(date[1][:1])+strings.ToLower(date[1][1:3]))
		day := atoiOr0(date[2])
		if err != nil || day < 1 || day > 31 {
			return time.Time{}, false
		}
		t := time.Date(local.Year(), month.Month(), day, hour, minute, 0, 0, loc)
		// "Jan 2" read in late December is next year's.
		if t.Before(local.Add(-48 * time.Hour)) {
			t = t.AddDate(1, 0, 0)
		}
		return t, true
	}

	if clock == nil {
		return time.Time{}, false
	}
	t := time.Date(local.Year(), local.Month(), local.Day(), hour, minute, 0, 0, loc)
	if !t.After(local) {
		t = t.AddDate(0, 0, 1)
	}
	return t, true
}

func atoiOr0(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// unheldAccounts returns, in order, the accounts not held at their usage
// limit at now.
func unheldAccounts(accounts []string, usage map[string]*AccountUsage, now time.Time) []string {
	var out []string
	for _, acc := range accounts {
		if u, ok := usage[acc]; ok && u.LimitedUntil.After(now) {
			continue
		}
		out = append(out, acc)
	}
	return out
}

// holdAccount records that email is at its usage limit until until, keeping
// a later hold already recorded. A configured account matching email in any
// case is the one held.
func holdAccount(usage map[string]*AccountUsage, configured []string, email string, until time.Time) string {
	for _, acc := range configured {
		if strings.EqualFold(acc, email) {
			email = acc
			break
		}
	}
	u, ok := usage[email]
	if !ok {
		u = &AccountUsage{Email: email}
		usage[email] = u
	}
	if until.After(u.LimitedUntil) {
		u.LimitedUntil = until
	}
	return email
}

// accountsLimitedError is the agent declining a sign-in: every configured
// account is at its usage limit, the first until Until.
type accountsLimitedError struct {
	Accounts int
	Until    time.Time
}

func (e *accountsLimitedError) Error() string {
	return fmt.Sprintf("all %d configured accounts are at their usage limit (the first lifts at %s); not signing in",
		e.Accounts, e.Until.Local().Format("Jan 2 15:04 MST"))
}

// allLimitedError reports that every configured account is held, and when
// the first hold lifts.
func allLimitedError(accounts []string, usage map[string]*AccountUsage) error {
	var first time.Time
	for _, acc := range accounts {
		if u, ok := usage[acc]; ok && (first.IsZero() || u.LimitedUntil.Before(first)) {
			first = u.LimitedUntil
		}
	}
	return &accountsLimitedError{Accounts: len(accounts), Until: first}
}

// failureCompletion reports err for a request; a decline because every
// account is at its limit says when to ask again.
func failureCompletion(requestID string, err error) completion {
	c := completion{RequestID: requestID, Error: err.Error()}
	var limited *accountsLimitedError
	if errors.As(err, &limited) {
		c.RetryAfter = limited.Until
	}
	return c
}

// oauthCompleter completes an OAuth flow and returns the challenge code and
// the account that was used. *Browser is the production implementation.
type oauthCompleter interface {
	// CompleteOAuth signs in with one of accounts (most preferred first,
	// any account when empty) and returns the code and the account used.
	CompleteOAuth(ctx context.Context, oauthURL string, accounts []string) (string, string, error)
}

// accountOrder lists the accounts a flow may use: the selected one first,
// then the rest in configured order as fallbacks. With no selection (no
// accounts configured) it is empty, meaning any account.
func accountOrder(selected string, configured []string) []string {
	if selected == "" {
		return nil
	}
	order := []string{selected}
	for _, account := range configured {
		if !strings.EqualFold(account, selected) {
			order = append(order, account)
		}
	}
	return order
}

// deliveryPolicy bounds acknowledged delivery of auth results.
type deliveryPolicy struct {
	// Timeout is the total time spent retrying one delivery.
	Timeout time.Duration
	// InitialDelay is the first retry delay; it doubles up to MaxDelay.
	InitialDelay time.Duration
	MaxDelay     time.Duration
}

// defaultDeliveryPolicy outlasts a dropped SSH tunnel reconnect while staying
// within the coordinator's default auth timeout plus its replay window.
var defaultDeliveryPolicy = deliveryPolicy{
	Timeout:      90 * time.Second,
	InitialDelay: 500 * time.Millisecond,
	MaxDelay:     8 * time.Second,
}

// completion is the auth result body posted to a coordinator's /auth/complete.
// Exactly one of Code or Error is set. Retries resend identical bytes, which
// the coordinator acknowledges as a duplicate of the response it accepted.
type completion struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code,omitempty"`
	Account   string `json:"account,omitempty"`
	Error     string `json:"error,omitempty"`
	// RetryAfter, with Error, is when an account frees up again.
	RetryAfter time.Time `json:"retry_after,omitzero"`
}

// DeliveryRejectedError is a final coordinator rejection of an auth result,
// such as an unknown, closed, or conflicting request. It is not retried.
type DeliveryRejectedError struct {
	StatusCode int
	Message    string
}

func (e *DeliveryRejectedError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("coordinator rejected auth result (HTTP %d)", e.StatusCode)
	}
	return fmt.Sprintf("coordinator rejected auth result (HTTP %d): %s", e.StatusCode, e.Message)
}

// retryableDeliveryStatus reports whether a coordinator status may succeed on
// redelivery. Everything else other than 2xx is final.
func retryableDeliveryStatus(code int) bool {
	return code >= 500 || code == http.StatusRequestTimeout || code == http.StatusTooManyRequests
}

// deliverCompletion posts an auth result until the coordinator acknowledges
// it with a 2xx status or rejects it with a final status. Transport errors,
// 5xx, 408, and 429 are retried with exponential backoff, so a response lost
// on the way back is redelivered rather than reported as success or dropped.
func deliverCompletion(ctx context.Context, client *http.Client, baseURL, token string, body completion, policy deliveryPolicy, logger *slog.Logger) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode auth result: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, policy.Timeout)
	defer cancel()

	delay := policy.InitialDelay
	for attempt := 1; ; attempt++ {
		status, msg, err := postCompletion(ctx, client, baseURL, token, payload)
		if err == nil && status >= 200 && status < 300 {
			logger.Info("auth result acknowledged",
				"request_id", body.RequestID,
				"attempt", attempt)
			return nil
		}
		if err == nil && !retryableDeliveryStatus(status) {
			return &DeliveryRejectedError{StatusCode: status, Message: msg}
		}
		if err == nil {
			err = fmt.Errorf("HTTP %d: %s", status, msg)
		}

		logger.Warn("auth result delivery failed",
			"request_id", body.RequestID,
			"attempt", attempt,
			"retry_in", delay,
			"error", err)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("auth result for request %s not acknowledged after %d attempts: %w", body.RequestID, attempt, err)
		case <-timer.C:
		}
		delay = min(delay*2, policy.MaxDelay)
	}
}

func postCompletion(ctx context.Context, client *http.Client, baseURL, token string, payload []byte) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/auth/complete", bytes.NewReader(payload))
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// The acknowledgement was cut off; redelivery is safe.
		return 0, "", fmt.Errorf("read acknowledgement: %w", err)
	}
	return resp.StatusCode, coordinatorErrorMessage(data), nil
}

// coordinatorErrorMessage extracts the "error" field of a coordinator JSON
// error body, falling back to the raw text.
func coordinatorErrorMessage(data []byte) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &body) == nil && body.Error != "" {
		return body.Error
	}
	return strings.TrimSpace(string(data))
}

// pendingRequest is one entry of a coordinator's /auth/pending list.
type pendingRequest struct {
	ID        string    `json:"id"`
	PaneID    int       `json:"pane_id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	// LimitedAccount hit its usage limit in the pane, which LimitReset
	// lifts ("3pm (America/New_York)"); both are empty when unknown.
	LimitedAccount string `json:"limited_account,omitempty"`
	LimitReset     string `json:"limit_reset,omitempty"`
}

// fetchPending lists a coordinator's pending auth requests.
func fetchPending(ctx context.Context, client *http.Client, baseURL, token string) ([]pendingRequest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/auth/pending", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, coordinatorErrorMessage(data))
	}

	var pending []pendingRequest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&pending); err != nil {
		return nil, fmt.Errorf("decode pending requests: %w", err)
	}
	return pending, nil
}

// Agent handles OAuth completion for the coordinator.
type Agent struct {
	config       Config
	logger       *slog.Logger
	server       *http.Server
	browser      *Browser
	oauth        oauthCompleter
	oauthMu      sync.Mutex // serializes browser flows (see runOAuth)
	client       *http.Client
	delivery     deliveryPolicy
	accountUsage map[string]*AccountUsage
	usagePath    string
	mu           sync.RWMutex
	cancel       context.CancelFunc
	stopCh       chan struct{}
	doneCh       chan struct{}
	running      bool

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
		client:       &http.Client{Timeout: 15 * time.Second},
		delivery:     defaultDeliveryPolicy,
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
		a.mu.Lock()
		a.running = false
		close(a.doneCh)
		a.mu.Unlock()
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
	a.logger.Info("using Chrome profile", "dir", a.browser.UserDataDir())
	if a.oauth == nil {
		a.oauth = a.browser
	}

	// Stop cancels in-flight OAuth flows and deliveries.
	ctx, a.cancel = context.WithCancel(ctx)

	// Set up HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("GET /status", a.handleStatus)
	mux.HandleFunc("POST /auth", a.handleAuth)
	mux.HandleFunc("GET /accounts", a.handleAccounts)

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
		a.logger.Info("starting agent HTTP server", "addr", addr)
		if err := a.server.Serve(listener); err != http.ErrServerClosed {
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
	if a.cancel != nil {
		a.cancel()
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
	pending, err := fetchPending(ctx, a.client, a.config.CoordinatorURL, a.config.CoordinatorToken)
	if err != nil {
		a.logger.Debug("failed to fetch pending requests", "error", err)
		return
	}

	for _, p := range pending {
		if ctx.Err() != nil {
			return
		}
		a.noteLimit(p)
		a.processAuthRequest(ctx, p.ID, p.URL)
	}
}

// noteLimit holds the account a request reports at its usage limit.
func (a *Agent) noteLimit(p pendingRequest) {
	if p.LimitedAccount == "" {
		return
	}
	until := limitResetTime(p.LimitReset, time.Now())
	a.mu.Lock()
	held := holdAccount(a.accountUsage, a.config.Accounts, p.LimitedAccount, until)
	a.mu.Unlock()
	a.logger.Info("account at its usage limit; passing over it",
		"request_id", p.ID,
		"account", held,
		"until", until.Format(time.RFC3339))
	go a.saveUsage()
}

// processAuthRequest completes one auth request and delivers the result.
// Success callbacks and usage records happen only after the coordinator
// acknowledges the code.
func (a *Agent) processAuthRequest(ctx context.Context, requestID, authURL string) {
	a.logger.Info("processing auth request", "request_id", requestID)

	account, code, usedAccount, err := a.runOAuth(ctx, authURL, "", func(account string) {
		if a.OnAuthStart != nil {
			a.OnAuthStart(authURL, account)
		}
	})
	if err != nil {
		a.logger.Error("OAuth failed",
			"request_id", requestID,
			"error", err)
		a.recordUsage(account, "failed")

		// Report the failure so the pane stops waiting for a code.
		if derr := deliverCompletion(ctx, a.client, a.config.CoordinatorURL, a.config.CoordinatorToken,
			failureCompletion(requestID, err), a.delivery, a.logger); derr != nil {
			a.logger.Warn("failed to report OAuth failure", "request_id", requestID, "error", derr)
		}

		if a.OnAuthFailed != nil {
			a.OnAuthFailed(account, err)
		}
		return
	}

	a.logger.Info("OAuth completed",
		"request_id", requestID,
		"account", usedAccount)

	if err := deliverCompletion(ctx, a.client, a.config.CoordinatorURL, a.config.CoordinatorToken,
		completion{RequestID: requestID, Code: code, Account: usedAccount}, a.delivery, a.logger); err != nil {
		a.logger.Error("auth code not delivered",
			"request_id", requestID,
			"account", usedAccount,
			"error", err)
		a.recordUsage(usedAccount, "undelivered")
		if a.OnAuthFailed != nil {
			a.OnAuthFailed(usedAccount, fmt.Errorf("deliver code for request %s: %w", requestID, err))
		}
		return
	}

	a.recordUsage(usedAccount, "success")
	if a.OnAuthComplete != nil {
		a.OnAuthComplete(usedAccount, code)
	}
}

// runOAuth selects an account (unless one is requested) and completes one
// OAuth flow. Flows are serialized because they share one Chrome profile
// directory, and each selection must see the account the previous flow used.
func (a *Agent) runOAuth(ctx context.Context, authURL, requested string, onStart func(account string)) (account, code, usedAccount string, err error) {
	a.oauthMu.Lock()
	defer a.oauthMu.Unlock()

	account = requested
	accounts := []string{requested}
	if account == "" {
		// Accounts at their usage limit are neither chosen nor fallen back
		// to; with all of them there, signing in would only hit a limit.
		a.mu.RLock()
		available := unheldAccounts(a.config.Accounts, a.accountUsage, time.Now())
		if len(a.config.Accounts) > 0 && len(available) == 0 {
			err = allLimitedError(a.config.Accounts, a.accountUsage)
		}
		a.mu.RUnlock()
		if err != nil {
			return "", "", "", err
		}
		account = a.selectAccount()
		accounts = accountOrder(account, available)
	}
	if onStart != nil {
		onStart(account)
	}
	code, usedAccount, err = a.oauth.CompleteOAuth(ctx, authURL, accounts)
	if usedAccount != "" {
		a.touchAccount(usedAccount)
	} else {
		a.touchAccount(account)
	}
	return account, code, usedAccount, err
}

// touchAccount marks an account as just used without recording an outcome.
func (a *Agent) touchAccount(email string) {
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

// selectAccount chooses which account to use based on strategy, among the
// accounts not at their usage limit.
func (a *Agent) selectAccount() string {
	a.mu.RLock()
	defer a.mu.RUnlock()

	accounts := unheldAccounts(a.config.Accounts, a.accountUsage, time.Now())
	if len(accounts) == 0 {
		return "" // Will use whatever account is currently logged in
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
	running := a.running
	a.mu.RUnlock()

	status := map[string]interface{}{
		"running":       running,
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
		copied := *u
		usages = append(usages, &copied)
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

// Helpers

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
