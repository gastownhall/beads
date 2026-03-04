# Audit: Sync Mode Complexity in Beads

**Wanted Item:** w-bd-004
**Date:** 2026-03-04
**Status:** Audit complete

## Executive Summary

Beads' sync subsystem has been significantly simplified through recent refactoring (v0.50-v0.53). The old multi-mode architecture (git-portable, belt-and-suspenders, dolt-native) has been collapsed to a single mode: **dolt-native**. However, vestigial configuration scaffolding and unnecessary abstraction layers remain. This audit identifies remaining complexity and recommends further simplifications.

## Current Architecture

### Sync Mode Configuration (Vestigial)

**File:** `internal/config/sync.go`

The `SyncMode` type, validation functions, and config plumbing still exist despite there being only one valid mode:

```go
type SyncMode string
const SyncModeDoltNative SyncMode = "dolt-native"

var validSyncModes = map[SyncMode]bool{
    SyncModeDoltNative: true,
}
```

`GetSyncMode()` is hardcoded to return `SyncModeDoltNative` regardless of configuration. Yet the infrastructure around it persists:

- `SyncMode` type definition and `String()` method
- `validSyncModes` map (1 entry)
- `ValidSyncModes()` function (returns 1-element slice)
- `IsValidSyncMode()` validation function
- `SyncConfig` struct in `config.go` with `Mode` field
- `GetSyncConfig()` that calls `GetSyncMode()`
- Config default: `v.SetDefault("sync.mode", SyncModeDoltNative)`
- Config validation in `cmd/bd/config.go` that checks `sync.mode` is valid
- Tests for all of the above (`sync_test.go`: 6 test functions)

**Recommendation: Remove.** Since there is only one mode and `GetSyncMode()` is hardcoded, all SyncMode machinery is dead weight. Any code that checks the sync mode can be simplified to unconditional dolt-native behavior.

### Export/Import Trigger Configuration

**File:** `internal/config/config.go` (lines 157-158)

```go
v.SetDefault("sync.export_on", SyncTriggerPush)  // push | change
v.SetDefault("sync.import_on", SyncTriggerPull)   // pull | change
```

These config keys (`sync.export_on`, `sync.import_on`) still exist with two trigger values each (`push`/`change` for export, `pull`/`change` for import). They are read into `SyncConfig` via `GetSyncConfig()`.

**Assessment:** These triggers remain meaningful for controlling when Dolt sync operations fire. However, since `bd sync` is now a no-op (v0.51 changelog), and Dolt handles persistence directly, it is worth verifying whether these triggers are still consumed by any runtime code path. If they are only used in the hook system (`internal/hooks/`), they may still be relevant. If not, they are dead config.

**Recommendation: Audit callers.** If `sync.export_on` and `sync.import_on` have no runtime consumers, remove them. If they are consumed, document which code paths use them.

### Push/Pull Routing Complexity (Justified)

**File:** `internal/storage/dolt/store.go` (lines 1351-1462)

Each of `Push()`, `ForcePush()`, and `Pull()` has a 3-way routing decision:

1. **Git-protocol remote** (SSH, git+https://) -> shell out to `dolt push/pull` CLI
2. **Hosted Dolt with remoteUser** -> `CALL DOLT_PUSH('--user', ...)` via SQL
3. **Default** (DoltHub, S3, GCS, file) -> `CALL DOLT_PUSH(?, ?)` via SQL

This produces significant code duplication. Each method repeats the same pattern:
```go
if s.isGitProtocolRemote(ctx) { ... CLI path ... }
if s.remoteUser != "" { ... SQL with --user ... }
// default SQL path
```

**Assessment:** This routing is necessary -- the three paths exist because Dolt has genuinely different authentication mechanisms. The comments explain why git-protocol remotes cannot use the SQL connection (MySQL connection timeouts during transfer). This is **justified complexity**.

**Recommendation: Extract a helper.** The 3-way dispatch pattern could be extracted into a single `execDoltRemoteOp` helper that takes the operation (push/pull/fetch), force flag, and CLI args. This would eliminate ~50 lines of duplication across Push/ForcePush/Pull without changing semantics. However, this is a minor refactor and not urgent.

### Federation Peer System (Active, Well-Structured)

**File:** `internal/storage/dolt/federation.go` (340 lines)
**File:** `internal/storage/dolt/credentials.go` (473 lines)

The federation subsystem provides:
- Peer-to-peer sync: `Sync()`, `PushTo()`, `PullFrom()`, `Fetch()`
- Credential management: AES-GCM encryption, key migration, peer CRUD
- Sync status tracking: ahead/behind counts, conflict detection
- CLI/SQL routing: same `isPeerGitProtocolRemote` pattern as main Push/Pull

The `Sync()` method (federation.go:186-256) orchestrates a 5-step bidirectional sync: fetch, get-status, merge, resolve-conflicts, push. This is straightforward and well-commented.

**Assessment:** This is active, well-structured code. The credential encryption with key migration (legacy SHA-256-derived key to random AES-256 key) is solid. The `withPeerCredentials` / `withEnvCredentials` pattern cleanly separates CLI-subprocess isolation from SQL-path mutex protection.

**Recommendation: No changes needed.** This is appropriate complexity for federation.

### Conflict Resolution Configuration (Active, Clean)

**File:** `internal/config/sync.go` (lines 53-125)

Four conflict strategies (`newest`, `ours`, `theirs`, `manual`) and four field-level strategies (`newest`, `max`, `union`, `manual`) are well-defined with validation. These are actively used by the federation `Sync()` method for auto-resolution.

**Assessment:** Clean, well-tested, appropriate complexity.

**Recommendation: No changes needed.**

### Sovereignty Tiers (Active, Clean)

**File:** `internal/config/sync.go` (lines 127-168)

Four sovereignty tiers (T1-T4) for federation access control. Used in `FederationPeer.Sovereignty`.

**Assessment:** Small, well-defined, appropriately simple.

**Recommendation: No changes needed.**

### Tracker SyncEngine (Active, Good Abstraction)

**File:** `internal/tracker/engine.go`

A shared sync engine for external trackers (Linear, GitLab, Jira) with `PullHooks`/`PushHooks` for customization. This replaced ~800 lines of duplicated sync code (v0.50.3 changelog).

**Assessment:** Good abstraction that unified three parallel implementations. Not part of the Dolt sync path -- this is for external issue tracker bidirectional sync.

**Recommendation: No changes needed.** This is separate from Dolt sync mode and is already well-factored.

### Auto-Increment Reset After Pull (Workaround)

**File:** `internal/storage/dolt/store.go` (lines 1464-1488)

After every pull, `resetAutoIncrements()` iterates over 6 hardcoded tables and resets their `AUTO_INCREMENT` to `MAX(id) + 1`. This is called in all three Pull routing paths and in the Pull after federation sync.

**Assessment:** This is a workaround for a Dolt behavior where pulling can leave auto-increment values out of sync. The hardcoded table list is brittle.

**Recommendation: Monitor.** If Dolt fixes this upstream, this workaround can be removed. Consider making the table list configurable or deriving it from schema introspection if more tables are added.

## Summary of Recommendations

| Area | Recommendation | Effort | Impact |
|------|---------------|--------|--------|
| SyncMode type + validation | **Remove entirely** | Small | Removes ~80 lines of dead code + ~100 lines of tests |
| `sync.mode` config key | **Remove** (keep only as deprecated no-op) | Small | Simplifies config validation |
| `sync.export_on`/`sync.import_on` | **Audit callers**, remove if dead | Small | Removes dead config or documents live usage |
| Push/Pull/ForcePush 3-way routing | **Extract helper** (optional) | Medium | ~50 lines deduplication |
| Federation peer system | **No change** | - | Already clean |
| Conflict/field strategies | **No change** | - | Already clean |
| Sovereignty tiers | **No change** | - | Already clean |
| Tracker SyncEngine | **No change** | - | Already clean |
| Auto-increment reset | **Monitor** for Dolt upstream fix | - | Future cleanup |

## Files Analyzed

| File | Lines | Role |
|------|-------|------|
| `internal/config/sync.go` | 240 | Sync mode, conflict, sovereignty, field strategy types |
| `internal/config/sync_test.go` | 436 | Tests for all sync config types |
| `internal/config/config.go` | 921 | Config initialization, defaults, SyncConfig/ConflictConfig structs |
| `internal/config/yaml_config.go` | ~300 | YAML config management, yaml-only keys |
| `internal/storage/dolt/store.go` | 1668 | DoltStore: Push, Pull, ForcePush, auto-increment reset |
| `internal/storage/dolt/federation.go` | 340 | Federation sync: Sync, PushTo, PullFrom, Fetch |
| `internal/storage/dolt/credentials.go` | 473 | Federation peer credentials, encryption |
| `internal/storage/versioned.go` | 60 | Shared types: Conflict, SyncStatus, FederationPeer |
| `internal/tracker/engine.go` | ~200 | External tracker SyncEngine |
| `internal/hooks/hooks.go` | ~100 | Hook runner (create/update/close events) |
| `cmd/bd/config.go` | ~500 | CLI config commands, sync.mode validation |
| `cmd/bd/info.go` | ~400 | Version history documenting sync removals |

## Historical Context

The sync subsystem has undergone major simplification across v0.50-v0.53:

- **v0.50.3**: Tracker sync code unified via shared SyncEngine (~800 lines removed)
- **v0.51.0**: SQLite backend, JSONL sync, 3-way merge, tombstones, storage factory, daemon stubs removed. `bd sync` became a no-op.
- **v0.52.0**: Dead git-portable sync functions removed (#1793)
- **v0.53.0**: JSONL sync-branch pipeline removed (~11,000 lines). Daemon infrastructure and 3-way merge remnants removed.

The remaining SyncMode scaffolding is the last vestige of the old multi-mode era and can be safely removed.
