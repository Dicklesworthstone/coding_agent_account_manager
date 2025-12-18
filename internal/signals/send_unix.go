//go:build !windows

package signals

import "syscall"

func SendHUP(pid int) error {
	return syscall.Kill(pid, syscall.SIGHUP)
}
