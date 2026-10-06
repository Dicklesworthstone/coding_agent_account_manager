package keepalive

import (
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
	"time"
)

var engineNow = time.Date(2026, 10, 6, 20, 0, 0, 0, time.UTC)

func engineAuth(provider string, expiry time.Time, access string) []byte {
	if provider == "claude" {
		return []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":"test-refresh","expiresAt":%d,"accountId":"test-account","email":"keepalive@example.invalid"}}`, access, expiry.UnixMilli()))
	}
	return []byte(fmt.Sprintf(`{"key":%q,"refresh_token":"test-refresh","expires_at":%q,"user_id":"test-account","email":"keepalive@example.invalid"}`, access, expiry.Format(time.RFC3339)))
}

func engineGrant(t *testing.T, provider string, expiry time.Time) Grant {
	t.Helper()
	home := t.TempDir()
	grant := Grant{Provider: provider, Kind: "host", Home: home, Env: map[string]string{"HOME": home}}
	if provider == "claude" {
		grant.AuthPath = filepath.Join(home, ".claude", ".credentials.json")
		grant.IdentityPath = filepath.Join(home, ".claude.json")
		grant.Scrub = []string{"CLAUDE_CONFIG_DIR"}
	} else {
		grant.AuthPath = filepath.Join(home, ".grok", "auth.json")
		grant.Env["GROK_HOME"] = filepath.Dir(grant.AuthPath)
	}
	if err := os.MkdirAll(filepath.Dir(grant.AuthPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(grant.AuthPath, engineAuth(provider, expiry, "test-access-before"), 0600); err != nil {
		t.Fatal(err)
	}
	grant = inspectGrant(grant, []string{filepath.Join(home, "vault")})
	if grant.BlockedReason != "" {
		t.Fatalf("fixture grant blocked: %s", grant.BlockedReason)
	}
	return grant
}

func engineCLI(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake native CLI uses a POSIX shell")
	}
	path := filepath.Join(t.TempDir(), "fake-native")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nset -eu\n"+script+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func engineOptions(t *testing.T, provider, bin string) Options {
	t.Helper()
	opts := Options{StateDir: filepath.Join(t.TempDir(), "state"), Now: func() time.Time { return engineNow }, Timeout: 3 * time.Second}
	if provider == "claude" {
		opts.ClaudeBin = bin
	} else {
		opts.GrokBin = bin
	}
	return opts
}

func runEngine(t *testing.T, grant Grant, opts Options) Result {
	t.Helper()
	results, err := Run(context.Background(), []Grant{grant}, opts)
	if err != nil || len(results) != 1 {
		t.Fatalf("Run returned %d results, error %v", len(results), err)
	}
	return results[0]
}

func TestRunNativeUsesLiveOwnerAndScrubsOverrides(t *testing.T) {
	for _, provider := range []string{"claude", "grok"} {
		t.Run(provider, func(t *testing.T) {
			grant := engineGrant(t, provider, engineNow.Add(-time.Hour))
			capture := filepath.Join(t.TempDir(), "capture")
			next := filepath.Join(t.TempDir(), "next.json")
			if err := os.WriteFile(next, engineAuth(provider, engineNow.Add(6*time.Hour), "test-access-after"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CAAM_KEEPALIVE_CAPTURE", capture)
			t.Setenv("CAAM_KEEPALIVE_NEXT", next)
			for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_USE_BEDROCK", "GROK_AUTH", "GROK_AUTH_PATH", "GROK_API_KEY", "GROK_DEPLOYMENT_KEY", "XAI_API_KEY", "XAI_API_TOKEN"} {
				t.Setenv(name, "test-ambient-credential")
			}
			t.Setenv("GROK_HOME", filepath.Join(t.TempDir(), "wrong-grok"))
			t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), "wrong-claude"))
			script := `test -z "${ANTHROPIC_API_KEY:-}${ANTHROPIC_AUTH_TOKEN:-}${ANTHROPIC_BASE_URL:-}${CLAUDE_CODE_OAUTH_TOKEN:-}${CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR:-}${CLAUDE_CODE_USE_BEDROCK:-}${GROK_AUTH:-}${GROK_AUTH_PATH:-}${GROK_API_KEY:-}${GROK_DEPLOYMENT_KEY:-}${XAI_API_KEY:-}${XAI_API_TOKEN:-}"
test -z "$(ls -A .)"
printf '%s\n' "$HOME" "$PWD" "$@" > "$CAAM_KEEPALIVE_CAPTURE"
printf 'provider might echo test-access-before test-refresh\n'
printf 'provider might echo test-access-after test-refresh\n' >&2
`
			if provider == "claude" {
				script += `test -z "${CLAUDE_CONFIG_DIR:-}"
test "$CLAUDE_CODE_DISABLE_AGENT_VIEW" = 1
cp "$CAAM_KEEPALIVE_NEXT" "$HOME/.claude/.credentials.json"`
			} else {
				script += `test "$GROK_HOME" = "$HOME/.grok"
cp "$CAAM_KEEPALIVE_NEXT" "$GROK_HOME/auth.json"`
			}
			opts := engineOptions(t, provider, engineCLI(t, script))
			called := false
			opts.AfterRenew = func(_ context.Context, live Grant, after CredentialSnapshot) ([]SyncResult, error) {
				called = true
				if live.AuthPath != grant.AuthPath || !after.ExpiresAt.Equal(engineNow.Add(6*time.Hour)) {
					t.Errorf("sync received another source or an old credential")
				}
				return []SyncResult{{Profile: "same-account", Status: "synced"}}, nil
			}
			result := runEngine(t, grant, opts)
			if !result.Success || !result.Attempted || result.Status != "rotated" || !called || !reflect.DeepEqual(result.SyncedProfiles, []string{"same-account"}) {
				t.Fatalf("unexpected result: %+v, sync=%v", result, called)
			}
			data, err := os.ReadFile(capture)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
			if len(lines) < 3 || lines[0] != grant.Home || lines[1] == grant.Home || !strings.Contains(filepath.Base(lines[1]), "caam-keepalive-") {
				t.Fatalf("native command did not use the live HOME and an empty workdir: %q", data)
			}
			expectedArgs := []string{"models"}
			if provider == "claude" {
				expectedArgs = []string{"-p", "ping", "--model", "haiku", "--effort", "low",
					"--no-session-persistence", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
					"--setting-sources", "project", "--settings", `{"disableAllHooks":true}`,
					"--tools", "", "--disable-slash-commands", "--system-prompt", "Reply with one word."}
			}
			if !reflect.DeepEqual(lines[2:], expectedArgs) {
				t.Fatalf("native argv %q, want %q", lines[2:], expectedArgs)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"test-access-before", "test-access-after", "test-refresh", "test-ambient-credential", "ANTHROPIC_API_KEY"} {
				if strings.Contains(string(encoded), secret) {
					t.Errorf("result leaked %q", secret)
				}
			}
		})
	}
}

func TestRunChecksCredentialsAfterNativeExit(t *testing.T) {
	tests := []struct {
		name, script, wantStatus, wantReason string
		validBefore, wantSuccess             bool
	}{
		{"removed with zero exit", `mv "$GROK_HOME/auth.json" "$GROK_HOME/rejected-auth.json"`, "failed", "credential_missing", false, false},
		{"malformed with zero exit", `printf 'invalid json' > "$GROK_HOME/auth.json"`, "failed", "credential_invalid", false, false},
		{"unchanged expired with zero exit", `exit 0`, "failed", "still_expired", false, false},
		{"unchanged valid with zero exit", `exit 0`, "still_valid", "", true, true},
		{"unchanged valid with nonzero exit", `printf 'test-secret' >&2; exit 17`, "failed", "native_cli_failed", true, false},
		{"renewed before model failure", `printf '%s' '{"key":"new-access","refresh_token":"new-refresh","expires_at":"2026-10-07T20:00:00Z","user_id":"test-account"}' > "$GROK_HOME/auth.json"; exit 17`, "rotated", "native_cli_failed_after_rotation", false, true},
		{"different account", `printf '%s' '{"key":"other","refresh_token":"other-refresh","expires_at":"2026-10-07T20:00:00Z","user_id":"different-account"}' > "$GROK_HOME/auth.json"`, "failed", "account_changed", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			expiry := engineNow.Add(-time.Minute)
			if tc.validBefore {
				expiry = engineNow.Add(time.Hour)
			}
			grant := engineGrant(t, "grok", expiry)
			opts := engineOptions(t, "grok", engineCLI(t, tc.script))
			called := false
			opts.AfterRenew = func(context.Context, Grant, CredentialSnapshot) ([]SyncResult, error) {
				called = true
				return nil, nil
			}
			result := runEngine(t, grant, opts)
			if !result.Attempted || result.Status != tc.wantStatus || result.Reason != tc.wantReason || result.Success != tc.wantSuccess {
				t.Fatalf("result %+v, want %s/%s success=%v", result, tc.wantStatus, tc.wantReason, tc.wantSuccess)
			}
			if called != tc.wantSuccess {
				t.Errorf("sync called=%v for native success=%v", called, tc.wantSuccess)
			}
		})
	}
}

func TestRunMinGapAndExpiredBypass(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(20*time.Minute))
	marker := filepath.Join(t.TempDir(), "attempts")
	t.Setenv("CAAM_KEEPALIVE_ATTEMPTS", marker)
	opts := engineOptions(t, "grok", engineCLI(t, `printf 'attempt\n' >> "$CAAM_KEEPALIVE_ATTEMPTS"`))
	first := runEngine(t, grant, opts)
	if !first.Attempted || !first.Success {
		t.Fatalf("initial keepalive: %+v", first)
	}
	opts.Now = func() time.Time { return engineNow.Add(5 * time.Minute) }
	second := runEngine(t, grant, opts)
	if second.Attempted || second.Reason != "min_gap" || !second.Success || !second.NextEligibleAt.Equal(engineNow.Add(DefaultMinGap)) {
		t.Fatalf("repeated valid token was not throttled: %+v", second)
	}
	if err := os.WriteFile(grant.AuthPath, engineAuth("grok", engineNow.Add(-time.Minute), "test-access-before"), 0600); err != nil {
		t.Fatal(err)
	}
	third := runEngine(t, grant, opts)
	if !third.Attempted || third.Reason != "still_expired" {
		t.Fatalf("expired grant should bypass the recent attempt: %+v", third)
	}
	data, err := os.ReadFile(marker)
	if err != nil || strings.Count(string(data), "attempt\n") != 2 {
		t.Fatalf("attempt count = %q, error %v", data, err)
	}
	state, err := os.ReadFile(filepath.Join(opts.StateDir, grantKey(grant)+".json"))
	if err != nil || strings.Contains(string(state), "test-access") || strings.Contains(string(state), "test-refresh") {
		t.Fatalf("unsafe or unreadable state: %q, error %v", state, err)
	}
}

func TestRunDryRunAndOutsideTTLDoNotWrite(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("dry_run_%v", dryRun), func(t *testing.T) {
			expiry := engineNow.Add(6 * time.Hour)
			if dryRun {
				expiry = engineNow.Add(-time.Minute)
			}
			grant := engineGrant(t, "grok", expiry)
			before, err := os.ReadFile(grant.AuthPath)
			if err != nil {
				t.Fatal(err)
			}
			opts := engineOptions(t, "grok", filepath.Join(t.TempDir(), "does-not-exist"))
			opts.DryRun = dryRun
			result := runEngine(t, grant, opts)
			if result.Attempted || !result.Success || (dryRun && result.Status != "dry_run") || (!dryRun && result.Reason != "outside_ttl") {
				t.Fatalf("unexpected read-only result: %+v", result)
			}
			for _, path := range []string{opts.StateDir, grant.AuthPath + ".caam-keepalive.lock"} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("read-only run created %s (error %v)", path, err)
				}
			}
			after, err := os.ReadFile(grant.AuthPath)
			if err != nil || string(after) != string(before) {
				t.Fatalf("read-only run changed credential, error %v", err)
			}
		})
	}
}

func TestRunOwnerDiagnosticDoesNotConsumeGrantKey(t *testing.T) {
	owner := engineGrant(t, "grok", engineNow.Add(time.Hour))
	alias := owner
	alias.Kind, alias.Name = "isolated", "same-file"
	alias = inspectGrant(alias, nil)
	alias.BlockedReason, alias.Owner = "another live reference owns this same physical credential file", owner.Ref()
	freezeGrant(&alias)
	for _, grants := range [][]Grant{{alias, owner}, {owner, alias}} {
		results, err := Run(context.Background(), grants, Options{DryRun: true, Now: func() time.Time { return engineNow }, StateDir: t.TempDir()})
		if err != nil || len(results) != 2 {
			t.Fatalf("unexpected results: %+v, error %v", results, err)
		}
		for _, result := range results {
			if !result.Success || result.Attempted {
				t.Fatalf("a diagnostic consumed the owner's runnable key: %+v", result)
			}
			if result.Ref == owner.Ref() && result.Status != "dry_run" {
				t.Fatalf("live owner lost eligibility: %+v", result)
			}
		}
	}
}

func TestRunDirectoryAliasSharesMinimumGap(t *testing.T) {
	owner := engineGrant(t, "grok", engineNow.Add(time.Hour))
	aliasHome := filepath.Join(t.TempDir(), "home-alias")
	if err := os.Symlink(owner.Home, aliasHome); err != nil {
		t.Skipf("directory symlinks are unavailable: %v", err)
	}
	alias := Grant{Provider: "grok", Kind: "host", Home: aliasHome,
		AuthPath: filepath.Join(aliasHome, ".grok", "auth.json"),
		Env:      map[string]string{"HOME": aliasHome, "GROK_HOME": filepath.Join(aliasHome, ".grok")}}
	alias = inspectGrant(alias, nil)
	opts := engineOptions(t, "grok", engineCLI(t, ":"))
	if result := runEngine(t, owner, opts); !result.Success || !result.Attempted {
		t.Fatalf("first attempt failed: %+v", result)
	}
	if result := runEngine(t, alias, opts); !result.Success || result.Attempted || result.Reason != "min_gap" {
		t.Fatalf("directory alias bypassed the live grant's minimum gap: %+v", result)
	}
}

func TestRunExplicitlyDisabledMinGap(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
	opts := engineOptions(t, "grok", engineCLI(t, `exit 0`))
	first := runEngine(t, grant, opts)
	if !first.Attempted || !first.Success {
		t.Fatalf("first attempt: %+v", first)
	}
	opts.DisableMinGap = true
	second := runEngine(t, grant, opts)
	if !second.Attempted || !second.Success {
		t.Fatalf("explicitly disabled min-gap still throttled: %+v", second)
	}
}

func TestRunNativeExecutableRelativeToCaller(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
	bin := engineCLI(t, `exit 0`)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, bin)
	if err != nil {
		t.Fatal(err)
	}
	result := runEngine(t, grant, engineOptions(t, "grok", relative))
	if !result.Attempted || !result.Success {
		t.Fatalf("native executable was resolved relative to the empty child cwd: %+v", result)
	}
}

func TestRunConcurrentCallsShareGrantLockAcrossStateDirs(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
	control := t.TempDir()
	started, release := filepath.Join(control, "started"), filepath.Join(control, "release")
	t.Setenv("CAAM_KEEPALIVE_STARTED", started)
	t.Setenv("CAAM_KEEPALIVE_RELEASE", release)
	bin := engineCLI(t, `printf started > "$CAAM_KEEPALIVE_STARTED"
while [ ! -f "$CAAM_KEEPALIVE_RELEASE" ]; do sleep 0.01; done`)
	opts := engineOptions(t, "grok", bin)
	t.Cleanup(func() { _ = os.WriteFile(release, []byte("release"), 0600) })
	type outcome struct {
		results []Result
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, err := Run(context.Background(), []Grant{grant}, opts)
		done <- outcome{results, err}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(started); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first native command did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	otherOpts := opts
	otherOpts.StateDir = filepath.Join(t.TempDir(), "another-state-root")
	second := runEngine(t, grant, otherOpts)
	if second.Attempted || second.Reason != "already_running" {
		t.Fatalf("concurrent call did not respect live grant lock: %+v", second)
	}
	if err := os.WriteFile(release, []byte("release"), 0600); err != nil {
		t.Fatal(err)
	}
	first := <-done
	if first.err != nil || len(first.results) != 1 || !first.results[0].Success {
		t.Fatalf("first call: %+v", first)
	}
}

func TestRunTimeoutIsBoundedAndDoesNotSync(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
	opts := engineOptions(t, "grok", engineCLI(t, `exec sleep 30`))
	opts.Timeout = 40 * time.Millisecond
	called := false
	opts.AfterRenew = func(context.Context, Grant, CredentialSnapshot) ([]SyncResult, error) {
		called = true
		return nil, nil
	}
	started := time.Now()
	result := runEngine(t, grant, opts)
	if result.Success || !result.Attempted || result.Reason != "native_cli_timeout" || called {
		t.Fatalf("unexpected timeout result: %+v, sync=%v", result, called)
	}
	if time.Since(started) > time.Second {
		t.Fatal("native timeout did not bound the command")
	}
}

func TestRunSyncFailurePreservesRenewalSuccess(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
	opts := engineOptions(t, "grok", engineCLI(t, `exit 0`))
	opts.AfterRenew = func(context.Context, Grant, CredentialSnapshot) ([]SyncResult, error) {
		return nil, errors.New("a provider error might contain test-secret")
	}
	result := runEngine(t, grant, opts)
	if !result.Success || result.Status != "still_valid" || result.Reason != "vault_sync_failed" || len(result.Sync) != 1 || result.Sync[0].Status != "failed" {
		t.Fatalf("vault failure obscured successful native renewal: %+v", result)
	}
}

func TestRunTimeoutStopsDescendantBeforeReleasingGrant(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(time.Hour))
	marker := filepath.Join(t.TempDir(), "descendant-write")
	t.Setenv("CAAM_KEEPALIVE_DESCENDANT_MARKER", marker)
	opts := engineOptions(t, "grok", engineCLI(t, `(sleep 0.2; printf escaped > "$CAAM_KEEPALIVE_DESCENDANT_MARKER") &
wait`))
	opts.Timeout = 40 * time.Millisecond
	result := runEngine(t, grant, opts)
	if result.Reason != "native_cli_timeout" {
		t.Fatalf("unexpected result: %+v", result)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("native descendant survived cancellation (stat error %v)", err)
	}
}

func TestRunInvalidAndUndiscoveredGrantsNeverExecute(t *testing.T) {
	grant := engineGrant(t, "grok", engineNow.Add(-time.Minute))
	opts := engineOptions(t, "grok", filepath.Join(t.TempDir(), "does-not-exist"))
	for _, mutate := range []func(*Grant){
		func(g *Grant) { g.source = nil },
		func(g *Grant) { g.AuthPath = filepath.Join(g.Home, "vault", "grok", "snapshot", "auth.json") },
		func(g *Grant) { g.Env = map[string]string{"HOME": g.Home, "GROK_HOME": filepath.Join(g.Home, "vault")} },
	} {
		changed := grant
		mutate(&changed)
		result := runEngine(t, changed, opts)
		if result.Attempted || result.Success || result.Status != "blocked" {
			t.Fatalf("modified/undiscovered grant was eligible: %+v", result)
		}
	}
}
