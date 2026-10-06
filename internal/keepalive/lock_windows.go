//go:build windows

package keepalive

import (
	"errors"
	"os"
	"os/exec"

	"golang.org/x/sys/windows"
)

func tryFileLock(file *os.File) error {
	err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 0xffffffff, 0xffffffff, &windows.Overlapped{})
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return errLockBusy
	}
	return err
}

func unlockFile(file *os.File) error {
	return windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 0xffffffff, 0xffffffff, &windows.Overlapped{})
}

// CommandContext terminates the direct native process on Windows. Unix also
// uses a process group because CLI launchers there commonly spawn descendants.
func configureNativeProcess(_ *exec.Cmd) {}

func nativeFileLinkCount(path string, _ os.FileInfo) (uint64, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return 0, err
	}
	return uint64(info.NumberOfLinks), nil
}
