package cmd

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
)

func tarTestSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink creation is unavailable: %v", err)
		}
		t.Fatal(err)
	}
}

func TestTarImportRejectsAmbiguousOrCorruptManifestBeforeChangingVault(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*vaultExportManifest)
	}{
		{"file-owned-by-another-profile", func(m *vaultExportManifest) { m.Items[0].Files[0].Path = "vault/codex/other/auth.json" }},
		{"duplicate-file", func(m *vaultExportManifest) { m.Items[0].Files = append(m.Items[0].Files, m.Items[0].Files[0]) }},
		{"duplicate-profile", func(m *vaultExportManifest) { m.Items = append(m.Items, m.Items[0]) }},
		{"case-alias-profile", func(m *vaultExportManifest) {
			item := vaultExportItem{Tool: "codex", Profile: "WORK", Files: []vaultExportFile{m.Items[0].Files[0]}}
			item.Files[0].Path = "vault/codex/WORK/auth.json"
			m.Items = append(m.Items, item)
		}},
		{"traversal", func(m *vaultExportManifest) { m.Items[0].Files[0].Path = "vault/codex/work/../auth.json" }},
		{"empty-component", func(m *vaultExportManifest) { m.Items[0].Files[0].Path = "vault/codex/work//auth.json" }},
		{"windows-device", func(m *vaultExportManifest) { m.Items[0].Files[0].Path = "vault/codex/work/CON.json" }},
		{"windows-trailing-dot", func(m *vaultExportManifest) { m.Items[0].Files[0].Path = "vault/codex/work/auth.json." }},
		{"system-profile", func(m *vaultExportManifest) { m.Items[0].Profile = "_original" }},
		{"private-recovery", func(m *vaultExportManifest) { m.Items[0].Profile = "_caam-vault-backup~old" }},
		{"bad-checksum-format", func(m *vaultExportManifest) { m.Items[0].Files[0].SHA256 = "not-a-sha256" }},
		{"wrong-checksum", func(m *vaultExportManifest) { m.Items[0].Files[0].SHA256 = strings.Repeat("0", 64) }},
		{"wrong-size", func(m *vaultExportManifest) { m.Items[0].Files[0].Size++ }},
		{"oversized", func(m *vaultExportManifest) { m.Items[0].Files[0].Size = maxFileBytes + 1 }},
		{"file-directory-collision", func(m *vaultExportManifest) {
			file := m.Items[0].Files[0]
			file.Path += "/child.json"
			m.Items[0].Files = append(m.Items[0].Files, file)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			old := map[string]string{"auth.json": `{"access_token":"SYNTHETIC-old"}`, "meta.json": `{"description":"keep"}`}
			tarWriteProfile(t, v.ProfilePath("codex", "work"), old)
			before, err := vaulttransfer.Capture(v.BasePath())
			if err != nil {
				t.Fatal(err)
			}
			archive := tarRawFixture(t, map[string]map[string]string{"codex/work": {"auth.json": `{"access_token":"SYNTHETIC-new"}`}}, test.mutate)
			if _, err := importArchive(bytes.NewReader(archive), v, importOptions{Force: true}); err == nil {
				t.Fatal("malformed archive was accepted")
			}
			if err := before.Check(); err != nil {
				t.Fatalf("archive validation failure changed the vault: %v", err)
			}
		})
	}
}

func tarAppendEntry(t *testing.T, original []byte, header *tar.Header, contents string) []byte {
	t.Helper()
	files := tarReadPayloads(t, original)
	var buffer bytes.Buffer
	compressed := gzip.NewWriter(&buffer)
	archive := tar.NewWriter(compressed)
	if err := writeTarBytes(archive, exportManifestName, 0600, time.Time{}, files[exportManifestName]); err != nil {
		t.Fatal(err)
	}
	var names []string
	for name := range files {
		if name != exportManifestName {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if err := writeTarBytes(archive, name, 0600, time.Time{}, files[name]); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.WriteHeader(header); err != nil {
		t.Fatal(err)
	}
	if _, err := archive.Write([]byte(contents)); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressed.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func TestTarImportRejectsDuplicatePayloadLinksAndIncompleteCompression(t *testing.T) {
	const credential = `{"access_token":"SYNTHETIC-transfer"}`
	for _, invalid := range []string{"duplicate", "symlink", "unexpected", "truncated", "gzip-checksum"} {
		t.Run(invalid, func(t *testing.T) {
			v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			archive := tarRawFixture(t, map[string]map[string]string{"codex/work": {"auth.json": credential}}, nil)
			switch invalid {
			case "duplicate":
				archive = tarAppendEntry(t, archive, &tar.Header{Name: "vault/codex/work/auth.json", Mode: 0600, Size: int64(len(credential))}, credential)
			case "symlink":
				archive = tarAppendEntry(t, archive, &tar.Header{Name: "vault/codex/work/linked.json", Typeflag: tar.TypeSymlink, Linkname: "../../outside"}, "")
			case "unexpected":
				archive = tarAppendEntry(t, archive, &tar.Header{Name: "vault/codex/work/undeclared.json", Mode: 0600, Size: int64(len(credential))}, credential)
			case "truncated":
				archive = archive[:len(archive)-8]
			case "gzip-checksum":
				archive[len(archive)-8] ^= 1
			}
			if _, err := importArchive(bytes.NewReader(archive), v, importOptions{}); err == nil {
				t.Fatal("invalid archive was accepted")
			}
			if _, err := os.Stat(v.BasePath()); !os.IsNotExist(err) {
				t.Fatal("invalid archive reached destination mutation")
			}
		})
	}
}

func TestTarTransferRejectsEscapingProfilesAndSymlinkedVaultPaths(t *testing.T) {
	for _, unsafe := range []string{"profile-traversal", "provider-symlink", "root-symlink", "profile-symlink"} {
		t.Run(unsafe, func(t *testing.T) {
			root := t.TempDir()
			v := authfile.NewVault(filepath.Join(root, "vault"))
			outside := filepath.Join(root, "outside")
			tarWriteProfile(t, outside, map[string]string{"auth.json": `{"access_token":"SYNTHETIC-outside"}`})
			request := exportRequest{Tool: "codex", Profile: "work"}
			switch unsafe {
			case "profile-traversal":
				request.Profile = "../../outside"
			case "provider-symlink":
				if err := os.MkdirAll(v.BasePath(), 0700); err != nil {
					t.Fatal(err)
				}
				tarTestSymlink(t, outside, filepath.Join(v.BasePath(), "codex"))
			case "root-symlink":
				tarTestSymlink(t, outside, v.BasePath())
			case "profile-symlink":
				if err := os.MkdirAll(filepath.Join(v.BasePath(), "codex"), 0700); err != nil {
					t.Fatal(err)
				}
				tarTestSymlink(t, outside, v.ProfilePath("codex", "work"))
			}
			before, err := vaulttransfer.Capture(outside)
			if err != nil {
				t.Fatal(err)
			}
			if prepared, err := prepareExportArchive(v, request, exportOptions{}); err == nil {
				prepared.Close()
				t.Fatal("unsafe export path was accepted")
			}
			if unsafe != "profile-traversal" {
				archive := tarRawFixture(t, map[string]map[string]string{"codex/work": {"auth.json": `{"access_token":"SYNTHETIC-new"}`}}, nil)
				if _, err := importArchive(bytes.NewReader(archive), v, importOptions{Force: true}); err == nil {
					t.Fatal("symlinked import destination was accepted")
				}
			}
			if err := before.Check(); err != nil {
				t.Fatalf("refused transfer modified the external directory: %v", err)
			}
		})
	}
}

func TestTarImportCannotChangeProviderOrSelectRecoveryName(t *testing.T) {
	archive := tarRawFixture(t, map[string]map[string]string{"codex/work": {"auth.json": `{"access_token":"SYNTHETIC-source"}`}}, nil)
	for _, opt := range []importOptions{{AsTool: "claude", AsProfile: "work"}, {AsTool: "codex", AsProfile: "_original"}, {AsTool: "codex", AsProfile: "../outside"}} {
		v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
		if _, err := importArchive(bytes.NewReader(archive), v, opt); err == nil {
			t.Fatal("unsafe --as mapping was accepted")
		}
		if _, err := os.Stat(v.BasePath()); !os.IsNotExist(err) {
			t.Fatal("invalid --as mapping reached destination mutation")
		}
	}
}

func TestTarExportRejectsPortablePathAliases(t *testing.T) {
	for _, alias := range []string{"profile-case", "directory-case"} {
		t.Run(alias, func(t *testing.T) {
			v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			profile := v.ProfilePath("codex", "work")
			tarWriteProfile(t, profile, map[string]string{"auth.json": `{"access_token":"SYNTHETIC-primary"}`})
			if alias == "profile-case" {
				other := v.ProfilePath("codex", "WORK")
				tarWriteProfile(t, other, map[string]string{"auth.json": `{"access_token":"SYNTHETIC-secondary"}`})
				one, err := os.Stat(profile)
				if err != nil {
					t.Fatal(err)
				}
				two, err := os.Stat(other)
				if err != nil {
					t.Fatal(err)
				}
				if os.SameFile(one, two) {
					t.Skip("filesystem does not support distinct case-alias profiles")
				}
			} else {
				tarWriteProfile(t, profile, map[string]string{"Policy/a.json": `{}`, "policy/b.json": `{}`})
				one, err := os.Stat(filepath.Join(profile, "Policy"))
				if err != nil {
					t.Fatal(err)
				}
				two, err := os.Stat(filepath.Join(profile, "policy"))
				if err != nil {
					t.Fatal(err)
				}
				if os.SameFile(one, two) {
					t.Skip("filesystem does not support distinct case-alias directories")
				}
			}
			prepared, err := prepareExportArchive(v, exportRequest{ToolAll: true, Tool: "codex"}, exportOptions{})
			if err == nil {
				prepared.Close()
				t.Fatal("export produced an archive that cannot be imported portably")
			}
		})
	}
}

func TestTarExportRejectsOversizedCapturedFilesBeforePreparingProfiles(t *testing.T) {
	v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	profile := v.ProfilePath("claude", "work")
	tarWriteProfile(t, profile, tarClaudeTestFiles(t, "team", "Team"))
	file, err := os.OpenFile(filepath.Join(profile, ".claude.json"), os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maxFileBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	prepared, err := prepareExportArchive(v, exportRequest{Tool: "claude", Profile: "work"}, exportOptions{})
	if err == nil {
		prepared.Close()
		t.Fatal("oversized settings were exported")
	}
	if !strings.Contains(err.Error(), "unusually large file") {
		t.Fatalf("settings were parsed before the captured size bound was enforced: %v", err)
	}
}
