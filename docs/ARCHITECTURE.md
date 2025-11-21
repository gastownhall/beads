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
