package authfile

import (
	"path/filepath"
	"os"
	"testing"
)

func TestBackup_PlusInProfileName(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")
	authDir := filepath.Join(tmpDir, "auth")

	if err := os.MkdirAll(authDir, 0700); err != nil {
		t.Fatal(err)
	}
	authFile := filepath.Join(authDir, "auth.json")
	if err := os.WriteFile(authFile, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}

	v := NewVault(vaultDir)
	fileSet := AuthFileSet{
		Tool: "testtool",
		Files: []AuthFileSpec{
			{Tool: "testtool", Path: authFile, Required: true},
		},
	}

	// This is a common email pattern: user+tag@gmail.com
	profileName := "user+tag@gmail.com"

	err := v.Backup(fileSet, profileName)
	if err != nil {
		t.Fatalf("Backup() failed for profile name with plus '%s': %v", profileName, err)
	}
}
