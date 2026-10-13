package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedClaudeMetadataRequiresSelectedOAuth(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claude", "source")
	for _, mode := range []any{"oauth", "api-key", "vertex-adc", "device-code", "", "OAuth", " oauth ", nil, 42, true, map[string]string{"secret": "do-not-print"}} {
		metadata := map[string]any{
			"provider": "claude", "name": "source", "base_path": root, "auth_mode": mode,
			"metadata": map[string]string{"future_option": "retained"},
		}
		data, err := json.Marshal(metadata)
		if err != nil {
			t.Fatal(err)
		}
		err = validateIsolatedClaudeMetadata(data, root)
		wantOK := mode == any("oauth")
		if (err == nil) != wantOK {
			t.Fatalf("mode %v: error=%v, want success=%v", mode, err, wantOK)
		}
		if err != nil && strings.Contains(err.Error(), "do-not-print") {
			t.Fatal("metadata rejection exposed source data")
		}
	}
}

func TestIsolatedClaudeMetadataRejectsMissingModeAndRedirects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "claude", "source")
	for _, bad := range []map[string]any{
		{"provider": "claude", "name": "source"},
		{"provider": "codex", "name": "source", "auth_mode": "oauth"},
		{"provider": "claude", "name": "other", "auth_mode": "oauth"},
		{"provider": "claude", "name": "source", "auth_mode": "oauth", "base_path": filepath.Dir(root)},
		{"provider": "claude", "name": "source", "auth_mode": "oauth", "base_path": false},
	} {
		data, err := json.Marshal(bad)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateIsolatedClaudeMetadata(data, root); err == nil {
			t.Fatal("metadata with missing selection, wrong identity, or redirected root accepted")
		}
	}
	for _, data := range []string{"null", "[]", "{}", "{", `{"provider":"claude","name":"source","auth_mode":"oauth"} trailing`} {
		if err := validateIsolatedClaudeMetadata([]byte(data), root); err == nil {
			t.Fatal("invalid metadata document accepted")
		}
	}
	// Unknown optional fields are allowed; the selected mode is not optional.
	valid := []byte(`{"provider":"claude","name":"source","auth_mode":"oauth","future":{"setting":true}}`)
	if err := validateIsolatedClaudeMetadata(valid, root); err != nil {
		t.Fatal(err)
	}
	for _, badRoot := range []string{"", "relative/source"} {
		if err := validateIsolatedClaudeMetadata(valid, badRoot); err == nil {
			t.Fatal("non-absolute store root accepted")
		}
	}
}

func TestIsolatedMetadataReadAndValidationAreReadOnly(t *testing.T) {
	root := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "profile.json")
	data := []byte(`{"provider":"claude","name":"source","auth_mode":"api-key"}`)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	source, err := readIsolatedSourceFile(path, true, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateIsolatedClaudeMetadata(source.data, root); err == nil {
		t.Fatal("API-key source accepted")
	}
	if err := source.unchanged(); err != nil {
		t.Fatalf("failed validation changed source: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "profile.json" {
		t.Fatalf("validation created source files: %v", err)
	}
}
