# Claude Auth/Session Assumptions Inventory

**Generated:** 2026-01-21
**Identity handling updated:** 2026-11-02, issue #132
**Bead:** caam-nqjf
**Purpose:** Audit Claude-specific features for broken assumptions about auth/session data

---

## Executive Summary

The classification counts below record the original January audit. The
CLAUDE-001 section describes the current account and organization ownership
contract; the other entries retain their historical findings.

| Classification | Count | Action |
|----------------|-------|--------|
| Correct | 4 | No changes needed |
| Fixable | 3 | Graceful degradation / warnings |
| Remove/Disable | 3 | Hard-disable to avoid misleading users |

**Key finding:** Claude OAuth access tokens are opaque, and current credential
files may omit account identifiers. A separate `.claude.json` can contain a
stale account label. Local metadata and email alone cannot prove token ownership.

---

## Feature Inventory

### CLAUDE-001: Identity Extraction from Credentials

**Files:** `internal/identity/claude.go`, `internal/authfile/claude_identity.go`,
`internal/authfile/switch.go`, `internal/keepalive/keepalive.go`,
`internal/provider/claude/claude.go`, `internal/profile/profile.go`
**Functions:** `ExtractFromClaudeCredentials`, `VerifyClaudeCredentials`,
`ClaudeSameAccount`
**Status:** Token-bound account and organization identification implemented for
[issue #132](https://github.com/Dicklesworthstone/coding_agent_account_manager/issues/132).

**Ownership model:** A Claude account UUID and organization UUID together
identify the selected account context. Personal and Team organizations can
share an account UUID and email, and organization names can also collide.
Neither email nor a name is a safe matching key. `.claude.json` is independently
mutable state, so even a complete `oauthAccount` block is only a local hint
until checked against the selected credential.

At backup, activation, or keepalive boundaries, a bounded HTTPS request to
`https://api.anthropic.com/api/oauth/profile` can identify the captured access
token. The verifier requires both provider identifiers, rejects redirects,
limits the response body and request duration, and does not include tokens or
provider response bodies in diagnostics. Expired access tokens are not sent
for verification. The read-only identity extractor and profile listing do
not initiate requests.

The result is stored in a `claude_identity` envelope containing the identity,
a fingerprint of the access/refresh token pair, and a verification timestamp.
Vault profiles use `meta.json`; native credentials can use the private sibling
`.caam-identity.json`. These files contain no duplicate token values. A binding
is accepted only for its captured token pair. A stale or malformed binding
does not revive the paired `.claude.json` label as trusted identity. The cache
survives process restarts and remains useful for ownership after token expiry;
it is not evidence of current login usability or independent verification of
the opaque refresh token.

| Evidence | Permitted use |
|----------|---------------|
| Exact saved/live credentials | Recognize the matching snapshot offline; identity can remain unknown |
| Matching token-bound account and organization | Match different verified generations and preserve a newer same-account credential |
| Email or unverified `.claude.json` alone | Display a marked legacy hint; never authorize a cross-generation match or overwrite |
| Provider unavailable or access token expired, no existing binding | Preserve the grant with unverified labels omitted; refuse activation until ownership can be verified |
| Tokens replaced in a profile with an existing binding | Refuse activation unless verification confirms the recorded account and organization |
| Conflicting recorded account or organization UUIDs | Refuse selection, repair and copying; never resolve the conflict with a label or first match |

Successful verification corrects stale `oauthAccount` labels in a new backup
or in the settings prepared for activation. Backup leaves its native source
unchanged. A named saved account cannot silently become another organization;
unsaved credentials can instead be preserved under a separate automatic backup.
Keepalive synchronizes strictly newer credentials only into a verified matching
user snapshot, and binds the new generation before it can be treated as that
profile's identity. `_original` and automatic recovery snapshots are immutable
after creation, including during activation. `verify identity --copy-to` can
publish a verified modern OAuth grant under a new user label while leaving all
original evidence intact. Preserving or verifying a grant does not assert its
current usability.

Isolated profile imports carry a matching binding with the exact captured
credentials into a private `.caam-identity.json`, including after access-token
expiry, without contacting the provider. A saved `meta.json` binding that no
longer matches blocks import; a stale native cache contributes no identity.
Both metadata records are checked and guarded, so a valid primary proof cannot
hide contradictory secondary evidence.
The imported `.claude.json` account block is corrected or omitted while unrelated
session state is retained. Source credentials, keychain authority when selected,
and the metadata used for the decision are rechecked during the recoverable
credential/cache/state batch and before the prepared profile directory is
published. Source changes leave a previously installed profile in place.

Execution and isolated profile identity loading share one directory selector:
an installed XDG `xdg_config/claude-code` authentication source is authoritative,
even when its token is expired or malformed. Legacy-only profiles use
`home/.claude`. A newer credential in an ignored directory cannot change the
reported organization, and a cached `profile.json` identity cannot replace
missing or unreadable current Claude credentials. These lookups stay offline.

`caam ls` displays organization names and stable UUIDs. Legacy local labels
are marked `[unverified]`; JSON exposes `organization_id` and a true
`identity_verified` field for matching provider bindings. The coordinator
carries stable account and organization identifiers when known. The browser
agent refuses organization-specific recovery requests because its email-based
configuration cannot reliably choose a Claude organization.

Participating CAAM processes serialize managed mutations with a private
per-vault process lock. Native Claude clients do not acquire that lock, so
source and destination checks still detect changes during capture and publication.
The existing switch publication path recovers from reported errors, but it
is not a crash-atomic transaction across all files and the native keychain.
CAAM cannot change credentials already held in a running pane or prevent a
native session from rewriting shared files after the switch. Use isolated or
shallow profiles for concurrent accounts. The report's suspected concurrent
session race is distinct from these deterministic simulated-writer tests.

**Coverage:** Synthetic tests exercise Personal and Team organizations sharing
an email, same-name organizations, stale settings, provider outages and expired
tokens, substituted access tokens, concurrent source/destination changes,
automatic backups, activation, keepalive synchronization, browser refusal,
credential-free diagnostics, and restart persistence.

---

### CLAUDE-002: Auth File Identity Extraction

**File:** `internal/discovery/identity.go:31-68`
**Function:** `extractClaudeIdentity`
**Classification:** Remove/Disable
**Status:** ✅ RESOLVED (2026-01-21, caam-u8wz)

**Assumptions:**
- Auth files contain `email`, `user.email`, or `accountId`
- Access tokens are JWTs that can be decoded for email claims

**Reality:**
- Auth files do NOT contain email or accountId
- Claude tokens are OPAQUE, not JWTs - cannot be decoded

**Action:** Remove JWT decoding attempt; return empty identity with `success=true` to avoid errors.

**Resolution:**
- Removed JWT decoding from `extractClaudeIdentity()` - Claude tokens are opaque
- Added `claudeAiOauth` field checking for email/accountId (backwards compatibility)
- Function returns empty identity with `valid=true` when no identity available
- Added comprehensive test coverage for opaque token scenarios

---

### CLAUDE-003: Auth File Paths

**File:** `internal/provider/claude/claude.go:88-113`
**Function:** `AuthFiles`
**Classification:** Correct

**Assumptions:**
- Primary: `~/.claude/.credentials.json`
- Optional: `~/.claude.json`, `~/.config/claude-code/auth.json`, `~/.claude/settings.json`

**Reality:** File paths are correct.

**Action:** No changes needed.

---

### CLAUDE-004: Auth Detection & Validation

**File:** `internal/provider/claude/claude.go:388-530, 706-903`
**Functions:** `DetectExistingAuth`, `ValidateToken`
**Classification:** Correct

**Assumptions:**
- `claudeAiOauth` with `accessToken`/`refreshToken` indicates valid auth
- `expiresAt` provides token expiry

**Reality:** Structure is correct; expiry parsing works.

**Action:** No changes needed.

---

### CLAUDE-005: Token Expiry Parsing

**File:** `internal/health/expiry.go:37-192`
**Functions:** `ParseClaudeExpiry`, `parseClaudeCredentialsFile`
**Classification:** Correct
**Status:** ✅ VERIFIED (2026-01-21, caam-g8mv)

**Assumptions:**
- `claudeAiOauth.expiresAt` is Unix milliseconds
- `refreshToken` indicates refresh capability

**Reality:** Both fields ARE present and correctly formatted.

**Action:** No changes needed.

**Verification:**
- Reviewed `parseClaudeCredentialsFile()` implementation
- Correctly parses `expiresAt` as Unix milliseconds via `time.UnixMilli()`
- Correctly detects `refreshToken` presence
- Comprehensive test coverage in `expiry_test.go`

---

### CLAUDE-006: Token Refresh API

**File:** `internal/refresh/claude.go:32-157`
**Functions:** `RefreshClaudeToken`, `UpdateClaudeAuth`
**Classification:** Remove/Disable
**Status:** ✅ DISABLED (2026-01-21, caam-g8mv)

**Assumptions:**
- OAuth refresh endpoint exists at `api.anthropic.com/oauth/token`
- Standard OAuth2 `refresh_token` grant works
- Client ID `claude-code-cli` is valid

**Reality:**
- Code comments say: *"TODO: Reverse engineer or find official docs"*
- Endpoint is **SPECULATIVE** - no public documentation
- Refresh likely handled internally by Claude Code

**Action:** Disable automatic token refresh; add warning that refresh is unsupported; rely on `/login` for re-authentication.

**Resolution:**
- `refreshClaude()` in `internal/refresh/refresh.go` now returns `UnsupportedError` immediately
- Added `ClaudeRefreshDisabled = true` sentinel in `internal/refresh/claude.go`
- Updated documentation to clarify Claude Code handles refresh internally
- Users should use `/login` command to re-authenticate when tokens expire

---

### CLAUDE-007: Usage API

**File:** `internal/usage/claude.go:44-141`
**Function:** `ClaudeFetcher.Fetch`
**Classification:** Fixable

**Assumptions:**
- Usage API at `api.anthropic.com/api/oauth/usage`
- Requires `anthropic-beta: oauth-2025-04-20` header
- Returns `five_hour`, `seven_day`, `opus` utilization windows

**Reality:**
- API is **UNDOCUMENTED** - beta header suggests internal/experimental
- May work but could break without notice

**Action:** Add graceful degradation; mark as experimental in UI; add config option to disable.

---

### CLAUDE-008: Token Pricing

**File:** `internal/pricing/claude.go:4-13`
**Constant:** `ClaudePricing`
**Classification:** Fixable

**Assumptions:**
- Pricing per-1M tokens for opus/sonnet/haiku

**Reality:**
- Pricing is correct for **API key mode**
- Does NOT apply to Claude Max subscription (all-you-can-eat)

**Action:** Add warning that pricing applies to API mode only; consider hiding for Max subscription profiles.

---

### CLAUDE-009: Log Parsing

**File:** `internal/logs/claude.go:12-188`
**Type:** `ClaudeScanner`
**Classification:** Remove/Disable

**Assumptions:**
- Logs at `~/.local/share/claude/logs/`
- JSONL format with specific fields

**Reality:**
- Log location/format may have changed in newer versions
- Needs verification against current Claude Code

**Action:** Verify log location and format; disable if incorrect; add graceful degradation.

---

### CLAUDE-010: Login Handler

**File:** `internal/handoff/claude.go:12-153`
**Type:** `ClaudeLoginHandler`
**Classification:** Correct

**Assumptions:**
- `/login` command triggers authentication
- Output patterns indicate login state

**Reality:** `/login` IS the documented auth method; patterns are reasonable.

**Action:** No changes needed.

---

## Priority Action Items

### Priority 1: Disable Broken Features (High Risk)

| Feature ID | Module | Action |
|------------|--------|--------|
| CLAUDE-002 | discovery/identity | Disable JWT decoding for Claude tokens |
| CLAUDE-006 | refresh/claude | Disable automatic token refresh |

**Rationale:** These features cannot work with current Claude Code and actively mislead users.

### Priority 2: Graceful Degradation (Medium Risk)

| Feature ID | Module | Action |
|------------|--------|--------|
| CLAUDE-001 | identity/claude, authfile | Require token-bound account and organization evidence; preserve unknown grants without asserting labels |
| CLAUDE-007 | usage/claude | Add fallback when API fails |
| CLAUDE-008 | pricing/claude | Add "API key only" warning |

### Priority 3: Verification Needed (Low-Medium Risk)

| Feature ID | Module | Action |
|------------|--------|--------|
| CLAUDE-009 | logs/claude | Verify log location/format against current version |

---

## Recommendations

1. **Immediate:** Disable `RefreshClaudeToken` and remove JWT decoding from identity extraction
2. **Short-term:** Update TUI to display "identity unavailable" instead of empty fields
3. **Medium-term:** Add experimental feature flags for undocumented APIs
4. **Documentation:** Update user-facing docs to clarify which features work with Claude Max vs API key mode

---

## Test Matrix

| Feature | Unit Tests | E2E Tests | Docs |
|---------|-----------|-----------|------|
| CLAUDE-001 | `identity/claude_test.go`, `authfile/claude_rotation_test.go`, `keepalive/keepalive_test.go` | `cmd/caam/cmd/activate_test.go`, `cmd/caam/cmd/ls_type_test.go` | README account and organization identity |
| CLAUDE-002 | `discovery/identity_test.go` | N/A | N/A |
| CLAUDE-003 | `provider/claude/claude_test.go` | Backup/restore | N/A |
| CLAUDE-004 | `provider/claude/claude_test.go` | Status command | N/A |
| CLAUDE-005 | `health/expiry_test.go` | Health display | N/A |
| CLAUDE-006 | `refresh/claude_test.go` | N/A | Remove from docs |
| CLAUDE-007 | `usage/claude_test.go` | Monitor command | Mark experimental |
| CLAUDE-008 | `pricing/claude_test.go` | Cost display | Add API-key note |
| CLAUDE-009 | `logs/claude_test.go` | N/A | Verify |
| CLAUDE-010 | `handoff/handler_test.go` | Login flow | N/A |
