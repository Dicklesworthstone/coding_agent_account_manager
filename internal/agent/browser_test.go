package agent

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
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
			name:     "Simple format",
			html:     `<div>Your code: MNOP-9012</div>`,
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

// TestAccountSelectionSelectors verifies the chooser selectors target the
// attribute Google's account chooser actually uses.
func TestAccountSelectionSelectors(t *testing.T) {
	got := preferredAccountSelectors("test@example.com")
	if len(got) == 0 || got[0] != `[data-identifier="test@example.com"]` {
		t.Fatalf("preferredAccountSelectors = %q, want data-identifier first", got)
	}
	for _, sel := range append(got, anyAccountSelectors...) {
		if !strings.Contains(sel, "[data-") {
			t.Errorf("account selector %q does not match on an account attribute", sel)
		}
	}
}

// TestConsentSelectors verifies consent button selector patterns.
func TestConsentSelectors(t *testing.T) {
	if len(consentSelectors) == 0 {
		t.Fatal("no consent selectors")
	}
	for _, selector := range consentSelectors {
		if strings.TrimSpace(selector) == "" {
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

func TestCodeFromCallbackURL(t *testing.T) {
	tests := []struct {
		url, want string
	}{
		{"https://console.anthropic.com/oauth/code/callback?code=aB3_dE-fGh1234567890&state=st4te_XYZ", "aB3_dE-fGh1234567890#st4te_XYZ"},
		{"https://platform.claude.com/oauth/code/callback/?code=onlycode", "onlycode"},
		{"https://console.anthropic.com/oauth/code/callback?error=access_denied", ""},
		{"https://claude.ai/oauth/authorize?code=true&client_id=x", ""},
		{"https://accounts.google.com/o/oauth2/v2/auth?code=nope", ""},
		{"::not a url", ""},
	}
	for _, tt := range tests {
		if got := codeFromCallbackURL(tt.url); got != tt.want {
			t.Errorf("codeFromCallbackURL(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestOnCodePage(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://console.anthropic.com/oauth/code/success":   true,
		"https://platform.claude.com/oauth/code/callback":    true,
		"https://claude.ai/oauth/authorize?code=true":        false, // consent page
		"https://claude.ai/login?returnTo=%2Foauth":          false, // not signed in to Claude
		"https://claude.ai/new":                              false,
		"https://accounts.google.com/signin/v2/identifier":   false,
		"https://evil-anthropic.com/oauth/code/callback":     false,
		"https://claude.ai.evil.example/oauth/code/callback": false,
	} {
		if got := onCodePage(raw); got != want {
			t.Errorf("onCodePage(%q) = %v, want %v", raw, got, want)
		}
	}
}

func TestExtractChallengeCodePrefersPasteReadyCode(t *testing.T) {
	html := `<div class="ABCDEFGHIJ">Paste this into Claude Code:</div><code>xY9_k2-mNpQrStUvWx12#Ab_cd-EF34</code>`
	if got := extractChallengeCode(html); got != "xY9_k2-mNpQrStUvWx12#Ab_cd-EF34" {
		t.Fatalf("extractChallengeCode = %q", got)
	}
}

func TestResolveChromeUserDataDir(t *testing.T) {
	caamHome := t.TempDir()
	t.Setenv("CAAM_HOME", caamHome)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}

	want := filepath.Join(caamHome, "data", "auth-agent-chrome")
	for _, in := range []string{"", "  "} {
		if got := ResolveChromeUserDataDir(in); got != want {
			t.Errorf("ResolveChromeUserDataDir(%q) = %q, want the persistent default %q", in, got, want)
		}
	}
	if got := ResolveChromeUserDataDir("~/chrome"); got != filepath.Join(home, "chrome") {
		t.Errorf("tilde not expanded: %q", got)
	}
	if got := ResolveChromeUserDataDir("/opt/chrome"); got != "/opt/chrome" {
		t.Errorf("absolute dir changed: %q", got)
	}
	if got := NewBrowser(BrowserConfig{}).UserDataDir(); got != want {
		t.Errorf("NewBrowser default profile = %q, want %q", got, want)
	}
}

// fakeChrome writes a script that records its arguments and exits with code.
func fakeChrome(t *testing.T, code int) (execPath, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in for Chrome")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	execPath = filepath.Join(dir, "chrome")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$@\" > %q\necho 'profile in use' >&2\nexit %d\n", argsFile, code)
	if err := os.WriteFile(execPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return execPath, argsFile
}

func TestOpenForSignInLaunchesChromeOnAgentProfile(t *testing.T) {
	execPath, argsFile := fakeChrome(t, 0)
	profile := filepath.Join(t.TempDir(), "nested", "profile")

	b := NewBrowser(BrowserConfig{UserDataDir: profile, ExecPath: execPath})
	if err := b.OpenForSignIn(context.Background(), SignInURLs...); err != nil {
		t.Fatalf("OpenForSignIn: %v", err)
	}

	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Split(strings.TrimSpace(string(data)), "\n")
	want := append([]string{"--user-data-dir=" + profile, "--no-first-run", "--no-default-browser-check"}, SignInURLs...)
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("chrome args = %q, want %q", got, want)
	}
	info, err := os.Stat(profile)
	if err != nil {
		t.Fatalf("profile dir not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("profile dir mode = %o, want 700 (it holds session cookies)", perm)
	}
}

func TestOpenForSignInReportsChromeFailure(t *testing.T) {
	execPath, _ := fakeChrome(t, 3)
	b := NewBrowser(BrowserConfig{UserDataDir: t.TempDir(), ExecPath: execPath})
	err := b.OpenForSignIn(context.Background(), "https://accounts.google.com/AddSession")
	if err == nil || !strings.Contains(err.Error(), "profile in use") {
		t.Fatalf("OpenForSignIn error = %v, want Chrome's exit status and output", err)
	}
}

// chromeForTest returns a Chrome or Chromium binary or skips the test.
func chromeForTest(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("drives a real browser")
	}
	for _, p := range []string{os.Getenv("CAAM_TEST_CHROME"), "/opt/pw-browsers/chromium", findChrome()} {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skip("Chrome/Chromium not installed")
	return ""
}

// connListener hands tunneled connections to an http.Server.
type connListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newConnListener() *connListener {
	return &connListener{conns: make(chan net.Conn), done: make(chan struct{})}
}

func (l *connListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *connListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return nil
}

func (l *connListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func (l *connListener) push(c net.Conn) {
	select {
	case l.conns <- c:
	case <-l.done:
		c.Close()
	}
}

// tunnelProxy is a CONNECT proxy that terminates every tunnel with a
// self-signed certificate and serves handler, so Chrome (with
// --ignore-certificate-errors) reaches fixture pages at real hostnames.
func tunnelProxy(t *testing.T, handler http.Handler) string {
	t.Helper()
	certSrv := httptest.NewTLSServer(http.NotFoundHandler())
	certs := certSrv.TLS.Certificates
	certSrv.Close()

	tunnels := newConnListener()
	// Chrome's own background connections drop mid-handshake; keep that
	// out of the test output.
	site := &http.Server{Handler: handler, ErrorLog: log.New(io.Discard, "", 0)}
	go site.Serve(tunnels)

	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		if _, err := conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
			conn.Close()
			return
		}
		tunnels.push(tls.Server(conn, &tls.Config{Certificates: certs}))
	}))
	t.Cleanup(func() {
		proxy.Close()
		site.Close()
		tunnels.Close()
	})
	return proxy.URL
}

// oauthFixture plays the Claude Code OAuth flow: authorize -> (no Claude
// session) Claude login -> "Continue with Google" -> Google account chooser
// -> Claude session for the chosen account -> consent -> code callback.
// Claude sessions are a real cookie, so clearing claude.ai cookies signs the
// browser out of Claude. Its markup follows the real pages where it matters:
// the login page's return URL mentions "authorize" and its submit button
// belongs to the email form, Google marks accounts with data-identifier
// only, the consent page names the signed-in account (next to a support
// address), and its button is not a submit button. With googleSignedOut,
// Google asks for an email instead of offering accounts; claudeSessionAs is
// a Claude session the browser already has on its first visit.
type oauthFixture struct {
	googleSignedOut bool
	claudeSessionAs string
	// googleConsent shows Google's "Sign in to claude.ai" consent screen
	// after an account is picked.
	googleConsent bool
	// noClaudeAccount is a Google account without a Claude account: signing
	// in with it lands on Claude's sign-up page.
	noClaudeAccount string

	mu          sync.Mutex
	sessionUsed bool
	chosen      []string // accounts picked in Google's chooser
	approved    []string // accounts whose consent was granted
	created     []string // Claude accounts created on the sign-up page
	emailSubmit int
}

func (f *oauthFixture) choices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.chosen...)
}

func (f *oauthFixture) approvals() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.approved...)
}

// claudeSession returns the request's Claude account, starting the
// preexisting session on the first visit.
func (f *oauthFixture) claudeSession(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie("sessionKey"); err == nil && c.Value != "" {
		return c.Value
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.claudeSessionAs == "" || f.sessionUsed {
		return ""
	}
	f.sessionUsed = true
	http.SetCookie(w, &http.Cookie{Name: "sessionKey", Value: f.claudeSessionAs, Path: "/", Secure: true, MaxAge: 30 * 24 * 3600})
	return f.claudeSessionAs
}

func (f *oauthFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := url.QueryEscape(q.Get("state"))
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	switch host + r.URL.Path {
	case "claude.ai/oauth/authorize":
		account := f.claudeSession(w, r)
		if account == "" {
			http.Redirect(w, r, "https://claude.ai/login?returnTo="+url.QueryEscape("/oauth/authorize?state="+q.Get("state")), http.StatusFound)
			return
		}
		fmt.Fprintf(w, `<html><body><p>Claude Code would like to connect to your Claude account.</p>
<p>Signed in as %s</p><p>Questions? Contact support@anthropic.com</p>
<button type="button" aria-label="Allow access" onclick="location.href='/oauth/approve?state=%s'">Authorize</button></body></html>`, account, state)
	case "claude.ai/login":
		ret, _ := url.Parse(q.Get("returnTo"))
		state := url.QueryEscape(ret.Query().Get("state"))
		fmt.Fprintf(w, `<html><body><form action="/login/email" method="post">
<input type="hidden" name="returnTo" value="%s"><input name="email" placeholder="Enter your email">
<button type="submit">Continue with email</button></form>
<button type="button" onclick="location.href='https://accounts.google.com/v3/signin/accountchooser?state=%s'"><img alt="">Continue with Google</button>
</body></html>`, q.Get("returnTo"), state)
	case "claude.ai/login/email":
		f.mu.Lock()
		f.emailSubmit++
		f.mu.Unlock()
		fmt.Fprint(w, `<html><body>Check your email</body></html>`)
	case "accounts.google.com/v3/signin/picked":
		next := "https://claude.ai/login/google/callback?state=" + state + "&account=" + url.QueryEscape(q.Get("account"))
		if f.googleConsent {
			http.Redirect(w, r, "/signin/oauth/id?next="+url.QueryEscape(next), http.StatusFound)
			return
		}
		http.Redirect(w, r, next, http.StatusFound)
	case "accounts.google.com/signin/oauth/id":
		fmt.Fprintf(w, `<html><body><h1>Sign in to claude.ai</h1><p>Google will share your name and email with claude.ai.</p>
<button type="button" aria-label="Continue" onclick="location.href='%s'">Continue</button></body></html>`, q.Get("next"))
	case "claude.ai/login/google/callback":
		account := q.Get("account")
		f.mu.Lock()
		f.chosen = append(f.chosen, account)
		f.mu.Unlock()
		if account == f.noClaudeAccount {
			http.Redirect(w, r, "/onboarding?state="+state+"&account="+url.QueryEscape(account), http.StatusFound)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: "sessionKey", Value: account, Path: "/", Secure: true, MaxAge: 30 * 24 * 3600})
		http.Redirect(w, r, "/oauth/authorize?state="+state, http.StatusFound)
	case "claude.ai/onboarding":
		fmt.Fprintf(w, `<html><body><h1>Create your Claude account</h1>
<p>To continue, authorize Anthropic to process your data and allow marketing email.</p>
<form action="/onboarding/create?account=%s" method="post"><button type="submit">Create account</button></form></body></html>`, url.QueryEscape(q.Get("account")))
	case "claude.ai/onboarding/create":
		f.mu.Lock()
		f.created = append(f.created, q.Get("account"))
		f.mu.Unlock()
		fmt.Fprint(w, `<html><body>Account created</body></html>`)
	case "accounts.google.com/v3/signin/identifier":
		fmt.Fprint(w, `<html><body><h1>Sign in</h1><input type="email" aria-label="Email or phone"><button type="button">Next</button></body></html>`)
	case "accounts.google.com/v3/signin/accountchooser":
		if f.googleSignedOut {
			http.Redirect(w, r, "/v3/signin/identifier?state="+state, http.StatusFound)
			return
		}
		fmt.Fprintf(w, `<html><body><h1>Choose an account</h1><ul>
<li><div role="link" data-identifier="a@example.com" onclick="pick(this)">Alice a@example.com</div></li>
<li><div role="link" data-identifier="b@example.com" onclick="pick(this)">Bob b@example.com</div></li>
</ul><script>function pick(el){location.href="https://accounts.google.com/v3/signin/picked?state=%s&account="+encodeURIComponent(el.dataset.identifier)}</script></body></html>`, state)
	case "claude.ai/oauth/approve":
		c, _ := r.Cookie("sessionKey")
		f.mu.Lock()
		if c != nil {
			f.approved = append(f.approved, c.Value)
		}
		f.mu.Unlock()
		http.Redirect(w, r, "https://console.anthropic.com/oauth/code/callback?code=fixture-code-123&state="+state, http.StatusFound)
	case "console.anthropic.com/oauth/code/callback":
		fmt.Fprint(w, `<html><body><p>Paste this into Claude Code</p></body></html>`)
	default:
		http.NotFound(w, r)
	}
}

func fixtureBrowser(t *testing.T, fixture http.Handler) *Browser {
	t.Helper()
	chrome := chromeForTest(t)
	// Not t.TempDir: Chrome's helper processes can write to the profile for
	// a moment after the browser exits, which fails its strict cleanup.
	profile, err := os.MkdirTemp("", "caam-chrome-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for range 30 {
			if os.RemoveAll(profile) == nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	})
	b := NewBrowser(BrowserConfig{UserDataDir: profile, ExecPath: chrome, Headless: true})
	b.stepDelay = 200 * time.Millisecond
	b.flowTimeout = 45 * time.Second
	b.extraOpts = []chromedp.ExecAllocatorOption{
		chromedp.ProxyServer(tunnelProxy(t, fixture)),
		chromedp.Flag("ignore-certificate-errors", true),
		chromedp.NoSandbox,
	}
	return b
}

func TestCompleteOAuthInChromeSelectsPreferredAccount(t *testing.T) {
	fixture := &oauthFixture{}
	b := fixtureBrowser(t, fixture)

	start := time.Now()
	code, account, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-42", []string{"b@example.com"})
	if err != nil {
		t.Fatalf("CompleteOAuth: %v (after %v)", err, time.Since(start))
	}
	if code != "fixture-code-123#st-42" {
		t.Errorf("code = %q, want the callback's code#state", code)
	}
	if account != "b@example.com" {
		t.Errorf("account = %q, want the preferred account", account)
	}
	if got := fixture.choices(); len(got) != 1 || got[0] != "b@example.com" {
		t.Errorf("accounts chosen in Google's chooser = %q, want only b@example.com", got)
	}
	fixture.mu.Lock()
	emailSubmits := fixture.emailSubmit
	fixture.mu.Unlock()
	if emailSubmits != 0 {
		t.Errorf("submitted Claude's email login form %d times; the login page must continue with Google", emailSubmits)
	}
	// A selector that is absent from the page must not stall the flow.
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("flow took %v", elapsed)
	}
}

func TestCompleteOAuthInChromeFallsBackWithinConfiguredAccounts(t *testing.T) {
	fixture := &oauthFixture{}
	b := fixtureBrowser(t, fixture)

	// The preferred account is not signed in to this profile: the next
	// configured account is used, and that is what must be reported.
	code, account, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-7", []string{"zoe@example.com", "b@example.com"})
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if code != "fixture-code-123#st-7" {
		t.Errorf("code = %q", code)
	}
	if got := fixture.choices(); len(got) != 1 || got[0] != "b@example.com" {
		t.Fatalf("accounts chosen = %q, want the next configured account", got)
	}
	if account != "b@example.com" {
		t.Errorf("account = %q, want the account actually chosen (b@example.com)", account)
	}
}

func TestCompleteOAuthInChromeNeverUsesUnconfiguredAccounts(t *testing.T) {
	fixture := &oauthFixture{}
	b := fixtureBrowser(t, fixture)

	// The chooser offers a@ and b@, neither configured.
	_, _, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-u", []string{"zoe@example.com"})
	if err == nil || !strings.Contains(err.Error(), "zoe@example.com") || !strings.Contains(err.Error(), "caam auth-agent signin") {
		t.Fatalf("err = %v, want none of the configured accounts signed in", err)
	}
	if got := fixture.choices(); len(got) != 0 {
		t.Fatalf("signed in with %q, an account that was not configured", got)
	}

	// A Claude session for an unconfigured account is never approved.
	fixture = &oauthFixture{claudeSessionAs: "a@example.com"}
	b = fixtureBrowser(t, fixture)
	if _, _, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-v", []string{"zoe@example.com"}); err == nil {
		t.Fatal("CompleteOAuth succeeded without any configured account")
	}
	if got := fixture.approvals(); len(got) != 0 {
		t.Fatalf("approved %q, an account that was not configured", got)
	}
}

func TestCompleteOAuthInChromeWithoutAccountsUsesFirstOffered(t *testing.T) {
	fixture := &oauthFixture{}
	b := fixtureBrowser(t, fixture)
	_, account, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-f", nil)
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if account != "a@example.com" {
		t.Errorf("account = %q, want the first offered (a@example.com)", account)
	}
}

func TestCompleteOAuthInChromeExplainsSignedOutProfile(t *testing.T) {
	fixture := &oauthFixture{googleSignedOut: true}
	b := fixtureBrowser(t, fixture)

	_, _, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=secret-state", []string{"a@example.com"})
	if err == nil {
		t.Fatal("CompleteOAuth succeeded without any signed-in Google account")
	}
	msg := err.Error()
	if !strings.Contains(msg, "caam auth-agent signin") || !strings.Contains(msg, "accounts.google.com/v3/signin/identifier") {
		t.Errorf("error = %q, want the sign-in page and the signin remedy", msg)
	}
	if strings.Contains(msg, "secret-state") {
		t.Errorf("error leaks OAuth state: %q", msg)
	}
}

func TestStuckFlowError(t *testing.T) {
	for raw, want := range map[string]string{
		"https://claude.ai/login?returnTo=%2Foauth%2Fauthorize%3Fstate%3Dx": "caam auth-agent signin",
		"https://accounts.google.com/v3/signin/identifier?state=x":          "caam auth-agent signin",
		"https://claude.ai/oauth/authorize?state=x":                         "last page claude.ai/oauth/authorize",
		"": "no authorization code found",
	} {
		msg := stuckFlowError(raw).Error()
		if !strings.Contains(msg, want) {
			t.Errorf("stuckFlowError(%q) = %q, want it to mention %q", raw, msg, want)
		}
		if strings.Contains(msg, "state=") {
			t.Errorf("stuckFlowError(%q) leaks the query: %q", raw, msg)
		}
	}
}

func TestCompleteOAuthInChromeSwitchesFromAnotherClaudeAccount(t *testing.T) {
	// The profile is still signed in to Claude as a@, which would approve
	// a@ again (likely the account that just hit its limit).
	fixture := &oauthFixture{claudeSessionAs: "a@example.com"}
	b := fixtureBrowser(t, fixture)

	code, account, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-9", []string{"b@example.com"})
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if code != "fixture-code-123#st-9" || account != "b@example.com" {
		t.Fatalf("code=%q account=%q, want b@example.com's code", code, account)
	}
	if got := fixture.approvals(); len(got) != 1 || got[0] != "b@example.com" {
		t.Fatalf("consent granted for %q, want only b@example.com", got)
	}
	if got := fixture.choices(); len(got) != 1 || got[0] != "b@example.com" {
		t.Fatalf("Google chooser picks = %q", got)
	}
}

func TestCompleteOAuthInChromeKeepsMatchingClaudeSession(t *testing.T) {
	fixture := &oauthFixture{claudeSessionAs: "b@example.com"}
	b := fixtureBrowser(t, fixture)

	code, account, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-3", []string{"b@example.com"})
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if code != "fixture-code-123#st-3" || account != "b@example.com" {
		t.Fatalf("code=%q account=%q", code, account)
	}
	if got := fixture.choices(); len(got) != 0 {
		t.Fatalf("signed in again (%q) although Claude already had the selected account", got)
	}
	if got := fixture.approvals(); len(got) != 1 || got[0] != "b@example.com" {
		t.Fatalf("approvals = %q", got)
	}
}

func TestShownAccounts(t *testing.T) {
	text := "Signed in as Bob.Smith@Example.com\nNeed help? support@anthropic.com or privacy@claude.ai\nbob.smith@example.com"
	if got := shownAccounts(text); len(got) != 1 || got[0] != "bob.smith@example.com" {
		t.Fatalf("shownAccounts = %q, want the one account, without Anthropic addresses", got)
	}
}

func writeCookieDB(t *testing.T, dir string, rows [][2]string) {
	t.Helper()
	path := filepath.Join(dir, "Default", "Network", "Cookies")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT NOT NULL, name TEXT NOT NULL, encrypted_value BLOB)`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO cookies (host_key, name, encrypted_value) VALUES (?, ?, x'00')`, r[0], r[1]); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProfileSessions(t *testing.T) {
	if g, c, err := ProfileSessions(t.TempDir()); err != nil || g || c {
		t.Fatalf("unused profile = %v %v %v, want no sessions", g, c, err)
	}

	signedOut := t.TempDir()
	writeCookieDB(t, signedOut, [][2]string{{".google.com", "NID"}, {"accounts.google.com", "__Host-GAPS"}, {"claude.ai", "anthropic-device-id"}})
	if g, c, err := ProfileSessions(signedOut); err != nil || g || c {
		t.Fatalf("consent/device cookies only = %v %v %v, want no sessions", g, c, err)
	}

	signedIn := t.TempDir()
	writeCookieDB(t, signedIn, [][2]string{{".google.com", "__Secure-1PSID"}, {".claude.ai", "sessionKey"}})
	if g, c, err := ProfileSessions(signedIn); err != nil || !g || !c {
		t.Fatalf("signed-in profile = %v %v %v, want both sessions", g, c, err)
	}
}

func TestProfileSessionsReadsRealChromeProfile(t *testing.T) {
	fixture := &oauthFixture{claudeSessionAs: "b@example.com"}
	b := fixtureBrowser(t, fixture)
	if _, _, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-p", []string{"b@example.com"}); err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	// Chrome closed gracefully, so the claude.ai session cookie the fixture
	// set during the flow is in its cookie store on disk.
	_, claude, err := ProfileSessions(b.UserDataDir())
	if err != nil || !claude {
		t.Errorf("ProfileSessions after a Claude sign-in = claude:%v err:%v", claude, err)
		filepath.Walk(b.UserDataDir(), func(path string, info os.FileInfo, err error) error {
			if err == nil && strings.Contains(filepath.Base(path), "Cookies") {
				t.Logf("cookie store %s (%d bytes)", path, info.Size())
				if db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1"); err == nil {
					if rows, err := db.Query(`SELECT host_key, name FROM cookies`); err == nil {
						for rows.Next() {
							var host, name string
							rows.Scan(&host, &name)
							t.Logf("  cookie %s %s", host, name)
						}
						rows.Close()
					} else {
						t.Logf("  query: %v", err)
					}
					db.Close()
				}
			}
			return nil
		})
	}
}

func TestCompleteOAuthInChromeClicksThroughGoogleConsent(t *testing.T) {
	fixture := &oauthFixture{googleConsent: true}
	b := fixtureBrowser(t, fixture)
	code, account, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-g", []string{"b@example.com"})
	if err != nil {
		t.Fatalf("CompleteOAuth: %v", err)
	}
	if code != "fixture-code-123#st-g" || account != "b@example.com" {
		t.Fatalf("code=%q account=%q", code, account)
	}
}

func TestCompleteOAuthInChromeNeverSignsUpForClaude(t *testing.T) {
	// The preferred account has no Claude account: Claude's sign-up page
	// mentions "authorize" and "allow" and has a submit button. The agent
	// must stop there, not create an account.
	fixture := &oauthFixture{noClaudeAccount: "b@example.com"}
	b := fixtureBrowser(t, fixture)
	_, _, err := b.CompleteOAuth(context.Background(),
		"https://claude.ai/oauth/authorize?code=true&client_id=c&state=st-n", []string{"b@example.com"})
	if err == nil {
		t.Fatal("CompleteOAuth succeeded through a sign-up page")
	}
	if !strings.Contains(err.Error(), "claude.ai/onboarding") {
		t.Errorf("error = %v, want it to name the page it stopped on", err)
	}
	fixture.mu.Lock()
	created := append([]string(nil), fixture.created...)
	fixture.mu.Unlock()
	if len(created) != 0 {
		t.Fatalf("created Claude accounts %q", created)
	}
}
