package authfile

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestNewVault(t *testing.T) {
	v := NewVault("/some/path")
	if v == nil {
		t.Fatal("NewVault returned nil")
	}
	if v.basePath != "/some/path" {
		t.Errorf("basePath = %q, want %q", v.basePath, "/some/path")
	}
}

func TestDefaultVaultPath(t *testing.T) {
	// Save and restore environment
	origXDG := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("XDG_DATA_HOME", origXDG)

	t.Run("with XDG_DATA_HOME set", func(t *testing.T) {
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		path := DefaultVaultPath()
		want := "/custom/data/caam/vault"
		if path != want {
			t.Errorf("DefaultVaultPath() = %q, want %q", path, want)
		}
	})

	t.Run("without XDG_DATA_HOME", func(t *testing.T) {
		os.Unsetenv("XDG_DATA_HOME")
		path := DefaultVaultPath()
		homeDir, _ := os.UserHomeDir()
		want := filepath.Join(homeDir, ".local", "share", "caam", "vault")
		if path != want {
			t.Errorf("DefaultVaultPath() = %q, want %q", path, want)
		}
	})
}

func TestVaultProfilePath(t *testing.T) {
	v := NewVault("/vault")
	path := v.ProfilePath("claude", "work-1")
	want := "/vault/claude/work-1"
	if path != want {
		t.Errorf("ProfilePath() = %q, want %q", path, want)
	}
}

func TestVaultBackupPath(t *testing.T) {
	v := NewVault("/vault")
	path := v.BackupPath("claude", "work-1", "auth.json")
	want := "/vault/claude/work-1/auth.json"
	if path != want {
		t.Errorf("BackupPath() = %q, want %q", path, want)
	}
}

func TestVaultBackup(t *testing.T) {
	t.Run("successful backup with required file", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		// Create auth file
		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		content := []byte(`{"token": "secret123"}`)
		if err := os.WriteFile(authFile, content, 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		if err := v.Backup(fileSet, "profile1"); err != nil {
			t.Fatalf("Backup() error = %v", err)
		}

		// Verify backup was created
		backupPath := v.BackupPath("testtool", "profile1", "auth.json")
		backedUp, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("reading backup: %v", err)
		}
		if string(backedUp) != string(content) {
			t.Errorf("backup content = %q, want %q", backedUp, content)
		}

		// Verify metadata was written
		metaPath := filepath.Join(vaultDir, "testtool", "profile1", "meta.json")
		if _, err := os.Stat(metaPath); err != nil {
			t.Errorf("metadata file not created: %v", err)
		}
	})

	t.Run("missing required file fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/auth.json", Required: true},
			},
		}

		err := v.Backup(fileSet, "profile1")
		if err == nil {
			t.Fatal("Backup() should fail for missing required file")
		}
	})

	t.Run("optional file missing succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		// Create required file only
		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		requiredFile := filepath.Join(authDir, "required.json")
		if err := os.WriteFile(requiredFile, []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: requiredFile, Required: true},
				{Tool: "testtool", Path: filepath.Join(authDir, "optional.json"), Required: false},
			},
		}

		if err := v.Backup(fileSet, "profile1"); err != nil {
			t.Fatalf("Backup() error = %v", err)
		}
	})

	t.Run("no files to backup fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/auth.json", Required: false},
			},
		}

		err := v.Backup(fileSet, "profile1")
		if err == nil {
			t.Fatal("Backup() should fail when no files to backup")
		}
	})
}

func TestVaultRestore(t *testing.T) {
	t.Run("successful restore", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		// Create backup in vault
		profileDir := filepath.Join(vaultDir, "testtool", "profile1")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		backupContent := []byte(`{"token": "restored"}`)
		backupFile := filepath.Join(profileDir, "auth.json")
		if err := os.WriteFile(backupFile, backupContent, 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		authFile := filepath.Join(authDir, "auth.json")
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		if err := v.Restore(fileSet, "profile1"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}

		// Verify restore
		restored, err := os.ReadFile(authFile)
		if err != nil {
			t.Fatalf("reading restored file: %v", err)
		}
		if string(restored) != string(backupContent) {
			t.Errorf("restored content = %q, want %q", restored, backupContent)
		}
	})

	t.Run("profile not found fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/some/auth.json", Required: true},
			},
		}

		err := v.Restore(fileSet, "nonexistent")
		if err == nil {
			t.Fatal("Restore() should fail for nonexistent profile")
		}
	})

	t.Run("missing required backup fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")

		// Create profile dir but without the required file
		profileDir := filepath.Join(vaultDir, "testtool", "profile1")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/some/auth.json", Required: true},
			},
		}

		err := v.Restore(fileSet, "profile1")
		if err == nil {
			t.Fatal("Restore() should fail for missing required backup")
		}
	})

	t.Run("optional backup missing succeeds", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		// Create profile dir with required file only
		profileDir := filepath.Join(vaultDir, "testtool", "profile1")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "required.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: filepath.Join(authDir, "required.json"), Required: true},
				{Tool: "testtool", Path: filepath.Join(authDir, "optional.json"), Required: false},
			},
		}

		if err := v.Restore(fileSet, "profile1"); err != nil {
			t.Fatalf("Restore() error = %v", err)
		}
	})
}

func TestVaultList(t *testing.T) {
	t.Run("empty vault returns empty list", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		profiles, err := v.List("testtool")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		if len(profiles) != 0 {
			t.Errorf("List() = %v, want empty", profiles)
		}
	})

	t.Run("returns profiles", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create profile directories
		profiles := []string{"profile1", "profile2", "profile3"}
		for _, p := range profiles {
			if err := os.MkdirAll(filepath.Join(tmpDir, "testtool", p), 0700); err != nil {
				t.Fatal(err)
			}
		}

		v := NewVault(tmpDir)
		result, err := v.List("testtool")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}

		if len(result) != len(profiles) {
			t.Errorf("List() returned %d profiles, want %d", len(result), len(profiles))
		}
	})

	t.Run("ignores files in tool directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create profile dir and a file (not a dir)
		if err := os.MkdirAll(filepath.Join(tmpDir, "testtool", "profile1"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tmpDir, "testtool", "somefile.txt"), []byte(""), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(tmpDir)
		result, err := v.List("testtool")
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}

		if len(result) != 1 {
			t.Errorf("List() returned %d profiles, want 1", len(result))
		}
	})
}

func TestVaultListAll(t *testing.T) {
	t.Run("empty vault returns empty map", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		result, err := v.ListAll()
		if err != nil {
			t.Fatalf("ListAll() error = %v", err)
		}
		if len(result) != 0 {
			t.Errorf("ListAll() = %v, want empty", result)
		}
	})

	t.Run("returns all tools and profiles", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create profiles for multiple tools
		tools := map[string][]string{
			"claude": {"work", "personal"},
			"codex":  {"main"},
			"gemini": {"account1", "account2", "account3"},
		}

		for tool, profiles := range tools {
			for _, p := range profiles {
				if err := os.MkdirAll(filepath.Join(tmpDir, tool, p), 0700); err != nil {
					t.Fatal(err)
				}
			}
		}

		v := NewVault(tmpDir)
		result, err := v.ListAll()
		if err != nil {
			t.Fatalf("ListAll() error = %v", err)
		}

		if len(result) != len(tools) {
			t.Errorf("ListAll() returned %d tools, want %d", len(result), len(tools))
		}

		for tool, expectedProfiles := range tools {
			gotProfiles, ok := result[tool]
			if !ok {
				t.Errorf("tool %q not found in result", tool)
				continue
			}
			if len(gotProfiles) != len(expectedProfiles) {
				t.Errorf("tool %q: got %d profiles, want %d", tool, len(gotProfiles), len(expectedProfiles))
			}
		}
	})
}

func TestVaultDelete(t *testing.T) {
	t.Run("deletes profile directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create profile with files
		profileDir := filepath.Join(tmpDir, "testtool", "profile1")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(tmpDir)
		if err := v.Delete("testtool", "profile1"); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}

		// Verify deletion
		if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
			t.Error("profile directory should be deleted")
		}
	})

	t.Run("deleting nonexistent profile is noop", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Should not error
		if err := v.Delete("testtool", "nonexistent"); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
	})
}

func TestVaultActiveProfile(t *testing.T) {
	t.Run("returns matching profile", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		// Create auth file with specific content
		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		content := []byte(`{"token": "match-this-content"}`)
		if err := os.WriteFile(authFile, content, 0600); err != nil {
			t.Fatal(err)
		}

		// Create matching profile in vault
		profileDir := filepath.Join(vaultDir, "testtool", "myprofile")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), content, 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		profile, err := v.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile() error = %v", err)
		}
		if profile != "myprofile" {
			t.Errorf("ActiveProfile() = %q, want %q", profile, "myprofile")
		}
	})

	t.Run("returns empty for no matching profile", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		// Create auth file
		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		if err := os.WriteFile(authFile, []byte(`{"token": "current"}`), 0600); err != nil {
			t.Fatal(err)
		}

		// Create non-matching profile
		profileDir := filepath.Join(vaultDir, "testtool", "other")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"token": "different"}`), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		profile, err := v.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile() error = %v", err)
		}
		if profile != "" {
			t.Errorf("ActiveProfile() = %q, want empty string", profile)
		}
	})

	t.Run("returns empty for no auth files", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/auth.json", Required: true},
			},
		}

		profile, err := v.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile() error = %v", err)
		}
		if profile != "" {
			t.Errorf("ActiveProfile() = %q, want empty string", profile)
		}
	})
}

func TestHasAuthFiles(t *testing.T) {
	t.Run("returns true when required file exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		authFile := filepath.Join(tmpDir, "auth.json")
		if err := os.WriteFile(authFile, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}

		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		if !HasAuthFiles(fileSet) {
			t.Error("HasAuthFiles() = false, want true")
		}
	})

	t.Run("returns false when required file missing", func(t *testing.T) {
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/auth.json", Required: true},
			},
		}

		if HasAuthFiles(fileSet) {
			t.Error("HasAuthFiles() = true, want false")
		}
	})

	t.Run("ignores optional files", func(t *testing.T) {
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/optional.json", Required: false},
			},
		}

		// No required files means no auth present
		if HasAuthFiles(fileSet) {
			t.Error("HasAuthFiles() = true, want false (no required files)")
		}
	})
}

func TestClearAuthFiles(t *testing.T) {
	t.Run("removes existing files", func(t *testing.T) {
		tmpDir := t.TempDir()

		// Create auth files
		authFile1 := filepath.Join(tmpDir, "auth1.json")
		authFile2 := filepath.Join(tmpDir, "auth2.json")
		if err := os.WriteFile(authFile1, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(authFile2, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}

		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile1, Required: true},
				{Tool: "testtool", Path: authFile2, Required: false},
			},
		}

		if err := ClearAuthFiles(fileSet); err != nil {
			t.Fatalf("ClearAuthFiles() error = %v", err)
		}

		// Verify files removed
		if _, err := os.Stat(authFile1); !os.IsNotExist(err) {
			t.Error("authFile1 should be removed")
		}
		if _, err := os.Stat(authFile2); !os.IsNotExist(err) {
			t.Error("authFile2 should be removed")
		}
	})

	t.Run("handles nonexistent files gracefully", func(t *testing.T) {
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/auth.json", Required: true},
			},
		}

		// Should not error
		if err := ClearAuthFiles(fileSet); err != nil {
			t.Fatalf("ClearAuthFiles() error = %v", err)
		}
	})
}

func TestCopyFile(t *testing.T) {
	t.Run("copies file content", func(t *testing.T) {
		tmpDir := t.TempDir()

		src := filepath.Join(tmpDir, "source.txt")
		dst := filepath.Join(tmpDir, "dest.txt")
		content := []byte("test content for copy")

		if err := os.WriteFile(src, content, 0600); err != nil {
			t.Fatal(err)
		}

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile() error = %v", err)
		}

		got, err := os.ReadFile(dst)
		if err != nil {
			t.Fatalf("reading dst: %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("copied content = %q, want %q", got, content)
		}
	})

	t.Run("creates parent directories", func(t *testing.T) {
		tmpDir := t.TempDir()

		src := filepath.Join(tmpDir, "source.txt")
		dst := filepath.Join(tmpDir, "nested", "deep", "dest.txt")

		if err := os.WriteFile(src, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile() error = %v", err)
		}

		if _, err := os.Stat(dst); err != nil {
			t.Errorf("destination file not created: %v", err)
		}
	})

	t.Run("sets secure permissions", func(t *testing.T) {
		tmpDir := t.TempDir()

		src := filepath.Join(tmpDir, "source.txt")
		dst := filepath.Join(tmpDir, "dest.txt")

		if err := os.WriteFile(src, []byte("secret"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := copyFile(src, dst); err != nil {
			t.Fatalf("copyFile() error = %v", err)
		}

		info, err := os.Stat(dst)
		if err != nil {
			t.Fatal(err)
		}

		// Check permissions are 0600
		if info.Mode().Perm() != 0600 {
			t.Errorf("file permissions = %o, want 0600", info.Mode().Perm())
		}
	})
}

func TestHashFile(t *testing.T) {
	t.Run("returns correct SHA256 hash", func(t *testing.T) {
		tmpDir := t.TempDir()

		content := []byte("hash this content")
		file := filepath.Join(tmpDir, "test.txt")
		if err := os.WriteFile(file, content, 0600); err != nil {
			t.Fatal(err)
		}

		got, err := hashFile(file)
		if err != nil {
			t.Fatalf("hashFile() error = %v", err)
		}

		// Calculate expected hash
		h := sha256.Sum256(content)
		want := hex.EncodeToString(h[:])

		if got != want {
			t.Errorf("hashFile() = %q, want %q", got, want)
		}
	})

	t.Run("same content produces same hash", func(t *testing.T) {
		tmpDir := t.TempDir()

		content := []byte("identical content")
		file1 := filepath.Join(tmpDir, "file1.txt")
		file2 := filepath.Join(tmpDir, "file2.txt")

		if err := os.WriteFile(file1, content, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file2, content, 0600); err != nil {
			t.Fatal(err)
		}

		hash1, err := hashFile(file1)
		if err != nil {
			t.Fatal(err)
		}
		hash2, err := hashFile(file2)
		if err != nil {
			t.Fatal(err)
		}

		if hash1 != hash2 {
			t.Errorf("identical files have different hashes: %q vs %q", hash1, hash2)
		}
	})

	t.Run("different content produces different hash", func(t *testing.T) {
		tmpDir := t.TempDir()

		file1 := filepath.Join(tmpDir, "file1.txt")
		file2 := filepath.Join(tmpDir, "file2.txt")

		if err := os.WriteFile(file1, []byte("content A"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file2, []byte("content B"), 0600); err != nil {
			t.Fatal(err)
		}

		hash1, err := hashFile(file1)
		if err != nil {
			t.Fatal(err)
		}
		hash2, err := hashFile(file2)
		if err != nil {
			t.Fatal(err)
		}

		if hash1 == hash2 {
			t.Error("different files should have different hashes")
		}
	})

	t.Run("error for nonexistent file", func(t *testing.T) {
		_, err := hashFile("/nonexistent/file.txt")
		if err == nil {
			t.Error("hashFile() should error for nonexistent file")
		}
	})
}

func TestBackupRestore_RoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	authDir := filepath.Join(tmpDir, "auth")

	// Create original auth file
	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(authDir, "auth.json")
	originalContent := []byte(`{"token": "original-secret-token-12345"}`)
	if err := os.WriteFile(authFile, originalContent, 0600); err != nil {
		t.Fatal(err)
	}

	v := NewVault(vaultDir)
	fileSet := AuthFileSet{
		Tool: "testtool",
		Files: []AuthFileSpec{
			{Tool: "testtool", Path: authFile, Required: true},
		},
	}

	// Backup
	if err := v.Backup(fileSet, "roundtrip"); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	// Modify original
	if err := os.WriteFile(authFile, []byte(`{"token": "modified"}`), 0600); err != nil {
		t.Fatal(err)
	}

	// Restore
	if err := v.Restore(fileSet, "roundtrip"); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	// Verify original content restored
	restored, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(originalContent) {
		t.Errorf("restored content = %q, want %q", restored, originalContent)
	}

	// Verify active profile detection
	profile, err := v.ActiveProfile(fileSet)
	if err != nil {
		t.Fatal(err)
	}
	if profile != "roundtrip" {
		t.Errorf("ActiveProfile() = %q, want %q", profile, "roundtrip")
	}
}
