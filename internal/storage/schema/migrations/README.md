# Writing migrations against Dolt

Migration files here are **frozen once merged** (`scripts/check-migration-hygiene.sh`
check C): editing one forks fresh clones from upgraded clones through the
recorded content hash. A migration that FAILS on drifted databases cannot be
fixed forward with a new migration either, because the failing file aborts the
pass before any higher version runs. The escape hatch is a **pre-migration
repair** in Go, keyed to the pending version — see the header of
`../migration_repairs.go` and the `preMigrationRepairs` registry.

A frozen migration that is correct but too slow to finish gets the second
escape hatch: an **execution plan** in `../migration_body_plans.go`
(`migrationBodyPlans`). A plan is registered for one exact source, file name and
SHA-256, keeps the text before and after a named anchor pair byte for byte, and
replaces only what lies between with equivalent SQL (optionally after indexing
tables the frozen body itself creates and drops). The cursor row still records
the frozen file's hash, `AllMigrationsSQL` still emits the frozen bytes, and a
plan that no longer matches its file fails the step before it touches anything.
Main 0059 and its twin ignored/0015 have one (#6520): their correlated
recompute did not finish in two hours on ~14k wisp dependencies.

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

**Restore any session variable you set, in the script that set it.** Six
migrations here clear `FOREIGN_KEY_CHECKS` and restore it (0041, 0043, 0047,
0050, 0053, 0058); keep that shape. Since wy-s8ytnw the steady-state open in
`uow.openAndInitSchema` hands the pool that ran the migration straight to the
provider for live queries, so a system session variable still changed when a
script reports success leaks into ordinary queries. Pairing the SETs inside one
script is what makes that safe — `initSchemaAttempt` pins one session per
attempt, so both statements land on the same session, and an attempt that dies
between them returns its connection to the pool, which is why the restore
cannot be the caller's job. User variables (`SET @sql`, `SET @needs_add`) are
exempt: nothing outside these scripts reads one, and each assigns before it
reads.

**The `dolt` CLI is not a safe harness for guarded migrations, twice over.**
`dolt sql -f`/`-q`/piped-stdin silently no-ops `PREPARE`/`EXECUTE`-driven
`ALTER TABLE` — every guarded migration here uses exactly that shape — and
`dolt sql -q "USE db; <stmt>"` exits **0** when the statement fails, so the
schema change is refused while the exit code reports success. Verify DDL
outcomes by reading `INFORMATION_SCHEMA`, never an exit code, and test guarded
migrations through the embedded (cgo) engine, where `PREPARE`/`EXECUTE` behaves
normally. See `internal/storage/embeddeddolt/migrate_frozen_guard_convergence_test.go`.

**Moving a tracked table onto the dolt_ignored plane loses its rows on every
clone but the one that migrates.** 0062 drops versioned `events`, commits the
drop, and recreates the table ignored, refilled from a temp copy. The commit
replicates; the refill does not. A clone that pulls the migration, or clones
after it, gets the drop and an empty table, while its rows still sit in its own
history at the drop commit's first parent (gastownhall/beads#7039). A future
flip must plan the refill for every clone, not only the migrator's, and that
refill cannot live in frozen SQL: the drop commit is only findable by its
frozen message, and the old rows are only readable `AS OF` its first parent
(`dolt_commit_ancestors.parent_hash` with `parent_index = 0`; the table's
`commit_hash` column names the child). Ignored 0031 is the shape to copy: an
inert marker whose Go repair (`../events_preflip_seed.go`) does the reads and
writes, with a header comment that names the table it writes so the raw-text
dirty-table guard still sees it (`migrationSQLTouchesTable` scans comments on
purpose). Never edit the frozen drop message; the repair matches it exactly.

**An UPDATE that does not assign `updated_at` stamps it.** `issues`, `wisps`,
`routes` and `federation_peers` declare `updated_at ... ON UPDATE
CURRENT_TIMESTAMP`, so any UPDATE that changes one of their rows overwrites its
last-modified time with the migration's wall clock (#5981: 0046/0047's
`is_blocked` recompute stamped every blocked issue on upgrade). Assign it
yourself in every UPDATE of those tables, `SET is_blocked = 0, updated_at =
updated_at`, as 0059 and ignored 0015 do; an explicit assignment suppresses the
clause. `TestMigrationUpdatesOfAutoStampedTablesKeepUpdatedAt` parses both
series and fails on an UPDATE that does not.

**So does an UPDATE that only joins one of those tables.** go-mysql-server,
unlike MySQL, applies the `ON UPDATE` clause of every table a multi-table
UPDATE joins, and writes the joined row back: 0041's `UPDATE dependencies d
JOIN issues i ON i.id = d.depends_on_id SET d.depends_on_issue_id = ...`
stamped every issue a dependency points at. Assign the joined table's
`updated_at` to itself as well (`SET d.x = ..., i.updated_at = i.updated_at`),
or test the other table with `IN (SELECT id FROM issues)` instead of joining
it. The census fails on a join that does neither.

The frozen migrations that predate these rules are listed in `updatedAtGuards`
(`../migration_updated_at_guard.go`). Main 0035, 0046, 0047 and 0053 and
ignored 0007 rewrite rows of an auto-stamped table: each entry names the one
column the step rewrites (`is_blocked` or `ephemeral`). Main 0041, 0047 and
0053 and ignored 0003 and 0011 join one: each entry lists it as
`joinStamped`. The runner snapshots `(id, updated_at, <column>)` of the step's
tables, plus a fingerprint of every other column of a `joinStamped` table,
before its pre-migration repair and body. After the body, before the step's
cursor row and commit, it writes the old `updated_at` back on the rows the
body rewrote: a row whose column changed, and on a `joinStamped` table a row
whose other columns are all unchanged. A row whose column shows the body left
it alone, and whose other columns changed, keeps whatever time another writer
gave it during the step. The census also fails if a guarded step's UPDATE
assigns anything but its registered column. That registry is closed: a new
migration self-assigns instead of joining it.

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

## Release branches

A release branch takes migrations only as cherry-picks of `main`'s, and the
runner's cursor is `MAX(version)`: a migration numbered below one a database
has already applied never runs. So in each series (main and ignored), a release
branch's migrations stay a byte-identical prefix of `main`'s. Backport a
migration only at the branch's next free number, only as `main`'s identical
file, and never renumber it. Once `main` has merged a higher-numbered
migration, a fix for the release line can't be a new migration file. A Go
pre-migration repair reaches only stores whose version is still pending; for
stores already at the branch's latest version, use an open-time repair gated
by its own `migrationWorkNeeded` term, as `ensureBackfilledCustomStatusesCustomTypes`
does, and make `main`'s migration tolerate the repaired shape. RELEASING.md
("Backports") has the full recipe.

