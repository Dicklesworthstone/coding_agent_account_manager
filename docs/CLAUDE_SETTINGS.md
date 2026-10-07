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

Updating an existing Claude backup also records authentication sources that are
now absent. It removes obsolete `.credentials.json`, `auth.json` and Desktop
cache snapshots from that profile instead of accumulating old fallbacks. A live
Desktop file containing preferences but no token cache is likewise auth-absent;
those preferences are never copied into the vault. Present raw credentials keep
their verbatim backup path. Keychain-only OAuth is mirrored before absence is
classified, and malformed sources fail before any existing vault files change.
Other profiles and immutable system backups are not rewritten. Backup still
requires a complete current auth source; policy-only files cannot erase a saved
login by pretending to be a new one. These checks do not make backup crash-atomic
or lock native writers; do not run backup while a login is being rewritten.

The same vault restore implementation is used by `activate`, automatic rotation,
robot/API callers, the TUI, wrap and workspace switches. Shared workflow fields
in `.claude.json`, including user/project MCP configuration and trust decisions,
are also preserved without copying the outgoing account's identity/session state.

### Switching authentication sources

An account that authenticates through a helper or settings environment must not
inherit the outgoing account's OAuth fallback. After validating the complete
target, a vault restore removes live `.credentials.json` and `auth.json` when
the selected profile has no snapshot for that source. Present credential
snapshots still use the existing same-account freshness protection.

Vault restoration stages incoming raw OAuth credentials, mixed settings,
Desktop caches and private recovery copies for retired credentials as one
batch, regardless of file-set order. A staging failure cannot first replace
settings or remove the outgoing OAuth token. Freshness decisions use the
captured outgoing identity before any mixed-state document is changed. Exact
credential bytes are retained, including when a newer same-account live grant
must win over its stale snapshot.

Settings and incoming credentials are installed before retirement; a later
installation, retirement or validation failure rolls back earlier writes where
still safe. Recreated native logins are never deliberately overwritten by
credential rollback: recovery into a retired path requires that path to remain
absent. Otherwise the error reports the retained private copy
(`credentials.json.rollback.*`). Replaced files likewise require both the
installed file identity and bytes to match before they can be rolled back.

Desktop's `oauth:tokenCache` and `oauth:tokenCacheV2` fields also come exclusively
from the target, including absence. Switching to an account without a Desktop
cache removes the outgoing caches while preserving live Desktop preferences
such as theme and window placement. This does not copy a whole Desktop config.

When the default macOS keychain bridge is active, a target without an OAuth
mirror also removes the old login-keychain item. A later keychain read therefore
cannot resurrect the outgoing OAuth identity. Retirement preflight is read-only,
accepts the explicitly captured keychain mirror, and rejects detected changes to
the source snapshot, live credential or keychain before deleting the file.
Unknown credential symlinks and nonregular files fail rather than being removed.
Explicit config directories and a disabled keychain bridge remain isolated from
the default login item.

### Keychain publication and recovery

Keychain publication is the last step of the recoverable restore batch, not a
separate unguarded write after file recovery copies have been discarded. CAAM
checks the installed file bundle, captured source snapshots and outgoing
keychain item before publication. A cached status lookup cannot substitute an
older disk mirror for the authoritative grant during this switch.

A rejected keychain write or removal triggers file rollback. If a command
reports failure after installing the selected item, CAAM restores the captured
outgoing item only when it can still recognize its own selected result, then
verifies that recovery. A different native login is left untouched. When the
keychain outcome or recovery cannot be confirmed, the error reports an original
`keychain.rollback.*` copy when one existed. These copies are private (0600) and
contain credentials; treat them like the vault, never paste them into reports,
and retain a newer native login rather than blindly replaying old tokens.

Successful publication releases the recovery copies. Restore's final check is
read-only: it cannot replay an older mirror over a native rotation that occurred
after publication. These keychain writes are not compare-and-swap operations, so these are
optimistic guards and returned-error recovery, not a lock against native writers
or a guarantee of crash-atomic switching.

### Project policy versus project session state

A `.claude.json` project record is not a single shared setting. Only known
workflow fields follow the live file: `allowedTools`, `hasTrustDialogAccepted`,
`mcpServers`, `mcpContextUris`, `enabledMcpjsonServers`, `disabledMcpjsonServers`,
`hasClaudeMdExternalIncludesApproved` and
`hasClaudeMdExternalIncludesWarningShown`. The selected account keeps its own
project history, session IDs, usage/cost records and unknown project caches.
Switching profiles never imports those records from the outgoing account.

Removing an approval, MCP registration or entire project from the canonical
file removes that shared policy from the next activation, without removing the
selected profile's private history. An empty or `null` projects map likewise
carries no shared approvals. Malformed non-object project records stop a shared
refresh before it writes configuration. Set `profile_keys: ["projects"]` in the
JSON policy below when the entire project map, including nested MCP secrets,
must remain account-specific. `profile_keys: ["mcpServers"]` alone scopes only
the top-level MCP map, not registrations nested under `projects`.

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

### New shallow profiles

An implicit real-home `.claude.json` seed obeys the same account classification
at creation, before the first launch. It excludes known credentials, helpers,
account caches and every configured `profile_keys` field. Its `env` object is
private by default; only explicitly approved `shared_env_keys` are seeded.
This prevents a fresh profile from inheriting host-only MCP headers, hook
commands or project settings that the operator marked private. Other existing
onboarding/preference bootstrap behavior is retained in shared mode.

In `per-profile` mode, a fresh implicit seed carries only the installation
`userID` and `hasCompletedOnboarding` markers, unless those keys are themselves
configured private. It does not adopt the host's workflow policy as the new
account's private policy. An explicitly selected `.claude.json` snapshot remains
an account-owned source, not an implicit host seed.

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

Vault, isolated and shallow refreshes use one `.claude.json` merge policy,
including permissions, auto-mode, model/effort choices, hooks, MCP configuration
and the account-specific exceptions above. Shallow refresh prepares both
`settings.json` and `.claude.json` before writing either document. Changes to a
destination after preparation are rejected, and a semantically unchanged legacy
file is not reformatted on every spawn. Change reports identify keys rather than
printing potentially credential-bearing values.

Prepared updates also capture their shared-policy and selected-account source
files, including absence. If a source is edited, removed or created before
application, activation fails instead of reinstalling revoked permissions or
stale authentication settings. Batch updates check all inputs before the first
write, and each file is checked again after staging. These are optimistic edit
checks: they do not lock the native CLI or guarantee a multi-file transaction.

### Settings-batch failure recovery

Vault restore batches, isolated/shallow settings batches, and the settings
portion of logout stage every replacement and rollback copy before installing
any document. A staging failure leaves the destination documents unchanged.
On a later installation, input-validation or vault-keychain publication failure,
the batch attempts to restore its earlier writes in reverse order, including
restoring an originally absent file to absence.

Rollback never deliberately overwrites a detected native edit or replacement:
both the installed file identity and its bytes must still match the batch's
write. If recovery is unsafe or fails, the error includes the preserved original
file's path (`settings.json.rollback.*`). Regular recovery copies are mode 0600;
they can contain credentials. Review them locally alongside the current files,
retain the newest login, and do not paste them into issue reports or blindly
restore possibly revoked permission rules or authentication. Successful batches
and completed rollbacks remove their temporary copies.

This is recovery from returned I/O errors, not crash atomicity or a lock against
Claude Code. Another process can observe files between individual renames or
between file installation and keychain publication. Stop concurrent native
logins before retrying a failed switch; inspect any reported recovery paths
when a native edit or unavailable keychain prevented complete recovery.

`shallow-spawn --no-sync-config` skips policy refresh, not identity isolation.
It validates both private `settings.json` and `.claude.json` before repairing
any recognized shared settings link. A symlinked, host-hard-linked or malformed
`.claude.json` stops launch. Existing valid private documents are not reformatted
or refreshed, missing private files remain absent, and canonical policy content
is not read unless a settings link needs repair. Path privacy is rechecked when
applying the prepared validation, even if the file's bytes have not changed.

`shallow-spawn --print-env` and `caam env` are read-only. The latter supports safely quoted shell
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

New private-key exceptions cannot establish the provenance of fields already
copied into an older profile. Review those profile-owned values against that
account's trusted settings; CAAM must not guess that an existing private helper,
MCP header or hook secret belongs to the currently selected host account.
