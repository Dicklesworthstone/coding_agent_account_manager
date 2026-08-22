//go:build unix

package exec

import (
	"os"
	"syscall"
	"testing"
	"time"

	creack "github.com/creack/pty"
)

// openPTYPair returns a master/slave pair so tests can exercise real
// TIOCGWINSZ/TIOCSWINSZ behaviour without needing a controlling terminal.
func openPTYPair(t *testing.T) (master, slave *os.File) {
	t.Helper()
	m, s, err := creack.Open()
	if err != nil {
		t.Skipf("cannot open a pty on this host: %v", err)
	}
	t.Cleanup(func() {
		m.Close()
		s.Close()
	})
	return m, s
}

func TestTerminalSizeReadsWindowSize(t *testing.T) {
	master, slave := openPTYPair(t)
	if err := creack.Setsize(master, &creack.Winsize{Rows: 47, Cols: 238}); err != nil {
		t.Fatalf("Setsize: %v", err)
	}

	rows, cols, ok := terminalSize(int(slave.Fd()))
	if !ok {
		t.Fatal("terminalSize reported !ok for a pty slave")
	}
	if rows != 47 || cols != 238 {
		t.Fatalf("terminalSize = %dx%d, want 47x238", rows, cols)
	}
}

func TestTerminalSizeRejectsNonTerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if rows, cols, ok := terminalSize(int(f.Fd())); ok {
		t.Fatalf("terminalSize on a regular file reported ok (%dx%d)", rows, cols)
	}
}

func waitForResize(t *testing.T, fc *fakeController, want [2]uint16) bool {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range fc.resizeCalls() {
			if r == want {
				return true
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func TestWatchTerminalResizeAppliesTerminalSize(t *testing.T) {
	master, slave := openPTYPair(t)
	if err := creack.Setsize(master, &creack.Winsize{Rows: 40, Cols: 120}); err != nil {
		t.Fatalf("Setsize: %v", err)
	}

	fc := &fakeController{}
	stop := watchTerminalResize(int(slave.Fd()), fc)
	defer stop()

	// Simulate the terminal emulator reporting a resize.
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !waitForResize(t, fc, [2]uint16{40, 120}) {
		t.Fatalf("PTY was not resized to the terminal's 40x120 after SIGWINCH (calls: %v)", fc.resizeCalls())
	}

	// After stop, further resize notifications must be ignored.
	stop()
	before := len(fc.resizeCalls())
	if err := syscall.Kill(os.Getpid(), syscall.SIGWINCH); err != nil {
		t.Fatalf("kill: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	if after := len(fc.resizeCalls()); after != before {
		t.Fatalf("Resize applied after stop (%d -> %d calls)", before, after)
	}
}
