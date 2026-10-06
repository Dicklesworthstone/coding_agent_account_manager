package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestVerifyProfileResult(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	result := VerifyProfileResult{
		Provider:    "claude",
		Profile:     "work",
		Status:      "healthy",
		TokenExpiry: &expiry,
		ExpiresIn:   "59m",
		ErrorCount:  0,
		Penalty:     0,
		Issues:      []string{},
		Score:       1.3,
	}

	// Test JSON serialization
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal VerifyProfileResult: %v", err)
	}

	var decoded VerifyProfileResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal VerifyProfileResult: %v", err)
	}

	if decoded.Provider != result.Provider {
		t.Errorf("Provider mismatch: got %s, want %s", decoded.Provider, result.Provider)
	}
	if decoded.Profile != result.Profile {
		t.Errorf("Profile mismatch: got %s, want %s", decoded.Profile, result.Profile)
	}
	if decoded.Status != result.Status {
		t.Errorf("Status mismatch: got %s, want %s", decoded.Status, result.Status)
	}
}

func TestVerifyOutput(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	output := VerifyOutput{
		Profiles: []VerifyProfileResult{
			{
				Provider:    "claude",
				Profile:     "work",
				Status:      "healthy",
				TokenExpiry: &expiry,
				ExpiresIn:   "59m",
				Score:       1.3,
			},
			{
				Provider:    "claude",
				Profile:     "personal",
				Status:      "warning",
				TokenExpiry: &expiry,
				ExpiresIn:   "45m",
				ErrorCount:  1,
				Issues:      []string{"Token expiring soon"},
				Score:       0.4,
			},
		},
		Summary: VerifySummary{
			TotalProfiles: 2,
			HealthyCount:  1,
			WarningCount:  1,
			CriticalCount: 0,
			UnknownCount:  0,
		},
		Recommendations: []string{
			"Run 'caam refresh claude personal' to refresh expiring token",
		},
	}

	// Test JSON serialization
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal VerifyOutput: %v", err)
	}

	var decoded VerifyOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal VerifyOutput: %v", err)
	}

	if decoded.Summary.TotalProfiles != 2 {
		t.Errorf("TotalProfiles mismatch: got %d, want 2", decoded.Summary.TotalProfiles)
	}
	if decoded.Summary.HealthyCount != 1 {
		t.Errorf("HealthyCount mismatch: got %d, want 1", decoded.Summary.HealthyCount)
	}
}

func TestFormatTimeRemaining(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "< 1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{24 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
		{-5 * time.Minute, "5m"}, // Handles negative (expired)
	}

	for _, tc := range tests {
		got := formatTimeRemaining(tc.duration)
		if got != tc.expected {
			t.Errorf("formatTimeRemaining(%v) = %q, want %q", tc.duration, got, tc.expected)
		}
	}
}

func TestGetVerifyStatusIcon(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"healthy", "✓"},
		{"warning", "⚠"},
		{"critical", "✗"},
		{"unknown", "?"},
		{"invalid", "?"},
	}

	for _, tc := range tests {
		got := getVerifyStatusIcon(tc.status)
		if got != tc.expected {
			t.Errorf("getVerifyStatusIcon(%q) = %q, want %q", tc.status, got, tc.expected)
		}
	}
}

func TestGenerateRecommendations(t *testing.T) {
	now := time.Now()
	expiredTime := now.Add(-time.Hour)
	expiringSoonTime := now.Add(30 * time.Minute)

	output := &VerifyOutput{
		Profiles: []VerifyProfileResult{
			{
				Provider:    "claude",
				Profile:     "work",
				Status:      "critical",
				TokenExpiry: &expiredTime,
				ExpiresIn:   "expired",
			},
			{
				Provider:    "codex",
				Profile:     "dev",
				Status:      "warning",
				TokenExpiry: &expiringSoonTime,
				ExpiresIn:   "30m",
			},
		},
		Summary: VerifySummary{
			TotalProfiles: 2,
			CriticalCount: 1,
			WarningCount:  1,
		},
	}

	recs := generateRecommendations(output)

	// Should have recommendations for expiring and expired tokens
	if len(recs) == 0 {
		t.Error("Expected recommendations to be generated")
	}

	// Check for refresh recommendation
	hasRefresh := false
	for _, r := range recs {
		if strings.Contains(r, "refresh") {
			hasRefresh = true
			break
		}
	}
	if !hasRefresh {
		t.Error("Expected a refresh recommendation for expiring token")
	}

	// Check for re-login recommendation
	hasRelogin := false
	for _, r := range recs {
		if strings.Contains(r, "Re-login") {
			hasRelogin = true
			break
		}
	}
	if !hasRelogin {
		t.Error("Expected a re-login recommendation for expired token")
	}
}

func TestCursorSessionVerifyRecommendation(t *testing.T) {
	expires := time.Now().Add(12 * time.Hour)
	output := &VerifyOutput{Profiles: []VerifyProfileResult{{
		Provider: "cursor", Profile: "session", TokenExpiry: &expires,
		ReloginWarningLead: 24 * time.Hour,
	}}}
	recs := generateRecommendations(output)
	if len(recs) != 1 || !strings.Contains(recs[0], "caam login cursor session") || strings.Contains(recs[0], "refresh") {
		t.Fatalf("non-renewable session recommendation = %v", recs)
	}
}

func TestPrintVerifyOutput(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	output := &VerifyOutput{
		Profiles: []VerifyProfileResult{
			{
				Provider:    "claude",
				Profile:     "work",
				Status:      "healthy",
				TokenExpiry: &expiry,
				ExpiresIn:   "59m",
				Score:       1.3,
			},
		},
		Summary: VerifySummary{
			TotalProfiles: 1,
			HealthyCount:  1,
		},
	}

	var buf bytes.Buffer
	printVerifyOutput(&buf, output)
	result := buf.String()

	// Check for expected output elements
	if !strings.Contains(result, "Profile Health Verification") {
		t.Error("Expected output to contain header")
	}
	if !strings.Contains(result, "Claude:") {
		t.Error("Expected output to contain provider name")
	}
	if !strings.Contains(result, "work") {
		t.Error("Expected output to contain profile name")
	}
	if !strings.Contains(result, "✓") {
		t.Error("Expected output to contain healthy status icon")
	}
	if !strings.Contains(result, "Summary:") {
		t.Error("Expected output to contain summary")
	}
}

func TestPrintVerifyOutputNoProfiles(t *testing.T) {
	output := &VerifyOutput{
		Profiles: []VerifyProfileResult{},
		Summary:  VerifySummary{},
	}

	var buf bytes.Buffer
	printVerifyOutput(&buf, output)
	result := buf.String()

	if !strings.Contains(result, "No profiles found") {
		t.Error("Expected 'No profiles found' message for empty profile list")
	}
}
