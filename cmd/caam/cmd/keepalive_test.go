package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/keepalive"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
	"github.com/spf13/cobra"
)

type keepaliveCLIFixture struct {
	root, home, base string
	vault            *authfile.Vault
}

func newKeepaliveCLIFixture(t *testing.T) keepaliveCLIFixture {
	t.Helper()
	root := t.TempDir()
	f := keepaliveCLIFixture{root: root, home: filepath.Join(root, "home"), base: filepath.Join(root, "shallow")}
	for key, value := range map[string]string{
		"HOME": f.home, "CAAM_REAL_HOME": "", "CAAM_HOME": filepath.Join(root, "caam"),
		"CAAM_SHALLOW_HOMES_DIR": f.base, "CLAUDE_CONFIG_DIR": "", "GROK_HOME": "",
		"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"),
	} {
		t.Setenv(key, value)
	}
	f.vault = authfile.NewVault(authfile.DefaultVaultPath())
	return f
}

func keepaliveCLIAuth(provider, token string, expiry time.Time) string {
	if provider == "claude" {
		return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"accountId":"account-a","email":"alice@example.invalid"}}`, token, "refresh-"+token, expiry.UnixMilli())
	}
	return fmt.Sprintf(`{"key":%q,"refresh_token":%q,"expires_at":%q,"user_id":"account-a","email":"alice@example.invalid"}`, token, "refresh-"+token, expiry.UTC().Format(time.RFC3339))
}

func writeKeepaliveCLIFile(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0600); err != nil {
		t.Fatal(err)
	}
}

func installKeepaliveCLI(t *testing.T, provider, script string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake native CLI uses a POSIX shell")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, provider), []byte("#!/bin/sh\nset -eu\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func executeKeepaliveCLI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, stderr bytes.Buffer
	rootHookRan := false
	root := &cobra.Command{Use: "caam", PersistentPreRunE: func(*cobra.Command, []string) error {
		rootHookRan = true
		return errors.New("root migration hook must not run")
	}}
	root.AddCommand(newKeepaliveCmd())
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"keepalive", "--json"}, args...))
	err := root.Execute()
	if rootHookRan {
		t.Fatal("keepalive invoked the root migration/warnings hook")
	}
	if stderr.Len() != 0 {
		t.Fatalf("JSON command emitted a second error stream: %q", stderr.String())
	}
	return out.String(), err
}

func decodeKeepaliveCLI(t *testing.T, data string) keepaliveOutput {
	t.Helper()
	var output keepaliveOutput
	if err := json.Unmarshal([]byte(data), &output); err != nil {
		t.Fatalf("invalid JSON output: %v: %q", err, data)
	}
	return output
}

func snapshotKeepaliveCLIFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			state[path] = "directory"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		state[path] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestKeepaliveCLIRotatesLiveGrantAndUpdatesMatchingVault(t *testing.T) {
	f := newKeepaliveCLIFixture(t)
	auth := filepath.Join(f.home, ".grok", "auth.json")
	old := keepaliveCLIAuth("grok", "test-old-access", time.Now().Add(-time.Hour))
	fresh := keepaliveCLIAuth("grok", "test-new-access", time.Now().Add(6*time.Hour))
	writeKeepaliveCLIFile(t, auth, old)
	saved := filepath.Join(f.vault.ProfilePath("grok", "work"), "auth.json")
	writeKeepaliveCLIFile(t, saved, old)
	writeKeepaliveCLIFile(t, filepath.Join(f.vault.ProfilePath("grok", "work"), "meta.json"), `{"type":"user"}`)
	t.Setenv("CAAM_KEEPALIVE_TEST_NEXT", fresh)
	t.Setenv("GROK_AUTH", "test-ambient-secret")
	t.Setenv("XAI_API_KEY", "test-ambient-secret")
	installKeepaliveCLI(t, "grok", `test "$1" = models
test -z "${GROK_AUTH:-}${XAI_API_KEY:-}"
printf '%s' "$CAAM_KEEPALIVE_TEST_NEXT" > "$GROK_HOME/auth.json"
printf 'test-old-access test-new-access test-ambient-secret\n' >&2`)
	data, err := executeKeepaliveCLI(t, "host:grok")
	output := decodeKeepaliveCLI(t, data)
	if err != nil || !output.Success || len(output.Results) != 1 || output.Results[0].Status != "rotated" || !output.Results[0].Attempted {
		t.Fatalf("native renewal did not succeed: %+v, error %v", output, err)
	}
	if !reflect.DeepEqual(output.Results[0].SyncedProfiles, []string{"work"}) {
		t.Fatalf("matching snapshot was not synchronized: %+v", output.Results[0])
	}
	for _, path := range []string{auth, saved} {
		contents, err := os.ReadFile(path)
		if err != nil || string(contents) != fresh {
			t.Fatalf("new credential missing from %s (error %v)", path, err)
		}
	}
	for _, secret := range []string{"test-old-access", "test-new-access", "test-ambient-secret", "refresh-test-"} {
		if strings.Contains(data, secret) {
			t.Errorf("JSON output leaked %s", secret)
		}
	}
}

func TestKeepaliveCLIZeroExitWithRemovedCredentialFails(t *testing.T) {
	f := newKeepaliveCLIFixture(t)
	auth := filepath.Join(f.home, ".grok", "auth.json")
	writeKeepaliveCLIFile(t, auth, keepaliveCLIAuth("grok", "test-access", time.Now().Add(-time.Hour)))
	installKeepaliveCLI(t, "grok", `mv "$GROK_HOME/auth.json" "$GROK_HOME/rejected-auth.json"`)
	data, err := executeKeepaliveCLI(t, "host:grok")
	output := decodeKeepaliveCLI(t, data)
	if err == nil || output.Success || len(output.Results) != 1 || output.Results[0].Success || output.Results[0].Reason != "credential_missing" {
		t.Fatalf("zero native exit concealed removed credentials: %+v, error %v", output, err)
	}
}

func TestKeepaliveCLIReadOnlyModesBypassRootHooksAndDoNotWrite(t *testing.T) {
	for _, mode := range []string{"--dry-run", "--print-systemd"} {
		t.Run(mode, func(t *testing.T) {
			f := newKeepaliveCLIFixture(t)
			writeKeepaliveCLIFile(t, filepath.Join(f.home, ".grok", "auth.json"), keepaliveCLIAuth("grok", "test-access", time.Now().Add(-time.Hour)))
			marker := filepath.Join(f.root, "native-started")
			t.Setenv("CAAM_KEEPALIVE_TEST_MARKER", marker)
			t.Setenv("ANTHROPIC_API_KEY", "test-only-hidden-credential")
			t.Setenv("GROK_AUTH", "test-only-hidden-credential")
			installKeepaliveCLI(t, "grok", `printf called > "$CAAM_KEEPALIVE_TEST_MARKER"; exit 90`)
			before := snapshotKeepaliveCLIFiles(t, f.root)
			data, err := executeKeepaliveCLI(t, "host:grok", mode, "--ttl", "1h", "--min-gap", "0", "--timeout", "7s", "--base", f.base, "--state-dir", filepath.Join(f.root, "state"))
			if err != nil {
				t.Fatalf("read-only command failed: %v: %s", err, data)
			}
			if mode == "--dry-run" {
				out := decodeKeepaliveCLI(t, data)
				if !out.DryRun || len(out.Results) != 1 || out.Results[0].Status != "dry_run" || out.Results[0].Attempted {
					t.Fatalf("invalid dry-run result: %+v", out)
				}
			} else {
				var out keepaliveSystemdOutput
				if json.Unmarshal([]byte(data), &out) != nil || !out.Success || !strings.Contains(out.Timer, "OnCalendar=*:0/30\nPersistent=true") || !strings.Contains(out.Service, `"host:grok"`) ||
					!strings.Contains(out.Service, `"--ttl" "1h0m0s" "--min-gap" "0s" "--timeout" "7s"`) || !strings.Contains(out.Service, `"--base"`) || !strings.Contains(out.Service, `"--state-dir"`) {
					t.Fatalf("invalid systemd output: %s", data)
				}
			}
			if strings.Contains(data, "test-only-hidden-credential") {
				t.Fatal("read-only output serialized an ambient credential")
			}
			if !reflect.DeepEqual(before, snapshotKeepaliveCLIFiles(t, f.root)) {
				t.Fatal("read-only keepalive created or changed files")
			}
		})
	}
}

func TestKeepaliveCLIRefusalsIdentifyActualShallowOwner(t *testing.T) {
	f := newKeepaliveCLIFixture(t)
	credential := keepaliveCLIAuth("claude", "test-access", time.Now().Add(time.Hour))
	writeKeepaliveCLIFile(t, filepath.Join(f.home, ".claude", ".credentials.json"), credential)
	writeKeepaliveCLIFile(t, filepath.Join(f.base, "work", ".claude", ".credentials.json"), credential)
	writeKeepaliveCLIFile(t, filepath.Join(f.base, "work", shallow.ProfileMetaFilename), `{"provider":"claude","name":"work"}`)
	writeKeepaliveCLIFile(t, filepath.Join(f.vault.ProfilePath("claude", "saved-work"), ".credentials.json"), credential)
	for _, selector := range []string{"host:claude", "vault:claude/saved-work", "saved-work"} {
		data, err := executeKeepaliveCLI(t, selector)
		out := decodeKeepaliveCLI(t, data)
		if err == nil || out.Success || !strings.Contains(out.Error, "shallow:claude/work") || len(out.Results) != 0 {
			t.Fatalf("%s did not refuse with actual owner: %+v, error %v", selector, out, err)
		}
	}
	t.Setenv("CAAM_KEEPALIVE_TEST_SHALLOW", filepath.Join(f.base, "work"))
	installKeepaliveCLI(t, "claude", `test "$HOME" = "$CAAM_KEEPALIVE_TEST_SHALLOW"`)
	data, err := executeKeepaliveCLI(t, "claude")
	out := decodeKeepaliveCLI(t, data)
	if err != nil || !out.Success || len(out.Results) != 2 || out.Results[0].Attempted || out.Results[0].Status != "skipped" || !out.Results[1].Attempted {
		t.Fatalf("provider selection did not renew only the shallow owner: %+v, error %v", out, err)
	}
}

func TestKeepaliveProviderSelectorDoesNotMatchForeignProfileName(t *testing.T) {
	grants := []keepalive.Grant{
		{Provider: "claude", Kind: "shallow", Name: "work"},
		{Provider: "grok", Kind: "isolated", Name: "claude"},
	}
	selected, err := selectKeepaliveGrants(grants, []string{"claude"}, nil)
	if err != nil || len(selected) != 1 || selected[0].Provider != "claude" {
		t.Fatalf("provider selector included a foreign profile with the provider's name: %+v, error %v", selected, err)
	}
}

func TestKeepaliveCLISelectionCannotConcealCompetingOwners(t *testing.T) {
	f := newKeepaliveCLIFixture(t)
	for _, name := range []string{"work", "other"} {
		writeKeepaliveCLIFile(t, filepath.Join(f.base, name, ".claude", ".credentials.json"), keepaliveCLIAuth("claude", "test-"+name, time.Now().Add(time.Hour)))
		writeKeepaliveCLIFile(t, filepath.Join(f.base, name, shallow.ProfileMetaFilename), fmt.Sprintf(`{"provider":"claude","name":%q}`, name))
	}
	for _, selector := range []string{"claude", "shallow:claude/work", "work"} {
		data, err := executeKeepaliveCLI(t, selector)
		out := decodeKeepaliveCLI(t, data)
		if err == nil || out.Success || !strings.Contains(out.Error, "ambiguous live grant owners") || !strings.Contains(out.Error, "shallow:claude/other") {
			t.Fatalf("%s concealed competing owner: %+v, error %v", selector, out, err)
		}
		for _, result := range out.Results {
			if result.Success || result.Attempted {
				t.Fatalf("competing owner was executed: %+v", result)
			}
		}
	}
}

func TestKeepaliveCLIZeroMinGapIsHonored(t *testing.T) {
	f := newKeepaliveCLIFixture(t)
	writeKeepaliveCLIFile(t, filepath.Join(f.home, ".grok", "auth.json"), keepaliveCLIAuth("grok", "test-access", time.Now().Add(time.Hour)))
	installKeepaliveCLI(t, "grok", `exit 0`)
	for i, args := range [][]string{{"grok"}, {"grok"}, {"grok", "--min-gap", "0"}} {
		data, err := executeKeepaliveCLI(t, args...)
		out := decodeKeepaliveCLI(t, data)
		if err != nil || !out.Success || len(out.Results) != 1 || out.Results[0].Attempted != (i != 1) {
			t.Fatalf("min-gap call %d: %+v, error %v", i, out, err)
		}
	}
}

func TestKeepaliveSystemdEscapesArgumentsAndRejectsControls(t *testing.T) {
	for _, tc := range []struct {
		value string
		exec  bool
		want  string
	}{
		{`/home/a b/100%/$tool"\bin`, true, `"/home/a b/100%%/$$tool\"\\bin"`},
		{`HOME=/home/a b/100%/$user`, false, `"HOME=/home/a b/100%%/$user"`},
	} {
		got, err := quoteKeepaliveSystemd(tc.value, tc.exec)
		if err != nil || got != tc.want {
			t.Errorf("quote(%q, %v) = %q, %v; want %q", tc.value, tc.exec, got, err, tc.want)
		}
	}
	for _, value := range []string{"bad\nline", "bad\rline", "bad\tline", "bad\x00value", "bad\x7fvalue"} {
		if _, err := quoteKeepaliveSystemd(value, true); err == nil {
			t.Errorf("accepted unsafe systemd argument %q", value)
		}
	}
	args := []string{"/opt/caam tools/caam", "keepalive", "--json", "--ttl", "1h0m0s", "--min-gap", "0s", "host:grok"}
	service, timer, err := renderKeepaliveSystemd(args, map[string]string{"HOME": "/home/test", "GROK_HOME": "/home/test/.grok"})
	if err != nil || !strings.Contains(service, `ExecStart="/opt/caam tools/caam" "keepalive" "--json" "--ttl" "1h0m0s" "--min-gap" "0s" "host:grok"`) || !strings.Contains(timer, "OnCalendar=*:0/30\nPersistent=true") {
		t.Fatalf("systemd rendering lost command options or persistent calendar: %q, %q, %v", service, timer, err)
	}
	if _, _, err := renderKeepaliveSystemd([]string{"relative/caam"}, nil); err == nil {
		t.Fatal("accepted non-absolute service executable")
	}
	if _, _, err := renderKeepaliveSystemd([]string{"/usr/bin/caam", "keepalive", ";"}, nil); err == nil {
		t.Fatal("accepted systemd's command separator as a selector")
	}
}
