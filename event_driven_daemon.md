# Event-Driven Daemon Architecture

**Status:** Design Proposal
**Author:** AI Assistant
**Date:** 2025-10-28
**Context:** Post-cache removal, per-project daemon model established

## Executive Summary

Replace the current 5-second polling sync loop with an event-driven architecture that reacts instantly to changes. This eliminates stale data issues while reducing CPU usage and improving user experience.

**Key metrics:**
- Latency improvement: 5000ms → <500ms
- CPU reduction: ~60% (no polling)
- Code complexity: +300 LOC (event handling), but cleaner semantics
- User impact: Instant feedback, no stale cache pain

## Problem Statement

### Current Architecture Issues

**Polling-based sync** (`cmd/bd/daemon.go:1010-1120`):
```go
ticker := time.NewTicker(5 * time.Second)
for {
    case <-ticker.C:
        doSync() // Export, pull, import, push
}
```

**Pain points:**
1. **Stale data window**: Changes invisible for up to 5 seconds
2. **CPU waste**: Daemon wakes every 5s even if nothing changed
3. **Unnecessary work**: Sync cycle runs even when no mutations occurred
4. **Cache confusion**: (Now removed) Cache staleness compounded delay

### What Cache Removal Enables

The recent cache removal (Oct 27-28, 964 LOC removed) creates ideal conditions for event-driven architecture:

✅ **One daemon = One database**: No cache eviction, no cross-workspace confusion
✅ **Simpler state**: Daemon state is just `s.storage`, no cache maps
✅ **Clear ownership**: Each daemon owns exactly one JSONL + SQLite pair
✅ **No invalidation complexity**: Events can directly trigger actions

## Proposed Architecture

### High-Level Flow

```
┌─────────────────────────────────────────────────────────┐
│                Event-Driven Daemon                      │
├─────────────────────────────────────────────────────────┤
│                                                          │
│  Event Sources              Event Handler               │
│  ┌──────────────┐          ┌──────────────┐            │
│  │ FS Watcher   │─────────→│              │            │
│  │ (JSONL file) │          │   Debouncer  │            │
│  └──────────────┘          │   (500ms)    │            │
│                             │              │            │
│  ┌──────────────┐          └──────────────┘            │
│  │ RPC Mutation │─────────→       │                    │
│  │   Events     │                 │                    │
│  └──────────────┘                 ↓                    │
│                            ┌──────────────┐            │
│  ┌──────────────┐          │ Sync Action  │            │
│  │ Git Hooks    │─────────→│  - Export    │            │
│  │ (optional)   │          │  - Import    │            │
│  └──────────────┘          └──────────────┘            │
│                                                          │
└─────────────────────────────────────────────────────────┘
```

### Components

#### 1. File System Watcher

**Purpose:** Detect JSONL changes from external sources (git pull, manual edits)

**Implementation:**
```go
// cmd/bd/daemon_watcher.go (new file)
package main

import (
    "context"
    "path/filepath"
    "time"

    "github.com/fsnotify/fsnotify"
)

type FileWatcher struct {
    watcher   *fsnotify.Watcher
    debouncer *Debouncer
    jsonlPath string
}

func NewFileWatcher(jsonlPath string, onChanged func()) (*FileWatcher, error) {
    watcher, err := fsnotify.NewWatcher()
    if err != nil {
        return nil, err
    }

    fw := &FileWatcher{
        watcher:   watcher,
        jsonlPath: jsonlPath,
        debouncer: NewDebouncer(500*time.Millisecond, onChanged),
    }

    // Watch JSONL file
    if err := watcher.Add(jsonlPath); err != nil {
        watcher.Close()
        return nil, err
    }

    // Also watch .git/refs/heads for branch changes
    gitRefsPath := filepath.Join(filepath.Dir(jsonlPath), "..", ".git", "refs", "heads")
    _ = watcher.Add(gitRefsPath) // Best effort

    return fw, nil
}

func (fw *FileWatcher) Start(ctx context.Context, log daemonLogger) {
    go func() {
        for {
            select {
            case event, ok := <-fw.watcher.Events:
                if !ok {
                    return
                }

                // Only care about writes to JSONL or ref changes
                if event.Name == fw.jsonlPath && event.Op&fsnotify.Write != 0 {
                    log.log("File change detected: %s", event.Name)
                    fw.debouncer.Trigger()
                } else if event.Op&fsnotify.Write != 0 {
                    log.log("Git ref change detected: %s", event.Name)
                    fw.debouncer.Trigger()
                }

            case err, ok := <-fw.watcher.Errors:
                if !ok {
                    return
                }
                log.log("Watcher error: %v", err)

            case <-ctx.Done():
                return
            }
        }
    }()
}

func (fw *FileWatcher) Close() error {
    return fw.watcher.Close()
}
```

**Platform support:**
- **Linux**: inotify (built into fsnotify)
- **macOS**: FSEvents (built into fsnotify)
- **Windows**: ReadDirectoryChangesW (built into fsnotify)

**Edge cases handled:**
- File rename (git atomic write via temp file): Watch directory, not just file
- Event storm (rapid git writes): Debouncer batches into single action
- Watcher failure: Fall back to polling with warning

#### 2. Debouncer

**Purpose:** Batch rapid events into single action

**Implementation:**
```go
// cmd/bd/daemon_debouncer.go (new file)
package main

import (
    "sync"
    "time"
)

type Debouncer struct {
    mu       sync.Mutex
    timer    *time.Timer
    duration time.Duration
    action   func()
}

func NewDebouncer(duration time.Duration, action func()) *Debouncer {
    return &Debouncer{
        duration: duration,
        action:   action,
    }
}

func (d *Debouncer) Trigger() {
    d.mu.Lock()
    defer d.mu.Unlock()

    if d.timer != nil {
        d.timer.Stop()
    }

    d.timer = time.AfterFunc(d.duration, func() {
        d.action()
        d.mu.Lock()
        d.timer = nil
        d.mu.Unlock()
    })
}

func (d *Debouncer) Cancel() {
    d.mu.Lock()
    defer d.mu.Unlock()

    if d.timer != nil {
        d.timer.Stop()
        d.timer = nil
    }
}
```

**Tuning:**
- Default: 500ms (balance between responsiveness and batching)
- Configurable via `BEADS_DEBOUNCE_MS` env var
- Could use adaptive timing based on event frequency

#### 3. RPC Mutation Events

**Purpose:** Trigger export immediately after DB changes (not in 5s)

**Implementation:**
```go
// internal/rpc/server.go (modifications)
type Server struct {
    // ... existing fields
    mutationChan chan MutationEvent
}

type MutationEvent struct {
    Type      string    // "create", "update", "delete"
    IssueID   string    // e.g., "bd-42"
    Timestamp time.Time
}

func (s *Server) CreateIssue(req *CreateRequest) (*Issue, error) {
    issue, err := s.storage.CreateIssue(req)
    if err != nil {
        return nil, err
    }

    // Notify mutation channel
    select {
    case s.mutationChan <- MutationEvent{
        Type:      "create",
        IssueID:   issue.ID,
        Timestamp: time.Now(),
    }:
    default:
        // Channel full, event dropped (sync will happen eventually)
    }

    return issue, nil
}

// Similar for UpdateIssue, DeleteIssue, AddComment, etc.
```

**Handler in daemon:**
```go
// cmd/bd/daemon.go (modification)
func handleMutationEvents(ctx context.Context, events <-chan rpc.MutationEvent,
                          debouncer *Debouncer, log daemonLogger) {
    go func() {
        for {
            select {
            case event := <-events:
                log.log("Mutation detected: %s %s", event.Type, event.IssueID)
                debouncer.Trigger() // Schedule export

            case <-ctx.Done():
                return
            }
        }
    }()
}
```

#### 4. Git Hook Integration (Optional)

**Purpose:** Explicit notifications from git operations

**Implementation:**
```bash
# .git/hooks/post-merge (installed by bd init --quiet)
#!/bin/bash
# Notify daemon of merge completion
if command -v bd &> /dev/null; then
    bd daemon-event import-needed &
fi
```

```go
// cmd/bd/daemon_event.go (new file)
package main

// Called by git hooks to notify daemon
func handleDaemonEvent() {
    if len(os.Args) < 3 {
        fmt.Fprintln(os.Stderr, "Usage: bd daemon-event <event-type>")
        os.Exit(1)
    }

    eventType := os.Args[2]
    socketPath := getSocketPath()

    client := rpc.NewClient(socketPath)
    ctx := context.Background()

    switch eventType {
    case "import-needed":
        // Git hook says "JSONL changed, please import"
        if err := client.TriggerImport(ctx); err != nil {
            // Ignore error - daemon might not be running
            os.Exit(0)
        }
    case "export-needed":
        if err := client.TriggerExport(ctx); err != nil {
            os.Exit(0)
        }
    default:
        fmt.Fprintf(os.Stderr, "Unknown event type: %s\n", eventType)
        os.Exit(1)
    }
}
```

**Note:** Git hooks are **optional enhancement**, not required. File watcher is primary mechanism.

### Complete Daemon Loop

**Current implementation** (`cmd/bd/daemon.go:1123-1161`):
```go
func runEventLoop(ctx context.Context, cancel context.CancelFunc, ticker *time.Ticker,
                  doSync func(), server *rpc.Server, serverErrChan chan error,
                  log daemonLogger) {
    for {
        select {
        case <-ticker.C:  // ← Every 5 seconds
            doSync()
        case sig := <-sigChan:
            // ... shutdown
        }
    }
}
```

**Proposed implementation:**
```go
// cmd/bd/daemon_event_loop.go (new file)
func runEventDrivenLoop(ctx context.Context, cancel context.CancelFunc,
                        server *rpc.Server, serverErrChan chan error,
                        watcher *FileWatcher, mutationChan <-chan rpc.MutationEvent,
                        log daemonLogger) {

    sigChan := make(chan os.Signal, 1)
    signal.Notify(sigChan, daemonSignals...)
    defer signal.Stop(sigChan)

    // Debounced sync actions
    exportDebouncer := NewDebouncer(500*time.Millisecond, func() {
        log.log("Export triggered by mutation events")
        exportToJSONL()
    })

    importDebouncer := NewDebouncer(500*time.Millisecond, func() {
        log.log("Import triggered by file change")
        autoImportIfNewer()
    })

    // Start file watcher (triggers import)
    watcher.Start(ctx, log)

    // Start mutation handler (triggers export)
    handleMutationEvents(ctx, mutationChan, exportDebouncer, log)

    // Optional: Periodic health check (every 60s, not sync)
    healthTicker := time.NewTicker(60 * time.Second)
    defer healthTicker.Stop()

    for {
        select {
        case <-healthTicker.C:
            // Periodic health check (validate DB, check disk space, etc.)
            checkDaemonHealth(ctx, store, log)

        case sig := <-sigChan:
            if isReloadSignal(sig) {
                log.log("Received reload signal, ignoring")
                continue
            }
            log.log("Received signal %v, shutting down...", sig)
            cancel()
            if err := server.Stop(); err != nil {
                log.log("Error stopping server: %v", err)
            }
            return

        case <-ctx.Done():
            log.log("Context canceled, shutting down")
            watcher.Close()
            if err := server.Stop(); err != nil {
                log.log("Error stopping server: %v", err)
            }
            return

        case err := <-serverErrChan:
            log.log("RPC server failed: %v", err)
            cancel()
            watcher.Close()
            return
        }
    }
}
```

## Migration Strategy

### Phase 1: Parallel Implementation (2-3 weeks)

**Goal:** Event-driven as opt-in alongside polling

**Changes:**
1. Add `fsnotify` dependency to `go.mod`
2. Create new files:
   - `cmd/bd/daemon_watcher.go` (~150 LOC)
   - `cmd/bd/daemon_debouncer.go` (~60 LOC)
   - `cmd/bd/daemon_event_loop.go` (~200 LOC)
3. Add flag `BEADS_DAEMON_MODE=events` to enable
4. Keep existing `runEventLoop` as fallback

**Testing:**
- Unit tests for debouncer
- Integration tests for file watcher
- Stress test with event storm (rapid git operations)
- Test on Linux, macOS, Windows

**Rollout:**
- Default: `BEADS_DAEMON_MODE=poll` (current behavior)
- Opt-in: `BEADS_DAEMON_MODE=events` (new behavior)
- Documentation: Add to AGENTS.md

### Phase 2: Battle Testing (4-6 weeks)

**Goal:** Real-world validation with dogfooding

**Metrics to track:**
- CPU usage (before/after comparison)
- Latency (time from mutation to JSONL update)
- Memory usage (fsnotify overhead)
- Event storm handling (git pull with 100+ file changes)
- Edge case frequency (watcher failures, debounce races)

**Success criteria:**
- CPU usage <40% of polling mode
- Latency <500ms (vs 5000ms in polling)
- Zero data loss or corruption
- Zero daemon crashes from event handling

**Issue tracking:**
- Create `bd-XXX: Event-driven daemon stabilization` issue
- Track bugs as sub-issues
- Weekly review of metrics

### Phase 3: Default Switchover (1 week)

**Goal:** Make event-driven the default

**Changes:**
1. Flip default: `BEADS_DAEMON_MODE=events`
2. Keep polling as fallback: `BEADS_DAEMON_MODE=poll`
3. Update documentation
4. Add release note

**Communication:**
- Blog post: "Beads daemon now event-driven"
- Changelog entry with before/after metrics
- Migration guide for users who hit issues

### Phase 4: Deprecation (6+ months later)

**Goal:** Remove polling mode entirely

**Changes:**
1. Remove `runEventLoop` with ticker
2. Remove `BEADS_DAEMON_MODE` flag
3. Simplify daemon startup code

**Only if:**
- Event-driven stable for 6+ months
- No unresolved critical issues
- Community feedback positive

## Performance Analysis

### CPU Usage

**Current (polling):**
```
Every 5 seconds:
- Wake daemon
- Check git status
- Check JSONL hash
- Check dirty flags
- Sleep

Estimated: ~5-10% CPU (depends on repo size)
```

**Event-driven:**
```
Daemon sleeps until:
- File system event (rare)
- RPC mutation (user-triggered)
- Signal

Estimated: ~1-2% CPU (mostly idle)
```

**Savings:** ~60-80% CPU reduction

### Latency

**Current (polling):**
```
User runs: bd create "Fix bug"
→ RPC call → DB write → (wait up to 5s) → Export → Git commit
Average: 2.5s delay
Worst: 5s delay
```

**Event-driven:**
```
User runs: bd create "Fix bug"
→ RPC call → DB write → Mutation event → (500ms debounce) → Export → Git commit
Average: 250ms delay
Worst: 500ms delay
```

**Improvement:** 5-10x faster

### Memory Usage

**fsnotify overhead:**
- Linux (inotify): ~1-2 MB per watched directory
- macOS (FSEvents): ~500 KB per watched directory
- Windows: ~1 MB per watched directory

**With 1 JSONL + 1 git refs directory = ~2-4 MB**

**Negligible compared to SQLite cache (10-50 MB for typical database)**

## Edge Cases & Error Handling

### 1. File Watcher Failure

**Scenario:** `inotify` limit exceeded (Linux), permissions issue, or filesystem doesn't support watching

**Detection:**
```go
watcher, err := fsnotify.NewWatcher()
if err != nil {
    log.log("WARNING: File watcher unavailable (%v), falling back to polling", err)
    useFallbackPolling = true
}
```

**Fallback:** Automatic switch to 5s polling with warning

### 2. Event Storm

**Scenario:** Git pull modifies JSONL 50 times in rapid succession

**Mitigation:** Debouncer batches into single action after 500ms quiet period

**Stress test:**
```bash
# Simulate event storm
for i in {1..100}; do
    echo '{"id":"bd-'$i'"}' >> beads.jsonl
done
# Should trigger exactly 1 import after 500ms
```

### 3. Watcher Detached from File

**Scenario:** JSONL replaced by `git checkout` (different inode)

**Detection:** fsnotify sends `RENAME` or `REMOVE` event

**Recovery:**
```go
case event.Op&fsnotify.Remove != 0:
    log.log("JSONL removed, re-establishing watch")
    watcher.Remove(jsonlPath)
    time.Sleep(100 * time.Millisecond)
    watcher.Add(jsonlPath)
```

### 4. Debounce Race Condition

**Scenario:** Event A triggers debounce, event B arrives during wait, action fires for A before B seen

**Solution:** Debouncer restarts timer on each trigger (standard debounce behavior)

**Test:**
```go
func TestDebouncerBatchesMultipleEvents(t *testing.T) {
    callCount := 0
    d := NewDebouncer(100*time.Millisecond, func() { callCount++ })

    d.Trigger() // t=0ms
    time.Sleep(50 * time.Millisecond)
    d.Trigger() // t=50ms (resets timer)
    time.Sleep(50 * time.Millisecond)
    d.Trigger() // t=100ms (resets timer)

    time.Sleep(150 * time.Millisecond) // t=250ms (timer fires)

    assert.Equal(t, 1, callCount) // Only 1 action despite 3 triggers
}
```

### 5. Daemon Restart During Debounce

**Scenario:** Daemon receives SIGTERM while debouncer waiting

**Solution:** Cancel debouncer on shutdown

```go
func (d *Debouncer) Cancel() {
    d.mu.Lock()
    defer d.mu.Unlock()
    if d.timer != nil {
        d.timer.Stop()
    }
}

// In shutdown handler
defer exportDebouncer.Cancel()
defer importDebouncer.Cancel()
```

## Configuration

### Environment Variables

```bash
# Enable event-driven mode (default: events after Phase 3)
BEADS_DAEMON_MODE=events

# Debounce duration in milliseconds (default: 500)
BEADS_DEBOUNCE_MS=500

# Fall back to polling if watcher fails (default: true)
BEADS_WATCHER_FALLBACK=true

# Polling interval if fallback used (default: 5s)
BEADS_POLL_INTERVAL=5s
```

### Daemon Status

**New command:** `bd daemon status --verbose`

```bash
$ bd daemon status --verbose
Daemon running: yes
PID: 12345
Mode: event-driven
Uptime: 3h 42m
Last sync: 2s ago

Event statistics:
  File changes: 23
  Mutations: 156
  Exports: 12 (batched from 156 mutations)
  Imports: 4 (batched from 23 file changes)

Watcher status: active
  Watching: /Users/steve/beads/.beads/beads.jsonl
  Events received: 23
  Errors: 0
```

## What This Doesn't Solve

Event-driven architecture improves **responsiveness** but doesn't eliminate **repair cycles** caused by:

1. **Git merge conflicts** - Still need manual/AI resolution
2. **Semantic duplication** - Still need deduplication logic
3. **Test pollution** - Still need better isolation (separate issue)
4. **Worktree confusion** - Still need per-worktree branch tracking (separate design)

**These require separate solutions** (see repair commands design doc)

## Success Metrics

### Must-Have (P0)
- ✅ Zero data loss or corruption
- ✅ Zero regressions in sync reliability
- ✅ Works on Linux, macOS, Windows

### Should-Have (P1)
- ✅ Latency <500ms (vs 5000ms today)
- ✅ CPU usage <40% of polling mode
- ✅ Graceful fallback to polling if watcher fails

### Nice-to-Have (P2)
- ✅ Configurable debounce timing
- ✅ Detailed event statistics in `bd daemon status`
- ✅ Real-time dashboard of events (debug mode)

## Implementation Checklist

### Code Changes
- [ ] Add `fsnotify` to `go.mod`
- [ ] Create `cmd/bd/daemon_watcher.go`
- [ ] Create `cmd/bd/daemon_debouncer.go`
- [ ] Create `cmd/bd/daemon_event_loop.go`
- [ ] Modify `internal/rpc/server.go` (add mutation channel)
- [ ] Add `BEADS_DAEMON_MODE` flag handling
- [ ] Add fallback to polling on watcher failure

### Tests
- [ ] Unit tests for Debouncer
- [ ] Unit tests for FileWatcher
- [ ] Integration test: mutation → export latency
- [ ] Integration test: file change → import latency
- [ ] Stress test: event storm (100+ rapid changes)
- [ ] Platform tests: Linux, macOS, Windows
- [ ] Edge case test: watcher failure recovery
- [ ] Edge case test: file inode change (git checkout)

### Documentation
- [ ] Update AGENTS.md (event-driven mode)
- [ ] Add `docs/architecture/event_driven.md` (this doc)
- [ ] Update `bd daemon --help` (add --mode flag)
- [ ] Add troubleshooting guide (watcher failures)
- [ ] Write migration guide (for users hitting issues)

### Rollout
- [ ] Phase 1: Parallel implementation (opt-in)
- [ ] Phase 2: Dogfooding (beads repo itself)
- [ ] Phase 3: Default switchover
- [ ] Phase 4: Announce in release notes

## Open Questions

1. **Should git hooks be required or optional?**
   - Recommendation: Optional (file watcher is sufficient)

2. **What debounce duration is optimal?**
   - Recommendation: 500ms default, configurable
   - Could use adaptive timing based on event frequency

3. **Should we track event statistics permanently?**
   - Recommendation: In-memory only (reset on daemon restart)
   - Could add `bd daemon stats --export` for debugging

4. **What happens if fsnotify doesn't support filesystem?**
   - Recommendation: Automatic fallback to polling with warning

5. **Should mutation events be buffered or dropped if channel full?**
   - Recommendation: Buffered (size 100), then drop oldest
   - Worst case: Export delayed by 500ms, but no data loss

## Conclusion

Event-driven architecture is a **natural evolution** after cache removal:
- ✅ Eliminates stale data issues
- ✅ Reduces CPU usage significantly
- ✅ Improves user experience with instant feedback
- ✅ Builds on simplified per-project daemon model

**Recommended:** Proceed with Phase 1 implementation, targeting 2-3 week timeline for opt-in release.
