package coordinator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Backend specifies which terminal multiplexer to use for pane monitoring.
type Backend string

const (
	// BackendWezTerm uses WezTerm's native mux-server (PREFERRED).
	// Benefits: integrated multiplexing, domain awareness, rich metadata.
	BackendWezTerm Backend = "wezterm"

	// BackendTmux uses tmux as a fallback for other terminals.
	// Use this with Ghostty, Alacritty, iTerm2, or other terminals without
	// built-in multiplexing. Requires tmux server running.
	// Limitations: no domain awareness, extra process layer, less metadata.
	BackendTmux Backend = "tmux"

	// BackendAuto tries WezTerm first, falls back to tmux.
	BackendAuto Backend = "auto"
)

// Config configures the coordinator.
type Config struct {
	// Backend specifies which terminal multiplexer to use.
	// Options: "wezterm" (preferred), "tmux", or "auto" (try wezterm, fall back to tmux).
	// Default: "auto"
	Backend Backend

	// PollInterval is how often to check pane output.
	PollInterval time.Duration

	// AuthTimeout is how long an agent has to answer a request once it has
	// fetched it. Requests no agent has fetched yet wait without a limit.
	AuthTimeout time.Duration

	// StateTimeout is how long to wait in intermediate states before timing out.
	StateTimeout time.Duration

	// ResumePrompt is the text to inject after successful auth.
	ResumePrompt string

	// PaneFilter filters which panes to monitor.
	// If nil, monitors all panes.
	PaneFilter func(Pane) bool

	// OutputLines is how many lines to retrieve from pane output.
	OutputLines int

	// Logger for structured logging.
	Logger *slog.Logger

	// LocalAgentURL is the URL of the local auth agent.
	LocalAgentURL string

	// AuthToken is an optional shared secret required by the coordinator API.
	// When set, clients must send "Authorization: Bearer <token>".
	// A token is mandatory when the API listens on a non-loopback address.
	AuthToken string

	// ResponseReplayWindow is how long a closed request remembers the response
	// it accepted, so an agent retrying a delivery whose acknowledgement was
	// lost receives the same acknowledgement instead of an unknown-request
	// rejection.
	ResponseReplayWindow time.Duration

	// LoginCooldown is the minimum time between /login injections per pane.
	LoginCooldown time.Duration

	// MethodSelectCooldown is the minimum time between method selection injections per pane.
	MethodSelectCooldown time.Duration

	// ResumeCooldown is the minimum time between resume prompt injections per pane.
	// This prevents duplicate resume prompts if the state detection triggers multiple times.
	ResumeCooldown time.Duration

	// MaxLoginRetries is how many times a failed login is retried (by
	// injecting /login again) within one rate-limit episode.
	MaxLoginRetries int

	// PaneClient allows injecting a custom pane client (useful for tests).
	// If nil, one is selected based on Backend.
	PaneClient PaneClient

	// CompactionReminderEnabled enables auto-injection of reminder after compaction.
	CompactionReminderEnabled bool

	// CompactionReminderPrompt is the text to inject after compaction is detected.
	// Default: "Reread AGENTS.md so it's still fresh in your mind."
	CompactionReminderPrompt string

	// CompactionReminderCooldown is the minimum time between reminder injections per pane.
	// This prevents spam if compaction is detected repeatedly.
	// Default: 10m
	CompactionReminderCooldown time.Duration

	// CompactionReminderRegex allows a custom regex pattern for compaction detection.
	// If nil, uses the default Patterns.CompactingBanner.
	CompactionReminderRegex *regexp.Regexp
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Backend:                    BackendAuto, // Try WezTerm first, fall back to tmux
		PollInterval:               500 * time.Millisecond,
		AuthTimeout:                5 * time.Minute,
		StateTimeout:               30 * time.Second,
		OutputLines:                100,
		ResumePrompt:               "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		LocalAgentURL:              "http://localhost:7890",
		LoginCooldown:              5 * time.Second,
		MethodSelectCooldown:       2 * time.Second,
		ResumeCooldown:             10 * time.Second,
		MaxLoginRetries:            2,
		CompactionReminderEnabled:  false, // Opt-in feature
		CompactionReminderPrompt:   "Reread AGENTS.md so it's still fresh in your mind.\n",
		CompactionReminderCooldown: 10 * time.Minute,
		CompactionReminderRegex:    nil, // Use default Patterns.CompactingBanner
		ResponseReplayWindow:       15 * time.Minute,
	}
}

// FileConfig is the on-disk coordinator configuration. 'caam setup
// distributed' writes it to the remote host and 'caam auth-coordinator
// --config' reads it, so both sides share this one definition.
type FileConfig struct {
	Bind           string `json:"bind,omitempty"`
	Port           int    `json:"port,omitempty"`
	PollInterval   string `json:"poll_interval,omitempty"`
	AuthTimeout    string `json:"auth_timeout,omitempty"`
	StateTimeout   string `json:"state_timeout,omitempty"`
	ResumePrompt   string `json:"resume_prompt,omitempty"`
	ResumeCooldown string `json:"resume_cooldown,omitempty"`
	OutputLines    int    `json:"output_lines,omitempty"`
	Backend        string `json:"backend,omitempty"`
	AuthToken      string `json:"auth_token,omitempty"`
}

// LoadFileConfig reads a FileConfig from path.
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

// Apply overlays the file settings onto base. Unset fields keep base values.
func (fc FileConfig) Apply(base Config) (Config, error) {
	cfg := base
	durations := []struct {
		name  string
		value string
		dst   *time.Duration
	}{
		{"poll_interval", fc.PollInterval, &cfg.PollInterval},
		{"auth_timeout", fc.AuthTimeout, &cfg.AuthTimeout},
		{"state_timeout", fc.StateTimeout, &cfg.StateTimeout},
		{"resume_cooldown", fc.ResumeCooldown, &cfg.ResumeCooldown},
	}
	for _, d := range durations {
		if strings.TrimSpace(d.value) == "" {
			continue
		}
		parsed, err := time.ParseDuration(d.value)
		if err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", d.name, err)
		}
		if parsed <= 0 {
			return Config{}, fmt.Errorf("parse %s: must be positive, got %s", d.name, d.value)
		}
		*d.dst = parsed
	}
	if fc.ResumePrompt != "" {
		cfg.ResumePrompt = fc.ResumePrompt
	}
	if fc.OutputLines < 0 {
		return Config{}, fmt.Errorf("output_lines must not be negative")
	}
	if fc.OutputLines != 0 {
		cfg.OutputLines = fc.OutputLines
	}
	if fc.Backend != "" {
		backend, err := ParseBackend(fc.Backend)
		if err != nil {
			return Config{}, err
		}
		cfg.Backend = backend
	}
	if fc.AuthToken != "" {
		cfg.AuthToken = fc.AuthToken
	}
	return cfg, nil
}

// ParseBackend parses a terminal multiplexer backend name.
func ParseBackend(value string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "wezterm":
		return BackendWezTerm, nil
	case "tmux":
		return BackendTmux, nil
	case "auto", "":
		return BackendAuto, nil
	default:
		return "", fmt.Errorf("invalid backend %q: use wezterm, tmux, or auto", value)
	}
}

// Auth request lifecycle statuses.
const (
	// RequestPending requests are offered to agents via /auth/pending.
	RequestPending = "pending"
	// RequestAccepted requests hold an accepted code awaiting injection and
	// confirmation in the pane.
	RequestAccepted = "accepted"
	// RequestCompleted requests finished with a confirmed login.
	RequestCompleted = "completed"
	// RequestFailed requests ended with an agent error or a rejected login.
	RequestFailed = "failed"
	// RequestExpired requests timed out or lost their pane before an outcome.
	RequestExpired = "expired"
)

// Errors returned by ReceiveAuthResponse. The API maps them to distinct HTTP
// statuses so agents can tell a retryable delivery from a permanent rejection.
var (
	// ErrInvalidAuthResponse means the response carried neither a code nor an
	// error, or carried both.
	ErrInvalidAuthResponse = errors.New("auth response must carry exactly one of code or error")
	// ErrUnknownAuthRequest means the coordinator never issued the request.
	ErrUnknownAuthRequest = errors.New("unknown request")
	// ErrAuthRequestClosed means the request ended before this response arrived.
	ErrAuthRequestClosed = errors.New("auth request is closed")
	// ErrAuthResponseConflict means the request already accepted a different
	// response; accepted codes are never overwritten.
	ErrAuthResponseConflict = errors.New("auth request already accepted a different response")
)

// AuthRequest represents an authentication request issued for a pane.
type AuthRequest struct {
	ID        string    `json:"id"`
	PaneID    int       `json:"pane_id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"` // pending, accepted, completed, failed, expired
	// ClaimedAt is when an agent first fetched the request. AuthTimeout runs
	// from here: until an agent is around, the request simply waits.
	ClaimedAt time.Time `json:"claimed_at,omitzero"`

	// response is the first valid response accepted for this request.
	response *AuthResponse
	closedAt time.Time
}

// AuthResponse contains the result from the local agent.
type AuthResponse struct {
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Account   string `json:"account"`
	Error     string `json:"error,omitempty"`
}

// Coordinator manages pane monitoring and auth recovery.
type Coordinator struct {
	config     Config
	paneClient PaneClient
	logger     *slog.Logger
	trackers   map[int]*PaneTracker // paneID -> tracker
	requests   map[string]*AuthRequest
	closed     map[string]*AuthRequest // recently closed requests, kept for replay
	mu         sync.RWMutex
	stopCh     chan struct{}
	doneCh     chan struct{}
	running    bool
	runID      string // Correlation ID for this coordinator run

	// Callbacks
	OnAuthRequest  func(req *AuthRequest)
	OnAuthComplete func(paneID int, account string)
	OnAuthFailed   func(paneID int, err error)
}

// RedactURL returns a redacted version of a URL for safe logging.
// Only shows the base path, hiding query parameters that may contain sensitive data.
func RedactURL(url string) string {
	if url == "" {
		return ""
	}
	// Find query string start
	if idx := len(url); idx > 0 {
		for i, c := range url {
			if c == '?' {
				return url[:i] + "?[REDACTED]"
			}
		}
	}
	return url
}

// RedactCode returns a redacted auth code for safe logging.
func RedactCode(code string) string {
	if len(code) <= 4 {
		return "[REDACTED]"
	}
	return code[:2] + "..." + code[len(code)-2:]
}

// New creates a new coordinator.
func New(config Config) *Coordinator {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	// Generate a run ID for correlation across all logs from this coordinator instance
	runID := uuid.New().String()[:8]

	// Select pane client based on backend configuration, unless provided
	paneClient := config.PaneClient
	if paneClient == nil {
		paneClient = selectPaneClient(config.Backend, config.Logger)
	}

	// Create logger with run_id for correlation
	logger := config.Logger.With("run_id", runID)

	return &Coordinator{
		config:     config,
		paneClient: paneClient,
		logger:     logger,
		trackers:   make(map[int]*PaneTracker),
		requests:   make(map[string]*AuthRequest),
		closed:     make(map[string]*AuthRequest),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
		runID:      runID,
	}
}

// RunID returns the correlation ID for this coordinator run.
func (c *Coordinator) RunID() string {
	return c.runID
}

// selectPaneClient chooses the appropriate backend based on configuration.
func selectPaneClient(backend Backend, logger *slog.Logger) PaneClient {
	ctx := context.Background()

	switch backend {
	case BackendWezTerm:
		return NewWezTermClient()

	case BackendTmux:
		return NewTmuxClient()

	case BackendAuto:
		fallthrough
	default:
		// WezTerm is preferred; tmux is the fallback. The choice follows
		// whichever is running, so a multiplexer started after the
		// coordinator is picked up.
		auto := newAutoPaneClient(ctx, logger, NewWezTermClient(), NewTmuxClient())
		if auto.IsAvailable(ctx) {
			logger.Info("using terminal multiplexer backend", "backend", auto.Backend())
		} else {
			logger.Warn("no terminal multiplexer running yet; will use WezTerm or tmux once one starts")
		}
		return auto
	}
}

// Start begins the coordinator monitoring loop.
func (c *Coordinator) Start(ctx context.Context) error {
	c.mu.Lock()
	if c.running {
		c.mu.Unlock()
		return fmt.Errorf("coordinator already running")
	}
	c.running = true
	// Recreate channels for this run (in case of restart after Stop)
	c.stopCh = make(chan struct{})
	c.doneCh = make(chan struct{})
	c.mu.Unlock()

	go c.monitorLoop(ctx)
	return nil
}

// Stop halts the coordinator.
func (c *Coordinator) Stop() error {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return nil
	}
	c.running = false
	// Capture channels under lock to prevent TOCTOU race with Start()
	// which might create new channels before we close these
	stopCh := c.stopCh
	doneCh := c.doneCh
	c.mu.Unlock()

	// Close stopCh only once (safe since we checked running flag under lock)
	select {
	case <-stopCh:
		// Already closed
	default:
		close(stopCh)
	}
	<-doneCh
	return nil
}

// monitorLoop is the main polling loop.
func (c *Coordinator) monitorLoop(ctx context.Context) {
	defer close(c.doneCh)

	ticker := time.NewTicker(c.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.pollPanes(ctx)
		}
	}
}

// pollPanes checks all panes for state changes.
func (c *Coordinator) pollPanes(ctx context.Context) {
	panes, err := c.paneClient.ListPanes(ctx)
	if err != nil {
		c.logger.Error("failed to list panes", "error", err)
		return
	}

	// Track which panes we've seen
	seenPanes := make(map[int]bool)

	for _, pane := range panes {
		seenPanes[pane.PaneID] = true

		// Apply filter if configured
		if c.config.PaneFilter != nil && !c.config.PaneFilter(pane) {
			continue
		}

		c.processPaneState(ctx, pane)
	}

	// Clean up trackers for panes that no longer exist. Their open requests
	// can never be delivered, so they stop being offered to agents.
	c.mu.Lock()
	for paneID, tracker := range c.trackers {
		if !seenPanes[paneID] {
			c.logger.Debug("pane disappeared, removing tracker", "pane_id", paneID)
			if requestID := tracker.GetRequestID(); requestID != "" {
				c.closeRequestLocked(requestID, RequestExpired)
			}
			delete(c.trackers, paneID)
		}
	}
	c.pruneClosedLocked(time.Now())
	c.mu.Unlock()
}

// processPaneState handles state transitions for a single pane.
func (c *Coordinator) processPaneState(ctx context.Context, pane Pane) {
	c.mu.Lock()
	tracker, exists := c.trackers[pane.PaneID]
	if !exists {
		tracker = NewPaneTracker(pane.PaneID)
		c.trackers[pane.PaneID] = tracker
	}
	c.mu.Unlock()

	// Get pane output
	output, err := c.paneClient.GetText(ctx, pane.PaneID, -c.config.OutputLines)
	if err != nil {
		c.logger.Debug("failed to get pane text", "pane_id", pane.PaneID, "error", err)
		return
	}

	currentState := tracker.GetState()
	outputChanged := false

	tracker.mu.Lock()
	if output != tracker.LastOutput {
		tracker.LastOutput = output
		outputChanged = true
	}
	tracker.LastCheck = time.Now()
	tracker.mu.Unlock()

	if !outputChanged && currentState == StateIdle {
		return
	}

	// Handle state-specific logic
	switch currentState {
	case StateIdle:
		c.handleIdleState(ctx, tracker, output)

	case StateRateLimited:
		c.handleRateLimitedState(ctx, tracker, output)

	case StateAwaitingMethodSelect:
		c.handleAwaitingMethodSelectState(ctx, tracker, output)

	case StateAwaitingURL:
		c.handleAwaitingURLState(ctx, tracker, output)

	case StateAuthPending:
		c.handleAuthPendingState(ctx, tracker, output)

	case StateCodeReceived:
		c.handleCodeReceivedState(ctx, tracker, output)

	case StateAwaitingConfirm:
		c.handleAwaitingConfirmState(ctx, tracker, output)

	case StateResuming:
		c.handleResumingState(ctx, tracker, output)

	case StateFailed:
		// After the failure has been visible for StateTimeout, retry the
		// login (a slow agent or an expired code is often transient) up
		// to MaxLoginRetries times per rate-limit episode, then leave the
		// pane to a human.
		if tracker.TimeSinceStateChange() > c.config.StateTimeout {
			c.closeRequest(tracker.GetRequestID(), RequestFailed)
			c.retryOrGiveUp(ctx, tracker, output)
		}
	}
}

// retryOrGiveUp re-injects /login into a failed pane while its retry budget
// lasts, otherwise returns it to IDLE.
func (c *Coordinator) retryOrGiveUp(ctx context.Context, tracker *PaneTracker, output string) {
	retries := tracker.GetRetryCount()
	if retries >= c.config.MaxLoginRetries {
		c.logger.Warn("login retries exhausted; leaving pane for manual recovery",
			"pane_id", tracker.PaneID,
			"retries", retries,
			"action", "give_up")
		tracker.Reset()
		tracker.SetGaveUp(true)
		return
	}

	if err := sendKeys(ctx, c.paneClient, tracker.PaneID, LoginKeys(output)); err != nil {
		c.logger.Error("injection failed",
			"pane_id", tracker.PaneID,
			"state", StateFailed.String(),
			"inject_type", "login_retry",
			"error", err,
			"action", "inject_failed")
		tracker.Reset()
		return
	}

	tracker.Reset()
	tracker.SetRetryCount(retries + 1)
	tracker.SetState(StateRateLimited)
	tracker.SetCooldown("login", c.config.LoginCooldown)
	c.logger.Info("retrying login after failure",
		"pane_id", tracker.PaneID,
		"retry", retries+1,
		"max_retries", c.config.MaxLoginRetries,
		"action", "login_retry")
}

// bottomLines is how much of the end of a pane counts as "on screen" for
// adopting a login in progress.
const bottomLines = 12

// atBottom reports whether re matches within the last bottomLines non-empty
// lines of output.
func atBottom(output string, re *regexp.Regexp) bool {
	lines := strings.Split(StripANSI(output), "\n")
	var tail []string
	for i := len(lines) - 1; i >= 0 && len(tail) < bottomLines; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			tail = append(tail, lines[i])
		}
	}
	for _, line := range tail {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func (c *Coordinator) handleIdleState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, metadata := DetectState(output)

	if detected == StateRateLimited {
		c.logger.Info("state transition",
			"pane_id", tracker.PaneID,
			"from_state", StateIdle.String(),
			"to_state", StateRateLimited.String(),
			"reason", "rate_limit_detected",
			"reset_time", metadata["reset_time"],
			"action", "transition")
		tracker.SetState(StateRateLimited)
		// A new rate-limit episode gets a fresh retry budget.
		tracker.SetRetryCount(0)
		tracker.SetGaveUp(false)

		// Check login cooldown before injecting
		if tracker.IsOnCooldown("login") {
			c.logger.Debug("action blocked by cooldown",
				"pane_id", tracker.PaneID,
				"state", StateRateLimited.String(),
				"blocked_action", "login_inject",
				"cooldown_remaining", tracker.CooldownRemaining("login"),
				"action", "cooldown_skip")
			return
		}

		// Auto-inject /login, closing the usage-limit menu first if it is open.
		if RateLimitMenuOpen(output) {
			c.logger.Info("closing usage-limit menu before /login",
				"pane_id", tracker.PaneID,
				"action", "dismiss_menu")
		}
		if err := sendKeys(ctx, c.paneClient, tracker.PaneID, LoginKeys(output)); err != nil {
			c.logger.Error("injection failed",
				"pane_id", tracker.PaneID,
				"state", StateRateLimited.String(),
				"inject_type", "login_command",
				"error", err,
				"action", "inject_failed")
		} else {
			c.logger.Debug("injection succeeded",
				"pane_id", tracker.PaneID,
				"state", StateRateLimited.String(),
				"inject_type", "login_command",
				"cooldown_set", c.config.LoginCooldown,
				"action", "inject_success")
			tracker.SetCooldown("login", c.config.LoginCooldown)
		}
		return // Don't check for compaction if rate limited
	}

	// A login in progress that nothing is driving: the coordinator was
	// restarted mid-flow (an upgrade, say), or someone typed /login. Pick
	// it up where it stands, unless the pane was deliberately left for
	// manual recovery this episode.
	// The prompt must be on screen now, not just somewhere in scrollback:
	// adopting a finished login would type a code into a working session.
	if !tracker.HasGivenUp() {
		switch {
		case detected == StateAwaitingMethodSelect && atBottom(output, Patterns.SelectMethod):
			c.logger.Info("adopting login in progress",
				"pane_id", tracker.PaneID,
				"to_state", StateAwaitingMethodSelect.String(),
				"action", "adopt")
			tracker.SetState(StateAwaitingMethodSelect)
			return
		case detected == StateAwaitingURL && metadata["oauth_url"] != "" && atBottom(output, Patterns.PastePrompt):
			c.logger.Info("adopting login in progress",
				"pane_id", tracker.PaneID,
				"to_state", StateAwaitingURL.String(),
				"url_redacted", RedactURL(metadata["oauth_url"]),
				"action", "adopt")
			tracker.SetOAuthURL(metadata["oauth_url"])
			tracker.SetState(StateAwaitingURL)
			return
		}
	}

	// Check for compaction reminder (only if enabled and not rate limited)
	c.handleCompactionReminder(ctx, tracker, output)
}

// handleCompactionReminder checks for Claude's compaction banner and injects a reminder.
// This is called from handleIdleState when the pane is not in a rate-limited state.
func (c *Coordinator) handleCompactionReminder(ctx context.Context, tracker *PaneTracker, output string) {
	// Skip if feature is disabled
	if !c.config.CompactionReminderEnabled {
		return
	}

	// Detect compaction banner using configured or default pattern
	compacted, matchedText := DetectCompactingBannerWithPattern(output, c.config.CompactionReminderRegex)
	if !compacted {
		return
	}

	c.logger.Debug("compaction banner detected",
		"pane_id", tracker.PaneID,
		"state", StateIdle.String(),
		"matched_text", matchedText,
		"action", "compaction_detected")

	// Check if reminder already appears in recent output (prevent duplicate injection)
	if c.config.CompactionReminderPrompt != "" && strings.Contains(output, strings.TrimSpace(c.config.CompactionReminderPrompt)) {
		c.logger.Debug("action skipped - reminder already present",
			"pane_id", tracker.PaneID,
			"state", StateIdle.String(),
			"blocked_action", "compaction_reminder_inject",
			"reason", "reminder_already_in_output",
			"action", "compaction_skip")
		return
	}

	// Check per-pane cooldown to prevent spam
	if tracker.IsOnCooldown("compaction") {
		c.logger.Debug("action blocked by cooldown",
			"pane_id", tracker.PaneID,
			"state", StateIdle.String(),
			"blocked_action", "compaction_reminder_inject",
			"cooldown_remaining", tracker.CooldownRemaining("compaction"),
			"action", "cooldown_skip")
		return
	}

	// Inject the reminder prompt
	prompt := c.config.CompactionReminderPrompt
	if !strings.HasSuffix(prompt, "\n") {
		prompt += "\n"
	}

	c.logger.Info("compaction reminder injection starting",
		"pane_id", tracker.PaneID,
		"state", StateIdle.String(),
		"action", "inject_compaction_reminder")

	if err := c.paneClient.SendText(ctx, tracker.PaneID, prompt, true); err != nil {
		c.logger.Error("injection failed",
			"pane_id", tracker.PaneID,
			"state", StateIdle.String(),
			"inject_type", "compaction_reminder",
			"error", err,
			"action", "inject_failed")
		return
	}

	// Set cooldown to prevent repeated injections
	tracker.SetCooldown("compaction", c.config.CompactionReminderCooldown)
	c.logger.Debug("injection succeeded",
		"pane_id", tracker.PaneID,
		"state", StateIdle.String(),
		"inject_type", "compaction_reminder",
		"cooldown_set", c.config.CompactionReminderCooldown,
		"action", "inject_success")
}

func (c *Coordinator) handleRateLimitedState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, _ := DetectState(output)

	switch detected {
	case StateAwaitingMethodSelect:
		c.logger.Debug("state transition",
			"pane_id", tracker.PaneID,
			"from_state", StateRateLimited.String(),
			"to_state", StateAwaitingMethodSelect.String(),
			"reason", "method_select_prompt_detected",
			"action", "transition")
		tracker.SetState(StateAwaitingMethodSelect)

		// Check method select cooldown before injecting; the
		// AwaitingMethodSelect handler sends once it has passed.
		if tracker.IsOnCooldown("method_select") {
			c.logger.Debug("action blocked by cooldown",
				"pane_id", tracker.PaneID,
				"state", StateAwaitingMethodSelect.String(),
				"blocked_action", "method_select_inject",
				"cooldown_remaining", tracker.CooldownRemaining("method_select"),
				"action", "cooldown_skip")
			return
		}
		c.sendMethodSelect(ctx, tracker, output)

	case StateAwaitingURL:
		// Skip method select, URL shown directly
		url := ExtractOAuthURL(output)
		if url != "" {
			tracker.SetOAuthURL(url)
			tracker.SetState(StateAwaitingURL)
			c.logger.Info("state transition",
				"pane_id", tracker.PaneID,
				"from_state", StateRateLimited.String(),
				"to_state", StateAwaitingURL.String(),
				"reason", "oauth_url_detected_skip_method",
				"url_redacted", RedactURL(url),
				"action", "transition")
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("state timeout",
			"pane_id", tracker.PaneID,
			"state", StateRateLimited.String(),
			"timeout_duration", c.config.StateTimeout,
			"action", "timeout_reset")
		tracker.Reset()
	}
}

// maxSelectSends bounds login-method selections per cycle.
const maxSelectSends = 3

// sendMethodSelect chooses option 1 (Claude account with subscription) in
// the login-method menu. Claude Code's menus act on a digit at once, so "1"
// goes alone: an Enter after it would land on the next screen, the OAuth code
// prompt. If the menu is still showing on a later attempt with option 1
// highlighted, Enter confirms it (for a menu where digits only move the
// highlight).
func (c *Coordinator) sendMethodSelect(ctx context.Context, tracker *PaneTracker, output string) {
	time.Sleep(200 * time.Millisecond)
	sends := tracker.CountSelectSend()
	key := "1"
	if sends > 1 && atBottom(output, Patterns.OptionOne) {
		key = "\n"
	}
	if err := c.paneClient.SendText(ctx, tracker.PaneID, key, true); err != nil {
		c.logger.Error("injection failed",
			"pane_id", tracker.PaneID,
			"state", StateAwaitingMethodSelect.String(),
			"inject_type", "subscription_select",
			"error", err,
			"action", "inject_failed")
		return
	}
	c.logger.Debug("injection succeeded",
		"pane_id", tracker.PaneID,
		"state", StateAwaitingMethodSelect.String(),
		"inject_type", "subscription_select",
		"attempt", sends,
		"cooldown_set", c.config.MethodSelectCooldown,
		"action", "inject_success")
	tracker.SetCooldown("method_select", c.config.MethodSelectCooldown)
}

func (c *Coordinator) handleAwaitingMethodSelectState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, metadata := DetectState(output)

	// The menu still showing once the cooldown has passed means the
	// selection keystroke was lost (sent while the menu was rendering, or
	// never sent because of a cooldown); select again, a bounded number of
	// times, instead of waiting out the state timeout and stalling.
	if detected == StateAwaitingMethodSelect && !tracker.IsOnCooldown("method_select") &&
		tracker.GetSelectSends() < maxSelectSends {
		c.sendMethodSelect(ctx, tracker, output)
		return
	}

	if detected == StateAwaitingURL {
		url := metadata["oauth_url"]
		if url == "" {
			url = ExtractOAuthURL(output)
		}
		if url != "" {
			tracker.SetOAuthURL(url)
			tracker.SetState(StateAwaitingURL)
			c.logger.Info("state transition",
				"pane_id", tracker.PaneID,
				"from_state", StateAwaitingMethodSelect.String(),
				"to_state", StateAwaitingURL.String(),
				"reason", "oauth_url_detected",
				"url_redacted", RedactURL(url),
				"action", "transition")
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("state timeout",
			"pane_id", tracker.PaneID,
			"state", StateAwaitingMethodSelect.String(),
			"timeout_duration", c.config.StateTimeout,
			"action", "timeout_reset")
		tracker.Reset()
	}
}

func (c *Coordinator) handleAwaitingURLState(ctx context.Context, tracker *PaneTracker, output string) {
	// Extract URL if not already have it
	oauthURL := tracker.GetOAuthURL()
	if oauthURL == "" {
		url := ExtractOAuthURL(output)
		if url != "" {
			tracker.SetOAuthURL(url)
			oauthURL = url
		}
	}

	if oauthURL != "" && tracker.GetRequestID() == "" {
		// Create auth request for local agent
		req := &AuthRequest{
			ID:        uuid.New().String(),
			PaneID:    tracker.PaneID,
			URL:       oauthURL,
			CreatedAt: time.Now(),
			Status:    RequestPending,
		}

		c.mu.Lock()
		c.requests[req.ID] = req
		c.mu.Unlock()

		tracker.SetRequestID(req.ID)
		tracker.SetState(StateAuthPending)

		c.logger.Info("auth request created",
			"pane_id", tracker.PaneID,
			"request_id", req.ID,
			"from_state", StateAwaitingURL.String(),
			"to_state", StateAuthPending.String(),
			"url_redacted", RedactURL(oauthURL),
			"action", "auth_request_created")

		if c.OnAuthRequest != nil {
			snapshot := *req
			c.OnAuthRequest(&snapshot)
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("state timeout",
			"pane_id", tracker.PaneID,
			"state", StateAwaitingURL.String(),
			"timeout_duration", c.config.StateTimeout,
			"action", "timeout_reset")
		tracker.Reset()
	}
}

// agentWaitLogInterval spaces the warnings about a request no agent fetched.
const agentWaitLogInterval = 10 * time.Minute

func (c *Coordinator) handleAuthPendingState(ctx context.Context, tracker *PaneTracker, output string) {
	// Check if we received a code
	if tracker.GetReceivedCode() != "" {
		c.logger.Debug("state transition",
			"pane_id", tracker.PaneID,
			"from_state", StateAuthPending.String(),
			"to_state", StateCodeReceived.String(),
			"reason", "auth_code_received",
			"request_id", tracker.GetRequestID(),
			"action", "transition")
		tracker.SetState(StateCodeReceived)
		return
	}

	// Only an agent can complete the request, and the pane waits at its
	// paste prompt, so the timeout runs from when an agent claimed it. While
	// no agent is around (its machine asleep, say) the request waits rather
	// than spending the pane's retry budget.
	claimed := c.requestClaimedAt(tracker.GetRequestID())
	if claimed.IsZero() {
		if tracker.TimeSinceStateChange() > c.config.AuthTimeout && !tracker.IsOnCooldown("agent_wait") {
			c.logger.Warn("no auth agent has fetched the request; still waiting",
				"pane_id", tracker.PaneID,
				"state", StateAuthPending.String(),
				"request_id", tracker.GetRequestID(),
				"waiting", tracker.TimeSinceStateChange().Round(time.Second),
				"action", "await_agent")
			tracker.SetCooldown("agent_wait", agentWaitLogInterval)
		}
		return
	}

	// Check auth timeout
	if time.Since(claimed) > c.config.AuthTimeout {
		// A response accepted concurrently wins over the timeout; the next
		// poll moves the pane to CODE_RECEIVED.
		if !c.expireUnansweredRequest(tracker.GetRequestID()) {
			return
		}

		c.logger.Warn("auth timeout",
			"pane_id", tracker.PaneID,
			"state", StateAuthPending.String(),
			"request_id", tracker.GetRequestID(),
			"timeout_duration", c.config.AuthTimeout,
			"action", "auth_timeout")

		tracker.SetErrorMessage("auth timeout")
		tracker.SetState(StateFailed)

		if c.OnAuthFailed != nil {
			c.OnAuthFailed(tracker.PaneID, fmt.Errorf("auth timeout after %v", c.config.AuthTimeout))
		}
	}
}

func (c *Coordinator) handleCodeReceivedState(ctx context.Context, tracker *PaneTracker, output string) {
	code := tracker.GetReceivedCode()
	if code == "" {
		c.logger.Error("invalid state",
			"pane_id", tracker.PaneID,
			"state", StateCodeReceived.String(),
			"reason", "code_missing",
			"action", "transition_to_failed")
		tracker.SetState(StateFailed)
		return
	}

	// Inject the code
	c.logger.Info("code injection starting",
		"pane_id", tracker.PaneID,
		"state", StateCodeReceived.String(),
		"account", tracker.GetUsedAccount(),
		"code_redacted", RedactCode(code),
		"request_id", tracker.GetRequestID(),
		"action", "inject_code")

	if err := c.paneClient.SendText(ctx, tracker.PaneID, code+"\n", true); err != nil {
		c.logger.Error("injection failed",
			"pane_id", tracker.PaneID,
			"state", StateCodeReceived.String(),
			"inject_type", "auth_code",
			"error", err,
			"action", "inject_failed")
		tracker.SetErrorMessage(err.Error())
		tracker.SetState(StateFailed)
		return
	}

	c.logger.Debug("state transition",
		"pane_id", tracker.PaneID,
		"from_state", StateCodeReceived.String(),
		"to_state", StateAwaitingConfirm.String(),
		"reason", "code_injected",
		"action", "transition")
	tracker.SetState(StateAwaitingConfirm)
}

func (c *Coordinator) handleAwaitingConfirmState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, _ := DetectState(output)

	switch detected {
	case StateResuming:
		c.logger.Info("state transition",
			"pane_id", tracker.PaneID,
			"from_state", StateAwaitingConfirm.String(),
			"to_state", StateResuming.String(),
			"reason", "login_success_detected",
			"account", tracker.GetUsedAccount(),
			"request_id", tracker.GetRequestID(),
			"action", "transition")
		tracker.SetState(StateResuming)

	case StateFailed:
		c.logger.Error("login verification failed",
			"pane_id", tracker.PaneID,
			"state", StateAwaitingConfirm.String(),
			"request_id", tracker.GetRequestID(),
			"action", "transition_to_failed")
		c.closeRequest(tracker.GetRequestID(), RequestFailed)
		tracker.SetState(StateFailed)

		if c.OnAuthFailed != nil {
			c.OnAuthFailed(tracker.PaneID, fmt.Errorf("login failed"))
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("state timeout",
			"pane_id", tracker.PaneID,
			"state", StateAwaitingConfirm.String(),
			"request_id", tracker.GetRequestID(),
			"timeout_duration", c.config.StateTimeout,
			"action", "timeout_failed")

		c.closeRequest(tracker.GetRequestID(), RequestExpired)
		tracker.SetErrorMessage("confirmation timeout")
		tracker.SetState(StateFailed)
	}
}

// continueSettle is how long the session gets to return to its prompt after
// the post-login screen is dismissed.
const continueSettle = 2 * time.Second

func (c *Coordinator) handleResumingState(ctx context.Context, tracker *PaneTracker, output string) {
	// Claude Code confirms a login with "Login successful. Press Enter to
	// continue"; text typed on that screen is dropped (its newline only
	// dismisses it), so dismiss it first, once per cycle.
	if Patterns.PressEnter.MatchString(StripANSI(output)) && !tracker.MarkContinueSent() {
		if err := c.paneClient.SendText(ctx, tracker.PaneID, "\n", true); err != nil {
			c.logger.Error("injection failed",
				"pane_id", tracker.PaneID,
				"state", StateResuming.String(),
				"inject_type", "continue",
				"error", err,
				"action", "inject_failed")
			return
		}
		tracker.SetCooldown("continue", continueSettle)
		c.logger.Info("dismissed post-login screen",
			"pane_id", tracker.PaneID,
			"state", StateResuming.String(),
			"request_id", tracker.GetRequestID(),
			"action", "inject_continue")
		return
	}
	if tracker.IsOnCooldown("continue") {
		return
	}

	// Check resume cooldown to prevent duplicate injections
	if tracker.IsOnCooldown("resume") {
		c.logger.Debug("action blocked by cooldown",
			"pane_id", tracker.PaneID,
			"state", StateResuming.String(),
			"blocked_action", "resume_prompt_inject",
			"cooldown_remaining", tracker.CooldownRemaining("resume"),
			"action", "cooldown_skip")
		return
	}

	// Inject resume prompt
	c.logger.Info("resume prompt injection starting",
		"pane_id", tracker.PaneID,
		"state", StateResuming.String(),
		"request_id", tracker.GetRequestID(),
		"account", tracker.GetUsedAccount(),
		"action", "inject_resume")

	time.Sleep(500 * time.Millisecond)
	if err := c.paneClient.SendText(ctx, tracker.PaneID, c.config.ResumePrompt, true); err != nil {
		c.logger.Error("injection failed",
			"pane_id", tracker.PaneID,
			"state", StateResuming.String(),
			"inject_type", "resume_prompt",
			"error", err,
			"action", "inject_failed")
		return
	}

	// Set cooldown to prevent duplicate injections
	tracker.SetCooldown("resume", c.config.ResumeCooldown)
	c.logger.Debug("injection succeeded",
		"pane_id", tracker.PaneID,
		"state", StateResuming.String(),
		"inject_type", "resume_prompt",
		"cooldown_set", c.config.ResumeCooldown,
		"action", "inject_success")

	// Mark request complete
	requestID := tracker.GetRequestID()
	c.closeRequest(requestID, RequestCompleted)

	c.logger.Info("auth cycle complete",
		"pane_id", tracker.PaneID,
		"from_state", StateResuming.String(),
		"to_state", StateIdle.String(),
		"request_id", requestID,
		"account", tracker.GetUsedAccount(),
		"action", "auth_complete")

	if c.OnAuthComplete != nil {
		c.OnAuthComplete(tracker.PaneID, tracker.GetUsedAccount())
	}

	// Reset for next cycle
	tracker.Reset()
}

// ReceiveAuthResponse processes a response from the local agent.
//
// Acceptance is replay-safe: the first valid response for a request is
// stored and never overwritten. Redelivering that same response (an agent
// retrying after a lost acknowledgement) succeeds without side effects, even
// after the request has closed, while a different response is rejected with
// ErrAuthResponseConflict.
func (c *Coordinator) ReceiveAuthResponse(resp AuthResponse) error {
	resp.RequestID = strings.TrimSpace(resp.RequestID)
	resp.Code = strings.TrimSpace(resp.Code)
	resp.Account = strings.TrimSpace(resp.Account)
	resp.Error = strings.TrimSpace(resp.Error)
	if resp.RequestID == "" || (resp.Code == "") == (resp.Error == "") {
		return ErrInvalidAuthResponse
	}

	c.mu.Lock()
	req, open := c.requests[resp.RequestID]
	if !open {
		closed, known := c.closed[resp.RequestID]
		c.mu.Unlock()
		if !known {
			c.logger.Warn("unknown auth response",
				"request_id", resp.RequestID,
				"reason", "request_not_found",
				"action", "response_rejected")
			return fmt.Errorf("%w: %s", ErrUnknownAuthRequest, resp.RequestID)
		}
		return c.replayResult(closed, resp)
	}
	if req.response != nil {
		c.mu.Unlock()
		return c.replayResult(req, resp)
	}

	tracker := c.trackers[req.PaneID]
	if tracker == nil || tracker.GetRequestID() != resp.RequestID || tracker.GetState() != StateAuthPending {
		c.closeRequestLocked(resp.RequestID, RequestExpired)
		c.mu.Unlock()
		c.logger.Warn("auth response for inactive pane",
			"request_id", resp.RequestID,
			"pane_id", req.PaneID,
			"reason", "pane_not_awaiting_code",
			"action", "response_rejected")
		return fmt.Errorf("%w: %s: pane %d is no longer awaiting a code", ErrAuthRequestClosed, resp.RequestID, req.PaneID)
	}

	accepted := resp
	req.response = &accepted
	if resp.Error != "" {
		tracker.SetErrorMessage(resp.Error)
		tracker.SetState(StateFailed)
		c.closeRequestLocked(resp.RequestID, RequestFailed)
	} else {
		req.Status = RequestAccepted
		// The pane moves to CODE_RECEIVED on the next poll.
		tracker.SetAuthResponse(resp.Code, resp.Account)
	}
	c.mu.Unlock()

	if resp.Error != "" {
		c.logger.Error("auth response error received",
			"pane_id", tracker.PaneID,
			"request_id", resp.RequestID,
			"error", resp.Error,
			"action", "transition_to_failed")
		if c.OnAuthFailed != nil {
			c.OnAuthFailed(tracker.PaneID, fmt.Errorf("%s", resp.Error))
		}
		return nil
	}

	c.logger.Info("auth code received from agent",
		"pane_id", tracker.PaneID,
		"request_id", resp.RequestID,
		"account", resp.Account,
		"code_redacted", RedactCode(resp.Code),
		"action", "code_stored")
	return nil
}

// replayResult answers a response for a request that already accepted one, or
// that closed without one.
func (c *Coordinator) replayResult(req *AuthRequest, resp AuthResponse) error {
	c.mu.RLock()
	accepted := req.response
	status := req.Status
	c.mu.RUnlock()

	if accepted == nil {
		c.logger.Warn("auth response for closed request",
			"request_id", resp.RequestID,
			"status", status,
			"action", "response_rejected")
		return fmt.Errorf("%w: %s (%s)", ErrAuthRequestClosed, resp.RequestID, status)
	}
	if *accepted == resp {
		c.logger.Debug("duplicate auth response acknowledged",
			"request_id", resp.RequestID,
			"status", status,
			"action", "response_replayed")
		return nil
	}
	c.logger.Warn("conflicting auth response",
		"request_id", resp.RequestID,
		"status", status,
		"action", "response_rejected")
	return fmt.Errorf("%w: %s", ErrAuthResponseConflict, resp.RequestID)
}

// GetPendingRequests returns snapshots of the requests awaiting an agent,
// oldest first.
func (c *Coordinator) GetPendingRequests() []*AuthRequest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.pendingSnapshotsLocked()
}

// ClaimPendingRequests returns the pending requests for an agent to work on
// and records when each was first claimed, which starts its AuthTimeout.
func (c *Coordinator) ClaimPendingRequests() []*AuthRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	for _, req := range c.requests {
		if req.Status == RequestPending && req.ClaimedAt.IsZero() {
			req.ClaimedAt = now
		}
	}
	return c.pendingSnapshotsLocked()
}

// requestClaimedAt returns when an agent first claimed the request (zero if
// no agent has).
func (c *Coordinator) requestClaimedAt(requestID string) time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if req, ok := c.requests[requestID]; ok {
		return req.ClaimedAt
	}
	return time.Time{}
}

// pendingSnapshotsLocked returns copies of the pending requests, oldest
// first. Callers must hold c.mu.
func (c *Coordinator) pendingSnapshotsLocked() []*AuthRequest {
	pending := make([]*AuthRequest, 0, len(c.requests))
	for _, req := range c.requests {
		if req.Status == RequestPending {
			snapshot := *req
			snapshot.response = nil
			pending = append(pending, &snapshot)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].ID < pending[j].ID
		}
		return pending[i].CreatedAt.Before(pending[j].CreatedAt)
	})
	return pending
}

// GetStatus returns the current status of all tracked panes.
func (c *Coordinator) GetStatus() map[int]PaneState {
	c.mu.RLock()
	defer c.mu.RUnlock()

	status := make(map[int]PaneState)
	for paneID, tracker := range c.trackers {
		status[paneID] = tracker.GetState()
	}
	return status
}

// GetTrackers returns all pane trackers (for status display).
func (c *Coordinator) GetTrackers() []*PaneTracker {
	c.mu.RLock()
	defer c.mu.RUnlock()

	trackers := make([]*PaneTracker, 0, len(c.trackers))
	for _, t := range c.trackers {
		trackers = append(trackers, t)
	}
	return trackers
}

// Backend returns the name of the active terminal multiplexer backend.
// Returns "wezterm" (preferred) or "tmux" (fallback).
func (c *Coordinator) Backend() string {
	return c.paneClient.Backend()
}

// closeRequest ends a request with the given status.
func (c *Coordinator) closeRequest(requestID, status string) {
	if requestID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closeRequestLocked(requestID, status)
}

// closeRequestLocked moves an open request to the closed set, where it stays
// for the replay window so redelivered responses are answered consistently.
// Callers must hold c.mu.
func (c *Coordinator) closeRequestLocked(requestID, status string) {
	req, ok := c.requests[requestID]
	if !ok {
		return
	}
	delete(c.requests, requestID)
	req.Status = status
	req.closedAt = time.Now()
	if c.config.ResponseReplayWindow > 0 {
		c.closed[requestID] = req
	}
}

// expireUnansweredRequest closes a request as expired unless a response was
// already accepted for it. It reports whether the request expired.
func (c *Coordinator) expireUnansweredRequest(requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if req, ok := c.requests[requestID]; ok && req.response != nil {
		return false
	}
	c.closeRequestLocked(requestID, RequestExpired)
	return true
}

// pruneClosedLocked forgets closed requests older than the replay window.
// Callers must hold c.mu.
func (c *Coordinator) pruneClosedLocked(now time.Time) {
	for id, req := range c.closed {
		if now.Sub(req.closedAt) > c.config.ResponseReplayWindow {
			delete(c.closed, id)
		}
	}
}
