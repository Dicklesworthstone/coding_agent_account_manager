package exec

// Tests for the terminal-proxy half of issue #74: stdin must reach the PTY
// byte-for-byte (no echo, no interpretation, no synthesized EOF) and the
// window-size plumbing must carry real terminal dimensions.

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/iotest"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/pty"
)

// fakeController records InjectRaw and Resize calls. The embedded nil
// interface makes any other Controller method panic if touched, which is the
// intent: the proxy helpers must only ever use these two.
type fakeController struct {
	pty.Controller

	mu        sync.Mutex
	written   bytes.Buffer
	resizes   [][2]uint16
	injectErr error // returned by InjectRaw once failAfter calls have succeeded
	failAfter int
	calls     int
}

func (f *fakeController) InjectRaw(b []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.injectErr != nil && f.calls >= f.failAfter {
		return f.injectErr
	}
	f.calls++
	f.written.Write(b)
	return nil
}

func (f *fakeController) Resize(rows, cols uint16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizes = append(f.resizes, [2]uint16{rows, cols})
	return nil
}

func (f *fakeController) injected() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.written.String()
}

func (f *fakeController) resizeCalls() [][2]uint16 {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][2]uint16, len(f.resizes))
	copy(out, f.resizes)
	return out
}

func TestRelayStdinForwardsBytesVerbatim(t *testing.T) {
	// The exact sequences from the #74 report: kitty-protocol arrow press and
	// release, focus out/in, Alt+Backspace, plus a carriage return and Ctrl-C.
	// Every byte must reach the PTY unchanged, regardless of how the reader
	// chunks the stream.
	input := "abc\x1b[1;1:1B\x1b[1;1:3B\x1b[O\x1b[I\x1b[127;3u\r\x03"
	fc := &fakeController{}

	relayStdin(iotest.OneByteReader(bytes.NewReader([]byte(input))), fc)

	if got := fc.injected(); got != input {
		t.Fatalf("relayed bytes differ:\n got %q\nwant %q", got, input)
	}
}

func TestRelayStdinDoesNotSynthesizeEOF(t *testing.T) {
	fc := &fakeController{}
	relayStdin(bytes.NewReader([]byte("hello\n")), fc)

	got := fc.injected()
	if got != "hello\n" {
		t.Fatalf("relayed %q, want %q", got, "hello\n")
	}
	if bytes.IndexByte([]byte(got), 0x04) >= 0 {
		t.Fatal("relay injected a VEOF/Ctrl-D byte after stdin closed")
	}
}

func TestRelayStdinStopsWhenPTYRejectsInput(t *testing.T) {
	pr, pw := io.Pipe()
	fc := &fakeController{injectErr: errors.New("pty closed"), failAfter: 1}

	done := make(chan struct{})
	go func() {
		relayStdin(pr, fc)
		close(done)
	}()

	if _, err := pw.Write([]byte("first")); err != nil {
		t.Fatalf("write first: %v", err)
	}
	// The second chunk is rejected by the PTY; the relay must return even
	// though the pipe stays open (a wrapper must not hang on a dead child).
	if _, err := pw.Write([]byte("second")); err != nil {
		t.Fatalf("write second: %v", err)
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("relayStdin did not stop after InjectRaw failed")
	}
	pw.Close()

	if got := fc.injected(); got != "first" {
		t.Fatalf("relayed %q, want only the accepted chunk %q", got, "first")
	}
}

func TestClampWinsize(t *testing.T) {
	cases := map[int]uint16{0: 0, 1: 1, 238: 238, 65535: 65535, 65536: 65535, 1 << 20: 65535}
	for in, want := range cases {
		if got := clampWinsize(in); got != want {
			t.Errorf("clampWinsize(%d) = %d, want %d", in, got, want)
		}
	}
}
