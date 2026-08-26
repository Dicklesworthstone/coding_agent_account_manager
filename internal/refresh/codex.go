package refresh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Codex Constants
var (
	CodexTokenURL = "https://auth.openai.com/oauth/token"
)

const (
	CodexClientID = "app_EMoamEEZ73f0CkXaXp7hrann"
	CodexScopes   = "openid profile email"
)

// RefreshCodexToken refreshes the OAuth token for OpenAI Codex.
var RefreshCodexToken = func(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	if refreshToken == "" {
		return nil, fmt.Errorf("refresh token is empty")
	}

	if err := validateTokenEndpoint(CodexTokenURL, []string{"auth.openai.com"}); err != nil {
		return nil, err
	}

	body := map[string]string{
		"client_id":     CodexClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         CodexScopes,
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", CodexTokenURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("codex refresh failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Use bounded read to prevent memory exhaustion from large error responses
		body, err := readLimitedBody(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("codex refresh error %d (failed to read body: %v)", resp.StatusCode, err)
		}
		// Detect refresh_token_reused and return a structured error with
		// actionable guidance instead of a raw API dump.
		if resp.StatusCode == http.StatusUnauthorized && IsRefreshTokenReused(string(body)) {
			return nil, ErrRefreshTokenReused
		}
		return nil, fmt.Errorf("codex refresh error %d: %s", resp.StatusCode, string(body))
	}

	var tokenResp TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	return &tokenResp, nil
}

// UpdateCodexAuth updates the auth file with the new token.
func UpdateCodexAuth(path string, resp *TokenResponse) error {
	// Read existing file
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read auth file: %w", err)
	}

	var auth map[string]interface{}
	if err := json.Unmarshal(data, &auth); err != nil {
		return fmt.Errorf("parse auth file: %w", err)
	}

	// Prefer updating nested tokens if present (newer format).
	if rawTokens, ok := auth["tokens"]; ok {
		tokens, ok := rawTokens.(map[string]interface{})
		if !ok {
			return fmt.Errorf("invalid tokens format")
		}

		tokens["access_token"] = resp.AccessToken
		if resp.RefreshToken != "" {
			tokens["refresh_token"] = resp.RefreshToken
		}
		if resp.ExpiresIn > 0 {
			tokens["expires_at"] = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).Unix()
		}
		auth["tokens"] = tokens
		return writeAuthFile(path, auth)
	}

	// Update root fields (legacy format).
	auth["access_token"] = resp.AccessToken
	if resp.RefreshToken != "" {
		auth["refresh_token"] = resp.RefreshToken
	}

	// Calculate expires_at from expires_in
	if resp.ExpiresIn > 0 {
		auth["expires_at"] = time.Now().Add(time.Duration(resp.ExpiresIn) * time.Second).Unix()
	}

	return writeAuthFile(path, auth)
}

// CodexVerifyURL is the authenticated endpoint VerifyCodexToken probes. It is
// a variable (like CodexTokenURL) so tests can point it at a local server.
var CodexVerifyURL = "https://api.openai.com/v1/me"

const (
	// codexVerifyAttempts bounds the probe to one retry: a second attempt is
	// made only after a transient answer (429 or 5xx), never after a 401/403.
	codexVerifyAttempts = 2
	// codexVerifyRetryDelay is the pause before the retry when the provider
	// does not send a usable Retry-After header.
	codexVerifyRetryDelay = 750 * time.Millisecond
	// codexVerifyRetryMax caps a Retry-After value so a doctor run cannot be
	// stalled by an aggressive server hint.
	codexVerifyRetryMax = 3 * time.Second
)

// TokenVerifyError reports a non-2xx answer from the token verification
// endpoint. The status code is preserved so callers can tell a definitive
// rejection (401/403) apart from a transient failure (429/5xx) instead of
// collapsing every non-200 answer into "token rejected".
type TokenVerifyError struct {
	StatusCode int
	// Attempts is how many requests were made before giving up (1 or 2).
	Attempts int
}

func (e *TokenVerifyError) Error() string {
	if e.Attempts > 1 {
		return fmt.Sprintf("token verification failed with status %d after %d attempts", e.StatusCode, e.Attempts)
	}
	return fmt.Sprintf("token verification failed with status %d", e.StatusCode)
}

// Rejected reports whether the provider definitively refused the credential.
func (e *TokenVerifyError) Rejected() bool {
	return e.StatusCode == http.StatusUnauthorized || e.StatusCode == http.StatusForbidden
}

// Transient reports whether the failure says nothing about the credential
// itself: rate limiting or a server-side error.
func (e *TokenVerifyError) Transient() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// VerifyCodexToken probes whether an access token is accepted by the API.
//
// A nil return means the token was accepted (2xx). Any other status is
// returned as a *TokenVerifyError carrying that status; transient statuses
// (429, 5xx) are retried once after a short, bounded delay before being
// reported. Transport failures and context cancellation are returned wrapped
// as "request failed: ..." and carry no status.
func VerifyCodexToken(ctx context.Context, token string) error {
	if err := validateTokenEndpoint(CodexVerifyURL, []string{"api.openai.com"}); err != nil {
		return err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	var last *TokenVerifyError
	for attempt := 1; attempt <= codexVerifyAttempts; attempt++ {
		status, retryAfter, err := codexVerifyOnce(ctx, client, token)
		if err != nil {
			return fmt.Errorf("request failed: %w", err)
		}
		if status >= 200 && status < 300 {
			return nil
		}
		last = &TokenVerifyError{StatusCode: status, Attempts: attempt}
		if !last.Transient() || attempt == codexVerifyAttempts {
			return last
		}
		if err := sleepContext(ctx, codexVerifyBackoff(retryAfter)); err != nil {
			return fmt.Errorf("request failed: %w", err)
		}
	}
	return last
}

// codexVerifyOnce performs a single probe and returns the HTTP status plus the
// raw Retry-After header. The body is discarded: the probe only needs the
// status, and reading a bounded prefix lets the connection be reused.
func codexVerifyOnce(ctx context.Context, client *http.Client, token string) (int, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, CodexVerifyURL, nil)
	if err != nil {
		return 0, "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return resp.StatusCode, resp.Header.Get("Retry-After"), nil
}

// codexVerifyBackoff turns a Retry-After header into the delay before the
// single retry. Only the delay-seconds form is honored (an HTTP-date would
// need clock comparison for a one-shot retry and is not worth it); anything
// unparseable or non-positive falls back to the default, and every value is
// capped so the probe stays bounded.
func codexVerifyBackoff(retryAfter string) time.Duration {
	delay := codexVerifyRetryDelay
	if secs, err := strconv.Atoi(strings.TrimSpace(retryAfter)); err == nil && secs > 0 {
		delay = time.Duration(secs) * time.Second
	}
	if delay > codexVerifyRetryMax {
		delay = codexVerifyRetryMax
	}
	return delay
}

// sleepContext waits for d or until ctx is done, whichever comes first.
func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
