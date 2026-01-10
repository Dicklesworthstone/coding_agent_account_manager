package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

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

