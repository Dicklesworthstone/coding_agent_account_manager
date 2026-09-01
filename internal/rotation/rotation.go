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
	"sync"
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

// Policy identifies a rotation policy that shapes how candidates are ranked.
//
// The policy is orthogonal to the algorithm: the default availability policy
// keeps the existing behavior of every algorithm unchanged, while the opt-in
// drain policy replaces scoring with drain-before-reset ranking (issue #81).
type Policy string

const (
	// PolicyAvailability is the default policy: maximize immediate headroom
	// (the pre-existing behavior; scoring is unchanged).
	PolicyAvailability Policy = "availability"

	// PolicyDrain is an opt-in policy that prefers the profile whose included
	// quota resets soonest, among profiles still under the drain headroom
	// ceiling. This drains expiring quota before it is lost, instead of
	// leaving it unused while a fresher account is consumed.
	PolicyDrain Policy = "drain"
)

// DefaultDrainCeiling is the default headroom ceiling (percent used) for the
// drain policy. Profiles at or above this usage are held in reserve rather
// than drained further, so a task has room to finish.
const DefaultDrainCeiling = 95

// drainBaseScore is the score baseline for drain-eligible profiles. It is
// large enough that any drain candidate outranks reserve profiles (whose
// scores are availability-based, 0-100) for any realistic reset horizon.
const drainBaseScore = 10000.0

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
	Explanation  string         // One-line explanation of the choice (drain policy only)
}

// UsageInfo represents real-time rate limit usage for a profile.
type UsageInfo struct {
	ProfileName      string
	PrimaryPercent   int        // Primary window usage (0-100)
	SecondaryPercent int        // Secondary window usage (0-100)
	AvailScore       int        // Availability score (0-100, higher is better)
	ResetsAt         *time.Time // Earliest known quota reset time (nil if unknown)
	Error            string     // Error message if fetch failed
}

// Selector performs profile selection based on configured algorithm.
type Selector struct {
	mu           sync.RWMutex
	algorithm    Algorithm
	healthStore  *health.Storage
	db           *caamdb.DB
	rng          *rand.Rand
	avoidRecent  time.Duration         // Don't select profiles used within this duration
	usageData    map[string]*UsageInfo // Real-time usage data by profile name
	policy       Policy                // Selection policy (default: availability)
	drainCeiling int                   // Headroom ceiling (percent used) for the drain policy
}

// NewSelector creates a new profile selector.
func NewSelector(algorithm Algorithm, healthStore *health.Storage, db *caamdb.DB) *Selector {
	return &Selector{
		algorithm:    algorithm,
		healthStore:  healthStore,
		db:           db,
		rng:          rand.New(rand.NewSource(time.Now().UnixNano())),
		avoidRecent:  30 * time.Minute, // Default: avoid profiles used in last 30 min
		policy:       PolicyAvailability,
		drainCeiling: DefaultDrainCeiling,
	}
}

// SetPolicy sets the selection policy. An empty or unknown policy leaves the
// default (availability) in place.
func (s *Selector) SetPolicy(p Policy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch p {
	case PolicyAvailability, PolicyDrain:
		s.policy = p
	}
}

// SetDrainCeiling sets the headroom ceiling (percent used, 1-100) for the
// drain policy. Values outside that range are ignored.
func (s *Selector) SetDrainCeiling(percent int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if percent >= 1 && percent <= 100 {
		s.drainCeiling = percent
	}
}

// SetRNG sets a custom random number generator (useful for testing).
func (s *Selector) SetRNG(rng *rand.Rand) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rng = rng
}

// SetAvoidRecent sets how long to avoid recently-used profiles.
func (s *Selector) SetAvoidRecent(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.avoidRecent = d
}

// SetUsageData sets real-time usage data for consideration in smart selection.
func (s *Selector) SetUsageData(usage map[string]*UsageInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.usageData = usage
}

// Select chooses a profile from the given list using the configured algorithm.
// Returns an error if no profiles are available or all are in cooldown.
func (s *Selector) Select(tool string, profiles []string, currentProfile string) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

	// Opt-in drain policy overrides algorithm scoring (issue #81):
	// drain quota that resets soonest before it expires unused.
	if s.policy == PolicyDrain {
		return s.selectDrain(tool, available)
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

// selectDrain implements the opt-in drain-before-reset policy (issue #81).
//
// Among profiles that are not in cooldown, have usable quota below the
// configured headroom ceiling, and have a known future reset time, it prefers
// the profile whose quota resets SOONEST — draining included quota that would
// otherwise expire unused. Profiles at/above the ceiling, with unknown reset
// times, or with failed usage fetches are held in reserve, ranked by
// availability so the fallback order stays sensible.
func (s *Selector) selectDrain(tool string, profiles []string) (*Result, error) {
	now := time.Now()
	var scores []ProfileScore
	drainEligible := make(map[string]bool)
	usedPercents := make(map[string]int)
	resetIn := make(map[string]time.Duration)

	for _, p := range profiles {
		score := ProfileScore{Name: p}

		// Cooldown is disqualifying, same as the other selectors.
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

		var usage *UsageInfo
		if s.usageData != nil {
			usage = s.usageData[p]
		}

		switch {
		case usage != nil && usage.Error == "":
			// The most constrained window governs headroom.
			used := usage.PrimaryPercent
			if usage.SecondaryPercent > used {
				used = usage.SecondaryPercent
			}
			usedPercents[p] = used

			switch {
			case used >= s.drainCeiling:
				// Too close to the limit to drain safely; keep as reserve.
				score.Score = float64(usage.AvailScore)
				score.Reasons = append(score.Reasons, Reason{
					Text:     fmt.Sprintf("Above drain ceiling (%d%% used >= %d%%); held in reserve", used, s.drainCeiling),
					Positive: false,
				})
			case usage.ResetsAt != nil && usage.ResetsAt.After(now):
				// Drain candidate: sooner reset = higher score.
				until := usage.ResetsAt.Sub(now)
				drainEligible[p] = true
				resetIn[p] = until
				score.Score = drainBaseScore - until.Hours()
				score.Reasons = append(score.Reasons, Reason{
					Text:     fmt.Sprintf("Drain candidate: resets in %s, %d%% used", formatDuration(until), used),
					Positive: true,
				})
			default:
				// Usable quota but no (future) reset time; rank by availability.
				score.Score = float64(usage.AvailScore)
				score.Reasons = append(score.Reasons, Reason{
					Text:     "No quota reset time available; held in reserve",
					Positive: false,
				})
			}
		case usage != nil && usage.Error != "":
			score.Score = -10
			score.Reasons = append(score.Reasons, Reason{
				Text:     "Usage data unavailable; held in reserve",
				Positive: false,
			})
		default:
			score.Score = 0
			score.Reasons = append(score.Reasons, Reason{
				Text:     "No usage data; held in reserve",
				Positive: false,
			})
		}

		scores = append(scores, score)
	}

	// Sort by score descending; break ties by name for determinism.
	sort.Slice(scores, func(i, j int) bool {
		if scores[i].Score != scores[j].Score {
			return scores[i].Score > scores[j].Score
		}
		return scores[i].Name < scores[j].Name
	})

	if len(scores) == 0 {
		return nil, fmt.Errorf("no profiles available for %s", tool)
	}
	if scores[0].Score < -9000 {
		return nil, fmt.Errorf("all profiles for %s are in cooldown", tool)
	}

	selected := scores[0]

	// Explanation string requested by issue #81, e.g.:
	// "chose work: resets in 42m, 91% used; fallback personal held in reserve"
	var explanation string
	if drainEligible[selected.Name] {
		explanation = fmt.Sprintf("chose %s: resets in %s, %d%% used",
			selected.Name, formatDuration(resetIn[selected.Name]), usedPercents[selected.Name])
	} else {
		explanation = fmt.Sprintf("chose %s: no drain-eligible profile (need usable quota under %d%% with a known reset); ranked by availability",
			selected.Name, s.drainCeiling)
	}
	for _, ps := range scores[1:] {
		if ps.Score > -9000 {
			explanation += fmt.Sprintf("; fallback %s held in reserve", ps.Name)
			break
		}
	}

	return &Result{
		Selected:     selected.Name,
		Algorithm:    s.algorithm,
		Alternatives: scores,
		Explanation:  explanation,
	}, nil
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
				Score:   -10000,
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
				Score:   -10000,
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
				if bonus := planBonus(h.PlanType); bonus > 0 {
					score.Score += bonus
					score.Reasons = append(score.Reasons, Reason{
						Text:     fmt.Sprintf("%s plan", health.FormatPlanType(h.PlanType)),
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
			if usage, ok := s.usageData[p]; ok && usage != nil && usage.Error == "" {
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

// planBonus converts a subscription plan into rotation score points. The
// ranking itself lives in health.PlanTierOf so that this scorer and the health
// scorer cannot drift apart; only the point values are local.
func planBonus(planType string) float64 {
	switch health.PlanTierOf(planType) {
	case health.PlanTierEnterprise:
		return 30
	case health.PlanTierHighVolume:
		return 25
	case health.PlanTierStandard:
		return 20
	default:
		return 0
	}
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
	if r.Explanation != "" {
		sb.WriteString(fmt.Sprintf("  %s\n", r.Explanation))
	}

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
