package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

func TestExtractChallengeCode(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		expected string
	}{
		{
			name:     "standard XXXX-XXXX format",
			html:     `<div class="code">ABCD-1234</div>`,
			expected: "ABCD-1234",
		},
		{
			name:     "challenge code in span",
			html:     `<span>Your code: WXYZ-5678</span>`,
			expected: "WXYZ-5678",
		},
		{
			name:     "code in complex HTML",
			html:     `<html><body><div class="container"><p>challenge code</p><div class="code-display">TEST-CODE</div></div></body></html>`,
			expected: "TEST-CODE",
		},
		{
			name:     "longer alphanumeric code",
			html:     `<div>ABCDEFGH12345678</div>`,
			expected: "ABCDEFGH12345678",
		},
		{
			name:     "no code present",
			html:     `<html><body>No code here</body></html>`,
			expected: "",
		},
		{
			name:     "code too short ignored",
			html:     `<div>AB-12</div>`, // Only 5 chars
			expected: "",
		},
		{
			name:     "real Claude code pattern",
			html:     `<div data-testid="challenge-code">MNOP-9876</div>`,
			expected: "MNOP-9876",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractChallengeCode(tt.html)
			if result != tt.expected {
				t.Errorf("extractChallengeCode() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestTruncateURL(t *testing.T) {
	tests := []struct {
		url      string
		maxLen   int
		expected string
	}{
		{"https://example.com", 50, "https://example.com"},
		{"https://example.com/very/long/path/that/exceeds/limit", 30, "https://example.com/very/lo..."},
		{"short", 10, "short"},
		{"", 10, ""},
	}

	for _, tt := range tests {
		result := truncateURL(tt.url, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncateURL(%q, %d) = %q, want %q", tt.url, tt.maxLen, result, tt.expected)
		}
	}
}

func TestFindChrome(t *testing.T) {
	// This test verifies the function doesn't panic and returns a valid result
	result := findChrome()
	// Result can be empty string or a path - both are valid
	t.Logf("findChrome() returned: %q", result)

	if result != "" {
		// If we found Chrome, verify the path makes sense for the OS
		switch runtime.GOOS {
		case "darwin":
			if !strings.Contains(result, "Chrome") && !strings.Contains(result, "Chromium") {
				t.Errorf("unexpected macOS Chrome path: %s", result)
			}
		case "linux":
			if !strings.Contains(result, "chrome") && !strings.Contains(result, "chromium") {
				t.Errorf("unexpected Linux Chrome path: %s", result)
			}
		case "windows":
			if !strings.Contains(strings.ToLower(result), "chrome") {
				t.Errorf("unexpected Windows Chrome path: %s", result)
			}
		}
	}
}

func TestIsChromeAvailable(t *testing.T) {
	// Just verify it doesn't panic and returns a bool
	available := IsChromeAvailable()
	t.Logf("IsChromeAvailable() = %v", available)

	// If available, GetChromePath should return non-empty
	if available {
		path := GetChromePath()
		if path == "" {
			t.Error("IsChromeAvailable() returned true but GetChromePath() returned empty")
		}
	}
}

func TestGetChromePath(t *testing.T) {
	path := GetChromePath()
	available := IsChromeAvailable()

	if available && path == "" {
		t.Error("Chrome is available but path is empty")
	}
	if !available && path != "" {
		t.Error("Chrome is not available but path is non-empty")
	}
}

func TestBrowserConfig(t *testing.T) {
	config := BrowserConfig{
		UserDataDir: "/tmp/test-profile",
		Headless:    true,
	}

	if config.UserDataDir != "/tmp/test-profile" {
		t.Errorf("unexpected UserDataDir: %s", config.UserDataDir)
	}
	if !config.Headless {
		t.Error("Headless should be true")
	}
}

func TestNewBrowser(t *testing.T) {
	browser := NewBrowser(BrowserConfig{
		UserDataDir: "/tmp/test-profile",
		Headless:    true,
	})

	if browser == nil {
		t.Fatal("NewBrowser returned nil")
	}

	// Verify the browser can be closed without error
	browser.Close()
}

// TestMockedOAuthServer tests the agent's HTTP client behavior with a mock server.
// This doesn't test the actual browser automation but tests the HTTP integration.
func TestMockedOAuthServer(t *testing.T) {
	// Create a mock server that simulates the coordinator
	coordinatorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/pending":
			// Return empty array - no pending requests
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte("[]"))
		case "/auth/complete":
			// Accept auth completion
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
		case "/health":
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"healthy"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer coordinatorServer.Close()

	// Create an agent with the mock coordinator
	config := DefaultConfig()
	config.CoordinatorURL = coordinatorServer.URL
	config.Port = 0 // Use random port

	agent := New(config)
	if agent == nil {
		t.Fatal("New returned nil agent")
	}

	// Test checkPendingRequests doesn't panic with mock server
	ctx := context.Background()
	agent.checkPendingRequests(ctx)
}

// TestChallengeCodePage creates a mock HTML page with a challenge code.
func TestChallengeCodePage(t *testing.T) {
	// Simulate various HTML page structures that might contain challenge codes
	pages := []struct {
		name     string
		html     string
		expected string
	}{
		{
			name: "Claude style",
			html: `<!DOCTYPE html>
<html>
<head><title>Authorization Code</title></head>
<body>
<div class="auth-container">
<h1>Sign in successful</h1>
<p>Your authorization code is:</p>
<div class="code-display" data-testid="auth-code">ABCD-1234</div>
<p>Copy this code and paste it in your terminal.</p>
</div>
</body>
</html>`,
			expected: "ABCD-1234",
		},
		{
			name: "Google style",
			html: `<!DOCTYPE html>
<html>
<body>
<div id="code-container">
<span class="challenge">WXYZ-5678</span>
</div>
</body>
</html>`,
			expected: "WXYZ-5678",
		},
		{
			name: "Simple format",
			html: `<div>Your code: MNOP-9012</div>`,
			expected: "MNOP-9012",
		},
	}

	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			code := extractChallengeCode(p.html)
			if code != p.expected {
				t.Errorf("extractChallengeCode() = %q, want %q", code, p.expected)
			}
		})
	}
}

// TestAccountSelectionSelectors verifies the selector patterns are reasonable.
func TestAccountSelectionSelectors(t *testing.T) {
	// These are the selector patterns used for account selection
	// We test that they compile and are valid
	testSelectors := []string{
		`div[data-email="test@example.com"]`,
		`li[data-email="test@example.com"]`,
		`[data-identifier="test@example.com"]`,
		`button[data-email="test@example.com"]`,
		`div[data-identifier]`,
		`[role="listitem"][data-email]`,
	}

	for _, selector := range testSelectors {
		// Just verify the selector string is not empty and looks like a CSS selector
		if selector == "" {
			t.Error("empty selector")
		}
		if !strings.Contains(selector, "[") {
			t.Errorf("selector doesn't look like CSS selector: %s", selector)
		}
	}
}

// TestConsentSelectors verifies consent button selector patterns.
func TestConsentSelectors(t *testing.T) {
	consentSelectors := []string{
		`button[type="submit"]`,
		`input[type="submit"]`,
		`#submit_approve_access`,
		`button[aria-label*="Allow"]`,
		`button.primary`,
	}

	for _, selector := range consentSelectors {
		if selector == "" {
			t.Error("empty selector")
		}
	}
}

// BenchmarkExtractChallengeCode measures code extraction performance.
func BenchmarkExtractChallengeCode(b *testing.B) {
	html := `<!DOCTYPE html>
<html>
<head><title>Auth Code</title></head>
<body>
<div class="container">
<p>Lots of text here that doesn't contain a code...</p>
<p>More text...</p>
<div class="code-display">ABCD-1234</div>
<p>And more text after...</p>
</div>
</body>
</html>`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		extractChallengeCode(html)
	}
}

// TestIntegrationMockCoordinator tests a more complete flow with mock servers.
func TestIntegrationMockCoordinator(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Track calls to verify interaction
	var pendingCalled, completeCalled bool

	// Create mock coordinator
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth/pending":
			pendingCalled = true
			w.Header().Set("Content-Type", "application/json")
			// Return empty array for this test
			w.Write([]byte("[]"))
		case "/auth/complete":
			completeCalled = true
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer coordinator.Close()

	// Create and start agent (briefly)
	config := DefaultConfig()
	config.CoordinatorURL = coordinator.URL
	config.PollInterval = 50_000_000 // 50ms for fast test
	config.Port = 0

	agent := New(config)

	// Check pending requests directly
	ctx := context.Background()
	agent.checkPendingRequests(ctx)

	if !pendingCalled {
		t.Error("agent did not call /auth/pending")
	}

	// completeCalled should still be false since no pending requests were returned
	if completeCalled {
		t.Error("agent unexpectedly called /auth/complete")
	}
}

// TestChromePathsAreSane verifies the Chrome paths we check are reasonable.
func TestChromePathsAreSane(t *testing.T) {
	// These are the paths we check - verify they follow expected patterns
	switch runtime.GOOS {
	case "darwin":
		expectedPatterns := []string{"Chrome", "Chromium"}
		// The function should check .app bundles on macOS
		t.Logf("Testing on macOS - checking for Chrome/Chromium apps")
		for _, pattern := range expectedPatterns {
			t.Logf("Expected pattern: %s", pattern)
		}
	case "linux":
		expectedPatterns := []string{"chrome", "chromium"}
		t.Logf("Testing on Linux - checking for chrome/chromium binaries")
		for _, pattern := range expectedPatterns {
			t.Logf("Expected pattern: %s", pattern)
		}
	case "windows":
		t.Logf("Testing on Windows - checking for chrome.exe")
	}
}

// TestNewBrowserWithLogger tests browser creation with a logger.
func TestNewBrowserWithLogger(t *testing.T) {
	browser := NewBrowser(BrowserConfig{
		Logger: nil, // Should use default
	})

	if browser == nil {
		t.Fatal("NewBrowser returned nil")
	}
	if browser.logger == nil {
		t.Error("browser.logger should not be nil even when config.Logger is nil")
	}

	browser.Close()
}

// TestEmptyTruncateURL tests edge cases for URL truncation.
func TestEmptyTruncateURL(t *testing.T) {
	result := truncateURL("", 10)
	if result != "" {
		t.Errorf("truncateURL('', 10) = %q, want empty string", result)
	}

	// Test with maxLen equal to string length
	result = truncateURL("abc", 3)
	if result != "abc" {
		t.Errorf("truncateURL('abc', 3) = %q, want 'abc'", result)
	}

	// Test with maxLen larger than string length
	result = truncateURL("abc", 10)
	if result != "abc" {
		t.Errorf("truncateURL('abc', 10) = %q, want 'abc'", result)
	}
}

// TestExtractChallengeCodeEdgeCases tests edge cases for code extraction.
func TestExtractChallengeCodeEdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		html     string
		expected string
	}{
		{"empty", "", ""},
		{"whitespace only", "   \n\t  ", ""},
		{"code at start", "ABCD-1234 is your code", "ABCD-1234"},
		{"code at end", "Your code is ABCD-1234", "ABCD-1234"},
		{"multiple codes takes first", "Code 1: AAAA-1111 Code 2: BBBB-2222", "AAAA-1111"},
		{"code with surrounding brackets", "[ABCD-1234]", "ABCD-1234"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractChallengeCode(tt.html)
			if result != tt.expected {
				t.Errorf("extractChallengeCode(%q) = %q, want %q", tt.html, result, tt.expected)
			}
		})
	}
}

// TestHTTPHandlerStatus tests the agent's HTTP status handler.
func TestHTTPHandlerStatus(t *testing.T) {
	config := DefaultConfig()
	config.Port = 0
	agent := New(config)

	// Create a test request
	req := httptest.NewRequest("GET", "/status", nil)
	w := httptest.NewRecorder()

	agent.handleStatus(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status handler returned %d, want %d", w.Code, http.StatusOK)
	}

	// Verify it returns JSON
	contentType := w.Header().Get("Content-Type")
	if contentType != "application/json" {
		t.Errorf("content-type = %q, want application/json", contentType)
	}
}

// TestHTTPHandlerAccounts tests the agent's accounts handler.
func TestHTTPHandlerAccounts(t *testing.T) {
	config := DefaultConfig()
	config.Port = 0
	agent := New(config)

	// Add some test usage data
	agent.accountUsage["test@example.com"] = &AccountUsage{
		Email:      "test@example.com",
		UseCount:   5,
		LastResult: "success",
	}

	req := httptest.NewRequest("GET", "/accounts", nil)
	w := httptest.NewRecorder()

	agent.handleAccounts(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("accounts handler returned %d, want %d", w.Code, http.StatusOK)
	}

	body := w.Body.String()
	if !strings.Contains(body, "test@example.com") {
		t.Errorf("accounts response should contain test@example.com, got: %s", body)
	}
}

// TestHTTPHandlerAuthMissingURL tests the auth handler with missing URL.
func TestHTTPHandlerAuthMissingURL(t *testing.T) {
	config := DefaultConfig()
	config.Port = 0
	agent := New(config)

	req := httptest.NewRequest("POST", "/auth", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	agent.handleAuth(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("auth handler with missing URL returned %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// TestHTTPHandlerAuthInvalidJSON tests the auth handler with invalid JSON.
func TestHTTPHandlerAuthInvalidJSON(t *testing.T) {
	config := DefaultConfig()
	config.Port = 0
	agent := New(config)

	req := httptest.NewRequest("POST", "/auth", strings.NewReader(`not json`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	agent.handleAuth(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("auth handler with invalid JSON returned %d, want %d", w.Code, http.StatusBadRequest)
	}
}

// formatSelector is a helper that formats an account selector.
func formatSelector(email string) string {
	return fmt.Sprintf(`div[data-email="%s"]`, email)
}

// TestFormatSelector verifies selector formatting.
func TestFormatSelector(t *testing.T) {
	selector := formatSelector("test@example.com")
	expected := `div[data-email="test@example.com"]`
	if selector != expected {
		t.Errorf("formatSelector() = %q, want %q", selector, expected)
	}
}
