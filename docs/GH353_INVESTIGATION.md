# Investigation: GH #353 - Daemon Locking Issues in Codex Sandbox

## Problem Summary

When running `bd` inside the Codex sandbox (macOS host), users encounter persistent "Database out of sync with JSONL" errors that cannot be resolved through normal means (`bd import`). The root cause is a daemon process that the sandbox cannot signal or kill, creating a deadlock situation.

## Root Cause Analysis

### The Daemon Locking Mechanism

The daemon uses three mechanisms to claim a database:

1. **File lock (`flock`)** on `.beads/daemon.lock` - exclusive lock held while daemon is running
2. **PID file** at `.beads/daemon.pid` - contains daemon process ID (Windows compatibility)
3. **Lock metadata** in `daemon.lock` - JSON containing PID, database path, version, start time

**Source:** `cmd/bd/daemon_lock.go`

### Process Verification Issue

On Unix systems, `isProcessRunning()` uses `syscall.Kill(pid, 0)` to check if a process exists. In sandboxed environments:

- The daemon PID exists in the lock file
- `syscall.Kill(pid, 0)` returns EPERM (operation not permitted)
- The CLI can't verify if the daemon is actually running
- The CLI can't send signals to stop the daemon

**Source:** `cmd/bd/daemon_unix.go:26-28`

### Staleness Check Flow

When running `bd ready` or other read commands:

1. **With daemon connected:**
   - Command → Daemon RPC → `checkAndAutoImportIfStale()`
   - Daemon checks JSONL mtime vs `last_import_time` metadata
   - Daemon auto-imports if stale (with safeguards)
   - **Source:** `internal/rpc/server_export_import_auto.go:171-303`

2. **Without daemon (direct mode):**
   - Command → `ensureDatabaseFresh(ctx)` check
   - Compares JSONL mtime vs `last_import_time` metadata
   - **Refuses to proceed** if stale, shows error message
   - **Source:** `cmd/bd/staleness.go:20-51`

### The Deadlock Scenario

1. Daemon is running outside sandbox with database lock
2. User (in sandbox) runs `bd ready`
3. CLI tries to connect to daemon → connection fails or daemon is unreachable
4. CLI falls back to direct mode
5. Direct mode checks staleness → JSONL is newer than metadata
6. Error: "Database out of sync with JSONL. Run 'bd import' first."
7. User runs `bd import -i .beads/beads.jsonl`
8. Import updates metadata in database file
9. **But daemon still running with OLD metadata cached in memory**
10. User runs `bd ready` again → CLI connects to daemon
11. Daemon checks staleness using **cached metadata** → still stale!
12. **Infinite loop:** Can't fix because can't restart daemon

### Why `--no-daemon` Doesn't Always Work

The `--no-daemon` flag should work by setting `daemonClient = nil` and skipping daemon connection (**source:** `cmd/bd/main.go:287-289`). However:

1. If JSONL is genuinely newer than database (e.g., after `git pull`), the staleness check in direct mode will still fail
2. If the user doesn't specify `--no-daemon` consistently, the CLI will reconnect to the stale daemon
3. The daemon may still hold file locks that interfere with direct operations

## Existing Workarounds

### The `--sandbox` Flag

Already exists! Sets:
- `noDaemon = true` (skip daemon)
- `noAutoFlush = true` (skip auto-flush)
- `noAutoImport = true` (skip auto-import)

**Source:** `cmd/bd/main.go:201-206`

**Issue:** Still runs staleness check in direct mode, which fails if JSONL is actually newer.

## Proposed Solutions

### Solution 1: Force-Import Flag (Quick Fix) ⭐ **Recommended**

Add `--force` flag to `bd import` that:
- Updates `last_import_time` and `last_import_hash` metadata even when 0 issues imported
- Explicitly touches database file to update mtime
- Prints clear message: "Metadata updated (database already in sync)"

**Pros:**
- Minimal code change
- Solves immediate problem
- User can manually fix stuck state

**Cons:**
- Requires user to know about --force flag
- Doesn't prevent the problem from occurring

**Implementation location:** `cmd/bd/import.go` around line 349

### Solution 2: Skip-Staleness Flag (Escape Hatch) ⭐ **Recommended**

Add `--allow-stale` or `--no-staleness-check` global flag that:
- Bypasses `ensureDatabaseFresh()` check entirely
- Allows operations on potentially stale data
- Prints warning: "⚠️  Staleness check skipped, data may be out of sync"

**Pros:**
- Emergency escape hatch when stuck
- Minimal invasive change
- Works with `--sandbox` mode

**Cons:**
- User can accidentally work with stale data
- Should be well-documented as last resort

**Implementation location:** `cmd/bd/staleness.go:20` and callers

### Solution 3: Sandbox Detection (Automatic) ⭐⭐ **Best Long-term**

Auto-detect sandbox environment and adjust behavior:

```go
func isSandboxed() bool {
    // Try to signal a known process (e.g., our own parent)
    // If we get EPERM, we're likely sandboxed
    if syscall.Kill(os.Getppid(), 0) != nil {
        if err == syscall.EPERM {
            return true
        }
    }
    return false
}

// In PersistentPreRun:
if isSandboxed() {
    sandboxMode = true  // Auto-enable sandbox mode
    fmt.Fprintf(os.Stderr, "ℹ️  Sandbox detected, using direct mode\n")
}
```

Additionally, when daemon connection fails with permission errors:
- Automatically set `noDaemon = true` for subsequent operations
- Skip daemon health checks that require process signals

**Pros:**
- Zero configuration for users
- Prevents the problem entirely
- Graceful degradation

**Cons:**
- More complex heuristic
- May have false positives
- Requires testing in various environments

**Implementation locations:**
- `cmd/bd/main.go` (detection)
- `cmd/bd/daemon_unix.go` (process checks)

### Solution 4: Better Daemon Health Checks (Robust)

Enhance daemon health check to detect unreachable daemons:

1. When `daemonClient.Health()` fails, check why:
   - Connection refused → daemon not running
   - Timeout → daemon unreachable (sandbox?)
   - Permission denied → sandbox detected

2. On sandbox detection, automatically:
   - Set `noDaemon = true`
   - Clear cached daemon client
   - Proceed in direct mode

**Pros:**
- Automatic recovery
- Better error messages
- Handles edge cases

**Cons:**
- Requires careful timeout tuning
- More complex state management

**Implementation location:** `cmd/bd/main.go` around lines 300-367

### Solution 5: Daemon Metadata Refresh (Prevents Staleness)

Make daemon periodically refresh metadata from disk:

```go
// In daemon event loop, check metadata every N seconds
if time.Since(lastMetadataCheck) > 5*time.Second {
    lastImportTime, _ := store.GetMetadata(ctx, "last_import_time")
    // Update cached value
}
```

**Pros:**
- Daemon picks up external import operations
- Reduces stale metadata issues
- Works for other scenarios too

**Cons:**
- Doesn't solve sandbox permission issues
- Adds I/O overhead
- Still requires daemon restart eventually

**Implementation location:** `cmd/bd/daemon_event_loop.go`

## Recommended Implementation Plan

### Phase 1: Immediate Relief (1-2 hours)
1. ✅ Add `--force` flag to `bd import` (Solution 1)
2. ✅ Add `--allow-stale` global flag (Solution 2)
3. ✅ Update error message to suggest these flags

### Phase 2: Better UX (3-4 hours)
1. ✅ Implement sandbox detection heuristic (Solution 3)
2. ✅ Auto-enable `--sandbox` mode when detected
3. ✅ Update docs with sandbox troubleshooting

### Phase 3: Robustness (5-6 hours)
1. Enhance daemon health checks (Solution 4)
2. Add daemon metadata refresh (Solution 5)
3. Comprehensive testing in sandbox environments

## Testing Strategy

### Manual Testing in Codex Sandbox
1. Start daemon outside sandbox
2. Run `bd ready` inside sandbox → should detect sandbox
3. Run `bd import --force` → should update metadata
4. Run `bd ready --allow-stale` → should work despite staleness

### Automated Testing
1. Mock sandboxed environment (permission denied on signals)
2. Test daemon connection failure scenarios
3. Test metadata update in import with 0 changes
4. Test staleness check bypass flag

## Documentation Updates Needed

1. **TROUBLESHOOTING.md** - Add sandbox section with:
   - Symptoms of daemon lock issues
   - `--sandbox` flag usage
   - `--force` and `--allow-stale` as escape hatches

2. **CLI_REFERENCE.md** - Document new flags:
   - `--allow-stale` / `--no-staleness-check`
   - `bd import --force`

3. **Error message** in `staleness.go` - Add:
   ```
   If you're in a sandboxed environment (e.g., Codex):
     bd --sandbox ready
     bd import --force -i .beads/beads.jsonl
   ```

## Files to Modify

### Critical Path (Phase 1)
- [ ] `cmd/bd/import.go` - Add --force flag
- [ ] `cmd/bd/staleness.go` - Add staleness bypass, update error message
- [ ] `cmd/bd/main.go` - Add --allow-stale flag

### Enhancement (Phase 2-3)
- [ ] `cmd/bd/main.go` - Sandbox detection
- [ ] `cmd/bd/daemon_unix.go` - Permission-aware process checks
- [ ] `cmd/bd/daemon_event_loop.go` - Metadata refresh
- [ ] `internal/rpc/server_export_import_auto.go` - Better import handling

### Documentation
- [ ] `docs/TROUBLESHOOTING.md`
- [ ] `docs/CLI_REFERENCE.md`
- [ ] Issue #353 comment with workaround

## Open Questions

1. Should `--sandbox` auto-detect, or require explicit flag?
   - **Recommendation:** Start with explicit, add auto-detect in Phase 2

2. Should `--allow-stale` be per-command or global?
   - **Recommendation:** Global flag (less repetition)

3. What should happen to daemon lock files when daemon is unreachable?
   - **Recommendation:** Leave them (don't force-break locks), use direct mode

4. Should we add a `--force-direct` that ignores daemon locks entirely?
   - **Recommendation:** Not needed if sandbox detection works well

## Success Metrics

- Users in Codex can run `bd ready` without errors
- No false positives in sandbox detection
- Clear error messages guide users to solutions
- `bd import --force` always updates metadata
- `--sandbox` mode works reliably

---

**Investigation completed:** 2025-11-21
**Next steps:** Implement Phase 1 solutions
