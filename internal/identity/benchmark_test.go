package identity

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildTestJWT creates a JWT-like string for benchmarking.
// Does not include a valid signature - just the structural format.
func buildTestJWT(claims map[string]interface{}) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))

	claimsJSON, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(claimsJSON)

	// Fake signature (not validated anyway)
	signature := base64.RawURLEncoding.EncodeToString([]byte("fake-signature-data-for-benchmark"))

	return strings.Join([]string{header, payload, signature}, ".")
}

// BenchmarkJWTParsing benchmarks basic JWT parsing.
func BenchmarkJWTParsing(b *testing.B) {
	claims := map[string]interface{}{
		"email":    "user@example.com",
		"org":      "acme-corp",
		"exp":      time.Now().Add(time.Hour).Unix(),
		"plan":     "pro",
		"user_id":  "usr_abc123",
		"iat":      time.Now().Unix(),
		"iss":      "https://auth.example.com",
		"aud":      "api.example.com",
		"sub":      "user@example.com",
		"jti":      "unique-token-id-12345",
		"name":     "Test User",
		"nickname": "testuser",
	}
	token := buildTestJWT(claims)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ExtractFromJWT(token)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJWTParsingLargeClaims benchmarks JWT parsing with many claims.
func BenchmarkJWTParsingLargeClaims(b *testing.B) {
	claims := make(map[string]interface{})

	// Add standard claims
	claims["email"] = "user@example.com"
	claims["org"] = "acme-corp"
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	claims["iat"] = time.Now().Unix()
	claims["sub"] = "user@example.com"

	// Add many extra claims to simulate real-world tokens
	for i := 0; i < 50; i++ {
		claims["custom_claim_"+string(rune('a'+i%26))+string(rune('0'+i/26))] = "value_" + string(rune('A'+i%26))
	}

	// Add nested claims
	claims["permissions"] = map[string]interface{}{
		"read":   true,
		"write":  true,
		"admin":  false,
		"scopes": []string{"api:read", "api:write", "profile:read"},
	}

	token := buildTestJWT(claims)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := ExtractFromJWT(token)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJWTClaimExtraction focuses on the claim extraction overhead.
func BenchmarkJWTClaimExtraction(b *testing.B) {
	claims := map[string]interface{}{
		"email":              "user@example.com",
		"preferred_username": "testuser",
		"organization":       "acme-corp",
		"org":                "acme",
		"org_name":           "ACME Corporation",
		"plan_type":          "enterprise",
		"subscription_type":  "max",
		"account_id":         "acc_123",
		"user_id":            "usr_456",
		"exp":                time.Now().Add(time.Hour).Unix(),
	}
	token := buildTestJWT(claims)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		identity, err := ExtractFromJWT(token)
		if err != nil {
			b.Fatal(err)
		}
		// Access fields to ensure they're extracted
		if identity.Email == "" {
			b.Fatal("expected email")
		}
	}
}

// BenchmarkJWTParsingParallel benchmarks parallel JWT parsing.
func BenchmarkJWTParsingParallel(b *testing.B) {
	claims := map[string]interface{}{
		"email": "user@example.com",
		"org":   "acme-corp",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"plan":  "pro",
	}
	token := buildTestJWT(claims)

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := ExtractFromJWT(token)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkClaudeCredentialsWithSettings measures identity extraction when the
// oauthAccount fallback has to parse a realistically large .claude.json
// (vault snapshots run from tens of KB to a few hundred KB, since the file
// also carries per-project session history).
func BenchmarkClaudeCredentialsWithSettings(b *testing.B) {
	dir := b.TempDir()
	credentials := `{"claudeAiOauth":{"accessToken":"sk-ant-oat01-opaque","subscriptionType":"max","expiresAt":1788000000000}}`
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(credentials), 0600); err != nil {
		b.Fatalf("write .credentials.json: %v", err)
	}

	projects := make(map[string]interface{}, 400)
	for i := 0; i < 400; i++ {
		projects[fmt.Sprintf("/home/user/code/project-%d", i)] = map[string]interface{}{
			"allowedTools": []string{"Bash", "Read", "Edit", "Write"},
			"history":      []string{strings.Repeat("a recorded prompt line ", 20)},
		}
	}
	settings, err := json.Marshal(map[string]interface{}{
		"userID":   "per-installation-id",
		"projects": projects,
		"oauthAccount": map[string]interface{}{
			"accountUuid":      "0552aaaa-bbbb-cccc-dddd-eeeeeeeeeeee",
			"emailAddress":     "bench@example.com",
			"organizationName": "bench-org",
		},
	})
	if err != nil {
		b.Fatalf("marshal settings: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), settings, 0600); err != nil {
		b.Fatalf("write .claude.json: %v", err)
	}
	b.Logf(".claude.json size: %d bytes", len(settings))

	path := filepath.Join(dir, ".credentials.json")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id, err := ExtractFromClaudeCredentials(path)
		if err != nil {
			b.Fatalf("ExtractFromClaudeCredentials error: %v", err)
		}
		if id.Email == "" {
			b.Fatal("expected email from oauthAccount")
		}
	}
}
