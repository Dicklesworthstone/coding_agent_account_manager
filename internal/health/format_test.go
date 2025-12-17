package health

import (
	"strings"
	"testing"
	"time"
)

func TestFormatTimeRemaining(t *testing.T) {
	tests := []struct {
		name     string
		expiry   time.Time
		expected string
	}{
		{
			name:     "Expired",
			expiry:   time.Now().Add(-time.Hour),
			expected: "Expired",
		},
		{
			name:     "Less than a minute",
			expiry:   time.Now().Add(30 * time.Second),
			expected: "< 1m left",
		},
		{
			name:     "Minutes",
			expiry:   time.Now().Add(45 * time.Minute),
			expected: "45m left",
		},
		{
			name:     "Hours",
			expiry:   time.Now().Add(3 * time.Hour),
			expected: "3h left",
		},
		{
			name:     "Hours and minutes (short)",
			expiry:   time.Now().Add(2*time.Hour + 30*time.Minute),
			expected: "2h30m left",
		},
		{
			name:     "Days",
			expiry:   time.Now().Add(48 * time.Hour),
			expected: "2d left",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatTimeRemaining(tt.expiry)
			if got != tt.expected {
				t.Errorf("FormatTimeRemaining() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestFormatHealthStatus(t *testing.T) {
	now := time.Now()
	opts := FormatOptions{NoColor: true}

	tests := []struct {
		name     string
		status   HealthStatus
		health   *ProfileHealth
		contains string
	}{
		{
			name:     "Healthy with time",
			status:   StatusHealthy,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(2 * time.Hour)},
			contains: "2h",
		},
		{
			name:     "Warning with time",
			status:   StatusWarning,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(30 * time.Minute)},
			contains: "30m",
		},
		{
			name:     "Critical expired",
			status:   StatusCritical,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(-time.Hour)},
			contains: "Expired",
		},
		{
			name:     "Unknown nil health",
			status:   StatusUnknown,
			health:   nil,
			contains: "Unknown",
		},
		{
			name:     "With errors",
			status:   StatusWarning,
			health:   &ProfileHealth{ErrorCount1h: 2},
			contains: "errors",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatHealthStatus(tt.status, tt.health, opts)
			if !strings.Contains(got, tt.contains) {
				t.Errorf("FormatHealthStatus() = %q, want to contain %q", got, tt.contains)
			}
			// Should contain icon
			if !strings.ContainsAny(got, "🟢🟡🔴⚪") {
				t.Errorf("FormatHealthStatus() = %q, should contain an icon", got)
			}
		})
	}
}

func TestFormatStatusWithReason(t *testing.T) {
	now := time.Now()
	opts := FormatOptions{NoColor: true}

	tests := []struct {
		name     string
		status   HealthStatus
		health   *ProfileHealth
		contains []string
	}{
		{
			name:     "Healthy",
			status:   StatusHealthy,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(2 * time.Hour)},
			contains: []string{"🟢", "Healthy"},
		},
		{
			name:     "Expiring soon",
			status:   StatusWarning,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(10 * time.Minute)},
			contains: []string{"🟡", "Token expires"},
		},
		{
			name:     "Expired",
			status:   StatusCritical,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(-time.Hour)},
			contains: []string{"🔴", "Token expired"},
		},
		{
			name:     "With errors",
			status:   StatusWarning,
			health:   &ProfileHealth{TokenExpiresAt: now.Add(2 * time.Hour), ErrorCount1h: 2},
			contains: []string{"error"},
		},
		{
			name:     "Unknown",
			status:   StatusUnknown,
			health:   nil,
			contains: []string{"⚪", "Unknown"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatStatusWithReason(tt.status, tt.health, opts)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("FormatStatusWithReason() = %q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestFormatRecommendation(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name     string
		provider string
		profile  string
		health   *ProfileHealth
		contains string
		empty    bool
	}{
		{
			name:     "Nil health",
			provider: "claude",
			profile:  "test",
			health:   nil,
			empty:    true,
		},
		{
			name:     "Expired token",
			provider: "claude",
			profile:  "test",
			health:   &ProfileHealth{TokenExpiresAt: now.Add(-time.Hour)},
			contains: "login",
		},
		{
			name:     "Expiring token",
			provider: "codex",
			profile:  "work",
			health:   &ProfileHealth{TokenExpiresAt: now.Add(30 * time.Minute)},
			contains: "refresh",
		},
		{
			name:     "High errors",
			provider: "gemini",
			profile:  "main",
			health:   &ProfileHealth{TokenExpiresAt: now.Add(2 * time.Hour), ErrorCount1h: 5},
			contains: "switching",
		},
		{
			name:     "Healthy",
			provider: "claude",
			profile:  "work",
			health:   &ProfileHealth{TokenExpiresAt: now.Add(5 * time.Hour)},
			empty:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatRecommendation(tt.provider, tt.profile, tt.health)
			if tt.empty {
				if got != "" {
					t.Errorf("FormatRecommendation() = %q, want empty", got)
				}
			} else {
				if !strings.Contains(got, tt.contains) {
					t.Errorf("FormatRecommendation() = %q, want to contain %q", got, tt.contains)
				}
			}
		})
	}
}

func TestFormatPlanType(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"enterprise", "Enterprise"},
		{"ENTERPRISE", "Enterprise"},
		{"pro", "Pro"},
		{"Pro", "Pro"},
		{"team", "Team"},
		{"free", "Free"},
		{"", ""},
		{"custom", "custom"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := FormatPlanType(tt.input)
			if got != tt.expected {
				t.Errorf("FormatPlanType(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestColorizeStatus(t *testing.T) {
	// Test that colorization doesn't crash and returns non-empty
	statuses := []HealthStatus{StatusHealthy, StatusWarning, StatusCritical, StatusUnknown}
	for _, s := range statuses {
		result := colorizeStatus(s, "test")
		if result == "" {
			t.Errorf("colorizeStatus(%v) returned empty string", s)
		}
	}
}

func TestFormatDurationNatural(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "less than a minute"},
		{1 * time.Minute, "1 minute"},
		{5 * time.Minute, "5 minutes"},
		{1 * time.Hour, "1 hour"},
		{3 * time.Hour, "3 hours"},
		{24 * time.Hour, "1 day"},
		{72 * time.Hour, "3 days"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			got := formatDurationNatural(tt.duration)
			if got != tt.expected {
				t.Errorf("formatDurationNatural(%v) = %q, want %q", tt.duration, got, tt.expected)
			}
		})
	}
}
