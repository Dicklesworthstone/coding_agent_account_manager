package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

// Issue #78: doctor used to report every non-200 probe answer as "access token
// rejected by API" with refresh-token-reuse guidance, sending users into an
// unnecessary re-login on a 429 or 5xx. Only 401/403 may fail the check.
func TestClassifyCodexProbeError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  string
		wantMessage string
		wantDetail  string
	}{
		{
			name:        "401 fails",
			err:         &refresh.TokenVerifyError{StatusCode: 401, Attempts: 1},
			wantStatus:  "fail",
			wantMessage: "access token rejected by API (HTTP 401)",
			wantDetail:  "caam login codex work",
		},
		{
			name:        "403 fails",
			err:         &refresh.TokenVerifyError{StatusCode: 403, Attempts: 1},
			wantStatus:  "fail",
			wantMessage: "access token rejected by API (HTTP 403)",
			wantDetail:  "HTTP 403",
		},
		{
			name:        "429 warns and keeps the status",
			err:         &refresh.TokenVerifyError{StatusCode: 429, Attempts: 2},
			wantStatus:  "warn",
			wantMessage: "could not verify token (HTTP 429, transient)",
			wantDetail:  "after 2 attempt(s)",
		},
		{
			name:        "503 warns",
			err:         &refresh.TokenVerifyError{StatusCode: 503, Attempts: 2},
			wantStatus:  "warn",
			wantMessage: "could not verify token (HTTP 503, transient)",
			wantDetail:  "not a credential rejection",
		},
		{
			name:        "404 warns as unexpected",
			err:         &refresh.TokenVerifyError{StatusCode: 404, Attempts: 1},
			wantStatus:  "warn",
			wantMessage: "could not verify token (unexpected HTTP 404)",
			wantDetail:  "HTTP 404",
		},
		{
			name:        "wrapped verify error is still classified",
			err:         fmt.Errorf("probe: %w", &refresh.TokenVerifyError{StatusCode: 401, Attempts: 1}),
			wantStatus:  "fail",
			wantMessage: "access token rejected by API (HTTP 401)",
		},
		{
			name:        "transport failure warns",
			err:         fmt.Errorf("request failed: %w", errors.New("dial tcp: connection refused")),
			wantStatus:  "warn",
			wantMessage: "could not verify token (network error)",
			wantDetail:  "connection refused",
		},
		{
			name:        "timeout warns",
			err:         fmt.Errorf("request failed: %w", context.DeadlineExceeded),
			wantStatus:  "warn",
			wantMessage: "could not verify token (network error)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyCodexProbeError("codex", "work", tt.err)
			if got == nil {
				t.Fatal("expected a check result")
			}
			if got.Name != "codex/work token" {
				t.Errorf("Name = %q", got.Name)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.Message != tt.wantMessage {
				t.Errorf("Message = %q, want %q", got.Message, tt.wantMessage)
			}
			if tt.wantDetail != "" && !strings.Contains(got.Details, tt.wantDetail) {
				t.Errorf("Details should contain %q, got %q", tt.wantDetail, got.Details)
			}
			// Refresh-token reuse can only be established by the token endpoint,
			// never by this probe, so it must not be asserted for non-rejections.
			if got.Status != "fail" && strings.Contains(got.Details, "refresh_token_reused") {
				t.Errorf("non-rejection must not diagnose refresh_token_reused: %q", got.Details)
			}
		})
	}
}
