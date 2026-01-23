package coordinator

import (
	"regexp"
	"sync"
	"time"

	"github.com/charmbracelet/x/ansi"
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
	Cooldowns     map[string]time.Time // action -> cooldown expiry
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
		Cooldowns:    make(map[string]time.Time),
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
	t.Cooldowns = make(map[string]time.Time)
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

// SetCooldown sets a cooldown for an action.
func (t *PaneTracker) SetCooldown(action string, duration time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Cooldowns[action] = time.Now().Add(duration)
}

// IsOnCooldown returns true if an action is on cooldown.
func (t *PaneTracker) IsOnCooldown(action string) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	expiry, ok := t.Cooldowns[action]
	if !ok {
		return false
	}
	return time.Now().Before(expiry)
}

// CooldownRemaining returns the remaining cooldown time for an action.
func (t *PaneTracker) CooldownRemaining(action string) time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	expiry, ok := t.Cooldowns[action]
	if !ok {
		return 0
	}
	remaining := time.Until(expiry)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// ClearCooldown removes a cooldown for an action.
func (t *PaneTracker) ClearCooldown(action string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.Cooldowns, action)
}

// ClearAllCooldowns removes all cooldowns.
func (t *PaneTracker) ClearAllCooldowns() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.Cooldowns = make(map[string]time.Time)
}

// Patterns for detecting Claude Code states.
var Patterns = struct {
	RateLimit        *regexp.Regexp
	SelectMethod     *regexp.Regexp
	OAuthURL         *regexp.Regexp
	PastePrompt      *regexp.Regexp
	LoginSuccess     *regexp.Regexp
	LoginFailed      *regexp.Regexp
	OptionOne        *regexp.Regexp
	UsageLimitReset  *regexp.Regexp
	CompactingBanner *regexp.Regexp
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

	// "Conversation compacted · ctrl+o for history" or similar variants
	// Matches with optional box-drawing characters, middot/bullet separators,
	// and various whitespace. Also handles "Conversation was compacted" variants.
	CompactingBanner: regexp.MustCompile(`(?i)Conversation\s+(was\s+)?compacted[\s·•\-\|]*ctrl\+?o`),
}

// StripANSI removes ANSI escape codes from terminal output for pattern matching.
// This ensures patterns like "Logged in as" match even when wrapped in color codes.
func StripANSI(s string) string {
	return ansi.Strip(s)
}

// DetectState analyzes pane output and returns the detected state.
// Output is ANSI-normalized before pattern matching to handle colored terminal output.
func DetectState(output string) (PaneState, map[string]string) {
	metadata := make(map[string]string)

	// Strip ANSI codes for reliable pattern matching
	normalizedOutput := StripANSI(output)

	// Check for login success first (highest priority)
	if Patterns.LoginSuccess.MatchString(normalizedOutput) {
		return StateResuming, metadata
	}

	// Check for login failure
	if Patterns.LoginFailed.MatchString(normalizedOutput) {
		return StateFailed, metadata
	}

	// Check for OAuth URL (implies awaiting URL state)
	// Note: URLs should still be extracted from original output to preserve full URL
	if match := Patterns.OAuthURL.FindString(normalizedOutput); match != "" {
		// Extract from original to preserve any URL-encoded characters
		if origMatch := Patterns.OAuthURL.FindString(output); origMatch != "" {
			metadata["oauth_url"] = origMatch
		} else {
			metadata["oauth_url"] = match
		}
		return StateAwaitingURL, metadata
	}

	// Check for paste prompt (means URL was shown, awaiting code)
	if Patterns.PastePrompt.MatchString(normalizedOutput) {
		// Try to extract URL from original output too
		if match := Patterns.OAuthURL.FindString(output); match != "" {
			metadata["oauth_url"] = match
		}
		return StateAwaitingURL, metadata
	}

	// Check for login method selection prompt
	if Patterns.SelectMethod.MatchString(normalizedOutput) {
		return StateAwaitingMethodSelect, metadata
	}

	// Check for rate limit last (lowest priority, as it might be in history)
	if Patterns.RateLimit.MatchString(normalizedOutput) {
		if match := Patterns.UsageLimitReset.FindStringSubmatch(normalizedOutput); len(match) > 1 {
			metadata["reset_time"] = match[1]
		}
		return StateRateLimited, metadata
	}

	return StateIdle, metadata
}

// ExtractOAuthURL finds and returns the OAuth URL from output.
// Uses ANSI-stripped output to ensure clean URL extraction without
// terminal escape codes contaminating the URL.
func ExtractOAuthURL(output string) string {
	// Always use normalized output for URL extraction to avoid ANSI codes
	// being captured as part of the URL (e.g., trailing \x1b[0m)
	normalizedOutput := StripANSI(output)
	return Patterns.OAuthURL.FindString(normalizedOutput)
}

// DetectCompactingBanner checks if the output contains a Claude Code compacting banner.
// Returns true if detected, along with the matched text (useful for logging/debugging).
// The detection is performed on ANSI-stripped output to handle colored terminal output.
//
// Matches variants like:
//   - "Conversation compacted · ctrl+o for history"
//   - "Conversation was compacted • ctrl+o for history"
//   - Box-drawing decorated versions with various separators
func DetectCompactingBanner(output string) (detected bool, matchedText string) {
	normalizedOutput := StripANSI(output)
	match := Patterns.CompactingBanner.FindString(normalizedOutput)
	if match != "" {
		return true, match
	}
	return false, ""
}

// DetectCompactingBannerWithPattern allows using a custom regex pattern for detection.
// This enables configuration-driven pattern overrides for edge cases.
// If customPattern is nil, falls back to the default Patterns.CompactingBanner.
func DetectCompactingBannerWithPattern(output string, customPattern *regexp.Regexp) (detected bool, matchedText string) {
	normalizedOutput := StripANSI(output)

	pattern := customPattern
	if pattern == nil {
		pattern = Patterns.CompactingBanner
	}

	match := pattern.FindString(normalizedOutput)
	if match != "" {
		return true, match
	}
	return false, ""
}
