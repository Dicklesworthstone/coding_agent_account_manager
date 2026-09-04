package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Issue #100: `caam limits --profile NAME` read <vault>/<provider>/<name> and
// said nothing about it, so a healthy Claude account whose isolated-profile
// credentials had just been refreshed by an in-app /login was reported as
// "unauthorized: token expired or invalid" from a stale vault copy.
//
// The acceptance criterion from the report: stale vault credentials and
// healthy XDG credentials under one name must make `limits` either select an
// explicit source or report the ambiguity, never present the vault result as
// the profile's state.

// claudeCreds writes a Claude credential file with the given expiry, and with
// or without a refresh token (a refresh token makes the credential renewable
// and therefore healthy even past its expiry — issue #102).
func claudeCreds(t *testing.T, path string, expires time.Time, refreshable bool) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	oauth := map[string]any{
		"accessToken": "SYNTHETIC-ACCESS-" + filepath.Base(filepath.Dir(path)),
		"expiresAt":   expires.UnixMilli(),
	}
	if refreshable {
		oauth["refreshToken"] = "SYNTHETIC-REFRESH"
	}
	data, err := json.Marshal(map[string]any{"claudeAiOauth": oauth})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// namespaceFixture builds a vault, an isolated profile store and a shallow
// base under one temp root, and returns a lookup over them.
type namespaceFixture struct {
	root     string
	vaultDir string
	store    *profile.Store
	shallow  *shallow.Manager
	lookup   credentialLookup
}

func newNamespaceFixture(t *testing.T) *namespaceFixture {
	t.Helper()
	root := t.TempDir()
	realHome := filepath.Join(root, "home")
	if err := os.MkdirAll(realHome, 0o700); err != nil {
		t.Fatal(err)
	}
	mgr, err := shallow.NewManager(filepath.Join(root, "orch-homes"), realHome)
	if err != nil {
		t.Fatalf("shallow.NewManager: %v", err)
	}
	f := &namespaceFixture{
		root:     root,
		vaultDir: filepath.Join(root, "vault"),
		store:    profile.NewStore(filepath.Join(root, "profiles")),
		shallow:  mgr,
	}
	f.lookup = credentialLookup{
		VaultDir: f.vaultDir,
		Profiles: f.store,
		Shallow:  f.shallow,
		LiveHome: realHome,
		Now:      time.Now(),
	}
	return f
}

func (f *namespaceFixture) vaultClaude(t *testing.T, name string, expires time.Time, refreshable bool) {
	t.Helper()
	claudeCreds(t, filepath.Join(f.vaultDir, "claude", name, ".credentials.json"), expires, refreshable)
}

// isolatedClaude writes credentials where an in-app /login under `caam exec`
// puts them: the profile's XDG config dir, not its pseudo-HOME.
func (f *namespaceFixture) isolatedClaude(t *testing.T, name string, expires time.Time, refreshable bool) {
	t.Helper()
	prof, err := f.store.Create("claude", name, "oauth")
	if err != nil {
		t.Fatalf("create isolated profile: %v", err)
	}
	claudeCreds(t, filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json"), expires, refreshable)
}

func (f *namespaceFixture) shallowClaude(t *testing.T, name string, expires time.Time, refreshable bool) {
	t.Helper()
	if _, err := f.shallow.Create(name, shallow.CreateOptions{Provider: "claude"}); err != nil {
		t.Fatalf("create shallow profile: %v", err)
	}
	path, err := f.shallow.CredentialPath(name)
	if err != nil {
		t.Fatal(err)
	}
	claudeCreds(t, path, expires, refreshable)
}

// TestResolveProfileCredentialRefusesTheStaleVaultCopy is the reported bug.
func TestResolveProfileCredentialRefusesTheStaleVaultCopy(t *testing.T) {
	f := newNamespaceFixture(t)
	past := time.Now().Add(-30 * 24 * time.Hour)
	future := time.Now().Add(4 * time.Hour)

	f.vaultClaude(t, "salim", past, false)     // stale, nothing to renew with
	f.isolatedClaude(t, "salim", future, true) // freshly logged in

	res, err := resolveProfileCredential(f.lookup, "claude", "salim", "")
	if err != nil {
		t.Fatalf("resolveProfileCredential: %v", err)
	}
	if res.Selected.Namespace != credNamespaceVault {
		t.Fatalf("default namespace = %q, want %q", res.Selected.Namespace, credNamespaceVault)
	}
	if res.Selected.State != credStateExpired {
		t.Errorf("vault state = %q, want %q", res.Selected.State, credStateExpired)
	}
	if len(res.Healthier) != 1 || res.Healthier[0].Namespace != credNamespaceIsolated {
		t.Fatalf("Healthier = %+v, want the isolated copy", res.Healthier)
	}

	// The refusal must name both namespaces and hand over the exact commands.
	msg := res.AmbiguityError("claude", "salim").Error()
	for _, want := range []string{
		"ambiguous", credNamespaceVault, credNamespaceIsolated,
		"caam limits claude --profile salim --source isolated",
		"caam limits claude --profile salim --source vault",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("ambiguity error missing %q:\n%s", want, msg)
		}
	}
}

// TestResolveProfileCredentialExplicitSource: --source is the override, and it
// suppresses the ambiguity refusal entirely.
func TestResolveProfileCredentialExplicitSource(t *testing.T) {
	f := newNamespaceFixture(t)
	past := time.Now().Add(-30 * 24 * time.Hour)
	future := time.Now().Add(4 * time.Hour)
	f.vaultClaude(t, "salim", past, false)
	f.isolatedClaude(t, "salim", future, true)

	res, err := resolveProfileCredential(f.lookup, "claude", "salim", credNamespaceIsolated)
	if err != nil {
		t.Fatalf("resolveProfileCredential: %v", err)
	}
	if res.Selected.Namespace != credNamespaceIsolated || res.Selected.State != credStateHealthy {
		t.Fatalf("selected = %+v, want a healthy isolated credential", res.Selected)
	}
	if !res.Explicit {
		t.Error("Explicit = false, want true")
	}
	if len(res.Healthier) != 0 {
		t.Errorf("Healthier = %+v, want none when the caller chose", res.Healthier)
	}
	if res.Selected.Token == "" {
		t.Error("no token read from the isolated namespace")
	}

	// Asking for a namespace that does not hold the name is an error that says
	// where it does live.
	if _, err := resolveProfileCredential(f.lookup, "claude", "salim", credNamespaceShallow); err == nil {
		t.Error("want an error for a namespace with no such profile")
	} else if !strings.Contains(err.Error(), credNamespaceVault) {
		t.Errorf("error should point at the namespaces that do hold it: %v", err)
	}

	if _, err := resolveProfileCredential(f.lookup, "claude", "salim", "nonsense"); err == nil {
		t.Error("want an error for an unknown --source")
	}
}

// TestResolveProfileCredentialNoAmbiguityWhenVaultIsFine: an alternative that
// is not strictly healthier must be reported but must not block the lookup.
func TestResolveProfileCredentialNoAmbiguityWhenVaultIsFine(t *testing.T) {
	f := newNamespaceFixture(t)
	future := time.Now().Add(4 * time.Hour)
	f.vaultClaude(t, "dual", future, true)
	f.shallowClaude(t, "dual", future, true)

	res, err := resolveProfileCredential(f.lookup, "claude", "dual", "")
	if err != nil {
		t.Fatalf("resolveProfileCredential: %v", err)
	}
	if len(res.Healthier) != 0 {
		t.Errorf("Healthier = %+v, want none: both copies are healthy", res.Healthier)
	}
	if len(res.Alternatives) != 1 || res.Alternatives[0].Namespace != credNamespaceShallow {
		t.Fatalf("Alternatives = %+v, want the shallow copy", res.Alternatives)
	}

	// The human line must name the namespace and path actually read, and the
	// alternative, so the operator can see which copy produced the numbers.
	desc := res.describe("claude", "dual")
	for _, want := range []string{"vault", "shallow", f.vaultDir, "--source"} {
		if !strings.Contains(desc, want) {
			t.Errorf("describe() missing %q:\n%s", want, desc)
		}
	}

	// The JSON contract carries the same facts.
	report := res.report()
	if report.Namespace != credNamespaceVault || report.State != credStateHealthy || report.Explicit {
		t.Errorf("report = %+v", report)
	}
	if len(report.Alternatives) != 1 || report.Alternatives[0].Healthier {
		t.Errorf("report alternatives = %+v", report.Alternatives)
	}
	if _, err := json.Marshal(report); err != nil {
		t.Fatalf("marshal credential source: %v", err)
	}
}

// TestResolveProfileCredentialUnknownName: with the name in no namespace, the
// error says so rather than reporting an expired credential.
func TestResolveProfileCredentialUnknownName(t *testing.T) {
	f := newNamespaceFixture(t)
	_, err := resolveProfileCredential(f.lookup, "claude", "ghost", "")
	if err == nil {
		t.Fatal("want an error for a name in no namespace")
	}
	for _, want := range []string{"ghost", "vault", "isolated", "shallow"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// TestNamespaceProfileNames backs `--source <ns>` without `--profile`.
func TestNamespaceProfileNames(t *testing.T) {
	f := newNamespaceFixture(t)
	future := time.Now().Add(4 * time.Hour)
	f.vaultClaude(t, "b", future, true)
	f.vaultClaude(t, "a", future, true)
	f.isolatedClaude(t, "iso", future, true)
	f.shallowClaude(t, "sh", future, true)

	got, err := namespaceProfileNames(f.lookup, credNamespaceVault, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("vault names = %v, want [a b] sorted", got)
	}

	if got, err = namespaceProfileNames(f.lookup, credNamespaceIsolated, "claude"); err != nil || len(got) != 1 || got[0] != "iso" {
		t.Errorf("isolated names = %v (err %v), want [iso]", got, err)
	}
	if got, err = namespaceProfileNames(f.lookup, credNamespaceShallow, "claude"); err != nil || len(got) != 1 || got[0] != "sh" {
		t.Errorf("shallow names = %v (err %v), want [sh]", got, err)
	}
	// A codex-shaped question must not sweep up claude shallow profiles.
	if got, err = namespaceProfileNames(f.lookup, credNamespaceShallow, "codex"); err != nil || len(got) != 0 {
		t.Errorf("shallow codex names = %v (err %v), want none", got, err)
	}
}

// Issue #88: the offline usage source. `caam limits --cached` reads the
// snapshot Claude Code writes into each account's own .claude.json, so the
// staleness the feature trades for has to be visible per row — and a profile
// with no snapshot must read "no cached data", never 0%.

func TestCachedProfileUsage(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	fetched := now.Add(-3 * time.Hour)

	withCache := filepath.Join(dir, "with.json")
	body := `{"cachedUsageUtilization":{"fetchedAtMs":` +
		itoaMillis(fetched) +
		`,"accountUuid":"u","utilization":{"limits":[
		  {"kind":"session","group":"session","percent":53,"resets_at":"2099-01-01T00:00:00Z"},
		  {"kind":"weekly_all","group":"weekly","percent":11,"resets_at":"2099-01-05T00:00:00Z"}]}}}`
	if err := os.WriteFile(withCache, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	row := cachedProfileUsage("claude", "warm", withCache, now)
	if row.Usage == nil || row.Usage.Error != "" {
		t.Fatalf("row = %+v, want a clean cached row", row.Usage)
	}
	if row.Usage.PrimaryWindow.UsedPercent != 53 {
		t.Errorf("primary = %d%%, want 53", row.Usage.PrimaryWindow.UsedPercent)
	}
	if got := formatCacheAge(row.Usage, now); got != "3h0m ago" {
		t.Errorf("AS OF = %q, want %q", got, "3h0m ago")
	}

	// A profile Claude Code has never refreshed: "no cached data", and no
	// fabricated percentages anywhere on the row.
	cold := cachedProfileUsage("claude", "cold", filepath.Join(dir, "absent.json"), now)
	if cold.Usage == nil || cold.Usage.Error != usage.ErrNoCachedUsage.Error() {
		t.Fatalf("cold row = %+v, want the no-cached-data marker", cold.Usage)
	}
	if cold.Usage.PrimaryWindow != nil || cold.Usage.SecondaryWindow != nil {
		t.Error("a profile with no snapshot must have no windows, not zeroed ones")
	}
	if got := formatWindowPercent(cold.Usage.PrimaryWindow); got != "-" {
		t.Errorf("primary column = %q, want %q", got, "-")
	}
	if got := formatCacheAge(cold.Usage, now); got != "-" {
		t.Errorf("AS OF for an uncached profile = %q, want %q", got, "-")
	}

	// A namespace that cannot hold a .claude.json at all is the same story.
	none := cachedProfileUsage("claude", "none", "", now)
	if none.Usage.Error != usage.ErrNoCachedUsage.Error() {
		t.Errorf("empty path row = %+v", none.Usage)
	}
}

// TestRenderLimitsCachedTable pins the offline table: the AS OF column, the
// "no cached data" status, and the rolled-window marker.
func TestRenderLimitsCachedTable(t *testing.T) {
	now := time.Now()
	rows := []usage.ProfileUsage{
		{
			Provider: "claude", ProfileName: "warm",
			Usage: &usage.UsageInfo{
				Provider: "claude", Source: usage.SourceCache, FetchedAt: now.Add(-90 * time.Minute),
				PrimaryWindow:   &usage.UsageWindow{UsedPercent: 0, Rolled: true},
				SecondaryWindow: &usage.UsageWindow{UsedPercent: 29},
			},
		},
		{
			Provider: "claude", ProfileName: "cold",
			Usage: &usage.UsageInfo{
				Provider: "claude", Source: usage.SourceCache,
				Error: usage.ErrNoCachedUsage.Error(),
			},
		},
	}

	var b strings.Builder
	if err := renderLimits(&b, "table", rows, ""); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"AS OF", "no cached data", "0% (rolled)", "1h30m ago", "no network"} {
		if !strings.Contains(out, want) {
			t.Errorf("cached table missing %q:\n%s", want, out)
		}
	}

	// A live table must not grow the column.
	live := []usage.ProfileUsage{{
		Provider: "claude", ProfileName: "x",
		Usage: &usage.UsageInfo{Provider: "claude", Source: usage.SourceAPI, FetchedAt: now},
	}}
	b.Reset()
	if err := renderLimits(&b, "table", live, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b.String(), "AS OF") {
		t.Errorf("live table grew an AS OF column:\n%s", b.String())
	}
}

// TestCachedRowsAreNotOfferedAsBest: an account caam knows nothing about must
// never be recommended as the one with headroom.
func TestCachedRowsAreNotOfferedAsBest(t *testing.T) {
	rows := []usage.ProfileUsage{
		{Provider: "claude", ProfileName: "cold", Usage: &usage.UsageInfo{
			Provider: "claude", Source: usage.SourceCache, Error: usage.ErrNoCachedUsage.Error(),
		}},
		{Provider: "claude", ProfileName: "warm", Usage: &usage.UsageInfo{
			Provider: "claude", Source: usage.SourceCache, FetchedAt: time.Now(),
			PrimaryWindow: &usage.UsageWindow{UsedPercent: 40, Utilization: 0.4},
		}},
	}
	var b strings.Builder
	if err := renderBestProfile(&b, "table", rows, 0.8, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "warm") || strings.Contains(b.String(), "cold") {
		t.Errorf("best profile = %q, want the profile that actually has data", b.String())
	}
}

func itoaMillis(t time.Time) string {
	return strconv.FormatInt(t.UnixMilli(), 10)
}
