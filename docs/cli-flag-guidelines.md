# CLI Flag Naming Guidelines

This document establishes consistent flag naming conventions for the caam CLI.

## Flag Categories and Standards

### 1. Output Format Flags

**Standard:**
- `--json` (bool): Use when command only needs JSON vs default output
- `--format <value>` (string): Use when multiple formats needed (table, json, csv, brief, alerts)

**Commands using `--format`:** usage, limits, precheck, monitor, cost (when needing csv)

**Commands using `--json`:** All others with JSON output option

### 2. Provider Filtering

**Standard:** Use `--provider` (singular) for all commands.

| Command | Current | Action |
|---------|---------|--------|
| sessions | --provider | OK |
| history | --provider | OK |
| cost | --provider | OK |
| pool | --provider | OK |
| monitor | --provider | OK |
| robot | --provider | OK |
| sync | --provider | OK |
| watch | --providers | **FIX: Change to --provider** |
| bundle export | --providers | **FIX: Change to --provider** |
| bundle import | --providers | **FIX: Change to --provider** |

### 3. Confirmation Skip Flags

**Standard:** Use `--force` for skipping confirmation prompts.

| Command | Current | Action |
|---------|---------|--------|
| uninstall | --force | OK |
| delete | --force | OK |
| clear | --force | OK |
| import | --force | OK |
| add | --force | OK |
| update | --force | OK |
| config reset | --force | OK |
| profile delete | --force | OK |
| sync remove | --force | OK |
| bundle import | --force | OK |
| setup distributed | --yes | **FIX: Change to --force** |
| wezterm login-all | --yes | **FIX: Change to --force** |
| wezterm recover | --yes | **FIX: Change to --force** |

**Exception:** `--yes` is acceptable as an alias for `--force` where it reads more naturally, but `--force` should always work.

### 4. Preview/Dry-Run Flags

**Standard:** Use `--dry-run` for all preview operations.

Already consistent across: cleanup, next, setup, uninstall, bundle export, bundle import, wezterm, refresh, sync

### 5. Exclusion Flags

**Standard:** Use `--no-<thing>` for excluding items (export). Use `--skip-<thing>` for skipping actions (import).

**Export pattern (exclusion from output):**
- `--no-config`, `--no-projects`, `--no-health`

**Import pattern (skipping actions):**
- `--skip-config`, `--skip-projects`, `--skip-health`, `--skip-database`, `--skip-sync`

This distinction is correct and should be maintained.

### 6. Verbosity Flags

**Standard:**
- `--verbose` for increased output
- `--quiet` for suppressed output

Both may coexist on the same command (opposite ends of verbosity spectrum).

### 7. Common Flag Patterns

| Purpose | Flag | Short | Notes |
|---------|------|-------|-------|
| All items | --all | -a | Process all rather than specific |
| Force action | --force | -f | Skip confirmation |
| Quiet output | --quiet | -q | Suppress output |
| Dry run | --dry-run | -n | Preview mode |
| Verbose | --verbose | -v | Extra output |
| JSON output | --json | -j | JSON format |
| Format | --format | | When multiple formats |

### 8. Short Flags

**Guidelines:**
- Only add short flags for frequently-used flags
- Use standard Unix conventions where possible
- Avoid conflicting shorts within the same command

**Reserved shorts:**
- `-a` for --all
- `-f` for --force OR --format (not both on same command)
- `-j` for --json
- `-n` for --dry-run
- `-q` for --quiet
- `-v` for --verbose

## Changes Required

### High Priority (User-facing inconsistencies)

1. **watch.go**: Change `--providers` to `--provider`
2. **bundle.go**: Change `--providers` to `--provider`
3. **bundle_import.go**: Change `--providers` to `--provider`
4. **setup.go**: Add `--force` as alias for `--yes`
5. **wezterm.go**: Add `--force` as alias for `--yes`

### Low Priority (Code consistency)

Short flags could be added to frequently-used commands but are not strictly necessary.
