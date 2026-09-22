package cmd

import (
	"bytes"
	"context"
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
