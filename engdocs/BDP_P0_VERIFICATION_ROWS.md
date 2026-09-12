# BDP P0 verification rows — the row-level fence (ruling 13)

Status: **spikes complete, 2026-09-07; council-11 fold applied the same day**
(the mid-statement probes now establish their premise by observation, the
real-Dolt spikes gate on the dolt binary alone, and rule 3 below names
`CREATE TRIGGER` and `DROP TRIGGER` separately). Tree: `c0d8da42d` (upstream
main), spiked on branch `janet-beadgraph-p0-spikes` and merged into
`janet-beadgraph-p0`. Dolt: **2.1.8** (`dolt version`), local
binary; no Docker on the machine, so every sql-server here is a scratch
`dolt sql-server` started through the tree's own launcher
(`internal/storage/dbproxy/server.NewDoltServer`) under `t.TempDir()` with an
isolated `DOLT_ROOT_PATH`. Go 1.26.5, `go-sql-driver/mysql v1.10.0`,
`dolthub/driver/v2 v2.2.0` (the embedded engine), build flags from
`.buildflags` (`-tags=gms_pure_go`, `CGO_ENABLED=1`).

This answers the three P0 verification rows that gate the session-gated
trigger fence of `engdocs/BDP_GRAPH_CLI_AND_STORAGE_SPEC.md` Part B4 ("The
row-level fence (ruling 13)"; B2 carries the connection-hygiene claim; the
plan's §9 ruling 13 records the probes). Each row has a runnable test whose
outcome does not depend on scheduling: the two mid-statement probes (c3, d2)
cancel only after a second connection has *observed* the body's session
executing its `SELECT SLEEP(10)` in `information_schema.processlist`, so the
cancellation provably lands inside the statement and a probe whose premise
never holds fails rather than quietly exercising the between-statement case.
Nothing was added to the migration series (P0 has no migrations, slots are
claimed elsewhere); no production code changed.

| Row | Verdict | One line |
| --- | --- | --- |
| (i) embedded leg | **PASS** | `embeddeddolt.OpenSQL` + one pinned `*sql.Conn` + one `ExecContext` of the migration text creates the three `BEGIN … END` triggers; they fire (errno 1644) on INSERT/UPDATE/DELETE; `SET @bd_graph_role = 1` in the same session lets writes through; `dolt_schemas` stages and commits; the `withConn` `*sql.Tx` shape works. The "skips them by rule" branch is not needed. |
| (ii) pooled-connection hygiene | **PASS-WITH-RULE** | Clear-before-COMMIT fences the pooled session (a); commit-without-clear leaves it unfenced (b, the hazard). **Cancellation between statements does not close the connection on either server leg** — the session goes back to the pool with `@bd_graph_role = 1` — on the UOW leg because `closeAttempt` rolls back on `context.WithoutCancel` and releases the conn, on the `*sql.Tx` legs because `database/sql` keeps the connection when the driver implements `SessionResetter` and `Validator` (go-sql-driver does). Only a mid-statement cancellation closes it. Rule: the deferred clear runs on `context.WithoutCancel(ctx)` (proved to fence in c2), and a clear that cannot be confirmed poisons the connection. B2's "closed by the driver, not pooled" sentence is wrong as written. The embedded leg has no pool hazard: its driver never reuses a session. |
| (iii) hygiene checks vs `dolt_schemas` | **PASS-WITH-RULE** | Hygiene checks A–E pass on the trigger-carrying file (controls prove B, C, D still bite); the real runner (`runMigrations` → `execMigrationBody` → content hash → `commitMigrationStep`) applies it and commits `dolt_schemas` with the table and the cursor; `content_skew.go` reports a clone that applied different content for the same version and — as designed — says nothing about a clone that dropped the triggers out of band (equal hashes). Rules: (1) `AllMigrationsSQL()`'s `dolt sql -f` route cannot load the body (it splits at the body's inner `;`), so a trigger-carrying migration needs a DELIMITER-wrapped `cliCompatibleMigrationSQL` rendition, and the CLI parity oracle excludes `dolt_` tables so it cannot notice a bundle without the triggers; (2) check D's clone-local list and `schema.go`'s `doltIgnorePatterns` do not know `graph_authority_lease`, so the B4 twin is unenforced until they do; (3) trigger DDL is invisible to `migrationSQLTouchesTable`, so a pre-existing dirty `dolt_schemas` is refused late (post-pass signature check), not up front; (4) `dolt_schemas` is outside the eight tables the validator hashes and ruling 14's fetch-inspect reads — an out-of-band `DROP TRIGGER` replicates silently. |

Net: **the fence ships as designed on all three legs, with the rules above
folded into B2/B3/B4 and the P1 migration PR.** Nothing here sends v0 back to
"validator alone".

## Files

- `internal/storage/embeddeddolt/beadgraph_fence_spike_test.go` — row (i). `//go:build cgo`, gate `BEADS_TEST_EMBEDDED_DOLT=1` (the package's convention).
- `internal/storage/uow/beadgraph_fence_spike_test.go` — row (ii). Gate: a `dolt` binary (`testutil.RequireDoltBinary`, which honours `BEADS_TEST_SKIP=dolt` and fails rather than skips under `GITHUB_ACTIONS`), so the tree's Dolt lane runs it like every other real-Dolt test; there is no separate opt-in.
- `internal/storage/schema/beadgraph_fence_spike_test.go` — row (iii). Same gate for the four real-Dolt tests; the hygiene-script test needs only bash ≥ 4 and git and runs by default.
- `internal/storage/schema/testdata/beadgraph_fence_spike/9001_beadgraph_fence_spike.up.sql` — the throwaway migration (B4 shape, one table, three trigger pairs), shared by all three packages. `…_variant/9001_beadgraph_fence_spike.up.sql` — same version, no trigger block (the divergent clone).
- This report.

Under `BEADS_TEST_SKIP=dolt` (the `scripts/test.sh` default) — or locally
without a `dolt` binary — every Dolt-bound spike skips and `go test ./...`
stays green; the embedded spike keeps its package's `BEADS_TEST_EMBEDDED_DOLT=1`
opt-in.

## Rerun

```sh
cd /Users/dbox/repos/beads-janet-beadgraph-p0   # or any checkout of branch janet-beadgraph-p0
# row (i) — embedded engine, in-process (the package's own opt-in)
BEADS_TEST_EMBEDDED_DOLT=1 GOFLAGS=-tags=gms_pure_go CGO_ENABLED=1 \
  go test ./internal/storage/embeddeddolt/ -run 'TestSpikeBeadGraphFence' -count=1 -v
# row (ii) — scratch dolt sql-server, unit-of-work leg and *sql.Tx shape (needs `dolt` on PATH)
GOFLAGS=-tags=gms_pure_go CGO_ENABLED=1 \
  go test ./internal/storage/uow/ -run 'TestSpikeBeadGraphFence' -count=1 -v
# row (iii) — scratch dolt sql-server, runner, guards, skew, hygiene script, CLI bundle route
GOFLAGS=-tags=gms_pure_go CGO_ENABLED=1 \
  go test ./internal/storage/schema/ -run 'TestSpikeBeadGraphFence' -count=1 -v
# what the default runner sees: Dolt-bound spikes skip, the hygiene test runs
BEADS_TEST_SKIP=dolt GOFLAGS=-tags=gms_pure_go CGO_ENABLED=1 \
  go test ./internal/storage/schema/ ./internal/storage/uow/ ./internal/storage/embeddeddolt/ -run 'TestSpikeBeadGraphFence' -count=1 -v
```

Set `TMPDIR` to keep the scratch servers and temp git repos out of the default
temp directory; the tests stop every server in `t.Cleanup`. Each server-backed
test takes about a second after the first build.

The 2026-09-07 runs: all packages `ok`; `go vet` on the three packages clean;
`go build ./...` clean; `pgrep -fl "dolt sql-server"` empty afterwards.

---

## Row (i) — the embedded leg

**Question.** Through the tree's embedded Dolt path (the in-process engine
behind `internal/storage/embeddeddolt/open.go` `OpenSQL`, not a sql-server),
does a migration-style multi-statement SQL text create the session-gated
`BEGIN … END` triggers, do they fire on INSERT/UPDATE/DELETE, and does
`SET @bd_graph_role = 1` in the same session let a write through?

**Setup** (`TestSpikeBeadGraphFenceEmbeddedLeg`). A fresh data dir;
`OpenSQL(dir, "", "")` → `CREATE DATABASE spike` → close (the `initSchema`
shape); `OpenSQL(dir, "spike", "main")` → `db.Conn(ctx)` (the
`ApplySchemaMigrations` shape: one pinned connection); the whole migration
file in **one** `conn.ExecContext` (exactly `schema.execMigrationBody`); then
raw and in-role DML on that session; then `dolt_status` → `DOLT_ADD('-f', …)`
→ `DOLT_COMMIT` (the `commitMigrationStep` shape); then a fresh `OpenSQL` and
the `withConn` shape (`BeginTx` → body → `Commit`/`Rollback`).

**Observed.**

- One `ExecContext` of the file created all three triggers
  (`SELECT COUNT(*) FROM dolt_schemas WHERE type = 'trigger' AND name LIKE
  'graph_beads_spike_%'` = 3). A second pass of the same text (the
  `DROP TRIGGER IF EXISTS` + `CREATE TRIGGER` pair) succeeded and left exactly
  three: the pair is resumable on this leg too.
- Raw `INSERT` without the variable: `(*mysql.MySQLError) Error 1644:
  graph_beads_spike: out-of-role write refused` — the embedded driver surfaces
  the SIGNAL as the same errno the sql-server does. Raw `UPDATE` and `DELETE`
  against an existing row: refused, row untouched.
- `SET @bd_graph_role = 1` on the same session: INSERT, UPDATE, DELETE all
  pass; `SET @bd_graph_role = NULL`: the next INSERT is refused again.
- `dolt_status` listed `dolt_schemas` and `graph_beads_spike`;
  `CALL DOLT_ADD('-f', 'dolt_schemas')` and the table, then `DOLT_COMMIT`,
  left `SELECT COUNT(*) FROM dolt_schemas AS OF 'HEAD' …` = 3 and an empty
  `dolt_status`.
- Fresh engine handle, `withConn` shape: an out-of-role INSERT inside a
  `*sql.Tx` is refused; `SET` → INSERT → clear → `Commit` persists the row.
- Pool corollary (see row ii): after a `*sql.Tx` that set the variable and
  committed **without** clearing, the next statement on a one-connection pool
  was still fenced and `SELECT @bd_graph_role` was NULL. The reason is in the
  driver: `github.com/dolthub/driver/v2` `conn.go` `ResetSession` returns
  `driver.ErrBadConn` on purpose ("do not try to reuse connections … throw the
  session away and get a new one"), so `database/sql` opens a fresh session on
  every checkout after a release. Production additionally closes the whole
  engine handle per store call (`withConn`).

**Why it works.** `OpenSQL`'s DSN sets `multistatements=true`
(`buildDSN`); the driver's `prepareMultiStatement` splits the text with the
engine's own parser (`gms.NewMysqlParser().Parse(ctx, remainder, true)`), which
is `BEGIN … END`-aware — not the driver's naive `query_splitter.go`. The
upstream driver ships a `TestMultiStatementsTrigger` smoke test for the same
path.

**Verdict: PASS.** The fence is created, fires, and is gated on the embedded
leg through the tree's real code path. B4's "(i) … or skips them by rule"
alternative is not needed; A9 still makes embedded workspaces refuse
authority, and the triggers protect raw DML on them regardless.

---

## Row (ii) — pooled-connection hygiene on the SQL-server leg

**Question.** With the tree's own connection/pool setup, (a) does
set → write → clear-before-COMMIT leave the pooled connection fenced for the
next raw statement; (b) does commit-without-clear leave it unfenced; (c) when a
transaction is torn down by context cancellation, is the connection closed by
the driver or returned to the pool still carrying the variable?

**Setup** (`TestSpikeBeadGraphFencePooledConnectionHygiene`). One scratch
sql-server; the fence installed with one multi-statement `Exec` over the
provider DSN and committed. Every probe uses a **one-connection pool** and
compares `SELECT CONNECTION_ID()` inside the body with the follow-up statement,
so the follow-up is provably on the same server session.

- **UOW leg** (the serving leg): a real `doltSQLProvider` over the provider's
  own DSN builder (`buildDSN` → `util.DoltServerDSN`, `multiStatements=true`,
  `clientFoundRows`) and opener (`openDB`), bounded through its `PoolLimits`
  seam, driven with `RunTx` — so the pinned `*sql.Conn` +
  `START TRANSACTION` of `doltserver_tx.go`, the retry loop, `Commit`
  (`HasPendingChanges` + `DOLT_COMMIT('-Am')`) and `closeAttempt` are the
  tree's.
- **`*sql.Tx` shape**: the exact `DoltStore.withWriteTx` sequence
  (`internal/storage/dolt/store.go`: `s.db.BeginTx(ctx, nil)` → `fn(tx)` →
  `tx.Commit()` or `errors.Join(err, tx.Rollback())`), which
  `EmbeddedDoltStore.withConn` shares, over the CLI leg's DSN builder
  (`doltutil.ServerDSN`).

**Observed.**

| Probe | Leg / shape | Result |
| --- | --- | --- |
| a | UOW `RunTx`: `SET 1` → INSERT → `SET NULL` → commit | same `CONNECTION_ID`; raw INSERT **fenced** (errno 1644, SQLSTATE 45000); `@bd_graph_role` NULL |
| b | UOW `RunTx`: `SET 1` → INSERT → commit, no clear | same session; raw INSERT **passed**; `@bd_graph_role` = 1. **The hazard.** |
| c1 | UOW, caller's context cancelled between statements, deferred clear on the body's own ctx | the clear never reaches the server (`context.Canceled`: `go-sql-driver` `connection.go` `watchCancel` refuses to send on an already-cancelled ctx); `closeAttempt` (`tx.go`) rolls back on `context.WithoutCancel` (its stated purpose is to stop burning sessions) and `releaseConn` returns the session to the pool: `OpenConnections` = 1, same `CONNECTION_ID`, the in-role row was rolled back, raw INSERT **passed**, `@bd_graph_role` = 1 (ROLLBACK does not reset user variables). **The hazard, on the serving leg.** |
| c2 | as c1, deferred clear on `context.WithTimeout(context.WithoutCancel(ctx), 5s)` | the clear reaches the server; same session pooled; raw INSERT **fenced**; variable NULL. **The remedy.** |
| c3 | UOW, cancelled **mid-statement** (`SELECT SLEEP(10)`, cancelled once a second connection sees the session executing it in `information_schema.processlist`) | the driver's watcher closes the socket; `closeAttempt`'s ROLLBACK fails; `poisonConn` discards the session: `OpenConnections` = 0; the next statement is on a fresh session, **fenced** |
| d1 | `*sql.Tx`, cancelled between statements | `Tx.awaitDone` (and an explicit `Rollback`) take `rollback(discardConn=false)` because `database/sql` `beginDC` sets `keepConnOnRollback = SessionResetter && Validator`, both implemented by `go-sql-driver/mysql v1.10.0` (`ResetSession` only checks liveness); same session pooled, `OpenConnections` = 1; raw INSERT **passed**; variable = 1. **The hazard, on the CLI/embedded shape** — but see row (i): the embedded driver discards sessions, so only the CLI server leg is exposed. |
| d2 | `*sql.Tx`, cancelled mid-statement (same observation-then-cancel) | socket closed, `OpenConnections` = 0, next statement **fenced** |
| d3 | `*sql.Tx`, body error with a live context and a deferred clear before `Rollback` | **fenced** — the deferred clear works on every exit path except cancellation |

**Verdict: PASS-WITH-RULE.** (a) and (b) are exactly as B2 states. (c)
contradicts B2's "A connection whose transaction is torn down by cancellation
is closed by the driver, not pooled": that is true only when the cancellation
lands **inside** a statement. Between statements — the common case for a
client hang-up or an expired deadline — both server legs pool the session with
the variable set, and a naive deferred clear cannot help because the driver
will not send on a dead context. The rule to fold into B2/B3:

1. The deferred `SET @bd_graph_role = NULL` runs on
   `context.WithTimeout(context.WithoutCancel(ctx), short)` — the same
   discipline `uow.closeAttempt` already applies to its ROLLBACK — on every
   exit path including cancellation (c2 proves it fences).
2. If that clear does not return success, the connection must not go back to
   the pool. On the UOW leg the mechanism exists (`doltServerTx.poisonConn`,
   `Raw(func(any) error { return driver.ErrBadConn })`) but is not reachable
   from a body: a P1 production change exposes it (a `Poison` on `Tx`, or a
   body-level helper that runs the clear and poisons on failure).
3. On the CLI server leg (`withWriteTx`/`withRetryTx`) `database/sql` owns
   the teardown: `awaitDone` can roll back and pool the session before any
   wrapper code runs. The wrapper must own it instead — begin the `*sql.Tx`
   on `context.WithoutCancel(ctx)` while still passing the caller's ctx to
   each statement, so cancellation aborts statements but never lets
   `database/sql` release the connection behind the wrapper's back; then
   apply rule 1 and, on failure, `Raw`-poison the pinned connection (which
   means pinning `db.Conn` the way the UOW leg does). The embedded leg needs
   nothing.
4. The mutating-budget line in B2 ("two statements more than their SQL
   alone") still holds; the clear is the same statement on a different
   context.

Exposure, for calibration: the leak is process-local (a pool dies with its
process). A one-shot `bd` command is barely exposed; `bd serve` and any
long-lived UOW provider are, and that is the serving leg.

---

## Row (iii) — hygiene checks and `content_skew.go` vs `dolt_schemas`

**Question.** With a throwaway migration file carrying the trigger DDL, do the
tree's migration hygiene checks (B: no `NOW()/UUID()/RAND()`; C:
frozen-once-merged git diff; D: the ignored-series twin; plus A and E), the
runner's own guards, and the content-skew comparison
(`schema_migrations.content_hash`) tolerate trigger DDL and the resulting
`dolt_schemas` rows — including the case where one clone has the trigger and
another does not?

**Setup.** The file is embedded from `testdata/` as its own
`migrationSource` (cursor table `schema_migrations`, like the main series), so
the **real** `runMigrations` applies it with `commitEachStep=true` — the
production path — without touching the series. Five tests:

1. `TestSpikeBeadGraphFenceRunnerAppliesTriggerDDL` — the runner end to end.
2. `TestSpikeBeadGraphFenceDirtyDoltSchemasGuards` — MigrateUp's dirty-table
   machinery (`dirtyTables`, `dirtyTableSignature`,
   `pendingMigrationDirtyTables`/`migrationSQLTouchesTable`,
   `changedDirtyTableSignatures`, `stageSchemaTables`) against `dolt_schemas`.
3. `TestSpikeBeadGraphFenceContentSkewAcrossClones` — `ReadMigrationContentHashes`
   + `ContentHashSkew` the way `bd doctor` runs them (local HEAD vs
   `AS OF 'remotes/origin/main'`), across a file-remote push, a server-side
   `DOLT_CLONE`, and a clone that applied the trigger-less variant.
4. `TestSpikeBeadGraphFenceHygieneScript` — `scripts/check-migration-hygiene.sh`
   in a scratch git repo mirroring the migration tree (base commit = the
   shipped tree, `BASE_SHA` set), the spike file added as a new main-plane
   migration, plus negative controls.
5. `TestSpikeBeadGraphFenceCLIBundleRoute` — the `AllMigrationsSQL()` →
   `dolt sql -f` route versus the runner's protocol route.

**Observed.**

- **Runner.** `runMigrations` applied 1; `schema_migrations.content_hash` for
  9001 equals `sha256` of the file bytes; three trigger rows in `dolt_schemas`
  **at HEAD** (the per-step commit staged `dolt_schemas` along with the table
  and the cursor: `dolt_log` has `schema: apply migration
  9001_beadgraph_fence_spike.up.sql`, `dolt_status` is empty); over the tree's
  DSN the fence refuses raw INSERT/UPDATE/DELETE and admits them under the
  variable; re-applying the text through `execMigrationBody` is a clean no-op
  (three triggers). The runner needed no `dolt_schemas`-specific code: it
  stages whatever `dolt_status` reports, which is why 0017's view has always
  worked.
- **Guards.** `dolt_status` reports `dolt_schemas`;
  `dolt_diff('HEAD', 'WORKING', 'dolt_schemas')` signs it without error.
  `migrationSQLTouchesTable(spike, "dolt_schemas")` is **false** (the regex
  knows CREATE/ALTER/DROP TABLE, indexes and views, not triggers), so with a
  pre-existing dirty `dolt_schemas` (an uncommitted hand-made trigger or view)
  `pendingMigrationDirtyTables` returns nothing, the pass runs, and
  `changedDirtyTableSignatures` reports `[dolt_schemas]` afterwards — i.e.
  MigrateUp fails at the **end** with `pre-existing dirty tables changed during
  schema migration: dolt_schemas`, after the per-step commits have already
  recorded the cursor while the trigger rows sit unstaged (the step commit
  skips tables that were dirty before). On a clean working set
  (`stageSchemaTables` with nothing dirty before) `dolt_schemas` is staged
  (`DOLT_ADD('-f', 'dolt_schemas')`) and the terminal commit lands the three
  triggers at HEAD.
- **Skew.** Clone A (real file) vs clone C (variant, same version, no
  triggers): `ContentHashSkew` = `[9001]` (`19ff8829c7fe…` vs `363f36997d54…`)
  — the #4259 fork is reported exactly as for any other content difference. A
  pushed to `file://…`; `CALL DOLT_CLONE` produced B with the three triggers
  replicated and refusing raw INSERT; `ReadMigrationContentHashes(B,
  "remotes/origin/main")` works and `ContentHashSkew` is empty. B then
  `DROP TRIGGER`ed all three and committed: hashes unchanged, **skew still
  empty**, raw INSERT on B passes; the only signal is `dolt_schemas` itself —
  0 trigger rows at B's HEAD vs 3 `AS OF 'remotes/origin/main'`.
- **Hygiene script** (bash 5.3.15): the trigger-carrying file → exit 0,
  `Migration hygiene OK.`, no check skipped. Controls: `NOW()` inside the
  trigger body → `FAIL (nondeterministic SQL)` (B); appending to
  `0001_create_issues.up.sql` → `FAIL (frozen migrations)` (C);
  `ALTER TABLE leases ADD COLUMN …` without a twin → `FAIL (ignored twin
  missing)` (D). Finding: a twin-less main-plane `CREATE TABLE
  graph_authority_lease …` **passes** — the script's `ignored_tables` list
  (`wisps|wisp_…|repo_mtimes|local_metadata|leases|events|bd_events_journal|bd_events_seq|ignored_schema_migrations`)
  does not know the B4 lease table, and neither does `schema.go`'s
  `doltIgnorePatterns`. E is not engaged (no PREPARE);
  `preparedALTERTableStatements(spike)` is empty and `cliCompatibleMigrationSQL`
  returns the text unchanged for an unknown name.
- **CLI bundle route.** `dolt sql -f` on the frozen text: exit 1,
  `error on line 18 … syntax error at position 217 near 'graph_beads_spike:
  out-of-role write refused'` — the CLI batch splitter cuts the body at its
  inner `;` (`-q` and piped stdin behave the same; probed by hand). A
  `DELIMITER //` rendition loads through `dolt sql -f` (three triggers). The
  same rendition over the MySQL protocol: `Error 1105 (HY000): syntax error at
  position 10 near 'DELIMITER'` — `DELIMITER` is a client-side command. The
  frozen text over the protocol creates the three triggers. So one file
  cannot serve both routes, and `TestAllMigrationsSQLAppliesThroughDoltCLIAndRecordsLatestVersion`
  would fail on a trigger-carrying series file without a
  `cliCompatibleMigrationSQL` override. The parity oracle
  (`internal/storage/dolt/schema_cli_parity_integration_test.go`,
  `committedSchemaSnapshotQueries`) filters `LEFT(table_name, 5) <> 'dolt_'`
  and compares tables/columns/indexes/constraints only, so a bundle that
  drops the triggers passes it silently.

**Verdict: PASS-WITH-RULE.** Trigger DDL and its `dolt_schemas` rows trip
nothing the tree enforces on migration files; the runner commits them; skew
detection behaves exactly as designed. The rules for the P1 migration PR:

1. `cli_migrations.go`: a `DELIMITER`-wrapped rendition of each of the five
   table files for the fresh bundle, plus a bundle test that counts the 24
   triggers (the parity oracle cannot see them). The B4 sentence "irrelevant
   to the runner" is right about the runner and wrong about the bundle; the
   bts release-parity gate (B4, cross-repo coupling) compares the runtime
   files' content hashes, which the override does not change.
2. `scripts/check-migration-hygiene.sh` `ignored_tables` and `schema.go`
   `doltIgnorePatterns` (version-gated, as `events` is) learn
   `graph_authority_lease` **before** the lease migration lands, or check D
   cannot enforce the ignored-series twin B4 requires.
3. `migrationSQLTouchesTable` learns trigger DDL as a touch of `dolt_schemas`,
   with the two statements handled separately because their grammars differ:
   `CREATE TRIGGER <name> … ON <table> FOR EACH ROW …` names its subject
   table in the statement and touches `dolt_schemas` **and** `<table>`;
   `DROP TRIGGER [IF EXISTS] <name>` has **no** `ON <table>` clause and
   touches `dolt_schemas` only — the subject table, if the guard wants it,
   comes from a metadata lookup (the `dolt_schemas` row for `<name>`, or the
   B4 naming convention `<table>_bi|_bu|_bd`), never from the statement. A
   single `CREATE|DROP TRIGGER … ON <table>` pattern would miss every
   standalone drop and keep the dirty-`dolt_schemas` gap open. With both
   handled, a pre-existing dirty `dolt_schemas` is refused up front as
   `DirtyTablesError`, like any other table, instead of after the per-step
   commits.
4. The fence's presence is not covered by `content_skew.go` and `dolt_schemas`
   is not one of the eight tables the state-change validator hashes or
   ruling 14's fetch-inspect reads: a foreign `DROP TRIGGER` replicates on
   pull/clone with no signal. The replication/merge ADR should add a **fence
   census** — the 24 expected `dolt_schemas` trigger rows, checked by the
   validator and by fetch-inspect against the tracking ref (with care:
   `ready_issues` and any future view live in the same table).

---

## What the evidence contradicts in the design docs

1. **B2, row (ii) sentence** — "A connection whose transaction is torn down by
   cancellation is closed by the driver, not pooled." False for cancellation
   between statements on both server legs (c1, d1); true only mid-statement
   (c3, d2). Replace with the WithoutCancel clear + poison rule.
2. **B4, "irrelevant to the runner"** — right for `execMigrationBody`, but
   `AllMigrationsSQL()` feeds `dolt sql -f`, and the parity oracle cannot see
   missing triggers. Needs the bundle override and a trigger count.
3. **B4, "(check D)" for the lease twin** — unenforceable today: the script's
   list and `doltIgnorePatterns` lack `graph_authority_lease`.
4. **B4, "(iii) … the tree has no `dolt_schemas` handling today"** —
   confirmed (no reference outside `engdocs/`), and that is fine for the
   runner; the gap is the guard regex (rule 3) and the validator/fetch-inspect
   surface (rule 4).
5. **B4, "(i) … creates them through go-mysql-server or skips them by rule"**
   — creates them; the rule branch is unused.
6. **Ruling 13's probes** — all re-confirmed on both DSN builders
   (`doltutil.ServerDSN` and `util.DoltServerDSN`) and on the embedded driver;
   the embedded driver reports the SIGNAL as `*mysql.MySQLError` 1644 like the
   server.

## Production changes this implies (none made here)

- `internal/storage/uow`: a way for a body (or the `Tx` wrapper) to poison the
  pinned connection when the deferred clear fails; the clear itself on
  `context.WithoutCancel`.
- `internal/storage/dolt/store.go` `withWriteTx`/`withRetryTx`: begin the
  transaction on `context.WithoutCancel(ctx)` (statements keep the caller's
  ctx) and pin `db.Conn` so the wrapper can clear and poison; or route the
  graph bodies through the UOW leg only.
- `internal/storage/schema/migration_repairs.go`/`schema.go`
  `migrationSQLTouchesTable`: trigger DDL patterns.
- `scripts/check-migration-hygiene.sh`, `schema.go` `doltIgnorePatterns`:
  `graph_authority_lease`.
- `internal/storage/schema/cli_migrations.go`: DELIMITER renditions for the
  five table files; a bundle trigger-count test.
- The replication/merge ADR: the fence census over `dolt_schemas`.


## Current-main Read contract refresh — 2026-09-10

This follow-up preserves the dated spike report above. The P0 lineage at
`ec692e146ad2a879d1c6819ecc8bf786597d617b` now includes current Beads main
`a690b0a8c4d1ddc4f0bd9bf767499625dd71bc96` through merge commit
`a12d44364263c34455ec8e7ef965ea7dfae0a93b`. No migration or Issue-plane
behavior was added by this follow-up.

The wire package is repinned to the merged BDP Read foundation
`19923f5bb6cc3f4ee4c508e36df3bd4c5c52344b`: six verbatim upstream files and
13 spec fences, with regenerated blob identities, SHA-256 digests and source
line ranges in `internal/httpapi/bdpwire/schema/PROVENANCE`. This is the
27-definition Read bundle, not the later Read+Update/Transactional bundle.
The 46 catalog rows and 46 matrix plans are vendored contract inputs, **not
46 Beads HTTP passes or a capability claim**. Later profile and shared HTTP
runtime integration remain separate work.

The DTO now separates the max-only wildcard from explicit declarations;
strict decoding rejects missing bounds, wildcard labels (including an empty
or null label), empty explicit labels and explicit bounds above the whole-set
wildcard bound. Constructed declarations validate before marshaling. Ordinary
RFC 9457 extensions still round-trip, while `resource-erased` rejects the
presence of `pointer`, regardless of its JSON value, through strict decoding,
`encoding/json`, validation and marshaling. Existing graphops numeric laws
and independent canonical vectors are unchanged; the old wildcard wire
tripwire is replaced by adoption checks.

Current-base verification uses Go 1.26.5, Dolt 2.1.8 and the unchanged
`.buildflags`. Focused DTO/schema parity/strict decoding/round-trip/provenance/
matrix-contract and graphops tests pass. `BDP_SPEC_AT_PIN` names the exact
pinned spec bytes when reconstructing all derived examples. `make api-check`
regenerates an unchanged OpenAPI artifact and passes HTTP package tests.
`make ci-pr-lint` checks native and Windows targets against current main.
`make test` passes with 99 package results and 40.1% aggregate coverage; its
hermetic wrapper intentionally skips real-Dolt tests by default, so this is
not a whole-suite no-skip claim. The final affected-package run passes 95
top-level tests (192 including subtests), with zero skips. The final bounded
key-pattern and schema probes were checked by that focused run and the API
gate after the broad run began. No unrelated full-suite rerun was needed.

All seven existing fence spikes were rerun on the merged base: pooled UOW
hygiene, the five schema/migration probes, and the embedded leg. The first
server/schema invocation deliberately left the embedded opt-in disabled;
that one skip was then executed successfully in its own opt-in invocation.
No final spike remained unexecuted. Commands:

```sh
BEADS_TEST_ENV_RUN_DOLT=1 TEST_RUN=TestSpikeBeadGraphFence TEST_VERBOSE=1 \
  ./scripts/test.sh ./internal/storage/uow ./internal/storage/schema ./internal/storage/embeddeddolt
BEADS_TEST_ENV_RUN_DOLT=1 BEADS_TEST_EMBEDDED_DOLT=1 \
  TEST_RUN=TestSpikeBeadGraphFenceEmbeddedLeg TEST_VERBOSE=1 \
  ./scripts/test.sh ./internal/storage/embeddeddolt
```

The observed hazards and P1 rules above remain: cancellation between
statements can return an uncleared server session to the pool; a confirmed
clear on `WithoutCancel` restores the fence; CLI trigger text needs the
DELIMITER rendition; and the migration/replication checks do not acquire a
trigger census merely by passing these probes. These observations do not
implement a graph writer, storage fence or HTTP server.
