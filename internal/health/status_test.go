package health

import (
	"testing"
	"time"
)

func TestCalculateHealth(t *testing.T) {
	now := time.Now()
	config := DefaultHealthConfig()

	tests := []struct {
		name           string
		health         *ProfileHealth
		expectedStatus HealthStatus
	}{
		{
			name:           "Nil health",
			health:         nil,
			expectedStatus: StatusUnknown,
		},
		{
			name: "Healthy profile",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(2 * time.Hour),
				ErrorCount1h:   0,
				PlanType:       "pro",
			},
			expectedStatus: StatusHealthy,
		},
		{
			name: "Expired token",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-1 * time.Minute),
				ErrorCount1h:   0,
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "Expiring soon (warning)",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(30 * time.Minute),
				ErrorCount1h:   0,
			},
			expectedStatus: StatusWarning,
		},
		{
			name: "Critical expiry",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(5 * time.Minute),
				ErrorCount1h:   0,
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "High error count",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(2 * time.Hour),
				ErrorCount1h:   5,
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "High penalty",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(2 * time.Hour),
				Penalty:        2.0,
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "Medium penalty",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(2 * time.Hour),
				Penalty:        0.8,
			},
			expectedStatus: StatusWarning, // Score reduced below 0.5
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := CalculateHealth(tt.health, config)
			if status != tt.expectedStatus {
				t.Errorf("expected status %v, got %v", tt.expectedStatus, status)
			}
		})
	}
}

func TestPlanTierOf(t *testing.T) {
	tests := []struct {
		plan string
		want PlanTier
	}{
		{"enterprise", PlanTierEnterprise},
		{" Enterprise ", PlanTierEnterprise},
		{"max", PlanTierHighVolume},
		{"MAX", PlanTierHighVolume},
		{"ultra", PlanTierHighVolume},
		{"premium", PlanTierHighVolume},
		{"pro", PlanTierStandard},
		{"plus", PlanTierStandard},
		{"team", PlanTierStandard},
		{"free", PlanTierUnrated},
		{"", PlanTierUnrated},
		{"claude_pro_2025", PlanTierUnrated},
	}
	for _, tt := range tests {
		if got := PlanTierOf(tt.plan); got != tt.want {
			t.Errorf("PlanTierOf(%q) = %v, want %v", tt.plan, got, tt.want)
		}
	}
	if !(PlanTierUnrated < PlanTierStandard && PlanTierStandard < PlanTierHighVolume && PlanTierHighVolume < PlanTierEnterprise) {
		t.Error("tiers must be ordered unrated < standard < high-volume < enterprise")
	}
}

// TestCalculateHealth_PlanBonusByTier guards PR #87: now that the real plan
// spelling is stored, a Max account must score at least as well as a Pro one
// instead of losing its bonus for no longer being spelled "pro".
func TestCalculateHealth_PlanBonusByTier(t *testing.T) {
	config := DefaultHealthConfig()
	expiry := time.Now().Add(24 * time.Hour)
	scoreFor := func(plan string) float64 {
		_, score := CalculateHealth(&ProfileHealth{TokenExpiresAt: expiry, PlanType: plan}, config)
		return score
	}

	free, pro, team, max, ultra, enterprise := scoreFor("free"), scoreFor("pro"), scoreFor("team"), scoreFor("max"), scoreFor("ultra"), scoreFor("enterprise")
	if pro <= free {
		t.Errorf("pro %v must beat free %v", pro, free)
	}
	if team != pro {
		t.Errorf("team %v must equal pro %v", team, pro)
	}
	if max < pro || ultra < pro {
		t.Errorf("max %v / ultra %v must be at least pro %v", max, ultra, pro)
	}
	if enterprise < max {
		t.Errorf("enterprise %v must be at least max %v", enterprise, max)
	}
	if unknown := scoreFor("claude_pro_2025"); unknown != free {
		t.Errorf("unrecognized plan %v must score like free %v", unknown, free)
	}
}

func TestHealthStatus_String_Icon(t *testing.T) {
	tests := []struct {
		status   HealthStatus
		expected string
		icon     string
	}{
		{StatusHealthy, "healthy", "🟢"},
		{StatusWarning, "warning", "🟡"},
		{StatusCritical, "critical", "🔴"},
		{StatusUnknown, "unknown", "⚪"},
	}

	for _, tt := range tests {
		if got := tt.status.String(); got != tt.expected {
			t.Errorf("expected string %q, got %q", tt.expected, got)
		}
		if got := tt.status.Icon(); got != tt.icon {
			t.Errorf("expected icon %q, got %q", tt.icon, got)
		}
	}
}

// TestCalculateHealthRateLimitCap covers the PR #82 misclassification: an
// active rate-limit cooldown must be reported as rate-limited (warning), not
// escalated to critical by a (possibly stale) expired token timestamp.
func TestCalculateHealthRateLimitCap(t *testing.T) {
	now := time.Now()
	config := DefaultHealthConfig()

	tests := []struct {
		name           string
		health         *ProfileHealth
		expectedStatus HealthStatus
	}{
		{
			name: "active cooldown with stale expired token is warning, not critical",
			health: &ProfileHealth{
				TokenExpiresAt:   now.Add(-5 * 24 * time.Hour),
				ErrorCount1h:     0,
				RateLimitedUntil: now.Add(16 * time.Minute),
			},
			expectedStatus: StatusWarning,
		},
		{
			name: "active cooldown with valid token is warning, not healthy",
			health: &ProfileHealth{
				TokenExpiresAt:   now.Add(6 * time.Hour),
				ErrorCount1h:     0,
				RateLimitedUntil: now.Add(30 * time.Minute),
			},
			expectedStatus: StatusWarning,
		},
		{
			name: "lapsed cooldown does not mask a genuinely expired token",
			health: &ProfileHealth{
				TokenExpiresAt:   now.Add(-1 * time.Hour),
				ErrorCount1h:     0,
				RateLimitedUntil: now.Add(-1 * time.Minute),
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "active cooldown does not mask critical error rate",
			health: &ProfileHealth{
				TokenExpiresAt:   now.Add(-5 * 24 * time.Hour),
				ErrorCount1h:     5,
				RateLimitedUntil: now.Add(16 * time.Minute),
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "expired token without cooldown stays critical",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-1 * time.Minute),
				ErrorCount1h:   0,
			},
			expectedStatus: StatusCritical,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _ := CalculateHealth(tt.health, config)
			if status != tt.expectedStatus {
				t.Errorf("CalculateHealth() status = %v, want %v", status, tt.expectedStatus)
			}
		})
	}
}
