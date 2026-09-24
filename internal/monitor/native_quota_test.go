package monitor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

func TestNativeMonitorUsesCanonicalCredential(t *testing.T) {
	v := authfile.NewVault(t.TempDir())
	dir := v.ProfilePath("cursor", "seat")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"auth.json", "xdg-auth.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"accessToken":"SYNTHETIC"}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	m := NewMonitor(WithVault(v))
	got, err := m.readAccessToken("cursor", "seat")
	if err != nil || got != "cursor-root:"+filepath.Join(dir, "auth.json") {
		t.Fatalf("monitor credential = %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.readAccessToken("cursor", "seat"); err == nil {
		t.Fatal("monitor silently used a stale alternate credential")
	}
}

func TestNativeMonitorNeverDisplaysUnknownAsZero(t *testing.T) {
	for _, provider := range []string{"cursor", "grok"} {
		for _, row := range []*usage.UsageInfo{
			{Provider: provider, Error: "credential unavailable"},
			{Provider: provider, QuotaStatus: usage.QuotaDegraded, PrimaryWindow: &usage.UsageWindow{}},
			{Provider: provider, QuotaStatus: usage.QuotaOK, LimitStage: "HARD_BLOCK", PrimaryWindow: &usage.UsageWindow{}},
		} {
			if usagePercent(row) >= 0 || usageUnavailable(row) == "" {
				t.Errorf("unknown native row displayed as a measurement: %+v", row)
			}
		}
		known := &usage.UsageInfo{Provider: provider, QuotaStatus: usage.QuotaOK, PrimaryWindow: &usage.UsageWindow{}}
		if usagePercent(known) != 0 || usageUnavailable(known) != "" {
			t.Errorf("measured native zero lost: %+v", known)
		}
	}
}
