//go:build !windows

package keepalive

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRunRejectsFIFOStateAndLockBeforeNativeAttempt(t *testing.T) {
	for _, kind := range []string{"state", "lock"} {
		t.Run(kind, func(t *testing.T) {
			grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
			marker := filepath.Join(t.TempDir(), "native-attempted")
			t.Setenv("CAAM_KEEPALIVE_NATIVE_MARKER", marker)
			opts := engineOptions(t, "grok", engineCLI(t, `printf attempted > "$CAAM_KEEPALIVE_NATIVE_MARKER"`))
			if err := os.MkdirAll(opts.StateDir, 0700); err != nil {
				t.Fatal(err)
			}
			fifo := filepath.Join(opts.StateDir, grantKey(grant)+".json")
			wantReason := "state_invalid"
			if kind == "lock" {
				fifo = grant.AuthPath + ".caam-keepalive.lock"
				wantReason = "lock_unavailable"
			}
			if err := syscall.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}

			type outcome struct {
				results []Result
				err     error
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan outcome, 1)
			go func() {
				results, err := Run(ctx, []Grant{grant}, opts)
				done <- outcome{results: results, err: err}
			}()
			var got outcome
			select {
			case got = <-done:
			case <-time.After(time.Second):
				cancel()
				// A regressed state reader may block in os.Open before its
				// native timeout exists. Supply a nonblocking writer so that
				// open can finish and the failed test does not strand a reader.
				deadline := time.After(time.Second)
				tick := time.NewTicker(10 * time.Millisecond)
				defer tick.Stop()
				for {
					if writer, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
						writer.Close()
					}
					select {
					case <-done:
						t.Fatal("Run blocked on a FIFO until the test supplied a writer")
					case <-deadline:
						t.Fatal("Run remained blocked after bounded FIFO cleanup")
					case <-tick.C:
					}
				}
			}
			if got.err != nil || len(got.results) != 1 {
				t.Fatalf("Run returned %d results, error %v", len(got.results), got.err)
			}
			result := got.results[0]
			if result.Success || result.Attempted || result.Status != "failed" || result.Reason != wantReason {
				t.Fatalf("unexpected FIFO result: %+v", result)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("native CLI ran despite the FIFO: %v", err)
			}
		})
	}
}

func TestRunStopsNativeDescendantsBeforeUnlocking(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		name := "successful wrapper exit"
		if timeout {
			name = "timeout"
		}
		t.Run(name, func(t *testing.T) {
			grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
			before, err := os.ReadFile(grant.AuthPath)
			if err != nil {
				t.Fatal(err)
			}
			controlDir := t.TempDir()
			release := filepath.Join(controlDir, "release-child")
			started := filepath.Join(controlDir, "child-started")
			t.Setenv("CAAM_KEEPALIVE_RELEASE", release)
			t.Setenv("CAAM_KEEPALIVE_STARTED", started)
			body := `(
  printf started > "$CAAM_KEEPALIVE_STARTED"
  attempts=0
  while [ ! -f "$CAAM_KEEPALIVE_RELEASE" ] && [ "$attempts" -lt 500 ]; do
    attempts=$((attempts + 1))
    sleep 0.01
  done
  if [ -f "$CAAM_KEEPALIVE_RELEASE" ]; then
    printf '\n' >> "$GROK_HOME/auth.json"
  fi
) &
while [ ! -f "$CAAM_KEEPALIVE_STARTED" ]; do sleep 0.01; done`
			if timeout {
				body += "\nwait"
			}
			opts := engineOptions(t, "grok", engineCLI(t, body))
			if timeout {
				opts.Timeout = 250 * time.Millisecond
			}
			result := runEngine(t, grant, opts)
			if !result.Attempted || result.Success == timeout {
				t.Fatalf("unexpected native outcome: %+v", result)
			}
			if timeout && result.Reason != "native_cli_timeout" {
				t.Fatalf("timeout lost its diagnostic: %+v", result)
			}
			if _, err := os.Stat(started); err != nil {
				t.Fatalf("child must start before the regression is exercised: %v", err)
			}
			if err := os.WriteFile(release, nil, 0600); err != nil {
				t.Fatal(err)
			}
			// The native child polls every 10ms. It may modify the credential
			// only after Run returns, exposing a process that outlives its lock.
			time.Sleep(150 * time.Millisecond)
			after, err := os.ReadFile(grant.AuthPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("native descendant changed the credential after unlock: %v", err)
			}
		})
	}
}
