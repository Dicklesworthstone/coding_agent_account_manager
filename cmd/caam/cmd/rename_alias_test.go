package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

// setupAliasRenameEnv gives the test its own vault and config directory and
// seeds codex profiles with an auth.json each.
func setupAliasRenameEnv(t *testing.T, profiles ...string) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam_home"))

	oldVault := vault
	t.Cleanup(func() { vault = oldVault })
	vaultDir := filepath.Join(root, "vault")
	vault = authfile.NewVault(vaultDir)

	for _, p := range profiles {
		dir := filepath.Join(vaultDir, "codex", p)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"profile":"`+p+`"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return vaultDir
}

// newTestCmd returns a fresh command carrying the flags of the named
// command (rename or alias), so tests never share flag state through the
// package-level commands, with the given stdin and flag values.
func newTestCmd(t *testing.T, kind, stdin string, flags map[string]string) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{Use: kind}
	switch kind {
	case "rename":
		c.Flags().Bool("delete-old", false, "")
		c.Flags().Bool("migrate-aliases", true, "")
		c.Flags().Bool("json", false, "")
		c.Flags().BoolP("yes", "y", false, "")
	case "alias":
		c.Flags().Bool("list", false, "")
		c.Flags().StringP("remove", "r", "", "")
		c.Flags().Bool("json", false, "")
	default:
		t.Fatalf("unknown command kind %q", kind)
	}
	for k, v := range flags {
		if err := c.Flags().Set(k, v); err != nil {
			t.Fatalf("set --%s: %v", k, err)
		}
	}
	var stderr bytes.Buffer
	c.SetIn(strings.NewReader(stdin))
	c.SetErr(&stderr)
	return c, &stderr
}

func profileExists(vaultDir, name string) bool {
	_, err := os.Stat(filepath.Join(vaultDir, "codex", name, "auth.json"))
	return err == nil
}

func TestRenameCopiesAndKeepsOriginal(t *testing.T) {
	vaultDir := setupAliasRenameEnv(t, "auto-1")
	c, _ := newTestCmd(t, "rename", "", nil)
	if err := runRename(c, []string{"codex", "auto-1", "work"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !profileExists(vaultDir, "auto-1") || !profileExists(vaultDir, "work") {
		t.Fatalf("rename without --delete-old must keep the source and create the copy")
	}
	got, _ := os.ReadFile(filepath.Join(vaultDir, "codex", "work", "auth.json"))
	if string(got) != `{"profile":"auto-1"}` {
		t.Errorf("copied auth.json = %q", got)
	}
}

func TestRenameRefusesMissingSourceAndExistingDestination(t *testing.T) {
	setupAliasRenameEnv(t, "a", "b")
	c, _ := newTestCmd(t, "rename", "", nil)
	if err := runRename(c, []string{"codex", "missing", "x"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("missing source: err = %v", err)
	}
	if err := runRename(c, []string{"codex", "a", "b"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("existing destination: err = %v", err)
	}
}

func TestRenameDeleteOldNeedsConfirmation(t *testing.T) {
	vaultDir := setupAliasRenameEnv(t, "old")

	// No answer on stdin (e.g. a script): the old profile must survive, and
	// the prompt must not go to stdout.
	c, stderr := newTestCmd(t, "rename", "", map[string]string{"delete-old": "true"})
	if err := runRename(c, []string{"codex", "old", "new"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !profileExists(vaultDir, "old") || !profileExists(vaultDir, "new") {
		t.Fatalf("declined --delete-old must keep the old profile")
	}
	if !strings.Contains(stderr.String(), "Delete old profile codex/old?") {
		t.Errorf("confirmation prompt should be on stderr, got %q", stderr.String())
	}

	// "n" also declines.
	c, _ = newTestCmd(t, "rename", "n\n", map[string]string{"delete-old": "true"})
	if err := runRename(c, []string{"codex", "new", "newer"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if !profileExists(vaultDir, "new") {
		t.Fatalf("answering n must keep the old profile")
	}

	// "y" deletes.
	c, _ = newTestCmd(t, "rename", "y\n", map[string]string{"delete-old": "true"})
	if err := runRename(c, []string{"codex", "newer", "final"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if profileExists(vaultDir, "newer") || !profileExists(vaultDir, "final") {
		t.Fatalf("confirmed --delete-old should move the profile")
	}
}

// rename copies only top-level files, so --delete-old must refuse (before
// changing anything) when the source has subdirectories it would lose.
func TestRenameDeleteOldRefusesUncopiedSubdirectories(t *testing.T) {
	vaultDir := setupAliasRenameEnv(t, "old")
	if err := os.MkdirAll(filepath.Join(vaultDir, "codex", "old", "extra"), 0o700); err != nil {
		t.Fatal(err)
	}
	c, _ := newTestCmd(t, "rename", "", map[string]string{"delete-old": "true", "yes": "true"})
	err := runRename(c, []string{"codex", "old", "new"})
	if err == nil || !strings.Contains(err.Error(), "extra/") {
		t.Fatalf("err = %v, want a refusal naming extra/", err)
	}
	if !profileExists(vaultDir, "old") {
		t.Fatal("old profile was deleted")
	}
	if _, err := os.Stat(filepath.Join(vaultDir, "codex", "new")); !os.IsNotExist(err) {
		t.Errorf("refused rename should not create the destination (stat err = %v)", err)
	}
}

func TestRenameMigratesAliases(t *testing.T) {
	setupAliasRenameEnv(t, "auto-1")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	cfg.AddAlias("codex", "auto-1", "w")
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}

	c, _ := newTestCmd(t, "rename", "", nil)
	if err := runRename(c, []string{"codex", "auto-1", "work"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	cfg, err = config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolveAliasForProvider("codex", "w"); got != "work" {
		t.Errorf("alias w resolves to %q after rename, want work", got)
	}
}

func TestAliasAddConflictAndRemove(t *testing.T) {
	setupAliasRenameEnv(t, "one", "two")

	add := func(profile, alias string) error {
		c, _ := newTestCmd(t, "alias", "", nil)
		return runAlias(c, []string{"codex", profile, alias})
	}
	if err := add("one", "w"); err != nil {
		t.Fatalf("add alias: %v", err)
	}
	if err := add("one", "w"); err != nil {
		t.Errorf("re-adding the same alias should be a no-op, got %v", err)
	}
	if err := add("two", "w"); err == nil || !strings.Contains(err.Error(), "already used") {
		t.Errorf("alias reused for another profile: err = %v", err)
	}
	c, _ := newTestCmd(t, "alias", "", nil)
	if err := runAlias(c, []string{"codex", "ghost", "g"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("alias for a missing profile: err = %v", err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.ResolveAliasForProvider("codex", "w"); got != "one" {
		t.Fatalf("alias w resolves to %q, want one", got)
	}

	c, _ = newTestCmd(t, "alias", "", map[string]string{"remove": "w"})
	if err := runAlias(c, nil); err != nil {
		t.Fatalf("remove alias: %v", err)
	}
	c, _ = newTestCmd(t, "alias", "", map[string]string{"remove": "w"})
	if err := runAlias(c, nil); err == nil || !strings.Contains(err.Error(), "caam alias --list") {
		t.Errorf("removing a missing alias should point at 'caam alias --list', got %v", err)
	}
}

// The JSON result of a declined --delete-old must be the only thing on stdout.
func TestRenameDeleteOldJSONStaysParseable(t *testing.T) {
	setupAliasRenameEnv(t, "old")
	c, _ := newTestCmd(t, "rename", "", map[string]string{"delete-old": "true", "json": "true"})

	out, err := captureStdout(t, func() error {
		return runRename(c, []string{"codex", "old", "new"})
	})
	if err != nil {
		t.Fatalf("rename: %v", err)
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, out)
	}
	if result["deleted"] != false {
		t.Errorf("deleted = %v, want false", result["deleted"])
	}
}
