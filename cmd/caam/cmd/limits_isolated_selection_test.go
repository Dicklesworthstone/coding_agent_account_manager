package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	claudeprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
)

func writeQuotaSelectionFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

const quotaSelectedGrant = `{"claudeAiOauth":{"accessToken":"synthetic-selected","refreshToken":"synthetic-renewal","expiresAt":4102444800000,"accountId":"same-account"}}`
const quotaIgnoredGrant = `{"claudeAiOauth":{"accessToken":"synthetic-ignored","refreshToken":"synthetic-old-renewal","expiresAt":4102444800000,"accountId":"same-account"}}`

func TestQuotaCandidateFailureIsNotFetchable(t *testing.T) {
	for _, candidate := range []credentialCandidate{
		{State: credStateMissing},
		{State: credStateInvalid, Err: errors.New("source selection failed")},
		{State: credStateHealthy, Token: "unused", Err: errors.New("source changed")},
	} {
		if candidate.Found() {
			t.Fatal("bulk quota fetching accepted an unavailable source")
		}
	}
	if !(credentialCandidate{State: credStateHealthy, Token: "selected"}).Found() {
		t.Fatal("a usable source became unavailable")
	}
}

// Both files may contain working grants. The execution source, never the first
// readable file or the longest-lived token, must determine the quota account.
func TestIsolatedClaudeQuotaPathsMatchExecution(t *testing.T) {
	for _, tc := range []struct {
		name, credential, state string
		files                   map[string]string
	}{
		{
			name:       "XDG wins over valid legacy",
			credential: "xdg_config/claude-code/.credentials.json", state: "xdg_config/claude-code/.claude.json",
			files: map[string]string{
				"xdg_config/claude-code/.credentials.json": quotaSelectedGrant,
				"home/.claude/.credentials.json":           quotaIgnoredGrant,
				"xdg_config/claude-code/.claude.json":      `{"cachedUsageUtilization":{"source":"selected"}}`,
				"home/.claude.json":                        `{"cachedUsageUtilization":{"source":"ignored"}}`,
			},
		},
		{
			name:       "expired XDG still wins",
			credential: "xdg_config/claude-code/.credentials.json", state: "xdg_config/claude-code/.claude.json",
			files: map[string]string{
				"xdg_config/claude-code/.credentials.json": `{"claudeAiOauth":{"accessToken":"expired","expiresAt":1,"accountId":"same-account"}}`,
				"home/.claude/.credentials.json":           quotaIgnoredGrant,
			},
		},
		{
			name:       "malformed XDG cannot borrow legacy",
			credential: "xdg_config/claude-code/.credentials.json", state: "xdg_config/claude-code/.claude.json",
			files: map[string]string{"xdg_config/claude-code/.credentials.json": "{", "home/.claude/.credentials.json": quotaIgnoredGrant},
		},
		{
			name:       "empty XDG cannot borrow legacy",
			credential: "xdg_config/claude-code/.credentials.json", state: "xdg_config/claude-code/.claude.json",
			files: map[string]string{"xdg_config/claude-code/.credentials.json": "{}", "home/.claude/.credentials.json": quotaIgnoredGrant},
		},
		{
			name:       "legacy-only home state",
			credential: "home/.claude/.credentials.json", state: "home/.claude.json",
			files: map[string]string{"home/.claude/.credentials.json": quotaSelectedGrant, "home/.claude.json": "{}", "xdg_config/claude-code/settings.json": `{"model":"sonnet"}`},
		},
		{
			name:       "nested legacy state wins",
			credential: "home/.claude/.credentials.json", state: "home/.claude/.claude.json",
			files: map[string]string{"home/.claude/.credentials.json": quotaSelectedGrant, "home/.claude/.claude.json": "{}", "home/.claude.json": "{}"},
		},
		{
			name:       "malformed nested legacy state is not replaced",
			credential: "home/.claude/.credentials.json", state: "home/.claude/.claude.json",
			files: map[string]string{"home/.claude/.credentials.json": quotaSelectedGrant, "home/.claude/.claude.json": "{", "home/.claude.json": "{}"},
		},
		{
			name:       "missing XDG cache cannot borrow home state",
			credential: "xdg_config/claude-code/.credentials.json", state: "xdg_config/claude-code/.claude.json",
			files: map[string]string{"xdg_config/claude-code/.credentials.json": quotaSelectedGrant, "home/.claude.json": `{"cachedUsageUtilization":{"source":"ignored"}}`},
		},
		{
			name:       "cache without credentials remains readable",
			credential: "xdg_config/claude-code/.credentials.json", state: "xdg_config/claude-code/.claude.json",
			files: map[string]string{"xdg_config/claude-code/.claude.json": `{"cachedUsageUtilization":{"source":"selected"}}`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof := &profile.Profile{Name: "work", Provider: "claude", AuthMode: "oauth", BasePath: t.TempDir()}
			for path, data := range tc.files {
				writeQuotaSelectionFile(t, filepath.Join(prof.BasePath, filepath.FromSlash(path)), data)
			}
			before := captureQuotaSelectionTree(t, prof.BasePath)
			// Ambient host settings must not redirect an isolated quota read.
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "not-this-profile"))
			credential, state, err := isolatedClaudeQuotaPaths(prof)
			if err != nil || credential != filepath.Join(prof.BasePath, filepath.FromSlash(tc.credential)) || state != filepath.Join(prof.BasePath, filepath.FromSlash(tc.state)) {
				t.Fatalf("wrong source: credential=%s state=%s error=%v", credential, state, err)
			}
			env, err := claudeprovider.New().Env(context.Background(), prof)
			if err != nil || filepath.Dir(credential) != env["CLAUDE_CONFIG_DIR"] {
				t.Fatalf("quota differs from execution: %v", err)
			}
			assertQuotaSelectionTree(t, prof.BasePath, before)
		})
	}
}

func TestIsolatedClaudeQuotaPathsRejectConflictsAndWrongMode(t *testing.T) {
	prof := &profile.Profile{Name: "work", Provider: "claude", AuthMode: "oauth", BasePath: t.TempDir()}
	writeQuotaSelectionFile(t, filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json"), quotaSelectedGrant)
	writeQuotaSelectionFile(t, filepath.Join(prof.HomePath(), ".claude", ".credentials.json"), strings.ReplaceAll(quotaIgnoredGrant, "same-account", "different-account"))
	before := captureQuotaSelectionTree(t, prof.BasePath)
	for _, mode := range []string{"oauth", "api-key", "", "unknown"} {
		prof.AuthMode = mode
		credential, state, err := isolatedClaudeQuotaPaths(prof)
		if err == nil || credential != "" || state != "" {
			t.Fatalf("unsafe source accepted for mode %q", mode)
		}
		for _, secret := range []string{"synthetic", "same-account", "different-account"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatal("source error leaked credential or account data")
			}
		}
	}
	if _, _, err := isolatedClaudeQuotaPaths(nil); err == nil {
		t.Fatal("nil profile accepted")
	}
	assertQuotaSelectionTree(t, prof.BasePath, before)
}

type quotaSelectionEntry struct {
	info os.FileInfo
	data string
}

func captureQuotaSelectionTree(t *testing.T, root string) map[string]quotaSelectionEntry {
	t.Helper()
	entries := make(map[string]quotaSelectionEntry)
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		entry := quotaSelectionEntry{info: info}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			entry.data = string(data)
		}
		entries[path] = entry
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return entries
}

func assertQuotaSelectionTree(t *testing.T, root string, before map[string]quotaSelectionEntry) {
	t.Helper()
	after := captureQuotaSelectionTree(t, root)
	if len(before) != len(after) {
		t.Fatal("source tree entries changed")
	}
	for path, want := range before {
		got, ok := after[path]
		if !ok || !os.SameFile(want.info, got.info) || want.info.Mode() != got.info.Mode() || !want.info.ModTime().Equal(got.info.ModTime()) || want.data != got.data {
			t.Fatalf("source changed: %s", path)
		}
	}
}

// Exercise the actual command resolver as well as the path boundary. A failed
// isolated read cannot become a valid legacy grant or a silent shallow fallback.
func TestResolveIsolatedClaudeQuotaRejectsSelectedFailures(t *testing.T) {
	for _, fault := range []string{"malformed", "empty", "conflicting", "api-key"} {
		t.Run(fault, func(t *testing.T) {
			f := newNamespaceFixture(t)
			prof, err := f.store.Create("claude", "work", "oauth")
			if err != nil {
				t.Fatal(err)
			}
			xdg := filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json")
			writeQuotaSelectionFile(t, xdg, quotaSelectedGrant)
			writeQuotaSelectionFile(t, filepath.Join(prof.HomePath(), ".claude", ".credentials.json"), quotaIgnoredGrant)
			switch fault {
			case "malformed":
				writeQuotaSelectionFile(t, xdg, "{")
			case "empty":
				writeQuotaSelectionFile(t, xdg, "{}")
			case "conflicting":
				writeQuotaSelectionFile(t, xdg, strings.ReplaceAll(quotaSelectedGrant, "same-account", "different-account"))
			case "api-key":
				prof.AuthMode = "api-key"
				data, err := json.Marshal(prof)
				if err != nil {
					t.Fatal(err)
				}
				writeQuotaSelectionFile(t, prof.MetaPath(), string(data))
			}
			f.shallowClaude(t, "work", time.Now().Add(time.Hour), true)
			before := captureQuotaSelectionTree(t, f.root)
			candidate := f.lookup.inspect(credNamespaceIsolated, "claude", "work")
			if candidate.Err == nil || candidate.Found() || candidate.Token != "" || candidate.State != credStateInvalid {
				t.Fatal("invalid selected source was offered to bulk quota fetching")
			}
			for _, source := range []string{"", credNamespaceIsolated} {
				res, err := resolveProfileCredential(f.lookup, "claude", "work", source)
				if err == nil || res != nil {
					t.Fatal("selected error fell back to another login")
				}
				if strings.Contains(err.Error(), "synthetic") {
					t.Fatal("diagnostic leaked credentials")
				}
			}
			assertQuotaSelectionTree(t, f.root, before)
			// Explicit vault selection remains independent of the broken
			// isolated account; namespace precedence is not changed.
			f.vaultClaude(t, "work", time.Now().Add(time.Hour), true)
			for _, source := range []string{"", credNamespaceVault} {
				res, err := resolveProfileCredential(f.lookup, "claude", "work", source)
				if err != nil || res.Selected.Namespace != credNamespaceVault || len(res.Healthier) != 0 {
					t.Fatalf("unselected isolated failure blocked vault: %v", err)
				}
			}
		})
	}
}

func TestResolveIsolatedClaudeQuotaKeepsTokenAndCacheTogether(t *testing.T) {
	f := newNamespaceFixture(t)
	prof, err := f.store.Create("claude", "work", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	xdg := filepath.Join(prof.XDGConfigPath(), "claude-code")
	writeQuotaSelectionFile(t, filepath.Join(xdg, ".credentials.json"), quotaSelectedGrant)
	writeQuotaSelectionFile(t, filepath.Join(xdg, ".claude.json"), `{"cachedUsageUtilization":{"source":"selected"}}`)
	writeQuotaSelectionFile(t, filepath.Join(prof.HomePath(), ".claude", ".credentials.json"), quotaIgnoredGrant)
	writeQuotaSelectionFile(t, filepath.Join(prof.HomePath(), ".claude.json"), `{"cachedUsageUtilization":{"source":"ignored"}}`)
	before := captureQuotaSelectionTree(t, f.root)
	res, err := resolveProfileCredential(f.lookup, "claude", "work", credNamespaceIsolated)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selected.Token != "synthetic-selected" || res.Selected.Path != filepath.Join(xdg, ".credentials.json") || res.Selected.ClaudeJSON != filepath.Join(xdg, ".claude.json") {
		t.Fatal("quota lookup mixed the two authenticated layouts")
	}
	if f.lookup.claudeJSONPath(credNamespaceIsolated, "claude", "work") != res.Selected.ClaudeJSON {
		t.Fatal("offline and live quota chose different state files")
	}
	report, err := json.Marshal(res.report())
	if err != nil || strings.Contains(string(report), "synthetic") {
		t.Fatal("source report exposed a token")
	}
	assertQuotaSelectionTree(t, f.root, before)
}
