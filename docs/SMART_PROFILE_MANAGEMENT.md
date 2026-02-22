# Smart Profile Management - Design Document

> **Origin**: Inspired by [codex-pool](https://github.com/darvell/codex-pool) by @darvell
> **Goal**: Bring intelligent profile management to caam, preventing auth failures and helping users choose optimal profiles

## Executive Summary

This document outlines enhancements to caam inspired by codex-pool's sophisticated account management. While codex-pool is a **proxy** that load-balances requests across accounts in real-time, caam is a **profile manager** that switches credentials. We're adopting codex-pool's intelligence while respecting caam's different architecture.

**Key Insight**: codex-pool solves "which account should handle THIS request?" while caam solves "which account should I USE for my work session?" The intelligence transfers, but the application differs.

---

## Feature Analysis

### 1. Proactive Token Refresh (Priority: P1)

#### Background
OAuth tokens expire. When they do, CLI tools fail mid-session, disrupting workflow. Users must manually re-authenticate, losing context and momentum.

codex-pool refreshes tokens 5 minutes before expiry:
```go
if a.ExpiresAt.Before(now.Add(5*time.Minute)) {
    return true  // needs refresh
}
```

#### How This Applies to caam
caam can proactively refresh tokens in two scenarios:
1. **On profile activation**: Refresh if expiring soon
2. **Background monitoring**: Optional daemon that watches all profiles

#### Technical Requirements
- Parse token expiry from auth files (provider-specific formats)
- Implement OAuth refresh for each provider:
  - **Claude Code**: ⚠️ **NOT SUPPORTED** — OAuth endpoint undocumented; tokens are opaque. Users must re-authenticate via `/login`. See [CLAUDE_AUTH_INVENTORY.md](CLAUDE_AUTH_INVENTORY.md) (CLAUDE-006).
  - **OpenAI Codex**: JSON body to `auth.openai.com/oauth/token`
  - **Google Gemini**: Form-encoded to `oauth2.googleapis.com/token`
- Atomic credential file updates (already have this pattern!)
- Graceful fallback if refresh fails (notify user, don't break existing auth)

#### User Experience
```bash
$ caam activate claude work
Refreshing token (expires in 4 minutes)... done
Activated profile 'work' for claude

$ caam status
claude: work (token valid for 59 minutes)
codex:  personal (token expired - run 'caam login codex personal')
gemini: main (token valid for 23 hours)
```

#### Considerations
- **Offline handling**: Can't refresh without network - should warn, not fail
- **Refresh failures**: Log and notify, keep old token (might still work)
- **Rate limiting**: Don't spam refresh endpoints - cooldown period

---

### 2. Profile Health Scoring (Priority: P1)

#### Background
codex-pool uses multi-factor scoring to select the best account:
```go
score = headroom * planBonus * creditBonus - penalty
```

Factors include: rate limit capacity, token expiry, plan tier, error penalties.

#### How This Applies to caam
Instead of automatic selection, caam surfaces health information to help users choose. The TUI and CLI show health indicators, and we can suggest the "best" profile.

#### Simplified Health Model (MVP)
Rather than complex scoring, start with status tiers:

| Status | Icon | Meaning |
|--------|------|---------|
| Healthy | 🟢 | Token valid >1hr, no recent errors |
| Warning | 🟡 | Token expiring <1hr OR recent errors |
| Critical | 🔴 | Token expired OR many recent errors |
| Unknown | ⚪ | Can't determine status |

#### Health Factors (v1)
1. **Token expiry** (primary)
   - >1 hour: +1.0
   - 15-60 min: +0.5
   - <15 min: 0
   - Expired: -1.0

2. **Recent errors** (secondary)
   - No errors in 1hr: +0.3
   - 1-2 errors in 1hr: 0
   - 3+ errors in 1hr: -0.5

3. **Plan type** (bonus)
   - Pro/Team: +0.2
   - Enterprise: +0.3
   - Free: 0

#### Penalty Decay
From codex-pool - penalties decay 20% every 5 minutes:
```go
a.Penalty *= 0.8  // every 5 minutes
if a.Penalty < 0.01 {
    a.Penalty = 0
}
```

This prevents permanent blacklisting while discouraging repeated use of problematic profiles.

#### User Experience
```
┌─ Claude Profiles ────────────────────────────────────┐
│ ● work@company.com      🟢 Pro   59m left   [ACTIVE] │
│   personal@gmail.com    🟡 Free  12m left            │
│   test@example.com      🔴 Expired                   │
│                                                       │
│ Recommended: work@company.com (best health score)    │
└───────────────────────────────────────────────────────┘
```

---

### 3. Usage Analytics (Priority: P2)

#### Background
codex-pool tracks usage with BoltDB:
- Per-request detailed logs (for debugging)
- Per-account aggregates (for dashboards)
- Automatic pruning (configurable retention)

#### How This Applies to caam
caam doesn't proxy requests, so we can't track usage automatically. Options:

1. **Passive tracking**: Record profile activations, login events, errors
2. **Active integration**: Hook into CLI tools (complex, invasive)
3. **Manual import**: Let users import usage data from provider dashboards

#### MVP Approach: Activity Tracking
Track what caam controls:
- Profile activations (timestamp, profile, duration)
- Login/refresh events
- Errors encountered
- Profile switches

This gives users visibility into their caam usage patterns.

#### Database Schema (SQLite)
```sql
CREATE TABLE activity_log (
    id INTEGER PRIMARY KEY,
    timestamp DATETIME NOT NULL,
    event_type TEXT NOT NULL,  -- 'activate', 'login', 'refresh', 'error', 'switch'
    provider TEXT NOT NULL,
    profile_name TEXT NOT NULL,
    details TEXT,  -- JSON for flexible data
    duration_seconds INTEGER  -- for session tracking
);

CREATE TABLE profile_stats (
    provider TEXT NOT NULL,
    profile_name TEXT NOT NULL,
    total_activations INTEGER DEFAULT 0,
    total_errors INTEGER DEFAULT 0,
    total_active_seconds INTEGER DEFAULT 0,
    last_activated DATETIME,
    last_error DATETIME,
    PRIMARY KEY (provider, profile_name)
);

CREATE INDEX idx_activity_timestamp ON activity_log(timestamp);
CREATE INDEX idx_activity_provider ON activity_log(provider, profile_name);
```

#### User Experience
```bash
$ caam usage
Profile Usage (last 7 days)
───────────────────────────────────────
claude/work         42 sessions   18.5 hrs
claude/personal     12 sessions    4.2 hrs
codex/main          28 sessions   12.1 hrs
gemini/default       3 sessions    0.5 hrs

$ caam usage --profile claude/work --detailed
Session History for claude/work
───────────────────────────────────────
2025-12-17 09:15  2.3 hrs  (active)
2025-12-16 14:20  1.8 hrs
2025-12-16 09:00  3.1 hrs
...
```

---

### 4. Hot Reload / Runtime Configuration (Priority: P2)

#### Background
codex-pool reloads credentials without restart:
```go
POST /admin/reload → h.pool.replace(newAccounts)
```

#### How This Applies to caam
Two scenarios:
1. **TUI running**: User adds profile via CLI in another terminal
2. **Daemon mode**: Long-running process needs to pick up changes

#### Implementation Options
1. **File watching**: Use fsnotify to watch `~/.caam/profiles/`
2. **Signal handler**: SIGHUP triggers reload
3. **Explicit command**: `caam reload` sends message to running instance

#### MVP Approach
- Add file watching to TUI (fsnotify)
- TUI auto-refreshes profile list when directory changes
- Add `caam profiles reload` command for scripts

#### User Experience
```
# Terminal 1: TUI running
┌─ Claude Profiles ─────────────────────┐
│ ● work@company.com    🟢 Active       │
│   personal@gmail.com  🟡              │
└───────────────────────────────────────┘

# Terminal 2: Add new profile
$ caam backup claude newproject@corp.com
Profile saved.

# Terminal 1: TUI auto-updates (no restart needed!)
┌─ Claude Profiles ─────────────────────┐
│ ● work@company.com       🟢 Active    │
│   personal@gmail.com     🟡           │
│   newproject@corp.com    🟢 NEW       │  ← appeared automatically
└───────────────────────────────────────┘
```

---

### 5. Project-Profile Association (Priority: P3)

#### Background
codex-pool pins conversations to accounts for context continuity:
```go
if conversationID != "" {
    if id, ok := p.convPin[conversationID]; ok {
        return p.getLocked(id)  // return pinned account
    }
}
```

#### How This Applies to caam
Reframed as "project-profile association" - remember which profile a user prefers for each project directory.

#### Use Cases
1. **Multi-client freelancer**: Different clients = different accounts
2. **Work/personal separation**: Work projects use work account
3. **Team projects**: Shared projects use team account

#### Implementation
```json
// ~/.caam/projects.json
{
  "/home/user/work/client-a": {
    "claude": "client-a@work.com",
    "codex": "work-main"
  },
  "/home/user/personal/blog": {
    "claude": "personal@gmail.com"
  }
}
```

When activating a profile, check if CWD has an association:
```bash
$ cd /home/user/work/client-a
$ caam activate claude
Using project-associated profile: client-a@work.com
```

#### User Experience
```bash
# Associate current directory with a profile
$ caam project set claude client-a@work.com
Associated /home/user/work/client-a with claude/client-a@work.com

# List associations
$ caam project list
/home/user/work/client-a
  claude: client-a@work.com
  codex:  work-main

/home/user/personal/blog
  claude: personal@gmail.com

# Auto-activate based on directory
$ cd /home/user/work/client-a
$ caam activate claude  # or just 'caam activate' if only one association
Using project association: claude/client-a@work.com
Activated.
```

---

## Architecture Decisions

### Decision 1: No Proxy Mode (for now)
codex-pool is fundamentally a proxy. We considered adding proxy mode to caam but decided against it:
- **Complexity**: Port management, SSL, process lifecycle
- **Scope creep**: caam is a profile manager, not a proxy
- **Alternative exists**: codex-pool itself can be used alongside caam

**Future consideration**: `caam proxy` could be a separate binary/mode.

### Decision 2: SQLite for Storage
Options considered:
- BoltDB (like codex-pool): Fast, embedded, but limited queries
- SQLite: Mature, queryable, familiar tooling
- JSON files: Simple but slow for large datasets

**Choice**: SQLite - query flexibility outweighs simplicity of BoltDB.

### Decision 3: Status Indicators vs Numeric Scores
Options considered:
- Numeric scores (like codex-pool): 0.0-1.0 scores
- Status tiers: 🟢🟡🔴

**Choice**: Status tiers for v1 - more intuitive for users. Numeric scores can be internal implementation detail.

### Decision 4: Refresh Strategy
Options considered:
- Always refresh on activate
- Refresh only if expiring soon
- Background daemon

**Choice**: Hybrid - refresh if expiring within 10 minutes on activate, optional daemon for background refresh.

---

## Dependency Graph

```
┌─────────────────────────────────────────────────────────────────┐
│                    FOUNDATION LAYER                              │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────┐      ┌──────────────────┐                │
│  │ Health Metadata  │      │  SQLite Setup    │                │
│  │    Storage       │      │   (usage.db)     │                │
│  └────────┬─────────┘      └────────┬─────────┘                │
│           │                         │                           │
└───────────┼─────────────────────────┼───────────────────────────┘
            │                         │
┌───────────┼─────────────────────────┼───────────────────────────┐
│           │     CORE FEATURES       │                           │
├───────────┼─────────────────────────┼───────────────────────────┤
│           ▼                         ▼                           │
│  ┌──────────────────┐      ┌──────────────────┐                │
│  │  Health Scoring  │      │ Activity Tracking │                │
│  │   (P1 - core)    │      │    (P2 - MVP)     │                │
│  └────────┬─────────┘      └────────┬─────────┘                │
│           │                         │                           │
│           ▼                         │                           │
│  ┌──────────────────┐               │                           │
│  │  Token Refresh   │               │                           │
│  │   (P1 - core)    │               │                           │
│  │ (uses health to  │               │                           │
│  │  decide refresh) │               │                           │
│  └────────┬─────────┘               │                           │
│           │                         │                           │
└───────────┼─────────────────────────┼───────────────────────────┘
            │                         │
┌───────────┼─────────────────────────┼───────────────────────────┐
│           │    ENHANCEMENT LAYER    │                           │
├───────────┼─────────────────────────┼───────────────────────────┤
│           ▼                         ▼                           │
│  ┌──────────────────┐      ┌──────────────────┐                │
│  │  Penalty Decay   │      │  Usage Dashboard │                │
│  │      (P2)        │      │      (P2)        │                │
│  └──────────────────┘      └──────────────────┘                │
│                                                                  │
│  ┌──────────────────┐      ┌──────────────────┐                │
│  │   Hot Reload     │      │ Project Context  │                │
│  │      (P2)        │      │      (P3)        │                │
│  └──────────────────┘      └──────────────────┘                │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## Risk Analysis

| Risk | Impact | Mitigation |
|------|--------|------------|
| Token refresh breaks existing auth | High | Never delete working tokens; refresh to temp file first |
| OAuth endpoints change | Medium | Abstract provider-specific logic; easy to update |
| SQLite adds dependency | Low | SQLite is ubiquitous; CGO-free driver available |
| Feature creep | Medium | Strict MVP scoping; defer nice-to-haves |
| User confusion | Medium | Clear documentation; sensible defaults |

---

## Success Metrics

1. **Token refresh**: Zero unexpected auth failures due to expiry
2. **Health indicators**: Users can identify problematic profiles at a glance
3. **Usage tracking**: Users know which profiles they use most
4. **Hot reload**: TUI stays current without manual refresh

---

## Implementation Phases

### Phase 1: Foundation (P1)
- Health metadata storage
- Basic health status calculation
- Token expiry parsing

### Phase 2: Token Refresh (P1)
- Provider-specific OAuth handlers
- Refresh-on-activate logic
- CLI feedback

### Phase 3: Health Display (P1)
- Status indicators in CLI
- Status indicators in TUI
- Profile recommendations

### Phase 4: Activity Tracking (P2)
- SQLite setup
- Event recording
- Basic queries

### Phase 5: Enhancements (P2-P3)
- Usage dashboard
- Hot reload
- Penalty decay
- Project associations

---

## Appendix: Provider OAuth Details

### Claude Code
- Auth files: `~/.claude/.credentials.json`, `~/.claude.json`, `~/.config/claude-code/auth.json`
- API key mode: `~/.claude/settings.json` (apiKeyHelper)
- Token endpoint: ⚠️ **NOT SUPPORTED** — The Anthropic OAuth refresh endpoint is undocumented and speculative. Claude Code handles token refresh internally. Attempting external refresh may cause auth corruption.
- Identity: Email and accountId are NOT available in current auth format; tokens are opaque (not JWTs)
- Re-authentication: Use `/login` command when tokens expire

**Note:** See [CLAUDE_AUTH_INVENTORY.md](CLAUDE_AUTH_INVENTORY.md) for the complete audit of Claude auth assumptions.

### OpenAI Codex
- Auth file: `~/.codex/auth.json`
- Token endpoint: `https://auth.openai.com/oauth/token`
- Request format: JSON body
```json
{
  "client_id": "app_EMoamEEZ73f0CkXaXp7hrann",
  "grant_type": "refresh_token",
  "refresh_token": "...",
  "scope": "openid profile email"
}
```

### Google Gemini
- Auth files: `~/.gemini/settings.json`, `~/.gemini/oauth_creds.json`
- API key mode: `~/.gemini/.env`
- Vertex AI ADC: `~/.config/gcloud/application_default_credentials.json`
- Token endpoint: `https://oauth2.googleapis.com/token`
- Request format: Form-encoded
```
client_id=...&grant_type=refresh_token&refresh_token=...
```
