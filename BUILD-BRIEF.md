# caam Mac-Compliant Fork — Build Brief

Fork of Dicklesworthstone/coding_agent_account_manager under justakeyboardbetweenus.
Goal: ONE account manager for ALL coding-agent providers on macOS — claude (multi-Max),
codex, gemini, grok, deepseek, ollama, and Anthropic-compatible open-weight endpoints
(GLM/Moonshot) — with rotation, cooldowns, and health across all of them.

## Why (context)

- macOS Keychain is single-slot and non-configurable for Claude Code OAuth; upstream
  caam's file-copy vault can't switch Claude accounts on Mac (captures .claude.json
  without the token → forced /login every switch).
- Proven Mac fix: long-lived tokens from `claude setup-token` injected per-invocation
  via CLAUDE_CODE_OAUTH_TOKEN (+ CLAUDE_CONFIG_DIR per profile for settings/history
  isolation). Parallel-safe, zero Keychain prompts. Working reference implementation:
  ~/.config/veup/claude-accounts.zsh (`claude-as`), tokens at ~/.config/veup/claude-<name>-token.

## Architecture decision (the spine)

**Token/env-injection profiles as a first-class profile type**, not Keychain wrangling.
File-swap vault remains for codex/gemini (works fine). Darwin Keychain read/write is a
LATER optional fallback for the interactive login slot, never the primary mechanism.

## Workstreams (PR per workstream, onto fork main)

### WS1 — Reconcile local patches
Apply scratchpad export of the old dirty clone's patches (462 lines, diffed at daf5d55,
now 40 commits stale): identity/claude.go +88, identity/gemini.go +59, exec, health,
activate, root. Upstream's identity hardening wave (#34–#56) likely obsoletes parts.
Rule: upstream wins on duplicated functionality; keep local additions upstream lacks.
Branch reconcile/local-patches. All tests green.

### WS2 — Token profiles (core)
- New profile type "token" in vault (token file 0600 + meta.json type marker).
- `caam token add <provider> <name>` (stdin/paste), `caam token import` for existing
  ~/.config/veup/claude-*-token files.
- run/exec inject env (claude: CLAUDE_CODE_OAUTH_TOKEN + CLAUDE_CONFIG_DIR=~/.claude-<name>)
  instead of file swapping. activate on a token profile = set default for run/exec.
- Rotation, cooldown, health, status must treat token profiles as first-class
  (health = passive expiry/format check; --validate = cheap API probe).

### WS3 — Providers
- grok: verify upstream #57 works; wire into token/env model if key-based.
- deepseek: API-key profiles (DEEPSEEK_API_KEY injection).
- ollama: endpoint profiles (OLLAMA_HOST), no auth; health = GET /api/tags.
- anthropic-compatible endpoints (GLM, Moonshot/Kimi): claude-provider variant profile
  carrying ANTHROPIC_BASE_URL + ANTHROPIC_AUTH_TOKEN.

### WS4 (later, optional) — Darwin Keychain fallback
security(1) read/write of "Claude Code-credentials" for vaulting the interactive login
slot. Expect one-time interactive Allow; document it. Not the spine.

## Constraints

- Go; match upstream layout/idioms; tests for everything new.
- Keep upstream LICENSE (fork — house MIT+AI-Lab Rider does NOT apply).
- NEVER overwrite /opt/homebrew/bin/caam during dev; build to ./caam. Install manually
  only after doctor passes on all providers.
- Push auth: repo-local credential helper already configured (justakeyboardbetweenus
  via gh keyring). `upstream` remote = Dicklesworthstone.
- Keep diffs upstreamable — Emanuel may want WS2/WS3 as PRs.
