package exec

import (
	"io"
	"os"
	"sync"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
	"golang.org/x/term"
)

// Terminal proxying for PTY-wrapped runs (issue #74).
//
// SmartRunner executes the wrapped CLI on its own PTY so it can watch the
// output stream for rate-limit signatures and inject a login command during
// a handoff. That makes caam a terminal proxy, and a proxy has three duties
// the original wrapper skipped:
//
//  1. size — the PTY must be created at the real terminal's size and follow
//     it afterwards (SIGWINCH), or full-screen TUIs draw at the 80x24 default
//     for the life of the session;
//  2. raw mode — while the child runs, the real terminal must be in raw mode
//     so keystrokes are neither echoed nor interpreted by the OUTER line
//     discipline. Without it, arrow keys, Esc and Alt-combos are echoed by the
//     user's own terminal as literal "^[[A" text, and Ctrl-C interrupts the
//     wrapper instead of the tool;
//  3. input — bytes read from stdin must be relayed into the PTY verbatim.
//     The original wrapper relayed nothing, so the tool never received a
//     single keystroke.
//
// The child's PTY keeps its ordinary cooked settings (ONLCR, ISIG, ...), so
// its output already arrives CRLF-terminated and its own Ctrl-C handling is
// unchanged.

// terminalSize reports the window size of the terminal behind fd. ok is false
// when fd is not a terminal or the size is unknown or zero, in which case the
// caller should keep its current size rather than shrink the PTY to 0x0.
func terminalSize(fd int) (rows, cols uint16, ok bool) {
	width, height, err := term.GetSize(fd)
	if err != nil || width <= 0 || height <= 0 {
		return 0, 0, false
	}
	return clampWinsize(height), clampWinsize(width), true
}

// clampWinsize narrows an int dimension to the uint16 a PTY winsize carries.
func clampWinsize(v int) uint16 {
	const maxWinsize = int(^uint16(0))
	if v > maxWinsize {
		return uint16(maxWinsize)
	}
	return uint16(v)
}

// watchTerminalResize keeps ctrl's PTY sized like the terminal behind fd:
// each time the terminal reports a size change, the current size is re-read
// and applied. The returned function stops watching and is safe to call more
// than once.
func watchTerminalResize(fd int, ctrl pty.Controller) (stop func()) {
	sigCh := make(chan os.Signal, 1)
	unsubscribe := pty.NotifyResize(sigCh)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-sigCh:
				if rows, cols, ok := terminalSize(fd); ok {
					// Best effort: a closed PTY only means the child is gone.
					_ = ctrl.Resize(rows, cols)
				}
			case <-done:
				return
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			unsubscribe()
			close(done)
		})
	}
}

// relayStdin copies everything read from in into the PTY until in is
// exhausted or the PTY stops accepting input. Bytes are forwarded verbatim —
// escape sequences included — so the child sees exactly what the user typed.
//
// No end-of-file indication is synthesized when in closes: a PTY cannot carry
// EOF, and injecting the VEOF character would reach the child as a Ctrl-D
// keystroke, which full-screen tools treat as a command. A closed stdin
// therefore simply means "no more input"; the child keeps running and can
// still receive injected commands (e.g. the handoff login) until it exits.
func relayStdin(in io.Reader, ctrl pty.Controller) {
	buf := make([]byte, 4096)
	for {
		n, err := in.Read(buf)
		if n > 0 {
			if werr := ctrl.InjectRaw(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
