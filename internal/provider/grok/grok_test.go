package grok

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// NOTE: All fixtures here are SYNTHETIC. No test reads, writes, or copies the
// real ~/.grok credentials.

const fakeAuthJSON = `{"https://auth.x.ai::00000000-0000-0000-0000-000000000000":{"key":"SYNTHETIC-NOT-REAL","auth_mode":"sso","email":"grok-tester@example.com","user_id":"synthetic-user-1"}}`

func testProfile(t *testing.T) *profile.Profile {
	t.Helper()
	return &profile.Profile{
		Name:     "test",
		Provider: "grok",
		BasePath: t.TempDir(),
	}
}

func TestNew(t *testing.T) {
	if New() == nil {
		t.Fatal("New() returned nil")
	}
}

func TestProviderID(t *testing.T) {
	if got := New().ID(); got != "grok" {
		t.Errorf("ID() = %q, want grok", got)
	}
}

func TestProviderDisplayName(t *testing.T) {
	if got := New().DisplayName(); got == "" {
		t.Error("DisplayName() should not be empty")
	}
}

func TestProviderDefaultBin(t *testing.T) {
	if got := New().DefaultBin(); got != "grok" {
		t.Errorf("DefaultBin() = %q, want grok", got)
	}
}

func TestSupportedAuthModes(t *testing.T) {
	modes := New().SupportedAuthModes()
	if len(modes) != 1 || modes[0] != provider.AuthModeOAuth {
		t.Errorf("SupportedAuthModes() = %v, want [oauth]", modes)
	}
}

func TestAuthFiles_HonorsGrokHome(t *testing.T) {
	t.Setenv("GROK_HOME", "/g/.grok")
	files := New().AuthFiles()
	if len(files) != 2 {
		t.Fatalf("len(AuthFiles) = %d, want 2", len(files))
	}
	if files[0].Path != "/g/.grok/auth.json" || !files[0].Required {
		t.Errorf("first file = %+v, want required /g/.grok/auth.json", files[0])
	}
	if files[1].Path != "/g/.grok/config.toml" || files[1].Required {
		t.Errorf("second file = %+v, want optional /g/.grok/config.toml", files[1])
	}
}

func TestPrepareProfileAndEnv(t *testing.T) {
	p := New()
	prof := testProfile(t)

	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile: %v", err)
	}
	if _, err := os.Stat(filepath.Join(prof.HomePath(), ".grok")); err != nil {
		t.Errorf("profile grok home not created: %v", err)
	}

	env, err := p.Env(context.Background(), prof)
	if err != nil {
		t.Fatalf("Env: %v", err)
	}
	if env["HOME"] != prof.HomePath() {
		t.Errorf("HOME = %q, want %q", env["HOME"], prof.HomePath())
	}
	if env["GROK_HOME"] != filepath.Join(prof.HomePath(), ".grok") {
		t.Errorf("GROK_HOME = %q, want %q", env["GROK_HOME"], filepath.Join(prof.HomePath(), ".grok"))
	}
}

func TestStatus_LoggedInWithIdentity(t *testing.T) {
	p := New()
	prof := testProfile(t)

	status, err := p.Status(context.Background(), prof)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status.LoggedIn {
		t.Error("expected LoggedIn=false with no auth.json")
	}

	grokDir := filepath.Join(prof.HomePath(), ".grok")
	if err := os.MkdirAll(grokDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grokDir, "auth.json"), []byte(fakeAuthJSON), 0600); err != nil {
		t.Fatal(err)
	}

	status, err = p.Status(context.Background(), prof)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !status.LoggedIn {
		t.Error("expected LoggedIn=true with auth.json present")
	}
	if status.AccountID != "grok-tester@example.com" {
		t.Errorf("AccountID = %q, want grok-tester@example.com", status.AccountID)
	}
}

func TestLogout(t *testing.T) {
	p := New()
	prof := testProfile(t)

	grokDir := filepath.Join(prof.HomePath(), ".grok")
	if err := os.MkdirAll(grokDir, 0700); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(grokDir, "auth.json")
	if err := os.WriteFile(authPath, []byte(fakeAuthJSON), 0600); err != nil {
		t.Fatal(err)
	}

	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatalf("Logout: %v", err)
	}
	if _, err := os.Stat(authPath); !os.IsNotExist(err) {
		t.Error("auth.json should be removed after Logout")
	}

	// Logout is idempotent.
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Errorf("second Logout should not error: %v", err)
	}
}

func TestDetectExistingAuth(t *testing.T) {
	grokHome := filepath.Join(t.TempDir(), ".grok")
	t.Setenv("GROK_HOME", grokHome)
	p := New()

	// Missing directory: not found, no error.
	det, err := p.DetectExistingAuth()
	if err != nil {
		t.Fatalf("DetectExistingAuth: %v", err)
	}
	if det.Found {
		t.Error("expected Found=false with no auth.json")
	}

	// Valid auth.json: found.
	if err := os.MkdirAll(grokHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grokHome, "auth.json"), []byte(fakeAuthJSON), 0600); err != nil {
		t.Fatal(err)
	}
	det, err = p.DetectExistingAuth()
	if err != nil {
		t.Fatalf("DetectExistingAuth: %v", err)
	}
	if !det.Found || det.Primary == nil {
		t.Fatalf("expected Found=true with valid auth.json, got %+v", det)
	}
	if !det.Primary.IsValid {
		t.Error("expected primary location to be valid JSON")
	}

	// Invalid JSON: present but not valid, not Found.
	if err := os.WriteFile(filepath.Join(grokHome, "auth.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	det, err = p.DetectExistingAuth()
	if err != nil {
		t.Fatalf("DetectExistingAuth: %v", err)
	}
	if det.Found {
		t.Error("expected Found=false for invalid JSON")
	}
}

func TestImportAuth(t *testing.T) {
	p := New()
	prof := testProfile(t)

	src := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(src, []byte(fakeAuthJSON), 0600); err != nil {
		t.Fatal(err)
	}

	copied, err := p.ImportAuth(context.Background(), src, prof)
	if err != nil {
		t.Fatalf("ImportAuth: %v", err)
	}
	if len(copied) != 1 {
		t.Fatalf("len(copied) = %d, want 1", len(copied))
	}
	want := filepath.Join(prof.HomePath(), ".grok", "auth.json")
	if copied[0] != want {
		t.Errorf("copied to %q, want %q", copied[0], want)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Errorf("imported auth.json is not valid JSON: %v", err)
	}
}

func TestValidateToken(t *testing.T) {
	p := New()
	prof := testProfile(t)

	res, err := p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.Valid {
		t.Error("expected invalid with no auth.json")
	}

	grokDir := filepath.Join(prof.HomePath(), ".grok")
	if err := os.MkdirAll(grokDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(grokDir, "auth.json"), []byte(fakeAuthJSON), 0600); err != nil {
		t.Fatal(err)
	}
	res, err = p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if !res.Valid {
		t.Errorf("expected valid with parseable auth.json, got error %q", res.Error)
	}

	if err := os.WriteFile(filepath.Join(grokDir, "auth.json"), []byte("not json"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err = p.ValidateToken(context.Background(), prof, true)
	if err != nil {
		t.Fatalf("ValidateToken: %v", err)
	}
	if res.Valid {
		t.Error("expected invalid for unparseable auth.json")
	}
}
