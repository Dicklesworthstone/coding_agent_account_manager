package coordinator

import (
	"context"
	"fmt"
	"log/slog"
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
}

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Backend:       BackendAuto, // Try WezTerm first, fall back to tmux
		PollInterval:  500 * time.Millisecond,
		AuthTimeout:   60 * time.Second,
		StateTimeout:  30 * time.Second,
		OutputLines:   100,
		ResumePrompt:  "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
		LocalAgentURL: "http://localhost:7890",
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

	// Callbacks
	OnAuthRequest  func(req *AuthRequest)
	OnAuthComplete func(paneID int, account string)
	OnAuthFailed   func(paneID int, err error)
}

// New creates a new coordinator.
func New(config Config) *Coordinator {
	if config.Logger == nil {
		config.Logger = slog.Default()
	}

	// Select pane client based on backend configuration
	paneClient := selectPaneClient(config.Backend, config.Logger)

	return &Coordinator{
		config:     config,
		paneClient: paneClient,
		logger:     config.Logger,
		trackers:   make(map[int]*PaneTracker),
		requests:   make(map[string]*AuthRequest),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
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
	c.mu.Unlock()

	// Close stopCh only once (safe since we checked running flag under lock)
	select {
	case <-c.stopCh:
		// Already closed
	default:
		close(c.stopCh)
	}
	<-c.doneCh
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

	// Skip if output hasn't changed
	tracker.mu.Lock()
	if output == tracker.LastOutput {
		tracker.LastCheck = time.Now()
		tracker.mu.Unlock()
		return
	}
	tracker.LastOutput = output
	tracker.mu.Unlock()

	currentState := tracker.GetState()

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
			tracker.Reset()
		}
	}
}

func (c *Coordinator) handleIdleState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, metadata := DetectState(output)

	if detected == StateRateLimited {
		c.logger.Info("rate limit detected",
			"pane_id", tracker.PaneID,
			"reset_time", metadata["reset_time"])
		tracker.SetState(StateRateLimited)

		// Auto-inject /login command
		if err := c.paneClient.SendText(ctx, tracker.PaneID, "/login\n", true); err != nil {
			c.logger.Error("failed to inject /login",
				"pane_id", tracker.PaneID,
				"error", err)
		} else {
			c.logger.Debug("injected /login command", "pane_id", tracker.PaneID)
		}
	}
}

func (c *Coordinator) handleRateLimitedState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, _ := DetectState(output)

	switch detected {
	case StateAwaitingMethodSelect:
		c.logger.Debug("method selection prompt detected", "pane_id", tracker.PaneID)
		tracker.SetState(StateAwaitingMethodSelect)

		// Auto-select option 1 (Claude account with subscription)
		time.Sleep(200 * time.Millisecond)
		if err := c.paneClient.SendText(ctx, tracker.PaneID, "1\n", true); err != nil {
			c.logger.Error("failed to inject option selection",
				"pane_id", tracker.PaneID,
				"error", err)
		}

	case StateAwaitingURL:
		// Skip method select, URL shown directly
		url := ExtractOAuthURL(output)
		if url != "" {
			tracker.SetOAuthURL(url)
			tracker.SetState(StateAwaitingURL)
			c.logger.Info("OAuth URL detected (skip method select)",
				"pane_id", tracker.PaneID)
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("rate limited state timeout, resetting",
			"pane_id", tracker.PaneID)
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
			c.logger.Info("OAuth URL detected", "pane_id", tracker.PaneID)
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("awaiting method select timeout, resetting",
			"pane_id", tracker.PaneID)
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

		c.logger.Info("created auth request",
			"pane_id", tracker.PaneID,
			"request_id", req.ID,
			"url", oauthURL)

		if c.OnAuthRequest != nil {
			c.OnAuthRequest(req)
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("awaiting URL timeout, resetting",
			"pane_id", tracker.PaneID)
		tracker.Reset()
	}
}

func (c *Coordinator) handleAuthPendingState(ctx context.Context, tracker *PaneTracker, output string) {
	// Check if we received a code
	if tracker.GetReceivedCode() != "" {
		tracker.SetState(StateCodeReceived)
		return
	}

	// Check auth timeout
	if tracker.TimeSinceStateChange() > c.config.AuthTimeout {
		c.logger.Warn("auth pending timeout",
			"pane_id", tracker.PaneID,
			"request_id", tracker.GetRequestID())
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
		c.logger.Error("code received state but no code", "pane_id", tracker.PaneID)
		tracker.SetState(StateFailed)
		return
	}

	// Inject the code
	c.logger.Info("injecting auth code",
		"pane_id", tracker.PaneID,
		"account", tracker.GetUsedAccount())

	if err := c.paneClient.SendText(ctx, tracker.PaneID, code+"\n", true); err != nil {
		c.logger.Error("failed to inject code",
			"pane_id", tracker.PaneID,
			"error", err)
		tracker.SetErrorMessage(err.Error())
		tracker.SetState(StateFailed)
		return
	}

	tracker.SetState(StateAwaitingConfirm)
}

func (c *Coordinator) handleAwaitingConfirmState(ctx context.Context, tracker *PaneTracker, output string) {
	detected, _ := DetectState(output)

	switch detected {
	case StateResuming:
		c.logger.Info("login confirmed",
			"pane_id", tracker.PaneID,
			"account", tracker.GetUsedAccount())
		tracker.SetState(StateResuming)

	case StateFailed:
		c.logger.Error("login failed",
			"pane_id", tracker.PaneID)
		tracker.SetState(StateFailed)

		if c.OnAuthFailed != nil {
			c.OnAuthFailed(tracker.PaneID, fmt.Errorf("login failed"))
		}
	}

	// Check timeout
	if tracker.TimeSinceStateChange() > c.config.StateTimeout {
		c.logger.Warn("awaiting confirm timeout",
			"pane_id", tracker.PaneID)
		tracker.SetErrorMessage("confirmation timeout")
		tracker.SetState(StateFailed)
	}
}

func (c *Coordinator) handleResumingState(ctx context.Context, tracker *PaneTracker, output string) {
	// Inject resume prompt
	c.logger.Info("injecting resume prompt",
		"pane_id", tracker.PaneID)

	time.Sleep(500 * time.Millisecond)
	if err := c.paneClient.SendText(ctx, tracker.PaneID, c.config.ResumePrompt, true); err != nil {
		c.logger.Error("failed to inject resume prompt",
			"pane_id", tracker.PaneID,
			"error", err)
	}

	// Mark request complete
	requestID := tracker.GetRequestID()
	c.mu.Lock()
	if req, ok := c.requests[requestID]; ok {
		req.Status = "completed"
	}
	c.mu.Unlock()

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
		return fmt.Errorf("no tracker for request: %s", resp.RequestID)
	}

	if resp.Error != "" {
		c.logger.Error("auth response error",
			"request_id", resp.RequestID,
			"error", resp.Error)
		tracker.SetErrorMessage(resp.Error)
		tracker.SetState(StateFailed)

		c.mu.Lock()
		req.Status = "failed"
		c.mu.Unlock()

		if c.OnAuthFailed != nil {
			c.OnAuthFailed(tracker.PaneID, fmt.Errorf("%s", resp.Error))
		}
		return nil
	}

	tracker.SetAuthResponse(resp.Code, resp.Account)
	// State will transition on next poll

	c.logger.Info("received auth code",
		"request_id", resp.RequestID,
		"account", resp.Account,
		"pane_id", tracker.PaneID)

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
