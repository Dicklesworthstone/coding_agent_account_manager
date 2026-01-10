package workflows

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDaemonRefresh(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize environment and expiring profile")
	rootDir := h.TempDir
	pidFile := filepath.Join(os.TempDir(), "caam-daemon.pid")
	_ = os.Remove(pidFile)
	
	// Setup vault
	vaultDir := filepath.Join(rootDir, "vault")
	h.SetEnv("XDG_DATA_HOME", rootDir)
	
	// Create a profile with an expiring token
	// Daemon checks profiles in vault.
	profileDir := filepath.Join(vaultDir, "claude", "expiring")
	require.NoError(t, os.MkdirAll(profileDir, 0755))
	
	// Create .claude.json with expiry 5 mins from now (default threshold is 30m)
	expiring := time.Now().Add(5 * time.Minute).Format(time.RFC3339)
	authContent := fmt.Sprintf(`{
		"claudeAiOauth": {
			"accessToken": "old-token",
			"refreshToken": "refresh-me",
			"expiresAt": "%s"
		}
	}`, expiring)
	// Note: expiresAt in json is actually int64 (millis) usually in modern format, but string in some?
	// internal/refresh/claude.go handles response which has int or string.
	// But internal/health/expiry.go parses it.
	// Let's check how ParseClaudeExpiry works.
	// It parses .claude.json.
	
	// Actually, let's use the format that internal/health expects.
	// Assume it supports string RFC3339 or we might need to adjust.
	// Let's use 5 minutes from now.
	
	// Write auth file
	authPath := filepath.Join(profileDir, ".claude.json")
	require.NoError(t, os.WriteFile(authPath, []byte(authContent), 0600))
	
	// Env for helper
	env := os.Environ()
	env = append(env, "GO_WANT_DAEMON_HELPER=1")
	env = append(env, "MOCK_REFRESH_CLAUDE=1")
	env = append(env, fmt.Sprintf("XDG_DATA_HOME=%s", rootDir))
	
	h.EndStep("Setup")

	// 2. Start Daemon
	h.StartStep("Start", "Start daemon and wait for refresh")
	
	exe, err := os.Executable()
	require.NoError(t, err)
	
	// Run with --interval 1s to trigger check quickly
	// We need to override args in helper?
	// Helper forces args.
	// "caam daemon start --fg --verbose"
	// Default interval is 5m.
	
	// We need to allow overriding args in helper via env var?
	// Or modify helper to respect CAAM_DAEMON_ARGS env var.
	
	// For now, let's modify the helper to accept args override.
	
	// Let's pause and modify helper first.
	// I will do it in next step.
	
	// Assume helper supports CAAM_DAEMON_ARGS.
	env = append(env, "CAAM_DAEMON_ARGS=--interval 1s")
	
	cmd := exec.Command(exe, "-test.run=^TestDaemonHelper$")
	cmd.Env = env
	
	// Capture output
	logPath := filepath.Join(rootDir, "daemon.log")
	logFile, _ := os.Create(logPath)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	
	err = cmd.Start()
	require.NoError(t, err)
	
	// Wait for refresh to happen
	// We can check the auth file content
	
	h.LogInfo("Waiting for token update...")
	updated := false
	for i := 0; i < 100; i++ { // Wait up to 10s
		content, err := os.ReadFile(authPath)
		if err == nil {
			if strings.Contains(string(content), "new-mock-access-token") {
				updated = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	
	if !updated {
		logs, _ := os.ReadFile(logPath)
		fmt.Printf("Daemon Logs:\n%s\n", string(logs))
	}
	assert.True(t, updated, "Token was not refreshed")
	
	h.EndStep("Start")
	
	// 3. Cleanup
	cmd.Process.Signal(syscall.SIGTERM)
	cmd.Wait()
	logFile.Close()
}
