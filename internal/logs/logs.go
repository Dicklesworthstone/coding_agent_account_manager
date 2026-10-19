package logs

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// maxTranscriptLineBytes bounds how much of one JSONL line is kept in memory.
// Agent transcripts carry tool output and pasted images inline, so single
// lines can run to megabytes; a longer line is skipped, never fatal to the
// rest of its file. (A variable only so tests can exercise the bound.)
var maxTranscriptLineBytes = 32 << 20

// forEachJSONLLine calls fn with each line of r, without its trailing newline.
// Lines longer than maxTranscriptLineBytes are counted in skipped and passed
// over. The slice handed to fn is only valid until fn returns.
func forEachJSONLLine(ctx context.Context, r io.Reader, fn func(line []byte)) (skipped int, err error) {
	reader := bufio.NewReaderSize(r, 64*1024)
	var long []byte
	tooLong := false
	for {
		if err := ctx.Err(); err != nil {
			return skipped, err
		}
		chunk, readErr := reader.ReadSlice('\n')
		if errors.Is(readErr, bufio.ErrBufferFull) {
			if !tooLong {
				if len(long)+len(chunk) > maxTranscriptLineBytes {
					tooLong, long = true, long[:0]
				} else {
					long = append(long, chunk...)
				}
			}
			continue
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return skipped, readErr
		}
		line := chunk
		if len(long) > 0 {
			long = append(long, chunk...)
			line = long
		}
		switch {
		case tooLong || len(line) > maxTranscriptLineBytes:
			skipped++
		default:
			if line = bytes.TrimRight(line, "\r\n"); len(bytes.TrimSpace(line)) > 0 {
				fn(line)
			}
		}
		long, tooLong = long[:0], false
		if readErr != nil { // io.EOF
			return skipped, nil
		}
	}
}

// walkJSONLFiles calls fn for every regular *.jsonl file under root
// (recursively) last modified at or after since. A missing root is not an
// error, and an unreadable subdirectory or file is counted in skipped rather
// than ending the walk. A symlinked root (a relocated ~/.claude, say) is
// resolved; symlinks below it are not followed.
func walkJSONLFiles(ctx context.Context, root string, since time.Time, fn func(path string) error) (skipped int, err error) {
	resolved, err := filepath.EvalSymlinks(root)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	root = resolved
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if walkErr != nil {
			if path == root {
				return walkErr
			}
			skipped++
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			skipped++
			return nil
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if !since.IsZero() && info.ModTime().Before(since) {
			return nil // Appended-to files cannot hold entries newer than their mtime.
		}
		if fnErr := fn(path); fnErr != nil {
			if errors.Is(fnErr, context.Canceled) || errors.Is(fnErr, context.DeadlineExceeded) {
				return fnErr
			}
			skipped++
		}
		return nil
	})
	return skipped, err
}

// Scanner is the interface for provider-specific log scanners.
// Each AI CLI tool (Claude, Codex, Gemini) has its own log format,
// so each implements this interface with provider-specific parsing.
type Scanner interface {
	// Scan parses logs from the given directory since the given time.
	// If logDir is empty, uses the provider's default log directory.
	// Returns all entries with timestamps >= since.
	Scan(ctx context.Context, logDir string, since time.Time) (*ScanResult, error)

	// LogDir returns the default log directory for this provider.
	// This is typically ~/.local/share/<provider>/logs or similar.
	LogDir() string
}

// MultiScanner scans logs from multiple providers.
type MultiScanner struct {
	scanners map[string]Scanner
}

// NewMultiScanner creates a scanner that aggregates multiple providers.
func NewMultiScanner() *MultiScanner {
	return &MultiScanner{
		scanners: make(map[string]Scanner),
	}
}

// Register adds a scanner for a provider.
func (m *MultiScanner) Register(provider string, scanner Scanner) {
	m.scanners[provider] = scanner
}

// Providers returns the list of registered provider names.
func (m *MultiScanner) Providers() []string {
	providers := make([]string, 0, len(m.scanners))
	for p := range m.scanners {
		providers = append(providers, p)
	}
	return providers
}

// LogDir returns an empty string as MultiScanner aggregates multiple directories.
func (m *MultiScanner) LogDir() string {
	return ""
}

// Scan implements the Scanner interface but returns an error.
// MultiScanner requires using ScanAll or selecting a specific provider scanner.
func (m *MultiScanner) Scan(ctx context.Context, logDir string, since time.Time) (*ScanResult, error) {
	return nil, context.DeadlineExceeded // Return a generic error, or better a specific one
}

// Scanner returns the scanner for a specific provider, or nil if not registered.
func (m *MultiScanner) Scanner(provider string) Scanner {
	return m.scanners[provider]
}

// ScanAll scans all registered providers and returns combined results.
func (m *MultiScanner) ScanAll(ctx context.Context, since time.Time) (map[string]*ScanResult, error) {
	results := make(map[string]*ScanResult)

	for provider, scanner := range m.scanners {
		result, err := scanner.Scan(ctx, "", since)
		if err != nil {
			// Log error but continue with other providers
			result = &ScanResult{
				Provider:    provider,
				ParseErrors: 1,
				Since:       since,
				Until:       time.Now(),
			}
		}
		results[provider] = result
	}

	return results, nil
}

// CombinedTokenUsage returns aggregated token usage across all providers.
func (m *MultiScanner) CombinedTokenUsage(ctx context.Context, since time.Time) (*TokenUsage, error) {
	results, err := m.ScanAll(ctx, since)
	if err != nil {
		return nil, err
	}

	combined := NewTokenUsage()
	for _, result := range results {
		for _, entry := range result.Entries {
			combined.Add(entry)
		}
	}

	return combined, nil
}
