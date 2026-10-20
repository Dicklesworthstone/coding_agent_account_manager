package authfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func isolatedPublicationFixture(t *testing.T) (string, string, isolatedSourceDir, map[string]string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix directory durability protocol; Windows retains the existing publisher")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "new-vault", "claude", "saved")
	ancestor, err := existingIsolatedBackupAncestor(filepath.Dir(destination))
	if err != nil {
		t.Fatal(err)
	}
	stage := filepath.Join(root, "new-vault", "claude", "private", "prepared")
	if err := os.MkdirAll(stage, 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"synthetic-access","refreshToken":"synthetic-refresh"}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"synthetic-account"}}`,
		"meta.json":         `{"tool":"claude","profile":"saved","files":2}`,
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(stage, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return stage, destination, ancestor, files
}

func assertPublicationFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != len(files) {
		t.Fatalf("incomplete snapshot directory: %v", err)
	}
	for name, want := range files {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || string(data) != want {
			t.Fatalf("snapshot artifact %s changed: %v", name, err)
		}
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
			t.Fatalf("snapshot artifact %s lost private mode: %v", name, err)
		}
	}
}

func TestIsolatedPublicationFlushOrdering(t *testing.T) {
	stage, destination, ancestor, files := isolatedPublicationFixture(t)
	var calls []string
	syncFile := func(file *os.File) error {
		calls = append(calls, "sync:"+file.Name())
		return nil
	}
	publish := func() error {
		calls = append(calls, "publish")
		return os.Rename(stage, destination)
	}
	if err := publishIsolatedBackupWithSync(stage, destination, ancestor, publish, syncFile); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"sync:" + filepath.Join(stage, ".claude.json"),
		"sync:" + filepath.Join(stage, ".credentials.json"),
		"sync:" + filepath.Join(stage, "meta.json"),
		"sync:" + stage,
		"sync:" + filepath.Dir(destination),
		"sync:" + filepath.Dir(filepath.Dir(destination)),
		"sync:" + ancestor.path,
		"publish",
		"sync:" + filepath.Dir(destination),
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("incorrect durability ordering:\n got %v\nwant %v", calls, want)
	}
	assertPublicationFiles(t, destination, files)
}

func TestIsolatedPublicationFailuresPreserveCompleteState(t *testing.T) {
	// Seven pre-publication syncs, the atomic rename, then the final sync.
	for fault := 0; fault < 9; fault++ {
		t.Run(fmt.Sprintf("operation-%d", fault), func(t *testing.T) {
			stage, destination, ancestor, files := isolatedPublicationFixture(t)
			injected := errors.New("injected storage failure")
			step, published := 0, false
			next := func() error {
				current := step
				step++
				if current == fault {
					return injected
				}
				return nil
			}
			publish := func() error {
				if err := next(); err != nil {
					return err
				}
				if err := os.Rename(stage, destination); err != nil {
					return err
				}
				published = true
				return nil
			}
			err := publishIsolatedBackupWithSync(stage, destination, ancestor, publish, func(*os.File) error { return next() })
			if !errors.Is(err, injected) {
				t.Fatalf("lost storage failure: %v", err)
			}
			if step != fault+1 {
				t.Fatalf("continued after failed storage operation: %d", step)
			}
			var uncertain *SnapshotPublishedError
			if fault == 8 {
				if !published || !errors.As(err, &uncertain) || uncertain.Path != destination {
					t.Fatalf("post-publication failure misclassified: %v", err)
				}
				if message := uncertain.Error(); !strings.Contains(message, destination) || !strings.Contains(message, "inspect the saved entry before retrying") {
					t.Fatalf("missing uncertain-publication recovery guidance: %s", message)
				}
				assertPublicationFiles(t, destination, files)
				if _, err := os.Lstat(stage); !os.IsNotExist(err) {
					t.Fatalf("published snapshot was rolled back: %v", err)
				}
			} else {
				if published || errors.As(err, &uncertain) {
					t.Fatalf("failed preparation published a snapshot: %v", err)
				}
				if _, err := os.Lstat(destination); !os.IsNotExist(err) {
					t.Fatalf("failed preparation left a public entry: %v", err)
				}
				assertPublicationFiles(t, stage, files)
			}
		})
	}
}

func TestIsolatedPublicationRechecksSourceAfterFlushing(t *testing.T) {
	stage, destination, ancestor, files := isolatedPublicationFixture(t)
	sourcePath := filepath.Join(t.TempDir(), "source.json")
	putIsolatedTestFile(t, sourcePath, "original generation")
	source, err := readIsolatedSourceFile(sourcePath, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	checked := false
	err = publishIsolatedBackupWithSync(stage, destination, ancestor, func() error {
		checked = true
		if err := source.unchanged(); err != nil {
			return err
		}
		return os.Rename(stage, destination)
	}, func(file *os.File) error {
		if file.Name() == stage {
			putIsolatedTestFile(t, sourcePath, "rotated generation")
		}
		return nil
	})
	if err == nil || !checked || !strings.Contains(err.Error(), "source changed") {
		t.Fatalf("stale source survived flushing: %v", err)
	}
	assertPublicationFiles(t, stage, files)
	if _, err := os.Lstat(destination); !os.IsNotExist(err) {
		t.Fatalf("source change published a snapshot: %v", err)
	}
}

func TestIsolatedPublicationCollisionIsNotDurabilityError(t *testing.T) {
	stage, destination, ancestor, files := isolatedPublicationFixture(t)
	if err := os.Mkdir(destination, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "existing")
	putIsolatedTestFile(t, marker, "existing account")
	err := publishIsolatedBackupWithSync(stage, destination, ancestor, func() error {
		// The existing no-replace publisher reports collisions unchanged.
		return fmt.Errorf("snapshot destination appeared: %w", os.ErrExist)
	}, func(*os.File) error { return nil })
	var uncertain *SnapshotPublishedError
	if !errors.Is(err, os.ErrExist) || errors.As(err, &uncertain) {
		t.Fatalf("collision misclassified: %v", err)
	}
	data, err := os.ReadFile(marker)
	if err != nil || string(data) != "existing account" {
		t.Fatalf("collision damaged the destination: %v", err)
	}
	assertPublicationFiles(t, stage, files)
}

func TestIsolatedPublicationRealFilesystemSync(t *testing.T) {
	stage, destination, ancestor, files := isolatedPublicationFixture(t)
	if err := publishIsolatedBackupDurably(stage, destination, ancestor, func() error {
		return os.Rename(stage, destination)
	}); err != nil {
		t.Fatal(err)
	}
	assertPublicationFiles(t, destination, files)
}

func TestIsolatedPublicationWindowsRetainsPublisher(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-specific publication path")
	}
	want := errors.New("publisher result")
	called := false
	err := publishIsolatedBackupDurably("unused-stage", "unused-destination", isolatedSourceDir{}, func() error {
		called = true
		return want
	})
	if !called || !errors.Is(err, want) {
		t.Fatalf("Windows publication unexpectedly attempted Unix directory sync: %v", err)
	}
}

func TestIsolatedPublicationProcessExitKeepsAllOrNothing(t *testing.T) {
	for _, phase := range []string{"before", "after"} {
		t.Run(phase, func(t *testing.T) {
			stage, destination, ancestor, files := isolatedPublicationFixture(t)
			executable, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			child := exec.CommandContext(ctx, executable, "-test.run=^TestIsolatedPublicationExitChild$", "-test.count=1")
			child.Env = append(os.Environ(),
				"CAAM_TEST_PUBLICATION_STAGE="+stage,
				"CAAM_TEST_PUBLICATION_DESTINATION="+destination,
				"CAAM_TEST_PUBLICATION_ANCESTOR="+ancestor.path,
				"CAAM_TEST_PUBLICATION_EXIT="+phase)
			output, err := child.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 86 {
				t.Fatalf("child did not reach the publication boundary: %v\n%s", err, output)
			}
			if phase == "before" {
				assertPublicationFiles(t, stage, files)
				if _, err := os.Lstat(destination); !os.IsNotExist(err) {
					t.Fatalf("process exit before rename published credentials: %v", err)
				}
			} else {
				assertPublicationFiles(t, destination, files)
				if _, err := os.Lstat(stage); !os.IsNotExist(err) {
					t.Fatalf("post-rename process exit restored staging: %v", err)
				}
			}
		})
	}
}

func TestIsolatedPublicationExitChild(t *testing.T) {
	phase := os.Getenv("CAAM_TEST_PUBLICATION_EXIT")
	if phase == "" {
		return
	}
	stage := os.Getenv("CAAM_TEST_PUBLICATION_STAGE")
	destination := os.Getenv("CAAM_TEST_PUBLICATION_DESTINATION")
	ancestor, err := inspectIsolatedSourceDir(os.Getenv("CAAM_TEST_PUBLICATION_ANCESTOR"))
	if err != nil {
		t.Fatal(err)
	}
	published := false
	err = publishIsolatedBackupWithSync(stage, destination, ancestor, func() error {
		if err := os.Rename(stage, destination); err != nil {
			return err
		}
		published = true
		return nil
	}, func(file *os.File) error {
		if (phase == "before" && file.Name() == stage) || (phase == "after" && published) {
			os.Exit(86) // Only this test subprocess exits; no defers run.
		}
		return nil
	})
	t.Fatalf("child missed the requested process-exit boundary: %v", err)
}

func TestIsolatedPublicationRejectsUnsafePreparedFiles(t *testing.T) {
	for _, fault := range []string{"symlink", "directory", "empty", "missing", "outside-ancestor", "changed-ancestor"} {
		t.Run(fault, func(t *testing.T) {
			stage, destination, ancestor, _ := isolatedPublicationFixture(t)
			switch fault {
			case "symlink":
				if err := os.Symlink(filepath.Join(stage, "meta.json"), filepath.Join(stage, "unexpected")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "directory":
				if err := os.Mkdir(filepath.Join(stage, "unexpected"), 0700); err != nil {
					t.Fatal(err)
				}
			case "empty":
				stage = t.TempDir()
			case "missing":
				stage = filepath.Join(t.TempDir(), "missing")
			case "outside-ancestor":
				var err error
				ancestor, err = existingIsolatedBackupAncestor(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
			case "changed-ancestor":
				if err := os.Rename(ancestor.path, ancestor.path+".old"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = os.Rename(ancestor.path+".old", ancestor.path) })
			}
			called := false
			err := publishIsolatedBackupWithSync(stage, destination, ancestor, func() error {
				called = true
				return nil
			}, func(*os.File) error { return nil })
			if err == nil || called {
				t.Fatalf("unsafe stage accepted: %v", err)
			}
		})
	}
}

func TestIsolatedPublicationDetectsDirectoryReplacementDuringSync(t *testing.T) {
	stage, destination, ancestor, _ := isolatedPublicationFixture(t)
	called := false
	err := publishIsolatedBackupWithSync(stage, destination, ancestor, func() error {
		called = true
		return nil
	}, func(file *os.File) error {
		if file.Name() == stage {
			if err := os.Rename(stage, stage+".old"); err != nil {
				return err
			}
			return os.Mkdir(stage, 0700)
		}
		return nil
	})
	if err == nil || called {
		t.Fatalf("replaced stage published: %v", err)
	}
}

func TestIsolatedBackupAncestor(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ancestor, err := existingIsolatedBackupAncestor(filepath.Join(root, "new", "claude"))
	if err != nil || ancestor.path != root {
		t.Fatalf("wrong existing ancestor: %+v: %v", ancestor, err)
	}
	path := filepath.Join(root, "file")
	putIsolatedTestFile(t, path, "not a directory")
	if _, err := existingIsolatedBackupAncestor(path); err == nil {
		t.Fatal("file accepted as a vault ancestor")
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	ancestor, err = existingIsolatedBackupAncestor(filepath.Join(alias, "new"))
	if err != nil || ancestor.path != root || ancestor.unchanged() != nil {
		t.Fatalf("vault alias was not resolved: %+v: %v", ancestor, err)
	}
}
