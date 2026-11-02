package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

func TestExportImport_RoundTripSingleProfile(t *testing.T) {
	tmpDir := t.TempDir()

	srcVault := authfile.NewVault(filepath.Join(tmpDir, "src-vault"))
	profileDir := srcVault.ProfilePath("codex", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	wantAuth := `{"access_token":"test-token"}`
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(wantAuth), 0600); err != nil {
		t.Fatalf("WriteFile(auth.json) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "meta.json"), []byte(`{"tool":"codex","profile":"work"}`), 0600); err != nil {
		t.Fatalf("WriteFile(meta.json) error = %v", err)
	}

	archive := tarExportTestBytes(t, srcVault, exportRequest{Tool: "codex", Profile: "work"})

	dstVault := authfile.NewVault(filepath.Join(tmpDir, "dst-vault"))
	if _, err := importArchive(bytes.NewReader(archive), dstVault, importOptions{}); err != nil {
		t.Fatalf("importArchive() error = %v", err)
	}

	gotAuth, err := os.ReadFile(filepath.Join(dstVault.ProfilePath("codex", "work"), "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile(dest auth.json) error = %v", err)
	}
	if string(gotAuth) != wantAuth {
		t.Fatalf("auth.json = %q, want %q", string(gotAuth), wantAuth)
	}
}

func TestImport_WithAsRenamesProfile(t *testing.T) {
	tmpDir := t.TempDir()

	srcVault := authfile.NewVault(filepath.Join(tmpDir, "src-vault"))
	profileDir := srcVault.ProfilePath("codex", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"access_token":"test-token"}`), 0600); err != nil {
		t.Fatalf("WriteFile(auth.json) error = %v", err)
	}

	archive := tarExportTestBytes(t, srcVault, exportRequest{Tool: "codex", Profile: "work"})

	dstVault := authfile.NewVault(filepath.Join(tmpDir, "dst-vault"))
	if _, err := importArchive(bytes.NewReader(archive), dstVault, importOptions{AsTool: "codex", AsProfile: "server-work"}); err != nil {
		t.Fatalf("importArchive() error = %v", err)
	}

	if _, err := os.Stat(dstVault.ProfilePath("codex", "work")); !os.IsNotExist(err) {
		t.Fatalf("expected original profile to not exist, stat error = %v", err)
	}
	if _, err := os.Stat(dstVault.ProfilePath("codex", "server-work")); err != nil {
		t.Fatalf("expected renamed profile to exist, stat error = %v", err)
	}
}

func TestImport_ConflictRequiresForce(t *testing.T) {
	tmpDir := t.TempDir()

	srcVault := authfile.NewVault(filepath.Join(tmpDir, "src-vault"))
	profileDir := srcVault.ProfilePath("codex", "work")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"access_token":"new-token"}`), 0600); err != nil {
		t.Fatalf("WriteFile(auth.json) error = %v", err)
	}

	archive := tarExportTestBytes(t, srcVault, exportRequest{Tool: "codex", Profile: "work"})

	dstVault := authfile.NewVault(filepath.Join(tmpDir, "dst-vault"))
	existingDir := dstVault.ProfilePath("codex", "work")
	if err := os.MkdirAll(existingDir, 0700); err != nil {
		t.Fatalf("MkdirAll(existing) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(existingDir, "auth.json"), []byte(`{"access_token":"old-token"}`), 0600); err != nil {
		t.Fatalf("WriteFile(existing auth.json) error = %v", err)
	}

	if _, err := importArchive(bytes.NewReader(archive), dstVault, importOptions{}); err == nil {
		t.Fatalf("expected conflict error without --force")
	}
	result, err := importArchive(bytes.NewReader(archive), dstVault, importOptions{Force: true})
	if err != nil {
		t.Fatalf("importArchive(force) error = %v", err)
	}
	if len(result.Profiles) != 1 || result.Profiles[0].RecoveryPath == "" {
		t.Fatal("forced replacement did not retain the previous profile")
	}
	requireSwitchCredential(t, filepath.Join(result.Profiles[0].RecoveryPath, "auth.json"), `{"access_token":"old-token"}`)

	gotAuth, err := os.ReadFile(filepath.Join(dstVault.ProfilePath("codex", "work"), "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile(dest auth.json) error = %v", err)
	}
	if string(gotAuth) != `{"access_token":"new-token"}` {
		t.Fatalf("auth.json = %q, want %q", string(gotAuth), `{"access_token":"new-token"}`)
	}
}

func TestExportAllForTool_ImportsAllProfiles(t *testing.T) {
	tmpDir := t.TempDir()

	srcVault := authfile.NewVault(filepath.Join(tmpDir, "src-vault"))
	for _, profile := range []string{"work", "personal"} {
		profileDir := srcVault.ProfilePath("codex", profile)
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, "auth.json"), []byte(`{"p":"`+profile+`"}`), 0600); err != nil {
			t.Fatalf("WriteFile(auth.json) error = %v", err)
		}
	}

	targets, err := resolveExportTargets(srcVault, exportRequest{ToolAll: true, Tool: "codex"})
	if err != nil {
		t.Fatalf("resolveExportTargets() error = %v", err)
	}
	if len(targets) != 2 {
		t.Fatalf("targets = %d, want 2", len(targets))
	}

	archive := tarExportTestBytes(t, srcVault, exportRequest{ToolAll: true, Tool: "codex"})

	dstVault := authfile.NewVault(filepath.Join(tmpDir, "dst-vault"))
	if _, err := importArchive(bytes.NewReader(archive), dstVault, importOptions{}); err != nil {
		t.Fatalf("importArchive() error = %v", err)
	}

	for _, profile := range []string{"work", "personal"} {
		gotAuth, err := os.ReadFile(filepath.Join(dstVault.ProfilePath("codex", profile), "auth.json"))
		if err != nil {
			t.Fatalf("ReadFile(%s) error = %v", profile, err)
		}
		if string(gotAuth) != `{"p":"`+profile+`"}` {
			t.Fatalf("%s auth.json = %q, want %q", profile, string(gotAuth), `{"p":"`+profile+`"}`)
		}
	}
}

func tarExportTestBytes(t *testing.T, v *authfile.Vault, request exportRequest) []byte {
	t.Helper()
	prepared, err := prepareExportArchive(v, request, exportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	data, err := os.ReadFile(prepared.Path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
