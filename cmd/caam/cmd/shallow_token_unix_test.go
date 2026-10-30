//go:build unix

package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Keep a separate alias so tests can inspect the shared open-file-description
// flags after the reader consumes and closes its dedicated raw descriptor.
func shallowTokenPipe(t *testing.T) (fd int, alias int, writer *os.File) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	t.Cleanup(func() { _ = w.Close() })
	alias = int(r.Fd())
	fd, err = unix.Dup(alias)
	if err != nil {
		t.Fatal(err)
	}
	return fd, alias, w
}

func assertShallowTokenFDClosed(t *testing.T, fd int) {
	t.Helper()
	if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); !errors.Is(err, unix.EBADF) {
		t.Fatalf("credential descriptor must be closed before returning; got %v", err)
	}
}

func shallowTokenFDFlags(t *testing.T, fd int) int {
	t.Helper()
	flags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return flags
}

func TestShallowClaudeOAuthTokenReader(t *testing.T) {
	const token = "synthetic-setup-token_123=.abc"
	for _, tc := range []struct {
		name  string
		input string
		want  string
	}{
		{"without newline", token, token},
		{"LF", token + "\n", token},
		{"CRLF", token + "\r\n", token},
		{"maximum", strings.Repeat("x", shallowClaudeOAuthTokenMaxSize), strings.Repeat("x", shallowClaudeOAuthTokenMaxSize)},
		{"maximum with CRLF", strings.Repeat("x", shallowClaudeOAuthTokenMaxSize) + "\r\n", strings.Repeat("x", shallowClaudeOAuthTokenMaxSize)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fd, alias, writer := shallowTokenPipe(t)
			originalFlags := shallowTokenFDFlags(t, alias)
			done := make(chan error, 1)
			go func() {
				_, err := writer.WriteString(tc.input)
				_ = writer.Close()
				done <- err
			}()
			got, err := readShallowClaudeOAuthToken(context.Background(), fd)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatal("reader changed the synthetic credential")
			}
			assertShallowTokenFDClosed(t, fd)
			if got := shallowTokenFDFlags(t, alias); got != originalFlags {
				t.Fatalf("reader changed caller's descriptor flags: got %d, want %d", got, originalFlags)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestShallowClaudeOAuthTokenReaderRejectsMalformedInput(t *testing.T) {
	const secret = "synthetic-secret-that-must-not-appear"
	for _, tc := range []struct {
		name  string
		input string
	}{
		{"empty", ""},
		{"empty LF", "\n"},
		{"empty CRLF", "\r\n"},
		{"leading space", " " + secret},
		{"trailing space", secret + " "},
		{"tab", secret + "\t"},
		{"bare CR", secret + "\r"},
		{"NUL", secret + "\x00"},
		{"DEL", secret + "\x7f"},
		{"non ASCII", secret + "é"},
		{"second line", secret + "\nanother-token"},
		{"extra newline", secret + "\n\n"},
		{"oversized", strings.Repeat("x", shallowClaudeOAuthTokenMaxSize+1)},
		{"oversized with CRLF", strings.Repeat("x", shallowClaudeOAuthTokenMaxSize+1) + "\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fd, alias, writer := shallowTokenPipe(t)
			originalFlags := shallowTokenFDFlags(t, alias)
			done := make(chan struct{})
			go func() {
				_, _ = writer.WriteString(tc.input)
				_ = writer.Close()
				close(done)
			}()
			got, err := readShallowClaudeOAuthToken(context.Background(), fd)
			if err == nil || got != "" {
				t.Fatal("malformed input must fail without returning a credential")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatal("reader error disclosed credential bytes")
			}
			assertShallowTokenFDClosed(t, fd)
			if got := shallowTokenFDFlags(t, alias); got != originalFlags {
				t.Fatal("failed read changed caller's descriptor flags")
			}
			<-done
		})
	}
}

func TestShallowClaudeOAuthTokenReaderRejectsInvalidDescriptors(t *testing.T) {
	for _, fd := range []int{-1, 0, 1, 2} {
		if token, err := readShallowClaudeOAuthToken(context.Background(), fd); err == nil || token != "" {
			t.Fatalf("descriptor %d must not be accepted", fd)
		}
	}
	// Rejection of stdio must leave all three descriptors open.
	for fd := 0; fd < 3; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil {
			t.Fatalf("reader closed standard descriptor %d", fd)
		}
	}
	fd, _, _ := shallowTokenPipe(t)
	if err := unix.Close(fd); err != nil {
		t.Fatal(err)
	}
	if token, err := readShallowClaudeOAuthToken(context.Background(), fd); err == nil || token != "" {
		t.Fatal("closed descriptor must not be accepted")
	}
}

func TestShallowClaudeOAuthTokenReaderRejectsDescriptorTypes(t *testing.T) {
	for _, name := range []string{"directory", "device", "write-only pipe"} {
		t.Run(name, func(t *testing.T) {
			var file *os.File
			var err error
			switch name {
			case "directory":
				file, err = os.Open(t.TempDir())
			case "device":
				file, err = os.Open(os.DevNull)
			case "write-only pipe":
				var read *os.File
				read, file, err = os.Pipe()
				if err == nil {
					t.Cleanup(func() { _ = read.Close() })
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			fd, err := unix.Dup(int(file.Fd()))
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(fd)
			flags := shallowTokenFDFlags(t, fd)
			fdFlags, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0)
			if err != nil {
				t.Fatal(err)
			}
			if token, err := readShallowClaudeOAuthToken(context.Background(), fd); err == nil || token != "" {
				t.Fatal("unsupported descriptor must not produce a token")
			}
			if got := shallowTokenFDFlags(t, fd); got != flags {
				t.Fatal("rejection changed an unadopted descriptor's status flags")
			}
			if got, err := unix.FcntlInt(uintptr(fd), unix.F_GETFD, 0); err != nil || got != fdFlags {
				t.Fatal("rejection changed or closed an unadopted descriptor")
			}
		})
	}
}

func TestShallowClaudeOAuthTokenReaderRegularFile(t *testing.T) {
	const secret = "synthetic-file-token"
	path := filepath.Join(t.TempDir(), "synthetic-token")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	got, err := readShallowClaudeOAuthToken(context.Background(), fd)
	if err != nil || got != secret {
		t.Fatalf("regular descriptor read failed: %v", err)
	}
	assertShallowTokenFDClosed(t, fd)
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != secret+"\n" {
		t.Fatal("reader modified its input file")
	}
}

func TestShallowClaudeOAuthTokenReaderTimeoutAndCancellation(t *testing.T) {
	for _, name := range []string{"no input", "token without EOF", "already canceled", "canceled while waiting", "already nonblocking"} {
		t.Run(name, func(t *testing.T) {
			fd, alias, writer := shallowTokenPipe(t)
			if name == "already nonblocking" {
				if err := unix.SetNonblock(alias, true); err != nil {
					t.Fatal(err)
				}
			}
			originalFlags := shallowTokenFDFlags(t, alias)
			if name == "token without EOF" {
				if _, err := writer.WriteString("synthetic-partial-secret"); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			wantErr := context.DeadlineExceeded
			if name == "already canceled" {
				cancel()
				wantErr = context.Canceled
			} else if name == "canceled while waiting" {
				timer := time.AfterFunc(10*time.Millisecond, cancel)
				defer timer.Stop()
				wantErr = context.Canceled
			}
			started := time.Now()
			got, err := readShallowClaudeOAuthTokenWithin(ctx, fd, 50*time.Millisecond)
			if !errors.Is(err, wantErr) || got != "" {
				t.Fatalf("stalled token source did not fail closed: %v", err)
			}
			if time.Since(started) > 2*time.Second {
				t.Fatal("token read was not bounded")
			}
			if strings.Contains(err.Error(), "synthetic-partial-secret") {
				t.Fatal("timeout disclosed the partial token")
			}
			assertShallowTokenFDClosed(t, fd)
			if got := shallowTokenFDFlags(t, alias); got != originalFlags {
				t.Fatal("timeout/cancellation changed caller's descriptor flags")
			}
		})
	}
}
