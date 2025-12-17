# caam - Coding Agent Account Manager

![Release](https://img.shields.io/github/v/release/Dicklesworthstone/coding_agent_account_manager?style=for-the-badge&color=bd93f9)
![Go Version](https://img.shields.io/github/go-mod/go-version/Dicklesworthstone/coding_agent_account_manager?style=for-the-badge&color=6272a4)
![License](https://img.shields.io/badge/License-MIT-50fa7b?style=for-the-badge)
![Build Status](https://img.shields.io/github/actions/workflow/status/Dicklesworthstone/coding_agent_account_manager/ci.yml?style=for-the-badge&logo=github)

> **Instant auth switching for AI coding tool subscriptions. Switch accounts in under a second when you hit usage limits.**

---

### Quick Install

```bash
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/coding_agent_account_manager/main/install.sh?$(date +%s)" | bash
```

---

## TL;DR

`caam` manages authentication files for AI coding CLIs, enabling **sub-second account switching** when you hit usage limits on "all you can eat" subscriptions.

**The Problem:**
You have multiple AI coding subscriptions (Claude Max, GPT Pro, Gemini Ultra). When you hit the 5-hour usage limit, switching accounts means:
1. Run `/login` or equivalent
2. Wait for browser to open
3. Sign out of current Google/GitHub account
4. Sign in to new account
5. Authorize the app
6. Wait for redirect...

**That's 30-60 seconds of friction, multiple times per day.**

**The Solution:**
```bash
# Hit your limit? One command, instant switch:
caam activate claude jeff-gmail-2
```

**How it works:** Each CLI stores OAuth tokens in specific files. `caam` backs them up with labels and restores them on demand. No proxies, no env vars, no pseudo-HOME directories for simple switching—just atomic file copies.

---

## The Core Experience

### Lightning-Fast Account Switching

```bash
# One-time setup: login normally, then backup
claude                              # login via /login
caam backup claude jeff-gmail-1     # save auth files

# When you hit limits (takes < 1 second):
caam activate claude jeff-gmail-2   # restore different account
claude                              # continue working immediately
```

The switching is **atomic and instant**. No browser flows, no waiting, no interruption to your flow state.

### Multi-Tool Support

| Tool | Subscription | Auth Files |
|------|-------------|------------|
| **Claude Code** | Claude Max ($100/mo) | `~/.claude.json`, `~/.config/claude-code/auth.json` |
| **Codex CLI** | GPT Pro ($200/mo) | `~/.codex/auth.json` |
| **Gemini CLI** | Google One AI Premium | `~/.gemini/settings.json` |

### Two Operating Modes

**1. Vault Profiles (Simple Switching)**
Swap auth files in place. One account active at a time, instant switching.
```bash
caam backup claude work-account
caam activate claude personal-account
```

**2. Isolated Profiles (Parallel Sessions)**
Run multiple accounts simultaneously with full directory isolation.
```bash
caam profile add codex work
caam profile add codex personal
caam exec codex work -- "implement feature X"
caam exec codex personal -- "review code"
```

---

## Architecture

`caam` is deliberately simple. No daemons, no databases, no network calls. Just file operations.

```
┌─────────────────────────────────────────────────────────────────┐
│                        YOUR SYSTEM                               │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│   ~/.claude.json ◄──────┐                                        │
│   ~/.config/claude-code/ │         ┌──────────────────────────┐  │
│                          │         │  ~/.local/share/caam/    │  │
│   ~/.codex/auth.json ◄───┼─ caam ──┤  vault/                  │  │
│                          │         │    claude/               │  │
│   ~/.gemini/settings.json│         │      jeff-gmail-1/       │  │
│                          │         │      jeff-gmail-2/       │  │
│                          │         │    codex/                │  │
│                          │         │      work-account/       │  │
│                          │         └──────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘

backup:   AUTH FILES ──copy──► VAULT
activate: VAULT ──copy──► AUTH FILES
```

### Why This Works

OAuth tokens in these files are **bearer tokens**—whoever has the file has the access. The CLI tools don't verify device identity beyond what's in the token. By swapping files, you're effectively "becoming" that logged-in session.

### Vault Structure

```
~/.local/share/caam/
├── vault/                          # Saved auth profiles
│   ├── claude/
│   │   ├── jeff-gmail-1/
│   │   │   ├── .claude.json        # Backed up auth
│   │   │   ├── auth.json           # From ~/.config/claude-code/
│   │   │   └── meta.json           # Timestamp, source paths
│   │   └── jeff-gmail-2/
│   │       └── ...
│   ├── codex/
│   │   └── work-account/
│   │       └── auth.json
│   └── gemini/
│       └── personal/
│           └── settings.json
│
└── profiles/                       # Isolated profiles (advanced)
    └── codex/
        └── work/
            ├── profile.json        # Profile metadata
            ├── codex_home/         # Isolated CODEX_HOME
            │   └── auth.json
            └── home/               # Pseudo-HOME with symlinks
                ├── .ssh -> ~/.ssh
                └── .gitconfig -> ~/.gitconfig
```

---

## Command Reference

### Auth File Swapping (Primary Use Case)

| Command | Description |
|---------|-------------|
| `caam backup <tool> <name>` | Save current auth files to vault |
| `caam activate <tool> <name>` | Restore auth files from vault (instant switch!) |
| `caam status [tool]` | Show which profile is currently active |
| `caam ls [tool]` | List all saved profiles in vault |
| `caam delete <tool> <name>` | Remove a saved profile |
| `caam paths [tool]` | Show auth file locations for each tool |
| `caam clear <tool>` | Remove auth files (logout state) |

**Aliases:** `caam switch` and `caam use` work like `caam activate`

### Profile Isolation (Advanced)

For running multiple accounts **simultaneously** with full environment isolation:

| Command | Description |
|---------|-------------|
| `caam profile add <tool> <name>` | Create isolated profile directory |
| `caam profile ls [tool]` | List isolated profiles |
| `caam profile delete <tool> <name>` | Delete isolated profile |
| `caam profile status <tool> <name>` | Show isolated profile status |
| `caam login <tool> <profile>` | Run login flow for isolated profile |
| `caam exec <tool> <profile> [-- args]` | Run CLI with isolated profile |

---

## Workflow Examples

### Daily Workflow

```bash
# Morning: Check what's active
caam status
# claude: jeff-gmail-1 (active)
# codex:  work-openai (active)
# gemini: personal (active)

# Afternoon: Hit Claude usage limit
caam activate claude jeff-gmail-2
# Activated claude profile 'jeff-gmail-2'

claude  # Continue working immediately with new account
```

### Initial Setup

```bash
# 1. Login to first account using normal flow
claude
# Inside Claude: /login → authenticate with jeff-gmail-1@gmail.com

# 2. Backup the auth
caam backup claude jeff-gmail-1

# 3. Clear and login to second account
caam clear claude
claude
# Inside Claude: /login → authenticate with jeff-gmail-2@gmail.com

# 4. Backup that too
caam backup claude jeff-gmail-2

# 5. Now you can switch instantly forever!
caam activate claude jeff-gmail-1  # < 1 second
caam activate claude jeff-gmail-2  # < 1 second
```

### Parallel Sessions (Advanced)

```bash
# Create isolated profiles
caam profile add codex work
caam profile add codex personal

# Login to each (one-time, uses browser)
caam login codex work      # Opens browser for work account
caam login codex personal  # Opens browser for personal account

# Run simultaneously in different terminals
caam exec codex work -- "implement auth system"
caam exec codex personal -- "review PR #123"
```

---

## How Profile Detection Works

`caam status` determines the active profile by **content hashing**:

1. Read current auth files (e.g., `~/.claude.json`)
2. Compute SHA-256 hash of contents
3. Compare against hashes of all vault profiles
4. Match = that profile is active

This means:
- Profiles are detected even if you switched manually
- No hidden state files that can get out of sync
- Works correctly after system restarts

---

## Supported Tools Deep Dive

### Claude Code (Claude Max)

**Subscription:** Claude Max ($100/month for 5x usage)

**Auth Files:**
- `~/.claude.json` - Main authentication token
- `~/.config/claude-code/auth.json` - Secondary auth data

**Login Command:** Inside Claude Code, type `/login`

**Notes:** Claude Max has a 5-hour rolling usage window. When you hit it, you'll see rate limit messages. Switch accounts to continue.

### Codex CLI (GPT Pro)

**Subscription:** GPT Pro ($200/month unlimited)

**Auth Files:**
- `~/.codex/auth.json` (or `$CODEX_HOME/auth.json`)

**Login Command:** `codex login`

**Notes:** Respects `CODEX_HOME` environment variable for custom locations.

### Gemini CLI (Google One AI Premium)

**Subscription:** Google One AI Premium ($20/month)

**Auth Files:**
- `~/.gemini/settings.json`

**Login Command:** Start `gemini`, select "Login with Google"

---

## FAQ

**Q: Is this against terms of service?**

A: No. You're using your own legitimately-purchased subscriptions. `caam` just manages local auth files—it doesn't share accounts, bypass rate limits, or modify API traffic. Each account still respects its individual usage limits.

**Q: What if the tool updates and changes auth file locations?**

A: Run `caam paths` to see current locations. If they change in a tool update, we'll update `caam`. File an issue if you notice a discrepancy.

**Q: Can I use this on multiple machines?**

A: Auth files often contain machine-specific identifiers (device IDs, etc.). Backup and restore on each machine separately. Don't copy vault directories between machines.

**Q: What's the difference between vault profiles and isolated profiles?**

A:
- **Vault profiles** (`backup`/`activate`): Swap auth files in place. Simple, instant, one account active at a time per tool.
- **Isolated profiles** (`profile add`/`exec`): Full directory isolation with pseudo-HOME. Run multiple accounts simultaneously in parallel terminals.

**Q: Will this break my existing sessions?**

A: Switching profiles while a CLI is running may cause auth errors in the running session. Best practice: switch accounts before starting a new session, not during.

**Q: How do I know which account I'm currently using?**

A: Run `caam status`. It shows the active profile for each tool based on content hash matching.

---

## Installation

### One-liner (Recommended)

```bash
curl -fsSL "https://raw.githubusercontent.com/Dicklesworthstone/coding_agent_account_manager/main/install.sh?$(date +%s)" | bash
```

### From Source

```bash
git clone https://github.com/Dicklesworthstone/coding_agent_account_manager
cd caam
go build -o caam ./cmd/caam
sudo mv caam /usr/local/bin/
```

### Go Install

```bash
go install github.com/Dicklesworthstone/coding_agent_account_manager/cmd/caam@latest
```

---

## Tips

1. **Name profiles descriptively:** Use `jeff-gmail-1`, `work-openai`, `personal-gemini`—not `account1`, `backup`
2. **Backup before clearing:** `caam backup claude current && caam clear claude`
3. **Check status often:** `caam status` shows what's active across all tools
4. **Use --backup-current flag:** `caam activate claude new --backup-current` auto-saves current state

---

## Contributing

Issues and PRs welcome at [github.com/Dicklesworthstone/coding_agent_account_manager](https://github.com/Dicklesworthstone/coding_agent_account_manager).

## License

MIT
