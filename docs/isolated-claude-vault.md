# Save an isolated Claude login to the vault

`caam profile backup claude <isolated-name>` saves a registered isolated OAuth
login as a **new vault entry**, without clearing, activating, or refreshing the
currently active Claude login. Agents using that live login may keep running.
This is the bridge requested in issue #125.

## Workflow

```sh
caam profile add claude second
caam exec claude second
# Inside the isolated Claude session: /login, then /exit.

caam profile backup claude second
# Or choose a different NEW vault name:
caam profile backup claude second --as second-snapshot
```

The default destination is `vault/claude/second/`. Both stores now contain
`second`, so the normal account listing can show it as `vault+iso`. The source
profile remains in the isolated store, unchanged. No credential files or
`meta.json` need to be assembled manually.

Only when you intend to change the live account, activate the saved entry:

```sh
caam activate claude second
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
`/login` in the isolated profile before saving it.

The normal vault snapshot engine validates credentials and settings, checks
conflicting identity evidence, generates identity metadata, and writes private
files. The importer records the actual isolated source paths and publishes the
entire prepared snapshot atomically. Credential and settings read limits match
the existing vault backup limits. Normal activation retains its existing
account-settings policy rather than blindly replacing shared host settings.

## Failure and concurrency behavior

Existing vault names are never overwritten, even when the destination is an
empty directory or appears during preparation. Use `--as` with a new name;
there is deliberately no force-overwrite option on this command. This prevents
an old isolated copy from replacing a vault credential that has since rotated.

Missing required files, malformed JSON, incomplete credentials, conflicting
account identity, symlinked source files/directories, redirected profile metadata,
and a locked isolated session fail before publication. The isolated session must
be stopped, but unrelated live Claude sessions do not need to stop. A stale lock
must be resolved explicitly through the normal profile-lock workflow; this
command does not remove it.

The importer checks source file identities and bytes, absent optional files,
profile metadata, directory identities, and the isolated session lock again
before publication. An observed login, token-refresh, or directory change causes
an error with no new vault entry. Existing source credentials and vault entries
are left intact. These are optimistic checks, not a filesystem-wide lock against
arbitrary native writers: finish login/refresh and quiesce the isolated source
before taking its snapshot.

## OAuth refresh warning

A snapshot is **not a second independent login**. The isolated copy and vault
copy initially contain the same OAuth grant. Refreshing either can make the
other copy stale or invalidate its refresh token. This command never exchanges
or refreshes tokens, including for validation, and it does not promise to keep
the two copies synchronized.

After saving, keep the isolated session stopped while using the vault copy.
Do not resume the old isolated copy after the vault/live account has refreshed;
authenticate independently for concurrent use. To capture a later deliberately
completed isolated login, use another new vault name. For ordinary switching,
continue using `caam activate`, whose normal preservation logic handles outgoing
live credentials.

## Automation and command distinctions

```sh
caam profile backup claude second --as second-snapshot --json
```

JSON output includes `success`, `tool`, `profile`, `source_profile`, the resulting
vault `path`, and refresh warnings. Failures return a nonzero status with a JSON
`error`; credentials are never printed. This command bypasses startup token
warnings and data migration so saving an isolated account does not incidentally
probe or mirror the live keychain.

`caam backup` snapshots the current native login. `caam auth import` creates an
isolated profile. `caam profile backup` creates a vault entry from an existing
isolated Claude login. It currently supports Claude OAuth profiles only.
