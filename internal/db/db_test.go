package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenAt_CreatesDBAndRunsMigrations(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "caam.db")

	d, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file stat error = %v", err)
	}

	// Migration-created tables should exist.
	for _, table := range []string{"schema_version", "activity_log", "profile_stats", "limit_events"} {
		var name string
		if err := d.Conn().QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name); err != nil {
			t.Fatalf("table %s missing: %v", table, err)
		}
	}

	// Migrations should be idempotent.
	if err := RunMigrations(d.Conn()); err != nil {
		t.Fatalf("RunMigrations() second run error = %v", err)
	}

	var version int
	if err := d.Conn().QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&version); err != nil {
		t.Fatalf("read schema_version error = %v", err)
	}
	if version != 2 {
		t.Fatalf("schema_version max = %d, want 2", version)
	}
}

func TestOpenAt_EnablesWALMode(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "caam.db")

	d, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	var mode string
	if err := d.Conn().QueryRow(`PRAGMA journal_mode;`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode error = %v", err)
	}
	if strings.ToLower(mode) != "wal" {
		t.Fatalf("journal_mode = %q, want %q", mode, "wal")
	}
}

func TestOpenAt_CorruptDB_RenamedAndRecreated(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "caam.db")

	// Create an invalid "database" file.
	if err := os.WriteFile(path, []byte("not a database"), 0600); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}

	d, err := OpenAt(path)
	if err != nil {
		t.Fatalf("OpenAt() error = %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Original should be replaced by a valid sqlite database file.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file missing after recreate: %v", err)
	}

	backups, err := filepath.Glob(path + ".corrupt.*")
	if err != nil {
		t.Fatalf("glob corrupt backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("corrupt backup count = %d, want 1", len(backups))
	}
}
