package authfile

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func retirementTestWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeRetirementHonorsAbsentSnapshot(t *testing.T) {
	for _, shape := range []string{"file", "absent", "keychain-only", "stale-mirror", "cached-mirror"} {
		t.Run(shape, func(t *testing.T) {
			dir := t.TempDir()
			live, snapshot := filepath.Join(dir, ".credentials.json"), filepath.Join(dir, "missing-snapshot")
			const disk = `{"accessToken":"outgoing-disk-secret"}`
			const mirrored = `{"accessToken":"outgoing-keychain-secret"}`
			if shape == "file" || shape == "stale-mirror" || shape == "cached-mirror" {
				retirementTestWrite(t, live, disk)
			}
			var readMirror func() ([]byte, error)
			if strings.Contains(shape, "mirror") || shape == "keychain-only" {
				readMirror = func() ([]byte, error) { return []byte(mirrored), nil }
			}
			plan, err := prepareClaudeCredentialRetirement(snapshot, live, readMirror)
			if err != nil || plan == nil {
				t.Fatalf("prepare: %v, %v", plan, err)
			}
			// Simulate Restore's intervening keychain pull, including the case
			// where EnsureMirror's cache legitimately leaves the old disk alone.
			if shape == "stale-mirror" || shape == "keychain-only" {
				retirementTestWrite(t, live, mirrored+"\n")
			}
			if err := plan.Apply(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(live); !os.IsNotExist(err) {
				t.Fatalf("outgoing credential survived retirement: %v", err)
			}
		})
	}
}

func TestClaudeRetirementKeepsPresentSnapshotsOnNormalRestorePath(t *testing.T) {
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
	retirementTestWrite(t, live, `{"accessToken":"live"}`)
	retirementTestWrite(t, snapshot, `{"accessToken":"selected"}`)
	plan, err := prepareClaudeCredentialRetirement(snapshot, live, func() ([]byte, error) {
		t.Fatal("present credential snapshots must retain their existing restore path")
		return nil, nil
	})
	if err != nil || plan != nil {
		t.Fatalf("present snapshot scheduled for retirement: %v, %v", plan, err)
	}
	if got, err := os.ReadFile(live); err != nil || string(got) != `{"accessToken":"live"}` {
		t.Fatalf("preflight mutated live auth: %s, %v", got, err)
	}
}

func TestClaudeRetirementRejectsInterveningAccountChanges(t *testing.T) {
	for _, change := range []string{"live", "snapshot", "keychain", "keychain-created", "keychain-removed", "keychain-error"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
			const before = `{"accessToken":"outgoing-secret"}`
			const after = `{"accessToken":"native-new-secret"}`
			retirementTestWrite(t, live, before)
			mirror := []byte(before)
			if change == "keychain-created" {
				mirror = nil
			}
			var readErr error
			plan, err := prepareClaudeCredentialRetirement(snapshot, live, func() ([]byte, error) { return mirror, readErr })
			if err != nil {
				t.Fatal(err)
			}
			want := before
			switch change {
			case "live":
				want = after
				retirementTestWrite(t, live, after)
			case "snapshot":
				retirementTestWrite(t, snapshot, after)
			case "keychain", "keychain-created":
				mirror = []byte(after)
			case "keychain-removed":
				mirror = nil
			case "keychain-error":
				readErr = errors.New("keychain unavailable")
			}
			err = plan.Apply()
			if err == nil {
				t.Fatal("retired auth after an intervening account change")
			}
			if strings.Contains(err.Error(), "-secret") {
				t.Fatal("retirement error exposed credential data")
			}
			if got, err := os.ReadFile(live); err != nil || string(got) != want {
				t.Fatalf("intervening edit was not protected: %s, %v", got, err)
			}
		})
	}
}

func TestClaudeRetirementRefusesInvalidDestinations(t *testing.T) {
	dir := t.TempDir()
	live, snapshot := filepath.Join(dir, "live"), filepath.Join(dir, "snapshot")
	if _, err := prepareClaudeCredentialRetirement("", live, nil); err == nil {
		t.Fatal("accepted empty snapshot path")
	}
	if _, err := prepareClaudeCredentialRetirement(live, live, nil); err == nil {
		t.Fatal("accepted aliased source and destination")
	}
	if _, err := prepareClaudeCredentialRetirement(snapshot, dir, nil); err == nil {
		t.Fatal("accepted directory retirement")
	}
	if runtime.GOOS != "windows" {
		if err := os.Symlink(snapshot, live); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareClaudeCredentialRetirement(snapshot, live, nil); err == nil {
			t.Fatal("accepted unknown credential symlink")
		}
	}
}
