# Native Grok and Cursor quota reads

`caam limits` and the usage monitor can read Grok and Cursor quota data for
saved profiles. `caam next --usage-aware` can use those reads when selecting
an account. This integration adapts the native readers from PR #107 while
retaining main's newer Cursor path resolver and canonical vault layout.

```sh
caam limits grok --profile work --source vault
caam limits cursor --profile work --source isolated --format json
caam limits cursor --rank earliest-reset-headroom --format json
caam next cursor --usage-aware --dry-run
caam monitor
```

Omit `--dry-run` from `next` to activate the selected account. The quota reads
themselves do not activate an account, send model prompts, or overwrite the
selected credential. Grok billing uses a temporary private copy of auth.json
and the native ACP billing method; the copy is removed after the read.
Cursor uses authenticated read RPCs on its dashboard service. Neither reader
implements token refresh or interactive login.

## Credential isolation

Vault credentials use `<vault>/<provider>/<profile>/auth.json`. A stale
`xdg-auth.json` is not an alternate account source. For an isolated Cursor
profile, quota lookup uses the same platform resolver and private HOME layout
as the provider on main, not the caller's ambient XDG or APPDATA settings.
An invalid selected credential does not cause a search in a different tree.
Usage-aware activation reads the same vault and candidate set it will restore.

Grok receives only the selected credential in its staging home, not arbitrary
client configuration, hooks, or MCP servers. Ambient API keys and configuration
roots are removed or replaced. Cancellation closes inherited protocol pipes
as well as terminating the direct child. Provider error text is not repeated,
because even an opaque error can contain a credential.

Cursor's production endpoint is fixed rather than read from an ambient
endpoint override. Credential-bearing redirects are not followed. Requests
have deadlines and bounded response bodies, including with injected clients.

## Measured quota versus metadata

`quota_status` is `ok`, `degraded`, or `unavailable`. Missing or malformed
numbers do not become measured zeroes. Native rows require a valid measured
primary window and valid reported additional windows before they can be used
for routing. A genuine measured 0% remains usable.

Cursor grant amounts, model restrictions, and expiry times are retained as
metadata, not summed into an account-wide utilization percentage. A reported
limit stage prevents automatic usage-aware selection. A failed period read
is not replaced by an apparently generous grant balance.

The monitor renders unknown native usage as unknown rather than 0%. Batch
fetching also gives incomplete native rows an explicit error for older
recommendation and forecast consumers while retaining the detailed status.
Drain and availability ranking both check row eligibility before considering
headroom. Reset-based ranking still requires a future reset time.

`next --usage-aware` rejects native accounts with missing, invalid, failed,
or exhausted quota before any selection algorithm runs. This applies to a
sole candidate and to the forced round-robin retry. When every quota read
fails, selection fails before activation instead of falling back to an
unmeasured account. Selection without `--usage-aware` and legacy Claude/Codex
fallback behavior are unchanged.

This integration does not add native support to `precheck` or `run --precheck`,
and does not replace main's Cursor login, expiry, refresh, or path handling.
Those PR changes require separate reconciliation with the newer provider code.

## Validation

Regression coverage includes numeric parsing, transport isolation, private
Grok staging, cancellation, credential source selection, batch failure
propagation, monitor output, ranking, and usage-aware eligibility.

The implementation was exercised in an offline Go 1.23.2 focused harness:
23 tests passed with `-race`, and `go vet` passed. The harness uses unchanged
native production source files and selected complete declarations. Non-native
provider and log dependencies are substitutes that panic if executed. It is
not a full repository build: the repository requires Go 1.26.8, and the local
runner has neither that toolchain nor dependency network access. Full command
and monitor integration tests were added to the repository but were not run
in that harness. No live provider account was queried.

With the pinned toolchain and dependencies available, run:

```sh
go test ./internal/usage ./internal/rotation ./internal/monitor ./cmd/caam/cmd
go test ./internal/authfile ./internal/profile ./internal/provider/cursor
go vet ./internal/usage ./internal/rotation ./internal/monitor ./cmd/caam/cmd
```
