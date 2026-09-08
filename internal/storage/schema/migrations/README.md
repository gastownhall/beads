# Writing migrations against Dolt

Migration files here are **frozen once merged** (`scripts/check-migration-hygiene.sh`
check C): editing one forks fresh clones from upgraded clones through the
recorded content hash. A migration that FAILS on drifted databases cannot be
fixed forward with a new migration either, because the failing file aborts the
pass before any higher version runs. The escape hatch is a **pre-migration
repair** in Go, keyed to the pending version — see the header of
`../migration_repairs.go` and the `preMigrationRepairs` registry.

## Measured Dolt behaviours

These were measured against real Dolt (2.1.2 and 2.2.3) and confirmed against
Dolt/GMS source while fixing migration 0058. Each one has already cost a
redesign; they are recorded here so the next author does not re-derive them.

**DDL is not transactional across statements.** Each DDL statement implicitly
commits, so `START TRANSACTION` … `ALTER` … `ROLLBACK` leaves the table altered.
A destructive multi-statement rebuild cannot be made atomic by wrapping it. Make
each step individually guarded and resumable instead.

**A foreign key on the base column of a stored generated column is rejected —
on `ADD` *and* at `CREATE TABLE` time.**

```
Error 1105 (HY000): Cannot add foreign key on the base column of a stored
generated column.
```

The restriction is asymmetric: key-then-generated-column is accepted,
generated-column-then-key is not. That asymmetry is the only reason a table can
legitimately hold both, and it dictates the order of any rebuild. This is what
blocked 0058, whose two `ADD CONSTRAINT`s target the columns feeding
`depends_on_id`'s `COALESCE`.

**Foreign key constraint names are database-scoped, not table-scoped.** Dolt
keeps a root-wide collection and duplicate-name lookup ignores the declaring
table, so two tables cannot hold the same constraint name even transiently. A
build-alongside-and-swap rebuild cannot reuse the real constraint names on its
replacement table.

**A foreign key holds its backing index hostage.**

```
Error 1553 (HY000): can't drop index 'PRIMARY': needed in foreign key
constraint fk_wisp_dep_issue
```

Drop the foreign keys before `DROP PRIMARY KEY` and re-add them last. On the
legacy `wisp_dependencies` shape the primary key is the only `issue_id`-leading
index, so `fk_wisp_dep_issue` alone blocks the rebuild.

**Updating a base column rewrites a stored generated column, and therefore any
key over it.** Nulling one target column to satisfy a "exactly one target" check
recomputes a `COALESCE` generated column, which can land the row on another
row's value in a composite primary key that includes it:

```
duplicate primary key given: [w2,external:e1]
```

Normalize *after* dropping the generated column and its key, not before. (0058
normalizes before, and reaches the abort on any store whose rows collide.)

**The `dolt` CLI is not a safe harness for guarded migrations, twice over.**
`dolt sql -f`/`-q`/piped-stdin silently no-ops `PREPARE`/`EXECUTE`-driven
`ALTER TABLE` — every guarded migration here uses exactly that shape — and
`dolt sql -q "USE db; <stmt>"` exits **0** when the statement fails, so the
schema change is refused while the exit code reports success. Verify DDL
outcomes by reading `INFORMATION_SCHEMA`, never an exit code, and test guarded
migrations through the embedded (cgo) engine, where `PREPARE`/`EXECUTE` behaves
normally. See `internal/storage/embeddeddolt/migrate_frozen_guard_convergence_test.go`.

## Testing

`sqlmock` statement-echo tests cannot exercise any of the above; they assert the
statements you wrote, not what Dolt does with them. Anything touching generated
columns, constraint validation, or key rebuilds needs a real-Dolt test in
`internal/storage/embeddeddolt` (`BEADS_TEST_EMBEDDED_DOLT=1`, `-tags cgo`).

Assert the resulting **shape**, not `SHOW CREATE TABLE` text. A legacy table
healed by the shipped chain keeps its original column order and its original
secondary index names, because nothing reorders columns and Dolt reuses an
existing index to back a new foreign key rather than creating a
constraint-named one. Text equality against a fresh store fails on correctly
healed databases. Compare columns, primary key, unique keys, foreign keys with
their referenced tables and actions, checks, and index coverage — see
`migrate_wisp_dep_forward_repair_test.go`.
