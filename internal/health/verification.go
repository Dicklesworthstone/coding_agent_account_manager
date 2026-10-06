package health

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"
)

// Provider verification (issue #108).
//
// Everything else in ProfileHealth is derived from the credential file on
// disk: its expiry and whether it carries a refresh token. That cannot tell a
// working credential from one the provider has revoked; the two are
// byte-for-byte alike locally. Only the provider can tell them apart, so the
// commands that actually reach the provider (a caam refresh, the doctor probe,
// a `caam limits` read) record what the provider said here, and the verdict,
// the three signals and `ls`/`status` honour it.
//
// A rejection is tied to the credential it was observed on through a
// fingerprint of that credential. Logging in again mints a new credential, so
// the old rejection stops applying without anyone having to clear it.

// Verification kinds reported by ls/status JSON.
const (
	// VerificationProvider means the provider itself answered for this
	// credential (accepted or rejected it).
	VerificationProvider = "provider"
	// VerificationPassive means the verdict comes from the credential file
	// alone; the provider has not been asked.
	VerificationPassive = "passive"
)

// ProviderVerification is the result of one provider round trip for a
// profile's credential.
type ProviderVerification struct {
	// Accepted is true when the provider accepted the credential.
	Accepted bool
	// Reason is a short, non-secret code for a rejection (for example
	// "refresh_token_invalidated" or "access_token_rejected"). Ignored when
	// Accepted.
	Reason string
	// Fingerprint identifies the credential the provider answered for (see
	// CodexCredentialFingerprint). Empty when unknown.
	Fingerprint string
	// At is when the provider answered. Zero means now.
	At time.Time
	// AccessTokenOnly marks an acceptance that proves only that the access
	// token works (the doctor /v1/me probe, a usage read), not the refresh
	// token. It does not clear a refresh-token rejection of the same
	// credential: after a revocation the access token keeps working until
	// it expires, and then the account needs a new login.
	AccessTokenOnly bool
}

// ProviderRejected reports whether the provider's most recent answer for the
// credential this snapshot describes was a rejection.
//
// A rejection stops applying once a later provider check succeeds (for a
// refused refresh token, only a successful refresh counts; see
// ProviderVerification.AccessTokenOnly), or once
// the credential on disk is a different one than the rejected credential (the
// operator logged in again). When the current credential cannot be
// fingerprinted the rejection keeps applying: an unreadable credential is no
// evidence that the account recovered.
func (h *ProfileHealth) ProviderRejected() bool {
	if h == nil || h.ProviderRejectedAt.IsZero() {
		return false
	}
	if !h.LastVerifiedAt.IsZero() && !h.LastVerifiedAt.Before(h.ProviderRejectedAt) {
		return false
	}
	if h.RejectedFingerprint != "" && h.CredentialFingerprint != "" &&
		h.RejectedFingerprint != h.CredentialFingerprint {
		return false
	}
	return true
}

// ProviderVerifiedAt returns when the provider last accepted the credential
// this snapshot describes, or the zero time when it has not (or when that
// acceptance was for a different credential than the one now on disk).
func (h *ProfileHealth) ProviderVerifiedAt() time.Time {
	if h == nil || h.LastVerifiedAt.IsZero() {
		return time.Time{}
	}
	if h.VerifiedFingerprint != "" && h.CredentialFingerprint != "" &&
		h.VerifiedFingerprint != h.CredentialFingerprint {
		return time.Time{}
	}
	return h.LastVerifiedAt
}

// VerificationKind reports whether the current verdict rests on a provider
// answer about this credential or only on the credential file.
func (h *ProfileHealth) VerificationKind() string {
	if h.ProviderRejected() || !h.ProviderVerifiedAt().IsZero() {
		return VerificationProvider
	}
	return VerificationPassive
}

// RecordProviderVerification stores the result of a provider round trip for
// a profile. An acceptance clears an earlier rejection (except as noted in
// the function body); a rejection keeps
// the last acceptance time so both remain visible.
func (s *Storage) RecordProviderVerification(provider, name string, v ProviderVerification) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := s.acquireFileLock()
	if err != nil {
		return err
	}
	defer s.releaseFileLock(f)

	store, err := s.loadLocked()
	if err != nil {
		return err
	}

	key := profileKey(provider, name)
	h := store.Profiles[key]
	if h == nil {
		h = &ProfileHealth{}
		store.Profiles[key] = h
	}

	at := v.At
	if at.IsZero() {
		at = time.Now()
	}
	if v.Accepted {
		// Verdicts can land out of order: a `caam limits` read stamps its
		// answer with the fetch time but records it after every profile
		// has been read, while the refresh daemon may record a rejection
		// in between. An acceptance of the same credential that predates
		// the stored rejection must not erase it, and must not move the
		// last acceptance backwards.
		//
		// An access-token-only acceptance of the credential whose refresh
		// token the provider refused says nothing about that refusal: the
		// access token outlives the revocation, then nothing can renew it.
		// It is not recorded at all, since a newer acceptance would stop
		// the rejection from applying.
		sameCredential := v.Fingerprint == "" || h.RejectedFingerprint == "" || v.Fingerprint == h.RejectedFingerprint
		if v.AccessTokenOnly && sameCredential && !h.ProviderRejectedAt.IsZero() && isRefreshTokenRejection(h.ProviderRejection) {
			return nil
		}
		staleForRejection := !h.ProviderRejectedAt.IsZero() && at.Before(h.ProviderRejectedAt) && sameCredential
		if at.After(h.LastVerifiedAt) {
			h.LastVerifiedAt = at
			h.VerifiedFingerprint = v.Fingerprint
		}
		if !staleForRejection {
			h.ProviderRejectedAt = time.Time{}
			h.ProviderRejection = ""
			h.RejectedFingerprint = ""
		}
	} else {
		h.ProviderRejectedAt = at
		h.ProviderRejection = sanitizeRejectionReason(v.Reason)
		h.RejectedFingerprint = v.Fingerprint
	}

	return s.saveLocked(store)
}

// isRefreshTokenRejection reports whether a stored rejection reason came from
// the token endpoint refusing the refresh token, as opposed to a 401 for an
// access token (recorded as "access_token_rejected..." by doctor and limits).
func isRefreshTokenRejection(reason string) bool {
	return !strings.HasPrefix(reason, "access_token_")
}

// accessTokenRejectionSkew is how far in the future an access token's own
// expiry must be before a 401 for it counts as the provider rejecting the
// account. A token at (or, with the local clock behind, past) its expiry is
// routinely refused while its refresh token works fine.
const accessTokenRejectionSkew = 5 * time.Minute

// AccessTokenRejectionIsEvidence reports whether a 401 for an access token
// that expires at expiresAt says the account itself was rejected. It does
// only while the token is comfortably unexpired by its own claim: an expired
// (or unknown-expiry) access token beside a working refresh token is routine,
// and a 401 for it would mark a good account as needing a login.
func AccessTokenRejectionIsEvidence(expiresAt, now time.Time) bool {
	return !expiresAt.IsZero() && expiresAt.After(now.Add(accessTokenRejectionSkew))
}

// sanitizeRejectionReason keeps a rejection reason to a short code made of
// safe characters, so provider text can never carry a credential into
// health.json or command output.
func sanitizeRejectionReason(reason string) string {
	const maxLen = 64
	out := make([]byte, 0, len(reason))
	for i := 0; i < len(reason) && len(out) < maxLen; i++ {
		c := reason[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_':
			out = append(out, c)
		case c >= 'A' && c <= 'Z':
			out = append(out, c+('a'-'A'))
		case c == '-' || c == ' ' || c == '.':
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "rejected"
	}
	return string(out)
}

// CodexCredentialFingerprint returns a short, non-reversible identifier for
// the credential in a Codex auth.json, or "" when none can be read. It keys
// on the refresh token, which is what a revocation kills and what a new login
// replaces; an access-token-only file falls back to its access token.
func CodexCredentialFingerprint(data []byte) string {
	var auth struct {
		RefreshToken string `json:"refresh_token"`
		AccessToken  string `json:"access_token"`
		Tokens       struct {
			RefreshToken string `json:"refresh_token"`
			AccessToken  string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(data, &auth); err != nil {
		return ""
	}
	return credentialFingerprint(auth.Tokens.RefreshToken, auth.RefreshToken, auth.Tokens.AccessToken, auth.AccessToken)
}

// credentialFingerprint prefers a renewal credential over the access token
// it rotates. Configuration churn cannot clear a provider rejection.
func credentialFingerprint(secrets ...string) string {
	for _, secret := range secrets {
		if strings.TrimSpace(secret) != "" {
			sum := sha256.Sum256([]byte("caam-credential-fingerprint:" + secret))
			return hex.EncodeToString(sum[:8])
		}
	}
	return ""
}

// VerificationInfo is the provider-verification block of `ls --json` and
// `status --json` rows (issue #108).
type VerificationInfo struct {
	// Verification is "provider" when the provider has answered for the
	// credential now on disk, "passive" when the verdict rests on the
	// credential file alone.
	Verification string `json:"verification"`
	// LastVerifiedAt is when the provider last accepted this credential
	// (RFC 3339), omitted when it never has.
	LastVerifiedAt string `json:"last_verified_at,omitempty"`
	// ProviderRejectedAt and ProviderRejection describe an outstanding
	// provider rejection of this credential, omitted when there is none.
	ProviderRejectedAt string `json:"provider_rejected_at,omitempty"`
	ProviderRejection  string `json:"provider_rejection,omitempty"`
}

// VerificationFor builds the VerificationInfo block for a health snapshot.
func VerificationFor(h *ProfileHealth) VerificationInfo {
	out := VerificationInfo{Verification: VerificationPassive}
	if h == nil {
		return out
	}
	out.Verification = h.VerificationKind()
	if at := h.ProviderVerifiedAt(); !at.IsZero() {
		out.LastVerifiedAt = at.UTC().Format(time.RFC3339)
	}
	if h.ProviderRejected() {
		out.ProviderRejectedAt = h.ProviderRejectedAt.UTC().Format(time.RFC3339)
		out.ProviderRejection = h.ProviderRejection
		if out.ProviderRejection == "" {
			out.ProviderRejection = "rejected"
		}
	}
	return out
}
