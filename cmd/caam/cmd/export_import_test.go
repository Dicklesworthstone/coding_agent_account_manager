package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/bundle"
	"github.com/spf13/cobra"
)

func bundleImportTestCommand(t *testing.T, mode string, dryRun, jsonOutput bool) (*cobra.Command, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{RunE: runBundleImport}
	c.Flags().String("mode", mode, "")
	c.Flags().String("password", "", "")
	c.Flags().Bool("dry-run", dryRun, "")
	c.Flags().Bool("json", jsonOutput, "")
	for _, flag := range []string{"force", "skip-config", "skip-projects", "skip-health", "skip-database", "skip-sync"} {
		c.Flags().Bool(flag, false, "")
	}
	c.Flags().StringSlice("provider", nil, "")
	c.Flags().StringSlice("profiles", nil, "")
	out := &bytes.Buffer{}
	c.SetOut(out)
	c.SetErr(out)
	return c, out
}

func TestBundleImportClaudeOwnershipReportsSkippedAndRecoveryProfiles(t *testing.T) {
	for _, test := range []struct {
		name, mode string
		json, dry  bool
	}{
		{name: "smart text", mode: "smart"},
		{name: "smart json", mode: "smart", json: true},
		{name: "replace text", mode: "replace"},
		{name: "replace json", mode: "replace", json: true},
		{name: "replace preview", mode: "replace", json: true, dry: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
			sourceVault := filepath.Join(root, "source")
			localProfile := filepath.Join(authfile.DefaultVaultPath(), "claude", "shared")
			credentials := make(map[string]string)
			for _, name := range []string{"personal", "team"} {
				hours := 12
				profile := localProfile
				if name == "team" {
					hours = 48
					profile = filepath.Join(sourceVault, "claude", "shared")
				}
				data := fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`, "SYNTHETIC-"+name+"-access", "SYNTHETIC-"+name+"-refresh", time.Now().Add(time.Duration(hours)*time.Hour).UnixMilli())
				credentials[name] = data
				binding := claudeCLIIdentityBinding(t, []byte(data), "org-"+name, name)
				metadata, err := json.Marshal(map[string]any{"claude_identity": binding})
				if err != nil {
					t.Fatal(err)
				}
				writeNativeTestCredential(t, filepath.Join(profile, ".credentials.json"), data)
				writeNativeTestCredential(t, filepath.Join(profile, "meta.json"), string(metadata))
			}
			exported, err := (&bundle.VaultExporter{VaultPath: sourceVault}).Export(&bundle.ExportOptions{OutputDir: filepath.Join(root, "output")})
			if err != nil {
				t.Fatal(err)
			}
			c, out := bundleImportTestCommand(t, test.mode, test.dry, test.json)
			if err := c.RunE(c, []string{exported.OutputPath}); err != nil {
				t.Fatalf("bundle import: %v; output=%s", err, out.String())
			}
			if strings.Contains(out.String(), "SYNTHETIC-") {
				t.Fatalf("bundle output disclosed token values: %s", out.String())
			}
			var result bundle.ImportResult
			if test.json {
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.ProfileActions) != 1 {
					t.Fatalf("invalid structured result: %s, %v", out.String(), err)
				}
			}
			if test.mode == "smart" {
				if test.json {
					if result.SkippedProfiles != 1 || !strings.Contains(result.ProfileActions[0].Reason, "organization differs") {
						t.Fatalf("missing organization conflict: %+v", result)
					}
				} else if !strings.Contains(out.String(), "claude/shared") || !strings.Contains(out.String(), "organization differs") {
					t.Fatalf("text output hid skipped ownership conflict: %s", out.String())
				}
			} else if !test.dry {
				if test.json {
					if result.UpdatedProfiles != 1 || result.ProfileActions[0].RecoveryPath == "" {
						t.Fatalf("missing recovery path: %+v", result)
					}
					requireSwitchCredential(t, filepath.Join(result.ProfileActions[0].RecoveryPath, ".credentials.json"), credentials["personal"])
				} else if !strings.Contains(out.String(), "Previous profile preserved:") {
					t.Fatalf("text output hid recovery path: %s", out.String())
				}
			} else if result.UpdatedProfiles != 1 || result.ProfileActions[0].RecoveryPath != "" {
				t.Fatalf("dry run result=%+v", result)
			}
			want := credentials["personal"]
			if test.mode == "replace" && !test.dry {
				want = credentials["team"]
			}
			requireSwitchCredential(t, filepath.Join(localProfile, ".credentials.json"), want)
		})
	}
}

func TestBundleImportInvalidBindingReturnsStructuredFailureWithoutSecrets(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CAAM_HOME", filepath.Join(root, "caam"))
	manifest := bundle.NewManifest()
	manifest.Source.Hostname = "synthetic-cli-test"
	manifest.AddProfile("claude", "work")
	files := map[string]string{
		"vault/claude/work/.credentials.json": `{"claudeAiOauth":{"accessToken":"SYNTHETIC-PRIVATE-ACCESS","refreshToken":"SYNTHETIC-PRIVATE-REFRESH","expiresAt":4102444800000}}`,
		"vault/claude/work/meta.json":         `{"claude_identity":{"secret":"SYNTHETIC-PRIVATE-ACCESS"}}`,
	}
	for name, data := range files {
		checksum, err := bundle.ComputeDataChecksum([]byte(data), bundle.AlgorithmSHA256)
		if err != nil {
			t.Fatal(err)
		}
		manifest.AddChecksum(name, checksum)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	files[bundle.ManifestFileName] = string(encoded)
	path := filepath.Join(root, "invalid-proof.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, data := range files {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	c, out := bundleImportTestCommand(t, "replace", false, true)
	err = c.RunE(c, []string{path})
	if err == nil {
		t.Fatal("invalid bound import returned success")
	}
	if strings.Contains(out.String(), "SYNTHETIC-PRIVATE") || strings.Contains(err.Error(), "SYNTHETIC-PRIVATE") {
		t.Fatalf("failure disclosed token values: %s, %v", out.String(), err)
	}
	var result bundle.ImportResult
	if decodeErr := json.Unmarshal(out.Bytes(), &result); decodeErr != nil || len(result.Errors) != 1 || len(result.ProfileActions) != 1 || result.ProfileActions[0].Action != "error" {
		t.Fatalf("partial failure was not reported as JSON: %s, %v", out.String(), decodeErr)
	}
	if _, statErr := os.Stat(filepath.Join(authfile.DefaultVaultPath(), "claude", "work")); !os.IsNotExist(statErr) {
		t.Fatalf("invalid bound import published profile: %v", statErr)
	}
}

func TestBundleImportChecksumFailureDoesNotClaimSuccess(t *testing.T) {
	root := t.TempDir()
	manifest := bundle.NewManifest()
	manifest.Source.Hostname = "synthetic-cli-checksum-test"
	manifest.AddProfile("claude", "work")
	credentialPath := "vault/claude/work/.credentials.json"
	manifest.AddChecksum(credentialPath, strings.Repeat("0", 64))
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "invalid-checksum.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, data := range map[string]string{
		bundle.ManifestFileName: string(encoded),
		credentialPath:          `{"claudeAiOauth":{"accessToken":"SYNTHETIC-CHECKSUM-ACCESS","refreshToken":"SYNTHETIC-CHECKSUM-REFRESH"}}`,
	} {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	for _, jsonOutput := range []bool{false, true} {
		t.Run(fmt.Sprintf("json=%t", jsonOutput), func(t *testing.T) {
			t.Setenv("CAAM_HOME", t.TempDir())
			c, out := bundleImportTestCommand(t, "replace", false, jsonOutput)
			err := c.RunE(c, []string{path})
			if err == nil || !strings.Contains(err.Error(), "checksum verification failed") {
				t.Fatalf("checksum failure returned success: %v", err)
			}
			if strings.Contains(strings.ToLower(out.String()), "import complete") || strings.Contains(out.String(), "SYNTHETIC-CHECKSUM") {
				t.Fatalf("failed import claimed success or leaked credentials: %s", out.String())
			}
			if jsonOutput {
				var result bundle.ImportResult
				if err := json.Unmarshal(out.Bytes(), &result); err != nil || len(result.Errors) != 1 || result.VerificationResult == nil || result.VerificationResult.Valid || result.NewProfiles != 0 {
					t.Fatalf("checksum failure missing from structured result: %s, %v", out.String(), err)
				}
			} else if !strings.Contains(out.String(), "Import Incomplete") || !strings.Contains(out.String(), "checksum verification failed") {
				t.Fatalf("checksum failure missing from text result: %s", out.String())
			}
			if _, err := os.Lstat(authfile.DefaultVaultPath()); !os.IsNotExist(err) {
				t.Fatalf("checksum failure changed the vault: %v", err)
			}
		})
	}
}

// =============================================================================
// export.go Command Tests
// =============================================================================

func TestExportCommand(t *testing.T) {
	if exportCmd.Use != "export [tool/profile] [tool profile]" {
		t.Errorf("Expected Use 'export [tool/profile] [tool profile]', got %q", exportCmd.Use)
	}

	if exportCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if exportCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestExportCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"all", "false"},
		{"output", ""},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := exportCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Expected flag --%s", tt.name)
				return
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("Expected default %q, got %q", tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestExportCommandArgs(t *testing.T) {
	// exportCmd accepts 0-2 args
	err := exportCmd.Args(nil, []string{})
	if err != nil {
		t.Errorf("Expected no error for 0 args, got %v", err)
	}

	err = exportCmd.Args(nil, []string{"codex/work"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = exportCmd.Args(nil, []string{"codex", "work"})
	if err != nil {
		t.Errorf("Expected no error for 2 args, got %v", err)
	}

	err = exportCmd.Args(nil, []string{"codex", "work", "extra"})
	if err == nil {
		t.Error("Expected error for 3 args")
	}
}

// =============================================================================
// import.go Command Tests
// =============================================================================

func TestImportCommand(t *testing.T) {
	if importCmd.Use != "import <archive.tar.gz|->" {
		t.Errorf("Expected Use 'import <archive.tar.gz|->', got %q", importCmd.Use)
	}

	if importCmd.Short == "" {
		t.Error("Expected non-empty Short description")
	}

	if importCmd.Long == "" {
		t.Error("Expected non-empty Long description")
	}
}

func TestImportCommandFlags(t *testing.T) {
	flags := []struct {
		name     string
		defValue string
	}{
		{"as", ""},
		{"force", "false"},
	}

	for _, tt := range flags {
		t.Run(tt.name, func(t *testing.T) {
			flag := importCmd.Flags().Lookup(tt.name)
			if flag == nil {
				t.Errorf("Expected flag --%s", tt.name)
				return
			}
			if flag.DefValue != tt.defValue {
				t.Errorf("Expected default %q, got %q", tt.defValue, flag.DefValue)
			}
		})
	}
}

func TestImportCommandArgs(t *testing.T) {
	// importCmd requires exactly 1 arg
	err := importCmd.Args(nil, []string{})
	if err == nil {
		t.Error("Expected error for 0 args")
	}

	err = importCmd.Args(nil, []string{"archive.tar.gz"})
	if err != nil {
		t.Errorf("Expected no error for 1 arg, got %v", err)
	}

	err = importCmd.Args(nil, []string{"archive.tar.gz", "extra"})
	if err == nil {
		t.Error("Expected error for 2 args")
	}
}

// =============================================================================
// parseToolProfileArg Tests
// =============================================================================

func TestParseToolProfileArg(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		wantTool    string
		wantProfile string
		wantErr     bool
	}{
		{
			name:        "valid codex/work",
			input:       "codex/work",
			wantTool:    "codex",
			wantProfile: "work",
			wantErr:     false,
		},
		{
			name:        "valid claude/personal",
			input:       "claude/personal",
			wantTool:    "claude",
			wantProfile: "personal",
			wantErr:     false,
		},
		{
			name:        "uppercase tool normalized",
			input:       "CODEX/Work",
			wantTool:    "codex",
			wantProfile: "Work",
			wantErr:     false,
		},
		{
			name:        "with whitespace",
			input:       "  codex / work  ",
			wantTool:    "codex",
			wantProfile: "work",
			wantErr:     false,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
		{
			name:    "no separator",
			input:   "codexwork",
			wantErr: true,
		},
		{
			name:    "empty tool",
			input:   "/work",
			wantErr: true,
		},
		{
			name:    "empty profile",
			input:   "codex/",
			wantErr: true,
		},
		{
			name:    "only separator",
			input:   "/",
			wantErr: true,
		},
		{
			name:    "multiple separators",
			input:   "codex/work/extra",
			wantErr: true,
		},
		{
			name:    "whitespace only",
			input:   "   ",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, profile, err := parseToolProfileArg(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseToolProfileArg(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if tool != tt.wantTool {
					t.Errorf("parseToolProfileArg(%q) tool = %q, want %q", tt.input, tool, tt.wantTool)
				}
				if profile != tt.wantProfile {
					t.Errorf("parseToolProfileArg(%q) profile = %q, want %q", tt.input, profile, tt.wantProfile)
				}
			}
		})
	}
}
