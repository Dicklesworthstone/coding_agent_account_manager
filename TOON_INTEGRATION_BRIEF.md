# TOON Integration Brief: caam (coding_agent_account_manager)

Date: 2026-01-24
Bead: bd-1fq

## 1) Output Surfaces (robot)
Robot commands are JSON-only today (stdout JSON, stderr diagnostics):
- `caam robot status [provider]`
- `caam robot next <provider>`
- `caam robot act ...`
- `caam robot health`
- `caam robot watch` (streams NDJSON: one JSON object per line)
- `caam robot limits`, `precheck`, `validate`, `doctor`, `paths`, `history`, `config`

Non-robot commands also expose `--json` or `--format json` (e.g., `usage`, `limits`, `cost`, `monitor`, `doctor`, `sessions`, `auth`, `tag`), but bd-1fq scope is robot outputs.

## 2) Serialization Entry Points (file + functions)
File: `cmd/caam/cmd/robot.go`
- `RobotOutput` struct defines JSON envelope.
- `robotOutput(cmd, output)` → `json.NewEncoder(cmd.OutOrStdout()).Encode(output)` (single object).
- `robotError(...)` uses `robotOutput` and returns error.
- `emitWatchStatus(...)` → `json.NewEncoder(cmd.OutOrStdout()).Encode(event)` (NDJSON).
- `runRobotWatch` loop calls `emitWatchStatus` on interval.
- `runRobotQuickStart` embeds JSON schema in help text (update docs here too).

## 3) Format Flags & Env Precedence (proposed)
Add opt-in TOON while preserving JSON default:
1. CLI: `caam robot --format json|toon` (default `json`)
2. Env: `CAAM_OUTPUT_FORMAT`
3. Env fallback: `TOON_DEFAULT_FORMAT`
4. Default: JSON (backward compatible)

For streaming `robot watch`, consider `--format toonl` (TOON per line) to preserve stream semantics.

## 4) TOON Strategy (Go tool)
- Preferred: `toon-go` library (bd-u30) once available.
- Fallback: `tru` CLI subprocess if toon-go missing.
- For single-shot outputs: encode `RobotOutput` to TOON and write to stdout.
- For streaming watch:
  - Default remains JSONL.
  - If `--format toonl`, encode each event to TOON and write one line per event.
- On encode failure or missing encoder: emit JSON and warn on stderr (do not change exit codes).

## 5) Protocol Constraints
- Robot commands are automation-facing; keep JSON as default and preserve schema.
- Stdout must remain data-only; stderr for diagnostics/stats.
- `robot watch` streaming must remain stable; TOON should be additive only.

## 6) Docs to Update
- `README.md`: robot section + `--format toon` + env precedence.
- `cmd/caam/cmd/robot.go`: quick-start text (add TOON note).
- `--help` for `caam robot` (persistent flag).

## 7) Fixtures to Capture (bd-21h)
Suggested commands (safe, no side effects):
- `caam robot status --provider claude` (JSON)
- `caam robot next claude` (JSON)
- `caam robot health` (JSON)
- `caam robot watch --interval 1 --provider claude` (capture first 2-3 lines)

Store fixtures in `fixtures/real_world/` or agreed location.

## 8) Test Plan
Unit tests:
- Format precedence: CLI > `CAAM_OUTPUT_FORMAT` > `TOON_DEFAULT_FORMAT` > default.
- TOON round-trip: `robot status`, `robot next` decode to JSON equivalent.
- Watch streaming: `toonl` line-by-line decode (if implemented).
- Fallback: missing encoder -> JSON output + stderr warning.

E2E script (design):
- Run `caam robot status --format json` and `--format toon`, decode TOON, compare JSON.
- For watch: `timeout 3 caam robot watch --format toonl ...` capture N lines, decode each.
- Log to `test_logs/caam_<timestamp>/` with stdout/stderr/exit codes.

## 9) Risks & Edge Cases
- Streaming output (`watch`) requires `toonl` or JSONL fallback.
- Large provider lists: ensure TOON encoding performance acceptable.
- Encoder availability (toon-go/tru) must not break default JSON mode.
