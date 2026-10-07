# CAAM local dashboard

A local web UI for CAAM, built with Next.js. Each page uses the real loopback API:

- **Dashboard:** current tool logins, saved profiles, health needing attention, and coordinator reachability.
- **Profiles:** header search, provider filtering, identity and health, confirmed activation, and saved snapshots of the current live login.
- **Coordinators:** configured endpoints, transport, observed status, and connection guidance.
- **Activity:** recent recorded switches, logins, refreshes, errors, and cooldowns.
- **Usage:** recorded activations, errors, and completed-session time for the last hour, 24 hours, 7 days, or 30 days.
- **Settings:** the current API address, CAAM version, and Disconnect.

Provider controls derive from the API registry. The dashboard does not invent account counts, provider API-call totals, or quota consumption.

## Run locally

Use the Node and pnpm versions specified in `package.json`. Start the API in one terminal:

```bash
caam serve
```

Start the dashboard in another:

```bash
cd web/dashboard
pnpm install --frozen-lockfile
pnpm dev
```

The development and production scripts bind to `127.0.0.1` only. Open
[http://localhost:3000](http://localhost:3000). The default API address is
`http://127.0.0.1:7892`.

In another terminal, print a connection link:

```bash
caam serve --dashboard-url http://localhost:3000
```

The CLI link carries the API address and token in its URL fragment. The browser
does not send that fragment to a server. The dashboard removes it from the address
bar before validating the destination or sending an API request, including when
a new link is opened in an already loaded tab.

Alternatively, paste the token from `caam serve --show-token` into the connection
form. The token stays only in the current tab's memory, shared across the
dashboard's pages. It is never written to cookies or browser storage. Disconnecting
or reloading clears it; an old `caam.connection` local-storage entry is removed
rather than reused.

Only HTTP(S) API origins at `localhost`, `127.0.0.1`, or `[::1]` are accepted.
Requests use an Authorization header, omit cookies, refuse redirects, and disable
caching. The browser and `caam serve` must run on the same machine.

## Actions and recorded data

Activation requires confirmation naming the provider and profile. The dashboard
does not override cooldowns. A rejected action, including `success: false` in an
HTTP 200 response, remains an error; successful actions show any warnings and
refresh the affected data.

Saving a snapshot copies the selected provider's **current live login** into the
named saved profile. A new name is the default. Replacing an existing snapshot
requires a separate checkbox and a confirmation naming the snapshot and explaining
that its saved credentials will be replaced. Recovery snapshots are hidden by
default and cannot be activated or replaced from the dashboard. The API also
protects names created after the latest read.

Usage totals come from CAAM's recorded activity. A completed session is counted in
full when its completion is recorded, so it may have begun before the selected
interval. These are not provider API-call counts or quota percentages. Both Usage
and Activity distinguish an unavailable database from an available but empty
history.

Read-only data refreshes every 15 seconds and on demand. A failed read keeps any
previous result explicitly marked as potentially out of date. Period changes,
page changes, disconnects, and new sessions cancel obsolete requests and reject
late responses. Mutations are never retried automatically. After a lost
acknowledgement, inspect refreshed state before trying again.

## Validation

```bash
pnpm test
pnpm typecheck
pnpm lint
pnpm build
pnpm test:e2e
```

Vitest covers API destination and credential boundaries, CLI fragment handoff,
memory-only sessions, malformed responses, partial failures, stale reads and
actions, polling, activity availability, and explicit mutation outcomes.

Playwright preserves the route-mocked connection/action/error scenarios and
navigation checks. `e2e/live.spec.ts` also runs against a real `caam serve` with
synthetic credentials in an isolated temporary home. It builds CAAM from this
repository or uses `CAAM_BIN`, then verifies actual snapshot creation and
explicitly confirmed replacement without accessing the user's vault.

Build the dashboard before running Playwright. Install Chromium as needed with
`pnpm exec playwright install chromium`. Set `PLAYWRIGHT_CHROMIUM_EXECUTABLE`
only when intentionally using a preinstalled browser instead of Playwright's
pinned build.

For a production run, use `pnpm build` followed by `pnpm start`. Token refresh,
remote synchronization, bulk export, and account deletion remain CLI workflows.
