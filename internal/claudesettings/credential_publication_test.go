package claudesettings

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func credentialWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialReplacementPreservesCapturedBytes(t *testing.T) {
	for _, preserve := range []bool{false, true} {
		t.Run(map[bool]string{false: "selected", true: "newer-live"}[preserve], func(t *testing.T) {
			dir := t.TempDir()
			live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
			before := []byte(`{"accessToken":"newer-live"}`)
			selected := []byte("{ \"accessToken\" : \"snapshot\", \"counter\":9007199254740993 }\n")
			credentialWrite(t, live, string(before))
			credentialWrite(t, snapshot, string(selected))
			after := selected
			if preserve {
				after = before
			}
			want := bytes.Clone(after)
			sources := map[string][]byte{snapshot: selected}
			plan, err := PrepareCredentialReplacement(snapshot, live, before, after, sources, nil)
			if err != nil {
				t.Fatal(err)
			}
			// A caller cannot change the selected generation after preparation.
			before[0], selected[0] = '[', '['
			delete(sources, snapshot)
			if err := ApplyUpdatesWithRemovals([]*Update{plan}, nil); err != nil {
				t.Fatal(err)
			}
			got, err := Read(live)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("captured credentials changed: %v", err)
			}
		})
	}
}

func TestCredentialReplacementRejectsUnprovenGenerationAndAliases(t *testing.T) {
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
	before, selected := []byte(`{"accessToken":"live"}`), []byte(`{"accessToken":"selected"}`)
	credentialWrite(t, live, string(before))
	credentialWrite(t, snapshot, string(selected))
	for _, after := range [][]byte{nil, []byte(`{"accessToken":"uncaptured"}`)} {
		if _, err := PrepareCredentialReplacement(snapshot, live, before, after, map[string][]byte{snapshot: selected}, nil); err == nil {
			t.Fatal("accepted an uncaptured replacement")
		}
	}
	if _, err := PrepareCredentialReplacement(live, live, before, before, map[string][]byte{live: before}, nil); err == nil {
		t.Fatal("accepted aliased snapshot and live destination")
	}
	plan, err := PrepareCredentialReplacement(snapshot, live, before, selected, map[string][]byte{snapshot: selected}, nil)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "native")
	credentialWrite(t, replacement, string(before))
	if err := os.Rename(replacement, live); err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates([]*Update{plan}); err == nil {
		t.Fatal("overwrote identical-byte native replacement")
	}
	if runtime.GOOS != "windows" {
		linked := filepath.Join(dir, "linked")
		if err := os.Link(snapshot, linked); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareCredentialReplacement(snapshot, linked, selected, selected, map[string][]byte{snapshot: selected}, nil); err == nil {
			t.Fatal("accepted live credential hard-linked to the vault")
		}
	}
}

func TestCredentialFinalizerFailureRestoresWritesAndRemovals(t *testing.T) {
	dir := t.TempDir()
	live, snapshot, retired := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot"), filepath.Join(dir, "retired")
	const before = `{"accessToken":"old"}`
	const after = `{"accessToken":"new"}`
	credentialWrite(t, live, before)
	credentialWrite(t, snapshot, after)
	credentialWrite(t, retired, before)
	update, err := PrepareCredentialReplacement(snapshot, live, []byte(before), []byte(after), map[string][]byte{snapshot: []byte(after)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	removal, err := PrepareCredentialRemoval(filepath.Join(dir, "absent"), retired, []byte(before), nil)
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("publication denied")
	calls := 0
	err = ApplyUpdatesWithFinalizer([]*Update{update}, []*Removal{removal}, func() error {
		calls++
		got, err := Read(live)
		if err != nil || string(got) != after {
			t.Fatal("publication happened before credential installation")
		}
		if _, err := os.Stat(retired); !os.IsNotExist(err) {
			t.Fatal("publication happened before retirement")
		}
		return failed
	})
	if !errors.Is(err, failed) || calls != 1 {
		t.Fatalf("lost finalizer failure or invoked more than once: %v", err)
	}
	for _, path := range []string{live, retired} {
		got, err := Read(path)
		if err != nil || string(got) != before {
			t.Fatal("publication failure left mixed account state")
		}
	}
}

func TestCredentialFinalizerRechecksBundleAndSnapshot(t *testing.T) {
	for _, changed := range []string{"snapshot", "installed", "removed"} {
		t.Run(changed, func(t *testing.T) {
			dir := t.TempDir()
			live, snapshot, retired := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot"), filepath.Join(dir, "retired")
			const before = `{"accessToken":"old"}`
			const after = `{"accessToken":"new"}`
			credentialWrite(t, live, before)
			credentialWrite(t, snapshot, after)
			credentialWrite(t, retired, before)
			update, err := PrepareCredentialReplacement(snapshot, live, []byte(before), []byte(after), map[string][]byte{snapshot: []byte(after)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			removal, err := PrepareCredentialRemoval(filepath.Join(dir, "absent"), retired, []byte(before), nil)
			if err != nil {
				t.Fatal(err)
			}
			ops := batchFileOps{os.Rename, func(path string) error {
				if err := os.Remove(path); err != nil {
					return err
				}
				switch changed {
				case "snapshot":
					credentialWrite(t, snapshot, `{"accessToken":"rotated"}`)
				case "installed":
					credentialWrite(t, live, `{"accessToken":"native"}`)
				case "removed":
					credentialWrite(t, retired, `{"accessToken":"native"}`)
				}
				return nil
			}, os.Link}
			err = applyChangesFinal([]*Update{update}, []*Removal{removal}, ops, func() error {
				t.Fatal("published a changed bundle or snapshot")
				return nil
			})
			if err == nil || (!strings.Contains(err.Error(), "before publication") && !strings.Contains(err.Error(), "source changed")) {
				t.Fatalf("did not detect late bundle/source change: %v", err)
			}
			if changed == "installed" {
				got, _ := Read(live)
				if string(got) != `{"accessToken":"native"}` {
					t.Fatal("rollback overwrote native login")
				}
			}
		})
	}
}
