# caam web dashboard

A local web UI for caam, built with Next.js. It shows:
- which tools are logged in and with which profile;
- your saved profiles: activate one, or save the current login;
- the distributed-recovery coordinators;
- recent activity and cooldowns;
- per-profile errors.

Everything it shows and does goes through the local caam API (`caam serve`).
Nothing leaves your machine.

## Run it

```bash
caam serve                                       # the API, on 127.0.0.1:7892
cd web/dashboard && pnpm install && pnpm dev     # the dashboard, on localhost:3000
caam serve --dashboard-url http://localhost:3000 # prints a link that connects it
```

Open the printed link. It carries the API address and token in the URL
fragment, which the browser never sends to a server. The dashboard keeps the
token in this browser's local storage and removes it from the address bar.
Alternatively, paste the token from `caam serve --show-token` into the connect
form. Settings → Disconnect forgets the token.

The API only accepts requests from `localhost` origins and requires the
token on every call.

## Develop

```bash
pnpm typecheck
pnpm lint
pnpm test        # unit tests (vitest)
pnpm build       # production build; `pnpm start` serves it
pnpm test:e2e    # Playwright, against `pnpm start`
```

`e2e/live.spec.ts` builds caam from this repository, or uses `CAAM_BIN` if set.
It runs `caam serve` under a throwaway HOME holding a made-up Claude login,
then drives the dashboard against it: it connects through the
`--dashboard-url` link, saves the login from the UI, and checks that a wrong
token is refused. To use a preinstalled Chromium of another build than the
pinned Playwright, set `PLAYWRIGHT_CHROMIUM_EXECUTABLE`.
