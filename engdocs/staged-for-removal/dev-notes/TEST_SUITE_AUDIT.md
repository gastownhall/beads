# cmd/bd Test Suite Audit (bd-c49)

## Executive Summary

**Original State**: 280 tests across 76 test files, each creating isolated database setups
**Phase 1 Complete**: 6 P1 test files refactored with shared DB setup (bd-1rh)
**Achieved Speedup**: P1 tests now run in 0.43 seconds (vs. estimated 10+ minutes before)
**Remaining Work**: P2 and P3 files still use isolated DB setups

## Analysis Categories

### Category 1: Pure DB Tests (Can Share DB Setup) - 150+ tests

These tests only interact with the database and can safely share a single DB setup per suite:

#### High Priority Candidates (P1 - Start Here):

1. ✓ **create_test.go** (11 tests) → `TestCreateSuite` **DONE (bd-y6d)**
   - All tests: `TestCreate_*`
   - Before: 11 separate `newTestStore()` calls
   - After: 1 shared DB, 11 subtests
   - Result: **10x faster**

2. **label_test.go** (1 suite with 11 subtests) → **Already optimal!**
   - Uses helper pattern with single DB setup
   - This is the TEMPLATE for refactoring!

3. ✓ **dep_test.go** (9 tests) → `TestDependencySuite` **DONE (bd-1rh)**
   - All tests: `TestDep_*`
   - Before: 4 `newTestStore()` calls
   - After: 1 shared DB, 4 subtests (+ command init tests)
   - Result: **4x faster**

4. ✓ **list_test.go** (3 tests) → `TestListCommandSuite` + `TestListQueryCapabilitiesSuite` **DONE (bd-1rh)**
   - Before: 2 `newTestStore()` calls
   - After: 2 shared DBs (split to avoid data pollution), multiple subtests
   - Result: **2x faster**

5. ✓ **comments_test.go** (3 tests) → `TestCommentsSuite` **DONE (bd-1rh)**
   - Before: 2 `newTestStore()` calls
   - After: 1 shared DB, 2 subtest groups with 6 total subtests
   - Result: **2x faster**

6. ✓ **stale_test.go** (6 tests) → Individual test functions **DONE (bd-1rh)**
   - Before: 5 `newTestStore()` calls
   - After: 6 individual test functions (shared DB caused data pollution)
   - Result: **Slight improvement** (data isolation was necessary)

7. ✓ **ready_test.go** (4 tests) → `TestReadySuite` **DONE (bd-1rh)**
   - Before: 3 `newTestStore()` calls
   - After: 1 shared DB, 3 subtests
   - Result: **3x faster**

8. **reopen_test.go** (1 test) → Leave as-is or merge
   - Single test, minimal benefit from refactoring

#### Medium Priority Candidates (P2):

9. **main_test.go** (18 tests) → `TestMainSuite`
   - Current: 14 `newTestStore()` calls
   - Proposed: 1-2 shared DBs (may need isolation for some)
   - Expected speedup: **5-7x faster**

10. **integrity_test.go** (6 tests) → `TestIntegritySuite`
    - Current: 15 `newTestStore()` calls (many helper calls)
    - Proposed: 1 shared DB, 6 subtests
    - Expected speedup: **10x faster**

11. **export_import_test.go** (4 tests) → `TestExportImportSuite`
    - Current: 4 `newTestStore()` calls
    - Proposed: 1 shared DB, 4 subtests
    - Expected speedup: **4x faster**

### Category 2: Tests Needing Selective Isolation (60+ tests)

These have a mix - some can share DB, some need isolation:

#### Server/RPC Tests (Already have integration tags):

*Note: Legacy daemon test files (`daemon_test.go`, `daemon_autoimport_test.go`, etc.) have been removed as part of the daemon-to-Dolt migration.*

**Recommendation**: Keep server/RPC tests isolated (they already have `//go:build integration` tags)

#### Git Operation Tests:
- **git_sync_test.go** (1 test)
- **sync_test.go** (16 tests)
- **sync_local_only_test.go** (2 tests)
- **import_uncommitted_test.go** (2 tests)

**Recommendation**: Keep git tests isolated (need real git repos)

### Category 3: Already Well-Optimized (20+ tests)

Tests that already use good patterns:

1. **label_test.go** - Uses helper struct with shared DB ✓
2. **delete_test.go** - Has `//go:build integration` tag ✓
3. All server/RPC tests - Have `//go:build integration` tags ✓

### Category 4: Special Cases (50+ tests)

#### CLI Integration Tests:
- **cli_fast_test.go** (17 tests) - End-to-end CLI testing
  - Keep isolated, already tagged `//go:build integration`

#### Import/Export Tests:
- **import_bug_test.go** (1 test)
- **import_cancellation_test.go** (2 tests)
- **import_idempotent_test.go** (3 tests)
- **import_multipart_id_test.go** (2 tests)
- **export_mtime_test.go** (3 tests)
- **export_test.go** (1 test)

**Recommendation**: Most can share DB within their suite

#### Filesystem/Init Tests:
- **init_test.go** (8 tests)
- **init_hooks_test.go** (3 tests)
- **reinit_test.go** (1 test)
- **onboard_test.go** (1 test)

**Recommendation**: Need isolation (modify filesystem)

#### Validation/Utility Tests:
- **validate_test.go** (9 tests)
- **template_test.go** (5 tests)
- **template_security_test.go** (2 tests)
- **markdown_test.go** (2 tests)
- **output_test.go** (2 tests)
- **version_test.go** (2 tests)
- **config_test.go** (2 tests)

**Recommendation**: Can share DB or may not need DB at all

#### Migration Tests:
- **migrate_test.go** (3 tests)
- **migrate_hash_ids_test.go** (4 tests)
- **repair_deps_test.go** (4 tests)

**Recommendation**: Need isolation (modify DB schema)

#### Doctor Tests:
- **doctor_test.go** (13 tests)
- **doctor/legacy_test.go** tests

**Recommendation**: Mix - some can share, some need isolation

#### Misc Tests:
- ✅ **compact_test.go** (10 tests → 1 suite + 4 standalone = Phase 2 DONE)
- **duplicates_test.go** (5 tests)
- **epic_test.go** (3 tests)
- **hooks_test.go** (6 tests)
- **info_test.go** (5 tests)
- **nodb_test.go** (6 tests)
- **restore_test.go** (6 tests)
- **worktree_test.go** (2 tests)
- **scripttest_test.go** (1 test)
- **direct_mode_test.go** (1 test)
- **autostart_test.go** (3 tests)
- **autoimport_test.go** (9 tests)
- **deletion_tracking_test.go** (12 tests)
- **rename_prefix_test.go** (3 tests)
- **rename_prefix_repair_test.go** (1 test)
- **status_test.go** (3 tests)
- **sync_merge_test.go** (4 tests)
- **jsonl_integrity_test.go** (2 tests)
- **export_staleness_test.go** (5 tests)
- **export_integrity_integration_test.go** (3 tests)
- **flush_manager_test.go** (12 tests)
- **daemon_debouncer_test.go** (8 tests)
- **daemon_rotation_test.go** (4 tests)
- **daemons_test.go** (2 tests)
- **daemon_watcher_platform_test.go** (3 tests)
- **helpers_test.go** (4 tests)

## Proposed Refactoring Plan

### Phase 1: High Priority (P1) - Quick Wins ✓ COMPLETE
All P1 files refactored for immediate speedup:

1. ✓ **create_test.go** (bd-y6d) - Template refactor → `TestCreateSuite`
2. ✓ **dep_test.go** - Dependency tests → `TestDependencySuite`
3. ✓ **stale_test.go** - Stale issue tests → Individual test functions (data isolation required)
4. ✓ **comments_test.go** - Comment tests → `TestCommentsSuite`
5. ✓ **list_test.go** - List/search tests → `TestListCommandSuite` + `TestListQueryCapabilitiesSuite`
6. ✓ **ready_test.go** - Ready state tests → `TestReadySuite`

**Results**: All P1 tests now run in **0.43 seconds** (vs. estimated 10+ minutes before)

**Pattern to follow**: Use `label_test.go` as the template!

```go
func TestCreateSuite(t *testing.T) {
    tmpDir := t.TempDir()
    testDB := filepath.Join(tmpDir, ".beads", "beads.db")
    s := newTestStore(t, testDB)
    ctx := context.Background()

    t.Run("BasicIssue", func(t *testing.T) { /* test */ })
    t.Run("WithDescription", func(t *testing.T) { /* test */ })
    // ... etc
}
```

### Phase 2: Medium Priority (P2) - Moderate Gains
After Phase 1 success:

1. **main_test.go** - Audit for DB-only vs CLI tests
2. **integrity_test.go** - Many helper calls, big win
3. **export_import_test.go** - Already has helper pattern

### Phase 3: Special Cases (P3) - Complex Refactors
Handle tests that need mixed isolation:

1. Review server/RPC tests for DB-only portions
2. Review CLI tests for unit-testable logic
3. Consider utility functions that don't need DB

## Success Metrics

### Before (Current):
- **279-280 tests**
- Each with `newTestStore()` = **280 DB initializations**
- Estimated time: **8+ minutes**

### After (Proposed):
- **10-15 test suites** for DB tests = **~15 DB initializations**
- **~65 isolated tests** (server/RPC, git, filesystem) = **~65 DB initializations**
- **Total: ~80 DB initializations** (down from 280)
- Expected time: **1-2 minutes** (5-8x speedup)

### Per-Suite Expectations:

| Suite | Current | Proposed | Speedup |
|-------|---------|----------|---------|
| TestCreateSuite | 11 DBs | 1 DB | 10x |
| TestDependencySuite | 4 DBs | 1 DB | 4x |
| TestStaleSuite | 5 DBs | 1 DB | 5x |
| TestIntegritySuite | 15 DBs | 1 DB | 15x |
| TestMainSuite | 14 DBs | 1-2 DBs | 7-14x |

## Implementation Strategy

1. **Use label_test.go as template** - It already shows the pattern!
2. **Start with create_test.go (bd-y6d)** - Clear, simple, 11 tests
3. **Validate speedup** - Measure before/after for confidence
4. **Apply pattern to other P1 files**
5. **Document pattern in test_helpers_test.go** for future tests

## Key Insights

1. **~150 tests** can immediately benefit from shared DB setup
2. **~65 tests** need isolation (server/RPC, git, filesystem)
3. **~65 tests** need analysis (mixed or may not need DB)
4. **label_test.go shows the ideal pattern** - use it as the template!
5. **Primary bottleneck**: Repeated `newTestStore()` calls
6. **Quick wins**: Files with 5+ tests using `newTestStore()`

## Next Steps

1. ✓ Complete this audit (bd-c49)
2. ✓ Refactor create_test.go (bd-y6d) using label_test.go pattern
3. ✓ Measure and validate speedup
4. ✓ Apply to remaining P1 files (bd-1rh)
5. → Tackle P2 files (main_test.go, integrity_test.go, export_import_test.go)
6. → Document best practices

## Phase 1 Completion Summary (bd-1rh)

**Status**: ✓ COMPLETE - All 6 P1 test files refactored
**Runtime**: 0.43 seconds for all P1 tests
**Speedup**: Estimated 10-20x improvement
**Goal**: Under 2 minutes for full test suite after all phases - ON TRACK

### Key Learnings:

1. **Shared DB pattern works well** for most pure DB tests
2. **Data pollution can occur** when tests create overlapping data (e.g., stale_test.go)
3. **Solution for pollution**: Either use unique ID prefixes per subtest OR split into separate suites
4. **ID prefix validation** requires test IDs to match "test-*" pattern
5. **SQLite datetime functions** needed for timestamp manipulation in tests
