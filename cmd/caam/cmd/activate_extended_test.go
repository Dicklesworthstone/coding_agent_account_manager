package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestActivateCommand_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Create vault and mock globals")

	rootDir := h.TempDir
	vaultDir := filepath.Join(rootDir, "vault")

	// Create database path
	dbDir := filepath.Join(rootDir, "db")
	require.NoError(t, os.MkdirAll(dbDir, 0755))

	// Override DB path via env var (caamdb.Open uses XDG_DATA_HOME/caam/caam.db)
	h.SetEnv("HOME", rootDir)
	h.SetEnv("XDG_DATA_HOME", rootDir) // DB will be at rootDir/caam/caam.db
	configDir := filepath.Join(rootDir, "caam")
	h.SetEnv("CAAM_HOME", configDir)

	// Override vault and tools
	originalVault := vault
	originalTools := make(map[string]func() authfile.AuthFileSet)
	for k, v := range tools {
		originalTools[k] = v
	}
	defer func() {
		vault = originalVault
		tools = originalTools
		// Reset flags that may have been modified during tests
		activateCmd.Flags().Set("json", "false")
		activateCmd.Flags().Set("auto", "false")
		activateCmd.Flags().Set("force", "false")
	}()

	vault = authfile.NewVault(vaultDir)

	// Setup profiles in vault
	profileDir := filepath.Join(vaultDir, "claude", "work")
	require.NoError(t, os.MkdirAll(profileDir, 0755))

	authPath := filepath.Join(profileDir, "auth.json")
	require.NoError(t, os.WriteFile(authPath, []byte(`{"token":"work"}`), 0600))

	// Define target location for restore
	homeDir := filepath.Join(rootDir, "home")
	targetPath := filepath.Join(homeDir, "auth.json")

	tools["claude"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{
			Tool: "claude",
			Files: []authfile.AuthFileSpec{
				{Path: targetPath, Required: true},
			},
		}
	}

	h.EndStep("Setup")

	// 2. Test Basic Activation
	h.StartStep("Activate", "Activate 'work' profile")

	// Set flags before capture
	activateCmd.Flags().Set("json", "true")
	activateCmd.Flags().Set("auto", "false")

	// Run with stdout capture (panic-safe)
	outputStr, err := captureStdout(t, func() error {
		return runActivate(activateCmd, []string{"claude", "work"})
	})
	require.NoError(t, err)

	// Parse output
	var output activateOutput
	require.NoError(t, json.Unmarshal([]byte(outputStr), &output))

	assert.True(t, output.Success)
	assert.Equal(t, "claude", output.Tool)
	assert.Equal(t, "work", output.Profile)

	// Verify file restored
	content, err := os.ReadFile(targetPath)
	require.NoError(t, err)
	assert.Equal(t, `{"token":"work"}`, string(content))

	h.EndStep("Activate")

	// 3. Test Unknown Tool
	h.StartStep("Error", "Test unknown tool")

	activateCmd.Flags().Set("json", "true")

	// In json mode a failure still emits the JSON payload on stdout, but now also
	// returns a non-nil error so the process exits non-zero (issue #24).
	outputStr, err = captureStdout(t, func() error {
		return runActivate(activateCmd, []string{"unknown", "work"})
	})
	require.Error(t, err)

	var errOutput activateOutput
	require.NoError(t, json.Unmarshal([]byte(outputStr), &errOutput))

	assert.False(t, errOutput.Success)
	assert.Contains(t, errOutput.Error, "unknown tool")

	h.EndStep("Error")

	// 4. Test Cooldown Enforcement
	h.StartStep("Cooldown", "Test activation with active cooldown")

	// Setup DB with cooldown
	db, err := caamdb.Open()
	require.NoError(t, err)
	_, err = db.SetCooldown("claude", "work", time.Now(), 1*time.Hour, "test cooldown")
	db.Close()
	require.NoError(t, err)

	// Enable cooldown in config (requires writing config file or mocking config load)
	// runActivate loads config via config.LoadSPMConfig() -> config.Load()
	// config.Load() looks for config.yaml.
	// We can write a config file to XDG_CONFIG_HOME/caam/config.yaml
	require.NoError(t, os.MkdirAll(configDir, 0755))
	configPath := filepath.Join(configDir, "config.yaml")
	configContent := `
stealth:
  cooldown:
    enabled: true
`
	require.NoError(t, os.WriteFile(configPath, []byte(configContent), 0600))
	h.SetEnv("XDG_CONFIG_HOME", rootDir) // for config.json (old config)

	// Try activate
	outputStr, err = captureStdout(t, func() error {
		return runActivate(activateCmd, []string{"claude", "work"})
	})
	// In json mode the error payload is on stdout and a non-nil error is also
	// returned so the process exits non-zero (issue #24).
	require.Error(t, err)

	var cooldownOutput activateOutput
	require.NoError(t, json.Unmarshal([]byte(outputStr), &cooldownOutput))

	// Should fail due to cooldown
	assert.False(t, cooldownOutput.Success)
	assert.Contains(t, cooldownOutput.Error, "is in cooldown")

	h.EndStep("Cooldown")
}
