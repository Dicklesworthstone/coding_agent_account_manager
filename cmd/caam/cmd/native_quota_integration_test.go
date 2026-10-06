package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

func writeNativeTestCredential(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

// Exercise the native ACP reader with a fresh billing period, an exhausted
// account, and a malformed control. All credentials and executable responses
// are synthetic; the real Grok CLI and network are never used.
func setupGrokResetProfiles(t *testing.T) (map[string]string, time.Time) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	t.Setenv("CAAM_HOME", t.TempDir())
	t.Setenv("GROK_HOME", t.TempDir())
	oldVault, oldHealthStore := vault, healthStore
	vault = authfile.NewVault(t.TempDir())
	healthStore = nil
	t.Cleanup(func() { vault, healthStore = oldVault, oldHealthStore })
	for name, key := range map[string]string{
		"fresh": "SYNTHETIC-FRESH", "spent": "SYNTHETIC-SPENT", "malformed": "SYNTHETIC-MALFORMED",
	} {
		writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("grok", name), "auth.json"),
			fmt.Sprintf(`{"key":%q,"email":%q}`, key, name+"@example.com"))
	}
	now := time.Now().UTC()
	reset := now.Add(7 * 24 * time.Hour)
	period := fmt.Sprintf(`"currentPeriod":{"type":"WEEKLY","start":%q,"end":%q}`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), reset.Format(time.RFC3339Nano))
	t.Setenv("CAAM_TEST_GROK_FRESH", `{"subscriptionTier":"SuperGrok","config":{`+period+`}}`)
	t.Setenv("CAAM_TEST_GROK_SPENT", `{"config":{`+period+`,"creditUsagePercent":100}}`)
	t.Setenv("CAAM_TEST_GROK_MALFORMED", `{"config":{`+period+`,"creditUsagePercent":null}}`)
	binDir := t.TempDir()
	script := `#!/bin/sh
case "$(cat "$GROK_HOME/auth.json")" in
  *SYNTHETIC-FRESH*) billing=$CAAM_TEST_GROK_FRESH ;;
  *SYNTHETIC-SPENT*) billing=$CAAM_TEST_GROK_SPENT ;;
  *SYNTHETIC-MALFORMED*) billing=$CAAM_TEST_GROK_MALFORMED ;;
  *) exit 1 ;;
esac
while IFS= read -r line; do
  method=$(printf '%s' "$line" | sed -n 's/.*"method"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
  id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p')
  case "$method" in
    _x.ai/billing)
      printf '{"jsonrpc":"2.0","id":%s,"result":%s}\n' "$id" "$billing"
      exit 0
      ;;
    initialize|authenticate|session/new)
      printf '{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"synthetic"}}\n' "$id"
      ;;
    *) exit 1 ;;
  esac
done
`
	if err := os.WriteFile(filepath.Join(binDir, "grok"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	credentials := nativeRotationCredentials("grok", []string{"fresh", "spent", "malformed"})
	if len(credentials) != 3 {
		t.Fatalf("missing synthetic native credentials: %v", credentials)
	}
	return credentials, reset
}

func TestGrokResetZeroReachesUsageAwareCallers(t *testing.T) {
	credentials, reset := setupGrokResetProfiles(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fetcher := usage.NewMultiProfileFetcher()
	results := fetcher.FetchAllProfiles(ctx, "grok", credentials)
	if len(results) != 3 || results[0].ProfileName != "fresh" {
		t.Fatalf("fresh reset account did not rank first: %+v", results)
	}
	fresh := results[0].Usage
	if fresh == nil || fresh.QuotaStatus != usage.QuotaOK || !fresh.NumericQuotaKnown() || fresh.Error != "" || fresh.QuotaNote != "" {
		t.Fatalf("fresh reset account was not measured successfully: %+v", fresh)
	}
	window := fresh.PrimaryWindow
	if window == nil || window.Unmeasured || window.UsedPercent != 0 || window.Utilization != 0 ||
		window.Kind != "billing" || !window.ResetsAt.Equal(reset) {
		t.Fatalf("fresh reset did not preserve a measured zero billing window: %+v", window)
	}
	if near, measured := precheckNearLimit("grok", fresh, 0.8, ""); near || !measured {
		t.Fatalf("precheck rejected reset zero: near=%v measured=%v", near, measured)
	}
	if best := fetcher.GetBestProfile(ctx, "grok", credentials); best == nil || best.ProfileName != "fresh" {
		t.Fatalf("best-profile selection rejected reset zero: %+v", best)
	}
	if available := fetcher.GetProfilesAboveThreshold(ctx, "grok", credentials, 0.8); len(available) != 1 || available[0].ProfileName != "fresh" {
		t.Fatalf("threshold selection did not retain only the measured reset account: %+v", available)
	}

	// This is the same fetch-and-adapt path used by `caam next --usage-aware`.
	profiles := []string{"malformed", "spent", "fresh"}
	data := fetchUsageDataForProfiles("grok", profiles)
	adapted := data["fresh"]
	if adapted == nil || adapted.Error != "" || adapted.PrimaryPercent != 0 || adapted.AvailScore <= 0 ||
		adapted.ResetsAt == nil || !adapted.ResetsAt.Equal(reset) {
		t.Fatalf("rotation adapter lost measured reset zero: %+v", adapted)
	}
	if bad := data["malformed"]; bad == nil || bad.Error == "" {
		t.Fatalf("null usage was allowed through the rotation adapter: %+v", bad)
	}
	for _, algorithm := range []string{"smart", "random", "round_robin"} {
		for _, policy := range []string{"availability", "drain"} {
			t.Run(algorithm+"/"+policy, func(t *testing.T) {
				cfg := config.DefaultSPMConfig()
				cfg.Stealth.Rotation.Algorithm = algorithm
				cfg.Stealth.Rotation.Policy = policy
				selected, err := selectProfileWithRotationAndUsage("grok", profiles, "spent", cfg, nil, data, false)
				if err != nil || selected == nil || selected.Selected != "fresh" {
					t.Fatalf("usage-aware selection rejected reset zero: %+v, %v", selected, err)
				}
			})
		}
	}
}

func TestNativeLimitsUseMainCursorPathResolver(t *testing.T) {
	f := newNamespaceFixture(t)
	prof, err := f.store.Create("cursor", "seat", "oauth")
	if err != nil {
		t.Fatal(err)
	}
	paths := authfile.ResolveCursorPaths(prof.HomePath(), runtime.GOOS, func(string) string { return "" })
	writeNativeTestCredential(t, paths.AuthFile, `{"accessToken":"SYNTHETIC-CURRENT"}`)
	writeNativeTestCredential(t, filepath.Join(prof.XDGConfigPath(), "cursor", "auth.json"), `{"accessToken":"SYNTHETIC-STALE"}`)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "ambient"))
	t.Setenv("APPDATA", filepath.Join(t.TempDir(), "ambient"))
	got := f.lookup.inspect(credNamespaceIsolated, "cursor", "seat")
	if !got.Found() || got.Path != paths.AuthFile || got.Token != "cursor-root:"+paths.AuthFile {
		t.Fatalf("isolated lookup diverged from provider: %+v, want %s", got, paths.AuthFile)
	}
	writeNativeTestCredential(t, paths.AuthFile, `{}`)
	if got := f.lookup.inspect(credNamespaceIsolated, "cursor", "seat"); got.Found() {
		t.Fatalf("invalid canonical token fell back to stale XDG tree: %+v", got)
	}
	vaultPath := filepath.Join(f.vaultDir, "cursor", "seat", "auth.json")
	writeNativeTestCredential(t, vaultPath, `{"accessToken":"SYNTHETIC-VAULT"}`)
	writeNativeTestCredential(t, filepath.Join(filepath.Dir(vaultPath), "xdg-auth.json"), `{"accessToken":"SYNTHETIC-STALE"}`)
	if got := f.lookup.inspect(credNamespaceVault, "cursor", "seat"); got.Path != vaultPath {
		t.Fatalf("vault selected obsolete credential: %+v", got)
	}
}

func TestNativeSelectionRejectsUnknownBeforeEveryAlgorithm(t *testing.T) {
	for _, provider := range []string{"cursor", "grok"} {
		for _, algorithm := range []string{"smart", "random", "round_robin"} {
			cfg := config.DefaultSPMConfig()
			cfg.Stealth.Rotation.Algorithm = algorithm
			for _, policy := range []string{"availability", "drain"} {
				cfg.Stealth.Rotation.Policy = policy
				unknown := &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaDegraded,
					PrimaryWindow: &usage.UsageWindow{}}
				data := map[string]*rotation.UsageInfo{"unknown": toRotationUsageInfo("unknown", unknown, "")}
				for _, profiles := range [][]string{{"unknown"}, {"missing", "unknown"}} {
					if selected, err := selectProfileWithRotationAndUsage(provider, profiles, "", cfg, nil, data, false); err == nil || selected != nil {
						t.Errorf("%s/%s/%s selected unmeasured quota: %+v, %v", provider, algorithm, policy, selected, err)
					}
				}
			}
		}
	}
}

func TestNativeQuotaAdapterPreservesMeasuredZero(t *testing.T) {
	for _, provider := range []string{"cursor", "grok"} {
		known := &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaOK, PrimaryWindow: &usage.UsageWindow{}}
		data := map[string]*rotation.UsageInfo{"known": toRotationUsageInfo("known", known, "")}
		got, err := rotation.NativeQuotaCandidates(provider, []string{"missing", "known"}, data)
		if err != nil || !reflect.DeepEqual(got, []string{"known"}) {
			t.Errorf("measured zero was rejected: %v, %v", got, err)
		}
	}
}

func TestNativeUsageAwareSoleProfileDoesNotActivateWithoutQuota(t *testing.T) {
	home := t.TempDir()
	for key, value := range map[string]string{
		"HOME": home, "USERPROFILE": home,
		"CAAM_HOME":         filepath.Join(home, "caam"),
		"XDG_CONFIG_HOME":   filepath.Join(home, ".config"),
		"APPDATA":           filepath.Join(home, "AppData", "Roaming"),
		"CURSOR_CONFIG_DIR": filepath.Join(home, ".cursor"),
	} {
		t.Setenv(key, value)
	}
	oldVault := vault
	vault = authfile.NewVault(t.TempDir())
	t.Cleanup(func() { vault = oldVault })
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "only"), "auth.json"), `{}`)
	cmd := &cobra.Command{}
	cmd.Flags().Bool("dry-run", false, "")
	cmd.Flags().Bool("quiet", true, "")
	cmd.Flags().Bool("force", false, "")
	cmd.Flags().String("algorithm", "smart", "")
	cmd.Flags().String("policy", "availability", "")
	cmd.Flags().Bool("usage-aware", true, "")
	cmd.Flags().Bool("reload-daemon", false, "")
	if err := runNext(cmd, []string{"cursor"}); err == nil {
		t.Fatal("sole native account was activated without a measured quota")
	}
	live := authfile.ResolveCursorPaths(home, runtime.GOOS, os.Getenv).AuthFile
	if _, err := os.Stat(live); !os.IsNotExist(err) {
		t.Fatalf("failed usage-aware activation touched live auth: %v", err)
	}
}

func TestNativeRotationUsesSelectedVaultAndCandidates(t *testing.T) {
	t.Setenv("CAAM_HOME", t.TempDir())
	oldVault := vault
	vault = authfile.NewVault(t.TempDir())
	t.Cleanup(func() { vault = oldVault })
	selected := filepath.Join(vault.ProfilePath("cursor", "seat"), "auth.json")
	writeNativeTestCredential(t, selected, `{"accessToken":"SYNTHETIC-SELECTED"}`)
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "other"), "auth.json"), `{"accessToken":"SYNTHETIC-OTHER"}`)
	writeNativeTestCredential(t, filepath.Join(authfile.DefaultVaultPath(), "cursor", "seat", "auth.json"), `{"accessToken":"SYNTHETIC-DEFAULT"}`)
	got := nativeRotationCredentials("cursor", []string{"seat"})
	if len(got) != 1 || got["seat"] != "cursor-root:"+selected {
		t.Fatalf("rotation read a different vault or candidate set: %v", got)
	}
}
