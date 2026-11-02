package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func writeShallowCLIIdentity(t *testing.T, dir, token, organization, metadataName string) ([]byte, []byte) {
	t.Helper()
	credential := []byte(`{"claudeAiOauth":{"accessToken":"synthetic-` + token + `","refreshToken":"synthetic-refresh-` + token + `","expiresAt":1700000000000}}`)
	fingerprint, err := identity.ClaudeCredentialFingerprint(credential)
	if err != nil {
		t.Fatal(err)
	}
	binding := &identity.ClaudeIdentityBinding{
		Identity: identity.Identity{
			Provider: "claude", AccountID: "same-account", OrganizationID: organization,
			Email: "shared@example.com", Organization: organization, Verified: true,
		},
		CredentialFingerprint: fingerprint, VerifiedAt: time.Unix(1699990000, 0).UTC(),
	}
	metadata, err := json.Marshal(map[string]any{"claude_identity": binding})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{".credentials.json": credential, metadataName: metadata} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return credential, metadata
}

func TestShallowCLIProofSeedingAndLegacyCacheRepair(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires privileges on Windows")
	}
	for _, noSync := range []bool{false, true} {
		t.Run(map[bool]string{true: "no-sync", false: "sync"}[noSync], func(t *testing.T) {
			base, host := shallowEnv(t)
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			_, hostProof := writeShallowCLIIdentity(t, filepath.Join(host, ".claude"), "host", "personal", identity.ClaudeIdentityCacheFile)
			for name, body := range map[string]string{
				"auth.json":    `{"accessToken":"synthetic-host-alternate"}`,
				".claude.json": `{"oauthAccount":{"accountUuid":"host-account","organizationUuid":"personal"}}`,
			} {
				if err := os.WriteFile(filepath.Join(host, ".claude", name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			vaultDir := vault.ProfilePath("claude", "team")
			credential, _ := writeShallowCLIIdentity(t, vaultDir, "team", "team", "meta.json")
			if _, _, err := runCmdCaptured(t, "shallow-profile", "create", "team", "--from-vault", "claude/team", "--json"); err != nil {
				t.Fatal(err)
			}
			profileCredential := filepath.Join(base, "team", ".claude", ".credentials.json")
			id, err := identity.ExtractFromClaudeCredentials(profileCredential)
			if err != nil || id == nil || !id.Verified || id.OrganizationID != "team" {
				t.Fatalf("vault creation lost exact cached Team identity: %#v, %v", id, err)
			}
			cache := filepath.Join(base, "team", ".claude", identity.ClaudeIdentityCacheFile)
			if err := os.Rename(cache, cache+".saved"); err != nil {
				t.Fatal(err)
			}
			hostCache := filepath.Join(host, ".claude", identity.ClaudeIdentityCacheFile)
			if err := os.Symlink(hostCache, cache); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"auth.json", ".claude.json"} {
				path := filepath.Join(base, "team", ".claude", name)
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatalf("creation inherited host %s", name)
				}
				if err := os.Symlink(filepath.Join(host, ".claude", name), path); err != nil {
					t.Fatal(err)
				}
			}
			called := false
			previousExec := spawnExec
			spawnExec = func(_ string, _ []string, _ []string) error {
				called = true
				info, err := os.Lstat(cache)
				if err != nil || !info.Mode().IsRegular() {
					t.Fatalf("child started with a shared identity cache: %v", err)
				}
				for _, name := range []string{"auth.json", ".claude.json"} {
					data, err := os.ReadFile(filepath.Join(base, "team", ".claude", name))
					if name == "auth.json" {
						if !os.IsNotExist(err) {
							t.Fatalf("child retained inherited alternate credentials: %v", err)
						}
					} else if err != nil || string(data) != `{}` {
						t.Fatalf("child started with inherited host %s: %v", name, err)
					}
				}
				id, err := identity.ExtractFromClaudeCredentials(profileCredential)
				if err != nil || id == nil || id.Verified {
					t.Fatalf("repair attributed host proof to the Team grant: %#v, %v", id, err)
				}
				return nil
			}
			t.Cleanup(func() { spawnExec = previousExec })
			stdout, _, err := runCmdCaptured(t, "shallow-spawn", "team", "--print-env")
			if err == nil || strings.Contains(stdout, "export HOME") || called {
				t.Fatalf("unsafe export produced a usable environment or started a child: %v", err)
			}
			args := []string{"shallow-spawn", "team"}
			if noSync {
				args = append(args, "--no-sync-config")
			}
			args = append(args, "--", "sh", "-c", "true")
			if _, _, err := runCmdCaptured(t, args...); err != nil {
				t.Fatal(err)
			}
			if !called {
				t.Fatal("successful private-cache repair did not start the selected child")
			}
			if data, err := os.ReadFile(profileCredential); err != nil || string(data) != string(credential) {
				t.Fatal("cache repair replaced profile credentials")
			}
			if data, err := os.ReadFile(hostCache); err != nil || string(data) != string(hostProof) {
				t.Fatal("cache repair changed the host proof")
			}
			if _, _, err := runCmdCaptured(t, "shallow-spawn", "team", "--print-env"); err != nil {
				t.Fatalf("repaired profile cannot export its isolated environment: %v", err)
			}
		})
	}
}
