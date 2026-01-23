package coordinator

import (
	"context"
	"regexp"
	"sync"
	"testing"
	"time"
)

func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no ANSI codes",
			input:    "plain text",
			expected: "plain text",
		},
		{
			name:     "simple color code",
			input:    "\x1b[32mgreen text\x1b[0m",
			expected: "green text",
		},
		{
			name:     "bold text",
			input:    "\x1b[1mbold\x1b[0m",
			expected: "bold",
		},
		{
			name:     "multiple codes",
			input:    "\x1b[1m\x1b[32mLogged in as\x1b[0m user@example.com",
			expected: "Logged in as user@example.com",
		},
		{
			name:     "complex ANSI with cursor movement",
			input:    "\x1b[2J\x1b[H\x1b[32mWelcome back!\x1b[0m",
			expected: "Welcome back!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripANSI(tt.input)
			if got != tt.expected {
				t.Errorf("StripANSI() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestDetectStateWithANSI(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected PaneState
	}{
		{
			name:     "login success with ANSI colors",
			output:   "\x1b[1m\x1b[32mLogged in as\x1b[0m user@example.com",
			expected: StateResuming,
		},
		{
			name:     "rate limit with ANSI",
			output:   "\x1b[31mYou've hit your limit\x1b[0m. This resets 2pm",
			expected: StateRateLimited,
		},
		{
			name:     "welcome back with styling",
			output:   "\x1b[1mWelcome back!\x1b[0m Session resumed.",
			expected: StateResuming,
		},
		{
			name:     "login failed with colors",
			output:   "\x1b[31mLogin failed\x1b[0m: invalid code",
			expected: StateFailed,
		},
		{
			name:     "select method with ANSI",
			output:   "\x1b[36mSelect login method:\x1b[0m\n1. Claude account with subscription",
			expected: StateAwaitingMethodSelect,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, _ := DetectState(tt.output)
			if state != tt.expected {
				t.Errorf("DetectState() = %v, want %v", state, tt.expected)
			}
		})
	}
}

func TestDetectState(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected PaneState
	}{
		{
			name:     "idle output",
			output:   "Normal terminal output\nNothing special here",
			expected: StateIdle,
		},
		{
			name:     "rate limit detected",
			output:   "You've hit your limit on Claude usage today. This resets 2pm (America/New_York)",
			expected: StateRateLimited,
		},
		{
			name:     "method selection prompt",
			output:   "Select login method:\n1. Claude account with subscription\n2. API key",
			expected: StateAwaitingMethodSelect,
		},
		{
			name:     "OAuth URL shown",
			output:   "Open this URL in your browser: https://claude.ai/oauth/authorize?code_challenge=abc123",
			expected: StateAwaitingURL,
		},
		{
			name:     "paste code prompt",
			output:   "Paste code here if prompted > ",
			expected: StateAwaitingURL,
		},
		{
			name:     "login success",
			output:   "Logged in as user@example.com\nReady to continue",
			expected: StateResuming,
		},
		{
			name:     "login success - welcome back",
			output:   "Welcome back! Session resumed.",
			expected: StateResuming,
		},
		{
			name:     "login failed",
			output:   "Login failed: invalid code",
			expected: StateFailed,
		},
		{
			name:     "login failed - expired",
			output:   "Authentication error: code expired",
			expected: StateFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, _ := DetectState(tt.output)
			if state != tt.expected {
				t.Errorf("DetectState() = %v, want %v", state, tt.expected)
			}
		})
	}
}

func TestDetectStateMetadata(t *testing.T) {
	// Test that reset time is extracted from rate limit message
	output := "You've hit your limit. This resets 2pm (America/New_York)"
	state, metadata := DetectState(output)

	if state != StateRateLimited {
		t.Errorf("expected StateRateLimited, got %v", state)
	}

	if resetTime, ok := metadata["reset_time"]; !ok || resetTime != "2pm" {
		t.Errorf("expected reset_time=2pm, got %v", metadata)
	}
}

func TestExtractOAuthURL(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "URL in output",
			output:   "Please visit: https://claude.ai/oauth/authorize?code_challenge=xyz123&client_id=claude-code",
			expected: "https://claude.ai/oauth/authorize?code_challenge=xyz123&client_id=claude-code",
		},
		{
			name:     "no URL",
			output:   "Just some regular text",
			expected: "",
		},
		{
			name:     "URL with extra text after",
			output:   "Open https://claude.ai/oauth/authorize?foo=bar in browser",
			expected: "https://claude.ai/oauth/authorize?foo=bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := ExtractOAuthURL(tt.output)
			if url != tt.expected {
				t.Errorf("ExtractOAuthURL() = %q, want %q", url, tt.expected)
			}
		})
	}
}

func TestPaneTracker(t *testing.T) {
	tracker := NewPaneTracker(123)

	// Initial state should be idle
	if tracker.GetState() != StateIdle {
		t.Errorf("initial state = %v, want StateIdle", tracker.GetState())
	}

	// Verify pane ID
	if tracker.PaneID != 123 {
		t.Errorf("pane ID = %d, want 123", tracker.PaneID)
	}

	// Set state to rate limited
	tracker.SetState(StateRateLimited)
	if tracker.GetState() != StateRateLimited {
		t.Errorf("state after SetState = %v, want StateRateLimited", tracker.GetState())
	}

	// TimeSinceStateChange should be small
	if tracker.TimeSinceStateChange() > time.Second {
		t.Errorf("time since state change too large")
	}

	// Reset should return to idle
	tracker.Reset()
	if tracker.GetState() != StateIdle {
		t.Errorf("state after Reset = %v, want StateIdle", tracker.GetState())
	}

	// Reset should clear fields
	tracker.OAuthURL = "https://example.com"
	tracker.RequestID = "req-123"
	tracker.ReceivedCode = "code-456"
	tracker.UsedAccount = "user@example.com"
	tracker.ErrorMessage = "some error"
	tracker.Reset()

	if tracker.OAuthURL != "" {
		t.Errorf("OAuthURL not cleared")
	}
	if tracker.RequestID != "" {
		t.Errorf("RequestID not cleared")
	}
	if tracker.ReceivedCode != "" {
		t.Errorf("ReceivedCode not cleared")
	}
	if tracker.UsedAccount != "" {
		t.Errorf("UsedAccount not cleared")
	}
	if tracker.ErrorMessage != "" {
		t.Errorf("ErrorMessage not cleared")
	}
}

func TestPaneStateString(t *testing.T) {
	tests := []struct {
		state    PaneState
		expected string
	}{
		{StateIdle, "IDLE"},
		{StateRateLimited, "RATE_LIMITED"},
		{StateAwaitingMethodSelect, "AWAITING_METHOD_SELECT"},
		{StateAwaitingURL, "AWAITING_URL"},
		{StateAuthPending, "AUTH_PENDING"},
		{StateCodeReceived, "CODE_RECEIVED"},
		{StateAwaitingConfirm, "AWAITING_CONFIRM"},
		{StateResuming, "RESUMING"},
		{StateFailed, "FAILED"},
		{PaneState(999), "UNKNOWN"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expected {
				t.Errorf("String() = %q, want %q", got, tt.expected)
			}
		})
	}
}

type fakePaneClient struct {
	panes  []Pane
	output string
	sent   []string
	mu     sync.Mutex
}

func (f *fakePaneClient) ListPanes(ctx context.Context) ([]Pane, error) {
	return f.panes, nil
}

func (f *fakePaneClient) GetText(ctx context.Context, paneID int, startLine int) (string, error) {
	return f.output, nil
}

func (f *fakePaneClient) SendText(ctx context.Context, paneID int, text string, noPaste bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return nil
}

func (f *fakePaneClient) IsAvailable(ctx context.Context) bool {
	return true
}

func (f *fakePaneClient) Backend() string {
	return "fake"
}

func (f *fakePaneClient) sentText() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.sent))
	copy(out, f.sent)
	return out
}

func TestCoordinator_AuthPendingProcessesWithoutOutputChange(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Paste code here if prompted >",
	}

	cfg := DefaultConfig()
	coord := New(cfg)
	coord.paneClient = client

	tracker := NewPaneTracker(1)
	tracker.LastOutput = client.output
	tracker.SetState(StateAuthPending)
	tracker.SetRequestID("req-1")

	coord.trackers[1] = tracker
	coord.requests["req-1"] = &AuthRequest{
		ID:        "req-1",
		PaneID:    1,
		URL:       "https://claude.ai/oauth/authorize?code_challenge=abc",
		CreatedAt: time.Now(),
		Status:    "pending",
	}

	if err := coord.ReceiveAuthResponse(AuthResponse{
		RequestID: "req-1",
		Code:      "CODE123",
		Account:   "user@example.com",
	}); err != nil {
		t.Fatalf("ReceiveAuthResponse error: %v", err)
	}

	coord.processPaneState(context.Background(), client.panes[0])
	if tracker.GetState() != StateCodeReceived {
		t.Fatalf("state after auth response = %v, want %v", tracker.GetState(), StateCodeReceived)
	}

	coord.processPaneState(context.Background(), client.panes[0])
	if tracker.GetState() != StateAwaitingConfirm {
		t.Fatalf("state after code injection = %v, want %v", tracker.GetState(), StateAwaitingConfirm)
	}

	sent := client.sentText()
	found := false
	for _, s := range sent {
		if s == "CODE123\n" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected code to be injected, sent=%v", sent)
	}
}

func TestCoordinator_ResumeCooldownPreventsDoubleInjection(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Logged in as user@example.com", // Login success output
	}

	cfg := DefaultConfig()
	cfg.ResumeCooldown = 5 * time.Second // Short cooldown for testing
	coord := New(cfg)
	coord.paneClient = client

	tracker := NewPaneTracker(1)
	tracker.LastOutput = ""
	tracker.SetState(StateResuming)
	coord.trackers[1] = tracker

	ctx := context.Background()

	// First call should inject resume prompt
	coord.handleResumingState(ctx, tracker, client.output)
	sentBefore := client.sentText()
	if len(sentBefore) != 1 {
		t.Fatalf("expected 1 sent message after first call, got %d: %v", len(sentBefore), sentBefore)
	}

	// Verify the resume prompt was sent
	if sentBefore[0] != cfg.ResumePrompt {
		t.Fatalf("expected resume prompt %q, got %q", cfg.ResumePrompt, sentBefore[0])
	}

	// Create a new tracker (simulating next poll cycle) in resuming state
	tracker2 := NewPaneTracker(1)
	tracker2.SetState(StateResuming)
	tracker2.SetCooldown("resume", cfg.ResumeCooldown) // Copy the cooldown

	// Second call with cooldown active should NOT inject
	coord.handleResumingState(ctx, tracker2, client.output)
	sentAfter := client.sentText()
	// Should still be just 1 message (no additional injection)
	if len(sentAfter) != 1 {
		t.Fatalf("expected 1 sent message after second call (cooldown active), got %d: %v", len(sentAfter), sentAfter)
	}
}

func TestExtractOAuthURL_WithANSI(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
	}{
		{
			name:     "URL with ANSI prefix",
			output:   "\x1b[36mOpen: https://claude.ai/oauth/authorize?code=abc\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?code=abc",
		},
		{
			name:     "URL surrounded by colors",
			output:   "\x1b[1mVisit\x1b[0m https://claude.ai/oauth/authorize?foo=bar \x1b[32min browser\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?foo=bar",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := ExtractOAuthURL(tt.output)
			if url != tt.expected {
				t.Errorf("ExtractOAuthURL() = %q, want %q", url, tt.expected)
			}
		})
	}
}

// =============================================================================
// OAuth URL Extraction Unit Tests - Multiline Fixtures (caam-6sao.4)
// =============================================================================

func TestExtractOAuthURL_MultilineFixtures(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
		desc     string
	}{
		{
			name: "URL on single line",
			output: `Please visit the following URL to complete authentication:
https://claude.ai/oauth/authorize?code_challenge=abc123&code_challenge_method=S256&client_id=claude-code&redirect_uri=http%3A%2F%2Flocalhost%3A12345%2Fcallback&response_type=code&scope=openid%20profile%20email
Waiting for authentication...`,
			expected: "https://claude.ai/oauth/authorize?code_challenge=abc123&code_challenge_method=S256&client_id=claude-code&redirect_uri=http%3A%2F%2Flocalhost%3A12345%2Fcallback&response_type=code&scope=openid%20profile%20email",
			desc:     "Full URL with multiple query params should be extracted completely",
		},
		{
			name: "URL with complex query params",
			output: `Open this URL: https://claude.ai/oauth/authorize?code_challenge=xYz_ABC-123&state=abcdefghijklmnopqrstuvwxyz012345&nonce=1234567890abcdef`,
			expected: "https://claude.ai/oauth/authorize?code_challenge=xYz_ABC-123&state=abcdefghijklmnopqrstuvwxyz012345&nonce=1234567890abcdef",
			desc:     "URL with special characters in query params",
		},
		{
			name: "URL with URL-encoded values",
			output: `Auth URL: https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fauth%2Fcallback&scope=openid+profile+email`,
			expected: "https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fauth%2Fcallback&scope=openid+profile+email",
			desc:     "URL with percent-encoded values preserved",
		},
		{
			name:     "URL followed by punctuation",
			output:   "Visit https://claude.ai/oauth/authorize?code=abc. Then paste the code here.",
			expected: "https://claude.ai/oauth/authorize?code=abc.",
			desc:     "URL followed by period - regex captures trailing dot (cleanURL would strip it)",
		},
		{
			name:     "URL in parentheses",
			output:   "(Open https://claude.ai/oauth/authorize?token=xyz in browser)",
			expected: "https://claude.ai/oauth/authorize?token=xyz",
			desc:     "URL within parentheses - should not capture closing paren",
		},
		{
			name:     "URL in angle brackets",
			output:   "Link: <https://claude.ai/oauth/authorize?id=123>",
			expected: "https://claude.ai/oauth/authorize?id=123>",
			desc:     "URL in angle brackets - regex may capture trailing bracket",
		},
		{
			name: "Multiple URLs in output",
			output: `Primary: https://claude.ai/oauth/authorize?code=primary
Alternative: https://claude.ai/oauth/authorize?code=alternate`,
			expected: "https://claude.ai/oauth/authorize?code=primary",
			desc:     "Multiple URLs - should extract first one (FindString behavior)",
		},
		{
			name:     "URL with no query params",
			output:   "Go to https://claude.ai/oauth/authorize?",
			expected: "",
			desc:     "URL with empty query string - regex requires at least one char after ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := ExtractOAuthURL(tt.output)
			if url != tt.expected {
				t.Errorf("ExtractOAuthURL() = %q, want %q\nDescription: %s", url, tt.expected, tt.desc)
			}
			// Log extraction success for structured test output
			t.Logf("fixture=%s extraction_success=%v url_length=%d", tt.name, url == tt.expected, len(url))
		})
	}
}

func TestExtractOAuthURL_ANSIDecorated(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
		desc     string
	}{
		{
			name:     "URL with bold ANSI",
			output:   "\x1b[1mhttps://claude.ai/oauth/authorize?bold=true\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?bold=true",
			desc:     "Bold styled URL",
		},
		{
			name:     "URL with underline ANSI",
			output:   "\x1b[4mhttps://claude.ai/oauth/authorize?underline=true\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?underline=true",
			desc:     "Underlined URL",
		},
		{
			name:     "URL with 256-color ANSI",
			output:   "\x1b[38;5;33mhttps://claude.ai/oauth/authorize?color=256\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?color=256",
			desc:     "256-color foreground styled URL",
		},
		{
			name:     "URL with RGB ANSI",
			output:   "\x1b[38;2;100;150;200mhttps://claude.ai/oauth/authorize?rgb=true\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?rgb=true",
			desc:     "True-color (RGB) styled URL",
		},
		{
			name:     "URL with cursor movement codes",
			output:   "\x1b[2K\x1b[1Ghttps://claude.ai/oauth/authorize?cursor=move",
			expected: "https://claude.ai/oauth/authorize?cursor=move",
			desc:     "URL after cursor movement/clear codes",
		},
		{
			name:     "URL with multiple ANSI codes",
			output:   "\x1b[1m\x1b[4m\x1b[36mhttps://claude.ai/oauth/authorize?multi=style\x1b[0m\x1b[0m\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?multi=style",
			desc:     "URL with stacked ANSI codes",
		},
		{
			name:     "URL with ANSI in query params",
			output:   "https://claude.ai/oauth/authorize?foo=\x1b[32mbar\x1b[0m&baz=qux",
			expected: "https://claude.ai/oauth/authorize?foo=bar&baz=qux",
			desc:     "ANSI codes stripped before extraction - full URL preserved",
		},
		{
			name:     "URL split by ANSI reset",
			output:   "\x1b[36mhttps://claude.ai/oauth/authorize\x1b[0m?code=abc",
			expected: "https://claude.ai/oauth/authorize?code=abc",
			desc:     "ANSI codes stripped first - full URL extracted intact",
		},
		{
			name: "Complex terminal output with URL",
			output: "\x1b[2J\x1b[H\x1b[?25l\x1b[1;1H\x1b[36m╭───────────────────────────────────────╮\x1b[0m\n" +
				"\x1b[36m│\x1b[0m Please visit this URL:              \x1b[36m│\x1b[0m\n" +
				"\x1b[36m│\x1b[0m https://claude.ai/oauth/authorize?a=b \x1b[36m│\x1b[0m\n" +
				"\x1b[36m╰───────────────────────────────────────╯\x1b[0m",
			expected: "https://claude.ai/oauth/authorize?a=b",
			desc:     "URL in styled terminal box UI",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := ExtractOAuthURL(tt.output)
			if url != tt.expected {
				t.Errorf("ExtractOAuthURL() = %q, want %q\nDescription: %s", url, tt.expected, tt.desc)
			}
			t.Logf("fixture=%s extraction_success=%v url_length=%d", tt.name, url == tt.expected, len(url))
		})
	}
}

func TestExtractOAuthURL_TrailingPunctuation(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		expectedRaw string
		desc        string
	}{
		{
			name:        "trailing period",
			output:      "Go to https://claude.ai/oauth/authorize?x=1.",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1.",
			desc:        "Period after URL",
		},
		{
			name:        "trailing comma",
			output:      "URL is https://claude.ai/oauth/authorize?x=1, then paste code",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1,",
			desc:        "Comma after URL",
		},
		{
			name:        "trailing semicolon",
			output:      "https://claude.ai/oauth/authorize?x=1; # comment",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1;",
			desc:        "Semicolon after URL",
		},
		{
			name:        "trailing colon",
			output:      "See https://claude.ai/oauth/authorize?x=1: important!",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1:",
			desc:        "Colon after URL",
		},
		{
			name:        "trailing exclamation",
			output:      "Click https://claude.ai/oauth/authorize?x=1!",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1!",
			desc:        "Exclamation after URL",
		},
		{
			name:        "trailing paren",
			output:      "(see https://claude.ai/oauth/authorize?x=1)",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1)",
			desc:        "Closing paren after URL",
		},
		{
			name:        "trailing bracket",
			output:      "[https://claude.ai/oauth/authorize?x=1]",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1]",
			desc:        "Closing bracket after URL",
		},
		{
			name:        "trailing brace",
			output:      "{url: https://claude.ai/oauth/authorize?x=1}",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1}",
			desc:        "Closing brace after URL",
		},
		{
			name:        "trailing angle bracket",
			output:      "<https://claude.ai/oauth/authorize?x=1>",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1>",
			desc:        "Closing angle bracket after URL",
		},
		{
			name:        "multiple trailing punctuation",
			output:      "URL: https://claude.ai/oauth/authorize?x=1...",
			expectedRaw: "https://claude.ai/oauth/authorize?x=1...",
			desc:        "Multiple periods after URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := ExtractOAuthURL(tt.output)
			if url != tt.expectedRaw {
				t.Errorf("ExtractOAuthURL() = %q, want %q\nDescription: %s", url, tt.expectedRaw, tt.desc)
			}
			t.Logf("fixture=%s extraction_success=%v url_length=%d", tt.name, url == tt.expectedRaw, len(url))
		})
	}
}

func TestExtractOAuthURL_QueryParamPreservation(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected string
		desc     string
	}{
		{
			name:     "standard OAuth params",
			output:   "https://claude.ai/oauth/authorize?response_type=code&client_id=claude-code&redirect_uri=http%3A%2F%2Flocalhost%3A8080&scope=openid",
			expected: "https://claude.ai/oauth/authorize?response_type=code&client_id=claude-code&redirect_uri=http%3A%2F%2Flocalhost%3A8080&scope=openid",
			desc:     "Standard OAuth 2.0 query params",
		},
		{
			name:     "PKCE params",
			output:   "https://claude.ai/oauth/authorize?code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256",
			expected: "https://claude.ai/oauth/authorize?code_challenge=E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM&code_challenge_method=S256",
			desc:     "PKCE code challenge params with Base64URL characters",
		},
		{
			name:     "state param with special chars",
			output:   "https://claude.ai/oauth/authorize?state=abc123_XYZ-789",
			expected: "https://claude.ai/oauth/authorize?state=abc123_XYZ-789",
			desc:     "State param with underscores and hyphens",
		},
		{
			name:     "nonce param",
			output:   "https://claude.ai/oauth/authorize?nonce=f8a7d6c5b4e3a2b1",
			expected: "https://claude.ai/oauth/authorize?nonce=f8a7d6c5b4e3a2b1",
			desc:     "Nonce param with hex string",
		},
		{
			name:     "plus-encoded spaces",
			output:   "https://claude.ai/oauth/authorize?scope=openid+profile+email",
			expected: "https://claude.ai/oauth/authorize?scope=openid+profile+email",
			desc:     "Scope with plus-encoded spaces",
		},
		{
			name:     "percent-encoded special chars",
			output:   "https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fcallback%3Ffoo%3Dbar",
			expected: "https://claude.ai/oauth/authorize?redirect_uri=http%3A%2F%2Flocalhost%3A8080%2Fcallback%3Ffoo%3Dbar",
			desc:     "Nested percent-encoded URL in redirect_uri",
		},
		{
			name:     "empty param value",
			output:   "https://claude.ai/oauth/authorize?empty=&foo=bar",
			expected: "https://claude.ai/oauth/authorize?empty=&foo=bar",
			desc:     "Param with empty value",
		},
		{
			name:     "param without value",
			output:   "https://claude.ai/oauth/authorize?flag&foo=bar",
			expected: "https://claude.ai/oauth/authorize?flag&foo=bar",
			desc:     "Boolean flag param without equals sign",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := ExtractOAuthURL(tt.output)
			if url != tt.expected {
				t.Errorf("ExtractOAuthURL() = %q, want %q\nDescription: %s", url, tt.expected, tt.desc)
			}
			t.Logf("fixture=%s extraction_success=%v url_length=%d", tt.name, url == tt.expected, len(url))
		})
	}
}

func TestDetectState_OAuthURLMetadata(t *testing.T) {
	tests := []struct {
		name        string
		output      string
		wantState   PaneState
		wantURL     string
		desc        string
	}{
		{
			name:      "extracts URL to metadata",
			output:    "Open https://claude.ai/oauth/authorize?code=test123 in browser",
			wantState: StateAwaitingURL,
			wantURL:   "https://claude.ai/oauth/authorize?code=test123",
			desc:      "URL should be stored in metadata",
		},
		{
			name:      "URL with ANSI preserved in metadata",
			output:    "\x1b[32mhttps://claude.ai/oauth/authorize?ansi=true\x1b[0m",
			wantState: StateAwaitingURL,
			wantURL:   "https://claude.ai/oauth/authorize?ansi=true\x1b[0m",
			desc:      "URL extracted from original output - trailing ANSI captured (regex [^\\s]+ matches escape codes)",
		},
		{
			name:      "paste prompt with URL",
			output:    "https://claude.ai/oauth/authorize?x=1\nPaste code here if prompted >",
			wantState: StateAwaitingURL,
			wantURL:   "https://claude.ai/oauth/authorize?x=1",
			desc:      "Paste prompt state extracts URL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, metadata := DetectState(tt.output)
			if state != tt.wantState {
				t.Errorf("DetectState() state = %v, want %v\nDescription: %s", state, tt.wantState, tt.desc)
			}
			if gotURL := metadata["oauth_url"]; gotURL != tt.wantURL {
				t.Errorf("DetectState() metadata[oauth_url] = %q, want %q\nDescription: %s", gotURL, tt.wantURL, tt.desc)
			}
			t.Logf("fixture=%s state_correct=%v url_correct=%v url_length=%d",
				tt.name, state == tt.wantState, metadata["oauth_url"] == tt.wantURL, len(metadata["oauth_url"]))
		})
	}
}

// TestOAuthURLPattern_Regex tests the regex pattern directly
// =============================================================================
// Compacting Banner Detection Tests (caam-wgd8)
// =============================================================================

func TestDetectCompactingBanner(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantDetected bool
		wantContains string // Substring that should be in matched text
		desc         string
	}{
		{
			name:         "standard banner with middot",
			output:       "Conversation compacted · ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation compacted",
			desc:         "Standard Claude Code compacting banner",
		},
		{
			name:         "banner with bullet point",
			output:       "Conversation compacted • ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation compacted",
			desc:         "Variant with bullet point separator",
		},
		{
			name:         "banner with hyphen separator",
			output:       "Conversation compacted - ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation compacted",
			desc:         "Variant with hyphen separator",
		},
		{
			name:         "banner with pipe separator",
			output:       "Conversation compacted | ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation compacted",
			desc:         "Variant with pipe separator",
		},
		{
			name:         "banner with 'was compacted' variant",
			output:       "Conversation was compacted · ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation was compacted",
			desc:         "Alternative phrasing with 'was'",
		},
		{
			name:         "banner with extra whitespace",
			output:       "Conversation   compacted   ·   ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation",
			desc:         "Banner with extra whitespace between words",
		},
		{
			name:         "banner with ctrl+o (plus sign)",
			output:       "Conversation compacted · ctrl+o for history",
			wantDetected: true,
			wantContains: "ctrl+o",
			desc:         "ctrl+o with plus sign",
		},
		{
			name:         "banner with ctrlo (no plus)",
			output:       "Conversation compacted · ctrlo for history",
			wantDetected: true,
			wantContains: "ctrlo",
			desc:         "ctrlo without plus sign",
		},
		{
			name:         "case insensitive - uppercase",
			output:       "CONVERSATION COMPACTED · CTRL+O for history",
			wantDetected: true,
			wantContains: "CONVERSATION COMPACTED",
			desc:         "All uppercase should match (case insensitive)",
		},
		{
			name:         "case insensitive - mixed case",
			output:       "ConVersation ComPacted · Ctrl+O for history",
			wantDetected: true,
			wantContains: "ConVersation ComPacted",
			desc:         "Mixed case should match",
		},
		{
			name:         "no match - unrelated text",
			output:       "Normal terminal output here",
			wantDetected: false,
			wantContains: "",
			desc:         "Unrelated text should not match",
		},
		{
			name:         "no match - partial text",
			output:       "Conversation compacted",
			wantDetected: false,
			wantContains: "",
			desc:         "Partial match without ctrl+o should not match",
		},
		{
			name:         "no match - ctrl+o without compacted",
			output:       "Press ctrl+o for history",
			wantDetected: false,
			wantContains: "",
			desc:         "ctrl+o alone should not match",
		},
		{
			name:         "banner in multiline output",
			output:       "Some previous output\nConversation compacted · ctrl+o for history\nSome following output",
			wantDetected: true,
			wantContains: "Conversation compacted",
			desc:         "Banner embedded in multiline output",
		},
		{
			name:         "banner at end of output",
			output:       "Long conversation history here...\n\nConversation compacted · ctrl+o for history",
			wantDetected: true,
			wantContains: "Conversation compacted",
			desc:         "Banner at end of output",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, matchedText := DetectCompactingBanner(tt.output)
			if detected != tt.wantDetected {
				t.Errorf("DetectCompactingBanner() detected = %v, want %v\nDescription: %s", detected, tt.wantDetected, tt.desc)
			}
			if tt.wantDetected && tt.wantContains != "" {
				if matchedText == "" || !contains(matchedText, tt.wantContains) {
					t.Errorf("DetectCompactingBanner() matchedText = %q, want to contain %q", matchedText, tt.wantContains)
				}
			}
			t.Logf("fixture=%s detected=%v matched_length=%d", tt.name, detected, len(matchedText))
		})
	}
}

func TestDetectCompactingBanner_WithANSI(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantDetected bool
		desc         string
	}{
		{
			name:         "banner with color codes",
			output:       "\x1b[36mConversation compacted\x1b[0m · ctrl+o for history",
			wantDetected: true,
			desc:         "Banner with cyan color codes",
		},
		{
			name:         "banner with bold",
			output:       "\x1b[1mConversation compacted · ctrl+o for history\x1b[0m",
			wantDetected: true,
			desc:         "Banner with bold formatting",
		},
		{
			name:         "banner with 256-color",
			output:       "\x1b[38;5;208mConversation compacted · ctrl+o for history\x1b[0m",
			wantDetected: true,
			desc:         "Banner with 256-color foreground",
		},
		{
			name:         "banner with RGB color",
			output:       "\x1b[38;2;100;150;200mConversation compacted · ctrl+o for history\x1b[0m",
			wantDetected: true,
			desc:         "Banner with true-color (RGB) formatting",
		},
		{
			name:         "banner with cursor movement",
			output:       "\x1b[2K\x1b[1GConversation compacted · ctrl+o for history",
			wantDetected: true,
			desc:         "Banner after cursor clear and move",
		},
		{
			name:         "banner with stacked ANSI codes",
			output:       "\x1b[1m\x1b[4m\x1b[36mConversation compacted\x1b[0m\x1b[0m · ctrl+o for history\x1b[0m",
			wantDetected: true,
			desc:         "Banner with multiple stacked ANSI codes",
		},
		{
			name: "banner in styled terminal box",
			output: "\x1b[36m╭───────────────────────────────────────╮\x1b[0m\n" +
				"\x1b[36m│\x1b[0m Conversation compacted · ctrl+o for history \x1b[36m│\x1b[0m\n" +
				"\x1b[36m╰───────────────────────────────────────╯\x1b[0m",
			wantDetected: true,
			desc:         "Banner in styled terminal box with box-drawing characters",
		},
		{
			name:         "banner with ANSI in middle of text",
			output:       "Conversation \x1b[1mcompacted\x1b[0m · ctrl+o for history",
			wantDetected: true,
			desc:         "ANSI codes within banner text",
		},
		{
			name:         "complex terminal output with banner",
			output:       "\x1b[2J\x1b[H\x1b[?25l\x1b[1;1HConversation compacted · ctrl+o for history\x1b[?25h",
			wantDetected: true,
			desc:         "Banner with screen clear and cursor visibility codes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, matchedText := DetectCompactingBanner(tt.output)
			if detected != tt.wantDetected {
				t.Errorf("DetectCompactingBanner() detected = %v, want %v\nDescription: %s", detected, tt.wantDetected, tt.desc)
			}
			t.Logf("fixture=%s detected=%v matched=%q", tt.name, detected, matchedText)
		})
	}
}

func TestDetectCompactingBanner_EdgeCases(t *testing.T) {
	tests := []struct {
		name         string
		output       string
		wantDetected bool
		desc         string
	}{
		{
			name:         "empty string",
			output:       "",
			wantDetected: false,
			desc:         "Empty string should not match",
		},
		{
			name:         "only whitespace",
			output:       "   \n\t\n   ",
			wantDetected: false,
			desc:         "Whitespace only should not match",
		},
		{
			name:         "banner-like but wrong order",
			output:       "ctrl+o for history · Conversation compacted",
			wantDetected: false,
			desc:         "Reversed order should not match",
		},
		{
			name:         "typo in 'compacted'",
			output:       "Conversation compactd · ctrl+o for history",
			wantDetected: false,
			desc:         "Typo in 'compacted' should not match",
		},
		{
			name:         "very long output with banner",
			output:       string(make([]byte, 10000)) + "Conversation compacted · ctrl+o for history" + string(make([]byte, 10000)),
			wantDetected: true,
			desc:         "Banner in very long output should still be found",
		},
		{
			name:         "multiple banners",
			output:       "Conversation compacted · ctrl+o for history\nConversation compacted · ctrl+o for history",
			wantDetected: true,
			desc:         "Multiple banners - first one should match",
		},
		{
			name:         "unicode whitespace around banner",
			output:       "\u00A0Conversation compacted · ctrl+o for history\u00A0",
			wantDetected: true,
			desc:         "Non-breaking spaces around banner",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, _ := DetectCompactingBanner(tt.output)
			if detected != tt.wantDetected {
				t.Errorf("DetectCompactingBanner() detected = %v, want %v\nDescription: %s", detected, tt.wantDetected, tt.desc)
			}
		})
	}
}

func TestDetectCompactingBannerWithPattern(t *testing.T) {
	customPattern := regexp.MustCompile(`(?i)custom\s+compacting\s+message`)

	tests := []struct {
		name         string
		output       string
		pattern      *regexp.Regexp
		wantDetected bool
		desc         string
	}{
		{
			name:         "nil pattern uses default",
			output:       "Conversation compacted · ctrl+o for history",
			pattern:      nil,
			wantDetected: true,
			desc:         "nil pattern should fall back to default",
		},
		{
			name:         "custom pattern matches",
			output:       "Custom compacting message here",
			pattern:      customPattern,
			wantDetected: true,
			desc:         "Custom pattern should match custom text",
		},
		{
			name:         "custom pattern does not match default text",
			output:       "Conversation compacted · ctrl+o for history",
			pattern:      customPattern,
			wantDetected: false,
			desc:         "Custom pattern should not match default banner",
		},
		{
			name:         "default pattern does not match custom text",
			output:       "Custom compacting message here",
			pattern:      nil,
			wantDetected: false,
			desc:         "Default pattern should not match custom text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, _ := DetectCompactingBannerWithPattern(tt.output, tt.pattern)
			if detected != tt.wantDetected {
				t.Errorf("DetectCompactingBannerWithPattern() detected = %v, want %v\nDescription: %s", detected, tt.wantDetected, tt.desc)
			}
		})
	}
}

func TestCompactingBannerPattern_Regex(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantMatch bool
		desc      string
	}{
		{
			name:      "exact standard format",
			input:     "Conversation compacted · ctrl+o for history",
			wantMatch: true,
			desc:      "Standard format matches",
		},
		{
			name:      "without 'for history' suffix",
			input:     "Conversation compacted · ctrl+o",
			wantMatch: true,
			desc:      "Pattern only requires up to ctrl+o",
		},
		{
			name:      "conversation alone",
			input:     "Conversation",
			wantMatch: false,
			desc:      "Just 'Conversation' should not match",
		},
		{
			name:      "compacted alone",
			input:     "compacted",
			wantMatch: false,
			desc:      "Just 'compacted' should not match",
		},
		{
			name:      "different casing",
			input:     "conversation COMPACTED · CTRL+O",
			wantMatch: true,
			desc:      "Mixed case should match due to (?i) flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test on ANSI-stripped output (as the detection functions do)
			normalizedInput := StripANSI(tt.input)
			match := Patterns.CompactingBanner.MatchString(normalizedInput)
			if match != tt.wantMatch {
				t.Errorf("CompactingBanner.MatchString(%q) = %v, want %v\nDescription: %s", tt.input, match, tt.wantMatch, tt.desc)
			}
		})
	}
}

// contains is a helper function for checking substring presence
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestOAuthURLPattern_Regex(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantMatch  bool
		wantFull   string
		desc       string
	}{
		{
			name:      "basic authorize URL",
			input:     "https://claude.ai/oauth/authorize?code=abc",
			wantMatch: true,
			wantFull:  "https://claude.ai/oauth/authorize?code=abc",
			desc:      "Simple URL matches",
		},
		{
			name:      "URL without query string",
			input:     "https://claude.ai/oauth/authorize",
			wantMatch: false,
			wantFull:  "",
			desc:      "URL without query requires at least ?",
		},
		{
			name:      "URL with just question mark",
			input:     "https://claude.ai/oauth/authorize?",
			wantMatch: false,
			wantFull:  "",
			desc:      "URL with empty query string - regex requires at least one char after ?",
		},
		{
			name:      "URL in text",
			input:     "Please open https://claude.ai/oauth/authorize?x=1 now",
			wantMatch: true,
			wantFull:  "https://claude.ai/oauth/authorize?x=1",
			desc:      "URL extracted from surrounding text",
		},
		{
			name:      "wrong domain",
			input:     "https://example.com/oauth/authorize?code=abc",
			wantMatch: false,
			wantFull:  "",
			desc:      "Non-claude domain should not match",
		},
		{
			name:      "http instead of https",
			input:     "http://claude.ai/oauth/authorize?code=abc",
			wantMatch: false,
			wantFull:  "",
			desc:      "HTTP (non-secure) should not match",
		},
		{
			name:      "wrong path",
			input:     "https://claude.ai/api/authorize?code=abc",
			wantMatch: false,
			wantFull:  "",
			desc:      "Wrong path should not match",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match := Patterns.OAuthURL.FindString(tt.input)
			gotMatch := match != ""
			if gotMatch != tt.wantMatch {
				t.Errorf("OAuthURL.Match(%q) = %v, want %v\nDescription: %s", tt.input, gotMatch, tt.wantMatch, tt.desc)
			}
			if match != tt.wantFull {
				t.Errorf("OAuthURL.FindString(%q) = %q, want %q", tt.input, match, tt.wantFull)
			}
			t.Logf("fixture=%s match=%v extracted_length=%d", tt.name, gotMatch, len(match))
		})
	}
}

// Test compaction reminder injection logic
func TestCoordinator_CompactionReminderInjection(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Conversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Test reminder prompt"
	cfg.CompactionReminderCooldown = 10 * time.Minute
	cfg.PaneClient = client

	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = ""
	coord.trackers[1] = tracker

	ctx := context.Background()

	// Call handleIdleState which should detect compaction and inject
	coord.handleIdleState(ctx, tracker, client.output)

	// Check that prompt was injected
	client.mu.Lock()
	sent := client.sent
	client.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("expected 1 injection, got %d: %v", len(sent), sent)
	}
	if sent[0] != "Test reminder prompt\n" {
		t.Errorf("expected prompt with newline, got %q", sent[0])
	}
}

func TestCoordinator_CompactionReminderDisabled(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Conversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = false // Explicitly disabled
	cfg.PaneClient = client

	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = ""
	coord.trackers[1] = tracker

	ctx := context.Background()

	// Call handleIdleState - should NOT inject because feature is disabled
	coord.handleIdleState(ctx, tracker, client.output)

	client.mu.Lock()
	sent := client.sent
	client.mu.Unlock()

	if len(sent) != 0 {
		t.Fatalf("expected no injections when disabled, got %d: %v", len(sent), sent)
	}
}

func TestCoordinator_CompactionReminderCooldown(t *testing.T) {
	client := &fakePaneClient{
		panes:  []Pane{{PaneID: 1}},
		output: "Conversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Test reminder"
	cfg.CompactionReminderCooldown = 10 * time.Minute
	cfg.PaneClient = client

	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = ""
	coord.trackers[1] = tracker

	ctx := context.Background()

	// First injection should succeed
	coord.handleIdleState(ctx, tracker, client.output)

	client.mu.Lock()
	count1 := len(client.sent)
	client.mu.Unlock()

	if count1 != 1 {
		t.Fatalf("expected 1 injection on first call, got %d", count1)
	}

	// Second call should be blocked by cooldown
	coord.handleIdleState(ctx, tracker, client.output)

	client.mu.Lock()
	count2 := len(client.sent)
	client.mu.Unlock()

	if count2 != 1 {
		t.Fatalf("expected still 1 injection (cooldown should block), got %d", count2)
	}
}

func TestCoordinator_CompactionReminderSkipsIfAlreadyPresent(t *testing.T) {
	prompt := "Reread AGENTS.md so it's still fresh in your mind."
	client := &fakePaneClient{
		panes: []Pane{{PaneID: 1}},
		// Output contains both compaction banner AND the reminder already
		output: "Conversation compacted · ctrl+o for history\n" + prompt,
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = prompt
	cfg.CompactionReminderCooldown = 10 * time.Minute
	cfg.PaneClient = client

	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = ""
	coord.trackers[1] = tracker

	ctx := context.Background()

	// Should NOT inject because reminder is already in output
	coord.handleIdleState(ctx, tracker, client.output)

	client.mu.Lock()
	sent := client.sent
	client.mu.Unlock()

	if len(sent) != 0 {
		t.Fatalf("expected no injections when reminder already present, got %d: %v", len(sent), sent)
	}
}

func TestCoordinator_CompactionReminderDoesNotTriggerOnRateLimit(t *testing.T) {
	client := &fakePaneClient{
		panes: []Pane{{PaneID: 1}},
		// Output has BOTH rate limit AND compaction banner
		output: "You've hit your limit · resets 2pm\nConversation compacted · ctrl+o for history",
	}

	cfg := DefaultConfig()
	cfg.CompactionReminderEnabled = true
	cfg.CompactionReminderPrompt = "Test reminder"
	cfg.PaneClient = client

	coord := New(cfg)

	tracker := NewPaneTracker(1)
	tracker.LastOutput = ""
	coord.trackers[1] = tracker

	ctx := context.Background()

	// Call handleIdleState - rate limit takes precedence
	coord.handleIdleState(ctx, tracker, client.output)

	client.mu.Lock()
	sent := client.sent
	client.mu.Unlock()

	// Should inject /login, not the compaction reminder
	if len(sent) != 1 {
		t.Fatalf("expected 1 injection, got %d: %v", len(sent), sent)
	}
	if sent[0] != "/login\n" {
		t.Errorf("expected /login injection (rate limit takes precedence), got %q", sent[0])
	}
}
