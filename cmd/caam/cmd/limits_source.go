package cmd

// Credential namespaces for `caam limits --profile` (issue #100).
//
// caam stores a profile's credentials in three unrelated places, and one name
// can exist in all three at once:
//
//   - vault    — the backup/activate store, <vault>/<provider>/<name>/
//   - isolated — a `caam profile` sandbox, its own HOME plus XDG config dir,
//                which is where an in-app `/login` under `caam exec` writes
//   - shallow  — a shallow HOME under ~/orch-homes/<name>/
//
// `caam limits --profile NAME` used to read the vault copy and say nothing
// about it. For Claude that is exactly backwards: Claude cannot use `caam
// login`, its supported isolated-profile flow is `caam exec claude <name>`
// plus an in-app `/login`, and that flow never touches the vault. So the one
// provider whose login path cannot refresh the vault copy was reported purely
// from the vault copy, and a healthy account came back "unauthorized: token
// expired or invalid".
//
// The fix is source honesty, not convergence — rotating OAuth credentials are
// never copied between namespaces implicitly (see issue #73):
//
//  1. Output always names the namespace and path actually read.
//  2. When the same name exists elsewhere, the alternatives are reported with
//     their state, so an operator can see the vault copy is the stale one.
//  3. When an unselected namespace is strictly healthier than the default and
//     the caller did not pick one, the lookup FAILS rather than emitting a
//     routing verdict a controller would act on. `--source` is the override.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/authfile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/health"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/profile"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/shallow"
	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/usage"
)

// Credential namespace names, as accepted by --source and reported in output.
const (
	credNamespaceVault    = "vault"
	credNamespaceIsolated = "isolated"
	credNamespaceShallow  = "shallow"
)

// credNamespaces is the resolution order: the vault stays first so an
// unqualified --profile keeps meaning what it always meant.
var credNamespaces = []string{credNamespaceVault, credNamespaceIsolated, credNamespaceShallow}

// Credential states, worst to best. A namespace that holds no credential for
// the name at all is credStateMissing.
const (
	credStateMissing = "missing"
	credStateExpired = "expired"
	credStateUnknown = "unknown"
	credStateHealthy = "healthy"
)

// credStateRank orders states so "strictly healthier" is a comparison.
func credStateRank(state string) int {
	switch state {
	case credStateHealthy:
		return 3
	case credStateUnknown:
		return 2
	case credStateExpired:
		return 1
	default:
		return 0
	}
}

// credentialLookup holds the roots the three namespaces live under. Every path
// is injected so the resolver can be tested against temp directories.
type credentialLookup struct {
	VaultDir   string
	Profiles   *profile.Store
	Shallow    *shallow.Manager
	LiveHome   string // real HOME, for the live .claude.json of the active profile
	ActiveName func(provider string) string
	Now        time.Time
}

// credentialCandidate is what one namespace holds for a name.
type credentialCandidate struct {
	Namespace string
	// Path is the credential file that was read, or the path that was looked
	// for when nothing was found.
	Path string
	// ClaudeJSON is the profile's .claude.json in this namespace, which is
	// where Claude Code caches usage. Empty for non-claude providers.
	ClaudeJSON string
	Token      string
	State      string
	ExpiresAt  time.Time
}

// Found reports whether this namespace actually holds a credential.
func (c credentialCandidate) Found() bool { return c.State != credStateMissing }

// resolvedCredential is the outcome of a --profile lookup.
type resolvedCredential struct {
	Selected     credentialCandidate
	Alternatives []credentialCandidate
	// Healthier lists the alternatives that are strictly better than the
	// selected one. Non-empty only when the caller did not pass --source.
	Healthier []credentialCandidate
	Explicit  bool // --source was given
}

// ValidCredNamespace reports whether s names a namespace.
func ValidCredNamespace(s string) bool {
	for _, n := range credNamespaces {
		if n == s {
			return true
		}
	}
	return false
}

// candidatePaths lists, in preference order, the credential files a namespace
// may hold for (provider, name). A namespace that cannot hold this provider
// returns nothing.
func (l credentialLookup) candidatePaths(namespace, provider, name string) []string {
	switch namespace {
	case credNamespaceVault:
		dir := filepath.Join(l.VaultDir, provider, name)
		switch provider {
		case "claude":
			return []string{
				filepath.Join(dir, ".credentials.json"),
				filepath.Join(dir, ".claude.json"),
				filepath.Join(dir, "auth.json"),
			}
		case "codex":
			return []string{filepath.Join(dir, "auth.json")}
		}
	case credNamespaceIsolated:
		if l.Profiles == nil {
			return nil
		}
		prof, err := l.Profiles.Load(provider, name)
		if err != nil {
			return nil
		}
		switch provider {
		case "claude":
			// Legacy HOME layout first, then the XDG layout an in-app
			// /login writes under `caam exec` (issues #70, #72, #100).
			return []string{
				filepath.Join(prof.HomePath(), ".claude", ".credentials.json"),
				filepath.Join(prof.XDGConfigPath(), "claude-code", ".credentials.json"),
			}
		case "codex":
			return []string{filepath.Join(prof.CodexHomePath(), "auth.json")}
		}
	case credNamespaceShallow:
		if l.Shallow == nil {
			return nil
		}
		// A shallow profile knows its own provider; refuse to read a codex
		// profile's auth.json as if it were claude's.
		if p, err := l.Shallow.ResolveProvider(name); err != nil || shallow.NormalizeProvider(p) != provider {
			return nil
		}
		path, err := l.Shallow.CredentialPath(name)
		if err != nil {
			return nil
		}
		return []string{path}
	}
	return nil
}

// claudeJSONPath returns where this namespace keeps the profile's
// .claude.json — the file Claude Code caches usage into. For the profile that
// is currently active in the real HOME the live file supersedes the vault
// copy, which froze when the profile was switched away from.
func (l credentialLookup) claudeJSONPath(namespace, provider, name string) string {
	if provider != "claude" {
		return ""
	}
	switch namespace {
	case credNamespaceVault:
		if l.ActiveName != nil && l.LiveHome != "" && l.ActiveName(provider) == name {
			return filepath.Join(l.LiveHome, ".claude.json")
		}
		return filepath.Join(l.VaultDir, provider, name, ".claude.json")
	case credNamespaceIsolated:
		if l.Profiles == nil {
			return ""
		}
		prof, err := l.Profiles.Load(provider, name)
		if err != nil {
			return ""
		}
		return filepath.Join(prof.HomePath(), ".claude.json")
	case credNamespaceShallow:
		if l.Shallow == nil {
			return ""
		}
		home, err := l.Shallow.HomeFor(name)
		if err != nil {
			return ""
		}
		return filepath.Join(home, ".claude.json")
	}
	return ""
}

// inspect reads what one namespace holds for (provider, name).
func (l credentialLookup) inspect(namespace, provider, name string) credentialCandidate {
	out := credentialCandidate{Namespace: namespace, State: credStateMissing}
	paths := l.candidatePaths(namespace, provider, name)
	if len(paths) == 0 {
		return out
	}
	out.Path = paths[0]
	out.ClaudeJSON = l.claudeJSONPath(namespace, provider, name)

	for _, path := range paths {
		var token string
		var err error
		switch provider {
		case "claude":
			token, _, err = usage.ReadClaudeCredentials(path)
		case "codex":
			token, _, err = usage.ReadCodexCredentials(path)
		default:
			return out
		}
		if err != nil || token == "" {
			continue
		}
		out.Path = path
		out.Token = token
		out.State, out.ExpiresAt = l.credentialState(provider, path)
		return out
	}
	return out
}

// credentialState classifies a credential file that was read successfully.
// A renewable credential counts as healthy even past its expiry: the CLI or
// caam's refresher renews it without a human (issue #102).
func (l credentialLookup) credentialState(provider, path string) (string, time.Time) {
	var (
		info *health.ExpiryInfo
		err  error
	)
	switch provider {
	case "claude":
		info, err = health.ParseClaudeExpiry(filepath.Dir(path))
	case "codex":
		info, err = health.ParseCodexExpiry(path)
	}
	if err != nil || info == nil || info.ExpiresAt.IsZero() {
		return credStateUnknown, time.Time{}
	}
	now := l.Now
	if now.IsZero() {
		now = time.Now()
	}
	if info.ExpiresAt.After(now) || info.Renewable {
		return credStateHealthy, info.ExpiresAt
	}
	return credStateExpired, info.ExpiresAt
}

// resolveProfileCredential picks the namespace to read a named profile from
// and reports what else holds the same name.
//
// source == "" keeps the historical behavior — the vault wins — but the caller
// is told which namespace that was and, when another namespace holds a
// strictly healthier credential, is refused rather than handed a verdict drawn
// from the stale copy.
func resolveProfileCredential(l credentialLookup, provider, name, source string) (*resolvedCredential, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	if source != "" && !ValidCredNamespace(source) {
		return nil, fmt.Errorf("unknown --source %q (want one of: %s)", source, strings.Join(credNamespaces, ", "))
	}

	found := make([]credentialCandidate, 0, len(credNamespaces))
	for _, ns := range credNamespaces {
		if c := l.inspect(ns, provider, name); c.Found() {
			found = append(found, c)
		}
	}

	if source != "" {
		for _, c := range found {
			if c.Namespace == source {
				res := &resolvedCredential{Selected: c, Explicit: true}
				res.Alternatives = othersThan(found, source)
				return res, nil
			}
		}
		return nil, fmt.Errorf("no %s credential for %s/%s%s", source, provider, name, whereElse(found))
	}

	if len(found) == 0 {
		return nil, fmt.Errorf("no credentials found for %s/%s in any namespace (%s)", provider, name, strings.Join(credNamespaces, ", "))
	}

	selected := found[0] // credNamespaces order: vault first
	res := &resolvedCredential{Selected: selected}
	res.Alternatives = othersThan(found, selected.Namespace)
	for _, alt := range res.Alternatives {
		if credStateRank(alt.State) > credStateRank(selected.State) {
			res.Healthier = append(res.Healthier, alt)
		}
	}
	return res, nil
}

func othersThan(found []credentialCandidate, namespace string) []credentialCandidate {
	var out []credentialCandidate
	for _, c := range found {
		if c.Namespace != namespace {
			out = append(out, c)
		}
	}
	return out
}

func whereElse(found []credentialCandidate) string {
	if len(found) == 0 {
		return ""
	}
	names := make([]string, 0, len(found))
	for _, c := range found {
		names = append(names, c.Namespace)
	}
	return " (present in: " + strings.Join(names, ", ") + ")"
}

// AmbiguityError renders the refusal described at the top of this file: what
// was found where, and the exact commands that disambiguate it.
func (r *resolvedCredential) AmbiguityError(provider, name string) error {
	var b strings.Builder
	fmt.Fprintf(&b, "profile %q is ambiguous for %s: the %s credential is %s, but ",
		name, provider, r.Selected.Namespace, r.Selected.State)
	parts := make([]string, 0, len(r.Healthier))
	for _, alt := range r.Healthier {
		parts = append(parts, fmt.Sprintf("the %s one is %s", alt.Namespace, alt.State))
	}
	b.WriteString(strings.Join(parts, " and "))
	b.WriteString(".\n")
	fmt.Fprintf(&b, "caam limits reads the %s namespace by default and will not report a routing verdict from the stale copy.\n", credNamespaceVault)
	b.WriteString("Choose the source explicitly:\n")
	for _, alt := range r.Healthier {
		fmt.Fprintf(&b, "  caam limits %s --profile %s --source %s\n", provider, name, alt.Namespace)
	}
	fmt.Fprintf(&b, "  caam limits %s --profile %s --source %s", provider, name, r.Selected.Namespace)
	return fmt.Errorf("%s", b.String())
}

// report renders the resolution for the limits JSON output.
func (r *resolvedCredential) report() *usage.CredentialSource {
	if r == nil {
		return nil
	}
	out := &usage.CredentialSource{
		Namespace: r.Selected.Namespace,
		Path:      r.Selected.Path,
		State:     r.Selected.State,
		Explicit:  r.Explicit,
	}
	healthier := map[string]bool{}
	for _, alt := range r.Healthier {
		healthier[alt.Namespace] = true
	}
	for _, alt := range r.Alternatives {
		out.Alternatives = append(out.Alternatives, usage.CredentialAlternative{
			Namespace: alt.Namespace,
			Path:      alt.Path,
			State:     alt.State,
			Healthier: healthier[alt.Namespace],
		})
	}
	return out
}

// describe is the human-facing one-liner (plus alternatives) printed above the
// limits table so the operator can see which copy was read.
func (r *resolvedCredential) describe(provider, name string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s: read from the %s namespace (%s, %s)",
		provider, name, r.Selected.Namespace, r.Selected.State, r.Selected.Path)
	for _, alt := range r.Alternatives {
		fmt.Fprintf(&b, "\n  also present in %s: %s (%s)", alt.Namespace, alt.State, alt.Path)
	}
	if len(r.Alternatives) > 0 && !r.Explicit {
		fmt.Fprintf(&b, "\n  credentials are never copied between namespaces; select one with --source")
	}
	return b.String()
}

// namespaceProfileNames lists the profile names a namespace holds for a
// provider. It backs `--source <ns>` without `--profile`.
func namespaceProfileNames(l credentialLookup, namespace, provider string) ([]string, error) {
	var names []string
	switch namespace {
	case credNamespaceVault:
		entries, err := os.ReadDir(filepath.Join(l.VaultDir, provider))
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		for _, e := range entries {
			// caam's own bookkeeping directories (_original, auto-backups) are
			// not accounts anyone routes to, so they are not listed as ones.
			if e.IsDir() && !authfile.IsSystemProfile(e.Name()) {
				names = append(names, e.Name())
			}
		}
	case credNamespaceIsolated:
		if l.Profiles == nil {
			return nil, nil
		}
		profs, err := l.Profiles.List(provider)
		if err != nil {
			return nil, err
		}
		for _, p := range profs {
			names = append(names, p.Name)
		}
	case credNamespaceShallow:
		if l.Shallow == nil {
			return nil, nil
		}
		profs, err := l.Shallow.List()
		if err != nil {
			return nil, err
		}
		for _, p := range profs {
			if resolved, err := l.Shallow.ResolveProvider(p.Name); err == nil && shallow.NormalizeProvider(resolved) == provider {
				names = append(names, p.Name)
			}
		}
	}
	sort.Strings(names)
	return names, nil
}
