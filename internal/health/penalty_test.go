package health

import (
	"errors"
	"testing"
	"time"
)

func TestDecayPenalty(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name             string
		initialPenalty   float64
		lastUpdated      time.Time
		checkTime        time.Time
		expectedPenalty  float64
		shouldBeZero     bool
		expectTimeUpdate bool
	}{
		{
			name:             "No decay if less than interval",
			initialPenalty:   1.0,
			lastUpdated:      now,
			checkTime:        now.Add(4 * time.Minute),
			expectedPenalty:  1.0,
			shouldBeZero:     false,
			expectTimeUpdate: false,
		},
		{
			name:             "Single decay interval (20%)",
			initialPenalty:   1.0,
			lastUpdated:      now,
			checkTime:        now.Add(5 * time.Minute),
			expectedPenalty:  0.8, // 1.0 * 0.8
			expectTimeUpdate: true,
		},
		{
			name:             "Two decay intervals",
			initialPenalty:   1.0,
			lastUpdated:      now,
			checkTime:        now.Add(10 * time.Minute),
			expectedPenalty:  0.64, // 1.0 * 0.8 * 0.8
			expectTimeUpdate: true,
		},
		{
			name:             "Reset to zero if below min",
			initialPenalty:   0.012,
			lastUpdated:      now,
			checkTime:        now.Add(5 * time.Minute),
			expectedPenalty:  0.0, // 0.012 * 0.8 = 0.0096 < 0.01
			shouldBeZero:     true,
			expectTimeUpdate: true,
		},
		{
			name:             "First time update",
			initialPenalty:   1.0,
			lastUpdated:      time.Time{}, // Zero time
			checkTime:        now,
			expectedPenalty:  1.0,
			expectTimeUpdate: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &ProfileHealth{
				Penalty:          tt.initialPenalty,
				PenaltyUpdatedAt: tt.lastUpdated,
			}

			h.DecayPenalty(tt.checkTime)

			if tt.shouldBeZero {
				if h.Penalty != 0 {
					t.Errorf("expected penalty to be 0, got %f", h.Penalty)
				}
			} else {
				// Use small epsilon for float comparison
				if diff := h.Penalty - tt.expectedPenalty; diff > 0.0001 || diff < -0.0001 {
					t.Errorf("expected penalty %f, got %f", tt.expectedPenalty, h.Penalty)
				}
			}

			expectedTime := tt.checkTime
			if !tt.expectTimeUpdate {
				expectedTime = tt.lastUpdated
			}
			
			if !h.PenaltyUpdatedAt.Equal(expectedTime) {
				t.Errorf("expected updated time %v, got %v", expectedTime, h.PenaltyUpdatedAt)
			}
		})
	}
}

func TestAddPenalty(t *testing.T) {
	now := time.Now()
	h := &ProfileHealth{
		Penalty:          1.0,
		PenaltyUpdatedAt: now,
	}

	// Move forward 5 minutes (one decay interval) and add penalty
	// Expected: (1.0 * 0.8) + 0.5 = 1.3
	checkTime := now.Add(5 * time.Minute)
	h.AddPenalty(0.5, checkTime)

	expected := 1.3
	if diff := h.Penalty - expected; diff > 0.0001 || diff < -0.0001 {
		t.Errorf("expected penalty %f, got %f", expected, h.Penalty)
	}

	if !h.PenaltyUpdatedAt.Equal(checkTime) {
		t.Errorf("expected updated time %v, got %v", checkTime, h.PenaltyUpdatedAt)
	}
}

func TestPenaltyForError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected float64
	}{
		{
			name:     "Auth error (401)",
			err:      errors.New("API returned 401 Unauthorized"),
			expected: 1.0,
		},
		{
			name:     "Rate limit error (429)",
			err:      errors.New("429 Too Many Requests"),
			expected: 0.5,
		},
		{
			name:     "Server error (500)",
			err:      errors.New("500 Internal Server Error"),
			expected: 0.3,
		},
		{
			name:     "Timeout error",
			err:      errors.New("context deadline exceeded"),
			expected: 0.2,
		},
		{
			name:     "Generic error",
			err:      errors.New("something went wrong"),
			expected: 0.1,
		},
		{
			name:     "Nil error",
			err:      nil,
			expected: 0.0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PenaltyForError(tt.err)
			if got != tt.expected {
				t.Errorf("expected %f, got %f", tt.expected, got)
			}
		})
	}
}
