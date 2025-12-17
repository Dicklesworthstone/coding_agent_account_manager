# caam - Coding Agent Account Manager

**Instant auth switching for AI coding tool subscriptions.**

When you hit the 5-hour usage limit on your Claude Max account, switch to another account in under a second. No browser login flows, no waiting—just instant auth file swapping.

## The Problem

You have multiple "all you can eat" AI coding subscriptions:
- 5x Claude Max accounts
- 4x GPT Pro accounts
- 3x Gemini Ultra accounts

When you hit usage limits, you need to switch accounts. The official way:
1. Run `/login` (or equivalent)
2. Wait for browser to open
3. Sign out of current Google/GitHub account
4. Sign in to new account
5. Authorize the app
6. Wait for redirect...

**That's 30-60 seconds of friction, multiple times per day.**

## The Solution

caam backs up auth files after you login once, then restores them instantly:

```bash
# One-time setup: login normally, then backup
claude   # login via /login
caam backup claude jeff-gmail-1

# Later, when you hit limits:
caam activate claude jeff-gmail-2   # < 1 second!
claude   # Now using the other account
```

## How It Works

Each AI CLI stores OAuth tokens in specific files:

| Tool | Auth Files |
|------|-----------|
| Codex CLI | `~/.codex/auth.json` |
| Claude Code | `~/.claude.json`, `~/.config/claude-code/auth.json` |
| Gemini CLI | `~/.gemini/settings.json` |

caam simply:
1. **Backs up** these files to `~/.local/share/caam/vault/<tool>/<profile>/`
2. **Restores** them when you want to switch
3. **Tracks** which profile is currently active via content hashing

No proxies, no env vars, no pseudo-HOME directories. Just file copies.

## Installation

```bash
# From source
go install github.com/user/caam/cmd/caam@latest

# Or build locally
git clone https://github.com/user/caam
cd caam
go build -o caam ./cmd/caam
```

## Quick Start

```bash
# 1. Login to your first account using the tool's normal flow
claude
# Inside Claude: /login, authenticate with jeff-gmail-1@gmail.com

# 2. Backup the auth
caam backup claude jeff-gmail-1

# 3. Clear auth and login to second account
caam clear claude
claude
# Inside Claude: /login, authenticate with jeff-gmail-2@gmail.com

# 4. Backup that too
caam backup claude jeff-gmail-2

# 5. Now you can switch instantly!
caam activate claude jeff-gmail-1   # Switch to account 1
caam activate claude jeff-gmail-2   # Switch to account 2
```

## Commands

### Auth File Swapping (Primary)

| Command | Description |
|---------|-------------|
| `caam backup <tool> <name>` | Save current auth to vault |
| `caam activate <tool> <name>` | Restore auth (instant switch!) |
| `caam status [tool]` | Show which profile is active |
| `caam ls [tool]` | List all saved profiles |
| `caam delete <tool> <name>` | Remove a saved profile |
| `caam paths [tool]` | Show auth file locations |
| `caam clear <tool>` | Remove auth files (logout) |

Aliases: `caam switch` and `caam use` work like `caam activate`

### Profile Isolation (Advanced)

For running multiple sessions simultaneously with fully isolated environments:

| Command | Description |
|---------|-------------|
| `caam profile add <tool> <name>` | Create isolated profile directory |
| `caam profile ls [tool]` | List isolated profiles |
| `caam profile delete <tool> <name>` | Delete isolated profile |
| `caam profile status <tool> <name>` | Show isolated profile status |
| `caam login <tool> <profile>` | Run login flow for isolated profile |
| `caam exec <tool> <profile> [-- args]` | Run CLI with isolated profile |

Example:
```bash
# Create isolated profiles for parallel sessions
caam profile add codex work
caam profile add codex personal

# Login to each
caam login codex work
caam login codex personal

# Run with specific profile
caam exec codex work -- "implement feature X"
caam exec codex personal -- "review code"
```

## Supported Tools

### Codex CLI (GPT Pro)
- **Subscription**: GPT Pro ($200/mo unlimited)
- **Auth file**: `~/.codex/auth.json` (or `$CODEX_HOME/auth.json`)
- **Login command**: `codex login`

### Claude Code (Claude Max)
- **Subscription**: Claude Max ($100/mo for 5x usage)
- **Auth files**: `~/.claude.json`, `~/.config/claude-code/auth.json`
- **Login command**: `/login` inside Claude Code

### Gemini CLI (Gemini Ultra)
- **Subscription**: Google One AI Premium ($20/mo)
- **Auth file**: `~/.gemini/settings.json`
- **Login command**: Start `gemini`, select "Login with Google"

## Vault Structure

```
~/.local/share/caam/vault/
├── codex/
│   ├── work-account/
│   │   ├── auth.json
│   │   └── meta.json
│   └── personal-account/
│       └── ...
├── claude/
│   ├── jeff-gmail-1/
│   │   ├── .claude.json
│   │   ├── auth.json
│   │   └── meta.json
│   └── jeff-gmail-2/
│       └── ...
└── gemini/
    └── ...
```

## Profile Isolation Structure

For advanced users running parallel sessions:

```
~/.local/share/caam/profiles/
├── codex/
│   └── work/
│       ├── profile.json          # Profile metadata
│       ├── codex_home/           # CODEX_HOME for this profile
│       │   └── auth.json         # Isolated auth
│       └── home/                 # Pseudo-HOME (with symlinks)
│           ├── .ssh -> ~/.ssh    # Passthrough to real HOME
│           └── .gitconfig -> ~/.gitconfig
└── claude/
    └── personal/
        ├── profile.json
        ├── home/                 # HOME for Claude
        │   └── .claude.json
        └── xdg_config/           # XDG_CONFIG_HOME
            └── claude-code/
                └── auth.json
```

## Example Workflow

```bash
# Morning: Start with account 1
caam status
# codex: active profile 'work-1'
# claude: active profile 'jeff-gmail-1'
# gemini: active profile 'main'

# Afternoon: Hit Claude usage limit
caam activate claude jeff-gmail-2
# Activated claude profile 'jeff-gmail-2'
#   Run 'claude' to start using this account

claude   # Continue working with new account

# End of day: Switch back
caam activate claude jeff-gmail-1
```

## Tips

1. **Name profiles by account**: Use `jeff-gmail-1`, `work-openai`, etc.—not `account1`
2. **Backup before clearing**: `caam backup claude current && caam clear claude`
3. **Check status often**: `caam status` shows what's active
4. **Use --backup-current**: `caam activate claude new --backup-current` auto-saves

## FAQ

**Q: Is this against terms of service?**
A: No. You're using your own legitimately-purchased subscriptions. caam just manages local auth files—it doesn't share accounts, bypass rate limits, or modify API traffic.

**Q: What if the tool updates and changes auth file locations?**
A: Run `caam paths` to see current locations. If they change, we'll update caam.

**Q: Can I use this on multiple machines?**
A: Auth files are machine-specific (contain device IDs, etc.). Backup/restore on each machine separately.

**Q: What's the difference between vault profiles and isolated profiles?**
A:
- **Vault profiles** (backup/activate): Swap auth files in place. Simple, fast, one account at a time.
- **Isolated profiles** (profile add/exec): Full directory isolation. Run multiple accounts simultaneously in parallel.

## License

MIT
