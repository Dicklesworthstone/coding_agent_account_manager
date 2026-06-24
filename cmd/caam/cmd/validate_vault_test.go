package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
)

// writeClaudeVaultProfile writes a Claude .credentials.json into the vault for
// the given profile, with the given expiry offset and refresh-token presence.
func writeClaudeVaultProfile(t *testing.T, name string, expiresIn time.Duration, withRefresh bool) {
	t.Helper()
	dir := vault.ProfilePath("claude", name)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir vault profile: %v", err)
	}
	oauth := map[string]any{
		"accessToken": "access",
		"expiresAt":   time.Now().Add(expiresIn).UnixMilli(),
	}
	if withRefresh {
		oauth["refreshToken"] = "refresh"
	}
	payload, _ := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), payload, 0600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
}

// runValidateJSON invokes validate for the given tool and returns the parsed
// results plus the error returned to the caller.
func runValidateJSON(t *testing.T, tool string) ([]ValidationOutput, error) {
	t.Helper()

	// Capture stdout (outputJSON writes via fmt.Println to os.Stdout).
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	validateJSON = true
	t.Cleanup(func() { validateJSON = false })

	var args []string
	if tool != "" {
		args = []string{tool}
	}
	err := runValidate(validateCmd, args)

	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, rerr := r.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if rerr != nil {
			break
		}
	}

	var results []ValidationOutput
	if jerr := json.Unmarshal(buf, &results); jerr != nil {
		t.Fatalf("validate output not valid JSON: %v; raw=%q", jerr, string(buf))
	}
	return results, err
}

// TestValidate_UsesVaultBackedProfiles verifies issue #23: validate reads the
// vault (the same source as backup/activate/ls), and issue #22: a refreshable
// expired access token is reported valid while a hard-expired credential
// without a refresh token is reported invalid.
func TestValidate_UsesVaultBackedProfiles(t *testing.T) {
	tmp := t.TempDir()
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmp, "vault"))
	t.Cleanup(func() { vault = oldVault })

	// healthy: valid token, not expiring soon
	writeClaudeVaultProfile(t, "healthy", 48*time.Hour, true)
	// refreshable: access token expired but refresh token present
	writeClaudeVaultProfile(t, "refreshable", -1*time.Hour, true)
	// dead: expired and no refresh token
	writeClaudeVaultProfile(t, "dead", -1*time.Hour, false)
	// system profile must be skipped
	writeClaudeVaultProfile(t, "_original", 48*time.Hour, true)

	results, _ := runValidateJSON(t, "claude")

	got := map[string]ValidationOutput{}
	for _, r := range results {
		got[r.Profile] = r
	}

	if _, ok := got["_original"]; ok {
		t.Errorf("validate should skip system profiles, but included _original")
	}

	for _, name := range []string{"healthy", "refreshable", "dead"} {
		if _, ok := got[name]; !ok {
			t.Fatalf("validate did not include vault profile %q; got %+v", name, results)
		}
	}

	if !got["healthy"].Valid {
		t.Errorf("healthy profile should be valid: %+v", got["healthy"])
	}
	if !got["refreshable"].Valid {
		t.Errorf("refreshable (expired access + refresh token) profile should be valid: %+v", got["refreshable"])
	}
	if got["dead"].Valid {
		t.Errorf("hard-expired profile without refresh token should be invalid: %+v", got["dead"])
	}
}

// TestValidate_EmptyEncodesAsArray verifies issue #23's requirement that empty
// results encode as [] (not null) for easier agent parsing.
func TestValidate_EmptyEncodesAsArray(t *testing.T) {
	tmp := t.TempDir()
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(tmp, "empty-vault"))
	t.Cleanup(func() { vault = oldVault })

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	validateJSON = true
	t.Cleanup(func() { validateJSON = false })

	if err := runValidate(validateCmd, []string{"codex"}); err != nil {
		t.Fatalf("runValidate error: %v", err)
	}
	_ = w.Close()
	os.Stdout = oldStdout

	buf := make([]byte, 0, 256)
	tmpBuf := make([]byte, 256)
	for {
		n, rerr := r.Read(tmpBuf)
		if n > 0 {
			buf = append(buf, tmpBuf[:n]...)
		}
		if rerr != nil {
			break
		}
	}

	out := string(buf)
	if got := fmt.Sprintf("%s", out); got == "null\n" || got == "null" {
		t.Errorf("empty validate result should encode as [], got %q", out)
	}
}
