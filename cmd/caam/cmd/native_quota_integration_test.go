package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

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
					if selected, err := selectProfileWithRotationAndUsage(provider, profiles, "", cfg, nil, data); err == nil || selected != nil {
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
