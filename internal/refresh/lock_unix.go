//go:build !windows

package refresh

import (
	"errors"
	"os"
	"syscall"
)

func tryRefreshLock(file *os.File) error {
	err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return errRefreshLockBusy
	}
	return err
}

func unlockRefreshFile(file *os.File) error {
	return syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
}

func refreshFileLinkCount(_ *os.File, info os.FileInfo) (uint64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("credential link count unavailable")
	}
	return uint64(stat.Nlink), nil
}
