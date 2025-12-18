package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestActivate_AutoBackupsOriginalOnFirstSwitch(t *testing.T) {
	tmpDir := t.TempDir()

	// Isolate Codex auth location.
	oldCodexHome := os.Getenv("CODEX_HOME")
	t.Cleanup(func() { _ = os.Setenv("CODEX_HOME", oldCodexHome) })
	_ = os.Setenv("CODEX_HOME", filepath.Join(tmpDir, "codex_home"))

	if err := os.MkdirAll(os.Getenv("CODEX_HOME"), 0700); err != nil {
		t.Fatalf("MkdirAll(CODEX_HOME) error = %v", err)
	}

	original := []byte(`{"access_token":"original","token_type":"Bearer"}`)
	originalAuthPath := filepath.Join(os.Getenv("CODEX_HOME"), "auth.json")
	if err := os.WriteFile(originalAuthPath, original, 0600); err != nil {
		t.Fatalf("WriteFile(original auth) error = %v", err)
	}

	// Use a temp vault.
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	// Create a target profile in the vault with different contents.
	targetProfileDir := vault.ProfilePath("codex", "target")
	if err := os.MkdirAll(targetProfileDir, 0700); err != nil {
		t.Fatalf("MkdirAll(target profile) error = %v", err)
	}
	target := []byte(`{"access_token":"target","token_type":"Bearer"}`)
	if err := os.WriteFile(filepath.Join(targetProfileDir, "auth.json"), target, 0600); err != nil {
		t.Fatalf("WriteFile(target auth) error = %v", err)
	}

	if err := runActivate(activateCmd, []string{"codex", "target"}); err != nil {
		t.Fatalf("runActivate() error = %v", err)
	}

	// Ensure the original state was preserved as a system `_original` profile.
	origBackupPath := vault.BackupPath("codex", "_original", "auth.json")
	gotOriginal, err := os.ReadFile(origBackupPath)
	if err != nil {
		t.Fatalf("ReadFile(_original auth) error = %v", err)
	}
	if string(gotOriginal) != string(original) {
		t.Fatalf("_original auth mismatch: got %q want %q", gotOriginal, original)
	}

	// Ensure metadata marks it as a system "first-activate" backup.
	metaRaw, err := os.ReadFile(filepath.Join(vault.ProfilePath("codex", "_original"), "meta.json"))
	if err != nil {
		t.Fatalf("ReadFile(_original meta.json) error = %v", err)
	}
	var meta struct {
		Type          string   `json:"type"`
		CreatedBy     string   `json:"created_by"`
		OriginalPaths []string `json:"original_paths"`
	}
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("Unmarshal(_original meta.json) error = %v", err)
	}
	if meta.Type != "system" {
		t.Fatalf("meta.type = %q, want %q", meta.Type, "system")
	}
	if meta.CreatedBy != "first-activate" {
		t.Fatalf("meta.created_by = %q, want %q", meta.CreatedBy, "first-activate")
	}
	if len(meta.OriginalPaths) != 1 || meta.OriginalPaths[0] != originalAuthPath {
		t.Fatalf("meta.original_paths = %v, want [%s]", meta.OriginalPaths, originalAuthPath)
	}

	// Ensure activation actually switched the auth file.
	gotActive, err := os.ReadFile(originalAuthPath)
	if err != nil {
		t.Fatalf("ReadFile(active auth) error = %v", err)
	}
	if string(gotActive) != string(target) {
		t.Fatalf("active auth mismatch: got %q want %q", gotActive, target)
	}
}
