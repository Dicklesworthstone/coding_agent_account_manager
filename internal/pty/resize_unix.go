//go:build unix

package pty

import (
	"os"
	"os/signal"
	"syscall"
)

// NotifyResize subscribes ch to window-size change notifications for the
// process's controlling terminal (SIGWINCH). The returned function cancels
// the subscription. Give ch a buffer of at least one: notifications are
// coalesced, never queued, so a receiver only needs to know that the size
// changed and re-read it.
func NotifyResize(ch chan<- os.Signal) (stop func()) {
	signal.Notify(ch, syscall.SIGWINCH)
	return func() { signal.Stop(ch) }
}
