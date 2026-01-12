package discovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatchOnce(t *testing.T) {
	// Create temp directories
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	// Create mock Claude credentials
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "test@example.com",
			"subscriptionType": "max",
			"accountId":        "acct_123",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
		},
	}
	credsData, _ := json.Marshal(creds)
	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// Override HOME for test
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	// Run WatchOnce for claude only
	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	assert.Len(t, discovered, 1)
	assert.Equal(t, "claude/test@example.com", discovered[0])

	// Verify profile was created
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	assert.Contains(t, profiles, "test@example.com")
}

func TestWatcher_Discovery(t *testing.T) {
	// Create temp directories
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	// Override HOME for test
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	// Track discoveries
	var mu sync.Mutex
	var discoveries []string

	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers:        []string{"claude"},
		DebounceInterval: 100 * time.Millisecond,
		OnDiscovery: func(provider, email string, ident *identity.Identity) {
			mu.Lock()
			discoveries = append(discoveries, provider+"/"+email)
			mu.Unlock()
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, watcher.Start(ctx))
	defer watcher.Stop()

	// Give watcher time to set up
	time.Sleep(200 * time.Millisecond)

	// Create credentials file (simulating login)
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "newuser@example.com",
			"subscriptionType": "max",
			"accountId":        "acct_456",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
		},
	}
	credsData, _ := json.Marshal(creds)
	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// Wait for debounce and processing
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	assert.Len(t, discoveries, 1)
	assert.Equal(t, "claude/newuser@example.com", discoveries[0])
}

func TestWatcher_UpdateExisting(t *testing.T) {
	// Create temp directories
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	// Override HOME for test
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	// Create initial credentials
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "existing@example.com",
			"subscriptionType": "max",
			"accountId":        "acct_789",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
		},
	}
	credsData, _ := json.Marshal(creds)
	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// Run WatchOnce to create initial profile
	_, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	// Verify profile exists
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	assert.Contains(t, profiles, "existing@example.com")

	// Update credentials (new expiry)
	creds["claudeAiOauth"].(map[string]interface{})["expiresAt"] = time.Now().Add(2 * time.Hour).Unix()
	credsData, _ = json.Marshal(creds)
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// Run WatchOnce again - should update
	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	// Should report the update
	assert.Len(t, discovered, 1)
	assert.Equal(t, "claude/existing@example.com", discovered[0])
}
