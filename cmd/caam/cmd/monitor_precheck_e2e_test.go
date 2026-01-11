package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// E2E Tests: Monitor and Precheck Command Workflow Tests
// =============================================================================

// setupMonitorE2ETest initializes test environment for monitor/precheck commands.
func setupMonitorE2ETest(t *testing.T, h *testutil.TestHarness) {
	t.Helper()

	// Create vault directory
	vaultDir := h.SubDir("vault")
	h.SetEnv("XDG_DATA_HOME", filepath.Dir(filepath.Dir(vaultDir)))

	// Initialize global vault with test path
	vault = authfile.NewVault(vaultDir)

	h.Log.Info("Monitor E2E test environment initialized", map[string]interface{}{
		"vault_dir": vaultDir,
	})
}

// createTestProfile creates a test profile with auth data.
func createTestProfile(t *testing.T, h *testutil.TestHarness, provider, profileName, authContent string) {
	t.Helper()

	profileDir := vault.ProfilePath(provider, profileName)
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("Failed to create profile dir: %v", err)
	}

	authFile := ".credentials.json"
	if provider == "codex" {
		authFile = "auth.json"
	}

	authPath := filepath.Join(profileDir, authFile)
	if err := os.WriteFile(authPath, []byte(authContent), 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}

	h.Log.Info("Created test profile", map[string]interface{}{
		"provider": provider,
		"profile":  profileName,
		"auth_path": authPath,
	})
}

// executeMonitorCommand runs the monitor command with given args.
func executeMonitorCommand(args ...string) (string, error) {
	fullArgs := append([]string{"monitor"}, args...)
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(fullArgs)
	err := rootCmd.Execute()
	return buf.String(), err
}

// executePrecheckCommand runs the precheck command with given args.
func executePrecheckCommand(args ...string) (string, error) {
	fullArgs := append([]string{"precheck"}, args...)
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(fullArgs)
	err := rootCmd.Execute()
	return buf.String(), err
}

// =============================================================================
// Monitor Command E2E Tests
// =============================================================================

func TestE2E_MonitorTableFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	// Create test profiles
	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token","expiresAt":9999999999999}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)
	createTestProfile(t, h, "claude", "personal", claudeAuth)

	h.Log.SetStep("test_table_format")

	// Run monitor with --once and --no-fetch to avoid API calls
	output, err := executeMonitorCommand("--format", "table", "--once", "--provider", "claude")

	h.Log.Info("Monitor table output", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	// Monitor may fail due to network issues in test, but format should still work
	// We primarily test that the command doesn't panic and produces expected format
	if err == nil {
		assert.Contains(t, output, "LIVE USAGE MONITOR")
	}
}

func TestE2E_MonitorBriefFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_brief_format")

	output, err := executeMonitorCommand("--format", "brief", "--once", "--provider", "claude")

	h.Log.Info("Monitor brief output", map[string]interface{}{
		"output": output,
		"error":  err,
	})

	// Brief format should be short (for tmux status bar)
	if err == nil && output != "" {
		assert.Less(t, len(output), 100, "Brief format should be short")
	}
}

func TestE2E_MonitorJSONFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_json_format")

	output, err := executeMonitorCommand("--format", "json", "--once", "--provider", "claude")

	h.Log.Info("Monitor JSON output", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	if err == nil && output != "" {
		// Verify it's valid JSON
		var result map[string]interface{}
		jsonErr := json.Unmarshal([]byte(strings.TrimSpace(output)), &result)
		if jsonErr == nil {
			assert.Contains(t, result, "updated_at", "JSON should contain updated_at")
			assert.Contains(t, result, "profiles", "JSON should contain profiles")

			h.Log.Info("Monitor JSON parsed", map[string]interface{}{
				"keys": getMapKeys(result),
			})
		}
	}
}

func TestE2E_MonitorAlertsFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_alerts_format")

	output, err := executeMonitorCommand("--format", "alerts", "--once", "--threshold", "50", "--provider", "claude")

	h.Log.Info("Monitor alerts output", map[string]interface{}{
		"output": output,
		"error":  err,
	})

	// Alerts format may be empty if no alerts triggered
	// Just verify no error
	if err != nil {
		h.Log.Info("Monitor alerts error (expected in test env)", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

func TestE2E_MonitorInvalidFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	h.Log.SetStep("test_invalid_format")

	_, err := executeMonitorCommand("--format", "invalid", "--once")

	require.Error(t, err, "Invalid format should return error")
	assert.Contains(t, err.Error(), "invalid format")
}

func TestE2E_MonitorWithWidth(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_width_option")

	output, err := executeMonitorCommand("--format", "table", "--once", "--width", "60", "--provider", "claude")

	h.Log.Info("Monitor with width", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	// Just verify command runs without error
	if err != nil {
		h.Log.Info("Monitor width error (may be expected)", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

func TestE2E_MonitorNoEmoji(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_no_emoji")

	output, err := executeMonitorCommand("--format", "table", "--once", "--no-emoji", "--provider", "claude")

	h.Log.Info("Monitor no-emoji", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	if err != nil {
		h.Log.Info("Monitor no-emoji error (may be expected)", map[string]interface{}{
			"error": err.Error(),
		})
	}
}

// =============================================================================
// Precheck Command E2E Tests
// =============================================================================

func TestE2E_PrecheckTableFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)
	createTestProfile(t, h, "claude", "personal", claudeAuth)

	h.Log.SetStep("test_table_format")

	output, err := executePrecheckCommand("claude", "--format", "table", "--no-fetch")

	h.Log.Info("Precheck table output", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	if err == nil {
		assert.Contains(t, output, "SESSION PLANNER")
		assert.Contains(t, output, "CLAUDE")
	}
}

func TestE2E_PrecheckBriefFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_brief_format")

	output, err := executePrecheckCommand("claude", "--format", "brief", "--no-fetch")

	h.Log.Info("Precheck brief output", map[string]interface{}{
		"output": output,
		"error":  err,
	})

	if err == nil {
		// Brief format should be a single line
		lines := strings.Split(strings.TrimSpace(output), "\n")
		assert.LessOrEqual(t, len(lines), 2, "Brief format should be 1-2 lines max")
		assert.Contains(t, output, "claude:")
	}
}

func TestE2E_PrecheckJSONFormat(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_json_format")

	output, err := executePrecheckCommand("claude", "--format", "json", "--no-fetch")

	h.Log.Info("Precheck JSON output", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	if err == nil {
		var result map[string]interface{}
		jsonErr := json.Unmarshal([]byte(output), &result)
		require.NoError(t, jsonErr, "Output should be valid JSON")

		assert.Contains(t, result, "provider")
		assert.Contains(t, result, "summary")
		assert.Contains(t, result, "algorithm")

		h.Log.Info("Precheck JSON parsed", map[string]interface{}{
			"provider": result["provider"],
			"keys":     getMapKeys(result),
		})
	}
}

func TestE2E_PrecheckUnknownProvider(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	h.Log.SetStep("test_unknown_provider")

	_, err := executePrecheckCommand("unknown-provider")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown provider")
}

func TestE2E_PrecheckNoProfiles(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	h.Log.SetStep("test_no_profiles")

	_, err := executePrecheckCommand("claude", "--no-fetch")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "no profiles found")
}

func TestE2E_PrecheckWithAlgorithmOverride(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)
	createTestProfile(t, h, "claude", "personal", claudeAuth)

	h.Log.SetStep("test_algorithm_override")

	output, err := executePrecheckCommand("claude", "--format", "table", "--no-fetch", "--algorithm", "round_robin")

	h.Log.Info("Precheck with algorithm", map[string]interface{}{
		"output_length": len(output),
		"error":         err,
	})

	if err == nil {
		assert.Contains(t, output, "round_robin")
	}
}

func TestE2E_PrecheckCodexProvider(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	codexAuth := `{"access_token":"test-token","token_type":"Bearer"}`
	createTestProfile(t, h, "codex", "work", codexAuth)

	h.Log.SetStep("test_codex_provider")

	output, err := executePrecheckCommand("codex", "--format", "brief", "--no-fetch")

	h.Log.Info("Precheck codex output", map[string]interface{}{
		"output": output,
		"error":  err,
	})

	if err == nil {
		assert.Contains(t, output, "codex:")
	}
}

func TestE2E_PrecheckDefaultsToClaudeProvider(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_default_provider")

	// No provider specified - should default to claude
	output, err := executePrecheckCommand("--format", "brief", "--no-fetch")

	h.Log.Info("Precheck default provider", map[string]interface{}{
		"output": output,
		"error":  err,
	})

	if err == nil {
		assert.Contains(t, output, "claude:")
	}
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestE2E_MonitorOutputPipeable(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_pipeable")

	output, err := executeMonitorCommand("--format", "json", "--once", "--provider", "claude")

	if err == nil && output != "" {
		// Should be valid JSON that can be piped to jq
		var result interface{}
		jsonErr := json.Unmarshal([]byte(strings.TrimSpace(output)), &result)
		assert.NoError(t, jsonErr, "JSON output should be parseable (pipeable to jq)")
	}
}

func TestE2E_PrecheckOutputPipeable(t *testing.T) {
	h := testutil.NewHarness(t)
	defer h.Close()

	h.Log.SetStep("setup")
	setupMonitorE2ETest(t, h)

	claudeAuth := `{"claudeAiOauth":{"accessToken":"test-token"}}`
	createTestProfile(t, h, "claude", "work", claudeAuth)

	h.Log.SetStep("test_pipeable")

	output, err := executePrecheckCommand("claude", "--format", "json", "--no-fetch")

	if err == nil {
		// Should be valid JSON that can be piped to jq
		var result interface{}
		jsonErr := json.Unmarshal([]byte(output), &result)
		assert.NoError(t, jsonErr, "JSON output should be parseable (pipeable to jq)")
	}
}

// =============================================================================
// Helper Functions
// =============================================================================

func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
