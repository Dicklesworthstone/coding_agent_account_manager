package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/identity"
)

func TestVerifyIdentityCommandRepairsOnlySavedProfileAndReportsOrganization(t *testing.T) {
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { vault = oldVault })
	writeClaudeVaultProfile(t, "work", 48*time.Hour, true)
	dir := vault.ProfilePath("claude", "work")
	if err := os.WriteFile(filepath.Join(dir, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"shared@example.test","organizationName":"Personal"},"numStartups":37}`), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		calls++
		fingerprint, err := identity.ClaudeCredentialFingerprint(data)
		if err != nil {
			return nil, err
		}
		return &identity.ClaudeIdentityBinding{Identity: identity.Identity{Provider: "claude", AccountID: "same-user", Email: "shared@example.test", OrganizationID: "team-org", Organization: "Team", Verified: true}, CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC()}, nil
	}
	run := func(flags ...string) (authfile.ClaudeProfileIdentityResult, string, error) {
		t.Helper()
		command := newVerifyIdentityCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SilenceErrors, command.SilenceUsage = true, true
		command.SetArgs(append([]string{"claude", "work", "--json"}, flags...))
		err := command.Execute()
		var result authfile.ClaudeProfileIdentityResult
		if jsonErr := json.Unmarshal(output.Bytes(), &result); jsonErr != nil {
			t.Fatalf("invalid structured identity output: %v; %s", jsonErr, output.String())
		}
		return result, output.String(), err
	}
	result, _, err := run()
	if err != nil || result.State != identity.ClaudeIdentityUnknown || calls != 0 || result.Identity != nil {
		t.Fatalf("ordinary inspection made an online assertion: %+v, %v", result, err)
	}
	result, output, err := run("--repair")
	if err != nil || !result.Repaired || result.Identity == nil || result.Identity.OrganizationID != "team-org" || calls != 1 {
		t.Fatalf("identity repair command = %+v, %v", result, err)
	}
	if strings.Contains(output, "accessToken") || strings.Contains(output, "refreshToken") || strings.Contains(output, "credential_fingerprint") {
		t.Fatal("identity command exposed credential material")
	}
	result, _, err = run()
	if err != nil || result.Source != "cache" || result.Identity.OrganizationID != "team-org" || calls != 1 {
		t.Fatalf("subsequent CLI inspection failed to reuse saved proof: %+v, %v", result, err)
	}
	// Substitution keeps a plausible expiry and the original refresh token.
	// Generic validation must surface the broken binding without a login claim.
	if err := os.WriteFile(filepath.Join(dir, ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":"SYNTHETIC-PERSONAL-ACCESS","refreshToken":"refresh","expiresAt":4102444800000}}`), 0600); err != nil {
		t.Fatal(err)
	}
	result, output, err = run()
	if err == nil || result.State != identity.ClaudeIdentityCredentialChanged || result.Identity != nil || strings.Contains(output, "SYNTHETIC-PERSONAL-ACCESS") || calls != 1 {
		t.Fatalf("CLI did not reject substituted saved tokens offline: %+v, %v", result, err)
	}
	validation, _ := validateVaultProfile("claude", "work")
	if validation.Valid || validation.IdentityState != identity.ClaudeIdentityCredentialChanged || validation.LaunchUsable == nil || *validation.LaunchUsable || validation.LoginRequired != nil || !strings.Contains(validation.Recommendation, "verify identity") || calls != 1 {
		t.Fatalf("passive validate masked identity substitution: %+v", validation)
	}
	verification := verifyProfile("claude", "work")
	if verification.Status != "critical" || verification.IdentityState != identity.ClaudeIdentityCredentialChanged || verification.LoginRequired != nil || !strings.Contains(verification.Recommendation, "verify identity") || calls != 1 {
		t.Fatalf("passive verify masked identity substitution: %+v", verification)
	}
}

func TestVerifyIdentityCommandCopiesProtectedRecoveryWithoutRebinding(t *testing.T) {
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { vault = oldVault })
	writeClaudeVaultProfile(t, "_backup_legacy", 48*time.Hour, true)
	dir := vault.ProfilePath("claude", "_backup_legacy")
	for name, data := range map[string]string{
		"meta.json":    `{"type":"system","identity":"shared@example.test","description":"personal-label"}`,
		".claude.json": `{"oauthAccount":{"emailAddress":"shared@example.test","organizationName":"Personal"},"numStartups":37}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	before := make(map[string][]byte)
	info := make(map[string]os.FileInfo)
	for _, name := range []string{".credentials.json", ".claude.json", "meta.json"} {
		var err error
		before[name], err = os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		info[name], err = os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	vault.ClaudeIdentityVerifier = func(_ context.Context, data []byte) (*identity.ClaudeIdentityBinding, error) {
		calls++
		fingerprint, err := identity.ClaudeCredentialFingerprint(data)
		if err != nil {
			return nil, err
		}
		return &identity.ClaudeIdentityBinding{Identity: identity.Identity{Provider: "claude", AccountID: "same-user", Email: "shared@example.test", OrganizationID: "team-org", Organization: "Team", Verified: true}, CredentialFingerprint: fingerprint, VerifiedAt: time.Now().UTC()}, nil
	}
	run := func(profile string, flags ...string) (authfile.ClaudeProfileIdentityResult, error) {
		t.Helper()
		command := newVerifyIdentityCommand()
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SilenceErrors, command.SilenceUsage = true, true
		command.SetArgs(append([]string{"claude", profile, "--json"}, flags...))
		err := command.Execute()
		var result authfile.ClaudeProfileIdentityResult
		if jsonErr := json.Unmarshal(output.Bytes(), &result); jsonErr != nil {
			t.Fatalf("invalid identity command result: %v", jsonErr)
		}
		if strings.Contains(output.String(), "accessToken") || strings.Contains(output.String(), "refreshToken") || strings.Contains(output.String(), "credential_fingerprint") {
			t.Fatal("identity recovery command exposed credential material")
		}
		return result, err
	}
	result, err := run("_backup_legacy", "--repair")
	if err == nil || result.Repaired || calls != 0 {
		t.Fatalf("protected repair = %+v, %v", result, err)
	}
	result, err = run("_backup_legacy", "--online", "--copy-to", "personal-label")
	if err != nil || result.CopiedTo != "personal-label" || result.Repaired || result.Identity == nil || result.Identity.OrganizationID != "team-org" || calls != 1 {
		t.Fatalf("verified recovery command = %+v, %v; calls=%d", result, err, calls)
	}
	credential, err := os.ReadFile(vault.BackupPath("claude", "personal-label", ".credentials.json"))
	if err != nil || !bytes.Equal(credential, before[".credentials.json"]) {
		t.Fatal("recovery CLI did not preserve exact saved credential bytes")
	}
	result, err = run("personal-label")
	if err != nil || result.Source != "cache" || result.Identity == nil || result.Identity.OrganizationID != "team-org" || calls != 1 {
		t.Fatalf("recovered profile could not be verified offline: %+v, %v", result, err)
	}
	result, err = run("_backup_legacy", "--copy-to", "personal-label")
	if err == nil || result.CopiedTo != "" || calls != 1 {
		t.Fatal("repeat recovery overwrote an existing destination or unnecessarily verified it")
	}
	for name, expected := range before {
		current, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || !bytes.Equal(current, expected) {
			t.Fatalf("recovery evidence bytes changed: %s", name)
		}
		after, err := os.Stat(filepath.Join(dir, name))
		if err != nil || !os.SameFile(info[name], after) || info[name].Mode() != after.Mode() || !info[name].ModTime().Equal(after.ModTime()) {
			t.Fatalf("recovery evidence file changed: %s", name)
		}
	}
}

func TestVerifyIdentityCommandRejectsAmbiguousRepairOptions(t *testing.T) {
	for _, flags := range [][]string{
		{"--repair", "--copy-to", "recovered"},
		{"--copy-to="},
		{"--copy-to", "../outside"},
		{"--copy-to", "_backup_other"},
	} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			oldVault := vault
			vault = authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
			t.Cleanup(func() { vault = oldVault })
			vault.ClaudeIdentityVerifier = func(context.Context, []byte) (*identity.ClaudeIdentityBinding, error) {
				t.Error("invalid recovery request reached the provider")
				return nil, identity.ErrClaudeIdentityUnavailable
			}
			command := newVerifyIdentityCommand()
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			command.SilenceErrors, command.SilenceUsage = true, true
			command.SetArgs(append([]string{"claude", "_backup_legacy"}, flags...))
			if err := command.Execute(); err == nil {
				t.Fatal("ambiguous recovery options succeeded")
			}
		})
	}
}

func TestVerifyIdentityDiagnosticsDistinguishHelpersFromMalformedOAuth(t *testing.T) {
	oldVault := vault
	vault = authfile.NewVault(filepath.Join(t.TempDir(), "vault"))
	t.Cleanup(func() { vault = oldVault })
	helperDir := vault.ProfilePath("claude", "helper")
	if err := os.MkdirAll(helperDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(helperDir, "settings.json"), []byte(`{"apiKeyHelper":"never-run-during-passive-validation"}`), 0600); err != nil {
		t.Fatal(err)
	}
	validation, _ := validateVaultProfile("claude", "helper")
	if !validation.Valid || validation.IdentityState != identity.ClaudeIdentityUnknown {
		t.Fatalf("supported helper-only profile was rejected by OAuth diagnostics: %+v", validation)
	}
	verification := verifyProfile("claude", "helper")
	if verification.Status == "critical" || verification.IdentityState != identity.ClaudeIdentityUnknown {
		t.Fatalf("helper-only health was treated as a broken OAuth binding: %+v", verification)
	}
	writeClaudeVaultProfile(t, "broken", 24*time.Hour, true)
	if err := os.WriteFile(vault.BackupPath("claude", "broken", ".credentials.json"), []byte(`{"claudeAiOauth":{"accessToken":123}}`), 0600); err != nil {
		t.Fatal(err)
	}
	validation, _ = validateVaultProfile("claude", "broken")
	if validation.Valid || validation.IdentityState != "invalid_credentials" || validation.LoginRequired == nil || !*validation.LoginRequired || strings.Contains(validation.Recommendation, "verify identity") {
		t.Fatalf("malformed OAuth was recommended metadata-only repair: %+v", validation)
	}
	verification = verifyProfile("claude", "broken")
	if verification.Status != "critical" || verification.LoginRequired == nil || !*verification.LoginRequired {
		t.Fatalf("malformed OAuth did not receive invalid-credential health: %+v", verification)
	}
}

func TestVerifyProfileResult(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	result := VerifyProfileResult{
		Provider:    "claude",
		Profile:     "work",
		Status:      "healthy",
		TokenExpiry: &expiry,
		ExpiresIn:   "59m",
		ErrorCount:  0,
		Penalty:     0,
		Issues:      []string{},
		Score:       1.3,
	}

	// Test JSON serialization
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("Failed to marshal VerifyProfileResult: %v", err)
	}

	var decoded VerifyProfileResult
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal VerifyProfileResult: %v", err)
	}

	if decoded.Provider != result.Provider {
		t.Errorf("Provider mismatch: got %s, want %s", decoded.Provider, result.Provider)
	}
	if decoded.Profile != result.Profile {
		t.Errorf("Profile mismatch: got %s, want %s", decoded.Profile, result.Profile)
	}
	if decoded.Status != result.Status {
		t.Errorf("Status mismatch: got %s, want %s", decoded.Status, result.Status)
	}
}

func TestVerifyOutput(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	output := VerifyOutput{
		Profiles: []VerifyProfileResult{
			{
				Provider:    "claude",
				Profile:     "work",
				Status:      "healthy",
				TokenExpiry: &expiry,
				ExpiresIn:   "59m",
				Score:       1.3,
			},
			{
				Provider:    "claude",
				Profile:     "personal",
				Status:      "warning",
				TokenExpiry: &expiry,
				ExpiresIn:   "45m",
				ErrorCount:  1,
				Issues:      []string{"Token expiring soon"},
				Score:       0.4,
			},
		},
		Summary: VerifySummary{
			TotalProfiles: 2,
			HealthyCount:  1,
			WarningCount:  1,
			CriticalCount: 0,
			UnknownCount:  0,
		},
		Recommendations: []string{
			"Run 'caam refresh claude personal' to refresh expiring token",
		},
	}

	// Test JSON serialization
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		t.Fatalf("Failed to marshal VerifyOutput: %v", err)
	}

	var decoded VerifyOutput
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Failed to unmarshal VerifyOutput: %v", err)
	}

	if decoded.Summary.TotalProfiles != 2 {
		t.Errorf("TotalProfiles mismatch: got %d, want 2", decoded.Summary.TotalProfiles)
	}
	if decoded.Summary.HealthyCount != 1 {
		t.Errorf("HealthyCount mismatch: got %d, want 1", decoded.Summary.HealthyCount)
	}
}

func TestFormatTimeRemaining(t *testing.T) {
	tests := []struct {
		duration time.Duration
		expected string
	}{
		{30 * time.Second, "< 1m"},
		{5 * time.Minute, "5m"},
		{59 * time.Minute, "59m"},
		{1 * time.Hour, "1h"},
		{90 * time.Minute, "1h30m"},
		{24 * time.Hour, "1d"},
		{48 * time.Hour, "2d"},
		{-5 * time.Minute, "5m"}, // Handles negative (expired)
	}

	for _, tc := range tests {
		got := formatTimeRemaining(tc.duration)
		if got != tc.expected {
			t.Errorf("formatTimeRemaining(%v) = %q, want %q", tc.duration, got, tc.expected)
		}
	}
}

func TestGetVerifyStatusIcon(t *testing.T) {
	tests := []struct {
		status   string
		expected string
	}{
		{"healthy", "✓"},
		{"warning", "⚠"},
		{"critical", "✗"},
		{"unknown", "?"},
		{"invalid", "?"},
	}

	for _, tc := range tests {
		got := getVerifyStatusIcon(tc.status)
		if got != tc.expected {
			t.Errorf("getVerifyStatusIcon(%q) = %q, want %q", tc.status, got, tc.expected)
		}
	}
}

func TestGenerateRecommendations(t *testing.T) {
	now := time.Now()
	expiredTime := now.Add(-time.Hour)
	expiringSoonTime := now.Add(30 * time.Minute)

	output := &VerifyOutput{
		Profiles: []VerifyProfileResult{
			{
				Provider:       "claude",
				Profile:        "work",
				Status:         "critical",
				TokenExpiry:    &expiredTime,
				ExpiresIn:      "expired",
				Recommendation: health.FormatRecommendation("claude", "work", &health.ProfileHealth{TokenExpiresAt: expiredTime}),
			},
			{
				Provider:       "codex",
				Profile:        "dev",
				Status:         "warning",
				TokenExpiry:    &expiringSoonTime,
				ExpiresIn:      "30m",
				Recommendation: health.FormatRecommendation("codex", "dev", &health.ProfileHealth{TokenExpiresAt: expiringSoonTime, TokenRenewable: true}),
			},
		},
		Summary: VerifySummary{
			TotalProfiles: 2,
			CriticalCount: 1,
			WarningCount:  1,
		},
	}

	recs := generateRecommendations(output)

	// Should have recommendations for expiring and expired tokens
	if len(recs) == 0 {
		t.Error("Expected recommendations to be generated")
	}

	// Check for refresh recommendation
	hasRefresh := false
	for _, r := range recs {
		if strings.Contains(r, "refresh") {
			hasRefresh = true
			break
		}
	}
	if !hasRefresh {
		t.Error("Expected a refresh recommendation for expiring token")
	}

	// Check for re-login recommendation
	hasRelogin := false
	for _, r := range recs {
		if strings.Contains(r, "caam login") {
			hasRelogin = true
			break
		}
	}
	if !hasRelogin {
		t.Error("Expected a re-login recommendation for expired token")
	}
}

func TestCursorSessionVerifyRecommendation(t *testing.T) {
	expires := time.Now().Add(12 * time.Hour)
	output := &VerifyOutput{Profiles: []VerifyProfileResult{{
		Provider: "cursor", Profile: "session", TokenExpiry: &expires,
		ReloginWarningLead: 24 * time.Hour,
	}}}
	recs := generateRecommendations(output)
	if len(recs) != 1 || !strings.Contains(recs[0], "caam login cursor session") || strings.Contains(recs[0], "refresh") {
		t.Fatalf("non-renewable session recommendation = %v", recs)
	}
}

func TestPrintVerifyOutput(t *testing.T) {
	now := time.Now()
	expiry := now.Add(time.Hour)

	output := &VerifyOutput{
		Profiles: []VerifyProfileResult{
			{
				Provider:    "claude",
				Profile:     "work",
				Status:      "healthy",
				TokenExpiry: &expiry,
				ExpiresIn:   "59m",
				Score:       1.3,
			},
		},
		Summary: VerifySummary{
			TotalProfiles: 1,
			HealthyCount:  1,
		},
	}

	var buf bytes.Buffer
	printVerifyOutput(&buf, output)
	result := buf.String()

	// Check for expected output elements
	if !strings.Contains(result, "Profile Health Verification") {
		t.Error("Expected output to contain header")
	}
	if !strings.Contains(result, "Claude:") {
		t.Error("Expected output to contain provider name")
	}
	if !strings.Contains(result, "work") {
		t.Error("Expected output to contain profile name")
	}
	if !strings.Contains(result, "✓") {
		t.Error("Expected output to contain healthy status icon")
	}
	if !strings.Contains(result, "Summary:") {
		t.Error("Expected output to contain summary")
	}
}

func TestPrintVerifyOutputNoProfiles(t *testing.T) {
	output := &VerifyOutput{
		Profiles: []VerifyProfileResult{},
		Summary:  VerifySummary{},
	}

	var buf bytes.Buffer
	printVerifyOutput(&buf, output)
	result := buf.String()

	if !strings.Contains(result, "No profiles found") {
		t.Error("Expected 'No profiles found' message for empty profile list")
	}
}
