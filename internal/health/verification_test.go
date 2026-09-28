package health

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Issue #108: a Codex credential whose refresh token the provider revoked is
// byte-for-byte like a working one — refresh token present, access token days
// from expiry. Before provider verdicts were recorded it read healthy and
// launch_usable. Once the provider has refused it, it must not.
func revokedButFreshLooking(fp string) *ProfileHealth {
	now := time.Now()
	return &ProfileHealth{
		TokenExpiresAt:        now.Add(9 * 24 * time.Hour),
		TokenRenewable:        true,
		CredentialFingerprint: fp,
		ProviderRejectedAt:    now.Add(-time.Minute),
		ProviderRejection:     "refresh_token_invalidated",
		RejectedFingerprint:   fp,
	}
}

func TestProviderRejectionOverridesFreshLookingCredential(t *testing.T) {
	h := revokedButFreshLooking("fp1")

	if !h.ProviderRejected() {
		t.Fatal("ProviderRejected() = false for a rejection of the credential on disk")
	}
	if status := CalculateStatus(h); status != StatusCritical {
		t.Errorf("status = %v, want critical", status)
	}
	if _, score := CalculateHealth(h, DefaultHealthConfig()); score >= 0 {
		t.Errorf("score = %v, want negative so rotation ranks it last", score)
	}

	sig := CredentialSignals(h, DefaultHealthConfig())
	if sig.LaunchUsable == nil || *sig.LaunchUsable {
		t.Errorf("launch_usable = %v, want false", sig.LaunchUsable)
	}
	if sig.LoginRequired == nil || !*sig.LoginRequired {
		t.Errorf("login_required = %v, want true", sig.LoginRequired)
	}
	if sig.RefreshDue == nil || *sig.RefreshDue {
		t.Errorf("refresh_due = %v, want false: a refresh cannot fix a revoked token", sig.RefreshDue)
	}

	text := FormatHealthStatus(StatusCritical, h, FormatOptions{NoColor: true})
	if strings.Contains(text, "left") || !strings.Contains(text, "Login required") {
		t.Errorf("FormatHealthStatus = %q, want a login-required display rather than the expiry", text)
	}
	reasons := strings.Join(StatusReasons(h), "; ")
	if !strings.Contains(reasons, "refresh_token_invalidated") {
		t.Errorf("StatusReasons = %q, want the rejection reason", reasons)
	}

	v := VerificationFor(h)
	if v.Verification != VerificationProvider || v.ProviderRejection != "refresh_token_invalidated" || v.ProviderRejectedAt == "" {
		t.Errorf("VerificationFor = %+v, want a provider rejection", v)
	}
}

func TestProviderRejectionIsTiedToTheRejectedCredential(t *testing.T) {
	// A new login replaced the credential: the rejection no longer applies.
	h := revokedButFreshLooking("old")
	h.CredentialFingerprint = "new"
	if h.ProviderRejected() {
		t.Error("rejection of a replaced credential still applies")
	}
	if status := CalculateStatus(h); status != StatusHealthy {
		t.Errorf("status after re-login = %v, want healthy", status)
	}
	if v := VerificationFor(h); v.Verification != VerificationPassive {
		t.Errorf("verification = %q, want passive for an unverified new credential", v.Verification)
	}

	// The current credential cannot be read: no evidence it recovered.
	h = revokedButFreshLooking("old")
	h.CredentialFingerprint = ""
	if !h.ProviderRejected() {
		t.Error("rejection dropped when the current credential is unreadable")
	}

	// A later provider acceptance supersedes the rejection.
	h = revokedButFreshLooking("fp")
	h.LastVerifiedAt = h.ProviderRejectedAt.Add(time.Second)
	if h.ProviderRejected() {
		t.Error("rejection still applies after a later acceptance")
	}
}

func TestProviderVerifiedAtIgnoresAcceptanceOfAnotherCredential(t *testing.T) {
	h := &ProfileHealth{LastVerifiedAt: time.Now(), VerifiedFingerprint: "a", CredentialFingerprint: "b"}
	if !h.ProviderVerifiedAt().IsZero() {
		t.Error("acceptance of credential a reported for credential b")
	}
	h.CredentialFingerprint = "a"
	if h.ProviderVerifiedAt().IsZero() || VerificationFor(h).LastVerifiedAt == "" {
		t.Error("acceptance of the current credential not reported")
	}
}

func TestRecordProviderVerificationRoundTrip(t *testing.T) {
	s := NewStorage(filepath.Join(t.TempDir(), "health.json"))
	if err := s.SetTokenExpiry("codex", "work", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := s.RecordProviderVerification("codex", "work", ProviderVerification{
		Reason:      "Refresh Token Invalidated: sk-secret/../x",
		Fingerprint: "fp",
	}); err != nil {
		t.Fatal(err)
	}
	h, err := s.GetProfile("codex", "work")
	if err != nil || h == nil {
		t.Fatalf("GetProfile: %v, %v", h, err)
	}
	if h.TokenExpiresAt.IsZero() {
		t.Error("recording a verdict dropped the stored expiry")
	}
	if h.ProviderRejectedAt.IsZero() || h.RejectedFingerprint != "fp" {
		t.Errorf("rejection not stored: %+v", h)
	}
	if strings.ContainsAny(h.ProviderRejection, " :/.-") || len(h.ProviderRejection) > 64 {
		t.Errorf("reason not sanitized: %q", h.ProviderRejection)
	}
	h.CredentialFingerprint = "fp"
	if !h.ProviderRejected() {
		t.Error("stored rejection does not apply to its own credential")
	}

	if err := s.RecordProviderVerification("codex", "work", ProviderVerification{Accepted: true, Fingerprint: "fp2"}); err != nil {
		t.Fatal(err)
	}
	h, _ = s.GetProfile("codex", "work")
	if !h.ProviderRejectedAt.IsZero() || h.ProviderRejection != "" || h.RejectedFingerprint != "" {
		t.Errorf("acceptance did not clear the rejection: %+v", h)
	}
	if h.LastVerifiedAt.IsZero() || h.VerifiedFingerprint != "fp2" {
		t.Errorf("acceptance not stored: %+v", h)
	}

	raw, err := os.ReadFile(filepath.Join(filepath.Dir(s.path), "health.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "credential_fingerprint") {
		t.Error("the report-time CredentialFingerprint field was persisted")
	}
}

func TestSanitizeRejectionReason(t *testing.T) {
	for in, want := range map[string]string{
		"refresh_token_invalidated": "refresh_token_invalidated",
		"Invalid-Grant":             "invalid_grant",
		"":                          "rejected",
		"!!!":                       "rejected",
		strings.Repeat("a", 200):    strings.Repeat("a", 64),
	} {
		if got := sanitizeRejectionReason(in); got != want {
			t.Errorf("sanitizeRejectionReason(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCodexCredentialFingerprint(t *testing.T) {
	nested := []byte(`{"tokens":{"access_token":"at1","refresh_token":"rt-secret-1"}}`)
	sameRefresh := []byte(`{"tokens":{"access_token":"at2","refresh_token":"rt-secret-1"},"last_refresh":"x"}`)
	newLogin := []byte(`{"tokens":{"access_token":"at3","refresh_token":"rt-secret-2"}}`)
	flat := []byte(`{"access_token":"at1","refresh_token":"rt-secret-1"}`)

	a := CodexCredentialFingerprint(nested)
	if a == "" {
		t.Fatal("no fingerprint for a nested Codex auth.json")
	}
	if strings.Contains(a, "rt-secret") {
		t.Fatal("fingerprint leaks the token")
	}
	if b := CodexCredentialFingerprint(sameRefresh); b != a {
		t.Errorf("same refresh token, different fingerprint: %q vs %q", b, a)
	}
	if c := CodexCredentialFingerprint(newLogin); c == a {
		t.Error("a new login kept the old fingerprint")
	}
	if d := CodexCredentialFingerprint(flat); d != a {
		t.Errorf("flat layout fingerprint %q differs from nested %q for the same token", d, a)
	}
	if CodexCredentialFingerprint([]byte(`not json`)) != "" || CodexCredentialFingerprint([]byte(`{}`)) != "" {
		t.Error("fingerprint invented for an unreadable credential")
	}

	path := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(path, nested, 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := ParseCodexExpiry(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Fingerprint != a {
		t.Errorf("ParseCodexExpiry fingerprint = %q, want %q", info.Fingerprint, a)
	}
}

// Readers that work from stored health alone (rotation, TUI, monitor, API,
// precheck) never ran applyExpiryInfo, so CredentialFingerprint stayed empty
// and a rejection kept applying forever after the operator logged in again
// (review of c742360, GH #108). The store binds the vault credential's
// fingerprint so a new login clears the rejection there too.
func TestStoredRejectionClearsAfterNewLoginForStoreOnlyReaders(t *testing.T) {
	root := t.TempDir()
	s := NewStorage(filepath.Join(root, "health.json"))
	authDir := filepath.Join(root, "vault", "codex", "work")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeAuth := func(rt string) []byte {
		data := []byte(`{"tokens":{"access_token":"at","refresh_token":"` + rt + `"}}`)
		if err := os.WriteFile(filepath.Join(authDir, "auth.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	old := writeAuth("rt-revoked")
	if err := s.RecordProviderVerification("codex", "work", ProviderVerification{
		Reason: "refresh_token_invalidated", Fingerprint: CodexCredentialFingerprint(old),
	}); err != nil {
		t.Fatal(err)
	}

	h, err := s.GetProfile("codex", "work")
	if err != nil || h == nil {
		t.Fatalf("GetProfile: %v %v", h, err)
	}
	if !h.ProviderRejected() || CalculateStatus(h) != StatusCritical {
		t.Fatalf("rejection of the credential still in the vault must apply: %+v", h)
	}

	writeAuth("rt-new-login")
	h, _ = s.GetProfile("codex", "work")
	if h.ProviderRejected() {
		t.Errorf("GetProfile: rejection still applies after a new login: %+v", h)
	}
	all, err := s.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	if all["codex/work"].ProviderRejected() {
		t.Errorf("ListProfiles: rejection still applies after a new login: %+v", all["codex/work"])
	}
}

// A usage read stamps its acceptance with the fetch time but records it after
// every profile was read; a rejection recorded in between by the refresh
// daemon must survive that older acceptance.
func TestOlderAcceptanceDoesNotEraseNewerRejection(t *testing.T) {
	s := NewStorage(filepath.Join(t.TempDir(), "health.json"))
	t0 := time.Now().Add(-time.Minute)
	if err := s.RecordProviderVerification("codex", "work", ProviderVerification{
		Reason: "refresh_token_invalidated", Fingerprint: "fp", At: t0.Add(30 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordProviderVerification("codex", "work", ProviderVerification{
		Accepted: true, Fingerprint: "fp", At: t0,
	}); err != nil {
		t.Fatal(err)
	}
	h, _ := s.GetProfile("codex", "work")
	h.CredentialFingerprint = "fp"
	if !h.ProviderRejected() {
		t.Fatalf("older acceptance erased a newer rejection: %+v", h)
	}

	// An acceptance of a different (newer) credential still clears it.
	if err := s.RecordProviderVerification("codex", "work", ProviderVerification{
		Accepted: true, Fingerprint: "fp2", At: t0,
	}); err != nil {
		t.Fatal(err)
	}
	h, _ = s.GetProfile("codex", "work")
	if !h.ProviderRejectedAt.IsZero() {
		t.Errorf("acceptance of a new credential did not clear the rejection: %+v", h)
	}
}

func TestAccessTokenRejectionIsEvidence(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		exp  time.Time
		want bool
	}{
		{"unknown expiry", time.Time{}, false},
		{"expired", now.Add(-time.Hour), false},
		{"inside clock-skew margin", now.Add(2 * time.Minute), false},
		{"days left", now.Add(9 * 24 * time.Hour), true},
	}
	for _, tc := range cases {
		if got := AccessTokenRejectionIsEvidence(tc.exp, now); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
