package profile

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestVaultBackupHoldsSourceAgainstManagedWriters(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles"))
	source, err := store.Create("claude", "source", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(source.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("snapshot preparation failed")
	err = store.withVaultBackupSource("source", func(root string) error {
		if root != source.BasePath {
			t.Fatalf("wrong source path: %s", root)
		}
		if err := source.Lock(); err == nil {
			_ = source.Unlock()
			t.Error("session started while backup held the source")
		}
		if err := source.LockWithCleanup(); err == nil {
			_ = source.Unlock()
			t.Error("session cleanup bypassed backup lock")
		}
		if err := store.Delete("claude", "source"); err == nil {
			t.Error("source deleted during backup")
		}
		prepared := false
		if _, err := store.Import(context.Background(), "claude", "source", "oauth", true, func(*Profile) error {
			prepared = true
			return nil
		}); err == nil || prepared {
			t.Error("forced import started preparing a replacement during backup")
		}
		if _, err := store.Clone("claude", "source", "copy", CloneOptions{WithAuth: true}); err == nil {
			t.Error("source cloned during backup")
		}
		if err := store.withVaultBackupSource("source", func(string) error {
			t.Error("second backup entered the source")
			return nil
		}); err == nil {
			t.Error("second backup bypassed source lock")
		}
		// Unrelated accounts must remain usable throughout the handoff.
		other, err := store.Create("claude", "other", "oauth")
		if err != nil {
			t.Fatal(err)
		}
		if err := other.Lock(); err != nil {
			t.Fatalf("backup blocked an unrelated account: %v", err)
		}
		if err := other.Unlock(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(source.LockPath()); !os.IsNotExist(err) {
			t.Fatalf("backup wrote a source session lock: %v", err)
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("lost preparation failure: %v", err)
	}
	after, err := os.ReadFile(source.MetaPath())
	if err != nil || string(before) != string(after) {
		t.Fatalf("source metadata changed on failure: %v", err)
	}
	// Error unwinding must release exactly our mutation lock.
	if err := source.Lock(); err != nil {
		t.Fatalf("failed backup left the source locked: %v", err)
	}
	if err := source.Unlock(); err != nil {
		t.Fatal(err)
	}
	if err := store.withVaultBackupSource("source", func(string) error { return nil }); err != nil {
		t.Fatalf("subsequent backup failed: %v", err)
	}
}

func TestVaultBackupRejectsBusySourceBeforeCapture(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles"))
	unlock, err := store.lockMutation("claude", "source")
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	entered := false
	if err := store.withVaultBackupSource("source", func(string) error {
		entered = true
		return nil
	}); err == nil || entered {
		t.Fatalf("busy source was read: entered=%v, error=%v", entered, err)
	}
}

func TestVaultBackupSourceLockAcrossProcesses(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "profiles"))
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.withVaultBackupSource("source", func(string) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		child := exec.CommandContext(ctx, exe, "-test.run=^TestVaultBackupSourceLockChild$", "-test.count=1")
		child.Env = append(os.Environ(), "CAAM_TEST_VAULT_BACKUP_LOCK_ROOT="+store.basePath)
		if output, err := child.CombinedOutput(); err != nil {
			t.Fatalf("cross-process lock check failed: %v\n%s", err, output)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestVaultBackupSourceLockChild(t *testing.T) {
	root := os.Getenv("CAAM_TEST_VAULT_BACKUP_LOCK_ROOT")
	if root == "" {
		return // Subprocess-only assertion, also safe in an ordinary test run.
	}
	store := NewStore(root)
	entered := false
	if err := store.withVaultBackupSource("source", func(string) error {
		entered = true
		return nil
	}); err == nil || entered {
		t.Fatalf("another process entered a locked backup source: %v", err)
	}
}
