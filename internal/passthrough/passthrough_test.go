package passthrough

import (
	"os"
	"path/filepath"
	"testing"
)

// =============================================================================
// DefaultPassthroughs Tests
// =============================================================================

func TestDefaultPassthroughs(t *testing.T) {
	if len(DefaultPassthroughs) == 0 {
		t.Error("DefaultPassthroughs should not be empty")
	}

	// Check for expected common paths
	expected := map[string]bool{
		".ssh":       false,
		".gitconfig": false,
		".gnupg":     false,
	}

	for _, p := range DefaultPassthroughs {
		if _, ok := expected[p]; ok {
			expected[p] = true
		}
	}

	for path, found := range expected {
		if !found {
			t.Errorf("DefaultPassthroughs should contain %q", path)
		}
	}
}

// =============================================================================
// NewManager Tests
// =============================================================================

func TestNewManager(t *testing.T) {
	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}

	if m == nil {
		t.Fatal("NewManager() returned nil")
	}

	// Should have default passthroughs
	if len(m.passthroughs) != len(DefaultPassthroughs) {
		t.Errorf("passthroughs count = %d, want %d", len(m.passthroughs), len(DefaultPassthroughs))
	}

	// Should have real home set
	homeDir, _ := os.UserHomeDir()
	if m.realHome != homeDir {
		t.Errorf("realHome = %q, want %q", m.realHome, homeDir)
	}
}

func TestNewManagerWithPaths(t *testing.T) {
	customPaths := []string{".custom1", ".custom2"}

	m, err := NewManagerWithPaths(customPaths)
	if err != nil {
		t.Fatalf("NewManagerWithPaths() error = %v", err)
	}

	if len(m.passthroughs) != 2 {
		t.Errorf("passthroughs count = %d, want 2", len(m.passthroughs))
	}

	if m.passthroughs[0] != ".custom1" || m.passthroughs[1] != ".custom2" {
		t.Errorf("passthroughs = %v, want [.custom1, .custom2]", m.passthroughs)
	}
}

// =============================================================================
// Passthroughs and RealHome Accessor Tests
// =============================================================================

func TestPassthroughs(t *testing.T) {
	m, _ := NewManagerWithPaths([]string{".test1", ".test2"})
	paths := m.Passthroughs()

	if len(paths) != 2 {
		t.Errorf("Passthroughs() returned %d items, want 2", len(paths))
	}
}

func TestRealHome(t *testing.T) {
	m, _ := NewManager()
	homeDir, _ := os.UserHomeDir()

	if m.RealHome() != homeDir {
		t.Errorf("RealHome() = %q, want %q", m.RealHome(), homeDir)
	}
}

// =============================================================================
// AddPassthrough Tests
// =============================================================================

func TestAddPassthrough(t *testing.T) {
	m, _ := NewManagerWithPaths([]string{".existing"})

	t.Run("adds new path", func(t *testing.T) {
		m.AddPassthrough(".new")
		if len(m.passthroughs) != 2 {
			t.Errorf("passthroughs count = %d, want 2", len(m.passthroughs))
		}
	})

	t.Run("ignores duplicates", func(t *testing.T) {
		m.AddPassthrough(".existing")
		if len(m.passthroughs) != 2 {
			t.Errorf("passthroughs count = %d, want 2 (should ignore duplicate)", len(m.passthroughs))
		}
	})
}

// =============================================================================
// RemovePassthrough Tests
// =============================================================================

func TestRemovePassthrough(t *testing.T) {
	m, _ := NewManagerWithPaths([]string{".a", ".b", ".c"})

	t.Run("removes existing path", func(t *testing.T) {
		m.RemovePassthrough(".b")
		if len(m.passthroughs) != 2 {
			t.Errorf("passthroughs count = %d, want 2", len(m.passthroughs))
		}

		// Verify .b is gone
		for _, p := range m.passthroughs {
			if p == ".b" {
				t.Error("path .b should have been removed")
			}
		}
	})

	t.Run("handles non-existent path gracefully", func(t *testing.T) {
		count := len(m.passthroughs)
		m.RemovePassthrough(".nonexistent")
		if len(m.passthroughs) != count {
			t.Error("removing non-existent path should not change count")
		}
	})
}

// =============================================================================
// SetupPassthroughs Tests
// =============================================================================

func TestSetupPassthroughs(t *testing.T) {
	// Create a fake "real home" with test files
	realHome := t.TempDir()
	pseudoHome := t.TempDir()

	// Create test files in "real home"
	testFile := filepath.Join(realHome, ".testfile")
	if err := os.WriteFile(testFile, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}

	testDir := filepath.Join(realHome, ".testdir")
	if err := os.MkdirAll(testDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create manager with custom real home
	m := &Manager{
		passthroughs: []string{".testfile", ".testdir", ".nonexistent"},
		realHome:     realHome,
	}

	t.Run("creates symlinks for existing files", func(t *testing.T) {
		if err := m.SetupPassthroughs(pseudoHome); err != nil {
			t.Fatalf("SetupPassthroughs() error = %v", err)
		}

		// Verify symlink to file
		linkPath := filepath.Join(pseudoHome, ".testfile")
		target, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("readlink error = %v", err)
		}
		if target != testFile {
			t.Errorf("symlink target = %q, want %q", target, testFile)
		}

		// Verify symlink to directory
		dirLinkPath := filepath.Join(pseudoHome, ".testdir")
		target, err = os.Readlink(dirLinkPath)
		if err != nil {
			t.Fatalf("readlink error = %v", err)
		}
		if target != testDir {
			t.Errorf("symlink target = %q, want %q", target, testDir)
		}
	})

	t.Run("skips non-existent sources", func(t *testing.T) {
		// .nonexistent should not create a symlink
		linkPath := filepath.Join(pseudoHome, ".nonexistent")
		if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
			t.Error("symlink should not be created for non-existent source")
		}
	})

	t.Run("replaces existing symlinks", func(t *testing.T) {
		// Run setup again - should not error
		if err := m.SetupPassthroughs(pseudoHome); err != nil {
			t.Fatalf("SetupPassthroughs() second run error = %v", err)
		}

		// Verify symlink still valid
		linkPath := filepath.Join(pseudoHome, ".testfile")
		target, err := os.Readlink(linkPath)
		if err != nil {
			t.Fatalf("readlink error = %v", err)
		}
		if target != testFile {
			t.Errorf("symlink target = %q, want %q", target, testFile)
		}
	})

	t.Run("replaces existing regular file", func(t *testing.T) {
		// Create a regular file where symlink should be
		conflictPath := filepath.Join(pseudoHome, ".conflict")
		srcPath := filepath.Join(realHome, ".conflict")

		// Create source file
		if err := os.WriteFile(srcPath, []byte("source"), 0600); err != nil {
			t.Fatal(err)
		}

		// Create conflicting regular file in pseudo home
		if err := os.WriteFile(conflictPath, []byte("conflict"), 0600); err != nil {
			t.Fatal(err)
		}

		// Update manager to include the conflict path
		m.passthroughs = append(m.passthroughs, ".conflict")

		if err := m.SetupPassthroughs(pseudoHome); err != nil {
			t.Fatalf("SetupPassthroughs() error = %v", err)
		}

		// Should now be a symlink
		info, err := os.Lstat(conflictPath)
		if err != nil {
			t.Fatalf("lstat error = %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Error("should be a symlink after SetupPassthroughs")
		}
	})

	t.Run("skips existing directories", func(t *testing.T) {
		conflictDir := filepath.Join(pseudoHome, ".conflictdir")
		srcDir := filepath.Join(realHome, ".conflictdir")

		if err := os.MkdirAll(srcDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(conflictDir, 0700); err != nil {
			t.Fatal(err)
		}

		m2 := &Manager{
			passthroughs: []string{".conflictdir"},
			realHome:     realHome,
		}

		if err := m2.SetupPassthroughs(pseudoHome); err != nil {
			t.Fatalf("SetupPassthroughs() error = %v", err)
		}

		info, err := os.Lstat(conflictDir)
		if err != nil {
			t.Fatalf("lstat error = %v", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			t.Error("should not replace existing directory with symlink")
		}
	})

	t.Run("creates nested parent directories", func(t *testing.T) {
		nestedSrc := filepath.Join(realHome, ".config", "nested", "file")
		if err := os.MkdirAll(filepath.Dir(nestedSrc), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(nestedSrc, []byte("nested"), 0600); err != nil {
			t.Fatal(err)
		}

		m2 := &Manager{
			passthroughs: []string{".config/nested/file"},
			realHome:     realHome,
		}

		newPseudo := t.TempDir()
		if err := m2.SetupPassthroughs(newPseudo); err != nil {
			t.Fatalf("SetupPassthroughs() error = %v", err)
		}

		linkPath := filepath.Join(newPseudo, ".config", "nested", "file")
		if _, err := os.Lstat(linkPath); err != nil {
			t.Errorf("nested symlink should exist: %v", err)
		}
	})
}

// =============================================================================
// VerifyPassthroughs Tests
// =============================================================================

func TestVerifyPassthroughs(t *testing.T) {
	realHome := t.TempDir()
	pseudoHome := t.TempDir()

	// Create source file
	srcFile := filepath.Join(realHome, ".srcfile")
	if err := os.WriteFile(srcFile, []byte("src"), 0600); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		passthroughs: []string{".srcfile", ".missing"},
		realHome:     realHome,
	}

	// Setup the symlink first
	if err := m.SetupPassthroughs(pseudoHome); err != nil {
		t.Fatal(err)
	}

	t.Run("reports valid symlinks", func(t *testing.T) {
		statuses, err := m.VerifyPassthroughs(pseudoHome)
		if err != nil {
			t.Fatalf("VerifyPassthroughs() error = %v", err)
		}

		// Find status for .srcfile
		var found *Status
		for i := range statuses {
			if statuses[i].Path == ".srcfile" {
				found = &statuses[i]
				break
			}
		}

		if found == nil {
			t.Fatal("status for .srcfile not found")
		}
		if !found.SourceExists {
			t.Error("SourceExists should be true")
		}
		if !found.LinkExists {
			t.Error("LinkExists should be true")
		}
		if !found.LinkValid {
			t.Error("LinkValid should be true")
		}
		if found.Error != "" {
			t.Errorf("Error should be empty, got %q", found.Error)
		}
	})

	t.Run("reports missing source", func(t *testing.T) {
		statuses, _ := m.VerifyPassthroughs(pseudoHome)

		var found *Status
		for i := range statuses {
			if statuses[i].Path == ".missing" {
				found = &statuses[i]
				break
			}
		}

		if found == nil {
			t.Fatal("status for .missing not found")
		}
		if found.SourceExists {
			t.Error("SourceExists should be false for missing source")
		}
	})

	t.Run("reports invalid symlink target", func(t *testing.T) {
		// Create a symlink pointing to wrong target
		wrongLinkPath := filepath.Join(pseudoHome, ".wronglink")
		wrongSrcPath := filepath.Join(realHome, ".wronglink")
		if err := os.WriteFile(wrongSrcPath, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		// Create symlink to different location
		if err := os.Symlink("/wrong/target", wrongLinkPath); err != nil {
			t.Fatal(err)
		}

		m2 := &Manager{
			passthroughs: []string{".wronglink"},
			realHome:     realHome,
		}

		statuses, _ := m2.VerifyPassthroughs(pseudoHome)
		if len(statuses) == 0 {
			t.Fatal("expected status")
		}

		s := statuses[0]
		if s.LinkValid {
			t.Error("LinkValid should be false for wrong target")
		}
		if s.Error == "" {
			t.Error("Error should describe the wrong target")
		}
	})

	t.Run("reports non-symlink file", func(t *testing.T) {
		regularFile := filepath.Join(pseudoHome, ".regularfile")
		regularSrc := filepath.Join(realHome, ".regularfile")
		if err := os.WriteFile(regularSrc, []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(regularFile, []byte("regular"), 0600); err != nil {
			t.Fatal(err)
		}

		m2 := &Manager{
			passthroughs: []string{".regularfile"},
			realHome:     realHome,
		}

		statuses, _ := m2.VerifyPassthroughs(pseudoHome)
		if len(statuses) == 0 {
			t.Fatal("expected status")
		}

		s := statuses[0]
		if s.LinkValid {
			t.Error("LinkValid should be false for regular file")
		}
		if s.Error != "exists but is not a symlink" {
			t.Errorf("Error = %q, want 'exists but is not a symlink'", s.Error)
		}
	})
}

// =============================================================================
// RemovePassthroughs Tests
// =============================================================================

func TestRemovePassthroughs(t *testing.T) {
	realHome := t.TempDir()
	pseudoHome := t.TempDir()

	// Create source and symlink
	srcFile := filepath.Join(realHome, ".toremove")
	if err := os.WriteFile(srcFile, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}

	m := &Manager{
		passthroughs: []string{".toremove", ".notcreated"},
		realHome:     realHome,
	}

	// Setup symlinks
	if err := m.SetupPassthroughs(pseudoHome); err != nil {
		t.Fatal(err)
	}

	t.Run("removes existing symlinks", func(t *testing.T) {
		if err := m.RemovePassthroughs(pseudoHome); err != nil {
			t.Fatalf("RemovePassthroughs() error = %v", err)
		}

		linkPath := filepath.Join(pseudoHome, ".toremove")
		if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
			t.Error("symlink should be removed")
		}
	})

	t.Run("ignores non-existent files", func(t *testing.T) {
		// Running again should not error
		if err := m.RemovePassthroughs(pseudoHome); err != nil {
			t.Errorf("RemovePassthroughs() should not error for non-existent: %v", err)
		}
	})

	t.Run("preserves regular files", func(t *testing.T) {
		// Create a regular file (not a symlink)
		regularFile := filepath.Join(pseudoHome, ".regularkeep")
		if err := os.WriteFile(regularFile, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}

		m2 := &Manager{
			passthroughs: []string{".regularkeep"},
			realHome:     realHome,
		}

		if err := m2.RemovePassthroughs(pseudoHome); err != nil {
			t.Fatal(err)
		}

		// Regular file should still exist
		if _, err := os.Stat(regularFile); os.IsNotExist(err) {
			t.Error("regular file should not be removed")
		}
	})
}

// =============================================================================
// Status Struct Tests
// =============================================================================

func TestStatus(t *testing.T) {
	s := Status{
		Path:         ".test",
		SourceExists: true,
		LinkExists:   true,
		LinkValid:    false,
		Error:        "test error",
	}

	if s.Path != ".test" {
		t.Error("Path not set correctly")
	}
	if !s.SourceExists {
		t.Error("SourceExists not set correctly")
	}
	if !s.LinkExists {
		t.Error("LinkExists not set correctly")
	}
	if s.LinkValid {
		t.Error("LinkValid not set correctly")
	}
	if s.Error != "test error" {
		t.Error("Error not set correctly")
	}
}

// =============================================================================
// Integration Tests
// =============================================================================

func TestSetupVerifyRemoveCycle(t *testing.T) {
	realHome := t.TempDir()
	pseudoHome := t.TempDir()

	// Create test files
	files := []string{".a", ".b", ".config/nested"}
	for _, f := range files {
		path := filepath.Join(realHome, f)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(f), 0600); err != nil {
			t.Fatal(err)
		}
	}

	m := &Manager{
		passthroughs: files,
		realHome:     realHome,
	}

	// Setup
	if err := m.SetupPassthroughs(pseudoHome); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Verify all valid
	statuses, err := m.VerifyPassthroughs(pseudoHome)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}

	for _, s := range statuses {
		if !s.SourceExists {
			t.Errorf("%s: SourceExists should be true", s.Path)
		}
		if !s.LinkExists {
			t.Errorf("%s: LinkExists should be true", s.Path)
		}
		if !s.LinkValid {
			t.Errorf("%s: LinkValid should be true", s.Path)
		}
	}

	// Remove
	if err := m.RemovePassthroughs(pseudoHome); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	// Verify all gone
	for _, f := range files {
		linkPath := filepath.Join(pseudoHome, f)
		if _, err := os.Lstat(linkPath); !os.IsNotExist(err) {
			t.Errorf("symlink %s should be removed", f)
		}
	}
}

func TestManagerWithEmptyPaths(t *testing.T) {
	m, err := NewManagerWithPaths([]string{})
	if err != nil {
		t.Fatalf("NewManagerWithPaths() error = %v", err)
	}

	pseudoHome := t.TempDir()

	// Setup with empty paths should succeed
	if err := m.SetupPassthroughs(pseudoHome); err != nil {
		t.Errorf("SetupPassthroughs() with empty paths error = %v", err)
	}

	// Verify should return empty
	statuses, err := m.VerifyPassthroughs(pseudoHome)
	if err != nil {
		t.Errorf("VerifyPassthroughs() error = %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("VerifyPassthroughs() returned %d statuses, want 0", len(statuses))
	}

	// Remove should succeed
	if err := m.RemovePassthroughs(pseudoHome); err != nil {
		t.Errorf("RemovePassthroughs() error = %v", err)
	}
}
