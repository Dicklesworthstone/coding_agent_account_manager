package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

func quotaAdviceCommand(mode, model string, cached bool) *cobra.Command {
	cmd := &cobra.Command{Use: "limits"}
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("format", "json", "")
	cmd.Flags().Bool("best", mode == "best", "")
	cmd.Flags().Float64("threshold", 0.8, "")
	cmd.Flags().Bool("recommend", mode == "recommend", "")
	cmd.Flags().Bool("forecast", mode == "forecast", "")
	cmd.Flags().String("model", model, "")
	cmd.Flags().String("source", credNamespaceVault, "")
	cmd.Flags().Bool("cached", cached, "")
	cmd.Flags().String("rank", "", "")
	return cmd
}

func setupLimitsAdviceProfiles(t *testing.T, cached bool, responses map[string]string) {
	t.Helper()
	setupSubscriptionQuotaProfiles(t, "claude", responses)
	for name, body := range responses {
		dir := filepath.Join(getVaultDir(), "claude", name)
		if cached {
			writeNativeTestCredential(t, filepath.Join(dir, ".claude.json"), fmt.Sprintf(`{"cachedUsageUtilization":{"fetchedAtMs":1792580400000,"accountUuid":%q,"utilization":%s}}`, name, body))
		} else {
			writeNativeTestCredential(t, filepath.Join(dir, ".credentials.json"), fmt.Sprintf(`{"claudeAiOauth":{"accessToken":%q,"refreshToken":%q,"expiresAt":%d}}`, name, "synthetic-refresh-"+name, time.Now().Add(time.Hour).UnixMilli()))
		}
	}
}

func TestLimitsBestNeverFallsBackToAnExhaustedQuota(t *testing.T) {
	const modelSpent = `{"five_hour":null,"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`
	for _, cached := range []bool{false, true} {
		for _, tc := range []struct {
			name, model, want string
			responses         map[string]string
		}{
			{"weekly exhausted", "", "", map[string]string{"seat": `{"five_hour":null,"seven_day":{"utilization":100}}`}},
			{"session exhausted", "", "", map[string]string{"seat": `{"five_hour":{"utilization":100},"seven_day":{"utilization":0}}`}},
			{"model exhausted", "fable", "", map[string]string{"seat": modelSpent}},
			{"unknown model includes every cap", "", "", map[string]string{"seat": modelSpent}},
			{"different model remains usable", "sonnet", "seat", map[string]string{"seat": modelSpent}},
			{"busy usable fallback", "", "seat", map[string]string{"seat": `{"seven_day":{"utilization":85}}`}},
			{"best ignores alphabetical input order", "", "z-idle", map[string]string{
				"a-busy": `{"seven_day":{"utilization":70}}`, "z-idle": `{"seven_day":{"utilization":10}}`,
			}},
			{"fallback ignores alphabetical input order", "", "z-usable", map[string]string{
				"a-busy": `{"seven_day":{"utilization":95}}`, "z-usable": `{"seven_day":{"utilization":85}}`,
			}},
			{"advisory headroom precedes a higher score", "fable", "under-threshold", map[string]string{
				"high-score":      `{"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":85,"scope":{"model":{"display_name":"Fable"}}}]}`,
				"under-threshold": `{"five_hour":{"utilization":70},"seven_day":{"utilization":70}}`,
			}},
			{"fallback searches past a higher scoring exhausted seat", "fable", "usable", map[string]string{
				"tempting": modelSpent, "usable": `{"seven_day":{"utilization":85}}`,
			}},
			{"measured zero", "", "seat", map[string]string{"seat": `{"five_hour":{"utilization":null},"seven_day":{"utilization":0}}`}},
			{"unmeasured", "", "", map[string]string{"seat": `{}`}},
		} {
			t.Run(fmt.Sprintf("cached=%t/%s", cached, tc.name), func(t *testing.T) {
				setupLimitsAdviceProfiles(t, cached, tc.responses)
				for _, format := range []string{"json", "table"} {
					cmd := quotaAdviceCommand("best", tc.model, cached)
					if err := cmd.Flags().Set("format", format); err != nil {
						t.Fatal(err)
					}
					var output bytes.Buffer
					cmd.SetOut(&output)
					cmd.SetErr(&output)
					if err := runLimits(cmd, []string{"claude"}); err != nil {
						t.Fatalf("%s: %v\n%s", format, err, output.String())
					}
					if format == "json" {
						var selected *usage.ProfileUsage
						if err := json.Unmarshal(output.Bytes(), &selected); err != nil {
							t.Fatal(err)
						}
						if tc.want == "" && selected != nil || tc.want != "" && (selected == nil || selected.ProfileName != tc.want) {
							t.Fatalf("selected %+v, want %q\n%s", selected, tc.want, output.String())
						}
					} else if tc.want == "" {
						if !strings.Contains(output.String(), "No available profiles") || strings.Contains(output.String(), "Best profile:") {
							t.Fatalf("table recommended unusable quota: %s", output.String())
						}
					} else if !strings.Contains(output.String(), "Best profile: claude/"+tc.want) {
						t.Fatalf("table chose another profile: %s", output.String())
					}
				}
			})
		}
	}
}

func TestLimitsThresholdValidationPrecedesProviderRequests(t *testing.T) {
	setupLimitsAdviceProfiles(t, false, map[string]string{"spent": `{"seven_day":{"utilization":100}}`})
	transport := http.DefaultTransport
	requests := 0
	http.DefaultTransport = quotaTestTransport(func(req *http.Request) (*http.Response, error) {
		requests++
		return transport.RoundTrip(req)
	})
	for _, threshold := range []float64{math.NaN(), math.Inf(1), math.Inf(-1), -0.1, 1.1} {
		cmd := quotaAdviceCommand("best", "", false)
		if err := cmd.Flags().Set("threshold", fmt.Sprint(threshold)); err != nil {
			t.Fatal(err)
		}
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		if err := runLimits(cmd, []string{"claude"}); err == nil || !strings.Contains(err.Error(), "--threshold") || output.Len() != 0 {
			t.Fatalf("threshold %v: error=%v output=%s", threshold, err, output.String())
		}
		// The consumer keeps the hard guard even when called without the CLI.
		rows := []usage.ProfileUsage{{Provider: "claude", ProfileName: "spent", Usage: &usage.UsageInfo{SecondaryWindow: &usage.UsageWindow{UsedPercent: 100}}}}
		if err := renderBestProfile(&output, "json", rows, threshold, ""); err != nil || strings.TrimSpace(output.String()) != "null" {
			t.Fatalf("threshold %v bypassed the hard cap: %s, %v", threshold, output.String(), err)
		}
	}
	if requests != 0 {
		t.Fatalf("invalid thresholds caused %d provider requests", requests)
	}
}

func TestLimitsForecastUsesApplicableMeasuredWindows(t *testing.T) {
	const scoped = `{"seven_day":{"utilization":25},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`
	for _, cached := range []bool{false, true} {
		for _, tc := range []struct {
			name, body, model string
			load              int
			primary           bool
			exhausted         bool
		}{
			{"weekly exhausted", `{"five_hour":null,"seven_day":{"utilization":100}}`, "", 100, false, true},
			{"weekly measured zero", `{"five_hour":{"utilization":null},"seven_day":{"utilization":0}}`, "", 0, false, false},
			{"conventional measured zero", `{"five_hour":{"utilization":0},"seven_day":{"utilization":20}}`, "", 20, true, false},
			{"requested model exhausted", scoped, "fable", 100, false, true},
			{"unknown model includes every cap", scoped, "", 100, false, true},
			{"unrelated model remains usable", scoped, "sonnet", 25, false, false},
			{"another reset cannot clear unknown weekly cap", `{"five_hour":{"utilization":90,"resets_at":"2099-01-01T00:00:00Z"},"seven_day":{"utilization":100,"resets_at":null}}`, "", 100, true, true},
		} {
			t.Run(fmt.Sprintf("cached=%t/%s", cached, tc.name), func(t *testing.T) {
				setupLimitsAdviceProfiles(t, cached, map[string]string{"seat": tc.body})
				for _, format := range []string{"json", "table"} {
					cmd := quotaAdviceCommand("forecast", tc.model, cached)
					if err := cmd.Flags().Set("format", format); err != nil {
						t.Fatal(err)
					}
					var output bytes.Buffer
					cmd.SetOut(&output)
					cmd.SetErr(&output)
					if err := runLimits(cmd, []string{"claude"}); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(output.String(), "safe_to_use_in") || strings.Contains(output.String(), "safe for heavy usage") || strings.Contains(output.String(), "Safe to use in") {
						t.Fatalf("forecast invented future readiness: %s", output.String())
					}
					if strings.Contains(output.String(), "Quota exhausted") != tc.exhausted {
						t.Fatalf("forecast lost an applicable cap: %s", output.String())
					}
					if format == "json" {
						var rows []map[string]any
						if err := json.Unmarshal(output.Bytes(), &rows); err != nil || len(rows) != 1 {
							t.Fatalf("forecast JSON: %v\n%s", err, output.String())
						}
						row := rows[0]
						primary, present := row["current_primary_percent"]
						if !present || (primary != nil) != tc.primary || row["current_load_percent"] != float64(tc.load) {
							t.Fatalf("forecast invented an absent measurement or lost the binding load: %v", row)
						}
						if tc.load == 0 && row["current_secondary_percent"] != float64(0) {
							t.Fatalf("measured weekly zero became absent: %v", row)
						}
						if tc.model == "sonnet" && row["current_scoped_percent"] != nil {
							t.Fatalf("Sonnet inherited a Fable restriction: %v", row)
						}
					} else if !tc.primary && !strings.Contains(output.String(), "Primary not reported") {
						t.Fatalf("table fabricated primary zero: %s", output.String())
					}
				}
			})
		}
	}
}

func TestLimitsRecommendationsUseMeasuredWeeklyAndModelLoad(t *testing.T) {
	for _, cached := range []bool{false, true} {
		for _, modelQuota := range []bool{false, true} {
			t.Run(fmt.Sprintf("cached=%t/model_quota=%t", cached, modelQuota), func(t *testing.T) {
				spent := `{"five_hour":null,"seven_day":{"utilization":100}}`
				label := "Secondary"
				if modelQuota {
					spent = `{"seven_day":{"utilization":0},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`
					label = "Fable"
				}
				setupLimitsAdviceProfiles(t, cached, map[string]string{
					"spent": spent, "usable": `{"seven_day":{"utilization":25}}`, "unknown": `{}`,
				})
				cmd := quotaAdviceCommand("recommend", "fable", cached)
				var output bytes.Buffer
				cmd.SetOut(&output)
				cmd.SetErr(&output)
				if err := runLimits(cmd, []string{"claude"}); err != nil {
					t.Fatal(err)
				}
				var recs []Recommendation
				if err := json.Unmarshal(output.Bytes(), &recs); err != nil || len(recs) != 2 {
					t.Fatalf("recommendations: %v\n%s", err, output.String())
				}
				for _, rec := range recs {
					switch rec.Action {
					case "Switch from":
						if rec.Profile != "claude/spent" || rec.CurrentLoad != 100 || rec.Urgency != "now" || !strings.Contains(rec.Reason, label+" usage at 100%") {
							t.Fatalf("recommendation fabricated primary zero or missed urgency: %+v", rec)
						}
					case "Switch to":
						if rec.Profile != "claude/usable" || rec.CurrentLoad != 25 || !strings.Contains(rec.Reason, "Secondary usage at 25%") {
							t.Fatalf("recommendation did not use measured weekly headroom: %+v", rec)
						}
					default:
						t.Fatalf("unexpected recommendation: %+v", rec)
					}
				}
			})
		}
	}
}

func TestLimitsAdviceRequiresUsableMeasurements(t *testing.T) {
	spent := usage.ProfileUsage{Provider: "claude", ProfileName: "spent", Usage: &usage.UsageInfo{SecondaryWindow: &usage.UsageWindow{UsedPercent: 100}}}
	for _, tc := range []struct {
		name string
		info *usage.UsageInfo
	}{
		{"no windows", &usage.UsageInfo{}},
		{"unavailable status", &usage.UsageInfo{QuotaStatus: usage.QuotaUnavailable, SecondaryWindow: &usage.UsageWindow{}}},
		{"degraded status", &usage.UsageInfo{QuotaStatus: usage.QuotaDegraded, SecondaryWindow: &usage.UsageWindow{}}},
		{"rolled window", &usage.UsageInfo{SecondaryWindow: &usage.UsageWindow{Rolled: true}}},
		{"display-only window", &usage.UsageInfo{SecondaryWindow: &usage.UsageWindow{Unmeasured: true}}},
		{"nonfinite measurement", &usage.UsageInfo{SecondaryWindow: &usage.UsageWindow{Utilization: math.NaN()}}},
		{"model-only measurement", &usage.UsageInfo{ScopedWindows: []*usage.UsageWindow{{Label: "Fable"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unknown := usage.ProfileUsage{Provider: "claude", ProfileName: "unknown", Usage: tc.info}
			rows := []usage.ProfileUsage{unknown, spent}
			if recs := generateLimitsRecommendations(rows, 0.8, ""); len(recs) != 1 || recs[0].Action != "Switch from" || recs[0].Profile != "claude/spent" {
				t.Fatalf("unknown capacity became a switch target: %+v", recs)
			}
			if forecasts := generateForecasts(rows, ""); len(forecasts) != 1 || forecasts[0].Profile != "claude/spent" {
				t.Fatalf("unusable data became a numeric forecast: %+v", forecasts)
			}
			var output bytes.Buffer
			if err := renderRecommendations(&output, "table", []usage.ProfileUsage{unknown}, 0.8, ""); err != nil || strings.Contains(output.String(), "healthy") {
				t.Fatalf("unknown usage became healthy: %s, %v", output.String(), err)
			}
		})
	}
	// Legacy normalized rows can expose only a fraction. Advice must enforce
	// that restriction as strongly as selection does.
	fractional := usage.ProfileUsage{Provider: "claude", ProfileName: "fractional", Usage: &usage.UsageInfo{
		SecondaryWindow: &usage.UsageWindow{Utilization: 1},
	}}
	if recs := generateLimitsRecommendations([]usage.ProfileUsage{fractional}, 0.8, ""); len(recs) != 1 || recs[0].CurrentLoad != 100 || recs[0].Urgency != "now" {
		t.Fatalf("fraction-only hard cap lost its recommendation: %+v", recs)
	}
	if forecasts := generateForecasts([]usage.ProfileUsage{fractional}, ""); len(forecasts) != 1 || forecasts[0].CurrentLoad != 100 || forecasts[0].CurrentSecondary == nil || *forecasts[0].CurrentSecondary != 100 {
		t.Fatalf("fraction-only hard cap became zero: %+v", forecasts)
	}
}

func TestLimitsAdviceUsesOnlyApplicableResetTimes(t *testing.T) {
	now := time.Now()
	rows := []usage.ProfileUsage{{Provider: "claude", ProfileName: "seat", Usage: &usage.UsageInfo{
		SecondaryWindow: &usage.UsageWindow{UsedPercent: 95, ResetsAt: now.Add(48 * time.Hour)},
		TertiaryWindow:  &usage.UsageWindow{Label: "Opus", UsedPercent: 10, ResetsAt: now.Add(5 * time.Minute)},
	}}}
	recs := generateLimitsRecommendations(rows, 0.8, "sonnet")
	forecasts := generateForecasts(rows, "sonnet")
	if len(recs) != 1 || !strings.Contains(recs[0].SwitchIn, "d") || len(forecasts) != 1 || !strings.Contains(forecasts[0].NextResetIn, "d") || forecasts[0].CurrentScoped != nil {
		t.Fatalf("Sonnet borrowed the Opus reset: recommendations=%+v forecasts=%+v", recs, forecasts)
	}
}

func TestLimitsCachedAdviceRejectsContradictedAccount(t *testing.T) {
	setupLimitsAdviceProfiles(t, true, map[string]string{"seat": `{"seven_day":{"utilization":0}}`})
	path := filepath.Join(getVaultDir(), "claude", "seat", ".claude.json")
	body := `{"oauthAccount":{"accountUuid":"current-account"},"cachedUsageUtilization":{"fetchedAtMs":1792580400000,"accountUuid":"previous-account","utilization":{"seven_day":{"utilization":0}}}}`
	writeNativeTestCredential(t, path, body)
	for _, mode := range []string{"best", "recommend", "forecast"} {
		cmd := quotaAdviceCommand(mode, "", true)
		var output bytes.Buffer
		cmd.SetOut(&output)
		cmd.SetErr(&output)
		if err := runLimits(cmd, []string{"claude"}); err != nil || strings.TrimSpace(output.String()) != "null" {
			t.Fatalf("%s attributed another account's quota to this login: %s, %v", mode, output.String(), err)
		}
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != body {
		t.Fatal("cached advice modified the source state")
	}
}

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
	saved := `{"user_id":"seat-A","key":"SYNTHETIC-STALE","email":"fresh@example.com"}`
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
