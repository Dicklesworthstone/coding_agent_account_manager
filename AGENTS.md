# AGENTS.md — coding_agent_account_manager

> Guidelines for AI coding agents working in this Go codebase.

---

## RULE 0 - THE FUNDAMENTAL OVERRIDE PREROGATIVE

If I tell you to do something, even if it goes against what follows below, YOU MUST LISTEN TO ME. I AM IN CHARGE, NOT YOU.

---

## RULE NUMBER 1: NO FILE DELETION

**YOU ARE NEVER ALLOWED TO DELETE A FILE WITHOUT EXPRESS PERMISSION.** Even a new file that you yourself created, such as a test code file. You have a horrible track record of deleting critically important files or otherwise throwing away tons of expensive work. As a result, you have permanently lost any and all rights to determine that a file or folder should be deleted.

**YOU MUST ALWAYS ASK AND RECEIVE CLEAR, WRITTEN PERMISSION BEFORE EVER DELETING A FILE OR FOLDER OF ANY KIND.**

---

## Irreversible Git & Filesystem Actions — DO NOT EVER BREAK GLASS

1. **Absolutely forbidden commands:** `git reset --hard`, `git clean -fd`, `rm -rf`, or any command that can delete or overwrite code/data must never be run unless the user explicitly provides the exact command and states, in the same message, that they understand and want the irreversible consequences.
2. **No guessing:** If there is any uncertainty about what a command might delete or overwrite, stop immediately and ask the user for specific approval. "I think it's safe" is never acceptable.
3. **Safer alternatives first:** When cleanup or rollbacks are needed, request permission to use non-destructive options (`git status`, `git diff`, `git stash`, copying to backups) before ever considering a destructive command.
4. **Mandatory explicit plan:** Even after explicit user authorization, restate the command verbatim, list exactly what will be affected, and wait for a confirmation that your understanding is correct. Only then may you execute it—if anything remains ambiguous, refuse and escalate.
5. **Document the confirmation:** When running any approved destructive command, record (in the session notes / final response) the exact user text that authorized it, the command actually run, and the execution time. If that record is absent, the operation did not happen.

---

## Git Branch: ONLY Use `main`, NEVER `master`

**The default branch is `main`. The `master` branch exists only for legacy URL compatibility.**

- **All work happens on `main`** — commits, PRs, feature branches all merge to `main`
- **Never reference `master` in code or docs** — if you see `master` anywhere, it's a bug that needs fixing
- **The `master` branch must stay synchronized with `main`** — after pushing to `main`, also push to `master`:
  ```bash
  git push origin main:master
  ```

**If you see `master` referenced anywhere:**
1. Update it to `main`
2. Ensure `master` is synchronized: `git push origin main:master`

---

## Toolchain: Go & Make

We use **Go** with **Make** as the build system in this project.

- **Go version:** 1.24+ (see `go.mod` for minimum)
- **Toolchain:** `go1.24.4` (pinned in `go.mod`)
- **Build:** `make build` or `go build ./cmd/caam`
- **Binary name:** `caam`
- **Dependency versions:** Explicit in `go.mod` for stability

### Key Dependencies

| Package | Purpose |
|---------|---------|
| `cobra` | CLI command framework |
| `bubbletea` + `bubbles` + `lipgloss` | Terminal UI (TUI) |
| `chromedp` | Headless browser automation for OAuth |
| `fsnotify` | Filesystem event watching |
| `modernc.org/sqlite` | Pure-Go SQLite (no CGO) |
| `sftp` + `crypto/ssh` | Remote profile sync via SSH/SFTP |
| `creack/pty` | PTY allocation for passthrough mode |
| `stretchr/testify` | Test assertions |
| `yaml.v3` | YAML configuration parsing |
| `uuid` | Unique identifier generation |

### Build & Install

```bash
make build              # Build binary with version/commit/date ldflags
make install            # Install to GOPATH/bin
make release-snapshot   # Cross-platform build via goreleaser
```

### Lint Configuration

Uses `golangci-lint` with `errcheck` and `unused` disabled (see `.golangci.yml`).

---

## Code Editing Discipline

### No Script-Based Changes

**NEVER** run a script that processes/changes code files in this repo. Brittle regex-based transformations create far more problems than they solve.

- **Always make code changes manually**, even when there are many instances
- For many simple changes: use parallel subagents
- For subtle/complex changes: do them methodically yourself

### No File Proliferation

If you want to change something or add a feature, **revise existing code files in place**.

**NEVER** create variations like:
- `main_v2.go`
- `main_improved.go`
- `main_enhanced.go`

New files are reserved for **genuinely new functionality** that makes zero sense to include in any existing file. The bar for creating new files is **incredibly high**.

---

## Backwards Compatibility

We do not care about backwards compatibility—we're in early development with no users. We want to do things the **RIGHT** way with **NO TECH DEBT**.

- Never create "compatibility shims"
- Never create wrapper functions for deprecated APIs
- Just fix the code directly

---

## Compiler Checks (CRITICAL)

**After any substantive code changes, you MUST verify no errors were introduced:**

```bash
# Build the binary
make build

# Run tests
go test ./...

# Run tests with race detector
go test -race ./...

# Run linter
golangci-lint run ./...

# Verify formatting
go fmt ./...
goimports -w .
```

If you see errors, **carefully understand and resolve each issue**. Read sufficient context to fix them the RIGHT way.

---

## Logging & Console Output

- Use structured logging where available (e.g., `log/slog` or project logger).
- No random `fmt.Println` scattered in code; if needed for debugging, clean up before commit.
- Log structured context: IDs, operation names, error details.
- If a logger helper exists in the project, use it; do not invent a different pattern.

---

## Testing

### Testing Policy

Tests live alongside implementation in `_test.go` files within each package. Tests must cover:
- Happy path
- Edge cases (empty input, max values, boundary conditions)
- Error conditions

### Running Tests

```bash
# Run all tests
go test ./...

# Run with verbose output
go test -v ./...

# Run with race detector
go test -race -v ./...

# Run tests for a specific package
go test ./internal/rotation/...
go test ./internal/refresh/...
go test ./internal/sync/...

# Via Makefile
make test
make test-race
```

### Test Categories

| Package | Focus Areas |
|---------|-------------|
| `internal/profile` | Profile CRUD, tag validation, vault operations |
| `internal/rotation` | Rotation scoring algorithms, profile selection |
| `internal/refresh` | OAuth token refresh, URL guard (HTTPS enforcement) |
| `internal/sync` | Profile pool synchronization, mutex correctness |
| `internal/ratelimit` | Rate limit detection and signal parsing |
| `internal/discovery` | Provider/profile auto-discovery |
| `internal/daemon` | Background daemon lifecycle, PID management |
| `internal/wrap` | Stream wrapping, tee writer correctness |
| `internal/signals` | OS signal handling |
| `internal/coordinator` | Multi-account coordination logic |
| `internal/health` | Health check endpoints |
| `internal/exec` | Command execution, shell integration |
| `internal/tui` | Bubble Tea TUI components |
| `cmd/caam/cmd` | CLI command integration tests |

---

## Third-Party Library Usage

If you aren't 100% sure how to use a third-party library, **SEARCH ONLINE** to find the latest documentation and current best practices.

---

## coding_agent_account_manager — This Project

**This is the project you're working on.** `caam` (Coding Agent Account Manager) is a Go CLI tool for sub-100ms account switching across AI coding CLIs with fixed-cost subscription plans (Claude Max, GPT Pro, Gemini Ultra, Codex).

### What It Does

When you hit usage limits on one AI coding subscription, `caam` instantly swaps to another account by backing up and restoring OAuth token files — no browser, no OAuth dance, no interruption to flow state. It also provides automated rotation, rate limit detection, health monitoring, and a TUI dashboard.

### Architecture

```
CLI Command (cobra) → Profile Vault (~/.local/share/caam/vault/)
    │                         │
    ├── backup/activate       ├── claude/*.json    (per-account auth)
    ├── rotate (auto-select)  ├── codex/*.json
    ├── status/health         ├── gemini/*.json
    │                         └── chatgpt/*.json
    │
    ├── Rate Limit Detection ─── Signals / Log Parsing
    ├── OAuth Token Refresh ──── Browser Automation (chromedp)
    ├── Profile Rotation ─────── Scoring Algorithm (cooldown, usage, freshness)
    ├── Daemon Mode ──────────── Background monitoring + auto-rotation
    ├── Remote Sync ──────────── SFTP/SSH profile distribution
    └── TUI Dashboard ────────── Bubble Tea interactive interface
```

### Project Structure

```
coding_agent_account_manager/
├── cmd/caam/                  # CLI entry point (cobra root command)
├── internal/
│   ├── agent/                 # Agent detection and interaction
│   ├── api/                   # API server for remote control
│   ├── authfile/              # Auth file format parsing/writing
│   ├── authpool/              # Auth credential pooling
│   ├── authwatch/             # Auth file change monitoring
│   ├── browser/               # Headless browser automation (chromedp)
│   ├── bundle/                # Profile bundle import/export
│   ├── config/                # Configuration management
│   ├── coordinator/           # Multi-account coordination
│   ├── daemon/                # Background daemon lifecycle
│   ├── db/                    # SQLite storage (modernc.org/sqlite)
│   ├── deploy/                # Deployment helpers
│   ├── discovery/             # Provider/profile auto-discovery
│   ├── e2e/                   # End-to-end test helpers
│   ├── exec/                  # Command execution, shell integration
│   ├── handoff/               # Session handoff between accounts
│   ├── health/                # Health check and diagnostics
│   ├── identity/              # Identity management
│   ├── logs/                  # Structured logging
│   ├── monitor/               # Resource monitoring
│   ├── notify/                # Notification dispatch
│   ├── passthrough/           # PTY passthrough mode
│   ├── prediction/            # Usage prediction / forecasting
│   ├── pricing/               # Subscription pricing data
│   ├── profile/               # Profile CRUD, tags, vault ops
│   ├── project/               # Project context detection
│   ├── provider/              # Provider definitions (claude, codex, etc.)
│   ├── pty/                   # PTY allocation and management
│   ├── ratelimit/             # Rate limit detection and parsing
│   ├── refresh/               # OAuth token refresh, URL guards
│   ├── rotation/              # Profile rotation scoring algorithm
│   ├── setup/                 # First-run setup wizard
│   ├── signals/               # OS signal handling
│   ├── stealth/               # Stealth mode for avoiding detection
│   ├── sync/                  # Profile pool sync (SFTP/SSH)
│   ├── tailscale/             # Tailscale VPN integration
│   ├── testutil/              # Shared test utilities
│   ├── tui/                   # Bubble Tea TUI dashboard
│   ├── update/                # Self-update mechanism
│   ├── usage/                 # Usage tracking and analytics
│   ├── version/               # Build version info (ldflags)
│   ├── warnings/              # Warning system
│   ├── watcher/               # File watcher (fsnotify)
│   ├── wezterm/               # WezTerm terminal integration
│   └── wrap/                  # Stream wrapping / tee
├── web/                       # Web UI assets
├── docs/                      # Documentation
├── Makefile                   # Build targets
├── go.mod / go.sum            # Go module definition
└── .golangci.yml              # Linter config
```

### Key Files by Package

| Package | Key Files | Purpose |
|---------|-----------|---------|
| `cmd/caam/cmd` | `root.go`, `activate.go`, `backup.go`, `rotate.go`, `shell.go` | CLI commands: activate, backup, rotate, status, shell |
| `internal/profile` | `profile.go`, `profile_test.go` | Profile struct, tags (max 10, max 32 chars, lowercase alphanum + hyphens), vault CRUD |
| `internal/rotation` | `rotation.go` | Scoring algorithm: cooldown, usage history, freshness weighting |
| `internal/refresh` | `url_guard.go` | OAuth refresh with HTTPS enforcement, URL validation |
| `internal/sync` | `pool.go` | Profile pool sync with mutex protection, atomic saves |
| `internal/ratelimit` | `ratelimit.go` | Rate limit signal detection from provider responses |
| `internal/daemon` | `daemon.go` | Background daemon: PID file, auto-rotation, health monitoring |
| `internal/tui` | TUI components | Bubble Tea dashboard: profile list, status, rotation controls |
| `internal/exec` | `shell.go` | Shell quoting via `shellescape.Quote()` to prevent command injection |
| `internal/config` | Configuration | Vault path, provider settings, rotation parameters |

### Supported Providers

| Provider | Auth Files | Capabilities |
|----------|-----------|--------------|
| Claude (Anthropic) | `~/.claude.json` | Backup, activate, rotate, refresh |
| Codex (OpenAI) | `~/.codex/auth.json` | Backup, activate, rotate |
| Gemini (Google) | `~/.gemini/settings.json` | Backup, activate, rotate |
| Grok Build (xAI) | `~/.grok/auth.json` (+ `config.toml`; respects `GROK_HOME`) | Backup, activate, rotate |
| ChatGPT (OpenAI) | Various | Backup, activate |

### Key Design Decisions

- **Pure-Go SQLite** (`modernc.org/sqlite`) — no CGO dependency, fully portable binaries
- **Vault-based storage** (`~/.local/share/caam/vault/`) — per-provider, per-account directories for auth files
- **Sub-100ms switching** — file copy, not OAuth re-auth; no browser or network round-trip
- **Profile tags** — lowercase alphanumeric + hyphens, max 32 chars, max 10 tags per profile; filterable via `caam ls --tag`
- **Rotation scoring** — multi-signal algorithm considering cooldown period, historical usage, token freshness, and rate limit state
- **Shell safety** — all shell commands use `shellescape.Quote()` to prevent injection
- **HTTPS enforcement** — OAuth refresh endpoints validated via URL guard (no HTTP allowed)
- **Atomic saves** — profile pool sync uses temp-file + rename pattern
- **PID-based daemon** — stale PID detection handles crash recovery
- **Goreleaser** — cross-platform release builds with version/commit/date ldflags
- **Structured logging** — IDs, operation names, and error details in log output

---

## MCP Agent Mail — Multi-Agent Coordination

A mail-like layer that lets coding agents coordinate asynchronously via MCP tools and resources. Provides identities, inbox/outbox, searchable threads, and advisory file reservations with human-auditable artifacts in Git.

### Why It's Useful

- **Prevents conflicts:** Explicit file reservations (leases) for files/globs
- **Token-efficient:** Messages stored in per-project archive, not in context
- **Quick reads:** `resource://inbox/...`, `resource://thread/...`

### Same Repository Workflow

1. **Register identity:**
   ```
   ensure_project(project_key=<abs-path>)
   register_agent(project_key, program, model)
   ```

2. **Reserve files before editing:**
   ```
   file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true)
   ```

3. **Communicate with threads:**
   ```
   send_message(..., thread_id="FEAT-123")
   fetch_inbox(project_key, agent_name)
   acknowledge_message(project_key, agent_name, message_id)
   ```

4. **Quick reads:**
   ```
   resource://inbox/{Agent}?project=<abs-path>&limit=20
   resource://thread/{id}?project=<abs-path>&include_bodies=true
   ```

### Macros vs Granular Tools

- **Prefer macros for speed:** `macro_start_session`, `macro_prepare_thread`, `macro_file_reservation_cycle`, `macro_contact_handshake`
- **Use granular tools for control:** `register_agent`, `file_reservation_paths`, `send_message`, `fetch_inbox`, `acknowledge_message`

### Common Pitfalls

- `"from_agent not registered"`: Always `register_agent` in the correct `project_key` first
- `"FILE_RESERVATION_CONFLICT"`: Adjust patterns, wait for expiry, or use non-exclusive reservation
- **Auth errors:** If JWT+JWKS enabled, include bearer token with matching `kid`

---

## Beads (br) — Dependency-Aware Issue Tracking

Beads provides a lightweight, dependency-aware issue database and CLI (`br` - beads_rust) for selecting "ready work," setting priorities, and tracking status. It complements MCP Agent Mail's messaging and file reservations.

**Important:** `br` is non-invasive—it NEVER runs git commands automatically. You must manually commit changes after `br sync --flush-only`.

### Conventions

- **Single source of truth:** Beads for task status/priority/dependencies; Agent Mail for conversation and audit
- **Shared identifiers:** Use Beads issue ID (e.g., `caam-123`) as Mail `thread_id` and prefix subjects with `[caam-123]`
- **Reservations:** When starting a task, call `file_reservation_paths()` with the issue ID in `reason`

### Typical Agent Flow

1. **Pick ready work (Beads):**
   ```bash
   br ready --json  # Choose highest priority, no blockers
   ```

2. **Reserve edit surface (Mail):**
   ```
   file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true, reason="caam-123")
   ```

3. **Announce start (Mail):**
   ```
   send_message(..., thread_id="caam-123", subject="[caam-123] Start: <title>", ack_required=true)
   ```

4. **Work and update:** Reply in-thread with progress

5. **Complete and release:**
   ```bash
   br close caam-123 --reason "Completed"
   br sync --flush-only  # Export to JSONL (no git operations)
   ```
   ```
   release_file_reservations(project_key, agent_name, paths=["internal/**"])
   ```
   Final Mail reply: `[caam-123] Completed` with summary

### Mapping Cheat Sheet

| Concept | Value |
|---------|-------|
| Mail `thread_id` | `caam-###` |
| Mail subject | `[caam-###] ...` |
| File reservation `reason` | `caam-###` |
| Commit messages | Include `caam-###` for traceability |

---

## bv — Graph-Aware Triage Engine

bv is a graph-aware triage engine for Beads projects (`.beads/beads.jsonl`). It computes PageRank, betweenness, critical path, cycles, HITS, eigenvector, and k-core metrics deterministically.

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). For agent-to-agent coordination (messaging, work claiming, file reservations), use MCP Agent Mail.

**CRITICAL: Use ONLY `--robot-*` flags. Bare `bv` launches an interactive TUI that blocks your session.**

### The Workflow: Start With Triage

**`bv --robot-triage` is your single entry point.** It returns:
- `quick_ref`: at-a-glance counts + top 3 picks
- `recommendations`: ranked actionable items with scores, reasons, unblock info
- `quick_wins`: low-effort high-impact items
- `blockers_to_clear`: items that unblock the most downstream work
- `project_health`: status/type/priority distributions, graph metrics
- `commands`: copy-paste shell commands for next steps

```bash
bv --robot-triage        # THE MEGA-COMMAND: start here
bv --robot-next          # Minimal: just the single top pick + claim command
```

### Command Reference

**Planning:**
| Command | Returns |
|---------|---------|
| `--robot-plan` | Parallel execution tracks with `unblocks` lists |
| `--robot-priority` | Priority misalignment detection with confidence |

**Graph Analysis:**
| Command | Returns |
|---------|---------|
| `--robot-insights` | Full metrics: PageRank, betweenness, HITS, eigenvector, critical path, cycles, k-core, articulation points, slack |
| `--robot-label-health` | Per-label health: `health_level`, `velocity_score`, `staleness`, `blocked_count` |
| `--robot-label-flow` | Cross-label dependency: `flow_matrix`, `dependencies`, `bottleneck_labels` |
| `--robot-label-attention [--attention-limit=N]` | Attention-ranked labels |

**History & Change Tracking:**
| Command | Returns |
|---------|---------|
| `--robot-history` | Bead-to-commit correlations |
| `--robot-diff --diff-since <ref>` | Changes since ref: new/closed/modified issues, cycles |

**Other:**
| Command | Returns |
|---------|---------|
| `--robot-burndown <sprint>` | Sprint burndown, scope changes, at-risk items |
| `--robot-forecast <id\|all>` | ETA predictions with dependency-aware scheduling |
| `--robot-alerts` | Stale issues, blocking cascades, priority mismatches |
| `--robot-suggest` | Hygiene: duplicates, missing deps, label suggestions |
| `--robot-graph [--graph-format=json\|dot\|mermaid]` | Dependency graph export |
| `--export-graph <file.html>` | Interactive HTML visualization |

### Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank
bv --robot-triage --robot-triage-by-track    # Group by parallel work streams
bv --robot-triage --robot-triage-by-label    # Group by domain
```

### Understanding Robot Output

**All robot JSON includes:**
- `data_hash` — Fingerprint of source beads.jsonl
- `status` — Per-metric state: `computed|approx|timeout|skipped` + elapsed ms
- `as_of` / `as_of_commit` — Present when using `--as-of`

**Two-phase analysis:**
- **Phase 1 (instant):** degree, topo sort, density
- **Phase 2 (async, 500ms timeout):** PageRank, betweenness, HITS, eigenvector, cycles

### jq Quick Reference

```bash
bv --robot-triage | jq '.quick_ref'                        # At-a-glance summary
bv --robot-triage | jq '.recommendations[0]'               # Top recommendation
bv --robot-plan | jq '.plan.summary.highest_impact'        # Best unblock target
bv --robot-insights | jq '.status'                         # Check metric readiness
bv --robot-insights | jq '.Cycles'                         # Circular deps (must fix!)
```

---

## UBS — Ultimate Bug Scanner

**Golden Rule:** `ubs <changed-files>` before every commit. Exit 0 = safe. Exit >0 = fix & re-run.

### Commands

```bash
ubs file.go file2.go                       # Specific files (< 1s) — USE THIS
ubs $(git diff --name-only --cached)       # Staged files — before commit
ubs --only=go .                            # Language filter (3-5x faster)
ubs --ci --fail-on-warning .               # CI mode — before PR
ubs .                                      # Whole project (ignores vendor/, etc.)
```

### Output Format

```
Warning  Category (N errors)
    file.go:42:5 - Issue description
    Suggested fix
Exit code: 1
```

Parse: `file:line:col` -> location | fix suggestion -> how to fix | Exit 0/1 -> pass/fail

### Fix Workflow

1. Read finding -> category + fix suggestion
2. Navigate `file:line:col` -> view context
3. Verify real issue (not false positive)
4. Fix root cause (not symptom)
5. Re-run `ubs <file>` -> exit 0
6. Commit

### Bug Severity

- **Critical (always fix):** Memory safety, data races, SQL injection, command injection
- **Important (production):** Unchecked errors, resource leaks, nil pointer dereferences
- **Contextual (judgment):** TODO/FIXME, fmt.Println debugging

---

## RCH — Remote Compilation Helper

RCH offloads `go build`, `go test`, `golangci-lint`, and other compilation commands to a fleet of 8 remote Contabo VPS workers instead of building locally. This prevents compilation storms from overwhelming csd when many agents run simultaneously.

**RCH is installed at `~/.local/bin/rch` and is hooked into Claude Code's PreToolUse automatically.** Most of the time you don't need to do anything if you are Claude Code — builds are intercepted and offloaded transparently.

To manually offload a build:
```bash
rch exec -- go build ./cmd/caam
rch exec -- go test ./...
rch exec -- golangci-lint run ./...
```

Quick commands:
```bash
rch doctor                    # Health check
rch workers probe --all       # Test connectivity to all 8 workers
rch status                    # Overview of current state
rch queue                     # See active/waiting builds
```

If rch or its workers are unavailable, it fails open — builds run locally as normal.

**Note for Codex/GPT-5.2:** Codex does not have the automatic PreToolUse hook, but you can (and should) still manually offload compute-intensive compilation commands using `rch exec -- <command>`. This avoids local resource contention when multiple agents are building simultaneously.

---

## ast-grep vs ripgrep

**Use `ast-grep` when structure matters.** It parses code and matches AST nodes, ignoring comments/strings, and can **safely rewrite** code.

- Refactors/codemods: rename APIs, change import forms
- Policy checks: enforce patterns across a repo
- Editor/automation: LSP mode, `--json` output

**Use `ripgrep` when text is enough.** Fastest way to grep literals/regex.

- Recon: find strings, TODOs, log lines, config values
- Pre-filter: narrow candidate files before ast-grep

### Rule of Thumb

- Need correctness or **applying changes** -> `ast-grep`
- Need raw speed or **hunting text** -> `rg`
- Often combine: `rg` to shortlist files, then `ast-grep` to match/modify

### Go Examples

```bash
# Find structured code (ignores comments)
ast-grep run -l Go -p 'func $NAME($PARAMS) $RET { $$$BODY }'

# Find all error checks
ast-grep run -l Go -p 'if err != nil { $$$BODY }'

# Quick textual hunt
rg -n 'filepath\.Join\(' -t go

# Combine speed + precision
rg -l -t go 'json\.Marshal' | xargs ast-grep run -l Go -p 'json.Marshal($ARG)' --json
```

---

## Morph Warp Grep — AI-Powered Code Search

**Use `mcp__morph-mcp__warp_grep` for exploratory "how does X work?" questions.** An AI agent expands your query, greps the codebase, reads relevant files, and returns precise line ranges with full context.

**Use `ripgrep` for targeted searches.** When you know exactly what you're looking for.

**Use `ast-grep` for structural patterns.** When you need AST precision for matching/rewriting.

### When to Use What

| Scenario | Tool | Why |
|----------|------|-----|
| "How does profile rotation work?" | `warp_grep` | Exploratory; don't know where to start |
| "Where is the OAuth refresh logic?" | `warp_grep` | Need to understand architecture |
| "Find all uses of `json.Marshal`" | `ripgrep` | Targeted literal search |
| "Find files with `fmt.Println`" | `ripgrep` | Simple pattern |
| "Replace all `err != nil` checks" | `ast-grep` | Structural refactor |

### warp_grep Usage

```
mcp__morph-mcp__warp_grep(
  repoPath: "/dp/coding_agent_account_manager",
  query: "How does the profile rotation scoring algorithm work?"
)
```

Returns structured results with file paths, line ranges, and extracted code snippets.

### Anti-Patterns

- **Don't** use `warp_grep` to find a specific function name -> use `ripgrep`
- **Don't** use `ripgrep` to understand "how does X work" -> wastes time with manual reads
- **Don't** use `ripgrep` for codemods -> risks collateral edits

---

## cass — Cross-Agent Session Search

`cass` indexes prior agent conversations (Claude Code, Codex, Cursor, Gemini, ChatGPT, etc.) so we can reuse solved problems.

**CRITICAL: Never run bare `cass` (TUI). Always use `--robot` or `--json`.**

### Commands

```bash
cass health                                      # Check system status
cass search "profile rotation" --robot --limit 5
cass view /path/to/session.jsonl -n 42 --json
cass expand /path/to/session.jsonl -n 42 -C 3 --json
cass capabilities --json
cass robot-docs guide
```

### Tips

- Use `--fields minimal` for lean output
- Filter by agent with `--agent`
- Use `--days N` to limit to recent history
- stdout is data-only, stderr is diagnostics; exit code 0 means success

Treat cass as a way to avoid re-solving problems other agents already handled.

<!-- bv-agent-instructions-v1 -->

---

## Beads Workflow Integration

This project uses [beads_rust](https://github.com/Dicklesworthstone/beads_rust) (`br`) for issue tracking. Issues are stored in `.beads/` and tracked in git.

**Important:** `br` is non-invasive—it NEVER executes git commands. After `br sync --flush-only`, you must manually run `git add .beads/ && git commit`.

### Essential Commands

```bash
# View issues (launches TUI - avoid in automated sessions)
bv

# CLI commands for agents (use these instead)
br ready              # Show issues ready to work (no blockers)
br list --status=open # All open issues
br show <id>          # Full issue details with dependencies
br create --title="..." --type=task --priority=2
br update <id> --status=in_progress
br close <id> --reason "Completed"
br close <id1> <id2>  # Close multiple issues at once
br sync --flush-only  # Export to JSONL (NO git operations)
```

### Workflow Pattern

1. **Start**: Run `br ready` to find actionable work
2. **Claim**: Use `br update <id> --status=in_progress`
3. **Work**: Implement the task
4. **Complete**: Use `br close <id>`
5. **Sync**: Run `br sync --flush-only` then `git add .beads/ && git commit` at session end

### Key Concepts

- **Dependencies**: Issues can block other issues. `br ready` shows only unblocked work.
- **Priority**: P0=critical, P1=high, P2=medium, P3=low, P4=backlog (use numbers, not words)
- **Types**: task, bug, feature, epic, question, docs
- **Blocking**: `br dep add <issue> <depends-on>` to add dependencies

### Session Protocol

**Before ending any session, run this checklist:**

```bash
git status              # Check what changed
git add <files>         # Stage code changes
br sync --flush-only    # Export beads to JSONL
git add .beads/         # Stage beads changes
git commit -m "..."     # Commit everything together
git push                # Push to remote
```

### Best Practices

- Check `br ready` at session start to find available work
- Update status as you work (in_progress -> closed)
- Create new issues with `br create` when you discover tasks
- Use descriptive titles and set appropriate priority/type
- Always `br sync --flush-only && git add .beads/` before ending session

<!-- end-bv-agent-instructions -->

## Agent Activity Log

### 2025-12-20: BrownSnow Code Audit

**Scope:** Comprehensive review of codebase for bugs, security issues, inefficiencies, and reliability problems.

**Packages Audited:**
- `internal/wrap` - Stream wrapping and tee functionality
- `internal/ratelimit` - Rate limit detection
- `internal/discovery` - Provider/profile discovery
- `internal/daemon` - Background daemon management
- `internal/refresh` - OAuth token refresh
- `internal/rotation` - Profile rotation algorithms
- `internal/sync` - Profile pool synchronization
- `cmd/caam/cmd` - CLI command implementations

**Findings:**

1. **Minor Inefficiency - `internal/wrap/wrap.go`**: The `teeWriter` checks for partial buffer writes on each call. While technically correct for the io.Writer contract, this is unlikely to occur with typical destinations. Not a bug, just overly defensive.

2. **Minor Issue - `internal/daemon/daemon.go`**: PID file write uses `os.WriteFile` directly rather than the atomic temp-file + fsync + rename pattern. This is a minor reliability concern if the process crashes mid-write, but the daemon code already handles stale PID detection, so the impact is minimal.

3. **Security - Good Practices Observed:**
   - `cmd/caam/cmd/shell.go`: Proper shell quoting with `shellescape.Quote()` prevents command injection
   - `internal/refresh/url_guard.go`: Enforces HTTPS for OAuth endpoints, proper URL validation
   - `internal/rotation/rotation.go`: No injection vectors in scoring algorithms
   - `internal/sync/pool.go`: Proper mutex usage and atomic saves

**Conclusion:** No critical bugs or security vulnerabilities found. Codebase follows Go best practices with proper error handling, mutex protection, and input validation.

### 2025-12-20: TealMeadow - Profile Tags Feature (caam-g2yz.2)

Implemented complete profile tagging system:
- Added `Tags` field to Profile struct with validation (lowercase alphanumeric + hyphens, max 32 chars, max 10 tags)
- Created `caam tag` command with subcommands: add, remove, list, clear, all
- Added `--tag` filter to `caam ls` command
- Full test coverage in `internal/profile/profile_test.go`

### 2025-12-20: Path Construction Bug Fixes

Fixed two instances of incorrect path construction using string concatenation instead of `filepath.Join`:
- `internal/warnings/warnings.go:139`: `vaultPath + "/auth.json"` -> `filepath.Join(vaultPath, "auth.json")`
- `cmd/caam/cmd/root.go:210`: Same pattern fixed

These fixes ensure cross-platform compatibility (Windows path separators).

---

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **Sync beads** - `br sync --flush-only` to export to JSONL
5. **Hand off** - Provide context for next session

---

Note for Codex/GPT-5.2:

You constantly bother me and stop working with concerned questions that look similar to this:

```
Unexpected changes (need guidance)

- Working tree still shows edits I did not make in Cargo.toml, Cargo.lock, src/cli/commands/upgrade.rs, src/storage/sqlite.rs, tests/conformance.rs, tests/storage_deps.rs. Please advise whether to keep/commit/revert these before any further work. I did not touch them.

Next steps (pick one)

1. Decide how to handle the unrelated modified files above so we can resume cleanly.
2. Triage beads_rust-orko (clippy/cargo warnings) and beads_rust-ydqr (rustfmt failures).
3. If you want a full suite run later, fix conformance/clippy blockers and re-run cargo test --all.
```

NEVER EVER DO THAT AGAIN. The answer is literally ALWAYS the same: those are changes created by the potentially dozen of other agents working on the project at the same time. This is not only a common occurrence, it happens multiple times PER MINUTE. The way to deal with it is simple: you NEVER, under ANY CIRCUMSTANCE, stash, revert, overwrite, or otherwise disturb in ANY way the work of other agents. Just treat those changes identically to changes that you yourself made. Just fool yourself into thinking YOU made the changes and simply don't recall it for some reason.

---

## Note on Built-in TODO Functionality

Also, if I ask you to explicitly use your built-in TODO functionality, don't complain about this and say you need to use beads. You can use built-in TODOs if I tell you specifically to do so. Always comply with such orders.
