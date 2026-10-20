package cmd

import (
	"bytes"
	"context"
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
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
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

// A legacy-only profile is still a supported caam exec source. Import must use
// the same namespace, not require the user to copy/migrate authentication files.
func TestLegacyClaudeBackupUsesExecutionSourceAndActivates(t *testing.T) {
	for _, command := range []string{"backup", "profile-backup"} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/nested=%v", command, nested), func(t *testing.T) {
				xdg, live := setupProfileBackupCommand(t)
				t.Setenv("CAAM_KEYCHAIN", "0")
				root := filepath.Dir(filepath.Dir(xdg))
				legacy := filepath.Join(root, "home", ".claude")
				if err := os.MkdirAll(legacy, 0700); err != nil {
					t.Fatal(err)
				}
				var paths []string
				for _, name := range []string{".credentials.json", ".claude.json", "settings.json"} {
					target := filepath.Join(legacy, name)
					if name == ".claude.json" && !nested {
						target = filepath.Join(root, "home", name)
					}
					if err := os.Rename(filepath.Join(xdg, name), target); err != nil {
						t.Fatal(err)
					}
					paths = append(paths, target)
				}
				// Benign XDG policy cannot shadow the legacy login or leak into
				// its vault snapshot. Account state alone does not select XDG.
				for name, data := range map[string]string{
					"settings.json": `{"model":"different-xdg-model"}`,
					".claude.json":  `{"oauthAccount":{"accountUuid":"ignored-xdg"}}`,
				} {
					if err := os.WriteFile(filepath.Join(xdg, name), []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				prof, err := profile.NewStore(profile.DefaultStorePath()).Load("claude", "isolated")
				if err != nil {
					t.Fatal(err)
				}
				env, err := claude.New().Env(context.Background(), prof)
				if err != nil || env["CLAUDE_CONFIG_DIR"] != legacy {
					t.Fatalf("test does not represent a legacy execution source: %v", err)
				}
				sourceBefore, err := captureBackupTree(root)
				if err != nil {
					t.Fatal(err)
				}
				liveBefore, err := captureBackupTree(filepath.Dir(live))
				if err != nil {
					t.Fatal(err)
				}
				// Track home-level native state separately from ~/.claude.
				nativeState := filepath.Join(os.Getenv("HOME"), ".claude.json")
				if err := os.WriteFile(nativeState, []byte(`{"running":"native-account"}`), 0600); err != nil {
					t.Fatal(err)
				}
				stateBefore, err := captureBackupTree(nativeState)
				if err != nil {
					t.Fatal(err)
				}
				var out, stderr string
				if command == "backup" {
					out, stderr, err = executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
				} else {
					out, stderr, err = executeProfileBackupTest(t, "claude", "isolated", "--as", "saved", "--json")
				}
				if err != nil || stderr != "" {
					t.Fatalf("legacy import: %v; stdout=%s; stderr=%s", err, out, stderr)
				}
				var result profileBackupResult
				if err := json.Unmarshal([]byte(out), &result); err != nil || !result.Success {
					t.Fatalf("invalid result: %v: %s", err, out)
				}
				assertBackupTreeUnchanged(t, root, sourceBefore)
				assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
				assertBackupTreeUnchanged(t, nativeState, stateBefore)
				for _, path := range paths {
					want, err := os.ReadFile(path)
					if err != nil {
						t.Fatal(err)
					}
					got, err := os.ReadFile(filepath.Join(result.Path, filepath.Base(path)))
					if err != nil || !bytes.Equal(want, got) {
						t.Fatalf("source state not preserved: %s: %v", path, err)
					}
				}
				data, err := os.ReadFile(filepath.Join(result.Path, "meta.json"))
				if err != nil {
					t.Fatal(err)
				}
				var metadata struct {
					ConfigDir     string   `json:"source_config_dir"`
					OriginalPaths []string `json:"original_paths"`
					Identity      string   `json:"identity"`
				}
				if err := json.Unmarshal(data, &metadata); err != nil || metadata.ConfigDir != env["CLAUDE_CONFIG_DIR"] || !reflect.DeepEqual(paths, metadata.OriginalPaths) || !strings.Contains(metadata.Identity, "new@example.test") {
					t.Fatalf("wrong source metadata: %+v: %v", metadata, err)
				}
				v := authfile.NewVault(authfile.DefaultVaultPath())
				// Activation deliberately targets another synthetic HOME, so
				// the original running account never changes even in the test.
				home := t.TempDir()
				t.Setenv("HOME", home)
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
				if _, err := v.Switch(authfile.ClaudeAuthFiles(), "saved", authfile.SwitchOptions{BackupMode: "always"}); err != nil {
					t.Fatalf("legacy vault snapshot cannot activate: %v", err)
				}
				got, err := os.ReadFile(filepath.Join(home, ".claude", ".credentials.json"))
				if err != nil || !bytes.Contains(got, []byte("new-access")) {
					t.Fatalf("wrong activated credential: %v", err)
				}
				got, err = os.ReadFile(filepath.Join(home, ".claude.json"))
				if err != nil || !bytes.Contains(got, []byte("new@example.test")) || bytes.Contains(got, []byte("ignored-xdg")) {
					t.Fatalf("wrong activated identity: %v", err)
				}
				assertBackupTreeUnchanged(t, root, sourceBefore)
				assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
				assertBackupTreeUnchanged(t, nativeState, stateBefore)
			})
		}
	}
}

func TestClaudeBackupRejectsLayoutConflictLikeExecution(t *testing.T) {
	for _, command := range []string{"backup", "profile-backup"} {
		for _, evidence := range []string{"credential", "home-state", "helper"} {
			t.Run(command+"/"+evidence, func(t *testing.T) {
				xdg, live := setupProfileBackupCommand(t)
				root := filepath.Dir(filepath.Dir(xdg))
				legacy := filepath.Join(root, "home", ".claude")
				if err := os.MkdirAll(legacy, 0700); err != nil {
					t.Fatal(err)
				}
				files := map[string]string{
					filepath.Join(root, "home", ".claude.json"): `{"oauthAccount":{"accountUuid":"unprinted-other-account"}}`,
				}
				switch evidence {
				case "credential":
					files[filepath.Join(legacy, ".credentials.json")] = `{"claudeAiOauth":{"accessToken":"unprinted-access","refreshToken":"unprinted-refresh","accountId":"unprinted-other-account"}}`
				case "home-state":
					files[filepath.Join(legacy, "auth.json")] = `{"accessToken":"unprinted-access"}`
				case "helper":
					files[filepath.Join(legacy, "settings.json")] = `{"apiKeyHelper":"unprinted-never-executed-helper"}`
				}
				for path, data := range files {
					if err := os.WriteFile(path, []byte(data), 0600); err != nil {
						t.Fatal(err)
					}
				}
				prof, err := profile.NewStore(profile.DefaultStorePath()).Load("claude", "isolated")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := claude.New().Env(context.Background(), prof); err == nil || !strings.Contains(err.Error(), "conflicting accounts") {
					t.Fatalf("fixture must reproduce the provider's account conflict: %v", err)
				}
				sourceBefore, err := captureBackupTree(root)
				if err != nil {
					t.Fatal(err)
				}
				liveBefore, err := captureBackupTree(filepath.Dir(live))
				if err != nil {
					t.Fatal(err)
				}
				var out, stderr string
				if command == "backup" {
					out, stderr, err = executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
				} else {
					out, stderr, err = executeProfileBackupTest(t, "claude", "isolated", "--as", "saved", "--json")
				}
				var result profileBackupResult
				if err == nil || json.Unmarshal([]byte(out), &result) != nil || result.Success || !strings.Contains(result.Error, "conflicting accounts") || stderr != "" {
					t.Fatalf("conflicting profile was not rejected cleanly: %v; stdout=%s; stderr=%s", err, out, stderr)
				}
				if strings.Contains(out, "unprinted") {
					t.Fatal("conflict diagnostic exposed account or credential data")
				}
				assertBackupTreeUnchanged(t, root, sourceBefore)
				assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
				if _, err := os.Lstat(authfile.DefaultVaultPath()); !os.IsNotExist(err) {
					t.Fatalf("conflict reached vault preparation: %v", err)
				}
			})
		}
	}
}

func TestClaudeBackupKeepsOnlyXDGWhenLegacyIsNotConflicting(t *testing.T) {
	for _, sameAccount := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy-login=%v", sameAccount), func(t *testing.T) {
			xdg, live := setupProfileBackupCommand(t)
			root := filepath.Dir(filepath.Dir(xdg))
			legacy := filepath.Join(root, "home", ".claude")
			if err := os.MkdirAll(legacy, 0700); err != nil {
				t.Fatal(err)
			}
			state := `{"oauthAccount":{"accountUuid":"stale-identity-without-login"}}`
			if sameAccount {
				state = `{"oauthAccount":{"accountUuid":"new-id"}}`
				if err := os.WriteFile(filepath.Join(legacy, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"legacy-not-exported","refreshToken":"legacy-refresh"}}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "home", ".claude.json"), []byte(state), 0600); err != nil {
				t.Fatal(err)
			}
			prof, err := profile.NewStore(profile.DefaultStorePath()).Load("claude", "isolated")
			if err != nil {
				t.Fatal(err)
			}
			env, err := claude.New().Env(context.Background(), prof)
			if err != nil || env["CLAUDE_CONFIG_DIR"] != xdg {
				t.Fatalf("expected provider to select XDG: %v", err)
			}
			sourceBefore, err := captureBackupTree(root)
			if err != nil {
				t.Fatal(err)
			}
			liveBefore, err := captureBackupTree(filepath.Dir(live))
			if err != nil {
				t.Fatal(err)
			}
			out, stderr, err := executeBackupSourceTest(t, "claude", "saved", "--from-profile", "isolated", "--json")
			if err != nil || stderr != "" {
				t.Fatalf("non-conflicting legacy guard rejected: %v; %s; %s", err, out, stderr)
			}
			v := authfile.NewVault(authfile.DefaultVaultPath())
			for _, name := range []string{".credentials.json", ".claude.json", "settings.json"} {
				want, err := os.ReadFile(filepath.Join(xdg, name))
				if err != nil {
					t.Fatal(err)
				}
				got, err := os.ReadFile(v.BackupPath("claude", "saved", name))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatalf("legacy guard leaked into %s: %v", name, err)
				}
			}
			assertBackupTreeUnchanged(t, root, sourceBefore)
			assertBackupTreeUnchanged(t, filepath.Dir(live), liveBefore)
		})
	}
}
