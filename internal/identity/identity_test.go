package identity

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExtractFromJWT_AllFields(t *testing.T) {
	exp := time.Now().Add(2 * time.Hour).Unix()
	payload := map[string]interface{}{
		"email":        "user@example.com",
		"plan_type":    "pro",
		"organization": "Acme",
		"account_id":   "acc-123",
		"exp":          exp,
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "user@example.com")
	}
	if identity.PlanType != "pro" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "pro")
	}
	if identity.Organization != "Acme" {
		t.Errorf("Organization = %q, want %q", identity.Organization, "Acme")
	}
	if identity.AccountID != "acc-123" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "acc-123")
	}
	if identity.ExpiresAt.IsZero() || identity.ExpiresAt.Unix() != exp {
		t.Errorf("ExpiresAt = %v, want unix %d", identity.ExpiresAt, exp)
	}
}

func TestExtractFromJWT_Minimal(t *testing.T) {
	payload := map[string]interface{}{
		"preferred_username": "minimal@example.com",
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.Email != "minimal@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "minimal@example.com")
	}
	if !identity.ExpiresAt.IsZero() {
		t.Errorf("ExpiresAt should be zero when exp missing, got %v", identity.ExpiresAt)
	}
}

func TestExtractFromJWT_Expired(t *testing.T) {
	exp := time.Now().Add(-2 * time.Hour).Unix()
	payload := map[string]interface{}{
		"email": "expired@example.com",
		"exp":   exp,
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.ExpiresAt.Unix() != exp {
		t.Errorf("ExpiresAt = %v, want unix %d", identity.ExpiresAt, exp)
	}
}

func TestExtractFromJWT_Invalid(t *testing.T) {
	_, err := ExtractFromJWT("not-a.jwt.token")
	if err == nil {
		t.Fatal("expected error for malformed JWT")
	}
}

func TestExtractFromJWT_UnknownClaims(t *testing.T) {
	payload := map[string]interface{}{
		"custom_claim": "value",
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.Email != "" || identity.Organization != "" || identity.PlanType != "" || identity.AccountID != "" {
		t.Errorf("expected empty identity fields, got %+v", identity)
	}
}

func TestExtractFromCodexAuth_TopLevelToken(t *testing.T) {
	token := buildJWT(t, map[string]interface{}{"email": "codex@example.com"})
	path := writeAuthFile(t, map[string]interface{}{
		"id_token": token,
	})

	identity, err := ExtractFromCodexAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}
	if identity.Email != "codex@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "codex@example.com")
	}
	if identity.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "codex")
	}
}

func TestExtractFromCodexAuth_NestedToken(t *testing.T) {
	token := buildJWT(t, map[string]interface{}{"email": "nested@example.com"})
	path := writeAuthFile(t, map[string]interface{}{
		"tokens": map[string]interface{}{
			"id_token": token,
		},
	})

	identity, err := ExtractFromCodexAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}
	if identity.Email != "nested@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "nested@example.com")
	}
}

func TestExtractFromCodexAuth_PrefersNestedIDToken(t *testing.T) {
	nested := buildJWT(t, map[string]interface{}{"email": "nested@example.com"})
	path := writeAuthFile(t, map[string]interface{}{
		"access_token": "not-a-jwt",
		"tokens": map[string]interface{}{
			"id_token": nested,
		},
	})

	identity, err := ExtractFromCodexAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}
	if identity.Email != "nested@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "nested@example.com")
	}
}

func TestExtractFromCodexAuth_FallsBackToNestedAccessToken(t *testing.T) {
	nested := buildJWT(t, map[string]interface{}{"email": "fallback@example.com"})
	path := writeAuthFile(t, map[string]interface{}{
		"access_token": "not-a-jwt",
		"tokens": map[string]interface{}{
			"access_token": nested,
		},
	})

	identity, err := ExtractFromCodexAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}
	if identity.Email != "fallback@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "fallback@example.com")
	}
}

func TestExtractFromCodexAuth_MissingToken(t *testing.T) {
	path := writeAuthFile(t, map[string]interface{}{
		"access_token": "not-a-jwt",
	})

	if _, err := ExtractFromCodexAuth(path); err == nil {
		t.Fatal("expected error when id_token is missing")
	}
}

func buildJWT(t *testing.T, payload map[string]interface{}) string {
	t.Helper()

	header := map[string]interface{}{
		"alg": "none",
		"typ": "JWT",
	}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)
	return headerB64 + "." + payloadB64 + ".signature"
}

func writeAuthFile(t *testing.T, content map[string]interface{}) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal auth.json: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write auth.json: %v", err)
	}
	return path
}

// Fixture-based tests for Codex identity extraction

func TestFixture_CodexWithEmail(t *testing.T) {
	identity, err := ExtractFromCodexAuth("testdata/codex_with_email.json")
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}

	if identity.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "codex")
	}
	if identity.Email != "codex@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "codex@example.com")
	}
	if identity.PlanType != "pro" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "pro")
	}
}

func TestFixture_CodexNestedTokens(t *testing.T) {
	identity, err := ExtractFromCodexAuth("testdata/codex_nested_tokens.json")
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}

	if identity.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "codex")
	}
	if identity.Email != "nested@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "nested@example.com")
	}
	if identity.PlanType != "max" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "max")
	}
}

func TestFixture_CodexNoTokens(t *testing.T) {
	_, err := ExtractFromCodexAuth("testdata/codex_no_tokens.json")
	if err == nil {
		t.Error("expected error for fixture without tokens")
	}
}
