package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTagCommands_Extended(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// 1. Setup
	h.StartStep("Setup", "Initialize profile store and create fixture")
	
	rootDir := h.TempDir
	// Set env vars so DefaultStorePath uses our temp dir
	h.SetEnv("XDG_DATA_HOME", rootDir)
	
	// Initialize store
	storePath := filepath.Join(rootDir, "caam", "profiles")
	require.NoError(t, os.MkdirAll(storePath, 0755))
	
	// Override global profileStore
	originalStore := profileStore
	defer func() {
		profileStore = originalStore
		// Reset flags that may have been modified during tests
		tagListCmd.Flags().Set("json", "false")
	}()
	profileStore = profile.NewStore(storePath)
	
	// Create a test profile
	prof, err := profileStore.Create("claude", "work", "oauth")
	require.NoError(t, err)
	prof.Save()
	
	// Create another profile for 'all' command test
	prof2, err := profileStore.Create("claude", "personal", "oauth")
	require.NoError(t, err)
	prof2.AddTag("home")
	prof2.Save()
	
	h.EndStep("Setup")
	
	// 2. Test Tag Add
	h.StartStep("Add", "Add tags to profile")
	
	output, err := captureStdout(t, func() error {
		return runTagAdd(tagAddCmd, []string{"claude", "work", "project-x", "urgent"})
	})
	require.NoError(t, err)
	
	assert.Contains(t, output, "Added 2 tags")
	
	// Verify persistence
	loaded, err := profileStore.Load("claude", "work")
	require.NoError(t, err)
	assert.Contains(t, loaded.Tags, "project-x")
	assert.Contains(t, loaded.Tags, "urgent")
	
	h.EndStep("Add")
	
	// 3. Test Tag List (JSON)
	h.StartStep("List", "List tags as JSON")
	
	tagListCmd.Flags().Set("json", "true")
	output, err = captureStdout(t, func() error {
		return runTagList(tagListCmd, []string{"claude", "work"})
	})
	require.NoError(t, err)
	
	var listOut struct {
		Tags []string `json:"tags"`
	}
	err = json.Unmarshal([]byte(output), &listOut)
	require.NoError(t, err)
	assert.Len(t, listOut.Tags, 2)
	assert.Contains(t, listOut.Tags, "project-x")
	
	h.EndStep("List")
	
	// 4. Test Tag Remove
	h.StartStep("Remove", "Remove tag from profile")
	
	output, err = captureStdout(t, func() error {
		return runTagRemove(tagRemoveCmd, []string{"claude", "work", "urgent"})
	})
	require.NoError(t, err)
	
	assert.Contains(t, output, "Removed 1 tag")
	
	loaded, err = profileStore.Load("claude", "work")
	require.NoError(t, err)
	assert.NotContains(t, loaded.Tags, "urgent")
	assert.Contains(t, loaded.Tags, "project-x")
	
	h.EndStep("Remove")
	
	// 5. Test Tag All
	h.StartStep("All", "List all tags for provider")
	
	// work has "project-x", personal has "home"
	output, err = captureStdout(t, func() error {
		return runTagAll(tagAllCmd, []string{"claude"})
	})
	require.NoError(t, err)
	
	assert.Contains(t, output, "project-x")
	assert.Contains(t, output, "home")
	
	h.EndStep("All")
	
	// 6. Test Tag Clear
	h.StartStep("Clear", "Clear tags from profile")
	
	output, err = captureStdout(t, func() error {
		return runTagClear(tagClearCmd, []string{"claude", "work"})
	})
	require.NoError(t, err)
	
	assert.Contains(t, output, "Cleared 1 tag")
	
	loaded, err = profileStore.Load("claude", "work")
	require.NoError(t, err)
	assert.Empty(t, loaded.Tags)
	
	h.EndStep("Clear")
}


func TestTagAndDescribeVaultOnlyProfile(t *testing.T) {
	root := t.TempDir()
	origVault, origStore := vault, profileStore
	t.Cleanup(func() {
		vault, profileStore = origVault, origStore
		lsCmd.Flags().Set("tag", "")
		lsCmd.Flags().Set("json", "false")
	})
	vault = authfile.NewVault(filepath.Join(root, "vault"))
	profileStore = profile.NewStore(filepath.Join(root, "profiles"))

	for _, name := range []string{"work", "home"} {
		dir := filepath.Join(root, "vault", "codex", name)
		require.NoError(t, os.MkdirAll(dir, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{"access_token":"a","refresh_token":"r-`+name+`"}}`), 0o600))
	}

	// The vault profile has no isolated counterpart; tagging still works.
	_, err := captureStdout(t, func() error {
		return runTagAdd(tagAddCmd, []string{"codex", "work", "client-a"})
	})
	require.NoError(t, err)
	_, err = captureStdout(t, func() error {
		return profileDescribeCmd.RunE(profileDescribeCmd, []string{"codex", "work", "Client A seat"})
	})
	require.NoError(t, err)

	labels, err := vault.Labels("codex", "work")
	require.NoError(t, err)
	assert.Equal(t, []string{"client-a"}, labels.Tags)
	assert.Equal(t, "Client A seat", labels.Description)

	require.NoError(t, lsCmd.Flags().Set("tag", "client-a"))
	require.NoError(t, lsCmd.Flags().Set("json", "true"))
	var buf bytes.Buffer
	lsCmd.SetOut(&buf)
	t.Cleanup(func() { lsCmd.SetOut(nil) })
	require.NoError(t, runLs(lsCmd, []string{"codex"}))
	out := buf.String()
	var listed struct {
		Profiles []struct {
			Name        string   `json:"name"`
			Tags        []string `json:"tags"`
			Description string   `json:"description"`
		} `json:"profiles"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &listed), out)
	require.Len(t, listed.Profiles, 1, "ls --tag must match the tagged vault profile only")
	assert.Equal(t, "work", listed.Profiles[0].Name)
	assert.Equal(t, []string{"client-a"}, listed.Profiles[0].Tags)
	assert.Equal(t, "Client A seat", listed.Profiles[0].Description)

	// Unknown names are still rejected.
	_, err = captureStdout(t, func() error {
		return runTagAdd(tagAddCmd, []string{"codex", "nope", "x"})
	})
	require.Error(t, err)
}
