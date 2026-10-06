# Claude settings lifecycle

Claude's `settings.json` mixes continuously edited workflow policy with account
configuration. CAAM does not treat it as either an entirely global file or an
unconditional profile snapshot.

## Vault switches

On a Claude vault activation, the live machine's non-auth settings win, including
absence: deleting a permission rule, auto-mode rule, plugin, MCP server or hook
must not resurrect it from an older profile. CAAM does not union permission lists.
Model and effort choices, `permissions`, `autoMode`, MCP configuration, hooks,
plugin enablement and unknown non-auth top-level settings follow the live file.

Authentication-bearing settings come only from the selected profile, including
when they are absent from that profile. The built-in profile-scoped keys are
`apiKeyHelper`, `apiKey`, `api_key`, `awsAuthRefresh`, `awsCredentialExport`,
`forceLoginMethod`, `forceLoginOrgUUID` and `otelHeadersHelper`. The entire `env`
object is profile-scoped by default: arbitrary environment variables may select
credentials, cloud accounts, endpoints or helper behavior.

Backups remain complete snapshots for recovery. With no live settings file, a
snapshot can bootstrap settings; an existing empty object `{}` instead means
that the operator deliberately removed shared settings. A profile with no
settings snapshot cannot inherit the previous account's authentication fields.
Malformed settings fail activation rather than being copied over the live file.

The same vault restore implementation is used by `activate`, automatic rotation,
robot/API callers, the TUI, wrap and workspace switches. Shared workflow fields
in `.claude.json`, including user/project MCP configuration and trust decisions,
are also preserved without copying the outgoing account's identity/session state.

## Configuration and account-specific exceptions

Edit the **JSON** configuration at `$XDG_CONFIG_HOME/caam/config.json`, or
`~/.config/caam/config.json` when `XDG_CONFIG_HOME` is unset. This is not the YAML
SPM configuration. Add a `claude_settings` object alongside existing fields:

```json
{
  "claude_settings": {
    "mode": "shared",
    "profile_keys": ["mcpServers", "hooks"],
    "shared_env_keys": ["EDITOR", "ANTHROPIC_MODEL"]
  }
}
```

`shared` is the default. `profile_keys` names additional **whole top-level keys**
that must follow the selected account instead. The example makes MCP servers and
hooks private: use this when their commands, headers, URLs or nested environment
values contain account-specific secrets. CAAM cannot infer whether an arbitrary
hook command or MCP connection belongs to a particular account. Do not include
these keys in `profile_keys` when you intend them to be shared workflow policy.

`shared_env_keys` is an explicit opt-in for individual non-auth environment
variables. Known credential, authentication and backend/routing variables are
rejected. Do not opt in a custom variable that carries a secret or changes the
credential source. `env` itself cannot appear in `profile_keys` because it already
has conservative profile scope and its shared exceptions are handled separately.

For intentionally separate policy/configuration per account, use:

```json
{
  "claude_settings": { "mode": "per-profile" }
}
```

This selects the target profile's full settings document, including plugin
settings. Logout still removes authentication rather than deleting shared
workflow policy. Mode and key overrides survive normal config saves such as
changing aliases, defaults or workspaces. Invalid modes or unsafe environment
exceptions are rejected on both load and save.

## Isolated and shallow launches

Isolated and shallow Claude sessions prepare shared settings before native
execution. The required preparation hook aborts on malformed policy; optional
skill sharing remains a separate operation. Every settings destination is
validated before applying a batch, and edits made after preparation cause an
error instead of being overwritten.

An isolated profile uses the XDG `xdg_config/claude-code` directory when it
contains authentication. A profile with only legacy authentication continues to
use `home/.claude`; new profiles use XDG. Environment exports, imported files,
health checks and keepalive discovery follow that same choice. A healthier or
newer credential in an ignored directory cannot override the selected login.
Known conflicting account IDs are reported as an error. Missing settings receive
shared policy only, so preparing them cannot introduce a helper in an ignored
directory and change the selected authentication store. Older profile-local
`home/.claude.json` state is migrated into the selected legacy config directory
before launch when needed.

Shallow profiles keep `.claude/settings.json` private alongside credentials and
session state. `--from-vault claude/<profile>` imports that account's saved
settings while taking shared workflow policy from the current user. Existing
links to the canonical user's settings are detached without copying that user's
helpers or credential environment. Unknown links are rejected. Shared `.claude.json`
preferences and per-project approvals also follow deletions, including approvals
for projects removed from the canonical file; private history and runtime state
are retained.

`shallow-spawn --no-sync-config` skips policy refresh, but still requires a valid,
private settings file and repairs recognized shared links. `shallow-spawn
--print-env` and `caam env` are read-only. The latter supports safely quoted shell
output and a data-only JSON representation with `--json` (`set` and `unset`
fields); it does not trigger profile preparation or legacy store migration.

For canonical user paths, an explicit `CLAUDE_CONFIG_DIR` selects
`settings.json`, `.claude.json`, `.credentials.json` and `auth.json` in that
directory. Even a missing directory is authoritative: backup, discovery and
health cannot borrow the default user's credentials or policy. With no override,
the usual `~/.claude` settings/credentials and `~/.claude.json` state paths apply.

## Existing divergent profiles

CAAM cannot determine which of several old snapshots contains the intended
permissions. Before the first switch, reconcile desired non-auth settings into
the live file, keep credential-bearing fields out of that reconciliation, and
retain backups. Subsequent vault switches preserve that live policy. Do not copy
one entire account's settings over every vault snapshot: that can duplicate
`apiKeyHelper` or environment-based authentication across accounts.
