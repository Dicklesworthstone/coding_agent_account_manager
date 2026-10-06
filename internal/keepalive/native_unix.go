//go:build !windows

package keepalive

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// Keep native wrappers and their children in one process group. Stop that
// group before releasing the grant lock, including children left running
// after their wrapper exits successfully.
func runNativeCommand(_ context.Context, command *exec.Cmd) error {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	killGroup := func() error {
		if command.Process == nil {
			return os.ErrProcessDone
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	command.Cancel = killGroup
	if err := command.Start(); err != nil {
		return err
	}
	waitErr := command.Wait()
	if err := killGroup(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return errors.Join(waitErr, errNativeCleanup)
	}
	return waitErr
}
