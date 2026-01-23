package cmd

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGetTracePaths(t *testing.T) {
	tests := []struct {
		name       string
		agent      string
		noProjects bool
		wantErr    bool
	}{
		{
			name:       "claude paths",
			agent:      "claude",
			noProjects: false,
			wantErr:    false,
		},
		{
			name:       "claude no projects",
			agent:      "claude",
			noProjects: true,
			wantErr:    false,
		},
		{
			name:       "codex paths",
			agent:      "codex",
			noProjects: false,
			wantErr:    false,
		},
		{
			name:       "gemini paths",
			agent:      "gemini",
			noProjects: false,
			wantErr:    false,
		},
		{
			name:       "unsupported agent",
			agent:      "unsupported",
			noProjects: false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			paths, err := getTracePaths(tt.agent, tt.noProjects, nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("getTracePaths() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr && len(paths) == 0 {
				t.Error("getTracePaths() returned empty paths")
			}
		})
	}
}

func TestTakeSnapshot(t *testing.T) {
	// Create a temp directory with test files
	tmpDir := t.TempDir()

	// Create some test files
	testFiles := map[string]string{
		"file1.txt":           "content1",
		"file2.json":          `{"key": "value"}`,
		"subdir/nested.txt":   "nested content",
		"subdir/another.json": `{"nested": true}`,
	}

	for path, content := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	// Take snapshot
	snapshot := takeSnapshot([]string{tmpDir}, nil)

	// Verify snapshot
	if len(snapshot.Files) == 0 {
		t.Error("takeSnapshot() returned empty files")
	}

	// Check that timestamp is set
	if snapshot.Timestamp == "" {
		t.Error("takeSnapshot() timestamp not set")
	}

	// Check that we captured the expected files
	foundFiles := 0
	for path := range snapshot.Files {
		if !snapshot.Files[path].IsDir {
			foundFiles++
		}
	}
	if foundFiles != len(testFiles) {
		t.Errorf("takeSnapshot() found %d files, want %d", foundFiles, len(testFiles))
	}
}

func TestTakeSnapshotWithExcludes(t *testing.T) {
	tmpDir := t.TempDir()

	// Create test files including some to exclude
	testFiles := []string{
		"keep.txt",
		"keep.json",
		"exclude.log",
		"subdir/keep.txt",
		"subdir/exclude.log",
	}

	for _, path := range testFiles {
		fullPath := filepath.Join(tmpDir, path)
		dir := filepath.Dir(fullPath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("failed to create dir: %v", err)
		}
		if err := os.WriteFile(fullPath, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	// Take snapshot with exclusions
	snapshot := takeSnapshot([]string{tmpDir}, []string{"*.log"})

	// Check that .log files are excluded
	for path := range snapshot.Files {
		if filepath.Ext(path) == ".log" {
			t.Errorf("takeSnapshot() included excluded file: %s", path)
		}
	}
}

func TestComputeChanges(t *testing.T) {
	now := time.Now().Format(time.RFC3339)

	before := FileSnapshot{
		Timestamp: now,
		Files: map[string]FileInfo{
			"/test/unchanged.txt": {Path: "/test/unchanged.txt", Size: 100, Hash: "abc123"},
			"/test/modified.txt":  {Path: "/test/modified.txt", Size: 50, Hash: "old123"},
			"/test/removed.txt":   {Path: "/test/removed.txt", Size: 75, Hash: "remove123"},
		},
	}

	after := FileSnapshot{
		Timestamp: now,
		Files: map[string]FileInfo{
			"/test/unchanged.txt": {Path: "/test/unchanged.txt", Size: 100, Hash: "abc123"},
			"/test/modified.txt":  {Path: "/test/modified.txt", Size: 60, Hash: "new456"},
			"/test/added.txt":     {Path: "/test/added.txt", Size: 25, Hash: "add789"},
		},
	}

	changes := computeChanges(before, after)

	// Check added
	if len(changes.Added) != 1 {
		t.Errorf("computeChanges() added = %d, want 1", len(changes.Added))
	} else if changes.Added[0].Path != "/test/added.txt" {
		t.Errorf("computeChanges() added path = %s, want /test/added.txt", changes.Added[0].Path)
	}

	// Check modified
	if len(changes.Modified) != 1 {
		t.Errorf("computeChanges() modified = %d, want 1", len(changes.Modified))
	} else if changes.Modified[0].Path != "/test/modified.txt" {
		t.Errorf("computeChanges() modified path = %s, want /test/modified.txt", changes.Modified[0].Path)
	}

	// Check removed
	if len(changes.Removed) != 1 {
		t.Errorf("computeChanges() removed = %d, want 1", len(changes.Removed))
	} else if changes.Removed[0].Path != "/test/removed.txt" {
		t.Errorf("computeChanges() removed path = %s, want /test/removed.txt", changes.Removed[0].Path)
	}
}

func TestDeriveWatchRules(t *testing.T) {
	changes := TraceChanges{
		Added: []FileChange{
			{Path: "/home/user/.claude/.credentials.json", ChangeType: "added", SizeAfter: 100},
		},
		Modified: []FileChange{
			{Path: "/home/user/.claude/settings.json", ChangeType: "modified", SizeBefore: 50, SizeAfter: 60},
		},
	}

	rules := deriveWatchRules("claude", changes)

	if len(rules) == 0 {
		t.Error("deriveWatchRules() returned empty rules")
	}

	// Check that credentials file gets priority 0
	foundCredentials := false
	for _, r := range rules {
		if filepath.Base(r.Path) == ".credentials.json" && r.Priority == 0 {
			foundCredentials = true
			break
		}
	}
	if !foundCredentials {
		t.Error("deriveWatchRules() did not prioritize .credentials.json")
	}
}

func TestComputeSummary(t *testing.T) {
	before := FileSnapshot{
		Files: map[string]FileInfo{
			"/test/a.txt": {Size: 100},
			"/test/b.txt": {Size: 50},
		},
	}

	after := FileSnapshot{
		Files: map[string]FileInfo{
			"/test/a.txt": {Size: 100},
			"/test/c.txt": {Size: 75},
		},
	}

	changes := TraceChanges{
		Added:    []FileChange{{Path: "/test/c.txt", SizeAfter: 75}},
		Removed:  []FileChange{{Path: "/test/b.txt", SizeBefore: 50}},
		Modified: []FileChange{},
	}

	summary := computeSummary(before, after, changes)

	if summary.FilesAdded != 1 {
		t.Errorf("computeSummary() FilesAdded = %d, want 1", summary.FilesAdded)
	}
	if summary.FilesRemoved != 1 {
		t.Errorf("computeSummary() FilesRemoved = %d, want 1", summary.FilesRemoved)
	}
	if summary.FilesModified != 0 {
		t.Errorf("computeSummary() FilesModified = %d, want 0", summary.FilesModified)
	}
	if summary.BytesChanged != 125 { // 75 added + 50 removed
		t.Errorf("computeSummary() BytesChanged = %d, want 125", summary.BytesChanged)
	}
}

func TestHashFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "test content for hashing"

	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	hash, err := hashFile(testFile)
	if err != nil {
		t.Fatalf("hashFile() error = %v", err)
	}

	if hash == "" {
		t.Error("hashFile() returned empty hash")
	}

	// Hash should be consistent
	hash2, _ := hashFile(testFile)
	if hash != hash2 {
		t.Error("hashFile() returned inconsistent hash")
	}
}

func TestCreateFileInfo(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.txt")
	content := "test content"

	if err := os.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	info, err := os.Stat(testFile)
	if err != nil {
		t.Fatalf("failed to stat test file: %v", err)
	}

	fileInfo := createFileInfo(testFile, info)

	if fileInfo.Path != testFile {
		t.Errorf("createFileInfo() Path = %s, want %s", fileInfo.Path, testFile)
	}
	if fileInfo.Size != int64(len(content)) {
		t.Errorf("createFileInfo() Size = %d, want %d", fileInfo.Size, len(content))
	}
	if fileInfo.IsDir {
		t.Error("createFileInfo() IsDir = true, want false")
	}
	if fileInfo.Hash == "" {
		t.Error("createFileInfo() Hash is empty")
	}
	if fileInfo.ModTime == "" {
		t.Error("createFileInfo() ModTime is empty")
	}
}
