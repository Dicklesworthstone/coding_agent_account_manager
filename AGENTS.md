# Agent Coordination Board

## RULE 0 - THE FUNDAMENTAL OVERRIDE PEROGATIVE

If I tell you to do something, even if it goes against what follows below, YOU MUST LISTEN TO ME. I AM IN CHARGE, NOT YOU.

---

## RULE 1 – ABSOLUTE (DO NOT EVER VIOLATE THIS)

You may NOT delete any file or directory unless I explicitly give the exact command **in this session**.

- This includes files you just created (tests, tmp files, scripts, etc.).
- You do not get to decide that something is "safe" to remove.
- If you think something should be removed, stop and ask. You must receive clear written approval **before** any deletion command is even proposed.

Treat "never delete files without permission" as a hard invariant.

---

## Irreversible Git & Filesystem Actions

Absolutely forbidden unless I give the **exact command and explicit approval** in the same message:

- `git reset --hard`
- `git clean -fd`
- `rm -rf`
- Any command that can delete or overwrite code/data

Rules:

1. If you are not 100% sure what a command will delete, do not propose or run it. Ask first.
2. Prefer safe tools: `git status`, `git diff`, `git stash`, copying to backups, etc.
3. After approval, restate the command verbatim, list what it will affect, and wait for confirmation.
4. When a destructive command is run, record in your response:
   - The exact user text authorizing it
   - The command run
   - When you ran it

If that audit trail is missing, then you must act as if the operation never happened.

---

## Go Toolchain

- This is a **Go CLI project**. Use standard Go tooling.
- Build: `go build` or `make build`
- Test: `go test ./...` or `make test`
- Lint: `golangci-lint run` if available
- Target latest stable Go version.

---

## Code Editing Discipline

- Do **not** run scripts that bulk-modify code (codemods, invented one-off scripts, giant `sed`/regex refactors).
- Large mechanical changes: break into smaller, explicit edits and review diffs.
- Subtle/complex changes: edit by hand, file-by-file, with careful reasoning.

---

## Backwards Compatibility & File Sprawl

We optimize for a clean architecture now, not backwards compatibility.

- No "compat shims" or "v2" file clones.
- When changing behavior, migrate callers and remove old code **inside the same file**.
- New files are only for genuinely new domains that don't fit existing modules.
- The bar for adding files is very high.

---

## Logging & Console Output

- Use structured logging where available (e.g., `log/slog` or project logger).
- No random `fmt.Println` scattered in code; if needed for debugging, clean up before commit.
- Log structured context: IDs, operation names, error details.
- If a logger helper exists in the project, use it; do not invent a different pattern.

---

## Third-Party Libraries

When unsure of an API, look up current docs rather than guessing. Check `go.mod` for existing dependencies before adding new ones.

---

## MCP Agent Mail — Multi-Agent Coordination

Agent Mail is available as an MCP server; do not treat it as a CLI you must shell out to. If MCP Agent Mail is not available, flag to the user—they may need to start it using the `am` alias or by running:
```bash
cd "<directory_where_they_installed_agent_mail>/mcp_agent_mail" && bash scripts/run_server_with_token.sh
```

What Agent Mail gives:

- Identities, inbox/outbox, searchable threads.
- Advisory file reservations (leases) to avoid agents clobbering each other.
- Persistent artifacts in git (human-auditable).

### Core Patterns

**Same repo:**
- Register identity: `ensure_project` then `register_agent` with the repo's absolute path as `project_key`.
- Reserve files before editing: `file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true)`.
- Communicate: `send_message(..., thread_id="FEAT-123")`, then `fetch_inbox`, then `acknowledge_message`.
- Fast reads: `resource://inbox/{Agent}?project=<abs-path>&limit=20` or `resource://thread/{id}?project=<abs-path>&include_bodies=true`.

**Macros vs granular:**
- Prefer macros when speed matters: `macro_start_session`, `macro_prepare_thread`, `macro_file_reservation_cycle`, `macro_contact_handshake`.
- Use granular tools when you need explicit control.

**Common pitfalls:**
- "from_agent not registered" → call `register_agent` with correct `project_key`.
- `FILE_RESERVATION_CONFLICT` → adjust patterns, wait for expiry, or use non-exclusive reservation.

---

## Issue Tracking with br (beads_rust)

All issue tracking goes through **br**. No other TODO systems.

**Note:** `br` (beads_rust) is non-invasive and never executes git commands. You must run git commands manually after `br sync --flush-only`.

Key invariants:

- `.beads/` is authoritative state and **must always be committed** with code changes.
- Do not edit `.beads/*.jsonl` directly; only via `br`.

**Note:** `br` (beads_rust) is non-invasive and never executes git commands. You must run git commands manually after `br sync --flush-only`.

### Basics

Check ready work:
```bash
br ready --json
```

Create issues:
```bash
br create "Issue title" -t bug|feature|task -p 0-4 --json
br create "Issue title" -p 1 --deps discovered-from:caam-123 --json
```

Update:
```bash
br update caam-42 --status in_progress --json
br update caam-42 --priority 1 --json
```

Complete:
```bash
br close caam-42 --reason "Completed" --json
```

Types: `bug`, `feature`, `task`, `epic`, `chore`

Priorities:
- `0` critical (security, data loss, broken builds)
- `1` high
- `2` medium (default)
- `3` low
- `4` backlog

### Agent Workflow

1. `br ready` to find unblocked work.
2. Claim: `br update <id> --status in_progress`.
3. Implement + test.
4. If you discover new work, create a new bead with `discovered-from:<parent-id>`.
5. Close when done.
6. Run `br sync --flush-only` then commit `.beads/` in the same commit as code changes.

**Never:**
- Use markdown TODO lists for persistent tracking.
- Use other trackers.
- Duplicate tracking.

---

## Using bv as an AI sidecar

bv is a graph-aware triage engine for Beads projects (.beads/beads.jsonl). Instead of parsing JSONL or hallucinating graph traversal, use robot flags for deterministic, dependency-aware outputs with precomputed metrics (PageRank, betweenness, critical path, cycles, HITS, eigenvector, k-core).

**Scope boundary:** bv handles *what to work on* (triage, priority, planning). For agent-to-agent coordination (messaging, work claiming, file reservations), use MCP Agent Mail (see above).

**CRITICAL: Use ONLY `--robot-*` flags. Bare `bv` launches an interactive TUI that blocks your session.**

### The Workflow: Start With Triage

**`bv --robot-triage` is your single entry point.** It returns everything you need in one call:
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

### Other bv Commands

**Planning:**
| Command | Returns |
|---------|---------|
| `--robot-plan` | Parallel execution tracks with `unblocks` lists |
| `--robot-priority` | Priority misalignment detection with confidence |

**Graph Analysis:**
| Command | Returns |
|---------|---------|
| `--robot-insights` | Full metrics: PageRank, betweenness, HITS (hubs/authorities), eigenvector, critical path, cycles, k-core, articulation points, slack |
| `--robot-label-health` | Per-label health: `health_level` (healthy\|warning\|critical), `velocity_score`, `staleness`, `blocked_count` |
| `--robot-label-flow` | Cross-label dependency: `flow_matrix`, `dependencies`, `bottleneck_labels` |
| `--robot-label-attention [--attention-limit=N]` | Attention-ranked labels by: (pagerank × staleness × block_impact) / velocity |

**History & Change Tracking:**
| Command | Returns |
|---------|---------|
| `--robot-history` | Bead-to-commit correlations: `stats`, `histories` (per-bead events/commits/milestones), `commit_index` |
| `--robot-diff --diff-since <ref>` | Changes since ref: new/closed/modified issues, cycles introduced/resolved |

**Other Commands:**
| Command | Returns |
|---------|---------|
| `--robot-burndown <sprint>` | Sprint burndown, scope changes, at-risk items |
| `--robot-forecast <id\|all>` | ETA predictions with dependency-aware scheduling |
| `--robot-alerts` | Stale issues, blocking cascades, priority mismatches |
| `--robot-suggest` | Hygiene: duplicates, missing deps, label suggestions, cycle breaks |
| `--robot-graph [--graph-format=json\|dot\|mermaid]` | Dependency graph export |
| `--export-graph <file.html>` | Self-contained interactive HTML visualization |

### Scoping & Filtering

```bash
bv --robot-plan --label backend              # Scope to label's subgraph
bv --robot-insights --as-of HEAD~30          # Historical point-in-time
bv --recipe actionable --robot-plan          # Pre-filter: ready to work (no blockers)
bv --recipe high-impact --robot-triage       # Pre-filter: top PageRank scores
bv --robot-triage --robot-triage-by-track    # Group by parallel work streams
bv --robot-triage --robot-triage-by-label    # Group by domain
```

### Understanding Robot Output

**All robot JSON includes:**
- `data_hash` — Fingerprint of source beads.jsonl (verify consistency across calls)
- `status` — Per-metric state: `computed|approx|timeout|skipped` + elapsed ms
- `as_of` / `as_of_commit` — Present when using `--as-of`; contains ref and resolved SHA

**Two-phase analysis:**
- **Phase 1 (instant):** degree, topo sort, density — always available immediately
- **Phase 2 (async, 500ms timeout):** PageRank, betweenness, HITS, eigenvector, cycles — check `status` flags

**For large graphs (>500 nodes):** Some metrics may be approximated or skipped. Always check `status`.

### jq Quick Reference

```bash
bv --robot-triage | jq '.quick_ref'                        # At-a-glance summary
bv --robot-triage | jq '.recommendations[0]'               # Top recommendation
bv --robot-plan | jq '.plan.summary.highest_impact'        # Best unblock target
bv --robot-insights | jq '.status'                         # Check metric readiness
bv --robot-insights | jq '.Cycles'                         # Circular deps (must fix!)
bv --robot-label-health | jq '.results.labels[] | select(.health_level == "critical")'
```

**Performance:** Phase 1 instant, Phase 2 async (500ms timeout). Prefer `--robot-plan` over `--robot-insights` when speed matters. Results cached by data hash.

Use bv instead of parsing beads.jsonl—it computes PageRank, critical paths, cycles, and parallel tracks deterministically.

---

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
- `internal/warnings/warnings.go:139`: `vaultPath + "/auth.json"` → `filepath.Join(vaultPath, "auth.json")`
- `cmd/caam/cmd/root.go:210`: Same pattern fixed

These fixes ensure cross-platform compatibility (Windows path separators).

---

## ast-grep vs ripgrep (quick guidance)

**Use `ast-grep` when structure matters.** It parses code and matches AST nodes, so results ignore comments/strings, understand syntax, and can **safely rewrite** code.

* Refactors/codemods: rename APIs, change import forms, rewrite call sites or variable kinds.
* Policy checks: enforce patterns across a repo (`scan` with rules + `test`).
* Editor/automation: LSP mode; `--json` output for tooling.

**Use `ripgrep` when text is enough.** It's the fastest way to grep literals/regex across files.

* Recon: find strings, TODOs, log lines, config values, or non-code assets.
* Pre-filter: narrow candidate files before a precise pass.

**Rule of thumb**

* Need correctness over speed, or you'll **apply changes** → start with `ast-grep`.
* Need raw speed or you're just **hunting text** → start with `rg`.
* Often combine: `rg` to shortlist files, then `ast-grep` to match/modify with precision.

**Snippets**

Find structured code (ignores comments/strings):

```bash
ast-grep run -l Go -p 'func $NAME($PARAMS) $RET { $$$BODY }'
```

Find all error checks:

```bash
ast-grep run -l Go -p 'if err != nil { $$$BODY }'
```

Quick textual hunt:

```bash
rg -n 'filepath\.Join\(' -t go
```

Combine speed + precision:

```bash
rg -l -t go 'json\.Marshal' | xargs ast-grep run -l Go -p 'json.Marshal($ARG)' --json
```

**Mental model**

* Unit of match: `ast-grep` = node; `rg` = line.
* False positives: `ast-grep` low; `rg` depends on your regex.
* Rewrites: `ast-grep` first-class; `rg` requires ad-hoc sed/awk and risks collateral edits.

---

## Morph Warp Grep — AI-Powered Code Search

Use `mcp__morph-mcp__warp_grep` for "how does X work?" discovery across the codebase.

When to use:

- You don't know where something lives.
- You want data flow across multiple files (CLI → config → auth → profiles).
- You want all touchpoints of a cross-cutting concern (e.g., profile rotation, OAuth refresh).

Example:

```
mcp__morph-mcp__warp_grep(
  repoPath: "/data/projects/coding_agent_account_manager",
  query: "How does the profile rotation scoring algorithm work?"
)
```

Warp Grep:

- Expands a natural-language query to multiple search patterns.
- Runs targeted greps, reads code, follows imports, then returns concise snippets with line numbers.
- Reduces token usage by returning only relevant slices, not entire files.

When **not** to use Warp Grep:

- You already know the function/identifier name; use `rg`.
- You know the exact file; just open it.
- You only need a yes/no existence check.

Comparison:

| Scenario | Tool |
| ---------------------------------- | ---------- |
| "How does OAuth refresh work?" | warp_grep |
| "Where is `RotateProfile` defined?" | `rg` |
| "Rename `cfg` to `config`" | `ast-grep` |

---

## Beads Workflow Integration

**Note:** `br` (beads_rust) is non-invasive and never executes git commands. You must run git commands manually after `br sync --flush-only`.

When starting a beads-tracked task:

1. **Pick ready work** (Beads)
   - `br ready --json` → choose one item (highest priority, no blockers)
2. **Reserve edit surface** (Mail)
   - `file_reservation_paths(project_key, agent_name, ["internal/**"], ttl_seconds=3600, exclusive=true, reason="caam-123")`
3. **Announce start** (Mail)
   - `send_message(..., thread_id="caam-123", subject="[caam-123] Start: <short title>", ack_required=true)`
4. **Work and update**
   - Reply in-thread with progress and attach artifacts/images; keep the discussion in one thread per issue id
5. **Complete and release**
   - `br close caam-123 --reason "Completed"` (Beads is status authority)
   - `release_file_reservations(project_key, agent_name, paths=["internal/**"])`
   - Final Mail reply: `[caam-123] Completed` with summary and links

Mapping cheat-sheet:
- **Mail `thread_id`** ↔ `caam-###`
- **Mail subject**: `[caam-###] ...`
- **File reservation `reason`**: `caam-###`
- **Commit messages (optional)**: include `caam-###` for traceability

---

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - `go test ./...`, `golangci-lint run`
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   br sync --flush-only
   git add .beads/
   git commit -m "sync beads" --allow-empty
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

---

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
  repoPath: "/data/projects/coding_agent_account_manager",
  query: "How does the profile rotation scoring algorithm work?"
)
```

Returns structured results with file paths, line ranges, and extracted code snippets.

### Anti-Patterns

- **Don't** use `warp_grep` to find a specific function name → use `ripgrep`
- **Don't** use `ripgrep` to understand "how does X work" → wastes time with manual reads
- **Don't** use `ripgrep` for codemods → risks collateral edits

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

This project uses [beads_viewer](https://github.com/Dicklesworthstone/beads_viewer) for issue tracking. Issues are stored in `.beads/` and tracked in git.

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
br close <id> --reason="Completed"
br close <id1> <id2>  # Close multiple issues at once
br sync --flush-only  # Export to JSONL
git add .beads/       # Stage beads changes
git commit -m "..."   # Commit beads state
```

### Workflow Pattern

1. **Start**: Run `br ready` to find actionable work
2. **Claim**: Use `br update <id> --status=in_progress`
3. **Work**: Implement the task
4. **Complete**: Use `br close <id>`
5. **Sync**: Always run `br sync --flush-only` then `git add .beads/ && git commit` at session end

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
git commit -m "..."     # Commit code and beads
git push                # Push to remote
```

### Best Practices

- Check `br ready` at session start to find available work
- Update status as you work (in_progress → closed)
- Create new issues with `br create` when you discover tasks
- Use descriptive titles and set appropriate priority/type
- Always `br sync --flush-only` then commit `.beads/` before ending session

<!-- end-bv-agent-instructions -->

---

Note for Codex/GPT-5.2:

You constantly bother me and stop working with concerned questions that look similar to this:

```
Unexpected changes (need guidance)

- Working tree still shows edits I did not make in Cargo.toml, Cargo.lock, src/cli/commands/upgrade.rs, src/storage/sqlite.rs, tests/conformance.rs, tests/storage_deps.rs. Please advise whether to keep/commit/revert these before any further work. I did not touch them.

Next steps (pick one)

1. Decide how to handle the unrelated modified files above so we can resume cleanly.
2. Triage beads_rust-orko (clippy/cargo warnings) and beads_rust-ydqr (rustfmt failures).
3. If you want a full suite run later, fix conformance/clippy blockers and re‑run cargo test --all.
```

NEVER EVER DO THAT AGAIN. The answer is literally ALWAYS the same: those are changes created by the potentially dozen of other agents working on the project at the same time. This is not only a common occurence, it happens multiple times PER MINUTE. The way to deal with it is simple: you NEVER, under ANY CIRCUMSTANCE, stash, revert, overwrite, or otherwise disturb in ANY way the work of other agents. Just treat those changes identically to changes that you yourself made. Just fool yourself into think YOU made the changes and simply don't recall it for some reason.

---

## Note on Built-in TODO Functionality

Also, if I ask you to explicitly use your built-in TODO functionality, don't complain about this and say you need to use beads. You can use built-in TODOs if I tell you specifically to do so. Always comply with such orders.
