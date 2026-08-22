//go:build windows

package pty

import "os"

// NotifyResize is a no-op on Windows: there is no SIGWINCH equivalent, and
// the PTY controller itself is unsupported there (see NewController). The
// returned function is safe to call.
func NotifyResize(ch chan<- os.Signal) (stop func()) {
	return func() {}
}
