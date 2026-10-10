package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

func grokSyncAuth(t *testing.T, token string, expires time.Time) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"https://auth.x.ai::client": map[string]any{
		"key": token, "refresh_token": "r-" + token, "user_id": "account-a",
		"email": "alice@example.test", "expires_at": expires.UTC().Format(time.RFC3339),
	}})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestSyncGrokVaultFromLiveMakesStaleProfileUsable(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	t.Setenv("GROK_HOME", "")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	live := grokSyncAuth(t, "live", time.Now().Add(5*time.Hour))
	write(filepath.Join(home, ".grok", "auth.json"), live)
	vault := authfile.NewVault(filepath.Join(root, "vault"))
	vaultAuth := filepath.Join(vault.ProfilePath("grok", "live"), "auth.json")
	write(vaultAuth, grokSyncAuth(t, "stale", time.Now().Add(-time.Hour)))
	write(filepath.Join(vault.ProfilePath("grok", "live"), "meta.json"), []byte(`{"type":"user"}`))

	var warn bytes.Buffer
	if n := syncGrokVaultFromLive(context.Background(), vault, &warn); n != 1 {
		t.Fatalf("synced = %d (warnings: %q); want 1", n, warn.String())
	}
	info, err := health.ParseGrokExpiry(vaultAuth)
	if err != nil || !info.ExpiresAt.After(time.Now().Add(time.Hour)) {
		t.Fatalf("vault expiry after sync = %v, %v; want a fresh credential", info, err)
	}
	got, _ := os.ReadFile(vaultAuth)
	if !bytes.Equal(got, live) {
		t.Fatal("vault credential does not equal the live credential")
	}
}
