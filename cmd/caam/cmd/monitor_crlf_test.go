package cmd

import "testing"

// TestCRLFWriter verifies the raw-mode '\n' -> '\r\n' translation that keeps the
// live monitor table from stair-stepping diagonally across the screen while the
// terminal is in raw mode (OPOST/ONLCR disabled) — issue #37.
func TestCRLFWriter(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"a\nb\n", "a\r\nb\r\n"},
		{"a\r\nb\r\n", "a\r\nb\r\n"}, // already CRLF: unchanged
		{"no newline", "no newline"},
		{"\n", "\r\n"},
		{"x\r\ny\nz", "x\r\ny\r\nz"},
	}
	for _, tc := range cases {
		var buf []byte
		w := crlfWriter{w: writerFunc(func(p []byte) (int, error) {
			buf = append(buf, p...)
			return len(p), nil
		})}
		n, err := w.Write([]byte(tc.in))
		if err != nil {
			t.Fatalf("Write(%q) error: %v", tc.in, err)
		}
		// Must report bytes consumed from p (len(in)), not bytes written.
		if n != len(tc.in) {
			t.Errorf("Write(%q) returned n=%d, want %d", tc.in, n, len(tc.in))
		}
		if got := string(buf); got != tc.want {
			t.Errorf("crlfWriter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
