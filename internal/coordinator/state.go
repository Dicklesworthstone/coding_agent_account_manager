package coordinator

import (
	"regexp"
	"strings"
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
	PaneID       int
	State        PaneState
	LastCheck    time.Time
	StateEntered time.Time
	OAuthURL     string
	RequestID    string // ID for auth request
	ReceivedCode string // Code received from local agent
	UsedAccount  string // Account used for auth
	ErrorMessage string
	RetryCount   int
	ContinueSent bool                 // Enter sent to dismiss the post-login screen this cycle
	SelectSends  int                  // login-method selections sent this cycle
	GaveUp       bool                 // retries spent this rate-limit episode; left to a human (survives Reset)
	// LimitedAccount and LimitReset describe this episode's limit: the
	// account Claude Code was signed in with and the banner's reset. They
	// survive Reset, so retried logins still report them.
	LimitedAccount string
	LimitReset     string
	// HoldUntil is when the agent will have an account again after it
	// declined because all were at their limit; no login is started in the
	// pane before then. It survives Reset.
	HoldUntil time.Time
	LastOutput   string               // Cached output for duplicate detection
	Cooldowns    map[string]time.Time // action -> cooldown expiry
	mu           sync.RWMutex
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
	t.ContinueSent = false
	t.SelectSends = 0
	t.Cooldowns = make(map[string]time.Time)
}

// CountSelectSend records a login-method selection and returns how many were
// sent this cycle, including this one.
func (t *PaneTracker) CountSelectSend() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.SelectSends++
	return t.SelectSends
}

// SetLimit records the account that hit the limit and when it resets.
func (t *PaneTracker) SetLimit(account, reset string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.LimitedAccount = account
	t.LimitReset = reset
}

// GetLimit returns the account that hit the limit and when it resets.
func (t *PaneTracker) GetLimit() (account, reset string) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.LimitedAccount, t.LimitReset
}

// SetHoldUntil keeps logins out of the pane until t.
func (t *PaneTracker) SetHoldUntil(until time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.HoldUntil = until
}

// GetHoldUntil returns when logins may start in the pane again.
func (t *PaneTracker) GetHoldUntil() time.Time {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.HoldUntil
}

// SetGaveUp records whether the pane was left for manual recovery.
func (t *PaneTracker) SetGaveUp(gaveUp bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.GaveUp = gaveUp
}

// HasGivenUp reports whether the pane was left for manual recovery.
func (t *PaneTracker) HasGivenUp() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.GaveUp
}

// GetSelectSends returns the login-method selections sent this cycle.
func (t *PaneTracker) GetSelectSends() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.SelectSends
}

// MarkContinueSent records that the post-login screen was dismissed and
// reports whether it already had been this cycle.
func (t *PaneTracker) MarkContinueSent() (alreadySent bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	alreadySent = t.ContinueSent
	t.ContinueSent = true
	return alreadySent
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

// GetRetryCount returns how many login retries this episode has used.
func (t *PaneTracker) GetRetryCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.RetryCount
}

// SetRetryCount sets the login retry count. Reset leaves it unchanged so the
// budget spans the whole rate-limit episode.
func (t *PaneTracker) SetRetryCount(n int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.RetryCount = n
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
	LimitReset       *regexp.Regexp
	CompactingBanner *regexp.Regexp
	PressEnter       *regexp.Regexp
	RateLimitMenu    *regexp.Regexp
}{
	// "Login successful. Press Enter to continue…"
	PressEnter: regexp.MustCompile(`(?i)press\s+enter\s+to\s+continue`),

	// The menu Claude Code opens at a usage limit:
	//   What do you want to do?
	//   ❯ 1. Stop and wait for limit to reset
	//     2. Upgrade your plan
	// Its options vary by account and can list "Switch to usage credits" or
	// "Add funds to continue with usage credits" first.
	RateLimitMenu: regexp.MustCompile(`(?i)what do you want to do\?|stop and wait for limit to reset`),

	// Claude Code's limit banners, followed by their reset time on the same
	// line (which keeps conversation text about limits from matching):
	//   "You've hit your limit · resets 2pm (America/New_York)"
	//   "You've hit your session limit · resets 2pm" (also usage, weekly, Opus,
	//   Sonnet, Fable, usage credit, and spend limits; Claude Code 2.1+)
	//   "You're out of usage credits · resets 2pm"
	//   "Claude usage limit reached. Your limit will reset at 2pm (Europe/Paris)."
	//   "5-hour limit reached ∙ resets 2pm" / "Weekly limit reached ∙ resets Oct 9"
	// The forms naming a limit must start their line (after any TUI glyphs),
	// so Claude's own prose ("it looks like you've hit your rate limit…")
	// does not match. Tolerant of case and of a curly or missing apostrophe.
	RateLimit: regexp.MustCompile(`(?im)(you['’]?ve hit your limit|^[^\w\n]*you['’]?ve hit your (?:[\w'’-]+ ){1,4}?limit|^[^\w\n]*you['’]?re out of usage credits|usage limit reached|\b(?:5-hour|five-hour|weekly|opus|sonnet)\b[^\n]{0,20}\blimit reached)[^\n]{0,120}\breset`),

	// "Select login method:"
	SelectMethod: regexp.MustCompile(`(?i)select login method:`),

	// OAuth URL: https://claude.ai/oauth/authorize?code=true&... (or on a
	// claude.com host, which the agent also accepts)
	OAuthURL: regexp.MustCompile(`https://(?:[a-z0-9-]+\.)?claude\.(?:ai|com)/(?:[A-Za-z0-9_-]+/)*oauth/authorize\?[^\s]+`),

	// "Paste code here if prompted >"
	PastePrompt: regexp.MustCompile(`(?i)paste code here if prompted`),

	// "Logged in as user@example.com" or similar success patterns
	LoginSuccess: regexp.MustCompile(`(?i)(logged in as|login successful|successfully (authenticated|logged in)|welcome back)`),

	// Login failure patterns
	LoginFailed: regexp.MustCompile(`(?i)(login failed|authentication error|invalid code|expired|error signing)`),

	// "❯ 1. Claude account with subscription": option 1 highlighted
	OptionOne: regexp.MustCompile(`[❯>]\s*1\.\s*Claude account`),

	// Extract reset time from rate limit message
	UsageLimitReset: regexp.MustCompile(`(?i)resets?\s+(?:at\s+)?(\d+(?::\d+)?\s*[ap]m)`),

	// The whole reset of a limit banner, up to the next "·" separator:
	// "3pm (America/New_York)", "Oct 9, 3pm", "in 2h 13m"
	LimitReset: regexp.MustCompile(`(?i)\bresets?\s+(?:at\s+)?([^\n·∙•]{1,60})`),

	// "Conversation compacted (ctrl+o for history)" (Claude Code 2.1), or
	// "Conversation compacted · ctrl+o for history" and similar variants
	// Matches with optional box-drawing characters, middot/bullet separators,
	// and various whitespace. Also handles "Conversation was compacted" variants.
	CompactingBanner: regexp.MustCompile(`(?i)Conversation\s+(was\s+)?compacted[\s·•\-\|(]*ctrl\+?o`),
}

// StripANSI removes ANSI escape codes from terminal output for pattern matching.
// This ensures patterns like "Logged in as" match even when wrapped in color codes.
func StripANSI(s string) string {
	return ansi.Strip(s)
}

// stateDetectors map output patterns to pane states. Order breaks ties
// between matches at the same position.
var stateDetectors = []struct {
	pattern *regexp.Regexp
	state   PaneState
}{
	{Patterns.LoginSuccess, StateResuming},
	{Patterns.LoginFailed, StateFailed},
	{Patterns.OAuthURL, StateAwaitingURL},
	{Patterns.PastePrompt, StateAwaitingURL},
	{Patterns.SelectMethod, StateAwaitingMethodSelect},
	{Patterns.RateLimit, StateRateLimited},
}

// DetectState analyzes pane output and returns the detected state.
//
// The pane's scrollback holds earlier sessions and conversations, so the
// most recent recognized message decides the state: a fresh rate-limit
// banner is not masked by an old "Logged in as" or by the word "expired"
// higher up. Output is ANSI-normalized before matching.
func DetectState(output string) (PaneState, map[string]string) {
	metadata := make(map[string]string)
	normalizedOutput := StripANSI(output)

	state := StateIdle
	latest := -1
	for _, d := range stateDetectors {
		if pos := lastMatchStart(d.pattern, normalizedOutput); pos > latest {
			latest = pos
			state = d.state
		}
	}

	switch state {
	case StateAwaitingURL:
		if url := ExtractOAuthURL(output); url != "" {
			metadata["oauth_url"] = url
		}
	case StateRateLimited:
		if matches := Patterns.UsageLimitReset.FindAllStringSubmatch(normalizedOutput, -1); len(matches) > 0 {
			metadata["reset_time"] = matches[len(matches)-1][1]
		}
		if reset := limitResetText(normalizedOutput[latest:]); reset != "" {
			metadata["reset_text"] = reset
		}
	}
	return state, metadata
}

// limitResetText returns the reset of the limit banner that starts output,
// as Claude Code wrote it ("3pm (America/New_York)"), or "".
func limitResetText(output string) string {
	line, _, _ := strings.Cut(output, "\n")
	m := Patterns.LimitReset.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(m[1]), ".")
}

// lastMatchStart returns the start of the last match of re in s, or -1.
func lastMatchStart(re *regexp.Regexp, s string) int {
	locs := re.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return -1
	}
	return locs[len(locs)-1][0]
}

// ExtractOAuthURL returns the most recent OAuth URL in the output, so a
// retried login never reuses the URL of an earlier, abandoned attempt.
// Matching runs on ANSI-stripped output so escape codes never become part of
// the URL (e.g., a trailing \x1b[0m).
func ExtractOAuthURL(output string) string {
	matches := Patterns.OAuthURL.FindAllString(StripANSI(output), -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
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
