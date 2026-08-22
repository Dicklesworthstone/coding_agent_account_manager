// Package pty provides PTY (pseudo-terminal) control for command injection
// and output monitoring. This enables the smart session handoff feature
// where we can inject commands like /login into a running CLI session.
package pty

import (
	"context"
	"errors"
	"regexp"
	"time"
)

// ErrNotSupported is returned when PTY operations are not supported on
// the current platform (e.g., Windows without ConPTY).
var ErrNotSupported = errors.New("PTY operations not supported on this platform")

// ErrTimeout is returned when a wait operation times out.
var ErrTimeout = errors.New("operation timed out")

// ErrClosed is returned when operating on a closed controller.
var ErrClosed = errors.New("PTY controller is closed")

// Controller manages a PTY-wrapped command, allowing command injection
// and output monitoring. It enables typing commands into a running process
// and reading its output.
type Controller interface {
	// Start begins execution of the wrapped command.
	// Must be called before InjectCommand or ReadOutput.
	Start() error

	// InjectCommand types a command into the PTY as if a user typed it.
	// The command string is written followed by a newline.
	InjectCommand(cmd string) error

	// InjectRaw writes raw bytes to the PTY without adding a newline.
	// Useful for control characters or partial input.
	InjectRaw(data []byte) error

	// ReadOutput reads available output from the PTY.
	// Returns immediately with whatever output is available.
	// Returns empty string if no output is available.
	ReadOutput() (string, error)

	// ReadLine reads a single line from the PTY output.
	// Blocks until a newline is received or context is cancelled.
	ReadLine(ctx context.Context) (string, error)

	// WaitForPattern reads output until the pattern matches or timeout.
	// Returns the matched output on success.
	WaitForPattern(ctx context.Context, pattern *regexp.Regexp, timeout time.Duration) (string, error)

	// Wait waits for the command to exit and returns its exit code.
	Wait() (int, error)

	// Signal sends a signal to the running process (e.g., SIGINT).
	Signal(sig Signal) error

	// Resize sets the PTY window size. The child is notified the way a real
	// terminal would notify it (SIGWINCH on Unix), so full-screen programs
	// redraw at the new size. Both dimensions must be non-zero.
	Resize(rows, cols uint16) error

	// Close terminates the PTY and cleans up resources.
	// If the command is still running, it will be killed.
	Close() error

	// Fd returns the file descriptor of the PTY master.
	// Returns -1 if not available or not supported.
	Fd() int
}

// Signal represents a process signal.
type Signal int

const (
	// SIGINT is the interrupt signal (Ctrl+C).
	SIGINT Signal = iota
	// SIGTERM is the termination signal.
	SIGTERM
	// SIGKILL is the kill signal (cannot be caught).
	SIGKILL
	// SIGHUP is the hangup signal.
	SIGHUP
)

// Options configures PTY controller behavior.
type Options struct {
	// Rows is the number of terminal rows (default: 24).
	Rows uint16
	// Cols is the number of terminal columns (default: 80).
	Cols uint16
	// Dir is the working directory for the command.
	Dir string
	// Env is additional environment variables for the command.
	Env []string
}

// DefaultOptions returns sensible default options.
func DefaultOptions() *Options {
	return &Options{
		Rows: 24,
		Cols: 80,
	}
}
