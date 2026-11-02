package bundle

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

// Write raw manifests so recovery tests exercise the importer, including cases
// a normal exporter would reject. Every payload has its exact SHA-256 checksum.
func writeRecoveryBundle(t *testing.T, manifest *ManifestV1, files map[string]string) string {
	t.Helper()
	manifest.Source.Hostname = "synthetic-recovery-test"
	for name, data := range files {
		if name == EncryptionMarkerFile {
			continue
		}
		if _, exists := manifest.Checksums.Files[name]; !exists {
			checksum, err := ComputeDataChecksum([]byte(data), AlgorithmSHA256)
			if err != nil {
				t.Fatal(err)
			}
			manifest.AddChecksum(name, checksum)
		}
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "recovery.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, contents := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(contents)); err != nil {
			t.Fatal(err)
		}
	}
	entry, err := w.Create(ManifestFileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRecoveryFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func requireRecoveryFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Errorf("preserved file %s: got %q, error %v; want %q", path, got, err, want)
	}
}

func claudeBundleCredentials(t *testing.T, access, refresh string, expiry time.Time) string {
	t.Helper()
	oauth := map[string]any{"accessToken": access, "refreshToken": refresh}
	if !expiry.IsZero() {
		oauth["expiresAt"] = expiry.UnixMilli()
	}
	data, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func claudeBundleMetadata(t *testing.T, credentials, account, organization string) string {
	t.Helper()
	fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(credentials))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"description": "preserve unrelated profile metadata",
		"claude_identity": identity.ClaudeIdentityBinding{
			Identity: identity.Identity{
				Provider: "claude", AccountID: account, OrganizationID: organization,
				Email: "shared@example.test", Organization: organization,
			},
			CredentialFingerprint: fingerprint,
			VerifiedAt:            time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func proveLegacyBundleProfile(t *testing.T, profilePath string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(profilePath, ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}
	var legacy struct {
		OAuthToken struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
		} `json:"oauthToken"`
	}
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	credentials := claudeBundleCredentials(t, legacy.OAuthToken.Access, legacy.OAuthToken.Refresh, time.Time{})
	writeRecoveryFile(t, filepath.Join(profilePath, "meta.json"), claudeBundleMetadata(t, credentials, "account-shared", "org-personal"))
}

func TestVaultImporterClaudeSmartOwnership(t *testing.T) {
	cases := []struct {
		name                              string
		localBound, incomingBound         bool
		samePair, sameAccess, sameRefresh bool
		incomingAccount, incomingOrg      string
		breakLocal                        bool
		wantAction, reason                string
	}{
		{name: "verified rotation", localBound: true, incomingBound: true, wantAction: "update", reason: "fresher"},
		{name: "same email different organization", localBound: true, incomingBound: true, incomingOrg: "org-team", wantAction: "skip", reason: "organization differs"},
		{name: "different account", localBound: true, incomingBound: true, incomingAccount: "account-other", wantAction: "skip", reason: "account or organization differs"},
		{name: "unknown local owner", incomingBound: true, wantAction: "skip", reason: "ownership is unverified"},
		{name: "unknown incoming owner", localBound: true, wantAction: "skip", reason: "ownership is unverified"},
		{name: "same email is not proof", wantAction: "skip", reason: "ownership is unverified"},
		{name: "exact unverified pair", samePair: true, wantAction: "update", reason: "fresher"},
		{name: "same access different refresh", sameAccess: true, wantAction: "skip", reason: "ownership is unverified"},
		{name: "different access same refresh", sameRefresh: true, wantAction: "skip", reason: "ownership is unverified"},
		{name: "exact pair gains proof", samePair: true, incomingBound: true, wantAction: "update", reason: "importing verified"},
		{name: "exact pair preserves proof", samePair: true, localBound: true, wantAction: "skip", reason: "discard its verification"},
		{name: "conflicting proof for identical pair", samePair: true, localBound: true, incomingBound: true, incomingOrg: "org-team", wantAction: "skip", reason: "organization differs"},
		{name: "local credentials replaced", localBound: true, incomingBound: true, breakLocal: true, wantAction: "skip", reason: "credentials changed"},
	}
	for _, test := range cases {
		for _, dryRun := range []bool{false, true} {
			t.Run(test.name+"/dry="+fmt.Sprint(dryRun), func(t *testing.T) {
				vault := filepath.Join(t.TempDir(), "vault")
				profile := filepath.Join(vault, "claude", "shared@example.test")
				expiry := time.Now().Add(12 * time.Hour).Truncate(time.Second)
				localCredentials := claudeBundleCredentials(t, "synthetic-local-access", "synthetic-local-refresh", expiry)
				access, refresh := "synthetic-incoming-access", "synthetic-incoming-refresh"
				if test.samePair || test.sameAccess {
					access = "synthetic-local-access"
				}
				if test.samePair || test.sameRefresh {
					refresh = "synthetic-local-refresh"
				}
				incomingCredentials := claudeBundleCredentials(t, access, refresh, expiry.Add(24*time.Hour))
				localMetadata := `{"email":"shared@example.test","organization":"Personal"}`
				incomingMetadata := `{"email":"shared@example.test","organization":"Personal"}`
				if test.localBound {
					localMetadata = claudeBundleMetadata(t, localCredentials, "account-shared", "org-personal")
				}
				if test.breakLocal {
					localCredentials = claudeBundleCredentials(t, "synthetic-replaced-access", "synthetic-local-refresh", expiry)
				}
				account, organization := test.incomingAccount, test.incomingOrg
				if account == "" {
					account = "account-shared"
				}
				if organization == "" {
					organization = "org-personal"
				}
				if test.incomingBound {
					incomingMetadata = claudeBundleMetadata(t, incomingCredentials, account, organization)
				}
				writeRecoveryFile(t, filepath.Join(profile, ".credentials.json"), localCredentials)
				writeRecoveryFile(t, filepath.Join(profile, "meta.json"), localMetadata)
				writeRecoveryFile(t, filepath.Join(profile, "settings.json"), `{"isolation":"original-local"}`)
				manifest := NewManifest()
				manifest.AddProfile("claude", "shared@example.test")
				bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
					"vault/claude/shared@example.test/.credentials.json": incomingCredentials,
					"vault/claude/shared@example.test/meta.json":         incomingMetadata,
					"vault/claude/shared@example.test/.claude.json":      `{"oauthAccount":{"emailAddress":"shared@example.test","organizationName":"stale shared label"},"numStartups":7,"projects":{"/synthetic/project":{"trusted":true}}}`,
				})
				result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vault, Mode: ImportModeSmart, DryRun: dryRun})
				if err != nil || result == nil || len(result.ProfileActions) != 1 {
					t.Fatalf("smart import: result=%+v, error=%v", result, err)
				}
				action := result.ProfileActions[0]
				if action.Action != test.wantAction || !strings.Contains(action.Reason, test.reason) {
					t.Fatalf("action=%+v, want %s (%s)", action, test.wantAction, test.reason)
				}
				if dryRun || test.wantAction == "skip" {
					requireRecoveryFile(t, filepath.Join(profile, ".credentials.json"), localCredentials)
					requireRecoveryFile(t, filepath.Join(profile, "meta.json"), localMetadata)
					requireRecoveryFile(t, filepath.Join(profile, "settings.json"), `{"isolation":"original-local"}`)
					if action.RecoveryPath != "" {
						t.Fatal("read-only decision created a recovery snapshot")
					}
				} else {
					requireRecoveryFile(t, filepath.Join(profile, ".credentials.json"), incomingCredentials)
					requireRecoveryFile(t, filepath.Join(action.RecoveryPath, ".credentials.json"), localCredentials)
					requireRecoveryFile(t, filepath.Join(action.RecoveryPath, "meta.json"), localMetadata)
					requireRecoveryFile(t, filepath.Join(action.RecoveryPath, "settings.json"), `{"isolation":"original-local"}`)
					if !authfile.IsPrivateVaultEntry(filepath.Base(filepath.Dir(action.RecoveryPath))) {
						t.Fatalf("recovery path is routable: %s", action.RecoveryPath)
					}
					settings, err := os.ReadFile(filepath.Join(profile, ".claude.json"))
					if err != nil {
						t.Fatal(err)
					}
					var state struct {
						Account *struct {
							OrganizationID string `json:"organizationUuid"`
						} `json:"oauthAccount"`
						NumStartups int `json:"numStartups"`
						Projects    map[string]struct {
							Trusted bool `json:"trusted"`
						} `json:"projects"`
					}
					if err := json.Unmarshal(settings, &state); err != nil || state.NumStartups != 7 || !state.Projects["/synthetic/project"].Trusted {
						t.Fatalf("normalizing account changed unrelated state: %+v, %v", state, err)
					}
					if test.incomingBound {
						if state.Account == nil || state.Account.OrganizationID != organization {
							t.Fatalf("import retained stale account label: %+v", state.Account)
						}
					} else if state.Account != nil || !strings.Contains(action.Reason, "labels omitted") {
						t.Fatalf("unverified account label retained without warning: state=%+v, action=%+v", state.Account, action)
					}
				}
				profiles, err := authfile.NewVault(vault).List("claude")
				if err != nil || len(profiles) != 1 || profiles[0] != "shared@example.test" {
					t.Fatalf("profile isolation changed: %v, %v", profiles, err)
				}
				if dryRun {
					if _, err := os.Lstat(filepath.Join(vault, ".caam-mutation.lock")); !os.IsNotExist(err) {
						t.Fatalf("dry run mutated vault lock: %v", err)
					}
				}
			})
		}
	}
}

func TestVaultImporterClaudePrimaryControlsFreshness(t *testing.T) {
	for _, test := range []struct {
		name                      string
		primaryHours, legacyHours int
		unknownPrimary            bool
		wantAction                string
	}{
		{name: "new primary old secondary", primaryHours: 48, legacyHours: 1, wantAction: "update"},
		{name: "old primary newer secondary", primaryHours: 1, legacyHours: 96, wantAction: "skip"},
		{name: "unknown primary newer secondary", unknownPrimary: true, legacyHours: 96, wantAction: "skip"},
	} {
		t.Run(test.name, func(t *testing.T) {
			vault := filepath.Join(t.TempDir(), "vault")
			profile := filepath.Join(vault, "claude", "work")
			now := time.Now().Truncate(time.Second)
			localCredentials := claudeBundleCredentials(t, "synthetic-local", "synthetic-local-refresh", now.Add(12*time.Hour))
			writeRecoveryFile(t, filepath.Join(profile, ".credentials.json"), localCredentials)
			writeRecoveryFile(t, filepath.Join(profile, "meta.json"), claudeBundleMetadata(t, localCredentials, "account-shared", "org-personal"))
			expiry := now.Add(time.Duration(test.primaryHours) * time.Hour)
			if test.unknownPrimary {
				expiry = time.Time{}
			}
			incoming := claudeBundleCredentials(t, "synthetic-primary", "synthetic-primary-refresh", expiry)
			legacy := fmt.Sprintf(`{"oauthToken":{"access_token":"synthetic-secondary","refresh_token":"synthetic-secondary-refresh","expiry":%q}}`, now.Add(time.Duration(test.legacyHours)*time.Hour).Format(time.RFC3339))
			manifest := NewManifest()
			manifest.AddProfile("claude", "work")
			bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
				"vault/claude/work/.credentials.json": incoming,
				"vault/claude/work/.claude.json":      legacy,
				"vault/claude/work/meta.json":         claudeBundleMetadata(t, incoming, "account-shared", "org-personal"),
			})
			result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vault, Mode: ImportModeSmart})
			if err != nil || result == nil || len(result.ProfileActions) != 1 || result.ProfileActions[0].Action != test.wantAction {
				t.Fatalf("primary freshness decision: %+v, %v", result, err)
			}
			want := localCredentials
			if test.wantAction == "update" {
				want = incoming
			}
			requireRecoveryFile(t, filepath.Join(profile, ".credentials.json"), want)
		})
	}
}

func TestVaultImporterClaudeAuthJSONDoesNotMatchOrUseSecondaryLogin(t *testing.T) {
	for _, test := range []struct {
		name, incomingAuth, secondary string
		wantError                     bool
	}{
		{name: "different selected grants identical secondary pair", incomingAuth: `{"access_token":"synthetic-team-access","refresh_token":"synthetic-team-refresh"}`},
		{name: "same selected pair has no supported expiry", incomingAuth: `{"access_token":"synthetic-personal-access","refresh_token":"synthetic-personal-refresh"}`},
		{name: "API key must not inherit secondary pair", incomingAuth: `{"apiKey":"synthetic-team-key"}`},
		{name: "malformed unused secondary", incomingAuth: `{"access_token":"synthetic-team-access","refresh_token":"synthetic-team-refresh"}`, secondary: `{"ignored secondary`},
		{name: "malformed selected source cannot fall back", incomingAuth: `{"access_token":42}`, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			vault := filepath.Join(t.TempDir(), "vault")
			profile := filepath.Join(vault, "claude", "work")
			localAuth := `{"access_token":"synthetic-personal-access","refresh_token":"synthetic-personal-refresh"}`
			writeRecoveryFile(t, filepath.Join(profile, "auth.json"), localAuth)
			writeRecoveryFile(t, filepath.Join(profile, ".claude.json"), `{"oauthToken":{"access_token":"synthetic-unused","refresh_token":"synthetic-unused-refresh","expiry":"2000-01-01T00:00:00Z"}}`)
			secondary := test.secondary
			if secondary == "" {
				secondary = `{"oauthToken":{"access_token":"synthetic-unused","refresh_token":"synthetic-unused-refresh","expiry":"2099-01-01T00:00:00Z"}}`
			}
			manifest := NewManifest()
			manifest.AddProfile("claude", "work")
			bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
				"vault/claude/work/auth.json":    test.incomingAuth,
				"vault/claude/work/.claude.json": secondary,
			})
			result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vault, Mode: ImportModeSmart})
			if (err != nil) != test.wantError || result == nil || result.UpdatedProfiles != 0 {
				t.Fatalf("ignored secondary selected another login: %+v, %v", result, err)
			}
			if !test.wantError && result.SkippedProfiles != 1 {
				t.Fatalf("unproven selected auth was not preserved: %+v", result)
			}
			requireRecoveryFile(t, filepath.Join(profile, "auth.json"), localAuth)
		})
	}
}

func TestVaultImporterRejectsInvalidClaudeIdentityBeforeAnyChanges(t *testing.T) {
	credentials := claudeBundleCredentials(t, "synthetic-private-access", "synthetic-private-refresh", time.Now().Add(time.Hour))
	otherCredentials := claudeBundleCredentials(t, "synthetic-substituted-access", "synthetic-private-refresh", time.Now().Add(2*time.Hour))
	validMetadata := claudeBundleMetadata(t, credentials, "account-shared", "org-team")
	for _, test := range []struct {
		name, credentials, metadata, cache string
	}{
		{name: "changed access token", credentials: otherCredentials, metadata: validMetadata},
		{name: "malformed proof", credentials: credentials, metadata: `{"claude_identity":{"secret":"synthetic-private-access"}}`},
		{name: "invalid metadata", credentials: credentials, metadata: `{"synthetic-private-access"`},
		{name: "invalid primary cannot fall back", credentials: `{"claudeAiOauth":{"accessToken":42}}`, metadata: `{}`},
		{name: "stale native cache without canonical proof", credentials: credentials, metadata: `{}`, cache: claudeBundleMetadata(t, otherCredentials, "account-shared", "org-team")},
		{name: "changed canonical proof cannot fall back", credentials: otherCredentials, metadata: validMetadata, cache: claudeBundleMetadata(t, otherCredentials, "account-shared", "org-team")},
		{name: "invalid canonical proof cannot fall back", credentials: credentials, metadata: `{"claude_identity":false}`, cache: validMetadata},
	} {
		for _, mode := range []ImportMode{ImportModeSmart, ImportModeMerge, ImportModeReplace} {
			for _, dryRun := range []bool{false, true} {
				t.Run(test.name+"/"+string(mode)+"/dry="+fmt.Sprint(dryRun), func(t *testing.T) {
					root := t.TempDir()
					vault := filepath.Join(root, "vault")
					localPath := filepath.Join(vault, "claude", "work", ".credentials.json")
					writeRecoveryFile(t, localPath, credentials)
					configPath := filepath.Join(root, "config.json")
					writeRecoveryFile(t, configPath, "original-config")
					manifest := NewManifest()
					manifest.AddProfile("claude", "work")
					manifest.AddProfile("codex", "new-account")
					manifest.SetConfig(true, "config.json")
					files := map[string]string{
						"vault/claude/work/.credentials.json": test.credentials,
						"vault/claude/work/meta.json":         test.metadata,
						"vault/claude/work/.claude.json":      `{"oauthToken":{"access_token":"valid-secondary","refresh_token":"secondary-refresh","expiry":"2099-01-01T00:00:00Z"}}`,
						"vault/codex/new-account/auth.json":   `{"access_token":"synthetic-new-account"}`,
						"config.json":                         "incoming-config",
					}
					if test.cache != "" {
						files["vault/claude/work/"+identity.ClaudeIdentityCacheFile] = test.cache
					}
					bundlePath := writeRecoveryBundle(t, manifest, files)
					result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vault, ConfigPath: configPath, Mode: mode, DryRun: dryRun})
					if err == nil || result == nil || result.NewProfiles != 0 || result.UpdatedProfiles != 0 || len(result.Errors) != 1 {
						t.Fatalf("invalid incoming ownership accepted: %+v, %v", result, err)
					}
					if strings.Contains(err.Error(), "synthetic-private") || strings.Contains(err.Error(), "synthetic-substituted") {
						t.Fatalf("error disclosed credential material: %v", err)
					}
					if len(result.ProfileActions) != 1 || result.ProfileActions[0].Action != "error" {
						t.Fatalf("invalid profile not reported: %+v", result.ProfileActions)
					}
					requireRecoveryFile(t, localPath, credentials)
					requireRecoveryFile(t, configPath, "original-config")
					if _, err := os.Lstat(filepath.Join(vault, "codex", "new-account")); !os.IsNotExist(err) {
						t.Fatalf("invalid bundle partly imported another profile: %v", err)
					}
				})
			}
		}
	}
}

func TestVaultImporterPreparesAllProfilesBeforePublicationAndPreview(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("existing=%t/dry=%t", existing, dryRun), func(t *testing.T) {
				root := t.TempDir()
				vault := filepath.Join(root, "vault")
				old := claudeBundleCredentials(t, "synthetic-personal-access", "synthetic-personal-refresh", time.Now().Add(-time.Hour))
				incoming := claudeBundleCredentials(t, "synthetic-team-access", "synthetic-team-refresh", time.Now().Add(-time.Hour))
				if !dryRun || existing {
					// A real import may acquire an existing managed lock. Seed the
					// normal vault layout so the whole-tree assertion detects any
					// publication, recovery, or leftover staging directory.
					writeRecoveryFile(t, filepath.Join(vault, ".caam-mutation.lock"), "")
					writeRecoveryFile(t, filepath.Join(vault, "codex", "unrelated", "auth.json"), `{"api_key":"synthetic-unrelated"}`)
				}
				if existing {
					profile := filepath.Join(vault, "claude", "a-first")
					writeRecoveryFile(t, filepath.Join(profile, ".credentials.json"), old)
					writeRecoveryFile(t, filepath.Join(profile, "meta.json"), claudeBundleMetadata(t, old, "shared-account", "personal-org"))
					writeRecoveryFile(t, filepath.Join(profile, "private", "history.json"), `{"history":"keep-original"}`)
				}
				before, err := vaulttransfer.Capture(vault)
				if err != nil {
					t.Fatal(err)
				}
				config := filepath.Join(root, "config.json")
				writeRecoveryFile(t, config, `{"config":"keep-original"}`)
				manifest := NewManifest()
				manifest.AddProfile("claude", "a-first")
				manifest.AddProfile("claude", "z-malformed")
				manifest.SetConfig(true, "config.json")
				metadata := claudeBundleMetadata(t, incoming, "shared-account", "team-org")
				bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
					"vault/claude/a-first/.credentials.json":     incoming,
					"vault/claude/a-first/meta.json":             metadata,
					"vault/claude/a-first/.claude.json":          `{"oauthAccount":{"organizationUuid":"stale-personal-org"},"projects":{"project":{"trusted":true}}}`,
					"vault/claude/z-malformed/.credentials.json": incoming,
					"vault/claude/z-malformed/meta.json":         metadata,
					"vault/claude/z-malformed/.claude.json":      `{"oauthAccount":{"synthetic-private-label":`,
					"config.json":                                `{"config":"incoming"}`,
				})
				published := false
				importer := &VaultImporter{BundlePath: bundlePath, beforeProfilePublish: func(_, _ string) { published = true }}
				result, err := importer.Import(&ImportOptions{VaultPath: vault, ConfigPath: config, Mode: ImportModeReplace, DryRun: dryRun})
				if err == nil || !strings.Contains(err.Error(), "invalid Claude account settings") || result == nil {
					t.Fatalf("malformed later settings were accepted: result=%+v, error=%v", result, err)
				}
				if published || result.NewProfiles != 0 || result.UpdatedProfiles != 0 || len(result.Errors) != 1 || len(result.OptionalActions) != 0 {
					t.Fatalf("invalid import published or previewed a partial success: published=%t result=%+v", published, result)
				}
				if len(result.ProfileActions) != 1 || result.ProfileActions[0].Profile != "z-malformed" || result.ProfileActions[0].Action != "error" {
					t.Fatalf("normalization failure was not reported against its profile: %+v", result.ProfileActions)
				}
				if strings.Contains(err.Error(), "synthetic-private-label") || strings.Contains(strings.Join(result.Errors, " "), "synthetic-private-label") {
					t.Fatal("normalization error exposed private settings")
				}
				if err := before.Check(); err != nil {
					t.Fatalf("failed normalization changed the destination vault: %v", err)
				}
				requireRecoveryFile(t, config, `{"config":"keep-original"}`)
				if dryRun && !existing {
					if _, err := os.Lstat(vault); !os.IsNotExist(err) {
						t.Fatalf("dry-run created a vault or managed lock: %v", err)
					}
				}
			})
		}
	}
}

func TestVaultImporterChecksOriginalSourcesAfterPreparingAllProfiles(t *testing.T) {
	root := t.TempDir()
	vault := filepath.Join(root, "vault")
	writeRecoveryFile(t, filepath.Join(vault, ".caam-mutation.lock"), "")
	writeRecoveryFile(t, filepath.Join(vault, "claude", "unrelated", "auth.json"), `{"api_key":"synthetic-unrelated"}`)
	before, err := vaulttransfer.Capture(vault)
	if err != nil {
		t.Fatal(err)
	}
	credentials := claudeBundleCredentials(t, "synthetic-expired-access", "synthetic-expired-refresh", time.Now().Add(-time.Hour))
	metadata := claudeBundleMetadata(t, credentials, "shared-account", "team-org")
	manifest := NewManifest()
	files := make(map[string]string)
	for _, profile := range []string{"a-first", "z-later"} {
		manifest.AddProfile("claude", profile)
		files["vault/claude/"+profile+"/.credentials.json"] = credentials
		files["vault/claude/"+profile+"/meta.json"] = metadata
	}
	changed := false
	importer := &VaultImporter{BundlePath: writeRecoveryBundle(t, manifest, files)}
	importer.beforeProfilePublish = func(source, _ string) {
		if filepath.Base(source) != "a-first" {
			t.Fatalf("publication hook no longer identifies the original source: %s", source)
		}
		writeRecoveryFile(t, filepath.Join(filepath.Dir(source), "z-later", ".credentials.json"), `{"claudeAiOauth":{"accessToken":"synthetic-concurrent","refreshToken":"synthetic-concurrent-refresh"}}`)
		changed = true
	}
	result, err := importer.Import(&ImportOptions{VaultPath: vault, Mode: ImportModeSmart})
	if !changed || err == nil || !strings.Contains(err.Error(), "changed during transfer") || result == nil || result.NewProfiles != 0 || result.UpdatedProfiles != 0 {
		t.Fatalf("prepared copy hid a changed original source: changed=%t result=%+v error=%v", changed, result, err)
	}
	// Publication may create and remove its private sibling stage, changing
	// the provider directory's mtime. Every path, mode and payload must remain.
	after, err := vaulttransfer.Capture(vault)
	if err != nil || before.Digest() != after.Digest() {
		t.Fatalf("changed original source partly published the archive: %v", err)
	}
}

func TestVaultImporterClaudeReplaceRetainsOldOrganizationAndExpiredProof(t *testing.T) {
	for _, originalMetadata := range []string{"", `{"description":"keep this description"}`, `{"description":"keep this description","claude_identity":null}`} {
		t.Run("metadata="+originalMetadata, func(t *testing.T) {
			vault := filepath.Join(t.TempDir(), "vault")
			profile := filepath.Join(vault, "claude", "work")
			oldCredentials := claudeBundleCredentials(t, "synthetic-personal-access", "synthetic-personal-refresh", time.Now().Add(time.Hour))
			oldMetadata := claudeBundleMetadata(t, oldCredentials, "account-shared", "org-personal")
			writeRecoveryFile(t, filepath.Join(profile, ".credentials.json"), oldCredentials)
			writeRecoveryFile(t, filepath.Join(profile, "meta.json"), oldMetadata)
			writeRecoveryFile(t, filepath.Join(profile, "nested", "settings.json"), `{"isolation":"personal"}`)
			expired := claudeBundleCredentials(t, "synthetic-team-access", "synthetic-team-refresh", time.Now().Add(-24*time.Hour))
			cache := claudeBundleMetadata(t, expired, "account-shared", "org-team")
			manifest := NewManifest()
			manifest.AddProfile("claude", "work")
			files := map[string]string{
				"vault/claude/work/.credentials.json":                   expired,
				"vault/claude/work/" + identity.ClaudeIdentityCacheFile: cache,
				"vault/claude/work/.claude.json":                        `{"oauthAccount":{"accountUuid":"account-shared","organizationUuid":"org-personal","organizationName":"Personal"},"numStartups":11,"projects":{"/synthetic/team":{"trusted":true}}}`,
			}
			if originalMetadata != "" {
				files["vault/claude/work/meta.json"] = originalMetadata
			}
			bundlePath := writeRecoveryBundle(t, manifest, files)
			result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vault, Mode: ImportModeReplace})
			if err != nil || result == nil || result.UpdatedProfiles != 1 || len(result.ProfileActions) != 1 {
				t.Fatalf("explicit replacement: %+v, %v", result, err)
			}
			recovery := result.ProfileActions[0].RecoveryPath
			if recovery == "" {
				t.Fatal("replacement lost the previous profile's recovery path")
			}
			requireRecoveryFile(t, filepath.Join(recovery, ".credentials.json"), oldCredentials)
			requireRecoveryFile(t, filepath.Join(recovery, "meta.json"), oldMetadata)
			requireRecoveryFile(t, filepath.Join(recovery, "nested", "settings.json"), `{"isolation":"personal"}`)
			requireRecoveryFile(t, filepath.Join(profile, ".credentials.json"), expired)
			state, err := os.ReadFile(filepath.Join(profile, ".claude.json"))
			if err != nil {
				t.Fatal(err)
			}
			var stateDoc struct {
				Account struct {
					OrganizationID string `json:"organizationUuid"`
				} `json:"oauthAccount"`
				NumStartups int `json:"numStartups"`
				Projects    map[string]struct {
					Trusted bool `json:"trusted"`
				} `json:"projects"`
			}
			if err := json.Unmarshal(state, &stateDoc); err != nil || stateDoc.Account.OrganizationID != "org-team" || stateDoc.NumStartups != 11 || !stateDoc.Projects["/synthetic/team"].Trusted {
				t.Fatalf("verified Team import retained Personal label or lost unrelated state: %+v, %v", stateDoc, err)
			}
			metadata, err := os.ReadFile(filepath.Join(profile, "meta.json"))
			if err != nil {
				t.Fatal(err)
			}
			assessment, err := identity.InspectClaudeIdentityBinding(metadata, []byte(expired))
			if err != nil || assessment.Binding == nil || assessment.Binding.Identity.OrganizationID != "org-team" {
				t.Fatalf("expired identity did not persist canonically: %+v, %v", assessment, err)
			}
			if originalMetadata != "" && !strings.Contains(string(metadata), "keep this description") {
				t.Fatal("normalizing identity discarded unrelated profile metadata")
			}
			for _, path := range []string{filepath.Join(profile, ".credentials.json"), filepath.Join(profile, "meta.json"), filepath.Join(recovery, ".credentials.json")} {
				info, err := os.Stat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatalf("credential artifact permissions: %s, %v", path, err)
				}
			}
			// Reopen through the production reader: expiry must not discard an
			// exact credential binding after process restart.
			binding, err := identity.ReadClaudeIdentityBinding(filepath.Join(profile, ".credentials.json"))
			if err != nil || binding == nil || !binding.Identity.Verified || binding.Identity.OrganizationID != "org-team" {
				t.Fatalf("offline restart lost imported identity: %+v, %v", binding, err)
			}
			if _, err := os.Lstat(filepath.Join(profile, identity.ClaudeIdentityCacheFile)); !os.IsNotExist(err) {
				t.Fatalf("import retained duplicate native ownership metadata: %v", err)
			}
			// A legitimate later rotation maintains canonical vault metadata.
			// It must not leave a copied native cache that breaks the next export.
			rotated := claudeBundleCredentials(t, "synthetic-team-rotated-access", "synthetic-team-rotated-refresh", time.Now().Add(12*time.Hour))
			rotatedMetadata := claudeBundleMetadata(t, rotated, "account-shared", "org-team")
			rotatedProof, err := identity.InspectClaudeIdentityBinding([]byte(rotatedMetadata), []byte(rotated))
			if err != nil || rotatedProof.Binding == nil {
				t.Fatalf("invalid rotated fixture: %+v, %v", rotatedProof, err)
			}
			if err := authfile.NewVault(vault).SyncClaudeCredentials("work", []byte(expired), []byte(rotated), rotatedProof.Binding, nil); err != nil {
				t.Fatalf("same-owner synchronization after import: %v", err)
			}
			exported, err := (&VaultExporter{VaultPath: vault}).Export(&ExportOptions{OutputDir: t.TempDir()})
			if err != nil || exported == nil || exported.Manifest.Contents.Vault.TotalProfiles != 1 {
				t.Fatalf("post-rotation export: %+v, %v", exported, err)
			}
			extracted := t.TempDir()
			if err := (&VaultImporter{BundlePath: exported.OutputPath}).extractBundle(extracted); err != nil {
				t.Fatal(err)
			}
			exportedCredential := filepath.Join(extracted, "vault", "claude", "work", ".credentials.json")
			requireRecoveryFile(t, exportedCredential, rotated)
			exportedProof, err := identity.ReadClaudeIdentityBinding(exportedCredential)
			if err != nil || exportedProof == nil || exportedProof.Identity.OrganizationID != "org-team" || !exportedProof.MatchesCredentials([]byte(rotated)) {
				t.Fatalf("post-rotation export lost canonical ownership: %+v, %v", exportedProof, err)
			}
			requireRecoveryFile(t, filepath.Join(recovery, ".credentials.json"), oldCredentials)
		})
	}
}

func TestVaultImporterDetectsGenerationChangesAndRetainsRecovery(t *testing.T) {
	for _, scenario := range []string{"incoming changes", "incoming changes after publication", "destination rotates", "same bytes replaced", "publication fails", "concurrent login before publication", "concurrent login after publication"} {
		t.Run(scenario, func(t *testing.T) {
			vault := filepath.Join(t.TempDir(), "vault")
			profile := filepath.Join(vault, "claude", "work")
			old := claudeBundleCredentials(t, "synthetic-old-access", "synthetic-old-refresh", time.Now().Add(time.Hour))
			incoming := claudeBundleCredentials(t, "synthetic-incoming-access", "synthetic-incoming-refresh", time.Now().Add(2*time.Hour))
			concurrent := claudeBundleCredentials(t, "synthetic-concurrent-access", "synthetic-concurrent-refresh", time.Now().Add(3*time.Hour))
			oldMetadata := claudeBundleMetadata(t, old, "account-shared", "org-personal")
			concurrentMetadata := claudeBundleMetadata(t, concurrent, "account-shared", "org-team")
			writeRecoveryFile(t, filepath.Join(profile, ".credentials.json"), old)
			writeRecoveryFile(t, filepath.Join(profile, "meta.json"), oldMetadata)
			writeRecoveryFile(t, filepath.Join(profile, "private", "history.json"), `{"history":"original-profile"}`)
			manifest := NewManifest()
			manifest.AddProfile("claude", "work")
			bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
				"vault/claude/work/.credentials.json": incoming,
				"vault/claude/work/meta.json":         claudeBundleMetadata(t, incoming, "account-shared", "org-personal"),
			})
			importer := &VaultImporter{BundlePath: bundlePath}
			var incomingSource string
			changedAfterPublication := false
			importer.beforeProfilePublish = func(source, destination string) {
				incomingSource = source
				switch scenario {
				case "incoming changes":
					writeRecoveryFile(t, filepath.Join(source, ".credentials.json"), concurrent)
				case "destination rotates":
					writeRecoveryFile(t, filepath.Join(destination, ".credentials.json"), concurrent)
					writeRecoveryFile(t, filepath.Join(destination, "meta.json"), concurrentMetadata)
				case "same bytes replaced":
					path := filepath.Join(destination, ".credentials.json")
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					replacement := filepath.Join(t.TempDir(), "replacement")
					writeRecoveryFile(t, replacement, old)
					if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(replacement, path); err != nil {
						t.Fatal(err)
					}
				}
			}
			importer.renameProfile = func(from, to string) error {
				if strings.HasPrefix(filepath.Base(from), "_caam-vault-backup~import-") {
					switch scenario {
					case "incoming changes after publication":
						if err := os.Rename(from, to); err != nil {
							return err
						}
						writeRecoveryFile(t, filepath.Join(incomingSource, ".credentials.json"), concurrent)
						writeRecoveryFile(t, filepath.Join(incomingSource, "meta.json"), concurrentMetadata)
						changedAfterPublication = true
						return nil
					case "publication fails":
						return errors.New("injected publication interruption")
					case "concurrent login before publication":
						writeRecoveryFile(t, filepath.Join(to, ".credentials.json"), concurrent)
						writeRecoveryFile(t, filepath.Join(to, "meta.json"), concurrentMetadata)
						return errors.New("destination occupied during publication")
					case "concurrent login after publication":
						if err := os.Rename(from, to); err != nil {
							return err
						}
						writeRecoveryFile(t, filepath.Join(to, ".credentials.json"), concurrent)
						writeRecoveryFile(t, filepath.Join(to, "meta.json"), concurrentMetadata)
						return nil
					}
				}
				return os.Rename(from, to)
			}
			result, err := importer.Import(&ImportOptions{VaultPath: vault, Mode: ImportModeReplace})
			if scenario == "incoming changes after publication" && !changedAfterPublication {
				t.Fatal("source-change fixture did not reach completed publication")
			}
			if err == nil || result == nil || result.UpdatedProfiles != 0 || len(result.ProfileActions) != 1 || result.ProfileActions[0].Action != "error" {
				t.Fatalf("changed or interrupted transfer reported success: %+v, %v", result, err)
			}
			if strings.Contains(err.Error(), "synthetic-") {
				t.Fatalf("error disclosed credentials: %v", err)
			}
			want, wantMetadata := old, oldMetadata
			if scenario == "destination rotates" || strings.HasPrefix(scenario, "concurrent login") {
				want, wantMetadata = concurrent, concurrentMetadata
			}
			requireRecoveryFile(t, filepath.Join(profile, ".credentials.json"), want)
			requireRecoveryFile(t, filepath.Join(profile, "meta.json"), wantMetadata)
			recovery := result.ProfileActions[0].RecoveryPath
			if strings.HasPrefix(scenario, "concurrent login") {
				if recovery == "" {
					t.Fatal("failed publication lost the original snapshot")
				}
				requireRecoveryFile(t, filepath.Join(recovery, ".credentials.json"), old)
				requireRecoveryFile(t, filepath.Join(recovery, "meta.json"), oldMetadata)
				requireRecoveryFile(t, filepath.Join(recovery, "private", "history.json"), `{"history":"original-profile"}`)
			} else if recovery != "" {
				t.Fatalf("reported recovery path after preserving/restoring original: %s", recovery)
			} else {
				requireRecoveryFile(t, filepath.Join(profile, "private", "history.json"), `{"history":"original-profile"}`)
			}
		})
	}
}

func TestBundleOperationsCaptureGenerationAfterManagedLockWait(t *testing.T) {
	for _, operation := range []string{"import", "export"} {
		t.Run(operation, func(t *testing.T) {
			root := t.TempDir()
			vaultPath := filepath.Join(root, "vault")
			profile := filepath.Join(vaultPath, "claude", "work")
			personal := claudeBundleCredentials(t, "synthetic-personal", "synthetic-personal-refresh", time.Now().Add(time.Hour))
			team := claudeBundleCredentials(t, "synthetic-team", "synthetic-team-refresh", time.Now().Add(2*time.Hour))
			teamMetadata := claudeBundleMetadata(t, team, "account-shared", "org-team")
			writeRecoveryFile(t, filepath.Join(profile, ".credentials.json"), personal)
			writeRecoveryFile(t, filepath.Join(profile, "meta.json"), claudeBundleMetadata(t, personal, "account-shared", "org-personal"))
			incoming := claudeBundleCredentials(t, "synthetic-incoming", "synthetic-incoming-refresh", time.Now().Add(3*time.Hour))
			manifest := NewManifest()
			manifest.AddProfile("claude", "work")
			bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
				"vault/claude/work/.credentials.json": incoming,
				"vault/claude/work/meta.json":         claudeBundleMetadata(t, incoming, "account-shared", "org-personal"),
			})
			held, release, lockDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
			lockErrors := make(chan error, 1)
			var releaseOnce sync.Once
			go func() {
				defer close(lockDone)
				lockErrors <- authfile.NewVault(vaultPath).WithMutationLock(func() error {
					close(held)
					<-release
					if err := os.WriteFile(filepath.Join(profile, ".credentials.json"), []byte(team), 0600); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(profile, "meta.json"), []byte(teamMetadata), 0600)
				})
			}()
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				select {
				case <-lockDone:
				case <-time.After(10 * time.Second):
					t.Error("managed mutation did not finish")
				}
			})
			select {
			case <-held:
			case err := <-lockErrors:
				t.Fatalf("failed to acquire managed lock: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("managed mutation never acquired the lock")
			}
			type outcome struct {
				imported *ImportResult
				exported *ExportResult
				err      error
			}
			started, transferDone := make(chan struct{}), make(chan struct{})
			completed := make(chan outcome, 1)
			go func() {
				defer close(transferDone)
				close(started)
				var result outcome
				if operation == "import" {
					result.imported, result.err = (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vaultPath, Mode: ImportModeSmart})
				} else {
					result.exported, result.err = (&VaultExporter{VaultPath: vaultPath}).Export(&ExportOptions{OutputDir: filepath.Join(root, "output")})
				}
				completed <- result
			}()
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				select {
				case <-transferDone:
				case <-time.After(10 * time.Second):
					t.Error("bundle transfer did not finish")
				}
			})
			<-started
			select {
			case result := <-completed:
				t.Fatalf("transfer bypassed the held mutation lock: %+v", result)
			case <-time.After(40 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			if err := <-lockErrors; err != nil {
				t.Fatal(err)
			}
			var result outcome
			select {
			case result = <-completed:
			case <-time.After(10 * time.Second):
				t.Fatal("bundle transfer did not resume after mutation completed")
			}
			if result.err != nil {
				t.Fatalf("transfer failed after waiting: %v", result.err)
			}
			requireRecoveryFile(t, filepath.Join(profile, ".credentials.json"), team)
			if operation == "import" {
				if result.imported.SkippedProfiles != 1 || !strings.Contains(result.imported.ProfileActions[0].Reason, "organization differs") {
					t.Fatalf("import used stale ownership from before lock wait: %+v", result.imported)
				}
			} else {
				extracted := t.TempDir()
				if err := (&VaultImporter{BundlePath: result.exported.OutputPath}).extractBundle(extracted); err != nil {
					t.Fatal(err)
				}
				requireRecoveryFile(t, filepath.Join(extracted, "vault", "claude", "work", ".credentials.json"), team)
				requireRecoveryFile(t, filepath.Join(extracted, "vault", "claude", "work", "meta.json"), teamMetadata)
			}
		})
	}
}

func TestVaultImporterRejectsManifestTraversalBeforeAnyChanges(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		for _, names := range [][2]string{{"codex", "../../victim"}, {"../..", "victim"}, {"codex", `..\..\victim`}, {`C:\vault`, "victim"}, {"codex", "NUL"}, {"codex", "victim."}} {
			t.Run(names[0]+"/"+names[1]+"/dry="+fmt.Sprint(dryRun), func(t *testing.T) {
				root := t.TempDir()
				vaultPath := filepath.Join(root, "vault")
				victim := filepath.Join(root, "victim", "auth.json")
				existing := filepath.Join(vaultPath, "codex", "existing", "auth.json")
				configPath := filepath.Join(root, "config.json")
				writeRecoveryFile(t, victim, "outside-original")
				writeRecoveryFile(t, existing, "account-original")
				writeRecoveryFile(t, configPath, "config-original")
				manifest := NewManifest()
				manifest.AddProfile(names[0], names[1])
				manifest.AddProfile("codex", "existing")
				manifest.SetConfig(true, "config.json")
				bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
					"victim/auth.json": "outside-replacement", "vault/codex/existing/auth.json": "account-replacement", "config.json": "config-replacement",
				})
				result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vaultPath, ConfigPath: configPath, Mode: ImportModeReplace, Force: true, DryRun: dryRun})
				if err == nil {
					t.Fatal("unsafe manifest accepted")
				}
				if result != nil && len(result.ProfileActions) != 0 {
					t.Fatalf("unsafe manifest was previewed: %+v", result.ProfileActions)
				}
				requireRecoveryFile(t, victim, "outside-original")
				requireRecoveryFile(t, existing, "account-original")
				requireRecoveryFile(t, configPath, "config-original")
			})
		}
	}
}

func TestVaultImporterCannotReplacePrivateRecovery(t *testing.T) {
	const private = "_caam-vault-backup~synthetic"
	for _, dryRun := range []bool{false, true} {
		for _, names := range [][2]string{{"codex", private}, {private, "work"}, {"codex", strings.ToUpper(private)}, {strings.ToUpper(private), "work"}} {
			t.Run(names[0]+"/"+names[1]+"/dry="+fmt.Sprint(dryRun), func(t *testing.T) {
				root := t.TempDir()
				vault := filepath.Join(root, "vault")
				recovery := filepath.Join(vault, names[0], names[1], "previous", "auth.json")
				current := filepath.Join(vault, "codex", "current", "auth.json")
				writeRecoveryFile(t, recovery, "synthetic-previous-credential")
				writeRecoveryFile(t, current, "synthetic-current-credential")
				manifest := NewManifest()
				manifest.AddProfile(names[0], names[1])
				manifest.AddProfile("codex", "current")
				bundlePath := writeRecoveryBundle(t, manifest, map[string]string{
					"vault/" + names[0] + "/" + names[1] + "/previous/auth.json": "synthetic-recovery-replacement",
					"vault/codex/current/auth.json":                              "synthetic-current-replacement",
				})
				result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vault, Mode: ImportModeReplace, Force: true, DryRun: dryRun})
				if err == nil {
					t.Fatal("bundle can overwrite private recovery storage")
				}
				if result != nil && len(result.ProfileActions) != 0 {
					t.Fatal("private recovery was exposed in an import preview")
				}
				requireRecoveryFile(t, recovery, "synthetic-previous-credential")
				requireRecoveryFile(t, current, "synthetic-current-credential")
			})
		}
	}
}

func TestVaultImporterForceCannotBypassCorruption(t *testing.T) {
	for _, dryRun := range []bool{false, true} {
		t.Run(fmt.Sprint(dryRun), func(t *testing.T) {
			root := t.TempDir()
			vaultPath := filepath.Join(root, "vault")
			existing := filepath.Join(vaultPath, "codex", "work", "auth.json")
			configPath := filepath.Join(root, "config.json")
			writeRecoveryFile(t, existing, "account-original")
			writeRecoveryFile(t, configPath, "config-original")
			manifest := NewManifest()
			manifest.AddProfile("codex", "work")
			manifest.SetConfig(true, "config.json")
			checksum, err := ComputeDataChecksum([]byte("untampered"), AlgorithmSHA256)
			if err != nil {
				t.Fatal(err)
			}
			manifest.AddChecksum("vault/codex/work/auth.json", checksum)
			bundlePath := writeRecoveryBundle(t, manifest, map[string]string{"vault/codex/work/auth.json": "tampered", "config.json": "replacement"})
			result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vaultPath, ConfigPath: configPath, Mode: ImportModeReplace, Force: true, DryRun: dryRun})
			if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
				t.Fatalf("want integrity refusal, got %v", err)
			}
			if result == nil || result.VerificationResult == nil || result.VerificationResult.Valid || len(result.ProfileActions) != 0 {
				t.Fatalf("corruption was not rejected before preview: %+v", result)
			}
			requireRecoveryFile(t, existing, "account-original")
			requireRecoveryFile(t, configPath, "config-original")
		})
	}
}

func TestVaultImporterRejectsMetadataAsOptionalPayload(t *testing.T) {
	for _, source := range []string{EncryptionMarkerFile, ManifestFileName, EncryptionMarkerFile + "/", ".CAAM_ENCRYPTED", "MaNiFeSt.JsOn", ".CaAm_EnCrYpTeD/"} {
		t.Run(source, func(t *testing.T) {
			configPath := filepath.Join(t.TempDir(), "config.json")
			writeRecoveryFile(t, configPath, "original-config")
			manifest := NewManifest()
			manifest.SetConfig(true, source)
			bundlePath := writeRecoveryBundle(t, manifest, map[string]string{EncryptionMarkerFile: "unchecked-config"})
			if _, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{ConfigPath: configPath}); err == nil || !strings.Contains(err.Error(), "reserved bundle metadata") {
				t.Fatalf("want reserved metadata refusal, got %v", err)
			}
			requireRecoveryFile(t, configPath, "original-config")
		})
	}
}

func TestVaultImporterRejectsDestinationSymlinks(t *testing.T) {
	for _, target := range []string{"vault", "provider", "profile", "credential", "config", "config-parent", "sync-child"} {
		for _, dryRun := range []bool{false, true} {
			t.Run(target+"/dry="+fmt.Sprint(dryRun), func(t *testing.T) {
				root, outside := t.TempDir(), t.TempDir()
				vaultPath := filepath.Join(root, "vault")
				configPath := filepath.Join(root, "config", "config.json")
				syncPath := filepath.Join(root, "sync")
				link, to := "", outside
				switch target {
				case "vault":
					link = vaultPath
				case "provider":
					link = filepath.Join(vaultPath, "codex")
				case "profile":
					link = filepath.Join(vaultPath, "codex", "work")
				case "credential":
					link, to = filepath.Join(vaultPath, "codex", "work", "auth.json"), filepath.Join(outside, "auth.json")
				case "config":
					link, to = configPath, filepath.Join(outside, "config.json")
				case "config-parent":
					link = filepath.Dir(configPath)
				case "sync-child":
					link = filepath.Join(syncPath, "nested")
				}
				if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
					t.Fatal(err)
				}
				for _, file := range []string{"auth.json", "config.json", "codex/work/auth.json", "work/auth.json", "pools.json"} {
					writeRecoveryFile(t, filepath.Join(outside, file), "outside-original")
				}
				if err := os.Symlink(to, link); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
				manifest := NewManifest()
				manifest.AddProfile("codex", "work")
				manifest.SetConfig(true, "config.json")
				manifest.SetSyncConfig(true, "sync/")
				bundlePath := writeRecoveryBundle(t, manifest, map[string]string{"vault/codex/work/auth.json": "replacement", "config.json": "replacement", "sync/nested/pools.json": "replacement"})
				result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vaultPath, ConfigPath: configPath, SyncPath: syncPath, Mode: ImportModeReplace, Force: true, DryRun: dryRun})
				if err == nil {
					t.Fatal("destination symlink accepted")
				}
				if result != nil && len(result.ProfileActions) != 0 {
					t.Fatalf("unsafe destination was previewed: %+v", result.ProfileActions)
				}
				for _, file := range []string{"auth.json", "config.json", "codex/work/auth.json", "work/auth.json", "pools.json"} {
					requireRecoveryFile(t, filepath.Join(outside, file), "outside-original")
				}
				if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("rejected import replaced link: %v", err)
				}
			})
		}
	}
}

func TestVaultImporterReplacePreservesBackupNamedProfile(t *testing.T) {
	root := t.TempDir()
	vaultPath := filepath.Join(root, "vault")
	profilePath := filepath.Join(vaultPath, "codex", "work", "auth.json")
	backupNamedPath := filepath.Join(vaultPath, "codex", "work.bak", "auth.json")
	writeRecoveryFile(t, profilePath, "old-generation")
	writeRecoveryFile(t, backupNamedPath, "separate-account")
	manifest := NewManifest()
	manifest.AddProfile("codex", "work")
	bundlePath := writeRecoveryBundle(t, manifest, map[string]string{"vault/codex/work/auth.json": "new-generation"})
	result, err := (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: vaultPath, Mode: ImportModeReplace})
	if err != nil || result.UpdatedProfiles != 1 || len(result.Errors) != 0 {
		t.Fatalf("replace failed: %+v, %v", result, err)
	}
	requireRecoveryFile(t, profilePath, "new-generation")
	requireRecoveryFile(t, backupNamedPath, "separate-account")
}

func TestVaultImporterAllowsTrustedHomeAlias(t *testing.T) {
	realHome, aliasRoot := t.TempDir(), t.TempDir()
	homeAlias := filepath.Join(aliasRoot, "home")
	if err := os.Symlink(realHome, homeAlias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	manifest := NewManifest()
	manifest.AddProfile("codex", "work")
	manifest.SetConfig(true, "config.json")
	bundlePath := writeRecoveryBundle(t, manifest, map[string]string{"vault/codex/work/auth.json": "synthetic-credential", "config.json": "synthetic-config"})
	opts := &ImportOptions{VaultPath: filepath.Join(homeAlias, "data", "vault"), ConfigPath: filepath.Join(homeAlias, "config", "caam", "config.json"), Mode: ImportModeReplace}
	originalVault := opts.VaultPath
	result, err := (&VaultImporter{BundlePath: bundlePath}).Import(opts)
	if err != nil || result.NewProfiles != 1 || len(result.Errors) != 0 {
		t.Fatalf("trusted HOME alias rejected: %+v, %v", result, err)
	}
	if opts.VaultPath != originalVault {
		t.Fatal("import mutated caller options")
	}
	requireRecoveryFile(t, filepath.Join(realHome, "data", "vault", "codex", "work", "auth.json"), "synthetic-credential")
	requireRecoveryFile(t, filepath.Join(realHome, "config", "caam", "config.json"), "synthetic-config")
}

func TestVaultImporterRejectsSpecialAndDuplicateArchiveEntries(t *testing.T) {
	for _, mode := range []string{"symlink", "duplicate", "windows-traversal"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			bundlePath := filepath.Join(root, "invalid.zip")
			f, err := os.Create(bundlePath)
			if err != nil {
				t.Fatal(err)
			}
			w := zip.NewWriter(f)
			header := &zip.FileHeader{Name: "vault/codex/work/auth.json", Method: zip.Store}
			if mode == "symlink" {
				header.SetMode(os.ModeSymlink | 0600)
			} else if mode == "windows-traversal" {
				header.Name = `..\outside\auth.json`
			}
			entry, err := w.CreateHeader(header)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := entry.Write([]byte("synthetic")); err != nil {
				t.Fatal(err)
			}
			if mode == "duplicate" {
				if _, err := w.Create(header.Name); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			_, err = (&VaultImporter{BundlePath: bundlePath}).Import(&ImportOptions{VaultPath: filepath.Join(root, "vault"), Force: true})
			if err == nil || !strings.Contains(err.Error(), "extract bundle") {
				t.Fatalf("invalid archive was not refused during extraction: %v", err)
			}
		})
	}
}

func TestDefaultImportOptions(t *testing.T) {
	opts := DefaultImportOptions()
	if opts == nil {
		t.Fatal("DefaultImportOptions returned nil")
	}

	if opts.Mode != ImportModeSmart {
		t.Errorf("expected default mode 'smart', got %q", opts.Mode)
	}
}

func TestVaultImporter_Import_BundleNotFound(t *testing.T) {
	importer := &VaultImporter{
		BundlePath: "/nonexistent/bundle.zip",
	}

	_, err := importer.Import(nil)
	if err == nil {
		t.Error("expected error for nonexistent bundle")
	}
}

func TestVaultImporter_Import_EncryptedWithoutPassword(t *testing.T) {
	// Create a fake encrypted bundle (just need the filename pattern)
	tempDir := t.TempDir()
	bundlePath := filepath.Join(tempDir, "test.enc.zip")

	// Create empty file (won't be read, just needs to exist)
	if err := os.WriteFile(bundlePath, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}

	importer := &VaultImporter{
		BundlePath: bundlePath,
	}

	opts := DefaultImportOptions()
	opts.Password = "" // No password

	_, err := importer.Import(opts)
	if err == nil {
		t.Error("expected error for encrypted bundle without password")
	}
}

func TestVaultImporter_Import_DryRun(t *testing.T) {
	// Create a test bundle with export first
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create vault structure
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create auth file with expiry
	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "test_access_token",
			"refresh_token": "test_refresh_token",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Export
	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tempDir,
	}

	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir

	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Now test import with dry-run
	if err := os.MkdirAll(importVaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	importer := &VaultImporter{
		BundlePath: exportResult.OutputPath,
	}

	importOpts := DefaultImportOptions()
	importOpts.DryRun = true
	importOpts.VaultPath = importVaultDir

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import dry-run failed: %v", err)
	}

	// Verify dry-run results
	if result.Manifest == nil {
		t.Error("expected manifest in result")
	}

	if result.VerificationResult == nil {
		t.Error("expected verification result")
	}

	if !result.VerificationResult.Valid {
		t.Errorf("expected valid checksums: %s", result.VerificationResult.Summary())
	}

	// Verify profile would be added (not actually added in dry-run)
	if result.NewProfiles != 1 {
		t.Errorf("expected 1 new profile, got %d", result.NewProfiles)
	}

	// Verify vault wasn't actually modified
	importedProfile := filepath.Join(importVaultDir, "claude", "test@example.com")
	if _, err := os.Stat(importedProfile); !os.IsNotExist(err) {
		t.Error("dry-run should not create files")
	}
}

func TestVaultImporter_Import_ActualImport(t *testing.T) {
	// Create a test bundle with export first
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create vault structure
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	// Create auth file with expiry
	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "test_access_token",
			"refresh_token": "test_refresh_token",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Export
	exporter := &VaultExporter{
		VaultPath: vaultDir,
		DataPath:  tempDir,
	}

	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir

	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Now test actual import
	if err := os.MkdirAll(importVaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	importer := &VaultImporter{
		BundlePath: exportResult.OutputPath,
	}

	importOpts := DefaultImportOptions()
	importOpts.VaultPath = importVaultDir

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify results
	if result.NewProfiles != 1 {
		t.Errorf("expected 1 new profile, got %d", result.NewProfiles)
	}

	// Verify file was actually created
	importedProfile := filepath.Join(importVaultDir, "claude", "test@example.com", ".claude.json")
	if _, err := os.Stat(importedProfile); os.IsNotExist(err) {
		t.Error("import should have created profile file")
	}

	// Verify content
	importedData, err := os.ReadFile(importedProfile)
	if err != nil {
		t.Fatalf("read imported file: %v", err)
	}

	var imported map[string]interface{}
	if err := json.Unmarshal(importedData, &imported); err != nil {
		t.Fatalf("parse imported file: %v", err)
	}

	oauthToken, ok := imported["oauthToken"].(map[string]interface{})
	if !ok {
		t.Fatal("expected oauthToken in imported data")
	}

	if oauthToken["access_token"] != "test_access_token" {
		t.Error("imported token doesn't match")
	}
}

func TestVaultImporter_Import_MergeMode(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create source vault with profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "bundle_token",
			"refresh_token": "bundle_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Export
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create destination vault with existing profile
	existingProfileDir := filepath.Join(importVaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(existingProfileDir, 0700); err != nil {
		t.Fatal(err)
	}

	existingData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "local_token",
			"refresh_token": "local_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(12 * time.Hour).Format(time.RFC3339), // Older token
		},
	}
	existingJSON, _ := json.Marshal(existingData)
	if err := os.WriteFile(filepath.Join(existingProfileDir, ".claude.json"), existingJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Import in merge mode (should skip existing)
	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.Mode = ImportModeMerge
	importOpts.VaultPath = importVaultDir

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify profile was skipped
	if result.SkippedProfiles != 1 {
		t.Errorf("expected 1 skipped profile, got %d", result.SkippedProfiles)
	}

	// Verify local token was preserved
	importedData, _ := os.ReadFile(filepath.Join(existingProfileDir, ".claude.json"))
	var imported map[string]interface{}
	json.Unmarshal(importedData, &imported)

	oauthToken := imported["oauthToken"].(map[string]interface{})
	if oauthToken["access_token"] != "local_token" {
		t.Error("merge mode should have preserved local token")
	}
}

func TestVaultImporter_Import_ReplaceMode(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create source vault with profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "bundle_token",
			"refresh_token": "bundle_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Export
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create destination vault with existing profile
	existingProfileDir := filepath.Join(importVaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(existingProfileDir, 0700); err != nil {
		t.Fatal(err)
	}

	existingData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "local_token",
			"refresh_token": "local_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(48 * time.Hour).Format(time.RFC3339), // Fresher token
		},
	}
	existingJSON, _ := json.Marshal(existingData)
	if err := os.WriteFile(filepath.Join(existingProfileDir, ".claude.json"), existingJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Import in replace mode (should overwrite even fresher local)
	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.Mode = ImportModeReplace
	importOpts.VaultPath = importVaultDir

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify profile was updated
	if result.UpdatedProfiles != 1 {
		t.Errorf("expected 1 updated profile, got %d", result.UpdatedProfiles)
	}

	// Verify bundle token replaced local
	importedData, _ := os.ReadFile(filepath.Join(existingProfileDir, ".claude.json"))
	var imported map[string]interface{}
	json.Unmarshal(importedData, &imported)

	oauthToken := imported["oauthToken"].(map[string]interface{})
	if oauthToken["access_token"] != "bundle_token" {
		t.Error("replace mode should have overwritten with bundle token")
	}
}

func TestVaultImporter_Import_SmartMode_FresherBundle(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create source vault with FRESHER profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "bundle_token",
			"refresh_token": "bundle_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(48 * time.Hour).Format(time.RFC3339), // FRESHER
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}
	proveLegacyBundleProfile(t, profileDir)

	// Export
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create destination vault with OLDER profile
	existingProfileDir := filepath.Join(importVaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(existingProfileDir, 0700); err != nil {
		t.Fatal(err)
	}

	existingData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "local_token",
			"refresh_token": "local_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(12 * time.Hour).Format(time.RFC3339), // OLDER
		},
	}
	existingJSON, _ := json.Marshal(existingData)
	if err := os.WriteFile(filepath.Join(existingProfileDir, ".claude.json"), existingJSON, 0600); err != nil {
		t.Fatal(err)
	}
	proveLegacyBundleProfile(t, existingProfileDir)

	// Import in smart mode (should update because bundle is fresher)
	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.Mode = ImportModeSmart
	importOpts.VaultPath = importVaultDir

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify profile was updated
	if result.UpdatedProfiles != 1 {
		t.Errorf("expected 1 updated profile, got %d", result.UpdatedProfiles)
	}

	// Verify fresher bundle token replaced local
	importedData, _ := os.ReadFile(filepath.Join(existingProfileDir, ".claude.json"))
	var imported map[string]interface{}
	json.Unmarshal(importedData, &imported)

	oauthToken := imported["oauthToken"].(map[string]interface{})
	if oauthToken["access_token"] != "bundle_token" {
		t.Error("smart mode should have imported fresher bundle token")
	}
}

func TestVaultImporter_Import_SmartMode_FresherLocal(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create source vault with OLDER profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "bundle_token",
			"refresh_token": "bundle_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(12 * time.Hour).Format(time.RFC3339), // OLDER
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}
	proveLegacyBundleProfile(t, profileDir)

	// Export
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Create destination vault with FRESHER profile
	existingProfileDir := filepath.Join(importVaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(existingProfileDir, 0700); err != nil {
		t.Fatal(err)
	}

	existingData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token":  "local_token",
			"refresh_token": "local_refresh",
			"token_type":    "Bearer",
			"expiry":        time.Now().Add(48 * time.Hour).Format(time.RFC3339), // FRESHER
		},
	}
	existingJSON, _ := json.Marshal(existingData)
	if err := os.WriteFile(filepath.Join(existingProfileDir, ".claude.json"), existingJSON, 0600); err != nil {
		t.Fatal(err)
	}
	proveLegacyBundleProfile(t, existingProfileDir)

	// Import in smart mode (should skip because local is fresher)
	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.Mode = ImportModeSmart
	importOpts.VaultPath = importVaultDir

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify profile was skipped
	if result.SkippedProfiles != 1 {
		t.Errorf("expected 1 skipped profile, got %d", result.SkippedProfiles)
	}

	// Verify local token was preserved
	importedData, _ := os.ReadFile(filepath.Join(existingProfileDir, ".claude.json"))
	var imported map[string]interface{}
	json.Unmarshal(importedData, &imported)

	oauthToken := imported["oauthToken"].(map[string]interface{})
	if oauthToken["access_token"] != "local_token" {
		t.Error("smart mode should have preserved fresher local token")
	}
}

func TestVaultImporter_Import_ProviderFilter(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create source vault with multiple providers
	for _, provider := range []string{"claude", "codex", "gemini"} {
		profileDir := filepath.Join(vaultDir, provider, "test@example.com")
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}

		var authData map[string]interface{}
		var fileName string

		switch provider {
		case "claude":
			authData = map[string]interface{}{
				"oauthToken": map[string]interface{}{
					"access_token": "claude_token",
					"expiry":       time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				},
			}
			fileName = ".claude.json"
		case "codex":
			authData = map[string]interface{}{
				"access_token": "codex_token",
				"expires_at":   time.Now().Add(24 * time.Hour).Unix(),
			}
			fileName = "auth.json"
		case "gemini":
			authData = map[string]interface{}{
				"oauth_credentials": map[string]interface{}{
					"access_token": "gemini_token",
					"expiry":       time.Now().Add(24 * time.Hour).Format(time.RFC3339),
				},
			}
			fileName = "settings.json"
		}

		authJSON, _ := json.Marshal(authData)
		if err := os.WriteFile(filepath.Join(profileDir, fileName), authJSON, 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Export all
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("export failed: %v", err)
	}

	// Import only claude
	if err := os.MkdirAll(importVaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.VaultPath = importVaultDir
	importOpts.ProviderFilter = []string{"claude"}

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import failed: %v", err)
	}

	// Verify only claude was imported
	if result.NewProfiles != 1 {
		t.Errorf("expected 1 new profile (claude only), got %d", result.NewProfiles)
	}

	// Verify claude exists
	if _, err := os.Stat(filepath.Join(importVaultDir, "claude", "test@example.com")); os.IsNotExist(err) {
		t.Error("claude profile should have been imported")
	}

	// Verify codex does NOT exist
	if _, err := os.Stat(filepath.Join(importVaultDir, "codex", "test@example.com")); !os.IsNotExist(err) {
		t.Error("codex profile should NOT have been imported")
	}

	// Verify gemini does NOT exist
	if _, err := os.Stat(filepath.Join(importVaultDir, "gemini", "test@example.com")); !os.IsNotExist(err) {
		t.Error("gemini profile should NOT have been imported")
	}
}

func TestVaultImporter_Import_WithEncryption(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create vault with profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token": "secret_token",
			"expiry":       time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Export with encryption
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportOpts.Encrypt = true
	exportOpts.Password = "testpassword123"

	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("encrypted export failed: %v", err)
	}

	// Import with correct password
	if err := os.MkdirAll(importVaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.VaultPath = importVaultDir
	importOpts.Password = "testpassword123"

	result, err := importer.Import(importOpts)
	if err != nil {
		t.Fatalf("import with password failed: %v", err)
	}

	if result.NewProfiles != 1 {
		t.Errorf("expected 1 new profile, got %d", result.NewProfiles)
	}

	// Verify content
	importedData, err := os.ReadFile(filepath.Join(importVaultDir, "claude", "test@example.com", ".claude.json"))
	if err != nil {
		t.Fatal(err)
	}

	var imported map[string]interface{}
	json.Unmarshal(importedData, &imported)
	oauthToken := imported["oauthToken"].(map[string]interface{})
	if oauthToken["access_token"] != "secret_token" {
		t.Error("decrypted token doesn't match")
	}
}

func TestContainsIgnoreCase(t *testing.T) {
	tests := []struct {
		slice    []string
		s        string
		expected bool
	}{
		{[]string{"claude", "codex"}, "claude", true},
		{[]string{"claude", "codex"}, "Claude", true},
		{[]string{"claude", "codex"}, "CODEX", true},
		{[]string{"claude", "codex"}, "gemini", false},
		{[]string{}, "claude", false},
	}

	for _, tt := range tests {
		result := containsIgnoreCase(tt.slice, tt.s)
		if result != tt.expected {
			t.Errorf("containsIgnoreCase(%v, %q) = %v, want %v", tt.slice, tt.s, result, tt.expected)
		}
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	tests := []struct {
		s        string
		patterns []string
		expected bool
	}{
		{"alice@example.com", []string{"alice"}, true},
		{"alice@example.com", []string{"bob"}, false},
		{"work@company.com", []string{"work", "personal"}, true},
		{"random@test.com", []string{}, false},
		{"Alice@Example.com", []string{"alice"}, true}, // case insensitive
	}

	for _, tt := range tests {
		result := matchesAnyPattern(tt.s, tt.patterns)
		if result != tt.expected {
			t.Errorf("matchesAnyPattern(%q, %v) = %v, want %v", tt.s, tt.patterns, result, tt.expected)
		}
	}
}

// Tests for mergeJSONFile
func TestMergeJSONFile(t *testing.T) {
	t.Run("merge new keys into existing", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcPath := filepath.Join(tmpDir, "src.json")
		dstPath := filepath.Join(tmpDir, "dst.json")

		// Create source with new key
		srcData := `{"newkey": "newvalue", "shared": "source_value"}`
		if err := os.WriteFile(srcPath, []byte(srcData), 0600); err != nil {
			t.Fatal(err)
		}

		// Create destination with existing key
		dstData := `{"existing": "existingvalue", "shared": "dest_value"}`
		if err := os.WriteFile(dstPath, []byte(dstData), 0600); err != nil {
			t.Fatal(err)
		}

		if err := mergeJSONFile(srcPath, dstPath); err != nil {
			t.Fatalf("mergeJSONFile error = %v", err)
		}

		// Read result
		result, err := os.ReadFile(dstPath)
		if err != nil {
			t.Fatal(err)
		}

		var merged map[string]interface{}
		if err := json.Unmarshal(result, &merged); err != nil {
			t.Fatal(err)
		}

		// Should have all keys
		if merged["existing"] != "existingvalue" {
			t.Errorf("existing = %v, want existingvalue", merged["existing"])
		}
		if merged["newkey"] != "newvalue" {
			t.Errorf("newkey = %v, want newvalue", merged["newkey"])
		}
		// Source should overwrite destination
		if merged["shared"] != "source_value" {
			t.Errorf("shared = %v, want source_value", merged["shared"])
		}
	})

	t.Run("destination does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcPath := filepath.Join(tmpDir, "src.json")
		dstPath := filepath.Join(tmpDir, "subdir", "dst.json")

		srcData := `{"key": "value"}`
		if err := os.WriteFile(srcPath, []byte(srcData), 0600); err != nil {
			t.Fatal(err)
		}

		if err := mergeJSONFile(srcPath, dstPath); err != nil {
			t.Fatalf("mergeJSONFile error = %v", err)
		}

		// Should have created the file
		result, err := os.ReadFile(dstPath)
		if err != nil {
			t.Fatalf("File should be created: %v", err)
		}

		var merged map[string]interface{}
		json.Unmarshal(result, &merged)
		if merged["key"] != "value" {
			t.Errorf("key = %v, want value", merged["key"])
		}
	})

	t.Run("corrupt destination is replaced", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcPath := filepath.Join(tmpDir, "src.json")
		dstPath := filepath.Join(tmpDir, "dst.json")

		srcData := `{"key": "value"}`
		if err := os.WriteFile(srcPath, []byte(srcData), 0600); err != nil {
			t.Fatal(err)
		}

		// Corrupt destination
		if err := os.WriteFile(dstPath, []byte("not valid json"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := mergeJSONFile(srcPath, dstPath); err != nil {
			t.Fatalf("mergeJSONFile error = %v", err)
		}

		// Should have source values
		result, _ := os.ReadFile(dstPath)
		var merged map[string]interface{}
		json.Unmarshal(result, &merged)
		if merged["key"] != "value" {
			t.Errorf("key = %v, want value", merged["key"])
		}
	})

	t.Run("source does not exist", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcPath := filepath.Join(tmpDir, "nonexistent.json")
		dstPath := filepath.Join(tmpDir, "dst.json")

		err := mergeJSONFile(srcPath, dstPath)
		if err == nil {
			t.Error("Expected error for nonexistent source")
		}
	})

	t.Run("source is not valid json", func(t *testing.T) {
		tmpDir := t.TempDir()
		srcPath := filepath.Join(tmpDir, "src.json")
		dstPath := filepath.Join(tmpDir, "dst.json")

		if err := os.WriteFile(srcPath, []byte("not valid json"), 0600); err != nil {
			t.Fatal(err)
		}

		err := mergeJSONFile(srcPath, dstPath)
		if err == nil {
			t.Error("Expected error for invalid source JSON")
		}
	})
}

// Tests for copyDirectory
func TestCopyDirectory(t *testing.T) {
	t.Run("copies files recursively", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := filepath.Join(t.TempDir(), "dest")

		// Create source structure
		subDir := filepath.Join(srcDir, "subdir")
		if err := os.MkdirAll(subDir, 0700); err != nil {
			t.Fatal(err)
		}

		if err := os.WriteFile(filepath.Join(srcDir, "file1.txt"), []byte("content1"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(subDir, "file2.txt"), []byte("content2"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := copyDirectory(srcDir, dstDir); err != nil {
			t.Fatalf("copyDirectory error = %v", err)
		}

		// Verify files
		content1, err := os.ReadFile(filepath.Join(dstDir, "file1.txt"))
		if err != nil || string(content1) != "content1" {
			t.Errorf("file1.txt content = %q, want content1", content1)
		}

		content2, err := os.ReadFile(filepath.Join(dstDir, "subdir", "file2.txt"))
		if err != nil || string(content2) != "content2" {
			t.Errorf("subdir/file2.txt content = %q, want content2", content2)
		}
	})

	t.Run("creates destination if not exists", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := filepath.Join(t.TempDir(), "nested", "deep", "dest")

		if err := os.WriteFile(filepath.Join(srcDir, "file.txt"), []byte("data"), 0600); err != nil {
			t.Fatal(err)
		}

		if err := copyDirectory(srcDir, dstDir); err != nil {
			t.Fatalf("copyDirectory error = %v", err)
		}

		if _, err := os.Stat(filepath.Join(dstDir, "file.txt")); os.IsNotExist(err) {
			t.Error("file.txt should exist in destination")
		}
	})

	t.Run("source does not exist", func(t *testing.T) {
		srcDir := "/nonexistent/source"
		dstDir := t.TempDir()

		err := copyDirectory(srcDir, dstDir)
		if err == nil {
			t.Error("Expected error for nonexistent source")
		}
	})

	t.Run("empty source directory", func(t *testing.T) {
		srcDir := t.TempDir()
		dstDir := filepath.Join(t.TempDir(), "dest")

		if err := copyDirectory(srcDir, dstDir); err != nil {
			t.Fatalf("copyDirectory error = %v", err)
		}

		// Destination should exist but be empty
		entries, _ := os.ReadDir(dstDir)
		if len(entries) != 0 {
			t.Errorf("Destination should be empty, got %d entries", len(entries))
		}
	})
}

// Tests for import with wrong password
func TestVaultImporter_Import_WrongPassword(t *testing.T) {
	tempDir := t.TempDir()
	vaultDir := filepath.Join(tempDir, "vault")
	outputDir := filepath.Join(tempDir, "output")
	importVaultDir := filepath.Join(tempDir, "import_vault")

	// Create vault with profile
	profileDir := filepath.Join(vaultDir, "claude", "test@example.com")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatal(err)
	}

	authData := map[string]interface{}{
		"oauthToken": map[string]interface{}{
			"access_token": "secret_token",
			"expiry":       time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		},
	}
	authJSON, _ := json.Marshal(authData)
	if err := os.WriteFile(filepath.Join(profileDir, ".claude.json"), authJSON, 0600); err != nil {
		t.Fatal(err)
	}

	// Export with encryption
	exporter := &VaultExporter{VaultPath: vaultDir, DataPath: tempDir}
	exportOpts := DefaultExportOptions()
	exportOpts.OutputDir = outputDir
	exportOpts.Encrypt = true
	exportOpts.Password = "correct_password"

	exportResult, err := exporter.Export(exportOpts)
	if err != nil {
		t.Fatalf("encrypted export failed: %v", err)
	}

	// Try import with wrong password
	if err := os.MkdirAll(importVaultDir, 0700); err != nil {
		t.Fatal(err)
	}

	importer := &VaultImporter{BundlePath: exportResult.OutputPath}
	importOpts := DefaultImportOptions()
	importOpts.VaultPath = importVaultDir
	importOpts.Password = "wrong_password"

	_, err = importer.Import(importOpts)
	if err == nil {
		t.Error("Import should fail with wrong password")
	}
}
