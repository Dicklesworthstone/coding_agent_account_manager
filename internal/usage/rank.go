package usage

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Rank modes understood by RankProfiles.
const (
	// RankAvailability is the historical ordering: highest availability score
	// (that is, lowest utilization) first, ties broken by profile name. It is
	// exactly what --best has always used, and is offered here only so a
	// caller can name it explicitly.
	RankAvailability = "availability"

	// RankEarliestResetHeadroom orders profiles by the host quota law:
	//
	//   1. included allowance with headroom, EARLIEST governing reset first
	//   2. paid credits (included allowance already spent) last among usable
	//   3. spent-with-no-credits and unreadable profiles are not eligible
	//
	// The point is to spend an included allowance that is about to refresh
	// before it expires unused, and so to PRESERVE a later-resetting seat
	// rather than burning it first. That is the opposite of --best, which
	// picks the idlest seat and therefore reaches for the reserve.
	RankEarliestResetHeadroom = "earliest-reset-headroom"
)

// rankAliases maps the spellings a caller may reasonably type onto the
// canonical mode names.
var rankAliases = map[string]string{
	"earliest-reset-headroom":      RankEarliestResetHeadroom,
	"earliest-reset-with-headroom": RankEarliestResetHeadroom,
	"earliest_reset_headroom":      RankEarliestResetHeadroom,
	"availability":                 RankAvailability,
}

// RankModes lists the canonical rank mode names, for help and error text.
var RankModes = []string{RankEarliestResetHeadroom, RankAvailability}

// NormalizeRankMode resolves a user-supplied rank mode to its canonical name.
func NormalizeRankMode(s string) (string, bool) {
	mode, ok := rankAliases[strings.ToLower(strings.TrimSpace(s))]
	return mode, ok
}

// DefaultHeadroomCeiling is the percent-used ceiling below which an included
// allowance still counts as having headroom.
//
// 95 rather than 100: a seat that is 99% spent has enough left to accept a
// session and not enough to finish one, so handing it out as "the" seat for
// new work just moves the failure into the middle of a task. It deliberately
// matches rotation.DefaultDrainCeiling — same concept, same number — and
// cmd/caam pins the two together with a test.
const DefaultHeadroomCeiling = 95

// Tiers a ranked profile can fall into, worst last.
const (
	// TierIncludedHeadroom is an included allowance still under the headroom
	// ceiling, with a known future reset. These are the seats the law ranks.
	TierIncludedHeadroom = "included_headroom"

	// TierPaidCredits is a seat whose included allowance is spent but which
	// has paid credits to fall back on. Usable, but always last.
	TierPaidCredits = "paid_credits"

	// TierExhausted is a seat with no included headroom and no credits.
	TierExhausted = "exhausted"

	// TierUnknown is a seat whose limits could not be read, or that is
	// missing a window the law needs (a reset time, or the model-scoped row
	// a named model requires). Never eligible: an unreadable window must not
	// read as spare capacity.
	TierUnknown = "unknown"
)

// ScopedQuota is a model-scoped allowance as reported for a ranked profile.
type ScopedQuota struct {
	Label       string     `json:"label,omitempty"`
	UsedPercent int        `json:"used_percent"`
	ResetsAt    *time.Time `json:"resets_at,omitempty"`
}

// RankedProfile is one profile's placement under a rank mode.
type RankedProfile struct {
	Provider string `json:"provider"`
	Profile  string `json:"profile"`

	// Rank is the 1-based position among ELIGIBLE profiles. Ineligible
	// profiles carry 0.
	Rank int `json:"rank"`

	// Eligible reports whether this profile may be handed to new work.
	Eligible bool `json:"eligible"`

	// Tier is one of the Tier* constants.
	Tier string `json:"tier"`

	// Reason states, in one line, why the profile landed where it did.
	Reason string `json:"reason"`

	// UsedPercent is the worst applicable window's utilization, and
	// BindingWindow names which window that was ("primary", "secondary" or
	// "scoped").
	UsedPercent   int    `json:"used_percent"`
	BindingWindow string `json:"binding_window,omitempty"`

	// HeadroomPercent is 100-UsedPercent, floored at zero.
	HeadroomPercent int `json:"headroom_percent"`

	// GoverningWindow names the window whose reset time this rank mode sorts
	// on: the longest allowance the profile reports, because that is the one
	// that takes longest to come back and is therefore the one worth spending
	// before it refreshes. ResetsAt and ResetsInSeconds describe it.
	GoverningWindow string     `json:"governing_window,omitempty"`
	ResetsAt        *time.Time `json:"resets_at,omitempty"`
	ResetsInSeconds *int64     `json:"resets_in_seconds,omitempty"`

	// AvailabilityScore is the 0-100 score --best ranks on, carried through
	// so a caller can see both orderings at once.
	AvailabilityScore int `json:"availability_score"`

	// ScopedLimit is the model-scoped allowance closest to its cap among
	// those that constrain the requested model, when the profile reports one.
	ScopedLimit *ScopedQuota `json:"scoped_limit,omitempty"`

	// HasCredits reports paid credit availability (Codex reports this;
	// Claude does not).
	HasCredits bool `json:"has_credits"`

	// PlanType is the provider's subscription tier when it reports one.
	PlanType string `json:"plan_type,omitempty"`

	// Error carries the fetch error for a profile whose limits could not be
	// read at all.
	Error string `json:"error,omitempty"`
}

// RankResult is the whole ranking, as emitted to a caller.
type RankResult struct {
	Rank            string `json:"rank"`
	Provider        string `json:"provider,omitempty"`
	Model           string `json:"model,omitempty"`
	HeadroomCeiling int    `json:"headroom_ceiling_percent"`

	// RequireModelWindow reports whether a profile had to publish a window
	// scoped to Model in order to be eligible.
	RequireModelWindow bool `json:"require_model_window"`

	GeneratedAt time.Time `json:"generated_at"`

	// Selected is the top-ranked eligible profile, or null when there is
	// none. A caller that only wants an answer reads this field.
	Selected *RankedProfile `json:"selected"`

	// Profiles is every profile considered, eligible ones first in rank
	// order, then the ineligible ones with their reasons.
	Profiles []RankedProfile `json:"profiles"`

	// Error is set exactly when Selected is nil.
	Error string `json:"error,omitempty"`
}

// RankOptions parameterizes RankProfiles.
type RankOptions struct {
	// Mode is a canonical rank mode name (see NormalizeRankMode).
	Mode string

	// Provider is echoed into the result; it is not used for ranking.
	Provider string

	// Model is the model the work will run on, or "" when unknown. It
	// narrows which model-scoped allowance constrains a profile.
	Model string

	// HeadroomCeiling is the percent-used ceiling (1-100). Zero means
	// DefaultHeadroomCeiling.
	HeadroomCeiling int

	// RequireModelWindow makes a profile ineligible unless it publishes a
	// window scoped to Model. It exists because an omitted scoped row is not
	// evidence of capacity: an account whose weekly Fable allowance the API
	// simply did not report must not be handed Fable work.
	RequireModelWindow bool

	// Now is the clock; zero means time.Now().
	Now time.Time
}

// RankProfiles orders rows under opts and never returns nil.
//
// It is a pure read: it inspects usage rows and produces an ordering. It
// activates nothing, writes no credential, and touches no running session.
func RankProfiles(rows []ProfileUsage, opts RankOptions) *RankResult {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	ceiling := opts.HeadroomCeiling
	if ceiling <= 0 {
		ceiling = DefaultHeadroomCeiling
	}
	// Normalize rather than echo: the emitted `rank` field must name the
	// ordering that was actually applied, never a mode string that wasn't.
	mode, ok := NormalizeRankMode(opts.Mode)
	if !ok {
		mode = RankEarliestResetHeadroom
	}

	res := &RankResult{
		Rank:               mode,
		Provider:           opts.Provider,
		Model:              opts.Model,
		HeadroomCeiling:    ceiling,
		RequireModelWindow: opts.RequireModelWindow,
		GeneratedAt:        now,
		Profiles:           make([]RankedProfile, 0, len(rows)),
	}

	for _, row := range rows {
		res.Profiles = append(res.Profiles, classify(row, opts.Model, ceiling, opts.RequireModelWindow, now))
	}

	switch mode {
	case RankAvailability:
		sortByAvailability(res.Profiles)
	default:
		sortByEarliestResetHeadroom(res.Profiles)
	}

	rank := 0
	for i := range res.Profiles {
		if res.Profiles[i].Eligible {
			rank++
			res.Profiles[i].Rank = rank
			if res.Selected == nil {
				sel := res.Profiles[i]
				res.Selected = &sel
			}
		}
	}

	if res.Selected == nil {
		res.Error = noSelectionError(res.Profiles, ceiling)
	}
	return res
}

// noSelectionError explains, in one line an operator can act on, why nothing
// was selectable. Falling through to a static pin is exactly the failure this
// mode exists to prevent, so the message names the actual obstacle.
func noSelectionError(profiles []RankedProfile, ceiling int) string {
	if len(profiles) == 0 {
		return "no profiles to rank: caam read no usage rows for this provider (do not fall back to a static pin)"
	}
	counts := map[string]int{}
	for _, p := range profiles {
		counts[p.Tier]++
	}
	// Only the ineligible tiers can appear here: a paid-credits seat is
	// usable, so its presence would have produced a selection.
	parts := make([]string, 0, 2)
	for _, tier := range []string{TierUnknown, TierExhausted} {
		if counts[tier] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[tier], tier))
		}
	}
	return fmt.Sprintf(
		"no profile has included headroom under %d%%: %s (see each profile's reason; do not fall back to a static pin)",
		ceiling, strings.Join(parts, ", "))
}

// classify places one usage row into a tier and records everything the
// ordering and the operator need.
func classify(row ProfileUsage, model string, ceiling int, requireModelWindow bool, now time.Time) RankedProfile {
	out := RankedProfile{
		Provider: row.Provider,
		Profile:  row.ProfileName,
		Tier:     TierUnknown,
	}

	u := row.Usage
	if u == nil {
		out.Reason = "no usage data was read for this profile"
		return out
	}

	out.PlanType = u.PlanType
	out.AvailabilityScore = u.AvailabilityScoreForModel(model)
	if u.Credits != nil {
		out.HasCredits = u.Credits.Unlimited || u.Credits.HasCredits
	}
	if scoped := u.ScopedLimit(model); scoped != nil {
		out.ScopedLimit = &ScopedQuota{Label: scopedQuotaLabel(scoped), UsedPercent: scoped.UsedPercent}
		if !scoped.ResetsAt.IsZero() {
			t := scoped.ResetsAt
			out.ScopedLimit.ResetsAt = &t
		}
	}

	if u.Error != "" {
		out.Error = u.Error
		out.Reason = "limits could not be read: " + u.Error
		return out
	}

	used, window, ok := bindingUsage(u, model)
	if !ok {
		if u.QuotaStatus == QuotaDegraded || u.QuotaStatus == QuotaUnavailable {
			note := u.QuotaNote
			if note == "" {
				note = "the provider did not include a numeric utilization"
			}
			out.Reason = note + "; refusing to treat that as spare capacity"
			return out
		}
		out.Reason = "the provider reported no rate limit window for this profile"
		return out
	}
	out.UsedPercent = used
	out.BindingWindow = window
	if h := 100 - used; h > 0 {
		out.HeadroomPercent = h
	}

	// Record the reset horizon before any refusal, so a profile that is held
	// back still shows an operator when it would have been usable.
	gov := GoverningWindow(u, model)
	if gov != nil && !gov.ResetsAt.IsZero() {
		t := gov.ResetsAt
		out.ResetsAt = &t
		secs := int64(t.Sub(now) / time.Second)
		out.ResetsInSeconds = &secs
		out.GoverningWindow = governingWindowName(u, gov, model)
	}

	// A named model whose own allowance is absent is not spare capacity. Say
	// so and stand down rather than launching against a quota nobody read.
	if requireModelWindow && model != "" && out.ScopedLimit == nil {
		out.Reason = fmt.Sprintf(
			"no window scoped to %q was reported, so its allowance is unknown; refusing to treat an omitted row as capacity", model)
		return out
	}

	spent := used >= ceiling

	switch {
	case !spent && out.ResetsAt != nil && out.ResetsAt.After(now):
		out.Tier = TierIncludedHeadroom
		out.Eligible = true
		out.Reason = fmt.Sprintf("included allowance %d%% used (under the %d%% ceiling), %s resets in %s",
			used, ceiling, out.GoverningWindow, formatRankDuration(out.ResetsAt.Sub(now)))

	case !spent:
		// Headroom but no future reset: this mode ranks on reset time, so
		// there is nothing to rank it by. Reserve it rather than guess.
		out.Reason = fmt.Sprintf(
			"included allowance %d%% used but no future reset time was reported, so it cannot be ordered by reset", used)

	case out.HasCredits:
		out.Tier = TierPaidCredits
		out.Eligible = true
		out.Reason = fmt.Sprintf("included allowance spent (%d%% used, ceiling %d%%); paid credits remain, so it ranks last",
			used, ceiling)

	default:
		out.Tier = TierExhausted
		out.Reason = fmt.Sprintf("included allowance spent (%d%% used, ceiling %d%%) and no paid credits remain",
			used, ceiling)
	}

	return out
}

// bindingUsage returns the worst applicable window's utilization and which
// window that was. It mirrors the drain policy's notion of headroom: a spent
// model-scoped allowance constrains the account even while its general
// windows read as idle.
func bindingUsage(u *UsageInfo, model string) (int, string, bool) {
	type candidate struct {
		name string
		w    *UsageWindow
	}
	candidates := []candidate{
		{"primary", u.PrimaryWindow},
		{"secondary", u.SecondaryWindow},
		{"scoped", u.ScopedLimit(model)},
	}

	used := 0
	name := ""
	found := false
	for _, c := range candidates {
		if c.w == nil || c.w.Unmeasured {
			continue
		}
		if !found || c.w.UsedPercent > used {
			used = c.w.UsedPercent
			name = c.name
			found = true
		}
	}
	return used, name, found
}

// GoverningWindow returns the window whose reset time orders a profile under
// RankEarliestResetHeadroom: the LONGEST allowance the profile reports that
// still has a known reset time.
//
// The longest window is the included allowance being preserved or spent — a
// weekly cap, not the five-hour window that rolls over on its own several
// times a day. Ranking on the soonest reset instead (what
// UsageInfo.EarliestReset returns, and what the rotation drain policy uses)
// would sort seats by their five-hour window and say nothing about which
// weekly allowance is about to be lost.
//
// When no window reports a duration, the furthest-out reset stands in for the
// longest window, which picks the same one on both providers in practice.
func GoverningWindow(u *UsageInfo, model string) *UsageWindow {
	if u == nil {
		return nil
	}

	windows := []*UsageWindow{u.PrimaryWindow, u.SecondaryWindow, u.TertiaryWindow}
	if model != "" {
		if w := u.ScopedLimit(model); w != nil {
			windows = append(windows, w)
		}
	} else {
		for _, w := range u.ModelWindows {
			windows = append(windows, w)
		}
	}

	var best *UsageWindow
	for _, w := range windows {
		if w == nil || w.ResetsAt.IsZero() {
			continue
		}
		if best == nil {
			best = w
			continue
		}
		switch {
		case w.WindowDuration != best.WindowDuration:
			if w.WindowDuration > best.WindowDuration {
				best = w
			}
		case w.ResetsAt.After(best.ResetsAt):
			best = w
		}
	}
	return best
}

// governingWindowName labels the governing window for output.
func governingWindowName(u *UsageInfo, gov *UsageWindow, model string) string {
	switch {
	case gov == nil:
		return ""
	case gov == u.PrimaryWindow:
		return "primary"
	case gov == u.SecondaryWindow:
		return "secondary"
	case gov == u.TertiaryWindow:
		return "tertiary"
	case gov == u.ScopedLimit(model):
		return "scoped"
	}
	for name, w := range u.ModelWindows {
		if w == gov {
			return "scoped:" + name
		}
	}
	return "unknown"
}

// tierOrder ranks the tiers themselves, best first.
func tierOrder(tier string) int {
	switch tier {
	case TierIncludedHeadroom:
		return 0
	case TierPaidCredits:
		return 1
	case TierExhausted:
		return 2
	default:
		return 3
	}
}

// sortByEarliestResetHeadroom applies the host quota law: tier first, then
// earliest governing reset, then profile name for determinism.
func sortByEarliestResetHeadroom(profiles []RankedProfile) {
	sort.SliceStable(profiles, func(i, j int) bool {
		a, b := profiles[i], profiles[j]
		if ta, tb := tierOrder(a.Tier), tierOrder(b.Tier); ta != tb {
			return ta < tb
		}
		switch {
		case a.ResetsAt != nil && b.ResetsAt != nil:
			if !a.ResetsAt.Equal(*b.ResetsAt) {
				return a.ResetsAt.Before(*b.ResetsAt)
			}
		case a.ResetsAt != nil:
			return true
		case b.ResetsAt != nil:
			return false
		}
		return a.Profile < b.Profile
	})
}

// sortByAvailability reproduces the historical --best ordering.
func sortByAvailability(profiles []RankedProfile) {
	sort.SliceStable(profiles, func(i, j int) bool {
		a, b := profiles[i], profiles[j]
		if a.Eligible != b.Eligible {
			return a.Eligible
		}
		if a.AvailabilityScore != b.AvailabilityScore {
			return a.AvailabilityScore > b.AvailabilityScore
		}
		return a.Profile < b.Profile
	})
}

// scopedQuotaLabel names the model a scoped window belongs to, for providers
// that report one without a display name.
func scopedQuotaLabel(w *UsageWindow) string {
	if w == nil || w.Label == "" {
		return "model quota"
	}
	return w.Label
}

// formatRankDuration renders a reset horizon compactly.
func formatRankDuration(d time.Duration) string {
	if d < 0 {
		return "now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	hours := int(d.Hours())
	mins := int(d.Minutes()) % 60
	if hours >= 24 {
		return fmt.Sprintf("%dd%dh", hours/24, hours%24)
	}
	return fmt.Sprintf("%dh%dm", hours, mins)
}
