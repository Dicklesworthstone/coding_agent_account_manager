package cmd

import (
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/sync"
)

// Test helper functions that are exported

func TestGetStatusIcon(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{sync.StatusOnline, "🟢"},
		{sync.StatusOffline, "🔴"},
		{sync.StatusSyncing, "🔄"},
		{sync.StatusError, "⚠️"},
		{sync.StatusUnknown, "⚪"},
		{"other", "⚪"},
	}

	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			got := getStatusIcon(tt.status)
			if got != tt.expected {
				t.Errorf("getStatusIcon(%q) = %q, want %q", tt.status, got, tt.expected)
			}
		})
	}
}

func TestFormatTimeAgo(t *testing.T) {
	tests := []struct {
		name     string
		ago      time.Duration
		expected string
	}{
		{"just now", 30 * time.Second, "just now"},
		{"1 minute", 1 * time.Minute, "1 min ago"},
		{"5 minutes", 5 * time.Minute, "5 mins ago"},
		{"1 hour", 1 * time.Hour, "1 hour ago"},
		{"3 hours", 3 * time.Hour, "3 hours ago"},
		{"1 day", 25 * time.Hour, "1 day ago"},
		{"3 days", 72 * time.Hour, "3 days ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create time that was tt.ago in the past
			tm := time.Now().Add(-tt.ago)
			got := formatTimeAgo(tm)
			if got != tt.expected {
				t.Errorf("formatTimeAgo() = %q, want %q", got, tt.expected)
			}
		})
	}
}
