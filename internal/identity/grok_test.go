package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// NOTE: all fixtures are SYNTHETIC and mirror only the SHAPE of Grok Build's
// auth.json; no real tokens or accounts appear here.

func writeGrokFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

// Observed shape (Grok CLI 0.1.210): "<issuer>::<client-id>" top-level key.
func TestExtractFromGrokAuth_ObservedShape(t *testing.T) {
	path := writeGrokFixture(t, `{
		"https://auth.x.ai::00000000-0000-0000-0000-000000000000": {
			"key": "SYNTHETIC-NOT-REAL",
			"auth_mode": "sso",
			"email": "grok-tester@example.com",
			"user_id": "synthetic-user-1",
			"refresh_token": "SYNTHETIC-REFRESH",
			"expires_at": "2099-01-01T00:00:00Z"
		}
	}`)

	id, err := ExtractFromGrokAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromGrokAuth: %v", err)
	}
	if id.Email != "grok-tester@example.com" {
		t.Errorf("Email = %q, want grok-tester@example.com", id.Email)
	}
	if id.AccountID != "synthetic-user-1" {
		t.Errorf("AccountID = %q, want synthetic-user-1", id.AccountID)
	}
}

// Docs shape: the CLI's bundled docs use "https://accounts.x.ai/sign-in" as
// the top-level key, so keys must be treated as opaque.
func TestExtractFromGrokAuth_DocsShape(t *testing.T) {
	path := writeGrokFixture(t, `{
		"https://accounts.x.ai/sign-in": {
			"key": "SYNTHETIC-NOT-REAL",
			"email": "docs-shape@example.com"
		}
	}`)

	id, err := ExtractFromGrokAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromGrokAuth: %v", err)
	}
	if id.Email != "docs-shape@example.com" {
		t.Errorf("Email = %q, want docs-shape@example.com", id.Email)
	}
}

func TestExtractFromGrokAuth_UserIDOnly(t *testing.T) {
	path := writeGrokFixture(t, `{
		"https://auth.x.ai::00000000-0000-0000-0000-000000000000": {
			"key": "SYNTHETIC-NOT-REAL",
			"user_id": "synthetic-user-2"
		}
	}`)

	id, err := ExtractFromGrokAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromGrokAuth: %v", err)
	}
	if id.AccountID != "synthetic-user-2" {
		t.Errorf("AccountID = %q, want synthetic-user-2", id.AccountID)
	}
	if id.Email != "synthetic-user-2" {
		t.Errorf("Email display fallback = %q, want synthetic-user-2", id.Email)
	}
}

// Forward compatibility: flat top-level fields win if a future CLI version
// flattens the file.
func TestExtractFromGrokAuth_FlatShape(t *testing.T) {
	path := writeGrokFixture(t, `{"email":"flat@example.com","user_id":"flat-user"}`)

	id, err := ExtractFromGrokAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromGrokAuth: %v", err)
	}
	if id.Email != "flat@example.com" {
		t.Errorf("Email = %q, want flat@example.com", id.Email)
	}
}

// With multiple credential entries, an entry with an email is preferred over
// one with only a user_id, scanning keys in sorted order.
func TestExtractFromGrokAuth_PrefersEmailAcrossEntries(t *testing.T) {
	path := writeGrokFixture(t, `{
		"https://auth.x.ai::aaaaaaaa-0000-0000-0000-000000000000": {
			"key": "SYNTHETIC-1",
			"user_id": "id-only-user"
		},
		"https://auth.x.ai::bbbbbbbb-0000-0000-0000-000000000000": {
			"key": "SYNTHETIC-2",
			"email": "second@example.com"
		}
	}`)

	id, err := ExtractFromGrokAuth(path)
	if err != nil {
		t.Fatalf("ExtractFromGrokAuth: %v", err)
	}
	if id.Email != "second@example.com" {
		t.Errorf("Email = %q, want second@example.com (email preferred over user_id)", id.Email)
	}
}

func TestExtractFromGrokAuth_NoIdentity(t *testing.T) {
	path := writeGrokFixture(t, `{"https://auth.x.ai::x":{"key":"SYNTHETIC-ONLY-TOKEN"}}`)
	if _, err := ExtractFromGrokAuth(path); err == nil {
		t.Fatal("expected error for auth file with no identity fields")
	}
}

func TestExtractFromGrokAuth_InvalidJSON(t *testing.T) {
	path := writeGrokFixture(t, `not json`)
	if _, err := ExtractFromGrokAuth(path); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractFromGrokAuth_MissingFile(t *testing.T) {
	if _, err := ExtractFromGrokAuth(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for missing file")
	}
}
