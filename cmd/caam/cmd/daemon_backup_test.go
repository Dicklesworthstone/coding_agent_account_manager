package cmd

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/spf13/cobra"
)

func newAutoBackupCmd(t *testing.T, out *bytes.Buffer) *cobra.Command {
	t.Helper()
	c := &cobra.Command{RunE: runDaemonAutoBackup}
	c.Flags().AddFlagSet(daemonAutoBackupCmd.Flags())
	c.SetOut(out)
	// Flags are package-level; reset what a previous call set.
	t.Cleanup(func() {
		for _, name := range []string{"enable", "disable", "interval", "keep", "location"} {
			f := daemonAutoBackupCmd.Flags().Lookup(name)
			f.Value.Set(f.DefValue)
			f.Changed = false
		}
	})
	return c
}

func TestDaemonAutoBackupConfiguresSchedule(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAAM_HOME", root)

	var out bytes.Buffer
	c := newAutoBackupCmd(t, &out)
	location := filepath.Join(root, "archives")
	if err := c.ParseFlags([]string{"--enable", "--interval", "24h", "--keep", "3", "--location", location}); err != nil {
		t.Fatal(err)
	}
	if err := c.RunE(c, nil); err != nil {
		t.Fatalf("auto-backup: %v", err)
	}
	if !strings.Contains(out.String(), "every 1d, keeping 3, in "+location) {
		t.Fatalf("output = %q", out.String())
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Backup.Enabled || cfg.Backup.GetKeepLast() != 3 || cfg.Backup.GetLocation() != location || cfg.Backup.GetInterval().Hours() != 24 {
		t.Fatalf("saved backup config = %+v", cfg.Backup)
	}

	// Status JSON reflects the saved schedule.
	out.Reset()
	statusCmd := &cobra.Command{}
	statusCmd.Flags().Bool("json", true, "")
	statusCmd.SetOut(&out)
	if err := runDaemonStatus(statusCmd, nil); err != nil {
		t.Fatal(err)
	}
	var status struct {
		Running    bool `json:"running"`
		AutoBackup struct {
			Enabled  bool   `json:"enabled"`
			Interval string `json:"interval"`
		} `json:"auto_backup"`
	}
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, out.String())
	}
	if status.Running || !status.AutoBackup.Enabled || status.AutoBackup.Interval != "1d" {
		t.Fatalf("status = %+v", status)
	}
	if strings.Contains(out.String(), "0001-01-01") {
		t.Fatalf("zero timestamps must be omitted: %s", out.String())
	}
}

func TestDaemonAutoBackupRejectsBadValues(t *testing.T) {
	t.Setenv("CAAM_HOME", t.TempDir())
	for _, args := range [][]string{
		{"--enable", "--disable"},
		{"--interval", "5m"},
		{"--keep", "0"},
	} {
		var out bytes.Buffer
		c := newAutoBackupCmd(t, &out)
		if err := c.ParseFlags(args); err != nil {
			t.Fatal(err)
		}
		if err := c.RunE(c, nil); err == nil {
			t.Errorf("args %v accepted", args)
		}
		for _, name := range []string{"enable", "disable", "interval", "keep", "location"} {
			f := daemonAutoBackupCmd.Flags().Lookup(name)
			f.Value.Set(f.DefValue)
			f.Changed = false
		}
	}
}
