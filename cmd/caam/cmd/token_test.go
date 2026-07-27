package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
)

const testClaudeToken = "sk-ant-oat01-0123456789abcdef0123456789abcdef"

// setupTokenTest isolates the package globals (vault, healthStore) and HOME in
// a temp dir. HOME isolation keeps the claude file-swap machinery away from
// the real user auth files.
func setupTokenTest(t *testing.T) string {
	t.Helper()
	tmpDir := t.TempDir()

	t.Setenv("HOME", tmpDir)
	t.Setenv("CAAM_HOME", filepath.Join(tmpDir, "caam"))

	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmpDir, "vault"))
	t.Cleanup(func() { vault = oldVault })

	oldHealth := healthStore
	healthStore = health.NewStorage(filepath.Join(tmpDir, "health.json"))
	t.Cleanup(func() { healthStore = oldHealth })

	return tmpDir
}

func resetTokenFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		_ = tokenAddCmd.Flags().Set("json", "false")
		_ = tokenAddCmd.Flags().Set("force", "false")
		_ = tokenAddCmd.Flags().Set("no-verify", "false")
		_ = tokenImportCmd.Flags().Set("dir", "")
		_ = tokenImportCmd.Flags().Set("force", "false")
		_ = tokenImportCmd.Flags().Set("json", "false")
		_ = tokenLsCmd.Flags().Set("json", "false")
		_ = tokenRmCmd.Flags().Set("json", "false")
	})
}

func TestTokenAdd_FromStdin(t *testing.T) {
	setupTokenTest(t)
	resetTokenFlags(t)

	var out bytes.Buffer
	tokenAddCmd.SetIn(strings.NewReader(testClaudeToken + "\n"))
	tokenAddCmd.SetOut(&out)

	if err := runTokenAdd(tokenAddCmd, []string{"claude", "work"}); err != nil {
		t.Fatalf("runTokenAdd error: %v", err)
	}

	token, meta, err := vault.ReadTokenProfile("claude", "work")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != testClaudeToken {
		t.Errorf("stored token = %q, want input token", token)
	}
	if meta.Source != "stdin" {
		t.Errorf("meta.Source = %q, want stdin", meta.Source)
	}
}

func TestTokenAdd_RejectsMalformedToken(t *testing.T) {
	setupTokenTest(t)
	resetTokenFlags(t)

	tokenAddCmd.SetIn(strings.NewReader("not-a-claude-token\n"))
	tokenAddCmd.SetOut(&bytes.Buffer{})

	if err := runTokenAdd(tokenAddCmd, []string{"claude", "bad"}); err == nil {
		t.Fatal("expected error for malformed token")
	}
	if vault.IsTokenProfile("claude", "bad") {
		t.Error("malformed token was stored")
	}
}

func TestTokenAdd_RefusesOverwriteWithoutForce(t *testing.T) {
	setupTokenTest(t)
	resetTokenFlags(t)

	if err := vault.SaveTokenProfile("claude", "work", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	tokenAddCmd.SetIn(strings.NewReader(testClaudeToken))
	tokenAddCmd.SetOut(&bytes.Buffer{})
	if err := runTokenAdd(tokenAddCmd, []string{"claude", "work"}); err == nil {
		t.Fatal("expected already-exists error without --force")
	}

	_ = tokenAddCmd.Flags().Set("force", "true")
	tokenAddCmd.SetIn(strings.NewReader(testClaudeToken))
	if err := runTokenAdd(tokenAddCmd, []string{"claude", "work"}); err != nil {
		t.Fatalf("runTokenAdd with --force error: %v", err)
	}
}

func TestTokenImport_ScansVeupPattern(t *testing.T) {
	tmpDir := setupTokenTest(t)
	resetTokenFlags(t)

	importDir := filepath.Join(tmpDir, "veup")
	if err := os.MkdirAll(importDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(importDir, name), []byte(content), 0600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	writeFile("claude-burst-token", testClaudeToken+"\n")
	writeFile("claude-mcc22-token", "sk-ant-oat01-fedcba9876543210fedcba9876543210")
	writeFile("claude-accounts.zsh", "# not a token file")
	writeFile("unrelated.txt", "nope")

	_ = tokenImportCmd.Flags().Set("dir", importDir)
	var out bytes.Buffer
	tokenImportCmd.SetOut(&out)

	if err := runTokenImport(tokenImportCmd, nil); err != nil {
		t.Fatalf("runTokenImport error: %v", err)
	}

	for _, name := range []string{"burst", "mcc22"} {
		if !vault.IsTokenProfile("claude", name) {
			t.Errorf("expected imported token profile claude/%s", name)
		}
	}
	if vault.IsTokenProfile("claude", "accounts.zsh") {
		t.Error("non-token file was imported")
	}

	_, meta, err := vault.ReadTokenProfile("claude", "burst")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if want := filepath.Join(importDir, "claude-burst-token"); meta.Source != want {
		t.Errorf("meta.Source = %q, want %q", meta.Source, want)
	}
	if !strings.Contains(out.String(), "2 imported") {
		t.Errorf("output = %q, want '2 imported'", out.String())
	}

	// Second import without --force skips both.
	out.Reset()
	if err := runTokenImport(tokenImportCmd, nil); err != nil {
		t.Fatalf("second runTokenImport error: %v", err)
	}
	if !strings.Contains(out.String(), "0 imported") {
		t.Errorf("second import output = %q, want '0 imported'", out.String())
	}
	if !strings.Contains(out.String(), "Skipped claude/burst") {
		t.Errorf("second import output = %q, want skip notice", out.String())
	}
}

func TestTokenImport_JSONOutput(t *testing.T) {
	tmpDir := setupTokenTest(t)
	resetTokenFlags(t)

	importDir := filepath.Join(tmpDir, "veup")
	if err := os.MkdirAll(importDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(importDir, "claude-burst-token"), []byte(testClaudeToken), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_ = tokenImportCmd.Flags().Set("dir", importDir)
	_ = tokenImportCmd.Flags().Set("json", "true")
	var out bytes.Buffer
	tokenImportCmd.SetOut(&out)

	if err := runTokenImport(tokenImportCmd, nil); err != nil {
		t.Fatalf("runTokenImport error: %v", err)
	}

	var parsed struct {
		Imported int                 `json:"imported"`
		Results  []tokenImportResult `json:"results"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("parse JSON output: %v (raw: %s)", err, out.String())
	}
	if parsed.Imported != 1 || len(parsed.Results) != 1 {
		t.Errorf("parsed = %+v, want 1 imported", parsed)
	}
	if parsed.Results[0].Action != "imported" || parsed.Results[0].Name != "burst" {
		t.Errorf("result = %+v", parsed.Results[0])
	}
}

func TestTokenLsAndRm(t *testing.T) {
	setupTokenTest(t)
	resetTokenFlags(t)

	if err := vault.SaveTokenProfile("claude", "burst", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := vault.SetActiveTokenProfile("claude", "burst"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}

	_ = tokenLsCmd.Flags().Set("json", "true")
	var out bytes.Buffer
	tokenLsCmd.SetOut(&out)
	if err := runTokenLs(tokenLsCmd, nil); err != nil {
		t.Fatalf("runTokenLs error: %v", err)
	}
	var parsed struct {
		Profiles []tokenProfileJSON `json:"profiles"`
		Count    int                `json:"count"`
	}
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("parse JSON: %v (raw: %s)", err, out.String())
	}
	if parsed.Count != 1 || parsed.Profiles[0].Name != "burst" || !parsed.Profiles[0].Active {
		t.Errorf("ls output = %+v, want active burst", parsed)
	}
	if parsed.Profiles[0].Status != "ok" {
		t.Errorf("status = %q, want ok", parsed.Profiles[0].Status)
	}

	tokenRmCmd.SetOut(&bytes.Buffer{})
	if err := runTokenRm(tokenRmCmd, []string{"claude", "burst"}); err != nil {
		t.Fatalf("runTokenRm error: %v", err)
	}
	if vault.IsTokenProfile("claude", "burst") {
		t.Error("token profile still exists after rm")
	}
	if active, _ := vault.ActiveTokenProfile("claude"); active != "" {
		t.Errorf("active token profile = %q after rm, want cleared", active)
	}
}

func TestTokenRm_RefusesFileProfile(t *testing.T) {
	setupTokenTest(t)
	resetTokenFlags(t)

	dir := vault.ProfilePath("claude", "files")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := runTokenRm(tokenRmCmd, []string{"claude", "files"}); err == nil {
		t.Fatal("expected error deleting file-swap profile via token rm")
	}
}

func TestActivate_TokenProfileSetsDefault(t *testing.T) {
	setupTokenTest(t)

	if err := vault.SaveTokenProfile("claude", "burst", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	if err := runActivate(activateCmd, []string{"claude", "burst"}); err != nil {
		t.Fatalf("runActivate error: %v", err)
	}

	active, err := vault.ActiveTokenProfile("claude")
	if err != nil {
		t.Fatalf("ActiveTokenProfile error: %v", err)
	}
	if active != "burst" {
		t.Errorf("ActiveTokenProfile = %q, want burst", active)
	}

	// Activation timestamp must be recorded (first-class health).
	ph, err := healthStore.GetProfile("claude", "burst")
	if err != nil || ph == nil {
		t.Fatalf("GetProfile error: %v (ph=%v)", err, ph)
	}
	if ph.LastActivatedAt.IsZero() {
		t.Error("LastActivatedAt not recorded for token profile activation")
	}
}

func TestActivate_FileProfileClearsTokenDefault(t *testing.T) {
	tmpDir := setupTokenTest(t)

	// Token default set.
	if err := vault.SaveTokenProfile("claude", "burst", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := vault.SetActiveTokenProfile("claude", "burst"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}

	// A file-swap profile in the vault; live auth files live in the isolated
	// fake HOME created by setupTokenTest.
	profileDir := vault.ProfilePath("claude", "files")
	if err := os.MkdirAll(profileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	creds := []byte(`{"claudeAiOauth":{"accessToken":"file-token"}}`)
	if err := os.WriteFile(filepath.Join(profileDir, ".credentials.json"), creds, 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(tmpDir, ".claude"), 0700); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}

	if err := runActivate(activateCmd, []string{"claude", "files"}); err != nil {
		t.Fatalf("runActivate error: %v", err)
	}

	if active, _ := vault.ActiveTokenProfile("claude"); active != "" {
		t.Errorf("token default = %q after file-profile activation, want cleared", active)
	}
}

func TestStatus_ShowsTokenDefault(t *testing.T) {
	setupTokenTest(t)

	if err := vault.SaveTokenProfile("claude", "burst", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := vault.SetActiveTokenProfile("claude", "burst"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}

	_ = statusCmd.Flags().Set("json", "true")
	t.Cleanup(func() { _ = statusCmd.Flags().Set("json", "false") })
	var out bytes.Buffer
	statusCmd.SetOut(&out)

	if err := runStatus(statusCmd, []string{"claude"}); err != nil {
		t.Fatalf("runStatus error: %v", err)
	}

	var parsed statusOutput
	if err := json.Unmarshal(out.Bytes(), &parsed); err != nil {
		t.Fatalf("parse JSON: %v (raw: %s)", err, out.String())
	}
	if len(parsed.Tools) != 1 {
		t.Fatalf("tools = %+v, want 1 entry", parsed.Tools)
	}
	st := parsed.Tools[0]
	if st.ActiveProfile != "burst" || st.ProfileType != authfile.ProfileTypeToken {
		t.Errorf("status tool = %+v, want active token profile burst", st)
	}
	if st.Health == nil || st.Health.Status != "healthy" {
		t.Errorf("health = %+v, want healthy", st.Health)
	}
}

func TestValidateTokenProfile_Passive(t *testing.T) {
	setupTokenTest(t)

	if err := vault.SaveTokenProfile("claude", "good", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	out := validateTokenProfile(context.Background(), "claude", "good", false)
	if !out.Valid || out.Method != "passive" {
		t.Errorf("validateTokenProfile = %+v, want valid/passive", out)
	}

	// Corrupt the stored token: passive check must fail.
	tokenPath := filepath.Join(vault.ProfilePath("claude", "good"), authfile.TokenFileName)
	if err := os.WriteFile(tokenPath, []byte("garbage"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	out = validateTokenProfile(context.Background(), "claude", "good", false)
	if out.Valid {
		t.Errorf("validateTokenProfile on malformed token = %+v, want invalid", out)
	}
}

func TestValidateTokenProfile_ActiveProbe(t *testing.T) {
	setupTokenTest(t)

	if err := vault.SaveTokenProfile("claude", "probe", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	tests := []struct {
		name       string
		status     int
		wantValid  bool
		wantMethod string
	}{
		{"accepted", http.StatusOK, true, "active"},
		{"rejected", http.StatusUnauthorized, false, "active"},
		{"inconclusive falls back to passive", http.StatusBadGateway, true, "passive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte("{}"))
			}))
			defer srv.Close()

			oldURL := claudeTokenProbeURL
			claudeTokenProbeURL = srv.URL
			defer func() { claudeTokenProbeURL = oldURL }()

			out := validateTokenProfile(context.Background(), "claude", "probe", true)
			if out.Valid != tt.wantValid {
				t.Errorf("Valid = %v, want %v (out=%+v)", out.Valid, tt.wantValid, out)
			}
			if out.Method != tt.wantMethod {
				t.Errorf("Method = %q, want %q", out.Method, tt.wantMethod)
			}
			if gotAuth != "Bearer "+testClaudeToken {
				t.Errorf("Authorization = %q, want bearer token", gotAuth)
			}
		})
	}
}

func TestScanTokenImportDir_MissingDir(t *testing.T) {
	candidates, err := scanTokenImportDir(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Fatalf("scanTokenImportDir error: %v", err)
	}
	if candidates != nil {
		t.Errorf("candidates = %v, want nil for missing dir", candidates)
	}
}

func TestRotateTokenProfileIfCoolingDown(t *testing.T) {
	tmpDir := setupTokenTest(t)

	db, err := caamdb.OpenAt(filepath.Join(tmpDir, "caam.db"))
	if err != nil {
		t.Fatalf("OpenAt error: %v", err)
	}
	defer db.Close()

	if err := vault.SaveTokenProfile("claude", "burst", testClaudeToken, ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := vault.SaveTokenProfile("claude", "mcc22", "sk-ant-oat01-fedcba9876543210fedcba9876543210", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := vault.SetActiveTokenProfile("claude", "burst"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}

	selector := rotation.NewSelector(rotation.AlgorithmSmart, healthStore, db)

	// No cooldown: stays on the default.
	if got := rotateTokenProfileIfCoolingDown("claude", "burst", db, selector, true); got != "burst" {
		t.Errorf("without cooldown, got %q, want burst", got)
	}

	// Cooldown on the default: rotates to the other token profile and
	// records it as the new default.
	if _, err := db.SetCooldown("claude", "burst", time.Now().UTC(), time.Hour, "test"); err != nil {
		t.Fatalf("SetCooldown error: %v", err)
	}
	got := rotateTokenProfileIfCoolingDown("claude", "burst", db, selector, true)
	if got != "mcc22" {
		t.Errorf("with cooldown, got %q, want mcc22", got)
	}
	if active, _ := vault.ActiveTokenProfile("claude"); active != "mcc22" {
		t.Errorf("default after rotation = %q, want mcc22", active)
	}

	// Everything cooling down: falls back to current rather than failing.
	if _, err := db.SetCooldown("claude", "mcc22", time.Now().UTC(), time.Hour, "test"); err != nil {
		t.Fatalf("SetCooldown error: %v", err)
	}
	if got := rotateTokenProfileIfCoolingDown("claude", "mcc22", db, selector, true); got != "mcc22" {
		t.Errorf("all-cooldown fallback = %q, want mcc22", got)
	}
}
