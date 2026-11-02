//go:build !windows

package authfile

import (
	"errors"
	"os"
	"syscall"
)

// A substituted FIFO must not block before regular-file validation runs.
const mutationLockOpenFlags = syscall.O_NONBLOCK

func tryVaultMutationLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errVaultMutationLockBusy
	}
	return err
}

func unlockVaultMutationFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func mutationLockLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("lock link count unavailable")
	}
	return uint64(stat.Nlink), nil
}
