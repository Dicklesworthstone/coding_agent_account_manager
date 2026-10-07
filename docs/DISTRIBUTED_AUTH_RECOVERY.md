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
│  │ ├── fsnotify auth    │              │ 127.0.0.1:7890 + token │
│  │ │   files            │              │                        │
│  │ └── Auto-save        │              │                        │
│  │     profiles         │              │                        │
│  └──────────────────────┘              │                        │
└────────────────────────────────────────│────────────────────────┘
                                         ↑
                SSH connection opened    │  (agent polls /auth/pending,
                by the local agent       │   posts /auth/complete)
                                         │
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
6. auth-coordinator publishes a pending request; the local auth-agent picks it
   up from GET /auth/pending over its SSH connection
   {
     "id": "uuid",
     "pane_id": 123,
     "url": "https://claude.ai/oauth/authorize?...",
     "created_at": "2026-01-12T15:30:00Z"
   }
   ↓
7. auth-agent opens URL in chromedp-controlled Chrome
   ↓
8. auth-agent selects Google account (Least Recently Used)
   ↓
9. auth-agent extracts challenge code from page
   ↓
10. auth-agent POSTs /auth/complete and retries until acknowledged
    {
      "request_id": "uuid",
      "code": "XXXX-XXXX",
      "account": "alice@gmail.com"
    }
    ↓
11. auth-coordinator injects code + "\n" into pane (once, even if redelivered)
    ↓
12. auth-coordinator detects login success → injects resume prompt
    "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n"
    ↓
13. Session resumes automatically
```

## Feature 1: Auto-Discovery Watcher

### Command

```bash
caam watch --once                         # Capture existing native logins
caam watch                                # Continue watching in the foreground
caam watch --provider claude,codex,grok    # Restrict providers
caam watch --poll-interval 1s --debounce 500ms
```

### Implementation

The watcher covers Claude, Codex, Gemini, Antigravity (`agy`), Grok, OpenCode,
and Cursor. It resolves each provider's credential paths once and keeps the
provider attached to the full path, including when several providers have a
file named `auth.json`. An explicit `CLAUDE_CONFIG_DIR` selects that directory's
credentials, identity, and settings together.

Filesystem notifications trigger prompt checks. Content polling runs every
two seconds by default and recovers missed events, atomic file or directory
replacement, missing directories, and unavailable notification services.
Starting the watcher does not create native configuration directories. Changes
are coalesced per provider with a default 500ms settling interval. Credential
alternatives such as Gemini's `.env` and OAuth cache are included; use
`--watch-optional` to also react to settings that do not carry credentials.

Both continuous watching and `--once` use the same capture operation:

1. Read and validate the provider's credential state without modifying native
   files. An identity record or settings file alone is not a login.
2. Compare credential material with saved profiles, ignoring unrelated settings
   changes. Identical credentials do not produce another profile.
3. Update a saved grant only when ownership and a newer, complete credential
   generation are proven. Preserve the profile's name, settings, and metadata.
4. Publish other valid logins as complete new snapshots. A usable account label
   can name a new profile; missing identity or a conflicting existing name uses
   a unique `auto-...` name. Labels alone never authorize replacement.

Malformed or incomplete credentials leave saved profiles intact. Continuous
watching retries failed captures after the polling interval, including when a
vault write failed and the native credential has not changed again. A one-time
scan reports failures with a nonzero exit while retaining and displaying
successful captures from other providers. Stopping the watcher waits for any
capture and its callbacks to finish.

### Code Location

- `cmd/caam/cmd/watch.go` - CLI command
- `internal/discovery/watcher.go` - provider-aware notifications, polling, and scans
- `internal/authfile/discovery.go` - validated capture, ownership, and snapshot publication

## Feature 2: Distributed Auth Recovery

### Remote Component: auth-coordinator

#### Command

```bash
caam auth-coordinator [--bind 127.0.0.1] [--port 7890] [--poll-interval 500ms] [--resume-prompt "..."] [--auth-token "shared-secret"]
caam auth-coordinator status [--config ~/.config/caam/coordinator.json] [--json]
```

If `--auth-token` (or `CAAM_COORDINATOR_TOKEN`, or `auth_token` in the config
file) is set, the coordinator API requires `Authorization: Bearer <token>` from
the local agent. The API listens on `127.0.0.1` by default; binding to any
other address (for example a Tailscale IP) is refused unless a token is set.

#### Transport (what `caam setup distributed` provisions)

`caam setup distributed` deploys each coordinator with a freshly generated
per-host token in `~/.config/caam/coordinator.json` (mode 0600, `bind:
127.0.0.1`) and a systemd user unit whose `ExecStart` uses `%h` for the config
path. The unit's `PATH` is the user's login-shell `PATH` plus the system
directories (systemd otherwise gives user services only the latter, missing a
`wezterm` or `tmux` under the home directory or `/opt`). After starting the
service it calls the authenticated `/status` endpoint through the same SSH
connection, so a deployment is reported as successful only when the agent's
exact path works. It also checks that a multiplexer is on the service's
`PATH` and that lingering is on (without it, systemd stops the coordinator
when you log out), and prints a warning with the fix for either.

The local agent config (`~/.config/caam/distributed-agent.json`, mode 0600)
gets one entry per verified host. Each entry carries the token and an `ssh`
block; the agent opens its own SSH connection (ssh-agent, the configured
identity file, or default keys, with `known_hosts` checking) and reaches the
loopback-only API through it, reconnecting when the connection drops. Re-running
setup replaces only the entries of hosts it redeployed and keeps `accounts`,
`chrome_profile`, `strategy`, and other hosts untouched.

```json
{
  "port": 7891,
  "coordinators": [
    {
      "name": "csd",
      "url": "http://127.0.0.1:7890",
      "display_name": "csd-host",
      "token": "<per-host token>",
      "ssh": {"host": "100.64.0.5", "port": 22, "user": "ubuntu", "identity_file": "~/.ssh/id_ed25519"}
    }
  ],
  "poll_interval": "2s",
  "chrome_profile": "",
  "strategy": "lru",
  "accounts": []
}
```

When the local machine's binary targets another platform (a macOS agent
deploying to Linux), setup installs the published release matching the local
version into `~/.local/bin` with the official installer, keeping an existing
remote `caam` only when it already runs that release (any release for a
development build).

#### Upgrading coordinators

Keep coordinators on the same caam version as the local agent:

```bash
caam update                      # update this machine
caam update --remotes --dry-run  # show each coordinator's planned change
caam update --remotes            # bring every coordinator to this version
```

`--remotes` walks the coordinators in the agent config and, over each
entry's SSH connection, keeps the host's deployed `coordinator.json`,
replaces the binary (checksum-verified upload, or the release matching
this version when the local binary is for another platform), rewrites the
unit, restarts it and verifies `/status`. The previous binary and unit are
kept (`<binary>.caam-prev`, `<unit>.caam-prev`); if the upgraded coordinator
does not pass verification they are restored and the host is reported as
rolled back, and `caam update --remotes --rollback` restores them on demand.
`--force` redeploys at the same version, which also refreshes older units
(for example to pick up the login `PATH`). The command exits non-zero when
any host failed or was rolled back.

#### Delivery guarantees

- The first valid response for a request is stored and never overwritten.
- Redelivering the identical response is acknowledged again (`200`), including
  for 15 minutes after the request closes, so an agent whose acknowledgement was
  lost can retry safely. The pane receives the code once.
- A different response for an answered request is rejected with `409`; a
  response for a request that timed out or lost its pane gets `410`; an unknown
  request gets `404`; a response with neither (or both) `code` and `error` gets
  `400`.
- The agent retries transport errors, `5xx`, `408`, and `429` with exponential
  backoff for up to 90 seconds, treats every other status as final, and reports
  success (and records account usage) only after a `2xx` acknowledgement.

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
GET /health                      (no token required)
  Response: { "status": "ok", "backend": "wezterm", ... }

GET /auth/pending
  Response: [{ "id": "uuid", "pane_id": 123, "url": "...", "created_at": "...", "status": "pending" }]

POST /auth/complete              (alias: /auth/submit)
  Request: { "request_id": "uuid", "code": "XXXX-XXXX", "account": "alice@gmail.com" }
       or: { "request_id": "uuid", "error": "why the browser flow failed" }
  Response: 200 { "status": "accepted", "request_id": "uuid" }   (also for identical redelivery)
            400 invalid body, 401 bad token, 404 unknown request,
            409 different response already accepted, 410 request closed

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

## WezTerm Recovery Commands (Operator Guide)

This section documents the on-host `caam wezterm ...` commands that work directly
against WezTerm panes. These do not require the distributed agent/coordinator setup
and are ideal for batch recovery on a single host.

### Prereqs

- WezTerm installed and `wezterm cli` available in PATH on the host where panes live.
- Commands read recent pane output via `wezterm cli get-text`. Output is normalized by
  stripping ANSI/OSC escapes and box-drawing characters before matching.

### Pane Matching & Safety

By default, `login-all` and `oauth-urls` scan each pane and match in this order:

1. **Rate-limit markers** ("you've hit your limit", "rate limit", `429`, etc.)
2. **Tool markers** (e.g., "claude", "codex", "gemini")

Overrides and safety flags:

- `--match <regex>`: custom regex applied to normalized pane output.
- `--all`: bypass matching and target every pane.
- `--dry-run`: show targets without sending input (login-all only).
- `--yes` / `--force`: required in non-interactive shells (login-all, recover --auto).

### Quick Recipes

1) **Identify targets** (no writes):

```bash
caam wezterm recover --status
caam wezterm login-all claude --dry-run
```

2) **Inject `/login`** (optionally select subscription):

```bash
caam wezterm login-all claude --subscription --yes
```

3) **Extract OAuth URLs (copy-friendly report)**:

```bash
caam wezterm oauth-urls claude
```

Output format is tab-separated:

```
<pane_id>\t<scanned_at_rfc3339>\t<oauth_url>\t# <pane_title>
```

4) **Drive recovery state machine**:

```bash
# Interactive UI
caam wezterm recover

# One-shot auto-advance (single step per run)
caam wezterm recover --auto --yes

# Watch status refresh
caam wezterm recover --status --watch --interval 2s
```

### Recovery States & Actions

`caam wezterm recover` reports each pane in one of these states:

- **IDLE**: no action needed
- **RATE_LIMITED**: safe to inject `/login`
- **AWAITING_SELECT**: prompt shown → inject `1` (subscription)
- **AWAITING_URL**: OAuth URL detected → waiting for code
- **CODE_READY**: code available → inject code
- **RESUMING**: login success → inject resume prompt
- **FAILED**: error detected → retry

Interactive key bindings:

- `r`: refresh
- `l`: inject `/login` to RATE_LIMITED panes
- `s`: select subscription (`1`) on AWAITING_SELECT panes
- `c`: inject codes to CODE_READY panes
- `p`: inject resume prompt to RESUMING panes
- `a`: auto-advance all panes one step
- `q`: quit

### Resume Prompt Configuration

- Local recovery: `caam wezterm recover --resume-prompt "..."`
- Distributed coordinator: `caam auth-coordinator --resume-prompt "..."`

The default resume prompt includes the AGENTS reminder and trailing newline.

### Compaction Reminder (Coordinator Only)

The distributed coordinator can optionally inject a reminder when Claude shows the
compaction banner ("Conversation compacted · ctrl+o for history").

As of **January 23, 2026**, this is an internal coordinator config (no CLI flag yet).
Config fields in `internal/coordinator.Config`:

- `CompactionReminderEnabled`
- `CompactionReminderPrompt`
- `CompactionReminderCooldown`
- `CompactionReminderRegex`

This is tracked under **caam-imtg**. Until the CLI wiring lands, use the resume
prompt to ensure the AGENTS reminder is injected after successful auth.

### Debugging & Troubleshooting

- Set `CAAM_DEBUG=1` to emit JSON debug logs (pane scans, match reasons, URL counts).
- Common errors:
  - `wezterm CLI not found in PATH` → install WezTerm or fix PATH
  - `no wezterm panes found` → ensure mux server is running / correct host
  - `no panes matched (use --all to force)` → adjust `--match` or use `--all`
  - `non-interactive session: use --yes or --dry-run` → add `--yes`


### Local Component: auth-agent

#### Command

```bash
caam auth-agent [--port 7891] [--chrome-profile DIR] [--headless]
caam auth-agent --config ~/.config/caam/distributed-agent.json

# Sign in to the Google accounts (and Claude) the agent will use; once per machine
caam auth-agent signin [--config PATH] [--chrome-profile DIR]

# Run at login and restart on crash (launchd on macOS, systemd --user on Linux)
caam auth-agent service install [--config PATH]
caam auth-agent service status [--json]
caam auth-agent service uninstall
```

The agent drives its own persistent Chrome profile,
`~/.local/share/caam/auth-agent-chrome` (`$CAAM_HOME/data/auth-agent-chrome`
when `CAAM_HOME` is set), unless `chrome_profile` names another. Chrome locks
a profile while it is open and refuses automation of the everyday default
profile, so the agent never uses your normal browser profile; `signin` opens
the agent's profile so the Google sessions it needs persist across OAuth
flows.

On macOS the service is `~/Library/LaunchAgents/com.dicklesworthstone.caam.auth-agent.plist`
(logs in `~/Library/Logs/caam-auth-agent.log`), started in the GUI session so it
can drive a visible Chrome window. On Linux it is
`~/.config/systemd/user/caam-auth-agent.service`. Installing validates the
config first and is idempotent.

Account strategies: `lru` (default; never-used accounts first), `round_robin`,
and `random`.

#### Responsibilities

1. **HTTP Server**: Listen for auth requests from coordinator
2. **Browser Automation**: chromedp driving Chrome on a dedicated profile
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

#### Browser Flow

`Browser.CompleteOAuth` (chromedp) launches Chrome on the agent's profile,
opens the OAuth URL, and then inspects the page every step until it has a
code (at most 10 steps, 90 seconds):

1. **Code callback** (`.../oauth/code/callback?code=…&state=…`): the code is
   taken from the URL as `code#state`, the form Claude Code's paste prompt
   expects. On other Anthropic/Claude `/oauth/code` pages a displayed
   `code#state` is read from the page; no other page is ever scraped.
2. **Claude login** (`claude.ai/login`, when the profile's Claude session is
   missing or expired): clicks "Continue with Google".
3. **Google account chooser**: clicks the account the strategy prefers
   (by its `data-identifier`); if that account is not signed in to
   the profile, the first offered account is used and reported as the one
   used, so usage tracking stays truthful.
4. **Consent page**: clicks the approve button. Claude's authorize page
   approves whatever Claude account the profile is signed in to, without
   asking Google, so when it names a different account than the strategy
   chose, the agent first deletes the claude.ai cookies (Google sessions
   stay) and signs in again through Google with the chosen account, once
   per flow. The account the page names is reported as the one used.

Each click first checks that a matching element is visible, so an absent
selector costs one page evaluation rather than the flow's deadline. A flow
that ends on a sign-in page fails with an error naming that page (host and
path only, never the query) and the remedy, `caam auth-agent signin`.

`internal/agent/browser_test.go` drives this flow in real Chrome/Chromium
against local fixture pages served at the real hostnames through a
TLS-terminating proxy (skipped with `-short` or when no browser is found;
`CAAM_TEST_CHROME` selects the binary).

#### Code Location

- `cmd/caam/cmd/agent.go` - CLI command
- `internal/agent/agent.go` - single-coordinator agent, acknowledged delivery
- `internal/agent/multi.go` - multi-coordinator agent, SSH tunnel transport, config file
- `internal/agent/browser.go` - chromedp browser automation

### Transport

The agent always initiates the connection: it polls the coordinator and posts
results back, so nothing on the local machine needs to accept connections.

With a config from `caam setup distributed`, each coordinator entry's `ssh`
block makes the agent tunnel its requests over its own SSH connection; no
manual tunnel is needed.

For a hand-run single coordinator (`caam auth-agent --coordinator
http://localhost:7890`), forward the coordinator port yourself:

```bash
# On the local machine
ssh -N -L 7890:127.0.0.1:7890 user@remote-server

# Or with autossh for automatic reconnects
autossh -M 0 -f -N -L 7890:127.0.0.1:7890 user@remote-server \
    -o ServerAliveInterval=30 \
    -o ServerAliveCountMax=3
```

On a private network such as a tailnet you can instead run the coordinator
with `--bind <tailscale-ip> --auth-token <secret>` and point the agent at
`http://<tailscale-ip>:7890` with `--coordinator-token <secret>`.

## Configuration

Both files are JSON, written with mode 0600 by `caam setup distributed`, and
can be edited by hand. Omitted keys keep their defaults.

### Remote: `~/.config/caam/coordinator.json`

Read by `caam auth-coordinator --config <path>` (the systemd unit that setup
installs passes it) and by `caam auth-coordinator status`.

```json
{
  "bind": "127.0.0.1",
  "port": 7890,
  "poll_interval": "500ms",
  "auth_timeout": "60s",
  "state_timeout": "30s",
  "resume_prompt": "proceed. Reread AGENTS.md so it's still fresh in your mind. Use ultrathink.\n",
  "resume_cooldown": "10s",
  "output_lines": 100,
  "backend": "auto",
  "auth_token": "<generated per host>"
}
```

| Key | Default | Meaning |
|-----|---------|---------|
| `bind` | `127.0.0.1` | Listen host. A non-loopback bind is refused unless `auth_token` is set. |
| `port` | `7890` | Listen port. |
| `poll_interval` | `500ms` | How often panes are scanned. |
| `auth_timeout` | `60s` | How long a published auth request waits for the agent before failing. |
| `state_timeout` | `30s` | How long a pane may sit in a transitional state. |
| `resume_prompt` | see above | Text injected after a successful login. |
| `resume_cooldown` | `10s` | Wait after login success before injecting the resume prompt. |
| `output_lines` | `100` | Scrollback lines read per poll. |
| `backend` | `auto` | `auto` (WezTerm preferred, else tmux; follows whichever multiplexer is running, so one started after the coordinator is picked up), `wezterm`, or `tmux`. |
| `auth_token` | none | Bearer token required on every endpoint except `/health`. |

Command-line flags (`--bind`, `--port`, `--backend`, `--poll-interval`,
`--resume-prompt`, `--auth-token`) override the file; `CAAM_COORDINATOR_TOKEN`
overrides `auth_token` when `--auth-token` is not given.

### Local: `~/.config/caam/distributed-agent.json`

Read by `caam auth-agent --config <path>`, by the login service that
`caam auth-agent service install` registers, and by `caam serve` and
`caam robot status` to report coordinator health.

```json
{
  "port": 7891,
  "poll_interval": "2s",
  "coordinators": [
    {
      "name": "build1",
      "display_name": "build1.example.net",
      "url": "http://127.0.0.1:7890",
      "token": "<same as the remote auth_token>",
      "ssh": {
        "host": "build1.example.net",
        "port": 22,
        "user": "ubuntu",
        "identity_file": "~/.ssh/id_ed25519"
      }
    }
  ],
  "chrome_profile": "",
  "headless": false,
  "strategy": "lru",
  "accounts": ["alice@example.com", "bob@example.com"]
}
```

| Key | Default | Meaning |
|-----|---------|---------|
| `port` | `7891` | Port of the agent's local status API. |
| `poll_interval` | `2s` | How often each coordinator is polled for pending requests. |
| `coordinators[].url` | — | Coordinator base URL. With `ssh` set it is resolved on the remote host, so loopback is correct. |
| `coordinators[].token` | none | Bearer token for that coordinator. |
| `coordinators[].ssh` | none | Reach `url` through an SSH connection to this host (port 22, current user, and default keys/agent when omitted). The connection is redialed on failure. |
| `chrome_profile` | `<caam data>/auth-agent-chrome` | Chrome user-data directory holding the Google sessions; `~/` is expanded. Sign in with `caam auth-agent signin` (`chrome_user_data_dir` and `chrome_profile_dir` are accepted aliases). |
| `headless` | `false` | Run Chrome headless; Google sign-in usually needs a visible window. |
| `strategy` | `lru` | Account selection: `lru`, `round_robin`, or `random`. |
| `accounts` | `[]` | Accounts to rotate through; empty lets the OAuth page choose. |

A single-coordinator config may use `coordinator_url` and `coordinator_token`
instead of `coordinators`. Re-running `caam setup distributed` merges newly
discovered hosts into this file and keeps existing tokens (`--rotate-tokens`
issues new ones).

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
