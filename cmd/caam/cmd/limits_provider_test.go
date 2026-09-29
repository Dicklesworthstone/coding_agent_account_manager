package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

func TestLimitsAcceptsGrokAndCursor(t *testing.T) {
	cmd := &cobra.Command{Use: "limits"}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("format", "json", "")
	cmd.Flags().Bool("best", false, "")
	cmd.Flags().Float64("threshold", 0.8, "")
	cmd.Flags().Bool("recommend", false, "")
	cmd.Flags().Bool("forecast", false, "")
	cmd.Flags().String("model", "", "")
	cmd.Flags().String("source", "", "")
	cmd.Flags().Bool("cached", false, "")
	cmd.Flags().String("rank", "", "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// No saved profiles in the isolated test home. Both providers must be
	// accepted, and an empty collection must not crash.
	for _, provider := range []string{"grok", "cursor"} {
		buf.Reset()
		err := runLimits(cmd, []string{provider})
		if err != nil {
			t.Fatalf("%s limits: %v\n%s", provider, err, buf.String())
		}
		if strings.Contains(buf.String(), "not supported") {
			t.Fatalf("output treated %s as unsupported: %s", provider, buf.String())
		}
	}
}

func TestFetchFailuresStayOnTheirOwnProvider(t *testing.T) {
	// A cursor root with no credential and a grok home with no auth each
	// produce an error row. Neither call panics or drops the other profile
	// in the same batch.
	ctx := context.Background()
	fetcher := usage.NewMultiProfileFetcher()
	cursorRows := fetcher.FetchAllProfiles(ctx, "cursor", map[string]string{
		"one": t.TempDir(),
		"two": t.TempDir(),
	})
	if len(cursorRows) != 2 {
		t.Fatalf("cursor rows = %d, want 2", len(cursorRows))
	}
	for _, row := range cursorRows {
		if row.Usage == nil || row.Usage.Error == "" || row.Usage.NumericQuotaKnown() {
			t.Fatalf("cursor row %+v", row.Usage)
		}
	}
	grokRows := fetcher.FetchAllProfiles(ctx, "grok", map[string]string{
		"g": t.TempDir(),
	})
	if len(grokRows) != 1 || grokRows[0].Usage == nil || grokRows[0].Usage.Error == "" {
		t.Fatalf("grok row %+v", grokRows)
	}
}

func TestDegradedGrokIsNotTheBestProfile(t *testing.T) {
	rows := []usage.ProfileUsage{{
		Provider:    "grok",
		ProfileName: "partial",
		Usage: &usage.UsageInfo{
			Provider:    "grok",
			ProfileName: "partial",
			QuotaStatus: usage.QuotaDegraded,
			QuotaNote:   "no usage percentage",
			PlanType:    "Synthetic",
			PrimaryWindow: &usage.UsageWindow{
				Unmeasured: true,
				ResetsAt:   time.Now().Add(24 * time.Hour),
			},
			FetchedAt: time.Now(),
		},
	}}
	var buf bytes.Buffer
	if err := renderBestProfile(&buf, "json", rows, 0.8, ""); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(buf.String()) != "null" {
		t.Fatalf("best = %s, want null (unknown quota must not win)", buf.String())
	}
}

func TestNativeProfilesWithoutCredentialNotes(t *testing.T) {
	vault := t.TempDir()
	for _, dir := range []string{"cursor/good", "cursor/stale", "cursor/_original", "cursor/.hidden", "claude/nocreds"} {
		if err := os.MkdirAll(filepath.Join(vault, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// An older caam backup: cli-config.json only, no auth.json.
	if err := os.WriteFile(filepath.Join(vault, "cursor/stale/cli-config.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	notes := nativeProfilesWithoutCredentialNotes(vault, "cursor", map[string]string{"good": "cursor-root:x"})
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "cursor/stale: skipped") || !strings.Contains(notes[0], "caam backup cursor stale") {
		t.Fatalf("notes = %q", notes)
	}
	if got := nativeProfilesWithoutCredentialNotes(vault, "claude", nil); got != nil {
		t.Fatalf("claude profiles must not be annotated: %q", got)
	}
	if got := nativeProfilesWithoutCredentialNotes(filepath.Join(vault, "missing"), "cursor", nil); got != nil {
		t.Fatalf("missing vault produced notes: %q", got)
	}
}

func TestCursorTeamPoolNotes(t *testing.T) {
	cents := func(n int64) *int64 { return &n }
	rows := []usage.ProfileUsage{
		{Provider: "cursor", ProfileName: "team", Usage: &usage.UsageInfo{Billing: &usage.BillingSnapshot{
			TeamPoolCapCents: cents(100000), TeamPoolUsedCents: cents(91272), TeamPoolRemainingCents: cents(8728),
		}}},
		{Provider: "cursor", ProfileName: "solo", Usage: &usage.UsageInfo{Billing: &usage.BillingSnapshot{OnDemandUsedCents: cents(5)}}},
		{Provider: "cursor", ProfileName: "nil"},
		{Provider: "grok", ProfileName: "g", Usage: &usage.UsageInfo{Billing: &usage.BillingSnapshot{TeamPoolCapCents: cents(1)}}},
	}
	notes := cursorTeamPoolNotes(rows)
	want := "cursor/team: team on-demand pool $912.72 of $1000.00 used, $87.28 left"
	if len(notes) != 1 || notes[0] != want {
		t.Fatalf("notes = %q, want [%q]", notes, want)
	}
}
