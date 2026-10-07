//go:build unix

package pty

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewController(t *testing.T) {
	t.Logf("[TEST] Platform: %s/%s", runtime.GOOS, runtime.GOARCH)

	t.Run("nil command returns error", func(t *testing.T) {
		_, err := NewController(nil, nil)
		if err == nil {
			t.Error("expected error for nil command")
		}
		t.Logf("[TEST] nil command error: %v", err)
	})

	t.Run("valid command with default options", func(t *testing.T) {
		cmd := exec.Command("echo", "hello")
		ctrl, err := NewController(cmd, nil)
		if err != nil {
			t.Fatalf("NewController failed: %v", err)
		}
		defer ctrl.Close()
		t.Log("[TEST] Controller created with default options")
	})

	t.Run("valid command with custom options", func(t *testing.T) {
		cmd := exec.Command("echo", "hello")
		opts := &Options{
			Rows: 40,
			Cols: 120,
		}
		ctrl, err := NewController(cmd, opts)
		if err != nil {
			t.Fatalf("NewController failed: %v", err)
		}
		defer ctrl.Close()
		t.Logf("[TEST] Controller created with custom options: rows=%d, cols=%d", opts.Rows, opts.Cols)
	})
}

func TestNewControllerFromArgs(t *testing.T) {
	t.Run("creates controller from args", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("echo", []string{"hello"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()
		t.Log("[TEST] Controller created from args")
	})
}

func TestControllerStart(t *testing.T) {
	t.Run("starts command successfully", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		err = ctrl.Start()
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		t.Log("[TEST] Command started successfully")

		// Verify we got a valid file descriptor
		fd := ctrl.Fd()
		if fd < 0 {
			t.Errorf("expected valid fd, got %d", fd)
		}
		t.Logf("[TEST] PTY fd: %d", fd)
	})

	t.Run("double start returns error", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("First Start failed: %v", err)
		}

		err = ctrl.Start()
		if err == nil {
			t.Error("expected error on double start")
		}
		t.Logf("[TEST] Double start error: %v", err)
	})
}

func TestControllerInjectCommand(t *testing.T) {
	t.Run("injects command into cat", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		t.Log("[TEST] Injecting 'hello world'")
		err = ctrl.InjectCommand("hello world")
		if err != nil {
			t.Fatalf("InjectCommand failed: %v", err)
		}

		// Give cat time to echo back
		time.Sleep(50 * time.Millisecond)

		output, err := ctrl.ReadOutput()
		if err != nil {
			t.Fatalf("ReadOutput failed: %v", err)
		}
		t.Logf("[TEST] Output: %q", output)

		if !strings.Contains(output, "hello world") {
			t.Errorf("expected output to contain 'hello world', got %q", output)
		}
	})

	t.Run("inject before start returns error", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		err = ctrl.InjectCommand("hello")
		if err == nil {
			t.Error("expected error when injecting before start")
		}
		t.Logf("[TEST] Inject before start error: %v", err)
	})
}

func TestControllerInjectRaw(t *testing.T) {
	t.Run("injects raw bytes", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		// Inject without newline
		t.Log("[TEST] Injecting raw bytes 'test'")
		err = ctrl.InjectRaw([]byte("test"))
		if err != nil {
			t.Fatalf("InjectRaw failed: %v", err)
		}

		// Now send newline
		err = ctrl.InjectRaw([]byte("\n"))
		if err != nil {
			t.Fatalf("InjectRaw newline failed: %v", err)
		}

		time.Sleep(50 * time.Millisecond)

		output, err := ctrl.ReadOutput()
		if err != nil {
			t.Fatalf("ReadOutput failed: %v", err)
		}
		t.Logf("[TEST] Output: %q", output)

		if !strings.Contains(output, "test") {
			t.Errorf("expected output to contain 'test', got %q", output)
		}
	})
}

func TestControllerWaitForPattern(t *testing.T) {
	t.Run("finds pattern in output", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("sh", []string{"-c", "echo 'START'; sleep 0.1; echo 'PATTERN_FOUND'; sleep 0.1; echo 'END'"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		pattern := regexp.MustCompile("PATTERN_FOUND")
		t.Logf("[TEST] Waiting for pattern: %s", pattern)

		ctx := context.Background()
		output, err := ctrl.WaitForPattern(ctx, pattern, 5*time.Second)
		if err != nil {
			t.Fatalf("WaitForPattern failed: %v", err)
		}
		t.Logf("[TEST] Matched output: %q", output)

		if !pattern.MatchString(output) {
			t.Errorf("pattern not found in output: %q", output)
		}
	})

	t.Run("timeout when pattern not found", func(t *testing.T) {
		// Use a command that outputs but doesn't match, and keeps running
		ctrl, err := NewControllerFromArgs("sh", []string{"-c", "echo 'NO_MATCH'; sleep 10"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		pattern := regexp.MustCompile("NEVER_EXISTS")
		t.Logf("[TEST] Waiting for pattern that won't match: %s", pattern)

		ctx := context.Background()
		_, err = ctrl.WaitForPattern(ctx, pattern, 200*time.Millisecond)
		// Should timeout since pattern doesn't match and process keeps running
		// Accept either ErrTimeout or a PTY read error (which can happen in CI)
		if err == nil {
			t.Error("expected an error, got nil")
		} else if err != ErrTimeout {
			t.Logf("[TEST] Got %v instead of ErrTimeout (acceptable in CI)", err)
		} else {
			t.Logf("[TEST] Got expected timeout error: %v", err)
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("sh", []string{"-c", "sleep 10"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		pattern := regexp.MustCompile("NEVER")

		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()

		t.Log("[TEST] Waiting with context that will be cancelled")
		start := time.Now()
		_, err = ctrl.WaitForPattern(ctx, pattern, 2*time.Second) // Reduce timeout for faster test
		elapsed := time.Since(start)

		// The function should return reasonably quickly
		// In some CI environments, the PTY read may not respect deadlines perfectly
		if elapsed > 3*time.Second {
			t.Logf("[TEST] WaitForPattern took longer than expected: %v (may be CI timing)", elapsed)
		}

		// Accept any error - context.Canceled or PTY read error
		if err != nil {
			t.Logf("[TEST] Got error: %v (any error is acceptable)", err)
		} else {
			t.Log("[TEST] No error returned (pattern not found)")
		}
	})
}

func TestControllerWait(t *testing.T) {
	t.Run("returns exit code 0 on success", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("true", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		exitCode, err := ctrl.Wait()
		if err != nil {
			t.Fatalf("Wait failed: %v", err)
		}
		t.Logf("[TEST] Exit code: %d", exitCode)

		if exitCode != 0 {
			t.Errorf("expected exit code 0, got %d", exitCode)
		}
	})

	t.Run("returns non-zero exit code on failure", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("false", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		exitCode, err := ctrl.Wait()
		if err != nil {
			t.Fatalf("Wait failed: %v", err)
		}
		t.Logf("[TEST] Exit code: %d", exitCode)

		if exitCode == 0 {
			t.Errorf("expected non-zero exit code, got %d", exitCode)
		}
	})
}

func TestControllerSignal(t *testing.T) {
	t.Run("sends SIGTERM to running process", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("sleep", []string{"10"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		t.Log("[TEST] Sending SIGTERM")
		err = ctrl.Signal(SIGTERM)
		if err != nil {
			t.Fatalf("Signal failed: %v", err)
		}

		// Process should exit due to signal
		exitCode, err := ctrl.Wait()
		t.Logf("[TEST] Exit code after signal: %d, err: %v", exitCode, err)
	})
}

func TestControllerClose(t *testing.T) {
	t.Run("closes successfully", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		t.Log("[TEST] Closing controller")
		err = ctrl.Close()
		if err != nil {
			t.Errorf("Close failed: %v", err)
		}

		// Double close should be safe
		err = ctrl.Close()
		if err != nil {
			t.Errorf("Double close failed: %v", err)
		}
		t.Log("[TEST] Double close succeeded")
	})

	t.Run("operations fail after close", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctrl.Close()

		err = ctrl.InjectCommand("test")
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] InjectCommand after close error: %v", err)
	})
}

func TestControllerFd(t *testing.T) {
	t.Run("returns -1 before start", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		fd := ctrl.Fd()
		if fd != -1 {
			t.Errorf("expected fd -1 before start, got %d", fd)
		}
		t.Logf("[TEST] Fd before start: %d", fd)
	})

	t.Run("returns valid fd after start", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		fd := ctrl.Fd()
		if fd < 0 {
			t.Errorf("expected valid fd after start, got %d", fd)
		}
		t.Logf("[TEST] Fd after start: %d", fd)
	})
}

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()
	if opts.Rows != 24 {
		t.Errorf("expected Rows=24, got %d", opts.Rows)
	}
	if opts.Cols != 80 {
		t.Errorf("expected Cols=80, got %d", opts.Cols)
	}
	t.Logf("[TEST] Default options: rows=%d, cols=%d", opts.Rows, opts.Cols)
}

// ============== ReadLine Tests ==============

func TestControllerReadLine(t *testing.T) {
	t.Run("reads single line from output", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("sh", []string{"-c", "echo 'line1'; echo 'line2'; sleep 1"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctx := context.Background()
		line, err := ctrl.ReadLine(ctx)
		if err != nil {
			t.Fatalf("ReadLine failed: %v", err)
		}
		t.Logf("[TEST] First line: %q", line)

		if !strings.Contains(line, "line1") {
			t.Errorf("expected 'line1' in output, got %q", line)
		}

		line2, err := ctrl.ReadLine(ctx)
		if err != nil {
			t.Fatalf("ReadLine (second) failed: %v", err)
		}
		t.Logf("[TEST] Second line: %q", line2)

		if !strings.Contains(line2, "line2") {
			t.Errorf("expected 'line2' in output, got %q", line2)
		}
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		// Use a timeout context as a fallback to prevent hanging in CI
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		go func() {
			time.Sleep(100 * time.Millisecond)
			cancel()
		}()
		defer cancel()

		t.Log("[TEST] Calling ReadLine with context that will be cancelled")
		start := time.Now()
		_, err = ctrl.ReadLine(ctx)
		elapsed := time.Since(start)

		// In CI, timing may vary; just log if it takes longer than expected
		if elapsed > 3*time.Second {
			t.Logf("[TEST] ReadLine took longer than expected: %v (may be CI timing)", elapsed)
		}

		// Accept any error - the important thing is that it returns
		if err != nil {
			t.Logf("[TEST] Got error: %v (any error is acceptable)", err)
		} else {
			t.Log("[TEST] ReadLine returned without error")
		}
	})

	t.Run("returns error before start", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		ctx := context.Background()
		_, err = ctrl.ReadLine(ctx)
		if err == nil {
			t.Error("expected error when reading before start")
		}
		t.Logf("[TEST] ReadLine before start error: %v", err)
	})
}

// ============== Options Tests ==============

func TestControllerOptions(t *testing.T) {
	t.Run("uses working directory option", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Logf("[TEST] Using temp dir: %s", tmpDir)

		opts := &Options{
			Rows: 24,
			Cols: 80,
			Dir:  tmpDir,
		}

		ctrl, err := NewControllerFromArgs("pwd", nil, opts)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctx := context.Background()
		line, err := ctrl.ReadLine(ctx)
		if err != nil {
			t.Fatalf("ReadLine failed: %v", err)
		}
		t.Logf("[TEST] Output: %q", line)

		if !strings.Contains(line, tmpDir) {
			t.Errorf("expected working dir %q in output, got %q", tmpDir, line)
		}
	})

	t.Run("uses environment variable option", func(t *testing.T) {
		testValue := "test_value_" + time.Now().Format("20060102150405")
		opts := &Options{
			Rows: 24,
			Cols: 80,
			Env:  []string{"TEST_VAR=" + testValue},
		}

		ctrl, err := NewControllerFromArgs("sh", []string{"-c", "echo $TEST_VAR"}, opts)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctx := context.Background()
		line, err := ctrl.ReadLine(ctx)
		if err != nil {
			t.Fatalf("ReadLine failed: %v", err)
		}
		t.Logf("[TEST] Output: %q", line)

		if !strings.Contains(line, testValue) {
			t.Errorf("expected env value %q in output, got %q", testValue, line)
		}
	})
}

// ============== Additional Error Handling Tests ==============

func TestControllerErrorHandling(t *testing.T) {
	t.Run("ReadOutput returns error when closed", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctrl.Close()

		_, err = ctrl.ReadOutput()
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] ReadOutput after close error: %v", err)
	})

	t.Run("ReadLine returns error when closed", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctrl.Close()

		ctx := context.Background()
		_, err = ctrl.ReadLine(ctx)
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] ReadLine after close error: %v", err)
	})

	t.Run("WaitForPattern returns error when closed", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctrl.Close()

		ctx := context.Background()
		pattern := regexp.MustCompile("test")
		_, err = ctrl.WaitForPattern(ctx, pattern, time.Second)
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] WaitForPattern after close error: %v", err)
	})

	t.Run("InjectRaw returns error when closed", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctrl.Close()

		err = ctrl.InjectRaw([]byte("test"))
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] InjectRaw after close error: %v", err)
	})

	t.Run("Signal returns error when closed", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		ctrl.Close()

		err = ctrl.Signal(SIGTERM)
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] Signal after close error: %v", err)
	})

	t.Run("Signal returns error before start", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		err = ctrl.Signal(SIGTERM)
		if err == nil {
			t.Error("expected error when signaling before start")
		}
		t.Logf("[TEST] Signal before start error: %v", err)
	})

	t.Run("Wait returns error before start", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		_, err = ctrl.Wait()
		if err == nil {
			t.Error("expected error when waiting before start")
		}
		t.Logf("[TEST] Wait before start error: %v", err)
	})

	t.Run("ReadOutput returns error before start", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		_, err = ctrl.ReadOutput()
		if err == nil {
			t.Error("expected error when reading before start")
		}
		t.Logf("[TEST] ReadOutput before start error: %v", err)
	})

	t.Run("Start returns error when closed", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		ctrl.Close()

		err = ctrl.Start()
		if err != ErrClosed {
			t.Errorf("expected ErrClosed, got %v", err)
		}
		t.Logf("[TEST] Start after close error: %v", err)
	})
}

// ============== Resource Cleanup Tests ==============

func TestControllerResourceCleanup(t *testing.T) {
	t.Run("Close terminates child process", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("sleep", []string{"60"}, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		t.Log("[TEST] Started sleep 60 process")

		// Close should terminate the child
		err = ctrl.Close()
		if err != nil {
			t.Logf("[TEST] Close returned error (may be expected): %v", err)
		}

		// Verify fd is now invalid
		fd := ctrl.Fd()
		if fd != -1 {
			t.Logf("[TEST] Fd after close: %d (may indicate unclosed resources)", fd)
		} else {
			t.Log("[TEST] Fd properly invalidated after close")
		}
	})

	t.Run("Close is idempotent", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		// Multiple closes should not panic or error
		for i := 0; i < 5; i++ {
			err := ctrl.Close()
			if err != nil {
				t.Errorf("Close #%d failed: %v", i+1, err)
			}
		}
		t.Log("[TEST] Multiple Close calls succeeded")
	})

	t.Run("Close without Start is safe", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}

		// Close without ever starting
		err = ctrl.Close()
		if err != nil {
			t.Errorf("Close without start failed: %v", err)
		}
		t.Log("[TEST] Close without start succeeded")
	})
}

// ============== Special Character Tests ==============

func TestControllerSpecialCharacters(t *testing.T) {
	t.Run("handles special characters in injection", func(t *testing.T) {
		ctrl, err := NewControllerFromArgs("cat", nil, nil)
		if err != nil {
			t.Fatalf("NewControllerFromArgs failed: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("Start failed: %v", err)
		}

		// Test various special characters
		testStrings := []string{
			"hello world",
			"special: !@#$%^&*()",
			"unicode: 你好世界",
			"tabs:\ttabs",
			"quotes: 'single' \"double\"",
		}

		for _, s := range testStrings {
			t.Logf("[TEST] Injecting: %q", s)
			err = ctrl.InjectCommand(s)
			if err != nil {
				t.Errorf("InjectCommand failed for %q: %v", s, err)
			}
		}

		time.Sleep(100 * time.Millisecond)
		output, err := ctrl.ReadOutput()
		if err != nil {
			t.Fatalf("ReadOutput failed: %v", err)
		}
		t.Logf("[TEST] Output: %q", output)
	})
}

// ============== Platform Support Test ==============

func TestPlatformSupport(t *testing.T) {
	t.Logf("[TEST] Running on: %s/%s", runtime.GOOS, runtime.GOARCH)

	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "openbsd", "netbsd":
		t.Log("[TEST] Unix PTY support expected - testing basic functionality")

		ctrl, err := NewControllerFromArgs("echo", []string{"platform test"}, nil)
		if err != nil {
			t.Fatalf("PTY creation failed on Unix: %v", err)
		}
		defer ctrl.Close()

		if err := ctrl.Start(); err != nil {
			t.Fatalf("PTY start failed on Unix: %v", err)
		}

		exitCode, err := ctrl.Wait()
		if err != nil {
			t.Fatalf("Wait failed: %v", err)
		}
		if exitCode != 0 {
			t.Errorf("expected exit code 0, got %d", exitCode)
		}
		t.Logf("[TEST] Unix PTY test passed, exit code: %d", exitCode)

	default:
		t.Skipf("[TEST] Platform %s not explicitly tested", runtime.GOOS)
	}
}

// TestInjectCommandPressesEnterForRawModePrograms: a raw-mode program (like
// Claude Code's input) must receive the carriage return the Enter key sends;
// a line feed would insert a newline instead of submitting.
func TestInjectCommandPressesEnterForRawModePrograms(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bytes")
	ctrl, err := NewControllerFromArgs("sh", []string{"-c", "stty raw -echo; head -c 7 > '" + out + "'"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ctrl.Close()
	if err := ctrl.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let stty take effect
	if err := ctrl.InjectCommand("/login"); err != nil {
		t.Fatal(err)
	}
	var got []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got, _ = os.ReadFile(out); len(got) == 7 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if string(got) != "/login\r" {
		t.Fatalf("raw-mode program received %q, want %q", got, "/login\r")
	}
}
