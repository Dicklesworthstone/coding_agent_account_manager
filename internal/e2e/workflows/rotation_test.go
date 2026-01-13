package workflows

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRotationWorkflow(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize environment and profiles")
	rootDir := h.TempDir
	
	// Create config with round_robin
	configDir := filepath.Join(rootDir, "caam")
	require.NoError(t, os.MkdirAll(configDir, 0755))
	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
stealth:
  rotation:
    enabled: true
    algorithm: round_robin
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0600))
	
	// Setup vault with 3 profiles
	// When CAAM_HOME is set, vault path is CAAM_HOME/data/vault (not CAAM_HOME/vault)
	vaultDir := filepath.Join(configDir, "data", "vault")
	h.SetEnv("XDG_DATA_HOME", rootDir)
	h.SetEnv("CAAM_HOME", configDir)
	
	createProfile := func(name string) {
		dir := filepath.Join(vaultDir, "claude", name)
		require.NoError(t, os.MkdirAll(dir, 0755))
		// Mock auth file
		require.NoError(t, os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(fmt.Sprintf(`{"token":"%s"}`, name)), 0600))
	}
	
	createProfile("p1")
	createProfile("p2")
	createProfile("p3")
	
	// Ensure names are sorted p1, p2, p3. Vault listing order depends on FS, but rotation sorts them.
	
	// Helper to run CLI
	exe, err := os.Executable()
	require.NoError(t, err)
	
	// Mock tools map for CLI helper?
	// The CLI helper runs the binary. The binary uses `tools` map in `root.go`.
	// That map has `authfile.ClaudeAuthFiles`.
	// `ClaudeAuthFiles` checks home dir.
	// We need to set HOME so `ClaudeAuthFiles` points to a place where we can check active profile?
	// `activate` restores to `ClaudeAuthFiles`.
	
homeDir := filepath.Join(rootDir, "home")
	require.NoError(t, os.MkdirAll(homeDir, 0755))
	h.SetEnv("HOME", homeDir)
	
	// We also need to override `tools` in the subprocess?
	// We can't easily override global variables in the subprocess unless we use `TestCLIHelper` to do it.
	// But `TestCLIHelper` just calls `cmd.Execute()`.
	
	// However, `ClaudeAuthFiles` uses `os.UserHomeDir()`.
	// We mocked `HOME` env var. `os.UserHomeDir` usually respects it on Linux/macOS.
	// So `~/.claude.json` will be `rootDir/home/.claude.json`.
	// This is fine. We don't need to override `tools` map if we control HOME.
	
env := os.Environ()
	env = append(env, "GO_WANT_CLI_HELPER=1")
	env = append(env, fmt.Sprintf("XDG_DATA_HOME=%s", rootDir))
	env = append(env, fmt.Sprintf("CAAM_HOME=%s", configDir))
	env = append(env, fmt.Sprintf("HOME=%s", homeDir))
	
	runCLI := func(args ...string) (string, error) {
		cmdArgs := []string{"-test.run=^TestCLIHelper$", "--"}
		cmdArgs = append(cmdArgs, args...)
		cmd := exec.Command(exe, cmdArgs...)
		cmd.Env = env
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	
h.EndStep("Setup")
	
	// 2. Activate Auto (Round 1)
	h.StartStep("Round1", "First activation")
	out, err := runCLI("activate", "claude", "--auto")
	if err != nil {
		t.Fatalf("activate failed: %v\nOutput: %s", err, out)
	}
	// Should pick p1 (first alphabetically)
	assert.Contains(t, out, "Activated claude profile 'p1'")
	
	// Verify active profile
	// We can verify by checking which file content is in home
	content, _ := os.ReadFile(filepath.Join(homeDir, ".claude.json"))
	assert.Contains(t, string(content), "p1")
	
h.EndStep("Round1")
	
	// 3. Activate Auto (Round 2)
	h.StartStep("Round2", "Second activation")
	out, err = runCLI("activate", "claude", "--auto")
	require.NoError(t, err)
	// Round robin: next after p1 is p2
	assert.Contains(t, out, "Activated claude profile 'p2'")
	h.EndStep("Round2")
	
	// 4. Activate Auto (Round 3)
	h.StartStep("Round3", "Third activation")
	out, err = runCLI("activate", "claude", "--auto")
	require.NoError(t, err)
	// Round robin: next after p2 is p3
	assert.Contains(t, out, "Activated claude profile 'p3'")
	h.EndStep("Round3")
	
	// 5. Activate Auto (Round 4 - Loop)
	h.StartStep("Round4", "Fourth activation (loop)")
	out, err = runCLI("activate", "claude", "--auto")
	require.NoError(t, err)
	// Round robin: next after p3 is p1
	assert.Contains(t, out, "Activated claude profile 'p1'")
	h.EndStep("Round4")
}
