# Grok billing probes do not enroll accounts

`caam limits` reads Grok billing through a temporary, access-token-only
`GROK_HOME`. The quota read must not renew the grant in that copy or start an
interactive account login. This addresses the browser-launch failure reported
in #124 without changing the live credential or silently refreshing a saved
snapshot.

## Before launching Grok

The probe checks the exact captured credential bytes before it creates the
staging home or launches a process. Each credential entry needs a nonempty
access token and an absolute expiry more than 15 minutes in the future.
The native issuer/client-keyed layout and flat credentials are supported.
RFC3339 dates and integer Unix seconds/milliseconds are recognized. An
`expires_in` value alone cannot establish when a saved token expires.

Missing, malformed, expired, near-expiry, or unknown-expiry credentials produce
an unavailable quota result. A fresh sibling entry cannot rescue an incomplete
or stale entry: the probe must not guess which one the native client will use.
The 15-minute margin is conservative room for early renewal and clock skew,
not evidence that an untested Grok release uses that exact renewal threshold.

Renew the login in its owning native home, then retry the quota read. Do not
refresh the temporary billing copy or assume that a vault snapshot is the
owner of a live refresh grant. This change does not automatically synchronize
an old vault snapshot from another home.

## The permitted protocol

After initialization, CAAM requires ACP protocol version 1 and exactly one
advertised `cached_token` method of the default/agent type. Missing, malformed,
terminal-only, duplicate, or incompatible authentication advertisements stop
before `authenticate`. There is no fallback to browser or terminal login.
The credential deadline is checked again after initialization.

Cached-token authentication has a five-second deadline inside the existing
25-second probe deadline. Cancellation terminates the direct child and closes
both protocol pipes, including when a descendant retains them. A completed
handshake proceeds only through `session/new` and `_x.ai/billing`; CAAM sends
no model prompt. Credential material is never included in error messages.

The stage still strips refresh tokens and client secrets. Successful and
failed probes clean up their staging directory without writing the source
home, vault snapshot, or parent process environment.

## Browser defense and limits

The child receives a private failing `BROWSER` command and PATH-first stubs
for common browser openers. These are defense in depth, not an operating-system
sandbox: a client that invokes an absolute system opener can bypass them.
Known unusable credentials are rejected before any Grok process starts, and
unsupported authentication stops after initialization, but CAAM cannot promise
the behavior of an arbitrary native binary before it speaks the protocol.

Tests use synthetic credentials and a fake ACP process to check launch refusal,
handshake refusal, exact read-only RPC order, deadlines, cleanup, and opener
suppression. They do not authenticate against xAI or certify behavior of a
particular installed Grok release. Windows guard scripts and environment
handling compile cross-platform; native Windows behavior requires its own run.
