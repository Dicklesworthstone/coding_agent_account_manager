//go:build windows

package signals

import "fmt"

func SendHUP(pid int) error {
	return fmt.Errorf("SIGHUP not supported on Windows (pid=%d)", pid)
}
