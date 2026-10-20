package authfile

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIsolatedOAuthSelectionRejectsAlternativeCredentials(t *testing.T) {
	for _, document := range []string{"settings.json", ".claude.json"} {
		for _, key := range []string{"apiKeyHelper", "apiKey", "api_key", "primaryApiKey", "oauthToken", "sessionKey", "forceLoginGatewayUrl"} {
			t.Run(document+"/"+key, func(t *testing.T) {
				for _, value := range []any{"credential-must-not-leak", " ", nil, 42, true, []string{"credential-must-not-leak"}, map[string]string{"secret": "credential-must-not-leak"}} {
					data, err := json.Marshal(map[string]any{key: value})
					if err != nil {
						t.Fatal(err)
					}
					files := map[string][]byte{document: data, ".credentials.json": []byte(`{"claudeAiOauth":{"accessToken":"cached-access","refreshToken":"cached-refresh"}}`)}
					err = validateIsolatedClaudeOAuthSelection(files)
					if err == nil || !strings.Contains(err.Error(), key) {
						t.Fatalf("%s with %T accepted or misdiagnosed: %v", key, value, err)
					}
					if strings.Contains(err.Error(), "credential-must-not-leak") {
						t.Fatal("selection error exposed a secret")
					}
				}
			})
		}
	}
}

func TestIsolatedOAuthSelectionRejectsEnvironmentOverrides(t *testing.T) {
	for _, key := range []string{
		"ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CONFIG_DIR",
		"CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_SESSION_ACCESS_TOKEN",
		"CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR", "ANTHROPIC_PROFILE", "ANTHROPIC_FEDERATION_RULE_ID",
		"ANTHROPIC_ORGANIZATION_ID", "ANTHROPIC_IDENTITY_TOKEN_FILE", "CLAUDE_CODE_USE_BEDROCK",
		"CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_MANTLE",
	} {
		t.Run(key, func(t *testing.T) {
			for _, spelling := range []string{key, strings.ToLower(key)} {
				for _, value := range []any{"do-not-print-this-token", "1", "true", "yes", " false ", nil, 1, false, []string{"do-not-print-this-token"}} {
					data, err := json.Marshal(map[string]any{"env": map[string]any{spelling: value}})
					if err != nil {
						t.Fatal(err)
					}
					for _, document := range []string{"settings.json", ".claude.json"} {
						err := validateIsolatedClaudeOAuthSelection(map[string][]byte{document: data})
						if err == nil || !strings.Contains(err.Error(), key) {
							t.Fatalf("override %s (%T) accepted or misdiagnosed: %v", spelling, value, err)
						}
						if strings.Contains(err.Error(), "do-not-print-this-token") {
							t.Fatal("environment diagnostic exposed secret")
						}
					}
				}
			}
		})
	}
}

func TestIsolatedOAuthSelectionAllowsUnrelatedAndDisabledSettings(t *testing.T) {
	for _, data := range []string{
		`{}`,
		`{"model":"opus","permissions":{"allow":["Read"]},"hooks":{},"env":{"OPENAI_API_KEY":"other-provider","AWS_REGION":"us-east-1","ANTHROPIC_MODEL":"model-override"}}`,
		`{"apiKeyHelper":"","apiKey":"","api_key":"","primaryApiKey":"","oauthToken":"","sessionKey":"","forceLoginGatewayUrl":"","forceLoginMethod":"claudeai"}`,
		`{"forceLoginMethod":"","env":{"ANTHROPIC_API_KEY":"","ANTHROPIC_AUTH_TOKEN":"","CLAUDE_CODE_OAUTH_TOKEN":""}}`,
		`{"oauthAccount":{"accountUuid":"id","emailAddress":"account@example.test"},"hasCompletedOnboarding":true,"futureUnknown":{"preserve":true}}`,
	} {
		for _, document := range []string{"settings.json", ".claude.json"} {
			files := map[string][]byte{document: []byte(data)}
			before := append([]byte(nil), files[document]...)
			if err := validateIsolatedClaudeOAuthSelection(files); err != nil {
				t.Fatalf("valid %s rejected: %v", document, err)
			}
			if len(files) != 1 || !bytes.Equal(before, files[document]) {
				t.Fatal("validation changed captured settings")
			}
		}
	}
	for _, key := range []string{"CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_MANTLE"} {
		for _, disabled := range []string{"", "0", "false", "FALSE", "no", "No", "off", "OFF"} {
			data, err := json.Marshal(map[string]any{"env": map[string]string{key: disabled}})
			if err != nil {
				t.Fatal(err)
			}
			if err := validateIsolatedClaudeOAuthSelection(map[string][]byte{"settings.json": data}); err != nil {
				t.Fatalf("disabled %s rejected: %v", key, err)
			}
		}
	}
	if err := validateIsolatedClaudeOAuthSelection(nil); err != nil {
		t.Fatalf("absent optional settings: %v", err)
	}
}

func TestIsolatedOAuthSelectionRejectsAmbiguousDocumentsAndMethods(t *testing.T) {
	for _, data := range []string{
		"", "null", "[]", "{", `{} trailing`,
		`{"env":null}`, `{"env":[]}`, `{"env":"secret-must-not-leak"}`,
		`{"forceLoginMethod":"console"}`, `{"forceLoginMethod":"gateway"}`, `{"forceLoginMethod":"future-method"}`,
		`{"forceLoginMethod":null}`, `{"forceLoginMethod":42}`, `{"forceLoginMethod":["secret-must-not-leak"]}`,
		`{"env":{"ANTHROPIC_API_KEY":"","anthropic_api_key":"secret-must-not-leak"}}`,
	} {
		err := validateIsolatedClaudeOAuthSelection(map[string][]byte{"settings.json": []byte(data)})
		if err == nil {
			t.Fatalf("ambiguous selection accepted: %s", data)
		}
		if strings.Contains(err.Error(), "secret-must-not-leak") {
			t.Fatal("malformed settings exposed secret")
		}
	}
}

func TestIsolatedOAuthSelectionDoesNotConsultAmbientCredentials(t *testing.T) {
	for _, key := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_OAUTH_TOKEN"} {
		t.Setenv(key, "ambient-credential-must-not-be-read")
	}
	if err := validateIsolatedClaudeOAuthSelection(map[string][]byte{"settings.json": []byte(`{"model":"opus"}`)}); err != nil {
		t.Fatalf("ambient state changed source selection: %v", err)
	}
}

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
