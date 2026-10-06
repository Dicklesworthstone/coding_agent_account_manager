package cmd

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider"
	cursorprovider "github.com/Dicklesworthstone/coding_agent_account_manager/internal/provider/cursor"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
)

func TestDoctorValidatesIsolatedCursorCredentials(t *testing.T) {
	setupCodexVerificationVault(t)
	profileStore = profile.NewStore(t.TempDir())
	oldRegistry := registry
	t.Cleanup(func() { registry = oldRegistry })
	registry = provider.NewRegistry()
	registry.Register(cursorprovider.New())
	for _, tc := range []struct {
		name, auth, want string
		loggedIn         bool
	}{
		{"expired", string(cursorHealthCredential(t, time.Now().Add(-time.Hour), false)), "fail", false},
		{"near_expiry", string(cursorHealthCredential(t, time.Now().Add(3*24*time.Hour), false)), "warn", true},
		{"fresh", string(cursorHealthCredential(t, time.Now().Add(30*24*time.Hour), false)), "pass", true},
		{"renewable", string(cursorHealthCredential(t, time.Now().Add(-time.Hour), true)), "pass", true},
		{"empty", `{}`, "fail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prof, err := profileStore.Create("cursor", tc.name, "oauth")
			if err != nil {
				t.Fatal(err)
			}
			env, err := cursorprovider.New().Env(context.Background(), prof)
			if err != nil {
				t.Fatal(err)
			}
			paths := authfile.ResolveCursorPaths(prof.HomePath(), runtime.GOOS, func(key string) string { return env[key] })
			writeNativeTestCredential(t, paths.AuthFile, tc.auth)
			writeNativeTestCredential(t, filepath.Join(paths.ConfigDir, "cli-config.json"), `{"authInfo":{"email":"stale@example.invalid"}}`)
			found := false
			for _, result := range checkTokenValidation() {
				if result.Name != "cursor/"+tc.name {
					continue
				}
				found = true
				if result.Status != tc.want {
					t.Fatalf("doctor result = %+v, want %s", result, tc.want)
				}
				if tc.name == "near_expiry" && (!strings.Contains(result.Details, "caam login cursor") || strings.Contains(result.Details, "caam refresh")) {
					t.Fatalf("long-lead warning omitted relogin action: %+v", result)
				}
			}
			if !found {
				t.Fatal("doctor omitted the isolated Cursor profile")
			}
			output, err := captureStdout(t, func() error { return profileStatusCmd.RunE(profileStatusCmd, []string{"cursor", tc.name}) })
			if err != nil || !strings.Contains(output, fmt.Sprintf("Logged in: %v", tc.loggedIn)) {
				t.Fatalf("profile status = %q, %v", output, err)
			}
			switch tc.name {
			case "expired":
				if !strings.Contains(output, "Expires:") || !strings.Contains(output, "log in again") || !strings.Contains(output, "caam login cursor expired") || strings.Contains(output, "caam refresh") {
					t.Fatalf("expired status omitted expiry or relogin advice: %q", output)
				}
			case "near_expiry":
				if !strings.Contains(output, "Expires:") || !strings.Contains(output, "Action: Log in again") || !strings.Contains(output, "caam login cursor near_expiry") {
					t.Fatalf("near-expiry status omitted long-lead relogin action: %q", output)
				}
			case "renewable":
				if strings.Contains(output, "Expires:") || strings.Contains(output, "Action:") || strings.Contains(strings.ToLower(output), "log in again") {
					t.Fatalf("renewable status presented a cached token as a login deadline: %q", output)
				}
			}
		})
	}
}

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
