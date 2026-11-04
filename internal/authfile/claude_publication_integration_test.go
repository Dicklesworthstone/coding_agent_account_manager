package authfile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func TestClaudeRestorePublicationFailureRestoresIncomingOAuthAndSettings(t *testing.T) {
	for _, entry := range []string{"restore", "switch"} {
		for _, target := range []string{"oauth", "helper"} {
			t.Run(entry+"/"+target, func(t *testing.T) {
				f := newKeychainFixture(t)
				t.Setenv("CLAUDE_CONFIG_DIR", "")
				t.Setenv("XDG_CONFIG_HOME", t.TempDir())
				settings := filepath.Join(filepath.Dir(f.credPath), "settings.json")
				f.fileSet.Files = append(f.fileSet.Files, AuthFileSpec{Path: settings})
				outgoing := keychainCreds("outgoing")
				setClaudeFixtureIdentities(t, f.vault, map[string]identity.Identity{
					outgoing:                  {AccountID: "outgoing", OrganizationID: "outgoing-org"},
					keychainCreds("incoming"): {AccountID: "incoming", OrganizationID: "incoming-org"},
				})
				state := keychainState("outgoing@example.invalid")
				const policy = `{"permissions":{"allow":["Read"],"defaultMode":"auto"},"autoMode":{"allow":["tests"]},"mcpServers":{"local":{}},"hooks":{"Stop":[]},"apiKeyHelper":"outgoing-helper"}`
				f.storeToken(outgoing)
				publicationWrite(t, f.credPath, outgoing)
				publicationWrite(t, f.statePath, state)
				publicationWrite(t, settings, policy)
				profileDir := f.vault.ProfilePath("claude", target)
				publicationWrite(t, filepath.Join(profileDir, ".claude.json"), keychainState("incoming@example.invalid"))
				if target == "oauth" {
					publicationWrite(t, filepath.Join(profileDir, ".credentials.json"), keychainCreds("incoming"))
					publicationWrite(t, filepath.Join(profileDir, "settings.json"), `{"model":"stale"}`)
				} else {
					publicationWrite(t, filepath.Join(profileDir, "settings.json"), `{"apiKeyHelper":"incoming-helper"}`)
				}
				// Reads still use the real fake-keychain implementation. Reject
				// only publication, after Restore has installed its file batch.
				t.Setenv("CAAM_TEST_PUBLICATION_BACKEND", os.Getenv("CAAM_KEYCHAIN_BIN"))
				wrapper := filepath.Join(t.TempDir(), "reject-publication")
				const script = `#!/bin/sh
case "$1" in
  add-generic-password|delete-generic-password)
    echo "security: User interaction is not allowed." >&2
    exit 36
    ;;
esac
exec "$CAAM_TEST_PUBLICATION_BACKEND" "$@"
`
				if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CAAM_KEYCHAIN_BIN", wrapper)
				var err error
				if entry == "restore" {
					err = f.vault.Restore(f.fileSet, target)
				} else {
					_, err = f.vault.Switch(f.fileSet, target, SwitchOptions{})
				}
				if err == nil || !strings.Contains(err.Error(), "keychain") {
					t.Fatalf("publication failure not surfaced: %v", err)
				}
				for path, expected := range map[string]string{f.credPath: outgoing, f.statePath: state, settings: policy} {
					if got := string(publicationRead(t, path)); got != expected {
						t.Fatalf("failed %s left incoming account state at %s", entry, path)
					}
				}
				if got, exists := f.storedToken(); !exists || got != outgoing {
					t.Fatal("failed publication changed the authoritative outgoing login")
				}
			})
		}
	}
}

func TestClaudeIncomingOAuthStagingFailureDoesNotChangeSharedPolicy(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses procfs for deterministic staging failure")
	}
	for _, entry := range []string{"restore", "switch"} {
		t.Run(entry, func(t *testing.T) {
			t.Setenv("CAAM_KEYCHAIN", "0")
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			dir := t.TempDir()
			settings, legacy := filepath.Join(dir, "settings.json"), filepath.Join(dir, ".claude.json")
			const policy = `{"permissions":{"allow":[]},"apiKeyHelper":"outgoing"}`
			const state = `{"oauthAccount":{"accountUuid":"outgoing"}}`
			publicationWrite(t, settings, policy)
			publicationWrite(t, legacy, state)
			files := AuthFileSet{Tool: "claude", AllowOptionalOnly: true, Files: []AuthFileSpec{
				{Path: settings}, {Path: legacy}, {Path: "/proc/self/.credentials.json", Required: true},
			}}
			v := NewVault(t.TempDir())
			publicationWrite(t, filepath.Join(v.ProfilePath("claude", "target"), ".credentials.json"), keychainCreds("target"))
			publicationWrite(t, filepath.Join(v.ProfilePath("claude", "target"), "settings.json"), `{"apiKeyHelper":"target"}`)
			var err error
			if entry == "restore" {
				err = v.Restore(files, "target")
			} else {
				_, err = v.Switch(files, "target", SwitchOptions{BackupMode: "never"})
			}
			if err == nil {
				t.Fatal("expected unwritable incoming OAuth destination")
			}
			if string(publicationRead(t, settings)) != policy || string(publicationRead(t, legacy)) != state {
				t.Fatal("failed OAuth staging left target helpers/identity installed")
			}
		})
	}
}

func TestClaudeFinalPublicationCheckNeverReplaysAnOldMirror(t *testing.T) {
	f := newKeychainFixture(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	publicationWrite(t, f.credPath, keychainCreds("previously-published"))
	f.storeToken(keychainCreds("native-new-login"))
	if err := pushClaudeKeychain(f.fileSet); err == nil {
		t.Fatal("accepted a native keychain change after publication")
	}
	if got, ok := f.storedToken(); !ok || got != keychainCreds("native-new-login") {
		t.Fatal("final check replayed the old file over the native keychain login")
	}
}

func TestClaudeSwitchDetectsNativeLabelRewriteDuringKeychainPublication(t *testing.T) {
	f := newKeychainFixture(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	outgoing, incoming := keychainCreds("previous-owner"), keychainCreds("selected-owner")
	setClaudeFixtureIdentities(t, f.vault, map[string]identity.Identity{
		outgoing: {AccountID: "shared-account", OrganizationID: "personal", Email: "shared@example.test"},
		incoming: {AccountID: "shared-account", OrganizationID: "team", Email: "shared@example.test"},
	})
	f.storeToken(outgoing)
	publicationWrite(t, f.credPath, outgoing)
	publicationWrite(t, f.statePath, `{"oauthAccount":{"accountUuid":"shared-account","organizationUuid":"personal"}}`)
	publicationWrite(t, f.vault.BackupPath("claude", "team", claudeCredentialsFile), incoming)
	t.Setenv("CAAM_TEST_PUBLICATION_BACKEND", os.Getenv("CAAM_KEYCHAIN_BIN"))
	t.Setenv("CAAM_TEST_NATIVE_STATE", f.statePath)
	wrapper := filepath.Join(t.TempDir(), "native-label-writer")
	const script = `#!/bin/sh
if [ "$1" = "add-generic-password" ]; then
  printf '%s' '{"oauthAccount":{"accountUuid":"shared-account","organizationUuid":"personal"},"numStartups":81}' > "$CAAM_TEST_NATIVE_STATE"
fi
exec "$CAAM_TEST_PUBLICATION_BACKEND" "$@"
`
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CAAM_KEYCHAIN_BIN", wrapper)
	result, err := f.vault.Switch(f.fileSet, "team", SwitchOptions{})
	if err == nil || !strings.Contains(err.Error(), "shared account metadata changed") || !result.RestoreStarted || result.AutoBackup == "" {
		t.Fatalf("native metadata rewrite was reported as a successful switch: %+v, %v", result, err)
	}
	if string(publicationRead(t, f.statePath)) != `{"oauthAccount":{"accountUuid":"shared-account","organizationUuid":"personal"},"numStartups":81}` {
		t.Fatal("failure overwrote the concurrent native state")
	}
	if string(publicationRead(t, f.vault.BackupPath("claude", result.AutoBackup, claudeCredentialsFile))) != outgoing {
		t.Fatal("failed publication lost the outgoing recovery credential")
	}
	// The stale shared label must not change which credential is actually
	// active. Removing the interfering writer permits a guarded retry.
	active, err := f.vault.ActiveProfile(f.fileSet)
	if err != nil || active != "team" {
		t.Fatalf("native label changed token-based selection: %q, %v", active, err)
	}
	t.Setenv("CAAM_KEYCHAIN_BIN", os.Getenv("CAAM_TEST_PUBLICATION_BACKEND"))
	if _, err := f.vault.Switch(f.fileSet, "team", SwitchOptions{}); err != nil {
		t.Fatalf("retry failed to reconcile the shared label: %v", err)
	}
}

func TestClaudeRecoverableRestoreUsesExistingSameAccountFreshnessProtection(t *testing.T) {
	for _, sameAccount := range []bool{true, false} {
		t.Run(map[bool]string{true: "same-account", false: "different-account"}[sameAccount], func(t *testing.T) {
			f := newKeychainFixture(t)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			const fresh = `{"claudeAiOauth":{"accessToken":"newer","refreshToken":"newer-refresh","expiresAt":1993456000000}}`
			stale := keychainCreds("snapshot")
			liveAccount := identity.Identity{AccountID: "account-alice", OrganizationID: "org-alice", Email: "alice@example.invalid"}
			savedAccount := liveAccount
			if !sameAccount {
				savedAccount = identity.Identity{AccountID: "account-bob", OrganizationID: "org-bob", Email: "bob@example.invalid"}
			}
			setClaudeFixtureIdentities(t, f.vault, map[string]identity.Identity{fresh: liveAccount, stale: savedAccount})
			f.storeToken(fresh)
			publicationWrite(t, f.credPath, fresh)
			publicationWrite(t, f.statePath, keychainState("alice@example.invalid"))
			targetDir := f.vault.ProfilePath("claude", "target")
			publicationWrite(t, filepath.Join(targetDir, ".credentials.json"), stale)
			email := "alice@example.invalid"
			if !sameAccount {
				email = "bob@example.invalid"
			}
			publicationWrite(t, filepath.Join(targetDir, ".claude.json"), keychainState(email))
			if err := f.vault.Restore(f.fileSet, "target"); err != nil {
				t.Fatal(err)
			}
			want := stale
			if sameAccount {
				want = fresh
			}
			if string(publicationRead(t, f.credPath)) != want {
				t.Fatal("recoverable restore changed freshness/account selection semantics")
			}
			if got, exists := f.storedToken(); !exists || got != want {
				t.Fatal("keychain did not receive the freshness-selected credential")
			}
		})
	}
}
