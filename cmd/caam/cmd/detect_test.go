package cmd

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

func TestCheckPaths(t *testing.T) {
	// Create temp directory for testing using t.TempDir() for automatic cleanup
	tmpDir := t.TempDir()

	// Create a test file
	testFile := filepath.Join(tmpDir, "test-auth.json")
	if err := os.WriteFile(testFile, []byte(`{"test": true}`), 0600); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	// Create a test directory
	testDir := filepath.Join(tmpDir, "test-config")
	if err := os.MkdirAll(testDir, 0700); err != nil {
		t.Fatalf("failed to create test dir: %v", err)
	}

	// Create an unreadable file (if running as non-root)
	unreadableFile := filepath.Join(tmpDir, "unreadable.json")
	if err := os.WriteFile(unreadableFile, []byte(`{}`), 0000); err != nil {
		t.Fatalf("failed to create unreadable file: %v", err)
	}

	specs := []PathSpec{
		{Path: testFile, Description: "Test auth file"},
		{Path: testDir, Description: "Test config dir"},
		{Path: filepath.Join(tmpDir, "nonexistent.json"), Description: "Missing file"},
		{Path: unreadableFile, Description: "Unreadable file"},
	}

	results := checkPaths(specs)

	if len(results) != len(specs) {
		t.Errorf("expected %d results, got %d", len(specs), len(results))
	}

	// Check existing readable file
	if !results[0].Exists || !results[0].Readable {
		t.Errorf("expected test file to exist and be readable")
	}

	// Check existing readable directory
	if !results[1].Exists || !results[1].Readable {
		t.Errorf("expected test dir to exist and be readable")
	}

	// Check non-existent file
	if results[2].Exists {
		t.Errorf("expected nonexistent file to not exist")
	}

	// Check unreadable file (may be readable if running as root)
	if results[3].Exists && os.Getuid() != 0 && results[3].Readable {
		t.Errorf("expected unreadable file to not be readable")
	}
}

func TestHasValidAuth(t *testing.T) {
	tests := []struct {
		name     string
		paths    []DetectedPath
		expected bool
	}{
		{
			name:     "no paths",
			paths:    []DetectedPath{},
			expected: false,
		},
		{
			name: "all missing",
			paths: []DetectedPath{
				{Path: "/nonexistent1", Exists: false, Readable: false},
				{Path: "/nonexistent2", Exists: false, Readable: false},
			},
			expected: false,
		},
		{
			name: "exists but not readable",
			paths: []DetectedPath{
				{Path: "/exists", Exists: true, Readable: false},
			},
			expected: false,
		},
		{
			name: "exists and readable",
			paths: []DetectedPath{
				{Path: "/valid", Exists: true, Readable: true},
			},
			expected: true,
		},
		{
			name: "mixed - one valid",
			paths: []DetectedPath{
				{Path: "/missing", Exists: false, Readable: false},
				{Path: "/valid", Exists: true, Readable: true},
				{Path: "/unreadable", Exists: true, Readable: false},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := hasValidAuth(tc.paths)
			if result != tc.expected {
				t.Errorf("hasValidAuth() = %v, expected %v", result, tc.expected)
			}
		})
	}
}

func TestGetAgentSpecs(t *testing.T) {
	specs := getAgentSpecs()

	// Should have at least the core agents
	if len(specs) < 3 {
		t.Errorf("expected at least 3 agent specs, got %d", len(specs))
	}

	// Check that required agents are present
	names := make(map[string]bool)
	for _, spec := range specs {
		names[spec.Name] = true

		// Each spec should have required fields
		if spec.Name == "" {
			t.Error("spec has empty name")
		}
		if spec.DisplayName == "" {
			t.Errorf("spec %s has empty display name", spec.Name)
		}
		if len(spec.BinaryNames) == 0 {
			t.Errorf("spec %s has no binary names", spec.Name)
		}

		// Config and auth path functions should work
		if spec.ConfigPaths == nil {
			t.Errorf("spec %s has nil ConfigPaths", spec.Name)
		} else {
			paths := spec.ConfigPaths()
			if paths == nil {
				t.Errorf("spec %s ConfigPaths() returned nil", spec.Name)
			}
		}

		if spec.AuthPaths == nil {
			t.Errorf("spec %s has nil AuthPaths", spec.Name)
		} else {
			paths := spec.AuthPaths()
			if paths == nil {
				t.Errorf("spec %s AuthPaths() returned nil", spec.Name)
			}
		}
	}

	// Verify core agents are present
	requiredAgents := []string{"claude", "codex", "gemini"}
	for _, name := range requiredAgents {
		if !names[name] {
			t.Errorf("missing required agent spec: %s", name)
		}
	}
}

func TestDetectAgentNotInstalled(t *testing.T) {
	spec := AgentSpec{
		Name:        "nonexistent-cli",
		DisplayName: "Nonexistent CLI",
		BinaryNames: []string{"definitely-does-not-exist-12345"},
		ConfigPaths: func() []PathSpec { return []PathSpec{} },
		AuthPaths:   func() []PathSpec { return []PathSpec{} },
	}

	agent := detectAgent(context.Background(), spec, false)

	if agent.Installed {
		t.Error("expected agent to not be installed")
	}
	if agent.Status != StatusNotFound {
		t.Errorf("expected status %s, got %s", StatusNotFound, agent.Status)
	}
	if agent.BinaryPath != "" {
		t.Errorf("expected empty binary path, got %s", agent.BinaryPath)
	}
}

func TestRunDetectionFiltering(t *testing.T) {
	// Test that runDetection returns results for specified specs only
	specs := []AgentSpec{
		{
			Name:        "test1",
			DisplayName: "Test 1",
			BinaryNames: []string{"nonexistent1"},
			ConfigPaths: func() []PathSpec { return []PathSpec{} },
			AuthPaths:   func() []PathSpec { return []PathSpec{} },
		},
		{
			Name:        "test2",
			DisplayName: "Test 2",
			BinaryNames: []string{"nonexistent2"},
			ConfigPaths: func() []PathSpec { return []PathSpec{} },
			AuthPaths:   func() []PathSpec { return []PathSpec{} },
		},
	}

	report := runDetection(context.Background(), specs, false)

	if len(report.Agents) != 2 {
		t.Errorf("expected 2 agents, got %d", len(report.Agents))
	}

	if report.Summary.TotalAgents != 2 {
		t.Errorf("expected TotalAgents=2, got %d", report.Summary.TotalAgents)
	}

	// Both should be not found
	if report.Summary.NotFound != 2 {
		t.Errorf("expected NotFound=2, got %d", report.Summary.NotFound)
	}
}

func TestDetectedAgentStatus(t *testing.T) {
	// Test status string values
	tests := []struct {
		status   DetectedAgentStatus
		expected string
	}{
		{StatusReady, "ready"},
		{StatusNeedsAuth, "needs_auth"},
		{StatusNotFound, "not_found"},
		{StatusUnavailable, "unavailable"},
	}

	for _, tc := range tests {
		if string(tc.status) != tc.expected {
			t.Errorf("status %v = %q, expected %q", tc.status, string(tc.status), tc.expected)
		}
	}
}

func TestVersionRegex(t *testing.T) {
	// Test version extraction patterns
	specs := getAgentSpecs()

	testCases := []struct {
		agent    string
		output   string
		expected string
	}{
		{"claude", "claude-code 1.0.38", "1.0.38"},
		{"claude", "Claude Code v1.2.3", "1.2.3"},
		{"codex", "codex 0.1.2", "0.1.2"},
		{"codex", "Codex CLI v2.0.0", "2.0.0"},
		{"gemini", "gemini 1.0.0", "1.0.0"},
	}

	for _, tc := range testCases {
		t.Run(tc.agent+":"+tc.output, func(t *testing.T) {
			var spec *AgentSpec
			for i := range specs {
				if specs[i].Name == tc.agent {
					spec = &specs[i]
					break
				}
			}
			if spec == nil {
				t.Skipf("spec for %s not found", tc.agent)
			}

			// Test the regex pattern
			re, err := regexp.Compile(spec.VersionRegex)
			if err != nil {
				t.Fatalf("invalid regex: %v", err)
			}
			matches := re.FindStringSubmatch(tc.output)
			if len(matches) < 2 {
				// Try fallback
				fallback := regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
				match := fallback.FindString(tc.output)
				if match != tc.expected {
					t.Errorf("version extraction failed: got %q, expected %q", match, tc.expected)
				}
			} else if matches[1] != tc.expected {
				t.Errorf("version extraction: got %q, expected %q", matches[1], tc.expected)
			}
		})
	}
}
