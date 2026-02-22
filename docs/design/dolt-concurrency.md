# Dolt Concurrency Model: Transaction-Based Shared Main

> **Status**: Design draft
> **Date**: 2026-02-22
> **Authors**: Steve Yegge, crew max
> **Input**: Tim Sehn (Dolt co-founder), DoltHub blog 2026-02-18
> **Scope**: Beads (primary), Gas Town (operational), Wasteland (federation)

---

## Problem Statement

Beads is the universal data plane for Gas Town. Every agent — polecats, mayor,
witness, refinery, deacon, crew, dogs — reads and writes beads as their primary
means of coordination. The Dolt concurrency model must serve **all** of them,
not just polecats.

Gas Town currently uses a **branch-per-worker** strategy for Dolt concurrency
(historically called "branch-per-polecat," though the issue affects all agents):
workers get their own Dolt branches, write in isolation, and merge to main later.

This was designed to eliminate optimistic lock contention between concurrent
writers, and it works — 50 concurrent writers, 250 Dolt commits, 100% success
rate in tests. But the concurrency wins are **illusory** because:

1. **Workers can't see each other's beads.** A bead created by agent A is
   invisible to agent B until A's branch merges to main. This breaks
   cross-agent visibility for dispatching, dependency tracking, and status queries.

2. **Shared state must live on main.** Beads is the coordination layer for the
   entire town. Every role — polecats doing work, mayor dispatching, witness
   monitoring, refinery validating, crew assisting — needs the same view of
   bead state. Branch isolation is the opposite of what a shared data plane
   requires.

3. **Merge-at-done introduces staleness.** Long-running agents accumulate
   divergence. The merge at completion is a batch reconciliation point, not
   a continuous shared view.

4. **Branch proliferation.** Each sling creates a branch; cleanup relies on
   `gt done` or `gt polecat nuke`. Orphaned branches accumulate. The
   BD_BRANCH safety analysis (#1796) adds code complexity across the codebase.

Tim Sehn's guidance (2026-02-21): **"It is far simpler to use one branch, so
start there. You can get hundreds of transactions per second on a single
branch. We fixed the bug you ran into."**

## Tim's Key Insight

> "I think you dolt commit every sql statement. If you don't you want to wrap
> writes in a BEGIN and finish with a CALL DOLT_COMMIT(), ie in a transaction
> otherwise connections will commit each other's writes."

This identifies the core issue: without explicit SQL transactions, Dolt's
auto-commit mode means **any connection can inadvertently commit another
connection's uncommitted working set changes**. The fix is proper transaction
boundaries.

## Dolt Concurrency Architecture (Background)

From the [DoltHub concurrency blog post](https://www.dolthub.com/blog/2026-02-18-dolt-concurrency/),
Dolt has **two concurrency layers**:

### Layer 1: SQL Transactions (MVCC)

Standard SQL transaction semantics with a twist — conflict detection uses
**three-way merge** against the branch HEAD, not row-level locking:

- Different cells modified concurrently: **no conflict** (auto-merged)
- Same cell updated to identical value: **no conflict**
- Same cell updated to different values: **conflict** (must resolve)

This is more permissive than traditional databases. Two agents updating
different fields of the same bead will succeed without conflict.

Isolation level: **repeatable read** (vulnerable to lost updates if two
connections read-then-write the same cell).

### Layer 2: Commit Graph (Serialized)

Version control operations (`DOLT_COMMIT`, `DOLT_MERGE`, `DOLT_BRANCH`, etc.)
acquire a **global lock**, execute atomically, then release. Merge work
happens outside the lock; only final graph writes are serialized.

Performance: **hundreds of commit graph operations per second** in normal
operation. The global lock is planned to become per-branch but hasn't been
a bottleneck in practice.

### The Two Patterns

**Pattern 1: Transaction-Wrapped Dolt Commits** (recommended for us)
```sql
BEGIN;
INSERT INTO issues (id, title, status) VALUES ('gt-abc', 'Fix bug', 'open');
INSERT INTO dependencies (issue_id, depends_on_id, type) VALUES ('gt-abc', 'gt-def', 'blocks');
CALL DOLT_COMMIT('-Am', 'bd: create gt-abc');
-- Transaction ends, changes are atomically visible
```

**Pattern 2: Branch-Per-Client** (our current approach — retiring)
```sql
CALL DOLT_BRANCH('worker-ace-1708642800');
CALL DOLT_CHECKOUT('worker-ace-1708642800');
-- Isolated writes, invisible to other agents
-- Merge later
```

## Proposed Design: All-On-Main with Transaction Discipline

### Principle

All beads live on `main`. Concurrent access is managed through SQL transactions
with explicit `DOLT_COMMIT` at transaction boundaries. No per-worker branches.

### Transaction Semantics

#### Rule 1: Every Write Group Gets a Transaction

Every logical write operation (create bead, update status, close bead, add
dependency, etc.) must be wrapped in `BEGIN` ... `CALL DOLT_COMMIT()`.

```go
// BEFORE (current): auto-commit per statement, no transaction boundary
func (s *DoltStore) CreateIssue(ctx, issue, actor) {
    s.db.Exec("INSERT INTO issues ...")  // auto-committed
    // DOLT_COMMIT happens later via maybeAutoCommit()
}

// AFTER: explicit transaction with DOLT_COMMIT inside
func (s *DoltStore) CreateIssue(ctx, issue, actor) {
    tx, _ := s.db.BeginTx(ctx, nil)
    tx.Exec("INSERT INTO issues ...")
    tx.Exec("INSERT INTO labels ...")        // if applicable
    tx.Exec("INSERT INTO dependencies ...")  // if applicable
    tx.Exec("CALL DOLT_COMMIT('-Am', ?)", msg)
    tx.Commit()
}
```

This ensures:
- All writes within a logical operation are **atomic**
- No other connection can commit our uncommitted writes
- The `DOLT_COMMIT` is part of the transaction, so it only includes our changes
- If the transaction rolls back, no Dolt commit is created

#### Rule 2: Read Operations Don't Need Transactions

Simple reads (`SELECT` from `issues`, `dependencies`, etc.) can use bare
connections. They'll see the latest committed state of main. Dolt's
repeatable-read isolation means reads within a transaction see a consistent
snapshot.

#### Rule 3: Batch Mode Becomes Transaction-Scoped

The current batch mode (accumulate changes, commit at a logical boundary)
maps naturally to a long-lived transaction:

```go
// Batch: open transaction, do multiple writes, single DOLT_COMMIT at end
tx.Begin()
for _, issue := range issues {
    tx.Exec("INSERT INTO issues ...")
}
tx.Exec("CALL DOLT_COMMIT('-Am', ?)", batchMsg)
tx.Commit()
```

### What Changes in Beads

#### `store.go`: Remove Branch-Per-Worker

Remove the `BD_BRANCH` initialization block (lines 336-358). The store always
operates on main. Remove `SetMaxOpenConns(1)` / `SetMaxIdleConns(1)` — we
want connection pooling now since all connections share the same branch.

```go
// DELETE this block:
if bdBranch := os.Getenv("BD_BRANCH"); bdBranch != "" {
    db.SetMaxOpenConns(1)
    db.SetMaxIdleConns(1)
    // ... branch creation/checkout logic
}
```

#### `transaction.go`: Add DOLT_COMMIT to Transactions

The current `RunInTransaction` does `sql.BeginTx` / `Commit` but never calls
`DOLT_COMMIT`. This means SQL changes are committed to the working set but
not to Dolt's version history. Another connection could then inadvertently
include those changes in its own `DOLT_COMMIT`.

```go
// BEFORE:
func (s *DoltStore) runDoltTransaction(ctx, fn) error {
    sqlTx, _ := s.db.BeginTx(ctx, nil)
    tx := &doltTransaction{tx: sqlTx, store: s}
    fn(tx)
    return sqlTx.Commit()  // SQL commit only — no Dolt commit!
}

// AFTER: DOLT_COMMIT inside the transaction
func (s *DoltStore) runDoltTransaction(ctx, fn, commitMsg) error {
    sqlTx, _ := s.db.BeginTx(ctx, nil)
    tx := &doltTransaction{tx: sqlTx, store: s}
    if err := fn(tx); err != nil {
        sqlTx.Rollback()
        return err
    }
    // Dolt commit INSIDE the SQL transaction — atomic with the writes
    _, err := sqlTx.Exec("CALL DOLT_COMMIT('-Am', ?, '--author', ?)",
        commitMsg, s.commitAuthorString())
    if err != nil && !isNothingToCommit(err) {
        sqlTx.Rollback()
        return err
    }
    return sqlTx.Commit()
}
```

#### `dolt_autocommit.go`: Retire or Simplify

With `DOLT_COMMIT` moving inside transactions, the external auto-commit
wrapper (`maybeAutoCommit`) becomes unnecessary for most operations. It may
still be useful as a safety net for bare writes that escape the transaction
pattern (migration scripts, one-off fixes), but the primary write path
should use transaction-scoped commits.

#### `versioned.go`: Merge/Branch Operations

`Merge()` and `DeleteBranch()` are only needed during the migration period
(cleaning up existing worker branches). After migration, they become
dead code for the normal write path. Retain for federation use cases
(DoltHub remote merge) and standalone Beads.

### What Changes in Gas Town

#### `gt sling`: Stop Creating Branches

Currently `gt sling` creates a Dolt branch and injects `BD_BRANCH` into
the worker environment. After migration:
- No branch creation at sling time
- No `BD_BRANCH` env var
- All agents use the same main-branch connection pool

#### `gt done`: Stop Merging Branches

Currently `gt done` checks out main, merges the worker's branch, and
deletes it. After migration:
- No merge step
- No branch deletion
- `gt done` simply closes the bead (already on main, already visible)

#### `BD_BRANCH` Safety Infrastructure (#1796)

The entire `BD_BRANCH` safety analysis (`bdbranch/analyzer.go`,
`OnMain()`, `StripBdBranch()`, the arch test registry) can be retired.
This is a significant simplification of the codebase.

#### `session_manager.go`: No DoltBranch Option

Remove the `DoltBranch` field from session options and the injection of
`BD_BRANCH` into tmux sessions.

### Connection Pool Configuration

With all connections on main, we need proper pooling:

```go
db.SetMaxOpenConns(10)     // Allow concurrent readers + writers
db.SetMaxIdleConns(5)      // Keep warm connections
db.SetConnMaxLifetime(5 * time.Minute)
```

The exact numbers depend on the rig's concurrency level. A typical Gas Town
rig with 6 polecats + mayor + witness + refinery + deacon + crew = ~11
concurrent agents, each potentially holding a connection.

### Wisps: No Change Needed

Wisps are already in `dolt_ignore`-ed tables. They are not version-tracked,
not branched, and not federated (except digest publication). The wisps
concurrency model is purely SQL — standard MySQL-compatible concurrent writes
with no Dolt-specific concerns.

Wisps are worker-local by design. An agent's wisps are only meaningful to
that agent's session. Cross-agent wisp visibility (e.g., molecule step
coordination) uses the events/comments tables, which are also dolt_ignored.

**No changes to wisps are needed for this migration.**

### Conflict Resolution

With multiple connections writing to main concurrently, conflicts are possible
but rare due to Dolt's cell-level merge semantics:

| Scenario | Conflict? | Resolution |
|----------|-----------|------------|
| Two agents create different beads | No | Different rows, auto-merged |
| Two agents update different beads | No | Different rows, auto-merged |
| Two agents update different fields of same bead | No | Different cells, auto-merged |
| Two agents update same field of same bead | **Yes** | Last writer wins (updated_at) |
| One agent writes while another reads | No | Read sees committed state |

The "same field of same bead" case is rare in practice — beads are typically
owned by one agent at a time (assigned via sling). The main risk is
concurrent status updates (e.g., a polecat closes a bead while the witness
also updates it). Mitigation: use optimistic concurrency checks where needed
(check expected status before update).

## Migration Strategy

### Phase 1: Add Transaction Discipline (Non-Breaking)

Modify `RunInTransaction` to include `DOLT_COMMIT` inside the SQL transaction.
This works regardless of branch-per-worker — it's strictly additive safety.

- Modify `transaction.go`: Add commit message parameter, call `DOLT_COMMIT`
  before `tx.Commit()`
- Update all `RunInTransaction` callers to provide commit messages
- Keep `maybeAutoCommit` as fallback for bare writes
- **Test**: Existing concurrent_test.go should still pass

### Phase 2: Remove Branch-Per-Worker (Gas Town)

Conditional on Phase 1 being stable in production.

- Remove `BD_BRANCH` injection from `polecat_spawn.go` and `session_manager.go`
- Remove branch creation from `gt sling`
- Remove branch merge from `gt done`
- Remove `BD_BRANCH` env var handling from `store.go`
- Clean up `OnMain()`, `StripBdBranch()`, analyzer infrastructure
- **Test**: Deploy with 2-3 agents, verify cross-agent bead visibility

### Phase 3: Retire Branch Infrastructure (Cleanup)

- Remove `bdbranch/analyzer.go` and arch test registry
- Remove `BD_BRANCH` references from documentation
- Update `dolt-storage.md` design doc
- Clean up orphaned branches from existing installations
- **Test**: Full swarm (6+ polecats), stress test with concurrent writes

## Implications for Federation (Wasteland)

The move to all-on-main **simplifies** federation:

- **Push/pull is branch-clean.** A single main branch means `dolt push` and
  `dolt pull` operate on a single linear history (modulo Dolt's content-addressed
  merge commits). No branch namespace pollution.

- **Commit graph is simpler.** Branch-per-worker created a complex DAG that
  was hard to reason about when syncing with DoltHub remotes. All-on-main
  produces a cleaner commit history.

- **Cross-town bead visibility is immediate.** When town A pushes to DoltHub,
  town B pulls and sees all beads. No branch reconciliation needed.

- **Federation transactions.** The same `BEGIN` ... `DOLT_COMMIT` pattern
  applies to federation sync: pull remote changes, resolve conflicts, commit.
  This is the same flow as Dolt's native replication.

### Wisps and Federation

Wisps are `dolt_ignore`-d and never pushed. This is correct:

- Wisps are ephemeral, worker-local operational state
- Wisp digests (summaries) can optionally be crystallized into beads
  and published to main for federation
- The digest publication flow is already a separate operation that creates
  a proper bead (not a wisp)

## Implications for Standalone Beads

The `bd` CLI supports both embedded Dolt and server mode. This design
applies to **server mode only** (Gas Town's deployment). Embedded mode
is single-process and doesn't have the multi-connection concurrency
concerns described here.

For standalone `bd` with embedded Dolt:
- Single connection, auto-commit is fine
- No branch-per-worker (single user)
- Transaction wrapping is still good practice but not critical

## Performance Expectations

Tim's guidance: **hundreds of transactions per second on a single branch.**

Our workload:
- Typical Gas Town: 6-12 concurrent agents (all roles combined)
- Write patterns: create/update/close beads, ~1-10 writes per agent per minute
- Read patterns: status queries, bead lookups, ~10-100 reads per agent per minute
- Total: ~60-120 writes/minute, ~600-1200 reads/minute at peak

This is well within Dolt's single-branch capacity. Even at 10x scale (60+
agents across a large town), we'd hit ~1200 writes/minute = ~20 writes/second,
far below the hundreds-per-second ceiling.

## Open Questions

1. **Commit granularity.** Should every `bd create` produce its own Dolt
   commit, or should we batch at a higher level (e.g., per-molecule, per-formula
   step)? Per-operation gives better auditability; batching reduces commit
   graph size. Tim's model suggests per-operation is fine at our scale.

2. **Connection pool sizing.** What's the right pool size per rig? Need to
   test under load. Start conservative (10 max connections) and tune.

3. **Lost update protection.** Dolt's repeatable-read isolation doesn't
   prevent lost updates. Do we need application-level optimistic locking
   (e.g., `WHERE updated_at = ? AND status = ?`) for high-contention
   fields like `status` and `assignee`?

4. **Existing branch cleanup.** Production Gas Towns have accumulated
   worker branches. Need a migration script to merge-or-delete these
   before switching to all-on-main.

5. **Embedded mode fallback.** If a standalone `bd` user runs multiple
   processes against the same embedded Dolt (unlikely but possible), they'd
   hit the same issues. Document as unsupported, or add transaction
   discipline to embedded mode too?

## References

- [Dolt Concurrency Architecture (DoltHub blog, 2026-02-18)](https://www.dolthub.com/blog/2026-02-18-dolt-concurrency/)
- Tim Sehn email to Steve Yegge, 2026-02-21
- `beads/internal/storage/dolt/store.go` — DoltStore, branch-per-worker init
- `beads/internal/storage/dolt/transaction.go` — RunInTransaction
- `beads/cmd/bd/dolt_autocommit.go` — auto-commit wrapper
- `gastown/internal/cmd/done.go` — gt done merge flow
- `gastown/internal/polecat/session_manager.go` — BD_BRANCH injection
- `gastown/docs/design/dolt-storage.md` — current architecture doc
- `gastown/internal/analysis/bdbranch/` — BD_BRANCH safety analyzer
- Gas Town issue `gt-4j1g7p`: "Remove Dolt branch-per-polecat entirely" (predates this design)
