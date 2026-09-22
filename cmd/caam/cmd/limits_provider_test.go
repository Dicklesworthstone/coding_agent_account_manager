package cmd

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

func TestLimitsAcceptsGrok(t *testing.T) {
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

	// No saved profiles in the isolated test home. The command must accept
	// grok instead of rejecting it as unsupported, and it must not crash.
	err := runLimits(cmd, []string{"grok"})
	if err != nil {
		t.Fatalf("grok limits: %v\n%s", err, buf.String())
	}
	if strings.Contains(buf.String(), "not supported") {
		t.Fatalf("output treated grok as unsupported: %s", buf.String())
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
