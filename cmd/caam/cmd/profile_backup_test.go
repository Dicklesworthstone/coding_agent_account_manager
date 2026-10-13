package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/spf13/cobra"
)

func setupProfileBackupCommand(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CAAM_HOME", filepath.Join(home, "caam"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	source, err := profile.NewStore(profile.DefaultStorePath()).Create("claude", "isolated", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(source.XDGConfigPath(), "claude-code")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"new-access","refreshToken":"new-refresh","expiresAt":4102444800000}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"new-id","emailAddress":"new@example.test"}}`,
		"settings.json":     `{"model":"opus"}`,
	} {
		if err := os.WriteFile(filepath.Join(config, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	live := filepath.Join(home, ".claude", ".credentials.json")
	if err := os.MkdirAll(filepath.Dir(live), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(live, []byte("live credential must not even be probed"), 0600); err != nil {
		t.Fatal(err)
	}
	return config, live
}

func executeProfileBackupTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	root := &cobra.Command{
		Use: "caam", SilenceErrors: true, SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("unexpected live-login startup hook")
		},
	}
	parent := &cobra.Command{Use: "profile"}
	parent.AddCommand(newProfileBackupCommand())
	root.AddCommand(parent)
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"profile", "backup"}, args...))
	err := root.Execute()
	return out.String(), stderr.String(), err
}

func TestProfileBackupCommandNonDisruptiveJSON(t *testing.T) {
	config, live := setupProfileBackupCommand(t)
	marker := filepath.Join(t.TempDir(), "keychain-called")
	if runtime.GOOS != "windows" {
		bin := filepath.Join(t.TempDir(), "security")
		script := "#!/bin/sh\nprintf called > \"$CAAM_ISOLATED_TEST_MARKER\"\nexit 1\n"
		if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CAAM_KEYCHAIN", "1")
		t.Setenv("CAAM_KEYCHAIN_BIN", bin)
		t.Setenv("CAAM_ISOLATED_TEST_MARKER", marker)
	}
	before, err := os.Stat(live)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := executeProfileBackupTest(t, "claude", "isolated", "--as", "saved", "--json")
	if err != nil {
		t.Fatalf("backup failed: %v; stdout=%s; stderr=%s", err, out, stderr)
	}
	var result profileBackupResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Success || result.Profile != "saved" || result.SourceProfile != "isolated" || len(result.Warnings) == 0 {
		t.Fatalf("invalid JSON result: %s: %v", out, err)
	}
	if stderr != "" {
		t.Fatalf("JSON command emitted unstructured warnings: %s", stderr)
	}
	after, err := os.Stat(live)
	data, readErr := os.ReadFile(live)
	if err != nil || readErr != nil || !os.SameFile(before, after) || string(data) != "live credential must not even be probed" {
		t.Fatal("live credential was modified")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("isolated backup invoked the live keychain: %v", err)
	}
	for _, name := range []string{".credentials.json", ".claude.json", "settings.json"} {
		source, err := os.ReadFile(filepath.Join(config, name))
		if err != nil {
			t.Fatal(err)
		}
		saved, err := os.ReadFile(filepath.Join(result.Path, name))
		if err != nil || !bytes.Equal(source, saved) {
			t.Fatalf("missing snapshot artifact %s: %v", name, err)
		}
	}
}

func TestProfileBackupCommandDefaultsAndFailures(t *testing.T) {
	_, _ = setupProfileBackupCommand(t)
	out, stderr, err := executeProfileBackupTest(t, "claude", "isolated")
	if err != nil || !strings.Contains(out, "vault claude/isolated") || !strings.Contains(stderr, "not an independent OAuth login") {
		t.Fatalf("default backup: %s; %s; %v", out, stderr, err)
	}
	v := authfile.NewVault(authfile.DefaultVaultPath())
	path := v.BackupPath("claude", "isolated", ".credentials.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"claude", "isolated", "--json"},
		{"claude", "missing", "--json"},
		{"claude", "../escape", "--json"},
		{"claude", "isolated", "--as", "../escape", "--json"},
		{"codex", "isolated", "--json"},
	} {
		out, _, err := executeProfileBackupTest(t, args...)
		var result profileBackupResult
		if err == nil || json.Unmarshal([]byte(out), &result) != nil || result.Success || result.Error == "" {
			t.Fatalf("failure did not produce JSON plus a nonzero exit: %v: %s: %v", args, out, err)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failure damaged the existing vault account")
	}
}

func TestProfileBackupCommandRegistered(t *testing.T) {
	cmd, _, err := profileCmd.Find([]string{"backup"})
	if err != nil || cmd == profileCmd || cmd.Name() != "backup" {
		t.Fatalf("profile backup was not registered: %v", err)
	}
}

func TestProfileBackupCommandRejectsMetadataBeforeLoadingIdentity(t *testing.T) {
	for _, fault := range []string{"symlink", "oversized", "redirect"} {
		t.Run(fault, func(t *testing.T) {
			config, live := setupProfileBackupCommand(t)
			path := filepath.Join(filepath.Dir(filepath.Dir(config)), "profile.json")
			switch fault {
			case "symlink":
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(live, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			case "oversized":
				if err := os.Truncate(path, authfile.MaxDiscoveryFileBytes+1); err != nil {
					t.Fatal(err)
				}
			case "redirect":
				data := `{"name":"isolated","provider":"claude","base_path":"/not/the/profile"}`
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			}
			out, _, err := executeProfileBackupTest(t, "claude", "isolated", "--json")
			var result profileBackupResult
			if err == nil || json.Unmarshal([]byte(out), &result) != nil || result.Success {
				t.Fatalf("accepted unsafe metadata: %s: %v", out, err)
			}
			v := authfile.NewVault(authfile.DefaultVaultPath())
			if _, err := os.Lstat(v.ProfilePath("claude", "isolated")); !os.IsNotExist(err) {
				t.Fatalf("failed backup published a destination: %v", err)
			}
		})
	}
}
