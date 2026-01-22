package discovery

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

func TestWatchOnce_AutoProfileOnIdentityError(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, []byte("{invalid"), 0600))

	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	assert.True(t, strings.HasPrefix(discovered[0], "claude/auto-"))

	profiles, err := vault.List("claude")
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.True(t, strings.HasPrefix(profiles[0], "auto-"))
}

func TestWatcher_AutoProfileOnIdentityError(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

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

	time.Sleep(200 * time.Millisecond)

	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, []byte("{invalid"), 0600))

	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, discoveries, 1)
	assert.True(t, strings.HasPrefix(discoveries[0], "claude/auto-"))
}

// E2E Tests for realistic auth-file change sequences

// TestE2E_RapidFileChanges verifies that rapid file changes are debounced
// into a single backup event, preventing duplicate profiles.
func TestE2E_RapidFileChanges(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	var mu sync.Mutex
	var discoveryCount int

	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers:        []string{"claude"},
		DebounceInterval: 200 * time.Millisecond,
		OnDiscovery: func(provider, email string, ident *identity.Identity) {
			mu.Lock()
			discoveryCount++
			mu.Unlock()
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, watcher.Start(ctx))
	defer watcher.Stop()

	time.Sleep(200 * time.Millisecond)

	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")

	// Simulate rapid writes (like a token refresh writing multiple times)
	for i := 0; i < 5; i++ {
		creds := map[string]interface{}{
			"claudeAiOauth": map[string]interface{}{
				"accessToken":      "token-" + string(rune('A'+i)),
				"refreshToken":     "refresh-token",
				"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
				"subscriptionType": "claude_pro_2025",
			},
		}
		data, _ := json.Marshal(creds)
		require.NoError(t, os.WriteFile(credsPath, data, 0600))
		time.Sleep(20 * time.Millisecond) // Quick succession
	}

	// Wait for debounce to complete
	time.Sleep(600 * time.Millisecond)

	mu.Lock()
	count := discoveryCount
	mu.Unlock()

	// Should only trigger one discovery due to debouncing
	assert.Equal(t, 1, count, "rapid writes should be debounced to single discovery")

	// Verify only one profile was created
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	assert.Len(t, profiles, 1, "should have exactly one profile after rapid writes")
}

// TestE2E_RepeatDetection verifies that writing the same credentials
// twice doesn't create duplicate profiles.
func TestE2E_RepeatDetection(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	// Initial credentials with email (legacy format for test)
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "repeat@example.com",
			"accessToken":      "token-A",
			"refreshToken":     "refresh-A",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
			"subscriptionType": "max",
			"accountId":        "acct_repeat",
		},
	}
	credsData, _ := json.Marshal(creds)
	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// First discovery
	discovered1, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)
	assert.Len(t, discovered1, 1)

	profiles1, _ := vault.List("claude")
	assert.Len(t, profiles1, 1)

	// Write exact same credentials again
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// Second discovery - should detect already active
	discovered2, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	// Should not report as new discovery since content matches active profile
	assert.Empty(t, discovered2, "identical credentials should not be reported as new")

	// Should still have only one profile
	profiles2, _ := vault.List("claude")
	assert.Len(t, profiles2, 1, "should not create duplicate profiles")
}

// TestE2E_ClaudeCurrentFormatAutoProfile verifies that Claude's current
// auth format (without email/accountId) generates auto-named profiles.
func TestE2E_ClaudeCurrentFormatAutoProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	// Claude current format - no email or accountId
	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	fixtureData, err := os.ReadFile("testdata/claude_initial_login.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(credsPath, fixtureData, 0600))

	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	require.Len(t, discovered, 1)
	assert.True(t, strings.HasPrefix(discovered[0], "claude/auto-"),
		"Claude current format should generate auto-profile, got: %s", discovered[0])

	profiles, _ := vault.List("claude")
	require.Len(t, profiles, 1)
	assert.True(t, strings.HasPrefix(profiles[0], "auto-"),
		"profile name should be auto-generated")
}

// TestE2E_AccountSwitchDetection verifies that switching to different
// credentials creates separate profiles.
func TestE2E_AccountSwitchDetection(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")

	// First account
	creds1 := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "account1@example.com",
			"accessToken":      "token-account1",
			"refreshToken":     "refresh-account1",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
			"subscriptionType": "pro",
			"accountId":        "acct_001",
		},
	}
	data1, _ := json.Marshal(creds1)
	require.NoError(t, os.WriteFile(credsPath, data1, 0600))

	discovered1, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)
	assert.Len(t, discovered1, 1)
	assert.Equal(t, "claude/account1@example.com", discovered1[0])

	// Switch to second account
	creds2 := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "account2@example.com",
			"accessToken":      "token-account2",
			"refreshToken":     "refresh-account2",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
			"subscriptionType": "max",
			"accountId":        "acct_002",
		},
	}
	data2, _ := json.Marshal(creds2)
	require.NoError(t, os.WriteFile(credsPath, data2, 0600))

	discovered2, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)
	assert.Len(t, discovered2, 1)
	assert.Equal(t, "claude/account2@example.com", discovered2[0])

	// Should have two separate profiles
	profiles, _ := vault.List("claude")
	assert.Len(t, profiles, 2)
	assert.Contains(t, profiles, "account1@example.com")
	assert.Contains(t, profiles, "account2@example.com")
}

// TestE2E_PartialWriteRecovery verifies that partial/corrupted writes
// don't cause issues and are handled gracefully.
func TestE2E_PartialWriteRecovery(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	var mu sync.Mutex
	var discoveries []string
	var errors []error

	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers:        []string{"claude"},
		DebounceInterval: 100 * time.Millisecond,
		OnDiscovery: func(provider, email string, ident *identity.Identity) {
			mu.Lock()
			discoveries = append(discoveries, provider+"/"+email)
			mu.Unlock()
		},
		OnError: func(err error) {
			mu.Lock()
			errors = append(errors, err)
			mu.Unlock()
		},
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	require.NoError(t, watcher.Start(ctx))
	defer watcher.Stop()

	time.Sleep(200 * time.Millisecond)

	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")

	// Write partial/corrupted JSON (simulating interrupted write)
	fixtureData, err := os.ReadFile("testdata/partial_write.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(credsPath, fixtureData, 0600))

	time.Sleep(400 * time.Millisecond)

	// Now write valid credentials
	validCreds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken":      "valid-token",
			"refreshToken":     "valid-refresh",
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
			"subscriptionType": "claude_pro_2025",
		},
	}
	validData, _ := json.Marshal(validCreds)
	require.NoError(t, os.WriteFile(credsPath, validData, 0600))

	time.Sleep(400 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()

	// Should have at least one successful discovery (the valid one)
	// The partial write might create an auto-profile or be skipped
	assert.GreaterOrEqual(t, len(discoveries), 1, "should have at least one discovery")
}

// TestE2E_MultiProviderDiscovery verifies that watch mode can detect
// credentials from multiple providers in a single session.
func TestE2E_MultiProviderDiscovery(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".codex"), 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".gemini"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	// Create Claude credentials
	claudeCreds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "claude@example.com",
			"accessToken":      "claude-token",
			"subscriptionType": "max",
			"accountId":        "acct_claude",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
		},
	}
	claudeData, _ := json.Marshal(claudeCreds)
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".claude", ".credentials.json"), claudeData, 0600))

	// Create Codex credentials (using fixture)
	codexData, err := os.ReadFile("testdata/codex_initial_login.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".codex", "auth.json"), codexData, 0600))

	// Create Gemini credentials (using fixture)
	geminiData, err := os.ReadFile("testdata/gemini_initial_login.json")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".gemini", "settings.json"), geminiData, 0600))

	// Discover all providers
	discovered, err := WatchOnce(vault, []string{"claude", "codex", "gemini"}, nil)
	require.NoError(t, err)

	// Should find accounts from all three providers
	assert.Len(t, discovered, 3, "should discover accounts from all three providers")

	var foundClaude, foundCodex, foundGemini bool
	for _, d := range discovered {
		if strings.HasPrefix(d, "claude/") {
			foundClaude = true
		}
		if strings.HasPrefix(d, "codex/") {
			foundCodex = true
		}
		if strings.HasPrefix(d, "gemini/") {
			foundGemini = true
		}
	}
	assert.True(t, foundClaude, "should discover Claude account")
	assert.True(t, foundCodex, "should discover Codex account")
	assert.True(t, foundGemini, "should discover Gemini account")
}

// TestE2E_TokenRefreshNoNewProfile verifies that token refresh
// (same account, new token) updates existing profile without creating new one.
func TestE2E_TokenRefreshNoNewProfile(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	homeDir := filepath.Join(tmpDir, "home")

	require.NoError(t, os.MkdirAll(vaultDir, 0700))
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))

	vault := authfile.NewVault(vaultDir)

	origHome := os.Getenv("HOME")
	t.Setenv("HOME", homeDir)
	defer func() {
		if origHome != "" {
			os.Setenv("HOME", origHome)
		}
	}()

	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")

	// Initial login
	creds := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"email":            "refresh@example.com",
			"accessToken":      "initial-token",
			"refreshToken":     "refresh-token",
			"expiresAt":        time.Now().Add(time.Hour).Unix(),
			"subscriptionType": "pro",
			"accountId":        "acct_refresh",
		},
	}
	data, _ := json.Marshal(creds)
	require.NoError(t, os.WriteFile(credsPath, data, 0600))

	_, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	profiles1, _ := vault.List("claude")
	assert.Len(t, profiles1, 1)

	// Simulate token refresh (new access token, same account)
	creds["claudeAiOauth"].(map[string]interface{})["accessToken"] = "refreshed-token"
	creds["claudeAiOauth"].(map[string]interface{})["expiresAt"] = time.Now().Add(2 * time.Hour).Unix()
	data, _ = json.Marshal(creds)
	require.NoError(t, os.WriteFile(credsPath, data, 0600))

	// This should update, not create new
	_, err = WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	profiles2, _ := vault.List("claude")
	assert.Len(t, profiles2, 1, "token refresh should not create new profile")
	assert.Contains(t, profiles2, "refresh@example.com")
}
