# Ultrathink: Batching API for Bulk Issue Creation (bd-222)

**Date**: 2025-10-15
**Context**: Individual devs, small teams, future agent swarms, bulk imports
**Problem**: CreateIssue acquires dedicated connection per call, inefficient for bulk operations

## Executive Summary

**Recommended Solution**: **Hybrid approach - Add CreateIssues + Keep existing CreateIssue unchanged**

Provides high-performance batch path for bulk operations while maintaining simple single-issue API for typical use.

---

## Dependencies & Implementation Order

### Critical Dependency: bd-224 (status/closed_at invariant)

**bd-224 MUST be implemented before bd-222**

**Why**: Both issues modify the same code paths:
- bd-224: Fixes `import.go` to enforce `closed_at` invariant (status='closed' ⟺ closed_at != NULL)
- bd-222: Changes `import.go` to use `CreateIssues` instead of `CreateIssue` loop

**The Problem**:
If we implement bd-222 first:
1. `CreateIssues` won't enforce the closed_at invariant (inherits bug from CreateIssue)
2. Import switches to use `CreateIssues`
3. Import can still create inconsistent data (bd-224's bug persists)
4. Later bd-224 fix requires modifying BOTH CreateIssue AND CreateIssues

**The Solution**:
If we implement bd-224 first:
1. Add CHECK constraint: `(status = 'closed') = (closed_at IS NOT NULL)`
2. Fix `UpdateIssue` to manage closed_at automatically
3. Fix `import.go` to enforce invariant before calling CreateIssue
4. **Then** implement bd-222's `CreateIssues` with invariant already enforced:
   - Database constraint rejects bad data
   - Issue.Validate() checks the invariant (per bd-224)
   - Import code already normalizes before calling CreateIssues
   - No new code needed in CreateIssues - it's correct by construction!

### Implementation Impact

**CreateIssues must validate closed_at invariant** (from bd-224):

```go
// Phase 1: Validation
for i, issue := range issues {
    if err := issue.Validate(); err != nil {  // ← Validates invariant (bd-224)
        return fmt.Errorf("validation failed for issue %d: %w", i, err)
    }
}
```

After bd-224 is complete, `Issue.Validate()` will check:
```go
func (i *Issue) Validate() error {
    // ... existing validation ...

    // Enforce closed_at invariant (bd-224)
    if i.Status == StatusClosed && i.ClosedAt == nil {
        return fmt.Errorf("closed issues must have closed_at timestamp")
    }
    if i.Status != StatusClosed && i.ClosedAt != nil {
        return fmt.Errorf("non-closed issues cannot have closed_at timestamp")
    }

    return nil
}
```

This means `CreateIssues` automatically enforces the invariant through validation, with the database CHECK constraint as final defense.

### Import Code Simplification

**Before bd-224** (current import.go):
```go
for _, issue := range issues {
    // Complex logic to handle status/closed_at independently
    updates := make(map[string]interface{})
    if _, ok := rawData["status"]; ok {
        updates["status"] = issue.Status  // ← Doesn't manage closed_at
    }
    // ... more complex update logic
    store.CreateIssue(ctx, issue, "import")
}
```

**After bd-224** (import.go enforces invariant):
```go
for _, issue := range issues {
    // Normalize closed_at based on status BEFORE creating
    if issue.Status == types.StatusClosed {
        if issue.ClosedAt == nil {
            now := time.Now()
            issue.ClosedAt = &now
        }
    } else {
        issue.ClosedAt = nil  // ← Clear if not closed
    }
    store.CreateIssue(ctx, issue, "import")
}
```

**After bd-222** (import.go uses batch):
```go
// Normalize all issues
for _, issue := range issues {
    if issue.Status == types.StatusClosed {
        if issue.ClosedAt == nil {
            now := time.Now()
            issue.ClosedAt = &now
        }
    } else {
        issue.ClosedAt = nil
    }
}

// Single batch call (5-15x faster!)
store.CreateIssues(ctx, issues, "import")
```

Much simpler: normalize once, call batch API, database constraint enforces correctness.

### Recommended Implementation Sequence

1. ✅ **Implement bd-224 first** (P1 bug fix)
   - Add database CHECK constraint
   - Add validation to `Issue.Validate()`
   - Fix `UpdateIssue` to auto-manage closed_at
   - Fix `import.go` to normalize closed_at before creating

2. ✅ **Then implement bd-222** (P2 performance enhancement)
   - Add `CreateIssues` method (inherits bd-224's validation)
   - Update `import.go` to use `CreateIssues`
   - Import code is simpler (no per-issue loop, just normalize + batch)

3. ✅ **Benefits of this order**:
   - bd-224 fixes data integrity bug (higher priority)
   - bd-222 builds on correct foundation
   - No duplicate invariant enforcement code
   - Database constraint + validation = defense in depth
   - CreateIssues is correct by construction

---

## Current State Analysis

### How CreateIssue Works (sqlite.go:315-453)

```go
func (s *SQLiteStorage) CreateIssue(ctx, issue, actor) error {
    // 1. Acquire dedicated connection
    conn, err := s.db.Conn(ctx)
    defer conn.Close()

    // 2. BEGIN IMMEDIATE transaction (acquires write lock)
    conn.ExecContext(ctx, "BEGIN IMMEDIATE")

    // 3. Generate ID atomically if needed
    //    - Query issue_counters
    //    - Update counter with MAX(existing, calculated) + 1

    // 4. Insert issue
    // 5. Record creation event
    // 6. Mark dirty for export
    // 7. COMMIT
}
```

### Performance Characteristics

**Single Issue Creation**:
- Connection acquisition: ~1ms
- BEGIN IMMEDIATE: ~1-5ms (lock acquisition)
- ID generation: ~2-3ms (subquery + update)
- Insert + event + dirty: ~2-3ms
- COMMIT: ~1-2ms
- **Total: ~7-14ms per issue**

**Bulk Creation (100 issues, sequential)**:
- 100 connections: ~100ms
- 100 transactions: ~100-500ms (lock contention!)
- 100 ID generations: ~200-300ms
- 100 inserts: ~200-300ms
- **Total: ~600ms-1.2s**

**With Batching (estimated)**:
- 1 connection: ~1ms
- 1 transaction: ~1-5ms
- ID generation batch: ~10-20ms (one query for range)
- Bulk insert: ~50-100ms (prepared stmt, multiple VALUES)
- **Total: ~60-130ms (5-10x faster)**

### When Does This Matter?

**Low Impact** (current approach is fine):
- Interactive CLI use: `bd create "Fix bug"`
- Individual agent creating 1-5 issues
- Typical development workflow

**High Impact** (batching helps):
- ✅ Bulk import from JSONL (10-1000+ issues)
- ✅ Agent workflows generating issue decompositions (10-50 issues)
- ✅ Migrating from other systems (100-10000+ issues)
- ✅ Template instantiation (creating epic + subtasks)
- ✅ Test data generation

---

## Solution Options

### Option A: Simple All-or-Nothing Batch ⭐ **RECOMMENDED**

```go
// CreateIssues creates multiple issues atomically in a single transaction
func (s *SQLiteStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
```

**Semantics**:
- All issues created, or none created (atomicity)
- Single transaction, single connection
- Returns error if ANY issue fails validation or insertion
- IDs generated atomically as a range

**Pros**:
- ✅ Simple mental model (atomic batch)
- ✅ Clear error handling (one error = whole batch fails)
- ✅ Matches database transaction semantics
- ✅ Easy to implement (similar to CreateIssue)
- ✅ No partial state in database
- ✅ Safe for concurrent access (IMMEDIATE transaction)
- ✅ **5-10x faster for bulk operations**

**Cons**:
- ⚠️ If one issue is invalid, whole batch fails
- ⚠️ Caller must retry entire batch on error
- ⚠️ No indication of WHICH issue failed

**Mitigation**: Add validation-only mode to pre-check batch

**Verdict**: Best for most use cases (import, migrations, agent workflows)

### Option B: Partial Success with Error Details

```go
type CreateResult struct {
    ID      string
    Error   error
}

func (s *SQLiteStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) ([]CreateResult, error)
```

**Semantics**:
- Best-effort creation
- Returns results for each issue (ID or error)
- Transaction commits even if some issues fail
- Complex rollback semantics

**Pros**:
- ✅ Caller knows exactly which issues failed
- ✅ Partial progress on errors
- ✅ Good for unreliable input data

**Cons**:
- ❌ **Complex transaction semantics**: Which failures abort transaction?
- ❌ **Partial state in database**: Caller must track what succeeded
- ❌ **ID generation complexity**: Skip failed issues in counter?
- ❌ **Dirty tracking complexity**: Which issues to mark dirty?
- ❌ **Event recording**: Record events for succeeded issues only?
- ❌ More complex API for common case
- ❌ Caller must handle partial state

**Verdict**: Too complex, doesn't match database atomicity model

### Option C: Batch with Configurable Strategy

```go
type BatchOptions struct {
    FailFast        bool  // Stop on first error (default)
    ContinueOnError bool  // Best effort
    ValidateOnly    bool  // Dry run
}

func (s *SQLiteStorage) CreateIssues(ctx, issues, actor, opts) ([]CreateResult, error)
```

**Pros**:
- ✅ Flexible for different use cases
- ✅ Can support both atomic and partial modes

**Cons**:
- ❌ **Too much complexity** for the benefit
- ❌ Multiple code paths = more bugs
- ❌ Unclear which mode to use when
- ❌ Doesn't solve the core problem (connection overhead)

**Verdict**: Over-engineered for current needs

### Option D: Internal Optimization Only (No API Change)

Optimize CreateIssue internally to batch operations without changing API.

**Approach**:
- Connection pooling improvements
- Prepared statement caching
- WAL optimization

**Pros**:
- ✅ No API changes
- ✅ Benefits all callers automatically

**Cons**:
- ❌ **Can't eliminate transaction overhead** (still N transactions)
- ❌ **Can't eliminate ID generation overhead** (still N counter updates)
- ❌ **Limited improvement** (maybe 20-30% faster, not 5-10x)
- ❌ Doesn't address root cause

**Verdict**: Good to do anyway, but doesn't solve the problem

---

## Recommended Solution: **Simple All-or-Nothing Batch (Option A)**

### API Design

```go
// CreateIssues creates multiple issues atomically in a single transaction.
// All issues are created or none are created. Returns error if any issue
// fails validation or insertion.
//
// Performance: ~10x faster than calling CreateIssue in a loop for large batches.
// Use this for bulk imports, migrations, or agent workflows creating many issues.
//
// Issues with empty IDs will have IDs generated atomically. Issues with
// explicit IDs are used as-is (caller responsible for avoiding collisions).
func (s *SQLiteStorage) CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error
```

### Implementation Strategy

#### Phase 1: Validation

```go
// Validate all issues first (fail-fast)
for i, issue := range issues {
    if err := issue.Validate(); err != nil {
        return fmt.Errorf("validation failed for issue %d: %w", i, err)
    }
}
```

#### Phase 2: Connection & Transaction

```go
// Acquire dedicated connection (same as CreateIssue)
conn, err := s.db.Conn(ctx)
if err != nil {
    return fmt.Errorf("failed to acquire connection: %w", err)
}
defer conn.Close()

// BEGIN IMMEDIATE (same as CreateIssue)
if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
    return fmt.Errorf("failed to begin immediate transaction: %w", err)
}

committed := false
defer func() {
    if !committed {
        conn.ExecContext(context.Background(), "ROLLBACK")
    }
}()
```

#### Phase 3: Batch ID Generation

**Key Insight**: Generate ID range atomically, then assign sequentially

```go
// Count how many issues need IDs
needIDCount := 0
for _, issue := range issues {
    if issue.ID == "" {
        needIDCount++
    }
}

// Generate ID range atomically (if needed)
var nextID int
var prefix string
if needIDCount > 0 {
    // Get prefix from config
    err := conn.QueryRowContext(ctx,
        `SELECT value FROM config WHERE key = ?`,
        "issue_prefix").Scan(&prefix)
    if err == sql.ErrNoRows || prefix == "" {
        prefix = "bd"
    } else if err != nil {
        return fmt.Errorf("failed to get config: %w", err)
    }

    // Atomically reserve ID range: [nextID, nextID+needIDCount)
    // This is the KEY optimization - one counter update instead of N
    err = conn.QueryRowContext(ctx, `
        INSERT INTO issue_counters (prefix, last_id)
        SELECT ?, COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0) + ?
        FROM issues
        WHERE id LIKE ? || '-%'
          AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*'
        ON CONFLICT(prefix) DO UPDATE SET
            last_id = MAX(
                last_id,
                (SELECT COALESCE(MAX(CAST(substr(id, LENGTH(?) + 2) AS INTEGER)), 0)
                 FROM issues
                 WHERE id LIKE ? || '-%'
                   AND substr(id, LENGTH(?) + 2) GLOB '[0-9]*')
            ) + ?
        RETURNING last_id
    `, prefix, prefix, needIDCount, prefix, prefix, prefix, prefix, prefix, needIDCount).Scan(&nextID)
    if err != nil {
        return fmt.Errorf("failed to generate ID range: %w", err)
    }

    // Assign IDs sequentially
    currentID := nextID - needIDCount + 1
    for i := range issues {
        if issues[i].ID == "" {
            issues[i].ID = fmt.Sprintf("%s-%d", prefix, currentID)
            currentID++
        }
    }
}
```

#### Phase 4: Bulk Insert Issues

**Two approaches**:

**Approach A: Prepared Statement + Loop** (simpler, still fast)
```go
stmt, err := conn.PrepareContext(ctx, `
    INSERT INTO issues (
        id, title, description, design, acceptance_criteria, notes,
        status, priority, issue_type, assignee, estimated_minutes,
        created_at, updated_at, closed_at, external_ref
    ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`)
if err != nil {
    return fmt.Errorf("failed to prepare statement: %w", err)
}
defer stmt.Close()

now := time.Now()
for _, issue := range issues {
    issue.CreatedAt = now
    issue.UpdatedAt = now

    _, err = stmt.ExecContext(ctx,
        issue.ID, issue.Title, issue.Description, issue.Design,
        issue.AcceptanceCriteria, issue.Notes, issue.Status,
        issue.Priority, issue.IssueType, issue.Assignee,
        issue.EstimatedMinutes, issue.CreatedAt, issue.UpdatedAt,
        issue.ClosedAt, issue.ExternalRef,
    )
    if err != nil {
        return fmt.Errorf("failed to insert issue %s: %w", issue.ID, err)
    }
}
```

**Approach B: Multi-VALUE INSERT** (fastest, more complex)
```go
// Build multi-value INSERT
// INSERT INTO issues VALUES (...), (...), (...)
// More complex string building but ~2x faster for large batches
// Defer to performance testing phase
```

**Decision**: Start with Approach A (prepared statement), optimize to Approach B if benchmarks show need

#### Phase 5: Bulk Record Events

```go
// Prepare event statement
eventStmt, err := conn.PrepareContext(ctx, `
    INSERT INTO events (issue_id, event_type, actor, new_value)
    VALUES (?, ?, ?, ?)
`)
if err != nil {
    return fmt.Errorf("failed to prepare event statement: %w", err)
}
defer eventStmt.Close()

for _, issue := range issues {
    eventData, err := json.Marshal(issue)
    if err != nil {
        eventData = []byte(fmt.Sprintf(`{"id":"%s","title":"%s"}`, issue.ID, issue.Title))
    }

    _, err = eventStmt.ExecContext(ctx, issue.ID, types.EventCreated, actor, string(eventData))
    if err != nil {
        return fmt.Errorf("failed to record event for %s: %w", issue.ID, err)
    }
}
```

#### Phase 6: Bulk Mark Dirty

```go
// Bulk insert dirty markers
dirtyStmt, err := conn.PrepareContext(ctx, `
    INSERT INTO dirty_issues (issue_id, marked_at)
    VALUES (?, ?)
    ON CONFLICT (issue_id) DO UPDATE SET marked_at = excluded.marked_at
`)
if err != nil {
    return fmt.Errorf("failed to prepare dirty statement: %w", err)
}
defer dirtyStmt.Close()

dirtyTime := time.Now()
for _, issue := range issues {
    _, err = dirtyStmt.ExecContext(ctx, issue.ID, dirtyTime)
    if err != nil {
        return fmt.Errorf("failed to mark dirty %s: %w", issue.ID, err)
    }
}
```

#### Phase 7: Commit

```go
if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
    return fmt.Errorf("failed to commit transaction: %w", err)
}
committed = true
return nil
```

---

## Design Decisions & Tradeoffs

### Decision 1: All-or-Nothing Atomicity ✅

**Rationale**: Matches database transaction semantics, simpler mental model

**Tradeoff**: Batch fails if ANY issue is invalid
- **Mitigation**: Pre-validate all issues before starting transaction
- **Alternative**: Caller can retry with smaller batches or individual issues

### Decision 2: Same Transaction Semantics as CreateIssue ✅

Use BEGIN IMMEDIATE, not DEFERRED or EXCLUSIVE

**Rationale**:
- Consistency with existing CreateIssue
- Prevents race conditions in ID generation
- Serializes batch operations (which is fine - they're rare)

**Tradeoff**: Batches serialize (only one concurrent batch writer)
- **Impact**: Low - batch operations are rare (import, migration)
- **Benefit**: Simple, correct, no race conditions

### Decision 3: Atomic ID Range Reservation ✅

Generate range [nextID, nextID+N) in single counter update

**Rationale**: KEY optimization - avoids N counter updates

**Implementation**:
```sql
-- Old approach (CreateIssue): N updates
UPDATE issue_counters SET last_id = last_id + 1 RETURNING last_id;  -- N times

-- New approach (CreateIssues): 1 update
UPDATE issue_counters SET last_id = last_id + N RETURNING last_id;  -- Once
```

**Correctness**: Safe because BEGIN IMMEDIATE serializes batches

### Decision 4: Support Mixed ID Assignment ✅

Some issues can have explicit IDs, others auto-generated

**Use Case**: Import with some external IDs, some new issues

```go
issues := []*Issue{
    {ID: "ext-123", Title: "External issue"},  // Keep ID
    {ID: "", Title: "New issue"},               // Generate ID
    {ID: "bd-999", Title: "Explicit ID"},      // Keep ID
}
```

**Rationale**: Flexible for import scenarios

**Complexity**: Low - just count issues needing IDs

### Decision 5: Prepared Statements Over Multi-VALUE INSERT ✅

Start with prepared statement loop, optimize later if needed

**Rationale**:
- Simpler implementation
- Still much faster than N transactions (5-10x)
- Multi-VALUE INSERT only ~2x faster than prepared stmt
- Can optimize later if profiling shows need

### Decision 6: Keep CreateIssue Unchanged ✅

Don't modify existing CreateIssue implementation

**Rationale**:
- Backward compatibility
- No risk to existing callers
- Additive change only
- Different use cases (single vs batch)

---

## When to Use Which API

### Use CreateIssue (existing)

- ✅ Interactive CLI: `bd create "Title"`
- ✅ Single issue creation
- ✅ Agent creating 1-3 issues
- ✅ When simplicity matters
- ✅ When you want per-issue error handling

### Use CreateIssues (new)

- ✅ Bulk import from JSONL (10-1000+ issues)
- ✅ Migration from other systems (100-10000+ issues)
- ✅ Agent decomposing work into 10-50 issues
- ✅ Template instantiation (epic + subtasks)
- ✅ Test data generation
- ✅ When performance matters

**Rule of Thumb**: Use CreateIssues for N > 5 issues

---

## Implementation Checklist

### Phase 1: Core Implementation ✅
- [ ] Add `CreateIssues` to Storage interface (storage/storage.go)
- [ ] Implement SQLiteStorage.CreateIssues (storage/sqlite/sqlite.go)
- [ ] Add comprehensive unit tests
- [ ] Add concurrency tests (multiple batch writers)
- [ ] Add performance benchmarks

### Phase 2: CLI Integration
- [ ] Add `bd create-batch` command (or internal use only?)
- [ ] Update import.go to use CreateIssues for bulk imports
- [ ] Test with real JSONL imports

### Phase 3: Documentation
- [ ] Document CreateIssues API (godoc)
- [ ] Add batch import example
- [ ] Update EXTENDING.md with batch usage
- [ ] Performance notes in README

### Phase 4: Optimization (if needed)
- [ ] Profile CreateIssues with 100, 1000, 10000 issues
- [ ] Optimize to multi-VALUE INSERT if needed
- [ ] Consider batch size limits (split large batches)

---

## Testing Strategy

### Unit Tests

```go
func TestCreateIssues_Empty(t *testing.T)
func TestCreateIssues_Single(t *testing.T)
func TestCreateIssues_Multiple(t *testing.T)
func TestCreateIssues_WithExplicitIDs(t *testing.T)
func TestCreateIssues_MixedIDs(t *testing.T)
func TestCreateIssues_ValidationError(t *testing.T)
func TestCreateIssues_DuplicateID(t *testing.T)
func TestCreateIssues_RollbackOnError(t *testing.T)
```

### Concurrency Tests

```go
func TestCreateIssues_Concurrent(t *testing.T) {
    // 10 goroutines each creating 100 issues
    // Verify no ID collisions
    // Verify all issues created
}

func TestCreateIssues_MixedWithCreateIssue(t *testing.T) {
    // Concurrent CreateIssue + CreateIssues
    // Verify no ID collisions
}
```

### Performance Benchmarks

```go
func BenchmarkCreateIssue_Sequential(b *testing.B)
func BenchmarkCreateIssues_Batch(b *testing.B)

// Expected results (100 issues):
// CreateIssue x100:  ~600-1200ms
// CreateIssues:      ~60-130ms
// Speedup:           5-10x
```

### Integration Tests

```go
func TestImport_LargeJSONL(t *testing.T) {
    // Import 1000 issues from JSONL
    // Verify all created correctly
    // Verify performance < 1s
}
```

---

## Migration Plan

### Step 1: Add Interface Method (Non-Breaking)

```go
// storage/storage.go
type Storage interface {
    CreateIssue(ctx context.Context, issue *types.Issue, actor string) error
    CreateIssues(ctx context.Context, issues []*types.Issue, actor string) error  // NEW
    // ... rest unchanged
}
```

### Step 2: Implement SQLiteStorage.CreateIssues

Follow implementation strategy above

### Step 3: Add Tests

Comprehensive unit + concurrency + benchmark tests

### Step 4: Update Import (Optional)

```go
// cmd/bd/import.go - replace loop with batch
func importIssues(store Storage, issues []*Issue) error {
    // Old:
    // for _, issue := range issues {
    //     store.CreateIssue(ctx, issue, "import")
    // }

    // New:
    return store.CreateIssues(ctx, issues, "import")
}
```

**Note**: Start with internal use (import), expose CLI later if needed

### Step 5: Performance Testing

```bash
# Generate test JSONL
bd export > backup.jsonl

# Duplicate 100x for stress test
cat backup.jsonl backup.jsonl ... > large_test.jsonl

# Test import performance
time bd import large_test.jsonl
```

---

## Future Enhancements (NOT for bd-222)

### Batch Size Limits

If very large batches cause memory issues:

```go
func (s *SQLiteStorage) CreateIssues(ctx, issues, actor) error {
    const maxBatchSize = 1000

    for i := 0; i < len(issues); i += maxBatchSize {
        end := min(i+maxBatchSize, len(issues))
        batch := issues[i:end]

        if err := s.createIssuesBatch(ctx, batch, actor); err != nil {
            return fmt.Errorf("batch %d-%d failed: %w", i, end, err)
        }
    }
    return nil
}
```

**Decision**: Don't implement until we see issues with large batches (>1000)

### Validation-Only Mode

Pre-validate batch without creating:

```go
func (s *SQLiteStorage) ValidateIssues(ctx, issues) error
```

**Use Case**: Dry-run before bulk import

**Decision**: Add if import workflows request it

### Progress Callbacks

Report progress for long-running batches:

```go
type BatchProgress func(completed, total int)

func (s *SQLiteStorage) CreateIssuesWithProgress(ctx, issues, actor, progress) error
```

**Decision**: Add if agent workflows request it (likely for 1000+ issue batches)

---

## Performance Analysis

### Baseline (CreateIssue loop)

For 100 issues:
```
Connection overhead:  100ms   (1ms × 100)
Transaction overhead: 300ms   (3ms × 100, with lock contention)
ID generation:        250ms   (2.5ms × 100)
Insert + event:       250ms   (2.5ms × 100)
Total:                900ms
```

### With CreateIssues

For 100 issues:
```
Connection overhead:   1ms    (1 connection)
Transaction overhead:  5ms    (1 transaction)
ID range generation:   15ms   (1 query, more complex)
Bulk insert (prep):    50ms   (prepared stmt × 100)
Bulk events (prep):    30ms   (prepared stmt × 100)
Bulk dirty (prep):     20ms   (prepared stmt × 100)
Commit:                5ms
Total:                 126ms  (7x faster)
```

### Scalability

| Issues | CreateIssue Loop | CreateIssues | Speedup |
|--------|------------------|--------------|---------|
| 10     | 90ms            | 30ms         | 3x      |
| 100    | 900ms           | 126ms        | 7x      |
| 1000   | 9s              | 800ms        | 11x     |
| 10000  | 90s             | 6s           | 15x     |

**Key Insight**: Speedup increases with batch size due to fixed overhead amortization

---

## Why This Solution Wins

### For Individual Devs & Small Teams
- **Zero impact on normal workflow**: CreateIssue unchanged
- **Fast imports**: 1000 issues in <1s instead of 10s
- **Simple mental model**: All-or-nothing batch
- **No new concepts**: Same semantics as CreateIssue, just faster

### For Agent Swarms
- **Efficient decomposition**: Agent creates 50 subtasks in one call
- **Atomic work generation**: All issues created or none
- **No connection exhaustion**: One connection per batch
- **Safe concurrency**: BEGIN IMMEDIATE prevents races

### For New Codebase
- **Non-breaking change**: Additive API only
- **Performance win**: 5-15x faster for bulk operations
- **Simple implementation**: ~200 LOC, similar to CreateIssue
- **Battle-tested pattern**: Same transaction semantics as CreateIssue

---

## Alternatives Considered and Rejected

### Alternative 1: Auto-Batch in CreateIssue

Automatically detect rapid CreateIssue calls and batch them.

**Why Rejected**:
- ❌ Magical behavior (implicit batching)
- ❌ Complex implementation (goroutine + timer + coordination)
- ❌ Race conditions and edge cases
- ❌ Unpredictable performance (when does batch trigger?)
- ❌ Can't guarantee atomicity across auto-batch boundary

### Alternative 2: Separate Import API

Add ImportIssues specifically for JSONL import, not general-purpose.

**Why Rejected**:
- ❌ Limits use cases (what about agent workflows?)
- ❌ Name doesn't match behavior (it's just batch create)
- ❌ CreateIssues is more discoverable and general

### Alternative 3: Streaming API

```go
type IssueStream interface {
    Send(*Issue) error
    CloseAndCommit() error
}
func (s *SQLiteStorage) CreateIssueStream(ctx, actor) (IssueStream, error)
```

**Why Rejected**:
- ❌ More complex API (stateful stream object)
- ❌ Error handling complexity (partial writes?)
- ❌ Doesn't match Go/SQL idioms
- ❌ Caller must manage stream lifecycle
- ❌ Simple slice is easier to work with

---

## Conclusion

The **simple all-or-nothing batch API** (CreateIssues) is the best solution because:

1. **Significant performance win**: 5-15x faster for bulk operations
2. **Simple API**: Just like CreateIssue but with slice
3. **Safe**: Atomic transaction, no partial state
4. **Non-breaking**: Existing CreateIssue unchanged
5. **Flexible**: Supports mixed ID assignment (auto + explicit)
6. **Proven pattern**: Same transaction semantics as CreateIssue

The key insight is **atomic ID range reservation** - updating the counter once for N issues instead of N times. Combined with a single transaction and prepared statements, this provides major performance improvements without complexity.

This aligns perfectly with beads' goals: simple for individual devs, efficient for bulk operations, robust for agent swarms.

**Implementation size**: ~200 LOC + ~400 LOC tests = manageable, low-risk change
**Expected performance**: 5-15x faster for bulk operations (N > 10)
**Risk**: Low (additive API, comprehensive tests)
