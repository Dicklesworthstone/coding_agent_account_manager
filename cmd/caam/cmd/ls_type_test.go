package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/codex"
	"github.com/spf13/cobra"
)

// setupProfileTypes builds a codex vault with "both" and "vaultonly", and an
// isolated-profile store with "both" and "isoonly" (issue #110).
func setupProfileTypes(t *testing.T) {
	t.Helper()
	origVault, origTools, origStore := vault, tools, profileStore
	t.Cleanup(func() { vault, tools, profileStore = origVault, origTools, origStore })

	root := t.TempDir()
	vaultDir := filepath.Join(root, "vault")
	vault = authfile.NewVault(vaultDir)
	profileStore = profile.NewStore(filepath.Join(root, "profiles"))
	tools = map[string]func() authfile.AuthFileSet{
		"codex": func() authfile.AuthFileSet {
			return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
		},
	}
	for _, name := range []string{"both", "vaultonly"} {
		dir := filepath.Join(vaultDir, "codex", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"access_token":"a","refresh_token":"r"}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"both", "isoonly"} {
		if _, err := profileStore.Create("codex", name, "oauth"); err != nil {
			t.Fatalf("create isolated profile %s: %v", name, err)
		}
	}
}

func runLsJSON(t *testing.T, args []string) map[string]any {
	t.Helper()
	cmd := &cobra.Command{RunE: runLs}
	cmd.Flags().Bool("no-color", false, "")
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().String("tag", "", "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runLs(cmd, args); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	return out
}

func TestLsJSONReportsProfileType(t *testing.T) {
	setupProfileTypes(t)

	for _, args := range [][]string{{"codex"}, nil} {
		out := runLsJSON(t, args)

		// The row set is unchanged: vault profiles only.
		if got := out["count"]; got != float64(2) {
			t.Fatalf("args %v: count = %v, want 2", args, got)
		}
		rows := map[string]map[string]any{}
		for _, r := range out["profiles"].([]any) {
			row := r.(map[string]any)
			rows[row["name"].(string)] = row
		}
		for name, wantIsolated := range map[string]bool{"both": true, "vaultonly": false} {
			row, ok := rows[name]
			if !ok {
				t.Fatalf("args %v: no row for %s in %v", args, name, out)
			}
			if row["type"] != "vault" || row["isolated"] != wantIsolated {
				t.Errorf("args %v: %s type=%v isolated=%v, want vault/%v", args, name, row["type"], row["isolated"], wantIsolated)
			}
		}

		only, _ := out["isolated_only"].([]any)
		if len(only) != 1 {
			t.Fatalf("args %v: isolated_only = %v, want just isoonly", args, out["isolated_only"])
		}
		entry := only[0].(map[string]any)
		if entry["tool"] != "codex" || entry["name"] != "isoonly" || entry["type"] != "isolated" {
			t.Errorf("args %v: isolated_only entry = %v", args, entry)
		}
	}
}

func TestLsJSONOmitsIsolatedOnlyWhenNone(t *testing.T) {
	setupProfileTypes(t)
	profileStore = profile.NewStore(filepath.Join(t.TempDir(), "empty"))
	out := runLsJSON(t, []string{"codex"})
	if _, present := out["isolated_only"]; present {
		t.Errorf("isolated_only present with no isolated profiles: %v", out)
	}
}

func TestIsolatedProfileLoadErrorExplainsVaultProfiles(t *testing.T) {
	setupProfileTypes(t)

	_, err := profileStore.Load("codex", "vaultonly")
	if err == nil {
		t.Fatal("expected vaultonly to be missing from the isolated store")
	}
	msg := isolatedProfileLoadError("codex", "vaultonly", err).Error()
	for _, want := range []string{
		"codex/vaultonly is a vault profile, not an isolated profile",
		"caam activate codex vaultonly",
		"caam profile add codex vaultonly, then caam login codex vaultonly",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}

	_, err = profileStore.Load("codex", "nosuch")
	msg = isolatedProfileLoadError("codex", "nosuch", err).Error()
	if !strings.Contains(msg, "profile codex/nosuch not found") || !strings.Contains(msg, "caam profile ls codex") {
		t.Errorf("unknown profile message = %q", msg)
	}
}

func TestIsolatedProfileLoadErrorClaudeHint(t *testing.T) {
	setupProfileTypes(t)
	dir := filepath.Join(t.TempDir(), "vault")
	vault = authfile.NewVault(dir)
	if err := os.MkdirAll(filepath.Join(dir, "claude", "work"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := profileStore.Load("claude", "work")
	msg := isolatedProfileLoadError("claude", "work", err).Error()
	if strings.Contains(msg, "caam login claude") || !strings.Contains(msg, "caam exec claude work, then /login") {
		t.Errorf("claude hint must not suggest caam login: %s", msg)
	}
}

func TestExecAndLoginReportVaultProfile(t *testing.T) {
	setupProfileTypes(t)
	origRegistry := registry
	t.Cleanup(func() { registry = origRegistry })
	registry = provider.NewRegistry()
	registry.Register(codex.New())

	for _, c := range []*cobra.Command{execCmd, loginCmd} {
		err := c.RunE(c, []string{"codex", "vaultonly"})
		if err == nil || !strings.Contains(err.Error(), "is a vault profile") {
			t.Errorf("%s error = %v, want the vault-profile explanation", c.Name(), err)
		}
	}
}

func claudeCLIIdentityBinding(t *testing.T, credential []byte, orgID, organization string) *identity.ClaudeIdentityBinding {
	t.Helper()
	fingerprint, err := identity.ClaudeCredentialFingerprint(credential)
	if err != nil {
		t.Fatal(err)
	}
	return &identity.ClaudeIdentityBinding{
		Identity: identity.Identity{
			Provider: "claude", AccountID: "shared-account-id", Email: "shared@example.test",
			OrganizationID: orgID, Organization: organization,
		},
		CredentialFingerprint: fingerprint,
		VerifiedAt:            time.Now().UTC(),
	}
}

func setupClaudeOrganizationProfiles(t *testing.T) (authfile.AuthFileSet, map[string][]byte) {
	t.Helper()
	origVault, origTools, origStore, origHealth := vault, tools, profileStore, healthStore
	t.Cleanup(func() { vault, tools, profileStore, healthStore = origVault, origTools, origStore, origHealth })
	root := t.TempDir()
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	t.Setenv("CAAM_KEYCHAIN", "0")
	profileStore, healthStore = nil, nil
	vault = authfile.NewVault(filepath.Join(root, "vault"))
	liveDir := filepath.Join(root, "native")
	fileSet := authfile.AuthFileSet{
		Tool: "claude",
		Files: []authfile.AuthFileSpec{
			{Tool: "claude", Path: filepath.Join(liveDir, ".credentials.json"), Required: true},
			{Tool: "claude", Path: filepath.Join(liveDir, ".claude.json")},
		},
	}
	tools = map[string]func() authfile.AuthFileSet{
		"claude": func() authfile.AuthFileSet { return fileSet },
	}
	credentials := make(map[string][]byte)
	for _, name := range []string{"personal", "team"} {
		plan := "max"
		if name == "team" {
			plan = "team"
		}
		data := []byte(fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d,"subscriptionType":%q}}`,
			"SYNTHETIC-"+name+"-access", "SYNTHETIC-"+name+"-refresh", time.Now().Add(3*time.Hour).UnixMilli(), plan))
		credentials[name] = data
		profileDir := vault.ProfilePath("claude", name)
		writeNativeTestCredential(t, filepath.Join(profileDir, ".credentials.json"), string(data))
		// Deliberately reuse the display name. The stable organization UUID
		// must distinguish these rows even though both email and label match.
		binding := claudeCLIIdentityBinding(t, data, "org-"+name, "Shared display name")
		metadata, err := json.Marshal(map[string]any{"claude_identity": binding})
		if err != nil {
			t.Fatal(err)
		}
		writeNativeTestCredential(t, filepath.Join(profileDir, "meta.json"), string(metadata))
		// Simulate a stale .claude.json saved from another organization.
		writeNativeTestCredential(t, filepath.Join(profileDir, ".claude.json"), `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-stale","organizationName":"Stale label"}}`)
	}
	return fileSet, credentials
}

func TestLsClaudeOrganizationsUseBoundIdentityAndPersistAcrossRestart(t *testing.T) {
	fileSet, credentials := setupClaudeOrganizationProfiles(t)
	for _, active := range []string{"personal", "team", "unknown"} {
		t.Run(active, func(t *testing.T) {
			live := credentials[active]
			if active == "unknown" {
				live = []byte(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-unsaved-access","refreshToken":"SYNTHETIC-unsaved-refresh"}}`)
			}
			writeNativeTestCredential(t, fileSet.Files[0].Path, string(live))
			// Another running session rewrites metadata independently of the
			// selected token. Listing must use credentials to mark the row.
			writeNativeTestCredential(t, fileSet.Files[1].Path, `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-personal","organizationName":"Stale personal label"}}`)
			for restart := 0; restart < 2; restart++ {
				vault = authfile.NewVault(vault.BasePath())
				verificationCalls := 0
				vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
					verificationCalls++
					return nil, fmt.Errorf("identity verification unavailable")
				}
				for _, args := range [][]string{{"claude"}, nil} {
					out := runLsJSON(t, args)
					if out["count"] != float64(2) {
						t.Fatalf("unexpected listing count: %v", out["count"])
					}
					for _, value := range out["profiles"].([]any) {
						row := value.(map[string]any)
						name := row["name"].(string)
						id := row["identity"].(map[string]any)
						if id["email"] != "shared@example.test" || id["organization_id"] != "org-"+name || id["organization"] != "Shared display name" || id["identity_verified"] != true {
							t.Fatalf("listing used stale labels for %s: %v", name, id)
						}
						if row["active"] != (name == active) {
							t.Fatalf("active=%s: wrong active marker for %s", active, name)
						}
					}
					command := &cobra.Command{}
					command.Flags().Bool("json", false, "")
					command.Flags().Bool("no-color", true, "")
					command.Flags().String("tag", "", "")
					text, err := captureStdout(t, func() error { return runLs(command, args) })
					if err != nil {
						t.Fatal(err)
					}
					for _, expected := range []string{"ORGANIZATION", "Shared display name (org-personal)", "Shared display name (org-team)"} {
						if !strings.Contains(text, expected) {
							t.Fatalf("organization missing from listing: %q", expected)
						}
					}
					if strings.Contains(text, "Stale") || strings.Contains(text, "SYNTHETIC-") || strings.Contains(text, "[unverified]") {
						t.Fatal("listing exposed credentials or used unverified stale metadata")
					}
				}
				if verificationCalls != 0 {
					t.Fatal("listing attempted online identity verification")
				}
			}
		})
	}
}

func TestFormatOrganizationDisplay(t *testing.T) {
	for _, tc := range []struct {
		name string
		id   *identity.Identity
		want string
	}{
		{"missing", nil, "unknown"},
		{"unknown", &identity.Identity{Provider: "claude"}, "unknown"},
		{"verified", &identity.Identity{Organization: "Team", OrganizationID: "org-team", Verified: true}, "Team (org-team)"},
		{"unnamed", &identity.Identity{OrganizationID: "org-team", Verified: true}, "org-team"},
		{"unverified", &identity.Identity{Organization: "Team", OrganizationID: "org-team"}, "Team (org-team) [unverified]"},
		{"legacy", &identity.Identity{Organization: "Team"}, "Team (ID unknown) [unverified]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatOrganizationDisplay(tc.id); got != tc.want {
				t.Fatalf("organization = %q, want %q", got, tc.want)
			}
		})
	}
}
