# Distributed Auth Recovery System - Design Document

## Overview

This document describes two related features for caam:

1. **Auto-Discovery Watcher**: Automatically detect and save auth profiles when users log in naturally
2. **Distributed Auth Recovery**: Automatically handle Claude Code rate limit recovery across multiple remote terminal sessions

## Problem Statement

### Current Pain Points

1. **Manual profile management**: Users must explicitly run `caam backup` after each login
2. **Rate limit interruption**: When Claude Code hits usage limits mid-session:
   - Session shows "You've hit your limit"
   - User must manually type `/login`
   - User must copy OAuth URL to local browser
   - User must complete OAuth flow (select Google account, get challenge code)
   - User must paste code back into terminal
   - Repeat for each affected session (potentially many)

### Typical Scenario

User has 7+ Claude Max accounts and runs multiple Claude Code sessions on a remote Linux server, connected via WezTerm persistent sessions from a local Mac. When rate limits hit across several sessions simultaneously, the manual recovery process is extremely time-consuming and disruptive.

## Architecture

### System Components

```
┌─────────────────────────────────────────────────────────────────┐
│                     REMOTE (Linux Server)                        │
│                                                                  │
│  ┌──────────────────────┐    ┌────────────────────────────────┐ │
│  │ wezterm-mux-server   │    │ caam auth-coordinator (daemon) │ │
│  │ ├── Pane 1: claude   │←──→│ ├── Monitors all panes         │ │
│  │ ├── Pane 2: claude   │    │ ├── Detects rate limits        │ │
│  │ ├── Pane 3: claude   │    │ ├── Auto-injects /login        │ │
│  │ └── Pane N: ...      │    │ ├── Extracts OAuth URLs        │ │
│  └──────────────────────┘    │ ├── HTTP API (:7890)           │ │
│                              │ └── Injects codes back         │ │
│  ┌──────────────────────┐    └────────────────────────────────┘ │
│  │ caam watch (daemon)  │              ↑                        │
│  │ ├── fsnotify auth    │              │ localhost:7890         │
│  │ │   files            │              │                        │
│  │ └── Auto-save        │              │                        │
│  │     profiles         │              │                        │
│  └──────────────────────┘              │                        │
└────────────────────────────────────────│────────────────────────┘
                                         │
                    SSH Reverse Tunnel   │
                    (ssh -R 7890:localhost:7891)
                                         │
                                         ↓
┌─────────────────────────────────────────────────────────────────┐
│                      LOCAL (Mac Mini)                            │
│                                                                  │
│  ┌────────────────────────────────────────────────────────────┐ │
│  │ caam auth-agent (daemon)                                    │ │
│  │ ├── HTTP server (:7891)                                     │ │
│  │ ├── Receives auth requests from remote                      │ │
│  │ ├── Playwright browser automation                           │ │
│  │ │   ├── Opens OAuth URL                                     │ │
│  │ │   ├── Selects Google account (LRU)                        │ │
│  │ │   └── Extracts challenge code                             │ │
│  │ ├── Tracks account usage (LRU database)                     │ │
│  │ └── Sends codes back to coordinator                         │ │
│  └────────────────────────────────────────────────────────────┘ │
│                                                                  │
│  ┌──────────────────────┐                                       │
│  │ Chrome Browser       │                                       │
│  │ (all Google accounts │                                       │
│  │  already logged in)  │                                       │
│  └──────────────────────┘                                       │
└─────────────────────────────────────────────────────────────────┘
```

### Communication Flow

```
1. Claude Code hits rate limit
   ↓
2. auth-coordinator detects "You've hit your limit" in pane output
   ↓
3. auth-coordinator injects "/login\n" into pane
   ↓
4. auth-coordinator detects "Select login method:" → injects "1\n"
   ↓
5. auth-coordinator extracts OAuth URL from pane output
   ↓
6. auth-coordinator POSTs to local auth-agent (via SSH tunnel)
   {
     "pane_id": 123,
     "url": "https://claude.ai/oauth/authorize?...",
     "timestamp": "2026-01-12T15:30:00Z"
   }
   ↓
7. auth-agent opens URL in Playwright-controlled Chrome
   ↓
8. auth-agent selects Google account (Least Recently Used)
   ↓
9. auth-agent extracts challenge code from page
   ↓
10. auth-agent responds to coordinator with code
    {
      "code": "XXXX-XXXX",
      "account": "alice@gmail.com"
    }
    ↓
11. auth-coordinator injects code + "\n" into pane
    ↓
12. auth-coordinator detects login success → injects resume prompt
    "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n"
    ↓
13. Session resumes automatically
```

## Feature 1: Auto-Discovery Watcher

### Command

```bash
caam watch [--daemon] [--providers claude,codex,gemini]
```

### Implementation

Uses fsnotify to watch auth file changes:
- `~/.claude/.credentials.json`
- `~/.claude.json`
- `~/.config/claude-code/auth.json`
- `~/.codex/auth.json`
- `~/.gemini/settings.json`
- `~/.gemini/oauth_credentials.json`

On file change:
1. Debounce (wait 500ms for writes to settle)
2. Parse file to extract account identity:
   - Claude: JWT decode → extract email claim
   - Codex: JSON parse → extract user info
   - Gemini: JSON parse → extract account email
3. Check if profile already exists in vault
4. If new, create profile with email as name
5. Log action: "Auto-discovered profile: claude/alice@gmail.com"

### Code Location

- `cmd/caam/cmd/watch.go` - CLI command
- `internal/discovery/watcher.go` - fsnotify watcher
- `internal/identity/extractor.go` - Identity extraction (enhance existing)

## Feature 2: Distributed Auth Recovery

### Remote Component: auth-coordinator

#### Command

```bash
caam auth-coordinator [--port 7890] [--poll-interval 500ms] [--resume-prompt "..."]
```

#### Responsibilities

1. **Pane Discovery**: Poll `wezterm cli list --format json` to discover all panes
2. **Output Monitoring**: Poll `wezterm cli get-text --pane-id X` for each pane
3. **State Machine**: Track state per pane
4. **Text Injection**: Use `wezterm cli send-text --pane-id X --no-paste "text"`
5. **HTTP API**: Expose endpoints for local agent communication
6. **Queue Management**: Handle multiple simultaneous auth requests

#### State Machine

```
                    ┌──────────────────┐
                    │      IDLE        │
                    └────────┬─────────┘
                             │ detect "You've hit your limit"
                             ↓
                    ┌──────────────────┐
                    │  RATE_LIMITED    │
                    └────────┬─────────┘
                             │ inject "/login\n"
                             ↓
                    ┌──────────────────┐
                    │ AWAITING_METHOD  │
                    └────────┬─────────┘
                             │ detect "Select login method:" → inject "1\n"
                             ↓
                    ┌──────────────────┐
                    │  AWAITING_URL    │
                    └────────┬─────────┘
                             │ extract OAuth URL
                             ↓
                    ┌──────────────────┐
                    │ AUTH_PENDING     │──────┐
                    └────────┬─────────┘      │
                             │ receive code   │ timeout (60s)
                             ↓                │
                    ┌──────────────────┐      │
                    │ CODE_RECEIVED    │      │
                    └────────┬─────────┘      │
                             │ inject code    │
                             ↓                │
                    ┌──────────────────┐      │
                    │ AWAITING_CONFIRM │      │
                    └────────┬─────────┘      │
                             │ detect success │
                             ↓                ↓
                    ┌──────────────────┐  ┌───────────┐
                    │    RESUMING      │  │  FAILED   │
                    └────────┬─────────┘  └───────────┘
                             │ inject resume prompt
                             ↓
                    ┌──────────────────┐
                    │      IDLE        │
                    └──────────────────┘
```

#### Pattern Detection

```go
var patterns = struct {
    RateLimit    *regexp.Regexp
    SelectMethod *regexp.Regexp
    OAuthURL     *regexp.Regexp
    PastePrompt  *regexp.Regexp
    LoginSuccess *regexp.Regexp
    LoginFailed  *regexp.Regexp
}{
    RateLimit:    regexp.MustCompile(`You've hit your limit.*resets`),
    SelectMethod: regexp.MustCompile(`Select login method:`),
    OAuthURL:     regexp.MustCompile(`https://claude\.ai/oauth/authorize\?[^\s]+`),
    PastePrompt:  regexp.MustCompile(`Paste code here if prompted`),
    LoginSuccess: regexp.MustCompile(`Logged in as ([^\s]+@[^\s]+)`),
    LoginFailed:  regexp.MustCompile(`Login failed|Authentication error`),
}
```

#### HTTP API

```
POST /auth/request
  Request: { "pane_id": 123, "url": "https://...", "timestamp": "..." }
  Response: 202 Accepted { "request_id": "uuid" }

GET /auth/pending
  Response: [{ "request_id": "uuid", "pane_id": 123, "url": "...", "created_at": "..." }]

POST /auth/complete
  Request: { "request_id": "uuid", "code": "XXXX-XXXX", "account": "alice@gmail.com" }
  Response: 200 OK

GET /status
  Response: {
    "panes": [
      { "id": 123, "state": "IDLE", "last_check": "..." },
      { "id": 456, "state": "AUTH_PENDING", "request_id": "uuid" }
    ],
    "pending_auths": 2,
    "completed_today": 15
  }
```

#### Code Location

- `cmd/caam/cmd/coordinator.go` - CLI command
- `internal/coordinator/coordinator.go` - Main coordinator logic
- `internal/coordinator/state.go` - State machine
- `internal/coordinator/wezterm.go` - WezTerm CLI integration
- `internal/coordinator/api.go` - HTTP API server

### Local Component: auth-agent

#### Command

```bash
caam auth-agent [--port 7891] [--chrome-profile default] [--headless]
```

#### Responsibilities

1. **HTTP Server**: Listen for auth requests from coordinator
2. **Browser Automation**: Playwright with Chrome
3. **Account Selection**: LRU (Least Recently Used) strategy
4. **Code Extraction**: Parse challenge code from page
5. **Usage Tracking**: Track when each account was last used

#### LRU Account Tracking

```go
type AccountUsage struct {
    Email      string
    LastUsed   time.Time
    UseCount   int
    LastResult string  // "success", "rate_limited", "error"
}

// Storage: ~/.config/caam/account_usage.json
```

#### Playwright Flow

```typescript
async function completeOAuth(url: string): Promise<AuthResult> {
    const browser = await chromium.launch({
        headless: false,  // Need visible for Google auth
        channel: 'chrome',
    });

    const context = await browser.newContext({
        userDataDir: '/path/to/chrome/profile',  // Use existing Chrome profile
    });

    const page = await context.newPage();
    await page.goto(url);

    // Wait for either account selection or direct code page
    await page.waitForSelector(
        'div[data-email], .challenge-code, [data-testid="challenge-code"]',
        { timeout: 30000 }
    );

    // If account selection needed
    const accounts = await page.locator('div[data-email]').all();
    if (accounts.length > 0) {
        const lruAccount = await selectLRUAccount(accounts);
        await lruAccount.click();
        await page.waitForNavigation();
    }

    // Wait for and extract challenge code
    await page.waitForSelector('.challenge-code, [data-testid="challenge-code"]');
    const code = await page.textContent('.challenge-code');

    await browser.close();

    return {
        code: code.trim(),
        account: selectedAccount,
    };
}
```

#### Code Location

- `cmd/caam/cmd/agent.go` - CLI command (Go)
- `internal/agent/server.go` - HTTP server
- `internal/agent/browser.go` - Browser automation interface
- `tools/auth-agent/` - Playwright automation (TypeScript/Node.js)
  - `package.json`
  - `src/index.ts` - Main entry point
  - `src/oauth.ts` - OAuth flow automation
  - `src/accounts.ts` - Account selection logic

### SSH Tunnel Setup

The local agent binds to localhost:7891. The remote coordinator expects to reach the agent at localhost:7890.

#### Tunnel Command

```bash
# On local Mac, establish reverse tunnel
ssh -R 7890:localhost:7891 user@remote-server -N

# Or add to SSH config (~/.ssh/config)
Host remote-server
    RemoteForward 7890 localhost:7891
```

#### Auto-reconnect with autossh

```bash
autossh -M 0 -f -R 7890:localhost:7891 user@remote-server -N \
    -o ServerAliveInterval=30 \
    -o ServerAliveCountMax=3
```

## Configuration

### Remote Configuration (~/.config/caam/coordinator.yaml)

```yaml
coordinator:
  port: 7890
  poll_interval: 500ms
  wezterm_socket: /run/user/1000/wezterm-mux-server

  # Which panes to monitor (optional filter)
  pane_filter:
    # Only monitor panes with these titles or commands
    commands: ["claude", "claude-code"]
    # Or by tab title pattern
    title_pattern: ".*claude.*"

  # Text injection settings
  injection:
    # Prompt to inject after successful auth
    resume_prompt: |
      proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.
    # Delay between injections
    inject_delay: 100ms

  # Timeouts
  auth_timeout: 60s
  state_timeout: 30s
```

### Local Configuration (~/.config/caam/agent.yaml)

```yaml
agent:
  port: 7891

  browser:
    # Chrome executable path (auto-detected on Mac)
    executable: /Applications/Google Chrome.app/Contents/MacOS/Google Chrome
    # Chrome profile directory (uses default if empty)
    profile_dir: ""
    # Run headless (not recommended for Google auth)
    headless: false

  accounts:
    # Account selection strategy
    strategy: lru  # lru, round_robin, manual
    # Accounts to cycle through (optional, auto-detected from Google)
    emails:
      - alice@gmail.com
      - bob@gmail.com
      - carol@gmail.com
      - dave@gmail.com
      - eve@gmail.com
      - frank@gmail.com
      - grace@gmail.com
    # Minimum time before reusing an account
    min_reuse_interval: 30m
```

## Implementation Plan

### Phase 1: Auto-Discovery Watcher (Week 1)

1. Add fsnotify dependency
2. Implement `internal/discovery/watcher.go`
3. Enhance identity extraction for all providers
4. Add `caam watch` command
5. Test with manual logins

### Phase 2: Remote Coordinator (Week 2-3)

1. Implement WezTerm CLI wrapper
2. Implement state machine
3. Implement pattern detection
4. Add HTTP API server
5. Add `caam auth-coordinator` command
6. Test locally with mock agent

### Phase 3: Local Auth Agent (Week 3-4)

1. Set up TypeScript/Playwright project
2. Implement OAuth flow automation
3. Implement LRU account selection
4. Add HTTP server to receive requests
5. Add Go wrapper command
6. Test end-to-end with tunnel

### Phase 4: Integration & Polish (Week 4-5)

1. Add systemd service files for both components
2. Add launchd plist for Mac agent
3. Add monitoring/logging
4. Add macOS notifications (optional)
5. Documentation and examples
6. Edge case handling

## Security Considerations

1. **OAuth URLs**: Contain PKCE challenge, short-lived, single-use
2. **Challenge Codes**: Short-lived, single-use
3. **SSH Tunnel**: All communication encrypted, no external exposure
4. **Chrome Profile**: Uses existing logged-in sessions, no password handling

## Error Handling

### Coordinator Errors

- **WezTerm not running**: Log error, retry with backoff
- **Pane disappeared**: Remove from tracking, log warning
- **Auth timeout**: Transition to FAILED, notify user, allow retry
- **Local agent unreachable**: Queue request, retry when tunnel restored

### Agent Errors

- **Browser launch failed**: Return 500, log error
- **Account not available**: Fall back to next LRU account
- **Code extraction failed**: Retry once, then return error
- **Page timeout**: Return error with screenshot for debugging

## Monitoring

### Coordinator Metrics

- `panes_monitored` - Gauge of active panes
- `rate_limits_detected` - Counter
- `auths_requested` - Counter
- `auths_completed` - Counter
- `auths_failed` - Counter
- `auth_latency_seconds` - Histogram

### Agent Metrics

- `auth_requests_received` - Counter
- `auth_completed` - Counter by account
- `auth_failed` - Counter by reason
- `browser_sessions` - Gauge
- `oauth_duration_seconds` - Histogram

## Future Enhancements

1. **Usage Query**: Scrape Claude usage page to make smarter account selection
2. **Ghostty Support**: Extend to Ghostty terminal (different CLI interface)
3. **Codex/Gemini**: Extend auth recovery to other providers
4. **Multi-machine**: Support multiple remote servers
5. **Mobile Notification**: Push notification when auth needed (for manual backup)
6. **Account Pooling**: Share accounts across team with coordination
