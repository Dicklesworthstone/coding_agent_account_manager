package keepalive

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	claudeprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/claude"
	grokprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/grok"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
)

func discoveryFixture(t *testing.T) DiscoverOptions {
	t.Helper()
	root := t.TempDir()
	opts := DiscoverOptions{Home: filepath.Join(root, "home"), ShallowBase: filepath.Join(root, "shallow"),
		ProfilesPath: filepath.Join(root, "profiles"), VaultPath: filepath.Join(root, "vault")}
	t.Setenv("HOME", opts.Home)
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("GROK_HOME", "")
	return opts
}

func writeDiscoveryFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func writeDiscoveryClaude(t *testing.T, home, id, email, refresh string) {
	t.Helper()
	credentials := map[string]any{"claudeAiOauth": map[string]any{
		"accessToken": "synthetic-claude-access-" + refresh, "refreshToken": refresh, "expiresAt": time.Date(2026, 10, 6, 22, 0, 0, 0, time.UTC).UnixMilli(),
	}}
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(home, ".claude", ".credentials.json"), string(data))
	state, err := json.Marshal(map[string]any{"oauthAccount": map[string]string{"accountUuid": id, "emailAddress": email}})
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(home, ".claude.json"), string(state))
}

func writeDiscoveryGrok(t *testing.T, configDir, id, email, refresh string) {
	t.Helper()
	credentials := map[string]any{"synthetic-issuer::client": map[string]any{
		"key": "synthetic-grok-access-" + refresh, "refresh_token": refresh, "expires_at": "2026-10-06T22:00:00Z", "user_id": id, "email": email,
	}}
	data, err := json.Marshal(credentials)
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(configDir, "auth.json"), string(data))
}

func writeDiscoveryShallow(t *testing.T, opts DiscoverOptions, name, id, email, refresh string) {
	t.Helper()
	home := filepath.Join(opts.ShallowBase, name)
	writeDiscoveryFile(t, filepath.Join(home, shallow.ProfileMetaFilename), `{"provider":"claude","name":"`+name+`"}`)
	writeDiscoveryClaude(t, home, id, email, refresh)
}

func findDiscoveryGrant(t *testing.T, grants []Grant, ref string) Grant {
	t.Helper()
	for _, grant := range grants {
		if grant.Ref() == ref {
			return grant
		}
	}
	t.Fatalf("missing grant %s in %+v", ref, grants)
	return Grant{}
}

func TestDiscoverOwnership(t *testing.T) {
	for _, tc := range []struct {
		name               string
		hostID             string
		hostEmail          string
		shallowID          string
		shallowEmail       string
		hostRefresh        string
		shallowRefresh     string
		secondShallow      bool
		wantHostBlocked    bool
		wantShallowBlocked bool
		wantOwner          string
	}{
		{name: "shallow owns copied host account", hostID: "a", hostEmail: "a@example.test", shallowID: "a", shallowEmail: "a@example.test", wantHostBlocked: true, wantOwner: "shallow:claude/work"},
		{name: "different authoritative IDs keep same email independent", hostID: "a", hostEmail: "shared@example.test", shallowID: "b", shallowEmail: "shared@example.test"},
		{name: "email fallback ignores case", hostEmail: "Account@Example.test", shallowID: "a", shallowEmail: "account@example.test", wantHostBlocked: true, wantOwner: "shallow:claude/work"},
		{name: "two shallow homes refuse duplicate grant", hostID: "a", hostEmail: "a@example.test", shallowID: "a", shallowEmail: "a@example.test", secondShallow: true, wantHostBlocked: true, wantShallowBlocked: true},
		{name: "same refresh value contradicts account labels", hostID: "a", hostEmail: "a@example.test", shallowID: "b", shallowEmail: "b@example.test", hostRefresh: "same-family", shallowRefresh: "same-family", wantHostBlocked: true, wantShallowBlocked: true},
		{name: "unknown home cannot hide competing family", hostID: "a", hostEmail: "a@example.test", wantHostBlocked: true, wantShallowBlocked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := discoveryFixture(t)
			opts.Provider = "claude"
			hostRefresh, shallowRefresh := tc.hostRefresh, tc.shallowRefresh
			if hostRefresh == "" {
				hostRefresh = "host-family"
			}
			if shallowRefresh == "" {
				shallowRefresh = "shallow-family"
			}
			writeDiscoveryClaude(t, opts.Home, tc.hostID, tc.hostEmail, hostRefresh)
			writeDiscoveryShallow(t, opts, "work", tc.shallowID, tc.shallowEmail, shallowRefresh)
			if tc.secondShallow {
				writeDiscoveryShallow(t, opts, "other", tc.shallowID, tc.shallowEmail, "third-family")
			}
			grants, err := Discover(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			host := findDiscoveryGrant(t, grants, "host:claude")
			work := findDiscoveryGrant(t, grants, "shallow:claude/work")
			if (host.BlockedReason != "") != tc.wantHostBlocked || host.Owner != tc.wantOwner {
				t.Errorf("host blocked=%q owner=%q", host.BlockedReason, host.Owner)
			}
			if (work.BlockedReason != "") != tc.wantShallowBlocked {
				t.Errorf("shallow blocked=%q", work.BlockedReason)
			}
			if tc.secondShallow && findDiscoveryGrant(t, grants, "shallow:claude/other").BlockedReason == "" {
				t.Error("second competing home remained eligible")
			}
		})
	}
}

func TestDiscoverNativeEnvironmentsAndNoMutation(t *testing.T) {
	opts := discoveryFixture(t)
	customClaude, customGrok := filepath.Join(opts.Home, "custom-claude"), filepath.Join(opts.Home, "custom-grok")
	t.Setenv("CLAUDE_CONFIG_DIR", customClaude)
	t.Setenv("GROK_HOME", customGrok)
	writeDiscoveryClaude(t, opts.Home, "unused-host", "unused@example.test", "unused-host")
	writeDiscoveryFile(t, filepath.Join(customClaude, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"configured-access","refreshToken":"configured-refresh","expiresAt":1791324000000}}`)
	writeDiscoveryFile(t, filepath.Join(customClaude, ".claude.json"), `{"oauthAccount":{"accountUuid":"configured","emailAddress":"configured@example.test"}}`)
	writeDiscoveryGrok(t, customGrok, "grok-live", "grok@example.test", "grok-live")
	writeDiscoveryGrok(t, filepath.Join(opts.Home, ".grok"), "unused-grok", "unused@example.test", "unused-grok")
	writeDiscoveryShallow(t, opts, "parallel", "parallel", "parallel@example.test", "parallel-refresh")
	for _, providerID := range []string{"claude", "grok"} {
		prof := &profile.Profile{Provider: providerID, Name: "isolated", AuthMode: "oauth", BasePath: filepath.Join(opts.ProfilesPath, providerID, "isolated")}
		data, err := json.Marshal(prof)
		if err != nil {
			t.Fatal(err)
		}
		writeDiscoveryFile(t, filepath.Join(prof.BasePath, "profile.json"), string(data))
		if providerID == "claude" {
			config := filepath.Join(prof.XDGConfigPath(), "claude-code")
			writeDiscoveryFile(t, filepath.Join(config, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"isolated-access","refreshToken":"isolated-refresh","expiresAt":1791324000000}}`)
			writeDiscoveryFile(t, filepath.Join(config, ".claude.json"), `{"oauthAccount":{"accountUuid":"isolated-claude","emailAddress":"isolated@example.test"}}`)
		} else {
			writeDiscoveryGrok(t, filepath.Join(prof.HomePath(), ".grok"), "isolated-grok", "isolated-grok@example.test", "isolated-grok-refresh")
		}
	}
	before := discoveryTree(t, filepath.Dir(opts.Home))
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if after := discoveryTree(t, filepath.Dir(opts.Home)); !reflect.DeepEqual(before, after) {
		t.Fatal("read-only discovery changed files, links, or directories")
	}
	if len(grants) != 5 {
		t.Fatalf("got %d grants, want 5", len(grants))
	}
	for _, grant := range grants {
		if grant.BlockedReason != "" {
			t.Errorf("%s blocked: %s", grant.Ref(), grant.BlockedReason)
		}
	}
	hostClaude := findDiscoveryGrant(t, grants, "host:claude")
	if hostClaude.AuthPath != filepath.Join(customClaude, ".credentials.json") || hostClaude.Identity.AccountID != "configured" {
		t.Fatalf("configured host source = %+v", hostClaude)
	}
	hostGrok := findDiscoveryGrant(t, grants, "host:grok")
	if hostGrok.AuthPath != filepath.Join(customGrok, "auth.json") || hostGrok.Identity.AccountID != "grok-live" {
		t.Fatalf("configured Grok source = %+v", hostGrok)
	}
	shallowGrant := findDiscoveryGrant(t, grants, "shallow:claude/parallel")
	wantEnv, wantScrub := shallow.SpawnEnv("claude", shallowGrant.Home, "parallel", false, false)
	if !maps.Equal(shallowGrant.Env, wantEnv) || !reflect.DeepEqual(shallowGrant.Scrub, wantScrub) {
		t.Fatalf("keepalive shallow environment differs from shallow-spawn: %+v", shallowGrant)
	}
	for _, providerID := range []string{"claude", "grok"} {
		grant := findDiscoveryGrant(t, grants, "isolated:"+providerID+"/isolated")
		prof := &profile.Profile{BasePath: filepath.Join(opts.ProfilesPath, providerID, "isolated")}
		var expected map[string]string
		if providerID == "claude" {
			expected, err = claudeprovider.New().Env(context.Background(), prof)
		} else {
			expected, err = grokprovider.New().Env(context.Background(), prof)
		}
		if err != nil || !maps.Equal(grant.Env, expected) {
			t.Fatalf("isolated environment differs from native provider: %+v", grant.Env)
		}
	}
}

func discoveryTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String() + ":" + info.ModTime().String()
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			value += ":" + target
		} else if !d.IsDir() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + fingerprint(data)
		}
		result[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDiscoverRejectsVaultAliases(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "grok"
	vaultConfig := filepath.Join(opts.VaultPath, "grok", "saved")
	writeDiscoveryGrok(t, vaultConfig, "saved", "saved@example.test", "saved-refresh")
	if err := os.MkdirAll(opts.Home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(vaultConfig, filepath.Join(opts.Home, ".grok")); err != nil {
		t.Fatal(err)
	}
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	grant := findDiscoveryGrant(t, grants, "host:grok")
	if !strings.Contains(grant.BlockedReason, "vault snapshots") {
		t.Fatalf("vault alias accepted: %+v", grant)
	}
	if _, err := ReadCredential(grant); err == nil {
		t.Fatal("vault alias became a readable live source")
	}
}

func TestDiscoverRejectsVaultHardLink(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "grok"
	vaultConfig := filepath.Join(opts.VaultPath, "grok", "saved")
	writeDiscoveryGrok(t, vaultConfig, "saved", "saved@example.test", "saved-refresh")
	live := filepath.Join(opts.Home, ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(live), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(vaultConfig, "auth.json"), live); err != nil {
		t.Fatal(err)
	}
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	grant := findDiscoveryGrant(t, grants, "host:grok")
	if !strings.Contains(grant.BlockedReason, "vault snapshots") {
		t.Fatalf("hard-linked vault credential accepted: %+v", grant)
	}
}

func TestDiscoverRejectsNativeCredentialFileLinks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		link     func(string, string) error
		want     string
		dangling bool
	}{
		{"file symlink", os.Symlink, "file symlink", false},
		{"dangling file symlink", os.Symlink, "file symlink", true},
		{"hard link outside discovered homes", os.Link, "multiple hard links", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := discoveryFixture(t)
			opts.Provider = "grok"
			original := filepath.Join(filepath.Dir(opts.Home), "independent-native-home")
			if !tc.dangling {
				writeDiscoveryGrok(t, original, "live", "live@example.test", "synthetic-refresh")
			}
			alias := filepath.Join(opts.Home, ".grok", "auth.json")
			if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
				t.Fatal(err)
			}
			if err := tc.link(filepath.Join(original, "auth.json"), alias); err != nil {
				t.Fatal(err)
			}
			grants, err := Discover(context.Background(), opts)
			if err != nil {
				t.Fatal(err)
			}
			grant := findDiscoveryGrant(t, grants, "host:grok")
			if !strings.Contains(grant.BlockedReason, tc.want) || grant.Owner != "" {
				t.Fatalf("linked native source not refused: %+v", grant)
			}
			if _, err := ReadCredential(grant); err == nil {
				t.Fatal("linked native file became a runnable source")
			}
		})
	}
}

func TestDiscoverRejectsHardlinkedLiveHomes(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "grok"
	writeDiscoveryGrok(t, filepath.Join(opts.Home, ".grok"), "account", "account@example.test", "synthetic-family")
	prof := &profile.Profile{Provider: "grok", Name: "linked", AuthMode: "oauth", BasePath: filepath.Join(opts.ProfilesPath, "grok", "linked")}
	metadata, err := json.Marshal(prof)
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(prof.BasePath, "profile.json"), string(metadata))
	alias := filepath.Join(prof.HomePath(), ".grok", "auth.json")
	if err := os.MkdirAll(filepath.Dir(alias), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(opts.Home, ".grok", "auth.json"), alias); err != nil {
		t.Fatal(err)
	}
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 2 {
		t.Fatalf("got %d grants, want both linked homes", len(grants))
	}
	for _, grant := range grants {
		if grant.BlockedReason == "" || grant.Owner != "" {
			t.Fatalf("hardlinked homes must both refuse renewal: %+v", grant)
		}
	}
}

func TestDiscoverMissingHostAndRegisteredHomes(t *testing.T) {
	opts := discoveryFixture(t)
	grants, err := Discover(context.Background(), opts)
	if err != nil || len(grants) != 0 {
		t.Fatalf("unconfigured host should have no live grants: %+v err=%v", grants, err)
	}
	writeDiscoveryFile(t, filepath.Join(opts.ShallowBase, "not-logged-in", shallow.ProfileMetaFilename), `{"provider":"claude","name":"not-logged-in"}`)
	grants, err = Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	grant := findDiscoveryGrant(t, grants, "shallow:claude/not-logged-in")
	if len(grants) != 1 || grant.BlockedReason == "" {
		t.Fatalf("registered missing credential should remain visible: %+v", grants)
	}
	if _, err := ReadCredential(grant); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing credential classification lost: %v", err)
	}
}

func TestDiscoverIsolatedClaudeRefusesMultipleCredentialLocations(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "claude"
	prof := &profile.Profile{Provider: "claude", Name: "work", AuthMode: "oauth", BasePath: filepath.Join(opts.ProfilesPath, "claude", "work")}
	data, err := json.Marshal(prof)
	if err != nil {
		t.Fatal(err)
	}
	writeDiscoveryFile(t, filepath.Join(prof.BasePath, "profile.json"), string(data))
	writeDiscoveryClaude(t, prof.HomePath(), "legacy", "legacy@example.test", "legacy-refresh")
	configured := filepath.Join(prof.XDGConfigPath(), "claude-code")
	writeDiscoveryFile(t, filepath.Join(configured, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"configured-access","refreshToken":"configured-refresh","expiresAt":1791324000000}}`)
	writeDiscoveryFile(t, filepath.Join(configured, ".claude.json"), `{"oauthAccount":{"accountUuid":"configured","emailAddress":"configured@example.test"}}`)
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	grant := findDiscoveryGrant(t, grants, "isolated:claude/work")
	if !strings.Contains(grant.BlockedReason, "both legacy and configured") {
		t.Fatalf("ambiguous native credential selection accepted: %+v", grant)
	}
}

func TestDiscoverPhysicalAliasesHaveOneOwner(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "claude"
	writeDiscoveryClaude(t, opts.Home, "account", "account@example.test", "shared-refresh")
	workHome := filepath.Join(opts.ShallowBase, "work")
	writeDiscoveryFile(t, filepath.Join(workHome, shallow.ProfileMetaFilename), `{"provider":"claude","name":"work"}`)
	if err := os.Symlink(filepath.Join(opts.Home, ".claude"), filepath.Join(workHome, ".claude")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(opts.Home, ".claude.json"), filepath.Join(workHome, ".claude.json")); err != nil {
		t.Fatal(err)
	}
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	host := findDiscoveryGrant(t, grants, "host:claude")
	work := findDiscoveryGrant(t, grants, "shallow:claude/work")
	if host.Owner != work.Ref() || host.BlockedReason == "" || work.BlockedReason != "" {
		t.Fatalf("physical alias ownership: host=%+v work=%+v", host, work)
	}
}

func TestReadCredentialRejectsCallerMutation(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "grok"
	writeDiscoveryGrok(t, filepath.Join(opts.Home, ".grok"), "live", "live@example.test", "live-refresh")
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	original := findDiscoveryGrant(t, grants, "host:grok")
	for _, tc := range []struct {
		name   string
		change func(*Grant)
	}{
		{"path", func(g *Grant) { g.AuthPath = filepath.Join(opts.VaultPath, "grok", "saved", "auth.json") }},
		{"provider", func(g *Grant) { g.Provider = "claude" }},
		{"home", func(g *Grant) { g.Home = opts.VaultPath }},
		{"environment", func(g *Grant) { g.Env["GROK_HOME"] = opts.VaultPath }},
		{"scrub", func(g *Grant) { g.Scrub = append(g.Scrub, "HOME") }},
		{"identity", func(g *Grant) { g.Identity.AccountID = "other" }},
		{"block reason", func(g *Grant) { g.BlockedReason = "different policy" }},
		{"owner", func(g *Grant) { g.Owner = "shallow:grok/fake" }},
		{"unbound", func(g *Grant) { g.source = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := original
			g.Env = maps.Clone(g.Env)
			tc.change(&g)
			if _, err := ReadCredential(g); err == nil {
				t.Fatal("mutated source accepted")
			}
		})
	}
	// A legitimate native atomic rewrite stays at the same source path.
	writeDiscoveryGrok(t, filepath.Join(opts.Home, ".grok"), "live", "live@example.test", "rotated-refresh")
	snapshot, err := ReadCredential(original)
	if err != nil || snapshot.RefreshFingerprint == original.RefreshFingerprint || !SameAccount(snapshot.Identity, original.Identity) {
		t.Fatalf("native token rotation rejected: %+v err=%v", snapshot, err)
	}
	serialized, err := json.Marshal(struct {
		Grant    Grant
		Snapshot CredentialSnapshot
	}{original, snapshot})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"live-refresh", "rotated-refresh", original.RefreshFingerprint, original.CredentialFingerprint, "GROK_HOME"} {
		if strings.Contains(string(serialized), secret) {
			t.Errorf("serialized sensitive value %q", secret)
		}
	}
}

func TestReadCredentialMalformedControls(t *testing.T) {
	for _, tc := range []struct{ name, provider, credential, state string }{
		{"null object", "claude", `null`, ""},
		{"missing OAuth grant", "claude", `{}`, ""},
		{"null OAuth grant", "claude", `{"claudeAiOauth":null}`, ""},
		{"null access", "claude", `{"claudeAiOauth":{"accessToken":null,"refreshToken":"r"}}`, ""},
		{"empty access", "claude", `{"claudeAiOauth":{"accessToken":"","refreshToken":"r"}}`, ""},
		{"null refresh", "claude", `{"claudeAiOauth":{"accessToken":"a","refreshToken":null}}`, ""},
		{"null expiry", "claude", `{"claudeAiOauth":{"accessToken":"a","expiresAt":null}}`, ""},
		{"negative expiry", "claude", `{"claudeAiOauth":{"accessToken":"a","expiresAt":-1}}`, ""},
		{"fractional expiry", "claude", `{"claudeAiOauth":{"accessToken":"a","expiresAt":123.5}}`, ""},
		{"overflow expiry", "grok", `{"key":"a","expires_at":99999999999999999999999999}`, ""},
		{"malformed time", "grok", `{"key":"a","expires_at":"2026-99-99T12:00:00Z"}`, ""},
		{"conflicting expiry", "grok", `{"key":"a","expires_at":"2026-10-06T22:00:00Z","expiry":"2026-10-06T21:00:00Z"}`, ""},
		{"conflicting token aliases", "grok", `{"key":"a","access_token":"b"}`, ""},
		{"multiple native entries", "grok", `{"issuer1":{"key":"a","user_id":"a"},"issuer2":{"key":"b","user_id":"b"}}`, ""},
		{"null identity", "grok", `{"key":"a","user_id":null}`, ""},
		{"bad paired state", "claude", `{"claudeAiOauth":{"accessToken":"a"}}`, `{"oauthAccount":`},
		{"conflicting paired account", "claude", `{"claudeAiOauth":{"accessToken":"a","accountId":"a","email":"a@example.test"}}`, `{"oauthAccount":{"accountUuid":"b","emailAddress":"a@example.test"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			authPath, statePath := filepath.Join(root, "auth.json"), ""
			writeDiscoveryFile(t, authPath, tc.credential)
			if tc.state != "" {
				statePath = filepath.Join(root, ".claude.json")
				writeDiscoveryFile(t, statePath, tc.state)
			}
			if _, err := readCredentialFiles(tc.provider, authPath, statePath); err == nil {
				t.Fatal("malformed credential accepted")
			}
		})
	}
	if _, err := readCredentialFiles("grok", filepath.Join(t.TempDir(), "missing-auth.json"), ""); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing file error loses classification: %v", err)
	}
}

func TestReadCredentialUsesPairedClaudeState(t *testing.T) {
	opts := discoveryFixture(t)
	opts.Provider = "claude"
	writeDiscoveryShallow(t, opts, "work", "actual", "actual@example.test", "actual-refresh")
	workHome := filepath.Join(opts.ShallowBase, "work")
	// This historical nested file may be shared by the shallow symlink farm.
	// Its identity must not outrank the shallow HOME's own account state.
	writeDiscoveryFile(t, filepath.Join(workHome, ".claude", ".claude.json"), `{"oauthAccount":{"accountUuid":"stale","emailAddress":"stale@example.test"}}`)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(workHome, ".claude"))
	grants, err := Discover(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	work := findDiscoveryGrant(t, grants, "shallow:claude/work")
	if work.Identity.AccountID != "actual" {
		t.Fatalf("wrong paired state: %+v", work.Identity)
	}
}

func TestSameAccountAuthority(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b AccountIdentity
		want bool
	}{
		{"known IDs disagree", AccountIdentity{"one", "same@example.test"}, AccountIdentity{"two", "same@example.test"}, false},
		{"known ID survives email change", AccountIdentity{"one", "old@example.test"}, AccountIdentity{"one", "new@example.test"}, true},
		{"email fallback", AccountIdentity{"", "Same@Example.test"}, AccountIdentity{"one", "same@example.test"}, true},
		{"both unknown", AccountIdentity{}, AccountIdentity{}, false},
		{"one unknown", AccountIdentity{}, AccountIdentity{"one", "one@example.test"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if SameAccount(tc.a, tc.b) != tc.want {
				t.Fatal("wrong account comparison")
			}
		})
	}
}
