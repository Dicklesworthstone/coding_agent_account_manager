package monitor

import (
	"context"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

type claudeAuthorityFetchFunc func(context.Context, string, map[string]string) []usage.ProfileUsage

func (f claudeAuthorityFetchFunc) FetchAllProfiles(ctx context.Context, provider string, profiles map[string]string) []usage.ProfileUsage {
	return f(ctx, provider, profiles)
}

func TestClaudeMonitorRefreshUsesOnlySelectedCredentials(t *testing.T) {
	v := authfile.NewVault(t.TempDir())
	for name, files := range map[string]map[string]string{
		"selected": {"auth.json": `{"access_token":"synthetic-selected-A"}`, ".claude.json": `{"oauthToken":"synthetic-ignored-B"}`},
		"broken":   {".credentials.json": `{"claudeAiOauth":{"accessToken":""}}`, ".claude.json": `{"oauthToken":"synthetic-ignored-B"}`},
		"legacy":   {"settings.json": `{"permissions":{"allow":[]}}`, ".claude.json": `{"oauthToken":{"access_token":"synthetic-legacy"}}`},
	} {
		for filename, body := range files {
			writeProfileFile(t, v, "claude", name, filename, body)
		}
	}
	seen := make(map[string]string)
	fetcher := claudeAuthorityFetchFunc(func(_ context.Context, provider string, profiles map[string]string) []usage.ProfileUsage {
		rows := make([]usage.ProfileUsage, 0, len(profiles))
		for name, token := range profiles {
			seen[name] = token
			rows = append(rows, usage.ProfileUsage{
				Provider: provider, ProfileName: name,
				Usage: &usage.UsageInfo{
					Provider: provider, ProfileName: name,
					SecondaryWindow: &usage.UsageWindow{Utilization: 0.25, UsedPercent: 25},
				},
			})
		}
		return rows
	})
	m := NewMonitor(WithVault(v), WithProviders([]string{"claude"}), WithHealthStore(nil), WithFetcher(fetcher))
	if err := m.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen["selected"] != "synthetic-selected-A" || seen["legacy"] != "synthetic-legacy" {
		t.Fatal("monitor fetched an ignored credential or dropped a supported selected login")
	}
	broken := m.GetProfile("claude", "broken")
	if broken == nil || broken.Usage == nil || broken.Usage.Error == "" || broken.Usage.NumericQuotaKnown() {
		t.Fatal("invalid selected credential did not remain an unavailable diagnostic row")
	}
	if strings.Contains(broken.Usage.Error, "synthetic-") {
		t.Fatal("monitor diagnostic exposed credential contents")
	}
	for _, name := range []string{"selected", "legacy"} {
		row := m.GetProfile("claude", name)
		if row == nil || row.Usage == nil || !row.Usage.NumericQuotaKnown() {
			t.Fatalf("selected %s quota was lost", name)
		}
	}
}
