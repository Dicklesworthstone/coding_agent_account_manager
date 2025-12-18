package signals

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPIDFileRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	pidPath := filepath.Join(tmpDir, "caam.pid")

	if err := WritePIDFile(pidPath, 12345); err != nil {
		t.Fatalf("WritePIDFile: %v", err)
	}

	pid, err := ReadPIDFile(pidPath)
	if err != nil {
		t.Fatalf("ReadPIDFile: %v", err)
	}
	if pid != 12345 {
		t.Fatalf("pid=%d, want 12345", pid)
	}

	if err := RemovePIDFile(pidPath); err != nil {
		t.Fatalf("RemovePIDFile: %v", err)
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatalf("pid file should be removed, stat err=%v", err)
	}
}

func TestDefaultPIDFilePathUsesCAAMHome(t *testing.T) {
	orig := os.Getenv("CAAM_HOME")
	defer os.Setenv("CAAM_HOME", orig)

	tmpDir := t.TempDir()
	os.Setenv("CAAM_HOME", tmpDir)

	got := DefaultPIDFilePath()
	want := filepath.Join(tmpDir, "caam.pid")
	if got != want {
		t.Fatalf("DefaultPIDFilePath=%q, want %q", got, want)
	}
}
