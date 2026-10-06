//go:build unix

package claudesettings

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRefreshRejectsProfileRotationDuringPreparation(t *testing.T) {
	for name, prepare := range map[string]func(string, string, Policy) (*Update, error){
		"settings": PrepareRefresh,
		"legacy":   PrepareLegacyRefresh,
	} {
		for _, initial := range []string{"existing", "missing"} {
			t.Run(name+"/"+initial, func(t *testing.T) {
				dir := t.TempDir()
				profile := filepath.Join(dir, "profile.json")
				if initial == "existing" {
					writeSettings(t, profile, `{"apiKey":"old-account-key"}`)
				}
				shared := filepath.Join(dir, "shared.json")
				if err := syscall.Mkfifo(shared, 0600); err != nil {
					t.Fatal(err)
				}
				// The FIFO pauses preparation after the account read. Opening
				// its writer confirms that Read(shared) has started, so the
				// native rotation below always occurs inside preparation.
				type result struct {
					update *Update
					err    error
				}
				done := make(chan result, 1)
				go func() {
					update, err := prepare(shared, profile, Policy{})
					done <- result{update, err}
				}()
				t.Cleanup(func() {
					// Unblock a regressed reader even if the test exits early.
					if writer, err := os.OpenFile(shared, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
						_ = writer.Close()
					}
				})
				deadline := time.NewTimer(5 * time.Second)
				defer deadline.Stop()
				retry := time.NewTicker(time.Millisecond)
				defer retry.Stop()
				var writer *os.File
				for writer == nil {
					var err error
					writer, err = os.OpenFile(shared, os.O_WRONLY|syscall.O_NONBLOCK, 0)
					if err == nil {
						break
					}
					if !errors.Is(err, syscall.ENXIO) {
						t.Fatal(err)
					}
					select {
					case got := <-done:
						t.Fatalf("preparation finished before reading shared policy: %v", got.err)
					case <-deadline.C:
						t.Fatal("preparation did not reach the shared policy read")
					case <-retry.C:
					}
				}
				defer writer.Close()
				const rotated = `{"apiKey":"new-account-key"}`
				writeSettings(t, profile, rotated)
				if _, err := writer.Write([]byte(`{"model":"latest"}`)); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				var got result
				select {
				case got = <-done:
				case <-deadline.C:
					t.Fatal("preparation did not finish after releasing shared policy")
				}
				if got.err != nil {
					t.Fatal(got.err)
				}
				if err := got.update.Apply(); err == nil {
					t.Error("refresh accepted credentials changed during preparation")
				}
				if data, err := os.ReadFile(profile); err != nil || string(data) != rotated {
					t.Fatalf("refresh overwrote the native rotation: %s, %v", data, err)
				}
			})
		}
	}
}
