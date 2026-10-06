package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/spf13/cobra"
)

func claudeHealthCredential(expiry time.Time) string {
	return fmt.Sprintf(`{"claudeAiOauth":{"accessToken":"synthetic-health-token","expiresAt":%d}}`, expiry.UnixMilli())
}

func TestClaudeIsolatedHealthUsesNativeCredentialDirectory(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	selectedExpiry := now.Add(3 * time.Hour)
	for _, tc := range []struct {
		name      string
		xdg       string
		legacy    string
		want      time.Time
		wantKnown bool
	}{
		{"xdg_authoritative", claudeHealthCredential(selectedExpiry), claudeHealthCredential(now.Add(24 * time.Hour)), selectedExpiry, true},
		{"legacy_only", "", claudeHealthCredential(selectedExpiry), selectedExpiry, true},
		{"malformed_xdg", `{"claudeAiOauth":`, claudeHealthCredential(selectedExpiry), time.Time{}, false},
		{"opaque_xdg", `{"claudeAiOauth":{"accessToken":"opaque"}}`, claudeHealthCredential(selectedExpiry), time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupCodexVerificationVault(t)
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("USERPROFILE", home)
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
			t.Setenv("CLAUDE_CONFIG_DIR", "")
			t.Setenv("CAAM_KEYCHAIN", "0")
			tools["claude"] = authfile.ClaudeAuthFiles
			profileStore = profile.NewStore(filepath.Join(home, "profiles"))
			prof, err := profileStore.Create("claude", "work", "oauth")
			if err != nil {
				t.Fatal(err)
			}
			if tc.xdg != "" {
				writeNativeTestCredential(t, filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json"), tc.xdg)
			}
			if tc.legacy != "" {
				writeNativeTestCredential(t, filepath.Join(prof.HomePath(), ".claude", ".credentials.json"), tc.legacy)
			}
			writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("claude", "work"), ".credentials.json"), claudeHealthCredential(now.Add(-24*time.Hour)))
			if err := healthStore.SetTokenExpiry("claude", "work", now.Add(-48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			ph := buildProfileHealth("claude", "work")
			if !ph.TokenExpiresAt.Equal(tc.want) {
				t.Fatalf("health expiry = %v, want native expiry %v", ph.TokenExpiresAt, tc.want)
			}
			status := buildStatusHealth(ph)
			rows := runLsJSONForTest(t, "claude")
			row, ok := rows["work"]
			if !ok {
				t.Fatal("ls omitted isolated profile with a saved vault snapshot")
			}
			if tc.wantKnown {
				if status.ExpiresAt != tc.want.Format(time.RFC3339) || row.Health.ExpiresAt != tc.want.Format(time.RFC3339) {
					t.Fatalf("status/listing lost selected expiry: status=%+v listing=%+v", status, row)
				}
			} else if status.ExpiresAt != "" || row.Health.ExpiresAt != "" || ph.SelfRefreshing || ph.TokenRenewable {
				t.Fatalf("unknown selected credentials borrowed stale health: status=%+v listing=%+v health=%+v", status, row, ph)
			}
		})
	}
}

func TestClaudeLiveHealthHonorsExplicitConfigDirectory(t *testing.T) {
	setupCodexVerificationVault(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "selected"))
	t.Setenv("CAAM_KEYCHAIN", "0")
	tools["claude"] = authfile.ClaudeAuthFiles
	selected := filepath.Join(home, "selected", ".credentials.json")
	want := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	writeNativeTestCredential(t, selected, claudeHealthCredential(want))
	writeNativeTestCredential(t, filepath.Join(home, ".claude", ".credentials.json"), claudeHealthCredential(want.Add(48*time.Hour)))
	if err := vault.Backup(authfile.ClaudeAuthFiles(), "work"); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().Bool("no-color", true, "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runStatus(cmd, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	var got statusOutput
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tools) != 1 || got.Tools[0].Health == nil || got.Tools[0].Health.ExpiresAt != want.Format(time.RFC3339) {
		t.Fatalf("status read ignored live credential directory: %+v", got)
	}
	for _, data := range []string{`{"claudeAiOauth":`, `{"claudeAiOauth":{"accessToken":"opaque"}}`} {
		writeNativeTestCredential(t, selected, data)
		ph := &health.ProfileHealth{TokenExpiresAt: want, SelfRefreshing: true, TokenRenewable: true}
		applyLiveExpiry("claude", ph)
		if !ph.TokenExpiresAt.IsZero() || ph.SelfRefreshing || ph.TokenRenewable {
			t.Fatalf("invalid explicit native source borrowed legacy/stale expiry: %+v", ph)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, "missing-selected"))
	ph := &health.ProfileHealth{TokenExpiresAt: want, SelfRefreshing: true, TokenRenewable: true}
	applyLiveExpiry("claude", ph)
	if !ph.TokenExpiresAt.IsZero() || ph.SelfRefreshing || ph.TokenRenewable {
		t.Fatalf("missing explicit native source borrowed legacy/stale expiry: %+v", ph)
	}
}
