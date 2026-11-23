# Dependency Upgrade Log

**Date:** 2026-01-18  |  **Project:** coding_agent_account_manager  |  **Language:** Go

## Summary
- **Updated:** Minor version updates via go mod tidy
- **Skipped:** 1 (go-json-experiment/json requires Go 1.25+)
- **Failed:** 0
- **Needs attention:** 0

## Notes

- `go get -u ./...` attempted to pull go-json-experiment/json which requires Go 1.25 (project uses Go 1.24.4)
- Ran `go mod tidy` to clean up dependencies
- Project builds successfully with current module versions

## Verification

- `go build ./...` - Build successful

---

# Dependency Upgrade Log

**Date:** 2026-11-22  |  **Project:** coding_agent_account_manager  |  **Language:** Go

## Summary
- **Updated:** 4 modules (1 direct, 3 indirect); 1 indirect module dropped
- **Held:** chromedp/cdproto (and go-json-experiment/json, which they pin), modernc.org/libc and modernc.org/memory
- **Failed:** 0

## Updated
- `modernc.org/sqlite` v1.62.0 -> v1.63.0
- `github.com/dlclark/regexp2/v2` v2.2.1 -> v2.13.2 (indirect, via glamour/chroma)
- `github.com/xo/terminfo` v0.0.0-20220910002029-abceb7e1c41e -> v1.3.1 (indirect, via lipgloss/colorprofile)
- `github.com/charmbracelet/x/exp/slice` pseudo-version -> v0.1.0 (indirect)
- `golang.org/x/exp` dropped by `go mod tidy` (no longer needed)

## Held
- `github.com/chromedp/chromedp` v0.14.2 and `github.com/chromedp/cdproto`: v0.17+ redesigned the API, and
  `internal/agent/browser.go` drives the browser OAuth flow through it. That is a migration, not an update.
- `github.com/go-json-experiment/json`: stays at the pseudo-version chromedp v0.14.2 was built against.
- `modernc.org/libc` v1.80.1 and `modernc.org/memory` v1.12.1: modernc.org/sqlite must run against the exact
  libc version it was generated for. v1.63.0 requires libc v1.80.1, so `go get -u` moving libc to v1.82.x is
  pinned back.

## Performance
- sqlite workload (insert, point lookup, aggregate, update; 9 interleaved rounds, median):
  v1.62.0 total 2924 ms / CPU 2360 ms; v1.63.0 total 2889 ms / CPU 2316 ms. No regression.
- glamour render with chroma v2.27.0 (300 renders of a code-heavy document, 7 interleaved rounds, median):
  regexp2/v2 v2.2.1 total 2601 ms / CPU 3780 ms; v2.13.2 total 2368 ms / CPU 3302 ms. About 9% faster.

## Verification
- `go build ./...`, `go vet ./...`, gofmt clean; `go test -p 4 -count=1 ./...`: 57 of 59 test packages pass. The two
  failures are flaky at the base commit too:
  - `internal/keepalive` `TestRunStopsNativeDescendantsBeforeUnlocking/timeout`: 3/10 failures at base, 1/10 with
    the update, run alone.
  - `internal/installscript` `TestInstallerTemporaryDirectoryLifecycle`: fails under the full parallel run at base
    and with the update; passes when run alone.
- `govulncheck ./...`: no vulnerabilities, before and after.
