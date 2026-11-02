package authfile

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func isolatedClaudeFixture(t *testing.T) (string, *Vault, map[string]string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "profiles", "claude", "new-account")
	metadata, err := json.Marshal(map[string]string{"name": "new-account", "provider": "claude", "base_path": root, "auth_mode": "oauth"})
	if err != nil {
		t.Fatal(err)
	}
	putIsolatedTestFile(t, filepath.Join(root, "profile.json"), string(metadata))
	files := map[string]string{
		".credentials.json": `{"claudeAiOauth":{"accessToken":"isolated-access","refreshToken":"isolated-refresh","expiresAt":1600000000000,"scopes":["user:inference"]}}`,
		".claude.json":      `{"oauthAccount":{"accountUuid":"new-id","emailAddress":"new@example.test"},"hasCompletedOnboarding":true,"numStartups":7}`,
		"settings.json":     `{"model":"opus","permissions":{"allow":["Read"]}}`,
		"auth.json":         `{"accessToken":"isolated-secondary","refreshToken":"isolated-secondary-refresh"}`,
	}
	for name, data := range files {
		putIsolatedTestFile(t, filepath.Join(root, "xdg_config", "claude-code", name), data)
	}
	return root, NewVault(filepath.Join(t.TempDir(), "vault")), files
}

func assertIsolatedUnchanged(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(root, "xdg_config", "claude-code", name))
		if err != nil || string(got) != want {
			t.Fatalf("source %s changed: %v", name, err)
		}
	}
}

func assertIsolatedSnapshot(t *testing.T, vault *Vault, profile string, files map[string]string, account *identity.Identity) {
	t.Helper()
	for name, source := range files {
		path := vault.BackupPath("claude", profile, name)
		saved, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("missing snapshot %s: %v", name, err)
		}
		if name == ".claude.json" {
			var want, got map[string]interface{}
			if err := json.Unmarshal([]byte(source), &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(saved, &got); err != nil {
				t.Fatal(err)
			}
			delete(want, "oauthAccount")
			if account != nil {
				want["oauthAccount"] = map[string]interface{}{
					"accountUuid": account.AccountID, "organizationUuid": account.OrganizationID,
					"emailAddress": account.Email, "organizationName": account.Organization,
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatal("snapshot must use verified account labels and preserve all other Claude settings")
			}
		} else if string(saved) != source {
			t.Fatalf("incomplete snapshot %s", name)
		}
		info, err := os.Stat(path)
		if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
			t.Fatalf("insecure snapshot mode for %s: %v", name, err)
		}
	}
}

func isolatedClaudeTeamBinding(t *testing.T, credentials string) *identity.ClaudeIdentityBinding {
	t.Helper()
	fingerprint, err := identity.ClaudeCredentialFingerprint([]byte(credentials))
	if err != nil {
		t.Fatal(err)
	}
	return &identity.ClaudeIdentityBinding{
		Identity: identity.Identity{
			Provider: "claude", AccountID: "new-id", OrganizationID: "org-team",
			Email: "new@example.test", Organization: "Team",
		},
		CredentialFingerprint: fingerprint,
		VerifiedAt:            time.Now().UTC().Add(-24 * time.Hour),
	}
}

func assertIsolatedSavedBinding(t *testing.T, vault *Vault, profile string, expected *identity.ClaudeIdentityBinding) {
	t.Helper()
	// Reopen the vault so the assertions cannot depend on in-memory proof.
	reopened := NewVault(vault.BasePath())
	path := reopened.BackupPath("claude", profile, ".credentials.json")
	got, err := identity.ReadClaudeIdentityBinding(path)
	if err != nil || got == nil {
		t.Fatalf("saved proof missing after restart: %v", err)
	}
	if !identity.ClaudeSameAccount(&got.Identity, &expected.Identity) ||
		got.CredentialFingerprint != expected.CredentialFingerprint || !got.VerifiedAt.Equal(expected.VerifiedAt) {
		t.Fatal("saved proof does not belong to the captured account, organization, and credential generation")
	}
	account, err := identity.ExtractFromClaudeCredentials(path)
	if err != nil || account == nil || !account.Verified || account.OrganizationID != expected.Identity.OrganizationID {
		t.Fatalf("reopened snapshot lost verified organization identity: %v", err)
	}
}

func TestBackupIsolatedClaudePreservesLiveLoginAndActivates(t *testing.T) {
	root, vault, files := isolatedClaudeFixture(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CAAM_KEYCHAIN", "0")
	liveCredential := filepath.Join(home, ".claude", ".credentials.json")
	liveSettings := filepath.Join(home, ".claude.json")
	livePolicy := filepath.Join(home, ".claude", "settings.json")
	putIsolatedTestFile(t, liveCredential, `{"claudeAiOauth":{"accessToken":"live-access","refreshToken":"live-refresh","expiresAt":1600000000000,"accountId":"live-id","email":"live@example.test"}}`)
	putIsolatedTestFile(t, liveSettings, `{"oauthAccount":{"accountUuid":"live-id","emailAddress":"live@example.test"},"numStartups":99}`)
	putIsolatedTestFile(t, livePolicy, `{"model":"sonnet"}`)

	// A reader continuously using the live login must not observe even a
	// temporary clear/replacement. After backup, verify inode and exact bytes.
	before, err := readIsolatedSourceFile(liveCredential, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	settingsBefore, _ := os.ReadFile(liveSettings)
	policyBefore, _ := os.ReadFile(livePolicy)
	stop, readerDone := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			data, err := os.ReadFile(liveCredential)
			if err != nil || !bytes.Equal(data, before.data) {
				readerDone <- fmt.Errorf("live agent lost its credential: %v", err)
				return
			}
			select {
			case <-stop:
				readerDone <- nil
				return
			default:
			}
		}
	}()
	err = vault.BackupIsolatedClaude(root, "saved")
	close(stop)
	if readerErr := <-readerDone; readerErr != nil {
		t.Fatal(readerErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := before.unchanged(); err != nil {
		t.Fatalf("live login was touched: %v", err)
	}
	for path, want := range map[string][]byte{liveSettings: settingsBefore, livePolicy: policyBefore} {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("live settings changed: %s: %v", path, err)
		}
	}
	assertIsolatedUnchanged(t, root, files)
	assertIsolatedSnapshot(t, vault, "saved", files, nil)
	names, err := vault.List("claude")
	if err != nil || !reflect.DeepEqual(names, []string{"saved"}) {
		t.Fatalf("vault listing: %v, %v", names, err)
	}
	data, err := os.ReadFile(vault.BackupPath("claude", "saved", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var metadata struct {
		Identity      string   `json:"identity"`
		IdentityKeys  []string `json:"identity_keys"`
		OriginalPaths []string `json:"original_paths"`
		SourceType    string   `json:"source_type"`
		SourceProfile string   `json:"source_profile"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata.Identity != "" || len(metadata.IdentityKeys) != 0 ||
		metadata.SourceType != "isolated-profile" || metadata.SourceProfile != "new-account" || len(metadata.OriginalPaths) != len(files) {
		t.Fatalf("wrong source metadata or unverified account labels: %+v", metadata)
	}
	for _, path := range metadata.OriginalPaths {
		if filepath.Dir(path) != filepath.Join(root, "xdg_config", "claude-code") {
			t.Fatalf("temporary or live source path leaked into metadata: %s", path)
		}
	}

	// Activation uses the ordinary native destination fileset, not recorded
	// original_paths. It must select the captured credential, omit unverified
	// labels, and preserve the source.
	if _, err := vault.Switch(ClaudeAuthFiles(), "saved", SwitchOptions{BackupMode: "always"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(liveCredential)
	if err != nil || string(got) != files[".credentials.json"] {
		t.Fatalf("saved login was not activated: %v", err)
	}
	got, err = os.ReadFile(liveSettings)
	if err != nil || bytes.Contains(got, []byte("oauthAccount")) || bytes.Contains(got, []byte("new@example.test")) || bytes.Contains(got, []byte("live@example.test")) {
		t.Fatalf("activation published an unverified identity: %v", err)
	}
	assertIsolatedUnchanged(t, root, files)
}

func TestBackupIsolatedClaudeOmitsConflictingUnverifiedLabels(t *testing.T) {
	root, vault, files := isolatedClaudeFixture(t)
	files[".claude.json"] = `{"oauthAccount":{"accountUuid":"other-id","organizationUuid":"other-org","emailAddress":"other@example.test"},"hasCompletedOnboarding":true,"numStartups":7}`
	putIsolatedTestFile(t, filepath.Join(root, "xdg_config", "claude-code", ".claude.json"), files[".claude.json"])
	if err := vault.BackupIsolatedClaude(root, "saved"); err != nil {
		t.Fatalf("an unverifiable label must not prevent preserving a complete credential: %v", err)
	}
	assertIsolatedSnapshot(t, vault, "saved", files, nil)
	assertIsolatedUnchanged(t, root, files)
	metadata, err := os.ReadFile(vault.BackupPath("claude", "saved", "meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(metadata, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"identity", "identity_keys", "claude_identity"} {
		if _, exists := decoded[name]; exists {
			t.Fatalf("unverified source label persisted as trusted %s", name)
		}
	}
}

func TestBackupIsolatedClaudeCachedExpiredIdentityStaysOffline(t *testing.T) {
	root, vault, files := isolatedClaudeFixture(t)
	configDir := filepath.Join(root, "xdg_config", "claude-code")
	// The same email belongs to Personal and Team organizations. The captured
	// token has expired, but its exact-generation proof still identifies Team.
	files[".claude.json"] = `{"oauthAccount":{"accountUuid":"new-id","organizationUuid":"org-personal","organizationName":"Personal","emailAddress":"new@example.test"},"hasCompletedOnboarding":true,"numStartups":7}`
	putIsolatedTestFile(t, filepath.Join(configDir, ".claude.json"), files[".claude.json"])
	binding := isolatedClaudeTeamBinding(t, files[".credentials.json"])
	cache, err := json.Marshal(map[string]interface{}{"claude_identity": binding})
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(configDir, identity.ClaudeIdentityCacheFile)
	putIsolatedTestFile(t, cachePath, string(cache))
	cacheBefore, err := readIsolatedSourceFile(cachePath, true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	verificationCalls := 0
	vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
		verificationCalls++
		return nil, identity.ErrClaudeIdentityUnavailable
	}
	if err := vault.BackupIsolatedClaude(root, "saved"); err != nil {
		t.Fatal(err)
	}
	if verificationCalls != 0 {
		t.Fatalf("cached expired credential triggered %d online verification attempts", verificationCalls)
	}
	assertIsolatedSavedBinding(t, vault, "saved", binding)
	assertIsolatedSnapshot(t, vault, "saved", files, &binding.Identity)
	assertIsolatedUnchanged(t, root, files)
	if err := cacheBefore.unchanged(); err != nil {
		t.Fatalf("source identity cache was modified: %v", err)
	}
}

func TestBackupIsolatedClaudeUsesCallerVerifierForFreshSource(t *testing.T) {
	root, vault, files := isolatedClaudeFixture(t)
	configDir := filepath.Join(root, "xdg_config", "claude-code")
	files[".credentials.json"] = fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"fresh-isolated-access","refreshToken":"fresh-isolated-refresh","expiresAt":%d,"scopes":["user:inference","user:profile"]}}`, time.Now().Add(time.Hour).UnixMilli())
	files[".claude.json"] = `{"oauthAccount":{"accountUuid":"new-id","organizationUuid":"org-personal","organizationName":"Personal","emailAddress":"new@example.test"},"hasCompletedOnboarding":true,"numStartups":7}`
	for _, name := range []string{".credentials.json", ".claude.json"} {
		putIsolatedTestFile(t, filepath.Join(configDir, name), files[name])
	}
	binding := isolatedClaudeTeamBinding(t, files[".credentials.json"])
	verificationCalls := 0
	vault.ClaudeIdentityVerifier = func(_ context.Context, captured []byte) (*identity.ClaudeIdentityBinding, error) {
		verificationCalls++
		if !bytes.Equal(captured, []byte(files[".credentials.json"])) {
			t.Fatal("prepared backup verified a different credential generation")
		}
		return binding, nil
	}
	if err := vault.BackupIsolatedClaude(root, "saved"); err != nil {
		t.Fatal(err)
	}
	if verificationCalls != 1 {
		t.Fatalf("isolated snapshot did not use the caller verifier exactly once: %d", verificationCalls)
	}
	assertIsolatedSavedBinding(t, vault, "saved", binding)
	assertIsolatedSnapshot(t, vault, "saved", files, &binding.Identity)
	assertIsolatedUnchanged(t, root, files)
	if _, err := os.Lstat(filepath.Join(configDir, identity.ClaudeIdentityCacheFile)); !os.IsNotExist(err) {
		t.Fatalf("import mutated the isolated source by writing an identity cache: %v", err)
	}
}

func TestBackupIsolatedClaudeRejectsInvalidSources(t *testing.T) {
	for _, fault := range []string{"missing-credentials", "missing-identity", "malformed-credentials", "malformed-settings", "null-settings", "no-refresh", "no-identity", "locked", "metadata-redirect", "metadata-malformed", "credential-link", "identity-link", "directory-link"} {
		t.Run(fault, func(t *testing.T) {
			root, vault, _ := isolatedClaudeFixture(t)
			config := filepath.Join(root, "xdg_config", "claude-code")
			switch fault {
			case "missing-credentials", "missing-identity":
				name := ".credentials.json"
				if fault == "missing-identity" {
					name = ".claude.json"
				}
				if err := os.Rename(filepath.Join(config, name), filepath.Join(config, name+".old")); err != nil {
					t.Fatal(err)
				}
			case "malformed-credentials":
				putIsolatedTestFile(t, filepath.Join(config, ".credentials.json"), "{")
			case "malformed-settings":
				putIsolatedTestFile(t, filepath.Join(config, "settings.json"), "[]")
			case "null-settings":
				putIsolatedTestFile(t, filepath.Join(config, "settings.json"), "null")
			case "no-refresh":
				putIsolatedTestFile(t, filepath.Join(config, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"access"}}`)
			case "no-identity":
				putIsolatedTestFile(t, filepath.Join(config, ".claude.json"), `{}`)
			case "locked":
				putIsolatedTestFile(t, filepath.Join(root, ".lock"), `{"pid":1}`)
			case "metadata-redirect":
				putIsolatedTestFile(t, filepath.Join(root, "profile.json"), `{"provider":"claude","name":"new-account","base_path":"/somewhere/else"}`)
			case "metadata-malformed":
				putIsolatedTestFile(t, filepath.Join(root, "profile.json"), "{")
			case "credential-link", "identity-link", "directory-link":
				path := filepath.Join(config, ".credentials.json")
				if fault == "identity-link" {
					path = filepath.Join(config, ".claude.json")
				} else if fault == "directory-link" {
					path = config
				}
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(path+".old", path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			}
			if err := vault.BackupIsolatedClaude(root, "saved"); err == nil {
				t.Fatal("invalid source accepted")
			}
			if _, err := os.Lstat(vault.ProfilePath("claude", "saved")); !os.IsNotExist(err) {
				t.Fatalf("failed import published a profile: %v", err)
			}
		})
	}
}

func TestBackupIsolatedClaudeNoOverwrite(t *testing.T) {
	root, vault, files := isolatedClaudeFixture(t)
	if err := vault.BackupIsolatedClaude(root, "saved"); err != nil {
		t.Fatal(err)
	}
	before, err := readIsolatedSourceFile(vault.BackupPath("claude", "saved", ".credentials.json"), true, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if err := vault.BackupIsolatedClaude(root, "saved"); !errors.Is(err, os.ErrExist) {
		t.Fatalf("duplicate did not fail with ErrExist: %v", err)
	}
	if err := before.unchanged(); err != nil {
		t.Fatal(err)
	}
	assertIsolatedUnchanged(t, root, files)
	for _, name := range []string{"", "../escape", "_original"} {
		if err := vault.BackupIsolatedClaude(root, name); err == nil {
			t.Fatalf("unsafe target accepted: %q", name)
		}
	}
}

func TestBackupIsolatedClaudePublicationRaces(t *testing.T) {
	for _, fault := range []string{"token-refresh", "account-change", "optional-created", "source-replaced", "source-locked", "destination-created", "prepare-failure"} {
		t.Run(fault, func(t *testing.T) {
			root, vault, _ := isolatedClaudeFixture(t)
			config := filepath.Join(root, "xdg_config", "claude-code")
			if fault == "optional-created" {
				if err := os.Rename(filepath.Join(config, "settings.json"), filepath.Join(config, "settings.json.old")); err != nil {
					t.Fatal(err)
				}
			}
			err := vault.backupIsolatedClaude(root, "saved", func(prepared *Vault, files AuthFileSet, name string) error {
				if err := prepared.BackupWithOptions(files, name, BackupOptions{}); err != nil {
					return err
				}
				switch fault {
				case "token-refresh":
					putIsolatedTestFile(t, filepath.Join(config, ".credentials.json"), `{"claudeAiOauth":{"accessToken":"rotated","refreshToken":"rotated-renewal"}}`)
				case "account-change":
					putIsolatedTestFile(t, filepath.Join(config, ".claude.json"), `{"oauthAccount":{"emailAddress":"changed@example.test"}}`)
				case "optional-created":
					putIsolatedTestFile(t, filepath.Join(config, "settings.json"), `{"model":"sonnet"}`)
				case "source-replaced":
					if err := os.Rename(config, config+".old"); err != nil {
						return err
					}
					if err := os.Mkdir(config, 0700); err != nil {
						return err
					}
				case "source-locked":
					putIsolatedTestFile(t, filepath.Join(root, ".lock"), `{"pid":1}`)
				case "destination-created":
					if err := os.Mkdir(vault.ProfilePath("claude", "saved"), 0700); err != nil {
						return err
					}
				case "prepare-failure":
					return errors.New("injected preparation failure")
				}
				return nil
			})
			if err == nil {
				t.Fatal("racy source/destination was published")
			}
			if fault == "destination-created" {
				if !errors.Is(err, os.ErrExist) {
					t.Fatalf("concurrent target not protected: %v", err)
				}
				entries, err := os.ReadDir(vault.ProfilePath("claude", "saved"))
				if err != nil || len(entries) != 0 {
					t.Fatalf("concurrent empty destination replaced: %v", err)
				}
			} else if _, err := os.Lstat(vault.ProfilePath("claude", "saved")); !os.IsNotExist(err) {
				t.Fatalf("failed import published a destination: %v", err)
			}
		})
	}
}

func TestBackupIsolatedClaudeConcurrentImports(t *testing.T) {
	root, vault, files := isolatedClaudeFixture(t)
	var wg sync.WaitGroup
	errorsSeen := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errorsSeen <- NewVault(vault.BasePath()).BackupIsolatedClaude(root, "saved")
		}()
	}
	wg.Wait()
	close(errorsSeen)
	succeeded, existed := 0, 0
	for err := range errorsSeen {
		if err == nil {
			succeeded++
		} else if errors.Is(err, os.ErrExist) {
			existed++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || existed != 1 {
		t.Fatalf("expected one publication and one refusal, got %d/%d", succeeded, existed)
	}
	assertIsolatedUnchanged(t, root, files)
}
