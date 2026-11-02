package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

func tarClaudeTestFiles(t *testing.T, token, organization string) map[string]string {
	t.Helper()
	credentials := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`,
		"SYNTHETIC-"+token+"-access", "SYNTHETIC-"+token+"-refresh", time.Now().Add(-time.Hour).UnixMilli())
	binding := claudeCLIIdentityBinding(t, []byte(credentials), "org-"+organization, organization)
	metadata, err := json.Marshal(map[string]any{"claude_identity": binding, "description": "saved profile note"})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		".credentials.json":  credentials,
		"meta.json":          string(metadata),
		".claude.json":       `{"oauthAccount":{"accountUuid":"shared-account-id","emailAddress":"shared@example.test","organizationUuid":"org-stale-personal","organizationName":"Stale Personal"},"projects":{"/workspace/example":{"hasTrustDialogAccepted":true}},"numStartups":7}`,
		"nested/policy.json": `{"workspacePolicy":"preserve"}`,
	}
}

func tarWriteProfile(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		writeNativeTestCredential(t, filepath.Join(directory, filepath.FromSlash(name)), contents)
	}
}

// tarRawFixture bypasses export validation so import is tested against
// checksum-valid archives made by older clients or inconsistent sources.
func tarRawFixture(t *testing.T, profiles map[string]map[string]string, mutate func(*vaultExportManifest)) []byte {
	t.Helper()
	manifest := &vaultExportManifest{FormatVersion: exportFormatVersion, CreatedAt: time.Now().UTC()}
	var keys []string
	for key := range profiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var payloads []struct{ name, contents string }
	for _, key := range keys {
		parts := strings.SplitN(key, "/", 2)
		item := vaultExportItem{Tool: parts[0], Profile: parts[1]}
		var names []string
		for name := range profiles[key] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			contents := profiles[key][name]
			pathname := tarVaultPrefix + key + "/" + name
			digest := sha256.Sum256([]byte(contents))
			item.Files = append(item.Files, vaultExportFile{Path: pathname, SHA256: hex.EncodeToString(digest[:]), Size: int64(len(contents)), Mode: 0600})
			payloads = append(payloads, struct{ name, contents string }{pathname, contents})
		}
		manifest.Items = append(manifest.Items, item)
	}
	if mutate != nil {
		mutate(manifest)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	if err := writeTarBytes(archive, exportManifestName, 0600, manifest.CreatedAt, encoded); err != nil {
		t.Fatal(err)
	}
	for _, payload := range payloads {
		if err := writeTarBytes(archive, payload.name, 0600, manifest.CreatedAt, []byte(payload.contents)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func tarReadPayloads(t *testing.T, data []byte) map[string][]byte {
	t.Helper()
	compressed, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	archive := tar.NewReader(compressed)
	files := make(map[string][]byte)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		files[header.Name], err = io.ReadAll(archive)
		if err != nil {
			t.Fatal(err)
		}
	}
	return files
}

func TestTarClaudeExpiredProofSurvivesOfflineTransferAndRestart(t *testing.T) {
	for _, mode := range []string{"canonical", "cache-only", "unverified"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			source := authfile.NewVault(filepath.Join(root, "source"))
			files := tarClaudeTestFiles(t, "team", "Team")
			switch mode {
			case "canonical":
				// Matching canonical proof outranks a stale secondary native cache.
				files[identity.ClaudeIdentityCacheFile] = tarClaudeTestFiles(t, "personal", "Personal")["meta.json"]
			case "cache-only":
				files[identity.ClaudeIdentityCacheFile] = files["meta.json"]
				files["meta.json"] = `{"description":"saved profile note"}`
			case "unverified":
				files["meta.json"] = `{"description":"saved profile note"}`
			}
			target := source.ProfilePath("claude", "work")
			tarWriteProfile(t, target, files)
			source.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Fatal("offline export invoked the identity endpoint")
				return nil, nil
			}
			archive := tarExportTestBytes(t, source, exportRequest{Tool: "claude", Profile: "work"})
			for name, contents := range files {
				requireSwitchCredential(t, filepath.Join(target, name), contents)
			}
			payloads := tarReadPayloads(t, archive)
			prefix := tarVaultPrefix + "claude/work/"
			if _, exists := payloads[prefix+identity.ClaudeIdentityCacheFile]; exists {
				t.Fatal("redundant native cache reached the transfer payload")
			}
			var settings map[string]json.RawMessage
			if err := json.Unmarshal(payloads[prefix+".claude.json"], &settings); err != nil {
				t.Fatal(err)
			}
			if string(settings["numStartups"]) != "7" || !bytes.Contains(settings["projects"], []byte("hasTrustDialogAccepted")) {
				t.Fatal("export normalization discarded unrelated settings")
			}
			if mode == "unverified" {
				if _, exists := settings["oauthAccount"]; exists {
					t.Fatal("unverified account label remained in the archive")
				}
			} else if !bytes.Contains(settings["oauthAccount"], []byte("org-Team")) || bytes.Contains(settings["oauthAccount"], []byte("Stale Personal")) {
				t.Fatal("export did not pair Team credentials with Team metadata")
			}
			destination := authfile.NewVault(filepath.Join(root, "destination"))
			destination.ClaudeIdentityVerifier = source.ClaudeIdentityVerifier
			if _, err := importArchive(bytes.NewReader(archive), destination, importOptions{}); err != nil {
				t.Fatal(err)
			}
			// A fresh vault instance must recover ownership from saved bytes alone.
			restarted := authfile.NewVault(destination.BasePath())
			restarted.ClaudeIdentityVerifier = source.ClaudeIdentityVerifier
			state, err := restarted.ClaudeProfileIdentity(context.Background(), "work", authfile.ClaudeProfileIdentityOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unverified" {
				if state.State != identity.ClaudeIdentityUnknown || state.Identity != nil {
					t.Fatalf("unknown ownership became trusted: %s", state.State)
				}
			} else if state.State != identity.ClaudeIdentityVerified || state.Identity == nil || state.Identity.OrganizationID != "org-Team" {
				t.Fatalf("expired verified ownership was lost: %s", state.State)
			}
			requireSwitchCredential(t, filepath.Join(destination.ProfilePath("claude", "work"), ".credentials.json"), files[".credentials.json"])
		})
	}
}

func TestTarForceReplacementPreservesDifferentOrganizationSnapshot(t *testing.T) {
	root := t.TempDir()
	v := authfile.NewVault(filepath.Join(root, "vault"))
	old := tarClaudeTestFiles(t, "personal", "Personal")
	incoming := tarClaudeTestFiles(t, "team", "Team")
	profile := v.ProfilePath("claude", "shared-email")
	tarWriteProfile(t, profile, old)
	before, err := vaulttransfer.Capture(profile)
	if err != nil {
		t.Fatal(err)
	}
	archive := tarRawFixture(t, map[string]map[string]string{"claude/shared-email": incoming}, nil)
	if _, err := importArchive(bytes.NewReader(archive), v, importOptions{}); err == nil {
		t.Fatal("different organization silently replaced an existing profile")
	}
	if err := before.Check(); err != nil {
		t.Fatalf("refused import changed the existing profile: %v", err)
	}
	result, err := importArchive(bytes.NewReader(archive), v, importOptions{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Profiles) != 1 || !result.Profiles[0].Imported || result.Profiles[0].RecoveryPath == "" {
		t.Fatal("explicit replacement failed to retain recovery")
	}
	if err := before.CheckAt(result.Profiles[0].RecoveryPath); err != nil {
		t.Fatalf("previous profile was not retained completely: %v", err)
	}
	listed, err := v.List("claude")
	if err != nil || len(listed) != 1 || listed[0] != "shared-email" {
		t.Fatalf("private recovery was exposed as a selectable profile: %v, %v", listed, err)
	}
	binding, err := identity.ReadClaudeIdentityBinding(filepath.Join(profile, ".credentials.json"))
	if err != nil || binding == nil || binding.Identity.OrganizationID != "org-Team" {
		t.Fatal("replacement did not retain Team ownership")
	}
	settings, err := os.ReadFile(filepath.Join(profile, ".claude.json"))
	if err != nil || bytes.Contains(settings, []byte("Stale Personal")) || !bytes.Contains(settings, []byte("org-Team")) {
		t.Fatal("imported credentials and organization label disagree")
	}
}

func TestTarRejectsInvalidIncomingMetadataBeforePublishingAnyProfile(t *testing.T) {
	for _, invalid := range []string{"access", "refresh", "cache-only", "malformed-proof", "malformed-settings"} {
		t.Run(invalid, func(t *testing.T) {
			v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			valid := tarClaudeTestFiles(t, "personal", "Personal")
			bad := tarClaudeTestFiles(t, "team", "Team")
			switch invalid {
			case "access", "refresh":
				bad[".credentials.json"] = strings.ReplaceAll(bad[".credentials.json"], "team-"+invalid, "replaced-"+invalid)
			case "cache-only":
				bad[identity.ClaudeIdentityCacheFile] = valid["meta.json"]
				bad["meta.json"] = `{"description":"legacy"}`
			case "malformed-proof":
				bad["meta.json"] = `{"claude_identity":{"secret":"SYNTHETIC-DO-NOT-PRINT"}}`
			case "malformed-settings":
				bad[".claude.json"] = `{"oauthAccount":"SYNTHETIC-DO-NOT-PRINT"`
			}
			tarWriteProfile(t, v.ProfilePath("claude", "z-bad"), valid)
			before, err := vaulttransfer.Capture(v.ProfilePath("claude", "z-bad"))
			if err != nil {
				t.Fatal(err)
			}
			archive := tarRawFixture(t, map[string]map[string]string{"claude/a-valid": valid, "claude/z-bad": bad}, nil)
			result, err := importArchive(bytes.NewReader(archive), v, importOptions{Force: true})
			if err == nil || result != nil && len(result.Profiles) != 0 {
				t.Fatal("invalid incoming metadata was accepted or a previous item was published")
			}
			if strings.Contains(err.Error(), "SYNTHETIC-") {
				t.Fatal("identity failure disclosed credentials")
			}
			if err := before.Check(); err != nil {
				t.Fatalf("invalid import changed the previous profile: %v", err)
			}
			if _, err := os.Stat(v.ProfilePath("claude", "a-valid")); !os.IsNotExist(err) {
				t.Fatal("valid item was installed before later metadata was rejected")
			}
		})
	}
}
