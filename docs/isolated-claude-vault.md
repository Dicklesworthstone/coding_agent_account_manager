# Save an isolated Claude login to the vault

`caam backup claude <new-vault-name> --from-profile <isolated-name>` saves a
registered isolated OAuth login as a **new vault entry**, without clearing,
activating, or refreshing the currently active Claude login. Agents using that
live login may keep running. This is the bridge requested in issue #125.

`caam profile backup claude <isolated-name> [--as <new-vault-name>]` uses the same
locked source capture and atomic vault publication. There is no second store or
separate credential-import implementation behind these two interfaces.

## Workflow

```sh
caam profile add claude second
caam exec claude second
# Inside the isolated Claude session: /login, then /exit.

caam backup claude second-snapshot --from-profile second
# Equivalent:
# caam profile backup claude second --as second-snapshot
```

The destination is `vault/claude/second-snapshot/`. The source profile remains
in the isolated store, unchanged. No credential files or `meta.json` need to be
assembled manually. The destination name may equal the isolated source name
provided it is not already in the vault; the normal listing can then show it
as `vault+iso`. With `caam profile backup`, the source name is the default vault
name when `--as` is omitted.

Only when you intend to change the live account, activate the saved entry:

```sh
caam activate claude second-snapshot
```

Activation uses the normal vault switch/restore path. The source paths recorded
in metadata are provenance, **not** restore destinations.

## Which files are saved?

The authoritative source is the registered profile's
`xdg_config/claude-code/` directory, which `caam exec` selects through
`CLAUDE_CONFIG_DIR`. The snapshot includes `.credentials.json`, `.claude.json`,
and, when present, `settings.json` and `auth.json`. It does not combine a fresh
XDG login with stale files under the profile's legacy `home/.claude`, or with the
host's login, Desktop cache, or keychain.

A complete OAuth credential (access and refresh tokens) and an account identity
in `.claude.json` are required. Thus a credentials-only `caam auth import` profile
with a stub `.claude.json` is not silently treated as a completed login. Finish
`/login` in the isolated profile before saving it. The registered profile must
also explicitly select `auth_mode: oauth`. An API-key, unknown-mode, or
mode-less profile is rejected before its credential files are read, even if
it retains a complete OAuth cache from an earlier login. Create and authenticate
an OAuth profile rather than relabeling another mode's cached credentials.

The captured `settings.json` and `.claude.json` must also select that stored
OAuth login. A helper, another API key/token, a named Anthropic profile,
federation settings, an enabled cloud-provider selector, a config-directory or
API-endpoint override, custom authentication headers, or a non-subscription
`forceLoginMethod` causes the import to fail before vault preparation. These
settings can override a complete cached OAuth login even when the registered
profile still says `auth_mode: oauth`. Relevant selectors must have the expected
string types; malformed or ambiguous selections fail closed. The importer never
executes helpers, prints their values, refreshes tokens, or silently strips the
settings to manufacture a different login.

Ordinary workflow settings and other providers' environment variables remain
unchanged. Empty overrides and explicitly disabled cloud switches (`0`,
`false`, `no`, or `off`, case-insensitively) are allowed. Environment selector
names are checked case-insensitively so a snapshot cannot select a different
account when moved to Windows. These checks concern the saved profile files,
not external project/managed policy or the caller's ambient credentials.

The normal vault snapshot engine validates credentials and settings, checks
conflicting identity evidence, generates identity metadata, and writes private
files. The importer records the actual isolated source paths and publishes the
entire prepared snapshot atomically. Credential and settings read limits match
the existing vault backup limits. Normal activation retains its existing
account-settings policy rather than blindly replacing shared host settings.

## Failure and concurrency behavior

Existing vault names are never overwritten, even when the destination is an
empty directory or appears during preparation. Choose another positional vault
name (or use `--as` with `caam profile backup`); there is deliberately no
force-overwrite option for isolated imports. This prevents an old isolated copy
from replacing a vault credential that has since rotated.

An explicitly empty or whitespace-only `--from-profile` is an error, not a
request to fall back to the live login. Unsupported tools and invalid source
or destination names also fail without probing the live account.

Missing required files, malformed JSON, incomplete credentials, conflicting
account identity, symlinked source files/directories, redirected profile metadata,
and a locked isolated session fail before publication. The isolated session must
be stopped, but unrelated live Claude sessions do not need to stop. A stale lock
must be resolved explicitly through the normal profile-lock workflow; these
commands do not remove it.

The profile store holds its cross-process mutation lock throughout capture and
publication. The importer also checks source file identities and bytes, absent
optional files, profile metadata, directory identities, and the isolated session
lock again before publication. An observed login, token-refresh, or directory
change causes an error with no new vault entry. Existing source credentials and
vault entries are left intact. Native writers that bypass CAAM's locking still
require a quiescent isolated source: finish login/refresh before taking its
snapshot.

## OAuth refresh warning

A snapshot is **not a second independent login**. The isolated copy and vault
copy initially contain the same OAuth grant. Refreshing either can make the
other copy stale or invalidate its refresh token. Neither command exchanges or
refreshes tokens, including for validation, or promises to keep the two copies
synchronized.

After saving, keep the isolated session stopped while using the vault copy.
Do not resume the old isolated copy after the vault/live account has refreshed;
authenticate independently for concurrent use. To capture a later deliberately
completed isolated login, use another new vault name. For ordinary switching,
continue using `caam activate`, whose normal preservation logic handles outgoing
live credentials.

## Automation and command distinctions

```sh
caam backup claude second-snapshot --from-profile second --json
# Equivalent:
caam profile backup claude second --as second-snapshot --json
```

Both isolated interfaces emit the same JSON fields: `success`, `tool`, `profile`,
`source_profile`, the resulting vault `path`, and refresh warnings. Runtime
failures return a nonzero status with one JSON `error` result, without appending
Cobra usage or duplicate stderr errors; credentials are never printed. They
bypass startup token warnings and data migration so saving an isolated account
does not incidentally probe or mirror the live keychain.

`caam backup` **without** `--from-profile` retains its normal native-login backup
behavior. `caam auth import` creates an isolated profile, not a vault entry.
`caam backup --from-profile` and `caam profile backup` create vault entries from
existing isolated Claude logins. They currently support Claude OAuth profiles
only.
