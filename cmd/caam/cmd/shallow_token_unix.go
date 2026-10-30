//go:build unix

package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

const (
	shallowTokenFDSupported        = true
	shallowClaudeOAuthTokenMaxSize = 16 * 1024
	shallowClaudeOAuthTokenTimeout = 5 * time.Second
)

// readShallowClaudeOAuthToken consumes a dedicated inherited read descriptor.
// The sender must close its writer after the token and must not concurrently
// read from, reuse, or change the descriptor. Pipes temporarily use nonblocking
// mode so a stalled sender cannot hang the launch; their original status flags
// are restored before close, including on errors. Duplicated descriptors share
// these status flags, so the caller must dedicate the read end to this launch.
//
// Ownership ends here, before syscall.Exec: a deferred close in the caller would
// never run after a successful exec. Neither the descriptor nor a token file is
// handed to the child; the caller installs the token in its explicit environment.
func readShallowClaudeOAuthToken(ctx context.Context, fd int) (string, error) {
	return readShallowClaudeOAuthTokenWithin(ctx, fd, shallowClaudeOAuthTokenTimeout)
}

func readShallowClaudeOAuthTokenWithin(ctx context.Context, fd int, timeout time.Duration) (token string, resultErr error) {
	// PollFd uses int32 even on 64-bit systems. Never truncate an input into a
	// different descriptor, and never consume the launch's interactive stdio.
	if fd < 3 || fd > 1<<31-1 {
		return "", errors.New("--claude-oauth-token-fd requires an open descriptor between 3 and 2147483647")
	}
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		return "", errors.New("--claude-oauth-token-fd: descriptor is not open")
	}

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return "", errors.New("--claude-oauth-token-fd: cannot inspect credential descriptor")
	}
	kind := stat.Mode & unix.S_IFMT
	if kind != unix.S_IFIFO && kind != unix.S_IFREG {
		return "", errors.New("--claude-oauth-token-fd: descriptor must refer to a readable pipe or regular file")
	}
	if flags&unix.O_ACCMODE == unix.O_WRONLY {
		return "", errors.New("--claude-oauth-token-fd: descriptor is not readable")
	}

	// Adopt only a usable input descriptor. A mistaken number can name a Go
	// runtime poll descriptor; rejecting it must neither change nor close it.
	changedFlags := false
	defer func() {
		if changedFlags {
			if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags); err != nil && resultErr == nil {
				token = ""
				resultErr = errors.New("--claude-oauth-token-fd: cannot restore descriptor flags")
			}
		}
		// Close exactly once; retrying close after an error could close an
		// unrelated descriptor if the number has already been reused.
		if err := unix.Close(fd); err != nil && resultErr == nil {
			token = ""
			resultErr = errors.New("--claude-oauth-token-fd: cannot close credential descriptor")
		}
	}()

	fdFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
	if err != nil {
		return "", errors.New("--claude-oauth-token-fd: cannot inspect descriptor flags")
	}
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFD, fdFlags|unix.FD_CLOEXEC); err != nil {
		return "", errors.New("--claude-oauth-token-fd: cannot protect credential descriptor")
	}
	if kind == unix.S_IFIFO && flags&unix.O_NONBLOCK == 0 {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_SETFL, flags|unix.O_NONBLOCK); err != nil {
			return "", errors.New("--claude-oauth-token-fd: cannot prepare credential pipe")
		}
		changedFlags = true
	}

	// Allow one terminal CRLF in addition to a maximum-size token, and read
	// one extra byte to distinguish an oversized input without unbounded memory.
	data := make([]byte, shallowClaudeOAuthTokenMaxSize+3)
	defer clear(data)
	used := 0
	deadline := time.Now().Add(timeout)
	for {
		if err := shallowTokenReadContextError(ctx); err != nil {
			return "", err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return "", fmt.Errorf("--claude-oauth-token-fd: timed out waiting for token and EOF: %w", context.DeadlineExceeded)
		}
		if kind == unix.S_IFIFO {
			// A short poll interval bounds cancellation latency while the
			// absolute deadline also bounds a sender that trickles bytes.
			wait := min(remaining, 100*time.Millisecond)
			polls := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
			n, err := unix.Poll(polls, int((wait+time.Millisecond-1)/time.Millisecond))
			if errors.Is(err, unix.EINTR) {
				continue
			}
			if err != nil || polls[0].Revents&unix.POLLNVAL != 0 {
				return "", errors.New("--claude-oauth-token-fd: cannot wait for credential input")
			}
			if n == 0 {
				continue
			}
			if polls[0].Revents&(unix.POLLIN|unix.POLLHUP) == 0 {
				return "", errors.New("--claude-oauth-token-fd: credential pipe failed")
			}
		}
		n, err := unix.Read(fd, data[used:])
		if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) || errors.Is(err, unix.EWOULDBLOCK) {
			continue
		}
		if err != nil {
			return "", errors.New("--claude-oauth-token-fd: cannot read credential input")
		}
		if n == 0 {
			break
		}
		used += n
		if used == len(data) {
			return "", errors.New("--claude-oauth-token-fd: token exceeds 16384 bytes")
		}
	}

	value := data[:used]
	if len(value) > 0 && value[len(value)-1] == '\n' {
		value = value[:len(value)-1]
		if len(value) > 0 && value[len(value)-1] == '\r' {
			value = value[:len(value)-1]
		}
	}
	if len(value) == 0 {
		return "", errors.New("--claude-oauth-token-fd: token is empty")
	}
	if len(value) > shallowClaudeOAuthTokenMaxSize {
		return "", errors.New("--claude-oauth-token-fd: token exceeds 16384 bytes")
	}
	for _, b := range value {
		if b < 0x21 || b > 0x7e {
			return "", errors.New("--claude-oauth-token-fd: expected one token with no whitespace or control characters")
		}
	}
	return string(value), nil
}

func shallowTokenReadContextError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		// Return only known sentinel errors, never caller-controlled text.
		if errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("--claude-oauth-token-fd: token read deadline exceeded: %w", context.DeadlineExceeded)
		}
		return fmt.Errorf("--claude-oauth-token-fd: token read canceled: %w", context.Canceled)
	}
	return nil
}
