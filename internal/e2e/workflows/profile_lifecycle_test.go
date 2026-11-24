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

func TestProfileLifecycle(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize environment")
	rootDir := h.TempDir

	// Set XDG vars to isolate the test
	env := os.Environ()
	env = append(env, "GO_WANT_CLI_HELPER=1")
	env = append(env, fmt.Sprintf("XDG_DATA_HOME=%s", rootDir))
	env = append(env, fmt.Sprintf("XDG_CONFIG_HOME=%s", rootDir))
	env = append(env, fmt.Sprintf("HOME=%s", rootDir)) // Some tools use HOME

	exe, err := os.Executable()
	require.NoError(t, err)

	runCLI := func(args ...string) (string, error) {
		cmdArgs := []string{"-test.run=^TestCLIHelper$", "--"}
		cmdArgs = append(cmdArgs, args...)

		cmd := exec.Command(exe, cmdArgs...)
		cmd.Env = env

		output, err := cmd.CombinedOutput()
		return string(output), err
	}

	h.EndStep("Setup")

	// 2. Add Profile
	h.StartStep("Add", "Create isolated profile")
	out, err := runCLI("profile", "add", "claude", "test-work", "--description", "Work Profile")
	if err != nil {
		t.Fatalf("profile add failed: %v\nOutput: %s", err, out)
	}
	assert.Contains(t, out, "Created profile claude/test-work")

	// Verify directory structure
	profileDir := filepath.Join(rootDir, "caam", "profiles", "claude", "test-work")
	require.DirExists(t, profileDir)
	require.DirExists(t, filepath.Join(profileDir, "home"))
	require.FileExists(t, filepath.Join(profileDir, "profile.json"))

	h.EndStep("Add")

	// 3. Status
	h.StartStep("Status", "Check profile status")
	out, err = runCLI("profile", "status", "claude", "test-work")
	require.NoError(t, err)
	assert.Contains(t, out, "Profile: claude/test-work")
	assert.Contains(t, out, "Description: Work Profile")

	h.EndStep("Status")

	// 4. Update Description (Describe)
	h.StartStep("Describe", "Update description")
	out, err = runCLI("profile", "describe", "claude", "test-work", "New Description")
	require.NoError(t, err)
	assert.Contains(t, out, "Set description")

	out, err = runCLI("profile", "describe", "claude", "test-work")
	require.NoError(t, err)
	assert.Contains(t, out, "New Description")

	h.EndStep("Describe")

	// 5. List
	h.StartStep("List", "List profiles")
	out, err = runCLI("profile", "ls", "claude")
	require.NoError(t, err)
	assert.Contains(t, out, "test-work")
	assert.Contains(t, out, "New Description")

	h.EndStep("List")

	// 6. Exec (Simulation)
	// We can't easily run real 'claude' command since it's not installed.
	// But 'exec' runs 'runner.Run'.
	// We can try to run 'caam exec claude test-work -- echo hello'.
	// But 'runner' expects a provider. It tries to execute the provider binary.
	// We need to mock the provider binary?
	// Or we can rely on unit tests for 'exec'.
	// Let's just skip 'exec' in this lifecycle test unless we can mock the binary PATH.

	// 7. Delete
	h.StartStep("Delete", "Delete profile")
	out, err = runCLI("profile", "delete", "claude", "test-work", "--force")
	require.NoError(t, err)
	assert.Contains(t, out, "Deleted claude/test-work")

	// Verify directory gone
	if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
		t.Errorf("Profile directory still exists: %s", profileDir)
	}

	h.EndStep("Delete")
}
