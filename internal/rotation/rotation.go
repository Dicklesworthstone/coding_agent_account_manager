// Package rotation implements smart profile selection algorithms for caam.
//
// Instead of always selecting profiles in predictable patterns, rotation algorithms
// help vary which accounts are used and when, reducing detectable patterns.
//
// Three algorithms are available:
//   - smart: Multi-factor scoring based on health, cooldown, recency, and usage balance
//   - round_robin: Simple sequential rotation through available profiles
//   - random: Random selection (least predictable but may cluster)
package rotation

import (
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"time"

	caamdb "github.com/Dicklesworthstone/coding_agent_account_manager/internal/db"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
)

// Algorithm identifies a rotation algorithm.
type Algorithm string

const (
	// AlgorithmSmart uses multi-factor scoring to select the best profile.
	AlgorithmSmart Algorithm = "smart"

	// AlgorithmRoundRobin rotates sequentially through profiles.
	AlgorithmRoundRobin Algorithm = "round_robin"

	// AlgorithmRandom selects a profile at random.
	AlgorithmRandom Algorithm = "random"
)

// Reason explains why a profile was or wasn't selected.
type Reason struct {
	Text     string // Human-readable explanation
	Positive bool   // True if this is a good thing, false if it's a problem
}

// ProfileScore holds scoring information for a single profile.
type ProfileScore struct {
	Name    string   // Profile name
	Score   float64  // Numerical score (higher is better)
	Reasons []Reason // Human-readable explanations
}

// Result is the output of a rotation selection.
type Result struct {
	Selected     string         // The profile that was selected
	Alternatives []ProfileScore // Other profiles with their scores, sorted by score desc
	Algorithm    Algorithm      // Which algorithm was used
}

// UsageInfo represents real-time rate limit usage for a profile.
type UsageInfo struct {
	ProfileName      string
	PrimaryPercent   int     // Primary window usage (0-100)
	SecondaryPercent int     // Secondary window usage (0-100)
	AvailScore       int     // Availability score (0-100, higher is better)
	Error            string  // Error message if fetch failed
}

// Selector performs profile selection based on configured algorithm.
type Selector struct {
	algorithm   Algorithm
	healthStore *health.Storage
	db          *caamdb.DB
	rng         *rand.Rand
	avoidRecent time.Duration // Don't select profiles used within this duration
	usageData   map[string]*UsageInfo // Real-time usage data by profile name
}

// NewSelector creates a new profile selector.
func NewSelector(algorithm Algorithm, healthStore *health.Storage, db *caamdb.DB) *Selector {
	return &Selector{
		algorithm:   algorithm,
		healthStore: healthStore,
		db:          db,
		rng:         rand.New(rand.NewSource(time.Now().UnixNano())),
		avoidRecent: 30 * time.Minute, // Default: avoid profiles used in last 30 min
	}
}

// SetRNG sets a custom random number generator (useful for testing).
func (s *Selector) SetRNG(rng *rand.Rand) {
	s.rng = rng
}

// SetAvoidRecent sets how long to avoid recently-used profiles.
func (s *Selector) SetAvoidRecent(d time.Duration) {
	s.avoidRecent = d
}

// SetUsageData sets real-time usage data for consideration in smart selection.
func (s *Selector) SetUsageData(usage map[string]*UsageInfo) {
	s.usageData = usage
}

// Select chooses a profile from the given list using the configured algorithm.
// Returns an error if no profiles are available or all are in cooldown.
func (s *Selector) Select(tool string, profiles []string, currentProfile string) (*Result, error) {
	if len(profiles) == 0 {
		return nil, fmt.Errorf("no profiles available for %s", tool)
	}

	// Filter out system profiles (those starting with _)
	var available []string
	for _, p := range profiles {
		if !strings.HasPrefix(p, "_") {
			available = append(available, p)
		}
	}

	if len(available) == 0 {
		return nil, fmt.Errorf("no user profiles available for %s (only system profiles found)", tool)
	}

	// If only one profile, return it
	if len(available) == 1 {
		return &Result{
			Selected:  available[0],
			Algorithm: s.algorithm,
			Alternatives: []ProfileScore{{
				Name:    available[0],
				Score:   100,
				Reasons: []Reason{{Text: "Only available profile", Positive: true}},
			}},
		}, nil
	}

	switch s.algorithm {
	case AlgorithmRandom:
		return s.selectRandom(tool, available)
	case AlgorithmRoundRobin:
		return s.selectRoundRobin(tool, available, currentProfile)
	default:
		return s.selectSmart(tool, available)
	}
}

// selectRandom picks a profile at random.
func (s *Selector) selectRandom(tool string, profiles []string) (*Result, error) {
	// Filter out profiles in cooldown
	var eligible []string
	var inCooldown []ProfileScore

	now := time.Now()
	for _, p := range profiles {
		if s.isInCooldown(tool, p, now) {
			remaining := s.cooldownRemaining(tool, p, now)
			inCooldown = append(inCooldown, ProfileScore{
				Name:    p,
				Score:   -1000,
				Reasons: []Reason{{Text: fmt.Sprintf("In cooldown (%s remaining)", formatDuration(remaining)), Positive: false}},
			})
		} else {
			eligible = append(eligible, p)
		}
	}

	if len(eligible) == 0 {
		return nil, fmt.Errorf("all profiles for %s are in cooldown", tool)
	}

	idx := s.rng.Intn(len(eligible))
	selected := eligible[idx]

	// Build alternatives list
	var alternatives []ProfileScore
	for _, p := range eligible {
		reasons := []Reason{{Text: "Random selection", Positive: true}}
		score := 100.0
		if p == selected {
			reasons = append(reasons, Reason{Text: "Selected", Positive: true})
		}
		alternatives = append(alternatives, ProfileScore{Name: p, Score: score, Reasons: reasons})
	}
	alternatives = append(alternatives, inCooldown...)

	return &Result{
		Selected:     selected,
		Algorithm:    AlgorithmRandom,
		Alternatives: alternatives,
	}, nil
}

// selectRoundRobin picks the next profile in sequence.
func (s *Selector) selectRoundRobin(tool string, profiles []string, currentProfile string) (*Result, error) {
	// Sort profiles for consistent ordering
	sorted := make([]string, len(profiles))
	copy(sorted, profiles)
	sort.Strings(sorted)

	// Find current index
	currentIdx := -1
	for i, p := range sorted {
		if p == currentProfile {
			currentIdx = i
			break
		}
	}

	// Filter out profiles in cooldown, find next available
	now := time.Now()
	var alternatives []ProfileScore

	for _, p := range sorted {
		if s.isInCooldown(tool, p, now) {
			remaining := s.cooldownRemaining(tool, p, now)
			alternatives = append(alternatives, ProfileScore{
				Name:    p,
				Score:   -1000,
				Reasons: []Reason{{Text: fmt.Sprintf("In cooldown (%s remaining)", formatDuration(remaining)), Positive: false}},
			})
		}
	}

	// Try each profile starting from the one after current
	for i := 0; i < len(sorted); i++ {
		nextIdx := (currentIdx + 1 + i) % len(sorted)
		candidate := sorted[nextIdx]

		if !s.isInCooldown(tool, candidate, now) {
			// Add all non-cooldown profiles to alternatives
			for j, p := range sorted {
				if !s.isInCooldown(tool, p, now) {
					position := (j - currentIdx + len(sorted)) % len(sorted)
					reasons := []Reason{{Text: fmt.Sprintf("Position %d in rotation", position), Positive: true}}
					if p == candidate {
						reasons = append(reasons, Reason{Text: "Next in sequence", Positive: true})
					}
					alternatives = append(alternatives, ProfileScore{
						Name:    p,
						Score:   float64(len(sorted) - position),
						Reasons: reasons,
					})
				}
			}

			// Sort by score desc
			sort.Slice(alternatives, func(i, j int) bool {
				return alternatives[i].Score > alternatives[j].Score
			})

			return &Result{
				Selected:     candidate,
				Algorithm:    AlgorithmRoundRobin,
				Alternatives: alternatives,
			}, nil
		}
	}

	return nil, fmt.Errorf("all profiles for %s are in cooldown", tool)
}

// selectSmart uses multi-factor scoring to select the best profile.
func (s *Selector) selectSmart(tool string, profiles []string) (*Result, error) {
	now := time.Now()
	var scores []ProfileScore

	for _, p := range profiles {
		score := ProfileScore{Name: p, Score: 0}

		// Factor 1: Cooldown (disqualifying)
		if s.isInCooldown(tool, p, now) {
			remaining := s.cooldownRemaining(tool, p, now)
			score.Score = -10000
			score.Reasons = append(score.Reasons, Reason{
				Text:     fmt.Sprintf("In cooldown (%s remaining)", formatDuration(remaining)),
				Positive: false,
			})
			scores = append(scores, score)
			continue
		}

		// Factor 2: Health status
		if s.healthStore != nil {
			h, err := s.healthStore.GetProfile(tool, p)
			if err == nil && h != nil {
				status := health.CalculateStatus(h)
				switch status {
				case health.StatusHealthy:
					score.Score += 100
					if !h.TokenExpiresAt.IsZero() {
						ttl := time.Until(h.TokenExpiresAt)
						score.Reasons = append(score.Reasons, Reason{
							Text:     fmt.Sprintf("Healthy token (expires in %s)", formatDuration(ttl)),
							Positive: true,
						})
					} else {
						score.Reasons = append(score.Reasons, Reason{
							Text:     "Healthy status",
							Positive: true,
						})
					}
				case health.StatusWarning:
					score.Score += 50
					if !h.TokenExpiresAt.IsZero() {
						ttl := time.Until(h.TokenExpiresAt)
						score.Reasons = append(score.Reasons, Reason{
							Text:     fmt.Sprintf("Token expiring soon (%s)", formatDuration(ttl)),
							Positive: false,
						})
					}
				case health.StatusCritical:
					score.Score -= 50
					score.Reasons = append(score.Reasons, Reason{
						Text:     "Critical status (token expired or many errors)",
						Positive: false,
					})
				}

				// Penalty factor
				if h.Penalty > 0 {
					score.Score -= h.Penalty * 10
					score.Reasons = append(score.Reasons, Reason{
						Text:     fmt.Sprintf("Has penalty score (%.1f)", h.Penalty),
						Positive: false,
					})
				}

				// Plan type bonus
				switch h.PlanType {
				case "enterprise":
					score.Score += 30
					score.Reasons = append(score.Reasons, Reason{
						Text:     "Enterprise plan",
						Positive: true,
					})
				case "pro":
					score.Score += 20
					score.Reasons = append(score.Reasons, Reason{
						Text:     "Pro plan",
						Positive: true,
					})
				case "team":
					score.Score += 20
					score.Reasons = append(score.Reasons, Reason{
						Text:     "Team plan",
						Positive: true,
					})
				}
			} else {
				score.Reasons = append(score.Reasons, Reason{
					Text:     "No health data available",
					Positive: false,
				})
			}
		}

		// Factor 3: Recency (prefer profiles not used recently)
		lastUsed := s.getLastActivation(tool, p)
		if !lastUsed.IsZero() {
			since := time.Since(lastUsed)
			if since < s.avoidRecent {
				penalty := float64(s.avoidRecent-since) / float64(time.Hour) * 50
				score.Score -= penalty
				score.Reasons = append(score.Reasons, Reason{
					Text:     fmt.Sprintf("Used recently (%s ago)", formatDuration(since)),
					Positive: false,
				})
			} else {
				bonus := float64(since) / float64(time.Hour) * 5
				if bonus > 50 {
					bonus = 50 // Cap the bonus
				}
				score.Score += bonus
				score.Reasons = append(score.Reasons, Reason{
					Text:     fmt.Sprintf("Not used recently (%s ago)", formatDuration(since)),
					Positive: true,
				})
			}
		} else {
			score.Score += 25 // Slight bonus for never-used profiles
			score.Reasons = append(score.Reasons, Reason{
				Text:     "Never used before",
				Positive: true,
			})
		}

		// Factor 4: Real-time rate limit usage (if available)
		if s.usageData != nil {
			if usage, ok := s.usageData[p]; ok && usage.Error == "" {
				// Use availability score (0-100, higher is better)
				// Convert to bonus: 100 avail = +100 bonus, 0 avail = -100 penalty
				usageBonus := float64(usage.AvailScore) - 50 // Center around 0
				score.Score += usageBonus

				if usage.PrimaryPercent >= 80 {
					score.Reasons = append(score.Reasons, Reason{
						Text:     fmt.Sprintf("Primary limit %d%% used (near limit)", usage.PrimaryPercent),
						Positive: false,
					})
				} else if usage.PrimaryPercent <= 30 {
					score.Reasons = append(score.Reasons, Reason{
						Text:     fmt.Sprintf("Primary limit %d%% used (plenty available)", usage.PrimaryPercent),
						Positive: true,
					})
				} else {
					score.Reasons = append(score.Reasons, Reason{
						Text:     fmt.Sprintf("Primary limit %d%% used", usage.PrimaryPercent),
						Positive: true,
					})
				}

				if usage.SecondaryPercent >= 80 {
					score.Score -= 30 // Extra penalty for high secondary usage
					score.Reasons = append(score.Reasons, Reason{
						Text:     fmt.Sprintf("Secondary limit %d%% used (near limit)", usage.SecondaryPercent),
						Positive: false,
					})
				}
			} else if usage != nil && usage.Error != "" {
				// Fetching failed - slight penalty but don't disqualify
				score.Score -= 10
				score.Reasons = append(score.Reasons, Reason{
					Text:     "Usage data unavailable",
					Positive: false,
				})
			}
		}

		// Factor 5: Small random jitter to break ties
		jitter := s.rng.Float64() * 5
		score.Score += jitter

		scores = append(scores, score)
	}

	// Sort by score descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].Score > scores[j].Score
	})

	// Check if all profiles are in cooldown
	if len(scores) == 0 {
		return nil, fmt.Errorf("no profiles available for %s", tool)
	}

	if scores[0].Score < -9000 {
		return nil, fmt.Errorf("all profiles for %s are in cooldown", tool)
	}

	return &Result{
		Selected:     scores[0].Name,
		Algorithm:    AlgorithmSmart,
		Alternatives: scores,
	}, nil
}

// isInCooldown checks if a profile is currently in cooldown.
func (s *Selector) isInCooldown(tool, profile string, now time.Time) bool {
	if s.db == nil {
		return false
	}

	ev, err := s.db.ActiveCooldown(tool, profile, now)
	if err != nil || ev == nil {
		return false
	}
	return true
}

// cooldownRemaining returns how long until cooldown expires.
func (s *Selector) cooldownRemaining(tool, profile string, now time.Time) time.Duration {
	if s.db == nil {
		return 0
	}

	ev, err := s.db.ActiveCooldown(tool, profile, now)
	if err != nil || ev == nil {
		return 0
	}

	remaining := ev.CooldownUntil.Sub(now)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// getLastActivation returns when a profile was last activated.
// Returns zero time if unknown.
func (s *Selector) getLastActivation(tool, profile string) time.Time {
	if s == nil || s.db == nil {
		return time.Time{}
	}

	ts, err := s.db.LastActivation(tool, profile)
	if err != nil {
		return time.Time{}
	}
	return ts
}

// formatDuration returns a human-friendly duration string.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = -d
	}

	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		mins := int(d.Minutes()) % 60
		if mins == 0 {
			return fmt.Sprintf("%dh", hours)
		}
		return fmt.Sprintf("%dh %dm", hours, mins)
	}

	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	if hours == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd %dh", days, hours)
}

// FormatResult returns a human-readable representation of the selection result.
func FormatResult(r *Result) string {
	if r == nil {
		return "No selection result"
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recommended: %s\n", r.Selected))

	// Find the selected profile's reasons
	for _, ps := range r.Alternatives {
		if ps.Name == r.Selected {
			for _, reason := range ps.Reasons {
				prefix := "  + "
				if !reason.Positive {
					prefix = "  - "
				}
				sb.WriteString(prefix + reason.Text + "\n")
			}
			break
		}
	}

	// Show alternatives (excluding selected and cooldown profiles)
	var alts []ProfileScore
	for _, ps := range r.Alternatives {
		if ps.Name != r.Selected && ps.Score > -9000 {
			alts = append(alts, ps)
		}
	}

	if len(alts) > 0 {
		sb.WriteString("\nAlternatives:\n")
		for _, ps := range alts {
			sb.WriteString(fmt.Sprintf("  %s", ps.Name))
			// Show first negative reason if any, else first reason
			var reason string
			for _, r := range ps.Reasons {
				if !r.Positive {
					reason = r.Text
					break
				}
			}
			if reason == "" && len(ps.Reasons) > 0 {
				reason = ps.Reasons[0].Text
			}
			if reason != "" {
				sb.WriteString(fmt.Sprintf(" - %s", reason))
			}
			sb.WriteString("\n")
		}
	}

	// Show cooldown profiles
	var cooldowns []ProfileScore
	for _, ps := range r.Alternatives {
		if ps.Score <= -9000 {
			cooldowns = append(cooldowns, ps)
		}
	}

	if len(cooldowns) > 0 {
		sb.WriteString("\nIn cooldown:\n")
		for _, ps := range cooldowns {
			sb.WriteString(fmt.Sprintf("  %s", ps.Name))
			if len(ps.Reasons) > 0 {
				sb.WriteString(fmt.Sprintf(" - %s", ps.Reasons[0].Text))
			}
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
