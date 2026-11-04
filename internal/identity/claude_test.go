package identity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectClaudeIdentityBindingSeparatesMissingAndBrokenProof(t *testing.T) {
	credentials := []byte(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-TEAM-ACCESS","refreshToken":"SYNTHETIC-REFRESH","expiresAt":1}}`)
	fingerprint, err := ClaudeCredentialFingerprint(credentials)
	if err != nil {
		t.Fatal(err)
	}
	proof := ClaudeIdentityBinding{Identity: Identity{Provider: "claude", AccountID: "account", OrganizationID: "team", Email: "shared@example.test", Verified: true}, CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC()}
	encode := func(binding ClaudeIdentityBinding) []byte {
		t.Helper()
		data, err := json.Marshal(map[string]any{"claude_identity": binding, "description": "operator label"})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	for _, test := range []struct {
		name, state string
		data        []byte
		bad         bool
	}{
		{"missing", ClaudeIdentityUnknown, nil, false},
		{"legacy", ClaudeIdentityUnknown, []byte(`{"identity":"shared@example.test"}`), false},
		{"null proof", ClaudeIdentityUnknown, []byte(`{"claude_identity":null}`), false},
		{"empty file", ClaudeIdentityInvalidMetadata, []byte{}, true},
		{"null document", ClaudeIdentityInvalidMetadata, []byte(`null`), true},
		{"invalid JSON", ClaudeIdentityInvalidMetadata, []byte(`{"SYNTHETIC-SECRET-DO-NOT-PRINT`), true},
		{"incomplete proof", ClaudeIdentityInvalidMetadata, []byte(`{"claude_identity":{}}`), true},
		{"bound expired pair", ClaudeIdentityVerified, encode(proof), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := InspectClaudeIdentityBinding(test.data, credentials)
			if result.State != test.state || (err != nil) != test.bad {
				t.Fatalf("inspection = %+v, %v", result, err)
			}
			if err != nil && strings.Contains(err.Error(), "SYNTHETIC") {
				t.Fatal("inspection leaked malformed credential metadata")
			}
			if result.Binding != nil && (!result.Binding.Identity.Verified || result.RecordedIdentity == nil || result.RecordedIdentity.Verified) {
				t.Fatal("recorded ownership and current verified ownership were confused")
			}
		})
	}
	changed := []byte(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-PERSONAL-ACCESS","refreshToken":"SYNTHETIC-REFRESH"}}`)
	result, err := InspectClaudeIdentityBinding(encode(proof), changed)
	if err != nil || result.State != ClaudeIdentityCredentialChanged || result.Binding != nil || result.RecordedIdentity == nil || result.RecordedIdentity.Verified || result.RecordedIdentity.OrganizationID != "team" {
		t.Fatalf("substituted access token inherited a cached proof: %+v, %v", result, err)
	}
	for _, badFingerprint := range []string{strings.Repeat("z", 64), strings.Repeat(":", 64), strings.Repeat("F", 64), "short"} {
		invalid := proof
		invalid.CredentialFingerprint = badFingerprint
		result, err := InspectClaudeIdentityBinding(encode(invalid), credentials)
		if err == nil || result.State != ClaudeIdentityInvalidMetadata || result.RecordedIdentity != nil {
			t.Fatalf("invalid fingerprint accepted: %+v, %v", result, err)
		}
	}
}

func TestClaudeIdentitySourcesDoNotHideContradictoryEvidence(t *testing.T) {
	credential := []byte(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-current","refreshToken":"SYNTHETIC-refresh"}}`)
	previous := []byte(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-previous","refreshToken":"SYNTHETIC-refresh"}}`)
	encode := func(data []byte, account, organization string) []byte {
		t.Helper()
		fingerprint, err := ClaudeCredentialFingerprint(data)
		if err != nil {
			t.Fatal(err)
		}
		metadata, err := json.Marshal(map[string]any{"claude_identity": ClaudeIdentityBinding{
			Identity:              Identity{Provider: "claude", AccountID: account, OrganizationID: organization, Email: "shared@example.test"},
			CredentialFingerprint: fingerprint, VerifiedAt: time.Unix(1700000000, 0).UTC(),
		}})
		if err != nil {
			t.Fatal(err)
		}
		return metadata
	}
	team := encode(credential, "account", "team")
	for _, test := range []struct {
		name, state     string
		metadata, cache []byte
		bad             bool
	}{
		{"same owner", ClaudeIdentityVerified, team, team, false},
		{"different organization", "conflict", team, encode(credential, "account", "personal"), true},
		{"different account", "conflict", team, encode(credential, "other", "team"), true},
		{"stale conflicting cache", "conflict", team, encode(previous, "account", "personal"), true},
		{"changed primary cannot inherit cache", ClaudeIdentityCredentialChanged, encode(previous, "account", "team"), team, false},
		{"legacy with exact cache", ClaudeIdentityVerified, []byte(`{"identity":"label@example.test"}`), team, false},
		{"valid primary cannot hide malformed cache", ClaudeIdentityInvalidMetadata, team, []byte(`{"claude_identity":{}}`), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := InspectClaudeIdentitySources(test.metadata, test.cache, credential)
			if result.State != test.state || (err != nil) != test.bad || test.bad && result.Binding != nil {
				t.Fatalf("unexpected assessment state=%s error=%v", result.State, err)
			}
			dir := t.TempDir()
			for name, data := range map[string][]byte{".credentials.json": credential, "meta.json": test.metadata, ClaudeIdentityCacheFile: test.cache} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			binding, err := ReadClaudeIdentityBinding(filepath.Join(dir, ".credentials.json"))
			if (err != nil) != test.bad || (binding != nil) != (test.state == ClaudeIdentityVerified) {
				t.Fatalf("file-backed reader ignored the combined evidence: %v", err)
			}
		})
	}
}

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
		"organizationUuid": "org-1234",
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
	if id.OrganizationID != "org-1234" || id.Verified {
		t.Errorf("expected unverified organization UUID from metadata, got %+v", id)
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
	if id.Organization != "" || id.OrganizationID != "" {
		t.Errorf("conflicting settings must not supply another account's organization: %+v", id)
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

func TestClaudeSignedInIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	if got := ClaudeSignedInIdentity(); got != nil {
		t.Fatalf("no credentials: got %+v, want nil", got)
	}

	write := func(path, email string) {
		t.Helper()
		body := `{"numStartups": 3, "oauthAccount": {"accountUuid": "u", "organizationUuid": "org", "emailAddress": "` + email + `"}}`
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".claude", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"home-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(home, ".claude.json"), "home@example.com")
	if got := ClaudeSignedInIdentity(); got == nil || got.Email != "home@example.com" || got.OrganizationID != "org" || got.Verified {
		t.Fatalf("~/.claude.json: got %+v", got)
	}
	// CLAUDE_CONFIG_DIR moves the state file into that directory.
	cfg := filepath.Join(home, "alt")
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"isolated-token"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join(cfg, ".claude.json"), "alt@example.com")
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	if got := ClaudeSignedInIdentity(); got == nil || got.Email != "alt@example.com" || got.OrganizationID != "org" || got.Verified {
		t.Fatalf("CLAUDE_CONFIG_DIR: got %+v", got)
	}
}

func TestClaudeSameAccountRequiresAccountAndOrganization(t *testing.T) {
	personal := &Identity{Provider: "claude", AccountID: "user-uuid", OrganizationID: "personal-org", Email: "same@example.com"}
	cases := []struct {
		name     string
		other    *Identity
		same     bool
		conflict bool
	}{
		{"same account and org", &Identity{AccountID: "user-uuid", OrganizationID: "personal-org", Email: "renamed@example.com"}, true, false},
		{"same email different org", &Identity{AccountID: "user-uuid", OrganizationID: "team-org", Email: "same@example.com"}, false, true},
		{"different account same org", &Identity{AccountID: "other-user", OrganizationID: "personal-org", Email: "same@example.com"}, false, true},
		{"missing organization", &Identity{AccountID: "user-uuid", Email: "same@example.com"}, false, false},
		{"email only", &Identity{Email: "same@example.com"}, false, false},
		{"missing", nil, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClaudeSameAccount(personal, tc.other); got != tc.same {
				t.Fatalf("same = %v, want %v", got, tc.same)
			}
			if got := ClaudeIdentityConflicts(personal, tc.other); got != tc.conflict {
				t.Fatalf("conflict = %v, want %v", got, tc.conflict)
			}
		})
	}
}

func TestParseClaudeIdentityRejectsConflictingOrganizationMetadata(t *testing.T) {
	credentials := []byte(`{"claudeAiOauth":{"accountId":"user","organizationUuid":"team","email":"same@example.com","subscriptionType":"team"}}`)
	settings := []byte(`{"oauthAccount":{"accountUuid":"user","organizationUuid":"personal","organizationName":"Personal","emailAddress":"same@example.com"}}`)
	id, err := ParseClaudeIdentity(credentials, settings)
	if err != nil {
		t.Fatal(err)
	}
	if id.OrganizationID != "team" || id.Organization != "" || id.Verified {
		t.Fatalf("stale metadata was combined with credentials: %+v", id)
	}
}

type claudeIdentityTransport func(*http.Request) (*http.Response, error)

func (f claudeIdentityTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func claudeProfileResponse(body string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestVerifyClaudeCredentialsBindsProviderIdentityToBothTokens(t *testing.T) {
	credentials := []byte(`{"claudeAiOauth":{"accessToken":"test-access-secret","refreshToken":"test-refresh-secret","subscriptionType":"team","expiresAt":4102444800000}}`)
	client := &http.Client{Transport: claudeIdentityTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.String() != "https://api.anthropic.com/api/oauth/profile" {
			t.Fatalf("unexpected identity request destination")
		}
		if r.Header.Get("Authorization") != "Bearer test-access-secret" || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
			t.Fatal("missing OAuth request headers")
		}
		if !strings.Contains(r.UserAgent(), "coding_agent_account_manager") {
			t.Fatalf("request does not identify CAAM")
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Fatal("identity request is not bounded")
		}
		return claudeProfileResponse(`{"account":{"uuid":"account-uuid","email":"same@example.com"},"organization":{"uuid":"team-org","name":"Work Team"}}`, http.StatusOK), nil
	})}
	binding, err := VerifyClaudeCredentials(context.Background(), credentials, client)
	if err != nil {
		t.Fatal(err)
	}
	if binding.Identity.AccountID != "account-uuid" || binding.Identity.OrganizationID != "team-org" || binding.Identity.Organization != "Work Team" || !binding.Identity.Verified {
		t.Fatalf("incorrect verified identity: %+v", binding.Identity)
	}
	if !binding.MatchesCredentials(credentials) {
		t.Fatal("binding does not match the verified credentials")
	}
	for name, changed := range map[string]string{
		"access token":  `{"claudeAiOauth":{"accessToken":"other-access","refreshToken":"test-refresh-secret"}}`,
		"refresh token": `{"claudeAiOauth":{"accessToken":"test-access-secret","refreshToken":"other-refresh"}}`,
		"both tokens":   `{"claudeAiOauth":{"accessToken":"other-access","refreshToken":"other-refresh"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if binding.MatchesCredentials([]byte(changed)) {
				t.Fatal("binding accepted different token credentials")
			}
		})
	}
	if !binding.MatchesCredentials([]byte(`{"telemetry":true,"claudeAiOauth":{"refreshToken":"test-refresh-secret","accessToken":"test-access-secret","expiresAt":1}}`)) {
		t.Fatal("unrelated metadata must not change the identity of an exact token pair")
	}
	metadata, err := json.Marshal(map[string]interface{}{"claude_identity": binding})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(metadata), "test-access-secret") || strings.Contains(string(metadata), "test-refresh-secret") {
		t.Fatal("persisted identity contains credentials")
	}
	restarted, err := DecodeClaudeIdentityBinding(metadata, credentials)
	if err != nil || restarted == nil || !restarted.Identity.Verified || !ClaudeSameAccount(&binding.Identity, &restarted.Identity) {
		t.Fatalf("binding did not survive metadata reload: %v", err)
	}
}

func TestVerifyClaudeCredentialsFailureIsUncertaintyAndSecretFree(t *testing.T) {
	credentials := []byte(`{"claudeAiOauth":{"accessToken":"private-access-token","refreshToken":"private-refresh-token"}}`)
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"unauthorized", 401, "private-access-token", ErrClaudeTokenRejected},
		{"missing scope", 403, "private-access-token", ErrClaudeIdentityUnavailable},
		{"rate limited", 429, "private-access-token", ErrClaudeIdentityUnavailable},
		{"server unavailable", 503, "private-access-token", ErrClaudeIdentityUnavailable},
		{"null profile", 200, `null`, ErrClaudeIdentityUnavailable},
		{"malformed profile", 200, `{"account":"private-access-token"}`, ErrClaudeIdentityUnavailable},
		{"no org", 200, `{"account":{"uuid":"user","email":"same@example.com"}}`, ErrClaudeIdentityUnavailable},
		{"no account UUID", 200, `{"account":{"email":"same@example.com"},"organization":{"uuid":"org"}}`, ErrClaudeIdentityUnavailable},
		{"empty org UUID", 200, `{"account":{"uuid":"user"},"organization":{"uuid":"  "}}`, ErrClaudeIdentityUnavailable},
		{"oversized body", 200, strings.Repeat("x", MaxClaudeIdentityBytes+1), ErrClaudeIdentityUnavailable},
		{"trailing response", 200, `{"account":{"uuid":"u"},"organization":{"uuid":"o"}} {}`, ErrClaudeIdentityUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: claudeIdentityTransport(func(*http.Request) (*http.Response, error) {
				return claudeProfileResponse(tc.body, tc.status), nil
			})}
			binding, err := VerifyClaudeCredentials(context.Background(), credentials, client)
			if binding != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got binding=%v, error=%v; want %v", binding != nil, err, tc.want)
			}
			if strings.Contains(err.Error(), "private-") {
				t.Fatal("failure exposed a credential")
			}
		})
	}
	client := &http.Client{Transport: claudeIdentityTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport logged private-access-token")
	})}
	if _, err := VerifyClaudeCredentials(context.Background(), credentials, client); !errors.Is(err, ErrClaudeIdentityUnavailable) || strings.Contains(err.Error(), "private-") {
		t.Fatal("transport diagnostics must not leak into identity errors")
	}
}

func TestVerifyClaudeCredentialsDoesNotFollowRedirects(t *testing.T) {
	for _, location := range []string{"https://evil.example/steal", "https://api.anthropic.com/other"} {
		t.Run(location, func(t *testing.T) {
			calls := 0
			client := &http.Client{
				CheckRedirect: func(*http.Request, []*http.Request) error {
					t.Fatal("caller redirect policy must not override credential protection")
					return nil
				},
				Transport: claudeIdentityTransport(func(r *http.Request) (*http.Response, error) {
					calls++
					if calls > 1 {
						t.Fatal("bearer identity request followed a redirect")
					}
					response := claudeProfileResponse("", http.StatusFound)
					response.Header.Set("Location", location)
					return response, nil
				}),
			}
			binding, err := VerifyClaudeCredentials(context.Background(), []byte(`{"claudeAiOauth":{"accessToken":"opaque"}}`), client)
			if binding != nil || !errors.Is(err, ErrClaudeIdentityUnavailable) || calls != 1 {
				t.Fatalf("unexpected redirect result: %v, calls=%d", err, calls)
			}
		})
	}
}

func TestVerifyClaudeCredentialsExpiredAndCanceled(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: claudeIdentityTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	if _, err := VerifyClaudeCredentials(context.Background(), []byte(`{"claudeAiOauth":{"accessToken":"opaque","expiresAt":1}}`), client); !errors.Is(err, ErrClaudeTokenExpired) || calls != 0 {
		t.Fatalf("expired token must be handled offline: %v, calls=%d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := VerifyClaudeCredentials(ctx, []byte(`{"claudeAiOauth":{"accessToken":"opaque"}}`), client); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation not preserved: %v", err)
	}
}

func TestExtractClaudeBindingOverridesStaleSettingsAndSurvivesRestart(t *testing.T) {
	for _, metadataName := range []string{"meta.json", ClaudeIdentityCacheFile} {
		t.Run(metadataName, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".credentials.json")
			credentials := []byte(`{"claudeAiOauth":{"accessToken":"team-token","refreshToken":"team-refresh","subscriptionType":"team","expiresAt":4102444800000}}`)
			if err := os.WriteFile(path, credentials, 0o600); err != nil {
				t.Fatal(err)
			}
			fingerprint, err := ClaudeCredentialFingerprint(credentials)
			if err != nil {
				t.Fatal(err)
			}
			binding := &ClaudeIdentityBinding{Identity: Identity{Provider: "claude", AccountID: "same-user", OrganizationID: "team-org", Organization: "Team", Email: "same@example.com"}, CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC()}
			metadata, err := json.Marshal(map[string]interface{}{"claude_identity": binding})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, metadataName), metadata, 0o600); err != nil {
				t.Fatal(err)
			}
			writeClaudeSettings(t, dir, map[string]interface{}{"accountUuid": "same-user", "organizationUuid": "personal-org", "organizationName": "Personal", "emailAddress": "same@example.com"})
			for range 2 { // Separate reads, with all authority reconstructed from disk.
				id, err := ExtractFromClaudeCredentials(path)
				if err != nil || id.OrganizationID != "team-org" || id.Organization != "Team" || !id.Verified || id.PlanType != "team" {
					t.Fatalf("stale settings overrode bound token identity: %+v, %v", id, err)
				}
			}
			// Another session replaced only the access token, leaving both the
			// refresh token and the shared metadata untouched.
			if err := os.WriteFile(path, []byte(`{"claudeAiOauth":{"accessToken":"personal-token","refreshToken":"team-refresh","subscriptionType":"max"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			id, err := ExtractFromClaudeCredentials(path)
			if err != nil || id.Verified || id.AccountID != "" || id.OrganizationID != "" || id.Email != "" {
				t.Fatalf("replacement token inherited stale identity: %+v, %v", id, err)
			}
			captured, err := ReadClaudeIdentityBindingForCredentials(path, credentials)
			if err != nil || captured == nil || captured.Identity.OrganizationID != "team-org" {
				t.Fatalf("captured token generation should still match its binding: %v", err)
			}
		})
	}
}

func TestSavedClaudeBindingCannotBeReplacedByCopiedNativeCache(t *testing.T) {
	dir := t.TempDir()
	credentialsPath := filepath.Join(dir, ".credentials.json")
	oldCredentials := []byte(`{"claudeAiOauth":{"accessToken":"personal-access","refreshToken":"personal-refresh"}}`)
	newCredentials := []byte(`{"claudeAiOauth":{"accessToken":"team-access","refreshToken":"team-refresh","accountId":"user","organizationUuid":"personal","email":"same@example.com"}}`)
	if err := os.WriteFile(credentialsPath, newCredentials, 0o600); err != nil {
		t.Fatal(err)
	}
	writeClaudeSettings(t, dir, map[string]interface{}{"accountUuid": "user", "organizationUuid": "personal", "emailAddress": "same@example.com"})
	writeProof := func(name string, credentials []byte, organization string) {
		t.Helper()
		fingerprint, err := ClaudeCredentialFingerprint(credentials)
		if err != nil {
			t.Fatal(err)
		}
		binding := &ClaudeIdentityBinding{
			Identity:              Identity{Provider: "claude", AccountID: "user", OrganizationID: organization, Email: "same@example.com"},
			CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC(),
		}
		data, err := json.Marshal(map[string]interface{}{"claude_identity": binding})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeProof("meta.json", oldCredentials, "personal")
	writeProof(ClaudeIdentityCacheFile, newCredentials, "team")
	if binding, err := ReadClaudeIdentityBindingForCredentials(credentialsPath, newCredentials); err == nil || binding != nil {
		t.Fatalf("copied cache bypassed the saved profile's expected owner: binding=%v, err=%v", binding != nil, err)
	}
	id, err := ExtractFromClaudeCredentials(credentialsPath)
	if err != nil || id.Verified || id.AccountID != "" || id.OrganizationID != "" || id.Email != "" {
		t.Fatalf("changed saved credentials inherited an account label: %+v, %v", id, err)
	}
	// A native directory can contain unrelated metadata without a saved
	// account expectation; in that case the exact-token native cache applies.
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(`{"unrelated":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	id, err = ExtractFromClaudeCredentials(credentialsPath)
	if err != nil || !id.Verified || id.OrganizationID != "team" {
		t.Fatalf("unrelated metadata shadowed the valid native cache: %+v, %v", id, err)
	}
}
