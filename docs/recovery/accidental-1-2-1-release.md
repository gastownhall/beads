---
title: Accidental v1.2.1 Release
description: Recover a database migrated by the accidental, untested v1.2.1 release
---

v1.2.0 and v1.2.1 were published by accident on 2026-08-11, without release
testing. v1.2.2 superseded them by re-releasing the tested 1.1 line — it is
the v1.1.2 code under a higher version number, so every install channel
(Homebrew, npm, the install script, `go install`) moves forward onto tested
code. The 1.2.x-only features (work leases, the events journal, sync
federation, the HTTP API server, provenance events) are not in v1.2.2; they
will return in a properly tested release.

The catch: running the v1.2.1 binary even once (any command, including
`bd list`) migrated your local database schema from v53 to v65. The v1.2.2
binary speaks schema v53, so on such a database it stops with:

```
schema version mismatch: database is at v65, binary knows up to v53 (12 migrations ahead)
```

This guide fixes that. The v1.2.x schema changes were additive for the issue
data itself, but one of them was not additive for the audit trail: it moved
the `events` table off Dolt's versioned plane, and after the rollback the
1.1 line keeps writing audit rows into a local table that no longer versions
or syncs. Recovery therefore has two required parts — roll the schema cursor
back (steps 1–4), then restore the versioned audit table (step 5) — and a
database is not recovered until both are done: a store that skips step 5
looks healthy on the machine it runs on, but once published it hands every
future clone a database whose first issue write fails.

## Am I affected?

Only if you both ran v1.2.1 at least once **and** see the schema-mismatch
error above with v1.2.2 (or any 1.1.x binary). Users who upgraded but never
ran `bd`, and users whose workspace has a Dolt remote configured (the
remote-migrate gate blocked silent migration), are typically not affected.

You are also affected — without ever running v1.2.1 yourself — if you cloned
a database that was recovered without step 5: every read works, and the
first `bd create` or `bd import` fails with `Error 1146: table not found:
events`. Start at [A clone that is missing the events
table](#a-clone-that-is-missing-the-events-table) instead of here.

## Recommended fix: roll the schema cursor back

The v1.2.x migrations were written to be replay-safe after a cursor
rollback, so this is reversible and a later, properly tested 1.2.x upgrade
will work normally afterwards.

1. **Upgrade every machine and clone to v1.2.2 first.** A leftover v1.2.1
   binary that touches the database will silently re-migrate it.
2. Stop anything using the database: close running `bd` processes; in
   server mode also run `bd dolt stop`.
3. Take a backup copy of the workspace database:

   ```sh
   cp -a .beads .beads.backup-pre-recovery
   ```

   Keep it until step 6 passes. The batch in step 5 is not a transaction:
   a failure part-way through it may leave the working set changed, and the
   recovery for that is restoring this backup, not retrying on a
   half-changed database.

4. Roll the cursor back with the [Dolt CLI](https://docs.dolthub.com/cli-reference/cli)
   (any recent `dolt` release works; no `dolt config` setup is needed —
   the command carries its own author). The database directory is
   `.beads/embeddeddolt/<db>` (embedded mode, the default) or
   `.beads/dolt/<db>` (server mode):

   ```sh
   cd .beads/embeddeddolt/<db>
   dolt sql -q "DELETE FROM schema_migrations WHERE version > 53; CALL DOLT_ADD('schema_migrations'); CALL DOLT_COMMIT('-m', 'recovery: roll schema cursor back to v53 (accidental v1.2.1)', '--author', 'bd recovery <recovery@beads.invalid>')"
   ```

   (If this reports there is nothing to commit, the step was already
   done — safe to continue.)

5. **Restore the versioned events table (required).** One v1.2.x migration
   (the ignored-plane flip) dropped `events` from the versioned plane and
   re-created it as a dolt_ignored local table. The v1.2.2 line keeps
   writing audit rows to that local table, so this machine looks and works
   fine — but a dolt_ignored table does not replicate, and its re-created
   shape does not merge with pre-flip history. Publishing in that state
   makes every future clone write-dead: reads and `bd kv set` work, and the
   first `bd create` or `bd import` on the clone fails with `Error 1146:
   table not found: events`, because `bd create` and `bd import` record an
   audit event and the kv path does not. Restoration is part of completing
   recovery, not an optional extra: a strictly local store may appear
   healthy without it, but it does not qualify for publication or cloning
   until this step is done.

   The batch below restores the original versioned table from the flip
   commit's first parent (which still holds it, with its original column
   identity), keeps every row in your local table, and removes the ignore
   rule. Before running it, `dolt status` should show nothing staged: the
   commit at the end takes the whole staged set. Run it from the same
   database directory:

   ```sh
   dolt sql -q "SET @flip = (SELECT commit_hash FROM dolt_log
     WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)'
       AND EXISTS (SELECT 1 FROM dolt_ignore WHERE pattern = 'events')
       AND NOT EXISTS (SELECT 1 FROM dolt_log WHERE message = 'recovery: re-track events table')
     ORDER BY date LIMIT 1);
   SET @preflip = (SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = @flip AND parent_index = 0);
   CREATE TEMPORARY TABLE recovery_state (flip_commit_must_resolve VARCHAR(64) NOT NULL, its_parent_must_resolve VARCHAR(64) NOT NULL);
   INSERT INTO recovery_state VALUES (@flip, @preflip);
   SET @hist_rows = (SELECT COUNT(*) FROM events AS OF @preflip);
   CREATE TEMPORARY TABLE recovery_local (local_events_must_not_be_empty_when_history_has_rows INT NOT NULL);
   INSERT INTO recovery_local VALUES (IF(@hist_rows > 0, (SELECT COUNT(*) FROM events), 1));
   CREATE TABLE events_recovery_local AS
     SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at, id FROM events;
   DROP TABLE events;
   DELETE FROM dolt_ignore WHERE pattern = 'events';
   CALL DOLT_ADD('dolt_ignore');
   CALL DOLT_CHECKOUT(@preflip, '--', 'events');
   DELETE FROM events WHERE id NOT IN (SELECT id FROM events_recovery_local);
   INSERT IGNORE INTO events (issue_id, event_type, actor, old_value, new_value, comment, created_at, id)
     SELECT issue_id, event_type, actor, old_value, new_value, comment, created_at, id FROM events_recovery_local;
   DROP TABLE events_recovery_local;
   CALL DOLT_ADD('events');
   CALL DOLT_COMMIT('-m', 'recovery: restore versioned events table', '--author', 'bd recovery <recovery@beads.invalid>')"
   ```

   What the two guards refuse, and why:

   - The first guard resolves the flip commit by its exact frozen message
     and its first parent. If the batch stops there with a *non-nullable*
     error, this database never went through the flip (nothing to restore),
     restoration already completed (the ignore rule is gone), or the old
     optional re-track command already ran here (see [Already re-tracked
     events with the old optional
     step?](#already-re-tracked-events-with-the-old-optional-step)). If it
     stops with *table not found: events* instead, this clone never
     received the local table — use [A clone that is missing the events
     table](#a-clone-that-is-missing-the-events-table).
   - The second guard runs before anything destructive and refuses when
     your local `events` table is empty while history holds rows: an empty
     local table cannot prove those historical rows were deliberately
     deleted, so the recipe stops rather than delete them. A confirmed
     empty or missing local table has its own variant in that same section.
   - If you re-created `events` by hand — an empty table from a DDL
     snippet, or a partial reload from a backup — do not run this batch:
     its reconciliation treats rows that exist in history but not in your
     table as deleted, and an incomplete hand-made table would erase
     historical audit rows. Stop here, keep the backup, and ask for help.

   Two things to know about the outcome:

   - The restored table keeps its original versioned identity on purpose.
     The migration's re-created local table has a different column order,
     and a database that merges the two shapes fails with `different
     primary keys`. Restoring the original identity is what lets recovered
     machines merge with pre-flip teammates (see the next section).
   - The row outcome is deliberate: the versioned table ends up holding
     your local rows (the migration carried the pre-flip rows into the
     local table, and the 1.1 line has added to them since). Rows that
     exist only in history — for example the events of issues deleted
     after the flip — are deleted by the reconciliation, not recovered.
   - A failure before `DROP TABLE events` changed nothing (the guards run
     first); a failure after it may have left the working set half-changed.
     Restore the `.beads.backup-pre-recovery` copy and retry — do not
     re-run the batch on a half-changed database.

6. **Verify, then run `bd`.** Confirm the recovery landed (from the same
   database directory):

   ```sh
   dolt sql -q "SELECT MAX(version) AS schema_version FROM schema_migrations"
   dolt sql -q "SELECT COUNT(*) AS events_at_head FROM events AS OF 'HEAD'"
   dolt sql -q "SELECT COUNT(*) AS events_ignore_rules FROM dolt_ignore WHERE pattern = 'events'"
   dolt sql -q "SELECT table_name FROM dolt_status"
   ```

   Expect `53`, at least `1`, `0`, and no rows. Do not treat reads or a
   `bd kv set` as proof of recovery: `bd status`, `bd ready` and `bd show`
   all work on a store that cannot record a single issue change, and the
   kv path writes no audit event. The check that proves it is a real issue
   write — `bd create "recovery smoke test" -t chore -p 4` — which is
   exactly the operation that fails with `Error 1146` on an unrecovered
   clone.

   Then run any `bd` command from the workspace. It should work with no
   warnings and no `BD_IGNORE_SCHEMA_SKEW` needed.

This also works for databases **created** by v1.2.1 (not just upgraded
ones): the v65 schema is a superset of everything the 1.1 line needs, and a
created database ran the flip too, so step 5 finds the history it needs.

## Shared databases: recover every machine, then sync

If you push/pull issue data with teammates, both the migrated cursor and
the un-restored events state replicate, so the order matters:

1. Pick the machine whose local audit rows you care about most (the one
   that has been writing since the migration). It completes steps 1–6,
   including step 5, and pushes. Its recovery commit publishes the
   restored table for everyone else.
2. Every other machine that ran v1.2.1 also completes steps 1–6 **before
   it pulls**. Merging a recovered database with your own un-recovered
   local table loses those local rows to the merge's events handling, and
   the schema skew refuses the pull outright. Two recovered databases
   merge cleanly: both restored the same original table identity, so their
   events merge row-wise (rows only in history are gone on both sides).
3. A clone or teammate that never ran v1.2.1 pulls normally once the
   recovered history is pushed. Nothing special to do: the restored table
   has the identity their pre-flip history already knows.

Current bd releases re-create a missing `events` table automatically on
open. That is background context, not a shortcut: running a newer binary
against this database migrates it forward again, which is exactly what you
are recovering from. Stay on v1.2.2 until you deliberately upgrade the
whole team.

## A clone that is missing the events table

Symptom: `bd status`, `bd ready`, `bd show` and even `bd kv set` work, but
the first `bd create` or `bd import` fails:

```
Error: failed to record event for <db>-xxxxxx: record event in events: Error 1146: table not found: events
```

Reads cannot surface this — the audit write only happens on issue changes —
and the error text names a Dolt table and a MySQL error number, which
reads like corruption rather than an unfinished recovery on the source.
This clone inherited a database that was recovered without step 5. Two
repairs, in order of preference:

1. **Repair the source, then pull.** On the machine that published the
   database, complete steps 1–6 (it has the local `events` table and the
   flip in its own history), push, then pull here **before your first
   issue write**. The pull brings the restored versioned table; there is
   nothing else to do locally. If the source's cursor is still above 53,
   its step 4 rollback lands before this pull, so the clone arrives at 53
   with it.
2. **No machine has a local table, or it is confirmed empty: restore
   history only.** This includes the case of re-creating `events` by hand
   from a DDL snippet and leaving it empty. The variant below verifies the
   local table is absent or empty and refuses otherwise, restores the
   original table from history keeping only the rows whose issues still
   exist, and removes the ignore rule. Run it on one machine, push, and
   have peers pull:

   ```sh
   dolt sql -q "SET @flip = (SELECT commit_hash FROM dolt_log
     WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)'
       AND EXISTS (SELECT 1 FROM dolt_ignore WHERE pattern = 'events')
       AND NOT EXISTS (SELECT 1 FROM dolt_log WHERE message = 'recovery: re-track events table')
     ORDER BY date LIMIT 1);
   SET @preflip = (SELECT parent_hash FROM dolt_commit_ancestors WHERE commit_hash = @flip AND parent_index = 0);
   CREATE TEMPORARY TABLE recovery_state (flip_commit_must_resolve VARCHAR(64) NOT NULL, its_parent_must_resolve VARCHAR(64) NOT NULL);
   INSERT INTO recovery_state VALUES (@flip, @preflip);
   SET @have_events = (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'events');
   SET @local_rows_sql = IF(@have_events = 1, 'SELECT COUNT(*) INTO @local_rows FROM events', 'SET @local_rows = 0');
   PREPARE local_count FROM @local_rows_sql; EXECUTE local_count; DEALLOCATE PREPARE local_count;
   CREATE TEMPORARY TABLE recovery_empty (local_events_must_be_absent_or_empty INT NOT NULL);
   INSERT INTO recovery_empty VALUES (IF(@local_rows = 0, 1, NULL));
   DROP TABLE IF EXISTS events;
   DELETE FROM dolt_ignore WHERE pattern = 'events';
   CALL DOLT_ADD('dolt_ignore');
   CALL DOLT_CHECKOUT(@preflip, '--', 'events');
   DELETE FROM events WHERE issue_id NOT IN (SELECT id FROM issues);
   CALL DOLT_ADD('events');
   CALL DOLT_COMMIT('-m', 'recovery: restore events table from history', '--author', 'bd recovery <recovery@beads.invalid>')"
   ```

   The result is the historical rows whose issue ids still exist — not an
   empty audit table, and not rows for deleted issues. Audit rows recorded
   after the flip exist in no versioned history and no clone: if every
   machine's local table is gone, those rows are gone; recover them from a
   `.beads` backup if one exists. If instead the local table is non-empty
   but incomplete (a partial reload), stop: preserve the database and the
   backup and ask for help — this variant refuses to run, and that refusal
   is correct.

## Already re-tracked events with the old optional step?

An earlier version of this guide made restoration optional and re-tracked
the table with a single command. That command force-added the migration's
re-created table while leaving the `events` ignore rule committed at HEAD:
a tracked-but-ignored table, which every later write dirties and no
ordinary sync can commit — pushes keep working, pulls refuse, and the
dirty state replicates to clones. If your history holds a commit titled
`recovery: re-track events table`, finish the re-tracking with the command
below — on the source first, then on every affected clone, each before it
pulls:

```sh
dolt sql -q "DELETE FROM dolt_ignore WHERE pattern = 'events'; CALL DOLT_ADD('dolt_ignore'); CALL DOLT_ADD('-f', 'events'); CALL DOLT_COMMIT('--skip-empty', '-m', 'recovery: finish re-tracking events', '--author', 'bd recovery <recovery@beads.invalid>')"
```

After both sides have run it, push and pull normally. This keeps the table
identity already published, so pre-flip history still does not merge with
it; if a pull reports `different primary keys`, stop there — preserve the
workspace and the backup and ask for help. Automatic repair ends at that
point by design.

## Stopgap: keep working before you recover

If you need `bd` this minute, the skew guard has an escape hatch:

```sh
BD_IGNORE_SCHEMA_SKEW=1 bd <command>
```

This has been verified against the exact v53-binary/v65-database
combination: reads are identical and writes work, because the v1.2.x
schema additions are invisible to the 1.1 line. Two cautions. Audit-event
versioning stays paused until step 5 restores it, and issue writes record
their audit rows only into the local unversioned table — so treat this as
a stopgap, not a destination. And do not read success here as recovery:
`bd status` and `bd kv set` succeeding says nothing about whether issue
writes or future clones work; only the steps above, verified with a real
issue write, do.

## What recovery leaves behind

Data the accidental release wrote into 1.2.x-only structures stays in the
database but is unused by the 1.1 line: work-lease state (ephemeral,
5-minute horizon), events-journal rows (feature was off by default),
provenance rows (only written by an explicit new command), and
`storage_class` markers. None of it blocks a future 1.2.x upgrade, which
will simply resume using it.

## Alternative: full rollback via Dolt history

If you want the database history itself restored to its pre-migration
state (the cursor rollback keeps the migration commits in history), the
v1.2.1 migrator made one labeled Dolt commit per migration
(`schema: apply migration 0054_...` through `0065_...`), so the
pre-migration commit is easy to find. The safe sequence is: export with
the v1.2.1 binary (`bd export --all -o backup.jsonl`), stop everything and
copy `.beads` aside, `dolt reset --hard <pre-migration-commit>` in the
database directory, install v1.2.2, then `bd import backup.jsonl`.
Caveats: issues deleted after the upgrade come back (import cannot
re-delete), and audit events recorded while on v1.2.1 are lost. Most users
should prefer the cursor rollback above.

To rebuild a 1.1.2-compatible workspace from a full export instead, keeping
the migrated original aside, see
[Un-migrate from Schema v65](/recovery/unmigrate-schema-v65).
