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
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keychain"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
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
			"refreshToken":     "synthetic-refresh-token",
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
	require.ErrorIs(t, err, authfile.ErrInvalidCredentials)
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
	require.NoError(t, os.WriteFile(filepath.Join(homeDir, ".gemini", "oauth_creds.json"), geminiData, 0600))

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
	return fmt.Sprintf(`{"https://auth.example.test::client":{"key":%q,"refresh_token":%q,"expires_at":%q,"user_id":"grok-account","email":"grok@example.test"}}`, "access-"+generation, "refresh-"+generation, watcherGenerationTime(generation).Format(time.RFC3339))
}

func claudeWatcherCredential(generation string) string {
	// An opaque token can prove continuity through an unchanged refresh token.
	// A strictly later expiry proves this is a new generation worth saving.
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"synthetic-stable-claude-refresh","expiresAt":%d}}`, "access-"+generation, watcherGenerationTime(generation).UnixMilli())
}

func watcherGenerationTime(generation string) time.Time {
	base := time.Date(2030, time.January, 1, 0, 0, 0, 0, time.UTC)
	switch generation {
	case "rotated", "replacement", "second-run":
		return base.Add(time.Hour)
	case "after-reattach", "after-stop":
		return base.Add(2 * time.Hour)
	default:
		return base
	}
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
	selected := `{"claudeAiOauth":{"accessToken":"synthetic-selected-access","refreshToken":"synthetic-selected-refresh","expiresAt":1893456000000,"accountId":"selected","email":"selected@example.test"}}`
	writeWatcherFile(t, filepath.Join(configured, ".credentials.json"), selected)
	awaitWatcherDiscovery(t, events, "claude/selected@example.test")
	require.Equal(t, selected, readWatcherFile(t, vault.BackupPath("claude", "selected@example.test", ".credentials.json")))
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

func TestWatcherCapturesGeminiSelectorOnlyChanges(t *testing.T) {
	for _, selectedAPI := range []bool{false, true} {
		name := "key-to-oauth"
		beforeMode, afterMode := "gemini-api-key", "oauth-personal"
		if selectedAPI {
			name, beforeMode, afterMode = "oauth-to-key", "oauth-personal", "gemini-api-key"
		}
		t.Run(name, func(t *testing.T) {
			vault, home := watcherFixture(t)
			dir := filepath.Join(home, ".gemini")
			settings := func(mode string) string { return `{"selectedAuthType":"` + mode + `"}` }
			writeWatcherFile(t, filepath.Join(dir, "settings.json"), settings(beforeMode))
			writeWatcherFile(t, filepath.Join(dir, ".env"), "GEMINI_API_KEY=synthetic-selected-key\n")
			writeWatcherFile(t, filepath.Join(dir, "oauth_creds.json"), `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh","email":"oauth@example.test"}`)
			events := make(chan string, 8)
			watcher, err := NewWatcher(vault, quietWatcherConfig([]string{"gemini"}, events))
			require.NoError(t, err)
			require.NoError(t, watcher.Start(context.Background()))
			t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
			next := func() string {
				t.Helper()
				select {
				case event := <-events:
					provider, profile, ok := strings.Cut(event, "/")
					require.True(t, ok)
					require.Equal(t, "gemini", provider)
					return profile
				case <-time.After(5 * time.Second):
					t.Fatal("watcher did not capture the selected Gemini method")
					return ""
				}
			}
			previous := next()
			// OAuth and dotenv bytes do not change; the settings selector is
			// the only filesystem event which can discover the other grant.
			writeWatcherFile(t, filepath.Join(dir, "settings.json"), settings(afterMode))
			current := next()
			require.NotEqual(t, previous, current)
			require.NoError(t, watcher.Stop())
			require.Equal(t, settings(beforeMode), readWatcherFile(t, vault.BackupPath("gemini", previous, "settings.json")))
			require.Equal(t, settings(afterMode), readWatcherFile(t, vault.BackupPath("gemini", current, "settings.json")))
			profiles, err := vault.List("gemini")
			require.NoError(t, err)
			require.Len(t, profiles, 2)
		})
	}
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
	writeWatcherFile(t, filepath.Join(home, ".claude", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-claude-access","refreshToken":"synthetic-claude-refresh","expiresAt":1893456000000,"accountId":"claude-account","email":"claude@example.test"}}`)
	codex, err := os.ReadFile("testdata/codex_initial_login.json")
	require.NoError(t, err)
	writeWatcherFile(t, filepath.Join(home, ".codex", "auth.json"), string(codex))
	writeWatcherFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"type":"authorized_user","email":"gemini@example.test","access_token":"synthetic-gemini-access","refresh_token":"synthetic-gemini-refresh"}`)
	writeWatcherFile(t, filepath.Join(home, ".grok", "auth.json"), grokWatcherCredential("one"))
	openCode := authfile.OpenCodeAuthFiles().Files[0].Path
	writeWatcherFile(t, openCode, `{"access_token":"synthetic-opencode-access","email":"opencode@example.test"}`)
	cursorPaths := authfile.ResolveCursorPaths(home, runtime.GOOS, os.Getenv)
	writeWatcherFile(t, cursorPaths.AuthFile, `{"accessToken":"synthetic-cursor-session","refreshToken":"synthetic-cursor-refresh","email":"cursor@example.test"}`)

	want := map[string]bool{"claude/claude@example.test": true, "codex/codex-user@example.com": true, "gemini/gemini@example.test": true,
		"grok/grok@example.test": true, "opencode/auto-": true, "cursor/auto-": true}
	profilesByProvider := make(map[string]string)
	for range len(want) {
		select {
		case got := <-events:
			provider, profile, found := strings.Cut(got, "/")
			require.True(t, found, "discovery must identify its provider: %s", got)
			key := got
			if provider == "opencode" || provider == "cursor" {
				require.True(t, strings.HasPrefix(profile, "auto-"), "unproven identity must not claim an account label: %s", got)
				key = provider + "/auto-"
			}
			require.True(t, want[key], "duplicate or misrouted discovery: %s", got)
			profilesByProvider[provider] = profile
			delete(want, key)
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
	require.Contains(t, readWatcherFile(t, vault.BackupPath("cursor", profilesByProvider["cursor"], "auth.json")), "synthetic-cursor-session")
	require.Contains(t, readWatcherFile(t, vault.BackupPath("opencode", profilesByProvider["opencode"], "auth.json")), "synthetic-opencode-access")
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

func TestWatchOnceKeepsNamedClaudeSnapshotWhenOpaqueRotationIsUnproven(t *testing.T) {
	for _, tc := range []struct {
		name       string
		credential string
	}{
		{"different refresh token", `{"claudeAiOauth":{"accessToken":"synthetic-new-access","refreshToken":"synthetic-different-refresh","expiresAt":1893459600000}}`},
		{"unknown freshness", `{"claudeAiOauth":{"accessToken":"synthetic-new-access","refreshToken":"synthetic-stable-claude-refresh"}}`},
		{"equal freshness", `{"claudeAiOauth":{"accessToken":"synthetic-new-access","refreshToken":"synthetic-stable-claude-refresh","expiresAt":1893456000000}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vault, home := watcherFixture(t)
			auth := filepath.Join(home, ".claude", ".credentials.json")
			writeWatcherFile(t, filepath.Join(home, ".claude.json"), `{"oauthAccount":{"accountUuid":"claude-account","emailAddress":"claude@example.test"}}`)
			previous := claudeWatcherCredential("old")
			writeWatcherFile(t, auth, previous)
			require.NoError(t, vault.Backup(authfile.ClaudeAuthFiles(), "work"))
			writeWatcherFile(t, auth, tc.credential)

			discovered, err := WatchOnce(vault, []string{"claude"}, nil)
			require.NoError(t, err)
			require.Len(t, discovered, 1, "an unproven grant must still be preserved separately")
			require.True(t, strings.HasPrefix(discovered[0], "claude/auto-"))
			require.Equal(t, previous, readWatcherFile(t, vault.BackupPath("claude", "work", ".credentials.json")), "a shared advisory label cannot authorize replacing a named grant")
			profile := strings.TrimPrefix(discovered[0], "claude/")
			require.Equal(t, tc.credential, readWatcherFile(t, vault.BackupPath("claude", profile, ".credentials.json")))
			profiles, err := vault.List("claude")
			require.NoError(t, err)
			require.Len(t, profiles, 2)
			require.Contains(t, profiles, "work")
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
				// Cursor's authInfo is a label, not a restorable credential.
				writeWatcherFile(t, path, initial)
				advisory, err := WatchOnce(vault, []string{provider}, nil)
				require.NoError(t, err)
				require.Empty(t, advisory, "Cursor identity alone must not create an account")
				profiles, err := vault.List(provider)
				require.NoError(t, err)
				require.Empty(t, profiles)
				paths := authfile.ResolveCursorPaths(home, runtime.GOOS, os.Getenv)
				writeWatcherFile(t, paths.AuthFile, `{"apiKey":"synthetic-cursor-api-key"}`)
			}
			writeWatcherFile(t, path, initial)
			first, err := WatchOnce(vault, []string{provider}, nil)
			require.NoError(t, err)
			require.Len(t, first, 1)
			profiles, err := vault.List(provider)
			require.NoError(t, err)
			require.Len(t, profiles, 1)
			fileSet, _ := authfile.GetAuthFileSet(provider)
			require.NoError(t, vault.ValidateProfileCredentials(fileSet, profiles[0]))
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

func TestWatchOnceRejectsRefreshOnlyGeminiSettings(t *testing.T) {
	vault, home := watcherFixture(t)
	writeWatcherFile(t, filepath.Join(home, ".gemini", "settings.json"), `{"type":"authorized_user","email":"gemini@example.test","refresh_token":"synthetic-gemini-refresh"}`)
	discovered, err := WatchOnce(vault, []string{"gemini"}, nil)
	require.ErrorIs(t, err, authfile.ErrInvalidCredentials)
	require.Empty(t, discovered)
	profiles, err := vault.List("gemini")
	require.NoError(t, err)
	require.Empty(t, profiles, "a refresh-only intermediate write must not become a routable account")
}

type runtimeDiscovery struct {
	provider string
	profile  string
}

func runtimeWatcherHome(t *testing.T) (string, *authfile.Vault) {
	t.Helper()
	home := t.TempDir()
	for name, value := range map[string]string{
		"HOME": home, "XDG_CONFIG_HOME": filepath.Join(home, ".config"),
		"XDG_DATA_HOME": filepath.Join(home, ".local", "share"),
		"CODEX_HOME":    filepath.Join(home, ".codex"), "GEMINI_HOME": filepath.Join(home, ".gemini"),
		"GROK_HOME": filepath.Join(home, ".grok"), "CURSOR_CONFIG_DIR": filepath.Join(home, ".config", "cursor"),
		"CLAUDE_CONFIG_DIR": "", "CAAM_KEYCHAIN": "0",
	} {
		t.Setenv(name, value)
	}
	return home, authfile.NewVault(filepath.Join(home, "vault"))
}

func runtimeWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0700))
	require.NoError(t, os.WriteFile(path, data, 0600))
}

func runtimeGrokCredential(email string) []byte {
	data, _ := json.Marshal(map[string]string{
		"key": "SYNTHETIC-NOT-REAL-" + email, "email": email, "user_id": email,
	})
	return data
}

func runtimeWaitDiscovery(t *testing.T, discoveries <-chan runtimeDiscovery) runtimeDiscovery {
	t.Helper()
	select {
	case discovered := <-discoveries:
		return discovered
	case <-time.After(5 * time.Second):
		t.Fatal("running watcher did not capture credentials")
		return runtimeDiscovery{}
	}
}

func TestWatcherRuntime_MultipleProvidersWithSharedBasenames(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	discoveries := make(chan runtimeDiscovery, 16)
	watcher, err := NewWatcher(vault, WatcherConfig{
		DebounceInterval: 30 * time.Millisecond, PollInterval: 25 * time.Millisecond,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discoveries <- runtimeDiscovery{provider, profile}
		},
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })

	codex, err := os.ReadFile("testdata/codex_initial_login.json")
	require.NoError(t, err)
	cursorFiles := authfile.CursorAuthFiles()
	var cursorPath string
	for _, spec := range cursorFiles.Files {
		if filepath.Base(spec.Path) == "auth.json" {
			cursorPath = spec.Path
		}
	}
	require.NotEmpty(t, cursorPath)
	fixtures := map[string]struct {
		path string
		data []byte
	}{
		"claude": {
			filepath.Join(home, ".claude", ".credentials.json"),
			[]byte(`{"claudeAiOauth":{"accessToken":"synthetic-claude","refreshToken":"synthetic-refresh","email":"claude-runtime@example.com","accountId":"claude-runtime","expiresAt":4102444800000}}`),
		},
		"codex": {filepath.Join(home, ".codex", "auth.json"), codex},
		"grok": {
			filepath.Join(home, ".grok", "auth.json"),
			[]byte(`{"https://auth.x.ai::synthetic-client":{"key":"synthetic-grok","email":"grok-runtime@example.com","auth_mode":"sso"}}`),
		},
		"opencode": {
			filepath.Join(home, ".local", "share", "opencode", "auth.json"),
			[]byte(`{"openai":{"type":"oauth","access":"synthetic-opencode","refresh":"synthetic-refresh","expires":4102444800000}}`),
		},
		"cursor": {cursorPath, []byte(`{"accessToken":"synthetic-cursor","refreshToken":"synthetic-refresh"}`)},
		"gemini": {filepath.Join(home, ".gemini", ".env"), []byte("GEMINI_API_KEY=synthetic-gemini-key\n")},
		"agy": {
			filepath.Join(home, ".gemini", "antigravity-cli", "antigravity-oauth-token"),
			[]byte(`{"auth_method":"oauth","token":"synthetic-agy"}`),
		},
	}
	for _, fixture := range fixtures {
		runtimeWriteFile(t, fixture.path, fixture.data)
	}
	seen := make(map[string]bool)
	for range fixtures {
		discovered := runtimeWaitDiscovery(t, discoveries)
		require.False(t, seen[discovered.provider], "duplicate provider discovery")
		fixture, ok := fixtures[discovered.provider]
		require.True(t, ok, "unexpected provider %s", discovered.provider)
		seen[discovered.provider] = true
		data, err := os.ReadFile(vault.BackupPath(discovered.provider, discovered.profile, filepath.Base(fixture.path)))
		require.NoError(t, err)
		if filepath.Base(fixture.path) == ".env" {
			assert.Equal(t, fixture.data, data)
		} else {
			assert.JSONEq(t, string(fixture.data), string(data))
		}
	}
	require.NoError(t, watcher.Stop())
	assert.Len(t, seen, len(fixtures))
	for provider := range fixtures {
		profiles, err := vault.List(provider)
		require.NoError(t, err)
		assert.Len(t, profiles, 1, "provider %s", provider)
	}
}

func TestWatcherRuntime_MissingDirectoryAndAtomicReplacements(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	discoveries := make(chan runtimeDiscovery, 8)
	changes := make(chan string, 16)
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"grok"}, DebounceInterval: 20 * time.Millisecond, PollInterval: 25 * time.Millisecond,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discoveries <- runtimeDiscovery{provider, profile}
		},
		OnChange: func(_ string, path string) { changes <- path },
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	authDir := filepath.Join(home, ".grok")
	authPath := filepath.Join(authDir, "auth.json")
	_, err = os.Stat(authDir)
	require.True(t, os.IsNotExist(err), "watching must not create native configuration directories")

	runtimeWriteFile(t, authPath, runtimeGrokCredential("first@example.com"))
	assert.Equal(t, "first@example.com", runtimeWaitDiscovery(t, discoveries).profile)

	// Native clients write a sibling temp file and atomically rename it.
	tempPath := filepath.Join(authDir, "auth.json.native-write")
	runtimeWriteFile(t, tempPath, runtimeGrokCredential("second@example.com"))
	require.NoError(t, os.Rename(tempPath, authPath))
	assert.Equal(t, "second@example.com", runtimeWaitDiscovery(t, discoveries).profile)

	// A watch attached to the old directory inode must not go permanently deaf.
	require.NoError(t, os.Rename(authDir, filepath.Join(home, "old-grok-directory")))
	runtimeWriteFile(t, authPath, runtimeGrokCredential("third@example.com"))
	assert.Equal(t, "third@example.com", runtimeWaitDiscovery(t, discoveries).profile)

	for len(changes) > 0 {
		<-changes
	}
	require.NoError(t, os.Rename(authPath, filepath.Join(home, "logged-out-auth.json")))
	select {
	case path := <-changes:
		assert.Equal(t, authPath, path)
	case <-time.After(5 * time.Second):
		t.Fatal("credential removal was not reconciled")
	}
	runtimeWriteFile(t, authPath, runtimeGrokCredential("fourth@example.com"))
	assert.Equal(t, "fourth@example.com", runtimeWaitDiscovery(t, discoveries).profile)
	require.NoError(t, watcher.Stop())
	profiles, err := vault.List("grok")
	require.NoError(t, err)
	assert.Len(t, profiles, 4)
}

type runtimeEventSource struct {
	events     chan fsnotify.Event
	errors     chan error
	addErr     error
	eventsOnce sync.Once
	errorsOnce sync.Once
}

func (s *runtimeEventSource) Add(string) error                  { return s.addErr }
func (s *runtimeEventSource) Remove(string) error               { return nil }
func (s *runtimeEventSource) EventsChan() <-chan fsnotify.Event { return s.events }
func (s *runtimeEventSource) ErrorsChan() <-chan error          { return s.errors }
func (s *runtimeEventSource) Close() error {
	s.eventsOnce.Do(func() { close(s.events) })
	s.errorsOnce.Do(func() { close(s.errors) })
	return nil
}

func TestWatcherRuntime_PollingRecoversUnavailableAndLostEvents(t *testing.T) {
	for _, failure := range []string{"creation", "registration", "dropped events", "closed events", "closed errors"} {
		t.Run(failure, func(t *testing.T) {
			home, vault := runtimeWatcherHome(t)
			authPath := filepath.Join(home, ".grok", "auth.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(authPath), 0700))
			discoveries := make(chan runtimeDiscovery, 4)
			reported := make(chan error, 8)
			watcher, err := NewWatcher(vault, WatcherConfig{
				Providers: []string{"grok"}, DebounceInterval: 20 * time.Millisecond, PollInterval: 25 * time.Millisecond,
				OnDiscovery: func(provider, profile string, _ *identity.Identity) {
					discoveries <- runtimeDiscovery{provider, profile}
				},
				OnError: func(err error) { reported <- err },
			})
			require.NoError(t, err)
			source := &runtimeEventSource{events: make(chan fsnotify.Event), errors: make(chan error)}
			if failure == "registration" {
				source.addErr = os.ErrPermission
			}
			if failure == "closed events" {
				source.eventsOnce.Do(func() { close(source.events) })
			}
			if failure == "closed errors" {
				source.errorsOnce.Do(func() { close(source.errors) })
			}
			watcher.newEventSource = func() (watchEventSource, error) {
				if failure == "creation" {
					return nil, fmt.Errorf("synthetic notifier unavailable")
				}
				return source, nil
			}
			require.NoError(t, watcher.Start(context.Background()))
			t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
			if failure != "dropped events" {
				select {
				case <-reported:
				case <-time.After(5 * time.Second):
					t.Fatal("notifier failure was not reported")
				}
			}
			runtimeWriteFile(t, authPath, runtimeGrokCredential("polling@example.com"))
			assert.Equal(t, "polling@example.com", runtimeWaitDiscovery(t, discoveries).profile)
			require.NoError(t, watcher.Stop())
			profiles, err := vault.List("grok")
			require.NoError(t, err)
			assert.Equal(t, []string{"polling@example.com"}, profiles)
		})
	}
}

func TestWatcherPollingWorksWithoutBackendAndAfterDirectoryReplacement(t *testing.T) {
	vault, home := watcherFixture(t)
	events := make(chan string, 32)
	watcher, err := NewWatcher(vault, quietWatcherConfig([]string{"grok"}, events))
	require.NoError(t, err)
	watcher.newEventSource = func() (watchEventSource, error) { return nil, errors.New("synthetic backend unavailable") }
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
	watcher.newEventSource = func() (watchEventSource, error) {
		backend, err := fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		backends <- backend
		return &fsnotifySource{backend}, nil
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
	watcher.newEventSource = func() (watchEventSource, error) {
		var err error
		backend, err = fsnotify.NewWatcher()
		if err != nil {
			return nil, err
		}
		return &fsnotifySource{backend}, nil
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

func TestWatcherRuntime_RetriesUnchangedCredentialAfterVaultRecovery(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	// A temporarily unusable vault must not cause the only login event to be lost.
	runtimeWriteFile(t, filepath.Join(home, "vault"), []byte("synthetic temporary blocker"))
	discoveries := make(chan runtimeDiscovery, 4)
	reported := make(chan error, 8)
	var errorCount atomic.Int64
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"grok"}, DebounceInterval: 20 * time.Millisecond, PollInterval: 30 * time.Millisecond,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discoveries <- runtimeDiscovery{provider, profile}
		},
		OnError: func(err error) {
			errorCount.Add(1)
			reported <- err
		},
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	runtimeWriteFile(t, filepath.Join(home, ".grok", "auth.json"), runtimeGrokCredential("retry@example.com"))
	select {
	case <-reported:
	case <-time.After(5 * time.Second):
		t.Fatal("capture failure was not reported")
	}
	time.Sleep(120 * time.Millisecond)
	assert.EqualValues(t, 1, errorCount.Load(), "unchanged failure should not flood callbacks")
	require.NoError(t, os.Rename(filepath.Join(home, "vault"), filepath.Join(home, "vault-blocker")))
	require.NoError(t, os.MkdirAll(filepath.Join(home, "vault"), 0700))
	assert.Equal(t, "retry@example.com", runtimeWaitDiscovery(t, discoveries).profile)
}

func TestWatcherRuntime_RestartAndFrozenProviderPaths(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	discoveries := make(chan runtimeDiscovery, 8)
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"GROK", "grok-build"}, DebounceInterval: 20 * time.Millisecond, PollInterval: 25 * time.Millisecond,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discoveries <- runtimeDiscovery{provider, profile}
		},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"grok"}, watcher.config.Providers)
	var factories atomic.Int64
	watcher.newEventSource = func() (watchEventSource, error) {
		factories.Add(1)
		return newFSNotifySource()
	}
	// Environment changes after construction must not misattribute or redirect
	// pending events to another native login, including after a restart.
	t.Setenv("GROK_HOME", filepath.Join(home, "unrelated-grok-home"))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	for generation := 1; generation <= 2; generation++ {
		ctx, cancel := context.WithCancel(context.Background())
		require.NoError(t, watcher.Start(ctx))
		require.ErrorContains(t, watcher.Start(ctx), "already running")
		email := fmt.Sprintf("restart-%d@example.com", generation)
		runtimeWriteFile(t, filepath.Join(home, ".grok", "auth.json"), runtimeGrokCredential(email))
		assert.Equal(t, email, runtimeWaitDiscovery(t, discoveries).profile)
		cancel()
		require.NoError(t, watcher.Stop())
		select {
		case <-watcher.Done():
		default:
			t.Fatal("Stop returned before watcher completion")
		}
	}
	assert.EqualValues(t, 2, factories.Load(), "a restart must create a fresh filesystem notifier")
	profiles, err := vault.List("grok")
	require.NoError(t, err)
	assert.Len(t, profiles, 2)
	_, err = os.Stat(filepath.Join(home, "unrelated-grok-home"))
	assert.True(t, os.IsNotExist(err))
}

func TestWatcherRuntime_ConcurrentStopWaitsForCallback(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var callbacks atomic.Int64
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"grok"}, DebounceInterval: 20 * time.Millisecond, PollInterval: 25 * time.Millisecond,
		OnDiscovery: func(_, _ string, _ *identity.Identity) {
			callbacks.Add(1)
			entered <- struct{}{}
			<-release
		},
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	runtimeWriteFile(t, filepath.Join(home, ".grok", "auth.json"), runtimeGrokCredential("barrier@example.com"))
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("discovery callback did not begin")
	}
	stopped := make(chan error, 8)
	for range 8 {
		go func() { stopped <- watcher.Stop() }()
	}
	select {
	case <-stopped:
		t.Fatal("concurrent Stop returned while a callback was still running")
	case <-time.After(40 * time.Millisecond):
	}
	require.ErrorContains(t, watcher.Start(context.Background()), "already running")
	releaseOnce.Do(func() { close(release) })
	for range 8 {
		select {
		case err := <-stopped:
			require.NoError(t, err)
		case <-time.After(5 * time.Second):
			t.Fatal("concurrent Stop did not finish")
		}
	}
	runtimeWriteFile(t, filepath.Join(home, ".grok", "auth.json"), runtimeGrokCredential("after-stop@example.com"))
	time.Sleep(100 * time.Millisecond)
	assert.EqualValues(t, 1, callbacks.Load())
	profiles, err := vault.List("grok")
	require.NoError(t, err)
	assert.Equal(t, []string{"barrier@example.com"}, profiles, "no capture may finish after Stop returns")
}

func TestWatcherRuntime_SettingsChurnDoesNotDelayCredentialCapture(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	discoveries := make(chan runtimeDiscovery, 8)
	var changes atomic.Int64
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"claude"}, DebounceInterval: 40 * time.Millisecond, PollInterval: 20 * time.Millisecond,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discoveries <- runtimeDiscovery{provider, profile}
		},
		OnChange: func(_, _ string) { changes.Add(1) },
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	credential := []byte(`{"claudeAiOauth":{"accessToken":"synthetic-churn","refreshToken":"synthetic-refresh","email":"churn@example.com","accountId":"churn","expiresAt":4102444800000}}`)
	runtimeWriteFile(t, filepath.Join(home, ".claude", ".credentials.json"), credential)
	deadline := time.Now().Add(time.Second)
	for iteration := 0; time.Now().Before(deadline); iteration++ {
		runtimeWriteFile(t, filepath.Join(home, ".claude.json"), []byte(fmt.Sprintf(`{"projects":{"/synthetic":{"lastCost":%d}}}`, iteration)))
		runtimeWriteFile(t, filepath.Join(home, ".claude", "settings.json"), []byte(fmt.Sprintf(`{"theme":"synthetic-%d"}`, iteration)))
		select {
		case discovered := <-discoveries:
			assert.Equal(t, "churn@example.com", discovered.profile)
			require.NoError(t, watcher.Stop())
			assert.EqualValues(t, 1, changes.Load(), "policy-only writes must not trigger credential changes")
			return
		default:
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("continuous unrelated settings writes starved credential capture")
}

func TestWatcherRuntime_OptionalSettingsAreOptIn(t *testing.T) {
	for _, watchOptional := range []bool{false, true} {
		t.Run(fmt.Sprint(watchOptional), func(t *testing.T) {
			home, vault := runtimeWatcherHome(t)
			changes := make(chan string, 8)
			watcher, err := NewWatcher(vault, WatcherConfig{
				Providers: []string{"grok"}, DebounceInterval: 20 * time.Millisecond,
				PollInterval: 25 * time.Millisecond, WatchOptionalFiles: watchOptional,
				OnChange: func(_, path string) { changes <- path },
			})
			require.NoError(t, err)
			require.NoError(t, watcher.Start(context.Background()))
			t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
			path := filepath.Join(home, ".grok", "config.toml")
			runtimeWriteFile(t, path, []byte("auto_update = true\n"))
			if watchOptional {
				select {
				case changed := <-changes:
					assert.Equal(t, path, changed)
				case <-time.After(5 * time.Second):
					t.Fatal("explicit optional file watch did not observe settings")
				}
			} else {
				select {
				case <-changes:
					t.Fatal("optional settings triggered a default auth watch")
				case <-time.After(100 * time.Millisecond):
				}
			}
			require.NoError(t, watcher.Stop())
			profiles, err := vault.List("grok")
			require.NoError(t, err)
			assert.Empty(t, profiles, "settings alone must never become an account")
		})
	}
}

func TestWatcherRuntime_KeychainRotationWithoutFileEvents(t *testing.T) {
	home, vault := runtimeWatcherHome(t)
	items := testutil.FakeKeychain(t)
	discoveries := make(chan runtimeDiscovery, 4)
	watcher, err := NewWatcher(vault, WatcherConfig{
		Providers: []string{"claude"}, DebounceInterval: 20 * time.Millisecond, PollInterval: 30 * time.Millisecond,
		OnDiscovery: func(provider, profile string, _ *identity.Identity) {
			discoveries <- runtimeDiscovery{provider, profile}
		},
	})
	require.NoError(t, err)
	require.NoError(t, watcher.Start(context.Background()))
	t.Cleanup(func() { require.NoError(t, watcher.Stop()) })
	for generation := 1; generation <= 2; generation++ {
		email := fmt.Sprintf("keychain-%d@example.com", generation)
		credential := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-keychain-%d","refreshToken":"synthetic-refresh","email":%q,"accountId":%q,"expiresAt":4102444800000}}`, generation, email, email)
		testutil.FakeKeychainStore(t, items, keychain.ClaudeService, keychain.LoginAccount(), credential)
		assert.Equal(t, email, runtimeWaitDiscovery(t, discoveries).profile)
	}
	require.NoError(t, watcher.Stop())
	_, err = os.Stat(filepath.Join(home, ".claude", ".credentials.json"))
	assert.True(t, os.IsNotExist(err), "discovery must not create a stale mirror while reading the keychain")
	profiles, err := vault.List("claude")
	require.NoError(t, err)
	assert.Len(t, profiles, 2)
}
