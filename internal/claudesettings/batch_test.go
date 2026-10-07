package claudesettings

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func batchTestWrite(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func batchTestHelper(t *testing.T, path string) *Update {
	t.Helper()
	update, err := PrepareAPIKeyHelper(path, "incoming-synthetic-secret")
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func batchTestNoArtifacts(t *testing.T, dir string) {
	t.Helper()
	for _, pattern := range []string{"settings.json.stage.*", "settings.json.rollback.*"} {
		paths, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil || len(paths) != 0 {
			t.Fatalf("unexpected staged/recovery artifacts: %v, %v", paths, err)
		}
	}
}

func TestBatchLateFailureRestoresOriginalDocuments(t *testing.T) {
	for _, shape := range []string{"existing", "missing", "symlink", "dangling"} {
		t.Run(shape, func(t *testing.T) {
			if runtime.GOOS == "windows" && (shape == "symlink" || shape == "dangling") {
				t.Skip("symlink creation may require elevated Windows privileges")
			}
			dir := t.TempDir()
			firstPath, lastPath := filepath.Join(dir, "first.json"), filepath.Join(dir, "last.json")
			const before = `{"apiKeyHelper":"outgoing-synthetic-secret","permissions":{"allow":[]}}`
			batchTestWrite(t, lastPath, before)
			switch shape {
			case "existing":
				batchTestWrite(t, firstPath, before)
			case "symlink", "dangling":
				if shape == "symlink" {
					batchTestWrite(t, filepath.Join(dir, "source.json"), before)
				}
				if err := os.Symlink("source.json", firstPath); err != nil {
					t.Fatal(err)
				}
			}
			first, last := batchTestHelper(t, firstPath), batchTestHelper(t, lastPath)
			failure := errors.New("injected final rename failure")
			calls := 0
			err := applyUpdatesWithRename([]*Update{first, last}, func(from, to string) error {
				calls++
				if calls == 2 {
					return failure
				}
				return os.Rename(from, to)
			})
			if !errors.Is(err, failure) {
				t.Fatalf("lost install error: %v", err)
			}
			if strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatal("error exposed authentication values")
			}
			if got, err := Read(lastPath); err != nil || string(got) != before {
				t.Fatalf("uninstalled document changed: %s, %v", got, err)
			}
			if shape == "existing" || shape == "symlink" {
				if got, err := Read(firstPath); err != nil || string(got) != before {
					t.Fatalf("first document was not restored: %s, %v", got, err)
				}
			}
			if shape == "missing" {
				if _, err := os.Lstat(firstPath); !os.IsNotExist(err) {
					t.Fatalf("failed batch left a newly created credential: %v", err)
				}
			}
			if shape == "symlink" || shape == "dangling" {
				if target, err := os.Readlink(firstPath); err != nil || target != "source.json" {
					t.Fatalf("original relative link not restored: %q, %v", target, err)
				}
			}
			batchTestNoArtifacts(t, dir)
		})
	}
}

func TestBatchRollbackRetainsConcurrentNativeChangesAndRecovery(t *testing.T) {
	for _, change := range []string{"edit", "replace-identical", "remove", "rollback-io-failure"} {
		t.Run(change, func(t *testing.T) {
			dir := t.TempDir()
			firstPath, lastPath := filepath.Join(dir, "first.json"), filepath.Join(dir, "last.json")
			const before = `{"apiKeyHelper":"outgoing-synthetic-secret","permissions":{"allow":[]}}`
			batchTestWrite(t, firstPath, before)
			batchTestWrite(t, lastPath, before)
			first, last := batchTestHelper(t, firstPath), batchTestHelper(t, lastPath)
			failure := errors.New("injected install failure")
			want := string(first.after)
			calls := 0
			err := applyUpdatesWithRename([]*Update{first, last}, func(from, to string) error {
				calls++
				if calls == 2 {
					switch change {
					case "edit":
						want = `{"apiKeyHelper":"native-new-secret","permissions":{"allow":[]}}`
						batchTestWrite(t, firstPath, want)
					case "replace-identical":
						replacement := filepath.Join(dir, "native-replacement.json")
						batchTestWrite(t, replacement, want)
						if err := os.Rename(replacement, firstPath); err != nil {
							t.Fatal(err)
						}
					case "remove":
						if err := os.Remove(firstPath); err != nil {
							t.Fatal(err)
						}
					}
					return failure
				}
				if calls > 2 && change == "rollback-io-failure" {
					return errors.New("injected rollback failure")
				}
				return os.Rename(from, to)
			})
			if !errors.Is(err, failure) {
				t.Fatalf("lost original failure: %v", err)
			}
			backups, globErr := filepath.Glob(filepath.Join(dir, "settings.json.rollback.*"))
			if globErr != nil || len(backups) != 1 || !strings.Contains(err.Error(), backups[0]) {
				t.Fatalf("missing explicit recovery reference: %v, %v, %v", backups, globErr, err)
			}
			if got, err := Read(backups[0]); err != nil || string(got) != before {
				t.Fatalf("original authentication was not preserved: %s, %v", got, err)
			}
			if info, err := os.Stat(backups[0]); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
				t.Fatalf("recovery file permissions: %v, %v", info, err)
			}
			if change == "remove" {
				if _, err := os.Lstat(firstPath); !os.IsNotExist(err) {
					t.Fatalf("native removal was undone: %v", err)
				}
			} else if got, err := Read(firstPath); err != nil || string(got) != want {
				t.Fatalf("concurrent native credential was overwritten: %s, %v", got, err)
			}
			if got, err := Read(lastPath); err != nil || string(got) != before {
				t.Fatalf("failed destination was changed: %s, %v", got, err)
			}
		})
	}
}

func TestBatchRejectsMidCommitPolicyRevocationAndRestoresEarlierWrite(t *testing.T) {
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.json")
	batchTestWrite(t, shared, `{"permissions":{"allow":["Read"]}}`)
	var updates []*Update
	for _, name := range []string{"first", "second"} {
		path := filepath.Join(dir, name+".json")
		batchTestWrite(t, path, `{"apiKeyHelper":"private","permissions":{"allow":[]}}`)
		update, err := PrepareRefresh(shared, path, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		updates = append(updates, update)
	}
	calls := 0
	err := applyUpdatesWithRename(updates, func(from, to string) error {
		if err := os.Rename(from, to); err != nil {
			return err
		}
		calls++
		if calls == 1 {
			batchTestWrite(t, shared, `{"permissions":{"allow":[]}}`)
		}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("accepted source revocation during commit: %v", err)
	}
	for _, update := range updates {
		if got, err := Read(update.path); err != nil || string(got) != string(update.before) {
			t.Fatalf("partial policy survived failed commit: %s, %v", got, err)
		}
	}
	if got, err := Read(shared); err != nil || string(got) != `{"permissions":{"allow":[]}}` {
		t.Fatalf("canonical revocation was undone: %s, %v", got, err)
	}
	batchTestNoArtifacts(t, dir)
}

func TestBatchSuccessAndNoops(t *testing.T) {
	dir := t.TempDir()
	first, second, unchanged, absent := filepath.Join(dir, "first"), filepath.Join(dir, "second"), filepath.Join(dir, "unchanged"), filepath.Join(dir, "absent")
	batchTestWrite(t, first, `{"apiKeyHelper":"old"}`)
	batchTestWrite(t, unchanged, `{"permissions":{}}`)
	info, err := os.Stat(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	one, two := batchTestHelper(t, first), batchTestHelper(t, second)
	three, err := PreparePrivate(unchanged)
	if err != nil {
		t.Fatal(err)
	}
	four, err := PreparePrivate(absent)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates([]*Update{one, two, three, four}); err != nil {
		t.Fatal(err)
	}
	for _, update := range []*Update{one, two} {
		if got, err := Read(update.path); err != nil || string(got) != string(update.after) {
			t.Fatalf("failed to install prepared document: %s, %v", got, err)
		}
	}
	if after, err := os.Stat(unchanged); err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
		t.Fatalf("no-op rewrote a private file: %v", err)
	}
	if _, err := os.Lstat(absent); !os.IsNotExist(err) {
		t.Fatalf("absent validation created a document: %v", err)
	}
	batchTestNoArtifacts(t, dir)
}

func TestBatchRejectsInvalidPlansBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	batchTestWrite(t, path, `{"apiKeyHelper":"original"}`)
	update := batchTestHelper(t, path)
	for _, updates := range [][]*Update{{update, nil}, {update, update}, {update, {}}} {
		if err := ApplyUpdates(updates); err == nil {
			t.Fatal("accepted invalid batch")
		}
		if got, err := Read(path); err != nil || string(got) != string(update.before) {
			t.Fatalf("invalid batch changed live auth: %s, %v", got, err)
		}
	}
	batchTestNoArtifacts(t, dir)
}

func TestBatchStagingFailureDoesNotInstallPartialAuthentication(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses procfs for a deterministic unwritable destination")
	}
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("procfs unavailable")
	}
	dir := t.TempDir()
	live := filepath.Join(dir, "settings.json")
	const before = `{"apiKeyHelper":"outgoing","permissions":{"allow":["Read"]}}`
	batchTestWrite(t, live, before)
	first, err := PrepareAPIKeyHelper(live, "incoming")
	if err != nil {
		t.Fatal(err)
	}
	account := filepath.Join(dir, "account.json")
	batchTestWrite(t, account, `{"apiKey":"incoming"}`)
	second, err := PrepareRestore(account, filepath.Join("/proc/self", fmt.Sprintf("caam-settings-test-%d", os.Getpid())), Policy{})
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdates([]*Update{first, second}); err == nil {
		t.Fatal("expected procfs staging failure")
	}
	got, err := os.ReadFile(live)
	if err != nil || string(got) != before {
		t.Fatalf("failed batch installed part of the incoming account: %s (%v)", got, err)
	}
}
