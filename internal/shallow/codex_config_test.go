package shallow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Issue #103: a shallow codex profile's config.toml is copied from the real
// ~/.codex/config.toml once, at creation, and never reconciled. When a
// real-home MCP entry moved from the stdio transport to streamable HTTP, every
// shallow profile kept the old command/args block and codex then refused to
// parse its config at all:
//
//	url is not supported for stdio in mcp_servers.kernel
//
// The regression test the report asks for is TestSyncCodexConfigReplacesMCPTransport
// below: HTTP-only transport after the sync, byte-identical auth.json,
// classified profile-local state preserved, and a second sync a no-op.

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// codexFixture builds a manager over a temp real HOME and shallow base, plus a
// codex shallow profile called "cx".
func codexFixture(t *testing.T, realConfig string) (*Manager, string, string) {
	t.Helper()
	root := t.TempDir()
	realHome := filepath.Join(root, "home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if realConfig != "" {
		writeFile(t, filepath.Join(realHome, ".codex", "config.toml"), realConfig)
	}
	mgr, err := NewManager(filepath.Join(root, "orch-homes"), realHome)
	if err != nil {
		t.Fatal(err)
	}
	home, err := mgr.Create("cx", CreateOptions{Provider: "codex"})
	if err != nil {
		t.Fatalf("create codex shallow profile: %v", err)
	}
	return mgr, home, realHome
}

const realConfigStdio = `# Top-level settings
model = "gpt-5.2-codex"
model_reasoning_effort = "high"

# Notify hook
notify = ["/usr/local/bin/notify"]

[skills]
max_context_tokens = 40000

[mcp_servers.kernel]
command = "npx"
args = ["-y", "mcp-remote", "https://kernel.example/mcp"]
startup_timeout_sec = 30

[mcp_servers.kernel.env]
KERNEL_MODE = "compat"

[mcp_servers.other]
command = "other-server"
`

// realConfigHTTP is the same file after the operator switched kernel to native
// streamable HTTP: no command, no args, no env subtable.
const realConfigHTTP = `# Top-level settings
model = "gpt-5.2-codex"
model_reasoning_effort = "xhigh"

# Notify hook
notify = ["/usr/local/bin/notify"]

[skills]
max_context_tokens = 40000

[mcp_servers.kernel]
url = "https://kernel.example/mcp"
bearer_token_env_var = "KERNEL_TOKEN"

[mcp_servers.other]
command = "other-server"
`

func TestSyncCodexConfigReplacesMCPTransport(t *testing.T) {
	mgr, home, realHome := codexFixture(t, realConfigStdio)
	configPath := filepath.Join(home, ".codex", "config.toml")
	authPath := filepath.Join(home, ".codex", "auth.json")

	// The profile carries its own classified-local state, plus a server only
	// it knows about, plus a comment we must not lose.
	writeFile(t, authPath, `{"tokens":{"access_token":"SYNTHETIC"}}`)
	authBefore := readFile(t, authPath)
	writeFile(t, configPath, readFile(t, configPath)+`
# This profile's own server.
[mcp_servers.profile_only]
command = "mine"

[hooks.state."/home/.codex/hooks.json:pre_tool_use:0:0"]
trusted_hash = "abc123"

[projects."/work/repo"]
trust_level = "trusted"

[notice]
hide_rate_limit_model_nudge = true
`)

	// Step 2 of the reported reproduction: the real config switches transport.
	writeFile(t, filepath.Join(realHome, ".codex", "config.toml"), realConfigHTTP)

	changed, err := mgr.SyncCodexConfig("cx")
	if err != nil {
		t.Fatalf("SyncCodexConfig: %v", err)
	}
	if len(changed) == 0 {
		t.Fatal("sync reported no changes")
	}
	got := readFile(t, configPath)

	// The corruption in #103: command/args must not survive beside url.
	kernel := sectionText(t, got, "mcp_servers.kernel")
	if !strings.Contains(kernel, `url = "https://kernel.example/mcp"`) {
		t.Errorf("kernel server did not get the HTTP transport:\n%s", kernel)
	}
	for _, forbidden := range []string{"command", "args", "startup_timeout_sec"} {
		if strings.Contains(kernel, forbidden) {
			t.Errorf("stale stdio key %q survived in mcp_servers.kernel:\n%s", forbidden, kernel)
		}
	}
	// The env subtable belonged to the old transport and must go with it.
	if strings.Contains(got, "[mcp_servers.kernel.env]") {
		t.Errorf("the old transport's subtable survived:\n%s", got)
	}

	// Classified profile-local state is preserved, untouched.
	for _, want := range []string{
		`[hooks.state."/home/.codex/hooks.json:pre_tool_use:0:0"]`,
		`trusted_hash = "abc123"`,
		`[projects."/work/repo"]`,
		`trust_level = "trusted"`,
		"hide_rate_limit_model_nudge = true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("profile-local state lost: %q\n%s", want, got)
		}
	}

	// Nothing is deleted: a server only the profile has survives.
	if !strings.Contains(got, "[mcp_servers.profile_only]") || !strings.Contains(got, "# This profile's own server.") {
		t.Errorf("profile-only server or its comment was deleted:\n%s", got)
	}

	// Shared root settings follow the real side; comments survive.
	if !strings.Contains(got, `model_reasoning_effort = "xhigh"`) {
		t.Errorf("root key not refreshed:\n%s", got)
	}
	if !strings.Contains(got, "# Notify hook") || !strings.Contains(got, "# Top-level settings") {
		t.Errorf("comments were destroyed:\n%s", got)
	}

	// caam's own setting is re-enforced.
	if !strings.Contains(got, codexCredentialStoreLine) {
		t.Errorf("credential store not enforced:\n%s", got)
	}

	// Credentials are untouched.
	if after := readFile(t, authPath); after != authBefore {
		t.Errorf("auth.json changed: %q -> %q", authBefore, after)
	}

	// The report is names only — never a value.
	for _, name := range changed {
		if strings.ContainsAny(name, "=\"") {
			t.Errorf("change report leaked a value: %q", name)
		}
	}
	if !containsString(changed, "mcp_servers.kernel") {
		t.Errorf("changed = %v, want it to name mcp_servers.kernel", changed)
	}

	// Step 4: a second sync writes nothing.
	before := readFile(t, configPath)
	changed2, err := mgr.SyncCodexConfig("cx")
	if err != nil {
		t.Fatalf("second SyncCodexConfig: %v", err)
	}
	if len(changed2) != 0 {
		t.Errorf("second sync reported changes: %v", changed2)
	}
	if readFile(t, configPath) != before {
		t.Error("second sync rewrote the file")
	}
}

// TestSyncCodexConfigNeverUpdatesProfileLocalFromReal: creation seeds the
// profile from the real config (issue #46), so a profile legitimately starts
// with the operator's hook and project trust. What sync must never do is
// OVERWRITE what that profile has since decided for itself — the trust it
// granted, the notices it dismissed — with whatever the real HOME now says.
func TestSyncCodexConfigNeverUpdatesProfileLocalFromReal(t *testing.T) {
	mgr, home, realHome := codexFixture(t, "model = \"gpt-5.2-codex\"\n")
	configPath := filepath.Join(home, ".codex", "config.toml")

	writeFile(t, configPath, readFile(t, configPath)+`
[hooks.state."/hooks.json:pre_tool_use:0:0"]
trusted_hash = "PROFILE_DECISION"

[projects."/work/repo"]
trust_level = "untrusted"

[notice]
hide_rate_limit_model_nudge = false
`)
	// The real HOME has since made different decisions of its own.
	writeFile(t, filepath.Join(realHome, ".codex", "config.toml"), `model = "gpt-5.2-codex"
model_reasoning_effort = "xhigh"

[hooks.state."/hooks.json:pre_tool_use:0:0"]
trusted_hash = "REAL_DECISION"

[projects."/work/repo"]
trust_level = "trusted"

[notice]
hide_rate_limit_model_nudge = true
`)

	changed, err := mgr.SyncCodexConfig("cx")
	if err != nil {
		t.Fatalf("SyncCodexConfig: %v", err)
	}
	got := readFile(t, configPath)

	for _, forbidden := range []string{"REAL_DECISION", `trust_level = "trusted"`, "hide_rate_limit_model_nudge = true"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("profile-local state was overwritten from the real HOME: %q\n%s", forbidden, got)
		}
	}
	for _, want := range []string{"PROFILE_DECISION", `trust_level = "untrusted"`, "hide_rate_limit_model_nudge = false"} {
		if !strings.Contains(got, want) {
			t.Errorf("the profile's own decision was lost: %q\n%s", want, got)
		}
	}
	// Shared settings still sync, and profile-local names never appear in the
	// change report.
	if !strings.Contains(got, `model_reasoning_effort = "xhigh"`) {
		t.Errorf("shared root key did not sync:\n%s", got)
	}
	for _, name := range changed {
		for _, local := range []string{"hooks.state", "projects", "notice"} {
			if name == local || strings.HasPrefix(name, local+".") {
				t.Errorf("change report claims a profile-local section changed: %q", name)
			}
		}
	}
}

// TestSyncCodexConfigKeepsProfileHookStateWhenHooksChanges: replacing the
// [hooks] unit must not take [hooks.state.*] with it.
func TestSyncCodexConfigKeepsProfileHookStateWhenHooksChanges(t *testing.T) {
	mgr, home, realHome := codexFixture(t, "[hooks]\npre_tool_use = \"old.json\"\n")
	configPath := filepath.Join(home, ".codex", "config.toml")
	writeFile(t, configPath, readFile(t, configPath)+`
[hooks]
pre_tool_use = "old.json"

[hooks.state."/p/hooks.json:pre_tool_use:0:0"]
trusted_hash = "PROFILEHASH"
`)
	writeFile(t, filepath.Join(realHome, ".codex", "config.toml"), "[hooks]\npre_tool_use = \"new.json\"\n")

	if _, err := mgr.SyncCodexConfig("cx"); err != nil {
		t.Fatalf("SyncCodexConfig: %v", err)
	}
	got := readFile(t, configPath)
	if !strings.Contains(got, `pre_tool_use = "new.json"`) {
		t.Errorf("[hooks] not refreshed:\n%s", got)
	}
	if strings.Contains(got, `pre_tool_use = "old.json"`) {
		t.Errorf("stale [hooks] survived:\n%s", got)
	}
	if !strings.Contains(got, "PROFILEHASH") {
		t.Errorf("[hooks.state] was swept away with its parent:\n%s", got)
	}
}

// TestSyncCodexConfigEnforcesCredentialStore: whatever the real config says,
// a shallow profile stores credentials in its own HOME.
func TestSyncCodexConfigEnforcesCredentialStore(t *testing.T) {
	mgr, home, _ := codexFixture(t, "cli_auth_credentials_store = \"keychain\"\nmodel = \"gpt-5.2-codex\"\n")
	configPath := filepath.Join(home, ".codex", "config.toml")
	if _, err := mgr.SyncCodexConfig("cx"); err != nil {
		t.Fatalf("SyncCodexConfig: %v", err)
	}
	got := readFile(t, configPath)
	if strings.Contains(got, `"keychain"`) {
		t.Errorf("the real config's keychain setting was copied in:\n%s", got)
	}
	if strings.Count(got, codexCredentialStoreKey) != 1 {
		t.Errorf("want exactly one credential-store key:\n%s", got)
	}
	if !strings.Contains(got, codexCredentialStoreLine) {
		t.Errorf("credential store not enforced:\n%s", got)
	}
}

func TestSyncCodexConfigSkipsNonCodexAndMissingReal(t *testing.T) {
	// No real config: nothing to reconcile against.
	mgr, _, _ := codexFixture(t, "")
	if changed, err := mgr.SyncCodexConfig("cx"); err != nil || changed != nil {
		t.Errorf("no real config: changed=%v err=%v, want a no-op", changed, err)
	}

	// A claude profile is SyncClaudeConfig's business, not this one's.
	if _, err := mgr.Create("cl", CreateOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if changed, err := mgr.SyncCodexConfig("cl"); err != nil || changed != nil {
		t.Errorf("claude profile: changed=%v err=%v, want a no-op", changed, err)
	}
}

func TestSyncCodexConfigRefusesSymlinkedProfileConfig(t *testing.T) {
	mgr, home, realHome := codexFixture(t, "model = \"gpt-5.2-codex\"\n")
	configPath := filepath.Join(home, ".codex", "config.toml")
	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(realHome, ".codex", "config.toml"), configPath); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	_, err := mgr.SyncCodexConfig("cx")
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a refusal naming the symlink", err)
	}
	// The real config must be untouched.
	if got := readFile(t, filepath.Join(realHome, ".codex", "config.toml")); got != "model = \"gpt-5.2-codex\"\n" {
		t.Errorf("the real config was modified: %q", got)
	}
}

// sectionText returns the text of one table, for assertions that a key did not
// leak into a neighbouring section.
func sectionText(t *testing.T, doc, path string) string {
	t.Helper()
	parsed, err := parseTOMLBlocks([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range parsed.sections {
		if strings.Join(b.path, ".") == path {
			return b.text
		}
	}
	return ""
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestSyncCodexConfigHandlesMissingTrailingNewlines: a config.toml need not end
// with a newline, and blocks are joined by concatenation, so a missing
// separator would run two assignments together.
func TestSyncCodexConfigHandlesMissingTrailingNewlines(t *testing.T) {
	mgr, home, realHome := codexFixture(t, "model = \"a\"")
	configPath := filepath.Join(home, ".codex", "config.toml")

	// A profile config with no trailing newline anywhere.
	writeFile(t, configPath, "cli_auth_credentials_store = \"file\"\nmodel = \"a\"")
	writeFile(t, filepath.Join(realHome, ".codex", "config.toml"),
		"model = \"b\"\nnotify = [\"x\"]\n\n[features]\nunified_exec = true")

	if _, err := mgr.SyncCodexConfig("cx"); err != nil {
		t.Fatalf("SyncCodexConfig: %v", err)
	}
	got := readFile(t, configPath)
	for _, want := range []string{`model = "b"`, `notify = ["x"]`, "[features]", "unified_exec = true"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q:\n%s", want, got)
		}
	}
	// Nothing may have been welded together.
	for _, bad := range []string{`"a"notify`, `"b"notify`, `]cli_auth`, `true[`, `"file"model`} {
		if strings.Contains(got, bad) {
			t.Errorf("lines were concatenated without a separator (%q):\n%s", bad, got)
		}
	}
	// And the result still parses back into the same blocks.
	if _, err := parseTOMLBlocks([]byte(got)); err != nil {
		t.Fatalf("result does not re-parse: %v\n%s", err, got)
	}

	// Idempotent.
	if changed, err := mgr.SyncCodexConfig("cx"); err != nil || len(changed) != 0 {
		t.Errorf("second sync: changed=%v err=%v, want a no-op", changed, err)
	}
}
