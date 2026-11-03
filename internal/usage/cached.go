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
	"strings"
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
// file is absent or unreadable, predates the cache, or has no window entries.
// It is the expected state for a profile that has not run a session
// since Claude Code started caching, not a fault — and it must be reported as
// "no cached data", never as 0% used.
var ErrNoCachedUsage = errors.New("no cached usage data")

// A projected reset is useful for display but does not measure any usage in
// the new window. A live weekly allowance cannot establish that a previously
// reported session or model allowance has spare capacity after its reset.
var errCachedQuotaRolled = errors.New("cached usage crossed a quota reset; refresh usage before selecting this account")

var errCachedAccountMismatch = errors.New("cached usage belongs to a different Claude account; refresh usage before selecting this account")

// cachedUsageFile is the slice of a .claude.json this package reads.
type cachedUsageFile struct {
	Cached       *cachedUsageBlock `json:"cachedUsageUtilization"`
	OAuthAccount json.RawMessage   `json:"oauthAccount"`
}

type cachedUsageBlock struct {
	FetchedAtMs int64               `json:"fetchedAtMs"`
	AccountUUID string              `json:"accountUuid"`
	Utilization claudeUsageResponse `json:"utilization"`
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
// An incomplete, unsupported, or rolled quota returns diagnostic UsageInfo
// and an error; callers should retain the windows and original cache timestamp
// for display, then refresh usage before selecting the account for new work.
func ParseCachedClaudeUsage(data []byte, now time.Time) (*UsageInfo, error) {
	var file cachedUsageFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse .claude.json: %w", err)
	}
	if file.Cached == nil {
		return nil, ErrNoCachedUsage
	}
	quota := file.Cached.Utilization
	if len(quota.Limits) == 0 && quota.FiveHour == nil && quota.SevenDay == nil &&
		quota.Opus == nil && quota.SevenDayOpus == nil {
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

	// Use the live parser's normalization and general-quota guard. A partial
	// snapshot can retain diagnostic windows without becoming spare capacity.
	quotaErr := quota.apply(info, now)
	// Native account labels are not ownership proof, but an explicit
	// disagreement makes this snapshot unsafe to attribute to the login.
	// Legacy files may omit this object or carry an old string label.
	var account struct {
		AccountUUID string `json:"accountUuid"`
	}
	if quotaErr == nil && json.Unmarshal(file.OAuthAccount, &account) == nil {
		cachedID, currentID := strings.TrimSpace(info.AccountID), strings.TrimSpace(account.AccountUUID)
		if cachedID != "" && currentID != "" && cachedID != currentID {
			quotaErr = errCachedAccountMismatch
			info.QuotaStatus = QuotaDegraded
			info.Error, info.QuotaNote = quotaErr.Error(), quotaErr.Error()
		}
	}

	rolled := false
	for _, w := range []*UsageWindow{info.PrimaryWindow, info.SecondaryWindow, info.TertiaryWindow} {
		if markRolled(w, now) {
			rolled = true
		}
	}
	for _, w := range info.ModelWindows {
		if markRolled(w, now) {
			rolled = true
		}
	}
	for _, w := range info.ScopedWindows {
		if markRolled(w, now) {
			rolled = true
		}
	}
	if rolled && quotaErr == nil {
		// Keep the recorded windows, their projected resets, and the cache's
		// own timestamp for diagnostics. Selecting new work needs a fresh
		// measurement of every restriction, not this synthetic zero.
		quotaErr = errCachedQuotaRolled
		info.QuotaStatus = QuotaDegraded
		info.Error, info.QuotaNote = quotaErr.Error(), quotaErr.Error()
	}

	return info, quotaErr
}

// markRolled zeroes a window whose reset time has already passed, recording
// that it did so. The result also marks the snapshot unsafe for selection,
// including when another same-model window later replaces this candidate.
func markRolled(w *UsageWindow, now time.Time) bool {
	if w == nil || w.ResetsAt.IsZero() || w.ResetsAt.After(now) {
		return false
	}
	w.Rolled = true
	w.Utilization = 0
	w.UsedPercent = 0
	return true
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
