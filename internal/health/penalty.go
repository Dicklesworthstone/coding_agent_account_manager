package health

import (
	"strings"
	"time"
)

const (
	// DecayInterval is the time period after which penalty decays.
	DecayInterval = 5 * time.Minute
	// DecayRate is the multiplier applied to penalty every DecayInterval.
	// 0.8 means 20% decay per interval.
	DecayRate = 0.8
	// MinPenalty is the threshold below which penalty is reset to 0.
	MinPenalty = 0.01
)

// DecayPenalty applies decay to the profile's penalty based on elapsed time.
// It updates the Penalty and PenaltyUpdatedAt fields.
func (h *ProfileHealth) DecayPenalty(now time.Time) {
	if h.PenaltyUpdatedAt.IsZero() {
		h.PenaltyUpdatedAt = now
		return
	}

	elapsed := now.Sub(h.PenaltyUpdatedAt)
	if elapsed < DecayInterval {
		return
	}

	intervals := int(elapsed / DecayInterval)

	if intervals > 0 {
		for i := 0; i < intervals; i++ {
			h.Penalty *= DecayRate
		}
		if h.Penalty < MinPenalty {
			h.Penalty = 0
		}
		// Update time to the most recent interval boundary to preserve partial progress
		// or just set to now? The requirement says "h.PenaltyUpdatedAt = now"
		// simpler approach as per requirements:
		h.PenaltyUpdatedAt = now
	}
}

// AddPenalty applies decay first, then adds the new penalty amount.
func (h *ProfileHealth) AddPenalty(amount float64, now time.Time) {
	h.DecayPenalty(now)
	h.Penalty += amount
	h.PenaltyUpdatedAt = now
}

// PenaltyForError determines the penalty amount based on the error type.
func PenaltyForError(err error) float64 {
	if err == nil {
		return 0
	}

	// This is a simple string matching approach.
	// In a real system, we might check for specific error types or codes.
	msg := strings.ToLower(err.Error())

	if isAuthError(msg) {
		return 1.0
	}
	if isRateLimitError(msg) {
		return 0.5
	}
	if isServerError(msg) {
		return 0.3
	}
	if isTimeoutError(msg) {
		return 0.2
	}
	
	// Default penalty for other errors
	return 0.1
}

func isAuthError(msg string) bool {
	return strings.Contains(msg, "401") || 
		strings.Contains(msg, "403") || 
		strings.Contains(msg, "unauthorized") || 
		strings.Contains(msg, "forbidden") ||
		strings.Contains(msg, "authentication failed") ||
		strings.Contains(msg, "invalid token")
}

func isRateLimitError(msg string) bool {
	return strings.Contains(msg, "429") || 
		strings.Contains(msg, "rate limit") || 
		strings.Contains(msg, "too many requests") ||
		strings.Contains(msg, "quota exceeded")
}

func isServerError(msg string) bool {
	return strings.Contains(msg, "500") || 
		strings.Contains(msg, "502") || 
		strings.Contains(msg, "503") || 
		strings.Contains(msg, "504") ||
		strings.Contains(msg, "internal server error") ||
		strings.Contains(msg, "bad gateway") ||
		strings.Contains(msg, "service unavailable")
}

func isTimeoutError(msg string) bool {
	return strings.Contains(msg, "timeout") || 
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "context deadline")
}
