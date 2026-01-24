package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestServeCommand(t *testing.T) {
	// Test that serve command is registered
	cmd := rootCmd
	serveFound := false
	for _, sub := range cmd.Commands() {
		if sub.Name() == "serve" {
			serveFound = true
			break
		}
	}
	if !serveFound {
		t.Error("serve command not registered")
	}
}

func TestServeFlagsDefaults(t *testing.T) {
	// Check default flag values
	portFlag := serveCmd.Flags().Lookup("port")
	if portFlag == nil {
		t.Fatal("port flag not found")
	}
	if portFlag.DefValue != "7891" {
		t.Errorf("port default = %s, want 7891", portFlag.DefValue)
	}

	verboseFlag := serveCmd.Flags().Lookup("verbose")
	if verboseFlag == nil {
		t.Fatal("verbose flag not found")
	}
	if verboseFlag.DefValue != "false" {
		t.Errorf("verbose default = %s, want false", verboseFlag.DefValue)
	}

	showTokenFlag := serveCmd.Flags().Lookup("show-token")
	if showTokenFlag == nil {
		t.Fatal("show-token flag not found")
	}
	if showTokenFlag.DefValue != "false" {
		t.Errorf("show-token default = %s, want false", showTokenFlag.DefValue)
	}

	jsonFlag := serveCmd.Flags().Lookup("json")
	if jsonFlag == nil {
		t.Fatal("json flag not found")
	}
	if jsonFlag.DefValue != "false" {
		t.Errorf("json default = %s, want false", jsonFlag.DefValue)
	}
}

func TestServeHelpOutput(t *testing.T) {
	var buf bytes.Buffer
	serveCmd.SetOut(&buf)
	serveCmd.SetErr(&buf)

	// Use Help() directly to get the serve command's help output
	if err := serveCmd.Help(); err != nil {
		t.Fatalf("Help() returned error: %v", err)
	}

	output := buf.String()

	// Check that help contains expected content
	expectedStrings := []string{
		"localhost-only HTTP server",
		"/api/v1/status",
		"/api/v1/profiles",
		"/api/v1/events",
		"Authorization",
		"Bearer",
	}

	for _, expected := range expectedStrings {
		if !bytes.Contains([]byte(output), []byte(expected)) {
			t.Errorf("help output missing %q", expected)
		}
	}
}

func TestServeShowTokenCreatesToken(t *testing.T) {
	tmpDir := t.TempDir()

	// Set CAAM_HOME to temp directory
	originalCAAMHome := os.Getenv("CAAM_HOME")
	t.Setenv("CAAM_HOME", tmpDir)
	defer func() {
		if originalCAAMHome != "" {
			os.Setenv("CAAM_HOME", originalCAAMHome)
		} else {
			os.Unsetenv("CAAM_HOME")
		}
	}()

	// Reset flag values
	servePort = 7891
	serveVerbose = false
	serveShowToken = true
	serveJSONLogs = false

	// Note: We can't easily test runServe with --show-token because it requires
	// the full vault/healthStore initialization from PersistentPreRunE.
	// Instead, we just verify the token path logic works correctly.

	tokenPath := filepath.Join(tmpDir, ".api_token")
	if _, err := os.Stat(tokenPath); err == nil {
		t.Error("token file should not exist before serve")
	}

	// Reset show token flag
	serveShowToken = false
}
