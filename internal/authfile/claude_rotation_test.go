package authfile

// Tests for issue #73: Claude Code rotates both OAuth tokens in place while a
// profile is active, so vault recognition (ActiveProfile), the outgoing
// re-snapshot, and the restore freshness guard must key on the account
// provider-confirmed account AND organization identity. Issue #132 establishes
// that ~/.claude.json can be stale even while the credentials are valid.

import (
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

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

const (
	rotAliceUUID  = "11111111-2222-3333-4444-555555555555"
	rotAliceEmail = "alice@example.com"
	rotAliceUser  = "useridhash-alice"
	rotAliceOrg   = "org-1"
	rotTeamOrg    = "org-team"
	rotBobUUID    = "66666666-7777-8888-9999-000000000000"
	rotBobEmail   = "bob@example.com"
)

// claudeRotationCreds renders a .credentials.json in Claude Code's shape.
func claudeRotationCreds(access, refresh string, expiresAt int64) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"scopes":["user:inference"],"subscriptionType":"max"}}`,
		access, refresh, expiresAt)
}

// claudeRotationSettings renders a .claude.json carrying the given identity
// fields (empty strings are omitted) plus volatile UI state.
func claudeRotationSettings(uuid, email, userID string, numStartups int) string {
	acct := map[string]interface{}{"organizationUuid": "org-1"}
	if uuid != "" {
		acct["accountUuid"] = uuid
	}
	if email != "" {
		acct["emailAddress"] = email
	}
	root := map[string]interface{}{
		"numStartups":          numStartups,
		"changelogLastFetched": 1754000000000,
		"oauthAccount":         acct,
	}
	if userID != "" {
		root["userID"] = userID
	}
	raw, err := json.Marshal(root)
	if err != nil {
		panic(err)
	}
	return string(raw)
}

type claudeRotationFixture struct {
	t            *testing.T
	vault        *Vault
	vaultDir     string
	fileSet      AuthFileSet
	liveCreds    string
	liveSettings string
	proofs       map[string]*identity.ClaudeIdentityBinding
}

func newClaudeRotationFixture(t *testing.T) *claudeRotationFixture {
	t.Helper()
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	f := &claudeRotationFixture{
		t:            t,
		vaultDir:     filepath.Join(tmp, "vault"),
		liveCreds:    filepath.Join(claudeDir, ".credentials.json"),
		liveSettings: filepath.Join(home, ".claude.json"),
	}
	f.vault = NewVault(f.vaultDir)
	// This oracle is deliberately independent of .claude.json and all embedded
	// credential labels. Each synthetic token pair has one provider identity.
	f.proofs = make(map[string]*identity.ClaudeIdentityBinding)
	for _, entry := range []struct {
		credentials  string
		account      string
		organization string
		email        string
	}{
		{aliceGen1, rotAliceUUID, rotAliceOrg, rotAliceEmail},
		{aliceGen2, rotAliceUUID, rotAliceOrg, rotAliceEmail},
		{aliceTeamGen1, rotAliceUUID, rotTeamOrg, rotAliceEmail},
		{bobGen1, rotBobUUID, rotAliceOrg, rotBobEmail},
		{claudeRotationCreds("sk-ant-oat01-bob-gen9", "sk-ant-ort01-bob-gen9", 9999), rotBobUUID, rotAliceOrg, rotBobEmail},
		{claudeRotationCreds("sk-ant-oat01-alice-dead", "", 2000), rotAliceUUID, rotAliceOrg, rotAliceEmail},
	} {
		fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(entry.credentials))
		if err != nil {
			t.Fatal(err)
		}
		f.proofs[fingerprint] = &identity.ClaudeIdentityBinding{
			Identity:              identity.Identity{Provider: "claude", AccountID: entry.account, OrganizationID: entry.organization, Email: entry.email},
			CredentialFingerprint: fingerprint,
			VerifiedAt:            time.Now().UTC(),
		}
	}
	f.vault.ClaudeIdentityVerifier = func(_ context.Context, credentials []byte) (*identity.ClaudeIdentityBinding, error) {
		fingerprint, err := identity.ClaudeCredentialFingerprint(credentials)
		if err != nil {
			return nil, err
		}
		binding := f.proofs[fingerprint]
		if binding == nil {
			return nil, identity.ErrClaudeIdentityUnavailable
		}
		copy := *binding
		return &copy, nil
	}
	f.fileSet = AuthFileSet{
		Tool: "claude",
		Files: []AuthFileSpec{
			{Tool: "claude", Path: f.liveCreds, Required: true},
			{Tool: "claude", Path: f.liveSettings, Required: false},
		},
	}
	return f
}

func writeFixtureFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readFixtureFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(raw)
}

// writeLive sets the live auth state; an empty settings string leaves
// ~/.claude.json absent.
func (f *claudeRotationFixture) writeLive(creds, settings string) {
	f.t.Helper()
	writeFixtureFile(f.t, f.liveCreds, creds)
	if settings != "" {
		writeFixtureFile(f.t, f.liveSettings, settings)
	}
	f.writeBinding(filepath.Join(filepath.Dir(f.liveCreds), identity.ClaudeIdentityCacheFile), creds)
}

// writeProfile creates a previously verified vault snapshot; an empty settings
// string omits .claude.json without discarding the token-bound provider proof.
func (f *claudeRotationFixture) writeProfile(name, creds, settings string) {
	f.t.Helper()
	dir := filepath.Join(f.vaultDir, "claude", name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		f.t.Fatal(err)
	}
	writeFixtureFile(f.t, filepath.Join(dir, ".credentials.json"), creds)
	if settings != "" {
		writeFixtureFile(f.t, filepath.Join(dir, ".claude.json"), settings)
	}
	f.writeBinding(filepath.Join(dir, "meta.json"), creds)
}

func (f *claudeRotationFixture) writeBinding(path, credentials string) {
	f.t.Helper()
	binding, err := f.vault.ClaudeIdentityVerifier(context.Background(), []byte(credentials))
	if err != nil {
		writeFixtureFile(f.t, path, `{}`)
		return
	}
	metadata, err := json.Marshal(map[string]interface{}{"claude_identity": binding})
	if err != nil {
		f.t.Fatal(err)
	}
	writeFixtureFile(f.t, path, string(metadata))
}

// setClaudeFixtureIdentities models the provider response for exact synthetic
// token pairs. The assignment is explicit; no local identity metadata or profile
// name is ever read to infer the answer the verifier is supposed to establish.
func setClaudeFixtureIdentities(t *testing.T, vault *Vault, accounts map[string]identity.Identity) {
	t.Helper()
	bindings := make(map[string]*identity.ClaudeIdentityBinding, len(accounts))
	for credentials, account := range accounts {
		fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(credentials))
		if err != nil {
			t.Fatal(err)
		}
		account.Provider, account.Verified = "claude", true
		if account.AccountID == "" || account.OrganizationID == "" {
			t.Fatal("fixture provider response must identify both account and organization")
		}
		bindings[fingerprint] = &identity.ClaudeIdentityBinding{
			Identity: account, CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC(),
		}
	}
	vault.ClaudeIdentityVerifier = func(_ context.Context, credentials []byte) (*identity.ClaudeIdentityBinding, error) {
		fingerprint, err := identity.ClaudeCredentialFingerprint(credentials)
		if err != nil {
			return nil, err
		}
		binding := bindings[fingerprint]
		if binding == nil {
			return nil, identity.ErrClaudeIdentityUnavailable
		}
		copy := *binding
		return &copy, nil
	}
}

func (f *claudeRotationFixture) profileFile(name, file string) string {
	return filepath.Join(f.vaultDir, "claude", name, file)
}

func (f *claudeRotationFixture) active() string {
	f.t.Helper()
	name, err := f.vault.ActiveProfile(f.fileSet)
	if err != nil {
		f.t.Fatalf("ActiveProfile: %v", err)
	}
	return name
}

var (
	aliceGen1     = claudeRotationCreds("sk-ant-oat01-alice-gen1", "sk-ant-ort01-alice-gen1", 1000)
	aliceGen2     = claudeRotationCreds("sk-ant-oat01-alice-gen2", "sk-ant-ort01-alice-gen2", 2000)
	bobGen1       = claudeRotationCreds("sk-ant-oat01-bob-gen1", "sk-ant-ort01-bob-gen1", 1000)
	aliceTeamGen1 = claudeRotationCreds("sk-ant-oat01-alice-team-gen1", "sk-ant-ort01-alice-team-gen1", 1000)
)

func aliceSettings(numStartups int) string {
	return claudeRotationSettings(rotAliceUUID, rotAliceEmail, rotAliceUser, numStartups)
}

func bobSettings(numStartups int) string {
	return claudeRotationSettings(rotBobUUID, rotBobEmail, "useridhash-bob", numStartups)
}

// --- ActiveProfile ---------------------------------------------------------

func TestActiveProfileRecognizesRotatedClaudeLogin(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	// Claude Code has since refreshed both tokens in place; only volatile
	// settings fields changed alongside.
	f.writeLive(aliceGen2, aliceSettings(9))

	liveHash, _ := stableFileHash("claude", f.liveCreds)
	snapHash, _ := stableFileHash("claude", f.profileFile("alice", ".credentials.json"))
	if liveHash == snapHash {
		t.Fatal("fixture error: rotated credentials must not hash-match the snapshot")
	}

	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice via identity fallback after rotation", got)
	}
}

func TestActiveProfileExactTokenMatchStillWinsOverIdentity(t *testing.T) {
	f := newClaudeRotationFixture(t)
	// Two profiles for the same account; "alice" sorts first but only
	// "alice-work" holds the live token generation byte-for-byte.
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("alice-work", aliceGen2, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))

	if got := f.active(); got != "alice-work" {
		t.Fatalf("ActiveProfile = %q, want the exact token match alice-work", got)
	}
}

func TestActiveProfileIdentityIgnoresOtherAccounts(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(bobGen1, bobSettings(3))

	if got := f.active(); got != "" {
		t.Fatalf("ActiveProfile = %q, want no match for a different account", got)
	}
}

func TestActiveProfileIdentityPrefersNamedOverSystemProfile(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("_backup_20260807_201859", aliceGen1, aliceSettings(1))
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))

	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile = %q, want the user-named profile over the system one", got)
	}

	// With only a system profile for the account, it is still better than nothing.
	g := newClaudeRotationFixture(t)
	g.writeProfile("_backup_20260807_201859", aliceGen1, aliceSettings(1))
	g.writeLive(aliceGen2, aliceSettings(9))
	if got := g.active(); got != "_backup_20260807_201859" {
		t.Fatalf("ActiveProfile = %q, want the lone system profile", got)
	}
}

func TestActiveProfileNamedIdentityWinsOverCurrentSystemSnapshot(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))
	if err := f.vault.Backup(f.fileSet, "_backup_current"); err != nil {
		t.Fatal(err)
	}
	if got := f.active(); got != "alice" {
		t.Fatalf("a current safety snapshot displaced named account alice: %q", got)
	}
}

func TestActiveProfileDoesNotMatchCredentiallessIdentity(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.fileSet.AllowOptionalOnly = true
	f.writeLive(aliceGen2, aliceSettings(9))
	profileDir := f.vault.ProfilePath("claude", "empty-alice")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(profileDir, ".claude.json"), aliceSettings(1))
	writeFixtureFile(t, filepath.Join(profileDir, "meta.json"), `{"identity_keys":["uuid:`+rotAliceUUID+`"]}`)
	if got := f.active(); got != "" {
		t.Fatalf("identity-only profile reported active: %q", got)
	}
}

func TestActiveProfileIdentityFromMetaWhenSnapshotLacksSettings(t *testing.T) {
	f := newClaudeRotationFixture(t)
	// Snapshot has no .claude.json (backed up while it was absent) but Backup
	// recorded provider-confirmed ownership of these exact tokens in meta.json.
	f.writeProfile("alice", aliceGen1, "")
	f.writeLive(aliceGen2, aliceSettings(9))

	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice matched via meta.json identity", got)
	}
}

func TestActiveProfileRejectsUnverifiedSharedEmail(t *testing.T) {
	f := newClaudeRotationFixture(t)
	// Older snapshot shape: email only, no accountUuid, no userID.
	f.writeProfile("alice", aliceGen1, claudeRotationSettings("", "Alice@Example.com", "", 1))
	writeFixtureFile(t, f.profileFile("alice", "meta.json"), `{"identity":"alice@example.com","identity_keys":["email:alice@example.com"]}`)
	f.writeLive(aliceGen2, aliceSettings(9))

	if got := f.active(); got != "" {
		t.Fatalf("ActiveProfile = %q, want no cross-generation match from an unverified email", got)
	}
}

func TestActiveProfileRejectsMissingOrganizationProof(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))
	metadata := readJSONMap(t, f.profileFile("alice", "meta.json"))
	binding := metadata["claude_identity"].(map[string]interface{})
	boundIdentity := binding["identity"].(map[string]interface{})
	delete(boundIdentity, "organization_id")
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, f.profileFile("alice", "meta.json"), string(data))
	if got := f.active(); got != "" {
		t.Fatalf("missing organization proof selected profile %q", got)
	}
}

func TestActiveProfileDistinguishesOrganizationsWithStaleSharedMetadata(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	// Both snapshots and the live metadata claim the personal organization.
	// The token oracle independently associates the Team grant with Team.
	f.writeProfile("team", aliceTeamGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))
	if got := f.active(); got != "personal" {
		t.Fatalf("personal grant selected %q with the same email in two organizations", got)
	}
	f.writeLive(aliceTeamGen1, aliceSettings(10))
	if got := f.active(); got != "team" {
		t.Fatalf("Team grant selected %q under stale personal metadata", got)
	}
}

func TestActiveProfileDoesNotReuseProofAfterConcurrentTokenReplacement(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))
	// A different session replaces the token without updating either shared
	// .claude.json or CAAM's previous token-bound cache.
	writeFixtureFile(t, f.liveCreds, claudeRotationCreds("sk-ant-oat01-bob-gen9", "sk-ant-ort01-bob-gen9", 9999))
	if got := f.active(); got != "" {
		t.Fatalf("concurrently replaced credentials inherited old owner %q", got)
	}
}

func TestActiveProfileRejectsConflictingExactClaudeOwners(t *testing.T) {
	for _, field := range []string{"account_id", "organization_id"} {
		for _, liveProof := range []bool{false, true} {
			for _, conflictingName := range []string{"another-label", "_backup_conflicting"} {
				t.Run(fmt.Sprintf("%s/live-proof=%t/%s", field, liveProof, conflictingName), func(t *testing.T) {
					f := newClaudeRotationFixture(t)
					f.writeProfile("a-first-label", aliceGen1, aliceSettings(1))
					f.writeProfile(conflictingName, aliceGen1, aliceSettings(1))
					f.writeLive(aliceGen1, aliceSettings(3))
					if !liveProof {
						writeFixtureFile(t, filepath.Join(filepath.Dir(f.liveCreds), identity.ClaudeIdentityCacheFile), `{}`)
					}
					metadata := readJSONMap(t, f.profileFile(conflictingName, "meta.json"))
					binding := metadata["claude_identity"].(map[string]interface{})
					binding["identity"].(map[string]interface{})[field] = "different-stable-id"
					data, err := json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					writeFixtureFile(t, f.profileFile(conflictingName, "meta.json"), string(data))
					for _, selectProfile := range []func(AuthFileSet) (string, error){f.vault.ActiveProfile, f.vault.CurrentProfile} {
						if name, err := selectProfile(f.fileSet); name != "" || !errors.Is(err, ErrInvalidCredentials) {
							t.Fatalf("conflicting exact-token proofs selected %q: %v", name, err)
						}
					}
				})
			}
		}
	}
}

func TestActiveProfileRejectsAmbiguousExactClaudeLabels(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal-label", aliceGen1, aliceSettings(1))
	f.writeProfile("work-label", aliceGen1, aliceSettings(1))
	f.writeProfile("_backup_same-token", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen1, aliceSettings(3))
	for _, selectProfile := range []func(AuthFileSet) (string, error){f.vault.ActiveProfile, f.vault.CurrentProfile} {
		if name, err := selectProfile(f.fileSet); name != "" || err != nil {
			t.Fatalf("a profile label resolved duplicate credentials: %q, %v", name, err)
		}
	}
}

func TestProfileSelectionDoesNotSkipBrokenOwnershipForExactClaudeToken(t *testing.T) {
	for _, broken := range []string{"conflicting-cache", "malformed-binding", "unreadable-optional"} {
		for _, sameToken := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/same-token=%t", broken, sameToken), func(t *testing.T) {
				f := newClaudeRotationFixture(t)
				f.writeProfile("a-valid", aliceGen1, aliceSettings(1))
				other := bobGen1
				if sameToken {
					other = aliceGen1
				}
				f.writeProfile("z-broken", other, "")
				f.writeLive(aliceGen1, aliceSettings(3))
				switch broken {
				case "malformed-binding":
					writeFixtureFile(t, f.profileFile("z-broken", "meta.json"), `{"claude_identity":{"identity":{"email":"shared@example.test"}}}`)
				case "unreadable-optional":
					// A bad later artifact must not hide the already captured
					// token pair before readSwitchState has identified it.
					if err := os.Mkdir(f.profileFile("z-broken", claudeSettingsFile), 0700); err != nil {
						t.Fatal(err)
					}
				case "conflicting-cache":
					metadata := readJSONMap(t, f.profileFile("z-broken", "meta.json"))
					binding := metadata["claude_identity"].(map[string]interface{})
					binding["identity"].(map[string]interface{})["organization_id"] = "conflicting-organization"
					data, err := json.Marshal(metadata)
					if err != nil {
						t.Fatal(err)
					}
					writeFixtureFile(t, f.profileFile("z-broken", identity.ClaudeIdentityCacheFile), string(data))
				}
				for _, selectProfile := range []func(AuthFileSet) (string, error){f.vault.ActiveProfile, f.vault.CurrentProfile} {
					name, err := selectProfile(f.fileSet)
					if sameToken {
						if name != "" || !errors.Is(err, ErrInvalidCredentials) {
							t.Fatalf("broken ownership for matching tokens was ignored: %q, %v", name, err)
						}
					} else if err != nil || name != "a-valid" {
						t.Fatalf("unrelated broken profile blocked active owner: %q, %v", name, err)
					}
				}
			})
		}
	}
}

func TestProfileSelectionRechecksConcurrentClaudeAuthentication(t *testing.T) {
	for _, method := range []string{"active", "current"} {
		for _, changed := range []string{"credential", "shared-label"} {
			t.Run(method+"/"+changed, func(t *testing.T) {
				f := newKeychainFixture(t)
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				f.storeToken(aliceGen1)
				writeFixtureFile(t, f.statePath, aliceSettings(1))
				writeSwitchProfile(t, f.vault, "claude", "selected-label", map[string]string{
					claudeCredentialsFile: aliceGen1, claudeSettingsFile: aliceSettings(1),
				})
				replacement, destination := aliceTeamGen1, filepath.Join(f.items, "Claude_Code_credentials.secret")
				if changed == "shared-label" {
					replacement, destination = bobSettings(99), f.statePath
				}
				replacementPath := filepath.Join(t.TempDir(), "replacement.json")
				writeFixtureFile(t, replacementPath, replacement)
				t.Setenv("CAAM_TEST_SECURITY_READER", os.Getenv("CAAM_KEYCHAIN_BIN"))
				t.Setenv("CAAM_TEST_IDENTITY_REPLACEMENT", replacementPath)
				t.Setenv("CAAM_TEST_IDENTITY_DESTINATION", destination)
				t.Setenv("CAAM_TEST_IDENTITY_READ_MARK", filepath.Join(t.TempDir(), "read-completed"))
				wrapper := filepath.Join(t.TempDir(), "security-concurrent-login")
				// The real selection entry point receives the first native
				// credential, then a separate native writer changes state before
				// the vault scan completes. No timing sleeps or helper-only seam.
				script := `#!/bin/sh
if [ "$1" = find-generic-password ] && [ ! -f "$CAAM_TEST_IDENTITY_READ_MARK" ]; then
  "$CAAM_TEST_SECURITY_READER" "$@" || exit $?
  cp "$CAAM_TEST_IDENTITY_REPLACEMENT" "$CAAM_TEST_IDENTITY_DESTINATION" || exit $?
  : > "$CAAM_TEST_IDENTITY_READ_MARK"
  exit 0
fi
exec "$CAAM_TEST_SECURITY_READER" "$@"
`
				if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CAAM_KEYCHAIN_BIN", wrapper)
				selectProfile := f.vault.ActiveProfile
				if method == "current" {
					selectProfile = f.vault.CurrentProfile
				}
				name, err := selectProfile(f.fileSet)
				if changed == "credential" {
					if err == nil || name != "" || !strings.Contains(err.Error(), "changed during profile selection") {
						t.Fatalf("native login change selected stale profile %q: %v", name, err)
					}
				} else if err != nil || name != "selected-label" {
					t.Fatalf("shared label rewrite changed credential selection to %q: %v", name, err)
				}
				if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
					t.Fatalf("read-only selection created a credential mirror: %v", err)
				}
			})
		}
	}
}

func TestActiveProfileDuringConcurrentSharedClaudeMetadataWrites(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	f.writeProfile("team", aliceTeamGen1, aliceSettings(1))
	f.writeLive(aliceTeamGen1, aliceSettings(2))
	settingsPath := f.liveSettings
	stop := make(chan struct{})
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stop:
				finished <- nil
				return
			default:
			}
			err := os.WriteFile(settingsPath+".native-update", []byte(bobSettings(i)), 0600)
			if err == nil {
				err = os.Rename(settingsPath+".native-update", settingsPath)
			}
			if i == 0 {
				close(started)
			}
			if err != nil {
				finished <- err
				return
			}
		}
	}()
	<-started
	defer func() {
		close(stop)
		if err := <-finished; err != nil {
			t.Fatal(err)
		}
	}()
	for i := 0; i < 40; i++ {
		if name, err := f.vault.ActiveProfile(f.fileSet); err != nil || name != "team" {
			t.Fatalf("concurrent shared metadata selected %q: %v", name, err)
		}
	}
}

func TestActiveProfileIdentityIgnoresSharedMachineUserID(t *testing.T) {
	// Claude Code's top-level userID is per-installation: every account that
	// logs in on one machine carries the same value. Two different accounts
	// that share it must never be treated as the same identity.
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, claudeRotationSettings(rotAliceUUID, rotAliceEmail, "shared-machine-userid", 1))
	f.writeLive(
		claudeRotationCreds("sk-ant-oat01-bob-gen9", "sk-ant-ort01-bob-gen9", 9999),
		claudeRotationSettings(rotBobUUID, rotBobEmail, "shared-machine-userid", 3),
	)

	if got := f.active(); got != "" {
		t.Fatalf("ActiveProfile = %q, want no match: a shared userID is not an account identity", got)
	}

	// The restore guard must likewise not mistake bob's fresher live file for alice's.
	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen1 {
		t.Fatalf("restore kept another account's credentials because of a shared userID: %s", got)
	}
}

func TestActiveProfileBoundIdentityDoesNotRequireLiveSettings(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	// Provider proof remains valid when volatile native metadata is absent.
	f.writeLive(aliceGen2, "")

	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice from a token-bound proof", got)
	}
}

// --- Backup metadata -------------------------------------------------------

func TestBackupRecordsClaudeIdentityInMeta(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeLive(aliceGen1, aliceSettings(1))

	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	meta := readJSONMap(t, f.profileFile("alice", "meta.json"))
	if meta["identity"] != rotAliceEmail {
		t.Errorf("meta identity = %v, want %q", meta["identity"], rotAliceEmail)
	}
	keys, _ := meta["identity_keys"].([]interface{})
	want := map[string]bool{"uuid:" + rotAliceUUID: true, "org:" + rotAliceOrg: true}
	for _, k := range keys {
		if fmt.Sprint(k) == "userid:"+rotAliceUser {
			t.Errorf("meta identity_keys must not carry the per-installation userID: %v", keys)
		}
		delete(want, fmt.Sprint(k))
	}
	if len(want) != 0 {
		t.Errorf("meta identity_keys %v missing %v", keys, want)
	}
	binding, err := identity.ReadClaudeIdentityBinding(f.profileFile("alice", ".credentials.json"))
	if err != nil || binding == nil || binding.Identity.OrganizationID != rotAliceOrg {
		t.Fatalf("backup did not persist an exact-token organization proof: %v", err)
	}
}

// --- Restore freshness guard -----------------------------------------------

func TestRestoreKeepsFresherLiveClaudeCredentials(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9)) // same account, rotated (fresher)

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	if got := readFixtureFile(t, f.liveCreds); got != aliceGen2 {
		t.Fatalf("live credentials were clobbered with the stale snapshot:\n got %s\nwant %s", got, aliceGen2)
	}
	// A same-account freshness no-op preserves the current session state too.
	if got := readJSONMap(t, f.liveSettings)["numStartups"]; got != float64(9) {
		t.Fatalf("live .claude.json numStartups = %v, want the live value 9", got)
	}
}

func TestRestoreReplacesOlderLiveClaudeCredentials(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen2, aliceSettings(1))
	f.writeLive(aliceGen1, aliceSettings(9)) // same account but older than the snapshot

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen2 {
		t.Fatalf("older live credentials must be replaced by the snapshot, got %s", got)
	}
}

func TestRestoreReplacesOtherAccountEvenIfFresher(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeLive(claudeRotationCreds("sk-ant-oat01-bob-gen9", "sk-ant-ort01-bob-gen9", 9999), bobSettings(3))

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen1 {
		t.Fatalf("cross-account switch must overwrite the live file, got %s", got)
	}
}

func TestClaudeRestoreFreshnessRequiresConsistentAccount(t *testing.T) {
	withIdentity := func(creds, accountID, email string) string {
		var root map[string]interface{}
		if err := json.Unmarshal([]byte(creds), &root); err != nil {
			t.Fatal(err)
		}
		oauth := root["claudeAiOauth"].(map[string]interface{})
		oauth["accountId"], oauth["email"] = accountID, email
		data, err := json.Marshal(root)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	bobNewer := claudeRotationCreds("sk-ant-oat01-bob-gen9", "sk-ant-ort01-bob-gen9", 9999)
	for _, tc := range []struct {
		name         string
		snapshot     string
		live         string
		liveSettings string
		wantKeepLive bool
	}{
		{
			name:         "matching embedded and paired account keeps newer live tokens",
			snapshot:     withIdentity(aliceGen1, rotAliceUUID, rotAliceEmail),
			live:         withIdentity(aliceGen2, rotAliceUUID, rotAliceEmail),
			liveSettings: aliceSettings(9),
			wantKeepLive: true,
		},
		{
			name:         "shared email does not override different paired account UUID",
			snapshot:     aliceGen1,
			live:         bobNewer,
			liveSettings: claudeRotationSettings(rotBobUUID, rotAliceEmail, rotAliceUser, 9),
		},
		{
			name:         "same account and email do not override a different verified organization",
			snapshot:     aliceGen1,
			live:         claudeRotationCreds("sk-ant-oat01-alice-team-gen1", "sk-ant-ort01-alice-team-gen1", 9999),
			liveSettings: aliceSettings(9),
		},
		{
			name:         "live embedded account conflicts with stale paired settings",
			snapshot:     aliceGen1,
			live:         withIdentity(bobNewer, rotBobUUID, rotAliceEmail),
			liveSettings: aliceSettings(9),
		},
		{
			name:         "snapshot embedded account conflicts with its paired settings",
			snapshot:     withIdentity(bobGen1, rotBobUUID, rotAliceEmail),
			live:         aliceGen2,
			liveSettings: aliceSettings(9),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("alice", tc.snapshot, aliceSettings(1))
			f.writeLive(tc.live, tc.liveSettings)
			snapshotPath := f.profileFile("alice", ".credentials.json")

			if got := f.vault.claudeLiveIsNewer(f.liveCreds, snapshotPath); got != tc.wantKeepLive {
				t.Errorf("claudeLiveIsNewer() = %v, want %v", got, tc.wantKeepLive)
			}
			if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
				t.Fatalf("Restore: %v", err)
			}
			want := tc.snapshot
			if tc.wantKeepLive {
				want = tc.live
			}
			if got := readFixtureFile(t, f.liveCreds); got != want {
				t.Fatal("Restore retained the wrong account or token generation")
			}
			if got := readFixtureFile(t, snapshotPath); got != tc.snapshot {
				t.Fatal("Restore changed the saved profile")
			}
		})
	}
}

func TestClaudeFreshnessRejectsUnverifiedIdentityHints(t *testing.T) {
	for _, missing := range []string{"live", "snapshot"} {
		t.Run(missing, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("alice", aliceGen1, aliceSettings(1))
			f.writeLive(aliceGen2, aliceSettings(9))
			path := filepath.Join(filepath.Dir(f.liveCreds), identity.ClaudeIdentityCacheFile)
			if missing == "snapshot" {
				path = f.profileFile("alice", "meta.json")
			}
			writeFixtureFile(t, path, `{"identity_keys":["uuid:`+rotAliceUUID+`","org:`+rotAliceOrg+`","email:`+rotAliceEmail+`"]}`)
			if f.vault.claudeLiveIsNewer(f.liveCreds, f.profileFile("alice", ".credentials.json")) {
				t.Fatal("shared local labels authorized a freshness decision without a token-bound proof")
			}
		})
	}
}

func TestRestoreIgnoresFailedRefreshResidue(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	// Same account, "fresher" expiry, but the refresh token is gone: the
	// signature of a failed refresh. That is not a newer credential.
	f.writeLive(claudeRotationCreds("sk-ant-oat01-alice-dead", "", 2000), aliceSettings(9))

	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen1 {
		t.Fatalf("failed-refresh residue must be replaced by the snapshot, got %s", got)
	}
}

// --- ResnapshotOutgoing ----------------------------------------------------

func TestResnapshotOutgoingCapturesRotatedClaudeTokens(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("bob", bobGen1, bobSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))

	if err := f.vault.ResnapshotOutgoing(f.fileSet, "alice", "bob"); err != nil {
		t.Fatalf("ResnapshotOutgoing: %v", err)
	}
	if got := readFixtureFile(t, f.profileFile("alice", ".credentials.json")); got != aliceGen2 {
		t.Fatalf("vault snapshot not refreshed with rotated tokens, got %s", got)
	}
	if meta := readJSONMap(t, f.profileFile("alice", "meta.json")); meta["identity"] != rotAliceEmail {
		t.Fatalf("re-snapshot did not record identity in meta.json: %v", meta)
	}
}

func TestResnapshotOutgoingSkipsIncompleteLiveClaudeCredentials(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("bob", bobGen1, bobSettings(1))
	f.writeLive(claudeRotationCreds("sk-ant-oat01-alice-dead", "", 2000), aliceSettings(9))

	if err := f.vault.ResnapshotOutgoing(f.fileSet, "alice", "bob"); err != nil {
		t.Fatalf("ResnapshotOutgoing: %v", err)
	}
	if got := readFixtureFile(t, f.profileFile("alice", ".credentials.json")); got != aliceGen1 {
		t.Fatalf("a dead live credential must not overwrite the vault snapshot, got %s", got)
	}
}

// --- The reporter's scenario, end to end -----------------------------------

func TestClaudeRotationSwitchFlow(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("bob", bobGen1, bobSettings(1))
	// alice has been in use; Claude Code rotated her tokens in place.
	f.writeLive(aliceGen2, aliceSettings(9))

	// Switch to bob, the way `caam activate` does it.
	outgoing := f.active()
	if outgoing != "alice" {
		t.Fatalf("outgoing profile = %q, want alice", outgoing)
	}
	if err := f.vault.ResnapshotOutgoing(f.fileSet, outgoing, "bob"); err != nil {
		t.Fatalf("ResnapshotOutgoing: %v", err)
	}
	if err := f.vault.Restore(f.fileSet, "bob"); err != nil {
		t.Fatalf("Restore bob: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != bobGen1 {
		t.Fatalf("live credentials after switching to bob = %s, want bob's", got)
	}
	if got := f.active(); got != "bob" {
		t.Fatalf("ActiveProfile after switch = %q, want bob", got)
	}

	// Switch back: alice must come back with her ROTATED tokens, not the
	// stale generation whose refresh token was already consumed.
	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore alice: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen2 {
		t.Fatalf("alice restored with stale tokens:\n got %s\nwant %s", got, aliceGen2)
	}
	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile after switching back = %q, want alice", got)
	}
}

// A ~/.claude that is a symlink (GNU stow's default tree folding, dotfiles
// repos) must not block activation: v0.1.22 restored through it (#120).
func TestClaudeSwitchThroughSymlinkedClaudeDir(t *testing.T) {
	f := newClaudeRotationFixture(t)
	home := filepath.Dir(f.liveSettings)
	realDir := filepath.Join(filepath.Dir(home), "dotfiles", "claude")
	if err := os.MkdirAll(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(home, ".claude")
	if err := os.Remove(claudeDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realDir, claudeDir); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("bob", bobGen1, bobSettings(1))
	f.writeLive(aliceGen2, aliceSettings(9))

	if err := f.vault.ResnapshotOutgoing(f.fileSet, f.active(), "bob"); err != nil {
		t.Fatalf("ResnapshotOutgoing: %v", err)
	}
	if err := f.vault.Restore(f.fileSet, "bob"); err != nil {
		t.Fatalf("Restore bob through symlinked ~/.claude: %v", err)
	}
	if got := readFixtureFile(t, filepath.Join(realDir, ".credentials.json")); got != bobGen1 {
		t.Fatalf("credentials in the symlink target = %s, want bob's", got)
	}
	if info, err := os.Lstat(claudeDir); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("~/.claude is no longer the user's symlink: info=%v err=%v", info, err)
	}
	if err := f.vault.Restore(f.fileSet, "alice"); err != nil {
		t.Fatalf("Restore alice through symlinked ~/.claude: %v", err)
	}
	if got := readFixtureFile(t, f.liveCreds); got != aliceGen2 {
		t.Fatalf("alice restored with stale tokens through the symlink:\n got %s\nwant %s", got, aliceGen2)
	}
}

// ~/.claude.json keeps per-project state and grows with use; one larger than
// the credential read limit must still back up (v0.1.22 had no limit), while
// an oversized credential file stays refused (#120).
func TestClaudeBackupAcceptsLargeStateDocument(t *testing.T) {
	f := newClaudeRotationFixture(t)
	var root map[string]interface{}
	if err := json.Unmarshal([]byte(aliceSettings(1)), &root); err != nil {
		t.Fatal(err)
	}
	root["projects"] = map[string]interface{}{"/work": map[string]string{"history": strings.Repeat("x", int(MaxDiscoveryFileBytes)+1024)}}
	big, err := json.Marshal(root)
	if err != nil {
		t.Fatal(err)
	}
	f.writeLive(aliceGen1, string(big))
	if err := f.vault.Backup(f.fileSet, "alice"); err != nil {
		t.Fatalf("Backup with a %d MiB ~/.claude.json: %v", len(big)>>20, err)
	}
	saved := readJSONMap(t, f.profileFile("alice", ".claude.json"))
	savedAccount, _ := saved["oauthAccount"].(map[string]interface{})
	if savedAccount["accountUuid"] != rotAliceUUID || savedAccount["organizationUuid"] != rotAliceOrg || savedAccount["emailAddress"] != rotAliceEmail {
		t.Fatal("large state backup lost the verified account and organization")
	}
	// Rewriting oauthAccount and JSON whitespace is intentional; every other
	// setting, including the complete large project history, must survive.
	var original map[string]interface{}
	if err := json.Unmarshal(big, &original); err != nil {
		t.Fatal(err)
	}
	delete(saved, "oauthAccount")
	delete(original, "oauthAccount")
	if !reflect.DeepEqual(saved, original) {
		t.Fatal("large state backup changed unrelated native settings")
	}

	// The auth pool validates saved files the same way: a large state document
	// carries no credential (skipped), it is not invalid material.
	if err := ValidateCredentialData("claude", ".claude.json", big); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("ValidateCredentialData(large .claude.json) = %v, want ErrNoCredentials", err)
	}
	if err := ValidateCredentialData("claude", ".credentials.json", make([]byte, MaxDiscoveryFileBytes+1)); err == nil {
		t.Fatal("ValidateCredentialData accepted an oversized credential file")
	}

	if err := os.Truncate(f.liveCreds, MaxDiscoveryFileBytes+1); err != nil {
		t.Fatal(err)
	}
	err = f.vault.Backup(f.fileSet, "oversized")
	if err == nil || !strings.Contains(err.Error(), f.liveCreds) {
		t.Fatalf("oversized credential: err = %v, want a refusal naming %s", err, f.liveCreds)
	}
}

// --- Helpers ---------------------------------------------------------------

func TestClaudeIdentityKeys(t *testing.T) {
	cases := []struct {
		name string
		root string
		want []string
	}{
		{"full account", aliceSettings(1), []string{"uuid:" + rotAliceUUID, "org:" + rotAliceOrg, "email:" + rotAliceEmail}},
		{"normalizes case and whitespace", `{"oauthAccount":{"accountUuid":" ABCD ","emailAddress":" Alice@Example.com "}}`, []string{"uuid:abcd", "email:alice@example.com"}},
		{"legacy string account", `{"oauthAccount":"Legacy@Example.com"}`, []string{"account:legacy@example.com"}},
		{"userID alone is not an identity (per-installation value)", `{"userID":"u-1"}`, nil},
		{"no identity", `{"numStartups":3}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var root map[string]interface{}
			if err := json.Unmarshal([]byte(tc.root), &root); err != nil {
				t.Fatal(err)
			}
			got := claudeIdentityKeys(root)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("claudeIdentityKeys = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestClaudeProfileIdentityUsesVerifiedAccountAndOrganization(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("uuid-only", aliceGen1, claudeRotationSettings(rotAliceUUID, "", "", 1))
	f.writeProfile("team", aliceTeamGen1, aliceSettings(1))
	f.writeProfile("opaque", claudeRotationCreds("unknown-access", "unknown-refresh", 1000), aliceSettings(1))

	want := `claude:["` + rotAliceUUID + `","` + rotAliceOrg + `"]`
	if got := f.vault.ProfileIdentity("claude", "alice"); got != want {
		t.Errorf("ProfileIdentity(alice) = %q, want %q", got, want)
	}
	if got := f.vault.ProfileIdentity("claude", "uuid-only"); got != want {
		t.Errorf("ProfileIdentity(uuid-only) = %q, want %q", got, want)
	}
	if got := f.vault.ProfileIdentity("claude", "team"); got == "" || got == want {
		t.Errorf("same-email Team organization collapsed into personal identity: %q", got)
	}
	if got := f.vault.ProfileIdentity("claude", "opaque"); got != "" {
		t.Errorf("ProfileIdentity(opaque) = %q, want empty (metadata does not prove ownership)", got)
	}
	writeFixtureFile(t, f.profileFile("alice", ".credentials.json"), bobGen1)
	if got := f.vault.ProfileIdentity("claude", "alice"); got != "" {
		t.Errorf("replaced token retained a stale duplicate-detection key: %q", got)
	}
}
