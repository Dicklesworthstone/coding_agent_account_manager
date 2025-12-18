package browser

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"
)

// URLPattern matches localhost URLs commonly used in OAuth flows.
// Matches: http://localhost:PORT/path?query or http://127.0.0.1:PORT/path?query
var URLPattern = regexp.MustCompile(`https?://(?:localhost|127\.0\.0\.1):[0-9]+[^\s"']*`)

// URLCallback is called when a URL is detected in the output.
// Note: The return value is currently unused; all output is passed through.
type URLCallback func(url string) bool

// URLDetector wraps an io.Writer and scans for URLs in the output.
type URLDetector struct {
	output   io.Writer
	callback URLCallback
	mu       sync.Mutex
	buffer   []byte
}

// NewURLDetector creates a new URL detector that writes to output.
// When a localhost URL is detected, callback is invoked.
func NewURLDetector(output io.Writer, callback URLCallback) *URLDetector {
	return &URLDetector{
		output:   output,
		callback: callback,
	}
}

// Flush processes any remaining buffered data as a line.
func (d *URLDetector) Flush() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if len(d.buffer) > 0 {
		lineStr := string(d.buffer)
		if d.callback != nil {
			urls := URLPattern.FindAllString(lineStr, -1)
			for _, url := range urls {
				d.callback(url)
			}
		}
		d.buffer = nil
	}
}

// Write implements io.Writer.
func (d *URLDetector) Write(p []byte) (n int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.buffer = append(d.buffer, p...)

	for {
		idx := bytes.IndexByte(d.buffer, '\n')
		if idx < 0 {
			break
		}

		line := d.buffer[:idx+1]
		lineStr := string(line)

		if d.callback != nil {
			urls := URLPattern.FindAllString(lineStr, -1)
			for _, url := range urls {
				d.callback(url)
			}
		}

		d.buffer = d.buffer[idx+1:]
	}

	// Always pass through to output
	return d.output.Write(p)
}

// ScanReader scans an io.Reader line by line for URLs.
// Detected URLs are passed to callback. All content is written to output.
func ScanReader(reader io.Reader, output io.Writer, callback URLCallback) error {
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()

		// Check for URLs
		if callback != nil {
			urls := URLPattern.FindAllString(line, -1)
			for _, url := range urls {
				callback(url)
			}
		}

		// Write line to output
		if _, err := output.Write([]byte(line + "\n")); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// BrowserHelperScript generates a shell script that can be used as BROWSER env var.
// When invoked, the script will call the specified handler with the URL.
//
// Usage:
//
//	script := BrowserHelperScript("/path/to/caam", "profile-name")
//	os.Setenv("BROWSER", script)
//
// The script format allows the CLI tools to "open" URLs through caam's browser launcher.
func BrowserHelperScript(caamBinary, profileName string) string {
	// Create a simple inline script that calls caam
	return `sh -c '` + caamBinary + ` browser-open --profile="` + profileName + `" "$1"' _`
}

// WriteBrowserHelper writes a browser helper script to a temporary file.
// Returns the path to the script. Caller is responsible for cleanup.
func WriteBrowserHelper(caamBinary, profileName string) (string, error) {
	tmpDir := os.TempDir()
	scriptPath := filepath.Join(tmpDir, "caam-browser-helper.sh")

	script := `#!/bin/sh
# CAAM Browser Helper - opens URLs with configured browser profile
exec "` + caamBinary + `" browser-open --profile="` + profileName + `" "$1"
`

	if err := os.WriteFile(scriptPath, []byte(script), 0755); err != nil {
		return "", err
	}

	return scriptPath, nil
}

// DetectedURL represents a URL found in command output.
type DetectedURL struct {
	URL    string
	Source string // "stdout" or "stderr"
}

// OutputCapture captures both stdout and stderr, scanning for URLs.
type OutputCapture struct {
	Stdout       io.Writer
	Stderr       io.Writer
	DetectedURLs []DetectedURL
	OnURL        func(url string, source string)
	mu           sync.Mutex

	stdoutWriter *captureWriter
	stderrWriter *captureWriter
}

// NewOutputCapture creates a new output capture that writes to the given writers.
func NewOutputCapture(stdout, stderr io.Writer) *OutputCapture {
	c := &OutputCapture{
		Stdout: stdout,
		Stderr: stderr,
	}
	c.stdoutWriter = &captureWriter{capture: c, output: stdout, source: "stdout"}
	c.stderrWriter = &captureWriter{capture: c, output: stderr, source: "stderr"}
	return c
}

// StdoutWriter returns an io.Writer for stdout that scans for URLs.
func (c *OutputCapture) StdoutWriter() io.Writer {
	return c.stdoutWriter
}

// StderrWriter returns an io.Writer for stderr that scans for URLs.
func (c *OutputCapture) StderrWriter() io.Writer {
	return c.stderrWriter
}

// Flush processes any remaining buffered data in stdout/stderr writers.
func (c *OutputCapture) Flush() {
	c.stdoutWriter.Flush()
	c.stderrWriter.Flush()
}

// GetURLs returns all detected URLs (thread-safe).
func (c *OutputCapture) GetURLs() []DetectedURL {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]DetectedURL, len(c.DetectedURLs))
	copy(result, c.DetectedURLs)
	return result
}

// captureWriter wraps writes and scans for URLs.
type captureWriter struct {
	capture *OutputCapture
	output  io.Writer
	source  string
	buffer  []byte
	mu      sync.Mutex
}

func (w *captureWriter) Write(p []byte) (n int, err error) {
	// Pass through to output first
	n, err = w.output.Write(p)
	if n > 0 {
		w.mu.Lock()
		w.buffer = append(w.buffer, p[:n]...)

		for {
			idx := bytes.IndexByte(w.buffer, '\n')
			if idx < 0 {
				break
			}

			line := w.buffer[:idx+1] // Include newline
			lineStr := string(line)

			// Scan for URLs in this line
			urls := URLPattern.FindAllString(lineStr, -1)
			for _, url := range urls {
				w.capture.mu.Lock()
				w.capture.DetectedURLs = append(w.capture.DetectedURLs, DetectedURL{
					URL:    url,
					Source: w.source,
				})
				if w.capture.OnURL != nil {
					w.capture.OnURL(url, w.source)
				}
				w.capture.mu.Unlock()
			}

			w.buffer = w.buffer[idx+1:]
		}
		w.mu.Unlock()
	}

	return n, err
}

func (w *captureWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if len(w.buffer) > 0 {
		lineStr := string(w.buffer)
		urls := URLPattern.FindAllString(lineStr, -1)
		for _, url := range urls {
			w.capture.mu.Lock()
			w.capture.DetectedURLs = append(w.capture.DetectedURLs, DetectedURL{
				URL:    url,
				Source: w.source,
			})
			if w.capture.OnURL != nil {
				w.capture.OnURL(url, w.source)
			}
			w.capture.mu.Unlock()
		}
		w.buffer = nil
	}
}
