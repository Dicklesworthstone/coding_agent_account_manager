package agy

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
)

// NOTE: All fixtures here are SYNTHETIC. No test reads, writes, or copies the
// real ~/.gemini credentials. The synthetic token bytes below are not real
// tokens and never contain anything sensitive.

const (
	fakeTokenJSON = `{"auth_method":"oauth","token":"SYNTHETIC-NOT-A-REAL-TOKEN"}`
	fakeAccounts  = `{"active":"tester@example.com","old":["prev@example.com"]}`
	fakeCreds     = `{"access_token":"synthetic-access","refresh_token":"synthetic-refresh"}`
	fakeSettings  = `{"enableTelemetry":false,"model":"Gemini 3.1 Pro (High)"}`
)

// =============================================================================
// Provider Identity
// =============================================================================

func TestNew(t *testing.T) {
	if New() == nil {
		t.Fatal("New() returned nil")
	}
}

func TestProviderID(t *testing.T) {
	if got := New().ID(); got != "agy" {
		t.Errorf("ID() = %q, want %q", got, "agy")
	}
}

func TestProviderDisplayName(t *testing.T) {
	if got := New().DisplayName(); got == "" {
		t.Error("DisplayName() should not be empty")
	}
}

func TestProviderDefaultBin(t *testing.T) {
	if got := New().DefaultBin(); got != "agy" {
		t.Errorf("DefaultBin() = %q, want %q", got, "agy")
	}
}

func TestSupportedAuthModes(t *testing.T) {
	modes := New().SupportedAuthModes()
	if len(modes) != 1 || modes[0] != provider.AuthModeOAuth {
		t.Errorf("SupportedAuthModes() = %v, want [oauth]", modes)
	}
}

// =============================================================================
// AuthFiles
// =============================================================================

func TestAuthFiles(t *testing.T) {
	t.Run("token file is first and required", func(t *testing.T) {
		files := New().AuthFiles()
		if len(files) == 0 {
			t.Fatal("AuthFiles() returned no specs")
		}
		first := files[0]
		if filepath.Base(first.Path) != "antigravity-oauth-token" {
			t.Errorf("AuthFiles()[0] base = %q, want antigravity-oauth-token", filepath.Base(first.Path))
		}
		if !first.Required {
			t.Error("token file should be Required")
		}
	})

	t.Run("token lives under antigravity-cli", func(t *testing.T) {
		files := New().AuthFiles()
		want := filepath.Join("antigravity-cli", "antigravity-oauth-token")
		if filepath.Join(filepath.Base(filepath.Dir(files[0].Path)), filepath.Base(files[0].Path)) != want {
			t.Errorf("token path = %q, should end with %q", files[0].Path, want)
		}
	})

	t.Run("all basenames are unique (no vault collisions)", func(t *testing.T) {
		files := New().AuthFiles()
		seen := map[string]bool{}
		for _, f := range files {
			b := filepath.Base(f.Path)
			if seen[b] {
				t.Errorf("duplicate basename %q would collide in vault", b)
			}
			seen[b] = true
		}
	})

	t.Run("optional files are not required", func(t *testing.T) {
		files := New().AuthFiles()
		for _, f := range files[1:] {
			if f.Required {
				t.Errorf("file %q should be optional", filepath.Base(f.Path))
			}
		}
	})

	t.Run("respects GEMINI_HOME", func(t *testing.T) {
		t.Setenv("GEMINI_HOME", "/custom/gemini")
		files := New().AuthFiles()
		want := filepath.Join("/custom/gemini", "antigravity-cli", "antigravity-oauth-token")
		if files[0].Path != want {
			t.Errorf("AuthFiles()[0].Path = %q, want %q", files[0].Path, want)
		}
	})
}

// =============================================================================
// PrepareProfile / Env / ValidateProfile
// =============================================================================

func TestPrepareProfile(t *testing.T) {
	prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
	p := New()
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Fatalf("PrepareProfile() error = %v", err)
	}
	dir := filepath.Join(prof.HomePath(), ".gemini", "antigravity-cli")
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		t.Fatalf("antigravity-cli dir not created: %v", err)
	}
	// idempotent
	if err := p.PrepareProfile(context.Background(), prof); err != nil {
		t.Errorf("second PrepareProfile() error = %v", err)
	}
}

func TestEnv(t *testing.T) {
	prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
	env, err := New().Env(context.Background(), prof)
	if err != nil {
		t.Fatalf("Env() error = %v", err)
	}
	if env["HOME"] != prof.HomePath() {
		t.Errorf("HOME = %q, want %q", env["HOME"], prof.HomePath())
	}
	if len(env) != 1 {
		t.Errorf("Env() returned %d vars, want 1", len(env))
	}
}

func TestValidateProfile(t *testing.T) {
	t.Run("valid when prepared", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)
		if err := p.ValidateProfile(context.Background(), prof); err != nil {
			t.Errorf("ValidateProfile() error = %v", err)
		}
	})
	t.Run("invalid when home missing", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		if err := New().ValidateProfile(context.Background(), prof); err == nil {
			t.Error("ValidateProfile() should error when home missing")
		}
	})
}

// =============================================================================
// Status (auth-state detection)
// =============================================================================

func writeProfileToken(t *testing.T, prof *profile.Profile, content string) string {
	t.Helper()
	dir := filepath.Join(prof.HomePath(), ".gemini", "antigravity-cli")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "antigravity-oauth-token")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeProfileAccounts(t *testing.T, prof *profile.Profile, content string) {
	t.Helper()
	dir := filepath.Join(prof.HomePath(), ".gemini")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "google_accounts.json"), []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestStatus(t *testing.T) {
	t.Run("logged in when token present", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)
		writeProfileToken(t, prof, fakeTokenJSON)

		status, err := p.Status(context.Background(), prof)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if !status.LoggedIn {
			t.Error("LoggedIn should be true when token present")
		}
	})

	t.Run("not logged in when token missing", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, _ := p.Status(context.Background(), prof)
		if status.LoggedIn {
			t.Error("LoggedIn should be false when token missing")
		}
	})

	t.Run("reports active account email", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)
		writeProfileToken(t, prof, fakeTokenJSON)
		writeProfileAccounts(t, prof, fakeAccounts)

		status, _ := p.Status(context.Background(), prof)
		if status.AccountID != "tester@example.com" {
			t.Errorf("AccountID = %q, want tester@example.com", status.AccountID)
		}
	})

	t.Run("reports lock file status", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		status, _ := p.Status(context.Background(), prof)
		if status.HasLockFile {
			t.Error("HasLockFile should be false initially")
		}
		prof.Lock()
		defer prof.Unlock()
		status, _ = p.Status(context.Background(), prof)
		if !status.HasLockFile {
			t.Error("HasLockFile should be true when locked")
		}
	})
}

// =============================================================================
// ValidateToken
// =============================================================================

func TestValidateToken(t *testing.T) {
	t.Run("valid when token present and non-empty", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)
		writeProfileToken(t, prof, fakeTokenJSON)

		res, err := p.ValidateToken(context.Background(), prof, true)
		if err != nil {
			t.Fatalf("ValidateToken() error = %v", err)
		}
		if !res.Valid {
			t.Errorf("ValidateToken() should be valid, got error %q", res.Error)
		}
		if res.Method != "passive" {
			t.Errorf("Method = %q, want passive", res.Method)
		}
	})

	t.Run("invalid when token empty", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)
		writeProfileToken(t, prof, "")

		res, _ := p.ValidateToken(context.Background(), prof, true)
		if res.Valid {
			t.Error("ValidateToken() should be invalid for empty token")
		}
	})

	t.Run("invalid when token missing", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		res, _ := p.ValidateToken(context.Background(), prof, false)
		if res.Valid {
			t.Error("ValidateToken() should be invalid when token missing")
		}
	})
}

// =============================================================================
// Logout
// =============================================================================

func TestLogout(t *testing.T) {
	prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
	p := New()
	p.PrepareProfile(context.Background(), prof)
	tokenPath := writeProfileToken(t, prof, fakeTokenJSON)
	writeProfileAccounts(t, prof, fakeAccounts)

	if err := p.Logout(context.Background(), prof); err != nil {
		t.Fatalf("Logout() error = %v", err)
	}
	if _, err := os.Stat(tokenPath); !os.IsNotExist(err) {
		t.Error("token should be removed after Logout")
	}

	// Idempotent: logout on already-clean profile is fine.
	if err := p.Logout(context.Background(), prof); err != nil {
		t.Errorf("second Logout() error = %v", err)
	}
}

// =============================================================================
// DetectExistingAuth (account-identification parsing)
// =============================================================================

func TestDetectExistingAuth(t *testing.T) {
	setup := func(t *testing.T) string {
		home := t.TempDir()
		// Point GEMINI_HOME at an isolated dir so we NEVER touch real creds.
		gemHome := filepath.Join(home, ".gemini")
		t.Setenv("GEMINI_HOME", gemHome)
		if err := os.MkdirAll(filepath.Join(gemHome, "antigravity-cli"), 0700); err != nil {
			t.Fatal(err)
		}
		return gemHome
	}

	t.Run("detects token as primary", func(t *testing.T) {
		gemHome := setup(t)
		tokPath := filepath.Join(gemHome, "antigravity-cli", "antigravity-oauth-token")
		if err := os.WriteFile(tokPath, []byte(fakeTokenJSON), 0600); err != nil {
			t.Fatal(err)
		}

		det, err := New().DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}
		if !det.Found {
			t.Fatal("should have found auth")
		}
		if det.Primary == nil || det.Primary.Path != tokPath {
			t.Errorf("Primary = %v, want token path %q", det.Primary, tokPath)
		}
	})

	t.Run("parses active account from google_accounts.json", func(t *testing.T) {
		gemHome := setup(t)
		acctPath := filepath.Join(gemHome, "google_accounts.json")
		if err := os.WriteFile(acctPath, []byte(fakeAccounts), 0600); err != nil {
			t.Fatal(err)
		}

		det, err := New().DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}
		found := false
		for _, loc := range det.Locations {
			if filepath.Base(loc.Path) == "google_accounts.json" {
				found = true
				if !loc.Exists || !loc.IsValid {
					t.Errorf("google_accounts.json should be valid, err=%q", loc.ValidationError)
				}
			}
		}
		if !found {
			t.Error("google_accounts.json location not reported")
		}
	})

	t.Run("rejects accounts file with no active account", func(t *testing.T) {
		gemHome := setup(t)
		acctPath := filepath.Join(gemHome, "google_accounts.json")
		if err := os.WriteFile(acctPath, []byte(`{"old":[]}`), 0600); err != nil {
			t.Fatal(err)
		}
		det, _ := New().DetectExistingAuth()
		for _, loc := range det.Locations {
			if filepath.Base(loc.Path) == "google_accounts.json" && loc.IsValid {
				t.Error("accounts file without active account should be invalid")
			}
		}
	})

	t.Run("not found when nothing present", func(t *testing.T) {
		setup(t)
		det, err := New().DetectExistingAuth()
		if err != nil {
			t.Fatalf("DetectExistingAuth() error = %v", err)
		}
		if det.Found {
			t.Error("should not find auth in empty home")
		}
	})
}

func TestActiveAccountEmail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "google_accounts.json")
	if err := os.WriteFile(path, []byte(fakeAccounts), 0600); err != nil {
		t.Fatal(err)
	}
	if got := activeAccountEmail(path); got != "tester@example.com" {
		t.Errorf("activeAccountEmail() = %q, want tester@example.com", got)
	}
	if got := activeAccountEmail(filepath.Join(dir, "missing.json")); got != "" {
		t.Errorf("activeAccountEmail(missing) = %q, want empty", got)
	}
}

// =============================================================================
// ImportAuth (round-trip: import preserves bytes exactly)
// =============================================================================

func TestImportAuth(t *testing.T) {
	hashBytes := func(b []byte) string {
		h := sha256.Sum256(b)
		return hex.EncodeToString(h[:])
	}

	t.Run("imports token to antigravity-cli dir, byte-identical", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		srcDir := filepath.Join(t.TempDir(), ".gemini", "antigravity-cli")
		os.MkdirAll(srcDir, 0700)
		srcPath := filepath.Join(srcDir, "antigravity-oauth-token")
		if err := os.WriteFile(srcPath, []byte(fakeTokenJSON), 0600); err != nil {
			t.Fatal(err)
		}

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth() error = %v", err)
		}
		want := filepath.Join(prof.HomePath(), ".gemini", "antigravity-cli", "antigravity-oauth-token")
		if copied[0] != want {
			t.Errorf("copied to %q, want %q", copied[0], want)
		}
		got, _ := os.ReadFile(want)
		if hashBytes(got) != hashBytes([]byte(fakeTokenJSON)) {
			t.Error("imported token is not byte-identical")
		}
		// permissions enforced to 0600
		info, _ := os.Stat(want)
		if info.Mode().Perm() != 0600 {
			t.Errorf("imported token perms = %o, want 0600", info.Mode().Perm())
		}
	})

	t.Run("imports google_accounts.json to .gemini dir", func(t *testing.T) {
		prof := &profile.Profile{Name: "test", Provider: "agy", BasePath: t.TempDir()}
		p := New()
		p.PrepareProfile(context.Background(), prof)

		srcDir := filepath.Join(t.TempDir(), ".gemini")
		os.MkdirAll(srcDir, 0700)
		srcPath := filepath.Join(srcDir, "google_accounts.json")
		if err := os.WriteFile(srcPath, []byte(fakeAccounts), 0600); err != nil {
			t.Fatal(err)
		}

		copied, err := p.ImportAuth(context.Background(), srcPath, prof)
		if err != nil {
			t.Fatalf("ImportAuth() error = %v", err)
		}
		want := filepath.Join(prof.HomePath(), ".gemini", "google_accounts.json")
		if copied[0] != want {
			t.Errorf("copied to %q, want %q", copied[0], want)
		}
	})
}

// =============================================================================
// Path helpers honor GEMINI_HOME
// =============================================================================

func TestPathHelpers(t *testing.T) {
	t.Setenv("GEMINI_HOME", "/x/.gemini")
	if got := TokenPath(); got != "/x/.gemini/antigravity-cli/antigravity-oauth-token" {
		t.Errorf("TokenPath() = %q", got)
	}
	if got := AccountsPath(); got != "/x/.gemini/google_accounts.json" {
		t.Errorf("AccountsPath() = %q", got)
	}
	if got := OAuthCredsPath(); got != "/x/.gemini/oauth_creds.json" {
		t.Errorf("OAuthCredsPath() = %q", got)
	}
	if got := SettingsPath(); got != "/x/.gemini/antigravity-cli/settings.json" {
		t.Errorf("SettingsPath() = %q", got)
	}
}

// =============================================================================
// Interface compliance
// =============================================================================

func TestProviderInterface(t *testing.T) {
	var _ provider.Provider = (*Provider)(nil)
}

// suppress unused-const warning if fakeCreds/fakeSettings drift; reference them.
var _ = fakeCreds
var _ = fakeSettings
