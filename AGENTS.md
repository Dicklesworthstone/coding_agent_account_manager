# Agent Coordination Board

## Active Agents
- **Gemini**: Performed deep security/reliability audit (`caam-sec-audit`, `caam-sec-win`). Fixed env var deduplication, enforced `fsync`, and patched Windows command injection. Improved URL detection (`caam-ux-url`).
- **Codex (GPT-5.2)**: Fresh-eyes audit fixes landed: `caam-iks` (stale accx references/Makefile) and `caam-0ds` (DB stats last_error monotonic).
- **LilacCastle (Claude Opus 4.5)**: Bug fixes in rotation/activate code; closed stealth epic `caam-e8o`.

## Project Status
✅ **All 104 beads closed** - Project feature complete!

## Task Queue
- [x] Investigate codebase
- [x] `caam-6gi`: Implement penalty decay logic
- [x] Fix project glob associations (internal/project/store.go)
- [x] Fix profile atomic saves (internal/profile/profile.go)
- [x] `caam-e36`: Proactive Token Refresh (committed)
- [x] Fix `getProfileHealth` wiring in `root.go`
- [x] `caam-3nx`: Data Safety & Recovery (original backups, uninstall, protected profiles)
- [x] `caam-5ed`: Cooldown tracking (DB + CLI + activate integration)
- [x] `caam-ewh`: Smart profile rotation (--auto + tests/docs)
- [x] `caam-j06`: Fix rotation last-activation query
- [x] `caam-d8x`: Reliability: atomic config save + flush URL capture
- [x] `caam-l4q`: Health formatting: remove deprecated strings.Title
- [x] `caam-iks`: Fix stale accx references (Makefile/docs)
- [x] `caam-0ds`: DB stats: keep last_error monotonic

## Messages
(None)
