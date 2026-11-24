package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLsCommand_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Create vault with profiles")

	// Override global vault
	originalVault := vault
	originalTools := make(map[string]func() authfile.AuthFileSet)
	for k, v := range tools {
		originalTools[k] = v
	}
	defer func() {
		vault = originalVault
		tools = originalTools
	}()

	vaultDir := filepath.Join(h.TempDir, "vault")
	vault = authfile.NewVault(vaultDir)

	// Create some profiles manually in the vault structure
	// Claude profiles
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "work"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "personal"), 0755))

	// Codex profiles
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "codex", "project-x"), 0755))

	// We need to override tools map so lsCmd knows about these tools
	// and can check active profile (which requires fileSet)
	tools["claude"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{Tool: "claude", Files: []authfile.AuthFileSpec{}}
	}
	tools["codex"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
	}
	tools["gemini"] = func() authfile.AuthFileSet {
		return authfile.AuthFileSet{Tool: "gemini", Files: []authfile.AuthFileSpec{}}
	}
	h.EndStep("Setup")

	// 2. Execute ls --json
	h.StartStep("Execute", "Run ls --json")

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	// Reset flags
	lsCmd.Flags().Set("json", "true")
	lsCmd.Flags().Set("tag", "")
	lsCmd.Flags().Set("no-color", "false")

	// Run
	err := runLs(lsCmd, []string{})

	// Restore stdout
	w.Close()
	os.Stdout = oldStdout

	require.NoError(t, err)

	// Read output
	var buf bytes.Buffer
	io.Copy(&buf, r)
	outputJSON := buf.String()
	h.LogDebug("ls output", "json", outputJSON)
	h.EndStep("Execute")

	// 3. Verify
	h.StartStep("Verify", "Check JSON output")

	var output lsOutput
	err = json.Unmarshal([]byte(outputJSON), &output)
	require.NoError(t, err)

	assert.Equal(t, 3, output.Count)

	// Check profiles exist in output
	foundWork := false
	foundPersonal := false
	foundProjectX := false

	for _, p := range output.Profiles {
		if p.Tool == "claude" && p.Name == "work" {
			foundWork = true
		}
		if p.Tool == "claude" && p.Name == "personal" {
			foundPersonal = true
		}
		if p.Tool == "codex" && p.Name == "project-x" {
			foundProjectX = true
		}
	}

	assert.True(t, foundWork, "claude/work not found")
	assert.True(t, foundPersonal, "claude/personal not found")
	assert.True(t, foundProjectX, "codex/project-x not found")

	h.EndStep("Verify")
}

// TestLsCommand_FilterByTool tests `ls claude`
func TestLsCommand_FilterByTool(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	vaultDir := filepath.Join(h.TempDir, "vault")
	vault = authfile.NewVault(vaultDir)

	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "claude", "work"), 0755))
	require.NoError(t, os.MkdirAll(filepath.Join(vaultDir, "codex", "project-x"), 0755))

	// Reset flags
	lsCmd.Flags().Set("json", "true")

	// Capture stdout
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := runLs(lsCmd, []string{"claude"})

	w.Close()
	os.Stdout = oldStdout
	require.NoError(t, err)

	var buf bytes.Buffer
	io.Copy(&buf, r)

	var output lsOutput
	json.Unmarshal(buf.Bytes(), &output)

	assert.Equal(t, 1, output.Count)
	assert.Equal(t, "claude", output.Profiles[0].Tool)
	assert.Equal(t, "work", output.Profiles[0].Name)
}
