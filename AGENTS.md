### Using caam for instant AI coding tool account switching

caam (Coding Agent Account Manager) manages auth files for AI coding CLIs to enable sub-second account switching. When you hit usage limits on "all you can eat" subscriptions (GPT Pro, Claude Max, Gemini Ultra), switch accounts instantly without browser login flows.

**Core mechanism:** Each tool stores OAuth tokens in specific files. caam backs them up with labels and restores them on demand. No proxies, no env vars—just file copies.

---

### Quick Reference

```bash
# Backup current auth (after logging in normally)
caam backup claude jeff-gmail-1
caam backup codex work-openai
caam backup gemini personal

# Instant switch (< 1 second)
caam activate claude jeff-gmail-2
caam activate codex backup-account

# Check what's active
caam status

# List all saved profiles
caam ls

# Show auth file locations
caam paths
```

---

### Auth File Locations

| Tool | Auth Files | Login Command |
|------|-----------|---------------|
| Codex CLI | `~/.codex/auth.json` | `codex login` |
| Claude Code | `~/.claude.json`, `~/.config/claude-code/auth.json` | `/login` in Claude |
| Gemini CLI | `~/.gemini/settings.json` | Start `gemini`, select "Login with Google" |

The vault stores backups at: `~/.local/share/caam/vault/<tool>/<profile>/`

---

### Commands

#### Auth File Swapping (Primary)

| Command | Description |
|---------|-------------|
| `caam backup <tool> <name>` | Save current auth to vault |
| `caam activate <tool> <name>` | Restore auth (instant switch!) |
| `caam status [tool]` | Show which profile is active |
| `caam ls [tool]` | List all saved profiles |
| `caam delete <tool> <name>` | Remove a saved profile |
| `caam paths [tool]` | Show auth file locations |
| `caam clear <tool>` | Remove auth files (logout) |

#### Profile Isolation (Advanced)

For running multiple sessions simultaneously with fully isolated environments:

| Command | Description |
|---------|-------------|
| `caam profile add <tool> <name>` | Create isolated profile directory |
| `caam profile ls [tool]` | List isolated profiles |
| `caam profile delete <tool> <name>` | Delete isolated profile |
| `caam profile status <tool> <name>` | Show isolated profile status |
| `caam login <tool> <profile>` | Run login flow for isolated profile |
| `caam exec <tool> <profile> [-- args]` | Run CLI with isolated profile |

---

### Workflow for AI Agents

When working on projects that require AI coding tools:

1. **Check current status**: `caam status` to see which accounts are active
2. **Before long sessions**: Ensure you have backup profiles ready
3. **When hitting limits**: `caam activate <tool> <next-profile>` and continue

The switching is atomic and instant—no need to restart sessions or wait for browser flows.

---

### Advanced: Parallel Sessions

For running multiple accounts simultaneously:

```bash
# Create isolated profiles
caam profile add codex work
caam profile add codex personal

# Login to each (one-time)
caam login codex work
caam login codex personal

# Run with specific profile
caam exec codex work -- "implement feature X"
caam exec codex personal -- "review code"
```

Each profile has its own HOME/CODEX_HOME with passthrough symlinks to your real .ssh, .gitconfig, etc.

---

### ast-grep vs ripgrep (quick guidance)

**Use `ast-grep` when structure matters.** It parses code and matches AST nodes, ignoring comments/strings.

**Use `ripgrep` when text is enough.** Fastest way to grep literals/regex.

**Rule of thumb:**
- Need correctness or will **apply changes** → `ast-grep`
- Need raw speed or just **hunting text** → `rg`

---

### UBS Quick Reference

**Golden Rule:** `ubs <changed-files>` before every commit. Exit 0 = safe.

```bash
ubs file.go              # Specific file
ubs --only=go src/       # Language filter
```

---

You should try to follow all best practices laid out in the file GOLANG_BEST_PRACTICES.md

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
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
