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

// TestCalculateHealthSelfRefreshing covers issue #22: a credential the
// provider's own CLI renews in place (Claude Code) must not be downgraded by
// its short access-token TTL, while every other signal keeps working.
func TestCalculateHealthSelfRefreshing(t *testing.T) {
	now := time.Now()
	config := DefaultHealthConfig()

	tests := []struct {
		name           string
		health         *ProfileHealth
		expectedStatus HealthStatus
	}{
		{
			name: "access token expiring within the warning window stays healthy",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(20 * time.Minute),
				SelfRefreshing: true,
			},
			expectedStatus: StatusHealthy,
		},
		{
			name: "lapsed access token stays healthy",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-2 * time.Hour),
				SelfRefreshing: true,
			},
			expectedStatus: StatusHealthy,
		},
		{
			name: "same token without self-refresh is critical",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-2 * time.Hour),
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "an active cap still outranks self-refresh",
			health: &ProfileHealth{
				TokenExpiresAt:   now.Add(20 * time.Minute),
				SelfRefreshing:   true,
				RateLimitedUntil: now.Add(30 * time.Minute),
			},
			expectedStatus: StatusWarning,
		},
		{
			name: "errors still escalate a self-refreshing profile",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(20 * time.Minute),
				SelfRefreshing: true,
				ErrorCount1h:   5,
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
