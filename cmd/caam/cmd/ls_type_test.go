package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/spf13/cobra"
)

// setupProfileTypes builds a codex vault with "both" and "vaultonly", and an
// isolated-profile store with "both" and "isoonly" (issue #110).
func setupProfileTypes(t *testing.T) {
	t.Helper()
	origVault, origTools, origStore := vault, tools, profileStore
	t.Cleanup(func() { vault, tools, profileStore = origVault, origTools, origStore })

	root := t.TempDir()
	vaultDir := filepath.Join(root, "vault")
	vault = authfile.NewVault(vaultDir)
	profileStore = profile.NewStore(filepath.Join(root, "profiles"))
	tools = map[string]func() authfile.AuthFileSet{
		"codex": func() authfile.AuthFileSet {
			return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
		},
	}
	for _, name := range []string{"both", "vaultonly"} {
		dir := filepath.Join(vaultDir, "codex", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"access_token":"a","refresh_token":"r"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"both", "isoonly"} {
		if _, err := profileStore.Create("codex", name, "oauth"); err != nil {
			t.Fatalf("create isolated profile %s: %v", name, err)
		}
	}
}

func runLsJSON(t *testing.T, args []string) map[string]any {
	t.Helper()
	cmd := &cobra.Command{RunE: runLs}
	cmd.Flags().Bool("no-color", false, "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().String("tag", "", "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runLs(cmd, args); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	return out
}

func TestLsJSONReportsProfileType(t *testing.T) {
	setupProfileTypes(t)

	for _, args := range [][]string{{"codex"}, nil} {
		out := runLsJSON(t, args)

		// The row set is unchanged: vault profiles only.
		if got := out["count"]; got != float64(2) {
			t.Fatalf("args %v: count = %v, want 2", args, got)
		}
		rows := map[string]map[string]any{}
		for _, r := range out["profiles"].([]any) {
			row := r.(map[string]any)
			rows[row["name"].(string)] = row
		}
		for name, wantIsolated := range map[string]bool{"both": true, "vaultonly": false} {
			row, ok := rows[name]
			if !ok {
				t.Fatalf("args %v: no row for %s in %v", args, name, out)
			}
			if row["type"] != "vault" || row["isolated"] != wantIsolated {
				t.Errorf("args %v: %s type=%v isolated=%v, want vault/%v", args, name, row["type"], row["isolated"], wantIsolated)
			}
		}

		only, _ := out["isolated_only"].([]any)
		if len(only) != 1 {
			t.Fatalf("args %v: isolated_only = %v, want just isoonly", args, out["isolated_only"])
		}
		entry := only[0].(map[string]any)
		if entry["tool"] != "codex" || entry["name"] != "isoonly" || entry["type"] != "isolated" {
			t.Errorf("args %v: isolated_only entry = %v", args, entry)
		}
	}
}

func TestLsJSONOmitsIsolatedOnlyWhenNone(t *testing.T) {
	setupProfileTypes(t)
	profileStore = profile.NewStore(filepath.Join(t.TempDir(), "empty"))
	out := runLsJSON(t, []string{"codex"})
	if _, present := out["isolated_only"]; present {
		t.Errorf("isolated_only present with no isolated profiles: %v", out)
	}
}

func TestIsolatedProfileLoadErrorExplainsVaultProfiles(t *testing.T) {
	setupProfileTypes(t)

	_, err := profileStore.Load("codex", "vaultonly")
	if err == nil {
		t.Fatal("expected vaultonly to be missing from the isolated store")
	}
	msg := isolatedProfileLoadError("codex", "vaultonly", err).Error()
	for _, want := range []string{
		"codex/vaultonly is a vault profile, not an isolated profile",
		"caam activate codex vaultonly",
		"caam profile add codex vaultonly, then caam login codex vaultonly",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}

	_, err = profileStore.Load("codex", "nosuch")
	msg = isolatedProfileLoadError("codex", "nosuch", err).Error()
	if !strings.Contains(msg, "profile codex/nosuch not found") || !strings.Contains(msg, "caam profile ls codex") {
		t.Errorf("unknown profile message = %q", msg)
	}
}

func TestIsolatedProfileLoadErrorClaudeHint(t *testing.T) {
	setupProfileTypes(t)
	dir := filepath.Join(t.TempDir(), "vault")
	vault = authfile.NewVault(dir)
	if err := os.MkdirAll(filepath.Join(dir, "claude", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := profileStore.Load("claude", "work")
	msg := isolatedProfileLoadError("claude", "work", err).Error()
	if strings.Contains(msg, "caam login claude") || !strings.Contains(msg, "caam exec claude work, then /login") {
		t.Errorf("claude hint must not suggest caam login: %s", msg)
	}
}

func TestExecAndLoginReportVaultProfile(t *testing.T) {
	setupProfileTypes(t)
	origRegistry := registry
	t.Cleanup(func() { registry = origRegistry })
	registry = provider.NewRegistry()
	registry.Register(codex.New())

	for _, c := range []*cobra.Command{execCmd, loginCmd} {
		err := c.RunE(c, []string{"codex", "vaultonly"})
		if err == nil || !strings.Contains(err.Error(), "is a vault profile") {
			t.Errorf("%s error = %v, want the vault-profile explanation", c.Name(), err)
		}
	}
}
