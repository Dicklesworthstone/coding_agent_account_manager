package authfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const vaultMutationLockName = ".caam-mutation.lock"

var errVaultMutationLockBusy = errors.New("another vault mutation is in progress")

// WithMutationLock coordinates a complete vault operation with managed backup,
// switching, and synchronization. Exporters may use it to capture a consistent
// snapshot. The callback must retain generation checks for unmanaged writers
// and must not call another mutating Vault method or recursively take this lock.
func (v *Vault) WithMutationLock(operation func() error) error {
	if operation == nil {
		return errors.New("vault operation is required")
	}
	switchMu.Lock()
	defer switchMu.Unlock()
	release, err := v.lockMutation()
	if err != nil {
		return err
	}
	defer release()
	return operation()
}

// lockMutation serializes managed mutations of the same vault across processes.
// Native clients do not take this lock, so callers must retain their captured
// credential and settings generation checks. Callers must not nest this lock.
func (v *Vault) lockMutation() (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return v.lockMutationContext(ctx)
}

func (v *Vault) lockMutationContext(ctx context.Context) (func(), error) {
	noop := func() {}
	if err := ctx.Err(); err != nil {
		return noop, err
	}
	if v == nil || strings.TrimSpace(v.basePath) == "" {
		return noop, errors.New("vault base path is empty")
	}
	lock, err := openVaultMutationLock(v.basePath)
	if err != nil {
		return noop, err
	}
	if err := lock.acquire(ctx); err != nil {
		lock.close()
		return noop, err
	}
	return lock.close, nil
}

type vaultMutationLock struct {
	basePath  string
	canonical string
	directory *os.Root
	dirInfo   os.FileInfo
	file      *os.File
	fileInfo  os.FileInfo
	closeOnce sync.Once
}

func openVaultMutationLock(basePath string) (_ *vaultMutationLock, resultErr error) {
	abs, err := filepath.Abs(basePath)
	if err != nil {
		return nil, fmt.Errorf("resolve vault mutation directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0700); err != nil {
		return nil, fmt.Errorf("create vault mutation directory: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve vault mutation directory: %w", err)
	}
	directory, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, fmt.Errorf("open vault mutation directory: %w", err)
	}
	lock := &vaultMutationLock{basePath: abs, canonical: canonical, directory: directory}
	defer func() {
		if resultErr != nil {
			lock.close()
		}
	}()
	lock.dirInfo, err = directory.Stat(".")
	if err != nil || !lock.dirInfo.IsDir() {
		return nil, errors.New("vault mutation directory changed while opening")
	}
	// Create exclusively, or pin the existing regular inode. Root keeps these
	// opens inside the captured directory even if its pathname is rebound.
	for attempt := 0; attempt < 2; attempt++ {
		before, statErr := directory.Lstat(vaultMutationLockName)
		if statErr != nil && !os.IsNotExist(statErr) {
			return nil, fmt.Errorf("inspect vault mutation lock: %w", statErr)
		}
		if statErr == nil && !before.Mode().IsRegular() {
			return nil, errors.New("vault mutation lock is not a regular file")
		}
		flags := os.O_RDWR | mutationLockOpenFlags
		if os.IsNotExist(statErr) {
			flags |= os.O_CREATE | os.O_EXCL
		}
		lock.file, err = directory.OpenFile(vaultMutationLockName, flags, 0600)
		if os.IsExist(err) && os.IsNotExist(statErr) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("open vault mutation lock: %w", err)
		}
		lock.fileInfo, err = lock.file.Stat()
		if err != nil || !lock.fileInfo.Mode().IsRegular() || (before != nil && !os.SameFile(before, lock.fileInfo)) {
			return nil, errors.New("vault mutation lock changed while opening")
		}
		if err := lock.unchanged(); err != nil {
			return nil, err
		}
		if err := lock.file.Chmod(0600); err != nil {
			return nil, fmt.Errorf("secure vault mutation lock: %w", err)
		}
		return lock, nil
	}
	return nil, errors.New("vault mutation lock changed while creating")
}

func (lock *vaultMutationLock) unchanged() error {
	// Both spellings must still address the captured directory. This also
	// makes two configured aliases use one lock without trusting a retargeted
	// symlink or a newly created replacement directory while waiting.
	currentDir, err := os.Stat(lock.basePath)
	canonicalDir, canonicalErr := os.Lstat(lock.canonical)
	if err != nil || canonicalErr != nil || !canonicalDir.IsDir() ||
		!os.SameFile(lock.dirInfo, currentDir) || !os.SameFile(lock.dirInfo, canonicalDir) {
		return errors.New("vault mutation directory changed while locking")
	}
	current, err := lock.directory.Lstat(vaultMutationLockName)
	opened, openedErr := lock.file.Stat()
	if err != nil || openedErr != nil || !current.Mode().IsRegular() ||
		!os.SameFile(lock.fileInfo, current) || !os.SameFile(lock.fileInfo, opened) {
		return errors.New("vault mutation lock changed while locking")
	}
	links, err := mutationLockLinkCount(lock.file, opened)
	if err != nil {
		return fmt.Errorf("inspect vault mutation lock links: %w", err)
	}
	if links != 1 {
		return errors.New("vault mutation lock has multiple file links")
	}
	return nil
}

func (lock *vaultMutationLock) acquire(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("vault is busy with another update: %w", err)
		}
		if err := lock.unchanged(); err != nil {
			return err
		}
		if err := tryVaultMutationLock(lock.file); err == nil {
			// A waiter may have held an obsolete lock inode when the preceding
			// owner exited. Never authorize writes under that discarded lock.
			return lock.unchanged()
		} else if !errors.Is(err, errVaultMutationLockBusy) {
			return fmt.Errorf("lock vault mutation: %w", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("vault is busy with another update: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

func (lock *vaultMutationLock) close() {
	lock.closeOnce.Do(func() {
		if lock.file != nil {
			_ = unlockVaultMutationFile(lock.file)
			_ = lock.file.Close()
		}
		if lock.directory != nil {
			_ = lock.directory.Close()
		}
	})
}
