//go:build agy_live

// Package authfile live e2e test for the Antigravity (agy) provider.
//
// This test is GUARDED by the `agy_live` build tag so it NEVER runs in normal
// `go test ./...` or CI. Run it explicitly only on a machine with a real,
// authenticated agy install:
//
//	go test -tags agy_live -run TestE2E_AgyLiveBackupRestore -v ./internal/authfile/
//
// Safety guarantees:
//   - It NEVER mutates the real ~/.gemini credentials. The real token file is
//     read once to compute a SHA-256 hash + length; it is then copied (by the
//     vault) into a throwaway temp directory and restored into ANOTHER throwaway
//     temp directory. The real on-disk creds are left untouched.
//   - It NEVER prints raw credential bytes. Logging is limited to file lengths,
//     SHA-256 hashes, and the active account email.
package authfile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func liveSHA256(t *testing.T, path string) (string, int64) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:]), int64(len(data))
}

func liveActiveEmail(t *testing.T, accountsPath string) string {
	t.Helper()
	data, err := os.ReadFile(accountsPath)
	if err != nil {
		return ""
	}
	var parsed struct {
		Active string `json:"active"`
	}
	if json.Unmarshal(data, &parsed) != nil {
		return ""
	}
	return parsed.Active
}

func TestE2E_AgyLiveBackupRestore(t *testing.T) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot determine home dir: %v", err)
	}
	gemHome := filepath.Join(homeDir, ".gemini")
	if v := os.Getenv("GEMINI_HOME"); v != "" {
		gemHome = v
	}

	realToken := filepath.Join(gemHome, "antigravity-cli", "antigravity-oauth-token")
	if _, err := os.Stat(realToken); err != nil {
		t.Skipf("no live agy token at %s (run on an authenticated agy box): %v", realToken, err)
	}

	// Compute identity-safe metadata for the LIVE files (hashes + lengths only).
	realAccounts := filepath.Join(gemHome, "google_accounts.json")
	realCreds := filepath.Join(gemHome, "oauth_creds.json")

	tokHash, tokLen := liveSHA256(t, realToken)
	t.Logf("LIVE token: len=%d sha256=%s (bytes NEVER printed)", tokLen, tokHash)
	t.Logf("LIVE active account: %s", liveActiveEmail(t, realAccounts))

	origHashes := map[string]string{
		"antigravity-oauth-token": tokHash,
	}
	if _, err := os.Stat(realAccounts); err == nil {
		h, l := liveSHA256(t, realAccounts)
		origHashes["google_accounts.json"] = h
		t.Logf("LIVE google_accounts.json: len=%d sha256=%s", l, h)
	}
	if _, err := os.Stat(realCreds); err == nil {
		h, l := liveSHA256(t, realCreds)
		origHashes["oauth_creds.json"] = h
		t.Logf("LIVE oauth_creds.json: len=%d sha256=%s", l, h)
	}

	// Backup the LIVE creds into a throwaway vault. (Backup only READS the real
	// files; it copies them into the temp vault dir.)
	vaultDir := t.TempDir()
	v := NewVault(vaultDir)
	fs := AntigravityAuthFiles() // points at the real GEMINI_HOME
	if err := v.Backup(fs, "_live_e2e"); err != nil {
		t.Fatalf("Backup of live creds failed: %v", err)
	}
	t.Logf("backed up live creds to throwaway vault: %s", vaultDir)

	// Build a restore file set that targets a DIFFERENT throwaway directory, so
	// we never overwrite the real creds during restore.
	restoreHome := t.TempDir()
	restoreAntigravity := filepath.Join(restoreHome, "antigravity-cli")
	if err := os.MkdirAll(restoreAntigravity, 0700); err != nil {
		t.Fatal(err)
	}
	restoreFS := AuthFileSet{
		Tool: "agy",
		Files: []AuthFileSpec{
			{Tool: "agy", Path: filepath.Join(restoreAntigravity, "antigravity-oauth-token"), Required: true},
			{Tool: "agy", Path: filepath.Join(restoreHome, "google_accounts.json"), Required: false},
			{Tool: "agy", Path: filepath.Join(restoreHome, "oauth_creds.json"), Required: false},
			{Tool: "agy", Path: filepath.Join(restoreAntigravity, "settings.json"), Required: false},
		},
	}

	if err := v.Restore(restoreFS, "_live_e2e"); err != nil {
		t.Fatalf("Restore to throwaway location failed: %v", err)
	}

	// Verify byte-identity via hashes (never print bytes).
	for base, wantHash := range origHashes {
		var p string
		switch base {
		case "antigravity-oauth-token", "settings.json":
			p = filepath.Join(restoreAntigravity, base)
		default:
			p = filepath.Join(restoreHome, base)
		}
		gotHash, gotLen := liveSHA256(t, p)
		if gotHash != wantHash {
			t.Errorf("restored %s NOT byte-identical: want sha256=%s got sha256=%s", base, wantHash, gotHash)
		} else {
			t.Logf("restored %s OK: len=%d sha256=%s (byte-identical)", base, gotLen, gotHash)
		}
		info, _ := os.Stat(p)
		if info.Mode().Perm() != 0600 {
			t.Errorf("restored %s perms = %o, want 0600", base, info.Mode().Perm())
		}
	}

	// Final safety assertion: the REAL token is unchanged (same hash as start).
	finalHash, _ := liveSHA256(t, realToken)
	if finalHash != tokHash {
		t.Fatalf("SAFETY VIOLATION: real live token hash changed during test")
	}
	t.Log("verified: real live token left untouched")
}
