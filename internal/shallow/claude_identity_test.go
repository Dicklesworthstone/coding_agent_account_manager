package shallow

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func shallowIdentityCredential(t *testing.T, dir, token, org, metadataName string) string {
	t.Helper()
	path := filepath.Join(dir, ".credentials.json")
	// Expired credentials must retain a previously verified identity offline.
	data := `{"claudeAiOauth":{"accessToken":"synthetic-` + token + `","refreshToken":"synthetic-refresh-` + token + `","expiresAt":1700000000000,"subscriptionType":"team"},"unrelated":9007199254740993}`
	writeFile(t, path, data)
	if metadataName != "" {
		fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		binding := &identity.ClaudeIdentityBinding{
			Identity: identity.Identity{
				Provider: "claude", AccountID: "same-account", OrganizationID: org,
				Email: "shared@example.com", Organization: org, Verified: true,
			},
			CredentialFingerprint: fingerprint,
			VerifiedAt:            time.Unix(1699990000, 0).UTC(),
		}
		metadata, err := marshalClaudeIdentityCache(binding)
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, metadataName), string(metadata))
	}
	return path
}

func TestShallowClaudeSeedKeepsPrivateTokenBoundOrganization(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	mgr, host := onboardingEnv(t, `{"theme":"dark","oauthAccount":{"accountUuid":"same-account","organizationUuid":"personal"}}`)
	hostCredential := shallowIdentityCredential(t, filepath.Join(host, ".claude"), "host", "personal", identity.ClaudeIdentityCacheFile)
	hostCache := filepath.Join(host, ".claude", identity.ClaudeIdentityCacheFile)
	hostMeta := filepath.Join(host, ".claude", "meta.json")
	writeFile(t, hostMeta, readFile(t, hostCache))
	hostAuth := filepath.Join(host, ".claude", "auth.json")
	hostNested := filepath.Join(host, ".claude", ".claude.json")
	writeFile(t, hostAuth, `{"accessToken":"synthetic-host-alternate"}`)
	writeFile(t, hostNested, `{"oauthAccount":{"organizationUuid":"personal","emailAddress":"shared@example.com"},"apiKey":"synthetic-host-key"}`)
	sourceDir := t.TempDir()
	source := shallowIdentityCredential(t, sourceDir, "team", "team", "meta.json")
	state := filepath.Join(sourceDir, ".claude.json")
	writeFile(t, state, `{"oauthAccount":{"accountUuid":"same-account","organizationUuid":"personal","emailAddress":"shared@example.com"},"hasCompletedOnboarding":true,"theme":"light","projects":{"/work":{"lastSessionId":"team-session","large":9007199254740993}}}`)
	before := make(map[string]string)
	for _, path := range []string{source, state, filepath.Join(sourceDir, "meta.json"), hostCredential, hostCache, hostMeta, hostAuth, hostNested} {
		before[path] = readFile(t, path)
	}
	home, err := mgr.Create("team", CreateOptions{CredentialSource: source, SourceClaudeJSON: state})
	if err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(home, ".claude", ".credentials.json")
	cache := filepath.Join(home, ".claude", identity.ClaudeIdentityCacheFile)
	info, err := os.Lstat(cache)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("identity cache is not private mode 0600: %v", err)
	}
	for _, name := range []string{"meta.json", "auth.json", ".claude.json"} {
		if _, err := os.Lstat(filepath.Join(home, ".claude", name)); !os.IsNotExist(err) {
			t.Fatalf("shallow profile inherited host account file %s: %v", name, err)
		}
	}
	if got := readFile(t, credential); got != before[source] {
		t.Fatal("seeding changed the selected credential bytes")
	}
	got := readProfileClaudeJSON(t, home)
	account := parseJSONObject(t, got["oauthAccount"])
	if string(account["organizationUuid"]) != `"team"` || string(account["emailAddress"]) != `"shared@example.com"` {
		t.Fatalf("stale source label survived verified seeding: %s", got["oauthAccount"])
	}
	if !rawJSONEqual(got["projects"], parseJSONObject(t, []byte(before[state]))["projects"]) {
		t.Fatal("seeding changed unrelated session state or JSON number precision")
	}
	restarted, err := NewManager(mgr.baseDir, host)
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.EnsureClaudeSettingsPrivate("team"); err != nil {
		t.Fatal(err)
	}
	id, err := identity.ExtractFromClaudeCredentials(credential)
	if err != nil || id == nil || !id.Verified || id.OrganizationID != "team" {
		t.Fatalf("offline restart lost selected organization: %#v, %v", id, err)
	}
	for path, body := range before {
		if readFile(t, path) != body {
			t.Fatalf("seeding changed source %s", path)
		}
	}
}

func TestShallowClaudeSeedDropsUnverifiedLabelAndRejectsSavedSubstitution(t *testing.T) {
	for _, metadataName := range []string{identity.ClaudeIdentityCacheFile, "meta.json"} {
		t.Run(metadataName, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, _ := onboardingEnv(t, `{}`)
			sourceDir := t.TempDir()
			source := shallowIdentityCredential(t, sourceDir, "old", "team", metadataName)
			// The label and proof came from Team, but another session replaced
			// the access token. An unchanged refresh token is not proof.
			writeFile(t, source, strings.Replace(readFile(t, source), `"accessToken":"synthetic-old"`, `"accessToken":"synthetic-other"`, 1))
			state := filepath.Join(sourceDir, ".claude.json")
			writeFile(t, state, `{"oauthAccount":{"accountUuid":"same-account","organizationUuid":"team"},"theme":"light","hasCompletedOnboarding":true}`)
			home, err := mgr.Create("selected", CreateOptions{CredentialSource: source, SourceClaudeJSON: state})
			if metadataName == "meta.json" {
				if err == nil || !strings.Contains(err.Error(), "does not match") {
					t.Fatalf("saved-profile substitution was accepted: %v", err)
				}
				if _, err := os.Lstat(filepath.Join(mgr.baseDir, "selected")); !os.IsNotExist(err) {
					t.Fatal("rejected saved identity published a shallow profile")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := readProfileClaudeJSON(t, home)
			if got["oauthAccount"] != nil || string(got["theme"]) != `"light"` {
				t.Fatalf("uncertain seed kept stale identity or lost settings: %v", got)
			}
			id, err := identity.ExtractFromClaudeCredentials(filepath.Join(home, ".claude", ".credentials.json"))
			if err != nil || id.Verified || id.OrganizationID != "" {
				t.Fatalf("unverified seed acquired an organization: %#v, %v", id, err)
			}
		})
	}
}

func TestShallowClaudeCreateRejectsContradictorySecondaryIdentity(t *testing.T) {
	for _, scenario := range []string{"different organization", "malformed secondary"} {
		t.Run(scenario, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			mgr, _ := onboardingEnv(t, `{}`)
			dir := t.TempDir()
			source := shallowIdentityCredential(t, dir, "same-grant", "team", "meta.json")
			cache := filepath.Join(dir, identity.ClaudeIdentityCacheFile)
			if scenario == "different organization" {
				shallowIdentityCredential(t, dir, "same-grant", "personal", identity.ClaudeIdentityCacheFile)
			} else {
				writeFile(t, cache, `{"claude_identity":`)
			}
			before := make(map[string]string)
			for _, path := range []string{source, cache, filepath.Join(dir, "meta.json")} {
				before[path] = readFile(t, path)
			}
			if _, err := mgr.Create("selected", CreateOptions{CredentialSource: source}); err == nil {
				t.Fatal("shallow creation erased contradictory secondary ownership evidence")
			}
			if _, err := os.Lstat(filepath.Join(mgr.baseDir, "selected")); !os.IsNotExist(err) {
				t.Fatal("rejected ownership published a shallow profile")
			}
			for path, expected := range before {
				if readFile(t, path) != expected {
					t.Fatal("rejected creation changed source ownership evidence")
				}
			}
		})
	}
}

func TestShallowClaudeSeedGuardsSecondaryIdentityAfterVerifiedPrimary(t *testing.T) {
	for _, initiallyPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "present"}[initiallyPresent], func(t *testing.T) {
			dir := t.TempDir()
			source := shallowIdentityCredential(t, dir, "same-grant", "team", "meta.json")
			if initiallyPresent {
				shallowIdentityCredential(t, dir, "same-grant", "team", identity.ClaudeIdentityCacheFile)
			}
			seed, err := captureClaudeCredentialSeed(source)
			if err != nil || seed.binding == nil || seed.binding.Identity.OrganizationID != "team" {
				t.Fatalf("capture verified source: %v", err)
			}
			shallowIdentityCredential(t, dir, "same-grant", "personal", identity.ClaudeIdentityCacheFile)
			if err := seed.checkUnchanged(); err == nil {
				t.Fatal("a secondary ownership record changed without invalidating the captured source")
			}
		})
	}
}

func TestShallowClaudeIdentityRepairDetachesHostCache(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link creation requires privileges on Windows")
	}
	for _, linkKind := range []string{"symlink", "hardlink", "dangling"} {
		for _, sameGrant := range []bool{false, true} {
			t.Run(linkKind+"/same-grant="+map[bool]string{true: "yes", false: "no"}[sameGrant], func(t *testing.T) {
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				mgr, host := onboardingEnv(t, `{}`)
				home, err := mgr.Create("team", CreateOptions{})
				if err != nil {
					t.Fatal(err)
				}
				credential := shallowIdentityCredential(t, filepath.Join(home, ".claude"), "team", "team", "")
				token, org := "host", "personal"
				if sameGrant {
					token, org = "team", "team"
				}
				hostCredential := shallowIdentityCredential(t, filepath.Join(host, ".claude"), token, org, identity.ClaudeIdentityCacheFile)
				hostCache := filepath.Join(host, ".claude", identity.ClaudeIdentityCacheFile)
				hostMeta := filepath.Join(host, ".claude", "meta.json")
				writeFile(t, hostMeta, readFile(t, hostCache))
				if linkKind == "dangling" {
					if err := os.Rename(hostCache, hostCache+".saved"); err != nil {
						t.Fatal(err)
					}
				}
				cache := filepath.Join(home, ".claude", identity.ClaudeIdentityCacheFile)
				link := os.Symlink
				if linkKind == "hardlink" {
					link = os.Link
				}
				if err := link(hostCache, cache); err != nil {
					t.Fatal(err)
				}
				if err := link(hostMeta, filepath.Join(home, ".claude", "meta.json")); err != nil {
					t.Fatal(err)
				}
				beforeCredential, beforeHost := readFile(t, credential), readFile(t, hostCredential)
				beforeMeta := readFile(t, hostMeta)
				if err := mgr.ValidateClaudeConfigBoundary("team"); err == nil {
					t.Fatal("read-only export accepted shared identity metadata")
				}
				// Both spawn paths perform the mandatory privacy repair.
				if sameGrant {
					_, err = mgr.SyncClaudeConfig("team")
				} else {
					err = mgr.EnsureClaudeSettingsPrivate("team")
				}
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(cache)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
					t.Fatalf("cache did not become private: %v", err)
				}
				id, err := identity.ExtractFromClaudeCredentials(credential)
				if err != nil || id == nil {
					t.Fatalf("read repaired identity: %v", err)
				}
				wantVerified := sameGrant && linkKind != "dangling"
				if id.Verified != wantVerified || wantVerified && id.OrganizationID != "team" || !wantVerified && id.OrganizationID != "" {
					t.Fatalf("repair borrowed the wrong organization: %#v", id)
				}
				if err := mgr.ValidateClaudeConfigBoundary("team"); err != nil {
					t.Fatal(err)
				}
				if readFile(t, credential) != beforeCredential || readFile(t, hostCredential) != beforeHost || readFile(t, hostMeta) != beforeMeta {
					t.Fatal("cache repair changed credentials or host metadata")
				}
				// Later native logins must not change a repaired profile's proof.
				cacheBefore := readFile(t, cache)
				shallowIdentityCredential(t, filepath.Join(host, ".claude"), "later", "personal", identity.ClaudeIdentityCacheFile)
				if err := mgr.EnsureClaudeSettingsPrivate("team"); err != nil {
					t.Fatal(err)
				}
				afterInfo, err := os.Stat(cache)
				if err != nil || !os.SameFile(info, afterInfo) || readFile(t, cache) != cacheBefore {
					t.Fatal("repeat launch changed private identity metadata")
				}
			})
		}
	}
}

func TestShallowClaudeIdentityRepairRejectsConcurrentChanges(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	for _, change := range []string{"credentials", "cache"} {
		t.Run(change, func(t *testing.T) {
			mgr, host := onboardingEnv(t, `{}`)
			home, err := mgr.Create("team", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			credential := shallowIdentityCredential(t, filepath.Join(home, ".claude"), "team", "team", "")
			shallowIdentityCredential(t, filepath.Join(host, ".claude"), "host", "personal", identity.ClaudeIdentityCacheFile)
			cache := filepath.Join(home, ".claude", identity.ClaudeIdentityCacheFile)
			hostCache := filepath.Join(host, ".claude", identity.ClaudeIdentityCacheFile)
			if err := os.Symlink(hostCache, cache); err != nil {
				t.Fatal(err)
			}
			repairs, err := mgr.prepareClaudeIdentityRepairs(home)
			if err != nil {
				t.Fatal(err)
			}
			if change == "credentials" {
				shallowIdentityCredential(t, filepath.Dir(credential), "new", "team", "")
			} else {
				if err := writeFileAtomic(cache, []byte(`{"concurrent":true}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			beforeCredential, beforeCache := readFile(t, credential), readFile(t, cache)
			if err := mgr.applyClaudeIdentityRepairs(home, repairs); err == nil {
				t.Fatal("cache repair overwrote a concurrently replaced generation")
			}
			if readFile(t, credential) != beforeCredential || readFile(t, cache) != beforeCache {
				t.Fatal("rejected repair changed a concurrent edit")
			}
		})
	}
}

func TestShallowClaudeIdentityBoundaryRejectsOtherProfileLinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	for _, filename := range claudePrivateAccountFiles {
		t.Run(filename, func(t *testing.T) {
			mgr, _ := onboardingEnv(t, `{}`)
			home, err := mgr.Create("team", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			credential := shallowIdentityCredential(t, filepath.Join(home, ".claude"), "team", "team", "")
			other := t.TempDir()
			shallowIdentityCredential(t, other, "team", "team", filename)
			target := filepath.Join(other, filename)
			path := filepath.Join(home, ".claude", filename)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
			before, beforeCredential := readFile(t, target), readFile(t, credential)
			if err := mgr.ValidateClaudeConfigBoundary("team"); err == nil {
				t.Fatal("environment export accepted another profile's identity file")
			}
			if err := mgr.EnsureClaudeSettingsPrivate("team"); err == nil {
				t.Fatal("no-sync launch silently repaired an unrecognized identity link")
			}
			if _, err := mgr.SyncClaudeConfig("team"); err == nil {
				t.Fatal("normal launch accepted another profile's identity file")
			}
			info, err := os.Lstat(path)
			if err != nil || info.Mode()&os.ModeSymlink == 0 || readFile(t, target) != before || readFile(t, credential) != beforeCredential {
				t.Fatal("refused cross-profile link changed a source or destination")
			}
		})
	}
}

func TestShallowClaudeDetachesHostAlternateAuthentication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("link creation requires privileges on Windows")
	}
	for _, linkKind := range []string{"symlink", "hardlink"} {
		t.Run(linkKind, func(t *testing.T) {
			mgr, host := onboardingEnv(t, `{}`)
			home, err := mgr.Create("team", CreateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			credential := shallowIdentityCredential(t, filepath.Join(home, ".claude"), "team", "team", identity.ClaudeIdentityCacheFile)
			hostFiles := map[string]string{
				"auth.json":    `{"accessToken":"synthetic-host-alternate","accountId":"host-account"}`,
				".claude.json": `{"oauthAccount":{"organizationUuid":"personal","emailAddress":"shared@example.com"},"apiKey":"synthetic-host-key"}`,
			}
			link := os.Symlink
			if linkKind == "hardlink" {
				link = os.Link
			}
			for name, body := range hostFiles {
				source := filepath.Join(host, ".claude", name)
				writeFile(t, source, body)
				if err := link(source, filepath.Join(home, ".claude", name)); err != nil {
					t.Fatal(err)
				}
			}
			if err := mgr.ValidateClaudeConfigBoundary("team"); err == nil {
				t.Fatal("export accepted inherited alternate authentication")
			}
			if err := mgr.EnsureClaudeSettingsPrivate("team"); err != nil {
				t.Fatal(err)
			}
			for name, body := range hostFiles {
				path := filepath.Join(home, ".claude", name)
				info, err := os.Lstat(path)
				if name == "auth.json" {
					if !os.IsNotExist(err) {
						t.Fatalf("inherited alternate credential was not retired: %v", err)
					}
				} else if err != nil || !info.Mode().IsRegular() || readFile(t, path) != `{}` {
					t.Fatalf("inherited %s did not become an empty private document: %v", name, err)
				}
				if readFile(t, filepath.Join(host, ".claude", name)) != body {
					t.Fatalf("repair changed host %s", name)
				}
			}
			// A present empty auth.json would invalidate this otherwise usable
			// captured OAuth grant; a retired inherited alias must stay absent.
			fileSet := authfile.AuthFileSet{Tool: "claude", Files: []authfile.AuthFileSpec{
				{Tool: "claude", Path: credential, Required: true},
				{Tool: "claude", Path: filepath.Join(home, ".claude", "auth.json")},
			}}
			vault := authfile.NewVault(t.TempDir())
			if err := vault.Backup(fileSet, "team"); err != nil {
				t.Fatalf("repaired OAuth profile cannot be backed up: %v", err)
			}
			if err := vault.ValidateProfileCredentials(fileSet, "team"); err != nil {
				t.Fatalf("repaired OAuth profile is invalid: %v", err)
			}
			for name := range hostFiles {
				// Once private, a later profile-native login owns this file.
				// Repeat launch must preserve its credentials and session state.
				private := `{"profileField":"team","large":9007199254740993}`
				writeFile(t, filepath.Join(home, ".claude", name), private)
			}
			if err := mgr.EnsureClaudeSettingsPrivate("team"); err != nil {
				t.Fatal(err)
			}
			for name := range hostFiles {
				if readFile(t, filepath.Join(home, ".claude", name)) != `{"profileField":"team","large":9007199254740993}` {
					t.Fatalf("launch rewrote private alternate %s", name)
				}
			}
			id, err := identity.ExtractFromClaudeCredentials(credential)
			if err != nil || id == nil || !id.Verified || id.OrganizationID != "team" {
				t.Fatalf("alternate-file repair changed verified identity: %#v, %v", id, err)
			}
		})
	}
}

func TestShallowClaudeSourceGenerationGuard(t *testing.T) {
	for _, changedFile := range []string{".credentials.json", "meta.json"} {
		t.Run(changedFile, func(t *testing.T) {
			dir := t.TempDir()
			credential := shallowIdentityCredential(t, dir, "team", "team", "meta.json")
			seed, err := captureClaudeCredentialSeed(credential)
			if err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(dir, changedFile), `{}`)
			if err := seed.checkUnchanged(); err == nil {
				t.Fatalf("staging accepted concurrent %s replacement", changedFile)
			}
		})
	}
}
