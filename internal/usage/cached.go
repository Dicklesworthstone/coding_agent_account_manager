package usage

// Cached usage: the utilization snapshot Claude Code itself writes to disk.
//
// Every fetcher in this package answers "how much of this account's quota is
// spent" by presenting the account's bearer token to the provider. That needs
// the network, needs a live token, and — when several profiles are checked
// from one process — has one machine speaking for several accounts at once.
//
// Claude Code already records the answer locally: after each usage refresh it
// writes the figures it received into the top-level "cachedUsageUtilization"
// key of that account's own .claude.json. Reading it costs nothing, presents
// no token, and works offline.
//
// The cost is freshness. The snapshot only moves when that account itself runs
// a session, so for the profiles you most want to compare — the ones you are
// NOT currently using — it can be hours or days old, or absent entirely. This
// file therefore reports the snapshot's own timestamp and distinguishes "no
// cache yet" from "0% used"; it never invents a figure. Callers must surface
// both, which is why ErrNoCachedUsage is a named error rather than an empty
// result.
//
// This file deliberately imports nothing that talks to the network.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Values for UsageInfo.Source.
const (
	// SourceAPI marks figures fetched live from the provider's usage API.
	SourceAPI = "api"
	// SourceCache marks figures read from a snapshot Claude Code left on
	// local disk. FetchedAt is then the snapshot's own timestamp, not the
	// time caam read it, so now-FetchedAt is the true staleness.
	SourceCache = "cache"
)

// ErrNoCachedUsage reports that a .claude.json holds no usage snapshot: the
// file is absent or unreadable, predates the cache, or carries an empty limits
// array. It is the expected state for a profile that has not run a session
// since Claude Code started caching, not a fault — and it must be reported as
// "no cached data", never as 0% used.
var ErrNoCachedUsage = errors.New("no cached usage data")

// cachedUsageFile is the slice of a .claude.json this package reads.
type cachedUsageFile struct {
	Cached *cachedUsageBlock `json:"cachedUsageUtilization"`
}

type cachedUsageBlock struct {
	FetchedAtMs int64  `json:"fetchedAtMs"`
	AccountUUID string `json:"accountUuid"`
	Utilization struct {
		Limits []claudeLimit `json:"limits"`
	} `json:"utilization"`
}

// ReadCachedClaudeUsage reads the usage snapshot out of the .claude.json at
// path and renders it as the same UsageInfo the live fetcher produces, so
// every consumer downstream — the limits table, --best, the rotation scorer —
// works on it unchanged.
//
// now is the clock used to decide whether a window has already rolled over; it
// is a parameter so tests are not time-dependent.
func ReadCachedClaudeUsage(path string, now time.Time) (*UsageInfo, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoCachedUsage, path)
	}
	return ParseCachedClaudeUsage(data, now)
}

// ParseCachedClaudeUsage extracts the usage snapshot from the bytes of a
// .claude.json.
//
// A window whose reset time has already passed is reported as Rolled with 0%:
// the recorded percentage describes a window that has since emptied, and
// carrying the stale figure forward would overstate how much is spent. Rolled
// is set so the distinction stays visible rather than being laundered into a
// plain zero.
//
// Malformed JSON is an error. A well-formed file with no snapshot yields
// ErrNoCachedUsage so the caller can say "no cached data" instead of "0%".
func ParseCachedClaudeUsage(data []byte, now time.Time) (*UsageInfo, error) {
	var file cachedUsageFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse .claude.json: %w", err)
	}
	if file.Cached == nil || len(file.Cached.Utilization.Limits) == 0 {
		return nil, ErrNoCachedUsage
	}

	info := &UsageInfo{
		Provider:  "claude",
		Source:    SourceCache,
		AccountID: file.Cached.AccountUUID,
	}
	if file.Cached.FetchedAtMs > 0 {
		info.FetchedAt = time.UnixMilli(file.Cached.FetchedAtMs)
	}

	// The cached payload is the same limits[] array the usage API returns, so
	// it folds in through the same code path — including the per-model
	// ("weekly_scoped") allowances.
	applyLimits(info, file.Cached.Utilization.Limits)

	for _, w := range []*UsageWindow{info.PrimaryWindow, info.SecondaryWindow, info.TertiaryWindow} {
		markRolled(w, now)
	}
	for _, w := range info.ModelWindows {
		markRolled(w, now)
	}

	return info, nil
}

// markRolled zeroes a window whose reset time has already passed, recording
// that it did so.
func markRolled(w *UsageWindow, now time.Time) {
	if w == nil || w.ResetsAt.IsZero() || w.ResetsAt.After(now) {
		return
	}
	w.Rolled = true
	w.Utilization = 0
	w.UsedPercent = 0
}

// CacheAge reports how stale a cached snapshot is. It returns false when the
// figures did not come from a cache, or when the snapshot carried no
// timestamp — in which case the age is genuinely unknown and must not be
// rendered as "0s ago".
func (u *UsageInfo) CacheAge(now time.Time) (time.Duration, bool) {
	if u == nil || u.Source != SourceCache || u.FetchedAt.IsZero() {
		return 0, false
	}
	age := now.Sub(u.FetchedAt)
	if age < 0 {
		age = 0
	}
	return age, true
}
