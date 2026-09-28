package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/refresh"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
	"github.com/spf13/cobra"
)

type lsVerificationRow struct {
	Name   string `json:"name"`
	Health struct {
		Status             string `json:"status"`
		ExpiresAt          string `json:"expires_at"`
		LaunchUsable       *bool  `json:"launch_usable"`
		LoginRequired      *bool  `json:"login_required"`
		Verification       string `json:"verification"`
		LastVerifiedAt     string `json:"last_verified_at"`
		ProviderRejection  string `json:"provider_rejection"`
		ProviderRejectedAt string `json:"provider_rejected_at"`
	} `json:"health"`
}

func runLsJSONForTest(t *testing.T, tool string) map[string]lsVerificationRow {
	t.Helper()
	cmd := &cobra.Command{RunE: runLs}
	cmd.Flags().Bool("no-color", false, "")
	cmd.Flags().Bool("json", true, "")
	cmd.Flags().String("tag", "", "")
	_ = cmd.Flags().Set("json", "true")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := runLs(cmd, []string{tool}); err != nil {
		t.Fatalf("runLs: %v", err)
	}
	var out struct {
		Profiles []lsVerificationRow `json:"profiles"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	rows := map[string]lsVerificationRow{}
	for _, p := range out.Profiles {
		rows[p.Name] = p
	}
	return rows
}

func setupCodexVerificationVault(t *testing.T) (vaultDir string, store *health.Storage) {
	t.Helper()
	origVault, origTools, origStore, origHealth := vault, tools, profileStore, healthStore
	t.Cleanup(func() { vault, tools, profileStore, healthStore = origVault, origTools, origStore, origHealth })

	root := t.TempDir()
	vaultDir = filepath.Join(root, "vault")
	vault = authfile.NewVault(vaultDir)
	profileStore = nil
	store = health.NewStorage(filepath.Join(root, "health.json"))
	healthStore = store
	tools = map[string]func() authfile.AuthFileSet{
		"codex": func() authfile.AuthFileSet {
			return authfile.AuthFileSet{Tool: "codex", Files: []authfile.AuthFileSpec{}}
		},
	}
	return vaultDir, store
}

func writeCodexVaultAuth(t *testing.T, vaultDir, name, refreshToken string, expiresAt time.Time) []byte {
	t.Helper()
	dir := filepath.Join(vaultDir, "codex", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{
		"access_token":  "at-" + name,
		"refresh_token": refreshToken,
		"expires_at":    expiresAt.Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

// Issue #108, end to end: a freshly minted Codex credential whose refresh
// token the provider has revoked read 🟢 "9d left" and launch_usable. Once a
// provider rejection is on record for that credential, `ls --json` must say
// login_required and not launch_usable; after a new login (a different
// credential) the rejection no longer applies.
func TestLsJSONHonoursProviderRejection(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	nineDays := time.Now().Add(9 * 24 * time.Hour)
	revoked := writeCodexVaultAuth(t, vaultDir, "revoked", "rt-revoked", nineDays)
	writeCodexVaultAuth(t, vaultDir, "untouched", "rt-fine", nineDays)

	if err := store.RecordProviderVerification("codex", "revoked", health.ProviderVerification{
		Reason:      "refresh_token_invalidated",
		Fingerprint: health.CodexCredentialFingerprint(revoked),
	}); err != nil {
		t.Fatal(err)
	}

	rows := runLsJSONForTest(t, "codex")

	r := rows["revoked"]
	if r.Health.Status != "critical" {
		t.Errorf("revoked: status = %q, want critical", r.Health.Status)
	}
	if r.Health.LaunchUsable == nil || *r.Health.LaunchUsable {
		t.Errorf("revoked: launch_usable = %v, want false", r.Health.LaunchUsable)
	}
	if r.Health.LoginRequired == nil || !*r.Health.LoginRequired {
		t.Errorf("revoked: login_required = %v, want true", r.Health.LoginRequired)
	}
	if r.Health.Verification != "provider" || r.Health.ProviderRejection != "refresh_token_invalidated" || r.Health.ProviderRejectedAt == "" {
		t.Errorf("revoked: verification block = %+v", r.Health)
	}
	if r.Health.ExpiresAt == "" {
		t.Error("revoked: expires_at dropped; the expiry column stays as it was")
	}

	u := rows["untouched"]
	if u.Health.Status != "healthy" || u.Health.LaunchUsable == nil || !*u.Health.LaunchUsable {
		t.Errorf("untouched: %+v, want healthy and launch_usable", u.Health)
	}
	if u.Health.Verification != "passive" || u.Health.LastVerifiedAt != "" {
		t.Errorf("untouched: verification = %q last_verified_at = %q, want passive and none", u.Health.Verification, u.Health.LastVerifiedAt)
	}

	// The operator logs in again: a new refresh token, a new credential.
	writeCodexVaultAuth(t, vaultDir, "revoked", "rt-new-login", nineDays)
	rows = runLsJSONForTest(t, "codex")
	r = rows["revoked"]
	if r.Health.Status != "healthy" || r.Health.LaunchUsable == nil || !*r.Health.LaunchUsable {
		t.Errorf("after re-login: %+v, want healthy and launch_usable", r.Health)
	}
	if r.Health.ProviderRejection != "" {
		t.Errorf("after re-login: stale rejection %q still reported", r.Health.ProviderRejection)
	}
}

// A live `caam limits codex` read is a provider round trip. An answered read
// is an acceptance; a 401 for an unexpired access token is a rejection; a
// 401 for an expired access token (routine, the refresh token renews it), a
// 403 (an edge proxy can send one) and transport errors record nothing.
func TestRecordCodexUsageVerdicts(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	future := time.Now().Add(9 * 24 * time.Hour)
	past := time.Now().Add(-time.Hour)
	writeCodexVaultAuth(t, vaultDir, "ok", "rt-ok", future)
	rejected := writeCodexVaultAuth(t, vaultDir, "rejected", "rt-rej", future)
	writeCodexVaultAuth(t, vaultDir, "lapsed", "rt-lapsed", past)
	writeCodexVaultAuth(t, vaultDir, "offline", "rt-off", future)
	writeCodexVaultAuth(t, vaultDir, "edge403", "rt-403", future)

	now := time.Now()
	recordCodexUsageVerdicts(vaultDir, "codex", []usage.ProfileUsage{
		{ProfileName: "ok", Usage: &usage.UsageInfo{FetchedAt: now}},
		{ProfileName: "rejected", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized, HTTPStatus: 401}},
		{ProfileName: "lapsed", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized, HTTPStatus: 401}},
		{ProfileName: "edge403", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized, HTTPStatus: 403}},
		{ProfileName: "offline", Usage: &usage.UsageInfo{FetchedAt: now, Error: "request failed: dial tcp"}},
	})
	// Other providers are left alone.
	recordCodexUsageVerdicts(vaultDir, "claude", []usage.ProfileUsage{
		{ProfileName: "ok", Usage: &usage.UsageInfo{FetchedAt: now, Error: usage.ErrorUnauthorized}},
	})

	get := func(name string) *health.ProfileHealth {
		h, err := store.GetProfile("codex", name)
		if err != nil {
			t.Fatal(err)
		}
		return h
	}
	if h := get("ok"); h == nil || h.LastVerifiedAt.IsZero() {
		t.Errorf("ok: acceptance not recorded: %+v", h)
	}
	if h := get("rejected"); h == nil || h.ProviderRejectedAt.IsZero() || h.RejectedFingerprint != health.CodexCredentialFingerprint(rejected) {
		t.Errorf("rejected: rejection not recorded against its credential: %+v", h)
	}
	if h := get("lapsed"); h != nil {
		t.Errorf("lapsed: a 401 for an expired access token was recorded: %+v", h)
	}
	if h := get("edge403"); h != nil {
		t.Errorf("edge403: a 403 was recorded as a rejection: %+v", h)
	}
	if h := get("offline"); h != nil {
		t.Errorf("offline: a transport error was recorded: %+v", h)
	}
	if h, _ := store.GetProfile("claude", "ok"); h != nil {
		t.Errorf("claude row recorded: %+v", h)
	}
}

// doctor's live /v1/me probe records its verdict: a 401 marks the credential
// rejected, a 200 later clears it, and a 5xx records nothing.
func TestProbeVaultTokenRecordsVerdict(t *testing.T) {
	vaultDir, store := setupCodexVerificationVault(t)
	writeCodexVaultAuth(t, vaultDir, "work", "rt-work", time.Now().Add(9*24*time.Hour))

	status := http.StatusUnauthorized
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	defer server.Close()
	oldURL := refresh.CodexVerifyURL
	refresh.CodexVerifyURL = server.URL
	defer func() { refresh.CodexVerifyURL = oldURL }()

	if res := probeVaultToken("codex", "work"); res == nil || res.Status != "fail" {
		t.Fatalf("401 probe: %+v, want a failing check", res)
	}
	h, _ := store.GetProfile("codex", "work")
	if h == nil || h.ProviderRejection != "access_token_rejected_http_401" {
		t.Fatalf("401 not recorded: %+v", h)
	}
	if rows := runLsJSONForTest(t, "codex"); rows["work"].Health.LaunchUsable == nil || *rows["work"].Health.LaunchUsable {
		t.Errorf("ls after a rejected probe: %+v", rows["work"].Health)
	}

	status = http.StatusOK
	if res := probeVaultToken("codex", "work"); res != nil {
		t.Fatalf("200 probe: %+v, want no finding", res)
	}
	h, _ = store.GetProfile("codex", "work")
	if h == nil || !h.ProviderRejectedAt.IsZero() || h.LastVerifiedAt.IsZero() {
		t.Fatalf("200 did not clear the rejection: %+v", h)
	}
	if rows := runLsJSONForTest(t, "codex"); rows["work"].Health.Verification != "provider" || rows["work"].Health.LastVerifiedAt == "" {
		t.Errorf("ls after an accepted probe: %+v", rows["work"].Health)
	}

	status = http.StatusBadGateway
	before, _ := store.GetProfile("codex", "work")
	_ = probeVaultToken("codex", "work")
	after, _ := store.GetProfile("codex", "work")
	if !after.LastVerifiedAt.Equal(before.LastVerifiedAt) || !after.ProviderRejectedAt.IsZero() {
		t.Errorf("a 5xx changed the record: before %+v after %+v", before, after)
	}
}

// A vault copy routinely holds a lapsed access token beside a working refresh
// token. doctor's /v1/me probe answers 401 for it, and that must not mark the
// account "Login required" (review of c742360, GH #108): nothing clears such a
// false rejection until a refresh or probe succeeds, and the refresh daemon
// never refreshes an already-expired token.
func TestProbeVaultToken401ForExpiredAccessTokenRecordsNothing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	oldURL := refresh.CodexVerifyURL
	refresh.CodexVerifyURL = server.URL
	defer func() { refresh.CodexVerifyURL = oldURL }()

	for name, exp := range map[string]time.Duration{
		"expired":    -2 * time.Hour,
		"clock-skew": 2 * time.Minute,
	} {
		vaultDir, store := setupCodexVerificationVault(t)
		writeCodexVaultAuth(t, vaultDir, "work", "rt-work", time.Now().Add(exp))
		_ = probeVaultToken("codex", "work")
		if h, _ := store.GetProfile("codex", "work"); h != nil && !h.ProviderRejectedAt.IsZero() {
			t.Errorf("%s access token: 401 recorded as a provider rejection: %+v", name, h)
		}
	}
}

// Robot output for a provider-rejected profile named no reason and told
// agents to "investigate errors" (review of c742360, GH #108).
func TestRobotReasonForProviderRejection(t *testing.T) {
	ph := &health.ProfileHealth{
		TokenExpiresAt:        time.Now().Add(9 * 24 * time.Hour),
		TokenRenewable:        true,
		CredentialFingerprint: "fp",
		ProviderRejectedAt:    time.Now().Add(-time.Minute),
		ProviderRejection:     "refresh_token_expired",
		RejectedFingerprint:   "fp",
	}
	reason := getHealthReason(ph, health.CalculateStatus(ph))
	if !strings.Contains(reason, "refresh_token_expired") || !strings.Contains(reason, "login required") {
		t.Fatalf("reason = %q", reason)
	}
	rec := generateRecommendation(RobotProfileInfo{Health: RobotHealthInfo{Status: "critical", Reason: reason}})
	if rec != "log in again" {
		t.Errorf("recommendation = %q, want %q", rec, "log in again")
	}
}
