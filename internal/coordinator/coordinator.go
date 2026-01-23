package coordinator

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
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

	// AuthTimeout is how long to wait for auth completion.
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

	// LoginCooldown is the minimum time between /login injections per pane.
	LoginCooldown time.Duration

	// MethodSelectCooldown is the minimum time between method selection injections per pane.
	MethodSelectCooldown time.Duration

	// ResumeCooldown is the minimum time between resume prompt injections per pane.
	// This prevents duplicate resume prompts if the state detection triggers multiple times.
	ResumeCooldown time.Duration

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
		AuthTimeout:                60 * time.Second,
		StateTimeout:               30 * time.Second,
		OutputLines:                100,
		ResumePrompt:               "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		LocalAgentURL:              "http://localhost:7890",
		LoginCooldown:              5 * time.Second,
		MethodSelectCooldown:       2 * time.Second,
		ResumeCooldown:             10 * time.Second,
		CompactionReminderEnabled:  false, // Opt-in feature
		CompactionReminderPrompt:   "Reread AGENTS.md so it's still fresh in your mind.\n",
		CompactionReminderCooldown: 10 * time.Minute,
		CompactionReminderRegex:    nil, // Use default Patterns.CompactingBanner
	}
}

// AuthRequest represents a pending authentication request.
type AuthRequest struct {
	ID        string    `json:"id"`
	PaneID    int       `json:"pane_id"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
	Status    string    `json:"status"` // pending, processing, completed, failed
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
		// Try WezTerm first (preferred)
		wezterm := NewWezTermClient()
		if wezterm.IsAvailable(ctx) {
			logger.Info("using WezTerm backend (preferred)")
			return wezterm
		}

		// Fall back to tmux
		tmux := NewTmuxClient()
		if tmux.IsAvailable(ctx) {
			logger.Info("WezTerm not available, using tmux backend",
				"note", "WezTerm is recommended for better integration")
			return tmux
		}

		// Neither available - return WezTerm anyway, errors will surface later
		logger.Warn("no terminal multiplexer detected",
			"hint", "start WezTerm or tmux before running the coordinator")
		return wezterm
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

	// Clean up trackers for panes that no longer exist
	c.mu.Lock()
	for paneID := range c.trackers {
		if !seenPanes[paneID] {
			c.logger.Debug("pane disappeared, removing tracker", "pane_id", paneID)
			delete(c.trackers, paneID)
		}
	}
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
		// Check for timeout and reset
		if tracker.TimeSinceStateChange() > c.config.StateTimeout {
			c.logger.Info("resetting failed pane after timeout",
				"pane_id", tracker.PaneID)
			
			c.cleanupRequest(tracker.GetRequestID())
			tracker.Reset()
		}
	}
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

		// Auto-inject /login command
		if err := c.paneClient.SendText(ctx, tracker.PaneID, "/login\n", true); err != nil {
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

		// Check method select cooldown before injecting
		if tracker.IsOnCooldown("method_select") {
			c.logger.Debug("action blocked by cooldown",
				"pane_id", tracker.PaneID,
				"state", StateAwaitingMethodSelect.String(),
				"blocked_action", "method_select_inject",
				"cooldown_remaining", tracker.CooldownRemaining("method_select"),
				"action", "cooldown_skip")
			return
		}

		// Auto-select option 1 (Claude account with subscription)
		time.Sleep(200 * time.Millisecond)
		if err := c.paneClient.SendText(ctx, tracker.PaneID, "1\n", true); err != nil {
			c.logger.Error("injection failed",
				"pane_id", tracker.PaneID,
				"state", StateAwaitingMethodSelect.String(),
				"inject_type", "subscription_select",
				"error", err,
				"action", "inject_failed")
		} else {
			c.logger.Debug("injection succeeded",
				"pane_id", tracker.PaneID,
				"state", StateAwaitingMethodSelect.String(),
				"inject_type", "subscription_select",
				"cooldown_set", c.config.MethodSelectCooldown,
				"action", "inject_success")
			tracker.SetCooldown("method_select", c.config.MethodSelectCooldown)
		}

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

func (c *Coordinator) handleAwaitingMethodSelectState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, metadata := DetectState(output)

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
			Status:    "pending",
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
			c.OnAuthRequest(req)
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

	// Check auth timeout
	if tracker.TimeSinceStateChange() > c.config.AuthTimeout {
		c.logger.Warn("auth timeout",
			"pane_id", tracker.PaneID,
			"state", StateAuthPending.String(),
			"request_id", tracker.GetRequestID(),
			"timeout_duration", c.config.AuthTimeout,
			"action", "auth_timeout")

		c.cleanupRequest(tracker.GetRequestID())
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

		c.cleanupRequest(tracker.GetRequestID())
		tracker.SetErrorMessage("confirmation timeout")
		tracker.SetState(StateFailed)
	}
}

func (c *Coordinator) handleResumingState(ctx context.Context, tracker *PaneTracker, output string) {
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

	// Mark request complete and clean up
	requestID := tracker.GetRequestID()
	c.mu.Lock()
	if req, ok := c.requests[requestID]; ok {
		req.Status = "completed"
		delete(c.requests, requestID)
	}
	c.mu.Unlock()

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
func (c *Coordinator) ReceiveAuthResponse(resp AuthResponse) error {
	c.mu.Lock()
	req, ok := c.requests[resp.RequestID]
	if !ok {
		c.mu.Unlock()
		c.logger.Warn("unknown auth response",
			"request_id", resp.RequestID,
			"reason", "request_not_found",
			"action", "response_rejected")
		return fmt.Errorf("unknown request: %s", resp.RequestID)
	}
	req.Status = "processing"
	c.mu.Unlock()

	// Find tracker for this request
	c.mu.RLock()
	var tracker *PaneTracker
	for _, t := range c.trackers {
		if t.GetRequestID() == resp.RequestID {
			tracker = t
			break
		}
	}
	c.mu.RUnlock()

	if tracker == nil {
		c.logger.Warn("orphaned auth response",
			"request_id", resp.RequestID,
			"reason", "tracker_not_found",
			"action", "response_rejected")
		return fmt.Errorf("no tracker for request: %s", resp.RequestID)
	}

	if resp.Error != "" {
		c.logger.Error("auth response error received",
			"pane_id", tracker.PaneID,
			"request_id", resp.RequestID,
			"state", tracker.GetState().String(),
			"error", resp.Error,
			"action", "transition_to_failed")
		tracker.SetErrorMessage(resp.Error)
		tracker.SetState(StateFailed)

		c.mu.Lock()
		req.Status = "failed"
		delete(c.requests, resp.RequestID)
		c.mu.Unlock()

		if c.OnAuthFailed != nil {
			c.OnAuthFailed(tracker.PaneID, fmt.Errorf("%s", resp.Error))
		}
		return nil
	}

	tracker.SetAuthResponse(resp.Code, resp.Account)
	// State will transition on next poll

	c.logger.Info("auth code received from agent",
		"pane_id", tracker.PaneID,
		"request_id", resp.RequestID,
		"state", tracker.GetState().String(),
		"account", resp.Account,
		"code_redacted", RedactCode(resp.Code),
		"action", "code_stored")

	return nil
}

// GetPendingRequests returns all pending auth requests.
func (c *Coordinator) GetPendingRequests() []*AuthRequest {
	c.mu.RLock()
	defer c.mu.RUnlock()

	var pending []*AuthRequest
	for _, req := range c.requests {
		if req.Status == "pending" {
			pending = append(pending, req)
		}
	}
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

// cleanupRequest removes a request from the tracking map.
func (c *Coordinator) cleanupRequest(requestID string) {
	if requestID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.requests, requestID)
}
