package vaulttransfer

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func writeProfileFile(t *testing.T, directory, name, contents string) {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func requireProfileFile(t *testing.T, directory, name, expected string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(directory, name))
	if err != nil || string(contents) != expected {
		t.Fatalf("profile file %s did not retain its expected generation (read error: %v)", name, err)
	}
}

func captureTestProfile(t *testing.T, path string) *Snapshot {
	t.Helper()
	snapshot, err := Capture(path)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPublicationSourceCheckRollsBackCompleteProfile(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new", true: "replacement"}[existing], func(t *testing.T) {
			root := t.TempDir()
			src, dst := filepath.Join(root, "incoming"), filepath.Join(root, "vault", "claude", "work")
			writeProfileFile(t, src, "auth.json", `{"api_key":"synthetic-incoming"}`)
			writeProfileFile(t, src, "meta.json", `{"description":"incoming"}`)
			if existing {
				writeProfileFile(t, dst, "auth.json", `{"api_key":"synthetic-old"}`)
				writeProfileFile(t, dst, "meta.json", `{"description":"old"}`)
				writeProfileFile(t, dst, "nested/private.json", `{"history":"old"}`)
			}
			source, destination := captureTestProfile(t, src), captureTestProfile(t, dst)
			published := false
			opts := &Options{Provider: "claude", Rename: func(from, to string) error {
				err := os.Rename(from, to)
				if err == nil && to == dst && strings.HasPrefix(filepath.Base(from), "_caam-vault-backup~import-") {
					published = true
				}
				return err
			}, CheckSource: func() error {
				if published {
					return errors.New("source lease changed")
				}
				return nil
			}}
			recovery, err := Publish(source, destination, opts)
			if err == nil || !published || recovery != "" {
				t.Fatalf("failed source check did not complete rollback: published=%v recovery=%q error=%v", published, recovery, err)
			}
			if err := destination.Check(); err != nil {
				t.Fatalf("complete prior profile or absence was not restored: %v", err)
			}
			if existing {
				requireProfileFile(t, dst, "nested/private.json", `{"history":"old"}`)
			}
		})
	}
}

func TestPublicationDoesNotRollbackConcurrentLogin(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "incoming"), filepath.Join(root, "vault", "claude", "work")
	writeProfileFile(t, src, "auth.json", `{"api_key":"synthetic-incoming"}`)
	writeProfileFile(t, dst, "auth.json", `{"api_key":"synthetic-old"}`)
	writeProfileFile(t, dst, "meta.json", `{"description":"old"}`)
	source, destination := captureTestProfile(t, src), captureTestProfile(t, dst)
	published := false
	recovery, err := Publish(source, destination, &Options{
		Provider: "claude",
		Rename: func(from, to string) error {
			err := os.Rename(from, to)
			if err == nil && to == dst && strings.HasPrefix(filepath.Base(from), "_caam-vault-backup~import-") {
				published = true
			}
			return err
		},
		CheckSource: func() error {
			if published {
				writeProfileFile(t, dst, "auth.json", `{"api_key":"synthetic-independent-login"}`)
				writeProfileFile(t, dst, "meta.json", `{"description":"independent"}`)
				return errors.New("source lease changed")
			}
			return nil
		},
	})
	if err == nil || recovery == "" {
		t.Fatalf("concurrent login was not preserved with recovery: %q, %v", recovery, err)
	}
	requireProfileFile(t, dst, "auth.json", `{"api_key":"synthetic-independent-login"}`)
	requireProfileFile(t, dst, "meta.json", `{"description":"independent"}`)
	if err := destination.CheckAt(recovery); err != nil {
		t.Fatalf("previous complete profile was not retained: %v", err)
	}
}

func TestSnapshotDetectsReplacementAndPortableDigest(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profile")
	writeProfileFile(t, path, "auth.json", `{"api_key":"synthetic"}`)
	if err := os.MkdirAll(filepath.Join(path, "empty"), 0700); err != nil {
		t.Fatal(err)
	}
	before := captureTestProfile(t, path)
	clone, err := Prepare(before, filepath.Join(root, "copy"), "codex")
	if err != nil || clone.Digest() != before.Digest() {
		t.Fatalf("portable contents including empty directories changed: %v", err)
	}
	info, err := os.Stat(filepath.Join(path, "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, root, "replacement", `{"api_key":"synthetic"}`)
	if err := os.Chtimes(filepath.Join(root, "replacement"), info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(root, "replacement"), filepath.Join(path, "auth.json")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := before.ReadFile("auth.json"); err == nil {
		t.Fatal("replacement inode with identical bytes was accepted")
	}
	if err := before.Check(); err == nil {
		t.Fatal("replaced snapshot was accepted")
	}
}

func TestPublicationRejectsAbsentDestinationAncestorRebind(t *testing.T) {
	for _, component := range []string{"vault", "provider"} {
		t.Run(component, func(t *testing.T) {
			root := t.TempDir()
			src := filepath.Join(root, "incoming")
			vault, foreignVault := filepath.Join(root, "vault"), filepath.Join(root, "foreign")
			parent := filepath.Join(vault, "codex")
			dst := filepath.Join(parent, "work")
			writeProfileFile(t, src, "auth.json", `{"api_key":"synthetic-incoming"}`)
			for _, path := range []string{parent, filepath.Join(foreignVault, "codex")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
			}
			source, destination := captureTestProfile(t, src), captureTestProfile(t, dst)
			var foreignStage, retainedStage string
			renameCalls := 0
			recovery, err := Publish(source, destination, &Options{
				Provider: "codex",
				BeforePublish: func(_, _ string) {
					entries, err := os.ReadDir(parent)
					if err != nil {
						t.Fatal(err)
					}
					var stageName string
					for _, entry := range entries {
						if strings.HasPrefix(entry.Name(), "_caam-vault-backup~import-") {
							stageName = entry.Name()
						}
					}
					if stageName == "" {
						t.Fatal("publication hook did not observe its prepared stage")
					}
					rebound, target := vault, foreignVault
					retained := filepath.Join(root, "retained")
					retainedStage = filepath.Join(retained, "codex", stageName)
					if component == "provider" {
						rebound, target = parent, filepath.Join(foreignVault, "codex")
						retainedStage = filepath.Join(retained, stageName)
					}
					if err := os.Rename(rebound, retained); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(target, rebound); err != nil {
						t.Skipf("symlinks unavailable: %v", err)
					}
					// A path-only deferred RemoveAll would delete this other
					// writer's directory after the configured vault is rebound.
					foreignStage = filepath.Join(parent, stageName)
					writeProfileFile(t, foreignStage, "private.json", `{"owner":"independent"}`)
				},
				Rename: func(from, to string) error {
					renameCalls++
					return os.Rename(from, to)
				},
			})
			if err == nil || recovery != "" || renameCalls != 0 {
				t.Fatalf("absent destination crossed a rebound ancestor: recovery=%q renames=%d error=%v", recovery, renameCalls, err)
			}
			if _, err := os.Lstat(dst); !os.IsNotExist(err) {
				t.Fatalf("rejected publication created a profile in the foreign vault: %v", err)
			}
			requireProfileFile(t, foreignStage, "private.json", `{"owner":"independent"}`)
			requireProfileFile(t, retainedStage, "auth.json", `{"api_key":"synthetic-incoming"}`)
			if err := source.Check(); err != nil {
				t.Fatalf("rejected publication changed its source: %v", err)
			}
		})
	}
}

func TestSnapshotRejectsSourceAncestorRebind(t *testing.T) {
	root := t.TempDir()
	sourceRoot := filepath.Join(root, "source-vault")
	src := filepath.Join(sourceRoot, "codex", "work")
	writeProfileFile(t, src, "auth.json", `{"api_key":"synthetic-source"}`)
	source := captureTestProfile(t, src)
	retained := filepath.Join(root, "retained-vault")
	if err := os.Rename(sourceRoot, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(retained, sourceRoot); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	// The alias reaches the exact same file inodes, but the configured source
	// vault no longer names the captured directory itself.
	if err := source.Check(); err == nil {
		t.Fatal("source root rebind was accepted with unchanged profile contents")
	}
	if data, _, err := source.ReadFile("auth.json"); err == nil || data != nil {
		t.Fatal("source root rebind returned credential bytes")
	}
	if err := source.CheckAt(filepath.Join(retained, "codex", "work")); err == nil {
		t.Fatal("recovery relocation discarded the original ancestor guards")
	}
	dst := filepath.Join(root, "destination", "codex", "work")
	if _, err := Publish(source, captureTestProfile(t, dst), &Options{Provider: "codex"}); err == nil {
		t.Fatal("rebound source was published")
	}
	if _, err := os.Lstat(dst); !os.IsNotExist(err) {
		t.Fatalf("rebound source changed the destination: %v", err)
	}
	requireProfileFile(t, filepath.Join(retained, "codex", "work"), "auth.json", `{"api_key":"synthetic-source"}`)
}

func TestSnapshotRejectsIdenticalContentsUnderReplacedAncestor(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "real_directory", true: "trusted_host_alias"}[alias], func(t *testing.T) {
			root := t.TempDir()
			host := filepath.Join(root, "host")
			writeProfileFile(t, filepath.Join(host, "vault", "codex", "work"), "auth.json", `{"api_key":"synthetic-source"}`)
			selected := host
			if alias {
				selected = filepath.Join(root, "home")
				if err := os.Symlink(host, selected); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			path := filepath.Join(selected, "vault", "codex", "work")
			before := captureTestProfile(t, path)
			if err := before.Check(); err != nil {
				t.Fatalf("unchanged trusted host path was rejected: %v", err)
			}
			retained := filepath.Join(root, "retained-host")
			if err := os.Rename(host, retained); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(host, 0700); err != nil {
				t.Fatal(err)
			}
			// Preserve all lower file and directory inodes. For an alias its
			// lstat identity is unchanged too; only its target was replaced.
			if err := os.Rename(filepath.Join(retained, "vault"), filepath.Join(host, "vault")); err != nil {
				t.Fatal(err)
			}
			if after := captureTestProfile(t, path); after.Digest() != before.Digest() {
				t.Fatal("fixture changed portable profile contents")
			}
			if err := before.Check(); err == nil {
				t.Fatal("replacement ancestor with identical profile contents was accepted")
			}
			if _, _, err := before.ReadFile("auth.json"); err == nil {
				t.Fatal("replacement ancestor was ignored during a captured read")
			}
		})
	}
}

func TestSnapshotAllowsOwnedRelocationAndAncestorActivity(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "vault", "codex")
	path := filepath.Join(parent, "work")
	writeProfileFile(t, path, "auth.json", `{"api_key":"synthetic-source"}`)
	before := captureTestProfile(t, path)
	recovery := filepath.Join(parent, "_caam-vault-backup~recovery", "profile")
	if err := os.MkdirAll(filepath.Dir(recovery), 0700); err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, filepath.Join(parent, "sibling"), "meta.json", `{"owner":"another-profile"}`)
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(parent, old, old); err != nil {
		t.Fatal(err)
	}
	if err := before.Check(); err != nil {
		t.Fatalf("legitimate parent directory activity invalidated the profile: %v", err)
	}
	if err := os.Rename(path, recovery); err != nil {
		t.Fatal(err)
	}
	if err := before.CheckAt(recovery); err != nil {
		t.Fatalf("owned recovery rename lost captured identity: %v", err)
	}
	if err := os.Rename(recovery, path); err != nil {
		t.Fatal(err)
	}
	if err := before.Check(); err != nil {
		t.Fatalf("restoring an owned rename invalidated the original snapshot: %v", err)
	}
}

func TestSnapshotMissingAncestorsOnlyAllowRealDirectories(t *testing.T) {
	for _, alias := range []bool{false, true} {
		t.Run(map[bool]string{false: "created_directories", true: "new_alias"}[alias], func(t *testing.T) {
			root := t.TempDir()
			vault := filepath.Join(root, "vault")
			path := filepath.Join(vault, "codex", "work")
			before := captureTestProfile(t, path)
			if alias {
				outside := t.TempDir()
				if err := os.Mkdir(filepath.Join(outside, "codex"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, vault); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := before.Check(); (err != nil) != alias {
				t.Fatalf("missing ancestor transition: alias=%v error=%v", alias, err)
			}
		})
	}
}

func TestPublicationCleanupPreservesReusedStageName(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "incoming"), filepath.Join(root, "vault", "codex", "work")
	writeProfileFile(t, src, "auth.json", `{"api_key":"synthetic-incoming"}`)
	var foreignStage string
	_, err := Publish(captureTestProfile(t, src), captureTestProfile(t, dst), &Options{
		Provider: "codex",
		Rename: func(from, to string) error {
			if err := os.Rename(from, to); err != nil {
				return err
			}
			if to == dst && strings.HasPrefix(filepath.Base(from), "_caam-vault-backup~import-") {
				foreignStage = from
				writeProfileFile(t, foreignStage, "private.json", `{"owner":"independent"}`)
			}
			return nil
		},
	})
	if err != nil || foreignStage == "" {
		t.Fatalf("publication did not reach stage name reuse: %v", err)
	}
	requireProfileFile(t, dst, "auth.json", `{"api_key":"synthetic-incoming"}`)
	requireProfileFile(t, foreignStage, "private.json", `{"owner":"independent"}`)
}

func TestClaudePrepareBindsExpiredCredentialsOffline(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "profile")
	credentials := `{"claudeAiOauth":{"accessToken":"synthetic-team-access","refreshToken":"synthetic-team-refresh","expiresAt":1}}`
	writeProfileFile(t, path, ".credentials.json", credentials)
	fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(credentials))
	if err != nil {
		t.Fatal(err)
	}
	binding := identity.ClaudeIdentityBinding{
		Identity:              identity.Identity{Provider: "claude", AccountID: "same-account", OrganizationID: "team-org", Email: "shared@example.test"},
		CredentialFingerprint: fingerprint, VerifiedAt: time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC),
	}
	metadata, err := json.Marshal(map[string]any{"claude_identity": binding})
	if err != nil {
		t.Fatal(err)
	}
	writeProfileFile(t, path, identity.ClaudeIdentityCacheFile, string(metadata))
	writeProfileFile(t, path, ".claude.json", `{"oauthAccount":{"organizationUuid":"stale-personal-org","emailAddress":"shared@example.test"},"theme":"dark"}`)
	prepared, err := Prepare(captureTestProfile(t, path), filepath.Join(root, "staged"), "claude")
	if err != nil {
		t.Fatal(err)
	}
	proof, err := InspectClaude(prepared)
	if err != nil || proof.Binding == nil || proof.Binding.Identity.OrganizationID != "team-org" || proof.BindingFile != "meta.json" {
		t.Fatalf("expired exact grant lost offline proof: %v", err)
	}
	requireProfileFile(t, prepared.Path(), ".credentials.json", credentials)
	state, _, err := prepared.ReadFile(".claude.json")
	if err != nil || strings.Contains(string(state), "stale-personal-org") || !strings.Contains(string(state), "team-org") || !strings.Contains(string(state), "dark") {
		t.Fatalf("saved state was not normalized from proof: %v", err)
	}
	if _, exists, err := prepared.ReadFile(identity.ClaudeIdentityCacheFile); err != nil || exists {
		t.Fatalf("redundant native proof was retained: %v", err)
	}
	personal := *proof.Binding
	personal.Identity.OrganizationID = "personal-org"
	if err := CheckClaudeReplacement(&ClaudeIdentity{Binding: &personal, Fingerprint: fingerprint}, proof); err == nil {
		t.Fatal("same email and account authorized a different organization")
	}
	writeProfileFile(t, path, ".credentials.json", `{"claudeAiOauth":{"accessToken":"synthetic-other-access","refreshToken":"synthetic-team-refresh"}}`)
	if _, err := InspectClaude(captureTestProfile(t, path)); err == nil {
		t.Fatal("mismatched access token retained cached identity")
	}
}

func TestClaudeSettingsAuthorityDoesNotInheritIgnoredLegacyIdentity(t *testing.T) {
	const legacy = `{"oauthToken":{"accessToken":"synthetic-ignored-access","refreshToken":"synthetic-ignored-refresh"},"oauthAccount":{"accountUuid":"same-account","organizationUuid":"personal-org","emailAddress":"shared@example.test"},"theme":"dark"}`
	for _, test := range []struct {
		name     string
		settings string
	}{
		{"helper", `{"apiKeyHelper":"SYNTHETIC_VALUE","permissions":{"allow":["Read"]}}`},
		{"api_key_camel", `{"apiKey":"SYNTHETIC_VALUE"}`},
		{"api_key_snake", `{"api_key":"SYNTHETIC_VALUE"}`},
		{"environment_api_key", `{"env":{"ANTHROPIC_API_KEY":"SYNTHETIC_VALUE"}}`},
		{"environment_auth_token", `{"env":{"ANTHROPIC_AUTH_TOKEN":"SYNTHETIC_VALUE"}}`},
		{"environment_oauth_token", `{"env":{"CLAUDE_CODE_OAUTH_TOKEN":"SYNTHETIC_VALUE"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			var sources [2]*Snapshot
			var owners [2]*ClaudeIdentity
			for index, generation := range []string{"saved", "incoming"} {
				path := filepath.Join(root, generation)
				settings := strings.ReplaceAll(test.settings, "SYNTHETIC_VALUE", "synthetic-"+generation)
				writeProfileFile(t, path, "settings.json", settings)
				writeProfileFile(t, path, ".claude.json", legacy)
				sources[index] = captureTestProfile(t, path)
				owner, err := InspectClaude(sources[index])
				if err != nil || owner == nil {
					t.Fatalf("supported settings grant could not be inspected: %v", err)
				}
				if owner.Fingerprint != "" || owner.Binding != nil || owner.BindingFile != "" {
					t.Fatal("selected settings grant inherited ignored OAuth ownership")
				}
				owners[index] = owner
			}
			if err := CheckClaudeReplacement(owners[0], owners[1]); err == nil {
				t.Fatal("different settings grants were authorized by identical ignored OAuth credentials")
			}
			prepared, err := Prepare(sources[1], filepath.Join(root, "prepared"), "claude")
			if err != nil {
				t.Fatal(err)
			}
			requireProfileFile(t, prepared.Path(), "settings.json", strings.ReplaceAll(test.settings, "SYNTHETIC_VALUE", "synthetic-incoming"))
			state, exists, err := prepared.ReadFile(".claude.json")
			var saved map[string]json.RawMessage
			if err != nil || !exists || json.Unmarshal(state, &saved) != nil {
				t.Fatalf("prepared Claude state is unavailable: %v", err)
			}
			if _, exists := saved["oauthAccount"]; exists {
				t.Fatal("unverified account labels survived preparation")
			}
			if string(saved["theme"]) != `"dark"` || len(saved["oauthToken"]) == 0 {
				t.Fatal("preparation changed unrelated saved state")
			}
			if err := sources[1].Check(); err != nil {
				t.Fatalf("inspection or preparation modified the source: %v", err)
			}
		})
	}
}

func TestClaudeSettingsAuthorityRejectsIgnoredLegacyProof(t *testing.T) {
	credentials := `{"claudeAiOauth":{"accessToken":"synthetic-ignored-access","refreshToken":"synthetic-ignored-refresh"}}`
	fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(credentials))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"claude_identity": identity.ClaudeIdentityBinding{
		Identity:              identity.Identity{Provider: "claude", AccountID: "same-account", OrganizationID: "personal-org", Email: "shared@example.test"},
		CredentialFingerprint: fingerprint,
		VerifiedAt:            time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, proofFile := range []string{"meta.json", identity.ClaudeIdentityCacheFile} {
		t.Run(proofFile, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "incoming")
			writeProfileFile(t, path, "settings.json", `{"apiKeyHelper":"synthetic-selected-helper"}`)
			writeProfileFile(t, path, ".claude.json", `{"oauthToken":{"accessToken":"synthetic-ignored-access","refreshToken":"synthetic-ignored-refresh"}}`)
			writeProfileFile(t, path, proofFile, string(metadata))
			captured := captureTestProfile(t, path)
			if _, err := InspectClaude(captured); err == nil {
				t.Fatal("proof for ignored legacy credentials granted settings-based ownership")
			} else if strings.Contains(err.Error(), "synthetic-") {
				t.Fatal("inspection error disclosed credential material")
			}
			if _, err := Prepare(captured, filepath.Join(root, "prepared"), "claude"); err == nil {
				t.Fatal("mismatched selected-authority proof was prepared for publication")
			}
			if err := captured.Check(); err != nil {
				t.Fatalf("rejected proof changed its source: %v", err)
			}
		})
	}
}

func TestClaudeSettingsAuthorityPreservesPrimaryPrecedence(t *testing.T) {
	canonical := `{"claudeAiOauth":{"accessToken":"synthetic-primary-access","refreshToken":"synthetic-primary-refresh"}}`
	fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(canonical))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		filename    string
		credentials string
		fingerprint string
	}{
		{"modern", ".credentials.json", canonical, fingerprint},
		{"legacy_auth", "auth.json", `{"access_token":"synthetic-primary-access","refresh_token":"synthetic-primary-refresh"}`, fingerprint},
		{"legacy_key", "auth.json", `{"api_key":"synthetic-primary-key"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, settings := range []string{`{"env":{"ANTHROPIC_API_KEY":"synthetic-ignored-key"}}`, `{"apiKeyHelper":null}`, `{`} {
				path := t.TempDir()
				writeProfileFile(t, path, test.filename, test.credentials)
				writeProfileFile(t, path, "settings.json", settings)
				writeProfileFile(t, path, ".claude.json", `{"oauthToken":{"accessToken":"synthetic-ignored-access","refreshToken":"synthetic-ignored-refresh"}}`)
				owner, err := InspectClaude(captureTestProfile(t, path))
				if err != nil || owner == nil || owner.Fingerprint != test.fingerprint {
					t.Fatalf("unselected settings changed the primary credential authority: %v", err)
				}
			}
		})
	}
}

func TestClaudePolicyOnlySettingsPreserveLegacyProof(t *testing.T) {
	credentials := `{"claudeAiOauth":{"accessToken":"synthetic-legacy-access","refreshToken":"synthetic-legacy-refresh"}}`
	fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(credentials))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := json.Marshal(map[string]any{"claude_identity": identity.ClaudeIdentityBinding{
		Identity:              identity.Identity{Provider: "claude", AccountID: "same-account", OrganizationID: "team-org", Email: "shared@example.test"},
		CredentialFingerprint: fingerprint,
		VerifiedAt:            time.Date(2026, 11, 1, 1, 2, 3, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, settings := range []string{
		`{}`,
		`{"permissions":{"allow":["Read"]},"model":"claude-sonnet"}`,
		`{"apiKeyHelper":"","apiKey":" ","api_key":"","env":{"ANTHROPIC_BASE_URL":"https://example.invalid","ANTHROPIC_API_KEY":"","ANTHROPIC_AUTH_TOKEN":"","CLAUDE_CODE_OAUTH_TOKEN":""}}`,
	} {
		path := t.TempDir()
		writeProfileFile(t, path, "settings.json", settings)
		writeProfileFile(t, path, ".claude.json", `{"oauthToken":{"accessToken":"synthetic-legacy-access","refreshToken":"synthetic-legacy-refresh"}}`)
		writeProfileFile(t, path, "meta.json", string(metadata))
		owner, err := InspectClaude(captureTestProfile(t, path))
		if err != nil || owner == nil || owner.Fingerprint != fingerprint || owner.Binding == nil || owner.Binding.Identity.OrganizationID != "team-org" {
			t.Fatalf("policy-only settings displaced verified legacy authority: %v", err)
		}
	}
}

func TestClaudeMalformedSettingsCannotFallBackToLegacyIdentity(t *testing.T) {
	for _, settings := range []string{
		``, `{`, `null`, `[]`,
		`{"apiKeyHelper":null}`, `{"apiKey":23}`, `{"api_key":{}}`,
		`{"env":null}`, `{"env":[]}`,
		`{"env":{"ANTHROPIC_API_KEY":true}}`,
		`{"env":{"ANTHROPIC_AUTH_TOKEN":23}}`,
		`{"env":{"CLAUDE_CODE_OAUTH_TOKEN":null}}`,
	} {
		path := t.TempDir()
		writeProfileFile(t, path, "settings.json", settings)
		writeProfileFile(t, path, ".claude.json", `{"oauthToken":{"accessToken":"synthetic-ignored-access","refreshToken":"synthetic-ignored-refresh"}}`)
		if _, err := InspectClaude(captureTestProfile(t, path)); err == nil {
			t.Fatal("malformed selected settings inherited ignored legacy identity")
		} else if strings.Contains(err.Error(), "synthetic-") {
			t.Fatal("inspection error disclosed credential material")
		}
	}
}

func TestProfilePathRejectsAliasesAndReservedNames(t *testing.T) {
	for _, component := range []string{"vault", "provider", "profile"} {
		t.Run(component, func(t *testing.T) {
			root := t.TempDir()
			vault := filepath.Join(root, "vault")
			path := map[string]string{"vault": vault, "provider": filepath.Join(vault, "claude"), "profile": filepath.Join(vault, "claude", "work")}[component]
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			if err := os.Symlink(outside, path); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			if _, err := ProfilePath(vault, "claude", "work"); err == nil {
				t.Fatal("vault path alias was accepted")
			}
		})
	}
	for _, name := range []string{"../escape", "_original", "_caam-vault-backup~private", ".caam_import_partial", ".import_tmp_work_123"} {
		if err := ValidateProfileRef("claude", name); err == nil {
			t.Fatalf("reserved or escaping profile name accepted: %q", name)
		}
	}
}

func TestClaudeTransferRejectsNativeFilenameCaseAliases(t *testing.T) {
	for _, name := range []string{".CREDENTIALS.JSON", "AUTH.JSON", ".CLAUDE.JSON", "SETTINGS.JSON", "META.JSON", ".CAAM-IDENTITY.JSON"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "incoming")
			writeProfileFile(t, source, name, `{"access_token":"synthetic-alias-owner"}`)
			captured := captureTestProfile(t, source)
			if _, err := InspectClaude(captured); err == nil {
				t.Fatal("noncanonical native authority was accepted for transfer")
			}
			destination := filepath.Join(root, "vault", "claude", "work")
			writeProfileFile(t, destination, "auth.json", `{"apiKey":"synthetic-existing-owner"}`)
			previous := captureTestProfile(t, destination)
			if _, err := Publish(captured, previous, &Options{Provider: "claude"}); err == nil {
				t.Fatal("ambiguous native authority was published")
			}
			if err := previous.Check(); err != nil {
				t.Fatalf("rejected alias changed the existing profile: %v", err)
			}
		})
	}
}

func TestClaudeTransferRejectsOversizedSelectedFilesBeforeDecode(t *testing.T) {
	for _, test := range []struct {
		name  string
		limit int64
	}{
		{".credentials.json", authfile.MaxDiscoveryFileBytes},
		{"auth.json", authfile.MaxDiscoveryFileBytes},
		{"meta.json", identity.MaxClaudeIdentityBytes},
		{identity.ClaudeIdentityCacheFile, identity.MaxClaudeIdentityBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := t.TempDir()
			file, err := os.OpenFile(filepath.Join(path, test.name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			truncateErr := file.Truncate(test.limit + 1)
			closeErr := file.Close()
			if truncateErr != nil || closeErr != nil {
				t.Fatalf("create oversized fixture: %v, %v", truncateErr, closeErr)
			}
			_, err = InspectClaude(captureTestProfile(t, path))
			if err == nil || !strings.Contains(err.Error(), "supported size") {
				t.Fatalf("oversized selected input reached JSON decoding: %v", err)
			}
		})
	}
}
