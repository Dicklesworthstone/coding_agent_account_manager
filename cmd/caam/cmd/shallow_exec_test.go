package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	caamexec "github.com/Dicklesworthstone/coding_agent_account_manager/internal/exec"
)

// TestShallowChildHelperProcess acts as a cross-platform child binary for
// TestRunShallowChild so tests do not rely on Unix-only shell utilities.
func TestShallowChildHelperProcess(t *testing.T) {
	mode := os.Getenv("CAAM_TEST_SHALLOW_CHILD_MODE")
	if mode == "" {
		return
	}
	switch mode {
	case "echo":
		in, _ := io.ReadAll(os.Stdin)
		fmt.Fprintf(os.Stdout, "SELECTED=%s\nSHALLOW_PROFILE=%s\nIN=%s\nARGS=%s\n",
			os.Getenv("CAAM_TEST_SELECTED_ENV"),
			os.Getenv("SHALLOW_PROFILE"),
			strings.TrimSpace(string(in)),
			strings.Join(os.Args[len(os.Args)-2:], ","),
		)
		fmt.Fprintln(os.Stderr, "child-stderr-line")
		os.Exit(0)
	case "exit":
		code, _ := strconv.Atoi(os.Getenv("CAAM_TEST_SHALLOW_CHILD_CODE"))
		fmt.Fprintln(os.Stderr, "exiting-nonzero")
		os.Exit(code)
	case "sleep":
		time.Sleep(30 * time.Second)
		os.Exit(0)
	default:
		os.Exit(99)
	}
}

func TestRunShallowChild(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test executable: %v", err)
	}

	t.Run("propagates env args and streams", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		stdin := strings.NewReader("hello-from-stdin\n")
		env := []string{
			"CAAM_TEST_SHALLOW_CHILD_MODE=echo",
			"CAAM_TEST_SELECTED_ENV=/shallow/alice",
			"SHALLOW_PROFILE=alice",
		}
		args := []string{
			self,
			"-test.run=^TestShallowChildHelperProcess$",
			"--",
			"arg-one",
			"arg-two",
		}
		if err := runShallowChild(context.Background(), self, args, env, stdin, &stdout, &stderr); err != nil {
			t.Fatalf("runShallowChild: %v (stderr=%q)", err, stderr.String())
		}
		gotOut := stdout.String()
		for _, want := range []string{
			"SELECTED=/shallow/alice",
			"SHALLOW_PROFILE=alice",
			"IN=hello-from-stdin",
			"ARGS=arg-one,arg-two",
		} {
			if !strings.Contains(gotOut, want) {
				t.Errorf("stdout missing %q; got:\n%s", want, gotOut)
			}
		}
		if !strings.Contains(stderr.String(), "child-stderr-line") {
			t.Errorf("stderr = %q, want child-stderr-line", stderr.String())
		}
	})

	t.Run("propagates exact non-zero exit code", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		env := []string{
			"CAAM_TEST_SHALLOW_CHILD_MODE=exit",
			"CAAM_TEST_SHALLOW_CHILD_CODE=42",
		}
		args := []string{self, "-test.run=^TestShallowChildHelperProcess$"}
		err := runShallowChild(context.Background(), self, args, env, nil, &stdout, &stderr)
		if err == nil {
			t.Fatal("expected non-zero exit error, got nil")
		}
		var exitErr *caamexec.ExitCodeError
		if !errors.As(err, &exitErr) || exitErr.Code != 42 {
			t.Fatalf("err = %T(%v), want *ExitCodeError{Code: 42}", err, err)
		}
		if got := ExitCode(err); got != 42 {
			t.Fatalf("ExitCode(err) = %d, want 42", got)
		}
		if !strings.Contains(stderr.String(), "exiting-nonzero") {
			t.Errorf("stderr = %q, want exiting-nonzero", stderr.String())
		}
	})

	t.Run("cancels child when context expires", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
		defer cancel()
		var stdout, stderr bytes.Buffer
		env := []string{"CAAM_TEST_SHALLOW_CHILD_MODE=sleep"}
		args := []string{self, "-test.run=^TestShallowChildHelperProcess$"}
		start := time.Now()
		err := runShallowChild(ctx, self, args, env, nil, &stdout, &stderr)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Fatalf("child did not terminate promptly on cancellation: %v", elapsed)
		}
	})

	t.Run("shallow-spawn silences cobra noise on child ExitCodeError", func(t *testing.T) {
		shallowEnv(t)
		if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "winchild", "--json"); err != nil {
			t.Fatal(err)
		}
		origExec := spawnExec
		spawnExec = func(string, []string, []string) error {
			return &caamexec.ExitCodeError{Code: 23}
		}
		t.Cleanup(func() { spawnExec = origExec })

		_, stderr, err := runCmdCaptured(t, "shallow-spawn", "winchild", "--", self)
		if ExitCode(err) != 23 {
			t.Fatalf("ExitCode = %d (err=%v), want 23", ExitCode(err), err)
		}
		if strings.Contains(stderr, "Usage:") {
			t.Fatalf("unexpected usage output on child ExitCodeError: %q", stderr)
		}
	})
}
