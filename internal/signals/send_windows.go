//go:build windows

package signals

import "fmt"

func SendHUP(pid int) error {
	return fmt.Errorf("SIGHUP not supported on Windows (pid=%d)", pid)
}

// SendTerm is unsupported on Windows; daemon reload must be done manually.
func SendTerm(pid int) error {
	return fmt.Errorf("SIGTERM not supported on Windows (pid=%d)", pid)
}
