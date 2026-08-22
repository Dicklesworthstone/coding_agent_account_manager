//go:build unix

package pty

// Tests for issue #74: a PTY-wrapped child must see the window size the
// wrapper asked for, must be told when that size changes, and the wrapper's
// output readers must work on platforms where the PTY master has no read
// deadline support (macOS).

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func requireStty(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("stty"); err != nil {
		t.Skip("stty not available")
	}
}

func mustStart(t *testing.T, name string, args []string, opts *Options) Controller {
	t.Helper()
	ctrl, err := NewControllerFromArgs(name, args, opts)
	if err != nil {
		t.Fatalf("NewControllerFromArgs: %v", err)
	}
	t.Cleanup(func() { ctrl.Close() })
	if err := ctrl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return ctrl
}

func TestStartHonorsRequestedSize(t *testing.T) {
	requireStty(t)
	// stty reports the size of its stdin, which is the PTY slave.
	ctrl := mustStart(t, "stty", []string{"size"}, &Options{Rows: 47, Cols: 238})

	out, err := ctrl.WaitForPattern(context.Background(), regexp.MustCompile(`\b47 238\b`), 5*time.Second)
	if err != nil {
		t.Fatalf("child did not report the requested 47x238 size: %v (output %q)", err, out)
	}
}

func TestStartDefaultsZeroDimensions(t *testing.T) {
	requireStty(t)
	// An Options with unset dimensions must not hand the child a 0x0 terminal.
	ctrl := mustStart(t, "stty", []string{"size"}, &Options{})

	defaults := DefaultOptions()
	want := regexp.MustCompile(fmt.Sprintf(`\b%d %d\b`, defaults.Rows, defaults.Cols))
	out, err := ctrl.WaitForPattern(context.Background(), want, 5*time.Second)
	if err != nil {
		t.Fatalf("child did not report the default %dx%d size: %v (output %q)", defaults.Rows, defaults.Cols, err, out)
	}
}

func TestResizeDeliversNewSizeToChild(t *testing.T) {
	requireStty(t)
	// The child re-reads its window size continuously; after Resize it must
	// observe the new dimensions (TIOCSWINSZ on the master updates the shared
	// winsize and raises SIGWINCH in the child's process group).
	ctrl := mustStart(t, "sh", []string{"-c", "while :; do stty size; sleep 0.05; done"}, nil)

	if _, err := ctrl.WaitForPattern(context.Background(), regexp.MustCompile(`\b24 80\b`), 5*time.Second); err != nil {
		t.Fatalf("child never reported the initial default size: %v", err)
	}

	if err := ctrl.Resize(31, 101); err != nil {
		t.Fatalf("Resize: %v", err)
	}

	out, err := ctrl.WaitForPattern(context.Background(), regexp.MustCompile(`\b31 101\b`), 5*time.Second)
	if err != nil {
		t.Fatalf("child did not observe the resized 31x101 window: %v (output %q)", err, out)
	}
}

func TestResizeValidation(t *testing.T) {
	ctrl, err := NewControllerFromArgs("cat", nil, nil)
	if err != nil {
		t.Fatalf("NewControllerFromArgs: %v", err)
	}
	if err := ctrl.Resize(10, 10); err == nil {
		t.Fatal("Resize before Start must fail")
	}

	if err := ctrl.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := ctrl.Resize(0, 10); err == nil {
		t.Error("Resize with zero rows must fail")
	}
	if err := ctrl.Resize(10, 0); err == nil {
		t.Error("Resize with zero cols must fail")
	}
	if err := ctrl.Resize(10, 10); err != nil {
		t.Errorf("Resize with a valid size failed: %v", err)
	}

	if err := ctrl.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := ctrl.Resize(10, 10); err != ErrClosed {
		t.Errorf("Resize after Close = %v, want ErrClosed", err)
	}
}

func TestReadOutputDrainsThenReportsEOF(t *testing.T) {
	// Exercises the poll-based reader end to end: data is delivered, and once
	// the child has exited the reader reports io.EOF instead of spinning or
	// failing with a deadline error (the macOS failure mode before #74).
	ctrl := mustStart(t, "echo", []string{"READ_OUTPUT_MARKER"}, nil)

	var collected strings.Builder
	deadline := time.Now().Add(5 * time.Second)
	sawEOF := false
	for time.Now().Before(deadline) {
		out, err := ctrl.ReadOutput()
		collected.WriteString(out)
		if err == io.EOF {
			sawEOF = true
			break
		}
		if err != nil {
			t.Fatalf("ReadOutput: %v (collected %q)", err, collected.String())
		}
	}
	if !sawEOF {
		t.Fatalf("ReadOutput never reported EOF after the child exited (collected %q)", collected.String())
	}
	if !strings.Contains(collected.String(), "READ_OUTPUT_MARKER") {
		t.Fatalf("child output was not delivered: %q", collected.String())
	}
}

func TestNotifyResizeDeliversAndStops(t *testing.T) {
	ch := make(chan os.Signal, 1)
	stop := NotifyResize(ch)

	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		stop()
		t.Fatal("SIGWINCH was not delivered to the subscribed channel")
	}

	stop()
	select {
	case <-ch: // drain anything coalesced before stop
	default:
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill: %v", err)
	}
	select {
	case <-ch:
		t.Fatal("SIGWINCH delivered after the subscription was stopped")
	case <-time.After(200 * time.Millisecond):
	}
}
