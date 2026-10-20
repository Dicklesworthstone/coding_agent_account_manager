package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/spf13/cobra"
)

// Exercise Cobra dispatch, not only RunE: skipping live startup is part of the
// isolated import's safety boundary. Leave error/usage silencing at its defaults
// so failed --json imports must enforce their own output contract.
func executeBackupSourceTest(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	root := &cobra.Command{Use: "caam", PersistentPreRunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("unexpected live startup")
	}}
	cmd := &cobra.Command{Use: "backup <tool> <profile-name>", Args: cobra.ExactArgs(2), RunE: func(*cobra.Command, []string) error {
		return fmt.Errorf("unexpected native credential reader")
	}}
	cmd.Flags().Bool("json", false, "JSON output")
	configureBackupSource(cmd)
	root.AddCommand(cmd)
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"backup"}, args...))
	err := root.Execute()
	return out.String(), stderr.String(), err
}

func TestBackupFromProfilePublishesUsableVaultWithoutTouchingLive(t *testing.T) {
	config, live := setupProfileBackupCommand(t)
	t.Setenv("CAAM_KEYCHAIN", "0")
	// Track the complete native auth tree, including unrelated session files,
	// settings, directory mtimes, file permissions and inode identities.
	liveRoot := filepath.Dir(live)
	for name, data := range map[string]string{
		"settings.json": `{"model":"sonnet"}`,
		"session.json":  `{"running":true}`,
	} {
		if err := os.WriteFile(filepath.Join(liveRoot, name), []byte(data), 0640); err != nil {
			t.Fatal(err)
		}
	}
	before, err := captureBackupTree(liveRoot)
	if err != nil {
		t.Fatal(err)
	}
	sourceRoot := filepath.Dir(filepath.Dir(config))
	sourceBefore, err := captureBackupTree(sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := executeBackupSourceTest(t, "CLAUDE", "saved", "--from-profile", "isolated", "--json")
	if err != nil || stderr != "" {
		t.Fatalf("import failed: %v; stdout=%s; stderr=%s", err, out, stderr)
	}
	var result profileBackupResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Success || result.Tool != "claude" || result.Profile != "saved" || result.SourceProfile != "isolated" || len(result.Warnings) != 1 {
		t.Fatalf("invalid isolated backup result: %s: %v", out, err)
	}
	assertBackupTreeUnchanged(t, liveRoot, before)
	assertBackupTreeUnchanged(t, sourceRoot, sourceBefore)
	v := authfile.NewVault(authfile.DefaultVaultPath())
	names, err := v.List("claude")
	if err != nil || !reflect.DeepEqual(names, []string{"saved"}) {
		t.Fatalf("wrong vault listing: %v: %v", names, err)
	}
	for _, name := range []string{".credentials.json", ".claude.json", "settings.json", "meta.json"} {
		info, err := os.Stat(filepath.Join(result.Path, name))
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
			t.Fatalf("missing or insecure %s: %v", name, err)
		}
	}
	// Activation is explicitly separate, using a different synthetic live home
	// so the original running account remains untouched throughout this test.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	if _, err := v.Switch(authfile.ClaudeAuthFiles(), "saved", authfile.SwitchOptions{BackupMode: "always"}); err != nil {
		t.Fatalf("imported vault entry cannot be activated: %v", err)
	}
	activated, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
	if err != nil || !bytes.Contains(activated, []byte("new-access")) {
		t.Fatalf("wrong activated credential: %v", err)
	}
	assertBackupTreeUnchanged(t, liveRoot, before)
	assertBackupTreeUnchanged(t, sourceRoot, sourceBefore)
}

type backupTreeEntry struct {
	info os.FileInfo
	data []byte
}

func captureBackupTree(root string) (map[string]backupTreeEntry, error) {
	entries := make(map[string]backupTreeEntry)
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		entries[path] = backupTreeEntry{info: info, data: data}
		return nil
	})
	return entries, err
}

func assertBackupTreeUnchanged(t *testing.T, root string, before map[string]backupTreeEntry) {
	t.Helper()
	after, err := captureBackupTree(root)
	if err != nil || len(after) != len(before) {
		t.Fatalf("auth tree changed: %s: %v", root, err)
	}
	for path, want := range before {
		got, ok := after[path]
		if !ok || !os.SameFile(want.info, got.info) || want.info.Mode() != got.info.Mode() || !want.info.ModTime().Equal(got.info.ModTime()) || !bytes.Equal(want.data, got.data) {
			t.Fatalf("auth tree entry changed: %s", path)
		}
	}
}

func TestBackupFromProfileFailuresNeverFallBackToNative(t *testing.T) {
	_, live := setupProfileBackupCommand(t)
	before, err := captureBackupTree(filepath.Dir(live))
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"claude", "saved", "--from-profile="},
		{"claude", "saved", "--from-profile", "   "},
		{"claude", "saved", "--from-profile", "missing"},
		{"claude", "saved", "--from-profile", "../escape"},
		{"claude", "../escape", "--from-profile", "isolated"},
		{"claude", "_original", "--from-profile", "isolated"},
		{"codex", "saved", "--from-profile", "isolated"},
	} {
		out, stderr, err := executeBackupSourceTest(t, append(args, "--json")...)
		var result profileBackupResult
		if err == nil || json.Unmarshal([]byte(out), &result) != nil || result.Success || result.Error == "" || stderr != "" || strings.Contains(result.Error, "unexpected") {
			t.Fatalf("unsafe failure dispatch for %v: %s; %s; %v", args, out, stderr, err)
		}
		assertBackupTreeUnchanged(t, filepath.Dir(live), before)
		v := authfile.NewVault(authfile.DefaultVaultPath())
		if _, err := os.Lstat(v.ProfilePath("claude", "saved")); !os.IsNotExist(err) {
			t.Fatalf("failed import published credentials: %v", err)
		}
	}
}

func TestBackupFromProfileCollisionPreservesDestination(t *testing.T) {
	_, live := setupProfileBackupCommand(t)
	if out, _, err := executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated"); err != nil {
		t.Fatalf("initial import: %v: %s", err, out)
	}
	v := authfile.NewVault(authfile.DefaultVaultPath())
	path := v.ProfilePath("claude", "saved")
	before, err := captureBackupTree(path)
	if err != nil {
		t.Fatal(err)
	}
	liveBefore, err := captureBackupTree(filepath.Dir(live))
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
	if !errors.Is(err, os.ErrExist) || stderr != "" || !strings.Contains(out, `"success":false`) {
		t.Fatalf("collision did not fail safely: %s; %s; %v", out, stderr, err)
	}
	assertBackupTreeUnchanged(t, path, before)
	assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
}

func TestBackupWithoutSourceKeepsNativeStartupAndReader(t *testing.T) {
	var calls []string
	root := &cobra.Command{Use: "caam", PersistentPreRunE: func(*cobra.Command, []string) error {
		calls = append(calls, "startup")
		return nil
	}}
	cmd := &cobra.Command{Use: "backup", RunE: func(*cobra.Command, []string) error {
		calls = append(calls, "native")
		return nil
	}}
	configureBackupSource(cmd)
	root.AddCommand(cmd)
	root.SetArgs([]string{"backup", "claude", "existing"})
	if err := root.Execute(); err != nil || !reflect.DeepEqual(calls, []string{"startup", "native"}) {
		t.Fatalf("native backup changed: calls=%v, error=%v", calls, err)
	}
	if backupCmd.Flags().Lookup("from-profile") == nil {
		t.Fatal("--from-profile is not registered on the real backup command")
	}
}

func TestBackupFromProfileRejectsNonOAuthSelectionBeforeCredentials(t *testing.T) {
	for _, mode := range []string{"api-key", "vertex-adc", "", "unknown"} {
		t.Run("mode="+mode, func(t *testing.T) {
			config, live := setupProfileBackupCommand(t)
			root := filepath.Dir(filepath.Dir(config))
			path := filepath.Join(root, "profile.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]any
			if err := json.Unmarshal(data, &metadata); err != nil {
				t.Fatal(err)
			}
			metadata["auth_mode"] = mode
			data, err = json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			before, err := captureBackupTree(root)
			if err != nil {
				t.Fatal(err)
			}
			liveBefore, err := captureBackupTree(filepath.Dir(live))
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err := executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
			if err == nil || stderr != "" || !strings.Contains(out, "must select OAuth") {
				t.Fatalf("non-OAuth source exported cached OAuth: %s; %s; %v", out, stderr, err)
			}
			assertBackupTreeUnchanged(t, root, before)
			assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
			v := authfile.NewVault(authfile.DefaultVaultPath())
			if _, err := os.Lstat(v.ProfilePath("claude", "saved")); !os.IsNotExist(err) {
				t.Fatalf("wrong selected auth published: %v", err)
			}
			// Make the credential invalid too. Mode selection must still be
			// the first failure, rather than reading/parsing the cached grant.
			if err := os.WriteFile(filepath.Join(config, ".credentials.json"), []byte("not JSON"), 0600); err != nil {
				t.Fatal(err)
			}
			out, _, err = executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
			if err == nil || !strings.Contains(out, "must select OAuth") {
				t.Fatalf("credential validation preceded selection: %s: %v", out, err)
			}
		})
	}
}

func TestIsolatedBackupCommandsRejectSettingsCredentialOverrides(t *testing.T) {
	for _, tc := range []struct {
		name     string
		document string
		settings map[string]any
	}{
		{"helper", "settings.json", map[string]any{"apiKeyHelper": "never-run-this-helper"}},
		{"api-key", "settings.json", map[string]any{"apiKey": "must-not-print-secret"}},
		{"environment-token", "settings.json", map[string]any{"env": map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "must-not-print-secret"}}},
		{"environment-key-case", "settings.json", map[string]any{"env": map[string]string{"anthropic_api_key": "must-not-print-secret"}}},
		{"provider", "settings.json", map[string]any{"env": map[string]string{"CLAUDE_CODE_USE_VERTEX": "1"}}},
		{"profile", "settings.json", map[string]any{"env": map[string]string{"ANTHROPIC_PROFILE": "another-account"}}},
		{"endpoint", "settings.json", map[string]any{"env": map[string]string{"ANTHROPIC_BASE_URL": "https://gateway.invalid"}}},
		{"redirect", "settings.json", map[string]any{"env": map[string]string{"CLAUDE_CONFIG_DIR": "/different/login"}}},
		{"login-method", "settings.json", map[string]any{"forceLoginMethod": "console"}},
		{"malformed-selector", "settings.json", map[string]any{"apiKeyHelper": []string{"must-not-print-secret"}}},
		{"legacy-key", ".claude.json", map[string]any{"primaryApiKey": "must-not-print-secret"}},
		{"legacy-token", ".claude.json", map[string]any{"oauthToken": "must-not-print-secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, live := setupProfileBackupCommand(t)
			path := filepath.Join(config, tc.document)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var object map[string]any
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			for key, value := range tc.settings {
				object[key] = value
			}
			data, err = json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			sourceRoot := filepath.Dir(filepath.Dir(config))
			sourceBefore, err := captureBackupTree(sourceRoot)
			if err != nil {
				t.Fatal(err)
			}
			liveBefore, err := captureBackupTree(filepath.Dir(live))
			if err != nil {
				t.Fatal(err)
			}
			for _, legacyCommand := range []bool{false, true} {
				var out, stderr string
				if legacyCommand {
					out, stderr, err = executeProfileBackupTest(t, "claude", "isolated", "--as", "saved", "--json")
				} else {
					out, stderr, err = executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
				}
				var result profileBackupResult
				if !errors.Is(err, authfile.ErrInvalidCredentials) || json.Unmarshal([]byte(out), &result) != nil || result.Success || result.Error == "" || stderr != "" {
					t.Fatalf("override not refused as a structured failure: stdout=%s; stderr=%s; error=%v", out, stderr, err)
				}
				if strings.Contains(out+stderr+err.Error(), "must-not-print-secret") {
					t.Fatal("credential leaked into command diagnostics")
				}
				assertBackupTreeUnchanged(t, sourceRoot, sourceBefore)
				assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
				// Selection failure must occur before creating even a private
				// vault transaction, not merely before publishing the final name.
				if _, err := os.Lstat(authfile.DefaultVaultPath()); !os.IsNotExist(err) {
					t.Fatalf("rejected source reached vault preparation: %v", err)
				}
			}
		})
	}
}

func TestBackupFromProfilePreservesNonAuthSettingsVerbatim(t *testing.T) {
	config, live := setupProfileBackupCommand(t)
	data := []byte(`{"model":"opus","permissions":{"allow":["Read"]},"forceLoginMethod":"claudeai","env":{"OPENAI_API_KEY":"unrelated-provider","CLAUDE_CODE_USE_BEDROCK":"off"}}`)
	if err := os.WriteFile(filepath.Join(config, "settings.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	before, err := captureBackupTree(filepath.Dir(live))
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
	if err != nil {
		t.Fatalf("ordinary settings rejected: stdout=%s; stderr=%s; error=%v", out, stderr, err)
	}
	v := authfile.NewVault(authfile.DefaultVaultPath())
	got, err := os.ReadFile(v.BackupPath("claude", "saved", "settings.json"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("ordinary settings were stripped or changed: %v", err)
	}
	assertBackupTreeUnchanged(t, filepath.Dir(live), before)
}
