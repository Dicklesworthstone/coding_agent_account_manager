//go:build windows

package refresh

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

func tryRefreshLock(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 0xffffffff, 0xffffffff, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errRefreshLockBusy
	}
	return err
}

func unlockRefreshFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 0xffffffff, 0xffffffff, &windows.Overlapped{})
}

func refreshFileLinkCount(file *os.File, _ os.FileInfo) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}
