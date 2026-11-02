package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/vaulttransfer"
	"github.com/spf13/cobra"
)

func tarExportTestCommand(output string) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	command := &cobra.Command{RunE: runExport}
	command.Flags().Bool("all", false, "")
	command.Flags().String("output", output, "")
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	command.SetOut(stdout)
	command.SetErr(stderr)
	return command, stdout, stderr
}

func TestTarExportCommandPreservesExistingOutputOnValidationFailure(t *testing.T) {
	root := t.TempDir()
	previous := vault
	vault = authfile.NewVault(filepath.Join(root, "vault"))
	t.Cleanup(func() { vault = previous })
	files := tarClaudeTestFiles(t, "team", "Team")
	files[".credentials.json"] = tarClaudeTestFiles(t, "personal", "Personal")[".credentials.json"]
	tarWriteProfile(t, vault.ProfilePath("claude", "work"), files)
	output := filepath.Join(root, "existing.tar.gz")
	writeNativeTestCredential(t, output, "previous complete archive")
	command, stdout, stderr := tarExportTestCommand(output)
	if err := command.RunE(command, []string{"claude", "work"}); err == nil {
		t.Fatal("invalid credential binding was exported")
	}
	requireSwitchCredential(t, output, "previous complete archive")
	if stdout.Len() != 0 || strings.Contains(stderr.String(), "Exported") {
		t.Fatal("failed export emitted credentials or claimed success")
	}
}

func TestTarExportCannotOverwriteVaultCredentialsThroughOutput(t *testing.T) {
	root := t.TempDir()
	v := authfile.NewVault(filepath.Join(root, "vault"))
	profile := v.ProfilePath("claude", "work")
	files := tarClaudeTestFiles(t, "team", "Team")
	tarWriteProfile(t, profile, files)
	prepared, err := prepareExportArchive(v, exportRequest{Tool: "claude", Profile: "work"}, exportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	before, err := vaulttransfer.Capture(profile)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	outputs := []string{filepath.Join(profile, ".credentials.json"), filepath.Join(v.BasePath(), "archive.tar.gz")}
	if err := os.Symlink(profile, alias); err == nil {
		outputs = append(outputs, filepath.Join(alias, "meta.json"))
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	for _, output := range outputs {
		if err := writeExportFile(prepared, v.BasePath(), output); err == nil {
			t.Fatal("export output inside the vault was accepted")
		}
	}
	if err := before.Check(); err != nil {
		t.Fatalf("refused output changed saved credentials: %v", err)
	}
}

func TestTarExportOutputFailurePreservesExistingArchiveAndSuccessfulOutputIsPrivate(t *testing.T) {
	root := t.TempDir()
	v := authfile.NewVault(filepath.Join(root, "vault"))
	tarWriteProfile(t, v.ProfilePath("codex", "work"), map[string]string{"auth.json": `{"access_token":"SYNTHETIC-export"}`})
	prepared, err := prepareExportArchive(v, exportRequest{Tool: "codex", Profile: "work"}, exportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	output := filepath.Join(root, "existing.tar.gz")
	if err := os.WriteFile(output, []byte("previous complete archive"), 0644); err != nil {
		t.Fatal(err)
	}
	missing := &preparedExport{Path: filepath.Join(root, "missing-private-archive")}
	if err := writeExportFile(missing, v.BasePath(), output); err == nil {
		t.Fatal("missing prepared archive returned success")
	}
	requireSwitchCredential(t, output, "previous complete archive")
	if err := writeExportFile(prepared, v.BasePath(), output); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("published archive is not private: %v", err)
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	payloads := tarReadPayloads(t, data)
	if string(payloads["vault/codex/work/auth.json"]) != `{"access_token":"SYNTHETIC-export"}` {
		t.Fatal("successful archive does not contain the captured credential")
	}
	leftovers, err := filepath.Glob(filepath.Join(root, ".caam-export-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatal("archive publication left private temporary files behind")
	}
}

type tarFailingWriter struct{}

func (tarFailingWriter) Write([]byte) (int, error) { return 0, errors.New("injected output failure") }

func TestTarExportWriterFailureDoesNotClaimCompletion(t *testing.T) {
	previous := vault
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { vault = previous })
	tarWriteProfile(t, vault.ProfilePath("codex", "work"), map[string]string{"auth.json": `{"access_token":"SYNTHETIC-export"}`})
	command, _, stderr := tarExportTestCommand("")
	command.SetOut(tarFailingWriter{})
	if err := command.RunE(command, []string{"codex/work"}); err == nil {
		t.Fatal("failed archive writer returned success")
	}
	if strings.Contains(stderr.String(), "Exported") || strings.Contains(stderr.String(), "SYNTHETIC-") {
		t.Fatal("failed archive writer claimed completion or disclosed credentials")
	}
}

func TestTarImportCommandReportsRetainedRecoveryAndValidationFailures(t *testing.T) {
	previous := vault
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { vault = previous })
	old := tarClaudeTestFiles(t, "personal", "Personal")
	tarWriteProfile(t, vault.ProfilePath("claude", "work"), old)
	archive := tarRawFixture(t, map[string]map[string]string{"claude/work": tarClaudeTestFiles(t, "team", "Team")}, nil)
	command := &cobra.Command{RunE: runImport}
	command.Flags().String("as", "", "")
	command.Flags().Bool("force", true, "")
	command.SetIn(bytes.NewReader(archive))
	var stdout, stderr bytes.Buffer
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.RunE(command, []string{"-"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "Imported 1 profile") || !strings.HasPrefix(stderr.String(), "Previous profile preserved: ") {
		t.Fatalf("import did not report its result and recovery location: %s; %s", stdout.String(), stderr.String())
	}
	recovery := strings.TrimSpace(strings.TrimPrefix(stderr.String(), "Previous profile preserved: "))
	for name, data := range old {
		requireSwitchCredential(t, filepath.Join(recovery, name), data)
	}
	if strings.Contains(stdout.String()+stderr.String(), "SYNTHETIC-") {
		t.Fatal("import result disclosed credential bytes")
	}
	bad := tarRawFixture(t, map[string]map[string]string{"claude/work": tarClaudeTestFiles(t, "different", "Personal")}, func(m *vaultExportManifest) { m.Items[0].Files[0].SHA256 = strings.Repeat("0", 64) })
	stdout.Reset()
	stderr.Reset()
	command.SetIn(bytes.NewReader(bad))
	err := command.RunE(command, []string{"-"})
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("invalid import did not return an accurate failure: %v; %s; %s", err, stdout.String(), stderr.String())
	}
}

func TestTarBulkExportExcludesSystemAndPrivateRecoveryProfiles(t *testing.T) {
	v := authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	for _, profile := range []string{"work", "_original", "_backup_before_switch", "_caam-vault-backup~retained"} {
		tarWriteProfile(t, v.ProfilePath("codex", profile), map[string]string{"auth.json": `{"access_token":"SYNTHETIC-export"}`})
	}
	prepared, err := prepareExportArchive(v, exportRequest{ToolAll: true, Tool: "codex"}, exportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer prepared.Close()
	if len(prepared.Manifest.Items) != 1 || prepared.Manifest.Items[0].Profile != "work" {
		t.Fatal("bulk export included a system or private recovery profile")
	}
}
