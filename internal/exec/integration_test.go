package exec

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authpool"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/prediction"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/rotation"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Integration Tests: AuthPool + SmartRunner
// =============================================================================

func TestIntegration_AuthPoolSmartRunner_PoolProvidesReadyProfiles(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing AuthPool provides ready profiles to SmartRunner")

	// Setup AuthPool with profiles - must AddProfile first, then SetStatus
	pool := authpool.NewAuthPool()
	pool.AddProfile("claude", "alice")
	pool.AddProfile("claude", "bob")
	pool.AddProfile("claude", "carol")
	require.NoError(t, pool.SetStatus("claude", "alice", authpool.PoolStatusReady))
	require.NoError(t, pool.SetStatus("claude", "bob", authpool.PoolStatusReady))
	require.NoError(t, pool.SetStatus("claude", "carol", authpool.PoolStatusCooldown))

	h.LogInfo("[INTEGRATION] Pool setup complete: alice=ready, bob=ready, carol=cooldown")

	// Verify ready profiles
	ready := pool.GetReadyProfiles("claude")
	require.Len(t, ready, 2, "expected 2 ready profiles")
	h.LogInfo("[INTEGRATION] Ready profiles count: %d", len(ready))

	// Verify cooldown profile not in ready list
	for _, p := range ready {
		assert.NotEqual(t, "carol", p.ProfileName, "carol should not be in ready list")
	}

	// Verify individual status queries
	assert.Equal(t, authpool.PoolStatusReady, pool.GetStatus("claude", "alice"))
	assert.Equal(t, authpool.PoolStatusReady, pool.GetStatus("claude", "bob"))
	assert.Equal(t, authpool.PoolStatusCooldown, pool.GetStatus("claude", "carol"))

	h.LogInfo("[INTEGRATION] AuthPool status verification passed")
}

func TestIntegration_AuthPoolSmartRunner_CooldownTransition(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing profile cooldown transition via AuthPool")

	pool := authpool.NewAuthPool()
	pool.AddProfile("claude", "alice")
	pool.AddProfile("claude", "bob")
	require.NoError(t, pool.SetStatus("claude", "alice", authpool.PoolStatusReady))
	require.NoError(t, pool.SetStatus("claude", "bob", authpool.PoolStatusReady))

	// Initially 2 ready
	ready := pool.GetReadyProfiles("claude")
	require.Len(t, ready, 2)
	h.LogInfo("[INTEGRATION] Initial ready count: %d", len(ready))

	// Simulate rate limit on alice - transition to cooldown
	require.NoError(t, pool.SetStatus("claude", "alice", authpool.PoolStatusCooldown))
	h.LogInfo("[INTEGRATION] Simulated rate limit: alice -> cooldown")

	// Now only 1 ready
	ready = pool.GetReadyProfiles("claude")
	require.Len(t, ready, 1)
	assert.Equal(t, "bob", ready[0].ProfileName, "bob should be the only ready profile")
	h.LogInfo("[INTEGRATION] After cooldown: ready count = %d, profile = %s", len(ready), ready[0].ProfileName)

	// Verify alice is in cooldown
	assert.Equal(t, authpool.PoolStatusCooldown, pool.GetStatus("claude", "alice"))
	h.LogInfo("[INTEGRATION] Cooldown transition verification passed")
}

func TestIntegration_AuthPoolSmartRunner_SmartRunnerUsesPool(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing SmartRunner uses AuthPool for profile selection")

	// Setup vault with profiles
	vault := authfile.NewVault(h.TempDir)
	createTestProfile(t, vault, "claude", "alice")
	createTestProfile(t, vault, "claude", "bob")

	// Setup AuthPool - must AddProfile first, then SetStatus
	pool := authpool.NewAuthPool()
	pool.AddProfile("claude", "alice")
	pool.AddProfile("claude", "bob")
	require.NoError(t, pool.SetStatus("claude", "alice", authpool.PoolStatusReady))
	require.NoError(t, pool.SetStatus("claude", "bob", authpool.PoolStatusReady))

	// Create SmartRunner with pool
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	sr := NewSmartRunner(runner, SmartRunnerOptions{
		Vault:    vault,
		AuthPool: pool,
		HandoffConfig: &config.HandoffConfig{
			AutoTrigger:      true,
			MaxRetries:       3,
			FallbackToManual: true,
		},
	})

	// Verify SmartRunner was created with pool
	require.NotNil(t, sr)
	require.NotNil(t, sr.authPool)
	h.LogInfo("[INTEGRATION] SmartRunner created with AuthPool")

	// Verify pool is accessible from SmartRunner
	assert.Equal(t, pool, sr.authPool)
	h.LogInfo("[INTEGRATION] SmartRunner AuthPool integration verified")
}

// =============================================================================
// Integration Tests: Prediction + Alert
// =============================================================================

func TestIntegration_PredictionAlerts_GeneratesCorrectAlerts(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing prediction engine generates correct alerts")

	// Create predictions at various levels
	predictions := []*prediction.Prediction{
		{
			Provider:            "claude",
			Profile:             "alice",
			CurrentPercentKnown: true,
			CurrentPercent:      75,
			Warning:             prediction.WarningApproaching,
		},
		{
			Provider:            "claude",
			Profile:             "bob",
			CurrentPercentKnown: true,
			CurrentPercent:      92,
			Warning:             prediction.WarningImminent,
		},
		{
			Provider:            "claude",
			Profile:             "carol",
			CurrentPercentKnown: true,
			CurrentPercent:      45,
			Warning:             prediction.WarningNone,
		},
	}

	h.LogInfo("[INTEGRATION] Created predictions: alice=75%%, bob=92%%, carol=45%%")

	// Generate alerts
	opts := prediction.DefaultAlertOptions()
	alerts := prediction.GenerateAlerts(predictions, opts)

	h.LogInfo("[INTEGRATION] Generated %d alerts", len(alerts))

	// Verify alice gets approaching alert
	aliceAlerts := filterAlerts(alerts, "alice")
	require.GreaterOrEqual(t, len(aliceAlerts), 1, "alice should have at least 1 alert")
	h.LogInfo("[INTEGRATION] Alice alerts: %d", len(aliceAlerts))

	// Verify bob gets imminent alert
	bobAlerts := filterAlerts(alerts, "bob")
	require.GreaterOrEqual(t, len(bobAlerts), 1, "bob should have at least 1 alert")
	hasImminent := false
	for _, a := range bobAlerts {
		if a.Type == prediction.AlertImminentLimit {
			hasImminent = true
			break
		}
	}
	assert.True(t, hasImminent, "bob should have imminent limit alert")
	h.LogInfo("[INTEGRATION] Bob imminent alert verified")

	// Verify carol gets no alert (below warning threshold)
	carolAlerts := filterAlerts(alerts, "carol")
	assert.Len(t, carolAlerts, 0, "carol should have no alerts")
	h.LogInfo("[INTEGRATION] Carol correctly has no alerts")
}

func TestIntegration_PredictionAlerts_TimeBasedAlerts(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing time-based depletion alerts")

	predictions := []*prediction.Prediction{
		{
			Provider:            "claude",
			Profile:             "alice",
			CurrentPercentKnown: true,
			DataSources:         []string{"session"},
			CurrentPercent:      60,
			TimeToDepletion:     15 * time.Minute, // Below rotation threshold
			Confidence:          0.8,
			Warning:             prediction.WarningNone,
		},
		{
			Provider:            "claude",
			Profile:             "bob",
			CurrentPercentKnown: true,
			DataSources:         []string{"session"},
			CurrentPercent:      50,
			TimeToDepletion:     2 * time.Hour, // Above rotation threshold
			Confidence:          0.8,
			Warning:             prediction.WarningNone,
		},
	}

	opts := prediction.DefaultAlertOptions()
	opts.RotationThreshold = 30 * time.Minute
	opts.MinConfidence = 0.3

	alerts := prediction.GenerateAlerts(predictions, opts)

	h.LogInfo("[INTEGRATION] Generated %d alerts for time-based predictions", len(alerts))

	// Alice should get switch recommendation (15min < 30min threshold)
	aliceAlerts := filterAlerts(alerts, "alice")
	hasSwitchRec := false
	for _, a := range aliceAlerts {
		if a.Type == prediction.AlertSwitchRecommended {
			hasSwitchRec = true
			break
		}
	}
	assert.True(t, hasSwitchRec, "alice should have switch recommendation")
	h.LogInfo("[INTEGRATION] Alice switch recommendation verified")

	// Bob should not get switch recommendation (2h > 30min threshold)
	bobAlerts := filterAlerts(alerts, "bob")
	for _, a := range bobAlerts {
		if a.Type == prediction.AlertSwitchRecommended {
			t.Error("bob should not have switch recommendation")
		}
	}
	h.LogInfo("[INTEGRATION] Bob correctly has no switch recommendation")
}

func TestIntegration_PredictionAlerts_AllProfilesLow(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing all-profiles-low alert generation")

	// All profiles are in warning/critical range
	predictions := []*prediction.Prediction{
		{
			Provider:            "claude",
			Profile:             "alice",
			CurrentPercentKnown: true,
			CurrentPercent:      85,
			Warning:             prediction.WarningApproaching,
		},
		{
			Provider:            "claude",
			Profile:             "bob",
			CurrentPercentKnown: true,
			CurrentPercent:      90,
			Warning:             prediction.WarningImminent,
		},
	}

	opts := prediction.DefaultAlertOptions()
	alerts := prediction.GenerateAlerts(predictions, opts)

	h.LogInfo("[INTEGRATION] Generated %d alerts", len(alerts))

	// Should have "all profiles low" alert
	hasAllLow := false
	for _, a := range alerts {
		if a.Type == prediction.AlertAllProfilesLow {
			hasAllLow = true
			break
		}
	}
	assert.True(t, hasAllLow, "should have all-profiles-low alert")
	h.LogInfo("[INTEGRATION] All profiles low alert verified")
}

// =============================================================================
// Integration Tests: SmartRunner + Rotation
// =============================================================================

func TestIntegration_SmartRunnerRotation_UsesRotationAlgorithm(t *testing.T) {
	h := testutil.NewExtendedHarness(t)
	defer h.Close()

	h.LogInfo("[INTEGRATION] Testing SmartRunner uses rotation algorithm")

	// Setup vault with profiles
	vault := authfile.NewVault(h.TempDir)
	createTestProfile(t, vault, "claude", "alice")
	createTestProfile(t, vault, "claude", "bob")

	// Create rotation selector
	selector := rotation.NewSelector(rotation.AlgorithmSmart, nil, nil)

	// Create SmartRunner with rotation
	registry := provider.NewRegistry()
	runner := NewRunner(registry)
	sr := NewSmartRunner(runner, SmartRunnerOptions{
		Vault:    vault,
		Rotation: selector,
		HandoffConfig: &config.HandoffConfig{
			AutoTrigger:      true,
			MaxRetries:       3,
			FallbackToManual: true,
		},
	})

	require.NotNil(t, sr)
	require.NotNil(t, sr.rotation)
	h.LogInfo("[INTEGRATION] SmartRunner created with rotation selector")

	// Verify rotation algorithm is accessible
	assert.Equal(t, selector, sr.rotation)
	h.LogInfo("[INTEGRATION] SmartRunner rotation integration verified")
}

// =============================================================================
// Helper Functions
// =============================================================================

func filterAlerts(alerts []*prediction.Alert, profile string) []*prediction.Alert {
	var filtered []*prediction.Alert
	for _, a := range alerts {
		if a.Profile == profile {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func createTestProfile(t *testing.T, vault *authfile.Vault, provider, profile string) {
	t.Helper()
	dir := vault.ProfilePath(provider, profile)
	require.NoError(t, makeDir(dir))
	require.NoError(t, writeFile(filepath.Join(dir, ".claude.json"), "{}"))
}

func makeDir(path string) error {
	return mkdir(path, 0755)
}

func writeFile(path, content string) error {
	return write(path, []byte(content), 0600)
}

// mkdir and write are aliases for os functions to avoid import conflicts
var mkdir = func(path string, perm int) error {
	return nil // Will be replaced in init
}

var write = func(path string, data []byte, perm int) error {
	return nil // Will be replaced in init
}

func init() {
	mkdir = func(path string, perm int) error {
		return os.MkdirAll(path, os.FileMode(perm))
	}
	write = func(path string, data []byte, perm int) error {
		return os.WriteFile(path, data, os.FileMode(perm))
	}
}
