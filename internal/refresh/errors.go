package refresh

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ErrUnsupported indicates that a refresh operation is not supported or not
// configured for a given provider/profile.
//
// Callers should generally treat this as a "skipped" outcome rather than a hard
// failure.
var ErrUnsupported = errors.New("refresh unsupported")

// ErrStaleCredential indicates that a vault credential was superseded by a
// newer live credential for the same account. No refresh was attempted.
var ErrStaleCredential = errors.New("refresh skipped: newer live credential")

// ErrCredentialChanged means the request's captured source was replaced or
// edited. Its result must not overwrite that newer generation or its verdict.
var ErrCredentialChanged = errors.New("credential changed during refresh; current login was preserved")

// DeliveryError reports successful vault renewal whose captured live or
// isolated copy could not be updated safely. The committed token must not be
// rolled back, retried, or marked rejected because delivery was incomplete.
type DeliveryError struct{ Destinations []string }

func (e *DeliveryError) Error() string {
	return "credentials refreshed in vault; delivery skipped for " + strings.Join(e.Destinations, ", ") + "; current destination credentials were preserved"
}

func IsDeliveryIncomplete(err error) bool {
	var delivery *DeliveryError
	return errors.As(err, &delivery)
}

// IsSkipped identifies unsupported or superseded work. A replaced credential
// can obsolete a request already in flight; that is not evidence that the new
// login failed. Callers report it without applying a failure cooldown.
func IsSkipped(err error) bool {
	return errors.Is(err, ErrUnsupported) || errors.Is(err, ErrStaleCredential) || errors.Is(err, ErrCredentialChanged)
}

// ErrRefreshTokenReused indicates that the refresh token has already been
// consumed by another process. OpenAI implements single-use refresh token
// rotation: once a token is used, any subsequent attempt returns this error.
//
// Common causes: parallel refresh race (CAAM vs Codex CLI), stale vault copy
// restored after a newer refresh already consumed the token, or the Codex CLI
// refreshing tokens in the background while CAAM holds an older copy.
//
// The only recovery is to re-authenticate: 'caam login codex <profile>'.
var ErrRefreshTokenReused = errors.New("refresh token reused")

// UnsupportedError is returned when refresh cannot be performed for a provider
// due to missing required configuration or unsupported auth file formats.
type UnsupportedError struct {
	Provider string
	Reason   string
}

func (e *UnsupportedError) Error() string {
	if e == nil {
		return "refresh unsupported"
	}

	switch {
	case e.Provider == "" && e.Reason == "":
		return "refresh unsupported"
	case e.Provider == "":
		return fmt.Sprintf("refresh unsupported: %s", e.Reason)
	case e.Reason == "":
		return fmt.Sprintf("%s refresh unsupported", e.Provider)
	default:
		return fmt.Sprintf("%s refresh unsupported: %s", e.Provider, e.Reason)
	}
}

func (e *UnsupportedError) Unwrap() error {
	return ErrUnsupported
}

// StaleCredentialError prevents replaying a vault refresh token after the
// native CLI has already rotated the same account's live credential.
type StaleCredentialError struct {
	Provider string
	Profile  string
}

func (e *StaleCredentialError) Error() string {
	return fmt.Sprintf(
		"%s/%s: refresh skipped because the same account has newer live credentials; "+
			"back up the current login with: caam backup %s %s",
		e.Provider, e.Profile, e.Provider, e.Profile)
}

func (e *StaleCredentialError) Unwrap() error {
	return ErrStaleCredential
}

// RefreshTokenReusedError is returned when the provider rejects a refresh token
// because it has already been consumed (single-use token rotation).
type RefreshTokenReusedError struct {
	Provider string
	Profile  string
}

func (e *RefreshTokenReusedError) Error() string {
	loginCmd := fmt.Sprintf("caam login %s %s", e.Provider, e.Profile)
	return fmt.Sprintf(
		"%s/%s: refresh token has already been used (refresh_token_reused). "+
			"Another process (Codex CLI or a parallel CAAM refresh) consumed the token. "+
			"Re-authenticate with: %s",
		e.Provider, e.Profile, loginCmd)
}

func (e *RefreshTokenReusedError) Unwrap() error {
	return ErrRefreshTokenReused
}

// IsRefreshTokenReused checks if an error body from an OAuth token endpoint
// contains the refresh_token_reused error code. This is specific to OpenAI's
// single-use refresh token rotation.
func IsRefreshTokenReused(body string) bool {
	return strings.Contains(body, "refresh_token_reused")
}

// RefreshRejectedError reports that a provider's token endpoint definitively
// refused a refresh token: it was revoked, expired, or is otherwise invalid.
// Unlike a transport failure or a 5xx, retrying cannot help; the account needs
// a new login (issue #108). It never carries the response body, which may
// echo credential material.
type RefreshRejectedError struct {
	Provider   string
	StatusCode int
	// Code is the provider's error code when it sent one (for example
	// "refresh_token_invalidated" or "invalid_grant"), else "".
	Code string
}

func (e *RefreshRejectedError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("%s refresh token rejected (HTTP %d, %s); log in again", e.Provider, e.StatusCode, e.Code)
	}
	return fmt.Sprintf("%s refresh token rejected (HTTP %d); log in again", e.Provider, e.StatusCode)
}

// Reason returns a short code describing the rejection.
func (e *RefreshRejectedError) Reason() string {
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("refresh_rejected_http_%d", e.StatusCode)
}

// classifyRefreshRejection decides whether a non-200 token-endpoint response
// is a definitive refusal of the refresh token, and extracts the provider's
// error code. A 401 is a refusal whatever the body says (the Codex CLI treats
// a 401 from this endpoint as permanent too). A 403 counts only when it
// carries an OAuth error code, since an edge proxy can answer 403 with a
// challenge page. A 400 counts only when the provider names a grant error;
// other 400s are request bugs.
func classifyRefreshRejection(status int, body []byte) (string, bool) {
	code := oauthErrorCode(body)
	switch status {
	case 401:
		return code, true
	case 403:
		return code, code != ""
	case 400:
		if code == "invalid_grant" || strings.HasPrefix(code, "refresh_token_") {
			return code, true
		}
	}
	return "", false
}

// oauthErrorCode extracts an error code from either OAuth error layout:
// {"error":"invalid_grant"} or {"error":{"code":"refresh_token_expired"}}.
// Only a short identifier-like code is returned, never free text.
func oauthErrorCode(body []byte) string {
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.Error) == 0 {
		return ""
	}
	var code string
	var flat string
	if err := json.Unmarshal(envelope.Error, &flat); err == nil {
		code = flat
	} else {
		var nested struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(envelope.Error, &nested); err == nil {
			code = nested.Code
		}
	}
	if code == "" || len(code) > 64 {
		return ""
	}
	for _, c := range code {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.') {
			return ""
		}
	}
	return code
}
