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

	// The watch context must comfortably outlive the discovery poll below —
	// if it expires first, the watcher goes deaf mid-test.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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

	// Wait for debounce and processing. Poll instead of a single fixed sleep:
	// under heavy CPU load the fsnotify delivery plus the 100ms debounce can
	// exceed a fixed 500ms window, failing the test even though discovery
	// works (same flake class as the daemon run-loop tests, deflaked the same
	// way). Polling to a generous deadline preserves the test's intent.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(discoveries)
		mu.Unlock()
		if n > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()

	// require (not assert): indexing discoveries[0] after a non-fatal length
	// failure panicked, and the panic used to wedge the whole package via the
	// deferred watcher.Stop() deadlock (now fixed in eventLoop/Stop).
	require.Len(t, discoveries, 1)
	assert.Equal(t, "claude/newuser@example.com", discoveries[0])
}

// TestWatcher_StopAfterContextExpiry is a regression test for a Stop()
// deadlock: eventLoop closed doneCh only when it exited via stopCh, so if the
// watch context was cancelled (or expired) first, eventLoop returned without
// signalling and a subsequent Stop() blocked forever on <-doneCh.
func TestWatcher_StopAfterContextExpiry(t *testing.T) {
	tmpDir := t.TempDir()
	homeDir := filepath.Join(tmpDir, "home")
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))
	t.Setenv("HOME", homeDir)

	vault := authfile.NewVault(filepath.Join(tmpDir, "vault"))
	watcher, err := NewWatcher(vault, WatcherConfig{Providers: []string{"claude"}})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, watcher.Start(ctx))

	// Kill the context first, give the loops a moment to exit via ctx.Done(),
	// THEN Stop. Before the fix this deadlocked.
	cancel()
	time.Sleep(50 * time.Millisecond)

	done := make(chan error, 1)
	go func() { done <- watcher.Stop() }()
	select {
	case stopErr := <-done:
		require.NoError(t, stopErr)
	case <-time.After(10 * time.Second):
		t.Fatal("watcher.Stop() deadlocked after context cancellation")
	}
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

	// Poll instead of a fixed sleep: under heavy machine load the fsnotify
	// event + debounce interval can take well over 500ms to fire.
	deadline := time.Now().Add(4 * time.Second)
	for {
		mu.Lock()
		n := len(discoveries)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

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
		time.Sleep(5 * time.Millisecond) // Quick succession, well inside the debounce window
	}

	// Wait for the debounced discovery. Poll instead of a fixed sleep: under
	// heavy machine load the fsnotify event + debounce interval can take well
	// over 600ms to fire. After the first discovery, wait one more full
	// debounce interval of quiet so a spurious second discovery would be seen.
	deadline := time.Now().Add(4 * time.Second)
	for {
		mu.Lock()
		n := discoveryCount
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(400 * time.Millisecond)

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

	// Should have at least one successful discovery (the valid one).
	// The partial write might create an auto-profile or be skipped.
	// Poll instead of a fixed sleep: under heavy machine load the fsnotify
	// event + debounce interval can take well over 400ms to fire.
	deadline := time.Now().Add(4 * time.Second)
	for {
		mu.Lock()
		n := len(discoveries)
		mu.Unlock()
		if n >= 1 || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
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
