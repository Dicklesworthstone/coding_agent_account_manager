//go:build unix

package pty

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"

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

	// Start the command with a PTY. A zero dimension would hand the child a
	// 0x0 terminal (most TUIs then guess 80x24 and never recover), so fall
	// back to the defaults for any axis the caller left unset.
	winSize := &pty.Winsize{
		Rows: c.opts.Rows,
		Cols: c.opts.Cols,
	}
	if defaults := DefaultOptions(); winSize.Rows == 0 || winSize.Cols == 0 {
		if winSize.Rows == 0 {
			winSize.Rows = defaults.Rows
		}
		if winSize.Cols == 0 {
			winSize.Cols = defaults.Cols
		}
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

// readPollInterval bounds each wait for PTY output so callers that loop on
// ReadOutput/WaitForPattern observe context cancellation promptly.
const readPollInterval = 100 * time.Millisecond

// waitReadable blocks until the PTY master has data to read, the slave side
// hung up, or timeout elapses (readable == hungUp == false).
//
// It uses poll(2) instead of the os.File read deadline on purpose: a PTY
// master is not deadline-capable on every platform. On macOS, kqueue refuses
// /dev/ptmx, so Go never registers the master with its poller and
// SetReadDeadline fails with "file type does not support deadline" — which
// made every deadline-based read path return an error on the first call and
// the wrapped tool's output vanish entirely. ReadLine has always used this
// poll-based approach; ReadOutput and WaitForPattern now share it.
func waitReadable(fd int, timeout time.Duration) (readable, hungUp bool, err error) {
	ms := int(timeout / time.Millisecond)
	for {
		pollFd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, perr := unix.Poll(pollFd, ms)
		if perr != nil {
			if perr == syscall.EINTR {
				continue
			}
			return false, false, fmt.Errorf("poll pty: %w", perr)
		}
		if n == 0 {
			return false, false, nil
		}
		revents := pollFd[0].Revents
		if revents&unix.POLLNVAL != 0 {
			return false, false, fmt.Errorf("poll pty: descriptor is closed")
		}
		return revents&unix.POLLIN != 0, revents&(unix.POLLHUP|unix.POLLERR) != 0, nil
	}
}

// readAfterPoll performs a single read once waitReadable reported data. A
// read failure caused by the slave side being gone (EIO on Linux, EOF on the
// BSDs/macOS) is normalized to io.EOF so callers can stop cleanly.
func readAfterPoll(ptmx *os.File, buf []byte) (int, error) {
	nread, err := ptmx.Read(buf)
	if err != nil {
		if err == io.EOF || errors.Is(err, syscall.EIO) {
			return nread, io.EOF
		}
		return nread, fmt.Errorf("read from pty: %w", err)
	}
	return nread, nil
}

// ReadOutput reads all available output from the PTY without blocking indefinitely.
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

	readable, hungUp, err := waitReadable(int(ptmx.Fd()), readPollInterval)
	if err != nil {
		return "", err
	}
	if !readable {
		if hungUp {
			return "", io.EOF // Process exited and nothing is left to drain
		}
		return "", nil // No data available within the poll interval
	}

	buf := make([]byte, 4096)
	nread, err := readAfterPoll(ptmx, buf)
	if nread > 0 {
		return string(buf[:nread]), nil
	}
	if err != nil {
		return "", err
	}
	return "", nil
}

// ReadLine reads a single line from the PTY output.
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

	var line []byte
	buf := make([]byte, 1)
	fd := int(ptmx.Fd())

	for {
		// Check context cancellation first
		select {
		case <-ctx.Done():
			return string(line), ctx.Err()
		default:
		}

		pollFd := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pollFd, 100)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return string(line), fmt.Errorf("poll pty: %w", err)
		}
		if n == 0 {
			continue
		}
		revents := pollFd[0].Revents
		if revents&(unix.POLLERR|unix.POLLNVAL) != 0 && revents&unix.POLLIN == 0 {
			return string(line), fmt.Errorf("pty poll error: revents=%d", revents)
		}
		if revents&unix.POLLHUP != 0 && revents&unix.POLLIN == 0 {
			return string(line), io.EOF
		}

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
			if os.IsTimeout(err) {
				continue // Timeout, check context and retry
			}
			// Check for path error
			if pathErr, ok := err.(*os.PathError); ok && pathErr.Timeout() {
				continue
			}
			return string(line), fmt.Errorf("read from pty: %w", err)
		}
	}
}

// WaitForPattern reads output until the pattern matches or timeout.
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

	var output []byte
	buf := make([]byte, 4096)
	fd := int(ptmx.Fd())

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

		// Short poll so the context is re-checked periodically
		readable, hungUp, err := waitReadable(fd, readPollInterval)
		if err != nil {
			return string(output), err
		}
		if !readable {
			if hungUp {
				return string(output), io.EOF
			}
			continue // Nothing yet, check context and retry
		}

		nread, err := readAfterPoll(ptmx, buf)
		if nread > 0 {
			output = append(output, buf[:nread]...)
			if pattern.Match(output) {
				return string(output), nil
			}
		}
		if err != nil {
			return string(output), err
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

// Resize sets the PTY window size (TIOCSWINSZ on the master). The kernel
// delivers SIGWINCH to the child's foreground process group, exactly as a
// real terminal emulator would on a window resize.
func (c *unixController) Resize(rows, cols uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.started {
		return fmt.Errorf("controller not started")
	}
	if c.closed {
		return ErrClosed
	}
	if rows == 0 || cols == 0 {
		return fmt.Errorf("invalid window size %dx%d: both dimensions must be non-zero", rows, cols)
	}

	if err := pty.Setsize(c.ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return fmt.Errorf("set pty size: %w", err)
	}
	return nil
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
