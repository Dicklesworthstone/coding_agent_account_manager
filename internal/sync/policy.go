package sync

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Dicklesworthstone/coding_agent_account_manager/internal/config"
)

// SyncMode describes how a profile's credential data may move between
// machines (issue #66).
//
//   - ModeReplicate: current behavior — the freshest credential payload is
//     copied bidirectionally between machines.
//   - ModeHostLocal: the credential payload NEVER crosses machines; only
//     non-secret profile metadata (meta.json) is synchronized, so each
//     machine keeps its own independent OAuth grant for the same logical
//     account.
//
// A third mode from the #66 proposal, exclusive `handoff`, is deliberately
// deferred; host-local plus explicit local re-login covers the safety
// boundary without the lease machinery.
type SyncMode string

const (
	// ModeReplicate copies the freshest credential payload between machines.
	ModeReplicate SyncMode = "replicate"

	// ModeHostLocal excludes the credential payload from sync entirely;
	// only profile metadata replicates.
	ModeHostLocal SyncMode = "host-local"
)

// ParseSyncMode parses a user-supplied mode string. The second return is
// false for anything that is not an exact, supported mode name.
func ParseSyncMode(s string) (SyncMode, bool) {
	switch SyncMode(strings.TrimSpace(strings.ToLower(s))) {
	case ModeReplicate:
		return ModeReplicate, true
	case ModeHostLocal:
		return ModeHostLocal, true
	default:
		return "", false
	}
}

// rotatingOAuthProviders are providers whose subscription OAuth flow uses a
// rotating refresh-token family: every refresh consumes the current
// refresh_token, and presenting an already-consumed one can trigger
// server-side reuse detection that revokes the whole family.
//
// Evidence from this repository's own history:
//   - codex: issue #19 — restoring a stale vault snapshot revoked a real
//     GPT Pro account (reuse detection → family revocation).
//   - claude: issue #73 — Claude Code rotates the refresh token on every
//     refresh; vault matching had to move to rotation-stable identity keys
//     because token bytes churn continuously.
//
// Replicating such a family onto two concurrently active machines means
// both provider CLIs refresh independently, guaranteeing that one side
// eventually holds (and can re-propagate) a consumed generation.
var rotatingOAuthProviders = map[string]bool{
	"claude": true,
	"codex":  true,
}

// concurrencySafeProviders are providers whose synced credential files are
// safe (or at least not family-revoking) to replicate between machines:
// Google OAuth refresh tokens are stable rather than rotating (gemini, agy),
// and the remaining tools store static or opaque credentials whose observed
// behavior does not include reuse-detection revocation. They keep today's
// replicate default.
var concurrencySafeProviders = map[string]bool{
	"gemini":   true,
	"agy":      true,
	"grok":     true,
	"opencode": true,
	"cursor":   true,
}

// syncedProviders is the fixed set of provider vault directories the sync
// engine walks. Kept in one place so the walker and the policy status
// display cannot drift apart.
var syncedProviders = []string{"claude", "codex", "gemini", "opencode", "cursor"}

// SyncedProviders returns the providers whose vault directories participate
// in multi-machine sync, in stable order.
func SyncedProviders() []string {
	out := make([]string, len(syncedProviders))
	copy(out, syncedProviders)
	return out
}

// ProviderRotatesCredentials reports whether a provider's OAuth credential
// uses a rotating refresh-token family (see rotatingOAuthProviders).
func ProviderRotatesCredentials(provider string) bool {
	return rotatingOAuthProviders[strings.ToLower(strings.TrimSpace(provider))]
}

// DefaultModeForProvider returns the capability-derived default sync mode
// for a provider:
//
//   - rotating OAuth families  → host-local (replicating them can revoke accounts)
//   - known concurrency-safe   → replicate (today's behavior)
//   - unknown providers        → host-local (fail closed, per #66)
func DefaultModeForProvider(provider string) SyncMode {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case rotatingOAuthProviders[p]:
		return ModeHostLocal
	case concurrencySafeProviders[p]:
		return ModeReplicate
	default:
		return ModeHostLocal
	}
}

// PolicyResolver resolves the effective sync mode for a provider/profile,
// layering user configuration over provider capability defaults.
//
// Precedence (highest wins):
//  1. profile override  (config sync_policy.profiles["provider/profile"])
//  2. provider override (config sync_policy.providers["provider"])
//  3. provider capability default (DefaultModeForProvider)
type PolicyResolver struct {
	providerOverrides map[string]SyncMode
	profileOverrides  map[string]SyncMode
	invalid           []string // config entries with unparseable modes, for diagnostics
}

// NewPolicyResolver builds a resolver from raw configuration maps
// (provider → mode, "provider/profile" → mode). Entries whose mode string
// does not parse are ignored for resolution — the capability default keeps
// applying, which fails closed for rotating providers — and are reported
// via InvalidEntries so the CLI can warn.
func NewPolicyResolver(providerOverrides, profileOverrides map[string]string) *PolicyResolver {
	r := &PolicyResolver{
		providerOverrides: make(map[string]SyncMode),
		profileOverrides:  make(map[string]SyncMode),
	}
	for prov, raw := range providerOverrides {
		mode, ok := ParseSyncMode(raw)
		if !ok {
			r.invalid = append(r.invalid, fmt.Sprintf("sync_policy.providers[%q] = %q", prov, raw))
			continue
		}
		r.providerOverrides[strings.ToLower(strings.TrimSpace(prov))] = mode
	}
	for key, raw := range profileOverrides {
		mode, ok := ParseSyncMode(raw)
		if !ok {
			r.invalid = append(r.invalid, fmt.Sprintf("sync_policy.profiles[%q] = %q", key, raw))
			continue
		}
		r.profileOverrides[strings.ToLower(strings.TrimSpace(key))] = mode
	}
	sort.Strings(r.invalid)
	return r
}

// LoadPolicyResolver builds a resolver from the on-disk caam config.
// Configuration load failures degrade to capability defaults only — which
// fail closed for rotating providers — rather than blocking sync.
func LoadPolicyResolver() *PolicyResolver {
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return NewPolicyResolver(nil, nil)
	}
	return NewPolicyResolver(cfg.SyncPolicy.Providers, cfg.SyncPolicy.Profiles)
}

// ModeFor resolves the effective sync mode for a provider/profile pair.
func (r *PolicyResolver) ModeFor(provider, profile string) SyncMode {
	if r == nil {
		return DefaultModeForProvider(provider)
	}
	prov := strings.ToLower(strings.TrimSpace(provider))
	key := prov + "/" + strings.TrimSpace(profile)
	if mode, ok := r.profileOverrides[strings.ToLower(key)]; ok {
		return mode
	}
	if mode, ok := r.providerOverrides[prov]; ok {
		return mode
	}
	return DefaultModeForProvider(provider)
}

// Explain returns the provider-level mode (ignoring profile overrides) and
// a short human-readable reason, for status displays.
func (r *PolicyResolver) Explain(provider string) (SyncMode, string) {
	prov := strings.ToLower(strings.TrimSpace(provider))
	if r != nil {
		if mode, ok := r.providerOverrides[prov]; ok {
			return mode, "configured override"
		}
	}
	mode := DefaultModeForProvider(provider)
	switch {
	case rotatingOAuthProviders[prov]:
		return mode, "rotating OAuth default"
	case concurrencySafeProviders[prov]:
		return mode, "default"
	default:
		return mode, "unknown provider (fail closed)"
	}
}

// InvalidEntries lists configuration entries whose mode string did not
// parse and were therefore ignored.
func (r *PolicyResolver) InvalidEntries() []string {
	if r == nil {
		return nil
	}
	out := make([]string, len(r.invalid))
	copy(out, r.invalid)
	return out
}

// metadataSyncAllowlist names the profile files that are safe, non-secret
// metadata and may replicate for host-local profiles. Everything NOT on
// this list is treated as credential payload and never crosses machines
// under host-local — fail closed for filenames we have never seen.
var metadataSyncAllowlist = map[string]bool{
	"meta.json": true,
}

// isMetadataFile reports whether a profile file (by base name) is
// allowlisted non-secret metadata.
func isMetadataFile(name string) bool {
	return metadataSyncAllowlist[name]
}

// filterMetadataFiles keeps only allowlisted metadata files from a profile
// file map keyed by base filename.
func filterMetadataFiles(files map[string][]byte) map[string][]byte {
	out := make(map[string][]byte, 1)
	for name, data := range files {
		if isMetadataFile(name) {
			out[name] = data
		}
	}
	return out
}

// Notes attached to operations adjusted by policy. Constants so tests and
// the CLI stay in lockstep.
const (
	noteHostLocalMetadataOnly = "host-local: metadata only — credential payload never syncs"
	noteHostLocalDiverged     = "host-local: credentials differ across machines (independent grants); payload not merged"
)

// rotatingReplicateWarning is attached to replicate push/pull operations for
// rotating-OAuth providers when both machines already hold the credential:
// converging the two copies is exactly the #19 revocation hazard.
func rotatingReplicateWarning(provider string) string {
	return fmt.Sprintf(
		"WARNING: %s uses a rotating refresh-token family; replicating it between concurrently active machines can permanently revoke the account (see issues #19/#66). Configured sync_policy override forces replicate.",
		provider,
	)
}

// applyHostLocalDecision converts existence + freshness facts into the final
// operation for a host-local profile. Pure — unit tested directly.
//
// System profiles (leading underscore) are machine-local safety artifacts;
// they neither replicate payload nor propagate metadata stubs.
func applyHostLocalDecision(op *SyncOperation, localExists, remoteExists bool, localFresh, remoteFresh *TokenFreshness, systemProfile bool) *SyncOperation {
	op.Mode = ModeHostLocal
	op.PayloadExcluded = true
	op.LocalFreshness = localFresh
	op.RemoteFreshness = remoteFresh

	switch {
	case systemProfile:
		op.Direction = SyncSkip
	case !localExists && !remoteExists:
		op.Direction = SyncSkip
	case localExists && !remoteExists:
		op.Direction = SyncPush
		op.Note = noteHostLocalMetadataOnly
	case !localExists && remoteExists:
		op.Direction = SyncPull
		op.Note = noteHostLocalMetadataOnly
	default: // both exist: payloads stay put; surface divergence when provable
		op.Direction = SyncSkip
		if localFresh != nil && remoteFresh != nil &&
			(CompareFreshness(localFresh, remoteFresh) || CompareFreshness(remoteFresh, localFresh)) {
			op.Note = noteHostLocalDiverged
		}
	}
	return op
}

// HistoryAction is the action string recorded in sync history for this
// operation: push/pull, suffixed with "-meta" when the payload was excluded
// by policy so the log shows metadata-only transfers distinctly.
func (op *SyncOperation) HistoryAction() string {
	if op.PayloadExcluded && op.Direction != SyncSkip {
		return string(op.Direction) + "-meta"
	}
	return string(op.Direction)
}
