package authfile

// Tests for issue #73: Claude Code rotates both OAuth tokens in place while a
// profile is active, so vault recognition (ActiveProfile), the outgoing
// re-snapshot, and the restore freshness guard must key on the account
// identity carried by ~/.claude.json rather than on token bytes.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const (
	rotAliceUUID  = "11111111-2222-3333-4444-555555555555"
	rotAliceEmail = "alice@example.com"
	rotAliceUser  = "useridhash-alice"
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
}

// writeProfile creates a vault profile directly (no meta.json, as vaults
// written before #73 look); an empty settings string omits .claude.json.
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
	aliceGen1 = claudeRotationCreds("sk-ant-oat01-alice-gen1", "sk-ant-ort01-alice-gen1", 1000)
	aliceGen2 = claudeRotationCreds("sk-ant-oat01-alice-gen2", "sk-ant-ort01-alice-gen2", 2000)
	bobGen1   = claudeRotationCreds("sk-ant-oat01-bob-gen1", "sk-ant-ort01-bob-gen1", 1000)
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

func TestActiveProfileIdentityFromMetaWhenSnapshotLacksSettings(t *testing.T) {
	f := newClaudeRotationFixture(t)
	// Snapshot has no .claude.json (backed up while it was absent) but Backup
	// recorded the identity keys in meta.json.
	f.writeProfile("alice", aliceGen1, "")
	writeFixtureFile(t, f.profileFile("alice", "meta.json"),
		`{"tool":"claude","profile":"alice","identity":"alice@example.com","identity_keys":["uuid:`+rotAliceUUID+`"]}`)
	f.writeLive(aliceGen2, aliceSettings(9))

	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice matched via meta.json identity", got)
	}
}

func TestActiveProfileIdentityMatchesOnAnySharedKey(t *testing.T) {
	f := newClaudeRotationFixture(t)
	// Older snapshot shape: email only, no accountUuid, no userID.
	f.writeProfile("alice", aliceGen1, claudeRotationSettings("", "Alice@Example.com", "", 1))
	f.writeLive(aliceGen2, aliceSettings(9))

	if got := f.active(); got != "alice" {
		t.Fatalf("ActiveProfile = %q, want alice matched on the (normalized) email key", got)
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

func TestActiveProfileNoIdentityWithoutLiveSettings(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	// Rotated tokens but no live ~/.claude.json: nothing to identify the account by.
	f.writeLive(aliceGen2, "")

	if got := f.active(); got != "" {
		t.Fatalf("ActiveProfile = %q, want no match without any identity source", got)
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
	want := map[string]bool{"uuid:" + rotAliceUUID: true, "email:" + rotAliceEmail: true}
	for _, k := range keys {
		if fmt.Sprint(k) == "userid:"+rotAliceUser {
			t.Errorf("meta identity_keys must not carry the per-installation userID: %v", keys)
		}
		delete(want, fmt.Sprint(k))
	}
	if len(want) != 0 {
		t.Errorf("meta identity_keys %v missing %v", keys, want)
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
	// The settings snapshot itself is still restored (it holds no tokens).
	if got := readJSONMap(t, f.liveSettings)["numStartups"]; got != float64(1) {
		t.Fatalf("live .claude.json numStartups = %v, want the snapshot's 1", got)
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

// --- Helpers ---------------------------------------------------------------

func TestClaudeIdentityKeys(t *testing.T) {
	cases := []struct {
		name string
		root string
		want []string
	}{
		{"full account", aliceSettings(1), []string{"uuid:" + rotAliceUUID, "email:" + rotAliceEmail}},
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

func TestClaudeProfileIdentityLabelPrefersEmail(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("alice", aliceGen1, aliceSettings(1))
	f.writeProfile("uuid-only", aliceGen1, claudeRotationSettings(rotAliceUUID, "", "", 1))
	f.writeProfile("opaque", aliceGen1, "")

	if got := f.vault.ProfileIdentity("claude", "alice"); got != rotAliceEmail {
		t.Errorf("ProfileIdentity(alice) = %q, want %q", got, rotAliceEmail)
	}
	if got := f.vault.ProfileIdentity("claude", "uuid-only"); got != rotAliceUUID {
		t.Errorf("ProfileIdentity(uuid-only) = %q, want %q", got, rotAliceUUID)
	}
	if got := f.vault.ProfileIdentity("claude", "opaque"); got != "" {
		t.Errorf("ProfileIdentity(opaque) = %q, want empty (opaque tokens carry no identity)", got)
	}
}
