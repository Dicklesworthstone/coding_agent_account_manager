package cmd

import (
	"bytes"
	"context"
	"encoding/json"
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

func TestLimitsLiveSourceRequiresProviderAndSavedProfile(t *testing.T) {
	for _, tc := range []struct {
		name, provider, profile, message string
		cached                           bool
	}{
		{"provider required", "", "work", "one provider", false},
		{"unsupported provider", "claude", "work", "grok or cursor", false},
		{"saved profile required", "cursor", "", "requires --profile", false},
		{"offline unsupported", "grok", "work", "unavailable with --cached", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{Use: "limits"}
			cmd.Flags().String("source", "live", "")
			cmd.Flags().String("profile", tc.profile, "")
			cmd.Flags().String("format", "json", "")
			cmd.Flags().Bool("cached", tc.cached, "")
			var args []string
			if tc.provider != "" {
				args = []string{tc.provider}
			}
			err := runLimits(cmd, args)
			if err == nil || !strings.Contains(err.Error(), tc.message) {
				t.Fatalf("error = %v, want %q", err, tc.message)
			}
		})
	}
}

func TestLimitsLiveSourceReportsMeasuredAccountAndRefusesMismatch(t *testing.T) {
	setupGrokResetProfiles(t)
	// The command resolves the configured vault root, not the fixture's
	// in-memory vault object. Give it an old snapshot of the same account.
	savedPath := filepath.Join(getVaultDir(), "grok", "fresh", "auth.json")
	livePath := filepath.Join(os.Getenv("GROK_HOME"), "auth.json")
	saved := `{"user_id":"seat-A","key":"SYNTHETIC-STALE","email":"fresh@example.com","expires_at":"2099-01-01T00:00:00Z"}`
	live := `{"user_id":"seat-A","key":"SYNTHETIC-FRESH","email":"fresh@example.com","expires_at":"2099-01-01T00:00:00Z"}`
	writeNativeTestCredential(t, savedPath, saved)
	writeNativeTestCredential(t, livePath, live)
	cmd := &cobra.Command{Use: "limits"}
	cmd.Flags().String("source", "live", "")
	cmd.Flags().String("profile", "fresh", "")
	cmd.Flags().String("format", "json", "")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := runLimits(cmd, []string{"grok"}); err != nil {
		t.Fatal(err)
	}
	var rows []usage.ProfileUsage
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("limits JSON: %v\n%s", err, buf.String())
	}
	row := rows[0]
	if row.ProfileName != "fresh" || row.Usage == nil || !row.Usage.NumericQuotaKnown() ||
		row.Usage.QuotaStatus != usage.QuotaOK || row.Usage.PrimaryWindow.UsedPercent != 0 {
		t.Fatalf("fresh live account lost its measured quota: %+v", row)
	}
	if row.CredentialSource == nil || row.CredentialSource.Namespace != "live" || row.CredentialSource.Path != livePath || !row.CredentialSource.Explicit {
		t.Fatalf("limits omitted live credential provenance: %+v", row.CredentialSource)
	}
	for path, want := range map[string]string{savedPath: saved, livePath: live} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatal("limits changed a source credential")
		}
	}
	// A mismatch is a command failure in JSON mode, never a successful
	// empty array or another account reported under the requested name.
	writeNativeTestCredential(t, livePath, `{"user_id":"seat-B","key":"SYNTHETIC-FRESH","email":"fresh@example.com"}`)
	buf.Reset()
	err := runLimits(cmd, []string{"grok"})
	if err == nil || !strings.Contains(err.Error(), "does not match") || buf.Len() != 0 {
		t.Fatalf("mismatch error=%v output=%q", err, buf.String())
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
