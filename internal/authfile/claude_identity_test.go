package authfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func issue132Proof(t *testing.T, credentials []byte, account, organization string) *identity.ClaudeIdentityBinding {
	t.Helper()
	fingerprint, err := identity.ClaudeCredentialFingerprint(credentials)
	if err != nil {
		t.Fatal(err)
	}
	return &identity.ClaudeIdentityBinding{
		Identity: identity.Identity{Provider: "claude", AccountID: account, OrganizationID: organization,
			Email: "shared@example.test", Organization: organization, Verified: true},
		CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC(),
	}
}

func issue132WriteProof(t *testing.T, path string, proof *identity.ClaudeIdentityBinding) {
	t.Helper()
	data, err := json.Marshal(map[string]interface{}{"claude_identity": proof, "tool": "claude", "type": "user", "description": "keep this operator label"})
	if err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, path, string(data))
}

func TestClaudeProfileIdentityRepairsSavedLabelsWithoutActivation(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("work", aliceTeamGen1, aliceSettings(7))
	f.writeLive(bobGen1, bobSettings(3))
	writeFixtureFile(t, f.profileFile("work", "meta.json"), `{"description":"keep operator label","tags":["work"],"identity":"stale@example.test"}`)
	beforeMeta := readFixtureFile(t, f.profileFile("work", "meta.json"))
	beforeState := readFixtureFile(t, f.profileFile("work", claudeSettingsFile))
	beforeLive := readFixtureFile(t, f.liveSettings)
	calls := 0
	verify := f.vault.ClaudeIdentityVerifier
	f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		calls++
		if string(data) != aliceTeamGen1 {
			t.Fatal("verification selected native credentials instead of the named saved profile")
		}
		return verify(ctx, data)
	}
	result, err := f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{})
	if err != nil || result.State != identity.ClaudeIdentityUnknown || result.Identity != nil || calls != 0 {
		t.Fatalf("offline legacy inspection = %+v, %v; calls=%d", result, err, calls)
	}
	result, err = f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Online: true})
	if err != nil || result.State != identity.ClaudeIdentityVerified || result.Source != "provider" || result.Repaired || result.Identity.OrganizationID != rotTeamOrg || calls != 1 {
		t.Fatalf("read-only verification = %+v, %v; calls=%d", result, err, calls)
	}
	if readFixtureFile(t, f.profileFile("work", "meta.json")) != beforeMeta || readFixtureFile(t, f.profileFile("work", claudeSettingsFile)) != beforeState {
		t.Fatal("read-only verification modified the saved profile")
	}
	result, err = f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Repair: true})
	if err != nil || !result.Repaired || result.Identity.OrganizationID != rotTeamOrg || calls != 2 {
		t.Fatalf("repair = %+v, %v; calls=%d", result, err, calls)
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFixtureFile(t, f.profileFile("work", "meta.json"))), &meta); err != nil {
		t.Fatal(err)
	}
	if string(meta["description"]) != `"keep operator label"` || string(meta["tags"]) != `["work"]` {
		t.Fatal("repair lost operator metadata")
	}
	var state struct {
		Account struct {
			Organization string `json:"organizationUuid"`
		} `json:"oauthAccount"`
		NumStartups int `json:"numStartups"`
	}
	if err := json.Unmarshal([]byte(readFixtureFile(t, f.profileFile("work", claudeSettingsFile))), &state); err != nil || state.Account.Organization != rotTeamOrg || state.NumStartups != 7 {
		t.Fatalf("repair did not preserve session state and normalize the verified organization: %+v, %v", state, err)
	}
	if readFixtureFile(t, f.liveCreds) != bobGen1 || readFixtureFile(t, f.liveSettings) != beforeLive || readFixtureFile(t, f.profileFile("work", claudeCredentialsFile)) != aliceTeamGen1 {
		t.Fatal("metadata repair changed a saved credential or active login")
	}
	// The saved token is expired. Restart must still recognize exact ownership
	// without requiring a provider or rewriting already canonical metadata.
	restarted := NewVault(f.vaultDir)
	restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
		t.Error("cached expired identity required network after restart")
		return nil, identity.ErrClaudeIdentityUnavailable
	}
	before := make(map[string]os.FileInfo)
	for _, name := range []string{claudeCredentialsFile, "meta.json", claudeSettingsFile} {
		before[name], err = os.Stat(f.profileFile("work", name))
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err = restarted.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Repair: true})
	if err != nil || result.Repaired || result.Source != "cache" || result.Identity.OrganizationID != rotTeamOrg {
		t.Fatalf("offline restart repair = %+v, %v", result, err)
	}
	for name, info := range before {
		after, err := os.Stat(f.profileFile("work", name))
		if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
			t.Fatalf("repeated repair rewrote %s: %v", name, err)
		}
	}
}

func TestClaudeProfileIdentityRefusesRebindingButAcceptsSameOwnerRotation(t *testing.T) {
	for _, incoming := range []string{aliceGen2, aliceTeamGen1, bobGen1} {
		t.Run(incoming, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("personal", aliceGen1, aliceSettings(1))
			writeFixtureFile(t, f.profileFile("personal", claudeCredentialsFile), incoming)
			beforeMeta := readFixtureFile(t, f.profileFile("personal", "meta.json"))
			beforeState := readFixtureFile(t, f.profileFile("personal", claudeSettingsFile))
			result, err := f.vault.ClaudeProfileIdentity(context.Background(), "personal", ClaudeProfileIdentityOptions{})
			if err == nil || result.State != identity.ClaudeIdentityCredentialChanged || result.Identity != nil || result.RecordedIdentity == nil || result.RecordedIdentity.Verified {
				t.Fatalf("changed token received old ownership: %+v, %v", result, err)
			}
			result, err = f.vault.ClaudeProfileIdentity(context.Background(), "personal", ClaudeProfileIdentityOptions{Repair: true})
			if incoming == aliceGen2 {
				if err != nil || !result.Repaired || result.Identity.OrganizationID != rotAliceOrg {
					t.Fatalf("same-owner rotation was not repaired: %+v, %v", result, err)
				}
			} else {
				if err == nil || result.State != "conflict" || result.Repaired || result.Identity == nil || !result.Identity.Verified {
					t.Fatalf("different owner was rebound: %+v, %v", result, err)
				}
				if readFixtureFile(t, f.profileFile("personal", "meta.json")) != beforeMeta || readFixtureFile(t, f.profileFile("personal", claudeSettingsFile)) != beforeState {
					t.Fatal("ownership conflict modified saved metadata")
				}
			}
			if readFixtureFile(t, f.profileFile("personal", claudeCredentialsFile)) != incoming {
				t.Fatal("repair replaced saved tokens")
			}
		})
	}
}

func TestClaudeProfileIdentityRejectsConcurrentSourceReplacement(t *testing.T) {
	for _, change := range []string{claudeCredentialsFile, "meta.json", claudeSettingsFile, "same token new inode", "directory"} {
		t.Run(change, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("selected", aliceGen1, aliceSettings(1))
			f.writeLive(bobGen1, bobSettings(3))
			verify := f.vault.ClaudeIdentityVerifier
			f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				switch change {
				case "directory":
					dir := f.vault.ProfilePath("claude", "selected")
					if err := os.Rename(dir, dir+"-retained"); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(dir, 0700); err != nil {
						t.Fatal(err)
					}
					writeFixtureFile(t, filepath.Join(dir, claudeCredentialsFile), bobGen1)
					writeFixtureFile(t, filepath.Join(dir, claudeSettingsFile), bobSettings(2))
				case "same token new inode":
					path := f.profileFile("selected", claudeCredentialsFile)
					writeFixtureFile(t, path+".replacement", aliceGen1)
					if err := os.Rename(path+".replacement", path); err != nil {
						t.Fatal(err)
					}
				default:
					value := `{"concurrent":"preserve"}`
					if change == claudeCredentialsFile {
						value = bobGen1
					}
					writeFixtureFile(t, f.profileFile("selected", change), value)
				}
				return verify(ctx, data)
			}
			result, err := f.vault.ClaudeProfileIdentity(context.Background(), "selected", ClaudeProfileIdentityOptions{Online: true, Repair: true})
			if err == nil || result.State != "source_changed" || result.Repaired {
				t.Fatalf("concurrent source was repaired: %+v, %v", result, err)
			}
			if readFixtureFile(t, f.liveCreds) != bobGen1 {
				t.Fatal("failed saved repair changed native tokens")
			}
		})
	}
}

func TestClaudeProfileIdentityUnavailableAndExpiredAreSafe(t *testing.T) {
	for _, failure := range []error{identity.ErrClaudeTokenExpired, identity.ErrClaudeTokenRejected, errors.New("Authorization: Bearer SYNTHETIC-DO-NOT-PRINT")} {
		f := newClaudeRotationFixture(t)
		f.writeProfile("work", aliceGen1, aliceSettings(1))
		before := readFixtureFile(t, f.profileFile("work", "meta.json"))
		f.vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) { return nil, failure }
		result, err := f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Online: true, Repair: true})
		if err == nil || result.Repaired || result.Identity != nil || result.Source != "none" || strings.Contains(result.Detail, "SYNTHETIC") {
			t.Fatalf("unsuccessful provider request claimed cached success or leaked secrets: %+v, %v", result, err)
		}
		if readFixtureFile(t, f.profileFile("work", "meta.json")) != before {
			t.Fatal("provider failure changed saved ownership")
		}
	}
	f := newClaudeRotationFixture(t)
	f.writeProfile("expired", aliceGen1, aliceSettings(1))
	f.vault.ClaudeIdentityVerifier = nil
	result, err := f.vault.ClaudeProfileIdentity(context.Background(), "expired", ClaudeProfileIdentityOptions{Online: true})
	if !errors.Is(err, identity.ErrClaudeTokenExpired) || result.State != "expired" {
		t.Fatalf("production verifier did not skip expired token: %+v, %v", result, err)
	}
}

func TestClaudeProfileIdentityProtectsMetadataAndRecoverySnapshots(t *testing.T) {
	for _, malformed := range []string{`{"claude_identity":{}}`, `null`, `{"SYNTHETIC-PRIVATE`} {
		f := newClaudeRotationFixture(t)
		f.writeProfile("bad", aliceGen1, aliceSettings(1))
		writeFixtureFile(t, f.profileFile("bad", "meta.json"), malformed)
		f.vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
			t.Error("malformed expected owner must not be rebound")
			return nil, identity.ErrClaudeIdentityUnavailable
		}
		result, err := f.vault.ClaudeProfileIdentity(context.Background(), "bad", ClaudeProfileIdentityOptions{Repair: true})
		if err == nil || result.State != identity.ClaudeIdentityInvalidMetadata || strings.Contains(result.Detail, "SYNTHETIC") || readFixtureFile(t, f.profileFile("bad", "meta.json")) != malformed {
			t.Fatalf("invalid metadata was overwritten or leaked: %+v, %v", result, err)
		}
	}
	f := newClaudeRotationFixture(t)
	f.writeProfile("_original", aliceGen1, aliceSettings(1))
	if result, err := f.vault.ClaudeProfileIdentity(context.Background(), "_original", ClaudeProfileIdentityOptions{}); err != nil || result.State != identity.ClaudeIdentityVerified {
		t.Fatalf("recovery snapshot inspection failed: %+v, %v", result, err)
	}
	if _, err := f.vault.ClaudeProfileIdentity(context.Background(), "_original", ClaudeProfileIdentityOptions{Repair: true}); !errors.Is(err, errProtectedSystemProfile) {
		t.Fatalf("recovery snapshot was mutable: %v", err)
	}
	absent := NewVault(filepath.Join(t.TempDir(), "absent"))
	if _, err := absent.ClaudeProfileIdentity(context.Background(), "missing", ClaudeProfileIdentityOptions{}); err == nil {
		t.Fatal("missing profile was treated as verified")
	}
	if _, err := os.Stat(absent.basePath); !os.IsNotExist(err) {
		t.Fatal("read-only inspection created a vault")
	}
}

func TestClaudeRecoveryIdentityCopyPreservesEvidenceAndActivatesVerifiedGrant(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("_backup_old", aliceTeamGen1, aliceSettings(17))
	f.writeLive(bobGen1, bobSettings(5))
	writeFixtureFile(t, f.profileFile("_backup_old", "meta.json"), `{"type":"system","profile":"_backup_old","description":"historical operator label","identity":"untrusted@example.test","original_paths":["historical-login"],"previous_snapshot":"historical-copy","unknown_auth":"SYNTHETIC-OLD-AUTH"}`)
	writeFixtureFile(t, f.profileFile("_backup_old", claudeSettingsFile), `{"oauthAccount":{"emailAddress":"shared@example.test","organizationName":"Personal"},"numStartups":17,"oauthToken":"SYNTHETIC-OLD-OAUTH","sessionKey":"SYNTHETIC-OLD-SESSION","apiKey":"SYNTHETIC-OLD-API","api_key":"SYNTHETIC-OTHER-API","primaryApiKey":"SYNTHETIC-PRIMARY-KEY","apiKeyHelper":"SYNTHETIC-HELPER","env":{"ANTHROPIC_AUTH_TOKEN":"SYNTHETIC-ENV"},"cachedUsageUtilization":99}`)
	writeFixtureFile(t, f.profileFile("_backup_old", "unrelated-evidence.json"), `{"preserve":"original historical evidence"}`)
	before, beforeInfo := make(map[string]string), make(map[string]os.FileInfo)
	for _, name := range []string{claudeCredentialsFile, claudeSettingsFile, "meta.json", "unrelated-evidence.json"} {
		before[name] = readFixtureFile(t, f.profileFile("_backup_old", name))
		var err error
		beforeInfo[name], err = os.Stat(f.profileFile("_backup_old", name))
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	verify := f.vault.ClaudeIdentityVerifier
	f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		calls++
		return verify(ctx, data)
	}
	result, err := f.vault.ClaudeProfileIdentity(context.Background(), "_backup_old", ClaudeProfileIdentityOptions{CopyTo: "personal-label-is-not-identity"})
	if err != nil || result.CopiedTo != "personal-label-is-not-identity" || result.Repaired || result.Identity == nil || result.Identity.OrganizationID != rotTeamOrg || calls != 1 {
		t.Fatalf("recovery copy = %+v, %v; provider calls=%d", result, err, calls)
	}
	if readFixtureFile(t, f.liveCreds) != bobGen1 || readFixtureFile(t, f.profileFile(result.CopiedTo, claudeCredentialsFile)) != aliceTeamGen1 {
		t.Fatal("copy changed native credentials or substituted the saved grant")
	}
	var meta map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFixtureFile(t, f.profileFile(result.CopiedTo, "meta.json"))), &meta); err != nil || string(meta["type"]) != `"user"` || string(meta["copied_from"]) != `"_backup_old"` || string(meta["description"]) != `"historical operator label"` {
		t.Fatalf("copied metadata did not retain label and source provenance: %v", err)
	}
	for _, key := range []string{"original_paths", "previous_snapshot", "unknown_auth"} {
		if _, exists := meta[key]; exists {
			t.Fatalf("verified copy carried historical metadata field %s", key)
		}
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readFixtureFile(t, f.profileFile(result.CopiedTo, claudeSettingsFile))), &settings); err != nil || len(settings) != 1 || settings["oauthAccount"] == nil {
		t.Fatal("verified copy carried historical auth or session state alongside its verified identity")
	}
	if _, err := f.vault.ClaudeProfileIdentity(context.Background(), "_backup_old", ClaudeProfileIdentityOptions{CopyTo: result.CopiedTo}); !errors.Is(err, os.ErrExist) || calls != 1 {
		t.Fatalf("repeated copy did not refuse the existing destination before verification: %v; calls=%d", err, calls)
	}
	if _, err := f.vault.Switch(f.fileSet, result.CopiedTo, SwitchOptions{BackupMode: "never"}); err != nil {
		t.Fatalf("activate recovered profile: %v", err)
	}
	if readFixtureFile(t, f.liveCreds) != aliceTeamGen1 || f.active() != result.CopiedTo || calls != 1 {
		t.Fatal("recovered profile did not activate the verified grant without repeat network verification")
	}
	for name, expected := range before {
		after, err := os.Stat(f.profileFile("_backup_old", name))
		if err != nil || !os.SameFile(beforeInfo[name], after) || after.Mode() != beforeInfo[name].Mode() || !after.ModTime().Equal(beforeInfo[name].ModTime()) || readFixtureFile(t, f.profileFile("_backup_old", name)) != expected {
			t.Fatalf("original recovery evidence changed: %s, %v", name, err)
		}
	}
}

func TestClaudeRecoveryIdentityCopyRejectsAmbiguityAndConcurrentChanges(t *testing.T) {
	for _, scenario := range []string{"different organization", "different account", "conflicting cache", "unavailable", "source replaced", "destination appeared", "cancelled", "system destination", "repair and copy"} {
		t.Run(scenario, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("_backup_old", aliceTeamGen1, aliceSettings(1))
			f.writeLive(bobGen1, bobSettings(3))
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			opts := ClaudeProfileIdentityOptions{Online: true, CopyTo: "recovered"}
			switch scenario {
			case "different organization":
				issue132WriteProof(t, f.profileFile("_backup_old", "meta.json"), issue132Proof(t, []byte(aliceGen1), rotAliceUUID, "other-org"))
			case "different account":
				issue132WriteProof(t, f.profileFile("_backup_old", "meta.json"), issue132Proof(t, []byte(aliceTeamGen1), "other-account", rotTeamOrg))
			case "conflicting cache":
				issue132WriteProof(t, f.profileFile("_backup_old", identity.ClaudeIdentityCacheFile), issue132Proof(t, []byte(aliceTeamGen1), rotAliceUUID, "other-org"))
			case "system destination":
				opts.CopyTo = "_backup_new"
			case "repair and copy":
				opts.Repair = true
			}
			beforeMeta := readFixtureFile(t, f.profileFile("_backup_old", "meta.json"))
			verify := f.vault.ClaudeIdentityVerifier
			f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				switch scenario {
				case "unavailable":
					return nil, errors.New("Authorization: Bearer SYNTHETIC-DO-NOT-PRINT")
				case "source replaced":
					writeFixtureFile(t, f.profileFile("_backup_old", claudeCredentialsFile), bobGen1)
				case "destination appeared":
					if err := os.Mkdir(f.vault.ProfilePath("claude", opts.CopyTo), 0700); err != nil {
						t.Fatal(err)
					}
				case "cancelled":
					cancel()
				}
				return verify(ctx, data)
			}
			result, err := f.vault.ClaudeProfileIdentity(ctx, "_backup_old", opts)
			if err == nil || result.CopiedTo != "" || result.Repaired || strings.Contains(result.Detail, "SYNTHETIC") {
				t.Fatalf("unsafe recovery copy accepted or disclosed credentials: %+v, %v", result, err)
			}
			if scenario != "destination appeared" {
				if _, err := os.Lstat(f.vault.ProfilePath("claude", opts.CopyTo)); !os.IsNotExist(err) {
					t.Fatal("failed copy published an incomplete destination")
				}
			} else if entries, err := os.ReadDir(f.vault.ProfilePath("claude", opts.CopyTo)); err != nil || len(entries) != 0 {
				t.Fatal("copy overwrote an independently created destination")
			}
			if readFixtureFile(t, f.profileFile("_backup_old", "meta.json")) != beforeMeta || readFixtureFile(t, f.liveCreds) != bobGen1 {
				t.Fatal("failed copy changed recovery ownership or native credentials")
			}
		})
	}
}

func TestClaudeIdentityPublicationRejectsContradictorySavedCopies(t *testing.T) {
	for _, operation := range []string{"repair", "copy"} {
		for _, scenario := range []string{"different organization", "different account", "malformed matching proof", "conflicting secondary proof", "same owner", "unrelated broken profile"} {
			t.Run(operation+"/"+scenario, func(t *testing.T) {
				f := newClaudeRotationFixture(t)
				source := "work"
				opts := ClaudeProfileIdentityOptions{Online: true, Repair: true}
				if operation == "copy" {
					source = "_backup_old"
					opts = ClaudeProfileIdentityOptions{Online: true, CopyTo: "recovered"}
				}
				f.writeProfile(source, aliceTeamGen1, aliceSettings(1))
				writeFixtureFile(t, f.profileFile(source, "meta.json"), `{"description":"legacy unverified evidence"}`)
				f.writeProfile("other", aliceTeamGen1, aliceSettings(2))
				f.writeLive(bobGen1, bobSettings(3))
				switch scenario {
				case "different organization":
					issue132WriteProof(t, f.profileFile("other", "meta.json"), issue132Proof(t, []byte(aliceTeamGen1), rotAliceUUID, rotAliceOrg))
				case "different account":
					issue132WriteProof(t, f.profileFile("other", "meta.json"), issue132Proof(t, []byte(aliceTeamGen1), rotBobUUID, rotTeamOrg))
				case "malformed matching proof":
					writeFixtureFile(t, f.profileFile("other", "meta.json"), `{"claude_identity":`)
				case "conflicting secondary proof":
					issue132WriteProof(t, f.profileFile("other", identity.ClaudeIdentityCacheFile), issue132Proof(t, []byte(aliceTeamGen1), rotAliceUUID, rotAliceOrg))
				case "unrelated broken profile":
					writeFixtureFile(t, f.profileFile("other", claudeCredentialsFile), bobGen1)
					writeFixtureFile(t, f.profileFile("other", "meta.json"), `{"claude_identity":`)
				}
				before := make(map[string]claudeIdentityFile)
				for _, profile := range []string{source, "other"} {
					for _, name := range []string{claudeCredentialsFile, "meta.json", claudeSettingsFile, identity.ClaudeIdentityCacheFile} {
						path := f.profileFile(profile, name)
						info, err := os.Stat(path)
						if os.IsNotExist(err) {
							continue
						}
						if err != nil {
							t.Fatal(err)
						}
						before[path] = claudeIdentityFile{data: []byte(readFixtureFile(t, path)), info: info}
					}
				}
				verify, calls := f.vault.ClaudeIdentityVerifier, 0
				f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
					calls++
					return verify(ctx, data)
				}
				// The provider answer is still inspectable; it does not authorize
				// overwriting contradictory historical bindings elsewhere.
				result, err := f.vault.ClaudeProfileIdentity(context.Background(), source, ClaudeProfileIdentityOptions{Online: true})
				if err != nil || result.State != identity.ClaudeIdentityVerified || result.Identity == nil || result.Identity.OrganizationID != rotTeamOrg || result.Repaired || result.CopiedTo != "" {
					t.Fatalf("read-only provider verification failed: %+v, %v", result, err)
				}
				result, err = f.vault.ClaudeProfileIdentity(context.Background(), source, opts)
				conflict := scenario != "same owner" && scenario != "unrelated broken profile"
				if conflict {
					if !errors.Is(err, ErrInvalidCredentials) || result.State != "conflict" || result.Repaired || result.CopiedTo != "" {
						t.Fatalf("contradictory saved copy was published: %+v, %v", result, err)
					}
					if operation == "copy" {
						if _, err := os.Lstat(f.vault.ProfilePath("claude", "recovered")); !os.IsNotExist(err) {
							t.Fatal("conflicting evidence produced a recovery destination")
						}
					}
				} else if err != nil || result.State != identity.ClaudeIdentityVerified || operation == "repair" && !result.Repaired || operation == "copy" && result.CopiedTo != "recovered" {
					t.Fatalf("unrelated evidence blocked verified publication: %+v, %v", result, err)
				}
				if calls != 2 {
					t.Fatalf("offline consistency checks made extra provider requests: %d", calls)
				}
				for path, expected := range before {
					if !conflict && operation == "repair" && filepath.Dir(path) == f.vault.ProfilePath("claude", source) && filepath.Base(path) != claudeCredentialsFile {
						continue
					}
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(expected.info, after) || expected.info.Mode() != after.Mode() || !expected.info.ModTime().Equal(after.ModTime()) || !bytes.Equal(expected.data, []byte(readFixtureFile(t, path))) {
						t.Fatalf("identity publication changed preserved evidence: %s", filepath.Base(path))
					}
				}
				if readFixtureFile(t, f.liveCreds) != bobGen1 || readFixtureFile(t, f.liveSettings) != bobSettings(3) {
					t.Fatal("saved identity publication changed native authentication")
				}
			})
		}
	}
}

type changeClaudeEvidenceContext struct {
	context.Context
	ready  func() bool
	change func()
}

func (c *changeClaudeEvidenceContext) Err() error {
	if c.change != nil && c.ready() {
		change := c.change
		c.change = nil
		change()
	}
	return c.Context.Err()
}

func TestClaudeIdentityPublicationRechecksOtherCopiesBeforeCompletion(t *testing.T) {
	for _, operation := range []string{"repair", "copy"} {
		t.Run(operation, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			source := "work"
			opts := ClaudeProfileIdentityOptions{Repair: true}
			if operation == "copy" {
				source = "_backup_old"
				opts = ClaudeProfileIdentityOptions{CopyTo: "recovered"}
			}
			f.writeProfile(source, aliceTeamGen1, aliceSettings(1))
			writeFixtureFile(t, f.profileFile(source, "meta.json"), `{"description":"preserve recovery evidence"}`)
			f.writeProfile("other", aliceTeamGen1, aliceSettings(2))
			beforeMeta := readFixtureFile(t, f.profileFile(source, "meta.json"))
			beforeSettings := readFixtureFile(t, f.profileFile(source, claudeSettingsFile))
			changed := false
			ctx := &changeClaudeEvidenceContext{Context: context.Background(), ready: func() bool {
				if operation == "repair" {
					return readFixtureFile(t, f.profileFile(source, "meta.json")) != beforeMeta
				}
				staged, err := filepath.Glob(filepath.Join(f.vaultDir, "claude", vaultBackupPrefix+"identity-copy-*", "prepared", "meta.json"))
				if err != nil {
					t.Fatal(err)
				}
				return len(staged) != 0
			}, change: func() {
				changed = true
				issue132WriteProof(t, f.profileFile("other", "meta.json"), issue132Proof(t, []byte(aliceTeamGen1), rotAliceUUID, rotAliceOrg))
			}}
			result, err := f.vault.ClaudeProfileIdentity(ctx, source, opts)
			if !changed || !errors.Is(err, ErrInvalidCredentials) || result.State != "conflict" || result.Repaired || result.CopiedTo != "" {
				t.Fatalf("late contradictory ownership was published: changed=%v, result=%+v, error=%v", changed, result, err)
			}
			if readFixtureFile(t, f.profileFile(source, "meta.json")) != beforeMeta ||
				readFixtureFile(t, f.profileFile(source, claudeSettingsFile)) != beforeSettings ||
				readFixtureFile(t, f.profileFile(source, claudeCredentialsFile)) != aliceTeamGen1 {
				t.Fatal("late conflict did not preserve or roll back source metadata and credentials")
			}
			if _, err := os.Lstat(f.vault.ProfilePath("claude", "recovered")); !os.IsNotExist(err) {
				t.Fatal("late conflict published a recovery destination")
			}
			other, err := readStoredClaudeBinding(f.vault.ProfilePath("claude", "other"))
			if err != nil || other == nil || other.Identity.OrganizationID != rotAliceOrg {
				t.Fatal("rollback overwrote independently changed identity evidence")
			}
		})
	}
}

// Cancellation is delivered once a metadata write becomes visible. This
// exercises interruption between the two real filesystem publications rather
// than a helper that never reaches disk.
type cancelAfterIdentityPublication struct {
	context.Context
	path   string
	before []byte
}

func (c cancelAfterIdentityPublication) Err() error {
	current, err := os.ReadFile(c.path)
	if err == nil && !bytes.Equal(current, c.before) {
		return context.Canceled
	}
	return c.Context.Err()
}

func TestClaudeIdentityRepairInterruptionRollsBackAndCanBeRepeated(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("work", aliceTeamGen1, aliceSettings(13))
	f.writeLive(bobGen1, bobSettings(3))
	metaPath := f.profileFile("work", "meta.json")
	writeFixtureFile(t, metaPath, `{"description":"keep label"}`)
	beforeMeta := readFixtureFile(t, metaPath)
	beforeState := readFixtureFile(t, f.profileFile("work", claudeSettingsFile))
	ctx := cancelAfterIdentityPublication{Context: context.Background(), path: metaPath, before: []byte(beforeMeta)}
	result, err := f.vault.ClaudeProfileIdentity(ctx, "work", ClaudeProfileIdentityOptions{Repair: true})
	if !errors.Is(err, context.Canceled) || result.Repaired {
		t.Fatalf("interrupted repair = %+v, %v", result, err)
	}
	if readFixtureFile(t, metaPath) != beforeMeta || readFixtureFile(t, f.profileFile("work", claudeSettingsFile)) != beforeState || readFixtureFile(t, f.profileFile("work", claudeCredentialsFile)) != aliceTeamGen1 || readFixtureFile(t, f.liveCreds) != bobGen1 {
		t.Fatal("interrupted repair did not preserve the original saved and active state")
	}
	result, err = f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Repair: true})
	if err != nil || !result.Repaired || result.Identity.OrganizationID != rotTeamOrg {
		t.Fatalf("retry after interrupted repair = %+v, %v", result, err)
	}
	result, err = f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Repair: true})
	if err != nil || result.Repaired || result.Source != "cache" {
		t.Fatalf("repeated repair was not idempotent: %+v, %v", result, err)
	}
}

func TestClaudeProfileIdentityPromotesExactCacheWithoutOverridingSavedOwner(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("work", aliceTeamGen1, aliceSettings(1))
	cache := f.profileFile("work", identity.ClaudeIdentityCacheFile)
	issue132WriteProof(t, cache, issue132Proof(t, []byte(aliceTeamGen1), "account", "team"))
	writeFixtureFile(t, f.profileFile("work", "meta.json"), `{"description":"imported cache-only profile"}`)
	f.vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
		t.Error("matching expired cache required online verification")
		return nil, identity.ErrClaudeIdentityUnavailable
	}
	result, err := f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Repair: true})
	if err != nil || !result.Repaired || result.Source != "cache" || result.Identity.OrganizationID != "team" {
		t.Fatalf("cache-only proof was not promoted offline: %+v, %v", result, err)
	}
	// A copied cache may not replace the expected owner of an existing profile,
	// even though its token bytes match the subsequently substituted grant.
	issue132WriteProof(t, f.profileFile("work", "meta.json"), issue132Proof(t, []byte(aliceGen1), "account", "personal"))
	result, err = f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{})
	if err == nil || result.State != "conflict" || result.Identity != nil || result.RecordedIdentity.OrganizationID != "personal" {
		t.Fatalf("native cache bypassed saved ownership: %+v, %v", result, err)
	}
}

func TestClaudeProfileIdentityRejectsLinkedSavedFiles(t *testing.T) {
	for _, name := range []string{claudeCredentialsFile, "meta.json", identity.ClaudeIdentityCacheFile, claudeSettingsFile} {
		t.Run(name, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("work", aliceGen1, aliceSettings(1))
			path := f.profileFile("work", name)
			if _, err := os.Lstat(path); err == nil {
				if err := os.Rename(path, path+".retained"); err != nil {
					t.Fatal(err)
				}
			}
			external := filepath.Join(t.TempDir(), "private")
			writeFixtureFile(t, external, `{"SYNTHETIC-PRIVATE":"never disclose or overwrite"}`)
			if err := os.Symlink(external, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			f.vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Error("linked saved source was sent to the provider")
				return nil, identity.ErrClaudeIdentityUnavailable
			}
			result, err := f.vault.ClaudeProfileIdentity(context.Background(), "work", ClaudeProfileIdentityOptions{Online: true, Repair: true})
			if err == nil || result.Repaired || strings.Contains(result.Detail, "SYNTHETIC") || readFixtureFile(t, external) != `{"SYNTHETIC-PRIVATE":"never disclose or overwrite"}` {
				t.Fatalf("linked saved source accepted or disclosed: %+v, %v", result, err)
			}
		})
	}
}

func TestClaudeBackupRejectsSourceChangeDuringVerification(t *testing.T) {
	for _, changed := range []string{"credentials", "settings"} {
		t.Run(changed, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			writeFixtureFile(t, f.liveCreds, aliceGen1)
			writeFixtureFile(t, f.liveSettings, aliceSettings(1))
			f.vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				if changed == "credentials" {
					writeFixtureFile(t, f.liveCreds, bobGen1)
				} else {
					writeFixtureFile(t, f.liveSettings, bobSettings(2))
				}
				return issue132Proof(t, data, "account-a", "team"), nil
			}
			err := f.vault.Backup(f.fileSet, "selected")
			if err == nil || !strings.Contains(err.Error(), "changed") {
				t.Fatalf("backup accepted changing source: %v", err)
			}
			if _, err := os.Stat(f.vault.ProfilePath("claude", "selected")); !os.IsNotExist(err) {
				t.Fatalf("changing source published a named snapshot: %v", err)
			}
			if changed == "credentials" && readFixtureFile(t, f.liveCreds) != bobGen1 {
				t.Fatal("backup overwrote the concurrent login")
			}
		})
	}
}

func TestClaudeActivationRejectsChangeDuringIdentityVerification(t *testing.T) {
	for _, operation := range []string{"restore", "switch"} {
		for _, changed := range []string{"incoming", "live"} {
			t.Run(operation+"/"+changed, func(t *testing.T) {
				f := newClaudeRotationFixture(t)
				targetDir := f.vault.ProfilePath("claude", "target")
				if err := os.MkdirAll(targetDir, 0700); err != nil {
					t.Fatal(err)
				}
				writeFixtureFile(t, filepath.Join(targetDir, claudeCredentialsFile), bobGen1)
				writeFixtureFile(t, filepath.Join(targetDir, claudeSettingsFile), bobSettings(1))
				writeFixtureFile(t, f.liveCreds, aliceGen1)
				writeFixtureFile(t, f.liveSettings, aliceSettings(1))
				beforeSettings := readFixtureFile(t, f.liveSettings)
				f.vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
					if bytes.Equal(data, []byte(bobGen1)) {
						if changed == "incoming" {
							writeFixtureFile(t, filepath.Join(targetDir, claudeCredentialsFile), aliceGen2)
						}
						return issue132Proof(t, data, "account-b", "team"), nil
					}
					if changed == "live" {
						writeFixtureFile(t, f.liveCreds, aliceGen2)
					}
					return issue132Proof(t, data, "account-a", "personal"), nil
				}
				var err error
				if operation == "restore" {
					err = f.vault.Restore(f.fileSet, "target")
				} else {
					_, err = f.vault.Switch(f.fileSet, "target", SwitchOptions{BackupMode: "never"})
				}
				if err == nil {
					t.Fatal("activation accepted changed verification input")
				}
				want := aliceGen1
				if changed == "live" {
					want = aliceGen2
				}
				if readFixtureFile(t, f.liveCreds) != want || readFixtureFile(t, f.liveSettings) != beforeSettings {
					t.Fatal("failed verification changed the outgoing login or policy")
				}
			})
		}
	}
}

func TestClaudeSavedIdentityCannotBeOverriddenByCopiedNativeCache(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	personalProof := issue132Proof(t, []byte(aliceGen1), "same-account", "personal")
	issue132WriteProof(t, f.profileFile("personal", "meta.json"), personalProof)
	writeFixtureFile(t, f.profileFile("personal", claudeCredentialsFile), bobGen1)
	teamProof := issue132Proof(t, []byte(bobGen1), "same-account", "team")
	issue132WriteProof(t, f.profileFile("personal", identity.ClaudeIdentityCacheFile), teamProof)
	f.writeLive(aliceGen2, aliceSettings(2))
	f.vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		return issue132Proof(t, data, "same-account", "team"), nil
	}
	if _, err := f.vault.Switch(f.fileSet, "personal", SwitchOptions{}); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("copied cache bypassed named profile ownership: %v", err)
	}
	if readFixtureFile(t, f.liveCreds) != aliceGen2 {
		t.Fatal("rejected profile changed live credential")
	}
}

func TestClaudeSubstitutedSavedTokensDoNotMatchOrOverwriteExpectedOwner(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	issue132WriteProof(t, f.profileFile("personal", "meta.json"), issue132Proof(t, []byte(aliceGen1), "same-account", "personal"))
	writeFixtureFile(t, f.profileFile("personal", claudeCredentialsFile), bobGen1)
	writeFixtureFile(t, f.liveCreds, bobGen1)
	writeFixtureFile(t, f.liveSettings, aliceSettings(2))
	f.vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		return issue132Proof(t, data, "same-account", "team"), nil
	}
	if got, err := f.vault.ActiveProfile(f.fileSet); err != nil || got != "" {
		t.Fatalf("substituted named credential was marked active: %q, %v", got, err)
	}
	before := readFixtureFile(t, f.profileFile("personal", "meta.json"))
	if err := f.vault.Backup(f.fileSet, "personal"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("same substituted bytes bypassed saved owner: %v", err)
	}
	if readFixtureFile(t, f.profileFile("personal", "meta.json")) != before {
		t.Fatal("failed backup relabelled expected profile owner")
	}
}

func TestClaudeFirstActivationPersistsProofForOfflineRestart(t *testing.T) {
	f := newClaudeRotationFixture(t)
	dir := f.vault.ProfilePath("claude", "team")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(dir, claudeCredentialsFile), bobGen1)
	writeFixtureFile(t, filepath.Join(dir, claudeSettingsFile), aliceSettings(1))
	calls := 0
	f.vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		calls++
		return issue132Proof(t, data, "account-b", "team"), nil
	}
	if err := f.vault.Restore(f.fileSet, "team"); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("first activation verification calls = %d", calls)
	}
	restarted := NewVault(f.vaultDir)
	restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
		t.Error("verified expired snapshot required network after restart")
		return nil, identity.ErrClaudeIdentityUnavailable
	}
	if err := restarted.Restore(f.fileSet, "team"); err != nil {
		t.Fatal(err)
	}
	id, err := identity.ExtractFromClaudeCredentials(filepath.Join(dir, claudeCredentialsFile))
	if err != nil || !id.Verified || id.OrganizationID != "team" {
		t.Fatalf("saved proof did not survive restart: %+v, %v", id, err)
	}
	if got, err := restarted.ActiveProfile(f.fileSet); err != nil || got != "team" {
		t.Fatalf("offline active profile = %q, %v", got, err)
	}
}

func TestClaudeProtectedSnapshotReusesExactNativeProofAfterRestart(t *testing.T) {
	for _, operation := range []string{"switch", "restore"} {
		t.Run(operation, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			const profile = "_backup_legacy"
			f.writeProfile(profile, aliceTeamGen1, bobSettings(1))
			writeFixtureFile(t, f.profileFile(profile, "meta.json"), `{"description":"immutable legacy evidence"}`)
			f.writeLive(bobGen1, bobSettings(2))
			before := make(map[string]claudeIdentityFile)
			for _, name := range []string{claudeCredentialsFile, claudeSettingsFile, "meta.json"} {
				path := f.profileFile(profile, name)
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				before[path] = claudeIdentityFile{data: []byte(readFixtureFile(t, path)), info: info}
			}
			activate := func(v *Vault) error {
				if operation == "restore" {
					return v.Restore(f.fileSet, profile)
				}
				_, err := v.Switch(f.fileSet, profile, SwitchOptions{BackupMode: "never"})
				return err
			}
			verify, calls := f.vault.ClaudeIdentityVerifier, 0
			f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				calls++
				return verify(ctx, data)
			}
			if err := activate(f.vault); err != nil || calls != 1 {
				t.Fatalf("initial protected activation = %v; provider calls=%d", err, calls)
			}
			restarted := NewVault(f.vaultDir)
			restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Error("the exact native token proof was not reused after restart")
				return nil, identity.ErrClaudeIdentityUnavailable
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := activate(restarted); err != nil {
					t.Fatalf("offline protected activation: %v", err)
				}
			}
			if readFixtureFile(t, f.liveCreds) != aliceTeamGen1 {
				t.Fatal("offline recovery selected a different token generation")
			}
			for path, expected := range before {
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(expected.info, after) || !expected.info.ModTime().Equal(after.ModTime()) ||
					expected.info.Mode() != after.Mode() || !bytes.Equal(expected.data, []byte(readFixtureFile(t, path))) {
					t.Fatal("activation modified protected recovery evidence")
				}
			}
			if _, err := os.Stat(f.profileFile(profile, identity.ClaudeIdentityCacheFile)); !os.IsNotExist(err) {
				t.Fatal("activation wrote a proof into the protected snapshot")
			}
		})
	}
}

func TestClaudeNativeProofChangeCannotRebindUnboundProfile(t *testing.T) {
	for _, scenario := range []struct {
		operation, phase, changeOnRead string
		newerNative                    bool
	}{
		{"switch", "initial-capture", "2", false},
		{"switch", "live-recheck", "5", false},
		{"switch", "identity-publication", "7", false},
		{"restore", "initial-capture", "2", false},
		{"restore", "identity-publication", "4", true},
		{"backup", "saved-owner-capture", "3", false},
	} {
		t.Run(scenario.operation+"/"+scenario.phase, func(t *testing.T) {
			f := newKeychainFixture(t)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			nativeCredential := aliceGen1
			if scenario.newerNative {
				// The exact token pair is unchanged; only its local expiry is
				// newer, selecting Restore's metadata-only retention branch.
				nativeCredential = strings.Replace(aliceGen1, `"expiresAt":1000`, `"expiresAt":2000`, 1)
			}
			f.storeToken(nativeCredential)
			writeFixtureFile(t, f.statePath, aliceSettings(2))
			writeSwitchProfile(t, f.vault, "claude", "legacy", map[string]string{
				claudeCredentialsFile: aliceGen1, claudeSettingsFile: aliceSettings(2),
				"meta.json": `{"description":"unbound saved label"}`,
			})
			var mirrorInfo os.FileInfo
			if scenario.operation == "backup" {
				// Backup prepares its mirror before capturing identity. Supply
				// an identical existing mirror so refusal must leave it intact.
				writeFixtureFile(t, f.credPath, nativeCredential)
				var err error
				mirrorInfo, err = os.Stat(f.credPath)
				if err != nil {
					t.Fatal(err)
				}
			}
			cache := filepath.Join(filepath.Dir(f.credPath), identity.ClaudeIdentityCacheFile)
			issue132WriteProof(t, cache, issue132Proof(t, []byte(aliceGen1), rotAliceUUID, rotAliceOrg))
			replacement := filepath.Join(t.TempDir(), "replacement-proof.json")
			issue132WriteProof(t, replacement, issue132Proof(t, []byte(aliceGen1), rotAliceUUID, rotTeamOrg))
			t.Setenv("CAAM_TEST_PROOF_BACKEND", os.Getenv("CAAM_KEYCHAIN_BIN"))
			t.Setenv("CAAM_TEST_PROOF_SOURCE", replacement)
			t.Setenv("CAAM_TEST_PROOF_DESTINATION", cache)
			t.Setenv("CAAM_TEST_PROOF_READ_MARK", filepath.Join(t.TempDir(), "read-count"))
			t.Setenv("CAAM_TEST_PROOF_CHANGE_ON_READ", scenario.changeOnRead)
			wrapper := filepath.Join(t.TempDir(), "security-proof-change")
			// The source capture supplies proof A. Replace it either at the
			// following identity capture or during metadata-only publication.
			// Backup's first read is mirror preparation, before source capture.
			script := `#!/bin/sh
if [ "$1" = find-generic-password ]; then
  "$CAAM_TEST_PROOF_BACKEND" "$@" || exit $?
  count=0
  if [ -f "$CAAM_TEST_PROOF_READ_MARK" ]; then
    read -r count < "$CAAM_TEST_PROOF_READ_MARK"
  fi
  count=$((count + 1))
  printf '%s\n' "$count" > "$CAAM_TEST_PROOF_READ_MARK"
  if [ "$count" -ge "$CAAM_TEST_PROOF_CHANGE_ON_READ" ]; then
    cp "$CAAM_TEST_PROOF_SOURCE" "$CAAM_TEST_PROOF_DESTINATION" || exit $?
  fi
  exit 0
fi
exec "$CAAM_TEST_PROOF_BACKEND" "$@"
`
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CAAM_KEYCHAIN_BIN", wrapper)
			f.vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Error("an exact native proof required a provider request")
				return nil, identity.ErrClaudeIdentityUnavailable
			}
			var err error
			switch scenario.operation {
			case "switch":
				var result *SwitchResult
				result, err = f.vault.Switch(f.fileSet, "legacy", SwitchOptions{})
				if result.RestoreStarted || result.AutoBackup != "" {
					t.Fatal("conflicting native proof reached preservation or activation")
				}
			case "restore":
				err = f.vault.Restore(f.fileSet, "legacy")
			case "backup":
				err = f.vault.Backup(f.fileSet, "legacy")
			}
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("conflicting native owners were not refused: %v", err)
			}
			if got, ok := f.storedToken(); !ok || got != nativeCredential {
				t.Fatal("refusal changed the authoritative credential")
			}
			if mirrorInfo != nil {
				after, err := os.Stat(f.credPath)
				if err != nil || !os.SameFile(mirrorInfo, after) || !mirrorInfo.ModTime().Equal(after.ModTime()) || readFixtureFile(t, f.credPath) != nativeCredential {
					t.Fatal("refusal changed the existing native credential mirror")
				}
			} else if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
				t.Fatal("refusal created a native credential mirror")
			}
			if readFixtureFile(t, cache) != readFixtureFile(t, replacement) || readFixtureFile(t, f.statePath) != aliceSettings(2) ||
				readFixtureFile(t, f.vault.BackupPath("claude", "legacy", "meta.json")) != `{"description":"unbound saved label"}` ||
				readFixtureFile(t, f.vault.BackupPath("claude", "legacy", claudeCredentialsFile)) != aliceGen1 ||
				readFixtureFile(t, f.vault.BackupPath("claude", "legacy", claudeSettingsFile)) != aliceSettings(2) {
				t.Fatal("refusal changed saved evidence or overwrote the concurrent native proof")
			}
		})
	}
}

func TestClaudeFreshLiveNoopPersistsVerifiedRotation(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	issue132WriteProof(t, f.profileFile("personal", "meta.json"), issue132Proof(t, []byte(aliceGen1), "account-a", "personal"))
	writeFixtureFile(t, f.liveCreds, aliceGen2)
	writeFixtureFile(t, f.liveSettings, bobSettings(2))
	f.vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		return issue132Proof(t, data, "account-a", "personal"), nil
	}
	result, err := f.vault.Switch(f.fileSet, "personal", SwitchOptions{})
	if err != nil || !result.KeptLive {
		t.Fatalf("fresh live grant not kept: %+v, %v", result, err)
	}
	restarted := NewVault(f.vaultDir)
	restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
		t.Error("no-op verification was not cached")
		return nil, identity.ErrClaudeIdentityUnavailable
	}
	if got, err := restarted.CurrentProfile(f.fileSet); err != nil || got != "personal" {
		t.Fatalf("restarted owner = %q, %v", got, err)
	}
	if err := restarted.Restore(f.fileSet, "personal"); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, f.liveCreds) != aliceGen2 {
		t.Fatal("offline restore replayed expired older grant")
	}
}

func assertClaudeIdentityOnlyState(t *testing.T, path string, before []byte, binding *identity.ClaudeIdentityBinding) {
	t.Helper()
	decode := func(data []byte) map[string]interface{} {
		t.Helper()
		var result map[string]interface{}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	after := decode([]byte(readFixtureFile(t, path)))
	account, present := after["oauthAccount"]
	if binding == nil {
		if present {
			t.Fatal("unverified account label survived the kept-live operation")
		}
	} else {
		var expected map[string]interface{}
		if err := json.Unmarshal(claudeOAuthAccount(binding), &expected); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(account, expected) {
			t.Fatal("account metadata does not describe its verified credential")
		}
	}
	expected := decode(before)
	delete(expected, "oauthAccount")
	delete(after, "oauthAccount")
	if !reflect.DeepEqual(after, expected) {
		t.Fatal("keeping a credential changed unrelated settings or session state")
	}
}

func TestClaudeLegacyFreshnessNoopPersistsBothProofs(t *testing.T) {
	for _, operation := range []string{"switch", "restore"} {
		t.Run(operation, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			oldSettings := []byte(strings.TrimSuffix(bobSettings(1), "}") + `,"opaqueCounter":9007199254740993,"projects":{"/saved":{"lastCost":1,"sessionId":"saved-session"}}}`)
			liveSettings := []byte(strings.TrimSuffix(bobSettings(9), "}") + `,"opaqueCounter":9007199254740995,"projects":{"/live":{"lastCost":9,"sessionId":"live-session"}}}`)
			f.writeProfile("legacy", aliceGen1, string(oldSettings))
			writeFixtureFile(t, f.profileFile("legacy", "meta.json"), `{"tool":"claude","description":"preserve this label"}`)
			writeFixtureFile(t, f.liveCreds, aliceGen2)
			writeFixtureFile(t, f.liveSettings, string(liveSettings))
			infos := make(map[string]os.FileInfo)
			for _, path := range []string{f.liveCreds, f.profileFile("legacy", claudeCredentialsFile)} {
				info, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				infos[path] = info
			}
			if operation == "switch" {
				result, err := f.vault.Switch(f.fileSet, "legacy", SwitchOptions{})
				if err != nil || !result.KeptLive || result.RestoreStarted {
					t.Fatalf("legacy newer grant was not retained: %+v, %v", result, err)
				}
			} else if err := f.vault.Restore(f.fileSet, "legacy"); err != nil {
				t.Fatal(err)
			}
			for _, entry := range []struct{ path, credential string }{
				{f.liveCreds, aliceGen2}, {f.profileFile("legacy", claudeCredentialsFile), aliceGen1},
			} {
				binding, err := identity.ReadClaudeIdentityBinding(entry.path)
				if err != nil || binding == nil || !binding.MatchesCredentials([]byte(entry.credential)) {
					t.Fatalf("kept-live operation did not retain both token proofs: %v", err)
				}
			}
			proof, err := f.vault.ClaudeIdentityVerifier(context.Background(), []byte(aliceGen1))
			if err != nil {
				t.Fatal(err)
			}
			assertClaudeIdentityOnlyState(t, f.profileFile("legacy", claudeSettingsFile), oldSettings, proof)
			assertClaudeIdentityOnlyState(t, f.liveSettings, liveSettings, proof)
			if readJSONMap(t, f.profileFile("legacy", "meta.json"))["description"] != "preserve this label" {
				t.Fatal("first verification discarded operator metadata")
			}
			restarted := NewVault(f.vaultDir)
			restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Error("persisted legacy proof required the unavailable endpoint after restart")
				return nil, identity.ErrClaudeIdentityUnavailable
			}
			if err := restarted.Restore(f.fileSet, "legacy"); err != nil {
				t.Fatal(err)
			}
			result, err := restarted.Switch(f.fileSet, "legacy", SwitchOptions{})
			if err != nil || !result.KeptLive || result.RestoreStarted {
				t.Fatalf("offline restart lost the freshness decision: %+v, %v", result, err)
			}
			if readFixtureFile(t, f.liveCreds) != aliceGen2 || readFixtureFile(t, f.profileFile("legacy", claudeCredentialsFile)) != aliceGen1 {
				t.Fatal("identity publication replayed or rewrote a credential generation")
			}
			for path, before := range infos {
				after, err := os.Stat(path)
				if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("keeping credentials unnecessarily replaced their files")
				}
			}
		})
	}
}

func TestClaudeExactNoopNormalizesIdentityAndAvoidsRepeatedVerification(t *testing.T) {
	for _, verified := range []bool{true, false} {
		name := "verified"
		if !verified {
			name = "unavailable"
		}
		t.Run(name, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			settings := []byte(bobSettings(3))
			f.writeProfile("legacy", aliceGen1, string(settings))
			writeFixtureFile(t, f.profileFile("legacy", "meta.json"), `{"tool":"claude","identity":"wrong@example.test","identity_keys":["uuid:wrong"],"description":"keep this"}`)
			writeFixtureFile(t, f.liveCreds, aliceGen1)
			writeFixtureFile(t, f.liveSettings, string(settings))
			verify := f.vault.ClaudeIdentityVerifier
			calls := 0
			f.vault.ClaudeIdentityVerifier = func(ctx context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
				calls++
				if !verified {
					return nil, identity.ErrClaudeIdentityUnavailable
				}
				return verify(ctx, data)
			}
			beforeMeta := readFixtureFile(t, f.profileFile("legacy", "meta.json"))
			result, err := f.vault.Switch(f.fileSet, "legacy", SwitchOptions{})
			if !verified {
				if !errors.Is(err, ErrInvalidCredentials) || result.KeptLive || result.RestoreStarted || calls != 1 {
					t.Fatalf("unverified exact match bypassed the activation gate: %+v, %v", result, err)
				}
				if readFixtureFile(t, f.profileFile("legacy", "meta.json")) != beforeMeta ||
					readFixtureFile(t, f.profileFile("legacy", claudeSettingsFile)) != string(settings) ||
					readFixtureFile(t, f.liveSettings) != string(settings) ||
					readFixtureFile(t, f.liveCreds) != aliceGen1 ||
					readFixtureFile(t, f.profileFile("legacy", claudeCredentialsFile)) != aliceGen1 {
					t.Fatal("unavailable verification changed saved or live authentication")
				}
				return
			}
			if err != nil || !result.KeptLive || result.RestoreStarted {
				t.Fatalf("exact current credential was not retained: %+v, %v", result, err)
			}
			var proof *identity.ClaudeIdentityBinding
			if verified {
				proof, err = verify(context.Background(), []byte(aliceGen1))
				if err != nil || calls != 1 || len(result.Warnings) != 0 {
					t.Fatalf("exact captured token needed repeated verification: calls=%d, warnings=%v, err=%v", calls, result.Warnings, err)
				}
			} else if len(result.Warnings) != 1 {
				t.Fatal("unavailable verification was not reported")
			}
			for _, path := range []string{f.liveSettings, f.profileFile("legacy", claudeSettingsFile)} {
				assertClaudeIdentityOnlyState(t, path, settings, proof)
			}
			meta := readJSONMap(t, f.profileFile("legacy", "meta.json"))
			if !verified && (meta["identity"] != nil || meta["identity_keys"] != nil || meta["claude_identity"] != nil) {
				t.Fatal("unverified exact no-op retained stale metadata labels")
			}
			if meta["description"] != "keep this" || readFixtureFile(t, f.liveCreds) != aliceGen1 || readFixtureFile(t, f.profileFile("legacy", claudeCredentialsFile)) != aliceGen1 {
				t.Fatal("metadata-only no-op changed credentials or operator preferences")
			}
			if verified {
				paths := []string{f.liveCreds, f.liveSettings, filepath.Join(filepath.Dir(f.liveCreds), identity.ClaudeIdentityCacheFile),
					f.profileFile("legacy", claudeCredentialsFile), f.profileFile("legacy", claudeSettingsFile), f.profileFile("legacy", "meta.json")}
				before := make(map[string]os.FileInfo)
				for _, path := range paths {
					before[path], err = os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
				}
				restarted := NewVault(f.vaultDir)
				restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
					t.Error("canonical exact no-op required another provider request")
					return nil, identity.ErrClaudeIdentityUnavailable
				}
				if result, err := restarted.Switch(f.fileSet, "legacy", SwitchOptions{}); err != nil || !result.KeptLive {
					t.Fatalf("offline exact no-op: %+v, %v", result, err)
				}
				for path, info := range before {
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
						t.Fatalf("canonical no-op unnecessarily rewrote %s", filepath.Base(path))
					}
				}
			}
		})
	}
}

func TestClaudeLegacyKeychainNoopRetainsBothProofsWithoutMirror(t *testing.T) {
	for _, operation := range []string{"switch", "restore"} {
		t.Run(operation, func(t *testing.T) {
			f := newKeychainFixture(t)
			account := identity.Identity{AccountID: rotAliceUUID, OrganizationID: rotAliceOrg, Email: rotAliceEmail}
			setClaudeFixtureIdentities(t, f.vault, map[string]identity.Identity{aliceGen1: account, aliceGen2: account})
			f.storeToken(aliceGen2)
			writeFixtureFile(t, f.statePath, bobSettings(9))
			writeSwitchProfile(t, f.vault, "claude", "legacy", map[string]string{
				claudeCredentialsFile: aliceGen1, claudeSettingsFile: bobSettings(1),
			})
			if operation == "restore" {
				if err := f.vault.Restore(f.fileSet, "legacy"); err != nil {
					t.Fatal(err)
				}
			} else if result, err := f.vault.Switch(f.fileSet, "legacy", SwitchOptions{}); err != nil || !result.KeptLive {
				t.Fatalf("keychain legacy freshness no-op: %+v, %v", result, err)
			}
			restarted := NewVault(f.vault.BasePath())
			restarted.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Error("keychain no-op lost its saved token proof")
				return nil, identity.ErrClaudeIdentityUnavailable
			}
			if err := restarted.Restore(f.fileSet, "legacy"); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(f.credPath); !os.IsNotExist(err) {
				t.Fatal("identity-only publication created a keychain credential mirror")
			}
			if got, ok := f.storedToken(); !ok || got != aliceGen2 {
				t.Fatal("identity-only publication changed the keychain credential")
			}
			if readFixtureFile(t, f.vault.BackupPath("claude", "legacy", claudeCredentialsFile)) != aliceGen1 {
				t.Fatal("identity-only publication changed the saved credential")
			}
		})
	}
}

func TestClaudeIdentityCacheFailureDoesNotPartiallyActivate(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("bob", bobGen1, bobSettings(1))
	writeFixtureFile(t, f.liveCreds, aliceGen1)
	writeFixtureFile(t, f.liveSettings, aliceSettings(1))
	cache := filepath.Join(filepath.Dir(f.liveCreds), identity.ClaudeIdentityCacheFile)
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	before := readFixtureFile(t, f.liveSettings)
	if err := f.vault.Restore(f.fileSet, "bob"); err == nil {
		t.Fatal("invalid cache destination accepted")
	}
	if readFixtureFile(t, f.liveCreds) != aliceGen1 || readFixtureFile(t, f.liveSettings) != before {
		t.Fatal("cache preparation failure partially activated an account")
	}
}

func TestClaudeSyncPublishesMatchingIdentityAndPreservesOtherState(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, bobSettings(3))
	oldProof := issue132Proof(t, []byte(aliceGen1), "account-a", "personal")
	issue132WriteProof(t, f.profileFile("personal", "meta.json"), oldProof)
	proof := issue132Proof(t, []byte(aliceGen2), "account-a", "personal")
	if err := f.vault.SyncClaudeCredentials("personal", []byte(aliceGen1), []byte(aliceGen2), proof, nil); err != nil {
		t.Fatal(err)
	}
	if readFixtureFile(t, f.profileFile("personal", claudeCredentialsFile)) != aliceGen2 {
		t.Fatal("new grant not published")
	}
	got, err := identity.ReadClaudeIdentityBinding(f.profileFile("personal", claudeCredentialsFile))
	if err != nil || got == nil || !got.MatchesCredentials([]byte(aliceGen2)) {
		t.Fatalf("new grant lost proof: %v", err)
	}
	var settings struct {
		Starts  int `json:"numStartups"`
		Account struct {
			UUID string `json:"accountUuid"`
			Org  string `json:"organizationUuid"`
		} `json:"oauthAccount"`
	}
	if err := json.Unmarshal([]byte(readFixtureFile(t, f.profileFile("personal", claudeSettingsFile))), &settings); err != nil {
		t.Fatal(err)
	}
	if settings.Starts != 3 || settings.Account.UUID != "account-a" || settings.Account.Org != "personal" {
		t.Fatalf("synchronized settings are mislabelled or lost unrelated state: %+v", settings)
	}
	if !strings.Contains(readFixtureFile(t, f.profileFile("personal", "meta.json")), "keep this operator label") {
		t.Fatal("operator metadata lost")
	}
	wrong := issue132Proof(t, []byte(bobGen1), "account-a", "team")
	if err := f.vault.SyncClaudeCredentials("personal", []byte(aliceGen2), []byte(bobGen1), wrong, nil); err == nil {
		t.Fatal("other organization synchronized")
	}
	if readFixtureFile(t, f.profileFile("personal", claudeCredentialsFile)) != aliceGen2 {
		t.Fatal("rejected sync changed saved tokens")
	}
}

func TestClaudeSyncMetadataStagingFailurePreservesCredential(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	issue132WriteProof(t, f.profileFile("personal", "meta.json"), issue132Proof(t, []byte(aliceGen1), "account", "personal"))
	writeFixtureFile(t, f.profileFile("personal", claudeSettingsFile), "{broken")
	beforeMeta := readFixtureFile(t, f.profileFile("personal", "meta.json"))
	err := f.vault.SyncClaudeCredentials("personal", []byte(aliceGen1), []byte(aliceGen2), issue132Proof(t, []byte(aliceGen2), "account", "personal"), nil)
	if err == nil {
		t.Fatal("invalid settings accepted by publication")
	}
	if readFixtureFile(t, f.profileFile("personal", claudeCredentialsFile)) != aliceGen1 || readFixtureFile(t, f.profileFile("personal", "meta.json")) != beforeMeta {
		t.Fatal("preparation failure changed credential or proof")
	}
}

func TestClaudeSmartRecoveryDistinguishesNormalizedLabelsFromUnsavedState(t *testing.T) {
	for _, test := range []struct {
		name   string
		starts int
	}{{"normalized account label", 1}, {"unsaved session state", 2}} {
		t.Run(test.name, func(t *testing.T) {
			starts := test.starts
			f := newClaudeRotationFixture(t)
			f.writeProfile("alice", aliceGen1, aliceSettings(1))
			f.writeProfile("bob", bobGen1, bobSettings(1))
			f.writeLive(aliceGen1, aliceSettings(1))
			if first, err := f.vault.Switch(f.fileSet, "alice", SwitchOptions{}); err != nil || !first.KeptLive {
				t.Fatalf("initial identity normalization failed: %+v, %v", first, err)
			}
			// Native rotation rewrites the original unnormalized account label.
			// A separate session-state change still needs its own recovery copy.
			f.writeLive(aliceGen2, aliceSettings(starts))
			result, err := f.vault.Switch(f.fileSet, "bob", SwitchOptions{})
			if err != nil || result.ResnapshottedProfile != "alice" {
				t.Fatalf("newer verified grant was not preserved: %+v, %v", result, err)
			}
			if (result.AutoBackup != "") != (starts == 2) {
				t.Fatalf("recovery decision lost state or duplicated label-only changes: %+v", result)
			}
			if readFixtureFile(t, f.profileFile("alice", claudeCredentialsFile)) != aliceGen2 {
				t.Fatal("named profile lost the rotated credential")
			}
			if result.AutoBackup != "" {
				var state struct {
					Starts int `json:"numStartups"`
				}
				if err := json.Unmarshal([]byte(readFixtureFile(t, f.profileFile(result.AutoBackup, claudeSettingsFile))), &state); err != nil || state.Starts != starts {
					t.Fatalf("recovery lost unsaved session state: %+v, %v", state, err)
				}
			}
		})
	}
}

func TestClaudeSyncRejectsSourceChangeWhileWaitingForVaultLock(t *testing.T) {
	f := newClaudeRotationFixture(t)
	f.writeProfile("personal", aliceGen1, aliceSettings(1))
	issue132WriteProof(t, f.profileFile("personal", "meta.json"), issue132Proof(t, []byte(aliceGen1), "account", "personal"))
	writeFixtureFile(t, f.liveCreds, aliceGen2)
	before := make(map[string]string)
	for _, name := range []string{claudeCredentialsFile, "meta.json", claudeSettingsFile} {
		before[name] = readFixtureFile(t, f.profileFile("personal", name))
	}
	changed := errors.New("live source changed after caller preflight")
	checkSource := func() error {
		current, err := os.ReadFile(f.liveCreds)
		if err != nil {
			return err
		}
		if !bytes.Equal(current, []byte(aliceGen2)) {
			return changed
		}
		return nil
	}
	if err := checkSource(); err != nil {
		t.Fatal(err)
	}
	// Another managed operation owns the vault when keepalive finishes its
	// preflight. A native session does not need that lock to replace its token.
	release, err := f.vault.lockMutation()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	proof := issue132Proof(t, []byte(aliceGen2), "account", "personal")
	done := make(chan error, 1)
	go func() {
		done <- f.vault.SyncClaudeCredentials("personal", []byte(aliceGen1), []byte(aliceGen2), proof, checkSource)
	}()
	writeErr := os.WriteFile(f.liveCreds, []byte(bobGen1), 0600)
	release()
	select {
	case err := <-done:
		if writeErr != nil {
			t.Fatal(writeErr)
		}
		if !errors.Is(err, changed) {
			t.Fatalf("source replacement during lock wait was accepted: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("guarded synchronization did not finish after lock release")
	}
	for name, want := range before {
		if readFixtureFile(t, f.profileFile("personal", name)) != want {
			t.Fatalf("source replacement changed saved %s", name)
		}
	}
	if readFixtureFile(t, f.liveCreds) != bobGen1 {
		t.Fatal("synchronization overwrote the independent native login")
	}
}

func TestClaudeSyncSourceGuardProtectsStagingAndRollsBackPublication(t *testing.T) {
	for _, changeAfter := range []int{1, 2} {
		phase := "preflight"
		if changeAfter == 2 {
			phase = "staging"
		}
		t.Run(phase, func(t *testing.T) {
			f := newClaudeRotationFixture(t)
			f.writeProfile("personal", aliceGen1, bobSettings(3))
			issue132WriteProof(t, f.profileFile("personal", "meta.json"), issue132Proof(t, []byte(aliceGen1), "account", "personal"))
			writeFixtureFile(t, f.liveCreds, aliceGen2)
			before := make(map[string]string)
			for _, name := range []string{claudeCredentialsFile, "meta.json", claudeSettingsFile} {
				before[name] = readFixtureFile(t, f.profileFile("personal", name))
			}
			changed := errors.New("live source changed during synchronization")
			checks, sawPublished := 0, false
			checkSource := func() error {
				checks++
				if saved, err := os.ReadFile(f.profileFile("personal", claudeCredentialsFile)); err == nil && bytes.Equal(saved, []byte(aliceGen2)) {
					sawPublished = true
				}
				current, err := os.ReadFile(f.liveCreds)
				if err != nil {
					return err
				}
				if !bytes.Equal(current, []byte(aliceGen2)) {
					return changed
				}
				if checks == changeAfter {
					// This writer acts just after a successful source check; the
					// next boundary must detect it without undoing its new login.
					return os.WriteFile(f.liveCreds, []byte(bobGen1), 0600)
				}
				return nil
			}
			err := f.vault.SyncClaudeCredentials("personal", []byte(aliceGen1), []byte(aliceGen2), issue132Proof(t, []byte(aliceGen2), "account", "personal"), checkSource)
			if !errors.Is(err, changed) {
				t.Fatalf("source change after %s was accepted: %v", phase, err)
			}
			if checks != changeAfter+1 || sawPublished != (changeAfter == 2) {
				t.Fatalf("did not exercise the expected publication boundary: checks=%d published=%v", checks, sawPublished)
			}
			for name, want := range before {
				if readFixtureFile(t, f.profileFile("personal", name)) != want {
					t.Fatalf("source change after %s did not preserve saved %s", phase, name)
				}
			}
			if readFixtureFile(t, f.liveCreds) != bobGen1 {
				t.Fatal("rollback overwrote the independent native login")
			}
		})
	}
}
