// Package workflows contains end-to-end workflow tests.
package workflows

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
)

// TestE2E_MultiMachineSyncWorkflow tests synchronization between local and simulated remote machines.
// Since full SSH testing requires network access, this test uses:
// - Local file operations to simulate remote
// - Direct sync algorithm testing
// - Real freshness comparison logic
func TestE2E_MultiMachineSyncWorkflow(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	// ==========================================================================
	// Phase 1: Setup - Create local and simulated remote vaults
	// ==========================================================================
	h.StartStep("setup", "Creating local and simulated remote vaults")

	localVaultDir := h.SubDir("local_vault")
	remoteVaultDir := h.SubDir("remote_vault")
	syncStateDir := h.SubDir("sync_state")

	// Create a profile in local vault with a specific expiry
	localExpiry := time.Now().Add(24 * time.Hour)
	createSyncTestProfile(t, h, localVaultDir, "codex", "user@example.com", localExpiry)

	h.LogInfo("Created vaults", map[string]interface{}{
		"local_vault":  localVaultDir,
		"remote_vault": remoteVaultDir,
	})
	h.EndStep("setup")

	// ==========================================================================
	// Phase 2: Push Sync - Local has newer token
	// ==========================================================================
	h.StartStep("push_sync", "Testing push sync when local has newer token")

	h.StartStep("create_remote_profile", "Creating remote profile with older expiry")
	// Create remote profile with older expiry (should be overwritten)
	remoteExpiry := time.Now().Add(12 * time.Hour) // Older than local
	createSyncTestProfile(t, h, remoteVaultDir, "codex", "user@example.com", remoteExpiry)
	h.EndStep("create_remote_profile")

	h.StartStep("compare_freshness", "Comparing freshness between local and remote")
	localFreshness := extractTestFreshness(t, h, localVaultDir, "codex", "user@example.com")
	remoteFreshness := extractTestFreshness(t, h, remoteVaultDir, "codex", "user@example.com")

	// Local should be fresher (later expiry)
	localIsFresher := sync.CompareFreshness(localFreshness, remoteFreshness)
	if !localIsFresher {
		t.Errorf("Expected local to be fresher, but it wasn't")
		t.Errorf("Local expiry: %v, Remote expiry: %v", localFreshness.ExpiresAt, remoteFreshness.ExpiresAt)
	}

	h.LogInfo("Freshness comparison result", map[string]interface{}{
		"local_expiry":    localFreshness.ExpiresAt.Format(time.RFC3339),
		"remote_expiry":   remoteFreshness.ExpiresAt.Format(time.RFC3339),
		"local_is_fresher": localIsFresher,
		"direction":       "push",
	})
	h.EndStep("compare_freshness")

	h.StartStep("simulate_push", "Simulating push sync operation")
	// Simulate push: copy local to remote
	if localIsFresher {
		err := copyProfile(localVaultDir, remoteVaultDir, "codex", "user@example.com")
		if err != nil {
			t.Fatalf("Failed to simulate push: %v", err)
		}

		// Verify remote now matches local
		remoteAfterPush := extractTestFreshness(t, h, remoteVaultDir, "codex", "user@example.com")
		if !remoteAfterPush.ExpiresAt.Equal(localFreshness.ExpiresAt) {
			t.Errorf("After push, remote expiry (%v) should match local (%v)",
				remoteAfterPush.ExpiresAt, localFreshness.ExpiresAt)
		}
		h.LogInfo("Push sync successful", map[string]interface{}{
			"remote_new_expiry": remoteAfterPush.ExpiresAt.Format(time.RFC3339),
		})
	}
	h.EndStep("simulate_push")

	h.EndStep("push_sync")

	// ==========================================================================
	// Phase 3: Pull Sync - Remote has newer token
	// ==========================================================================
	h.StartStep("pull_sync", "Testing pull sync when remote has newer token")

	h.StartStep("update_remote", "Updating remote profile with newer expiry")
	// Update remote to have newer expiry
	newRemoteExpiry := time.Now().Add(48 * time.Hour) // Newer than local
	createSyncTestProfile(t, h, remoteVaultDir, "codex", "user@example.com", newRemoteExpiry)
	h.EndStep("update_remote")

	h.StartStep("compare_freshness_pull", "Comparing freshness for pull decision")
	localFreshness = extractTestFreshness(t, h, localVaultDir, "codex", "user@example.com")
	remoteFreshness = extractTestFreshness(t, h, remoteVaultDir, "codex", "user@example.com")

	// Remote should be fresher now
	remoteIsFresher := sync.CompareFreshness(remoteFreshness, localFreshness)
	if !remoteIsFresher {
		t.Errorf("Expected remote to be fresher, but it wasn't")
		t.Errorf("Local expiry: %v, Remote expiry: %v", localFreshness.ExpiresAt, remoteFreshness.ExpiresAt)
	}

	h.LogInfo("Freshness comparison result", map[string]interface{}{
		"local_expiry":     localFreshness.ExpiresAt.Format(time.RFC3339),
		"remote_expiry":    remoteFreshness.ExpiresAt.Format(time.RFC3339),
		"remote_is_fresher": remoteIsFresher,
		"direction":        "pull",
	})
	h.EndStep("compare_freshness_pull")

	h.StartStep("simulate_pull", "Simulating pull sync operation")
	// Simulate pull: copy remote to local
	if remoteIsFresher {
		err := copyProfile(remoteVaultDir, localVaultDir, "codex", "user@example.com")
		if err != nil {
			t.Fatalf("Failed to simulate pull: %v", err)
		}

		// Verify local now matches remote
		localAfterPull := extractTestFreshness(t, h, localVaultDir, "codex", "user@example.com")
		if !localAfterPull.ExpiresAt.Equal(remoteFreshness.ExpiresAt) {
			t.Errorf("After pull, local expiry (%v) should match remote (%v)",
				localAfterPull.ExpiresAt, remoteFreshness.ExpiresAt)
		}
		h.LogInfo("Pull sync successful", map[string]interface{}{
			"local_new_expiry": localAfterPull.ExpiresAt.Format(time.RFC3339),
		})
	}
	h.EndStep("simulate_pull")

	h.EndStep("pull_sync")

	// ==========================================================================
	// Phase 4: Conflict Resolution - Both modified
	// ==========================================================================
	h.StartStep("conflict_resolution", "Testing conflict resolution when both are modified")

	h.StartStep("modify_both", "Modifying both local and remote differently")
	// Create scenario: both modified with different expiries
	localConflictExpiry := time.Now().Add(36 * time.Hour)
	remoteConflictExpiry := time.Now().Add(72 * time.Hour) // Remote is fresher
	createSyncTestProfile(t, h, localVaultDir, "codex", "user@example.com", localConflictExpiry)
	createSyncTestProfile(t, h, remoteVaultDir, "codex", "user@example.com", remoteConflictExpiry)
	h.EndStep("modify_both")

	h.StartStep("resolve_conflict", "Resolving conflict by picking fresher token")
	localFreshness = extractTestFreshness(t, h, localVaultDir, "codex", "user@example.com")
	remoteFreshness = extractTestFreshness(t, h, remoteVaultDir, "codex", "user@example.com")

	// Determine winner
	var winner string
	var winnerFreshness *sync.TokenFreshness
	if sync.CompareFreshness(localFreshness, remoteFreshness) {
		winner = "local"
		winnerFreshness = localFreshness
	} else if sync.CompareFreshness(remoteFreshness, localFreshness) {
		winner = "remote"
		winnerFreshness = remoteFreshness
	} else {
		winner = "equal"
	}

	if winner != "remote" {
		t.Errorf("Expected remote to win conflict (has later expiry), but %s won", winner)
	}

	h.LogInfo("Conflict resolved", map[string]interface{}{
		"local_expiry":  localFreshness.ExpiresAt.Format(time.RFC3339),
		"remote_expiry": remoteFreshness.ExpiresAt.Format(time.RFC3339),
		"winner":        winner,
	})

	// Apply resolution (pull from remote)
	if winner == "remote" {
		err := copyProfile(remoteVaultDir, localVaultDir, "codex", "user@example.com")
		if err != nil {
			t.Fatalf("Failed to apply conflict resolution: %v", err)
		}

		localAfterResolution := extractTestFreshness(t, h, localVaultDir, "codex", "user@example.com")
		if !localAfterResolution.ExpiresAt.Equal(winnerFreshness.ExpiresAt) {
			t.Errorf("After resolution, local should have winner's expiry")
		}
		h.LogInfo("Conflict resolution applied", map[string]interface{}{
			"local_new_expiry": localAfterResolution.ExpiresAt.Format(time.RFC3339),
		})
	}
	h.EndStep("resolve_conflict")

	h.EndStep("conflict_resolution")

	// ==========================================================================
	// Phase 5: Queue Retry - Simulate failure and retry
	// ==========================================================================
	h.StartStep("queue_retry", "Testing queue retry mechanism")

	h.StartStep("setup_sync_state", "Setting up sync state for queue testing")
	// Create a sync state for testing queue operations
	state := sync.NewSyncState(syncStateDir)
	machine := sync.NewMachine("test-server", "192.168.1.100")
	state.Pool.AddMachine(machine)
	h.LogInfo("Created sync state", map[string]interface{}{
		"machine_id":   machine.ID,
		"machine_name": machine.Name,
	})
	h.EndStep("setup_sync_state")

	h.StartStep("simulate_failure", "Simulating sync failure and adding to queue")
	// Simulate a sync failure - add to queue
	state.AddToQueue("codex", "user@example.com", machine.ID, "connection refused")

	// Verify entry was added
	if len(state.Queue.Entries) != 1 {
		t.Errorf("Expected 1 queue entry, got %d", len(state.Queue.Entries))
	}

	entry := state.Queue.Entries[0]
	if entry.Provider != "codex" || entry.Profile != "user@example.com" {
		t.Errorf("Queue entry has wrong profile: %s/%s", entry.Provider, entry.Profile)
	}
	if entry.Attempts != 1 {
		t.Errorf("Expected 1 attempt, got %d", entry.Attempts)
	}
	if entry.LastError != "connection refused" {
		t.Errorf("Expected error 'connection refused', got '%s'", entry.LastError)
	}

	h.LogInfo("Added to queue", map[string]interface{}{
		"provider":   entry.Provider,
		"profile":    entry.Profile,
		"attempts":   entry.Attempts,
		"last_error": entry.LastError,
	})
	h.EndStep("simulate_failure")

	h.StartStep("retry_queue", "Retrying queued sync and verifying removal")
	// Simulate successful retry - record in history and remove from queue
	state.AddToHistory(sync.HistoryEntry{
		Timestamp: time.Now(),
		Trigger:   "retry",
		Provider:  "codex",
		Profile:   "user@example.com",
		Machine:   machine.Name,
		Action:    "push",
		Success:   true,
		Duration:  100 * time.Millisecond,
	})

	// Remove from queue (simulating successful sync)
	state.RemoveFromQueue("codex", "user@example.com", machine.ID)

	// Verify removal
	if len(state.Queue.Entries) != 0 {
		t.Errorf("Expected queue to be empty after successful retry, got %d entries", len(state.Queue.Entries))
	}

	// Verify history was recorded
	history := state.RecentHistory(10)
	if len(history) != 1 {
		t.Errorf("Expected 1 history entry, got %d", len(history))
	}
	if len(history) > 0 {
		histEntry := history[0]
		if !histEntry.Success {
			t.Errorf("History entry should show success")
		}
		if histEntry.Trigger != "retry" {
			t.Errorf("Expected trigger 'retry', got '%s'", histEntry.Trigger)
		}
	}

	h.LogInfo("Queue retry successful", map[string]interface{}{
		"queue_empty":   len(state.Queue.Entries) == 0,
		"history_count": len(history),
	})
	h.EndStep("retry_queue")

	h.StartStep("persist_state", "Persisting sync state to disk")
	// Save state to verify persistence
	err := state.Save()
	if err != nil {
		t.Errorf("Failed to save sync state: %v", err)
	}

	// Verify files were created
	if !h.FileExists(filepath.Join(syncStateDir, "queue.json")) {
		t.Error("queue.json should exist after save")
	}
	if !h.FileExists(filepath.Join(syncStateDir, "history.json")) {
		t.Error("history.json should exist after save")
	}

	h.LogInfo("State persisted", map[string]interface{}{
		"queue_file":   filepath.Join(syncStateDir, "queue.json"),
		"history_file": filepath.Join(syncStateDir, "history.json"),
	})
	h.EndStep("persist_state")

	h.EndStep("queue_retry")

	// Print summary
	t.Log("\n" + h.Summary())
}

// TestE2E_FreshnessComparison tests the freshness comparison algorithm in detail.
func TestE2E_FreshnessComparison(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Setting up freshness comparison tests")
	now := time.Now()
	h.EndStep("setup")

	testCases := []struct {
		name           string
		localExpiry    time.Time
		remoteExpiry   time.Time
		localModTime   time.Time
		remoteModTime  time.Time
		expectedWinner string
	}{
		{
			name:           "local_has_later_expiry",
			localExpiry:    now.Add(48 * time.Hour),
			remoteExpiry:   now.Add(24 * time.Hour),
			localModTime:   now,
			remoteModTime:  now,
			expectedWinner: "local",
		},
		{
			name:           "remote_has_later_expiry",
			localExpiry:    now.Add(24 * time.Hour),
			remoteExpiry:   now.Add(48 * time.Hour),
			localModTime:   now,
			remoteModTime:  now,
			expectedWinner: "remote",
		},
		{
			name:           "equal_expiry_local_later_mod",
			localExpiry:    now.Add(24 * time.Hour),
			remoteExpiry:   now.Add(24 * time.Hour),
			localModTime:   now.Add(1 * time.Hour),
			remoteModTime:  now,
			expectedWinner: "local",
		},
		{
			name:           "equal_expiry_remote_later_mod",
			localExpiry:    now.Add(24 * time.Hour),
			remoteExpiry:   now.Add(24 * time.Hour),
			localModTime:   now,
			remoteModTime:  now.Add(1 * time.Hour),
			expectedWinner: "remote",
		},
		{
			name:           "equal_expiry_equal_mod",
			localExpiry:    now.Add(24 * time.Hour),
			remoteExpiry:   now.Add(24 * time.Hour),
			localModTime:   now,
			remoteModTime:  now,
			expectedWinner: "equal",
		},
	}

	for _, tc := range testCases {
		h.StartStep(tc.name, "Testing: "+tc.name)

		local := &sync.TokenFreshness{
			Provider:   "codex",
			Profile:    "test",
			ExpiresAt:  tc.localExpiry,
			ModifiedAt: tc.localModTime,
			Source:     "local",
		}

		remote := &sync.TokenFreshness{
			Provider:   "codex",
			Profile:    "test",
			ExpiresAt:  tc.remoteExpiry,
			ModifiedAt: tc.remoteModTime,
			Source:     "remote",
		}

		var actualWinner string
		localFresher := sync.CompareFreshness(local, remote)
		remoteFresher := sync.CompareFreshness(remote, local)

		if localFresher && !remoteFresher {
			actualWinner = "local"
		} else if remoteFresher && !localFresher {
			actualWinner = "remote"
		} else {
			actualWinner = "equal"
		}

		if actualWinner != tc.expectedWinner {
			t.Errorf("%s: expected winner %q, got %q", tc.name, tc.expectedWinner, actualWinner)
		}

		h.LogInfo("Comparison result", map[string]interface{}{
			"local_expiry":    tc.localExpiry.Format(time.RFC3339),
			"remote_expiry":   tc.remoteExpiry.Format(time.RFC3339),
			"local_mod":       tc.localModTime.Format(time.RFC3339),
			"remote_mod":      tc.remoteModTime.Format(time.RFC3339),
			"expected_winner": tc.expectedWinner,
			"actual_winner":   actualWinner,
			"pass":            actualWinner == tc.expectedWinner,
		})

		h.EndStep(tc.name)
	}

	t.Log("\n" + h.Summary())
}

// TestE2E_SyncQueueManagement tests the sync queue management functionality.
func TestE2E_SyncQueueManagement(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Setting up queue management test")
	syncStateDir := h.SubDir("sync_state")
	state := sync.NewSyncState(syncStateDir)

	// Create multiple machines
	machine1 := sync.NewMachine("server-1", "192.168.1.1")
	machine2 := sync.NewMachine("server-2", "192.168.1.2")
	state.Pool.AddMachine(machine1)
	state.Pool.AddMachine(machine2)
	h.EndStep("setup")

	h.StartStep("add_entries", "Adding multiple queue entries")
	// Add entries for different profiles and machines
	state.AddToQueue("codex", "user1@example.com", machine1.ID, "timeout")
	state.AddToQueue("claude", "user2@example.com", machine1.ID, "auth failed")
	state.AddToQueue("codex", "user1@example.com", machine2.ID, "connection refused")

	if len(state.Queue.Entries) != 3 {
		t.Errorf("Expected 3 queue entries, got %d", len(state.Queue.Entries))
	}

	h.LogInfo("Queue entries added", map[string]interface{}{
		"count": len(state.Queue.Entries),
	})
	h.EndStep("add_entries")

	h.StartStep("retry_increment", "Testing retry count increment")
	// Re-add an existing entry (should increment attempts)
	state.AddToQueue("codex", "user1@example.com", machine1.ID, "timeout again")

	var foundEntry *sync.QueueEntry
	for i := range state.Queue.Entries {
		e := &state.Queue.Entries[i]
		if e.Provider == "codex" && e.Profile == "user1@example.com" && e.Machine == machine1.ID {
			foundEntry = e
			break
		}
	}

	if foundEntry == nil {
		t.Error("Could not find entry after re-add")
	} else if foundEntry.Attempts != 2 {
		t.Errorf("Expected 2 attempts after re-add, got %d", foundEntry.Attempts)
	}

	h.LogInfo("Retry increment verified", map[string]interface{}{
		"attempts":   foundEntry.Attempts,
		"last_error": foundEntry.LastError,
	})
	h.EndStep("retry_increment")

	h.StartStep("remove_entry", "Testing entry removal")
	// Remove one entry
	state.RemoveFromQueue("claude", "user2@example.com", machine1.ID)

	if len(state.Queue.Entries) != 2 {
		t.Errorf("Expected 2 queue entries after removal, got %d", len(state.Queue.Entries))
	}

	// Verify the right entry was removed
	for _, e := range state.Queue.Entries {
		if e.Provider == "claude" && e.Profile == "user2@example.com" {
			t.Error("Claude entry should have been removed")
		}
	}

	h.LogInfo("Entry removed", map[string]interface{}{
		"remaining_count": len(state.Queue.Entries),
	})
	h.EndStep("remove_entry")

	h.StartStep("persist_queue", "Testing queue persistence")
	// Save and reload
	if err := state.Save(); err != nil {
		t.Fatalf("Failed to save state: %v", err)
	}

	// Read queue file directly to verify
	queuePath := filepath.Join(syncStateDir, "queue.json")
	queueData, err := os.ReadFile(queuePath)
	if err != nil {
		t.Fatalf("Failed to read queue file: %v", err)
	}

	var savedQueue sync.SyncQueue
	if err := json.Unmarshal(queueData, &savedQueue); err != nil {
		t.Fatalf("Failed to parse queue file: %v", err)
	}

	if len(savedQueue.Entries) != 2 {
		t.Errorf("Expected 2 entries in saved queue, got %d", len(savedQueue.Entries))
	}

	h.LogInfo("Queue persisted", map[string]interface{}{
		"saved_entries": len(savedQueue.Entries),
	})
	h.EndStep("persist_queue")

	t.Log("\n" + h.Summary())
}

// TestE2E_SyncHistoryTracking tests the sync history tracking functionality.
func TestE2E_SyncHistoryTracking(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.StartStep("setup", "Setting up history tracking test")
	syncStateDir := h.SubDir("sync_state")
	state := sync.NewSyncState(syncStateDir)
	h.EndStep("setup")

	h.StartStep("add_history", "Adding history entries")
	// Add various history entries
	triggers := []string{"backup", "refresh", "manual", "retry"}
	actions := []string{"push", "pull", "skip"}

	for i := 0; i < 10; i++ {
		state.AddToHistory(sync.HistoryEntry{
			Timestamp: time.Now().Add(time.Duration(i) * time.Minute),
			Trigger:   triggers[i%len(triggers)],
			Provider:  "codex",
			Profile:   "user@example.com",
			Machine:   "server-1",
			Action:    actions[i%len(actions)],
			Success:   i%3 != 0, // Some failures
			Error:     map[bool]string{true: "", false: "simulated error"}[i%3 != 0],
			Duration:  time.Duration(100+i*10) * time.Millisecond,
		})
	}

	h.LogInfo("History entries added", map[string]interface{}{
		"count": len(state.History.Entries),
	})
	h.EndStep("add_history")

	h.StartStep("retrieve_history", "Retrieving recent history")
	// Get recent history
	recent := state.RecentHistory(5)
	if len(recent) != 5 {
		t.Errorf("Expected 5 recent entries, got %d", len(recent))
	}

	// Verify order (most recent first)
	for i := 1; i < len(recent); i++ {
		if recent[i].Timestamp.After(recent[i-1].Timestamp) {
			t.Error("History should be in reverse chronological order")
		}
	}

	h.LogInfo("Recent history retrieved", map[string]interface{}{
		"count": len(recent),
	})
	h.EndStep("retrieve_history")

	h.StartStep("persist_history", "Testing history persistence")
	// Save and verify
	if err := state.Save(); err != nil {
		t.Fatalf("Failed to save state: %v", err)
	}

	historyPath := filepath.Join(syncStateDir, "history.json")
	historyData, err := os.ReadFile(historyPath)
	if err != nil {
		t.Fatalf("Failed to read history file: %v", err)
	}

	var savedHistory sync.SyncHistory
	if err := json.Unmarshal(historyData, &savedHistory); err != nil {
		t.Fatalf("Failed to parse history file: %v", err)
	}

	if len(savedHistory.Entries) != 10 {
		t.Errorf("Expected 10 entries in saved history, got %d", len(savedHistory.Entries))
	}

	h.LogInfo("History persisted", map[string]interface{}{
		"saved_entries": len(savedHistory.Entries),
	})
	h.EndStep("persist_history")

	t.Log("\n" + h.Summary())
}

// =============================================================================
// Helper Functions
// =============================================================================

// createSyncTestProfile creates a test profile with specific expiry for sync testing.
func createSyncTestProfile(t *testing.T, h *testutil.ExtendedHarness, vaultDir, provider, profileName string, expiry time.Time) {
	t.Helper()

	profileDir := filepath.Join(vaultDir, provider, profileName)
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("Failed to create profile directory: %v", err)
	}

	// Create auth file based on provider
	var authData []byte
	var authFile string

	switch provider {
	case "codex":
		authFile = "auth.json"
		authData, _ = json.MarshalIndent(map[string]interface{}{
			"access_token":  "test-token-" + profileName,
			"refresh_token": "test-refresh-" + profileName,
			"expires_at":    expiry.Unix(),
		}, "", "  ")

	case "claude":
		authFile = ".claude.json"
		authData, _ = json.MarshalIndent(map[string]interface{}{
			"oauthToken": map[string]interface{}{
				"access_token":  "test-token-" + profileName,
				"refresh_token": "test-refresh-" + profileName,
				"token_type":    "Bearer",
				"expiry":        expiry.Format(time.RFC3339),
			},
		}, "", "  ")

	case "gemini":
		authFile = "settings.json"
		authData, _ = json.MarshalIndent(map[string]interface{}{
			"oauth_credentials": map[string]interface{}{
				"access_token":  "test-token-" + profileName,
				"refresh_token": "test-refresh-" + profileName,
				"expiry":        expiry.Format(time.RFC3339),
			},
		}, "", "  ")

	default:
		t.Fatalf("Unknown provider: %s", provider)
	}

	authPath := filepath.Join(profileDir, authFile)
	if err := os.WriteFile(authPath, authData, 0600); err != nil {
		t.Fatalf("Failed to write auth file: %v", err)
	}
}

// extractTestFreshness extracts freshness from a test profile.
func extractTestFreshness(t *testing.T, h *testutil.ExtendedHarness, vaultDir, provider, profileName string) *sync.TokenFreshness {
	t.Helper()

	profileDir := filepath.Join(vaultDir, provider, profileName)

	// Find auth files
	entries, err := os.ReadDir(profileDir)
	if err != nil {
		t.Fatalf("Failed to read profile directory: %v", err)
	}

	var filePaths []string
	for _, entry := range entries {
		if !entry.IsDir() {
			filePaths = append(filePaths, filepath.Join(profileDir, entry.Name()))
		}
	}

	freshness, err := sync.ExtractFreshnessFromFiles(provider, profileName, filePaths)
	if err != nil {
		t.Fatalf("Failed to extract freshness: %v", err)
	}

	return freshness
}

// copyProfile copies a profile from source vault to destination vault.
func copyProfile(srcVault, dstVault, provider, profileName string) error {
	srcDir := filepath.Join(srcVault, provider, profileName)
	dstDir := filepath.Join(dstVault, provider, profileName)

	// Ensure destination directory exists
	if err := os.MkdirAll(dstDir, 0700); err != nil {
		return err
	}

	// Read source files
	entries, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}

	// Copy each file
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		srcPath := filepath.Join(srcDir, entry.Name())
		dstPath := filepath.Join(dstDir, entry.Name())

		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}

		if err := os.WriteFile(dstPath, data, 0600); err != nil {
			return err
		}
	}

	return nil
}
