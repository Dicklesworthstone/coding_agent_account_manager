// Package cmd implements the CLI commands for caam.
package cmd

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// TraceReport is the full report from a trace-login session.
type TraceReport struct {
	Timestamp   string          `json:"timestamp"`
	Agent       string          `json:"agent"`
	Duration    string          `json:"duration"`
	Before      FileSnapshot    `json:"before"`
	After       FileSnapshot    `json:"after"`
	Changes     TraceChanges    `json:"changes"`
	WatchRules  []WatchRule     `json:"watch_rules"`
	Summary     TraceSummary    `json:"summary"`
}

// FileSnapshot represents a point-in-time snapshot of files.
type FileSnapshot struct {
	Timestamp string              `json:"timestamp"`
	Files     map[string]FileInfo `json:"files"`
}

// FileInfo contains metadata about a single file.
type FileInfo struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	ModTime  string `json:"mod_time"`
	Hash     string `json:"hash"`
	IsDir    bool   `json:"is_dir"`
}

// TraceChanges categorizes file changes between snapshots.
type TraceChanges struct {
	Added    []FileChange `json:"added"`
	Modified []FileChange `json:"modified"`
	Removed  []FileChange `json:"removed"`
}

// FileChange represents a single file change.
type FileChange struct {
	Path       string `json:"path"`
	ChangeType string `json:"change_type"`
	SizeBefore int64  `json:"size_before,omitempty"`
	SizeAfter  int64  `json:"size_after,omitempty"`
	HashBefore string `json:"hash_before,omitempty"`
	HashAfter  string `json:"hash_after,omitempty"`
}

// WatchRule is a derived rule for caam watch configuration.
type WatchRule struct {
	Path        string `json:"path"`
	Pattern     string `json:"pattern"`
	Description string `json:"description"`
	Priority    int    `json:"priority"`
}

// TraceSummary summarizes the trace results.
type TraceSummary struct {
	DirsScanned   int   `json:"dirs_scanned"`
	FilesTracked  int   `json:"files_tracked"`
	FilesAdded    int   `json:"files_added"`
	FilesModified int   `json:"files_modified"`
	FilesRemoved  int   `json:"files_removed"`
	BytesChanged  int64 `json:"bytes_changed"`
}

var traceCmd = &cobra.Command{
	Use:   "trace <agent>",
	Short: "Trace file changes during login/logout to discover touched paths",
	Long: `Trace file changes during login/logout to discover exactly which files an AI
coding agent touches during authentication.

This is a safe, read-only tool that:
  1. Takes a snapshot of candidate directories before login
  2. Waits for you to perform login/logout
  3. Takes a second snapshot and produces a diff report

The report shows added/changed/removed files with sizes and hashes,
plus derived "watch rules" for caam watch configuration.

Currently supports: claude (other agents planned)

Examples:
  caam trace claude                    # Trace Claude Code login files
  caam trace claude --json             # Output as JSON
  caam trace claude --no-projects      # Skip project .claude directories
  caam trace claude --exclude "*.log"  # Exclude log files

This is useful for:
  - Understanding which files need backup for profile switching
  - Debugging auth file issues
  - Generating caam watch configuration`,
	Args: cobra.ExactArgs(1),
	RunE: runTrace,
}

func init() {
	rootCmd.AddCommand(traceCmd)
	traceCmd.Flags().Bool("json", false, "output in JSON format")
	traceCmd.Flags().Bool("no-projects", false, "skip project .claude directories")
	traceCmd.Flags().StringSlice("exclude", nil, "exclude patterns (glob syntax)")
	traceCmd.Flags().StringSlice("include", nil, "additional directories to scan")
	traceCmd.Flags().Bool("verbose", false, "show detailed file information")
}

// getTracePaths returns the candidate directories to scan for an agent.
func getTracePaths(agent string, noProjects bool, extraPaths []string) ([]string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}

	xdgConfigHome := os.Getenv("XDG_CONFIG_HOME")
	if xdgConfigHome == "" {
		xdgConfigHome = filepath.Join(homeDir, ".config")
	}

	var paths []string

	switch strings.ToLower(agent) {
	case "claude":
		claudeConfigDir := os.Getenv("CLAUDE_CONFIG_DIR")
		if claudeConfigDir == "" {
			claudeConfigDir = filepath.Join(xdgConfigHome, "claude-code")
		}

		paths = []string{
			filepath.Join(homeDir, ".claude"),
			claudeConfigDir,
			filepath.Join(homeDir, ".claude.json"), // Single file, will be handled
		}

		// Add project .claude directories unless disabled
		if !noProjects {
			cwd, err := os.Getwd()
			if err == nil {
				projectClaude := filepath.Join(cwd, ".claude")
				if _, err := os.Stat(projectClaude); err == nil {
					paths = append(paths, projectClaude)
				}
			}
		}

	case "codex":
		codexHome := os.Getenv("CODEX_HOME")
		if codexHome == "" {
			codexHome = filepath.Join(homeDir, ".codex")
		}
		paths = []string{codexHome}

	case "gemini":
		paths = []string{
			filepath.Join(homeDir, ".gemini"),
		}

	default:
		return nil, fmt.Errorf("unsupported agent: %s (supported: claude, codex, gemini)", agent)
	}

	// Add extra paths
	paths = append(paths, extraPaths...)

	return paths, nil
}

// takeSnapshot creates a snapshot of files in the given directories.
func takeSnapshot(paths []string, excludePatterns []string) FileSnapshot {
	snapshot := FileSnapshot{
		Timestamp: time.Now().Format(time.RFC3339),
		Files:     make(map[string]FileInfo),
	}

	for _, basePath := range paths {
		info, err := os.Stat(basePath)
		if err != nil {
			continue // Path doesn't exist, skip
		}

		if info.IsDir() {
			// Walk directory
			err := filepath.Walk(basePath, func(path string, info os.FileInfo, err error) error {
				if err != nil {
					return nil // Skip files we can't access
				}

				// Check exclusions
				for _, pattern := range excludePatterns {
					matched, _ := filepath.Match(pattern, filepath.Base(path))
					if matched {
						if info.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}

				snapshot.Files[path] = createFileInfo(path, info)
				return nil
			})
			if err != nil {
				continue
			}
		} else {
			// Single file
			snapshot.Files[basePath] = createFileInfo(basePath, info)
		}
	}

	return snapshot
}

// createFileInfo creates a FileInfo from path and os.FileInfo.
func createFileInfo(path string, info os.FileInfo) FileInfo {
	fi := FileInfo{
		Path:    path,
		Size:    info.Size(),
		ModTime: info.ModTime().Format(time.RFC3339),
		IsDir:   info.IsDir(),
	}

	// Calculate hash for files (not directories)
	if !info.IsDir() && info.Size() < 10*1024*1024 { // Skip files > 10MB
		if hash, err := hashFile(path); err == nil {
			fi.Hash = hash
		}
	}

	return fi
}

// hashFile computes SHA-256 hash of a file.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// computeChanges compares two snapshots and returns the differences.
func computeChanges(before, after FileSnapshot) TraceChanges {
	changes := TraceChanges{}

	// Find added and modified files
	for path, afterInfo := range after.Files {
		beforeInfo, existed := before.Files[path]
		if !existed {
			changes.Added = append(changes.Added, FileChange{
				Path:       path,
				ChangeType: "added",
				SizeAfter:  afterInfo.Size,
				HashAfter:  afterInfo.Hash,
			})
		} else if afterInfo.Hash != beforeInfo.Hash || afterInfo.Size != beforeInfo.Size {
			changes.Modified = append(changes.Modified, FileChange{
				Path:       path,
				ChangeType: "modified",
				SizeBefore: beforeInfo.Size,
				SizeAfter:  afterInfo.Size,
				HashBefore: beforeInfo.Hash,
				HashAfter:  afterInfo.Hash,
			})
		}
	}

	// Find removed files
	for path, beforeInfo := range before.Files {
		if _, exists := after.Files[path]; !exists {
			changes.Removed = append(changes.Removed, FileChange{
				Path:       path,
				ChangeType: "removed",
				SizeBefore: beforeInfo.Size,
				HashBefore: beforeInfo.Hash,
			})
		}
	}

	// Sort changes by path for consistent output
	sort.Slice(changes.Added, func(i, j int) bool { return changes.Added[i].Path < changes.Added[j].Path })
	sort.Slice(changes.Modified, func(i, j int) bool { return changes.Modified[i].Path < changes.Modified[j].Path })
	sort.Slice(changes.Removed, func(i, j int) bool { return changes.Removed[i].Path < changes.Removed[j].Path })

	return changes
}

// deriveWatchRules creates watch rules from the changes.
func deriveWatchRules(agent string, changes TraceChanges) []WatchRule {
	// Collect unique directories and files that changed
	pathSet := make(map[string]bool)
	fileSet := make(map[string]bool)

	allChanges := append(append(changes.Added, changes.Modified...), changes.Removed...)
	for _, c := range allChanges {
		fileSet[c.Path] = true
		dir := filepath.Dir(c.Path)
		pathSet[dir] = true
	}

	var rules []WatchRule
	priority := 1

	// Create rules for directories
	for dir := range pathSet {
		rules = append(rules, WatchRule{
			Path:        dir,
			Pattern:     filepath.Join(dir, "*"),
			Description: fmt.Sprintf("Files changed in %s during %s login", shortenPath(dir), agent),
			Priority:    priority,
		})
		priority++
	}

	// Add specific file rules for important files
	homeDir, _ := os.UserHomeDir()
	for file := range fileSet {
		// Special handling for well-known auth files
		base := filepath.Base(file)
		switch {
		case base == ".credentials.json":
			rules = append(rules, WatchRule{
				Path:        file,
				Pattern:     file,
				Description: "OAuth credentials (primary auth)",
				Priority:    0, // Highest priority
			})
		case base == "auth.json":
			rules = append(rules, WatchRule{
				Path:        file,
				Pattern:     file,
				Description: "Auth credentials",
				Priority:    0,
			})
		case file == filepath.Join(homeDir, ".claude.json"):
			rules = append(rules, WatchRule{
				Path:        file,
				Pattern:     file,
				Description: "Claude session state (legacy)",
				Priority:    0,
			})
		}
	}

	// Sort by priority
	sort.Slice(rules, func(i, j int) bool {
		return rules[i].Priority < rules[j].Priority
	})

	// Deduplicate rules by path
	seen := make(map[string]bool)
	var uniqueRules []WatchRule
	for _, r := range rules {
		if !seen[r.Path] {
			seen[r.Path] = true
			uniqueRules = append(uniqueRules, r)
		}
	}

	return uniqueRules
}

// computeSummary creates a summary from the trace data.
func computeSummary(before, after FileSnapshot, changes TraceChanges) TraceSummary {
	// Count unique directories in after snapshot
	dirs := make(map[string]bool)
	for path, info := range after.Files {
		if info.IsDir {
			dirs[path] = true
		} else {
			dirs[filepath.Dir(path)] = true
		}
	}

	var bytesChanged int64
	for _, c := range changes.Added {
		bytesChanged += c.SizeAfter
	}
	for _, c := range changes.Modified {
		if c.SizeAfter > c.SizeBefore {
			bytesChanged += c.SizeAfter - c.SizeBefore
		} else {
			bytesChanged += c.SizeBefore - c.SizeAfter
		}
	}
	for _, c := range changes.Removed {
		bytesChanged += c.SizeBefore
	}

	return TraceSummary{
		DirsScanned:   len(dirs),
		FilesTracked:  len(after.Files),
		FilesAdded:    len(changes.Added),
		FilesModified: len(changes.Modified),
		FilesRemoved:  len(changes.Removed),
		BytesChanged:  bytesChanged,
	}
}

func runTrace(cmd *cobra.Command, args []string) error {
	agent := strings.ToLower(args[0])
	jsonOutput, _ := cmd.Flags().GetBool("json")
	noProjects, _ := cmd.Flags().GetBool("no-projects")
	excludePatterns, _ := cmd.Flags().GetStringSlice("exclude")
	includePaths, _ := cmd.Flags().GetStringSlice("include")
	verbose, _ := cmd.Flags().GetBool("verbose")

	// Get paths to trace
	paths, err := getTracePaths(agent, noProjects, includePaths)
	if err != nil {
		return err
	}

	if !jsonOutput {
		fmt.Printf("Tracing %s login file changes...\n\n", agent)
		fmt.Println("Scanning directories:")
		for _, p := range paths {
			fmt.Printf("  - %s\n", shortenPath(p))
		}
		if len(excludePatterns) > 0 {
			fmt.Println("\nExclude patterns:")
			for _, p := range excludePatterns {
				fmt.Printf("  - %s\n", p)
			}
		}
		fmt.Println()
	}

	// Take before snapshot
	if !jsonOutput {
		fmt.Println("Taking 'before' snapshot...")
	}
	startTime := time.Now()
	beforeSnapshot := takeSnapshot(paths, excludePatterns)
	if !jsonOutput {
		fmt.Printf("  Captured %d files\n\n", len(beforeSnapshot.Files))
	}

	// Wait for user action
	if !jsonOutput {
		fmt.Println("==============================================")
		fmt.Printf("Now perform your %s login or logout action.\n", agent)
		fmt.Println("When done, press ENTER to capture changes...")
		fmt.Println("==============================================")
	}

	reader := bufio.NewReader(os.Stdin)
	_, _ = reader.ReadString('\n')

	// Take after snapshot
	if !jsonOutput {
		fmt.Println("\nTaking 'after' snapshot...")
	}
	afterSnapshot := takeSnapshot(paths, excludePatterns)
	if !jsonOutput {
		fmt.Printf("  Captured %d files\n\n", len(afterSnapshot.Files))
	}

	// Compute changes
	changes := computeChanges(beforeSnapshot, afterSnapshot)
	watchRules := deriveWatchRules(agent, changes)
	summary := computeSummary(beforeSnapshot, afterSnapshot, changes)
	duration := time.Since(startTime)

	// Build report
	report := TraceReport{
		Timestamp:  time.Now().Format(time.RFC3339),
		Agent:      agent,
		Duration:   duration.String(),
		Before:     beforeSnapshot,
		After:      afterSnapshot,
		Changes:    changes,
		WatchRules: watchRules,
		Summary:    summary,
	}

	// Output
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	printTraceReport(report, verbose)
	return nil
}

func printTraceReport(report TraceReport, verbose bool) {
	fmt.Println("=== Trace Report ===")
	fmt.Printf("Agent: %s\n", report.Agent)
	fmt.Printf("Duration: %s\n\n", report.Duration)

	// Changes summary
	totalChanges := report.Summary.FilesAdded + report.Summary.FilesModified + report.Summary.FilesRemoved
	if totalChanges == 0 {
		fmt.Println("No file changes detected.")
		fmt.Println("\nPossible reasons:")
		fmt.Println("  - Login was already complete")
		fmt.Println("  - Files are stored elsewhere")
		fmt.Println("  - Try with --verbose to see all tracked files")
		return
	}

	fmt.Printf("Changes detected: %d total\n", totalChanges)
	fmt.Printf("  + %d added\n", report.Summary.FilesAdded)
	fmt.Printf("  ~ %d modified\n", report.Summary.FilesModified)
	fmt.Printf("  - %d removed\n", report.Summary.FilesRemoved)
	fmt.Printf("  Bytes changed: %d\n\n", report.Summary.BytesChanged)

	// Added files
	if len(report.Changes.Added) > 0 {
		fmt.Println("Added files:")
		for _, c := range report.Changes.Added {
			fmt.Printf("  + %s (%d bytes)\n", shortenPath(c.Path), c.SizeAfter)
			if verbose && c.HashAfter != "" {
				fmt.Printf("    hash: %s\n", c.HashAfter[:16]+"...")
			}
		}
		fmt.Println()
	}

	// Modified files
	if len(report.Changes.Modified) > 0 {
		fmt.Println("Modified files:")
		for _, c := range report.Changes.Modified {
			sizeDiff := c.SizeAfter - c.SizeBefore
			sign := "+"
			if sizeDiff < 0 {
				sign = ""
			}
			fmt.Printf("  ~ %s (%s%d bytes)\n", shortenPath(c.Path), sign, sizeDiff)
			if verbose {
				fmt.Printf("    before: %d bytes", c.SizeBefore)
				if c.HashBefore != "" {
					fmt.Printf(" [%s...]", c.HashBefore[:16])
				}
				fmt.Println()
				fmt.Printf("    after:  %d bytes", c.SizeAfter)
				if c.HashAfter != "" {
					fmt.Printf(" [%s...]", c.HashAfter[:16])
				}
				fmt.Println()
			}
		}
		fmt.Println()
	}

	// Removed files
	if len(report.Changes.Removed) > 0 {
		fmt.Println("Removed files:")
		for _, c := range report.Changes.Removed {
			fmt.Printf("  - %s (%d bytes)\n", shortenPath(c.Path), c.SizeBefore)
		}
		fmt.Println()
	}

	// Watch rules
	if len(report.WatchRules) > 0 {
		fmt.Println("=== Derived Watch Rules ===")
		fmt.Println("Add these to caam watch configuration:")
		fmt.Println()
		for _, r := range report.WatchRules {
			fmt.Printf("  %s\n", r.Pattern)
			if r.Description != "" {
				fmt.Printf("    # %s\n", r.Description)
			}
		}
		fmt.Println()
	}

	// Actionable suggestions
	fmt.Println("=== Next Steps ===")
	fmt.Println("To backup the discovered auth files:")
	fmt.Printf("  caam backup %s <profile-name>\n\n", report.Agent)

	fmt.Println("To save this report as JSON:")
	fmt.Printf("  caam trace %s --json > trace-report.json\n", report.Agent)
}
