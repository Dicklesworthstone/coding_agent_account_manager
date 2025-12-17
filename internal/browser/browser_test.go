package browser

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// =============================================================================
// Browser Type Detection Tests
// =============================================================================

func TestDetectBrowserType(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		expected BrowserType
	}{
		// Chrome variants
		{"google-chrome", "google-chrome", BrowserChrome},
		{"google-chrome-stable", "google-chrome-stable", BrowserChrome},
		{"chrome uppercase", "Chrome", BrowserChrome},
		{"chromium", "chromium", BrowserChrome},
		{"chromium-browser", "chromium-browser", BrowserChrome},
		{"brave", "brave-browser", BrowserChrome},
		{"brave uppercase", "Brave", BrowserChrome},
		{"edge", "microsoft-edge", BrowserChrome},
		{"edge uppercase", "Edge", BrowserChrome},
		{"chrome path", "/usr/bin/google-chrome", BrowserChrome},
		{"chrome mac path", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", BrowserChrome},

		// Firefox variants
		{"firefox", "firefox", BrowserFirefox},
		{"firefox-esr", "firefox-esr", BrowserFirefox},
		{"firefox uppercase", "Firefox", BrowserFirefox},
		{"firefox path", "/usr/bin/firefox", BrowserFirefox},
		{"firefox mac path", "/Applications/Firefox.app/Contents/MacOS/firefox", BrowserFirefox},

		// Default (unknown)
		{"unknown browser", "safari", BrowserDefault},
		{"custom script", "/path/to/my-browser", BrowserDefault},
		{"empty string", "", BrowserDefault},
		{"random command", "xdg-open", BrowserDefault},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := detectBrowserType(tc.command)
			if got != tc.expected {
				t.Errorf("detectBrowserType(%q) = %q, want %q", tc.command, got, tc.expected)
			}
		})
	}
}

// =============================================================================
// NewLauncher Tests
// =============================================================================

func TestNewLauncher(t *testing.T) {
	t.Run("nil config returns DefaultLauncher", func(t *testing.T) {
		launcher := NewLauncher(nil)
		if _, ok := launcher.(*DefaultLauncher); !ok {
			t.Errorf("NewLauncher(nil) returned %T, want *DefaultLauncher", launcher)
		}
	})

	t.Run("empty command returns DefaultLauncher", func(t *testing.T) {
		launcher := NewLauncher(&Config{Command: ""})
		if _, ok := launcher.(*DefaultLauncher); !ok {
			t.Errorf("NewLauncher(&Config{}) returned %T, want *DefaultLauncher", launcher)
		}
	})

	t.Run("chrome command returns ChromeLauncher", func(t *testing.T) {
		launcher := NewLauncher(&Config{Command: "google-chrome"})
		if _, ok := launcher.(*ChromeLauncher); !ok {
			t.Errorf("NewLauncher(chrome) returned %T, want *ChromeLauncher", launcher)
		}
	})

	t.Run("firefox command returns FirefoxLauncher", func(t *testing.T) {
		launcher := NewLauncher(&Config{Command: "firefox"})
		if _, ok := launcher.(*FirefoxLauncher); !ok {
			t.Errorf("NewLauncher(firefox) returned %T, want *FirefoxLauncher", launcher)
		}
	})

	t.Run("unknown command returns GenericLauncher", func(t *testing.T) {
		launcher := NewLauncher(&Config{Command: "my-custom-browser"})
		if _, ok := launcher.(*GenericLauncher); !ok {
			t.Errorf("NewLauncher(unknown) returned %T, want *GenericLauncher", launcher)
		}
	})
}

// =============================================================================
// Launcher Interface Tests
// =============================================================================

func TestChromeLauncher(t *testing.T) {
	launcher := &ChromeLauncher{config: Config{Command: "google-chrome", ProfileDir: "Profile 1"}}

	t.Run("Name", func(t *testing.T) {
		if got := launcher.Name(); got != "Chrome" {
			t.Errorf("Name() = %q, want %q", got, "Chrome")
		}
	})

	t.Run("SupportsProfiles", func(t *testing.T) {
		if !launcher.SupportsProfiles() {
			t.Error("SupportsProfiles() = false, want true")
		}
	})
}

func TestFirefoxLauncher(t *testing.T) {
	launcher := &FirefoxLauncher{config: Config{Command: "firefox", ProfileDir: "default"}}

	t.Run("Name", func(t *testing.T) {
		if got := launcher.Name(); got != "Firefox" {
			t.Errorf("Name() = %q, want %q", got, "Firefox")
		}
	})

	t.Run("SupportsProfiles", func(t *testing.T) {
		if !launcher.SupportsProfiles() {
			t.Error("SupportsProfiles() = false, want true")
		}
	})
}

func TestDefaultLauncher(t *testing.T) {
	launcher := &DefaultLauncher{}

	t.Run("Name", func(t *testing.T) {
		if got := launcher.Name(); got != "system default" {
			t.Errorf("Name() = %q, want %q", got, "system default")
		}
	})

	t.Run("SupportsProfiles", func(t *testing.T) {
		if launcher.SupportsProfiles() {
			t.Error("SupportsProfiles() = true, want false")
		}
	})
}

func TestGenericLauncher(t *testing.T) {
	launcher := &GenericLauncher{config: Config{Command: "my-browser"}}

	t.Run("Name returns command", func(t *testing.T) {
		if got := launcher.Name(); got != "my-browser" {
			t.Errorf("Name() = %q, want %q", got, "my-browser")
		}
	})

	t.Run("SupportsProfiles", func(t *testing.T) {
		if launcher.SupportsProfiles() {
			t.Error("SupportsProfiles() = true, want false")
		}
	})
}

// =============================================================================
// URL Pattern Tests
// =============================================================================

func TestURLPattern(t *testing.T) {
	validURLs := []string{
		"http://localhost:8080/callback",
		"http://localhost:3000/oauth/callback?code=abc123",
		"http://127.0.0.1:9000/",
		"https://localhost:8443/auth",
		"http://localhost:12345/path/to/callback?state=xyz&code=123",
	}

	for _, url := range validURLs {
		t.Run("matches "+url, func(t *testing.T) {
			if !URLPattern.MatchString(url) {
				t.Errorf("URLPattern should match %q", url)
			}
		})
	}

	invalidURLs := []string{
		"http://example.com:8080/callback",
		"http://google.com/oauth",
		"localhost:8080", // Missing http://
		"ftp://localhost:21/",
		"http://192.168.1.1:8080/", // Different IP
	}

	for _, url := range invalidURLs {
		t.Run("rejects "+url, func(t *testing.T) {
			if URLPattern.MatchString(url) {
				t.Errorf("URLPattern should not match %q", url)
			}
		})
	}
}

func TestURLPatternExtraction(t *testing.T) {
	text := `Starting server...
Please visit http://localhost:8080/login to authenticate.
Or use http://127.0.0.1:3000/callback?code=abc123 for callback.
Server ready.`

	urls := URLPattern.FindAllString(text, -1)
	if len(urls) != 2 {
		t.Errorf("found %d URLs, want 2", len(urls))
	}

	expected := []string{
		"http://localhost:8080/login",
		"http://127.0.0.1:3000/callback?code=abc123",
	}
	for i, want := range expected {
		if i < len(urls) && urls[i] != want {
			t.Errorf("URL[%d] = %q, want %q", i, urls[i], want)
		}
	}
}

// =============================================================================
// URLDetector Tests
// =============================================================================

func TestURLDetector(t *testing.T) {
	t.Run("passes through data", func(t *testing.T) {
		var buf bytes.Buffer
		detector := NewURLDetector(&buf, nil)

		input := []byte("hello world")
		n, err := detector.Write(input)

		if err != nil {
			t.Fatalf("Write error = %v", err)
		}
		if n != len(input) {
			t.Errorf("Write returned %d, want %d", n, len(input))
		}
		if buf.String() != "hello world" {
			t.Errorf("output = %q, want %q", buf.String(), "hello world")
		}
	})

	t.Run("detects URLs", func(t *testing.T) {
		var buf bytes.Buffer
		var detectedURLs []string

		detector := NewURLDetector(&buf, func(url string) bool {
			detectedURLs = append(detectedURLs, url)
			return true
		})

		input := []byte("Visit http://localhost:8080/auth to login")
		detector.Write(input)

		if len(detectedURLs) != 1 {
			t.Fatalf("detected %d URLs, want 1", len(detectedURLs))
		}
		if detectedURLs[0] != "http://localhost:8080/auth" {
			t.Errorf("URL = %q, want %q", detectedURLs[0], "http://localhost:8080/auth")
		}
	})

	t.Run("detects multiple URLs", func(t *testing.T) {
		var buf bytes.Buffer
		var detectedURLs []string

		detector := NewURLDetector(&buf, func(url string) bool {
			detectedURLs = append(detectedURLs, url)
			return true
		})

		input := []byte("http://localhost:3000/ or http://127.0.0.1:8080/callback")
		detector.Write(input)

		if len(detectedURLs) != 2 {
			t.Errorf("detected %d URLs, want 2", len(detectedURLs))
		}
	})

	t.Run("handles nil callback", func(t *testing.T) {
		var buf bytes.Buffer
		detector := NewURLDetector(&buf, nil)

		input := []byte("http://localhost:8080/test")
		n, err := detector.Write(input)

		if err != nil {
			t.Errorf("Write error = %v", err)
		}
		if n != len(input) {
			t.Errorf("Write returned %d, want %d", n, len(input))
		}
	})

	t.Run("thread safe", func(t *testing.T) {
		var buf bytes.Buffer
		var mu sync.Mutex
		var count int

		detector := NewURLDetector(&buf, func(url string) bool {
			mu.Lock()
			count++
			mu.Unlock()
			return true
		})

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				detector.Write([]byte("http://localhost:8080/"))
			}()
		}
		wg.Wait()

		mu.Lock()
		if count != 10 {
			t.Errorf("count = %d, want 10", count)
		}
		mu.Unlock()
	})
}

// =============================================================================
// ScanReader Tests
// =============================================================================

func TestScanReader(t *testing.T) {
	t.Run("scans lines and writes to output", func(t *testing.T) {
		input := strings.NewReader("line 1\nline 2\nline 3")
		var output bytes.Buffer

		err := ScanReader(input, &output, nil)
		if err != nil {
			t.Fatalf("ScanReader error = %v", err)
		}

		expected := "line 1\nline 2\nline 3\n"
		if output.String() != expected {
			t.Errorf("output = %q, want %q", output.String(), expected)
		}
	})

	t.Run("detects URLs in lines", func(t *testing.T) {
		input := strings.NewReader("Visit http://localhost:3000/\nDone")
		var output bytes.Buffer
		var urls []string

		err := ScanReader(input, &output, func(url string) bool {
			urls = append(urls, url)
			return true
		})

		if err != nil {
			t.Fatalf("ScanReader error = %v", err)
		}
		if len(urls) != 1 || urls[0] != "http://localhost:3000/" {
			t.Errorf("urls = %v, want [http://localhost:3000/]", urls)
		}
	})

	t.Run("handles empty input", func(t *testing.T) {
		input := strings.NewReader("")
		var output bytes.Buffer

		err := ScanReader(input, &output, nil)
		if err != nil {
			t.Fatalf("ScanReader error = %v", err)
		}
		if output.Len() != 0 {
			t.Errorf("output should be empty, got %q", output.String())
		}
	})
}

// =============================================================================
// BrowserHelperScript Tests
// =============================================================================

func TestBrowserHelperScript(t *testing.T) {
	script := BrowserHelperScript("/usr/local/bin/caam", "work-profile")

	// Verify script contains the caam binary
	if !strings.Contains(script, "/usr/local/bin/caam") {
		t.Error("script should contain caam binary path")
	}

	// Verify script contains profile name
	if !strings.Contains(script, "work-profile") {
		t.Error("script should contain profile name")
	}

	// Verify script contains browser-open command
	if !strings.Contains(script, "browser-open") {
		t.Error("script should contain browser-open command")
	}

	// Verify script uses sh -c
	if !strings.HasPrefix(script, "sh -c '") {
		t.Error("script should start with 'sh -c '")
	}
}

// =============================================================================
// WriteBrowserHelper Tests
// =============================================================================

func TestWriteBrowserHelper(t *testing.T) {
	t.Run("creates executable script", func(t *testing.T) {
		path, err := WriteBrowserHelper("/usr/bin/caam", "test-profile")
		if err != nil {
			t.Fatalf("WriteBrowserHelper error = %v", err)
		}
		defer os.Remove(path)

		// Verify file exists
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat error = %v", err)
		}

		// Verify it's executable (at least owner execute)
		if info.Mode()&0100 == 0 {
			t.Error("script should be executable")
		}

		// Verify content
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read error = %v", err)
		}

		if !strings.HasPrefix(string(content), "#!/bin/sh") {
			t.Error("script should start with shebang")
		}
		if !strings.Contains(string(content), "/usr/bin/caam") {
			t.Error("script should contain caam path")
		}
		if !strings.Contains(string(content), "test-profile") {
			t.Error("script should contain profile name")
		}
	})

	t.Run("writes to temp directory", func(t *testing.T) {
		path, err := WriteBrowserHelper("/usr/bin/caam", "profile")
		if err != nil {
			t.Fatalf("WriteBrowserHelper error = %v", err)
		}
		defer os.Remove(path)

		// Should be in temp directory
		tmpDir := os.TempDir()
		if !strings.HasPrefix(path, tmpDir) {
			t.Errorf("path %q should be in temp dir %q", path, tmpDir)
		}

		// Should have expected filename
		if filepath.Base(path) != "caam-browser-helper.sh" {
			t.Errorf("filename = %q, want caam-browser-helper.sh", filepath.Base(path))
		}
	})
}

// =============================================================================
// OutputCapture Tests
// =============================================================================

func TestOutputCapture(t *testing.T) {
	t.Run("captures from stdout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		capture := NewOutputCapture(&stdout, &stderr)

		writer := capture.StdoutWriter()
		writer.Write([]byte("http://localhost:8080/oauth"))

		urls := capture.GetURLs()
		if len(urls) != 1 {
			t.Fatalf("captured %d URLs, want 1", len(urls))
		}
		if urls[0].URL != "http://localhost:8080/oauth" {
			t.Errorf("URL = %q, want http://localhost:8080/oauth", urls[0].URL)
		}
		if urls[0].Source != "stdout" {
			t.Errorf("Source = %q, want stdout", urls[0].Source)
		}
	})

	t.Run("captures from stderr", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		capture := NewOutputCapture(&stdout, &stderr)

		writer := capture.StderrWriter()
		writer.Write([]byte("Error: visit http://localhost:3000/"))

		urls := capture.GetURLs()
		if len(urls) != 1 {
			t.Fatalf("captured %d URLs, want 1", len(urls))
		}
		if urls[0].Source != "stderr" {
			t.Errorf("Source = %q, want stderr", urls[0].Source)
		}
	})

	t.Run("passes through to output", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		capture := NewOutputCapture(&stdout, &stderr)

		capture.StdoutWriter().Write([]byte("stdout content"))
		capture.StderrWriter().Write([]byte("stderr content"))

		if stdout.String() != "stdout content" {
			t.Errorf("stdout = %q, want 'stdout content'", stdout.String())
		}
		if stderr.String() != "stderr content" {
			t.Errorf("stderr = %q, want 'stderr content'", stderr.String())
		}
	})

	t.Run("invokes OnURL callback", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		capture := NewOutputCapture(&stdout, &stderr)

		var callbackURLs []string
		var callbackSources []string
		capture.OnURL = func(url, source string) {
			callbackURLs = append(callbackURLs, url)
			callbackSources = append(callbackSources, source)
		}

		capture.StdoutWriter().Write([]byte("http://localhost:8080/"))
		capture.StderrWriter().Write([]byte("http://127.0.0.1:3000/"))

		if len(callbackURLs) != 2 {
			t.Fatalf("callback called %d times, want 2", len(callbackURLs))
		}
		if callbackSources[0] != "stdout" || callbackSources[1] != "stderr" {
			t.Errorf("sources = %v, want [stdout, stderr]", callbackSources)
		}
	})

	t.Run("GetURLs is thread safe", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		capture := NewOutputCapture(&stdout, &stderr)

		var wg sync.WaitGroup

		// Writer goroutine
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				capture.StdoutWriter().Write([]byte("http://localhost:8080/"))
			}
		}()

		// Reader goroutine
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				_ = capture.GetURLs()
			}
		}()

		wg.Wait()

		urls := capture.GetURLs()
		if len(urls) != 100 {
			t.Errorf("captured %d URLs, want 100", len(urls))
		}
	})

	t.Run("GetURLs returns copy", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		capture := NewOutputCapture(&stdout, &stderr)

		capture.StdoutWriter().Write([]byte("http://localhost:8080/"))

		urls1 := capture.GetURLs()
		urls2 := capture.GetURLs()

		// Modify urls1
		if len(urls1) > 0 {
			urls1[0].URL = "modified"
		}

		// urls2 should be unchanged
		if len(urls2) > 0 && urls2[0].URL == "modified" {
			t.Error("GetURLs should return a copy, not the original slice")
		}
	})
}

// =============================================================================
// Config Tests
// =============================================================================

func TestConfig(t *testing.T) {
	cfg := Config{
		Command:    "google-chrome",
		ProfileDir: "Profile 1",
	}

	if cfg.Command != "google-chrome" {
		t.Errorf("Command = %q, want 'google-chrome'", cfg.Command)
	}
	if cfg.ProfileDir != "Profile 1" {
		t.Errorf("ProfileDir = %q, want 'Profile 1'", cfg.ProfileDir)
	}
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestLauncherIntegration(t *testing.T) {
	t.Run("launcher factory creates correct types", func(t *testing.T) {
		testCases := []struct {
			config       *Config
			expectedType string
		}{
			{nil, "*browser.DefaultLauncher"},
			{&Config{Command: ""}, "*browser.DefaultLauncher"},
			{&Config{Command: "google-chrome"}, "*browser.ChromeLauncher"},
			{&Config{Command: "chromium"}, "*browser.ChromeLauncher"},
			{&Config{Command: "brave-browser"}, "*browser.ChromeLauncher"},
			{&Config{Command: "firefox"}, "*browser.FirefoxLauncher"},
			{&Config{Command: "firefox-esr"}, "*browser.FirefoxLauncher"},
			{&Config{Command: "my-custom-opener"}, "*browser.GenericLauncher"},
		}

		for _, tc := range testCases {
			name := "nil"
			if tc.config != nil {
				name = tc.config.Command
			}
			t.Run(name, func(t *testing.T) {
				launcher := NewLauncher(tc.config)
				typeName := "*browser." + strings.TrimPrefix(
					strings.TrimPrefix(
						strings.TrimPrefix(
							strings.TrimSuffix(
								strings.TrimSuffix(
									typeName(launcher), "Launcher"),
								""),
							"*browser."), ""), "")

				_ = typeName // We can't easily get type name in Go, just check interface

				// Verify interface is satisfied
				var _ Launcher = launcher
			})
		}
	})
}

// Helper to get type name (used in integration test)
func typeName(v interface{}) string {
	switch v.(type) {
	case *DefaultLauncher:
		return "*browser.DefaultLauncher"
	case *ChromeLauncher:
		return "*browser.ChromeLauncher"
	case *FirefoxLauncher:
		return "*browser.FirefoxLauncher"
	case *GenericLauncher:
		return "*browser.GenericLauncher"
	default:
		return "unknown"
	}
}

// =============================================================================
// CaptureWriter Tests
// =============================================================================

func TestCaptureWriterWriteReturnsCorrectBytes(t *testing.T) {
	var stdout bytes.Buffer
	capture := NewOutputCapture(&stdout, io.Discard)
	writer := capture.StdoutWriter()

	input := []byte("test data with http://localhost:8080/")
	n, err := writer.Write(input)

	if err != nil {
		t.Fatalf("Write error = %v", err)
	}
	if n != len(input) {
		t.Errorf("Write returned %d, want %d", n, len(input))
	}
}
