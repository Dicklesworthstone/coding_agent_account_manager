# RESEARCH FINDINGS: caam (coding_agent_account_manager) - TOON Integration Analysis

**Date**: 2026-01-25
**Bead**: bd-6ke
**Researcher**: Claude Code Agent (cc)

## 1. Project Overview

| Attribute | Value |
|-----------|-------|
| **Language** | Go 1.24 |
| **CLI Framework** | Cobra |
| **TUI Framework** | Bubble Tea + Lipgloss |
| **Tier** | 3 (Lower Impact - Account data) |
| **Directory** | `/dp/coding_agent_account_manager` |

### Key Dependencies
- `github.com/spf13/cobra` - CLI framework
- `github.com/charmbracelet/bubbletea` - TUI framework
- `modernc.org/sqlite` - Embedded database
- `encoding/json` - Standard JSON serialization

## 2. Robot Subcommand Structure

The `caam robot` subcommand provides a comprehensive agent-optimized interface:

| Command | Purpose | Output Type |
|---------|---------|-------------|
| `robot status [provider]` | Full system status overview | JSON |
| `robot next <provider>` | Suggest best profile to use | JSON |
| `robot act <action> <provider> [profile]` | Execute an action | JSON |
| `robot health` | Quick health check | JSON |
| `robot watch` | Stream status changes | NDJSON |
| `robot doctor` | Full system diagnostics | JSON |
| `robot paths` | Show all auth file paths | JSON |
| `robot validate` | Validate auth tokens | JSON |
| `robot limits` | Fetch rate limits and burn rate | JSON |
| `robot precheck` | Session planner with recommendations | JSON |
| `robot history` | Recent activity log | JSON |
| `robot config` | View or modify configuration | JSON |

## 3. Serialization Entry Points

**File**: `cmd/caam/cmd/robot.go` (55KB, ~1400 lines)

### Core Structs

```go
// RobotOutput is the standard response wrapper for all robot commands
type RobotOutput struct {
    Success     bool        `json:"success"`
    Command     string      `json:"command"`
    Timestamp   string      `json:"timestamp"`
    Data        interface{} `json:"data,omitempty"`
    Error       *RobotError `json:"error,omitempty"`
    Suggestions []string    `json:"suggestions,omitempty"`
    Timing      *RobotTiming `json:"timing,omitempty"`
}
```

### Serialization Functions

| Function | Purpose | Location |
|----------|---------|----------|
| `robotOutput(cmd, output)` | Write RobotOutput to stdout | robot.go:267-271 |
| `robotError(...)` | Create error output | robot.go:274-287 |
| `emitWatchStatus(...)` | Emit NDJSON for watch | robot.go (stream) |

### Current Serialization Pattern
```go
func robotOutput(cmd *cobra.Command, output RobotOutput) error {
    output.Timestamp = time.Now().UTC().Format(time.RFC3339)
    enc := json.NewEncoder(cmd.OutOrStdout())
    return enc.Encode(output)
}
```

## 4. Output Analysis

### Typical Output Sizes

| Command | JSON Size | Est. Tokens | Notes |
|---------|-----------|-------------|-------|
| `robot status` | ~4,054 bytes | ~1,000 | Full status with 8 profiles |
| `robot health` | ~424 bytes | ~100 | Quick health check |
| `robot doctor` | ~1,194 bytes | ~300 | Diagnostics with 12 checks |
| `robot paths` | ~1,440 bytes | ~350 | Auth file paths |
| `robot next` | ~458 bytes | ~115 | Profile recommendation |

### Output Characteristics

1. **Consistent Envelope**: All outputs use `RobotOutput` wrapper
2. **Timing Metadata**: Every response includes `timing.duration_ms`
3. **Actionable Suggestions**: `suggestions` array with next steps
4. **Structured Errors**: `RobotError` with `code`, `message`, `details`
5. **NDJSON Streaming**: `robot watch` emits one JSON object per line

### Sample Output Structure (status)
```json
{
  "success": true,
  "command": "status",
  "timestamp": "2026-01-25T05:23:36Z",
  "data": {
    "version": "dev",
    "providers": [...],
    "summary": {
      "total_profiles": 8,
      "healthy_profiles": 0,
      "all_profiles_blocked": true
    }
  },
  "suggestions": [...],
  "timing": {"started_at": "...", "duration_ms": 10}
}
```

## 5. TOON Integration Assessment

### Complexity Rating: **SIMPLE**

**Rationale**:
- Single serialization entry point (`robotOutput`)
- Consistent envelope structure
- Well-defined data types
- No complex nested streaming (watch is simple NDJSON)

### TOON Savings Estimate

| Command | Current Tokens | Expected TOON | Savings |
|---------|----------------|---------------|---------|
| `robot status` | ~1,000 | ~600 | 40% |
| `robot health` | ~100 | ~60 | 40% |
| `robot doctor` | ~300 | ~180 | 40% |
| `robot paths` | ~350 | ~210 | 40% |

**Overall Expected Savings**: 35-45% (tabular profile data benefits from TOON)

### Integration Strategy

1. **Add Format Flag** (Recommended approach from TOON_INTEGRATION_BRIEF.md):
   ```go
   var formatFlag string // "json" | "toon"

   func init() {
       robotCmd.PersistentFlags().StringVar(&formatFlag, "format", "json",
           "Output format: json (default) or toon")
   }
   ```

2. **Modify robotOutput**:
   ```go
   func robotOutput(cmd *cobra.Command, output RobotOutput) error {
       output.Timestamp = time.Now().UTC().Format(time.RFC3339)

       format := getOutputFormat() // CLI > CAAM_OUTPUT_FORMAT > TOON_DEFAULT_FORMAT > "json"

       if format == "toon" {
           return encodeTOON(cmd.OutOrStdout(), output)
       }

       enc := json.NewEncoder(cmd.OutOrStdout())
       return enc.Encode(output)
   }
   ```

3. **TOON Encoder Options**:
   - **Preferred**: `toon-go` library (bd-u30) when available
   - **Fallback**: Shell out to `tru --encode` CLI

4. **Watch Streaming**:
   - Add `--format toonl` for TOON-per-line streaming
   - Default remains JSONL for compatibility

### Files to Modify

| File | Changes |
|------|---------|
| `cmd/caam/cmd/robot.go` | Add `--format` flag, modify `robotOutput()` |
| `internal/toon/encoder.go` | New file: TOON encoding wrapper |
| `README.md` | Document `--format toon` option |

## 6. Protocol Constraints

- **Stdout**: Data only (JSON/TOON)
- **Stderr**: Diagnostics, warnings, stats
- **Exit Codes**: 0=success, 1=error, 2=partial success
- **Backward Compatibility**: JSON must remain default

## 7. Risks & Edge Cases

| Risk | Mitigation |
|------|------------|
| `toon-go` not available | Fallback to `tru` CLI subprocess |
| Streaming output | Use TOONL (TOON per line) |
| Large profile lists | Benchmark encoding performance |
| Encoder failure | Emit JSON + stderr warning |

## 8. Existing TOON Documentation

A detailed integration brief already exists:
- **File**: `/dp/coding_agent_account_manager/TOON_INTEGRATION_BRIEF.md`
- **Bead**: bd-1fq
- **Status**: Ready for implementation

## 9. Next Steps

1. **Wait for toon-go** (bd-u30) or use `tru` fallback
2. **Create implementation bead** for caam TOON integration
3. **Add to verification matrix** (bd-ba5)
4. **Update bd-6ke** with completion status

## 10. Deliverables Checklist

- [x] Map Cargo.toml/go.mod and src/ structure
- [x] Document existing caam robot subcommand
- [x] Identify robot status, robot next, robot act patterns
- [x] Review NDJSON robot watch streaming
- [x] Capture outputs: status, health, doctor, paths, next
- [x] Measure typical output sizes
- [x] Rate complexity: **SIMPLE**
- [x] Document dedicated robot subcommand pattern
- [x] Create RESEARCH_FINDINGS.md (this file)
