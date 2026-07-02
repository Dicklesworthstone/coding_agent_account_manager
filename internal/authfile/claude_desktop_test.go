package authfile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeJSON is a small helper for these tests.
func writeJSONT(t *testing.T, path string, v map[string]interface{}) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func readJSONT(t *testing.T, path string) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return m
}

func desktopPath(home string) string {
	return filepath.Join(home, "Library", "Application Support", "Claude", "config.json")
}

// TestClaudeDesktopTokenCacheDetection verifies token presence gating.
func TestClaudeDesktopTokenCacheDetection(t *testing.T) {
	home := t.TempDir()
	p := desktopPath(home)

	// Absent -> not ok, no error.
	if _, ok, err := claudeDesktopTokenCache(p); err != nil || ok {
		t.Fatalf("absent config: ok=%v err=%v", ok, err)
	}

	// Present but no token cache -> not ok.
	writeJSONT(t, p, map[string]interface{}{"theme": "dark", "windowBounds": "x"})
	if _, ok, err := claudeDesktopTokenCache(p); err != nil || ok {
		t.Fatalf("token-less config: ok=%v err=%v", ok, err)
	}

	// With a token cache -> ok, and only the token field is extracted.
	writeJSONT(t, p, map[string]interface{}{
		"theme":              "dark",
		"oauth:tokenCacheV2": "ENC-BLOB-A",
	})
	fields, ok, err := claudeDesktopTokenCache(p)
	if err != nil || !ok {
		t.Fatalf("token config: ok=%v err=%v", ok, err)
	}
	if len(fields) != 1 || fields["oauth:tokenCacheV2"] != "ENC-BLOB-A" {
		t.Fatalf("extracted fields wrong: %v", fields)
	}
}

// TestClaudeDesktopBackupRestoreRoundTrip proves the core PR #44 fix: swapping
// profiles updates the desktop token cache to the activated account while
// preserving unrelated desktop settings, and never persists those settings.
func TestClaudeDesktopBackupRestoreRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Neutralize CLAUDE_CONFIG_DIR/XDG so ClaudeAuthFiles is deterministic.
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".config", "claude-code"))

	vault := NewVault(filepath.Join(t.TempDir(), "vault"))
	fs := ClaudeAuthFiles()

	// Required primary credential must exist for backup to succeed.
	writeCreds := func(token string) {
		p := filepath.Join(home, ".claude", ".credentials.json")
		writeJSONT(t, p, map[string]interface{}{
			"claudeAiOauth": map[string]interface{}{"accessToken": token, "refreshToken": token},
		})
	}

	// --- Account A: desktop config holds A's cache + an unrelated setting.
	writeCreds("A-TOKEN")
	desktop := desktopPath(home)
	writeJSONT(t, desktop, map[string]interface{}{
		"userTheme":          "solarized", // unrelated desktop setting
		"oauth:tokenCacheV2": "ENC-A",
	})
	if err := vault.Backup(fs, "alice"); err != nil {
		t.Fatalf("backup alice: %v", err)
	}

	// The vault snapshot must contain ONLY the token field, not userTheme.
	snap := readJSONT(t, filepath.Join(vault.ProfilePath("claude", "alice"), "config.json"))
	if snap["oauth:tokenCacheV2"] != "ENC-A" {
		t.Fatalf("snapshot missing token: %v", snap)
	}
	if _, leaked := snap["userTheme"]; leaked {
		t.Fatalf("snapshot leaked unrelated desktop setting: %v", snap)
	}

	// --- Account B: sign in as B, desktop cache becomes B; back up B.
	writeCreds("B-TOKEN")
	writeJSONT(t, desktop, map[string]interface{}{
		"userTheme":          "solarized",
		"oauth:tokenCacheV2": "ENC-B",
	})
	if err := vault.Backup(fs, "bob"); err != nil {
		t.Fatalf("backup bob: %v", err)
	}

	// Now the live desktop cache is B. Simulate the reported bug surface: user
	// wants A back. Restore alice -> desktop cache must flip to A, userTheme kept.
	if err := vault.Restore(fs, "alice"); err != nil {
		t.Fatalf("restore alice: %v", err)
	}
	live := readJSONT(t, desktop)
	if live["oauth:tokenCacheV2"] != "ENC-A" {
		t.Fatalf("restore did not flip desktop cache to A: %v", live)
	}
	if live["userTheme"] != "solarized" {
		t.Fatalf("restore clobbered unrelated desktop setting: %v", live)
	}

	// --- Clear (logout): only the token cache is scrubbed, userTheme survives.
	if err := ClearAuthFiles(fs); err != nil {
		t.Fatalf("clear: %v", err)
	}
	live = readJSONT(t, desktop)
	if _, ok := live["oauth:tokenCacheV2"]; ok {
		t.Fatalf("clear did not scrub token cache: %v", live)
	}
	if live["userTheme"] != "solarized" {
		t.Fatalf("clear destroyed unrelated desktop setting: %v", live)
	}
}

// TestClaudeDesktopActiveProfileHashIgnoresSettings proves the hash keys on the
// token cache only, so unrelated desktop settings don't perturb detection.
func TestClaudeDesktopActiveProfileHashIgnoresSettings(t *testing.T) {
	home := t.TempDir()
	p := desktopPath(home)

	writeJSONT(t, p, map[string]interface{}{"userTheme": "a", "oauth:tokenCache": "ENC"})
	h1, err := stableFileHash("claude", p)
	if err != nil {
		t.Fatal(err)
	}
	writeJSONT(t, p, map[string]interface{}{"userTheme": "b", "extra": 42, "oauth:tokenCache": "ENC"})
	h2, err := stableFileHash("claude", p)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("hash changed with unrelated settings: %s != %s", h1, h2)
	}
	writeJSONT(t, p, map[string]interface{}{"userTheme": "b", "oauth:tokenCache": "DIFFERENT"})
	h3, err := stableFileHash("claude", p)
	if err != nil {
		t.Fatal(err)
	}
	if h3 == h1 {
		t.Fatalf("hash did not change when token cache changed")
	}
}
