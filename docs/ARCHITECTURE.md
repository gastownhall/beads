# Architecture

This document describes the internal architecture of the `bd` issue tracker, with particular focus on concurrency guarantees and data consistency.

## Auto-Flush Architecture

### Problem Statement (Issue bd-52)

The original auto-flush implementation suffered from a critical race condition when multiple concurrent operations accessed shared state:

- **Concurrent access points:**
  - Auto-flush timer goroutine (5s debounce)
  - Daemon sync goroutine
  - Concurrent CLI commands
  - Git hook execution
  - PersistentPostRun cleanup

- **Shared mutable state:**
  - `isDirty` flag
  - `needsFullExport` flag
  - `flushTimer` instance
  - `storeActive` flag

- **Impact:**
  - Potential data loss under concurrent load
  - Corruption when multiple agents/commands run simultaneously
  - Race conditions during rapid commits
  - Flush operations could access closed storage

### Solution: Event-Driven FlushManager

The race condition was eliminated by replacing timer-based shared state with an event-driven architecture using a single-owner pattern.

#### Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Command/Agent                        │
│                                                          │
│  markDirtyAndScheduleFlush() ─┐                         │
│  markDirtyAndScheduleFullExport() ─┐                    │
└────────────────────────────────────┼───┼────────────────┘
                                     │   │
                                     v   v
                    ┌────────────────────────────────────┐
                    │        FlushManager                │
                    │  (Single-Owner Pattern)            │
                    │                                    │
                    │  Channels (buffered):              │
                    │    - markDirtyCh                   │
                    │    - timerFiredCh                  │
                    │    - flushNowCh                    │
                    │    - shutdownCh                    │
                    │                                    │
                    │  State (owned by run() goroutine): │
                    │    - isDirty                       │
                    │    - needsFullExport               │
                    │    - debounceTimer                 │
                    └────────────────────────────────────┘
                                     │
                                     v
                    ┌────────────────────────────────────┐
                    │      flushToJSONLWithState()       │
                    │                                    │
                    │  - Validates store is active       │
                    │  - Checks JSONL integrity          │
                    │  - Performs incremental/full export│
                    │  - Updates export hashes           │
                    └────────────────────────────────────┘
```

#### Key Design Principles

**1. Single Owner Pattern**

All flush state (`isDirty`, `needsFullExport`, `debounceTimer`) is owned by a single background goroutine (`FlushManager.run()`). This eliminates the need for mutexes to protect this state.

**2. Channel-Based Communication**

External code communicates with FlushManager via buffered channels:
- `markDirtyCh`: Request to mark DB dirty (incremental or full export)
- `timerFiredCh`: Debounce timer expired notification
- `flushNowCh`: Synchronous flush request (returns error)
- `shutdownCh`: Graceful shutdown with final flush

**3. No Shared Mutable State**

The only shared state is accessed via atomic operations (channel sends/receives). The `storeActive` flag and `store` pointer still use a mutex, but only to coordinate with store lifecycle, not flush logic.

**4. Debouncing Without Locks**

The timer callback sends to `timerFiredCh` instead of directly manipulating state. The run() goroutine processes timer events in its select loop, eliminating timer-related races.

#### Concurrency Guarantees

**Thread-Safety:**
- `MarkDirty(fullExport bool)` - Safe from any goroutine, non-blocking
- `FlushNow() error` - Safe from any goroutine, blocks until flush completes
- `Shutdown() error` - Idempotent, safe to call multiple times

**Debouncing Guarantees:**
- Multiple `MarkDirty()` calls within the debounce window → single flush
- Timer resets on each mark, flush occurs after last modification
- FlushNow() bypasses debounce, forces immediate flush

**Shutdown Guarantees:**
- Final flush performed if database is dirty
- Background goroutine cleanly exits
- Idempotent via `sync.Once` - safe for multiple calls
- Subsequent operations after shutdown are no-ops

**Store Lifecycle:**
- FlushManager checks `storeActive` flag before every flush
- Store closure is coordinated via `storeMutex`
- Flush safely aborts if store closes mid-operation

#### Migration Path

The implementation maintains backward compatibility:

1. **Legacy path (tests):** If `flushManager == nil`, falls back to old timer-based logic
2. **New path (production):** Uses FlushManager event-driven architecture
3. **Wrapper functions:** `markDirtyAndScheduleFlush()` and `markDirtyAndScheduleFullExport()` delegate to FlushManager when available

This allows existing tests to pass without modification while fixing the race condition in production.

## Testing

### Race Detection

Comprehensive race detector tests ensure concurrency safety:

- `TestFlushManagerConcurrentMarkDirty` - Many goroutines marking dirty
- `TestFlushManagerConcurrentFlushNow` - Concurrent immediate flushes
- `TestFlushManagerMarkDirtyDuringFlush` - Interleaved mark/flush operations
- `TestFlushManagerShutdownDuringOperation` - Shutdown while operations ongoing
- `TestMarkDirtyAndScheduleFlushConcurrency` - Integration test with legacy API

Run with: `go test -race -run TestFlushManager ./cmd/bd`

### In-Process Test Compatibility

The FlushManager is designed to work correctly when commands run multiple times in the same process (common in tests):

- Each command execution in `PersistentPreRun` creates a new FlushManager
- `PersistentPostRun` shuts down the manager
- `Shutdown()` is idempotent via `sync.Once`
- Old managers are garbage collected when replaced

## Related Subsystems

### Daemon Mode

When running with daemon mode (`--no-daemon=false`), the CLI delegates to an RPC server. The FlushManager is NOT used in daemon mode - the daemon process has its own flush coordination.

The `daemonClient != nil` check in `PersistentPostRun` ensures FlushManager shutdown only occurs in direct mode.

### Auto-Import

Auto-import runs in `PersistentPreRun` before FlushManager is used. It may call `markDirtyAndScheduleFlush()` or `markDirtyAndScheduleFullExport()` if JSONL changes are detected.

Hash-based comparison (not mtime) prevents git pull false positives (issue bd-84).

### JSONL Integrity

`flushToJSONLWithState()` validates JSONL file hash before flush:
- Compares stored hash with actual file hash
- If mismatch detected, clears export_hashes and forces full re-export (issue bd-160)
- Prevents staleness when JSONL is modified outside bd

### Export Modes

**Incremental export (default):**
- Exports only dirty issues (tracked in `dirty_issues` table)
- Merges with existing JSONL file
- Faster for small changesets

**Full export (after ID changes):**
- Exports all issues from database
- Rebuilds JSONL from scratch
- Required after operations like `rename-prefix` that change issue IDs
- Triggered by `markDirtyAndScheduleFullExport()`

## Performance Characteristics

- **Debounce window:** Configurable via `getDebounceDuration()` (default 5s)
- **Channel buffer sizes:**
  - markDirtyCh: 10 events (prevents blocking during bursts)
  - timerFiredCh: 1 event (timer notifications coalesce naturally)
  - flushNowCh: 1 request (synchronous, one at a time)
  - shutdownCh: 1 request (one-shot operation)
- **Memory overhead:** One goroutine + minimal channel buffers per command execution
- **Flush latency:** Debounce duration + JSONL write time (typically <100ms for incremental)

## Blocked Issues Cache (bd-5qim)

### Problem Statement

The `bd ready` command originally computed blocked issues using a recursive CTE on every query. On a 10K issue database, each query took ~752ms, making the command feel sluggish and impractical for large projects.

### Solution: Materialized Cache Table

The `blocked_issues_cache` table materializes the blocking computation, storing issue IDs for all currently blocked issues. Queries now use a simple `NOT EXISTS` check against this cache, completing in ~29ms (25x speedup).

### Architecture

```
┌─────────────────────────────────────────────────────────┐
│                   GetReadyWork Query                     │
│                                                          │
│  SELECT ... FROM issues WHERE status IN (...)            │
│  AND NOT EXISTS (                                        │
│    SELECT 1 FROM blocked_issues_cache                    │
│    WHERE issue_id = issues.id                            │
│  )                                                       │
│                                                          │
│  Performance: 29ms (was 752ms with recursive CTE)       │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────┐
│              Cache Invalidation Triggers                 │
│                                                          │
│  1. AddDependency (blocks/parent-child only)             │
│  2. RemoveDependency (blocks/parent-child only)          │
│  3. UpdateIssue (on any status change)                   │
│  4. CloseIssue (changes status to closed)                │
│                                                          │
│  NOT triggered by: related, discovered-from deps         │
└─────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────┐
│               Cache Rebuild Process                      │
│                                                          │
│  1. DELETE FROM blocked_issues_cache                     │
│  2. INSERT INTO blocked_issues_cache                     │
│     WITH RECURSIVE CTE:                                  │
│       - Find directly blocked issues (blocks deps)       │
│       - Propagate to children (parent-child deps)        │
│  3. Happens in same transaction as triggering change     │
│                                                          │
│  Performance: <50ms full rebuild on 10K database         │
└─────────────────────────────────────────────────────────┘
```

### Blocking Semantics

An issue is blocked if:

1. **Direct blocking**: Has a `blocks` dependency on an open/in_progress/blocked issue
2. **Transitive blocking**: Parent is blocked and issue is connected via `parent-child` dependency

Closed issues never block others. Related and discovered-from dependencies don't affect blocking.

### Cache Invalidation Strategy

**Full rebuild on every change**

Instead of incremental updates, the cache is completely rebuilt (DELETE + INSERT) on any triggering change. This approach is chosen because:

- Rebuild is fast (<50ms even on 10K issues) due to optimized CTE
- Simpler implementation with no risk of partial/stale updates
- Dependency changes are rare compared to reads
- Guarantees consistency - cache matches database state exactly

**Transaction safety**

All cache operations happen within the same transaction as the triggering change:
- Uses transaction if provided, otherwise direct db connection
- Cache can never be in an inconsistent state visible to queries
- Foreign key CASCADE ensures cache entries deleted when issues are deleted

**Selective invalidation**

Only `blocks` and `parent-child` dependencies trigger rebuilds since they affect blocking semantics. Related and discovered-from dependencies don't trigger invalidation, avoiding unnecessary work.

### Performance Characteristics

**Query performance (GetReadyWork):**
- Before cache: ~752ms (recursive CTE)
- With cache: ~29ms (NOT EXISTS)
- Speedup: 25x

**Write overhead:**
- Cache rebuild: <50ms
- Only triggered on dependency/status changes (rare operations)
- Trade-off: slower writes for much faster reads

### Edge Cases

1. **Parent-child transitive blocking**
   - Children of blocked parents are automatically marked as blocked
   - Propagates through arbitrary depth hierarchies (limited to depth 50 for safety)

2. **Multiple blockers**
   - Issue blocked by multiple open issues stays blocked until all are closed
   - DISTINCT in CTE ensures issue appears once in cache

3. **Status changes**
   - Closing a blocker removes all blocked descendants from cache
   - Reopening a blocker adds them back

4. **Dependency removal**
   - Removing last blocker unblocks the issue
   - Removing parent-child link unblocks orphaned subtree

5. **Foreign key cascades**
   - Cache entries automatically deleted when issue is deleted
   - No manual cleanup needed

### Testing

Comprehensive test coverage in `blocked_cache_test.go`:
- Cache invalidation on dependency add/remove
- Cache updates on status changes
- Multiple blockers
- Deep hierarchies
- Transitive blocking via parent-child
- Related dependencies (should NOT affect cache)

Run tests: `go test -v ./internal/storage/sqlite -run TestCache`

### Implementation Files

- `internal/storage/sqlite/blocked_cache.go` - Cache rebuild and invalidation
- `internal/storage/sqlite/ready.go` - Uses cache in GetReadyWork queries
- `internal/storage/sqlite/dependencies.go` - Invalidates on dep changes
- `internal/storage/sqlite/queries.go` - Invalidates on status changes
- `internal/storage/sqlite/migrations/015_blocked_issues_cache.go` - Schema and initial population

### Future Optimizations

If rebuild becomes a bottleneck in very large databases (>100K issues):
- Consider incremental updates for specific dependency types
- Add indexes to dependencies table for CTE performance
- Implement dirty tracking to avoid rebuilds when cache is unchanged

However, current performance is excellent for realistic workloads.

## Future Improvements

Potential enhancements for multi-agent scenarios:

1. **Flush coordination across agents:**
   - Shared lock file to prevent concurrent JSONL writes
   - Detection of external JSONL modifications during flush

2. **Adaptive debounce window:**
   - Shorter debounce during interactive sessions
   - Longer debounce during batch operations

3. **Flush progress tracking:**
   - Expose flush queue depth via status API
   - Allow clients to wait for flush completion

4. **Per-issue dirty tracking optimization:**
   - Currently tracks full vs. incremental
   - Could track specific issue IDs for surgical updates
