# CAAM Feature Plan: Q1 2025 "No-Brainer" Features

## Executive Summary

This document outlines essential missing features identified through comprehensive code review. These features are "no-brainers" in the sense that their absence creates friction for users, and their implementation provides clear value with reasonable effort.

The features are organized into five EPICs representing distinct user-facing improvements:

1. **Profile Metadata & Organization** - Help users manage many profiles
2. **First-Run & Import Experience** - Reduce onboarding friction
3. **Resilience Configuration** - Make rate limit handling customizable
4. **Proactive User Communication** - Keep users informed without active checking
5. **Token Reliability** - Ensure auth actually works, not just files exist

---

## EPIC 1: Profile Metadata & Organization

### Problem Statement

Users with multiple profiles (common for consultants, team leads, or anyone with work/personal separation) struggle to remember what each profile is for. The current `AccountLabel` field is intended for email addresses, not purpose descriptions.

Additionally, as profile counts grow, users need ways to categorize and filter profiles beyond simple listing.

### Features

#### 1.1 Profile Description Field

**What:** Add a `Description` field to the Profile struct for free-form notes.

**Why:**
- `AccountLabel` is semantic for email/account ID
- Users need a place for notes like "Client X project", "Free tier for testing", "Team shared account"
- Description appears in all listing contexts, making profile selection easier

**Design Decisions:**
- Single `Description` string (not separate short/long fields - keep it simple)
- Optional field (omitempty) - no migration needed for existing profiles
- Max ~200 chars recommended but not enforced (user responsibility)
- Displayed in: `caam ls`, `caam status`, `caam profile show`, TUI profile list

**Implementation:**
1. Add `Description string` to Profile struct
2. Add `--description/-d` flag to `caam add` and `caam profile add`
3. Create `caam profile describe <tool> <profile> [description]` command
4. Update `caam ls` to show description (truncated if long)
5. Update TUI profile list to show description
6. Update `caam profile show` to include description

**Testing:**
- Unit tests for Profile serialization with description
- Integration test for describe command
- TUI test for description display

#### 1.2 Profile Tags

**What:** Add tagging system for categorizing profiles.

**Why:**
- Favorites are ordered priority lists - tags are unordered categories
- Users can filter by tag: `caam ls --tag project:acme`
- Tags enable organizational views: by project, team, environment, etc.

**Design Decisions:**
- Simple string tags (not key:value - too complex)
- Stored as `Tags []string` in Profile struct
- Tag names: lowercase, alphanumeric + hyphens, max 32 chars
- Special tags could influence rotation (future enhancement)

**Implementation:**
1. Add `Tags []string` to Profile struct
2. Create `caam tag` command group:
   - `caam tag add <tool>/<profile> <tags...>`
   - `caam tag remove <tool>/<profile> <tags...>`
   - `caam tag list` - show all used tags
3. Add `--tag` filter to `caam ls`
4. Display tags in TUI profile list

**Dependency:** None (can be done in parallel with Description)

#### 1.3 Profile Cloning

**What:** Clone an existing profile to create a new one with similar configuration.

**Why:**
- Setting up profiles with similar config (browser settings, auth mode) is tedious
- Cloning enables quick duplication for new accounts
- Common pattern: clone work profile for new client

**Design Decisions:**
- Default: Copy structure and settings, NOT auth files (new account scenario)
- `--with-auth`: Also copy auth files (same account, different profile scenario)
- Cloned profile starts with description: "Cloned from <source>"
- Browser settings are copied
- Passthrough symlinks are recreated fresh

**Implementation:**
1. Create `caam profile clone <tool> <source> <target>` command
2. Copy profile.yaml with new name and timestamps
3. Create new directory structure (home, xdg_config, provider-specific)
4. Set up passthrough symlinks for new profile
5. Optionally copy auth files with `--with-auth`
6. Add `--description` flag to set custom description

**Dependency:** Profile Description (should exist to set on cloned profile)

---

## EPIC 2: First-Run & Import Experience

### Problem Statement

Every new caam user already has existing auth credentials from using CLI tools directly. The current `caam init` wizard helps with setup but doesn't detect or import these existing credentials. Users must either:
1. Log in again through caam
2. Manually copy auth files

This creates unnecessary friction in the critical first-use experience.

### Features

#### 2.1 Auth Detection

**What:** Detect existing auth files in standard locations for each provider.

**Why:**
- Claude: `~/.claude.json`, `~/.config/claude-code/auth.json`
- Codex: `~/.codex/auth.json`
- Gemini: `~/.gemini/settings.json`, `~/.gemini/oauth_credentials.json`
- Detection enables import and status reporting

**Design Decisions:**
- Add `DetectExistingAuth()` method to each Provider interface
- Return metadata: location, last modified, file size
- Non-destructive: never modify original files
- Handle missing files gracefully

**Implementation:**
1. Add `DetectExistingAuth() (*AuthDetection, error)` to Provider interface
2. Implement for Claude, Codex, Gemini providers
3. Create `caam auth detect` command to show detected auth
4. Return structured data for use by import features

#### 2.2 Auth Import Command

**What:** Import existing auth into a new caam profile.

**Why:**
- Avoids re-authentication for existing credentials
- Creates proper isolated profile from detected auth
- Standalone command for users who skip init wizard

**Design Decisions:**
- `caam auth import <tool> [--name <profile-name>]`
- Default profile name: "imported" or "default"
- Copies auth files into new profile's isolated directories
- Creates proper profile.yaml with metadata
- Warns if profile name already exists

**Implementation:**
1. Create `caam auth import <tool>` command
2. Call provider's DetectExistingAuth()
3. Create new profile with specified or default name
4. Copy auth files to profile's directories
5. Set up passthrough symlinks
6. Add `--force` flag to overwrite existing profile

**Dependency:** Auth Detection (needs detection to know what to import)

#### 2.3 Init Wizard Enhancement

**What:** Integrate auth detection and import into the init wizard.

**Why:**
- First-run experience should be seamless
- Wizard already exists (`caam init`) - enhance rather than replace
- Users should see: "We detected existing Claude auth. Import it?"

**Design Decisions:**
- Add detection step after provider selection
- Show what was detected with modification times
- Offer to import with suggested profile name
- Allow skipping import
- Multiple providers can be imported

**Implementation:**
1. Add auth detection step to init wizard flow
2. Display detected auth with timestamps
3. Prompt for import confirmation
4. Allow custom profile naming
5. Continue with normal wizard flow after import

**Dependency:** Auth Import Command (uses same underlying logic)

---

## EPIC 3: Resilience Configuration

### Problem Statement

Rate limit handling in `caam wrap` uses hardcoded retry logic. Different providers have different rate limit behaviors, and users may want to customize retry behavior for their specific use cases.

Additionally, there's no automated backup mechanism - users must remember to run `caam backup` manually.

### Features

#### 3.1 Retry/Backoff Configuration

**What:** Make rate limit retry behavior configurable.

**Why:**
- Different providers have different rate limit windows
- Power users need control over retry behavior
- Some users want aggressive retries, others prefer fail-fast

**Design Decisions:**
- Config schema with sensible defaults:
  ```yaml
  wrap:
    max_retries: 3           # Maximum retry attempts (0 = no retry)
    initial_delay: 30s       # First retry delay
    max_delay: 5m            # Cap on delay growth
    backoff_multiplier: 2.0  # Exponential backoff factor
    jitter: true             # Add randomization to delays
  ```
- Per-provider overrides:
  ```yaml
  wrap:
    providers:
      claude:
        max_retries: 5       # Claude is more lenient
      codex:
        initial_delay: 60s   # OpenAI needs longer waits
  ```
- Respect `Retry-After` header when present (always)

**Implementation:**
1. Add WrapConfig struct to config package
2. Add per-provider WrapConfig overrides
3. Update wrap package to read config
4. Implement exponential backoff with jitter
5. Parse and respect Retry-After headers
6. Document configuration in README

**Testing:**
- Unit tests for backoff calculation
- Integration tests for config loading
- Test Retry-After header handling

#### 3.2 Auto-Backup Scheduling

**What:** Automatic periodic backups of the vault.

**Why:**
- Data protection without manual intervention
- Backup command exists but users forget to run it
- Recovery from accidental deletion or corruption

**Design Decisions:**
- Config schema:
  ```yaml
  backup:
    enabled: false           # Opt-in (don't surprise users)
    interval: 7d             # How often to backup
    keep_last: 5             # Number of backups to retain
    location: ~/.caam-backups  # Where to store backups
  ```
- Implemented in daemon (runs in background)
- Uses existing backup logic
- Rotates old backups automatically

**Implementation:**
1. Add BackupConfig struct to config package
2. Add backup scheduling to daemon
3. Implement backup rotation (delete oldest when over limit)
4. Add backup status to `caam daemon status`
5. Add `caam backup --auto-status` to show schedule

**Dependency:** Daemon must be running (already exists)

---

## EPIC 4: Proactive User Communication

### Problem Statement

The daemon runs in background doing token refresh and health checks, but has no way to alert users when something needs attention. Users must actively run `caam status` or `caam doctor` to discover issues.

### Features

#### 4.1 Desktop Notifications

**What:** System notifications for important events.

**Why:**
- Token expiring soon → user can refresh before it's critical
- Token expired → user knows to re-authenticate
- Rate limit hit → user knows why wrap paused
- Background operations → user knows system is working

**Design Decisions:**
- Use `beeep` library for cross-platform support (macOS, Linux, Windows)
- Config for notification control:
  ```yaml
  notifications:
    enabled: true
    levels:
      critical: true    # Token expired, auth failure
      warning: true     # Token expiring soon, high penalty
      info: false       # Sync complete, backup complete
  ```
- Respect system DND/Focus modes where possible
- Rate limit notifications themselves (no spam)

**Implementation:**
1. Add notifications package using beeep
2. Add NotificationConfig to config package
3. Integrate with daemon for token expiry checks
4. Integrate with wrap for rate limit events
5. Integrate with sync for completion events
6. Add `caam notify test` command for testing
7. Add notification throttling (no more than 1/minute per type)

**Platform Considerations:**
- macOS: Uses Notification Center
- Linux: Uses libnotify (notify-send)
- Windows: Uses toast notifications

---

## EPIC 5: Token Reliability

### Problem Statement

`caam doctor` checks if auth files exist, but doesn't verify tokens actually work. A user could have:
- Expired OAuth tokens
- Revoked API keys
- Corrupted auth files

And doctor would report "pass" because files exist.

### Features

#### 5.1 Token Validation

**What:** Actually test if tokens work.

**Why:**
- "Files exist" ≠ "Auth works"
- False confidence is worse than no confidence
- Early detection of auth problems saves debugging time

**Design Decisions:**
- Two levels of validation:
  1. **Passive:** Check token expiry timestamps (no network)
  2. **Active:** Make minimal API call (requires network, opt-in)
- Provider-specific validation:
  - API keys: Make minimal API call
  - OAuth: Check refresh token expiry, optionally attempt refresh
- `caam doctor --validate` for active validation
- `caam validate <tool> [profile]` for explicit validation

**Implementation:**
1. Add `ValidateToken(passive bool) (*ValidationResult, error)` to Provider
2. Implement passive validation (check expiry times)
3. Implement active validation (API call) for each provider
4. Add `--validate` flag to doctor
5. Create `caam validate` command
6. Display validation status in TUI health panel
7. Handle validation errors gracefully (network issues, etc.)

**API Call Strategy:**
- Claude: List models endpoint (minimal)
- Codex/OpenAI: List models endpoint
- Gemini: List models endpoint

**Testing:**
- Mock API responses for validation tests
- Test expiry checking logic
- Test error handling for network failures

---

## Dependency Graph

```
                    ┌─────────────────┐
                    │ Profile Struct  │
                    │   Enhancement   │
                    └────────┬────────┘
                             │
              ┌──────────────┼──────────────┐
              ▼              ▼              ▼
     ┌────────────┐  ┌────────────┐  ┌────────────┐
     │ Description│  │    Tags    │  │   Clone    │
     │   Field    │  │            │  │  (needs    │
     │            │  │            │  │description)│
     └────────────┘  └────────────┘  └─────┬──────┘
                                           │
                                           ▼
                                    ┌────────────┐
                                    │ Clone uses │
                                    │ Description│
                                    └────────────┘

     ┌────────────┐
     │   Auth     │
     │ Detection  │
     └─────┬──────┘
           │
           ▼
     ┌────────────┐
     │   Auth     │
     │  Import    │
     └─────┬──────┘
           │
           ▼
     ┌────────────┐
     │   Init     │
     │  Wizard    │
     │Enhancement │
     └────────────┘

     ┌────────────┐     ┌────────────┐
     │  Retry     │     │   Auto     │
     │  Config    │     │  Backup    │
     └────────────┘     └────────────┘
     (independent)      (needs daemon)

     ┌────────────┐
     │  Desktop   │
     │Notifications│
     └────────────┘
     (needs daemon)

     ┌────────────┐
     │   Token    │
     │ Validation │
     └────────────┘
     (independent, integrates with doctor)
```

## Implementation Order Recommendation

### Phase 1: Foundation (Week 1-2)
- Profile Description Field - enables better UX for all other features
- Auth Detection - required for import features
- Retry Config - improves wrap immediately

### Phase 2: Quick Wins (Week 2-3)
- Profile Tags - organizational improvement
- Auth Import Command - standalone value
- Token Validation (passive) - no network required

### Phase 3: Integration (Week 3-4)
- Profile Cloning - uses description
- Init Wizard Enhancement - uses auth import
- Token Validation (active) - opt-in API calls

### Phase 4: Background Features (Week 4-5)
- Desktop Notifications - requires daemon integration
- Auto-Backup - requires daemon integration

---

## Success Metrics

1. **Profile Description:** Users can identify profiles without checking AccountLabel
2. **Import:** New users have working profiles in <2 minutes from first run
3. **Retry Config:** Zero hardcoded retry values in wrap package
4. **Notifications:** Users receive alerts for token expiry before it impacts work
5. **Validation:** Doctor catches invalid tokens, not just missing files

---

## Open Questions

1. **Notification library choice:** beeep vs go-toast vs native calls?
   - Recommendation: beeep (most portable, actively maintained)

2. **Auto-backup encryption:** Should auto-backups be encrypted by default?
   - Recommendation: Yes, using same encryption as manual backups

3. **Tag limits:** Maximum tags per profile?
   - Recommendation: 10 tags, enforced at add time

4. **Validation API costs:** Document that active validation may incur minimal API costs?
   - Recommendation: Yes, warn in help text for --validate flag
