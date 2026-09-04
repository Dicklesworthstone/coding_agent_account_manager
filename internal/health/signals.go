package health

import "time"

// The three-signal credential contract (issue #102).
//
// `caam ls` reports one composite verdict per profile — healthy / warning /
// critical — and three different audiences were reading three different
// questions out of it:
//
//   - the refresh daemon and `warnings` want "should this credential be
//     refreshed soon?";
//   - a rotation controller wants "can a new agent start on this account
//     right now?";
//   - the operator wants "do I have to sit down and log in again?".
//
// For Claude those three collapse into one answer, which is how they came to
// share a single flag. For Codex they do not: caam refreshes Codex itself, so
// a lapsed access token is genuinely refresh-due, while the account behind it
// is perfectly usable and needs no human. Reporting the composite verdict
// alone made three live Codex profiles look expired.
//
// Signals splits them into three additive fields, computed from the same
// ProfileHealth the verdict is computed from, for every provider. The
// composite `status` is unchanged in meaning and stays the human-facing
// summary; controllers should route on LaunchUsable and schedulers on
// RefreshDue.
//
// Each field is a *bool so that "we have no evidence" stays distinguishable
// from "no": an unknown signal is nil (JSON null) and is never promoted to
// either healthy or login-required.

// Signals is the three-signal credential contract for one profile.
type Signals struct {
	// RefreshDue reports whether caam should renew this credential soon:
	// the expiry is known, it is inside the warning window (or already past),
	// and caam is the one that does the renewing. It is false for a
	// self-refreshing credential (Claude), which caam must leave alone, and
	// nil when no expiry could be determined.
	RefreshDue *bool `json:"refresh_due"`

	// LaunchUsable reports whether a new session can start on this account
	// right now. It is false when a re-login is required or an active
	// rate-limit cooldown is in effect, true when neither holds, and nil when
	// there is no evidence either way.
	LaunchUsable *bool `json:"launch_usable"`

	// LoginRequired reports whether a human must re-authenticate. It is true
	// only for a credential whose expiry has passed AND that carries nothing
	// to renew itself with; a lapsed but renewable access token is false. It
	// is nil when no expiry could be determined.
	LoginRequired *bool `json:"login_required"`
}

// CredentialSignals derives the three-signal contract from a health snapshot.
//
// Only the credential is consulted: error counts and penalties feed the
// composite verdict, not these fields, because a controller asking "can this
// account start work" must not be told "no" by an error budget that decays on
// its own.
func CredentialSignals(h *ProfileHealth, config HealthConfig) Signals {
	var out Signals
	if h == nil {
		return out
	}
	now := time.Now()
	rateLimited := h.RateLimited(now)

	if h.TokenExpiresAt.IsZero() {
		// No expiry evidence. A cooldown is still hard evidence that nothing
		// can launch right now; everything else stays unknown.
		if rateLimited {
			out.LaunchUsable = boolPtr(false)
		}
		return out
	}

	expired := h.TokenExpiresAt.Before(now)
	renewable := h.CredentialRenewable()

	// Refresh scheduling. Two things make a refresh impossible rather than
	// merely unnecessary, and neither is a refresh being "due":
	//   - Claude renews itself and caam's Claude refresh is disabled;
	//   - a credential with no refresh token has nothing to renew from, and
	//     needs a login instead (LoginRequired says so below).
	refreshDue := false
	if !h.SelfRefreshing && renewable {
		warning := time.Duration(config.TokenExpiryWarningMinutes) * time.Minute
		refreshDue = expired || h.TokenExpiresAt.Sub(now) <= warning
	}
	out.RefreshDue = boolPtr(refreshDue)

	loginRequired := expired && !renewable
	out.LoginRequired = boolPtr(loginRequired)
	out.LaunchUsable = boolPtr(!loginRequired && !rateLimited)

	return out
}

func boolPtr(b bool) *bool { return &b }
