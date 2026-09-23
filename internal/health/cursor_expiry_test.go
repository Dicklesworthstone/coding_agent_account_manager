package health

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCursorExpiryFromAccessToken(t *testing.T) {
	exp := time.Now().Add(25 * 24 * time.Hour).Unix()
	token := cursorTestJWT(t, map[string]any{"exp": exp})
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.json")
	body, err := json.Marshal(map[string]string{
		"accessToken":  token,
		"refreshToken": cursorTestJWT(t, map[string]any{"exp": exp}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	info, err := ParseCursorExpiry(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ExpiresAt.Unix() != exp {
		t.Fatalf("exp %s, want unix %d", info.ExpiresAt, exp)
	}
	if !info.Renewable || !info.HasRefreshToken {
		t.Fatalf("renewable %v refresh %v", info.Renewable, info.HasRefreshToken)
	}
	ph := &ProfileHealth{TokenExpiresAt: info.ExpiresAt, TokenRenewable: info.Renewable}
	if got := CalculateStatus(ph); got != StatusHealthy {
		t.Fatalf("status %v, want healthy", got)
	}
}

func TestParseCursorExpiryWithoutTokenIsNotHealthy(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli-config.json")
	if err := os.WriteFile(path, []byte(`{"authInfo":{"email":"meta@example.com"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCursorExpiry(path); err == nil {
		t.Fatal("authInfo was treated as a cursor credential")
	}
}

func cursorTestJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." +
		base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}
