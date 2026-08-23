//go:build unix

package pty

import (
	"context"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"
)

// Fast-exit output on macOS is subject to a kernel-level race: a child that
// writes and exits near instantly can have its final PTY output discarded
// before the parent reads it (poll may even report POLLIN while read returns
// EOF). No reader-side arrangement fully prevents it — holding a parent-side
// slave open preserves the buffer in most schedules but wedges a ctty session
// leader in exit (unreapable) and still loses output under load. See the
// KNOWN DARWIN LIMITATION note in controller_unix.go.
//
// These tests therefore retry a bounded number of times: the kernel race is
// absorbed (p(all attempts lose) ≈ p³), while DETERMINISTIC regressions —
// deadline errors on macOS masters, EOF-before-drain bugs, wrong data — still
// fail immediately because they fail every attempt or produce non-empty wrong
// output.
const fastExitAttempts = 3

// runFastExitAttempt starts the child once and reports how the read went:
// matched (pattern seen), raceLoss (output empty with EOF/timeout — the
// documented kernel race), or a hard failure message for anything else.
func runFastExitAttempt(t *testing.T, pattern *regexp.Regexp) (matched, raceLoss bool, hardFail string) {
	t.Helper()
	ctrl := mustStart(t, "sh", []string{"-c", "echo pty-fast-exit-probe"}, nil)
	defer ctrl.Close()

	out, err := ctrl.WaitForPattern(context.Background(), pattern, 5*time.Second)
	switch {
	case pattern.MatchString(out):
		return true, false, ""
	case strings.TrimSpace(out) == "" && (err == io.EOF || err == ErrTimeout):
		return false, true, ""
	default:
		return false, false, "unexpected outcome: err=" + errString(err) + " output=" + out
	}
}

func errString(err error) string {
	if err == nil {
		return "<nil>"
	}
	return err.Error()
}

func TestWaitForPatternFastExitOutputDelivered(t *testing.T) {
	pattern := regexp.MustCompile(`pty-fast-exit-probe`)
	for i := 0; i < 12; i++ {
		delivered := false
		for attempt := 0; attempt < fastExitAttempts; attempt++ {
			matched, raceLoss, hardFail := runFastExitAttempt(t, pattern)
			if hardFail != "" {
				t.Fatalf("iteration %d attempt %d: %s", i, attempt, hardFail)
			}
			if matched {
				delivered = true
				break
			}
			if raceLoss {
				t.Logf("iteration %d attempt %d: fast-exit output lost to the darwin PTY race; retrying", i, attempt)
			}
		}
		if !delivered {
			t.Fatalf("iteration %d: fast-exit output lost on all %d attempts", i, fastExitAttempts)
		}
	}
}

func TestReadLineFastExitOutputDelivered(t *testing.T) {
	for i := 0; i < 8; i++ {
		delivered := false
		for attempt := 0; attempt < fastExitAttempts && !delivered; attempt++ {
			ctrl := mustStart(t, "sh", []string{"-c", "echo pty-readline-probe"}, nil)
			var collected strings.Builder
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				line, err := ctrl.ReadLine(ctx)
				cancel()
				collected.WriteString(line)
				if strings.Contains(collected.String(), "pty-readline-probe") {
					delivered = true
					break
				}
				if err == io.EOF {
					break
				}
			}
			_ = ctrl.Close()
			if !delivered {
				t.Logf("iteration %d attempt %d: ReadLine lost fast-exit output (got %q); retrying", i, attempt, collected.String())
			}
		}
		if !delivered {
			t.Fatalf("iteration %d: ReadLine lost fast-exit output on all %d attempts", i, fastExitAttempts)
		}
	}
}
