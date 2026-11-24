package sync

import (
	"errors"
	"net"
	"testing"
	"time"
)

func TestSSHError(t *testing.T) {
	machine := NewMachine("test-machine", "192.168.1.100")

	t.Run("Error message", func(t *testing.T) {
		err := &SSHError{
			Machine:    machine,
			Operation:  "connect",
			Underlying: errors.New("connection refused"),
		}

		msg := err.Error()
		if msg != "SSH error on test-machine during connect: connection refused" {
			t.Errorf("Error() = %q, want correct format", msg)
		}
	})

	t.Run("Unwrap", func(t *testing.T) {
		underlying := errors.New("underlying error")
		err := &SSHError{
			Machine:    machine,
			Operation:  "connect",
			Underlying: underlying,
		}

		if err.Unwrap() != underlying {
			t.Error("Unwrap() should return underlying error")
		}
	})

	t.Run("IsTimeout with net.Error", func(t *testing.T) {
		err := &SSHError{
			Machine:    machine,
			Operation:  "connect",
			Underlying: &timeoutError{},
		}

		if !err.IsTimeout() {
			t.Error("IsTimeout() should return true for timeout errors")
		}
	})

	t.Run("IsTimeout with string", func(t *testing.T) {
		err := &SSHError{
			Machine:    machine,
			Operation:  "connect",
			Underlying: errors.New("i/o timeout"),
		}

		if !err.IsTimeout() {
			t.Error("IsTimeout() should return true for timeout string")
		}
	})

	t.Run("IsAuthFailure", func(t *testing.T) {
		tests := []struct {
			name      string
			operation string
			err       error
			want      bool
		}{
			{
				name:      "auth operation",
				operation: "auth",
				err:       errors.New("any error"),
				want:      true,
			},
			{
				name:      "unable to authenticate",
				operation: "connect",
				err:       errors.New("unable to authenticate"),
				want:      true,
			},
			{
				name:      "permission denied",
				operation: "connect",
				err:       errors.New("ssh: permission denied"),
				want:      true,
			},
			{
				name:      "no supported methods",
				operation: "connect",
				err:       errors.New("no supported methods remain"),
				want:      true,
			},
			{
				name:      "other error",
				operation: "connect",
				err:       errors.New("connection refused"),
				want:      false,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				err := &SSHError{
					Machine:    machine,
					Operation:  tt.operation,
					Underlying: tt.err,
				}

				if got := err.IsAuthFailure(); got != tt.want {
					t.Errorf("IsAuthFailure() = %v, want %v", got, tt.want)
				}
			})
		}
	})

	t.Run("IsNetworkError", func(t *testing.T) {
		err := &SSHError{
			Machine:   machine,
			Operation: "connect",
			Underlying: &net.OpError{
				Op:  "dial",
				Err: errors.New("connection refused"),
			},
		}

		if !err.IsNetworkError() {
			t.Error("IsNetworkError() should return true for net.Error")
		}
	})

	t.Run("IsHostKeyMismatch", func(t *testing.T) {
		err := &SSHError{
			Machine:    machine,
			Operation:  "hostkey",
			Underlying: errors.New("host key changed for 192.168.1.100"),
		}

		if !err.IsHostKeyMismatch() {
			t.Error("IsHostKeyMismatch() should return true")
		}
	})
}

// timeoutError implements net.Error for testing
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }

func TestRandomString(t *testing.T) {
	s1 := randomString(8)
	s2 := randomString(8)

	if len(s1) != 8 {
		t.Errorf("randomString(8) len = %d, want 8", len(s1))
	}

	if s1 == s2 {
		t.Error("randomString should generate different strings")
	}
}

func TestDefaultKeyPaths(t *testing.T) {
	paths := defaultKeyPaths()

	if len(paths) == 0 {
		t.Skip("No home directory - skipping")
	}

	// Should include standard key types
	hasED25519 := false
	hasRSA := false
	for _, p := range paths {
		if contains(p, "id_ed25519") {
			hasED25519 = true
		}
		if contains(p, "id_rsa") {
			hasRSA = true
		}
	}

	if !hasED25519 {
		t.Error("Should include id_ed25519")
	}
	if !hasRSA {
		t.Error("Should include id_rsa")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsHelper(s, substr))
}

func containsHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestConnectionPool(t *testing.T) {
	t.Run("NewConnectionPool", func(t *testing.T) {
		opts := DefaultConnectOptions()
		pool := NewConnectionPool(opts)

		if pool == nil {
			t.Fatal("NewConnectionPool() returned nil")
		}

		if pool.Size() != 0 {
			t.Errorf("Size() = %d, want 0", pool.Size())
		}
	})

	t.Run("Size and IsConnected", func(t *testing.T) {
		pool := NewConnectionPool(DefaultConnectOptions())

		if pool.Size() != 0 {
			t.Errorf("Initial Size() = %d, want 0", pool.Size())
		}

		if pool.IsConnected("nonexistent") {
			t.Error("IsConnected() should return false for nonexistent")
		}
	})

	t.Run("ConnectedMachines", func(t *testing.T) {
		pool := NewConnectionPool(DefaultConnectOptions())

		machines := pool.ConnectedMachines()
		if len(machines) != 0 {
			t.Errorf("ConnectedMachines() len = %d, want 0", len(machines))
		}
	})

	t.Run("Release nonexistent", func(t *testing.T) {
		pool := NewConnectionPool(DefaultConnectOptions())

		// Should not panic
		pool.Release("nonexistent")
	})

	t.Run("CloseAll empty", func(t *testing.T) {
		pool := NewConnectionPool(DefaultConnectOptions())

		// Should not panic
		pool.CloseAll()
	})
}

func TestConnectOptions(t *testing.T) {
	t.Run("DefaultConnectOptions", func(t *testing.T) {
		opts := DefaultConnectOptions()

		if opts.Timeout != 10*time.Second {
			t.Errorf("Timeout = %v, want 10s", opts.Timeout)
		}

		if !opts.UseAgent {
			t.Error("UseAgent should default to true")
		}

		if opts.SkipHostKeyCheck {
			t.Error("SkipHostKeyCheck should default to false")
		}
	})
}

func TestNewSSHClient(t *testing.T) {
	machine := NewMachine("test", "192.168.1.100")
	client := NewSSHClient(machine)

	if client == nil {
		t.Fatal("NewSSHClient() returned nil")
	}

	if client.machine != machine {
		t.Error("machine should be set")
	}

	if client.IsConnected() {
		t.Error("Should not be connected initially")
	}
}

func TestConnectivityResult(t *testing.T) {
	machine := NewMachine("test", "192.168.1.100")

	result := &ConnectivityResult{
		Machine:      machine,
		Success:      true,
		Latency:      23 * time.Millisecond,
		SSHVersion:   "SSH-2.0-OpenSSH_8.2",
		SFTPWorks:    true,
		CAAMFound:    true,
		ProfileCount: 5,
	}

	if !result.Success {
		t.Error("Success should be true")
	}

	if result.Latency != 23*time.Millisecond {
		t.Errorf("Latency = %v, want 23ms", result.Latency)
	}

	if result.ProfileCount != 5 {
		t.Errorf("ProfileCount = %d, want 5", result.ProfileCount)
	}
}

func TestPosixJoin(t *testing.T) {
	tests := []struct {
		name  string
		elems []string
		want  string
	}{
		{"empty", []string{}, ""},
		{"single", []string{"foo"}, "foo"},
		{"two", []string{"foo", "bar"}, "foo/bar"},
		{"three", []string{"a", "b", "c"}, "a/b/c"},
		{"with empty", []string{"a", "", "c"}, "a/c"},
		{"leading slash", []string{"/home", "user"}, "/home/user"},
		{"double slash cleanup", []string{"foo/", "/bar"}, "foo/bar"},
		{"all empty", []string{"", "", ""}, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := posixJoin(tt.elems...)
			if got != tt.want {
				t.Errorf("posixJoin(%v) = %q, want %q", tt.elems, got, tt.want)
			}
		})
	}
}

func TestPosixDir(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/home/user/file.txt", "/home/user"},
		{"/home/user", "/home"},
		{"/file.txt", "/"},
		{"file.txt", "."},
		{"foo/bar/baz", "foo/bar"},
		{"/", "/"},
		{"a", "."},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := posixDir(tt.path)
			if got != tt.want {
				t.Errorf("posixDir(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
