package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAddHelperProcess is the entry point for the mock process.
// It is called by execCommand when mocking is enabled.
func TestAddHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}

	// This code runs inside the "mocked" process (e.g., 'claude login')

	// Read where we should write the auth file
	authPath := os.Getenv("MOCK_AUTH_PATH")
	if authPath == "" {
		fmt.Fprintf(os.Stderr, "MOCK_AUTH_PATH not set\n")
		os.Exit(1)
	}

	// Verify we are supposed to be "claude" or whatever
	args := os.Args
	for len(args) > 0 {
		if args[0] == "--" {
			args = args[1:]
			break
		}
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "No command provided\n")
		os.Exit(1)
	}

	cmdName := args[0]
	// Simulate login behavior based on command
	switch cmdName {
	case "claude":
		// Create the auth file that addCmd expects
		content := `{"sessionKey": "new-session-key"}`
		if err := os.WriteFile(authPath, []byte(content), 0600); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to write mock auth: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Mock login successful. Wrote to %s\n", authPath)
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "Unknown mock command: %s\n", cmdName)
		os.Exit(1)
	}
}

func TestAddExtended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup Test Environment
	h.StartStep("Setup", "Initialize temp directories and overrides")

	rootDir := h.TempDir
	homeDir := filepath.Join(rootDir, "home")
	vaultDir := filepath.Join(rootDir, "vault")
	require.NoError(t, os.MkdirAll(homeDir, 0755))
	require.NoError(t, os.MkdirAll(vaultDir, 0755))

	// Defines paths
	fakeAuthPath := filepath.Join(homeDir, ".claude.json")

	// Pre-create "old" auth file to test backup logic
	oldAuthContent := `{"sessionKey": "old-session-key"}`
	require.NoError(t, os.WriteFile(fakeAuthPath, []byte(oldAuthContent), 0600))
	h.LogDebug("Created initial auth file", "path", fakeAuthPath)

	// Save global state
	originalVault := vault
	originalTools := make(map[string]func() authfile.AuthFileSet)
	for k, v := range tools {
		originalTools[k] = v
	}
	originalExecCommand := execCommand
	defer func() {
		vault = originalVault
		tools = originalTools
		execCommand = originalExecCommand
	}()

	// Override globals
	vault = authfile.NewVault(vaultDir)
	tools["claude"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{
			Tool: "claude",
			Files: []authfile.AuthFileSpec{
				{
					Path:        fakeAuthPath,
					Required:    true,
					Description: "Main auth token",
				},
			},
		}
	}

	// Override execCommand to call TestHelperProcess
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		cs := []string{"-test.run=TestAddHelperProcess", "--", name}
		cs = append(cs, args...)
		cmd := exec.CommandContext(ctx, os.Args[0], cs...)
		cmd.Env = append(os.Environ(),
			"GO_WANT_HELPER_PROCESS=1",
			"MOCK_AUTH_PATH="+fakeAuthPath,
		)
		return cmd
	}
	h.EndStep("Setup")

	// 2. Execute Command
	h.StartStep("Execute", "Run add command for claude")

	// We need to provide "claude" and "new-profile" as args
	// We also set --force to skip confirmation prompts
	// And --no-activate to simplify assertion
	addCmd.Flags().Set("force", "true")
	addCmd.Flags().Set("no-activate", "true")

	err := runAdd(addCmd, []string{"claude", "test-profile"})
	require.NoError(t, err)
	h.EndStep("Execute")

	// 3. Verify Results
	h.StartStep("Verify", "Check vault and backups")

	// Verify new profile exists in vault
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	assert.Contains(t, profiles, "test-profile")

	// Verify backup was created (since we had existing auth)
	// It should start with _auto_backup_
	foundBackup := false
	for _, p := range profiles {
		if len(p) > 12 && p[:12] == "_auto_backup" {
			foundBackup = true
			break
		}
	}
	assert.True(t, foundBackup, "Should have created an auto backup")

	// Verify content of the new profile
	profilePath := vault.ProfilePath("claude", "test-profile")

	// Legacy sessionKey credentials belong in .claude.json. Backup preserves
	// that basename so the saved file can be recognized and restored.

	// Just check if the file exists inside the profile dir
	foundFile := false
	err = filepath.Walk(profilePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && info.Name() == ".claude.json" {
			foundFile = true
			content, _ := os.ReadFile(path)
			assert.Contains(t, string(content), "new-session-key")
		}
		return nil
	})
	require.NoError(t, err)
	assert.True(t, foundFile, "New auth file should be in the vault profile")
	h.EndStep("Verify")
}
