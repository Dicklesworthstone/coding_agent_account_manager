package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractFromCursorAuth_AuthInfoEmail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli-config.json")
	// Synthetic. No real account, token, or auth id.
	body := []byte(`{"authInfo":{"email":"cursor-tester@example.com","displayName":"Tester"},"version":1}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := ExtractFromCursorAuth(path)
	if err != nil {
		t.Fatal(err)
	}
	if id.Email != "cursor-tester@example.com" {
		t.Fatalf("email = %q", id.Email)
	}
}

func TestExtractFromCursorAuth_MetadataWithoutEmailIsNotAnIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cli-config.json")
	body := []byte(`{"authInfo":{"displayName":"Tester"},"permissions":{"allow":[]}}`)
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractFromCursorAuth(path); err == nil {
		t.Fatal("expected no identity from a display name alone")
	}
}
