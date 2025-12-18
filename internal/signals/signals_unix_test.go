//go:build !windows

package signals

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestHandler_ReloadOnHUP(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close()

	if err := SendHUP(os.Getpid()); err != nil {
		t.Fatalf("SendHUP: %v", err)
	}

	select {
	case <-h.Reload():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for reload event")
	}
}

func TestHandler_DumpOnUSR1(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close()

	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case <-h.DumpStats():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dump event")
	}
}

func TestHandler_ShutdownOnTERM(t *testing.T) {
	h, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer h.Close()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("kill: %v", err)
	}

	select {
	case <-h.Shutdown():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for shutdown event")
	}
}
