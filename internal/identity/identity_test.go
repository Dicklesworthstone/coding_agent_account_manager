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

func TestExtractFromJWT_NestedOpenAIClaims(t *testing.T) {
	// Simulates a real Codex/OpenAI JWT where plan type is nested under
	// the "https://api.openai.com/auth" namespace claim.
	payload := map[string]interface{}{
		"sub": "user-abc",
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]interface{}{
			"chatgpt_plan_type": "plus",
			"user_id":           "uid-456",
		},
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.PlanType != "plus" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "plus")
	}
	if identity.AccountID != "uid-456" {
		t.Errorf("AccountID = %q, want %q", identity.AccountID, "uid-456")
	}
}

func TestExtractFromJWT_NestedEmailClaim(t *testing.T) {
	// Email only present inside a nested namespace.
	payload := map[string]interface{}{
		"sub": "non-email-sub",
		"https://api.openai.com/profile": map[string]interface{}{
			"email": "openai-user@example.com",
		},
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.Email != "openai-user@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "openai-user@example.com")
	}
}

func TestExtractFromJWT_TopLevelTakesPrecedenceOverNested(t *testing.T) {
	// When a claim exists at both top-level and nested, top-level wins.
	payload := map[string]interface{}{
		"email":     "top-level@example.com",
		"plan_type": "enterprise",
		"https://api.openai.com/auth": map[string]interface{}{
			"email":             "nested@example.com",
			"chatgpt_plan_type": "plus",
		},
	}
	token := buildJWT(t, payload)

	identity, err := ExtractFromJWT(token)
	if err != nil {
		t.Fatalf("ExtractFromJWT error: %v", err)
	}
	if identity.Email != "top-level@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "top-level@example.com")
	}
	if identity.PlanType != "enterprise" {
		t.Errorf("PlanType = %q, want %q", identity.PlanType, "enterprise")
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

// TestExtractFromCodexAuth_ExpiryFromAccessToken: identity claims still come
// from the id_token, but the expiry must be the access token's, since that is
// the token Codex actually authenticates with (PR #86).
func TestExtractFromCodexAuth_ExpiryFromAccessToken(t *testing.T) {
	idExp := time.Now().Add(-3 * 24 * time.Hour).Truncate(time.Second)
	accessExp := time.Now().Add(6 * 24 * time.Hour).Truncate(time.Second)

	t.Run("nested layout", func(t *testing.T) {
		path := writeAuthFile(t, map[string]interface{}{
			"tokens": map[string]interface{}{
				"id_token":     buildJWT(t, map[string]interface{}{"email": "id@example.com", "exp": idExp.Unix()}),
				"access_token": buildJWT(t, map[string]interface{}{"exp": accessExp.Unix()}),
			},
		})

		id, err := ExtractFromCodexAuth(path)
		if err != nil {
			t.Fatalf("ExtractFromCodexAuth error: %v", err)
		}
		if id.Email != "id@example.com" {
			t.Errorf("Email = %q, want the id_token's email", id.Email)
		}
		if !id.ExpiresAt.Equal(accessExp) {
			t.Errorf("ExpiresAt = %v, want access token exp %v", id.ExpiresAt, accessExp)
		}
	})

	t.Run("flat layout", func(t *testing.T) {
		path := writeAuthFile(t, map[string]interface{}{
			"id_token":     buildJWT(t, map[string]interface{}{"email": "flat@example.com", "exp": idExp.Unix()}),
			"access_token": buildJWT(t, map[string]interface{}{"exp": accessExp.Unix()}),
		})

		id, err := ExtractFromCodexAuth(path)
		if err != nil {
			t.Fatalf("ExtractFromCodexAuth error: %v", err)
		}
		if !id.ExpiresAt.Equal(accessExp) {
			t.Errorf("ExpiresAt = %v, want access token exp %v", id.ExpiresAt, accessExp)
		}
	})

	t.Run("id token expiry kept when access token has none", func(t *testing.T) {
		path := writeAuthFile(t, map[string]interface{}{
			"tokens": map[string]interface{}{
				"id_token":     buildJWT(t, map[string]interface{}{"email": "id@example.com", "exp": idExp.Unix()}),
				"access_token": "opaque",
			},
		})

		id, err := ExtractFromCodexAuth(path)
		if err != nil {
			t.Fatalf("ExtractFromCodexAuth error: %v", err)
		}
		if !id.ExpiresAt.Equal(idExp) {
			t.Errorf("ExpiresAt = %v, want id token exp %v", id.ExpiresAt, idExp)
		}
	})
}

func TestFixture_CodexOpenAINestedClaims(t *testing.T) {
	// Tests the real-world Codex/OpenAI JWT structure where plan type is
	// nested under "https://api.openai.com/auth" -> "chatgpt_plan_type".
	identity, err := ExtractFromCodexAuth("testdata/codex_openai_nested_claims.json")
	if err != nil {
		t.Fatalf("ExtractFromCodexAuth error: %v", err)
	}

	if identity.Provider != "codex" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "codex")
	}
	if identity.Email != "codex-nested-only@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "codex-nested-only@example.com")
	}
	if identity.PlanType != "plus" {
		t.Errorf("PlanType = %q, want %q (should extract from nested https://api.openai.com/auth claim)", identity.PlanType, "plus")
	}
	if identity.AccountID != "uid-nested-only" {
		t.Errorf("AccountID = %q, want %q (should extract from nested https://api.openai.com/auth claim)", identity.AccountID, "uid-nested-only")
	}
}

func TestFixture_CodexNoTokens(t *testing.T) {
	_, err := ExtractFromCodexAuth("testdata/codex_no_tokens.json")
	if err == nil {
		t.Error("expected error for fixture without tokens")
	}
}
