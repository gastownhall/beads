# main_test.go Cleanup Plan

## Problem
main_test.go has 18 tests using deprecated global state (isDirty, flushTimer, flushMutex).
These tests are slow (14 newTestStore() calls) and redundant with flush_manager_test.go.

## Root Cause
- FlushManager refactoring (bd-52) moved flush logic to isolated FlushManager
- Legacy path kept "for backward compatibility with tests"
- main_test.go still tests the DEPRECATED legacy path
- flush_manager_test.go tests the NEW FlushManager path

## Solution: Three-Phase Cleanup

### Phase 1: Remove Redundant Tests (THIS SESSION)

#### Tests to DELETE (covered by flush_manager_test.go):

1. **TestAutoFlushDirtyMarking** (line 22)
   - Tests that isDirty flag gets set
   - COVERED BY: TestFlushManagerMarkDirtyTriggersFlush
   - Uses: global isDirty, flushTimer

2. **TestAutoFlushDisabled** (line 59)
   - Tests that --no-auto-flush disables flushing
   - COVERED BY: TestFlushManagerDisabledDoesNotFlush
   - Uses: global autoFlushEnabled

3. **TestAutoFlushDebounce** (line 90)
   - ALREADY SKIPPED with note: "obsolete - tested in flush_manager_test.go"
   - DELETE entirely

4. **TestAutoFlushClearState** (line 184)
   - Tests clearAutoFlushState() resets flags
   - clearAutoFlushState() is legacy-only (no FlushManager equivalent yet)
   - Will be replaced when we add FlushManager.MarkClean()
   - DELETE (clearAutoFlushState tested implicitly in export/sync commands)

5. **TestAutoFlushConcurrency** (line 355)
   - Tests concurrent markDirtyAndScheduleFlush() calls
   - COVERED BY: TestFlushManagerConcurrentMarkDirty
   - Uses: global isDirty, flushTimer

6. **TestAutoFlushStoreInactive** (line 403)
   - Tests flush behavior when store is closed
   - COVERED BY: TestPerformFlushStoreInactive
   - Uses: global storeActive

7. **TestAutoFlushErrorHandling** (line 582)
   - Tests error scenarios (JSONL as directory)
   - COVERED BY: TestPerformFlushErrorHandling
   - Uses: newTestStore(), global state

#### Tests to KEEP (unique integration value):

1. **TestAutoFlushOnExit** (line 219)
   - Tests PersistentPostRun() calls flushManager.Shutdown()
   - Integration test: CLI lifecycle → flush behavior
   - NOT covered by flush_manager_test.go
   - **REFACTOR** to use FlushManager directly (not global state)

2. **TestAutoFlushJSONLContent** (line 446)
   - Tests actual JSONL file content after flush
   - Integration test: DB mutations → JSONL file output
   - NOT covered by flush_manager_test.go
   - **REFACTOR** to set up FlushManager properly

3. **Auto-import tests** (9 tests, lines 701-1412)
   - Test DB ↔ JSONL synchronization
   - Test merge conflict detection
   - Test status transition invariants
   - Integration tests with filesystem/git operations
   - **DEFER** to separate cleanup (different subsystem)

### Phase 2: Remove Legacy Path

After deleting redundant tests:

1. Add `MarkClean()` method to FlushManager
2. Update `clearAutoFlushState()` to use `flushManager.MarkClean()`
3. Remove legacy path from `markDirtyAndScheduleFlush()`
4. Remove legacy path from `markDirtyAndScheduleFullExport()`

### Phase 3: Remove Global State

After removing legacy path:

1. Remove global variables:
   - `isDirty` (line 72 in main.go)
   - `flushTimer` (line 75 in main.go)
   - `flushMutex` (line 74 in main.go)

2. Update test cleanup code:
   - cli_fast_test.go: Remove isDirty/flushTimer reset
   - direct_mode_test.go: Remove isDirty/flushTimer save/restore

## Expected Impact

### Before:
- 18 tests in main_test.go
- 14 newTestStore() calls
- ~15-20 seconds runtime (estimated)
- Testing deprecated code path

### After Phase 1:
- 11 tests in main_test.go (7 deleted)
- ~6-8 newTestStore() calls (auto-import tests)
- ~5-7 seconds runtime (estimated)
- Testing only integration behavior

### After Phase 2:
- Same test count
- Cleaner code (no legacy path)
- Tests use FlushManager directly

### After Phase 3:
- Same test count
- No global state pollution
- Tests can run in parallel (t.Parallel())
- ~2-3 seconds runtime (estimated)

## Implementation Steps

1. Add t.Skip() to 7 redundant tests ✓
2. Run tests to verify nothing breaks ✓
3. Delete skipped tests ✓
4. Refactor 2 keeper tests to use FlushManager
5. Add FlushManager.MarkClean() method
6. Remove legacy paths
7. Remove global variables
8. Run full test suite

## Files Modified

- `cmd/bd/main_test.go` - Delete 7 tests, refactor 2 tests
- `cmd/bd/flush_manager.go` - Add MarkClean() method
- `cmd/bd/autoflush.go` - Remove legacy paths
- `cmd/bd/main.go` - Remove global variables (Phase 3)
- `docs/MAIN_TEST_REFACTOR_NOTES.md` - Update with new approach

## References

- Original analysis: `docs/MAIN_TEST_REFACTOR_NOTES.md`
- FlushManager implementation: `cmd/bd/flush_manager.go`
- FlushManager tests: `cmd/bd/flush_manager_test.go`
- Issue bd-52: FlushManager refactoring
- Issue bd-159: Test config reference
