package identity

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestExtractFromGeminiConfig_AuthorizedUser(t *testing.T) {
	config := map[string]interface{}{
		"type":             "authorized_user",
		"email":            "user@example.com",
		"project_id":       "proj-123",
		"quota_project_id": "quota-456",
	}
	path := writeGeminiFile(t, config)

	identity, err := ExtractFromGeminiConfig(path)
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}
	if identity.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "user@example.com")
	}
	if identity.Organization != "proj-123" {
		t.Errorf("Organization = %q, want %q", identity.Organization, "proj-123")
	}
	if identity.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "gemini")
	}
}

func TestExtractFromGeminiConfig_ServiceAccount(t *testing.T) {
	config := map[string]interface{}{
		"type":         "service_account",
		"client_email": "sa@project.iam.gserviceaccount.com",
		"project_id":   "project-abc",
	}
	path := writeGeminiFile(t, config)

	identity, err := ExtractFromGeminiConfig(path)
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}
	if identity.Email != "sa@project.iam.gserviceaccount.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "sa@project.iam.gserviceaccount.com")
	}
	if identity.Organization != "project-abc" {
		t.Errorf("Organization = %q, want %q", identity.Organization, "project-abc")
	}
}

func TestExtractFromGeminiConfig_MissingFields(t *testing.T) {
	config := map[string]interface{}{
		"type": "authorized_user",
	}
	path := writeGeminiFile(t, config)

	identity, err := ExtractFromGeminiConfig(path)
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}
	if identity.Email != "" || identity.Organization != "" {
		t.Errorf("Expected empty fields, got %+v", identity)
	}
}

func TestExtractFromGeminiConfig_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "gemini.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0600); err != nil {
		t.Fatalf("write gemini.json: %v", err)
	}

	if _, err := ExtractFromGeminiConfig(path); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestExtractFromGeminiConfig_MissingFile(t *testing.T) {
	if _, err := ExtractFromGeminiConfig("/nonexistent/gemini.json"); err == nil {
		t.Fatal("expected error for missing file")
	}
}

func writeGeminiFile(t *testing.T, content map[string]interface{}) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "gemini.json")
	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal gemini config: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("write gemini.json: %v", err)
	}
	return path
}

// Fixture-based tests for Gemini identity extraction

func TestFixture_GeminiWithEmail(t *testing.T) {
	identity, err := ExtractFromGeminiConfig("testdata/gemini_with_email.json")
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}

	if identity.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "gemini")
	}
	if identity.Email != "gemini-user@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "gemini-user@example.com")
	}
	if identity.Organization != "my-project-123" {
		t.Errorf("Organization = %q, want %q", identity.Organization, "my-project-123")
	}
}

func TestFixture_GeminiNestedAccount(t *testing.T) {
	identity, err := ExtractFromGeminiConfig("testdata/gemini_nested_account.json")
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}

	if identity.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "gemini")
	}
	if identity.Email != "account@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "account@example.com")
	}
	if identity.Organization != "nested-project" {
		t.Errorf("Organization = %q, want %q", identity.Organization, "nested-project")
	}
}

func TestFixture_GeminiNestedUser(t *testing.T) {
	identity, err := ExtractFromGeminiConfig("testdata/gemini_nested_user.json")
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}

	if identity.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "gemini")
	}
	// user.email should be extracted
	if identity.Email != "user@example.com" {
		t.Errorf("Email = %q, want %q", identity.Email, "user@example.com")
	}
}

func TestFixture_GeminiNoEmail(t *testing.T) {
	identity, err := ExtractFromGeminiConfig("testdata/gemini_no_email.json")
	if err != nil {
		t.Fatalf("ExtractFromGeminiConfig error: %v", err)
	}

	if identity.Provider != "gemini" {
		t.Errorf("Provider = %q, want %q", identity.Provider, "gemini")
	}
	// Email should be empty
	if identity.Email != "" {
		t.Errorf("Email should be empty, got %q", identity.Email)
	}
	// Organization should be set from project_id
	if identity.Organization != "project-without-email" {
		t.Errorf("Organization = %q, want %q", identity.Organization, "project-without-email")
	}
}
