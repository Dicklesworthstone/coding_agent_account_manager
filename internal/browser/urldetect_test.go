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
// URLPattern Tests
// =============================================================================

func TestURLPattern_MatchesLocalhostURLs(t *testing.T) {
	tests := []struct {
		input   string
		matches []string
	}{
		{"http://localhost:8080/callback", []string{"http://localhost:8080/callback"}},
		{"https://localhost:8443/auth", []string{"https://localhost:8443/auth"}},
		{"http://127.0.0.1:3000/oauth", []string{"http://127.0.0.1:3000/oauth"}},
		{"https://127.0.0.1:443/token", []string{"https://127.0.0.1:443/token"}},
		{"http://localhost:8080/callback?code=abc123", []string{"http://localhost:8080/callback?code=abc123"}},
		{"http://localhost:8080/callback?code=abc&state=xyz", []string{"http://localhost:8080/callback?code=abc&state=xyz"}},
		{"Visit http://localhost:9000/auth to login", []string{"http://localhost:9000/auth"}},
		{"Multiple: http://localhost:1111/a and http://127.0.0.1:2222/b", []string{"http://localhost:1111/a", "http://127.0.0.1:2222/b"}},
	}

	for _, tc := range tests {
		got := URLPattern.FindAllString(tc.input, -1)
		if len(got) != len(tc.matches) {
			t.Errorf("URLPattern.FindAllString(%q) = %v, want %v", tc.input, got, tc.matches)
			continue
		}
		for i, m := range tc.matches {
			if got[i] != m {
				t.Errorf("URLPattern.FindAllString(%q)[%d] = %q, want %q", tc.input, i, got[i], m)
			}
		}
	}
}

func TestURLPattern_DoesNotMatchNonLocalhost(t *testing.T) {
	tests := []string{
		"http://example.com:8080/callback",
		"https://api.example.com/auth",
		"http://192.168.1.1:8080/callback",
		"plain text without URLs",
		"localhost without protocol",
		"http://localhost/no-port",
	}

	for _, input := range tests {
		got := URLPattern.FindAllString(input, -1)
		if len(got) != 0 {
			t.Errorf("URLPattern.FindAllString(%q) = %v, want empty", input, got)
		}
	}
}

// =============================================================================
// cleanURL Tests
// =============================================================================

func TestCleanURL(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"http://localhost:8080/callback", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback.", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback,", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback;", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback:", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback!", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback)", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback]", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback}", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback>", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback.,;:!)]}>", "http://localhost:8080/callback"},
		{"http://localhost:8080/callback?code=abc", "http://localhost:8080/callback?code=abc"},
	}

	for _, tc := range tests {
		got := cleanURL(tc.input)
		if got != tc.expected {
			t.Errorf("cleanURL(%q) = %q, want %q", tc.input, got, tc.expected)
		}
	}
}

// =============================================================================
// URLDetector Tests
// =============================================================================

func TestNewURLDetector(t *testing.T) {
	var buf bytes.Buffer
	callback := func(url string) bool { return true }
	detector := NewURLDetector(&buf, callback)

	if detector == nil {
		t.Fatal("NewURLDetector returned nil")
	}
	if detector.output != &buf {
		t.Error("NewURLDetector did not set output correctly")
	}
	if detector.callback == nil {
		t.Error("NewURLDetector did not set callback correctly")
	}
}

func TestURLDetector_Write_PassesThrough(t *testing.T) {
	var buf bytes.Buffer
	detector := NewURLDetector(&buf, nil)

	input := []byte("Hello, World!\n")
	n, err := detector.Write(input)
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	if n != len(input) {
		t.Errorf("Write returned %d, want %d", n, len(input))
	}
	if buf.String() != string(input) {
		t.Errorf("Output = %q, want %q", buf.String(), string(input))
	}
}

func TestURLDetector_Write_DetectsURLs(t *testing.T) {
	var buf bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}
	detector := NewURLDetector(&buf, callback)

	input := "Visit http://localhost:8080/callback?code=xyz to continue\n"
	_, err := detector.Write([]byte(input))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if len(detectedURLs) != 1 {
		t.Fatalf("Expected 1 URL, got %d", len(detectedURLs))
	}
	if detectedURLs[0] != "http://localhost:8080/callback?code=xyz" {
		t.Errorf("Detected URL = %q, want %q", detectedURLs[0], "http://localhost:8080/callback?code=xyz")
	}
}

func TestURLDetector_Write_MultipleLines(t *testing.T) {
	var buf bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}
	detector := NewURLDetector(&buf, callback)

	lines := []string{
		"Line 1: http://localhost:1111/a\n",
		"Line 2: no URL here\n",
		"Line 3: http://127.0.0.1:2222/b\n",
	}
	for _, line := range lines {
		_, err := detector.Write([]byte(line))
		if err != nil {
			t.Fatalf("Write error: %v", err)
		}
	}

	if len(detectedURLs) != 2 {
		t.Fatalf("Expected 2 URLs, got %d: %v", len(detectedURLs), detectedURLs)
	}
}

func TestURLDetector_Write_PartialLines(t *testing.T) {
	var buf bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}
	detector := NewURLDetector(&buf, callback)

	// Write partial line (no newline yet)
	_, _ = detector.Write([]byte("Visit http://localhost:8080"))
	if len(detectedURLs) != 0 {
		t.Error("Should not detect URL until newline")
	}

	// Complete the line
	_, _ = detector.Write([]byte("/callback\n"))
	if len(detectedURLs) != 1 {
		t.Fatalf("Expected 1 URL after newline, got %d", len(detectedURLs))
	}
}

func TestURLDetector_Flush(t *testing.T) {
	var buf bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}
	detector := NewURLDetector(&buf, callback)

	// Write partial line without newline
	_, _ = detector.Write([]byte("http://localhost:8080/callback"))
	if len(detectedURLs) != 0 {
		t.Error("Should not detect URL before flush")
	}

	// Flush should process remaining buffer
	detector.Flush()
	if len(detectedURLs) != 1 {
		t.Fatalf("Expected 1 URL after flush, got %d", len(detectedURLs))
	}
}

func TestURLDetector_Flush_EmptyBuffer(t *testing.T) {
	var buf bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}
	detector := NewURLDetector(&buf, callback)

	// Flush with empty buffer should not panic
	detector.Flush()
	if len(detectedURLs) != 0 {
		t.Error("Expected no URLs from empty buffer")
	}
}

func TestURLDetector_NilCallback(t *testing.T) {
	var buf bytes.Buffer
	detector := NewURLDetector(&buf, nil)

	// Should not panic with nil callback
	_, err := detector.Write([]byte("http://localhost:8080/callback\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}
	detector.Flush()
}

func TestURLDetector_Concurrent(t *testing.T) {
	var buf bytes.Buffer
	var mu sync.Mutex
	var detectedURLs []string
	callback := func(url string) bool {
		mu.Lock()
		detectedURLs = append(detectedURLs, url)
		mu.Unlock()
		return true
	}
	detector := NewURLDetector(&buf, callback)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = detector.Write([]byte("http://localhost:8080/path\n"))
		}(i)
	}
	wg.Wait()

	mu.Lock()
	count := len(detectedURLs)
	mu.Unlock()

	if count != 10 {
		t.Errorf("Expected 10 URLs, got %d", count)
	}
}

// =============================================================================
// ScanReader Tests
// =============================================================================

func TestScanReader_DetectsURLs(t *testing.T) {
	input := strings.NewReader("Line 1\nhttp://localhost:8080/auth\nLine 3\n")
	var output bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}

	err := ScanReader(input, &output, callback)
	if err != nil {
		t.Fatalf("ScanReader error: %v", err)
	}

	if len(detectedURLs) != 1 {
		t.Fatalf("Expected 1 URL, got %d", len(detectedURLs))
	}
	if detectedURLs[0] != "http://localhost:8080/auth" {
		t.Errorf("Detected URL = %q, want %q", detectedURLs[0], "http://localhost:8080/auth")
	}

	// Check output contains all lines
	if !strings.Contains(output.String(), "Line 1") {
		t.Error("Output missing 'Line 1'")
	}
	if !strings.Contains(output.String(), "Line 3") {
		t.Error("Output missing 'Line 3'")
	}
}

func TestScanReader_NilCallback(t *testing.T) {
	input := strings.NewReader("http://localhost:8080/auth\n")
	var output bytes.Buffer

	err := ScanReader(input, &output, nil)
	if err != nil {
		t.Fatalf("ScanReader error: %v", err)
	}

	if output.String() != "http://localhost:8080/auth\n" {
		t.Errorf("Output = %q, want %q", output.String(), "http://localhost:8080/auth\n")
	}
}

func TestScanReader_EmptyInput(t *testing.T) {
	input := strings.NewReader("")
	var output bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}

	err := ScanReader(input, &output, callback)
	if err != nil {
		t.Fatalf("ScanReader error: %v", err)
	}

	if len(detectedURLs) != 0 {
		t.Errorf("Expected 0 URLs, got %d", len(detectedURLs))
	}
}

func TestScanReader_MultipleURLsPerLine(t *testing.T) {
	input := strings.NewReader("URLs: http://localhost:1111/a and http://127.0.0.1:2222/b\n")
	var output bytes.Buffer
	var detectedURLs []string
	callback := func(url string) bool {
		detectedURLs = append(detectedURLs, url)
		return true
	}

	err := ScanReader(input, &output, callback)
	if err != nil {
		t.Fatalf("ScanReader error: %v", err)
	}

	if len(detectedURLs) != 2 {
		t.Fatalf("Expected 2 URLs, got %d", len(detectedURLs))
	}
}

// =============================================================================
// BrowserHelperScript Tests
// =============================================================================

func TestBrowserHelperScript_Content(t *testing.T) {
	script := BrowserHelperScript("/usr/bin/caam", "work-profile")

	if !strings.Contains(script, "/usr/bin/caam") {
		t.Error("Script should contain caam binary path")
	}
	if !strings.Contains(script, "work-profile") {
		t.Error("Script should contain profile name")
	}
	if !strings.Contains(script, "browser-open") {
		t.Error("Script should contain browser-open command")
	}
	if !strings.Contains(script, "$1") {
		t.Error("Script should contain $1 for URL argument")
	}
}

func TestBrowserHelperScript_SpecialChars(t *testing.T) {
	// Test with path containing spaces (should be quoted in script)
	script := BrowserHelperScript("/path/to/my caam", "test-profile")

	if !strings.Contains(script, "/path/to/my caam") {
		t.Error("Script should preserve binary path with spaces")
	}
}

// =============================================================================
// WriteBrowserHelper Tests
// =============================================================================

func TestWriteBrowserHelper_Content(t *testing.T) {
	path, err := WriteBrowserHelper("/usr/bin/caam", "test-profile")
	if err != nil {
		t.Fatalf("WriteBrowserHelper error: %v", err)
	}
	defer os.Remove(path)

	// Check file exists
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}

	// Check file is executable
	if info.Mode()&0111 == 0 {
		t.Error("Script should be executable")
	}

	// Check content
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile error: %v", err)
	}

	if !strings.HasPrefix(string(content), "#!/bin/sh") {
		t.Error("Script should start with shebang")
	}
	if !strings.Contains(string(content), "/usr/bin/caam") {
		t.Error("Script should contain caam binary path")
	}
	if !strings.Contains(string(content), "test-profile") {
		t.Error("Script should contain profile name")
	}
}

func TestWriteBrowserHelper_Path(t *testing.T) {
	path, err := WriteBrowserHelper("/usr/bin/caam", "profile")
	if err != nil {
		t.Fatalf("WriteBrowserHelper error: %v", err)
	}
	defer os.Remove(path)

	if filepath.Base(path) != "caam-browser-helper.sh" {
		t.Errorf("Expected filename 'caam-browser-helper.sh', got %q", filepath.Base(path))
	}
}

// =============================================================================
// OutputCapture Tests
// =============================================================================

func TestNewOutputCapture(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	if capture == nil {
		t.Fatal("NewOutputCapture returned nil")
	}
	if capture.Stdout != &stdout {
		t.Error("Stdout not set correctly")
	}
	if capture.Stderr != &stderr {
		t.Error("Stderr not set correctly")
	}
}

func TestOutputCapture_StdoutWriter(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StdoutWriter()
	if writer == nil {
		t.Fatal("StdoutWriter returned nil")
	}

	_, err := writer.Write([]byte("test output\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if stdout.String() != "test output\n" {
		t.Errorf("Stdout = %q, want %q", stdout.String(), "test output\n")
	}
}

func TestOutputCapture_StderrWriter(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StderrWriter()
	if writer == nil {
		t.Fatal("StderrWriter returned nil")
	}

	_, err := writer.Write([]byte("error output\n"))
	if err != nil {
		t.Fatalf("Write error: %v", err)
	}

	if stderr.String() != "error output\n" {
		t.Errorf("Stderr = %q, want %q", stderr.String(), "error output\n")
	}
}

func TestOutputCapture_DetectsURLsInStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StdoutWriter()
	_, _ = writer.Write([]byte("Visit http://localhost:8080/auth to login\n"))

	urls := capture.GetURLs()
	if len(urls) != 1 {
		t.Fatalf("Expected 1 URL, got %d", len(urls))
	}
	if urls[0].URL != "http://localhost:8080/auth" {
		t.Errorf("URL = %q, want %q", urls[0].URL, "http://localhost:8080/auth")
	}
	if urls[0].Source != "stdout" {
		t.Errorf("Source = %q, want %q", urls[0].Source, "stdout")
	}
}

func TestOutputCapture_DetectsURLsInStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StderrWriter()
	_, _ = writer.Write([]byte("Error: redirect to http://127.0.0.1:9000/callback\n"))

	urls := capture.GetURLs()
	if len(urls) != 1 {
		t.Fatalf("Expected 1 URL, got %d", len(urls))
	}
	if urls[0].URL != "http://127.0.0.1:9000/callback" {
		t.Errorf("URL = %q, want %q", urls[0].URL, "http://127.0.0.1:9000/callback")
	}
	if urls[0].Source != "stderr" {
		t.Errorf("Source = %q, want %q", urls[0].Source, "stderr")
	}
}

func TestOutputCapture_OnURLCallback(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	var callbackURLs []string
	var callbackSources []string
	capture.OnURL = func(url, source string) {
		callbackURLs = append(callbackURLs, url)
		callbackSources = append(callbackSources, source)
	}

	_, _ = capture.StdoutWriter().Write([]byte("http://localhost:8080/a\n"))
	_, _ = capture.StderrWriter().Write([]byte("http://127.0.0.1:9000/b\n"))

	if len(callbackURLs) != 2 {
		t.Fatalf("Expected 2 callbacks, got %d", len(callbackURLs))
	}
	if callbackSources[0] != "stdout" {
		t.Errorf("First source = %q, want %q", callbackSources[0], "stdout")
	}
	if callbackSources[1] != "stderr" {
		t.Errorf("Second source = %q, want %q", callbackSources[1], "stderr")
	}
}

func TestOutputCapture_Flush(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	// Write without newline
	_, _ = capture.StdoutWriter().Write([]byte("http://localhost:8080/auth"))

	// Should not be detected yet
	urls := capture.GetURLs()
	if len(urls) != 0 {
		t.Error("Should not detect URL before flush")
	}

	// Flush should process remaining
	capture.Flush()
	urls = capture.GetURLs()
	if len(urls) != 1 {
		t.Fatalf("Expected 1 URL after flush, got %d", len(urls))
	}
}

func TestOutputCapture_GetURLs_ThreadSafe(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	var wg sync.WaitGroup

	// Write URLs concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = capture.StdoutWriter().Write([]byte("http://localhost:8080/test\n"))
		}()
	}

	// Read URLs concurrently
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = capture.GetURLs()
		}()
	}

	wg.Wait()

	urls := capture.GetURLs()
	if len(urls) != 10 {
		t.Errorf("Expected 10 URLs, got %d", len(urls))
	}
}

func TestOutputCapture_GetURLs_ReturnsCopy(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	_, _ = capture.StdoutWriter().Write([]byte("http://localhost:8080/test\n"))

	urls1 := capture.GetURLs()
	urls2 := capture.GetURLs()

	// Modify urls1
	if len(urls1) > 0 {
		urls1[0].URL = "modified"
	}

	// urls2 should not be affected
	if len(urls2) > 0 && urls2[0].URL == "modified" {
		t.Error("GetURLs should return a copy, not the original slice")
	}
}

// =============================================================================
// captureWriter Tests
// =============================================================================

func TestCaptureWriter_PartialWrites(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StdoutWriter()

	// Write URL in parts
	_, _ = writer.Write([]byte("http://localhost"))
	_, _ = writer.Write([]byte(":8080/callback"))
	_, _ = writer.Write([]byte("?code=abc\n"))

	urls := capture.GetURLs()
	if len(urls) != 1 {
		t.Fatalf("Expected 1 URL from partial writes, got %d", len(urls))
	}
	if urls[0].URL != "http://localhost:8080/callback?code=abc" {
		t.Errorf("URL = %q, want %q", urls[0].URL, "http://localhost:8080/callback?code=abc")
	}
}

func TestCaptureWriter_MultipleURLsPerLine(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StdoutWriter()
	_, _ = writer.Write([]byte("URLs: http://localhost:1111/a and http://127.0.0.1:2222/b here\n"))

	urls := capture.GetURLs()
	if len(urls) != 2 {
		t.Fatalf("Expected 2 URLs, got %d", len(urls))
	}
}

func TestCaptureWriter_NoURLs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	capture := NewOutputCapture(&stdout, &stderr)

	writer := capture.StdoutWriter()
	_, _ = writer.Write([]byte("No URLs in this line\n"))
	_, _ = writer.Write([]byte("Or this one either\n"))

	urls := capture.GetURLs()
	if len(urls) != 0 {
		t.Errorf("Expected 0 URLs, got %d", len(urls))
	}
}

func TestCaptureWriter_WriteError(t *testing.T) {
	// Use a writer that always fails
	capture := NewOutputCapture(&failWriter{}, io.Discard)

	writer := capture.StdoutWriter()
	_, err := writer.Write([]byte("test\n"))
	if err == nil {
		t.Error("Expected error from failing writer")
	}
}

// failWriter always returns an error
type failWriter struct{}

func (f *failWriter) Write(p []byte) (n int, err error) {
	return 0, io.ErrClosedPipe
}

// =============================================================================
// ScanReader Edge Cases
// =============================================================================

func TestScanReader_WriteError(t *testing.T) {
	input := strings.NewReader("line 1\nline 2\n")
	err := ScanReader(input, &failWriter{}, nil)
	if err == nil {
		t.Error("Expected error when output writer fails")
	}
}

// =============================================================================
// DetectedURL Tests
// =============================================================================

func TestDetectedURL_Fields(t *testing.T) {
	url := DetectedURL{
		URL:    "http://localhost:8080/auth",
		Source: "stdout",
	}

	if url.URL != "http://localhost:8080/auth" {
		t.Errorf("URL = %q, want %q", url.URL, "http://localhost:8080/auth")
	}
	if url.Source != "stdout" {
		t.Errorf("Source = %q, want %q", url.Source, "stdout")
	}
}
