//go:build unix

package pty

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/creack/pty"
	"golang.org/x/sys/unix"
)

// unixController implements Controller for Unix systems (Linux, macOS, BSD).
type unixController struct {
	cmd  *exec.Cmd
	ptmx *os.File // PTY master
	opts *Options

	mu      sync.Mutex
	started bool
	closed  bool
}

// NewController creates a new PTY controller wrapping the given command.
// The command should not be started - NewController will start it.
func NewController(cmd *exec.Cmd, opts *Options) (Controller, error) {
	if cmd == nil {
		return nil, fmt.Errorf("cmd cannot be nil")
	}
	if opts == nil {
		opts = DefaultOptions()
	}

	return &unixController{
		cmd:  cmd,
		opts: opts,
	}, nil
}

// NewControllerFromArgs creates a new PTY controller for the given command and arguments.
func NewControllerFromArgs(name string, args []string, opts *Options) (Controller, error) {
	cmd := exec.Command(name, args...)
	return NewController(cmd, opts)
}

// Start begins execution of the wrapped command in a PTY.
func (c *unixController) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.started {
		return fmt.Errorf("controller already started")
	}
	if c.closed {
		return ErrClosed
	}

	// Apply options
	if c.opts.Dir != "" {
		c.cmd.Dir = c.opts.Dir
	}
	if len(c.opts.Env) > 0 {
		c.cmd.Env = append(os.Environ(), c.opts.Env...)
	}

	// Start the command with a PTY
	winSize := &pty.Winsize{
		Rows: c.opts.Rows,
		Cols: c.opts.Cols,
	}

	ptmx, err := pty.StartWithSize(c.cmd, winSize)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}

	c.ptmx = ptmx
	c.started = true

	return nil
}

// InjectCommand types a command into the PTY followed by a newline.
func (c *unixController) InjectCommand(cmd string) error {
	return c.InjectRaw([]byte(cmd + "\n"))
}

// InjectRaw writes raw bytes to the PTY.
func (c *unixController) InjectRaw(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return fmt.Errorf("controller not started")
	}
	if c.closed {
		return ErrClosed
	}

	_, err := c.ptmx.Write(data)
	if err != nil {
		return fmt.Errorf("write to pty: %w", err)
	}
	return nil
}

// ReadOutput reads all available output from the PTY without blocking.
// Note: This spawns a goroutine that may outlive the timeout if the PTY
// is blocked. The goroutine will terminate when Close() is called.
func (c *unixController) ReadOutput() (string, error) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return "", fmt.Errorf("controller not started")
	}
	if c.closed {
		c.mu.Unlock()
		return "", ErrClosed
	}
	ptmx := c.ptmx
	c.mu.Unlock()

	fd := int(ptmx.Fd())
	if fd < 0 {
		return "", fmt.Errorf("invalid pty fd")
	}

	var readfds unix.FdSet
	if err := fdSet(fd, &readfds); err != nil {
		return "", err
	}

	timeout := unix.Timeval{Sec: 0, Usec: 100000} // 100ms
	n, err := unix.Select(fd+1, &readfds, nil, nil, &timeout)
	if err != nil {
		if err == unix.EINTR {
			return "", nil
		}
		return "", fmt.Errorf("select on pty: %w", err)
	}
	if n == 0 || !fdIsSet(fd, &readfds) {
		return "", nil
	}

	buf := make([]byte, 4096)
	nread, err := ptmx.Read(buf)
	if nread > 0 {
		return string(buf[:nread]), nil
	}
	if err != nil && err != io.EOF {
		return "", fmt.Errorf("read from pty: %w", err)
	}
	return "", nil
}

func fdSet(fd int, set *unix.FdSet) error {
	if fd < 0 {
		return fmt.Errorf("invalid fd")
	}
	bitsPerWord := uint(unsafe.Sizeof(set.Bits[0]) * 8)
	idx := fd / int(bitsPerWord)
	if idx >= len(set.Bits) {
		return fmt.Errorf("fd %d out of range", fd)
	}
	set.Bits[idx] |= 1 << (uint(fd) % bitsPerWord)
	return nil
}

func fdIsSet(fd int, set *unix.FdSet) bool {
	if fd < 0 {
		return false
	}
	bitsPerWord := uint(unsafe.Sizeof(set.Bits[0]) * 8)
	idx := fd / int(bitsPerWord)
	if idx >= len(set.Bits) {
		return false
	}
	return set.Bits[idx]&(1<<(uint(fd)%bitsPerWord)) != 0
}

// ReadLine reads a single line from the PTY output.
// Uses select() with short timeouts for interruptibility without goroutine leaks.
func (c *unixController) ReadLine(ctx context.Context) (string, error) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return "", fmt.Errorf("controller not started")
	}
	if c.closed {
		c.mu.Unlock()
		return "", ErrClosed
	}
	ptmx := c.ptmx
	c.mu.Unlock()

	fd := int(ptmx.Fd())
	if fd < 0 {
		return "", fmt.Errorf("invalid pty fd")
	}

	var line []byte
	buf := make([]byte, 1)

	for {
		// Check context cancellation first
		select {
		case <-ctx.Done():
			return string(line), ctx.Err()
		default:
		}

		// Use select() with short timeout for interruptibility
		var readfds unix.FdSet
		if err := fdSet(fd, &readfds); err != nil {
			return string(line), err
		}

		// 100ms timeout allows context cancellation checks
		selectTimeout := unix.Timeval{Sec: 0, Usec: 100000}
		n, err := unix.Select(fd+1, &readfds, nil, nil, &selectTimeout)
		if err != nil {
			if err == unix.EINTR {
				continue // Interrupted, retry
			}
			return string(line), fmt.Errorf("select on pty: %w", err)
		}

		if n == 0 || !fdIsSet(fd, &readfds) {
			// Timeout - loop back to check context
			continue
		}

		// Data available, read one byte at a time
		nread, err := ptmx.Read(buf)
		if nread > 0 {
			line = append(line, buf[0])
			if buf[0] == '\n' {
				return string(line), nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return string(line), io.EOF
			}
			return string(line), fmt.Errorf("read from pty: %w", err)
		}
	}
}

// WaitForPattern reads output until the pattern matches or timeout.
// Uses select() with short timeouts for interruptibility without goroutine leaks.
func (c *unixController) WaitForPattern(ctx context.Context, pattern *regexp.Regexp, timeout time.Duration) (string, error) {
	if pattern == nil {
		return "", fmt.Errorf("pattern cannot be nil")
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", ErrClosed
	}
	ptmx := c.ptmx
	c.mu.Unlock()

	fd := int(ptmx.Fd())
	if fd < 0 {
		return "", fmt.Errorf("invalid pty fd")
	}

	var output []byte
	buf := make([]byte, 4096)

	for {
		// Check context cancellation first
		select {
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return string(output), ErrTimeout
			}
			return string(output), ctx.Err()
		default:
		}

		// Use select() with short timeout for interruptibility
		var readfds unix.FdSet
		if err := fdSet(fd, &readfds); err != nil {
			return string(output), err
		}

		// 100ms timeout allows context cancellation checks
		selectTimeout := unix.Timeval{Sec: 0, Usec: 100000}
		n, err := unix.Select(fd+1, &readfds, nil, nil, &selectTimeout)
		if err != nil {
			if err == unix.EINTR {
				continue // Interrupted, retry
			}
			return string(output), fmt.Errorf("select on pty: %w", err)
		}

		if n == 0 || !fdIsSet(fd, &readfds) {
			// Timeout - loop back to check context
			continue
		}

		// Data available, read it
		nread, err := ptmx.Read(buf)
		if nread > 0 {
			output = append(output, buf[:nread]...)
			if pattern.Match(output) {
				return string(output), nil
			}
		}
		if err != nil {
			if err == io.EOF {
				return string(output), io.EOF
			}
			return string(output), fmt.Errorf("read from pty: %w", err)
		}
	}
}

// Wait waits for the command to exit and returns its exit code.
func (c *unixController) Wait() (int, error) {
	c.mu.Lock()
	if !c.started {
		c.mu.Unlock()
		return -1, fmt.Errorf("controller not started")
	}
	cmd := c.cmd
	c.mu.Unlock()

	err := cmd.Wait()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode(), nil
		}
		return -1, fmt.Errorf("wait: %w", err)
	}
	return 0, nil
}

// Signal sends a signal to the running process.
func (c *unixController) Signal(sig Signal) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return fmt.Errorf("controller not started")
	}
	if c.closed {
		return ErrClosed
	}
	if c.cmd.Process == nil {
		return fmt.Errorf("process not running")
	}

	var s syscall.Signal
	switch sig {
	case SIGINT:
		s = syscall.SIGINT
	case SIGTERM:
		s = syscall.SIGTERM
	case SIGKILL:
		s = syscall.SIGKILL
	case SIGHUP:
		s = syscall.SIGHUP
	default:
		return fmt.Errorf("unknown signal: %d", sig)
	}

	return c.cmd.Process.Signal(s)
}

// Close terminates the PTY and cleans up resources.
func (c *unixController) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}
	c.closed = true

	var firstErr error

	// Close the PTY master (this will cause the child to receive SIGHUP)
	if c.ptmx != nil {
		if err := c.ptmx.Close(); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("close pty: %w", err)
		}
	}

	// Kill the process if still running
	if c.cmd != nil && c.cmd.Process != nil {
		// Try graceful termination first
		c.cmd.Process.Signal(syscall.SIGTERM)

		// Give it a moment to exit
		done := make(chan struct{})
		go func() {
			c.cmd.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Process exited
		case <-time.After(100 * time.Millisecond):
			// Force kill
			c.cmd.Process.Kill()
		}
	}

	return firstErr
}

// Fd returns the file descriptor of the PTY master.
func (c *unixController) Fd() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.ptmx == nil {
		return -1
	}
	return int(c.ptmx.Fd())
}
