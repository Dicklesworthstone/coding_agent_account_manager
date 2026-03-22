# Changelog

All notable changes to **caam** (Coding Agent Account Manager) are documented here.

Format: [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). This project uses [Semantic Versioning](https://semver.org/).

Repository: <https://github.com/Dicklesworthstone/coding_agent_account_manager>

---

## [Unreleased] — since v0.1.10 (2026-01-21)

> 67 commits on `main` after v0.1.10, spanning 2026-01-21 through 2026-03-21.

### New Providers

- **OpenCode** and **Cursor CLI** provider support ([`8da7fa0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8da7fa00fed1240aa664e05955fd8de83bce0966)) — resolves #8 and #9

### Coordinator & Orchestration

- Compaction reminder injection logic for long-running sessions ([`f12ecb6`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f12ecb663c403cc6e1d5202e4243a8a7c31940e6))
- Compaction reminder config and `trace-login` command ([`f35556d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f35556d51d7a6a2fa7199402bf3c6a6821eb9b44))
- Structured logging with `run_id` correlation and token redaction ([`ce6a06e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ce6a06ecd176e445ddec18d7689c88e6a4d7d4df))
- Improved coordinator state management ([`9d296b9`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9d296b91cc6f1bb26c4172f57f7ceb2b2e94d73d))
- Hardened coordinator/agent API behavior ([`4896870`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/489687067776f42a5eb1f445e17cfe2b19adbab2))

### API Server

- HTTP API server with enhanced browser control and TUI improvements ([`e5f10c6`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e5f10c6f65d1208c1bf5e13f45e09ffc9e980467))

### TUI

- Major improvements to profile and detail panels ([`0dc4c8b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0dc4c8ba969fb4b6bbd613436685ed75f58e15fa))
- Progress indicator component ([`d8cf246`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d8cf246fb09439334d519c2ac304067586e5fa8e))
- Updated dialogs, coordinator, and CLI tweaks ([`3d98146`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3d98146f2fedd8941a3b5fa57f3250ce6384e4af))

### CLI Commands

- `caam detect` — discover installed AI coding agents ([`cc64c9c`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/cc64c9c20bd3a625fabb52f02a330ae55524aed4))
- `caam wezterm recover` — batch rate-limit recovery ([`3822027`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/38220272af2458a723593555e19464daacc34056))
- Alias/rename workflow for auto-generated profiles ([`a03e7f4`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a03e7f4055e3106f9667b730d8d0d6e489808c6b))

### Claude Provider Hardening

- Disable Claude email extraction from opaque tokens (CLAUDE-001/002) ([`5a9bebd`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5a9bebd7a23d47ca4286bcc5be82cbdcee1fe748))
- Disable Claude token refresh (CLAUDE-006) — tokens are managed by Claude Code itself ([`1ca325c`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1ca325c7fecf1850f514305cc2dda88ef21aa5b0))
- Show `n/a` for unavailable Claude identity ([`1649777`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1649777d6675dc85a34d3c9a574f3484e0c0dcb9))
- ANSI-normalized login detection and resume cooldown ([`84183bf`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/84183bfc7692fa31d606b55f5218ccbe38852a68))
- Claude limitation regression tests and doc fixes ([`9861cc1`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9861cc11735c6e290d5da9f49f837f37b371b4d6))

### Installer & Distribution

- Hardened `install.sh` with gum UX, idempotency, and rollback ([`11cd441`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/11cd4416da79318940b2a14f74f80dd17f1fad48))
- Tailscale discovery with resilient parsing and warnings ([`aefe257`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/aefe2573980dfe6e68b954691c367bdd625ea887))
- ACFS notification workflow for installer changes ([`1589a72`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1589a72b923d4fc0bc478abcc9b567073ff2a083))

### Profile Detection

- Stable identity hashing for profile detection ([`c4d61bc`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c4d61bc6d81faa225c71e966ad5372ad003dd36c))

### Bug Fixes

- Bypass nested PTY wrapper for Claude Code provider ([`ce6585a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ce6585a50b22dc0c68eb5fd4ff5a63e43347d3b2))
- Session logging added to Claude PTY bypass; reorder checks ([`a61a341`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a61a341487466ea8bea412e2adf55cf63ba56e18))
- Rename `oauth_credentials.json` to `oauth_creds.json` for Gemini CLI compatibility ([`4c0c6d1`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/4c0c6d1a089110e43838a2e4744c31cd1ca205ef))
- Extract JWT claims from nested OpenAI namespace objects ([`0c7d2da`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0c7d2da9599abf1a7e027299ce1e415c560a079a), #6)
- PTY: handle poll HUP/error states cleanly ([`9c034d3`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9c034d381162e3354e383f65065f9e0e396d2089))
- PTY: keep ReadLine responsive under context cancel ([`21f9adc`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/21f9adcf50221e4ca6892af7d68993b8a13c433f))
- Drain PTY buffer on exit to prevent stdout/stderr loss ([`b50886b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/b50886b89cfd42ec77261a15644b5774b574efea))
- Improved OAuth account selector robustness; reduced token logging ([`17b65f4`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/17b65f4a6e01115084adc11f193d9cdc30928610))
- Improved auth token health checks and expiry detection ([`ac3ab4c`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ac3ab4c6f6adf3e04514c109e36f01aa8866f73b))

### CI & Tooling

- Align Go toolchain, fix golangci-lint version, harden flaky PTY tests ([`9d21baa`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9d21baa0b9ff013fad3ec239886f817215b0e2d5), [`9443281`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/944328117170794fa6afaaa8dbf0e661ef4cad0b))
- Improved CI workflow configuration for better test reliability ([`2414831`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/241483137fe4fadef8a373382d4ed7bf35c600cd))

### License

- Updated to MIT with OpenAI/Anthropic Rider ([`5c76214`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5c76214b1d5842bdf20804beef03627f7be6944b))

### Testing

- Watcher E2E tests with auth-change fixtures ([`b4f60b0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/b4f60b04f15399647076ca1dbe01abf554b8d451))
- Discovery parser fixture corpus ([`0b3b46f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0b3b46fd073fe90052335762529a925418cdfa5a))
- Identity extraction fixture tests ([`e9c7fbf`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e9c7fbf6ac2915471694fd966f91c335f4010687))
- Browser automation unit tests ([`a74c723`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a74c723b7016fd16def8e2faf8eee840fd3186f7))

---

## [v0.1.10] — 2026-01-21

> Release: <https://github.com/Dicklesworthstone/coding_agent_account_manager/releases/tag/v0.1.10> (Latest)
>
> Tags v0.1.6 through v0.1.10 were rapid-fire fixes to the GoReleaser pipeline on the same day as v0.1.5. Only v0.1.10 produced a successful published release. Tags v0.1.6–v0.1.9 are GoReleaser iteration artifacts with no independent content.

### Release Pipeline Fixes (v0.1.6 through v0.1.10)

- Write release manifest outside `dist/` directory ([`d6ee7a9`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d6ee7a9c549a8d4e23ed61a73a4e4e4de6f3d73a)) — **v0.1.10**
- Fix goreleaser manifest generation ([`3b920ad`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3b920adf200c944501b242a3fdbe2413986d70cb)) — v0.1.9
- Fix goreleaser manifest heredoc ([`ac0cbe2`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ac0cbe207aad1114e015301b2bf9246a3b52803e)) — v0.1.8
- Fix goreleaser manifest hook YAML ([`f11506d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f11506deaa12a33e4a28ec98392b68d79b76ecfc)) — v0.1.7 (Draft)
- Fix goreleaser manifest hook ([`f87d071`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f87d07134a8cc30bb2ee67150fe16c94eccf6953)) — v0.1.6

### Release Artifacts (v0.1.10)

- `caam_0.1.10_darwin_amd64.tar.gz`
- `caam_0.1.10_darwin_arm64.tar.gz`
- `caam_0.1.10_linux_amd64.tar.gz`
- `caam_0.1.10_linux_arm64.tar.gz`
- `caam_0.1.10_windows_amd64.zip`
- `SHA256SUMS` + `SHA256SUMS.sig` (cosign-signed)
- `release-manifest.json`

---

## [v0.1.5] — 2026-01-21

> Tag only (no published GitHub Release). 39 commits since v0.1.4.

### TUI Overhaul

- Enhanced TUI with `pick` command, WezTerm integration, and theme system ([`a342813`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a34281324a3529321f6e07fc1d2fa8992cb7b9af))
- Loading spinner/animation for async operations ([`87469d8`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/87469d8a031f7b94c7edf36ce0a84749177f9f1a))
- Color status severity indicators ([`0b39e82`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0b39e824f90211832e11d428fa3625370c19b326))
- Navigation breadcrumbs for sub-views ([`46f1891`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/46f189162b385fba64070254cb2a0dea55f44a09))
- Glamour markdown rendering for help view ([`9604f6d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9604f6d2e214ad2eba829aed791e42a66802281a))
- SPM config panel and enhanced TUI styles ([`37edbdc`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/37edbdc9f3be4039d934f5697223c24143a5fc50))
- Account status display in authfile panel ([`8a93157`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8a93157baf7ef5e59c5d1e053a46bb5c93b92180))
- TUI preferences config ([`6e78b77`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6e78b77f323f1460a03ecb19dbffd8a7a4144f45))

### Release Engineering

- Cosign-signed release assets ([`f3bd102`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f3bd102d2c6103257aea1248bafbf93b1f9d2247))
- Enhanced release workflow and documentation ([`2dd286e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2dd286eeee18ac4c4db1e91363086d21b72e2bbf))

### Bug Fixes

- Flush rate limit buffer ([`7b0ebb2`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/7b0ebb272d065a146ae9385d4e7fb458ec32a9d0))
- Standardize warning message capitalization ([`9272814`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/927281444be3ab68ebf0d14061137c00f09f315a))
- Use global DB singleton; add `UseGlobalEnv` option ([`a6b97ab`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a6b97ab3052432a53c4f5901fc9174618e993982))
- 64KB buffer limits to stream observers; optimize detector locking ([`e17b501`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e17b501d240812ecf8aee0cd5e0aecbdcc191622))
- 64KB buffer limit to lineObserverWriter ([`ed28379`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ed28379588f4c4d3d19a01f91f51b01902d69166))

### Testing

- WezTerm CLI test harness ([`e691c9f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e691c9fd451b7a69d4ef6eebb4bce771aeeb15a4))
- Pane discovery unit tests and matcher fixtures ([`12ca517`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/12ca5172f53d153cf39e639ab433fb7bc4325ebf))
- Spinner test coverage improvements ([`69c413d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/69c413d81b68e2907ec8ca122c9a1c58e9a379da))

### Documentation

- Prioritize Homebrew/Scoop installation methods in README ([`1a2b6db`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1a2b6dbf6d64e0f834922642f84edd6c321cba28))
- Claude auth/session assumptions inventory ([`24a7110`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/24a71104934fb2722d9e901c3477d1f625e2cfb5))

---

## [v0.1.4] — 2026-01-15

> Release: <https://github.com/Dicklesworthstone/coding_agent_account_manager/releases/tag/v0.1.4>
>
> 26 commits since v0.1.3. Subsumes v0.1.2 and v0.1.3 content. Security hardening and reliability focus.

### Security Hardening

- Guard against path traversal in passthrough ([`c8accef`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c8accefed84f5e927b04b86d49dd8dbca6818290))
- Atomic write in `setup.go`; fix TOCTOU race in `CleanStaleLock` ([`ac556cb`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ac556cb1bc2db1286795b2906977f682e15ea69c))
- Agent code review: race conditions, security, and resource leaks ([`9260ef1`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9260ef1e5c7ba45a5ecee9cd0e2e0532ae17cae9))
- Data durability, safety, and security fixes ([`c48b810`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c48b8101f8daea0f63cc46f601d44265d3267f90))
- Error handling and safety improvements ([`5966ed7`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5966ed7a812c135db8041f481d8401c3846491b9))

### Features

- Session tracking and configurable cooldown in SmartRunner ([`315a2da`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/315a2da93608258ef83f2b4d4cc17c2350a8917f))
- Non-interactive distributed setup with `--yes` and `--print-script` flags ([`35a6aee`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/35a6aeeb998420d0d0570005c8c0b759ac916450))

### Bug Fixes

- Support `+` character in profile names; improve file handling ([`041f7a3`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/041f7a356e9c229956994be9d34735deeae3a45e))
- Fix race condition in `ForceRefresh` via atomic status check ([`7570334`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/75703346cfc40e50b0110c0eba1d01f56cd20457))
- Prevent false positive login detection with negative patterns ([`5adb109`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5adb109c8109a426d1dc4601d6b7bada559a099c))
- Atomic writes in `mergeJSONFile` to prevent corruption ([`5443095`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/544309528dadc7b5d7664cef6bc23370f1bfffd2))
- Refine prediction engine and Gemini refresh ([`924f30a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/924f30a8245bc8df94f592d2816fcd36e6ffe3ed))
- Fix unix select build for Go 1.25 ([`f56fcc7`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f56fcc7c2e0404107c8cf1b1db5ee03a8e27e30c)) — v0.1.2
- TUI PID file write fix ([`ae087b2`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ae087b2e65748061e4ebcd5cfdcd0faf241421fd))

### Refactoring

- Replace `unix.Select()` with `SetReadDeadline()` for PTY polling ([`b074887`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/b0748877f1a465e7a6fe5b2ebedcfba293da3ef6))
- Remove unused `WritePIDFile` function ([`d1d0640`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d1d06404d41c23bf85adbce7f03c74cb46ed7773))
- Clean up regex and remove duplicate provider helpers ([`30756fc`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/30756fce75dee383f29fa41506c42949398fc1a5))

### Minor Tags Within This Range

- **v0.1.2** (2026-01-13) — Fix unix select build for Go 1.25 ([`f56fcc7`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f56fcc7c2e0404107c8cf1b1db5ee03a8e27e30c)). Published release.
- **v0.1.3** (2026-01-14) — Close bead caam-m9rk ([`2b4c04a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2b4c04a590f60022d5d5189778bda391caed081e)). Published release. Adds non-interactive distributed setup.

---

## [v0.1.1] — 2026-01-13

> Tag only (no published GitHub Release). 176 commits since v0.1.0. Massive feature expansion covering distributed infrastructure, robot mode, usage intelligence, and comprehensive test coverage.

### Distributed Auth Recovery

- Full distributed auth recovery system ([`535e15f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/535e15f940d08f28ee89d8f10d0c95c1782773c4))
- Tmux backend support as coordinator fallback ([`26485d0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/26485d035ed73af36f400e27232b71e934a50b97))
- Auth agent/coordinator config support ([`bdbb9b0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/bdbb9b0a522e4e69ce3a169257ca1fdd6c6bec31))
- Notification delivery system ([`fbcd8f5`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/fbcd8f5a30dec0c8af2ffd46256c0175861529da))

### Robot Mode

- Robot mode CLI for coding agents ([`345e354`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/345e354641893bd9df70454725d8236c8f6b497b))
- Major overhaul with quick-start guide and new commands ([`7dbfac3`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/7dbfac3397e968fc52008924413dc1f9e529b335))

### Auth Pool & Daemon

- Authentication pool management package ([`52ecaae`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/52ecaaebb2b5fb3d7e45d3adaae16913280691de))
- Auth pool integrated with daemon ([`6c96921`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6c96921313e9d970c81092547abc99f74cd08160))
- Improved auth pool monitoring and health tracking ([`e6c083a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e6c083abb225adfe040d0151533690356f9645c1))
- Automatic vault backup scheduler ([`9e70253`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9e702533484ad082d72fc3b586439b0e8e65d0aa))
- Backup scheduling configuration ([`ba078be`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ba078bef351b7b64a8eeffdb2755622488631616))
- SIGHUP reload support ([`1c9e216`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1c9e21621a90c4151bbb8ab2c3eb359bf94cd7af))

### Usage Intelligence

- PTY controller for command injection ([`6146867`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/61468676c8ba8ea9fd07d32ab88ca0d9ea6b229f))
- Real-time session token tracking ([`80ceaea`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/80ceaeaf919b29956ff00b9a2341cd554ed63b00))
- Burn rate calculation ([`8605b06`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8605b064387c0a7fb3d1c744e98bc0ec0169d2bd))
- Depletion prediction ([`9cf8a8d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9cf8a8d9c926574620dae33f895e5839a9d52053))
- Smart recommendations, forecasting, and precheck ([`fb3e54a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/fb3e54a92a7d26afb5b25be7c1b73849edef264b))
- Real-time rate limit tracking for smart rotation ([`5d3a39f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5d3a39f723840afaa2c592afbbe0bb1b07cd3d52))
- Cost tokens subcommand for token cost analysis ([`e74d252`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e74d2521bf07c624ccec4ab6e7da3261c87576b9))
- Cost tracking for AI CLI wrap sessions ([`003104b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/003104bd4719bbf1a5c256582bc48ffcadd907c1))

### Monitoring & Observability

- `caam monitor` command with multiple output formats and live monitoring ([`dc6ac68`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/dc6ac68c2624942cad776b683ee2d30a346d37d4))
- `caam precheck` command for session planning ([`dffa7d8`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/dffa7d87391d7b193df55f95ea6b6242281d5177))
- Predictive alerts and monitor renders ([`90205df`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/90205dfad52becbbea55e80547c1e614f92cf4de))

### SmartRunner

- SmartRunner implementation for intelligent CLI wrapping ([`1dd929f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1dd929f36c7f439b4f7c2e316636ca1d1aa69152))
- Unified configuration system ([`898e7c6`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/898e7c6ea8bbf34d9c843333f4bc61ddf224a868))

### Log Scanning

- Claude JSONL log scanner ([`aea54ce`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/aea54ce0628fcce5f1eca2f571086f3875d8d051))
- Internal logs package structure ([`3b54bdb`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3b54bdb94161fe44374558df9b9eaee4631ba81b))

### CLI Commands

- `caam validate` command for token validation ([`84342ed`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/84342ed45d9cd43c469a26dbe12a58f209b98518))
- `doctor --validate` flag ([`fe93b03`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/fe93b03f9387252ed37aa81e07bd2ed857fdba70))
- `ls --tag` filter for profile listing ([`254611e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/254611e35719b80863385982eaf8d526282025bc))
- Provider-specific login handlers ([`2812258`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2812258c2cb963e4350bbfef524e90f8e856986e))
- `CAAM_HOME` environment variable support ([`49f7c16`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/49f7c160f2521a98c11093fda0c0041071f744a4))

### TUI

- Profile editing and sync panel dialogs ([`6001b4b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6001b4b79e3d6151e760834f574222a32780915e))

### Sync & Token Freshness

- Improved token freshness detection and SSH discovery ([`2ee2189`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2ee21898c280dd3e5c15b855e3ede5a2afcdcf25))

### Distribution

- GoReleaser auto-publishing to Homebrew and Scoop ([`0c5d09d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0c5d09d02f6780d72d437afe5dd454dc26429e21))

### Bug Fixes

- Race condition in token refresh and cleanup ([`cf388b3`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/cf388b33de8933bd5dbd0d835278a47e8f646b0d))
- Prevent double-close panics; improve deploy robustness ([`20bcc3a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/20bcc3a617951e4827d51eae7d07f28fc85403e9))
- Content verification to prevent race condition in refresh ([`7a16831`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/7a168317cb3fdb10b2a88a1b64af2bc2bedb9e1c))
- Double-check active profile before restore ([`119c2fc`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/119c2fcc3999993765bda57b45a24f5daa2e96fd))
- Correct Claude Code credential file paths ([`06aa4be`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/06aa4bed08b6809676391ff4f33fa5f63e6c1155))
- Allow `@` character in profile names for email-based naming ([`d62b40f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d62b40fae049d2269c5dca55c0756ce4f55d52dc))
- Prioritize `id_token` over `access_token` in Codex extraction ([`21d6801`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/21d6801a1a165b1345341ad6d2f193ca93f4180d))
- Replace deprecated `strings.Title` with `capitalizeFirst` ([`d2babf1`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d2babf199e2ee028a5070288334a08d9d82eed4f))
- PTY goroutine timeout replaced with `syscall.Select` for reads ([`9f75ecd`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9f75ecd7560f7337217f6e71bd91d3465d0982c3))
- Reduce lock contention in authwatch `SaveState` ([`8d4c452`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8d4c452aaa188dcfe36113d3b6445225bccd425e))
- Fix race conditions in authpool concurrency ([`0b27813`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0b27813834cdd14027873fc7d04dd49a6c35e070))
- Cross-platform and reliability improvements ([`2d99bd5`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2d99bd5f563f646d7c34ab0d1db13ef599a469b3))
- Proactive token expiry warnings with path fix ([`4cba461`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/4cba4611ab7aea29a7f0e6c4090be04622dd4a31))
- Profile locking for transient profiles ([`8bc20c2`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8bc20c27e58b3b2f82ad90d36f282d81994723aa))
- EPERM handling in process detection and log file leak ([`0a087b5`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0a087b56c4569f5118a13f853ce8d2020eeacfac))

### Testing

- E2E daemon lifecycle tests ([`aa29220`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/aa29220dd471a34201fc80dd71440aa794019e4c))
- E2E profile lifecycle tests ([`c81efe5`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c81efe5d955dfb5f4aff218d4a0586ff159e5eef))
- SmartRunner E2E test ([`bc00759`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/bc00759b7e653f254d8757a1af219d9371e6da12))
- AuthPool + SmartRunner + Prediction integration tests ([`5ffaa7a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5ffaa7a7e23551fcd9f1e9b88ac6078b55d4116e))
- Monitor and precheck E2E tests ([`edf72ed`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/edf72edb020e8b3c359eb7fd3b1533bc0b573d66))
- Stealth package coverage: 38% to 95% ([`6ed88fe`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6ed88fe5988a56e6cc28b134326fc4bb32f0f438))
- Health package coverage: 73.9% to 85.6% ([`b63f449`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/b63f449ceb493ce6c243a46adde6860718006203))
- Authwatch package coverage: 63.8% to 84.2% ([`f6be56d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f6be56d1e1461f7d37b55f7374272ae390fcb5f4))
- Watcher package coverage: 61.7% to 84.7% ([`1d373ee`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1d373eef22694fa8ffc501725672cf25b151436d))
- Wrap package coverage: 72.0% to 77.1% ([`6351b0b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6351b0bbca68dc8f5a69da628b5ae952fc3ab69f))
- Warnings package coverage: 49.4% to 92.1% ([`1de1c48`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1de1c48d0aaaf94c06e8d08776ba4037cf4ec9ab))
- Daemon package coverage: 57% to 76.2% ([`9577ac4`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9577ac4687ddc82fd79c6e6ead5dda6aff752ee4))
- JWT parsing and burn rate benchmarks ([`1883c03`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1883c0386d22a970a13986210129e3b0e2daf7a5))
- CI safety checks preventing tests from corrupting real auth files ([`89ce145`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/89ce145157cdf997b47c74b11e876247919b4eac))
- ExtendedHarness with JSON export and perf regression detection ([`4212848`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/4212848717cfbe589fc4862435d0cddf1940f620))
- TUI component unit tests ([`194e8ee`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/194e8ee1ddf8f9461c3805ac20ef2052369590f2))

---

## [v0.1.0] — 2025-12-20

> Release: <https://github.com/Dicklesworthstone/coding_agent_account_manager/releases/tag/v0.1.0>
>
> Initial public release. 358 commits from project inception (2025-12-17) to release.

### Core Account Switching

- **Sub-100ms account switching** for Claude Code, Codex CLI, and Gemini CLI
- **Vault-based profile management** — `backup`, `activate`, `status`, `ls`, `delete`, `clear`, `paths` commands
- **Isolated profile system** — `profile add`, `profile ls`, `exec`, `login` for parallel sessions with pseudo-HOME isolation
- **Content-hash-based active profile detection** via SHA-256 comparison against vault
- Export/import for vault transfer ([`9ecbf12`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9ecbf12ca188ff744c3e4b8d5a554b7e43c7a531))

### Smart Profile Management

- Health metadata storage with token expiry parsing ([`522fd3d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/522fd3d651c4b7eeb328f7a78451539d135f3f13), [`d8921ea`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d8921ea69d252624a61fdc0dce7631f223c77046))
- SPM configuration system ([`bb8b03a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/bb8b03a29e1c6b616ff99cafd9d7071d2523ec45))
- Health status display in `list` and `status` commands ([`c5b3f03`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c5b3f037c91f7e82c07ea078eb3bfe2f2ab19d77))
- Smart profile rotation with `--auto` flag ([`ff66e8a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/ff66e8aff66e8111aed35933f820056ed75076a2))
- Cooldown tracking and enforcement on activate ([`44bd3de`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/44bd3ded0ef8d50dfd205bce1a3e7906e207d603))
- Penalty tracking with exponential decay ([`04415cd`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/04415cd4fc7dbdff3d87381701abe46c714ebf72))
- Proactive token refresh for Codex and Gemini ([`35d1205`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/35d120552ec899319f0a3d2f1e36b2a8654177eb))

### Stealth & Safety

- Switch delay before activate ([`8d074a0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8d074a0222b267cfc0e7636cbcb8edc557b46a62))
- Stealth config for detection mitigation ([`2b9108a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2b9108a741dd08eebd475c4f83c28dbf382fda3b))
- Auto-backup `_original` on first activate ([`7144ea0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/7144ea0ec7a4dc9cbce675ccd7e86da024c8a3de))
- Smart auto-backup before profile switch ([`2c1ba4a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2c1ba4a89a77cdc99b8773b9728ed91ba2c3d53c))
- Safety config for data recovery ([`d55e4a0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d55e4a079398e26b0eca6d328807f1aacf3f18f7))
- Protected system vault profiles ([`b20fcfa`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/b20fcfaef48bf253359d43339c817eae27fa8663))
- Vault/profile path segment validation ([`94869e0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/94869e0414b8aef12033a207b5b692dd3948d37e))
- Shell injection prevention in browser helper scripts ([`194cd8e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/194cd8e0199c1eba269184dd024c36c833f755af))

### TUI (Bubble Tea)

- Full Bubble Tea TUI scaffolding ([`9eadd8e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9eadd8ee2352d3b039eb085ed1d26fb8f77c772c))
- Profiles table panel ([`8eecab2`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8eecab2c0277fb6b9f8d282cbc724a8e238ea992))
- Detail/action panel ([`6628eb1`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6628eb13621a6fa2100c90722411b03ad91514ac))
- Keyboard navigation and actions ([`cec6aa9`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/cec6aa955dc60b36487f10e940ba8e52a0313749))
- Search/filter mode ([`260f2de`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/260f2de82384874532d4931ae8c66096866ceaab))
- Hot reload via fsnotify profile watcher ([`a75c8f5`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a75c8f56f2bccededff031d832451fec6180ad65), [`3020169`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/30201696085422cd688996f558643e3ce22047c1))
- Usage analytics panel ([`d4d9c5e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d4d9c5e822d7561d03ddf0b27376d80ffdfb952b))
- Project context display ([`3682ec8`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3682ec89f96c58c489d6a56a519fe9217799844c))
- Signal handler, PID file, and reload command ([`d373866`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d373866f7b8c16a0a45228226d4b094949276593))
- Sync panel and export/import functionality ([`decd06f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/decd06f46e545c7b0cd67a17c242526473f654f1))
- Dialog components (text input, confirmation, multi-field) ([`d643d35`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d643d3567af414366bd30dacf7cc85f4c29d9099))
- Open account page in browser via `w` key ([`37a46d8`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/37a46d8dcd6848352da5f31eb574d9e4bc2848dc))

### CLI Commands

- `caam init` — first-time setup wizard with auth discovery ([`cf8c01e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/cf8c01e24e7aa63c31e2cbbca1742f85d5a49e07), [`9d6f1df`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9d6f1df004bb53e606bd35bbaa6c174837533285))
- `caam doctor` — diagnostics ([`1f66ebf`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1f66ebfb50c2927a1b27174df0e1545aa9fc3fe6))
- `caam open` — launch provider account pages ([`c71d433`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c71d433f799a93fbed9e2a30f7b4ff245f54245a))
- `caam env` — shell integration ([`83b34dc`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/83b34dcf23f78b464ae13d2ca64fbb96944f5e1b))
- `caam shell init` — shell integration hooks ([`044ad93`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/044ad938ba42cf7ed4facad99bb23a1049528409))
- `caam sessions`, `caam use`, `caam which` ([`23ce9d0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/23ce9d004301a2c5265d8c81f7467d7543c72d0a))
- `caam uninstall` — restore originals and clean up ([`1ef2d05`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1ef2d05171a2ddb734f24073b2b72f50543554c2))
- `caam verify` — profile token validation ([`f5a3738`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f5a373860b3fc898c8d037dd5d75804735a0ef47))
- `caam next` — one-command profile rotation preview ([`a7402fa`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/a7402faab82c2747e01cef3b627ddce87eda7ad9))
- `caam history` — activity log with filtering and `--json` ([`78a4e3a`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/78a4e3aa2e7fd7f5be4a762e5a3340fa84d5964c), [`00626af`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/00626afa8cb168bdce71fc7751c6c78cc2f4c4b2))
- `caam wrap` — auto rate limit detection and profile rotation ([`2f31113`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/2f311133cdbf92be7de599ef93c3ab3bddf37115))
- `caam profile unlock` — stale lock detection ([`75f5ceb`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/75f5cebd57cda233f99db669f79e44fc7eba15b1))
- Profile aliases and fuzzy matching ([`3a854ac`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3a854ac8f3dfc279a3d445fc8c5939c18e3cc830))
- One-command account capture ([`495ca62`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/495ca6236aa8d480ea578c1f408d557479aefb12))
- `--json` flag across `activate`, `backup`, `ls`, `status`, `cooldown list`, `project list` ([`60d5ff8`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/60d5ff8aadd19407c05048e88037b5e1291c656c), [`cba06db`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/cba06db66d284437f97fd6698139fd07654546b2), [`906ea7d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/906ea7d52d47ccc6431b5bdf730f74bea35427c3))
- `caam workspace` — switch all tools at once ([`e93b4da`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e93b4dad8eb109aca1e5b2a48608972cb16979e4))
- `caam usage` analytics CLI ([`3dca33b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3dca33b1330c2db49415f6a829a9d90d4769f1c1))
- Auth detection CLI and profile description support ([`58816be`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/58816befb3539a8ba57d8fef72d5956b1be0e124))
- Auth import command ([`d690d49`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d690d490216eb263e76645d18127076d795a174c))
- Browser profile auto-detection for init wizard ([`1fa09e2`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1fa09e204d91774e1089bde145246e321ca6eeb2))
- Auth file watcher for external change detection ([`8ba0e54`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8ba0e54fcd068c8d9fc93c619ac4b73e4a8fe154))
- Project association CLI and activate lookup ([`48486ed`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/48486edb0f670a98493a56c90aab24d304592ec7))
- Manual refresh command ([`72c9d55`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/72c9d55c1d0f61a8ccb49d5aafb10e83d4f5c711))
- Cooldown TTL in status output ([`1049cb8`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1049cb86f0b6b75890a231c837f0316546b62628))

### Sync & Bundle

- Bundle export/import and sync functionality ([`38ebe08`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/38ebe08f84b3c9cf689853deadffd474e55fd96b))
- SSH connectivity and transport layer ([`b4d58a9`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/b4d58a9459c1095ebca59e4d4c75c8a152d157ab))
- Token freshness comparison and sync algorithm ([`96fa65e`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/96fa65e54973ca6125841ee52416a8b4711d27bc))
- Sync init wizard and JSON output support ([`0af37fe`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0af37fe0ac0f52ccb45a8c2be0820a4d0e438077))
- Sync infrastructure and bundle format packages ([`e5e5658`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e5e565804ea40a15125d4f1ee4734f1e271d9df0))

### Database & Storage

- Embedded SQLite database with migrations ([`1608ac5`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/1608ac5e7c62b334a2271b19c5429f196e896ece))
- Activity event logging ([`c3ec77b`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c3ec77b85762e21813cc4e7d55c2e37a6b586460))
- Data retention and automatic cleanup ([`49c72d1`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/49c72d161aee8af846ae2f63a1687c7f5e53f6d7))
- Prevent data loss when retention days is 0 ([`8dc29cd`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8dc29cdcc741d831a5cf8b7630e1fb53107689ac))

### Provider Support

- OAuth refresh handlers and device code flow ([`70a17ae`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/70a17aefc80854034e38d21d096b55866c88317a), [`0a6dedb`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0a6dedbb365c6f0c0d2622621ea0ae2bbf2e084d))
- Codex device-code login for isolated profiles ([`94042ec`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/94042ec869a3aae909c180659abb086215139c18))
- Codex session capture and resume ([`9f07305`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9f07305c6096f7ddfa4f6a85487f6d35dda4909a))
- Gemini auth detection and profile description ([`c3e8668`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/c3e8668e0e637cb3d338cd47fb5f35decfed0027))
- Centralized `ProviderMeta` for account URLs ([`28e3b3f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/28e3b3fc37a20809a60468fdd0b92c759f890f0b))

### Reliability

- fsync on authfile metadata writes ([`5d4d790`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/5d4d7903344cd68eaa9320b8b34ad010c201daef))
- fsync on SPM config and health storage saves ([`d7785ad`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/d7785addbc783bc819cd71a3925c82d03e5c381b))
- fsync on CSV writes; fix data race in state Save ([`76b6c43`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/76b6c43f8e4ba707001464a9c63e51aa43bab347))
- Prevent TOCTOU race in health and project storage ([`cde2145`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/cde2145a691cdee1c6c78611d89a2701db62f6e6), [`587e3fd`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/587e3fd31456617b847146c35a6657b14cf59c7f))
- Exclusive lock in `SyncState.Save()` ([`dd84330`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/dd843300fb3dd07cf926442e676d225449d53f0b))
- Hardened PID file operations and project store ([`8f89150`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8f89150c0450bdd0f6ee0a4fbc3a0cce2c881ec6))
- Atomic config save; flush URL capture ([`8353c70`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/8353c709cf822ed7b556bf0c0cc7e32b902f8faf))
- Multiple security and reliability fixes in sync and import ([`bf1ef74`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/bf1ef74e1c099cde9f6ae7ff68e481495520b246))
- Environment deduplication and atomic file sync ([`9a45c01`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/9a45c0167260848ff482151b19f88f0f85b515b2))
- Windows command injection fix and URL punctuation UX fix ([`f61c564`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/f61c564c847b587df8142b1d47ff67ee15e00514))
- Glob association bug fix and profile save atomicity ([`6a6591f`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/6a6591f15f01f0d54506194f4b59c92f938fbe54))
- POSIX paths for remote SFTP operations ([`904afbf`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/904afbf83711ec8b11498b169232482db88d7b41))
- Buffered teeWriter output for reliable rate limit detection ([`0dd11f0`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/0dd11f0bf2bdf5bd6cfd05ff698ad9d8dbc3b988))

### Testing (v0.1.0)

- Comprehensive test suite across all packages:
  - authfile (35 tests), profile (82.3% coverage), codex provider, claude provider (34 tests), gemini provider (38 tests), cmd/caam/cmd (96 tests), passthrough, version/config/provider/browser, internal/exec, E2E test harness (27 tests)
  - E2E: TUI interaction (27 tests), CLI workflows (9 tests), profile backup/restore (9 tests), error handling
- GitHub Actions CI/CD pipeline ([`976975d`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/976975ddae61868e58c3adf74e5dedc7f59d7022))

### Infrastructure

- Lock file with PID validation ([`e163485`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e1634857d9dd1551a5432a627506244f0dc513d4))
- Charm library dependencies (Bubble Tea, Lip Gloss, Glamour) ([`3ffb4b6`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/3ffb4b6c1639133255fb2dc1a7e264be027d2f20))
- Install script ([`e034bf4`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/e034bf48a2e1b34f604a086c47590a5a3af369b5))

---

## Project Genesis — 2025-12-17

- Initial commit with planning beads ([`337afee`](https://github.com/Dicklesworthstone/coding_agent_account_manager/commit/337afee798bafc8c3b145ec14aadd475232fc4b5))

---

<!-- Link definitions -->
[Unreleased]: https://github.com/Dicklesworthstone/coding_agent_account_manager/compare/v0.1.10...HEAD
[v0.1.10]: https://github.com/Dicklesworthstone/coding_agent_account_manager/compare/v0.1.5...v0.1.10
[v0.1.5]: https://github.com/Dicklesworthstone/coding_agent_account_manager/compare/v0.1.4...v0.1.5
[v0.1.4]: https://github.com/Dicklesworthstone/coding_agent_account_manager/compare/v0.1.1...v0.1.4
[v0.1.1]: https://github.com/Dicklesworthstone/coding_agent_account_manager/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/Dicklesworthstone/coding_agent_account_manager/releases/tag/v0.1.0
