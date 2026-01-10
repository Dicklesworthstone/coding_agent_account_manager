package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
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
			Name:           "work",
			Score:          150.5,
			UsagePercent:   45,
			AvailScore:     77,
			HealthStatus:   "healthy",
			TokenExpiry:    "3h",
			TimeToDepletion: "2h 15m",
			Reasons:        []string{"+ Healthy token (expires in 3h)"},
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
	result := buildPrecheckResult("claude", profiles, nil, nil, nil, healthStore, nil, "smart")

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
