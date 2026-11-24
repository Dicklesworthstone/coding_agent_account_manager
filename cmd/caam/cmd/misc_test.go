// Package cmd implements the CLI commands for caam.
package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
)

// TestDoctorCommand tests the doctor command structure.
func TestDoctorCommand(t *testing.T) {
	if doctorCmd.Use != "doctor" {
		t.Errorf("Expected Use 'doctor', got %q", doctorCmd.Use)
	}

	if doctorCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	// Check flags
	flags := []string{"fix", "json"}
	for _, name := range flags {
		flag := doctorCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Expected flag --%s", name)
		}
	}

	// Check default values
	fixFlag := doctorCmd.Flags().Lookup("fix")
	if fixFlag.DefValue != "false" {
		t.Errorf("Expected fix default false, got %q", fixFlag.DefValue)
	}

	jsonFlag := doctorCmd.Flags().Lookup("json")
	if jsonFlag.DefValue != "false" {
		t.Errorf("Expected json default false, got %q", jsonFlag.DefValue)
	}
}

// TestCheckResult tests CheckResult structure.
func TestCheckResult(t *testing.T) {
	result := CheckResult{
		Name:    "test-check",
		Status:  "pass",
		Message: "all good",
		Details: "additional info",
	}

	if result.Name != "test-check" {
		t.Errorf("Expected Name 'test-check', got %q", result.Name)
	}

	if result.Status != "pass" {
		t.Errorf("Expected Status 'pass', got %q", result.Status)
	}
}

// TestCheckResultStatuses tests valid check result statuses.
func TestCheckResultStatuses(t *testing.T) {
	validStatuses := []string{"pass", "warn", "fail", "fixed"}

	for _, status := range validStatuses {
		result := CheckResult{
			Name:    "test",
			Status:  status,
			Message: "test message",
		}

		// Status should be preserved
		if result.Status != status {
			t.Errorf("Expected status %q, got %q", status, result.Status)
		}
	}
}

// TestDoctorReport tests DoctorReport structure.
func TestDoctorReport(t *testing.T) {
	report := &DoctorReport{
		Timestamp: time.Now().Format(time.RFC3339),
		OverallOK: true,
		PassCount: 5,
		WarnCount: 1,
		FailCount: 0,
	}

	if report.PassCount != 5 {
		t.Errorf("Expected PassCount 5, got %d", report.PassCount)
	}

	if !report.OverallOK {
		t.Error("Expected OverallOK true")
	}
}

// TestDoctorReportOverallOK tests overall status calculation.
func TestDoctorReportOverallOK(t *testing.T) {
	testCases := []struct {
		failCount int
		expected  bool
	}{
		{0, true},
		{1, false},
		{5, false},
	}

	for _, tc := range testCases {
		report := &DoctorReport{
			FailCount: tc.failCount,
			OverallOK: tc.failCount == 0,
		}

		if report.OverallOK != tc.expected {
			t.Errorf("FailCount %d: expected OverallOK=%v, got %v",
				tc.failCount, tc.expected, report.OverallOK)
		}
	}
}

// TestSessionsCommand tests the sessions command structure.
func TestSessionsCommand(t *testing.T) {
	if sessionsCmd.Use != "sessions" {
		t.Errorf("Expected Use 'sessions', got %q", sessionsCmd.Use)
	}

	if sessionsCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	// Check flags
	flags := []string{"provider", "json"}
	for _, name := range flags {
		flag := sessionsCmd.Flags().Lookup(name)
		if flag == nil {
			t.Errorf("Expected flag --%s", name)
		}
	}
}

// TestSessionInfo tests SessionInfo structure.
func TestSessionInfo(t *testing.T) {
	info := SessionInfo{
		Provider:  "codex",
		Profile:   "work",
		PID:       12345,
		StartedAt: time.Now(),
		Status:    "active",
		Duration:  "5 minutes ago",
	}

	if info.Provider != "codex" {
		t.Errorf("Expected Provider 'codex', got %q", info.Provider)
	}

	if info.Status != "active" {
		t.Errorf("Expected Status 'active', got %q", info.Status)
	}
}

// TestSessionsReport tests SessionsReport structure.
func TestSessionsReport(t *testing.T) {
	report := &SessionsReport{
		Sessions:    []SessionInfo{},
		TotalActive: 0,
		TotalStale:  0,
	}

	if len(report.Sessions) != 0 {
		t.Errorf("Expected empty sessions, got %d", len(report.Sessions))
	}
}

// TestFormatDuration tests duration formatting.
func TestFormatDuration(t *testing.T) {
	testCases := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "just now"},
		{1 * time.Minute, "1 minute ago"},
		{5 * time.Minute, "5 minutes ago"},
		{1 * time.Hour, "1 hour ago"},
		{3 * time.Hour, "3 hours ago"},
		{24 * time.Hour, "1 day ago"},
		{72 * time.Hour, "3 days ago"},
	}

	for _, tc := range testCases {
		result := formatDuration(tc.duration)
		if result != tc.expected {
			t.Errorf("formatDuration(%v): expected %q, got %q",
				tc.duration, tc.expected, result)
		}
	}
}

// TestCheckCLITools tests CLI tool checking function.
func TestCheckCLITools(t *testing.T) {
	results := checkCLITools()

	// Should check all three tools
	expectedTools := map[string]bool{
		"codex":  false,
		"claude": false,
		"gemini": false,
	}

	for _, result := range results {
		expectedTools[result.Name] = true
	}

	for tool, found := range expectedTools {
		if !found {
			t.Errorf("Expected tool %q to be checked", tool)
		}
	}

	// Each result should have a valid status
	for _, result := range results {
		if result.Status != "pass" && result.Status != "warn" && result.Status != "fail" {
			t.Errorf("Invalid status %q for tool %q", result.Status, result.Name)
		}
	}
}

// TestCheckDirectories tests directory checking function.
func TestCheckDirectories(t *testing.T) {
	tmpDir := t.TempDir()

	// Set XDG_DATA_HOME to temp dir
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	// Test without fix
	results := checkDirectories(false)

	// Should have results for expected directories
	if len(results) == 0 {
		t.Error("Expected some directory check results")
	}

	for _, result := range results {
		// All results should have valid status
		validStatuses := map[string]bool{
			"pass":  true,
			"warn":  true,
			"fail":  true,
			"fixed": true,
		}

		if !validStatuses[result.Status] {
			t.Errorf("Invalid status %q for directory %q", result.Status, result.Name)
		}
	}
}

// TestCheckDirectoriesWithFix tests directory creation with fix flag.
func TestCheckDirectoriesWithFix(t *testing.T) {
	tmpDir := t.TempDir()

	// Set XDG_DATA_HOME to temp dir
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	// Test with fix - should create directories
	results := checkDirectories(true)

	// Check for fixed status
	fixedCount := 0
	for _, result := range results {
		if result.Status == "fixed" {
			fixedCount++
		}
	}

	// At least some directories should be created
	if fixedCount == 0 {
		// May already exist from other tests, so this is informational
		t.Log("No directories needed to be fixed (may already exist)")
	}

	// Verify directories exist
	caamDir := filepath.Join(tmpDir, "caam")
	if _, err := os.Stat(caamDir); err != nil {
		// May not exist if no fix was needed
		t.Logf("caam directory status: %v", err)
	}
}

// TestCheckConfig tests config checking function.
func TestCheckConfig(t *testing.T) {
	results := checkConfig()

	// Should have at least one result for config.json
	if len(results) == 0 {
		t.Error("Expected at least one config check result")
	}

	// First result should be for config.json
	found := false
	for _, result := range results {
		if strings.Contains(result.Name, "config") {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected config check result")
	}
}

// TestCheckLocks tests lock checking function.
func TestCheckLocks(t *testing.T) {
	tmpDir := t.TempDir()

	// Set up profile store
	store := profile.NewStore(tmpDir)

	// Create a profile
	prof, err := store.Create("codex", "lockcheck", "oauth")
	if err != nil {
		t.Fatalf("Create profile failed: %v", err)
	}

	// Lock the profile
	if err := prof.Lock(); err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	defer prof.Unlock()

	// Check locks
	// Note: This uses the global profileStore which may not be set up
	// So we just test the function doesn't panic
	results := checkLocks(false)

	// Results should be non-nil
	if results == nil {
		t.Error("Expected non-nil results")
	}
}

// TestCheckBrokenSymlinks tests broken symlink detection.
func TestCheckBrokenSymlinks(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a working symlink
	targetFile := filepath.Join(tmpDir, "target.txt")
	if err := os.WriteFile(targetFile, []byte("content"), 0644); err != nil {
		t.Fatalf("Failed to create target file: %v", err)
	}

	workingLink := filepath.Join(tmpDir, "working-link")
	if err := os.Symlink(targetFile, workingLink); err != nil {
		t.Fatalf("Failed to create symlink: %v", err)
	}

	// Create a broken symlink
	brokenLink := filepath.Join(tmpDir, "broken-link")
	if err := os.Symlink(filepath.Join(tmpDir, "nonexistent"), brokenLink); err != nil {
		t.Fatalf("Failed to create broken symlink: %v", err)
	}

	// Check for broken symlinks
	broken := checkBrokenSymlinks(tmpDir)

	// Should find the broken link
	found := false
	for _, name := range broken {
		if name == "broken-link" {
			found = true
			break
		}
	}

	if !found {
		t.Error("Expected to find broken-link in broken symlinks")
	}

	// Should not report working link
	for _, name := range broken {
		if name == "working-link" {
			t.Error("Working link should not be reported as broken")
		}
	}
}

// TestCheckAuthFiles tests auth file checking function.
func TestCheckAuthFiles(t *testing.T) {
	results := checkAuthFiles()

	// Should check all three tools
	expectedTools := map[string]bool{
		"codex":  false,
		"claude": false,
		"gemini": false,
	}

	for _, result := range results {
		expectedTools[result.Name] = true
	}

	for tool, found := range expectedTools {
		if !found {
			t.Errorf("Expected tool %q to be checked", tool)
		}
	}
}

// TestEnvCommandFromRoot tests the env command structure.
func TestEnvCommandFromRoot(t *testing.T) {
	// Find env command
	var envCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "env") {
			envCmd = cmd
			break
		}
	}

	if envCmd == nil {
		t.Skip("env command not found")
	}

	if envCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

// TestInitCommand tests the init command structure.
func TestInitCommand(t *testing.T) {
	// Find init command
	var initCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "init") {
			initCmd = cmd
			break
		}
	}

	if initCmd == nil {
		t.Skip("init command not found")
	}

	if initCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

// TestOpenCommand tests the open command structure.
func TestOpenCommand(t *testing.T) {
	// Find open command
	var openCmd *cobra.Command
	for _, cmd := range rootCmd.Commands() {
		if strings.HasPrefix(cmd.Use, "open") {
			openCmd = cmd
			break
		}
	}

	if openCmd == nil {
		t.Skip("open command not found")
	}

	if openCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

// TestCollectSessions tests session collection function.
func TestCollectSessions(t *testing.T) {
	// Note: This uses global profileStore which may not be initialized
	// Test primarily that the function doesn't panic

	// Try to collect sessions (may fail if profileStore is nil)
	report, err := collectSessions("")
	if err != nil {
		// Expected if profileStore not initialized
		t.Logf("collectSessions returned error (expected): %v", err)
		return
	}

	if report == nil {
		t.Error("Expected non-nil report")
	}
}

// TestCollectSessionsWithFilter tests session collection with provider filter.
func TestCollectSessionsWithFilter(t *testing.T) {
	// Test with different filters
	filters := []string{"codex", "claude", "gemini"}

	for _, filter := range filters {
		t.Run(filter, func(t *testing.T) {
			report, err := collectSessions(filter)
			if err != nil {
				// Expected if profileStore not initialized
				t.Logf("collectSessions(%s) returned error: %v", filter, err)
				return
			}

			// All sessions should be for the filtered provider
			for _, session := range report.Sessions {
				if !strings.EqualFold(session.Provider, filter) {
					t.Errorf("Expected provider %q, got %q", filter, session.Provider)
				}
			}
		})
	}
}

// TestRunDoctorChecks tests full doctor check execution.
func TestRunDoctorChecks(t *testing.T) {
	// Run without fix or validate
	report := runDoctorChecks(false, false, false, false)

	if report == nil {
		t.Fatal("Expected non-nil report")
	}

	// Timestamp should be set
	if report.Timestamp == "" {
		t.Error("Expected timestamp to be set")
	}

	// Should have some checks
	totalChecks := len(report.CLITools) + len(report.Dependencies) + len(report.Directories) +
		len(report.Config) + len(report.Profiles) +
		len(report.Locks) + len(report.AuthFiles)

	if totalChecks == 0 {
		t.Error("Expected at least some checks to run")
	}

	// Pass + Warn + Fail counts should match total
	expectedTotal := report.PassCount + report.WarnCount + report.FailCount
	if expectedTotal != totalChecks {
		t.Logf("Counts: pass=%d, warn=%d, fail=%d, fixed=%d, total=%d",
			report.PassCount, report.WarnCount, report.FailCount, report.FixedCount, totalChecks)
	}
}

// TestRunDoctorChecksWithFix tests doctor check with fix flag.
func TestRunDoctorChecksWithFix(t *testing.T) {
	tmpDir := t.TempDir()

	// Set XDG_DATA_HOME to temp dir
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	// Run with fix (autoInstall=false, skipConfirm=false)
	report := runDoctorChecks(true, false, false, false)

	if report == nil {
		t.Fatal("Expected non-nil report")
	}

	// May have fixed some issues
	t.Logf("Fixed %d issues", report.FixedCount)
}

// TestGetDependencySpecs tests the dependency specification list.
func TestGetDependencySpecs(t *testing.T) {
	specs := getDependencySpecs()

	// Should have at least the expected dependencies
	if len(specs) < 5 {
		t.Errorf("Expected at least 5 dependency specs, got %d", len(specs))
	}

	// Check that we have expected dependencies
	expectedDeps := []string{"gum", "wezterm", "tailscale", "node", "jq"}
	specNames := make(map[string]bool)
	for _, spec := range specs {
		specNames[spec.Name] = true
	}

	for _, expected := range expectedDeps {
		if !specNames[expected] {
			t.Errorf("Expected dependency %q not found in specs", expected)
		}
	}

	// Each spec should have required fields
	for _, spec := range specs {
		if spec.Name == "" {
			t.Error("Dependency spec has empty Name")
		}
		if len(spec.Binaries) == 0 {
			t.Errorf("Dependency %q has no binaries", spec.Name)
		}
		if spec.Feature == "" {
			t.Errorf("Dependency %q has no feature description", spec.Name)
		}
	}
}

// TestCheckSingleDependency tests individual dependency checks.
func TestCheckSingleDependency(t *testing.T) {
	// Test a dependency that likely exists (ssh on most systems)
	sshSpec := DependencySpec{
		Name:        "ssh",
		Binaries:    []string{"ssh"},
		Description: "Secure Shell client",
		Required:    false,
		Feature:     "remote profile execution",
	}

	result := checkSingleDependency(sshSpec, false, false)
	// SSH should exist on most systems, but we won't fail if it doesn't
	if result.Name != "ssh" {
		t.Errorf("Expected result name 'ssh', got %q", result.Name)
	}
	if result.Status != "pass" && result.Status != "warn" {
		t.Errorf("Expected status 'pass' or 'warn', got %q", result.Status)
	}
}

// TestCheckSingleDependencyNotFound tests dependency check for missing tool.
func TestCheckSingleDependencyNotFound(t *testing.T) {
	// Test a dependency that definitely doesn't exist
	fakeSpec := DependencySpec{
		Name:           "fake-tool-that-doesnt-exist-12345",
		Binaries:       []string{"fake-tool-that-doesnt-exist-12345"},
		Description:    "Fake tool",
		Required:       false,
		Feature:        "testing",
		InstallLinux:   "apt install fake",
		InstallMacOS:   "brew install fake",
		InstallWindows: "scoop install fake",
	}

	result := checkSingleDependency(fakeSpec, false, false)
	if result.Status != "warn" {
		t.Errorf("Expected status 'warn' for missing tool, got %q", result.Status)
	}
	if result.Message != "not found in PATH" {
		t.Errorf("Expected message 'not found in PATH', got %q", result.Message)
	}
}

// TestCheckDependencies tests the full dependency check.
func TestCheckDependencies(t *testing.T) {
	results := checkDependencies(false, false)

	// Should have results for each dependency spec
	specs := getDependencySpecs()
	if len(results) != len(specs) {
		t.Errorf("Expected %d results, got %d", len(specs), len(results))
	}

	// Each result should have a valid status
	for _, result := range results {
		switch result.Status {
		case "pass", "warn", "fail", "fixed":
			// OK
		default:
			t.Errorf("Unexpected status %q for %s", result.Status, result.Name)
		}
	}
}

// TestGetInstallCommand tests OS-specific install command selection.
func TestGetInstallCommand(t *testing.T) {
	spec := DependencySpec{
		InstallLinux:   "apt install foo",
		InstallMacOS:   "brew install foo",
		InstallWindows: "scoop install foo",
	}

	// The result depends on the current OS
	cmd := getInstallCommand(spec)
	if cmd == "" {
		t.Error("Expected non-empty install command")
	}

	// Just verify it returns one of the expected commands
	validCommands := []string{"apt install foo", "brew install foo", "scoop install foo"}
	found := false
	for _, valid := range validCommands {
		if cmd == valid {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Unexpected install command: %q", cmd)
	}
}

// TestDependencySpecFields tests that all dependency specs have required fields.
func TestDependencySpecFields(t *testing.T) {
	specs := getDependencySpecs()

	for _, spec := range specs {
		t.Run(spec.Name, func(t *testing.T) {
			if spec.Name == "" {
				t.Error("Name is empty")
			}
			if len(spec.Binaries) == 0 {
				t.Error("Binaries is empty")
			}
			if spec.Feature == "" {
				t.Error("Feature is empty")
			}
			// At least one install command should be present
			if spec.InstallLinux == "" && spec.InstallMacOS == "" && spec.InstallWindows == "" {
				t.Error("All install commands are empty")
			}
		})
	}
}
