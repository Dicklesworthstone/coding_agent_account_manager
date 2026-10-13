package authfile

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func putIsolatedTestFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedSourceRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "credential.json")
	if _, err := readIsolatedSourceFile(path, true, 8); err == nil {
		t.Fatal("missing required source accepted")
	}
	absent, err := readIsolatedSourceFile(path, false, 8)
	if err != nil || absent.info != nil || absent.unchanged() != nil {
		t.Fatalf("optional absence: %+v, %v", absent, err)
	}
	putIsolatedTestFile(t, path, "12345678")
	if err := absent.unchanged(); err == nil {
		t.Fatal("newly created optional source was not detected")
	}
	got, err := readIsolatedSourceFile(path, true, 8)
	if err != nil || string(got.data) != "12345678" || got.unchanged() != nil {
		t.Fatalf("bounded source read: %+v, %v", got, err)
	}
	if _, err := readIsolatedSourceFile(path, true, 7); err == nil {
		t.Fatal("oversized source accepted")
	}
	if _, err := readIsolatedSourceFile(root, true, 8); err == nil {
		t.Fatal("directory accepted as a source file")
	}
	link := filepath.Join(root, "linked.json")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := readIsolatedSourceFile(link, true, 8); err == nil {
		t.Fatal("source file symlink accepted")
	}
	if f, err := openIsolatedSourceFile(link); err == nil {
		f.Close()
		if runtime.GOOS != "windows" {
			t.Fatal("Unix source opener followed a symlink")
		}
	}
}

func TestIsolatedSourceDetectsGenerationChanges(t *testing.T) {
	for _, change := range []string{"bytes", "replacement", "removal", "symlink", "permissions"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.json")
			putIsolatedTestFile(t, path, "before")
			source, err := readIsolatedSourceFile(path, true, 1024)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "bytes":
				// Same size, inode and restored timestamp: compare bytes too.
				putIsolatedTestFile(t, path, "after!")
				if err := os.Chtimes(path, source.info.ModTime(), source.info.ModTime()); err != nil {
					t.Fatal(err)
				}
			case "replacement":
				putIsolatedTestFile(t, path+".new", "before")
				if err := os.Rename(path+".new", path); err != nil {
					t.Fatal(err)
				}
			case "removal":
				if err := os.Rename(path, path+".retired"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(path, path+".retired"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".retired", path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "permissions":
				if runtime.GOOS == "windows" {
					t.Skip("Unix file modes")
				}
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if err := source.unchanged(); err == nil {
				t.Fatal("source generation change was accepted")
			}
		})
	}
}

func TestIsolatedSourceDirectoryIdentity(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "source")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	dir, err := inspectIsolatedSourceDir(path)
	if err != nil || dir.unchanged() != nil {
		t.Fatalf("inspect source directory: %v", err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := dir.unchanged(); err == nil {
		t.Fatal("directory replacement accepted")
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := inspectIsolatedSourceDir(link); err == nil {
		t.Fatal("directory symlink accepted")
	}
}
