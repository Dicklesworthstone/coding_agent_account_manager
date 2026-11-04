package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrecheckCommandHelp(t *testing.T) {
	cmd := rootCmd
	cmd.SetArgs([]string{"precheck", "--help"})

	buf := new(bytes.Buffer)
	cmd.SetOut(buf)
	cmd.SetErr(buf)

	err := cmd.Execute()
	require.NoError(t, err)

	output := buf.String()
	// Help text contains these
	assert.Contains(t, output, "session planning")
	assert.Contains(t, output, "--format")
	assert.Contains(t, output, "--no-fetch")
	assert.Contains(t, output, "precheck")
}

func TestPrecheckDoesNotCountExpiredSessionAsReadyBackup(t *testing.T) {
	setupCursorHealthVault(t)
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "expired"), "auth.json"), string(cursorHealthCredential(t, time.Now().Add(-time.Hour), false)))
	writeNativeTestCredential(t, filepath.Join(vault.ProfilePath("cursor", "renewable"), "auth.json"), string(cursorHealthCredential(t, time.Time{}, true)))
	healthStore.SetVaultPath(vault.BasePath())
	// Even when selection has no result (for example every usable account
	// is capped), the planner must not turn a default score into readiness.
	result := buildPrecheckResult("cursor", []string{"expired", "renewable"}, nil, nil, nil, healthStore, nil, "smart", "")
	if result.Recommended != nil || result.Summary.ReadyProfiles != 1 || len(result.Backups) != 1 || result.Backups[0].Name != "renewable" {
		t.Fatalf("planner advertised a hard-expired session as ready: %+v", result)
	}
	found := false
	for _, alert := range result.Alerts {
		if alert.Profile == "expired" && alert.Type == "login_required" {
			found = true
			if !strings.Contains(alert.Action, "cursor-agent login") || strings.Contains(alert.Action, "caam refresh") {
				t.Fatalf("planner omitted native relogin guidance: %+v", alert)
			}
		}
	}
	if !found {
		t.Fatal("planner omitted login-required alert")
	}
}

func TestPrecheckUsesActualVaultAndRejectionStore(t *testing.T) {
	setupCursorHealthVault(t)
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "custom-vault"))
	path := filepath.Join(vault.ProfilePath("cursor", "rejected"), "auth.json")
	writeNativeTestCredential(t, path, string(cursorHealthCredential(t, time.Time{}, true)))
	info, err := health.ParseCursorExpiry(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := healthStore.RecordProviderVerification("cursor", "rejected", health.ProviderVerification{Reason: "access_token_rejected", Fingerprint: info.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	c := &cobra.Command{}
	c.Flags().String("format", "json", "")
	c.Flags().Bool("no-fetch", true, "")
	c.Flags().Duration("timeout", time.Second, "")
	c.Flags().String("algorithm", "random", "")
	c.Flags().String("policy", "drain", "")
	c.Flags().String("model", "", "")
	var output bytes.Buffer
	c.SetOut(&output)
	c.SetErr(&output)
	if err := runPrecheckCmd(c, []string{"cursor"}); err == nil || !strings.Contains(err.Error(), "login required") {
		t.Fatalf("planner ignored selected-vault rejection metadata: %v, output=%s", err, output.String())
	}
}

func TestPrecheckRequestedQuotaDoesNotFallBackToUnmeasured(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			setupSubscriptionQuotaProfiles(t, provider, map[string]string{"unreadable": ""})
			for _, noFetch := range []bool{false, true} {
				c := &cobra.Command{}
				c.Flags().String("format", "json", "")
				c.Flags().Bool("no-fetch", noFetch, "")
				c.Flags().Duration("timeout", time.Second, "")
				c.Flags().String("algorithm", "random", "")
				c.Flags().String("policy", "drain", "")
				c.Flags().String("model", "", "")
				var output bytes.Buffer
				c.SetOut(&output)
				c.SetErr(&output)
				err := runPrecheckCmd(c, []string{provider})
				if noFetch {
					if err != nil {
						t.Fatalf("explicit no-fetch selection changed: %v", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "measured available quota") {
					t.Fatalf("failed requested read recommended unknown capacity: %v, output=%s", err, output.String())
				}
			}
		})
	}
}

func TestPrecheckOnlyCountsQuotaEligibleProfilesAsReady(t *testing.T) {
	setupSubscriptionQuotaProfiles(t, "claude", map[string]string{
		"weekly-only": `{"five_hour":{"utilization":null},"seven_day":{"utilization":20}}`,
		"spent":       subscriptionQuotaResponse("claude", 100), "unreadable": "",
	})
	c := &cobra.Command{}
	c.Flags().String("format", "json", "")
	c.Flags().Bool("no-fetch", false, "")
	c.Flags().Duration("timeout", time.Second, "")
	c.Flags().String("algorithm", "round_robin", "")
	c.Flags().String("policy", "availability", "")
	c.Flags().String("model", "", "")
	var output bytes.Buffer
	c.SetOut(&output)
	c.SetErr(&output)
	if err := runPrecheckCmd(c, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	var result PrecheckResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid planner JSON: %v, %s", err, output.String())
	}
	if result.Recommended == nil || result.Recommended.Name != "weekly-only" || len(result.Backups) != 0 || result.Summary.ReadyProfiles != 1 {
		t.Fatalf("planner advertised blocked quotas as ready: %+v", result)
	}
	if result.Recommended.UsagePercent != 20 {
		t.Fatalf("absent session hid measured weekly usage: %+v", result.Recommended)
	}
	require.Empty(t, result.Recommended.TimeToDepletion)
	require.Nil(t, result.Forecast)
	for _, alert := range result.Alerts {
		require.NotEqual(t, "all_low", alert.Type, "healthy weekly quota was dropped: %+v", alert)
		require.NotEqual(t, "rotation_recommended", alert.Type, "missing burn rate invented a forecast: %+v", alert)
	}
}

func runClaudeQuotaPlannerTest(t *testing.T, model string) PrecheckResult {
	t.Helper()
	c := &cobra.Command{}
	c.Flags().String("format", "json", "")
	c.Flags().Bool("no-fetch", false, "")
	c.Flags().Duration("timeout", time.Second, "")
	c.Flags().String("algorithm", "round_robin", "")
	c.Flags().String("policy", "availability", "")
	c.Flags().String("model", model, "")
	var output bytes.Buffer
	c.SetOut(&output)
	c.SetErr(&output)
	require.NoError(t, runPrecheckCmd(c, []string{"claude"}), "output=%s", output.String())
	var result PrecheckResult
	require.NoError(t, json.Unmarshal(output.Bytes(), &result), "output=%s", output.String())
	return result
}

func TestPrecheckQuotaWarningsDoNotRequireBurnRates(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ready      string
		percent    int
		other      int
		unreadable bool
		allLow     bool
	}{
		{"weekly zero beside exhaustion", `{"five_hour":null,"seven_day":{"utilization":0}}`, 0, 100, false, false},
		{"weekly only beside exhaustion", `{"five_hour":{"utilization":null},"seven_day":{"utilization":20}}`, 20, 100, false, false},
		{"all measured accounts near quota", `{"seven_day":{"utilization":85}}`, 85, 95, false, true},
		{"unknown account prevents aggregate claim", `{"seven_day":{"utilization":85}}`, 85, 95, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			responses := map[string]string{
				"a-ready": tc.ready,
				"z-other": subscriptionQuotaResponse("claude", tc.other),
			}
			if tc.unreadable {
				responses["unreadable"] = ""
			}
			setupSubscriptionQuotaProfiles(t, "claude", responses)
			result := runClaudeQuotaPlannerTest(t, "")
			require.NotNil(t, result.Recommended)
			require.Equal(t, "a-ready", result.Recommended.Name)
			require.Equal(t, tc.percent, result.Recommended.UsagePercent)
			require.Empty(t, result.Recommended.TimeToDepletion)
			require.Nil(t, result.Forecast)
			allLow, otherWarning := false, false
			for _, alert := range result.Alerts {
				require.NotEqual(t, "rotation_recommended", alert.Type, "quota facts became burn-rate advice: %+v", alert)
				require.NotContains(t, alert.Message, "Will hit limit", "unknown rate manufactured an ETA")
				if alert.Type == "all_low" {
					allLow = true
				}
				if alert.Profile == "z-other" && alert.Type == "imminent" {
					otherWarning = true
				}
				if tc.percent < 70 && alert.Profile == "a-ready" {
					t.Fatalf("healthy measured account received a quota warning: %+v", alert)
				}
			}
			require.Equal(t, tc.allLow, allLow, "alerts=%+v", result.Alerts)
			require.True(t, otherWarning, "high measured usage was lost without a forecast: %+v", result.Alerts)
		})
	}
}

func TestPrecheckQuotaAlertsUseRequestedModel(t *testing.T) {
	const scoped = `{"seven_day":{"utilization":20},"limits":[{"kind":"weekly_scoped","percent":100,"scope":{"model":{"display_name":"Fable"}}}]}`
	for _, tc := range []struct {
		name           string
		model          string
		body           string
		want           string
		wantScoped     bool
		wantRestricted bool
	}{
		{"unrelated model", "sonnet", scoped, "a-scoped", false, false},
		{"matching model", "fable", scoped, "b-ready", true, true},
		{"unknown model remains conservative", "", scoped, "b-ready", true, true},
		{"general weekly cap still applies", "sonnet", `{"seven_day":{"utilization":100},"limits":[{"kind":"weekly_scoped","percent":0,"scope":{"model":{"display_name":"Fable"}}}]}`, "b-ready", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setupSubscriptionQuotaProfiles(t, "claude", map[string]string{
				"a-scoped": tc.body,
				"b-ready":  subscriptionQuotaResponse("claude", 50),
			})
			result := runClaudeQuotaPlannerTest(t, tc.model)
			require.NotNil(t, result.Recommended)
			require.Equal(t, tc.want, result.Recommended.Name)
			require.Empty(t, result.Recommended.TimeToDepletion)
			require.Nil(t, result.Forecast)
			scopedAlert, restricted := false, false
			for _, alert := range result.Alerts {
				require.NotEqual(t, "all_low", alert.Type, "the healthy backup disappeared: %+v", result.Alerts)
				require.NotEqual(t, "rotation_recommended", alert.Type)
				if alert.Profile != "a-scoped" {
					continue
				}
				if alert.Type == "scoped_limit" {
					scopedAlert = true
				}
				if alert.Type == "imminent" {
					restricted = true
				}
			}
			require.Equal(t, tc.wantScoped, scopedAlert, "alerts=%+v", result.Alerts)
			require.Equal(t, tc.wantRestricted, restricted, "alerts=%+v", result.Alerts)
		})
	}
}

func TestPrecheckAggregateIncludesMissingRequestedRows(t *testing.T) {
	usageMap := map[string]*usage.UsageInfo{
		"spent":       {Provider: "claude", SecondaryWindow: &usage.UsageWindow{UsedPercent: 100}},
		"unrequested": {Provider: "claude", SecondaryWindow: &usage.UsageWindow{UsedPercent: 100}},
	}
	result := buildPrecheckResult("claude", []string{"spent", "missing"}, nil, usageMap, nil, nil, nil, "smart", "")
	require.Zero(t, result.Summary.ReadyProfiles)
	var spentWarning bool
	for _, alert := range result.Alerts {
		require.NotEqual(t, "all_low", alert.Type, "missing quota was silently removed from an all-profile claim")
		require.NotEqual(t, "unrequested", alert.Profile)
		if alert.Profile == "spent" && alert.Type == "imminent" {
			spentWarning = true
		}
	}
	require.True(t, spentWarning, "missing data suppressed a known individual restriction")
}

func TestPrecheckForecastUsesCurrentModelEvidence(t *testing.T) {
	for _, tc := range []struct {
		name        string
		burnRate    bool
		resetsFirst bool
		wantETA     bool
	}{
		{"unscoped estimate without rate", false, false, false},
		{"measured rate for requested model", true, false, true},
		{"reset prevents projected depletion", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			info := &usage.UsageInfo{
				Provider: "claude", ProfileName: "ready",
				SecondaryWindow: &usage.UsageWindow{UsedPercent: 20},
				ModelWindows: map[string]*usage.UsageWindow{
					"fable": {Label: "Fable", UsedPercent: 100},
				},
				EstimatedDepletion:  time.Now().Add(10 * time.Minute),
				DepletionConfidence: 1,
			}
			if tc.burnRate {
				info.BurnRate = &usage.BurnRateInfo{PercentPerHour: 20, Confidence: 0.9, SampleSize: 10}
			}
			if tc.resetsFirst {
				info.SecondaryWindow.ResetsAt = time.Now().Add(time.Hour)
			}
			result := buildPrecheckResult("claude", []string{"ready"},
				&rotation.Result{Selected: "ready"},
				map[string]*usage.UsageInfo{"ready": info}, nil, nil, nil, "smart", "sonnet")
			require.NotNil(t, result.Recommended)
			if tc.wantETA {
				require.Equal(t, formatDurationShort(4*time.Hour), result.Recommended.TimeToDepletion)
				require.NotNil(t, result.Forecast)
				require.Equal(t, result.Recommended.TimeToDepletion, result.Forecast.NextRotation)
			} else {
				require.Empty(t, result.Recommended.TimeToDepletion)
				require.Nil(t, result.Forecast)
			}
			require.Empty(t, result.Alerts, "unrelated model or stale forecast affected the session")
		})
	}
}

func TestPrecheckCooldownDoesNotReviveUnusableQuota(t *testing.T) {
	setupSubscriptionQuotaProfiles(t, "claude", map[string]string{
		"cooled": subscriptionQuotaResponse("claude", 20),
		"spent":  subscriptionQuotaResponse("claude", 100), "unreadable": "",
	})
	db, err := caamdb.Open()
	if err != nil {
		t.Fatal(err)
	}
	_, cooldownErr := db.SetCooldown("claude", "cooled", time.Now(), time.Hour, "synthetic rate limit")
	closeErr := db.Close()
	if cooldownErr != nil || closeErr != nil {
		t.Fatalf("could not establish cooldown: %v, %v", cooldownErr, closeErr)
	}
	c := &cobra.Command{}
	c.Flags().String("format", "json", "")
	c.Flags().Bool("no-fetch", false, "")
	c.Flags().Duration("timeout", time.Second, "")
	c.Flags().String("algorithm", "random", "")
	c.Flags().String("policy", "drain", "")
	c.Flags().String("model", "", "")
	var output bytes.Buffer
	c.SetOut(&output)
	c.SetErr(&output)
	if err := runPrecheckCmd(c, []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	var result PrecheckResult
	if err := json.Unmarshal(output.Bytes(), &result); err != nil {
		t.Fatalf("invalid planner JSON: %v, %s", err, output.String())
	}
	if result.Recommended != nil || len(result.Backups) != 0 || result.Summary.ReadyProfiles != 0 || result.Summary.CooldownCount != 1 {
		t.Fatalf("nil selection revived an exhausted or unmeasured backup: %+v", result)
	}
	quotaAlerts := 0
	for _, alert := range result.Alerts {
		if alert.Type == "quota_unavailable" {
			quotaAlerts++
		}
	}
	if quotaAlerts != 2 {
		t.Fatalf("unusable backups lack quota explanations: %+v", result.Alerts)
	}
}

func TestPrecheckUnknownProvider(t *testing.T) {
	// Test that unknown providers are rejected
	// This tests the validation logic directly
	providers := []string{"claude", "codex", "gemini"}
	unknownProvider := "unknown-provider"

	// Known providers should be in the tools map
	for _, p := range providers {
		_, ok := tools[p]
		assert.True(t, ok, "provider %s should be in tools map", p)
	}

	// Unknown provider should not be in tools map
	_, ok := tools[unknownProvider]
	assert.False(t, ok, "unknown provider should not be in tools map")
}

func TestPrecheckResult_JSON(t *testing.T) {
	result := &PrecheckResult{
		Provider: "claude",
		Recommended: &ProfileRecommendation{
			Name:         "work",
			Score:        150.5,
			UsagePercent: 45,
			AvailScore:   77,
			HealthStatus: "healthy",
			TokenExpiry:  "3h 30m",
			Reasons:      []string{"+ Healthy token", "+ Not used recently"},
		},
		Backups: []ProfileRecommendation{
			{
				Name:         "personal",
				Score:        120.0,
				UsagePercent: 60,
				AvailScore:   70,
				HealthStatus: "warning",
			},
		},
		InCooldown: []CooldownProfile{
			{
				Name:          "overflow",
				CooldownUntil: time.Now().Add(30 * time.Minute),
				Remaining:     "30m",
			},
		},
		Alerts: []PrecheckAlert{
			{
				Type:    "approaching",
				Profile: "personal",
				Message: "Usage at 60%",
				Urgency: "medium",
				Action:  "caam activate claude --auto",
			},
		},
		Summary: &UsageSummary{
			TotalProfiles:   3,
			ReadyProfiles:   2,
			CooldownCount:   1,
			AvgUsagePercent: 52,
			HealthyCount:    1,
			WarningCount:    1,
			CriticalCount:   0,
		},
		Algorithm: "smart",
		FetchedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := precheckOutputJSON(&buf, result)
	require.NoError(t, err)

	// Parse the JSON output
	var parsed PrecheckResult
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err)

	assert.Equal(t, "claude", parsed.Provider)
	assert.Equal(t, "work", parsed.Recommended.Name)
	assert.Equal(t, 45, parsed.Recommended.UsagePercent)
	assert.Len(t, parsed.Backups, 1)
	assert.Len(t, parsed.InCooldown, 1)
	assert.Len(t, parsed.Alerts, 1)
	assert.Equal(t, 3, parsed.Summary.TotalProfiles)
}

func TestPrecheckResult_Brief(t *testing.T) {
	testCases := []struct {
		name     string
		result   *PrecheckResult
		expected string
	}{
		{
			name: "with recommendation",
			result: &PrecheckResult{
				Provider: "claude",
				Recommended: &ProfileRecommendation{
					Name:         "work",
					UsagePercent: 45,
				},
			},
			expected: "claude: work (45% used)\n",
		},
		{
			name: "no usage data",
			result: &PrecheckResult{
				Provider: "claude",
				Recommended: &ProfileRecommendation{
					Name: "work",
				},
			},
			expected: "claude: work\n",
		},
		{
			name: "all in cooldown",
			result: &PrecheckResult{
				Provider: "codex",
				InCooldown: []CooldownProfile{
					{Name: "a", Remaining: "30m"},
				},
			},
			expected: "codex: all in cooldown\n",
		},
		{
			name: "no profiles",
			result: &PrecheckResult{
				Provider: "gemini",
			},
			expected: "gemini: no profiles\n",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := precheckOutputBrief(&buf, tc.result)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, buf.String())
		})
	}
}

func TestPrecheckResult_Table(t *testing.T) {
	result := &PrecheckResult{
		Provider: "claude",
		Recommended: &ProfileRecommendation{
			Name:            "work",
			Score:           150.5,
			UsagePercent:    45,
			AvailScore:      77,
			HealthStatus:    "healthy",
			TokenExpiry:     "3h",
			TimeToDepletion: "2h 15m",
			Reasons:         []string{"+ Healthy token (expires in 3h)"},
		},
		Backups: []ProfileRecommendation{
			{Name: "personal", HealthStatus: "warning", UsagePercent: 60},
		},
		InCooldown: []CooldownProfile{
			{Name: "overflow", Remaining: "30m"},
		},
		Alerts: []PrecheckAlert{
			{Message: "Usage approaching limit", Urgency: "medium"},
		},
		Summary: &UsageSummary{
			TotalProfiles:   3,
			ReadyProfiles:   2,
			CooldownCount:   1,
			AvgUsagePercent: 52,
			HealthyCount:    1,
			WarningCount:    1,
		},
		Algorithm: "smart",
		FetchedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := precheckOutputTable(&buf, result)
	require.NoError(t, err)

	output := buf.String()

	// Check header
	assert.Contains(t, output, "SESSION PLANNER")
	assert.Contains(t, output, "CLAUDE")
	assert.Contains(t, output, "smart")

	// Check recommended section
	assert.Contains(t, output, "RECOMMENDED:")
	assert.Contains(t, output, "work")
	assert.Contains(t, output, "[healthy]")
	assert.Contains(t, output, "45%")

	// Check backup section
	assert.Contains(t, output, "BACKUP PROFILES:")
	assert.Contains(t, output, "personal")

	// Check cooldown section
	assert.Contains(t, output, "IN COOLDOWN:")
	assert.Contains(t, output, "overflow")
	assert.Contains(t, output, "30m")

	// Check alerts section
	assert.Contains(t, output, "ALERTS:")
	assert.Contains(t, output, "Usage approaching limit")

	// Check summary section
	assert.Contains(t, output, "SUMMARY:")
	assert.Contains(t, output, "3 total")
	assert.Contains(t, output, "2 ready")

	// Check quick actions
	assert.Contains(t, output, "QUICK ACTIONS:")
	assert.Contains(t, output, "caam activate")
}

func TestRenderUsageBar(t *testing.T) {
	testCases := []struct {
		name     string
		percent  int
		width    int
		expected string
	}{
		{"0_percent", 0, 10, "[----------]"},
		{"50_percent", 50, 10, "[#####-----]"},
		{"100_percent", 100, 10, "[##########]"},
		{"negative_clamped", -10, 10, "[----------]"},
		{"over100_clamped", 150, 10, "[##########]"},
		{"25_percent_wide", 25, 20, "[#####---------------]"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := renderUsageBar(tc.percent, tc.width)
			assert.Equal(t, tc.expected, result)
		})
	}
}

func TestBuildPrecheckResult(t *testing.T) {
	tmpDir := t.TempDir()
	vaultDir := filepath.Join(tmpDir, "vault")

	// Create vault with profiles
	err := os.MkdirAll(filepath.Join(vaultDir, "claude", "work"), 0755)
	require.NoError(t, err)
	err = os.MkdirAll(filepath.Join(vaultDir, "claude", "personal"), 0755)
	require.NoError(t, err)

	// Create minimal auth files
	authContent := `{"claudeAiOauth":{"accessToken":"test"}}`
	err = os.WriteFile(filepath.Join(vaultDir, "claude", "work", ".credentials.json"), []byte(authContent), 0644)
	require.NoError(t, err)
	err = os.WriteFile(filepath.Join(vaultDir, "claude", "personal", ".credentials.json"), []byte(authContent), 0644)
	require.NoError(t, err)

	// Create health store
	healthPath := filepath.Join(tmpDir, "health.json")
	healthStore := health.NewStorage(healthPath)

	profiles := []string{"work", "personal"}

	// Build result without usage data or selection
	result := buildPrecheckResult("claude", profiles, nil, nil, nil, healthStore, nil, "smart", "")

	assert.Equal(t, "claude", result.Provider)
	assert.Equal(t, "smart", result.Algorithm)
	assert.NotNil(t, result.Summary)
	assert.Equal(t, 2, result.Summary.TotalProfiles)

	// With no selection, both profiles should be in backups
	// or recommended should be nil
	if result.Recommended == nil {
		assert.Len(t, result.Backups, 2)
	}
}

func TestPrecheckResultAlerts(t *testing.T) {
	result := &PrecheckResult{
		Provider: "claude",
		Alerts: []PrecheckAlert{
			{Type: "imminent", Message: "Limit imminent", Urgency: "high"},
			{Type: "approaching", Message: "Approaching limit", Urgency: "medium"},
			{Type: "all_low", Message: "All profiles low", Urgency: "high"},
		},
	}

	var buf bytes.Buffer
	err := precheckOutputTable(&buf, result)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "[!!!]") // High urgency icon
	assert.Contains(t, output, "[!!]")  // Medium urgency icon
}

func TestPrecheckResultForecast(t *testing.T) {
	result := &PrecheckResult{
		Provider: "claude",
		Recommended: &ProfileRecommendation{
			Name:            "work",
			TimeToDepletion: "2h 30m",
		},
		Summary: &UsageSummary{
			ReadyProfiles: 3,
		},
		Forecast: &RotationForecast{
			NextRotation:       "2h 30m",
			ProfilesUntilReset: 3,
		},
	}

	var buf bytes.Buffer
	err := precheckOutputJSON(&buf, result)
	require.NoError(t, err)

	var parsed PrecheckResult
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err)

	assert.NotNil(t, parsed.Forecast)
	assert.Equal(t, "2h 30m", parsed.Forecast.NextRotation)
	assert.Equal(t, 3, parsed.Forecast.ProfilesUntilReset)
}

func TestPrecheckEmptyBackups(t *testing.T) {
	result := &PrecheckResult{
		Provider: "claude",
		Recommended: &ProfileRecommendation{
			Name: "only",
		},
		Backups: []ProfileRecommendation{},
		Summary: &UsageSummary{
			TotalProfiles: 1,
			ReadyProfiles: 1,
		},
		Algorithm: "smart",
		FetchedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := precheckOutputTable(&buf, result)
	require.NoError(t, err)

	output := buf.String()

	// Should not contain BACKUP PROFILES section when empty
	assert.NotContains(t, output, "BACKUP PROFILES:")
}

func TestPrecheckManyBackups(t *testing.T) {
	// Test that only top 3 backups are shown
	result := &PrecheckResult{
		Provider: "claude",
		Recommended: &ProfileRecommendation{
			Name: "main",
		},
		Backups: []ProfileRecommendation{
			{Name: "backup1"},
			{Name: "backup2"},
			{Name: "backup3"},
			{Name: "backup4"},
			{Name: "backup5"},
		},
		Summary: &UsageSummary{
			TotalProfiles: 6,
			ReadyProfiles: 6,
		},
		Algorithm: "smart",
		FetchedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := precheckOutputTable(&buf, result)
	require.NoError(t, err)

	output := buf.String()

	// Should show first 3 and "... and 2 more"
	assert.Contains(t, output, "backup1")
	assert.Contains(t, output, "backup2")
	assert.Contains(t, output, "backup3")
	assert.Contains(t, output, "... and 2 more")
	assert.NotContains(t, output, "backup4")
	assert.NotContains(t, output, "backup5")
}

func TestProfileRecommendationReasons(t *testing.T) {
	rec := ProfileRecommendation{
		Name: "test",
		Reasons: []string{
			"+ Healthy token",
			"- Used recently",
			"+ Pro plan",
		},
	}

	result := &PrecheckResult{
		Provider:    "claude",
		Recommended: &rec,
		Summary:     &UsageSummary{TotalProfiles: 1, ReadyProfiles: 1},
		Algorithm:   "smart",
		FetchedAt:   time.Now(),
	}

	var buf bytes.Buffer
	err := precheckOutputTable(&buf, result)
	require.NoError(t, err)

	output := buf.String()
	assert.Contains(t, output, "Healthy token")
	assert.Contains(t, output, "Used recently")
	assert.Contains(t, output, "Pro plan")
}

func TestPrecheckResultPoolStatus(t *testing.T) {
	result := &PrecheckResult{
		Provider: "claude",
		Recommended: &ProfileRecommendation{
			Name:       "work",
			PoolStatus: "ready",
		},
		Summary:   &UsageSummary{TotalProfiles: 1, ReadyProfiles: 1},
		Algorithm: "smart",
		FetchedAt: time.Now(),
	}

	var buf bytes.Buffer
	err := precheckOutputJSON(&buf, result)
	require.NoError(t, err)

	var parsed PrecheckResult
	err = json.Unmarshal(buf.Bytes(), &parsed)
	require.NoError(t, err)

	assert.Equal(t, "ready", parsed.Recommended.PoolStatus)
}
