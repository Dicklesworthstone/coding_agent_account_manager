package claudesettings

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func removalWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func removalWant(t *testing.T, path, body string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil || string(data) != body {
		t.Fatalf("unexpected contents at %s: %q, %v", path, data, err)
	}
}

func removalFixture(t *testing.T) ([]*Update, []*Removal, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	before := make(map[string]string)
	var updates []*Update
	for _, name := range []string{"settings.json", ".claude.json"} {
		path := filepath.Join(dir, "live", name)
		before[path] = `{"model":"live-policy","apiKeyHelper":"outgoing"}`
		removalWrite(t, path, before[path])
		snapshot := filepath.Join(dir, "target", name)
		removalWrite(t, snapshot, `{"model":"stale","apiKeyHelper":"target"}`)
		update, err := PrepareRestore(snapshot, path, Policy{})
		if err != nil {
			t.Fatal(err)
		}
		updates = append(updates, update)
	}
	var removals []*Removal
	for _, name := range []string{".credentials.json", "auth.json"} {
		path := filepath.Join(dir, "live", name)
		before[path] = `{"access_token":"outgoing-` + name + `"}`
		removalWrite(t, path, before[path])
		removal, err := PrepareCredentialRemoval(filepath.Join(dir, "target", name), path, []byte(before[path]), nil)
		if err != nil {
			t.Fatal(err)
		}
		removals = append(removals, removal)
	}
	return updates, removals, before
}

func TestCredentialRemovalBatchCommitsSettingsAndAbsence(t *testing.T) {
	updates, removals, _ := removalFixture(t)
	if err := ApplyUpdatesWithRemovals(updates, removals); err != nil {
		t.Fatal(err)
	}
	for _, update := range updates {
		removalWant(t, update.path, string(update.after))
		if !strings.Contains(string(update.after), "live-policy") || !strings.Contains(string(update.after), "target") {
			t.Fatal("shared policy and target auth were not both retained")
		}
	}
	for _, removal := range removals {
		if _, err := os.Lstat(removal.path); !os.IsNotExist(err) {
			t.Fatalf("retired source remains: %s: %v", removal.path, err)
		}
	}
	for _, pattern := range []string{"*.stage.*", "*.rollback.*"} {
		paths, err := filepath.Glob(filepath.Join(filepath.Dir(removals[0].path), pattern))
		if err != nil || len(paths) != 0 {
			t.Fatalf("successful batch left recovery files: %v, %v", paths, err)
		}
	}
}

func TestCredentialRemovalBatchRestoresBothKindsOfChange(t *testing.T) {
	for _, failure := range []string{"settings", "first retirement", "second retirement", "external authority"} {
		t.Run(failure, func(t *testing.T) {
			updates, removals, before := removalFixture(t)
			injected := errors.New("injected I/O failure")
			ops := batchFileOps{os.Rename, os.Remove, os.Link}
			ops.rename = func(src, dst string) error {
				if failure == "settings" && dst == updates[1].path && strings.Contains(src, ".stage.") {
					return injected
				}
				return os.Rename(src, dst)
			}
			ops.remove = func(path string) error {
				if (failure == "first retirement" && path == removals[0].path) ||
					(failure == "second retirement" && path == removals[1].path) {
					return injected
				}
				return os.Remove(path)
			}
			if failure == "external authority" {
				removals[1].verify = func() error {
					if _, err := os.Stat(removals[0].path); os.IsNotExist(err) {
						return injected
					}
					return nil
				}
			}
			if err := applyChanges(updates, removals, ops); !errors.Is(err, injected) {
				t.Fatalf("expected failure was not surfaced: %v", err)
			}
			for path, want := range before {
				removalWant(t, path, want)
			}
		})
	}
}

func TestCredentialRemovalRollbackDoesNotReplaceNativeLogin(t *testing.T) {
	for _, sameBytes := range []bool{false, true} {
		t.Run(map[bool]string{false: "new key", true: "identical bytes"}[sameBytes], func(t *testing.T) {
			updates, removals, before := removalFixture(t)
			path := removals[0].path
			body := `{"access_token":"new-native-login"}`
			if sameBytes {
				body = before[path]
			}
			var nativeInfo os.FileInfo
			ops := batchFileOps{os.Rename, os.Remove, os.Link}
			ops.remove = func(candidate string) error {
				if candidate == removals[1].path {
					removalWrite(t, path, body)
					nativeInfo, _ = os.Stat(path)
					return errors.New("fail after native login recreated the retired path")
				}
				return os.Remove(candidate)
			}
			err := applyChanges(updates, removals, ops)
			if err == nil || !strings.Contains(err.Error(), "original preserved at") {
				t.Fatalf("missing recovery diagnostic: %v", err)
			}
			removalWant(t, path, body)
			info, statErr := os.Stat(path)
			if statErr != nil || !os.SameFile(info, nativeInfo) {
				t.Fatal("rollback replaced the native login's inode")
			}
			for _, update := range updates {
				removalWant(t, update.path, before[update.path])
			}
			backups, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "credentials.json.rollback.*"))
			if len(backups) != 1 || !strings.Contains(err.Error(), backups[0]) {
				t.Fatalf("expected exactly one reported private recovery copy: %v, %v", backups, err)
			}
			removalWant(t, backups[0], before[path])
			info, statErr = os.Stat(backups[0])
			if statErr != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
				t.Fatalf("recovery copy is not private: %v, %v", info, statErr)
			}
		})
	}
}

func TestCredentialRemovalRejectsChangedInputsBeforeAnyWrites(t *testing.T) {
	for _, change := range []string{"snapshot", "dangling snapshot", "edit", "replace", "remove", "alias"} {
		t.Run(change, func(t *testing.T) {
			if runtime.GOOS == "windows" && (change == "dangling snapshot" || change == "alias") {
				t.Skip("symlink creation may require elevated Windows privileges")
			}
			updates, removals, before := removalFixture(t)
			r := removals[0]
			switch change {
			case "snapshot":
				removalWrite(t, r.snapshot, `{"new":"credential"}`)
			case "dangling snapshot":
				if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), r.snapshot); err != nil {
					t.Fatal(err)
				}
			case "edit":
				removalWrite(t, r.path, `{"rotated":"credential"}`)
			case "replace":
				replacement := filepath.Join(t.TempDir(), "replacement")
				removalWrite(t, replacement, before[r.path])
				if err := os.Rename(replacement, r.path); err != nil {
					t.Fatal(err)
				}
			case "remove":
				if err := os.Remove(r.path); err != nil {
					t.Fatal(err)
				}
			case "alias":
				if err := os.Rename(r.path, r.path+".original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(r.path+".original", r.path); err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyUpdatesWithRemovals(updates, removals); err == nil {
				t.Fatal("accepted changed retirement input")
			}
			for _, update := range updates {
				removalWant(t, update.path, before[update.path])
			}
			removalWant(t, removals[1].path, before[removals[1].path])
		})
	}
}

func TestCredentialRemovalAbsentAndEmptyAreDistinct(t *testing.T) {
	dir := t.TempDir()
	path, source := filepath.Join(dir, "live"), filepath.Join(dir, "source")
	r, err := PrepareCredentialRemoval(source, path, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdatesWithRemovals(nil, []*Removal{r}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("absent source materialized: %v", err)
	}
	removalWrite(t, path, "")
	if err := ApplyUpdatesWithRemovals(nil, []*Removal{r}); err == nil {
		t.Fatal("new empty file was mistaken for prior absence")
	}
	r, err = PrepareCredentialRemoval(source, path, []byte{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdatesWithRemovals(nil, []*Removal{r}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("empty credential was not retired: %v", err)
	}
}

func TestCredentialRemovalBatchRejectsDuplicateDestinations(t *testing.T) {
	updates, removals, before := removalFixture(t)
	r, err := PrepareCredentialRemoval(filepath.Join(t.TempDir(), "absent"), updates[0].path, []byte(before[updates[0].path]), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyUpdatesWithRemovals(updates, append(removals, r)); err == nil {
		t.Fatal("accepted settings replacement and retirement at the same path")
	}
	for path, body := range before {
		removalWant(t, path, body)
	}
}
