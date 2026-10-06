package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/fsnotify/fsnotify"
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
			"accessToken":      "test-access-token",
			"subscriptionType": "max",
			"accountId":        "acct_123",
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
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
			"accessToken":      "new-user-access-token",
			"subscriptionType": "max",
			"accountId":        "acct_456",
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
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
			"accessToken":      "initial-access-token",
			"subscriptionType": "max",
			"accountId":        "acct_789",
			"expiresAt":        time.Now().Add(time.Hour).UnixMilli(),
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

	// A real native refresh changes the token generation as well as expiry.
	// Expiry-only metadata edits do not represent a different credential.
	creds["claudeAiOauth"].(map[string]interface{})["accessToken"] = "rotated-access-token"
	creds["claudeAiOauth"].(map[string]interface{})["expiresAt"] = time.Now().Add(2 * time.Hour).UnixMilli()
	credsData, _ = json.Marshal(creds)
	require.NoError(t, os.WriteFile(credsPath, credsData, 0600))

	// Run WatchOnce again - should update
	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)

	// Should report the update
	assert.Len(t, discovered, 1)
	assert.Equal(t, "claude/existing@example.com", discovered[0])
}

func TestWatchOnce_AutoProfileWithoutIdentity(t *testing.T) {
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
	// Current Claude tokens may be opaque and carry no account identity.
	// That still permits a backup; malformed JSON does not.
	require.NoError(t, os.WriteFile(credsPath, []byte(`{"claudeAiOauth":{"accessToken":"opaque-without-identity"}}`), 0600))

	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)
	require.Len(t, discovered, 1)
	assert.True(t, strings.HasPrefix(discovered[0], "claude/auto-"))

	profiles, err := vault.List("claude")
	require.NoError(t, err)
	require.Len(t, profiles, 1)
	assert.True(t, strings.HasPrefix(profiles[0], "auto-"))
}

func TestWatchOnce_RejectsMalformedCredentials(t *testing.T) {
	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CAAM_KEYCHAIN", "0")
	require.NoError(t, os.MkdirAll(filepath.Join(homeDir, ".claude"), 0700))
	credsPath := filepath.Join(homeDir, ".claude", ".credentials.json")
	require.NoError(t, os.WriteFile(credsPath, []byte("{invalid"), 0600))
	vault := authfile.NewVault(filepath.Join(homeDir, "vault"))

	discovered, err := WatchOnce(vault, []string{"claude"}, nil)
	require.NoError(t, err)
	assert.Empty(t, discovered, "malformed auth cannot produce a discovered account")
	require.ErrorIs(t, vault.Backup(authfile.ClaudeAuthFiles(), "malformed"), authfile.ErrInvalidCredentials)
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	assert.Empty(t, profiles, "refused auth must not leave an empty auto profile")
}

func TestWatcher_AutoProfileWithoutIdentity(t *testing.T) {
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
	require.NoError(t, os.WriteFile(credsPath, []byte(`{"claudeAiOauth":{"accessToken":"opaque-without-identity"}}`), 0600))

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

	// Only the complete credential can produce a successful discovery.
	// Malformed snapshots are refused before creating an auto profile.
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
	assert.Len(t, discoveries, 1, "only the valid credential should be discovered")
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

func watcherFixture(t *testing.T) (*authfile.Vault, string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	require.NoError(t, os.MkdirAll(home, 0700))
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home, "CAAM_HOME": filepath.Join(root, "caam"), "CAAM_KEYCHAIN": "0",
		"XDG_CONFIG_HOME": filepath.Join(home, ".config"), "XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"CLAUDE_CONFIG_DIR": "", "CODEX_HOME": filepath.Join(home, ".codex"),
		"GEMINI_HOME": filepath.Join(home, ".gemini"), "GROK_HOME": filepath.Join(home, ".grok"),
		"CURSOR_CONFIG_DIR": filepath.Join(home, ".config", "cursor"), "APPDATA": filepath.Join(home, "AppData", "Roaming"),
	} {
		t.Setenv(key, value)
	}
	return authfile.NewVault(filepath.Join(root, "vault")), home
}

func writeWatcherFile(t *testing.T, path, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
}

func readWatcherFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return string(data)
}

func awaitWatcherDiscovery(t *testing.T, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		require.Equal(t, want, got)
	case <-time.After(5 * time.Second):
		t.Fatalf("watcher did not discover %s", want)
	}
}

func grokWatcherCredential(generation string) string {
	return fmt.Sprintf(`{"https://auth.example.test::client":{"key":%q,"refresh_token":%q,"expires_at":"2030-01-01T00:00:00Z","user_id":"grok-account","email":"grok@example.test"}}`, "access-"+generation, "refresh-"+generation)
}

func claudeWatcherCredential(generation string) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":1893456000000}}`, "access-"+generation, "refresh-"+generation)
}

func TestWatcherClaudeExplicitDirectoryOwnsCredentialsAndIdentity(t *testing.T) {
	vault, home := watcherFixture(t)
	configured := filepath.Join(home, "custom-claude")
	t.Setenv("CLAUDE_CONFIG_DIR", configured)
	writeWatcherFile(t, filepath.Join(home, ".claude", ".credentials.json"), claudeWatcherCredential("ignored"))
	writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"ignored","emailAddress":"ignored@example.test"}}`)
	events := make(chan string, 4)
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"claude"}, DebounceInterval: 20 * time.Millisecond, PollInterval: time.Hour,
		OnDiscovery: func(provider, name string, _ *identity.Identity) { events <- provider + "/" + name },
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	defer watcher.Stop()
	writeWatcherFile(t, filepath.Join(configured, ".claude.json"), `{"oauthAccount":{"accountUuid":"selected","emailAddress":"selected@example.test"}}`)
	writeWatcherFile(t, filepath.Join(configured, ".credentials.json"), claudeWatcherCredential("selected"))
	awaitWatcherDiscovery(t, events, "claude/selected@example.test")
	require.Equal(t, claudeWatcherCredential("selected"), readWatcherFile(t, vault.BackupPath("claude", "selected@example.test", ".credentials.json")))
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	require.Equal(t, []string{"selected@example.test"}, profiles)
}

func quietWatcherConfig(providers []string, events chan<- string) WatcherConfig {
	return WatcherConfig{Providers: providers, DebounceInterval: 20 * time.Millisecond, PollInterval: 30 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			events <- provider + "/" + profile
		}}
}

func TestWatcherRoutesSharedBasenamesToTheirExactProviders(t *testing.T) {
	vault, home := watcherFixture(t)
	providers := []string{"claude", "codex", "gemini", "grok", "opencode", "cursor"}
	for _, provider := range providers {
		fileSet, _ := authfile.GetAuthFileSet(provider)
		for _, spec := range fileSet.Files {
			require.NoError(t, os.MkdirAll(filepath.Dir(spec.Path), 0700))
		}
	}
	events := make(chan string, 32)
	cfg := quietWatcherConfig(nil, events)
	cfg.PollInterval = time.Hour // Only real asynchronous notifications can discover these writes.
	watcher, err := NewWatcher(vault, cfg)
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })

	writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"claude-account","emailAddress":"claude@example.test"}}`)
	writeWatcherFile(t, filepath.Join(home, ".claude", ".credentials.json"), claudeWatcherCredential("one"))
	codex, err := os.ReadFile("testdata/codex_initial_login.json")
	require.NoError(t, err)
	writeWatcherFile(t, filepath.Join(home, ".codex", "auth.json"), string(codex))
	writeWatcherFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"type":"authorized_user","email":"gemini@example.test","refresh_token":"synthetic-gemini-refresh"}`)
	writeWatcherFile(t, filepath.Join(home, ".grok", "auth.json"), grokWatcherCredential("one"))
	openCode := authfile.OpenCodeAuthFiles().Files[0].Path
	writeWatcherFile(t, openCode, `{"access_token":"synthetic-opencode-access","email":"opencode@example.test"}`)
	cursorPaths := authfile.ResolveCursorPaths(home, runtime.GOOS, os.Getenv)
	writeWatcherFile(t, cursorPaths.AuthFile, `{"accessToken":"synthetic-cursor-session","refreshToken":"synthetic-cursor-refresh","email":"cursor@example.test"}`)

	want := map[string]bool{"claude/claude@example.test": true, "codex/codex-user@example.com": true, "gemini/gemini@example.test": true,
		"grok/grok@example.test": true, "opencode/opencode@example.test": true, "cursor/cursor@example.test": true}
	for range len(want) {
		select {
		case got := <-events:
			require.True(t, want[got], "duplicate or misrouted discovery: %s", got)
			delete(want, got)
		case <-time.After(5 * time.Second):
			t.Fatalf("missing asynchronous discoveries: %v", want)
		}
	}
	writeWatcherFile(t, filepath.Join(cursorPaths.ConfigDir, "cli-config.json"), `{"theme":"changed","authInfo":{"email":"stale-config@example.test"}}`)
	writeWatcherFile(t, filepath.Join(home, ".cursor", "settings.json"), `{"permissions":{"allow":["Read"]}}`)
	require.Never(t, func() bool { return len(events) != 0 }, 150*time.Millisecond, 10*time.Millisecond, "Cursor config churn must not replace its auth.json identity")
	require.NoError(t, watcher.Stop())
	for _, provider := range providers {
		profiles, err := vault.List(provider)
		require.NoError(t, err)
		require.Len(t, profiles, 1, "wrong provider snapshot count for %s", provider)
	}
	require.Equal(t, grokWatcherCredential("one"), readWatcherFile(t, vault.BackupPath("grok", "grok@example.test", "auth.json")))
	require.Contains(t, readWatcherFile(t, vault.BackupPath("cursor", "cursor@example.test", "auth.json")), "synthetic-cursor-session")
}

func TestWatchOnceSavesModernClaudeRotationWithoutDuplicatingAliases(t *testing.T) {
	for _, profile := range []string{"claude@example.test", "work"} {
		t.Run(profile, func(t *testing.T) {
			vault, home := watcherFixture(t)
			auth := filepath.Join(home, ".claude", ".credentials.json")
			writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"claude-account","emailAddress":"claude@example.test"}}`)
			writeWatcherFile(t, auth, claudeWatcherCredential("old"))
			fileSet := authfile.ClaudeAuthFiles()
			require.NoError(t, vault.Backup(fileSet, profile))
			writeWatcherFile(t, auth, claudeWatcherCredential("rotated"))
			active, err := vault.ActiveProfile(fileSet)
			require.NoError(t, err)
			require.Equal(t, profile, active, "the account must match despite changed token bytes")
			discovered, err := WatchOnce(vault, []string{"claude"}, nil)
			require.NoError(t, err)
			require.Equal(t, []string{"claude/" + profile}, discovered)
			require.Equal(t, claudeWatcherCredential("rotated"), readWatcherFile(t, vault.BackupPath("claude", profile, ".credentials.json")))
			profiles, err := vault.List("claude")
			require.NoError(t, err)
			require.Equal(t, []string{profile}, profiles)

			before := readWatcherFile(t, vault.BackupPath("claude", profile, "meta.json"))
			writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"claude-account","emailAddress":"claude@example.test"},"numStartups":900}`)
			writeWatcherFile(t, filepath.Join(home, ".claude", "settings.json"), `{"permissions":{"allow":["Read"]},"model":"current"}`)
			discovered, err = WatchOnce(vault, []string{"claude"}, nil)
			require.NoError(t, err)
			require.Empty(t, discovered, "shared policy churn must not trigger another snapshot")
			require.Equal(t, before, readWatcherFile(t, vault.BackupPath("claude", profile, "meta.json")))
		})
	}
}

func TestWatcherRotatesClaudeAndIgnoresOptionalConfigChurn(t *testing.T) {
	vault, home := watcherFixture(t)
	auth := filepath.Join(home, ".claude", ".credentials.json")
	writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"claude-account","emailAddress":"claude@example.test"}}`)
	writeWatcherFile(t, auth, claudeWatcherCredential("old"))
	require.NoError(t, vault.Backup(authfile.ClaudeAuthFiles(), "work"))
	events := make(chan string, 32)
	watcher, err := NewWatcher(vault, quietWatcherConfig([]string{"claude"}, events))
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	writeWatcherFile(t, auth+".new", claudeWatcherCredential("rotated"))
	require.NoError(t, os.Rename(auth+".new", auth))
	awaitWatcherDiscovery(t, events, "claude/work")
	require.Equal(t, claudeWatcherCredential("rotated"), readWatcherFile(t, vault.BackupPath("claude", "work", ".credentials.json")))
	writeWatcherFile(t, filepath.Join(home, ".claude", "settings.json"), `{"hooks":{"Stop":[]},"model":"new-preference"}`)
	writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"claude-account","emailAddress":"claude@example.test"},"numStartups":3}`)
	require.Never(t, func() bool { return len(events) != 0 }, 180*time.Millisecond, 10*time.Millisecond, "optional config churn generated a backup")
	require.NoError(t, watcher.Stop())
	_, err = os.Stat(vault.BackupPath("claude", "work", "settings.json"))
	require.True(t, os.IsNotExist(err), "workflow-only edits should not have rewritten the snapshot")
}

func TestWatchOnceOptionalOnlyCredentialsIgnoreSharedPolicy(t *testing.T) {
	for _, provider := range []string{"claude", "cursor"} {
		t.Run(provider, func(t *testing.T) {
			vault, home := watcherFixture(t)
			path := filepath.Join(home, ".claude", "settings.json")
			initial, updated := `{"apiKeyHelper":"synthetic-helper","model":"initial"}`, `{"apiKeyHelper":"synthetic-helper","model":"changed","permissions":{"allow":["Read"]}}`
			if provider == "cursor" {
				path = filepath.Join(os.Getenv("CURSOR_CONFIG_DIR"), "cli-config.json")
				initial, updated = `{"authInfo":{"email":"keychain@example.test"},"theme":"initial"}`, `{"authInfo":{"email":"keychain@example.test"},"theme":"changed"}`
			}
			writeWatcherFile(t, path, initial)
			first, err := WatchOnce(vault, []string{provider}, nil)
			require.NoError(t, err)
			require.Len(t, first, 1)
			profiles, err := vault.List(provider)
			require.NoError(t, err)
			require.Len(t, profiles, 1)
			snapshot := vault.BackupPath(provider, profiles[0], filepath.Base(path))
			before := readWatcherFile(t, snapshot)
			writeWatcherFile(t, path, updated)
			discovered, err := WatchOnce(vault, []string{provider}, nil)
			require.NoError(t, err)
			require.Empty(t, discovered, "optional-only auth must not generate new snapshots on policy changes")
			require.Equal(t, before, readWatcherFile(t, snapshot))
			after, err := vault.List(provider)
			require.NoError(t, err)
			require.Equal(t, profiles, after)
		})
	}
}

func TestWatcherPollingWorksWithoutBackendAndAfterDirectoryReplacement(t *testing.T) {
	vault, home := watcherFixture(t)
	events := make(chan string, 32)
	watcher, err := NewWatcher(vault, quietWatcherConfig([]string{"grok"}, events))
	require.NoError(t, err)
	watcher.newBackend = func() (*fsnotify.Watcher, error) { return nil, errors.New("synthetic backend unavailable") }
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	dir := filepath.Join(home, ".grok")
	auth := filepath.Join(dir, "auth.json")
	writeWatcherFile(t, auth, grokWatcherCredential("first"))
	awaitWatcherDiscovery(t, events, "grok/grok@example.test")
	require.NoError(t, os.Rename(dir, filepath.Join(home, "previous-grok")))
	writeWatcherFile(t, auth, grokWatcherCredential("replacement"))
	awaitWatcherDiscovery(t, events, "grok/grok@example.test")
	require.Equal(t, grokWatcherCredential("replacement"), readWatcherFile(t, vault.BackupPath("grok", "grok@example.test", "auth.json")))
	writeWatcherFile(t, filepath.Join(dir, "config.toml"), "model = 'changed-preference'\n")
	require.Never(t, func() bool { return len(events) != 0 }, 150*time.Millisecond, 10*time.Millisecond)
	profiles, err := vault.List("grok")
	require.NoError(t, err)
	require.Equal(t, []string{"grok@example.test"}, profiles)
}

func TestWatcherReopensClosedBackend(t *testing.T) {
	vault, home := watcherFixture(t)
	dir := filepath.Join(home, ".grok")
	require.NoError(t, os.MkdirAll(dir, 0700))
	events := make(chan string, 32)
	backends := make(chan *fsnotify.Watcher, 4)
	watcher, err := NewWatcher(vault, quietWatcherConfig([]string{"grok"}, events))
	require.NoError(t, err)
	watcher.newBackend = func() (*fsnotify.Watcher, error) {
		backend, err := fsnotify.NewWatcher()
		if err == nil {
			backends <- backend
		}
		return backend, err
	}
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	first := <-backends
	require.NoError(t, first.Close())
	writeWatcherFile(t, filepath.Join(dir, "auth.json"), grokWatcherCredential("recovered"))
	awaitWatcherDiscovery(t, events, "grok/grok@example.test")
	select {
	case next := <-backends:
		require.NotSame(t, first, next)
		require.Contains(t, next.WatchList(), dir)
	case <-time.After(5 * time.Second):
		t.Fatal("notification backend was not recreated")
	}
}

func TestWatcherReattachesReplacedNativeDirectory(t *testing.T) {
	vault, home := watcherFixture(t)
	dir := filepath.Join(home, ".grok")
	require.NoError(t, os.MkdirAll(dir, 0700))
	events := make(chan string, 32)
	cfg := quietWatcherConfig([]string{"grok"}, events)
	cfg.PollInterval = time.Hour
	watcher, err := NewWatcher(vault, cfg)
	require.NoError(t, err)
	var backend *fsnotify.Watcher
	watcher.newBackend = func() (*fsnotify.Watcher, error) {
		var err error
		backend, err = fsnotify.NewWatcher()
		return backend, err
	}
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	writeWatcherFile(t, filepath.Join(dir, "auth.json"), grokWatcherCredential("first"))
	awaitWatcherDiscovery(t, events, "grok/grok@example.test")
	require.NoError(t, os.Rename(dir, filepath.Join(home, "previous-grok")))
	writeWatcherFile(t, filepath.Join(dir, "auth.json"), grokWatcherCredential("replacement"))
	awaitWatcherDiscovery(t, events, "grok/grok@example.test")
	require.Contains(t, backend.WatchList(), dir, "replacement reconciliation must reattach the native directory before reporting discovery")
	writeWatcherFile(t, filepath.Join(dir, "auth.json"), grokWatcherCredential("after-reattach"))
	awaitWatcherDiscovery(t, events, "grok/grok@example.test")
	require.Equal(t, grokWatcherCredential("after-reattach"), readWatcherFile(t, vault.BackupPath("grok", "grok@example.test", "auth.json")))
}

func TestWatcherStopStartUsesFreshBackendAndWaitsForWrites(t *testing.T) {
	vault, home := watcherFixture(t)
	dir := filepath.Join(home, ".grok")
	require.NoError(t, os.MkdirAll(dir, 0700))
	events := make(chan string, 32)
	cfg := quietWatcherConfig([]string{"grok"}, events)
	cfg.PollInterval = time.Hour
	watcher, err := NewWatcher(vault, cfg)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	for _, generation := range []string{"first-run", "second-run"} {
		require.NoError(t, watcher.Start(context.Background()))
		writeWatcherFile(t, filepath.Join(dir, "auth.json"), grokWatcherCredential(generation))
		awaitWatcherDiscovery(t, events, "grok/grok@example.test")
		var group sync.WaitGroup
		for range 4 {
			group.Go(func() { assert.NoError(t, watcher.Stop()) })
		}
		group.Wait()
		require.Equal(t, grokWatcherCredential(generation), readWatcherFile(t, vault.BackupPath("grok", "grok@example.test", "auth.json")))
	}
	writeWatcherFile(t, filepath.Join(dir, "auth.json"), grokWatcherCredential("after-stop"))
	require.Never(t, func() bool { return len(events) != 0 }, 100*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, grokWatcherCredential("second-run"), readWatcherFile(t, vault.BackupPath("grok", "grok@example.test", "auth.json")))
}
