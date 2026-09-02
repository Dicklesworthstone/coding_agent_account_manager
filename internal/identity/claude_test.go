package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractFromClaudeCredentials_AllFields(t *testing.T) {
	exp := time.Now().Add(90 * time.Minute).UTC()
	cred := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accountId":        "acc-123",
			"subscriptionType": "max",
			"email":            "claude@example.com",
			"expiresAt":        exp.Unix() * 1000, // milliseconds
		},
	}
	path := writeClaudeFile(t, cred)

	identity, err := ExtractFromClaudeCredentials(path)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if identity.AccountID != "acc-123" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-123")
	}
	if identity.PlanType != "max" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "max")
	}
	if identity.Email != "claude@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "claude@example.com")
	}
	if identity.ExpiresAt.Unix() != exp.Unix() {
		t.Errorf("ExpiresAt = %v, want unix %d", identity.ExpiresAt, exp.Unix())
	}
	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
}

func TestExtractFromClaudeCredentials_Minimal(t *testing.T) {
	cred := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accountId": "acc-min",
		},
	}
	path := writeClaudeFile(t, cred)

	identity, err := ExtractFromClaudeCredentials(path)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if identity.AccountID != "acc-min" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-min")
	}
	if identity.PlanType != "" || identity.Email != "" {
		t.Errorf("Expected empty PlanType/Email, got %+v", identity)
	}
}

func TestExtractFromClaudeCredentials_MissingObject(t *testing.T) {
	cred := map[string]interface{}{
		"unrelated": "value",
	}
	path := writeClaudeFile(t, cred)

	identity, err := ExtractFromClaudeCredentials(path)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
	if identity.AccountID != "" || identity.PlanType != "" || identity.Email != "" {
		t.Errorf("Expected empty identity fields, got %+v", identity)
	}
}

func TestExtractFromClaudeCredentials_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	if err := os.WriteFile(path, []byte("{not json"), 0600); err != nil {
		t.Fatalf("write credentials.json: %v", err)
	}

	if _, err := ExtractFromClaudeCredentials(path); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractFromClaudeCredentials_MissingFile(t *testing.T) {
	if _, err := ExtractFromClaudeCredentials("/nonexistent/claude.json"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

// TestExtractFromClaudeCredentials_CurrentFormat tests the realistic format
// seen in current Claude Code auth files (early 2026+).
// These files do NOT contain email or accountId - only expiresAt and subscriptionType.
// See: docs/CLAUDE_AUTH_INVENTORY.md (CLAUDE-001)
func TestExtractFromClaudeCredentials_CurrentFormat(t *testing.T) {
	exp := time.Now().Add(4 * time.Hour).UTC()
	cred := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			// accessToken is opaque (not a JWT)
			"accessToken":      "sk-ant-oat01-XXXX-opaque-token-not-decodable-XXXX",
			"refreshToken":     "sk-ant-ort01-YYYY-refresh-token-YYYY",
			"expiresAt":        exp.UnixMilli(),
			"subscriptionType": "claude_pro_2025",
			// NOTE: email and accountId are NOT present in current format
		},
	}
	path := writeClaudeFile(t, cred)

	identity, err := ExtractFromClaudeCredentials(path)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	// Provider should always be set
	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}

	// PlanType and ExpiresAt should be populated
	if identity.PlanType != "claude_pro_2025" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "claude_pro_2025")
	}
	if identity.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}

	// Email and AccountID should be empty (not present in current format)
	if identity.Email != "" {
		t.Errorf("Email should be empty in current format, got %q", identity.Email)
	}
	if identity.AccountID != "" {
		t.Errorf("AccountID should be empty in current format, got %q", identity.AccountID)
	}
}

// TestExtractFromClaudeCredentials_OpaqueToken verifies we don't crash
// or return misleading data when given an opaque (non-JWT) access token.
func TestExtractFromClaudeCredentials_OpaqueToken(t *testing.T) {
	cred := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			// This looks like a JWT but isn't - it's an opaque token
			"accessToken":      "sk-ant-oat01-this.is.not.a.jwt",
			"subscriptionType": "max",
		},
	}
	path := writeClaudeFile(t, cred)

	identity, err := ExtractFromClaudeCredentials(path)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	// Should succeed but with empty identity fields (no email/accountId)
	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
	if identity.PlanType != "max" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "max")
	}
	// Email and AccountID should be empty
	if identity.Email != "" || identity.AccountID != "" {
		t.Errorf("Expected empty email/accountId, got email=%q accountId=%q", identity.Email, identity.AccountID)
	}
}

// writeClaudeSettings writes a .claude.json with the given oauthAccount block
// (plus filler keys, since the real file is mostly unrelated state) into dir.
func writeClaudeSettings(t *testing.T, dir string, account map[string]interface{}) {
	t.Helper()
	content := map[string]interface{}{
		"numStartups":            42,
		"hasCompletedOnboarding": true,
		"projects":               map[string]interface{}{"/tmp/repo": map[string]interface{}{"allowedTools": []string{}}},
	}
	if account != nil {
		content["oauthAccount"] = account
	}
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal .claude.json: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), data, 0600); err != nil {
		t.Fatalf("write .claude.json: %v", err)
	}
}

// TestExtractFromClaudeCredentials_IdentityFromClaudeJSON covers PR #85: the
// current credentials carry no email, but the .claude.json beside them (a
// vault snapshot) holds the account identity under oauthAccount.
func TestExtractFromClaudeCredentials_IdentityFromClaudeJSON(t *testing.T) {
	dir := t.TempDir()
	cred := map[string]interface{}{
		"claudeAiOauth": map[string]interface{}{
			"accessToken":      "sk-ant-oat01-opaque",
			"subscriptionType": "max",
			"expiresAt":        time.Now().Add(4 * time.Hour).UnixMilli(),
		},
	}
	data, _ := json.Marshal(cred)
	credPath := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credPath, data, 0600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, dir, map[string]interface{}{
		"accountUuid":      "uuid-1234",
		"emailAddress":     "vault@example.com",
		"organizationName": "vault-org",
	})

	id, err := ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "vault@example.com" {
		t.Errorf("Email = %q, want %q", id.Email, "vault@example.com")
	}
	if id.AccountID != "uuid-1234" {
		t.Errorf("AccountID = %q, want %q", id.AccountID, "uuid-1234")
	}
	if id.Organization != "vault-org" {
		t.Errorf("Organization = %q, want %q", id.Organization, "vault-org")
	}
	if id.PlanType != "max" {
		t.Errorf("PlanType = %q, want %q (credentials stay authoritative)", id.PlanType, "max")
	}
	if id.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should come from the credentials")
	}
}

// The live layout keeps the credentials in ~/.claude/ and the identity in
// ~/.claude.json, one directory up.
func TestExtractFromClaudeCredentials_IdentityFromParentClaudeJSON(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(claudeDir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"subscriptionType":"pro"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, home, map[string]interface{}{
		"accountUuid":  "uuid-live",
		"emailAddress": "live@example.com",
	})

	id, err := ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "live@example.com" || id.AccountID != "uuid-live" {
		t.Errorf("identity = %+v, want live@example.com / uuid-live", id)
	}

	// A stray .claude.json INSIDE .claude/ (left by an older tool that set
	// CLAUDE_CONFIG_DIR=~/.claude, and mirrored into every shallow profile by
	// the symlink farm) must not outrank the canonical parent file (#91).
	writeClaudeSettings(t, claudeDir, map[string]interface{}{
		"accountUuid":  "uuid-stale",
		"emailAddress": "stale@example.com",
	})
	id, err = ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "live@example.com" || id.AccountID != "uuid-live" {
		t.Errorf("identity = %+v, want the parent ~/.claude.json to win over the stray nested file", id)
	}
}

// With no usable parent file the nested .claude.json is still a fallback.
func TestExtractFromClaudeCredentials_NestedClaudeJSONIsFallback(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(claudeDir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"subscriptionType":"pro"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, claudeDir, map[string]interface{}{"emailAddress": "nested@example.com"})

	// No parent file at all.
	id, err := ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "nested@example.com" {
		t.Errorf("Email = %q, want nested fallback %q", id.Email, "nested@example.com")
	}

	// A parent file without an oauthAccount block does not shadow the fallback.
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	id, err = ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "nested@example.com" {
		t.Errorf("Email = %q, want nested fallback %q", id.Email, "nested@example.com")
	}
}

// When CLAUDE_CONFIG_DIR points at the .claude directory itself, Claude Code
// keeps its state file inside it, so the nested file is canonical again.
func TestExtractFromClaudeCredentials_ConfigDirPinsNestedClaudeJSON(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(claudeDir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, home, map[string]interface{}{"emailAddress": "parent@example.com"})
	writeClaudeSettings(t, claudeDir, map[string]interface{}{"emailAddress": "configdir@example.com"})

	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	id, err := ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "configdir@example.com" {
		t.Errorf("Email = %q, want %q (CLAUDE_CONFIG_DIR names this dir)", id.Email, "configdir@example.com")
	}

	// A CLAUDE_CONFIG_DIR naming some OTHER directory (a different profile's
	// config dir, say) does not change the precedence for this one.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(t.TempDir(), ".claude"))
	id, err = ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "parent@example.com" {
		t.Errorf("Email = %q, want parent %q", id.Email, "parent@example.com")
	}
}

// The parent directory is only consulted for a ".claude" credentials dir:
// a stray .claude.json one level above an arbitrary directory is not paired.
func TestExtractFromClaudeCredentials_ParentOnlyForDotClaudeDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "profile")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	credPath := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, root, map[string]interface{}{"emailAddress": "stray@example.com"})

	id, err := ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "" {
		t.Errorf("Email = %q, want empty (unrelated parent .claude.json must not be used)", id.Email)
	}
}

// Legacy credentials that still carry email/accountId keep them.
func TestExtractFromClaudeCredentials_CredentialsFieldsWin(t *testing.T) {
	dir := t.TempDir()
	credPath := filepath.Join(dir, ".credentials.json")
	if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"email":"legacy@example.com","accountId":"acc-legacy"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, dir, map[string]interface{}{
		"accountUuid":      "uuid-settings",
		"emailAddress":     "settings@example.com",
		"organizationName": "settings-org",
	})

	id, err := ExtractFromClaudeCredentials(credPath)
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}
	if id.Email != "legacy@example.com" || id.AccountID != "acc-legacy" {
		t.Errorf("identity = %+v, want the credentials' own email/accountId", id)
	}
	if id.Organization != "settings-org" {
		t.Errorf("Organization = %q, want %q (filled from .claude.json since credentials have none)", id.Organization, "settings-org")
	}
}

// Missing, malformed, or oauthAccount-less .claude.json leaves the identity
// fields empty rather than failing the whole extraction.
func TestExtractFromClaudeCredentials_ClaudeJSONFallbacks(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string){
		"missing": func(t *testing.T, dir string) {},
		"malformed": func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, ".claude.json"), []byte("{oops"), 0600)
		},
		"no account": func(t *testing.T, dir string) {
			writeClaudeSettings(t, dir, nil)
		},
		"null account": func(t *testing.T, dir string) {
			_ = os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":null}`), 0600)
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			credPath := filepath.Join(dir, ".credentials.json")
			if err := os.WriteFile(credPath, []byte(`{"claudeAiOauth":{"subscriptionType":"max"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			setup(t, dir)

			id, err := ExtractFromClaudeCredentials(credPath)
			if err != nil {
				t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
			}
			if id.Email != "" || id.AccountID != "" || id.Organization != "" {
				t.Errorf("identity = %+v, want empty identity fields", id)
			}
			if id.PlanType != "max" {
				t.Errorf("PlanType = %q, want %q", id.PlanType, "max")
			}
		})
	}
}

func writeClaudeFile(t *testing.T, content map[string]interface{}) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal credentials: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write credentials.json: %v", err)
	}
	return path
}

// Fixture-based tests for comprehensive coverage

func TestFixture_ClaudeCurrentFormat(t *testing.T) {
	identity, err := ExtractFromClaudeCredentials("testdata/claude_current_format.json")
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
	if identity.PlanType != "claude_pro_2025" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "claude_pro_2025")
	}
	// Current format has no email/accountId
	if identity.Email != "" {
		t.Errorf("Email should be empty, got %q", identity.Email)
	}
	if identity.AccountID != "" {
		t.Errorf("AccountID should be empty, got %q", identity.AccountID)
	}
	// expiresAt: 1737619200000 ms = 2025-01-23T12:00:00Z
	if identity.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}
}

func TestFixture_ClaudeLegacyWithEmail(t *testing.T) {
	identity, err := ExtractFromClaudeCredentials("testdata/claude_legacy_with_email.json")
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
	if identity.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "user@example.com")
	}
	if identity.AccountID != "acc-123456789" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-123456789")
	}
	if identity.PlanType != "max" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "max")
	}
}

func TestFixture_ClaudeMinimal(t *testing.T) {
	identity, err := ExtractFromClaudeCredentials("testdata/claude_minimal.json")
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
	// Minimal has only accessToken, no identity fields
	if identity.Email != "" || identity.AccountID != "" || identity.PlanType != "" {
		t.Errorf("Expected all empty fields, got email=%q accountId=%q planType=%q",
			identity.Email, identity.AccountID, identity.PlanType)
	}
}

func TestFixture_ClaudeNoOauth(t *testing.T) {
	identity, err := ExtractFromClaudeCredentials("testdata/claude_no_oauth.json")
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	// Valid JSON but no claudeAiOauth section
	if identity.Provider != "claude" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "claude")
	}
	if identity.Email != "" || identity.AccountID != "" || identity.PlanType != "" {
		t.Errorf("Expected all empty fields for no oauth, got %+v", identity)
	}
}

func TestFixture_ClaudeEpochSeconds(t *testing.T) {
	identity, err := ExtractFromClaudeCredentials("testdata/claude_epoch_seconds.json")
	if err != nil {
		t.Fatalf("ExtractFromClaudeCredentials error: %v", err)
	}

	// This fixture has expiresAt in seconds (not milliseconds)
	// The parser should normalize it correctly
	if identity.ExpiresAt.IsZero() {
		t.Error("ExpiresAt should not be zero")
	}
	if identity.PlanType != "free" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "free")
	}
}

func TestFixture_ClaudeInvalid(t *testing.T) {
	_, err := ExtractFromClaudeCredentials("testdata/claude_invalid.json")
	if err == nil {
		t.Error("expected error for invalid JSON fixture")
	}
}
