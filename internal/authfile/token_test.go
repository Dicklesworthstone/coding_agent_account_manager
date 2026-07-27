package authfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestVault(t *testing.T) *Vault {
	t.Helper()
	return NewVault(t.TempDir())
}

func TestSaveAndReadTokenProfile(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "burst", "sk-ant-oat01-test-token-value", "/some/source"); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	token, meta, err := v.ReadTokenProfile("claude", "burst")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "sk-ant-oat01-test-token-value" {
		t.Errorf("token = %q, want stored value", token)
	}
	if meta.Type != ProfileTypeToken {
		t.Errorf("meta.Type = %q, want %q", meta.Type, ProfileTypeToken)
	}
	if meta.Provider != "claude" || meta.Name != "burst" {
		t.Errorf("meta = %+v, want provider/name set", meta)
	}
	if meta.Source != "/some/source" {
		t.Errorf("meta.Source = %q, want %q", meta.Source, "/some/source")
	}
	if meta.CreatedAt.IsZero() {
		t.Error("meta.CreatedAt is zero")
	}
}

func TestSaveTokenProfile_Permissions(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "burst", "sk-ant-oat01-abc", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	tokenPath := filepath.Join(v.BasePath(), "claude", "burst", TokenFileName)
	info, err := os.Stat(tokenPath)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("token file mode = %o, want 0600", perm)
	}

	metaPath := filepath.Join(v.BasePath(), "claude", "burst", TokenMetaFileName)
	info, err = os.Stat(metaPath)
	if err != nil {
		t.Fatalf("stat meta file: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("meta file mode = %o, want 0600", perm)
	}
}

func TestSaveTokenProfile_TrimsWhitespace(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "burst", "  sk-ant-oat01-abc\n", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	token, _, err := v.ReadTokenProfile("claude", "burst")
	if err != nil {
		t.Fatalf("ReadTokenProfile error: %v", err)
	}
	if token != "sk-ant-oat01-abc" {
		t.Errorf("token = %q, want trimmed", token)
	}
}

func TestSaveTokenProfile_EmptyToken(t *testing.T) {
	v := newTestVault(t)
	if err := v.SaveTokenProfile("claude", "burst", "   \n", ""); err == nil {
		t.Error("expected error for empty token")
	}
}

func TestSaveTokenProfile_InvalidName(t *testing.T) {
	v := newTestVault(t)
	if err := v.SaveTokenProfile("claude", "../escape", "sk-ant-oat01-abc", ""); err == nil {
		t.Error("expected error for path-traversal profile name")
	}
}

func TestIsTokenProfile(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "tok", "sk-ant-oat01-abc", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}

	// A file-swap profile: plain dir with an auth file, no meta.json.
	fileDir := filepath.Join(v.BasePath(), "claude", "files")
	if err := os.MkdirAll(fileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fileDir, ".credentials.json"), []byte("{}"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if !v.IsTokenProfile("claude", "tok") {
		t.Error("IsTokenProfile(tok) = false, want true")
	}
	if v.IsTokenProfile("claude", "files") {
		t.Error("IsTokenProfile(files) = true, want false")
	}
	if v.IsTokenProfile("claude", "missing") {
		t.Error("IsTokenProfile(missing) = true, want false")
	}
}

func TestListTokenProfiles_MixedVault(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "burst", "sk-ant-oat01-a", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := v.SaveTokenProfile("claude", "mcc22", "sk-ant-oat01-b", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	fileDir := filepath.Join(v.BasePath(), "claude", "swap")
	if err := os.MkdirAll(fileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	tokens, err := v.ListTokenProfiles("claude")
	if err != nil {
		t.Fatalf("ListTokenProfiles error: %v", err)
	}
	if len(tokens) != 2 {
		t.Fatalf("ListTokenProfiles = %v, want 2 entries", tokens)
	}

	// Token profiles must also appear in the regular List (first-class).
	all, err := v.List("claude")
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("List = %v, want 3 entries (2 token + 1 file)", all)
	}
}

func TestActiveTokenProfile_RoundTrip(t *testing.T) {
	v := newTestVault(t)

	if got, err := v.ActiveTokenProfile("claude"); err != nil || got != "" {
		t.Fatalf("ActiveTokenProfile on empty vault = %q, %v; want \"\", nil", got, err)
	}

	if err := v.SaveTokenProfile("claude", "burst", "sk-ant-oat01-a", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := v.SetActiveTokenProfile("claude", "burst"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}

	got, err := v.ActiveTokenProfile("claude")
	if err != nil {
		t.Fatalf("ActiveTokenProfile error: %v", err)
	}
	if got != "burst" {
		t.Errorf("ActiveTokenProfile = %q, want %q", got, "burst")
	}

	if err := v.ClearActiveTokenProfile("claude"); err != nil {
		t.Fatalf("ClearActiveTokenProfile error: %v", err)
	}
	if got, _ := v.ActiveTokenProfile("claude"); got != "" {
		t.Errorf("ActiveTokenProfile after clear = %q, want \"\"", got)
	}
	// Clearing again must be a no-op, not an error.
	if err := v.ClearActiveTokenProfile("claude"); err != nil {
		t.Errorf("ClearActiveTokenProfile (already clear) error: %v", err)
	}
}

func TestSetActiveTokenProfile_RejectsNonToken(t *testing.T) {
	v := newTestVault(t)

	fileDir := filepath.Join(v.BasePath(), "claude", "swap")
	if err := os.MkdirAll(fileDir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := v.SetActiveTokenProfile("claude", "swap"); err == nil {
		t.Error("expected error for non-token profile")
	}
}

func TestActiveTokenProfile_StaleMarker(t *testing.T) {
	v := newTestVault(t)

	if err := v.SaveTokenProfile("claude", "gone", "sk-ant-oat01-a", ""); err != nil {
		t.Fatalf("SaveTokenProfile error: %v", err)
	}
	if err := v.SetActiveTokenProfile("claude", "gone"); err != nil {
		t.Fatalf("SetActiveTokenProfile error: %v", err)
	}
	if err := v.DeleteForce("claude", "gone"); err != nil {
		t.Fatalf("DeleteForce error: %v", err)
	}

	// Marker points at a deleted profile: treated as unset.
	if got, err := v.ActiveTokenProfile("claude"); err != nil || got != "" {
		t.Errorf("ActiveTokenProfile with stale marker = %q, %v; want \"\", nil", got, err)
	}
}

func TestTokenEnv_Claude(t *testing.T) {
	env, err := TokenEnv("claude", "burst", "sk-ant-oat01-a")
	if err != nil {
		t.Fatalf("TokenEnv error: %v", err)
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "sk-ant-oat01-a" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".claude-burst")
	if env["CLAUDE_CONFIG_DIR"] != want {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want %q", env["CLAUDE_CONFIG_DIR"], want)
	}
}

func TestTokenEnv_UnsupportedTool(t *testing.T) {
	if _, err := TokenEnv("codex", "x", "tok"); err == nil {
		t.Error("expected error for unsupported tool")
	}
}

func TestValidateTokenFormat(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		token   string
		wantErr bool
	}{
		{"claude oat token", "claude", "sk-ant-oat01-" + strings.Repeat("x", 40), false},
		{"claude api key shape", "claude", "sk-ant-api03-" + strings.Repeat("x", 40), false},
		{"claude wrong prefix", "claude", "ghp_" + strings.Repeat("x", 40), true},
		{"claude too short", "claude", "sk-ant-x", true},
		{"empty", "claude", "  ", true},
		{"unknown tool passes non-empty", "otherx", "anything-goes", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateTokenFormat(tt.tool, tt.token)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateTokenFormat(%s, ...) error = %v, wantErr %v", tt.tool, err, tt.wantErr)
			}
		})
	}
}
