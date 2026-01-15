package coordinator

import (
	"regexp"
	"sync"
	"time"
)

// PaneState represents the authentication state of a monitored pane.
type PaneState int

const (
	// StateIdle - pane is running normally, no rate limit detected.
	StateIdle PaneState = iota
	// StateRateLimited - rate limit message detected, awaiting /login.
	StateRateLimited
	// StateAwaitingMethodSelect - /login was sent, waiting for method selection prompt.
	StateAwaitingMethodSelect
	// StateAwaitingURL - method selected, waiting for OAuth URL to appear.
	StateAwaitingURL
	// StateAuthPending - URL extracted, waiting for auth completion from local agent.
	StateAuthPending
	// StateCodeReceived - code received from local agent, waiting to inject.
	StateCodeReceived
	// StateAwaitingConfirm - code injected, waiting for login confirmation.
	StateAwaitingConfirm
	// StateResuming - auth complete, injecting resume prompt.
	StateResuming
	// StateFailed - auth failed, manual intervention needed.
	StateFailed
)

func (s PaneState) String() string {
	switch s {
	case StateIdle:
		return "IDLE"
	case StateRateLimited:
		return "RATE_LIMITED"
	case StateAwaitingMethodSelect:
		return "AWAITING_METHOD_SELECT"
	case StateAwaitingURL:
		return "AWAITING_URL"
	case StateAuthPending:
		return "AUTH_PENDING"
	case StateCodeReceived:
		return "CODE_RECEIVED"
	case StateAwaitingConfirm:
		return "AWAITING_CONFIRM"
	case StateResuming:
		return "RESUMING"
	case StateFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// PaneTracker tracks the state of a single pane.
type PaneTracker struct {
	PaneID        int
	State         PaneState
	LastCheck     time.Time
	StateEntered  time.Time
	OAuthURL      string
	RequestID     string // ID for auth request
	ReceivedCode  string // Code received from local agent
	UsedAccount   string // Account used for auth
	ErrorMessage  string
	RetryCount    int
	LastOutput    string // Cached output for duplicate detection
	mu            sync.RWMutex
}

// NewPaneTracker creates a tracker for a pane.
func NewPaneTracker(paneID int) *PaneTracker {
	now := time.Now()
	return &PaneTracker{
		PaneID:       paneID,
		State:        StateIdle,
		LastCheck:    now,
		StateEntered: now,
	}
}

// SetState transitions to a new state.
func (t *PaneTracker) SetState(state PaneState) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = state
	t.StateEntered = time.Now()
}

// GetState returns the current state.
func (t *PaneTracker) GetState() PaneState {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.State
}

// TimeSinceStateChange returns duration since last state change.
func (t *PaneTracker) TimeSinceStateChange() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return time.Since(t.StateEntered)
}

// Reset returns tracker to idle state.
func (t *PaneTracker) Reset() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.State = StateIdle
	t.StateEntered = time.Now()
	t.OAuthURL = ""
	t.RequestID = ""
	t.ReceivedCode = ""
	t.UsedAccount = ""
	t.ErrorMessage = ""
}

// Thread-safe accessors for tracker fields

// GetOAuthURL returns the OAuth URL.
func (t *PaneTracker) GetOAuthURL() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.OAuthURL
}

// SetOAuthURL sets the OAuth URL.
func (t *PaneTracker) SetOAuthURL(url string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.OAuthURL = url
}

// GetRequestID returns the request ID.
func (t *PaneTracker) GetRequestID() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.RequestID
}

// SetRequestID sets the request ID.
func (t *PaneTracker) SetRequestID(id string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.RequestID = id
}

// GetReceivedCode returns the received code.
func (t *PaneTracker) GetReceivedCode() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ReceivedCode
}

// SetReceivedCode sets the received code.
func (t *PaneTracker) SetReceivedCode(code string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ReceivedCode = code
}

// GetUsedAccount returns the used account.
func (t *PaneTracker) GetUsedAccount() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.UsedAccount
}

// SetUsedAccount sets the used account.
func (t *PaneTracker) SetUsedAccount(account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.UsedAccount = account
}

// GetErrorMessage returns the error message.
func (t *PaneTracker) GetErrorMessage() string {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.ErrorMessage
}

// SetErrorMessage sets the error message.
func (t *PaneTracker) SetErrorMessage(msg string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ErrorMessage = msg
}

// SetAuthResponse sets the received code and account atomically.
func (t *PaneTracker) SetAuthResponse(code, account string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ReceivedCode = code
	t.UsedAccount = account
}

// Patterns for detecting Claude Code states.
var Patterns = struct {
	RateLimit       *regexp.Regexp
	SelectMethod    *regexp.Regexp
	OAuthURL        *regexp.Regexp
	PastePrompt     *regexp.Regexp
	LoginSuccess    *regexp.Regexp
	LoginFailed     *regexp.Regexp
	OptionOne       *regexp.Regexp
	UsageLimitReset *regexp.Regexp
}{
	// "You've hit your limit · resets 2pm (America/New_York)"
	RateLimit: regexp.MustCompile(`You've hit your limit.*resets`),

	// "Select login method:"
	SelectMethod: regexp.MustCompile(`Select login method:`),

	// OAuth URL: https://claude.ai/oauth/authorize?code=true&...
	OAuthURL: regexp.MustCompile(`https://claude\.ai/oauth/authorize\?[^\s]+`),

	// "Paste code here if prompted >"
	PastePrompt: regexp.MustCompile(`Paste code here if prompted`),

	// "Logged in as user@example.com" or similar success patterns
	LoginSuccess: regexp.MustCompile(`(?i)(logged in as|successfully authenticated|welcome back)`),

	// Login failure patterns
	LoginFailed: regexp.MustCompile(`(?i)(login failed|authentication error|invalid code|expired|error signing)`),

	// "1. Claude account with subscription"
	OptionOne: regexp.MustCompile(`[❯>]\s*1\.\s*Claude account`),

	// Extract reset time from rate limit message
	UsageLimitReset: regexp.MustCompile(`resets\s+(\d+[ap]m)`),
}

// DetectState analyzes pane output and returns the detected state.
func DetectState(output string) (PaneState, map[string]string) {
	metadata := make(map[string]string)

	// Check for login success first (highest priority)
	if Patterns.LoginSuccess.MatchString(output) {
		return StateResuming, metadata
	}

	// Check for login failure
	if Patterns.LoginFailed.MatchString(output) {
		return StateFailed, metadata
	}

	// Check for OAuth URL (implies awaiting URL state)
	if match := Patterns.OAuthURL.FindString(output); match != "" {
		metadata["oauth_url"] = match
		return StateAwaitingURL, metadata
	}

	// Check for paste prompt (means URL was shown, awaiting code)
	if Patterns.PastePrompt.MatchString(output) {
		// Try to extract URL from this output too
		if match := Patterns.OAuthURL.FindString(output); match != "" {
			metadata["oauth_url"] = match
		}
		return StateAwaitingURL, metadata
	}

	// Check for login method selection prompt
	if Patterns.SelectMethod.MatchString(output) {
		return StateAwaitingMethodSelect, metadata
	}

	// Check for rate limit last (lowest priority, as it might be in history)
	if Patterns.RateLimit.MatchString(output) {
		if match := Patterns.UsageLimitReset.FindStringSubmatch(output); len(match) > 1 {
			metadata["reset_time"] = match[1]
		}
		return StateRateLimited, metadata
	}

	return StateIdle, metadata
}

// ExtractOAuthURL finds and returns the OAuth URL from output.
func ExtractOAuthURL(output string) string {
	return Patterns.OAuthURL.FindString(output)
}
