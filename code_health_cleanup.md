# Code Health Cleanup Epic

## Epic: Code Health & Technical Debt Cleanup

**Type:** epic
**Priority:** 2
**Status:** open

**Description:**

Comprehensive codebase cleanup to remove dead code, refactor monolithic files, deduplicate utilities, and improve maintainability. Based on ultrathink code health analysis conducted 2025-10-27.

**Goals:**
- Remove ~1,500 LOC of dead/unreachable code
- Split 2 monolithic files (server.go 2,273 LOC, sqlite.go 2,136 LOC) into focused modules
- Deduplicate scattered utility functions (normalizeLabels, BD_DEBUG checks)
- Consolidate test coverage (2,019 LOC of collision tests)
- Improve code navigation and reduce merge conflicts

**Impact:** Reduces codebase by ~6-8%, improves maintainability, faster CI/CD

**Acceptance Criteria:**
- All unreachable code identified by `deadcode` analyzer is removed
- RPC server split into <500 LOC files with clear responsibilities
- Duplicate utility functions centralized
- Test coverage maintained or improved
- All tests passing
- Documentation updated

**Estimated Effort:** 11 days across 4 phases

---

## Phase 1: Dead Code Removal

### Issue: Delete cmd/bd/import_phases.go - entire file is dead code

**Type:** task
**Priority:** 1
**Dependencies:** parent-child:epic
**Labels:** cleanup, dead-code, phase-1

**Description:**

The file `cmd/bd/import_phases.go` (377 LOC) contains 7 functions that are all unreachable according to the deadcode analyzer. This appears to be an abandoned import refactoring that was never completed or has been replaced by the current implementation in `import.go`.

**Unreachable functions:**
- `getOrCreateStore` (line 15)
- `handlePrefixMismatch` (line 43)
- `handleCollisions` (line 87)
- `upsertIssues` (line 155)
- `importDependencies` (line 240)
- `importLabels` (line 281)
- `importComments` (line 316)

**Evidence:**
```bash
go run golang.org/x/tools/cmd/deadcode@latest -test ./...
# Shows all 7 functions as unreachable
```

No external callers found via:
```bash
grep -r "getOrCreateStore\|handlePrefixMismatch\|handleCollisions\|upsertIssues" cmd/bd/*.go
# Only matches within import_phases.go itself
```

**Acceptance Criteria:**
- Delete `cmd/bd/import_phases.go`
- Verify all tests still pass: `go test ./cmd/bd/...`
- Verify import functionality works: test `bd import` command
- Run deadcode analyzer to confirm no new unreachable code

**Impact:** Removes 377 LOC, simplifies import logic

---

### Issue: Remove deprecated rename functions from import_shared.go

**Type:** task
**Priority:** 1
**Dependencies:** parent-child:epic
**Labels:** cleanup, dead-code, phase-1

**Description:**

The file `cmd/bd/import_shared.go` contains deprecated and unreachable rename functions (~100 LOC) that are no longer used. The active implementation has moved to `internal/importer/importer.go`.

**Functions to remove:**
- `renameImportedIssuePrefixes` (line 262) - wrapper function, unused
- `renameImportedIssuePrefixesOld` (line 267) - marked Deprecated, 70 LOC
- `replaceIDReferences` (line 332) - only called by deprecated function

**Evidence:**
```bash
go run golang.org/x/tools/cmd/deadcode@latest -test ./...
# Shows these as unreachable
```

The actual implementation is in:
- `internal/importer/importer.go` - `RenameImportedIssuePrefixes`

**Acceptance Criteria:**
- Remove lines 262-340 from `cmd/bd/import_shared.go`
- Verify no callers exist: `grep -r "renameImportedIssuePrefixes\|replaceIDReferences" cmd/bd/`
- All tests pass: `go test ./cmd/bd/...`
- Import with rename works: `bd import --rename-on-import`

**Impact:** Removes ~100 LOC of deprecated code

---

### Issue: Delete skipped tests for "old buggy behavior"

**Type:** task
**Priority:** 1
**Dependencies:** parent-child:epic
**Labels:** cleanup, dead-code, test-cleanup, phase-1

**Description:**

Three test functions are permanently skipped with comments indicating they test behavior that was fixed in GH#120. These tests will never run again and should be deleted.

**Test functions to remove:**

1. `cmd/bd/import_collision_test.go:228`
   ```go
   t.Skip("Test expects old buggy behavior - needs rewrite for GH#120 fix")
   ```

2. `cmd/bd/import_collision_test.go:505`
   ```go
   t.Skip("Test expects old buggy behavior - needs rewrite for GH#120 fix")
   ```

3. `internal/storage/sqlite/collision_test.go:919`
   ```go
   t.Skip("Test expects old buggy behavior - needs rewrite for GH#120 fix")
   ```

**Acceptance Criteria:**
- Delete the 3 test functions entirely (~150 LOC total)
- Update test file comments to reference GH#120 fix if needed
- All remaining tests pass: `go test ./...`
- No reduction in meaningful test coverage (these test fixed bugs)

**Impact:** Removes ~150 LOC of permanently skipped tests

---

### Issue: Remove unreachable RPC methods

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** cleanup, dead-code, rpc, phase-1

**Description:**

Several RPC server and client methods are unreachable and should be removed:

**Server methods (internal/rpc/server.go):**
- `Server.GetLastImportTime` (line 2116)
- `Server.SetLastImportTime` (line 2123)
- `Server.findJSONLPath` (line 2255)

**Client methods (internal/rpc/client.go):**
- `Client.Import` (line 311) - RPC import not used (daemon uses autoimport)

**Evidence:**
```bash
go run golang.org/x/tools/cmd/deadcode@latest -test ./...
```

**Acceptance Criteria:**
- Remove the 4 unreachable methods (~80 LOC total)
- Verify no callers: `grep -r "GetLastImportTime\|SetLastImportTime\|findJSONLPath" .`
- All tests pass: `go test ./internal/rpc/...`
- Daemon functionality works: test daemon start/stop/operations

**Impact:** Removes ~80 LOC of unused RPC code

---

### Issue: Remove unreachable utility functions

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** cleanup, dead-code, phase-1

**Description:**

Several small utility functions are unreachable:

**Files to clean:**
1. `internal/storage/sqlite/hash.go` - `computeIssueContentHash` (line 17)
   - Check if entire file can be deleted if only contains this function

2. `internal/config/config.go` - `FileUsed` (line 151)
   - Delete unused config helper

3. `cmd/bd/git_sync_test.go` - `verifyIssueOpen` (line 300)
   - Delete dead test helper

4. `internal/compact/haiku.go` - `HaikuClient.SummarizeTier2` (line 81)
   - Tier 2 summarization not implemented
   - Options: implement feature OR delete method

**Acceptance Criteria:**
- Remove unreachable functions
- If entire files can be deleted (like hash.go), delete them
- For SummarizeTier2: decide to implement or delete, document decision
- All tests pass: `go test ./...`
- Verify no callers exist for each function

**Impact:** Removes 50-100 LOC depending on decisions

---

## Phase 2: Refactor Monolithic Files

### Issue: Split internal/rpc/server.go into focused modules

**Type:** task
**Priority:** 1
**Dependencies:** parent-child:epic
**Labels:** refactor, architecture, phase-2

**Description:**

The file `internal/rpc/server.go` is 2,273 lines with 50+ methods, making it difficult to navigate and prone to merge conflicts. Split into 8 focused files with clear responsibilities.

**Current structure:** Single 2,273-line file with:
- Connection handling
- Request routing
- All 40+ RPC method implementations
- Storage caching
- Health checks & metrics
- Cleanup loops

**Target structure:**
```
internal/rpc/
├── server.go          # Core server, connection handling (~300 lines)
│                      # - NewServer, Start, Stop, WaitReady
│                      # - handleConnection, handleRequest
│                      # - Signal handling
│
├── methods_issue.go   # Issue operations (~400 lines)
│                      # - handleCreate, handleUpdate, handleClose
│                      # - handleList, handleShow
│
├── methods_deps.go    # Dependency operations (~200 lines)
│                      # - handleDepAdd, handleDepRemove
│
├── methods_labels.go  # Label operations (~150 lines)
│                      # - handleLabelAdd, handleLabelRemove
│
├── methods_ready.go   # Ready work queries (~150 lines)
│                      # - handleReady, handleStats
│
├── methods_compact.go # Compaction operations (~200 lines)
│                      # - handleCompact
│
├── methods_comments.go # Comment operations (~150 lines)
│                       # - handleCommentAdd, handleCommentList
│
├── storage_cache.go   # Storage caching logic (~300 lines)
│                      # - getStorageForRequest
│                      # - findDatabaseForCwd
│                      # - Storage cache management
│                      # - evictStaleStorage, aggressiveEviction
│
├── health.go          # Health & metrics (~200 lines)
│                      # - handleHealth, handleMetrics
│                      # - handlePing, handleStatus
│                      # - checkMemoryPressure
│
├── protocol.go        # (already separate - no change)
└── client.go          # (already separate - no change)
```

**Acceptance Criteria:**
- All 50 methods split into appropriate files
- Each file <500 LOC
- All methods remain on `*Server` receiver (no behavior change)
- All tests pass: `go test ./internal/rpc/...`
- Verify daemon works: start daemon, run operations, check health
- Update internal documentation if needed
- No change to public API

**Migration strategy:**
1. Create new files with appropriate methods
2. Keep `server.go` as main file with core server logic
3. Test incrementally after each file split
4. Final verification with full test suite

**Impact:**
- Better code navigation
- Reduced merge conflicts
- Easier to find specific RPC operations
- Improved testability (can test method groups independently)

---

### Issue: Extract SQLite migrations into separate files

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** refactor, database, phase-2

**Description:**

The file `internal/storage/sqlite/sqlite.go` is 2,136 lines and contains 11 sequential migrations alongside core storage logic. Extract migrations into a versioned system.

**Current issues:**
- 11 migration functions mixed with core logic
- Hard to see migration history
- Sequential migrations slow database open
- No clear migration versioning

**Migration functions to extract:**
- `migrateDirtyIssuesTable()`
- `migrateIssueCountersTable()`
- `migrateExternalRefColumn()`
- `migrateCompositeIndexes()`
- `migrateClosedAtConstraint()`
- `migrateCompactionColumns()`
- `migrateSnapshotsTable()`
- `migrateCompactionConfig()`
- `migrateCompactedAtCommitColumn()`
- `migrateExportHashesTable()`
- Plus 1 more (11 total)

**Target structure:**
```
internal/storage/sqlite/
├── sqlite.go          # Core storage (~800 lines)
│                      # - CRUD operations
│                      # - Database connection
│                      # - Main Storage interface implementation
│
├── schema.go          # Table definitions (~200 lines)
│                      # - CREATE TABLE statements
│                      # - Initial schema
│
├── migrations.go      # Migration orchestration (~200 lines)
│                      # - runMigrations()
│                      # - Migration version tracking
│                      # - Migration registry
│
└── migrations/        # Individual migrations
    ├── 001_initial_schema.go
    ├── 002_dirty_issues.go
    ├── 003_issue_counters.go
    ├── 004_external_ref.go
    ├── 005_composite_indexes.go
    ├── 006_closed_at_constraint.go
    ├── 007_compaction_columns.go
    ├── 008_snapshots_table.go
    ├── 009_compaction_config.go
    ├── 010_compacted_at_commit.go
    └── 011_export_hashes.go
```

**Each migration file format:**
```go
package migrations

func Migration_001_InitialSchema(db *sql.DB) error {
    // Migration logic
}

func init() {
    Register(1, "initial_schema", Migration_001_InitialSchema)
}
```

**Acceptance Criteria:**
- All 11 migrations extracted to separate files
- Migration version tracking in database
- Migrations run in order on fresh database
- Existing databases upgrade correctly
- All tests pass: `go test ./internal/storage/sqlite/...`
- Database initialization time unchanged or improved
- Add migration rollback capability (optional, nice-to-have)

**Benefits:**
- Clear migration history
- Each migration self-contained
- Easier to review migration changes in PRs
- Future migrations easier to add

**Impact:** Reduces sqlite.go from 2,136 to ~1,000 lines, improves maintainability

---

## Phase 3: Deduplicate Code

### Issue: Extract normalizeLabels to shared utility package

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** refactor, deduplication, phase-3

**Description:**

The `normalizeLabels` function appears in multiple locations with identical implementation. Extract to a shared utility package.

**Current locations:**
- `internal/rpc/server.go:37` (53 lines) - full implementation
- `cmd/bd/list.go:50-52` - uses the server version (needs to use new shared version)

**Function purpose:**
- Trims whitespace from labels
- Removes empty strings
- Deduplicates labels
- Preserves order

**Target structure:**
```
internal/util/
├── strings.go         # String utilities
    └── NormalizeLabels([]string) []string
```

**Implementation:**
```go
package util

import "strings"

// NormalizeLabels trims whitespace, removes empty strings, and deduplicates labels
// while preserving order.
func NormalizeLabels(ss []string) []string {
    seen := make(map[string]struct{})
    out := make([]string, 0, len(ss))
    for _, s := range ss {
        s = strings.TrimSpace(s)
        if s == "" {
            continue
        }
        if _, ok := seen[s]; ok {
            continue
        }
        seen[s] = struct{}{}
        out = append(out, s)
    }
    return out
}
```

**Acceptance Criteria:**
- Create `internal/util/strings.go` with `NormalizeLabels`
- Add comprehensive unit tests in `internal/util/strings_test.go`
- Update `internal/rpc/server.go` to import and use `util.NormalizeLabels`
- Update `cmd/bd/list.go` to import and use `util.NormalizeLabels`
- Remove duplicate implementations
- All tests pass: `go test ./...`
- Verify label normalization works: test `bd list --label` commands

**Impact:** DRY principle, single source of truth, easier to test

---

### Issue: Centralize BD_DEBUG logging into debug package

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** refactor, deduplication, logging, phase-3

**Description:**

The codebase has 43 scattered instances of `if os.Getenv("BD_DEBUG") != ""` debug checks across 6 files. Centralize into a debug logging package.

**Current locations:**
- `cmd/bd/main.go` - 15 checks
- `cmd/bd/autoflush.go` - 6 checks
- `cmd/bd/nodb.go` - 4 checks
- `internal/rpc/server.go` - 2 checks
- `internal/rpc/client.go` - 5 checks
- `cmd/bd/daemon_autostart.go` - 11 checks

**Current pattern:**
```go
if os.Getenv("BD_DEBUG") != "" {
    fmt.Fprintf(os.Stderr, "Debug: %s\n", msg)
}
```

**Target structure:**
```
internal/debug/
└── debug.go
```

**Implementation:**
```go
package debug

import (
    "fmt"
    "os"
)

// Enabled is true if BD_DEBUG environment variable is set
var Enabled = os.Getenv("BD_DEBUG") != ""

// Logf prints a debug message to stderr if debug mode is enabled
func Logf(format string, args ...interface{}) {
    if Enabled {
        fmt.Fprintf(os.Stderr, "Debug: "+format+"\n", args...)
    }
}

// Printf prints a debug message to stderr without "Debug:" prefix
func Printf(format string, args ...interface{}) {
    if Enabled {
        fmt.Fprintf(os.Stderr, format, args...)
    }
}
```

**Acceptance Criteria:**
- Create `internal/debug/debug.go` with `Enabled`, `Logf`, `Printf`
- Add unit tests in `internal/debug/debug_test.go` (test with/without BD_DEBUG)
- Replace all 43 instances of `os.Getenv("BD_DEBUG")` checks with `debug.Logf()`
- Verify debug output works: run with `BD_DEBUG=1 bd status`
- All tests pass: `go test ./...`
- No behavior change (output identical to before)

**Migration example:**
```go
// Before:
if os.Getenv("BD_DEBUG") != "" {
    fmt.Fprintf(os.Stderr, "Debug: connected to daemon at %s\n", socketPath)
}

// After:
debug.Logf("connected to daemon at %s", socketPath)
```

**Benefits:**
- Centralized debug logging
- Easier to add structured logging later
- Testable (can mock debug output)
- Consistent debug message format

**Impact:** Removes 43 scattered checks, improves code clarity

---

### Issue: Consider central serialization package for JSON handling

**Type:** task
**Priority:** 3
**Dependencies:** parent-child:epic
**Labels:** refactor, deduplication, serialization, phase-3, optional

**Description:**

Multiple parts of the codebase handle JSON serialization of issues with slightly different approaches. Consider creating a centralized serialization package to ensure consistency.

**Current serialization locations:**
- `cmd/bd/export.go` - JSONL export (issues to file)
- `cmd/bd/import.go` - JSONL import (file to issues)
- `internal/rpc/protocol.go` - RPC JSON marshaling
- `internal/storage/memory/memory.go` - In-memory marshaling

**Potential benefits:**
- Single source of truth for JSON format
- Consistent field naming
- Easier to add new fields
- Centralized validation

**Potential structure:**
```
internal/serialization/
├── issue.go           # Issue JSON serialization
├── dependency.go      # Dependency serialization
├── jsonl.go          # JSONL file operations
└── jsonl_test.go     # Tests
```

**Note:** This is marked **optional** because:
- Current serialization mostly works
- May not provide enough benefit to justify refactor
- Risk of breaking compatibility

**Acceptance Criteria (if implemented):**
- Create serialization package with documented JSON format
- Migrate export/import to use centralized serialization
- All existing JSONL files can be read with new code
- All tests pass: `go test ./...`
- Export/import round-trip works perfectly
- RPC protocol unchanged (or backwards compatible)

**Decision point:** Evaluate if benefits outweigh refactoring cost

**Impact:** TBD based on investigation - may defer to future work

---

## Phase 4: Test Cleanup

### Issue: Audit and consolidate collision test coverage

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** test-cleanup, phase-4

**Description:**

The codebase has 2,019 LOC of collision detection tests across 3 files. Run coverage analysis to identify redundant test cases and consolidate.

**Test files:**
- `cmd/bd/import_collision_test.go` - 974 LOC
- `cmd/bd/autoimport_collision_test.go` - 750 LOC
- `cmd/bd/import_collision_regression_test.go` - 295 LOC

**Total:** 2,019 LOC of collision tests

**Analysis steps:**

1. **Run coverage analysis:**
   ```bash
   go test -cover -coverprofile=coverage.out ./cmd/bd/
   go tool cover -func=coverage.out | grep collision
   go tool cover -html=coverage.out -o coverage.html
   ```

2. **Identify redundant tests:**
   - Look for tests covering identical code paths
   - Check for overlapping table-driven test cases
   - Find tests made obsolete by later tests

3. **Document findings:**
   - Which tests are essential?
   - Which tests duplicate coverage?
   - What's the minimum set of tests for full coverage?

**Consolidation strategy:**
- Keep regression tests for critical bugs
- Merge overlapping table-driven tests
- Remove redundant edge case tests covered elsewhere
- Ensure all collision scenarios still tested

**Acceptance Criteria:**
- Coverage analysis completed and documented
- Redundant tests identified (~800 LOC estimated)
- Consolidated test suite maintains or improves coverage
- All remaining tests pass: `go test ./cmd/bd/...`
- Test run time unchanged or faster
- Document which tests were removed and why
- Coverage percentage maintained: `go test -cover ./cmd/bd/` shows same %

**Expected outcome:** Reduce to ~1,200 LOC (save ~800 lines) while maintaining coverage

**Impact:** Faster test runs, easier maintenance, clearer test intent

---

## Documentation & Validation

### Issue: Update documentation after code health cleanup

**Type:** task
**Priority:** 2
**Dependencies:** parent-child:epic
**Labels:** documentation, phase-4

**Description:**

Update all documentation to reflect code structure changes after cleanup phases complete.

**Documentation to update:**

1. **AGENTS.md** - Update file structure references
2. **CONTRIBUTING.md** (if exists) - Update build/test instructions
3. **Code comments** - Update any outdated references
4. **Package documentation** - Update godoc for reorganized packages

**New documentation to add:**

1. **internal/util/README.md** - Document shared utilities
2. **internal/debug/README.md** - Document debug logging
3. **internal/rpc/README.md** - Document new file organization
4. **internal/storage/sqlite/migrations/README.md** - Migration system docs

**Acceptance Criteria:**
- All documentation references to deleted files removed
- New package READMEs written
- Code comments updated for reorganized code
- Migration guide for developers (if needed)
- Architecture diagrams updated (if they exist)

**Impact:** Keeps documentation in sync with code

---

### Issue: Run final validation and cleanup checks

**Type:** task
**Priority:** 1
**Dependencies:** parent-child:epic
**Labels:** validation, phase-4

**Description:**

Final validation pass to ensure all cleanup objectives met and no regressions introduced.

**Validation checklist:**

**1. Dead code verification:**
```bash
go run golang.org/x/tools/cmd/deadcode@latest -test ./...
# Should show zero unreachable functions
```

**2. Test coverage:**
```bash
go test -cover ./...
# Coverage should be maintained or improved
```

**3. Build verification:**
```bash
go build ./cmd/bd/
# Should build without errors
```

**4. Linting:**
```bash
golangci-lint run
# Should show improvements in code quality metrics
```

**5. Integration tests:**
```bash
# Test key workflows:
bd init
bd create "Test issue"
bd list
bd daemon
bd export
bd import
bd compact --dry-run
```

**6. Metrics verification:**
- Count LOC before/after: `find . -name "*.go" | xargs wc -l`
- Verify ~2,500 LOC reduction achieved
- Count files before/after

**7. Git clean check:**
```bash
go mod tidy
go fmt ./...
git status # Should show only intended changes
```

**Acceptance Criteria:**
- Zero unreachable functions per deadcode analyzer
- All tests pass: `go test ./...`
- Test coverage maintained or improved
- Builds cleanly: `go build ./...`
- Linting shows improvements
- Integration tests all pass
- LOC reduction target achieved (~2,500 LOC)
- No unintended behavior changes
- Git commit messages document all changes

**Final metrics to report:**
- LOC removed: ~____
- Files deleted: ____
- Files created: ____
- Test coverage: ____%
- Build time: ____ (before/after)
- Test run time: ____ (before/after)

**Impact:** Confirms all cleanup objectives achieved successfully

---

## Notes

**Analysis date:** 2025-10-27
**Analysis tool:** `deadcode` analyzer + manual code review
**Total estimated LOC reduction:** ~2,500 (6.8% of 37K codebase)
**Total estimated effort:** 11 days across 4 phases

**Risk assessment:**
- **Low risk:** Dead code removal (Phase 1)
- **Medium risk:** File splits (Phase 2) - requires careful testing
- **Low risk:** Deduplication (Phase 3)
- **Low risk:** Test cleanup (Phase 4)

**Dependencies:**
- All child issues depend on epic
- Phase 2+ should wait for Phase 1 completion
- Each phase can be worked independently
- Recommend completing in order: 1 → 2 → 3 → 4

**Success metrics:**
- ✅ All deadcode warnings eliminated
- ✅ No files >1,000 LOC
- ✅ Zero duplicate utility functions
- ✅ Test coverage maintained at 85%+
- ✅ All integration tests passing
- ✅ Documentation updated
