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
			name: "Expired live token without refresh is critical",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-1 * time.Minute),
				ErrorCount1h:   0,
				ExpiryLive:     true,
			},
			expectedStatus: StatusCritical,
		},
		{
			name: "Stale snapshot expiry with no errors is unknown, never critical",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-5 * 24 * time.Hour),
				ErrorCount1h:   0,
				ExpiryLive:     false,
			},
			expectedStatus: StatusUnknown,
		},
		{
			name: "Expired live access token with refresh and no errors is not critical",
			health: &ProfileHealth{
				TokenExpiresAt:  now.Add(-1 * time.Minute),
				ErrorCount1h:    0,
				ExpiryLive:      true,
				HasRefreshToken: true,
			},
			expectedStatus: StatusHealthy,
		},
		{
			name: "Rate-limited cooldown outranks stale snapshot expiry",
			health: &ProfileHealth{
				TokenExpiresAt: now.Add(-5 * 24 * time.Hour),
				ErrorCount1h:   0,
				ExpiryLive:     false,
				CooldownUntil:  now.Add(16 * time.Minute),
			},
			expectedStatus: StatusWarning,
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
