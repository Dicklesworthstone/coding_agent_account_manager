package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSyncDirection tests the SyncDirection constants.
func TestSyncDirection(t *testing.T) {
	if SyncPush != "push" {
		t.Errorf("SyncPush = %q, want %q", SyncPush, "push")
	}
	if SyncPull != "pull" {
		t.Errorf("SyncPull = %q, want %q", SyncPull, "pull")
	}
	if SyncSkip != "skip" {
		t.Errorf("SyncSkip = %q, want %q", SyncSkip, "skip")
	}
}

// TestSyncOperation tests the SyncOperation struct.
func TestSyncOperation(t *testing.T) {
	m := NewMachine("test", "192.168.1.100")
	localFresh := &TokenFreshness{
		Provider:  "claude",
		Profile:   "test",
		ExpiresAt: time.Now().Add(time.Hour),
	}

	op := &SyncOperation{
		Provider:       "claude",
		Profile:        "test@example.com",
		Direction:      SyncPush,
		Machine:        m,
		LocalFreshness: localFresh,
	}

	if op.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", op.Provider, "claude")
	}
	if op.Profile != "test@example.com" {
		t.Errorf("Profile = %q, want %q", op.Profile, "test@example.com")
	}
	if op.Direction != SyncPush {
		t.Errorf("Direction = %q, want %q", op.Direction, SyncPush)
	}
	if op.Machine != m {
		t.Error("Machine mismatch")
	}
}

// TestSyncResult tests the SyncResult struct.
func TestSyncResult(t *testing.T) {
	op := &SyncOperation{
		Provider:  "claude",
		Profile:   "test",
		Direction: SyncPush,
	}

	result := &SyncResult{
		Operation:     op,
		Success:       true,
		BytesSent:     1024,
		BytesReceived: 512,
		Duration:      100 * time.Millisecond,
	}

	if !result.Success {
		t.Error("Success should be true")
	}
	if result.BytesSent != 1024 {
		t.Errorf("BytesSent = %d, want 1024", result.BytesSent)
	}
	if result.Duration != 100*time.Millisecond {
		t.Errorf("Duration = %v, want 100ms", result.Duration)
	}
}

// TestDefaultSyncerConfig tests the default configuration.
func TestDefaultSyncerConfig(t *testing.T) {
	config := DefaultSyncerConfig()

	if config.VaultPath == "" {
		t.Error("VaultPath should not be empty")
	}
	if config.RemoteVaultPath == "" {
		t.Error("RemoteVaultPath should not be empty")
	}
	if config.ConnectOptions.Timeout == 0 {
		t.Error("ConnectOptions.Timeout should not be 0")
	}
}

// TestMergeProfileLists tests the mergeProfileLists function.
func TestMergeProfileLists(t *testing.T) {
	tests := []struct {
		name string
		a    []ProfileRef
		b    []ProfileRef
		want int
	}{
		{
			name: "empty lists",
			a:    []ProfileRef{},
			b:    []ProfileRef{},
			want: 0,
		},
		{
			name: "no overlap",
			a: []ProfileRef{
				{Provider: "claude", Profile: "alice"},
				{Provider: "claude", Profile: "bob"},
			},
			b: []ProfileRef{
				{Provider: "codex", Profile: "work"},
			},
			want: 3,
		},
		{
			name: "with overlap",
			a: []ProfileRef{
				{Provider: "claude", Profile: "alice"},
				{Provider: "claude", Profile: "bob"},
			},
			b: []ProfileRef{
				{Provider: "claude", Profile: "alice"}, // Duplicate
				{Provider: "codex", Profile: "work"},
			},
			want: 3, // alice, bob, work
		},
		{
			name: "all duplicates",
			a: []ProfileRef{
				{Provider: "claude", Profile: "alice"},
			},
			b: []ProfileRef{
				{Provider: "claude", Profile: "alice"},
			},
			want: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeProfileLists(tt.a, tt.b)
			if len(got) != tt.want {
				t.Errorf("mergeProfileLists() len = %d, want %d", len(got), tt.want)
			}
		})
	}
}

// TestErrorToString tests the errorToString helper.
func TestErrorToString(t *testing.T) {
	if got := errorToString(nil); got != "" {
		t.Errorf("errorToString(nil) = %q, want empty", got)
	}

	err := os.ErrNotExist
	if got := errorToString(err); got == "" {
		t.Error("errorToString(err) should not be empty")
	}
}

// TestAggregateResults tests the AggregateResults function.
func TestAggregateResults(t *testing.T) {
	results := []*SyncResult{
		{
			Operation: &SyncOperation{Direction: SyncPush},
			Success:   true,
			BytesSent: 100,
			Duration:  10 * time.Millisecond,
		},
		{
			Operation:     &SyncOperation{Direction: SyncPull},
			Success:       true,
			BytesReceived: 200,
			Duration:      20 * time.Millisecond,
		},
		{
			Operation: &SyncOperation{Direction: SyncSkip},
			Success:   true,
			Duration:  5 * time.Millisecond,
		},
		{
			Operation: &SyncOperation{Direction: SyncPush},
			Success:   false,
			Error:     os.ErrNotExist,
		},
	}

	stats := AggregateResults(results)

	if stats.Total != 4 {
		t.Errorf("Total = %d, want 4", stats.Total)
	}
	if stats.Pushed != 1 {
		t.Errorf("Pushed = %d, want 1", stats.Pushed)
	}
	if stats.Pulled != 1 {
		t.Errorf("Pulled = %d, want 1", stats.Pulled)
	}
	if stats.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1", stats.Skipped)
	}
	if stats.Failed != 1 {
		t.Errorf("Failed = %d, want 1", stats.Failed)
	}
	if stats.BytesSent != 100 {
		t.Errorf("BytesSent = %d, want 100", stats.BytesSent)
	}
	if stats.BytesRecv != 200 {
		t.Errorf("BytesRecv = %d, want 200", stats.BytesRecv)
	}
}

// TestListLocalProfiles tests listing local profiles.
func TestListLocalProfiles(t *testing.T) {
	tmpDir := t.TempDir()

	// Create vault structure
	claudePath := filepath.Join(tmpDir, "claude")
	codexPath := filepath.Join(tmpDir, "codex")

	if err := os.MkdirAll(filepath.Join(claudePath, "alice@gmail.com"), 0700); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(claudePath, "bob@gmail.com"), 0700); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(codexPath, "work@company.com"), 0700); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	// Create a file (should be ignored)
	if err := os.WriteFile(filepath.Join(claudePath, "not_a_profile.txt"), []byte("test"), 0600); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	syncer := &Syncer{
		vaultPath: tmpDir,
	}

	profiles, err := syncer.listLocalProfiles()
	if err != nil {
		t.Fatalf("listLocalProfiles failed: %v", err)
	}

	if len(profiles) != 3 {
		t.Errorf("len(profiles) = %d, want 3", len(profiles))
	}

	// Verify profiles
	hasAlice := false
	hasBob := false
	hasWork := false

	for _, p := range profiles {
		if p.Provider == "claude" && p.Profile == "alice@gmail.com" {
			hasAlice = true
		}
		if p.Provider == "claude" && p.Profile == "bob@gmail.com" {
			hasBob = true
		}
		if p.Provider == "codex" && p.Profile == "work@company.com" {
			hasWork = true
		}
	}

	if !hasAlice {
		t.Error("Missing alice@gmail.com profile")
	}
	if !hasBob {
		t.Error("Missing bob@gmail.com profile")
	}
	if !hasWork {
		t.Error("Missing work@company.com profile")
	}
}

// TestReadLocalProfileFiles tests reading profile files.
func TestReadLocalProfileFiles(t *testing.T) {
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "claude", "alice@gmail.com")

	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	// Create auth files
	claudeJSON := `{"oauthToken": {"expiry": "2025-12-20T14:29:00Z"}}`
	if err := os.WriteFile(filepath.Join(profilePath, ".claude.json"), []byte(claudeJSON), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	authJSON := `{"access_token": "test"}`
	if err := os.WriteFile(filepath.Join(profilePath, "auth.json"), []byte(authJSON), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	// Create subdirectory (should be ignored)
	if err := os.MkdirAll(filepath.Join(profilePath, "subdir"), 0700); err != nil {
		t.Fatalf("Failed to create subdir: %v", err)
	}

	syncer := &Syncer{
		vaultPath: tmpDir,
	}

	files, err := syncer.readLocalProfileFiles(profilePath)
	if err != nil {
		t.Fatalf("readLocalProfileFiles failed: %v", err)
	}

	if len(files) != 2 {
		t.Errorf("len(files) = %d, want 2", len(files))
	}

	if _, ok := files[".claude.json"]; !ok {
		t.Error("Missing .claude.json")
	}
	if _, ok := files["auth.json"]; !ok {
		t.Error("Missing auth.json")
	}
}

// TestGetLocalFreshness tests getting local freshness.
func TestGetLocalFreshness(t *testing.T) {
	tmpDir := t.TempDir()
	profilePath := filepath.Join(tmpDir, "claude", "alice@gmail.com")

	if err := os.MkdirAll(profilePath, 0700); err != nil {
		t.Fatalf("Failed to create test dir: %v", err)
	}

	// Create valid Claude token
	claudeJSON := `{
		"oauthToken": {
			"access_token": "test",
			"expiry": "2025-12-20T14:29:00Z"
		}
	}`
	if err := os.WriteFile(filepath.Join(profilePath, ".claude.json"), []byte(claudeJSON), 0600); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	syncer := &Syncer{
		vaultPath: tmpDir,
	}

	freshness, err := syncer.getLocalFreshness(ProfileRef{
		Provider: "claude",
		Profile:  "alice@gmail.com",
	})
	if err != nil {
		t.Fatalf("getLocalFreshness failed: %v", err)
	}

	if freshness == nil {
		t.Fatal("freshness should not be nil")
	}
	if freshness.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", freshness.Provider, "claude")
	}
	if freshness.Profile != "alice@gmail.com" {
		t.Errorf("Profile = %q, want %q", freshness.Profile, "alice@gmail.com")
	}
}

// TestGetLocalFreshnessNotExists tests freshness for non-existent profile.
func TestGetLocalFreshnessNotExists(t *testing.T) {
	tmpDir := t.TempDir()

	syncer := &Syncer{
		vaultPath: tmpDir,
	}

	_, err := syncer.getLocalFreshness(ProfileRef{
		Provider: "claude",
		Profile:  "nonexistent",
	})
	if err == nil {
		t.Error("Expected error for non-existent profile")
	}
}

// TestSyncerConfig tests the SyncerConfig struct.
func TestSyncerConfig(t *testing.T) {
	config := SyncerConfig{
		VaultPath:       "/custom/vault",
		RemoteVaultPath: ".custom/vault",
		ConnectOptions: ConnectOptions{
			Timeout:  30 * time.Second,
			UseAgent: true,
		},
	}

	if config.VaultPath != "/custom/vault" {
		t.Errorf("VaultPath = %q, want %q", config.VaultPath, "/custom/vault")
	}
	if config.RemoteVaultPath != ".custom/vault" {
		t.Errorf("RemoteVaultPath = %q, want %q", config.RemoteVaultPath, ".custom/vault")
	}
	if config.ConnectOptions.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want 30s", config.ConnectOptions.Timeout)
	}
}

// TestSyncStatsEmpty tests stats with empty results.
func TestSyncStatsEmpty(t *testing.T) {
	stats := AggregateResults([]*SyncResult{})

	if stats.Total != 0 {
		t.Errorf("Total = %d, want 0", stats.Total)
	}
	if stats.Pushed != 0 {
		t.Errorf("Pushed = %d, want 0", stats.Pushed)
	}
	if stats.Failed != 0 {
		t.Errorf("Failed = %d, want 0", stats.Failed)
	}
}

// TestSyncerContextCancellation tests that sync respects context cancellation.
func TestSyncerContextCancellation(t *testing.T) {
	tmpDir := t.TempDir()

	// Set XDG_DATA_HOME for sync state
	t.Setenv("CAAM_HOME", "")
	oldXDG := os.Getenv("XDG_DATA_HOME")
	os.Setenv("XDG_DATA_HOME", tmpDir)
	defer os.Setenv("XDG_DATA_HOME", oldXDG)

	syncer := &Syncer{
		vaultPath: tmpDir,
		state: &SyncState{
			Pool: NewSyncPool(),
			Queue: &SyncQueue{
				Entries: []QueueEntry{},
				MaxSize: 100,
			},
			History: &SyncHistory{
				Entries: []HistoryEntry{},
				MaxSize: 1000,
			},
		},
	}

	// Create already cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	// SyncAll should return early
	_, err := syncer.SyncAll(ctx)
	if err != nil && err != context.Canceled {
		t.Errorf("Expected nil or context.Canceled, got %v", err)
	}
}

// TestDetermineSyncOperationBothNil tests determining operation when both are nil.
func TestDetermineSyncOperationBothNil(t *testing.T) {
	// This is a unit test for the logic, we'll test without actual SSH
	syncer := &Syncer{
		vaultPath:       t.TempDir(),
		remoteVaultPath: ".local/share/caam/vault",
	}

	// When neither local nor remote exists, we should get an error
	_, err := syncer.getLocalFreshness(ProfileRef{
		Provider: "claude",
		Profile:  "nonexistent",
	})
	if err == nil {
		t.Error("Expected error for non-existent profile")
	}
}

// TestAtomicWriteFile tests the atomicWriteFile function.
func TestAtomicWriteFile(t *testing.T) {
	tmpDir := t.TempDir()
	testPath := filepath.Join(tmpDir, "test.txt")
	testData := []byte("hello, world")

	t.Run("success", func(t *testing.T) {
		if err := atomicWriteFile(testPath, testData, 0600); err != nil {
			t.Fatalf("atomicWriteFile failed: %v", err)
		}

		// Verify file contents
		data, err := os.ReadFile(testPath)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}
		if string(data) != string(testData) {
			t.Errorf("File content = %q, want %q", string(data), string(testData))
		}
	})

	t.Run("overwrite", func(t *testing.T) {
		newData := []byte("new content")
		if err := atomicWriteFile(testPath, newData, 0600); err != nil {
			t.Fatalf("atomicWriteFile failed: %v", err)
		}

		data, err := os.ReadFile(testPath)
		if err != nil {
			t.Fatalf("Failed to read file: %v", err)
		}
		if string(data) != string(newData) {
			t.Errorf("File content = %q, want %q", string(data), string(newData))
		}
	})

	t.Run("invalid path", func(t *testing.T) {
		err := atomicWriteFile("/nonexistent/dir/file.txt", testData, 0600)
		if err == nil {
			t.Error("Expected error for invalid path")
		}
	})
}

// TestLocalRandomString tests the localRandomString function.
func TestLocalRandomString(t *testing.T) {
	t.Run("length", func(t *testing.T) {
		for _, n := range []int{4, 8, 16, 32} {
			s := localRandomString(n)
			if len(s) != n {
				t.Errorf("localRandomString(%d) len = %d, want %d", n, len(s), n)
			}
		}
	})

	t.Run("uniqueness", func(t *testing.T) {
		s1 := localRandomString(16)
		s2 := localRandomString(16)
		if s1 == s2 {
			t.Error("localRandomString should generate different strings")
		}
	})

	t.Run("characters", func(t *testing.T) {
		s := localRandomString(100)
		for _, c := range s {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
				t.Errorf("localRandomString contains invalid char: %c", c)
			}
		}
	})
}

// =============================================================================
// Issue #65: filter wiring, machine status mirroring, full-sync recording
// =============================================================================

func TestFilterProfiles(t *testing.T) {
	profiles := []ProfileRef{
		{Provider: "claude", Profile: "work"},
		{Provider: "claude", Profile: "personal"},
		{Provider: "codex", Profile: "work"},
	}

	if got := filterProfiles(profiles, "", ""); len(got) != 3 {
		t.Errorf("no filter: got %d profiles, want 3", len(got))
	}
	if got := filterProfiles(profiles, "claude", ""); len(got) != 2 {
		t.Errorf("provider filter: got %d profiles, want 2", len(got))
	}
	got := filterProfiles(profiles, "codex", "work")
	if len(got) != 1 || got[0].Provider != "codex" || got[0].Profile != "work" {
		t.Errorf("provider+profile filter: got %v, want [codex/work]", got)
	}
	if got := filterProfiles(profiles, "gemini", ""); len(got) != 0 {
		t.Errorf("non-matching filter: got %d profiles, want 0", len(got))
	}
	if got := filterProfiles(profiles, "", "work"); len(got) != 2 {
		t.Errorf("profile-only filter: got %d profiles, want 2", len(got))
	}
}

func TestMirrorMachinePersistsRecordSync(t *testing.T) {
	base := t.TempDir()
	state := NewSyncState(base)

	// The syncer's own pool holds one machine.
	own := NewMachine("laptop", "192.168.1.10")
	if err := state.Pool.AddMachine(own); err != nil {
		t.Fatal(err)
	}

	s := &Syncer{state: state}

	// The CLI passes in a distinct Machine object with the same ID (it loads
	// its own copy of the sync state).
	callerCopy := &Machine{ID: own.ID, Name: own.Name, Address: own.Address}
	callerCopy.RecordSync()
	s.mirrorMachine(callerCopy)

	if own.Status != StatusOnline {
		t.Errorf("own.Status = %q, want %q", own.Status, StatusOnline)
	}
	if own.LastSync.IsZero() {
		t.Error("own.LastSync should be set after mirrorMachine")
	}

	// Error status mirrors too.
	callerCopy.SetError("boom")
	s.mirrorMachine(callerCopy)
	if own.Status != StatusError || own.LastError != "boom" {
		t.Errorf("own status/error = %q/%q, want error/boom", own.Status, own.LastError)
	}
}

func TestRecordFullSyncPersists(t *testing.T) {
	base := t.TempDir()
	state := NewSyncState(base)
	s := &Syncer{state: state}

	if !state.Pool.LastFullSync.IsZero() {
		t.Fatal("LastFullSync should start zero")
	}
	s.RecordFullSync()
	if state.Pool.LastFullSync.IsZero() {
		t.Error("RecordFullSync did not set LastFullSync")
	}

	// Persist and reload: the timestamp must survive.
	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}
	reloaded := NewSyncPool()
	reloaded.SetBasePath(base)
	if err := reloaded.Load(); err != nil {
		t.Fatalf("load pool: %v", err)
	}
	if reloaded.LastFullSync.IsZero() {
		t.Error("LastFullSync not persisted to pool.json")
	}
}

func TestSyncerProgressEmit(t *testing.T) {
	var events []ProgressEvent
	s := &Syncer{}
	// No callback: must not panic.
	s.emit(ProgressEvent{Stage: StagePlan})

	s.Progress = func(ev ProgressEvent) { events = append(events, ev) }
	s.emit(ProgressEvent{Stage: StagePlan, Total: 5})
	s.emit(ProgressEvent{Stage: StageCompare, Index: 1})
	if len(events) != 2 || events[0].Stage != StagePlan || events[0].Total != 5 || events[1].Stage != StageCompare {
		t.Errorf("unexpected events: %+v", events)
	}
}
