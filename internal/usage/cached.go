package usage

// Cached usage: reading the utilization snapshot Claude Code itself writes.
//
// Claude Code stores the usage figures it last received from the API in the
// top-level "cachedUsageUtilization" key of ~/.claude.json, refreshed by that
// account's own sessions. Reading it costs nothing and, unlike the live
// fetchers in this package, never presents a bearer token: no account is
// touched by another account's process, which is exactly the shared-credential
// pattern that gets subscriptions revoked.
//
// This file deliberately imports nothing that talks to the network.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// Window kinds emitted by Claude Code in the cached utilization payload.
const (
	// CachedKindSession is the rolling 5-hour window.
	CachedKindSession = "session"
	// CachedKindWeeklyAll is the weekly cap covering every model.
	CachedKindWeeklyAll = "weekly_all"
	// CachedKindWeeklyScoped is the weekly cap for one model (see the window's
	// Label for which one).
	CachedKindWeeklyScoped = "weekly_scoped"
)

// ErrNoCachedUsage reports that a .claude.json holds no usage snapshot: the
// file is absent, predates the cache, or carries an empty limits array. It is
// an expected state for a profile that has not run a session yet, not a fault.
var ErrNoCachedUsage = errors.New("no cached usage utilization")

// CachedWindow is one rate-limit window from the cached snapshot.
type CachedWindow struct {
	// Kind is the raw kind from Claude Code ("session", "weekly_all", ...).
	Kind string `json:"kind"`

	// Label is the human-facing name: "5h", "weekly", or, for a model-scoped
	// weekly window, the model's display name (e.g. "Fable").
	Label string `json:"label"`

	// Percent is the share of the window consumed, 0-100. A rolled window
	// reads 0 regardless of what the snapshot recorded.
	Percent int `json:"percent"`

	// ResetsAt is when the window rolls over; nil when the snapshot omits it.
	ResetsAt *time.Time `json:"resets_at"`

	// Rolled is true when ResetsAt has already passed, meaning the recorded
	// percentage describes a window that has since emptied.
	Rolled bool `json:"rolled"`
}

// CachedUsage is the usage snapshot Claude Code cached for one account.
type CachedUsage struct {
	// AccountUUID identifies the account the snapshot belongs to.
	AccountUUID string `json:"account_uuid"`

	// FetchedAt is when Claude Code last refreshed the snapshot; zero when the
	// snapshot omits the timestamp.
	FetchedAt time.Time `json:"fetched_at"`

	// Windows are the rate-limit windows, in the order Claude Code recorded.
	Windows []CachedWindow `json:"windows"`
}

// cachedUsageFile is the slice of .claude.json this package cares about.
type cachedUsageFile struct {
	Cached *cachedUsageBlock `json:"cachedUsageUtilization"`
}

type cachedUsageBlock struct {
	FetchedAtMs int64  `json:"fetchedAtMs"`
	AccountUUID string `json:"accountUuid"`
	Utilization struct {
		Limits []cachedLimit `json:"limits"`
	} `json:"utilization"`
}

type cachedLimit struct {
	Kind     string  `json:"kind"`
	Percent  *int    `json:"percent"`
	ResetsAt *string `json:"resets_at"`
	Scope    *struct {
		Model *struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

// modelName is the display name of the model this limit is scoped to, if any.
func (l cachedLimit) modelName() string {
	if l.Scope == nil || l.Scope.Model == nil {
		return ""
	}
	return l.Scope.Model.DisplayName
}

// ReadCachedUsage reads the usage snapshot from a .claude.json at path. A
// missing or unreadable file yields ErrNoCachedUsage, since a profile with no
// file simply has nothing cached yet.
func ReadCachedUsage(path string, now time.Time) (*CachedUsage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrNoCachedUsage, path)
	}
	return ParseCachedUsage(data, now)
}

// ParseCachedUsage extracts the usage snapshot from the bytes of a
// .claude.json. Windows whose reset time has already passed are reported as
// rolled and read 0%: nothing has run against them since they emptied.
// Malformed JSON is an error; a well-formed file without a snapshot yields
// ErrNoCachedUsage.
func ParseCachedUsage(data []byte, now time.Time) (*CachedUsage, error) {
	var file cachedUsageFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse .claude.json: %w", err)
	}
	if file.Cached == nil || len(file.Cached.Utilization.Limits) == 0 {
		return nil, ErrNoCachedUsage
	}

	out := &CachedUsage{AccountUUID: file.Cached.AccountUUID}
	if file.Cached.FetchedAtMs > 0 {
		out.FetchedAt = time.UnixMilli(file.Cached.FetchedAtMs)
	}

	for _, limit := range file.Cached.Utilization.Limits {
		w := CachedWindow{Kind: limit.Kind, Label: cachedWindowLabel(limit.Kind, limit.modelName())}
		if limit.Percent != nil {
			w.Percent = clampPercent(*limit.Percent)
		}
		if limit.ResetsAt != nil {
			if ts, err := time.Parse(time.RFC3339, *limit.ResetsAt); err == nil {
				w.ResetsAt = &ts
				if !ts.After(now) {
					w.Rolled = true
					w.Percent = 0
				}
			}
		}
		out.Windows = append(out.Windows, w)
	}

	return out, nil
}

// Window returns the first window of the given kind, or nil when the snapshot
// has none.
func (c *CachedUsage) Window(kind string) *CachedWindow {
	if c == nil {
		return nil
	}
	for i := range c.Windows {
		if c.Windows[i].Kind == kind {
			return &c.Windows[i]
		}
	}
	return nil
}

// ScopedLabel returns the model display name of the model-scoped weekly
// window, or "" when the snapshot has no such window. Callers use it to title
// the per-model column of a table spanning several accounts.
func (c *CachedUsage) ScopedLabel() string {
	w := c.Window(CachedKindWeeklyScoped)
	if w == nil || w.Label == CachedKindWeeklyScoped {
		return ""
	}
	return w.Label
}

// Age reports how long ago the snapshot was refreshed. It is 0 when the
// snapshot carries no fetch timestamp.
func (c *CachedUsage) Age(now time.Time) time.Duration {
	if c == nil || c.FetchedAt.IsZero() {
		return 0
	}
	age := now.Sub(c.FetchedAt)
	if age < 0 {
		return 0
	}
	return age
}

// cachedWindowLabel maps a window kind (plus the model it is scoped to, if
// any) to the name shown to the user.
func cachedWindowLabel(kind, model string) string {
	switch kind {
	case CachedKindSession:
		return "5h"
	case CachedKindWeeklyAll:
		return "weekly"
	case CachedKindWeeklyScoped:
		if model != "" {
			return model
		}
		return kind
	default:
		return kind
	}
}

func clampPercent(p int) int {
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return p
	}
}
