package authfile

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestVaultMutationLockCreatesPrivatePersistentLock(t *testing.T) {
	base := filepath.Join(t.TempDir(), "nested", "vault")
	vault := NewVault(base)
	release, err := vault.lockMutation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	info, err := os.Lstat(filepath.Join(base, vaultMutationLockName))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("regular lock was not created: %v", err)
	}
	dirInfo, err := os.Stat(base)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && (info.Mode().Perm() != 0600 || dirInfo.Mode().Perm() != 0700) {
		t.Fatalf("lock or new vault has nonprivate permissions: %v, %v", info.Mode(), dirInfo.Mode())
	}
	release()
	release() // Cleanup must be safe even when a caller releases early.
	nextRelease, err := NewVault(base).lockMutation()
	if err != nil {
		t.Fatal(err)
	}
	defer nextRelease()
	current, err := os.Lstat(filepath.Join(base, vaultMutationLockName))
	if err != nil || !os.SameFile(info, current) {
		t.Fatalf("release replaced the persistent lock inode: %v", err)
	}
}

func TestVaultMutationLockRejectsUnsafeFiles(t *testing.T) {
	for _, kind := range []string{"directory", "symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			base := t.TempDir()
			lockPath := filepath.Join(base, vaultMutationLockName)
			marker := filepath.Join(t.TempDir(), "unchanged")
			if err := os.WriteFile(marker, []byte("existing file"), 0600); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "directory":
				if err := os.Mkdir(lockPath, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(marker, lockPath); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "hardlink":
				if err := os.Link(marker, lockPath); err != nil {
					t.Skipf("hardlinks unavailable: %v", err)
				}
			}
			release, err := NewVault(base).lockMutation()
			if release == nil {
				t.Fatal("failure returned a nil cleanup function")
			}
			release()
			if err == nil {
				t.Fatal("unsafe mutation lock was accepted")
			}
			data, readErr := os.ReadFile(marker)
			if readErr != nil || string(data) != "existing file" {
				t.Fatalf("unsafe lock modified another file: %v", readErr)
			}
		})
	}
}

func TestVaultMutationLockAliasesShareOneLock(t *testing.T) {
	base := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(base, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	release, err := NewVault(base).lockMutation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	otherRelease, err := NewVault(alias).lockMutationContext(ctx)
	otherRelease()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("alternate directory path bypassed lock: %v", err)
	}
	release()
	otherRelease, err = NewVault(alias).lockMutation()
	if err != nil {
		t.Fatal(err)
	}
	otherRelease()
}

func TestVaultMutationLockRejectsReplacementWhileWaiting(t *testing.T) {
	for _, replace := range []string{"lock file", "vault directory", "directory alias"} {
		t.Run(replace, func(t *testing.T) {
			base := filepath.Join(t.TempDir(), "vault")
			if err := os.Mkdir(base, 0700); err != nil {
				t.Fatal(err)
			}
			configured := base
			if replace == "directory alias" {
				configured = filepath.Join(t.TempDir(), "alias")
				if err := os.Symlink(base, configured); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			// Capture the same descriptors a waiting process holds, then rebind
			// a pathname before acquisition. It must not authorize any writes.
			waiting, err := openVaultMutationLock(configured)
			if err != nil {
				t.Fatal(err)
			}
			defer waiting.close()
			switch replace {
			case "lock file":
				path := filepath.Join(base, vaultMutationLockName)
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "vault directory":
				if err := os.Rename(base, base+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(base, 0700); err != nil {
					t.Fatal(err)
				}
			case "directory alias":
				if err := os.Rename(configured, configured+".retained"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), configured); err != nil {
					t.Fatal(err)
				}
			}
			if err := waiting.acquire(context.Background()); err == nil {
				t.Fatal("replaced directory or lock authorized a mutation")
			}
		})
	}
}

func TestVaultMutationLockCancelledAndInvalidVault(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	base := filepath.Join(t.TempDir(), "not-created")
	release, err := NewVault(base).lockMutationContext(ctx)
	release()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled operation returned %v", err)
	}
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatalf("cancelled acquisition created a vault: %v", err)
	}
	for _, vault := range []*Vault{nil, NewVault("")} {
		release, err := vault.lockMutation()
		if release == nil || err == nil {
			t.Fatalf("invalid vault returned nil cleanup or no error: %v", err)
		}
		release()
	}
}

func TestVaultMutationLockExcludesAnotherProcessAndRecoversAfterExit(t *testing.T) {
	for _, mode := range []string{"release", "crash"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestVaultMutationLockProcessHelper$", "-test.count=1")
			command.Env = append(os.Environ(), "CAAM_TEST_VAULT_MUTATION_PATH="+base, "CAAM_TEST_VAULT_MUTATION_MODE="+mode)
			output, err := command.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			input, err := command.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			command.Stderr = os.Stderr
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				_ = input.Close()
				if !waited {
					_ = command.Process.Kill()
					_ = command.Wait()
				}
			}()
			line, err := bufio.NewReader(output).ReadString('\n')
			if err != nil || line != "locked\n" {
				t.Fatalf("child did not acquire the mutation lock: %q, %v", line, err)
			}
			info, err := os.Lstat(filepath.Join(base, vaultMutationLockName))
			if err != nil {
				t.Fatal(err)
			}
			waitCtx, waitCancel := context.WithTimeout(ctx, 100*time.Millisecond)
			release, err := NewVault(base).lockMutationContext(waitCtx)
			waitCancel()
			release()
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("second process bypassed the held vault lock: %v", err)
			}
			if _, err := io.WriteString(input, "exit\n"); err != nil {
				t.Fatal(err)
			}
			err = command.Wait()
			waited = true
			if mode == "crash" {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 86 {
					t.Fatalf("child did not exit without cleanup: %v", err)
				}
			} else if err != nil {
				t.Fatalf("child failed to release the lock: %v", err)
			}
			release, err = NewVault(base).lockMutation()
			if err != nil {
				t.Fatalf("kernel did not release the previous process lock: %v", err)
			}
			defer release()
			current, err := os.Lstat(filepath.Join(base, vaultMutationLockName))
			if err != nil || !os.SameFile(info, current) {
				t.Fatalf("recovery required replacing the lock file: %v", err)
			}
		})
	}
}

func TestVaultMutationLockProcessHelper(t *testing.T) {
	base := os.Getenv("CAAM_TEST_VAULT_MUTATION_PATH")
	if base == "" {
		return
	}
	release, err := NewVault(base).lockMutation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := io.WriteString(os.Stdout, "locked\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("CAAM_TEST_VAULT_MUTATION_MODE") == "crash" {
		os.Exit(86) // Deliberately bypass defers in this isolated subprocess.
	}
}
