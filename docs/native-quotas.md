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
caam run grok --precheck -- "review this diff"
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

`quota_status` is `ok`, `degraded`, or `unavailable`. Native rows require a
valid measured primary window and valid reported additional windows before
they can be used for routing. A genuine measured 0% remains usable.

Grok's proto3 billing response can omit zero-valued usage scalars after a
period resets. CAAM infers a measured 0% only when `creditUsagePercent`,
`credit_usage_percent`, `used`, `monthlyLimit`, and `monthly_limit` are all
absent and a complete, valid billing period contains the fetch time. The
start is inclusive and the end is exclusive. Every supplied period bound
must parse and agree, including alternate field names; fallback dates cannot
repair an incomplete or malformed current period. The measured window retains
the period's reset time and duration for usage-aware selection.

Present usage fields, including nulls and incomplete used/limit pairs, cannot
trigger this zero inference. Missing, malformed, conflicting, future,
inverted, or expired period bounds also leave omitted usage unmeasured.
Existing valid explicit percentages and used/limit ratios remain measured.

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

`caam run grok|cursor --precheck` reads the active account's quota from the
same vault it would switch within. A reported limit stage, or a measured
window at the threshold, counts as near the limit. The switch target must be
another account that passes the same measured-and-unspent eligibility check
that `next --usage-aware` uses and is itself below the threshold. If no other
account qualifies, the run proceeds on the current account with a stderr
notice. When the current account's quota cannot be measured, the
precheck prints a notice and does not switch. It never treats unknown quota as
capacity.

The `caam precheck` session planner still fetches live usage for Claude and
Codex only. This integration does not replace main's Cursor login, expiry,
refresh, or path handling.

## Validation

Regression coverage includes numeric parsing, transport isolation, private
Grok staging, cancellation, credential source selection, batch failure
propagation, monitor output, ranking, and usage-aware eligibility.

Grok reset-zero regressions exercise the ACP reader, batch fetching, ranking,
the `next --usage-aware` rotation adapter, and an actual precheck switch from
an exhausted profile to a freshly reset one. Synthetic CLI responses keep
these tests independent of live provider accounts. Malformed usage and ACP
errors remain ineligible for quota-based selection.

Run these checks with the repository's pinned Go 1.26.8 toolchain:

```sh
go test ./internal/usage ./internal/rotation ./internal/monitor ./cmd/caam/cmd
go test -race ./internal/usage ./internal/rotation ./internal/monitor ./cmd/caam/cmd
go test ./internal/authfile ./internal/profile ./internal/provider/cursor
go vet ./internal/usage ./internal/rotation ./internal/monitor ./cmd/caam/cmd
```
