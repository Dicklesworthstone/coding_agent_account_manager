package keepalive

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func writeSyncFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func syncCredentialJSON(t *testing.T, provider, token string, owner AccountIdentity, expires time.Time) []byte {
	t.Helper()
	var payload any
	if provider == "claude" {
		oauth := map[string]any{"accessToken": token, "refreshToken": "synthetic-refresh-" + token}
		if owner.AccountID != "" {
			oauth["accountId"] = owner.AccountID
		}
		if owner.Email != "" {
			oauth["email"] = owner.Email
		}
		if owner.OrganizationID != "" {
			oauth["organizationUuid"] = owner.OrganizationID
		}
		if !expires.IsZero() {
			oauth["expiresAt"] = expires.UnixMilli()
		}
		payload = map[string]any{"claudeAiOauth": oauth}
	} else {
		oauth := map[string]any{"key": token, "refresh_token": "synthetic-refresh-" + token}
		if owner.AccountID != "" {
			oauth["user_id"] = owner.AccountID
		}
		if owner.Email != "" {
			oauth["email"] = owner.Email
		}
		if !expires.IsZero() {
			oauth["expires_at"] = expires.Format(time.RFC3339Nano)
		}
		payload = map[string]any{"synthetic-issuer::client": oauth}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func syncIdentityJSON(t *testing.T, owner AccountIdentity) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"oauthAccount":           map[string]any{"accountUuid": owner.AccountID, "organizationUuid": owner.OrganizationID, "emailAddress": owner.Email},
		"hasCompletedOnboarding": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeSyncBinding(t *testing.T, authPath string, credentials []byte, owner AccountIdentity, native bool) {
	t.Helper()
	fingerprint, err := identity.ClaudeCredentialFingerprint(credentials)
	if err != nil {
		t.Fatal(err)
	}
	if owner.AccountID == "" || owner.OrganizationID == "" {
		t.Fatal("test binding requires both stable identity components")
	}
	path := filepath.Join(filepath.Dir(authPath), "meta.json")
	if native {
		path = filepath.Join(filepath.Dir(authPath), identity.ClaudeIdentityCacheFile)
	}
	metadata := map[string]any{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &metadata); err != nil {
			t.Fatal(err)
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	metadata["claude_identity"] = &identity.ClaudeIdentityBinding{
		Identity:              identity.Identity{Provider: "claude", AccountID: owner.AccountID, OrganizationID: owner.OrganizationID, Email: owner.Email},
		CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC(),
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, path, data)
}

func syncFixture(t *testing.T, provider string) (Grant, CredentialSnapshot, *authfile.Vault) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "live")
	authPath := filepath.Join(home, ".grok", "auth.json")
	identityPath := ""
	env := map[string]string{"HOME": home, "GROK_HOME": filepath.Dir(authPath)}
	owner := AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}
	if provider == "claude" {
		owner.OrganizationID = "org-a"
		authPath = filepath.Join(home, ".claude", ".credentials.json")
		identityPath = filepath.Join(home, ".claude.json")
		env = map[string]string{"HOME": home}
		writeSyncFile(t, identityPath, syncIdentityJSON(t, owner))
	}
	writeSyncFile(t, authPath, syncCredentialJSON(t, provider, "live-access", owner, time.Now().Add(4*time.Hour).UTC().Truncate(time.Second)))
	if provider == "claude" {
		writeSyncBinding(t, authPath, readSyncFile(t, authPath), owner, true)
	}
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	grant := inspectGrant(Grant{Provider: provider, Kind: "host", Home: home, AuthPath: authPath,
		IdentityPath: identityPath, Env: env}, []string{vault.BasePath()})
	if grant.BlockedReason != "" {
		t.Fatalf("synthetic live fixture blocked: %s", grant.BlockedReason)
	}
	observed, err := ReadCredential(grant)
	if err != nil {
		t.Fatal(err)
	}
	return grant, observed, vault
}

func readSyncFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSyncVaultCopiesOnlyNewerCredentialsOfTheSameAccount(t *testing.T) {
	for _, provider := range []string{"claude", "grok"} {
		t.Run(provider, func(t *testing.T) {
			cases := []struct {
				name         string
				profile      string
				owner        AccountIdentity
				expiryOffset time.Duration
				unknown      bool
				malformed    bool
				metadata     string
				wantCode     string
			}{
				{name: "same account", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, expiryOffset: -time.Hour, wantCode: "newer_live_credential"},
				{name: "known id wins over changed email", owner: AccountIdentity{AccountID: "account-a", Email: "old-address@example.test"}, expiryOffset: -time.Hour, wantCode: "newer_live_credential"},
				{name: "different ids cannot fall back to matching email", owner: AccountIdentity{AccountID: "account-b", Email: "alice@example.test"}, expiryOffset: -time.Hour, wantCode: "identity_mismatch"},
				{name: "email fallback when snapshot lacks id", owner: AccountIdentity{Email: "ALICE@example.test"}, expiryOffset: -time.Hour, wantCode: "newer_live_credential"},
				{name: "different email", owner: AccountIdentity{Email: "bob@example.test"}, expiryOffset: -time.Hour, wantCode: "identity_mismatch"},
				{name: "unknown identity", owner: AccountIdentity{}, expiryOffset: -time.Hour, wantCode: "identity_mismatch"},
				{name: "same expiry", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, wantCode: "not_newer"},
				{name: "newer saved copy", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, expiryOffset: time.Hour, wantCode: "not_newer"},
				{name: "unknown saved age", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, unknown: true, wantCode: "snapshot_expiry_unknown"},
				{name: "malformed saved credentials", malformed: true, wantCode: "snapshot_invalid"},
				{name: "original system snapshot", profile: "_original", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, expiryOffset: -time.Hour, wantCode: "system_profile"},
				{name: "automatic system snapshot", profile: "_backup_20261006", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, expiryOffset: -time.Hour, wantCode: "system_profile"},
				{name: "metadata marks system profile", owner: AccountIdentity{AccountID: "account-a", Email: "alice@example.test"}, metadata: `{"type":"system","created_by":"auto"}`, expiryOffset: -time.Hour, wantCode: "system_profile"},
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					grant, observed, vault := syncFixture(t, provider)
					if provider == "claude" && !tc.malformed {
						if tc.owner.AccountID != "" {
							tc.owner.OrganizationID = "org-a"
						} else {
							tc.wantCode = "snapshot_identity_unproven"
						}
					}
					profile := tc.profile
					if profile == "" {
						profile = "saved-account"
					}
					dir := vault.ProfilePath(provider, profile)
					authPath := filepath.Join(dir, filepath.Base(grant.AuthPath))
					expires := observed.ExpiresAt.Add(tc.expiryOffset)
					if tc.unknown {
						expires = time.Time{}
					}
					saved := syncCredentialJSON(t, provider, "saved-access", tc.owner, expires)
					if tc.malformed {
						saved = []byte(`{"key":null}`)
					}
					writeSyncFile(t, authPath, saved)
					metadata := []byte(tc.metadata)
					if tc.metadata == "" {
						metadata = []byte(`{"type":"user","description":"preserve my profile notes"}`)
					}
					writeSyncFile(t, filepath.Join(dir, "meta.json"), metadata)
					if provider == "claude" && tc.owner.AccountID != "" && !tc.malformed {
						writeSyncBinding(t, authPath, saved, tc.owner, false)
						metadata = readSyncFile(t, filepath.Join(dir, "meta.json"))
					}
					settings := []byte(`{"policy":"saved settings must survive"}`)
					writeSyncFile(t, filepath.Join(dir, "settings.json"), settings)
					results, err := SyncVault(context.Background(), grant, observed, vault)
					if err != nil {
						t.Fatal(err)
					}
					if len(results) != 1 || results[0].Profile != profile || results[0].Code != tc.wantCode {
						t.Fatalf("results = %+v; want code %s", results, tc.wantCode)
					}
					want := saved
					if tc.wantCode == "newer_live_credential" {
						want = observed.data
						if results[0].Status != "synced" {
							t.Fatalf("successful copy status = %s", results[0].Status)
						}
					}
					if got := readSyncFile(t, authPath); !bytes.Equal(got, want) {
						t.Fatal("saved credential changed contrary to identity/freshness decision")
					}
					if got := readSyncFile(t, grant.AuthPath); !bytes.Equal(got, observed.data) {
						t.Fatal("vault synchronization wrote into the live credential")
					}
					if provider == "claude" && tc.wantCode == "newer_live_credential" {
						binding, err := identity.ReadClaudeIdentityBinding(authPath)
						if err != nil || binding == nil || !binding.MatchesCredentials(observed.data) {
							t.Fatalf("updated credential lost its identity binding: %v", err)
						}
						var meta map[string]any
						if err := json.Unmarshal(readSyncFile(t, filepath.Join(dir, "meta.json")), &meta); err != nil || meta["description"] != "preserve my profile notes" {
							t.Fatal("identity update discarded profile notes")
						}
					} else if !bytes.Equal(readSyncFile(t, filepath.Join(dir, "meta.json")), metadata) {
						t.Fatal("rejected synchronization changed profile metadata")
					}
					if !bytes.Equal(readSyncFile(t, filepath.Join(dir, "settings.json")), settings) {
						t.Fatal("vault synchronization changed unrelated settings or profile metadata")
					}
					if info, err := os.Stat(authPath); err != nil || info.Mode().Perm() != 0600 {
						t.Fatalf("saved credential permissions changed: %v, %v", info, err)
					}
				})
			}
		})
	}
}

func TestSyncVaultUsesVerifiedClaudeIdentityAndTheActualLiveHome(t *testing.T) {
	grant, initial, vault := syncFixture(t, "claude")
	now := time.Now().UTC().Truncate(time.Second)
	// Current native Claude credentials can have opaque tokens and no identity;
	// their identity must come from proof bound to those particular tokens.
	live := syncCredentialJSON(t, "claude", "opaque-live", AccountIdentity{}, now.Add(8*time.Hour))
	writeSyncFile(t, grant.AuthPath, live)
	writeSyncBinding(t, grant.AuthPath, live, initial.Identity, true)
	observed, err := ReadCredential(grant)
	if err != nil {
		t.Fatal(err)
	}
	dir := vault.ProfilePath("claude", "alice")
	saved := syncCredentialJSON(t, "claude", "opaque-saved", AccountIdentity{}, now.Add(time.Hour))
	writeSyncFile(t, filepath.Join(dir, ".credentials.json"), saved)
	writeSyncBinding(t, filepath.Join(dir, ".credentials.json"), saved, initial.Identity, false)
	// An independent Claude session can leave a different organization label
	// alongside the saved credential. Correct only that label from the proof.
	accountState := []byte(`{"oauthAccount":{"accountUuid":"old-account","organizationUuid":"old-organization","organizationName":"Stale Team","emailAddress":"alice@example.test"},"hasCompletedOnboarding":true,"numStartups":7,"projects":{"/repo":{"allowedTools":["Read"],"lastSessionId":"saved-session"}},"permissions":{"allow":["Read"],"defaultMode":"auto"}}`)
	writeSyncFile(t, filepath.Join(dir, ".claude.json"), accountState)
	wrongHome := t.TempDir()
	wrongCredential := syncCredentialJSON(t, "claude", "unrelated-live", AccountIdentity{AccountID: "account-b", Email: "bob@example.test"}, now.Add(9*time.Hour))
	wrongPath := filepath.Join(wrongHome, ".claude", ".credentials.json")
	writeSyncFile(t, wrongPath, wrongCredential)
	t.Setenv("HOME", wrongHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(wrongHome, ".claude"))
	results, err := SyncVault(context.Background(), grant, observed, vault)
	if err != nil || len(results) != 1 || results[0].Status != "synced" {
		t.Fatalf("sync = %+v, %v", results, err)
	}
	if !bytes.Equal(readSyncFile(t, filepath.Join(dir, ".credentials.json")), live) {
		t.Fatal("did not copy the grant's own credential")
	}
	var before, after map[string]any
	if err := json.Unmarshal(accountState, &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(readSyncFile(t, filepath.Join(dir, ".claude.json")), &after); err != nil {
		t.Fatal(err)
	}
	wantAccount := map[string]any{
		"accountUuid": observed.Identity.AccountID, "organizationUuid": observed.Identity.OrganizationID,
		"emailAddress": observed.Identity.Email,
	}
	if !reflect.DeepEqual(after["oauthAccount"], wantAccount) {
		t.Fatal("saved account and organization were not corrected from the token-bound proof")
	}
	delete(before, "oauthAccount")
	delete(after, "oauthAccount")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("synchronization changed nonidentity Claude state")
	}
	if !bytes.Equal(readSyncFile(t, wrongPath), wrongCredential) || os.Getenv("HOME") != wrongHome || os.Getenv("CLAUDE_CONFIG_DIR") != filepath.Dir(wrongPath) {
		t.Fatal("sync changed the caller's unrelated home or environment")
	}
}

func TestSyncVaultSeparatesClaudeOrganizationsAndPersistsProof(t *testing.T) {
	grant, initial, vault := syncFixture(t, "claude")
	personal := initial.Identity
	personal.OrganizationID = "personal-organization"
	team := personal
	team.OrganizationID = "team-organization"
	live := syncCredentialJSON(t, "claude", "team-current", AccountIdentity{}, initial.ExpiresAt.Add(time.Hour))
	writeSyncFile(t, grant.AuthPath, live)
	writeSyncFile(t, grant.IdentityPath, syncIdentityJSON(t, personal)) // stale concurrent Claude session
	writeSyncBinding(t, grant.AuthPath, live, team, true)
	grant = inspectGrant(Grant{Provider: "claude", Kind: "host", Home: grant.Home, AuthPath: grant.AuthPath,
		IdentityPath: grant.IdentityPath, Env: grant.Env}, []string{vault.BasePath()})
	observed, err := ReadCredential(grant)
	if err != nil || observed.Identity.OrganizationID != team.OrganizationID {
		t.Fatalf("stale native metadata outranked token ownership: %+v, %v", observed.Identity, err)
	}
	saved := map[string][]byte{}
	for name, owner := range map[string]AccountIdentity{"personal": personal, "team": team} {
		path := filepath.Join(vault.ProfilePath("claude", name), ".credentials.json")
		saved[name] = syncCredentialJSON(t, "claude", name+"-saved", AccountIdentity{}, initial.ExpiresAt.Add(-time.Hour))
		writeSyncFile(t, path, saved[name])
		writeSyncBinding(t, path, saved[name], owner, false)
		// Both saved state files carry the same misleading label.
		writeSyncFile(t, filepath.Join(filepath.Dir(path), ".claude.json"), syncIdentityJSON(t, personal))
	}
	results, err := SyncVault(context.Background(), grant, observed, vault)
	if err != nil || len(results) != 2 {
		t.Fatalf("organization sync = %+v, %v", results, err)
	}
	for _, result := range results {
		want := "identity_mismatch"
		if result.Profile == "team" {
			want = "newer_live_credential"
		}
		if result.Code != want {
			t.Fatalf("organization %s: %+v; want %s", result.Profile, result, want)
		}
	}
	personalPath := filepath.Join(vault.ProfilePath("claude", "personal"), ".credentials.json")
	if !bytes.Equal(readSyncFile(t, personalPath), saved["personal"]) {
		t.Fatal("Team renewal overwrote the same-email personal profile")
	}
	restarted := authfile.NewVault(vault.BasePath())
	id, err := SnapshotIdentity(restarted, "claude", "team")
	if err != nil || !id.Verified || !SameAccount(id, team) {
		t.Fatalf("restart lost the exact-token organization proof: %+v, %v", id, err)
	}
	data, err := json.Marshal(results)
	if err != nil || strings.Contains(string(data), "team-current") || strings.Contains(string(data), "synthetic-refresh") {
		t.Fatal("sync result exposed credentials")
	}
}

func TestSyncVaultRejectsReplacedAccessTokenWithUnchangedRefresh(t *testing.T) {
	grant, observed, vault := syncFixture(t, "claude")
	path := filepath.Join(vault.ProfilePath("claude", "saved"), ".credentials.json")
	old := syncCredentialJSON(t, "claude", "saved-access", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
	writeSyncFile(t, path, old)
	writeSyncBinding(t, path, old, observed.Identity, false)
	var changed map[string]any
	if err := json.Unmarshal(observed.data, &changed); err != nil {
		t.Fatal(err)
	}
	changed["claudeAiOauth"].(map[string]any)["accessToken"] = "other-organization-access"
	data, err := json.Marshal(changed)
	if err != nil {
		t.Fatal(err)
	}
	writeSyncFile(t, grant.AuthPath, data)
	results, err := SyncVault(context.Background(), grant, observed, vault)
	if err != nil || len(results) != 1 || results[0].Code != "source_identity_unverified" {
		t.Fatalf("replaced access token reused an old identity binding: %+v, %v", results, err)
	}
	if !bytes.Equal(readSyncFile(t, path), old) {
		t.Fatal("mismatched access token escaped into the saved profile")
	}
}

func TestSyncVaultRefusesAmbiguousEmailOnlyLiveOwner(t *testing.T) {
	grant, observed, vault := syncFixture(t, "grok")
	owner := AccountIdentity{Email: "alice@example.test"}
	writeSyncFile(t, grant.AuthPath, syncCredentialJSON(t, "grok", "email-only", owner, observed.ExpiresAt))
	grant = inspectGrant(Grant{Provider: grant.Provider, Kind: grant.Kind, Home: grant.Home,
		AuthPath: grant.AuthPath, Env: grant.Env}, []string{vault.BasePath()})
	observed, err := ReadCredential(grant)
	if err != nil {
		t.Fatal(err)
	}
	before := make(map[string][]byte)
	for _, id := range []string{"account-a", "account-b"} {
		path := filepath.Join(vault.ProfilePath("grok", id), "auth.json")
		before[path] = syncCredentialJSON(t, "grok", "saved-"+id, AccountIdentity{AccountID: id, Email: owner.Email}, observed.ExpiresAt.Add(-time.Hour))
		writeSyncFile(t, path, before[path])
	}
	results, err := SyncVault(context.Background(), grant, observed, vault)
	if err != nil || len(results) != 1 || results[0].Code != "ambiguous_saved_account" {
		t.Fatalf("ambiguous saved owners = %+v, %v", results, err)
	}
	for path, data := range before {
		if !bytes.Equal(readSyncFile(t, path), data) {
			t.Fatal("ambiguous email-only identity overwrote a saved account")
		}
	}
}

func TestSyncVaultDoesNotLoseIdentityWhenClaudeTokensBecomeOpaque(t *testing.T) {
	grant, observed, vault := syncFixture(t, "claude")
	path := filepath.Join(vault.ProfilePath("claude", "alice"), ".credentials.json")
	old := syncCredentialJSON(t, "claude", "old-inline-identity", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
	writeSyncFile(t, path, old)
	writeSyncFile(t, grant.AuthPath, syncCredentialJSON(t, "claude", "opaque-modern-token", AccountIdentity{}, observed.ExpiresAt.Add(time.Hour)))
	writeSyncBinding(t, grant.AuthPath, readSyncFile(t, grant.AuthPath), observed.Identity, true)
	current, err := ReadCredential(grant)
	if err != nil {
		t.Fatal(err)
	}
	results, err := SyncVault(context.Background(), grant, current, vault)
	if err != nil || len(results) != 1 || results[0].Code != "snapshot_identity_unproven" {
		t.Fatalf("opaque-token transition = %+v, %v", results, err)
	}
	if !bytes.Equal(readSyncFile(t, path), old) {
		t.Fatal("opaque replacement erased the saved profile's only account identity")
	}
	if _, err := SnapshotIdentity(vault, "claude", "alice"); err == nil {
		t.Fatal("unverified inline identity was presented as the saved token's owner")
	}
}

func TestSyncVaultRefusesChangedOrUnusableLiveCredential(t *testing.T) {
	for _, tc := range []struct {
		name string
		exp  func(CredentialSnapshot) time.Time
		code string
	}{
		{name: "expired", exp: func(CredentialSnapshot) time.Time { return time.Now().Add(-time.Minute) }, code: "source_changed"},
		{name: "unknown expiry", exp: func(CredentialSnapshot) time.Time { return time.Time{} }, code: "source_changed"},
		{name: "same expiry different credential", exp: func(s CredentialSnapshot) time.Time { return s.ExpiresAt }, code: "source_changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			grant, observed, vault := syncFixture(t, "grok")
			path := filepath.Join(vault.ProfilePath("grok", "alice"), "auth.json")
			old := syncCredentialJSON(t, "grok", "saved", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
			writeSyncFile(t, path, old)
			writeSyncFile(t, grant.AuthPath, syncCredentialJSON(t, "grok", "changed", observed.Identity, tc.exp(observed)))
			results, err := SyncVault(context.Background(), grant, observed, vault)
			if err != nil || len(results) != 1 || results[0].Code != tc.code {
				t.Fatalf("unsafe source = %+v, %v", results, err)
			}
			if !bytes.Equal(readSyncFile(t, path), old) {
				t.Fatal("unproven live credential was saved")
			}
		})
	}
}

func TestSyncVaultRefusesLiveCredentialWithoutRefreshToken(t *testing.T) {
	for _, provider := range []string{"claude", "grok"} {
		for _, rereadObserved := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/observedWithoutRefresh=%t", provider, rereadObserved), func(t *testing.T) {
				grant, observed, vault := syncFixture(t, provider)
				path := filepath.Join(vault.ProfilePath(provider, "alice"), filepath.Base(grant.AuthPath))
				old := syncCredentialJSON(t, provider, "renewable-saved", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
				writeSyncFile(t, path, old)
				var replacement []byte
				if provider == "claude" {
					replacement = []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"access-only","accountId":"account-a","expiresAt":%d}}`, observed.ExpiresAt.Add(time.Hour).UnixMilli()))
				} else {
					replacement = []byte(fmt.Sprintf(`{"key":"access-only","user_id":"account-a","expires_at":%q}`, observed.ExpiresAt.Add(time.Hour).Format(time.RFC3339Nano)))
				}
				writeSyncFile(t, grant.AuthPath, replacement)
				if rereadObserved {
					var err error
					observed, err = ReadCredential(grant)
					if err != nil {
						t.Fatal(err)
					}
				}
				results, err := SyncVault(context.Background(), grant, observed, vault)
				if err != nil || len(results) != 1 || results[0].Status != "skipped" || results[0].Code != "source_not_renewable" {
					t.Fatalf("non-renewable live grant was accepted: %+v, %v", results, err)
				}
				if !bytes.Equal(readSyncFile(t, path), old) {
					t.Fatal("access-only live credential replaced a renewable saved snapshot")
				}
			})
		}
	}
}

func TestSnapshotIdentityIsReadOnlyAndRejectsUnsafePaths(t *testing.T) {
	grant, observed, vault := syncFixture(t, "claude")
	dir := vault.ProfilePath("claude", "alice")
	path := filepath.Join(dir, ".credentials.json")
	writeSyncFile(t, path, observed.data)
	writeSyncBinding(t, path, observed.data, observed.Identity, false)
	for _, profile := range []string{"../alice", "../../live", filepath.Join(dir, ".."), "missing"} {
		if _, err := SnapshotIdentity(vault, "claude", profile); err == nil {
			t.Errorf("accepted invalid or missing profile %q", profile)
		}
	}
	if _, err := SnapshotIdentity(vault, "../claude", "alice"); err == nil {
		t.Fatal("accepted an unsafe provider path")
	}
	id, err := SnapshotIdentity(vault, "claude", "alice")
	if err != nil || id != observed.Identity {
		t.Fatalf("identity = %+v, %v", id, err)
	}
	if !bytes.Equal(readSyncFile(t, path), observed.data) || !bytes.Equal(readSyncFile(t, grant.AuthPath), observed.data) {
		t.Fatal("diagnostic identity lookup changed credentials")
	}
	linkedDir := vault.ProfilePath("claude", "linked")
	if err := os.MkdirAll(linkedDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(grant.AuthPath, filepath.Join(linkedDir, ".credentials.json")); err != nil {
		t.Skipf("platform cannot create test symlink: %v", err)
	}
	if _, err := SnapshotIdentity(vault, "claude", "linked"); err == nil {
		t.Fatal("diagnostic lookup followed a credential symlink")
	}
}

func TestSyncVaultRereadsLiveOwnerAfterTheNativeCommand(t *testing.T) {
	for _, provider := range []string{"claude", "grok"} {
		t.Run(provider, func(t *testing.T) {
			grant, observed, vault := syncFixture(t, provider)
			path := filepath.Join(vault.ProfilePath(provider, "alice"), filepath.Base(grant.AuthPath))
			old := syncCredentialJSON(t, provider, "old", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
			writeSyncFile(t, path, old)
			// Keep the email identical: a changed account ID must still refuse.
			changed := AccountIdentity{AccountID: "account-b", OrganizationID: observed.Identity.OrganizationID, Email: observed.Identity.Email}
			writeSyncFile(t, grant.AuthPath, syncCredentialJSON(t, provider, "changed-owner", changed, observed.ExpiresAt.Add(time.Hour)))
			if provider == "claude" {
				writeSyncFile(t, grant.IdentityPath, syncIdentityJSON(t, changed))
				writeSyncBinding(t, grant.AuthPath, readSyncFile(t, grant.AuthPath), changed, true)
			}
			results, err := SyncVault(context.Background(), grant, observed, vault)
			if err != nil || len(results) != 1 || results[0].Code != "source_owner_changed" {
				t.Fatalf("changed-owner sync = %+v, %v", results, err)
			}
			if !bytes.Equal(readSyncFile(t, path), old) {
				t.Fatal("a changed live owner overwrote the saved account")
			}
		})
	}
}

func TestSyncVaultRereadsFilesAfterNativeLockContention(t *testing.T) {
	for _, change := range []string{"newer live rotation", "newer vault snapshot", "changed Claude sidecar"} {
		t.Run(change, func(t *testing.T) {
			grant, observed, vault := syncFixture(t, "claude")
			path := filepath.Join(vault.ProfilePath("claude", "alice"), ".credentials.json")
			old := syncCredentialJSON(t, "claude", "old", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
			writeSyncFile(t, path, old)
			writeSyncBinding(t, path, old, observed.Identity, false)
			lock, err := acquireNativeCredentialLock(context.Background(), grant)
			if err != nil {
				t.Fatal(err)
			}
			locked := true
			t.Cleanup(func() {
				if locked {
					closeCredentialLock(lock)
				}
			})
			type outcome struct {
				results []SyncResult
				err     error
			}
			done := make(chan outcome, 1)
			go func() {
				results, err := SyncVault(context.Background(), grant, observed, vault)
				done <- outcome{results, err}
			}()
			// These changes happen while the native writer owns the lock; the
			// saved-copy reader must observe them after it obtains that lock.
			want := old
			wantCode := "newer_live_credential"
			switch change {
			case "newer live rotation":
				want = syncCredentialJSON(t, "claude", "newest-live", observed.Identity, observed.ExpiresAt.Add(time.Hour))
				writeSyncFile(t, grant.AuthPath, want)
				writeSyncBinding(t, grant.AuthPath, want, observed.Identity, true)
			case "newer vault snapshot":
				want = syncCredentialJSON(t, "claude", "newest-vault", observed.Identity, observed.ExpiresAt.Add(time.Hour))
				writeSyncFile(t, path, want)
				writeSyncBinding(t, path, want, observed.Identity, false)
				wantCode = "not_newer"
			case "changed Claude sidecar":
				writeSyncFile(t, grant.IdentityPath, syncIdentityJSON(t, AccountIdentity{AccountID: "account-b", OrganizationID: "wrong-org", Email: "bob@example.test"}))
				// Bound token identity outranks the stale shared state file.
				want = observed.data
			}
			closeCredentialLock(lock)
			locked = false
			select {
			case got := <-done:
				if got.err != nil || len(got.results) != 1 || got.results[0].Code != wantCode {
					t.Fatalf("sync after contention = %+v, %v; want %s", got.results, got.err, wantCode)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("sync did not finish after releasing the native lock")
			}
			if !bytes.Equal(readSyncFile(t, path), want) {
				t.Fatal("sync did not preserve the newest same-account state")
			}
		})
	}
}

func TestSyncVaultNativeLockContentionDoesNotBreakTheLockOrWrite(t *testing.T) {
	grant, observed, vault := syncFixture(t, "grok")
	path := filepath.Join(vault.ProfilePath("grok", "alice"), "auth.json")
	old := syncCredentialJSON(t, "grok", "old", observed.Identity, observed.ExpiresAt.Add(-time.Hour))
	writeSyncFile(t, path, old)
	lock, err := acquireNativeCredentialLock(context.Background(), grant)
	if err != nil {
		t.Fatal(err)
	}
	defer closeCredentialLock(lock)
	lockInfo, err := lock.Stat()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	results, err := SyncVault(ctx, grant, observed, vault)
	if !errors.Is(err, context.DeadlineExceeded) || len(results) != 1 || results[0].Code != "native_lock_unavailable" {
		t.Fatalf("contended sync = %+v, %v", results, err)
	}
	currentInfo, err := os.Stat(nativeCredentialLockPath(grant))
	if err != nil || !os.SameFile(lockInfo, currentInfo) {
		t.Fatalf("native lock inode was replaced: %v", err)
	}
	if !bytes.Equal(readSyncFile(t, path), old) || !bytes.Equal(readSyncFile(t, grant.AuthPath), observed.data) {
		t.Fatal("lock contention changed a saved or live credential")
	}
	if holder := string(readSyncFile(t, nativeCredentialLockPath(grant))); !strings.HasPrefix(holder, fmt.Sprintf("%d:", os.Getpid())) {
		t.Fatalf("native lock did not record the PID:timestamp protocol: %q", holder)
	}
}

func TestReplaceSavedCredentialAbortsWhenSourceOrSnapshotChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	old := []byte(`{"key":"saved"}`)
	writeSyncFile(t, path, old)
	err := replaceSavedCredential(path, []byte(`{"key":"new"}`), func(string) error { return errSyncChanged })
	if !errors.Is(err, errSyncChanged) || !bytes.Equal(readSyncFile(t, path), old) {
		t.Fatalf("changed-source copy did not abort cleanly: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 || entries[0].Name() != "auth.json" {
		t.Fatalf("staged credential material was left behind: %v, %v", entries, err)
	}
}
