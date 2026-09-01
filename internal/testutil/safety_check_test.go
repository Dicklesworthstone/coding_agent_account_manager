package testutil

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestNoRealHomeWrites scans all test files to ensure they don't write to real home directories.
// This prevents bugs like the vault_test.go issue where tests corrupted real auth files.
func TestNoRealHomeWrites(t *testing.T) {
	rootDir := findProjectRoot(t)

	// Dangerous patterns that could write to real home directories
	dangerousPatterns := []struct {
		name    string
		pattern *regexp.Regexp
		message string
	}{
		{
			name:    "HOME_WITHOUT_TEMP",
			pattern: regexp.MustCompile(`os\.Getenv\("HOME"\)\s*\n[^}]*if\s+\w+\s*==\s*""\s*\{[^}]*=\s*tmpDir`),
			message: "Pattern 'if HOME == \"\" { use tmpDir }' is dangerous - HOME is always set. Use tmpDir directly.",
		},
		{
			name:    "USERHOMEDIR_WRITE",
			pattern: regexp.MustCompile(`UserHomeDir\(\)[^;]*\n[^}]*WriteFile`),
			message: "os.UserHomeDir() followed by WriteFile may write to real home directory.",
		},
		{
			name:    "DIRECT_HOME_PATH",
			pattern: regexp.MustCompile(`filepath\.Join\([^,]*os\.Getenv\("HOME"\)[^)]*\)[^;]*\n[^}]*WriteFile`),
			message: "filepath.Join with os.Getenv(\"HOME\") followed by WriteFile may write to real home.",
		},
	}

	// Files to scan
	var testFiles []string
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk directory: %v", err)
	}

	// Known safe exceptions (files that have been manually audited)
	safeExceptions := map[string]bool{
		// These files properly save/restore HOME or only read (don't write)
		"internal/sync/sync_test.go":               true, // Uses defer to restore HOME
		"internal/authwatch/authwatch_test.go":     true, // Uses defer to restore HOME
		"internal/passthrough/passthrough_test.go": true, // Only reads UserHomeDir for verification
		"internal/testutil/safety_check_test.go":   true, // This file - contains patterns for detection
		"internal/logs/claude_test.go":             true, // Uses t.TempDir(); UserHomeDir only for path verification
		"internal/logs/codex_test.go":              true, // Uses t.TempDir(); UserHomeDir only for path verification
		"internal/logs/gemini_test.go":             true, // Uses t.TempDir(); UserHomeDir only for path verification
		"internal/health/expiry_test.go":           true, // UserHomeDir only for path verification, no writes
		"internal/wezterm/config_test.go":          true, // UserHomeDir only for expected path comparison; WriteFile uses t.TempDir()
	}

	var violations []string

	for _, file := range testFiles {
		relPath, _ := filepath.Rel(rootDir, file)

		// Skip known safe files
		if safeExceptions[relPath] {
			continue
		}

		content, err := os.ReadFile(file)
		if err != nil {
			t.Errorf("Failed to read %s: %v", relPath, err)
			continue
		}

		for _, dp := range dangerousPatterns {
			if dp.pattern.Match(content) {
				violations = append(violations, relPath+": "+dp.message)
			}
		}

		// Additional line-by-line checks for more specific patterns
		violations = append(violations, checkFileForDangerousPatterns(relPath, content)...)
	}

	if len(violations) > 0 {
		t.Errorf("Found %d potential unsafe test patterns that could corrupt real auth files:\n\n%s\n\n"+
			"FIX: Always use t.TempDir() or explicitly set HOME to a temp directory.\n"+
			"If this is a false positive, add the file to safeExceptions with a comment explaining why.",
			len(violations), strings.Join(violations, "\n"))
	}
}

// checkFileForDangerousPatterns does line-by-line analysis for dangerous patterns
func checkFileForDangerousPatterns(relPath string, content []byte) []string {
	var violations []string
	scanner := bufio.NewScanner(strings.NewReader(string(content)))
	lineNum := 0

	// Track context
	var inTestFunc bool
	var hasHomeOverride bool
	var hasTempDir bool

	for scanner.Scan() {
		lineNum++
		line := scanner.Text()

		// Track if we're in a test function
		if strings.Contains(line, "func Test") {
			inTestFunc = true
			hasHomeOverride = false
			hasTempDir = false
		}

		// Track if we have safe patterns
		if strings.Contains(line, "t.TempDir()") {
			hasTempDir = true
		}
		if strings.Contains(line, `os.Setenv("HOME"`) && strings.Contains(line, "tmpDir") {
			hasHomeOverride = true
		}

		// Check for dangerous pattern: using HOME directly without override
		if inTestFunc && !hasHomeOverride && !hasTempDir {
			// Check for direct HOME usage in file operations
			if strings.Contains(line, `os.Getenv("HOME")`) {
				// Look for WriteFile or MkdirAll nearby
				if strings.Contains(line, "WriteFile") || strings.Contains(line, "MkdirAll") {
					violations = append(violations,
						relPath+":"+strconv.Itoa(lineNum)+": Uses os.Getenv(\"HOME\") without temp dir override")
				}
			}
		}

		// End of function (simplified detection)
		if inTestFunc && line == "}" {
			inTestFunc = false
		}
	}

	return violations
}

// TestNoHardcodedAuthPaths checks for hardcoded paths to auth files
func TestNoHardcodedAuthPaths(t *testing.T) {
	rootDir := findProjectRoot(t)

	// Patterns that might indicate hardcoded auth paths
	dangerousPaths := []string{
		`"~/.claude"`,
		`"~/.codex"`,
		`"~/.config/gemini"`,
		`"/home/` + `"`,    // Split to avoid matching this file
		`os.UserHomeDir()`, // Only dangerous in certain contexts
	}

	var testFiles []string
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk directory: %v", err)
	}

	// Files that are allowed to reference these paths (for verification, not writing)
	allowedFiles := map[string]bool{
		"internal/testutil/safety_check_test.go":  true, // This file
		"internal/provider/claude/claude_test.go": true, // Verifies paths
		"internal/provider/codex/codex_test.go":   true, // Verifies paths
		"internal/provider/gemini/gemini_test.go": true, // Verifies paths
		"internal/authfile/authfile_test.go":      true, // Verifies paths
	}

	for _, file := range testFiles {
		relPath, _ := filepath.Rel(rootDir, file)
		if allowedFiles[relPath] {
			continue
		}

		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		contentStr := string(content)
		for _, pattern := range dangerousPaths {
			if strings.Contains(contentStr, pattern) {
				// Check if it's in a WriteFile context
				lines := strings.Split(contentStr, "\n")
				for i, line := range lines {
					if strings.Contains(line, pattern) && strings.Contains(line, "WriteFile") {
						t.Errorf("%s:%d: Contains potentially dangerous path pattern %q with WriteFile",
							relPath, i+1, pattern)
					}
				}
			}
		}
	}
}

// TestTempDirUsage verifies that tests creating auth files use t.TempDir()
func TestTempDirUsage(t *testing.T) {
	rootDir := findProjectRoot(t)

	// Patterns that create auth-like files
	authFilePatterns := []string{
		"auth.json",
		".credentials.json",
		".claude.json",
		"credentials.json",
	}

	var testFiles []string
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, "_test.go") {
			testFiles = append(testFiles, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk directory: %v", err)
	}

	for _, file := range testFiles {
		relPath, _ := filepath.Rel(rootDir, file)
		content, err := os.ReadFile(file)
		if err != nil {
			continue
		}

		contentStr := string(content)

		// Check if file creates auth files
		createsAuthFiles := false
		for _, pattern := range authFilePatterns {
			if strings.Contains(contentStr, pattern) && strings.Contains(contentStr, "WriteFile") {
				createsAuthFiles = true
				break
			}
		}

		if !createsAuthFiles {
			continue
		}

		// Verify it uses t.TempDir(), b.TempDir(), testutil harness, or properly overrides HOME
		hasTempDir := strings.Contains(contentStr, "t.TempDir()") ||
			strings.Contains(contentStr, "b.TempDir()") // Benchmarks use b.TempDir()
		hasHarness := strings.Contains(contentStr, "testutil.NewHarness") ||
			strings.Contains(contentStr, "testutil.NewExtendedHarness") ||
			strings.Contains(contentStr, "NewHarness(t)") ||
			strings.Contains(contentStr, "NewExtendedHarness(t)")
		hasHomeOverride := strings.Contains(contentStr, `os.Setenv("HOME"`) &&
			(strings.Contains(contentStr, "tmpDir") || strings.Contains(contentStr, "TempDir"))

		if !hasTempDir && !hasHarness && !hasHomeOverride {
			t.Errorf("%s: Creates auth files but doesn't use t.TempDir(), testutil.NewHarness, or override HOME to temp dir", relPath)
		}
	}
}

// TestEveryTestPackageIsolatesHome fails when a package with tests has no
// TestMain that calls IsolatedMain. TestNoRealHomeWrites scans test sources
// for dangerous patterns, but the writers that corrupted a developer's
// ~/.claude.json live in production code the tests call (auth restore, wrap,
// the CLI commands), which no source scan can see. The runtime guard is the
// real protection; this test keeps it from eroding as packages are added.
func TestEveryTestPackageIsolatesHome(t *testing.T) {
	rootDir := findProjectRoot(t)

	testFiles := map[string][]string{}
	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "vendor" || info.Name() == "testdata") {
			return filepath.SkipDir
		}
		if strings.HasSuffix(path, "_test.go") {
			dir := filepath.Dir(path)
			testFiles[dir] = append(testFiles[dir], path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Failed to walk directory: %v", err)
	}

	var missing []string
	for dir, files := range testFiles {
		guarded := false
		for _, file := range files {
			content, err := os.ReadFile(file)
			if err != nil {
				t.Errorf("Failed to read %s: %v", file, err)
				continue
			}
			src := string(content)
			if strings.Contains(src, "func TestMain(") && strings.Contains(src, "IsolatedMain(") {
				guarded = true
				break
			}
		}
		if !guarded {
			rel, _ := filepath.Rel(rootDir, dir)
			missing = append(missing, rel)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("%d package(s) with tests do not run under an isolated HOME:\n\n  %s\n\n"+
			"FIX: add a main_test.go whose TestMain calls testutil.IsolatedMain(m) (copy any existing one). "+
			"Without it, `go test` on a machine with live logins can overwrite real auth files and launch real CLIs.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

// TestIsolateEnv checks the guard itself: HOME moves to a fresh directory,
// derived variables are cleared, secrets are dropped, the stubs shadow the
// real CLIs, and restore puts everything back.
func TestIsolateEnv(t *testing.T) {
	t.Setenv("HOME", "/nonexistent/home")
	t.Setenv("CODEX_HOME", "/nonexistent/codex")
	t.Setenv("ANTHROPIC_API_KEY", "secret")
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent/xdg")
	t.Setenv(isolatedMarker, "")
	os.Unsetenv(isolatedMarker)
	origPath := os.Getenv("PATH")

	restore, err := IsolateEnv()
	if err != nil {
		t.Fatalf("IsolateEnv() error = %v", err)
	}

	home := os.Getenv("HOME")
	if home == "/nonexistent/home" || home == "" {
		t.Fatalf("HOME = %q, want a fresh temporary directory", home)
	}
	if st, err := os.Stat(home); err != nil || !st.IsDir() {
		t.Errorf("HOME %q is not a directory: %v", home, err)
	}
	for _, key := range []string{"CODEX_HOME", "ANTHROPIC_API_KEY", "XDG_CONFIG_HOME"} {
		if v, ok := os.LookupEnv(key); ok {
			t.Errorf("%s = %q, want unset", key, v)
		}
	}
	if os.Getenv(isolatedMarker) != "1" {
		t.Errorf("%s not exported for helper processes", isolatedMarker)
	}
	if runtime.GOOS != "windows" {
		for _, bin := range []string{"claude", "codex", "open"} {
			path, err := exec.LookPath(bin)
			if err != nil {
				t.Errorf("LookPath(%s) error = %v, want a stub", bin, err)
				continue
			}
			if !strings.HasPrefix(path, home) {
				t.Errorf("LookPath(%s) = %q, want a stub under %q", bin, path, home)
			}
			if out, err := exec.Command(path, "--version").CombinedOutput(); err != nil || len(out) != 0 {
				t.Errorf("stub %s: output %q, err %v; want silent success", bin, out, err)
			}
		}
	}

	restore()
	if got := os.Getenv("HOME"); got != "/nonexistent/home" {
		t.Errorf("after restore HOME = %q, want %q", got, "/nonexistent/home")
	}
	if got := os.Getenv("ANTHROPIC_API_KEY"); got != "secret" {
		t.Errorf("after restore ANTHROPIC_API_KEY = %q, want restored", got)
	}
	if got := os.Getenv("PATH"); got != origPath {
		t.Errorf("after restore PATH changed")
	}
	if _, ok := os.LookupEnv(isolatedMarker); ok {
		t.Errorf("after restore %s should be unset again", isolatedMarker)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("after restore the temporary HOME %q still exists", home)
	}
}

func findProjectRoot(t *testing.T) string {
	t.Helper()

	// Start from current directory and walk up
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get working directory: %v", err)
	}

	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("Could not find project root (go.mod)")
		}
		dir = parent
	}
}
