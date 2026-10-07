package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/spf13/cobra"
)

// =============================================================================
// cleanup.go Tests
// =============================================================================

func TestCleanupCommand(t *testing.T) {
	if cleanupCmd.Use != "cleanup" {
		t.Errorf("Expected Use 'cleanup', got %q", cleanupCmd.Use)
	}

	if cleanupCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if cleanupCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestCleanupCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"dry-run", "false"},
		{"days", "0"},
		{"quiet", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := cleanupCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Expected flag --%s", tt.name)
				return
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("Expected default %q, got %q", tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestDBStatsCommand(t *testing.T) {
	if dbStatsCmd.Use != "db" {
		t.Errorf("Expected Use 'db', got %q", dbStatsCmd.Use)
	}

	if dbStatsCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}
}

func TestDBStatsShowCommand(t *testing.T) {
	if dbStatsShowCmd.Use != "stats" {
		t.Errorf("Expected Use 'stats', got %q", dbStatsShowCmd.Use)
	}

	if dbStatsShowCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if dbStatsShowCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
		{1099511627776, "1.0 TB"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := formatBytes(tt.bytes)
			if got != tt.want {
				t.Errorf("formatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

// =============================================================================
// defaults.go Tests
// =============================================================================

func TestUseCommand(t *testing.T) {
	if useCmd.Use != "use <provider> <profile>" {
		t.Errorf("Expected Use 'use <provider> <profile>', got %q", useCmd.Use)
	}

	if useCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if useCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestUseCommandArgs(t *testing.T) {
	// useCmd requires exactly 2 args
	err := useCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = useCmd.Args(nil, []string{"codex"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}

	err = useCmd.Args(nil, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = useCmd.Args(nil, []string{"codex", "work", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}
}

func TestWhichCommand(t *testing.T) {
	if whichCmd.Use != "which [provider]" {
		t.Errorf("Expected Use 'which [provider]', got %q", whichCmd.Use)
	}

	if whichCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if whichCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestWhichCommandArgs(t *testing.T) {
	// whichCmd allows 0 or 1 args
	err := whichCmd.Args(nil, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args, got %v", err)
	}

	err = whichCmd.Args(nil, []string{"codex"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = whichCmd.Args(nil, []string{"codex", "extra"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

// =============================================================================
// env.go Tests
// =============================================================================

func TestEnvCmd_Structure(t *testing.T) {
	if envCmd.Use != "env <tool> <profile>" {
		t.Errorf("Expected Use 'env <tool> <profile>', got %q", envCmd.Use)
	}

	if envCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if envCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestEnvCmd_Flags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"unset", "false"},
		{"export-prefix", "export"},
		{"fish", "false"},
		{"json", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := envCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Expected flag --%s", tt.name)
				return
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("Expected default %q, got %q", tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestEnvCmd_Args(t *testing.T) {
	// envCmd requires exactly 2 args
	err := envCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = envCmd.Args(nil, []string{"codex"})
	if err == nil {
		t.Error("Expected error for 1 arg")
	}

	err = envCmd.Args(nil, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = envCmd.Args(nil, []string{"codex", "work", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}
}

func TestEnvCmdShellPathsRoundTrip(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX shell paths contain characters Windows forbids")
	}
	base := filepath.Join(t.TempDir(), "literal $HOME $(printf substituted) `printf substituted` 'quote' \"double\" \\backslash\nnext")
	prof := setupEnvCommandProfile(t, base)
	for _, shell := range []string{"sh", "fish"} {
		t.Run(shell, func(t *testing.T) {
			bin, err := exec.LookPath(shell)
			if err != nil {
				t.Skipf("%s unavailable: %v", shell, err)
			}
			cmd, out := envOutputCommand(t, shell == "fish", false, false)
			if err := envCmd.RunE(cmd, []string{"codex", prof.Name}); err != nil {
				t.Fatal(err)
			}
			script := out.String() + "\nprintf '%s\\000' \"$HOME\" \"$CODEX_HOME\"\n"
			got, err := exec.Command(bin, "-c", script).Output()
			if err != nil {
				t.Fatalf("evaluate exports: %v", err)
			}
			want := prof.HomePath() + "\x00" + prof.CodexHomePath() + "\x00"
			if string(got) != want {
				t.Fatalf("exported paths did not round-trip: got %q, want %q", got, want)
			}
		})
	}
}

func TestEnvCmdJSONIsDataOnlyAndReadOnly(t *testing.T) {
	prof := setupEnvCommandProfile(t, t.TempDir())
	snapshot := func() map[string]string {
		files := make(map[string]string)
		if err := filepath.WalkDir(prof.BasePath, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			files[path] = ""
			if !entry.IsDir() {
				data, err := os.ReadFile(path)
				if err != nil {
					return err
				}
				files[path] = string(data)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return files
	}
	beforeFiles := snapshot()
	before, err := os.ReadFile(prof.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	beforeInfo, err := os.Stat(prof.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	wantSet, err := codex.New().Env(context.Background(), prof)
	if err != nil {
		t.Fatal(err)
	}
	wantUnset := []string{"CODEX_API_KEY", "OPENAI_API_KEY", "OPENAI_BASE_URL"}
	wantReset := []string{"CODEX_HOME", "HOME"}
	if runtime.GOOS == "windows" {
		wantSet["USERPROFILE"] = prof.HomePath()
		wantUnset = []string{"CODEX_API_KEY", "HOMEDRIVE", "HOMEPATH", "OPENAI_API_KEY", "OPENAI_BASE_URL"}
		wantReset = append(wantReset, "USERPROFILE")
	}
	for _, unset := range []bool{false, true, false} {
		cmd, out := envOutputCommand(t, false, true, unset)
		if err := envCmd.RunE(cmd, []string{"codex", prof.Name}); err != nil {
			t.Fatal(err)
		}
		var result struct {
			Set   map[string]string `json:"set"`
			Unset []string          `json:"unset"`
		}
		if err := json.Unmarshal(out.Bytes(), &result); err != nil {
			t.Fatalf("output contains non-JSON text: %s, %v", out, err)
		}
		if unset {
			if len(result.Set) != 0 || !reflect.DeepEqual(result.Unset, wantReset) {
				t.Fatalf("unexpected JSON unset operation: %#v", result)
			}
		} else if !reflect.DeepEqual(result.Set, wantSet) || !reflect.DeepEqual(result.Unset, wantUnset) {
			t.Fatalf("unexpected JSON exports: %#v", result)
		}
	}
	after, err := os.ReadFile(prof.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Stat(prof.MetaPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) || !beforeInfo.ModTime().Equal(afterInfo.ModTime()) {
		t.Fatal("printing environment rewrote the profile")
	}
	if !reflect.DeepEqual(beforeFiles, snapshot()) {
		t.Fatal("printing environment changed profile contents")
	}
	if _, err := os.Stat(prof.LockPath()); !os.IsNotExist(err) {
		t.Fatalf("printing environment created a profile lock: %v", err)
	}
}

func TestEnvCmdClearsInheritedCredentialOverrides(t *testing.T) {
	for _, tc := range []struct {
		tool string
		keys []string
	}{
		{"codex", []string{"OPENAI_API_KEY", "CODEX_API_KEY", "OPENAI_BASE_URL"}},
		{"claude", []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK"}},
	} {
		t.Run(tc.tool, func(t *testing.T) {
			setupEnvCommandProfile(t, t.TempDir())
			registry.Register(claude.New())
			prof, err := profileStore.Create(tc.tool, "selected", "oauth")
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.keys {
				t.Setenv(key, "synthetic-parent-credential")
			}
			for _, fish := range []bool{false, true} {
				cmd, out := envOutputCommand(t, fish, false, false)
				if err := envCmd.RunE(cmd, []string{tc.tool, prof.Name}); err != nil {
					t.Fatal(err)
				}
				for _, key := range tc.keys {
					unsetLine := "unset " + key + "\n"
					if fish {
						unsetLine = "set -e " + key + "\n"
					}
					if !strings.Contains(out.String(), unsetLine) {
						t.Errorf("missing credential removal for %s in shell plan", key)
					}
				}
				if strings.Contains(out.String(), "synthetic-parent-credential") {
					t.Fatal("shell plan disclosed an inherited credential")
				}
				if !fish && runtime.GOOS != "windows" {
					// Evaluate the real emitted program, including repeated selection.
					// Successful export must remove keys rather than merely blank them.
					check := out.String() + out.String()
					for _, key := range tc.keys {
						check += "test -z \"${" + key + "+present}\" || exit 1\n"
					}
					child := exec.Command("sh", "-e", "-c", check)
					if output, err := child.CombinedOutput(); err != nil {
						t.Fatalf("credential overrides survived shell evaluation: %v, %s", err, output)
					}
				}
			}
			cmd, out := envOutputCommand(t, false, true, false)
			if err := envCmd.RunE(cmd, []string{tc.tool, prof.Name}); err != nil {
				t.Fatal(err)
			}
			var changes provider.EnvironmentChanges
			if err := json.Unmarshal(out.Bytes(), &changes); err != nil {
				t.Fatal(err)
			}
			for _, key := range tc.keys {
				if !slices.Contains(changes.Unset, key) {
					t.Errorf("JSON plan omitted removal of %s", key)
				}
				if os.Getenv(key) != "synthetic-parent-credential" {
					t.Error("printing a plan modified the caller's environment")
				}
			}
		})
	}
}

func TestEnvCmdAPIKeyModeKeepsDocumentedKeyInput(t *testing.T) {
	prof := setupEnvCommandProfile(t, t.TempDir())
	prof.AuthMode = "api-key"
	if err := prof.Save(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENAI_API_KEY", "synthetic-explicit-key")
	cmd, out := envOutputCommand(t, false, true, false)
	if err := envCmd.RunE(cmd, []string{"codex", prof.Name}); err != nil {
		t.Fatal(err)
	}
	var changes provider.EnvironmentChanges
	if err := json.Unmarshal(out.Bytes(), &changes); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(changes.Unset, "OPENAI_API_KEY") {
		t.Fatal("API-key mode removed its documented ambient key input")
	}
	if _, copied := changes.Set["OPENAI_API_KEY"]; copied || strings.Contains(out.String(), "synthetic-explicit-key") {
		t.Fatal("environment export should not copy or expose the ambient key")
	}
}

func TestEnvCmdErrorOutputByFormat(t *testing.T) {
	setupEnvCommandProfile(t, t.TempDir())
	for _, jsonOutput := range []bool{false, true} {
		cmd, out := envOutputCommand(t, false, jsonOutput, false)
		if err := envCmd.RunE(cmd, []string{"codex", "missing"}); err == nil {
			t.Fatal("missing profile unexpectedly succeeded")
		}
		if jsonOutput && out.Len() != 0 {
			t.Fatalf("JSON failure emitted shell text: %s", out)
		}
		if !jsonOutput && !strings.HasPrefix(out.String(), "false ") {
			t.Fatalf("shell failure did not make eval fail: %s", out)
		}
	}
}

func TestEnvCommandDoesNotMigrateLegacyData(t *testing.T) {
	legacyData, caamHome := t.TempDir(), t.TempDir()
	t.Setenv("XDG_DATA_HOME", legacyData)
	t.Setenv("CAAM_HOME", caamHome)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	legacyPath := filepath.Join(legacyData, "caam", "migration-marker.txt")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("keep in legacy store"), 0600); err != nil {
		t.Fatal(err)
	}
	prof := setupEnvCommandProfile(t, profile.DefaultStorePath())
	oldVault, oldProject, oldHealth, oldConfig, oldRunner := vault, projectStore, healthStore, cfg, runner
	t.Cleanup(func() {
		vault, projectStore, healthStore, cfg, runner = oldVault, oldProject, oldHealth, oldConfig, oldRunner
	})
	for _, jsonOutput := range []bool{false, true} {
		cmd, out := envOutputCommand(t, false, jsonOutput, false)
		cmd.Use = envCmd.Use
		cmd.Args = envCmd.Args
		cmd.RunE = envCmd.RunE
		root := &cobra.Command{Use: "caam", PersistentPreRunE: rootCmd.PersistentPreRunE}
		root.AddCommand(cmd)
		root.SetArgs([]string{"env", "codex", prof.Name})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		if out.Len() == 0 {
			t.Fatal("command emitted no environment")
		}
		if shouldShowWarnings(cmd) {
			t.Fatal("environment export would inspect active credentials")
		}
		if _, err := os.Stat(filepath.Join(config.DefaultDataPath(), "migration-marker.txt")); !os.IsNotExist(err) {
			t.Fatalf("environment command migrated legacy data: %v", err)
		}
	}
	if got, err := os.ReadFile(legacyPath); err != nil || string(got) != "keep in legacy store" {
		t.Fatalf("legacy source changed: %q, %v", got, err)
	}
}

func TestEnvCommandPreflightFailureOutput(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CAAM_HOME", t.TempDir())
	setupEnvCommandProfile(t, profile.DefaultStorePath())
	oldVault, oldProject, oldHealth, oldConfig, oldRunner := vault, projectStore, healthStore, cfg, runner
	t.Cleanup(func() {
		vault, projectStore, healthStore, cfg, runner = oldVault, oldProject, oldHealth, oldConfig, oldRunner
	})
	path := config.ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"claude_settings":{"mode":"invalid"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for name, args := range map[string][]string{
		"arguments": {"env", "codex"},
		"config":    {"env", "codex", "exports"},
	} {
		t.Run(name, func(t *testing.T) {
			for _, jsonOutput := range []bool{false, true} {
				cmd, out := envOutputCommand(t, false, jsonOutput, false)
				cmd.Use, cmd.Args, cmd.RunE = envCmd.Use, envCmd.Args, envCmd.RunE
				root := &cobra.Command{Use: "caam", PersistentPreRunE: rootCmd.PersistentPreRunE}
				root.AddCommand(cmd)
				root.SetOut(out)
				root.SetErr(&bytes.Buffer{})
				root.SetArgs(args)
				if err := root.Execute(); err == nil {
					t.Fatal("preflight unexpectedly succeeded")
				}
				if jsonOutput && out.Len() != 0 {
					t.Fatalf("JSON preflight failure emitted shell text: %s", out)
				}
				if !jsonOutput && !strings.HasPrefix(out.String(), "false ") {
					t.Fatalf("shell preflight failure did not make eval fail: %s", out)
				}
			}
		})
	}
}

func setupEnvCommandProfile(t *testing.T, base string) *profile.Profile {
	t.Helper()
	oldStore, oldRegistry := profileStore, registry
	t.Cleanup(func() { profileStore, registry = oldStore, oldRegistry })
	profileStore = profile.NewStore(base)
	registry = provider.NewRegistry()
	registry.Register(codex.New())
	prof, err := profileStore.Create("codex", "exports", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	return prof
}

func envOutputCommand(t *testing.T, fish, jsonOutput, unset bool) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.Flags().Bool("fish", fish, "")
	cmd.Flags().Bool("json", jsonOutput, "")
	cmd.Flags().Bool("unset", unset, "")
	cmd.Flags().String("export-prefix", "export", "")
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	return cmd, out
}
