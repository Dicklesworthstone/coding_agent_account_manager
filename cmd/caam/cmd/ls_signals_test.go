package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/spf13/cobra"
)

// Issue #102, end to end: three live Codex profiles read `warning` in
// `caam ls` from an access-token expiry months in the past that the Codex CLI
// renews on next use. The verdict must be healthy, and the JSON must carry the
// three signals a controller routes on.
func TestLsJSONCarriesCredentialSignals(t *testing.T) {
	origVault, origTools, origStore := vault, tools, profileStore
	t.Cleanup(func() { vault, tools, profileStore = origVault, origTools, origStore })

	vaultDir := filepath.Join(t.TempDir(), "vault")
	vault = authfile.NewVault(vaultDir)
	profileStore = nil // no isolated profiles: read the vault snapshot

	tools = map[string]func() authfile.AuthFileSet{
		"codex": func() authfile.AuthFileSet {
			return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
		},
	}

	write := func(name string, body map[string]any) {
		dir := filepath.Join(vaultDir, "codex", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lapsed := time.Now().Add(-40 * 24 * time.Hour).Unix()
	// The reported shape: a long-expired access token beside a refresh token.
	write("live", map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": lapsed})
	// The genuinely dead shape: nothing to renew from.
	write("dead", map[string]any{"access_token": "a", "expires_at": lapsed})

	cmd := &cobra.Command{RunE: runLs}
	cmd.Flags().Bool("no-color", false, "")
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().String("tag", "", "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runLs(cmd, []string{"codex"}); err != nil {
		t.Fatalf("runLs: %v", err)
	}

	var out struct {
		Profiles []struct {
			Name   string `json:"name"`
			Health struct {
				Status        string `json:"status"`
				RefreshDue    *bool  `json:"refresh_due"`
				LaunchUsable  *bool  `json:"launch_usable"`
				LoginRequired *bool  `json:"login_required"`
			} `json:"health"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	got := map[string]struct {
		status                 string
		refresh, launch, login *bool
	}{}
	for _, p := range out.Profiles {
		got[p.Name] = struct {
			status                 string
			refresh, launch, login *bool
		}{p.Health.Status, p.Health.RefreshDue, p.Health.LaunchUsable, p.Health.LoginRequired}
	}

	live, ok := got["live"]
	if !ok {
		t.Fatalf("profile 'live' missing from %q", buf.String())
	}
	if live.status != "healthy" {
		t.Errorf("renewable Codex profile: status = %q, want healthy", live.status)
	}
	if live.refresh == nil || !*live.refresh {
		t.Errorf("refresh_due = %v, want true: caam refreshes Codex and needs the signal", live.refresh)
	}
	if live.launch == nil || !*live.launch {
		t.Errorf("launch_usable = %v, want true: the account works", live.launch)
	}
	if live.login == nil || *live.login {
		t.Errorf("login_required = %v, want false: no human is needed", live.login)
	}

	dead, ok := got["dead"]
	if !ok {
		t.Fatalf("profile 'dead' missing from %q", buf.String())
	}
	if dead.status != "critical" {
		t.Errorf("unrenewable expired profile: status = %q, want critical", dead.status)
	}
	if dead.login == nil || !*dead.login {
		t.Errorf("login_required = %v, want true", dead.login)
	}
	if dead.launch == nil || *dead.launch {
		t.Errorf("launch_usable = %v, want false", dead.launch)
	}
}
