//go:build windows

package keepalive

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestKeepaliveWindowsNativeDescendantsStopBeforeReturn(t *testing.T) {
	for _, cancelWhileRunning := range []bool{false, true} {
		name := "successful wrapper exit"
		if cancelWhileRunning {
			name = "context cancellation"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			started, release, late := filepath.Join(dir, "started"), filepath.Join(dir, "release"), filepath.Join(dir, "late-write")
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			mode := "wrapper-exit"
			if cancelWhileRunning {
				mode = "wrapper-wait"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestKeepaliveWindowsNativeHelper$", "--", mode, started, release, late)
			command.Env = append(os.Environ(), "CAAM_KEEPALIVE_WINDOWS_HELPER=1")
			done := make(chan error, 1)
			go func() { done <- runNativeCommand(ctx, command) }()
			if !waitWindowsNativeMarker(started, 5*time.Second) {
				t.Fatal("native descendant did not start before containment was exercised")
			}
			if cancelWhileRunning {
				cancel()
			}
			select {
			case err := <-done:
				if (err != nil) != cancelWhileRunning {
					t.Fatalf("native result = %v, cancellation = %v", err, cancelWhileRunning)
				}
				if errors.Is(err, errNativeCleanup) {
					t.Fatalf("native job did not drain: %v", err)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("native process cleanup did not return")
			}
			// The descendant can write only after this marker appears. A leaked
			// wrapper child would therefore modify state after the grant unlocks.
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			if waitWindowsNativeMarker(late, 200*time.Millisecond) {
				t.Fatal("native descendant wrote after runNativeCommand returned")
			}
		})
	}
}

func TestKeepaliveWindowsNativeCancelledBeforeStart(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestKeepaliveWindowsNativeHelper$")
	if err := runNativeCommand(ctx, command); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled start returned %v", err)
	}
	if command.Process != nil {
		t.Fatal("cancelled invocation created a native process")
	}
}

// The helper is this package's isolated test executable, never a real provider
// CLI. A wrapper starts one normal CreateProcess child to exercise job inheritance.
func TestKeepaliveWindowsNativeHelper(t *testing.T) {
	if os.Getenv("CAAM_KEEPALIVE_WINDOWS_HELPER") != "1" {
		return
	}
	var args []string
	for i, arg := range os.Args {
		if arg == "--" {
			args = os.Args[i+1:]
			break
		}
	}
	if len(args) != 4 {
		os.Exit(2)
	}
	mode, started, release, late := args[0], args[1], args[2], args[3]
	if mode == "leaf" {
		if err := os.WriteFile(started, nil, 0600); err != nil {
			os.Exit(3)
		}
		if !waitWindowsNativeMarker(release, 15*time.Second) {
			os.Exit(4)
		}
		if err := os.WriteFile(late, []byte("unexpected native write"), 0600); err != nil {
			os.Exit(5)
		}
		os.Exit(0)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(6)
	}
	child := exec.Command(executable, "-test.run=^TestKeepaliveWindowsNativeHelper$", "--", "leaf", started, release, late)
	if err := child.Start(); err != nil {
		os.Exit(7)
	}
	if !waitWindowsNativeMarker(started, 5*time.Second) {
		_ = child.Process.Kill()
		_ = child.Wait()
		os.Exit(8)
	}
	if mode == "wrapper-wait" {
		_ = child.Wait()
	}
	os.Exit(0)
}

func waitWindowsNativeMarker(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
