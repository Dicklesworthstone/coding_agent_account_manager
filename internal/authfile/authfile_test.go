package authfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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
	origCaamHome := os.Getenv("CAAM_HOME")
	origXDG := os.Getenv("XDG_DATA_HOME")
	defer os.Setenv("CAAM_HOME", origCaamHome)
	defer os.Setenv("XDG_DATA_HOME", origXDG)

	t.Run("with CAAM_HOME set", func(t *testing.T) {
		os.Setenv("CAAM_HOME", "/custom/caam")
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		path := DefaultVaultPath()
		want := "/custom/caam/data/vault"
		if path != want {
			t.Errorf("DefaultVaultPath() = %q, want %q", path, want)
		}
	})

	t.Run("with XDG_DATA_HOME set", func(t *testing.T) {
		os.Unsetenv("CAAM_HOME")
		os.Setenv("XDG_DATA_HOME", "/custom/data")
		path := DefaultVaultPath()
		want := "/custom/data/caam/vault"
		if path != want {
			t.Errorf("DefaultVaultPath() = %q, want %q", path, want)
		}
	})

	t.Run("without XDG_DATA_HOME", func(t *testing.T) {
		os.Unsetenv("CAAM_HOME")
		os.Unsetenv("XDG_DATA_HOME")
		path := DefaultVaultPath()
		homeDir, _ := os.UserHomeDir()
		want := filepath.Join(homeDir, ".local", "share", "caam", "vault")
		if path != want {
			t.Errorf("DefaultVaultPath() = %q, want %q", path, want)
		}
	})
}

func TestClaudeAuthFilesConfigDirectoryPaths(t *testing.T) {
	for _, mode := range []string{"legacy", "xdg", "explicit existing", "explicit missing"} {
		t.Run(mode, func(t *testing.T) {
			home, custom := t.TempDir(), filepath.Join(t.TempDir(), "claude-config")
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", "")
			want := map[string]string{
				".credentials.json": filepath.Join(home, ".claude", ".credentials.json"),
				".claude.json":      filepath.Join(home, ".claude.json"),
				"settings.json":     filepath.Join(home, ".claude", "settings.json"),
				"auth.json":         filepath.Join(home, ".config", "claude-code", "auth.json"),
				"config.json":       claudeDesktopConfigPath(home),
			}
			if mode == "xdg" {
				xdg := t.TempDir()
				t.Setenv("XDG_CONFIG_HOME", xdg)
				want["auth.json"] = filepath.Join(xdg, "claude-code", "auth.json")
			}
			if strings.HasPrefix(mode, "explicit") {
				t.Setenv("CLAUDE_CONFIG_DIR", custom)
				delete(want, "config.json")
				for file := range want {
					want[file] = filepath.Join(custom, file)
				}
				if mode == "explicit existing" {
					if err := os.MkdirAll(custom, 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			files := ClaudeAuthFiles()
			if len(files.Files) != len(want) {
				t.Fatalf("resolved %d auth files, want %d", len(files.Files), len(want))
			}
			for _, spec := range files.Files {
				if expected := want[filepath.Base(spec.Path)]; spec.Path != expected {
					t.Errorf("resolved path %q, want %q", spec.Path, expected)
				}
			}
		})
	}
}

func TestClaudeExplicitConfigBackupAndSettingsLifecycle(t *testing.T) {
	home, canonical := t.TempDir(), filepath.Join(t.TempDir(), "configured-claude")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CLAUDE_CONFIG_DIR", canonical)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CAAM_KEYCHAIN", "0")
	legacy := map[string]string{
		filepath.Join(home, ".claude", ".credentials.json"): `{"claudeAiOauth":{"accessToken":"host-access"}}`,
		filepath.Join(home, ".claude", "settings.json"):     `{"apiKeyHelper":"host-helper","permissions":{"allow":["host"]}}`,
		filepath.Join(home, ".claude.json"):                 `{"oauthToken":"host-token"}`,
		claudeDesktopConfigPath(home):                       `{"oauth:tokenCache":"host-desktop"}`,
	}
	for path, data := range legacy {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		writeFixtureFile(t, path, data)
	}
	files := ClaudeAuthFiles()
	vault := NewVault(t.TempDir())
	if HasAuthFiles(files) {
		t.Fatal("missing canonical directory fell back to host credentials")
	}
	if err := vault.Backup(files, "missing"); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("missing canonical backup error = %v, want ErrNoCredentials", err)
	}
	canonicalData := map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"canonical-access","refreshToken":"canonical-refresh"}}`,
		".claude.json":      `{"oauthToken":"canonical-token","mcpServers":{"canonical":{}}}`,
		"settings.json":     `{"apiKeyHelper":"canonical-helper","permissions":{"deny":["old-policy"]}}`,
		"auth.json":         `{"access_token":"canonical-auth"}`,
	}
	if err := os.MkdirAll(canonical, 0700); err != nil {
		t.Fatal(err)
	}
	for file, data := range canonicalData {
		writeFixtureFile(t, filepath.Join(canonical, file), data)
	}
	if err := vault.Backup(files, "canonical"); err != nil {
		t.Fatal(err)
	}
	for file, want := range canonicalData {
		got := readFixtureFile(t, vault.BackupPath("claude", "canonical", file))
		var gotObject, wantObject map[string]interface{}
		if err := json.Unmarshal([]byte(got), &gotObject); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(want), &wantObject); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(gotObject, wantObject) {
			t.Fatalf("backup %s = %s, want canonical contents %s", file, got, want)
		}
	}
	settingsPath := filepath.Join(canonical, "settings.json")
	writeFixtureFile(t, settingsPath, `{"apiKeyHelper":"outgoing-helper","permissions":{"deny":["latest-policy"]}}`)
	if err := vault.Restore(files, "canonical"); err != nil {
		t.Fatal(err)
	}
	settings := readFixtureFile(t, settingsPath)
	if !strings.Contains(settings, "canonical-helper") || !strings.Contains(settings, "latest-policy") || strings.Contains(settings, "old-policy") {
		t.Fatalf("canonical settings bypassed shared policy merge: %s", settings)
	}
	if err := ClearAuthFiles(files); err != nil {
		t.Fatal(err)
	}
	settings = readFixtureFile(t, settingsPath)
	if strings.Contains(settings, "apiKeyHelper") || !strings.Contains(settings, "latest-policy") {
		t.Fatalf("canonical clear lost policy or retained authentication: %s", settings)
	}
	for path, want := range legacy {
		if got := readFixtureFile(t, path); got != want {
			t.Fatalf("canonical lifecycle changed host file %s: %s", path, got)
		}
	}
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

	t.Run("invalid profile name fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		if err := os.WriteFile(authFile, []byte(`{"token": "secret123"}`), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		if err := v.Backup(fileSet, "/"); err == nil {
			t.Fatal("Backup() should fail for invalid profile name")
		}
	})

	t.Run("invalid characters in profile name fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		if err := os.WriteFile(authFile, []byte(`{"token": "secret123"}`), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		// Test various invalid characters that should be rejected:
		// - Control characters (filesystem issues)
		// - Shell metacharacters (command injection prevention)
		// - Spaces (shell word splitting issues)
		invalidNames := []string{
			// Control characters
			"profile\nwith\nnewlines",
			"profile\twith\ttabs",
			"profile\rwith\rcarriage",
			"profile\x00with\x00null",
			"profile\x1fwith\x1fcontrol",
			"profile\x7fwith\x7fdel",
			// Shell metacharacters (command injection vectors)
			"profile$(touch /tmp/pwned)",
			"profile`touch /tmp/pwned`",
			"profile;rm -rf /",
			"profile|cat /etc/passwd",
			"profile&background",
			"profile'quoted'",
			`profile"doublequoted"`,
			// Spaces (word splitting)
			"profile with spaces",
			// Other special characters (@ is allowed for email-based names)
			"profile#hashtag",
			"profile$var",
			"profile%mod",
			"profile^caret",
			"profile*glob",
			"profile?question",
			"profile[bracket]",
			"profile{brace}",
			"profile<redirect>",
			"profile!bang",
			"profile~tilde",
		}

		for _, name := range invalidNames {
			if err := v.Backup(fileSet, name); err == nil {
				t.Errorf("Backup() should fail for profile name with invalid chars: %q", name)
			}
		}

		// Test valid names that should pass
		validNames := []string{
			"simple",
			"with-hyphen",
			"with_underscore",
			"with.period",
			"MixedCase123",
			"profile-1.backup_v2",
			"alice@gmail.com",
			"work@company.com",
			"profile@email",
		}

		for _, name := range validNames {
			// Create a fresh vault for each valid test
			testVault := filepath.Join(tmpDir, "vault-valid-"+name)
			vt := NewVault(testVault)
			if err := vt.Backup(fileSet, name); err != nil {
				t.Errorf("Backup() should succeed for valid profile name %q: %v", name, err)
			}
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

		// A partial snapshot must be rejected before its first file replaces
		// the live account. Discovering the missing file during copying is late.
		profileDir := filepath.Join(vaultDir, "testtool", "profile1")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "first.json"), []byte(`{"token":"target"}`), 0600); err != nil {
			t.Fatal(err)
		}
		livePath := filepath.Join(tmpDir, "first.json")
		const live = `{"token":"live"}`
		if err := os.WriteFile(livePath, []byte(live), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: livePath, Required: true},
				{Tool: "testtool", Path: filepath.Join(tmpDir, "missing.json"), Required: true},
			},
		}

		err := v.Restore(fileSet, "profile1")
		if !errors.Is(err, ErrNoCredentials) {
			t.Fatalf("Restore() = %v, want ErrNoCredentials", err)
		}
		if got, err := os.ReadFile(livePath); err != nil || string(got) != live {
			t.Fatalf("partial snapshot changed live credentials: %v", err)
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

func TestMigrateGeminiVaultDir(t *testing.T) {
	t.Run("renames old file to new", func(t *testing.T) {
		dir := t.TempDir()
		oldPath := filepath.Join(dir, "oauth_credentials.json")
		newPath := filepath.Join(dir, "oauth_creds.json")
		if err := os.WriteFile(oldPath, []byte(`{"client_id":"x"}`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := MigrateGeminiVaultDir(dir); err != nil {
			t.Fatalf("MigrateGeminiVaultDir() error = %v", err)
		}
		if _, err := os.Stat(newPath); err != nil {
			t.Errorf("new file should exist: %v", err)
		}
		if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
			t.Errorf("old file should not exist after rename")
		}
	})

	t.Run("no-op when new file already exists", func(t *testing.T) {
		dir := t.TempDir()
		oldPath := filepath.Join(dir, "oauth_credentials.json")
		newPath := filepath.Join(dir, "oauth_creds.json")
		if err := os.WriteFile(oldPath, []byte(`old`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(newPath, []byte(`new`), 0600); err != nil {
			t.Fatal(err)
		}
		if err := MigrateGeminiVaultDir(dir); err != nil {
			t.Fatalf("MigrateGeminiVaultDir() error = %v", err)
		}
		data, _ := os.ReadFile(newPath)
		if string(data) != "new" {
			t.Errorf("new file should be unchanged, got %q", data)
		}
	})

	t.Run("no-op when no old file", func(t *testing.T) {
		dir := t.TempDir()
		if err := MigrateGeminiVaultDir(dir); err != nil {
			t.Fatalf("MigrateGeminiVaultDir() error = %v", err)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		dir := t.TempDir()
		oldPath := filepath.Join(dir, "oauth_credentials.json")
		newPath := filepath.Join(dir, "oauth_creds.json")
		if err := os.WriteFile(oldPath, []byte(`{"client_id":"x"}`), 0600); err != nil {
			t.Fatal(err)
		}
		// Run twice
		if err := MigrateGeminiVaultDir(dir); err != nil {
			t.Fatal(err)
		}
		if err := MigrateGeminiVaultDir(dir); err != nil {
			t.Fatal(err)
		}
		data, _ := os.ReadFile(newPath)
		if string(data) != `{"client_id":"x"}` {
			t.Errorf("content should survive double migration, got %q", data)
		}
	})
}

func TestVaultRestore_MigratesGeminiFilename(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	authDir := filepath.Join(tmpDir, "auth")

	// Create vault profile with OLD filename
	profileDir := filepath.Join(vaultDir, "gemini", "testprofile")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	oldContent := []byte(`{"client_id":"test","client_secret":"s","refresh_token":"r"}`)
	if err := os.WriteFile(filepath.Join(profileDir, "oauth_credentials.json"), oldContent, 0600); err != nil {
		t.Fatal(err)
	}

	v := NewVault(vaultDir)
	authFile := filepath.Join(authDir, ".gemini", "oauth_creds.json")
	fileSet := AuthFileSet{
		Tool: "gemini",
		Files: []AuthFileSpec{
			{Tool: "gemini", Path: authFile, Required: false},
		},
		AllowOptionalOnly: true,
	}
	if err := v.ValidateProfileCredentials(fileSet, "testprofile"); err != nil {
		t.Fatalf("ValidateProfileCredentials() error = %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(profileDir, "oauth_credentials.json")); err != nil || string(got) != string(oldContent) {
		t.Fatalf("validation changed legacy snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(profileDir, "oauth_creds.json")); !os.IsNotExist(err) {
		t.Fatalf("validation migrated the legacy snapshot: %v", err)
	}

	if err := v.Restore(fileSet, "testprofile"); err != nil {
		t.Fatalf("Restore() error = %v", err)
	}

	// Verify the file was restored to the NEW name location
	restored, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("restored file should exist at new path: %v", err)
	}
	if string(restored) != string(oldContent) {
		t.Errorf("restored content = %q, want %q", restored, oldContent)
	}
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

func TestVaultBackup_SystemProfilesAreImmutable(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	authDir := filepath.Join(tmpDir, "auth")

	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(authDir, "auth.json")
	if err := os.WriteFile(authFile, []byte(`{"token":"v1"}`), 0600); err != nil {
		t.Fatal(err)
	}

	v := NewVault(vaultDir)
	fileSet := AuthFileSet{
		Tool: "testtool",
		Files: []AuthFileSpec{
			{Tool: "testtool", Path: authFile, Required: true},
		},
	}

	if err := v.Backup(fileSet, "_system"); err != nil {
		t.Fatalf("Backup() error = %v", err)
	}

	// Change source and ensure overwrite is refused.
	if err := os.WriteFile(authFile, []byte(`{"token":"v2"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.Backup(fileSet, "_system"); err == nil {
		t.Fatal("Backup() should refuse overwriting system profiles")
	}
}

func TestVaultBackupOriginal(t *testing.T) {
	t.Run("creates _original when current auth is not backed up", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		original := []byte(`{"token":"original"}`)
		if err := os.WriteFile(authFile, original, 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: authFile, Required: true},
			},
		}

		did, err := v.BackupOriginal(fileSet)
		if err != nil {
			t.Fatalf("BackupOriginal() error = %v", err)
		}
		if !did {
			t.Fatal("BackupOriginal() did = false, want true")
		}

		backupPath := v.BackupPath("testtool", "_original", "auth.json")
		got, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != string(original) {
			t.Fatalf("backup content mismatch: got %q want %q", got, original)
		}

		// Idempotent: second call is a no-op.
		did, err = v.BackupOriginal(fileSet)
		if err != nil {
			t.Fatalf("BackupOriginal() second call error = %v", err)
		}
		if did {
			t.Fatal("BackupOriginal() second call did = true, want false")
		}
	})

	t.Run("skips when current auth already matches a vault profile", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		content := []byte(`{"token":"already-backed-up"}`)
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

		if err := v.Backup(fileSet, "work"); err != nil {
			t.Fatalf("Backup() error = %v", err)
		}

		did, err := v.BackupOriginal(fileSet)
		if err != nil {
			t.Fatalf("BackupOriginal() error = %v", err)
		}
		if did {
			t.Fatal("BackupOriginal() did = true, want false")
		}

		if _, err := os.Stat(v.ProfilePath("testtool", "_original")); !os.IsNotExist(err) {
			t.Fatalf("_original should not be created; stat err=%v", err)
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

	t.Run("refuses to delete system profiles without force", func(t *testing.T) {
		tmpDir := t.TempDir()

		profileDir := filepath.Join(tmpDir, "testtool", "_system")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}

		v := NewVault(tmpDir)
		if err := v.Delete("testtool", "_system"); err == nil {
			t.Fatal("Delete() should fail for system profiles")
		}

		// Verify still exists.
		if _, err := os.Stat(profileDir); err != nil {
			t.Fatalf("profile directory should still exist: %v", err)
		}

		if err := v.DeleteForce("testtool", "_system"); err != nil {
			t.Fatalf("DeleteForce() error = %v", err)
		}
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

	t.Run("rejects invalid segments", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		if err := v.Delete("/", "profile"); err == nil {
			t.Fatal("Delete() should fail for invalid tool segment")
		}
		if err := v.Delete("testtool", "/"); err == nil {
			t.Fatal("Delete() should fail for invalid profile segment")
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

	t.Run("ignores optional file differences when required files present", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		requiredPath := filepath.Join(authDir, "auth.json")
		optionalPath := filepath.Join(authDir, "settings.json")
		requiredContent := []byte(`{"token": "required-match"}`)
		if err := os.WriteFile(requiredPath, requiredContent, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(optionalPath, []byte(`{"session": "current-volatile"}`), 0600); err != nil {
			t.Fatal(err)
		}

		profileDir := filepath.Join(vaultDir, "testtool", "stable")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), requiredContent, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "settings.json"), []byte(`{"session": "backup-volatile"}`), 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: requiredPath, Required: true},
				{Tool: "testtool", Path: optionalPath, Required: false},
			},
		}

		profile, err := v.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile() error = %v", err)
		}
		if profile != "stable" {
			t.Errorf("ActiveProfile() = %q, want %q", profile, "stable")
		}
	})

	t.Run("matches optional-only profiles when allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		optionalPath := filepath.Join(authDir, "optional.json")
		optionalContent := []byte(`{"token": "optional-only"}`)
		if err := os.WriteFile(optionalPath, optionalContent, 0600); err != nil {
			t.Fatal(err)
		}

		profileDir := filepath.Join(vaultDir, "testtool", "optional")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "optional.json"), optionalContent, 0600); err != nil {
			t.Fatal(err)
		}

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: filepath.Join(authDir, "required.json"), Required: true},
				{Tool: "testtool", Path: optionalPath, Required: false},
			},
			AllowOptionalOnly: true,
		}

		profile, err := v.ActiveProfile(fileSet)
		if err != nil {
			t.Fatalf("ActiveProfile() error = %v", err)
		}
		if profile != "optional" {
			t.Errorf("ActiveProfile() = %q, want %q", profile, "optional")
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

	t.Run("accepts optional files when allowed", func(t *testing.T) {
		tmpDir := t.TempDir()
		optionalPath := filepath.Join(tmpDir, "optional.json")
		if err := os.WriteFile(optionalPath, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}

		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: optionalPath, Required: false},
			},
			AllowOptionalOnly: true,
		}

		if !HasAuthFiles(fileSet) {
			t.Error("HasAuthFiles() = false, want true (optional files allowed)")
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

func TestVaultBackupCurrent(t *testing.T) {
	t.Run("creates timestamped backup", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")
		authDir := filepath.Join(tmpDir, "auth")

		if err := os.MkdirAll(authDir, 0700); err != nil {
			t.Fatal(err)
		}
		authFile := filepath.Join(authDir, "auth.json")
		content := []byte(`{"token":"current"}`)
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

		backupName, err := v.BackupCurrent(fileSet)
		if err != nil {
			t.Fatalf("BackupCurrent() error = %v", err)
		}

		// Check backup name format
		if backupName == "" {
			t.Fatal("BackupCurrent() returned empty name")
		}
		if len(backupName) < 8 || backupName[:8] != "_backup_" {
			t.Errorf("backup name %q doesn't start with _backup_", backupName)
		}

		// Verify backup content
		backupPath := v.BackupPath("testtool", backupName, "auth.json")
		got, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("backup content = %q, want %q", got, content)
		}
	})

	t.Run("no-op when no auth files", func(t *testing.T) {
		tmpDir := t.TempDir()
		vaultDir := filepath.Join(tmpDir, "vault")

		v := NewVault(vaultDir)
		fileSet := AuthFileSet{
			Tool: "testtool",
			Files: []AuthFileSpec{
				{Tool: "testtool", Path: "/nonexistent/auth.json", Required: true},
			},
		}

		backupName, err := v.BackupCurrent(fileSet)
		if err != nil {
			t.Fatalf("BackupCurrent() error = %v", err)
		}
		if backupName != "" {
			t.Errorf("BackupCurrent() = %q, want empty string when no auth files", backupName)
		}
	})
}

func TestVaultRotateAutoBackups(t *testing.T) {
	t.Run("deletes oldest when over limit", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create 5 backup profiles
		backups := []string{
			"_backup_20251201_100000",
			"_backup_20251202_100000",
			"_backup_20251203_100000",
			"_backup_20251204_100000",
			"_backup_20251205_100000",
		}
		for _, name := range backups {
			profileDir := v.ProfilePath("testtool", name)
			if err := os.MkdirAll(profileDir, 0700); err != nil {
				t.Fatal(err)
			}
		}

		// Rotate to keep only 3
		if err := v.RotateAutoBackups("testtool", 3); err != nil {
			t.Fatalf("RotateAutoBackups() error = %v", err)
		}

		// Check remaining profiles
		profiles, _ := v.List("testtool")
		if len(profiles) != 3 {
			t.Errorf("after rotation: %d profiles, want 3", len(profiles))
		}

		// Oldest 2 should be deleted
		for _, name := range backups[:2] {
			profileDir := v.ProfilePath("testtool", name)
			if _, err := os.Stat(profileDir); !os.IsNotExist(err) {
				t.Errorf("profile %s should have been deleted", name)
			}
		}

		// Newest 3 should remain
		for _, name := range backups[2:] {
			profileDir := v.ProfilePath("testtool", name)
			if _, err := os.Stat(profileDir); os.IsNotExist(err) {
				t.Errorf("profile %s should still exist", name)
			}
		}
	})

	t.Run("no-op when within limit", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create 2 backup profiles
		backups := []string{"_backup_20251201_100000", "_backup_20251202_100000"}
		for _, name := range backups {
			profileDir := v.ProfilePath("testtool", name)
			if err := os.MkdirAll(profileDir, 0700); err != nil {
				t.Fatal(err)
			}
		}

		// Rotate with limit of 5 (more than we have)
		if err := v.RotateAutoBackups("testtool", 5); err != nil {
			t.Fatalf("RotateAutoBackups() error = %v", err)
		}

		// All should remain
		profiles, _ := v.List("testtool")
		if len(profiles) != 2 {
			t.Errorf("after rotation: %d profiles, want 2", len(profiles))
		}
	})

	t.Run("no-op when maxBackups is 0", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create many backups
		for i := 0; i < 10; i++ {
			profileDir := v.ProfilePath("testtool", "_backup_2025120"+string(rune('0'+i))+"_100000")
			if err := os.MkdirAll(profileDir, 0700); err != nil {
				t.Fatal(err)
			}
		}

		// Rotate with 0 means unlimited
		if err := v.RotateAutoBackups("testtool", 0); err != nil {
			t.Fatalf("RotateAutoBackups() error = %v", err)
		}

		// All should remain (0 = unlimited)
		profiles, _ := v.List("testtool")
		if len(profiles) != 10 {
			t.Errorf("after rotation: %d profiles, want 10 (unlimited)", len(profiles))
		}
	})

	t.Run("only rotates _backup_ profiles", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create mix of profiles
		profiles := []string{
			"_backup_20251201_100000",
			"_backup_20251202_100000",
			"_backup_20251203_100000",
			"_original",
			"work",
			"personal",
		}
		for _, name := range profiles {
			profileDir := v.ProfilePath("testtool", name)
			if err := os.MkdirAll(profileDir, 0700); err != nil {
				t.Fatal(err)
			}
		}

		// Rotate to keep only 1 backup
		if err := v.RotateAutoBackups("testtool", 1); err != nil {
			t.Fatalf("RotateAutoBackups() error = %v", err)
		}

		// Should have: 1 backup + _original + work + personal = 4
		remaining, _ := v.List("testtool")
		if len(remaining) != 4 {
			t.Errorf("after rotation: %d profiles, want 4", len(remaining))
		}

		// _original, work, personal should still exist
		for _, name := range []string{"_original", "work", "personal"} {
			profileDir := v.ProfilePath("testtool", name)
			if _, err := os.Stat(profileDir); os.IsNotExist(err) {
				t.Errorf("non-backup profile %s should still exist", name)
			}
		}
	})
}

func TestVaultCopyProfile(t *testing.T) {
	t.Run("successful copy", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create source profile with files
		srcDir := v.ProfilePath("testtool", "source")
		if err := os.MkdirAll(srcDir, 0700); err != nil {
			t.Fatal(err)
		}

		authContent := []byte(`{"token": "secret123"}`)
		if err := os.WriteFile(filepath.Join(srcDir, "auth.json"), authContent, 0600); err != nil {
			t.Fatal(err)
		}

		metaContent := []byte(`{"tool": "testtool", "profile": "source"}`)
		if err := os.WriteFile(filepath.Join(srcDir, "meta.json"), metaContent, 0600); err != nil {
			t.Fatal(err)
		}

		// Copy to destination
		if err := v.CopyProfile("testtool", "source", "dest"); err != nil {
			t.Fatalf("CopyProfile() error = %v", err)
		}

		// Verify destination exists with same content
		destDir := v.ProfilePath("testtool", "dest")
		if _, err := os.Stat(destDir); os.IsNotExist(err) {
			t.Fatal("destination directory not created")
		}

		copiedAuth, err := os.ReadFile(filepath.Join(destDir, "auth.json"))
		if err != nil {
			t.Fatalf("reading dest auth: %v", err)
		}
		if string(copiedAuth) != string(authContent) {
			t.Errorf("auth content = %q, want %q", copiedAuth, authContent)
		}

		// Verify source still exists
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			t.Error("source directory should still exist")
		}
	})

	t.Run("source not found fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		err := v.CopyProfile("testtool", "nonexistent", "dest")
		if err == nil {
			t.Fatal("CopyProfile() should fail for nonexistent source")
		}
	})

	t.Run("destination exists fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create both source and destination
		srcDir := v.ProfilePath("testtool", "source")
		dstDir := v.ProfilePath("testtool", "dest")
		if err := os.MkdirAll(srcDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dstDir, 0700); err != nil {
			t.Fatal(err)
		}

		err := v.CopyProfile("testtool", "source", "dest")
		if err == nil {
			t.Fatal("CopyProfile() should fail when destination exists")
		}
	})

	t.Run("invalid source profile name fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		err := v.CopyProfile("testtool", "../escape", "dest")
		if err == nil {
			t.Fatal("CopyProfile() should fail for invalid source profile name")
		}
	})

	t.Run("invalid destination profile name fails", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create source
		srcDir := v.ProfilePath("testtool", "source")
		if err := os.MkdirAll(srcDir, 0700); err != nil {
			t.Fatal(err)
		}

		err := v.CopyProfile("testtool", "source", "../escape")
		if err == nil {
			t.Fatal("CopyProfile() should fail for invalid destination profile name")
		}
	})

	t.Run("meta.json updated with new profile name", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create source profile with meta.json
		srcDir := v.ProfilePath("testtool", "auto-20260121-143022")
		if err := os.MkdirAll(srcDir, 0700); err != nil {
			t.Fatal(err)
		}

		metaContent := []byte(`{"tool": "testtool", "profile": "auto-20260121-143022", "backed_up_at": "2026-01-21T14:30:22Z"}`)
		if err := os.WriteFile(filepath.Join(srcDir, "meta.json"), metaContent, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, "auth.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}

		// Copy to friendly name
		if err := v.CopyProfile("testtool", "auto-20260121-143022", "work"); err != nil {
			t.Fatalf("CopyProfile() error = %v", err)
		}

		// Read and verify meta.json
		destDir := v.ProfilePath("testtool", "work")
		metaPath := filepath.Join(destDir, "meta.json")
		metaData, err := os.ReadFile(metaPath)
		if err != nil {
			t.Fatalf("reading meta.json: %v", err)
		}

		var meta map[string]interface{}
		if err := json.Unmarshal(metaData, &meta); err != nil {
			t.Fatalf("parsing meta.json: %v", err)
		}

		if meta["profile"] != "work" {
			t.Errorf("meta.json profile = %v, want 'work'", meta["profile"])
		}
		if meta["copied_from"] != "auto-20260121-143022" {
			t.Errorf("meta.json copied_from = %v, want 'auto-20260121-143022'", meta["copied_from"])
		}
		if meta["copied_at"] == nil {
			t.Error("meta.json should have copied_at timestamp")
		}
	})

	t.Run("copies system profile to regular name", func(t *testing.T) {
		tmpDir := t.TempDir()
		v := NewVault(tmpDir)

		// Create system profile
		srcDir := v.ProfilePath("testtool", "_backup_20251217_143022")
		if err := os.MkdirAll(srcDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, "auth.json"), []byte(`{}`), 0600); err != nil {
			t.Fatal(err)
		}

		// Copy to regular name
		if err := v.CopyProfile("testtool", "_backup_20251217_143022", "restored"); err != nil {
			t.Fatalf("CopyProfile() error = %v", err)
		}

		// Both should exist
		if _, err := os.Stat(srcDir); os.IsNotExist(err) {
			t.Error("source system profile should still exist")
		}
		if _, err := os.Stat(v.ProfilePath("testtool", "restored")); os.IsNotExist(err) {
			t.Error("destination profile should exist")
		}
	})
}

func TestVaultLabelsSurviveBackup(t *testing.T) {
	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "auth", "auth.json")
	if err := os.MkdirAll(filepath.Dir(authFile), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(authFile, []byte(`{"token":"one"}`), 0600); err != nil {
		t.Fatal(err)
	}
	v := NewVault(filepath.Join(tmpDir, "vault"))
	fileSet := AuthFileSet{Tool: "testtool", Files: []AuthFileSpec{{Tool: "testtool", Path: authFile, Required: true}}}

	if _, err := v.Labels("testtool", "work"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("labels of a missing profile: err = %v, want ErrNotExist", err)
	}
	if err := v.SetLabels("testtool", "work", ProfileLabels{Tags: []string{"x"}}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("labeling a missing profile: err = %v, want ErrNotExist", err)
	}

	if err := v.Backup(fileSet, "work"); err != nil {
		t.Fatal(err)
	}
	want := ProfileLabels{Description: "Client A", Tags: []string{"client-a", "urgent"}}
	if err := v.SetLabels("testtool", "work", want); err != nil {
		t.Fatalf("SetLabels: %v", err)
	}
	got, err := v.Labels("testtool", "work")
	if err != nil || got.Description != want.Description || strings.Join(got.Tags, ",") != "client-a,urgent" {
		t.Fatalf("Labels = %+v, %v", got, err)
	}

	// Labeling keeps the snapshot metadata.
	meta, err := os.ReadFile(filepath.Join(tmpDir, "vault", "testtool", "work", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(meta), `"backed_up_at"`) || !strings.Contains(string(meta), `"original_paths"`) {
		t.Fatalf("SetLabels dropped snapshot metadata: %s", meta)
	}

	// Re-backing up the account keeps its labels.
	if err := os.WriteFile(authFile, []byte(`{"token":"two"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := v.Backup(fileSet, "work"); err != nil {
		t.Fatal(err)
	}
	got, err = v.Labels("testtool", "work")
	if err != nil || got.Description != "Client A" || len(got.Tags) != 2 {
		t.Fatalf("labels after re-backup = %+v, %v", got, err)
	}

	// Clearing removes the keys entirely.
	if err := v.SetLabels("testtool", "work", ProfileLabels{}); err != nil {
		t.Fatal(err)
	}
	meta, _ = os.ReadFile(filepath.Join(tmpDir, "vault", "testtool", "work", "meta.json"))
	if strings.Contains(string(meta), "description") || strings.Contains(string(meta), "tags") {
		t.Fatalf("cleared labels still present: %s", meta)
	}
}
