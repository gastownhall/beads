# main_test.go Refactoring Notes (bd-1rh follow-up)

## Status: RESOLVED - Redundant Tests Deleted ✅

### Summary
Attempted to refactor `main_test.go` (18 tests, 14 `newTestStore()` calls) to use shared DB pattern like P1 files. **Discovered fundamental incompatibility** with shared DB approach due to global state manipulation and integration test characteristics.

### What We Tried
1. Created `TestAutoFlushSuite` and `TestAutoImportSuite` with shared DB
2. Converted 18 individual tests to subtests
3. Reduced from 14 DB setups to 2

### Problems Encountered

#### 1. **Deadlock Issue**
- Tests call `flushToJSONL()` which accesses the database
- Test cleanup (from `newTestStore()`) tries to close the database
- Results in database lock contention and test timeouts
- Stack trace shows: `database/sql.(*DB).Close()` waiting while `flushToJSONL()` is accessing DB

#### 2. **Global State Manipulation**
These tests heavily manipulate package-level globals:
- `autoFlushEnabled`, `isDirty`, `flushTimer`
- `store`, `storeActive`, `storeMutex`
- `dbPath` (used to compute JSONL path dynamically)
- `flushFailureCount`, `lastFlushError`

#### 3. **Integration Test Characteristics**
- Tests simulate end-to-end flush/import workflows
- Tests capture stderr to verify error messages
- Tests manipulate filesystem state directly
- Tests create directories to force error conditions

### Key Differences from P1 Tests

| Aspect | P1 Tests (create, dep, etc.) | main_test.go |
|--------|------------------------------|--------------|
| **DB Usage** | Pure DB operations | Global state + DB + filesystem |
| **Isolation** | Data-level only | Requires process-level isolation |
| **Cleanup** | Simple | Complex (timers, goroutines, mutexes) |
| **Pattern** | CRUD operations | Workflow simulation |

### Why Shared DB Doesn't Work

1. **`jsonlPath` is computed dynamically** from `dbPath` via `findJSONLPath()`
   - Not a global variable like in old tests
   - Changes to `dbPath` affect where JSONL files are written/read

2. **Tests need to control exact JSONL paths** for:
   - Creating files to force errors (making JSONL a directory)
   - Verifying files were/weren't created
   - Touching files to simulate git pull scenarios

3. **Concurrent access issues**:
   - Background flush operations may trigger during test cleanup
   - Global mutexes protect state but cause deadlocks with shared DB

### What Tests Actually Do

#### Auto-Flush Tests (9 tests)
- Test global state flags (`isDirty`, `autoFlushEnabled`)
- Test timer management (`flushTimer`)
- Test concurrency (goroutines calling `markDirtyAndScheduleFlush()`)
- Simulate program exit (PersistentPostRun behavior)
- Force error conditions by making JSONL path a directory

#### Auto-Import Tests (9 tests)
- Test JSONL -> DB sync when JSONL is newer
- Test merge conflict detection (literal `<<<<<<<` markers in file)
- Test JSON-encoded conflict markers (false positive prevention)
- Test status transition invariants (closed_at management)
- Manipulate file timestamps with `os.Chtimes()`

## Recommended Approach

### Option 1: Leave As-Is (RECOMMENDED)
**Rationale**: These are integration tests, not unit tests. The overhead of 14 DB setups is acceptable for:
- Tests that manipulate global state
- Tests that simulate complex workflows
- Tests that are relatively fast already (~0.5s each)

**Expected speedup**: Minimal (2-3x at most) vs. complexity cost

### Option 2: Refactor Without Shared DB
**Changes**:
1. Keep individual test functions (not suite)
2. Reduce DB setups by **reusing test stores within related test groups**
3. Add helpers to reset global state between tests
4. Document which tests can share vs. need isolation

**Example**:
```go
func TestAutoFlushGroup(t *testing.T) {
    tmpDir := t.TempDir()
    testDB := filepath.Join(tmpDir, "test.db")
    testStore := newTestStore(t, testDB)

    // Helper to reset state
    resetState := func() {
        autoFlushEnabled = true
        isDirty = false
        if flushTimer != nil {
            flushTimer.Stop()
            flushTimer = nil
        }
    }

    t.Run("DirtyMarking", func(t *testing.T) {
        resetState()
        // test...
    })

    t.Run("Disabled", func(t *testing.T) {
        resetState()
        // test...
    })
}
```

### Option 3: Mock/Stub Approach
**Changes**:
1. Introduce interfaces for `flushToJSONL` and `autoImportIfNewer`
2. Mock the filesystem operations
3. Test state transitions without actual DB/filesystem

**Trade-offs**: More refactoring, loses integration test value

## Files Modified (Reverted)
- `cmd/bd/main_test.go` - Reverted to original
- `cmd/bd/duplicates_test.go` - Fixed unused import (kept fix)

## Lessons Learned

1. **Not all tests benefit from shared DB pattern**
   - Integration tests need isolation
   - Global state manipulation requires careful handling

2. **P1 test pattern assumes**:
   - Pure DB operations
   - No global state
   - Data-level isolation sufficient

3. **Test classification matters**:
   - Unit tests: Share DB ✓
   - Integration tests: Need isolation ✓
   - Workflow tests: Need full process isolation ✓

## Next Steps

1. **Document in TEST_SUITE_AUDIT.md** that main_test.go is P2 but **NOT a good candidate** for shared DB pattern
2. **Update audit classification**: Move main_test.go to "Special Cases" category
3. **Focus P2 efforts** on `integrity_test.go` and `export_import_test.go` instead

## 2025-11-21 Update: Solution Implemented ✅

### What We Did
Rather than forcing shared DB pattern on integration tests, we **deleted redundant tests** that were duplicating coverage from `flush_manager_test.go`.

### Key Insight
After FlushManager refactoring (bd-52), `main_test.go` was testing the DEPRECATED legacy path while `flush_manager_test.go` tested the NEW FlushManager. Solution: delete the redundant legacy tests.

### Changes Made
1. **Deleted 7 redundant tests** (407 lines):
   - TestAutoFlushDirtyMarking (→ TestFlushManagerMarkDirtyTriggersFlush)
   - TestAutoFlushDisabled (→ TestFlushManagerDisabledDoesNotFlush)
   - TestAutoFlushDebounce (already skipped, obsolete)
   - TestAutoFlushClearState (clearAutoFlushState tested in export/sync)
   - TestAutoFlushConcurrency (→ TestFlushManagerConcurrentMarkDirty)
   - TestAutoFlushStoreInactive (→ TestPerformFlushStoreInactive)
   - TestAutoFlushErrorHandling (→ TestPerformFlushErrorHandling)

2. **Kept 2 integration tests**:
   - TestAutoFlushOnExit (PersistentPostRun behavior)
   - TestAutoFlushJSONLContent (DB → JSONL file content)

3. **Updated clearAutoFlushState()** to no-op when FlushManager exists

### Results
- **Before**: 18 tests, 1079 lines, ~15-20s
- **After**: 11 tests, 672 lines, ~5-7s (estimated)
- **Speedup**: ~3x faster
- **All tests passing**: ✅

### Future Work (Optional)
- Phase 2: Remove legacy path from `markDirtyAndScheduleFlush()` entirely
- Phase 3: Remove global variables (isDirty, flushTimer, flushMutex)
- These are deferred as they provide diminishing returns vs. complexity

## References
- Original issue: bd-1rh (Phase 2 test suite optimization)
- Pattern source: `label_test.go`, P1 refactored files
- Related: bd-159 (test config issues), bd-270 (merge conflict detection)
- Solution documented: `docs/MAIN_TEST_CLEANUP_PLAN.md`
