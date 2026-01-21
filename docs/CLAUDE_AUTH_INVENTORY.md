# Claude Auth/Session Assumptions Inventory

**Generated:** 2026-01-21
**Bead:** caam-nqjf
**Purpose:** Audit Claude-specific features for broken assumptions about auth/session data

---

## Executive Summary

| Classification | Count | Action |
|----------------|-------|--------|
| Correct | 4 | No changes needed |
| Fixable | 3 | Graceful degradation / warnings |
| Remove/Disable | 3 | Hard-disable to avoid misleading users |

**Key Finding:** Recent Claude Code auth files no longer include `email` or `accountId` fields, and access tokens are opaque (not JWTs). Features relying on these assumptions should be disabled or updated.

---

## Feature Inventory

### CLAUDE-001: Identity Extraction from Credentials

**File:** `internal/identity/claude.go:12-41`
**Function:** `ExtractFromClaudeCredentials`
**Classification:** Fixable
**Status:** ✅ RESOLVED (2026-01-21, caam-u8wz)

**Assumptions:**
- `claudeAiOauth` contains `accountId`, `email`, `subscriptionType`, `expiresAt`

**Reality:**
- `expiresAt` and `subscriptionType` ARE present
- `accountId` and `email` are NO LONGER present in current versions

**Action:** Return empty strings for missing fields; update TUI to handle empty identity gracefully.

**Resolution:**
- Function already returns empty strings for missing fields (graceful degradation)
- Added detailed documentation warning that email/accountId are no longer present
- Added test cases for current Claude auth format without email/accountId
- Added test cases for opaque tokens

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
| CLAUDE-001 | identity/claude | Handle missing email/accountId gracefully |
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
| CLAUDE-001 | `identity/claude_test.go` | N/A | Update README |
| CLAUDE-002 | `discovery/identity_test.go` | N/A | N/A |
| CLAUDE-003 | `provider/claude/claude_test.go` | Backup/restore | N/A |
| CLAUDE-004 | `provider/claude/claude_test.go` | Status command | N/A |
| CLAUDE-005 | `health/expiry_test.go` | Health display | N/A |
| CLAUDE-006 | `refresh/claude_test.go` | N/A | Remove from docs |
| CLAUDE-007 | `usage/claude_test.go` | Monitor command | Mark experimental |
| CLAUDE-008 | `pricing/claude_test.go` | Cost display | Add API-key note |
| CLAUDE-009 | `logs/claude_test.go` | N/A | Verify |
| CLAUDE-010 | `handoff/handler_test.go` | Login flow | N/A |
