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

This guide fixes that. Your issues are all still there, but the migration
did take one table out of Dolt's versioned history: the `events` audit table,
which every `bd create`, `bd update` and `bd import` writes to. Recovery
rolls the schema cursor back and puts `events` back under version control.
Both steps are required; the second is what lets clones of the database
take writes again.

## Am I affected?

Only if you both ran v1.2.1 at least once **and** see the schema-mismatch
error above with v1.2.2 (or any 1.1.x binary). Users who upgraded but never
ran `bd`, and users whose workspace has a Dolt remote configured (the
remote-migrate gate blocked silent migration), are typically not affected.

You are also affected if a clone fails its first issue write with:

```
Error: failed to record event for <id>: record event in events: Error 1146: table not found: events
```

That clone came from a database that was recovered without restoring
`events`; see [If `events` is missing or empty](#if-events-is-missing-or-empty).

## Recommended fix: roll the schema cursor back

The v1.2.x migrations were written to be replay-safe after a cursor
rollback, so this is reversible and a later, properly tested 1.2.x upgrade
will work normally afterwards.

1. **Upgrade every machine and clone to v1.2.2 first.** A leftover v1.2.1
   binary that touches the database will silently re-migrate it.
2. Stop anything using the database: close running `bd` processes; in
   server mode also run `bd dolt stop`.
3. Take a backup copy of the workspace database, and keep it until every
   step below has succeeded:

   ```sh
   cp -a .beads .beads.backup-pre-recovery
   ```

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

5. **Restore the versioned `events` table.** v1.2.1 committed the removal
   of `events` from versioned history and kept the table only on this
   machine, which v1.2.2 cannot do for a clone. From the same database
   directory, run this batch; it puts back the table v1.2.1 removed, with
   its original schema, and fills it with this machine's audit rows:

   ```sh
   dolt sql <<'SQL'
   -- The commit in which v1.2.1 removed events from versioned history.
   SET @flip = (SELECT commit_hash FROM dolt_log
     WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)'
       AND EXISTS (SELECT 1 FROM dolt_ignore WHERE pattern = 'events')
       AND NOT EXISTS (SELECT 1 FROM dolt_log WHERE message = 'recovery: re-track events table')
     ORDER BY date LIMIT 1);
   SET @preflip = CONCAT(@flip, '~1');
   -- Checks. Each one stops the batch before anything changes.
   CREATE TEMPORARY TABLE recovery_check_history (events_history_found VARCHAR(64) NOT NULL);
   INSERT INTO recovery_check_history VALUES (@flip);
   CREATE TEMPORARY TABLE recovery_check_staged (nothing_else_staged INT NOT NULL);
   INSERT INTO recovery_check_staged SELECT IF(COUNT(*) = 0, 1, NULL) FROM dolt_status WHERE staged = 1;
   CREATE TEMPORARY TABLE recovery_check_empty (local_events_not_empty INT NOT NULL);
   INSERT INTO recovery_check_empty SELECT IF(COUNT(*) = 0 AND (SELECT COUNT(*) FROM events AS OF @preflip) > 0, NULL, 1) FROM events;
   CREATE TEMPORARY TABLE recovery_check_carried (local_events_carry_history INT NOT NULL);
   INSERT INTO recovery_check_carried SELECT IF(COUNT(*) = 0, 1, NULL) FROM events AS OF @preflip AS h
     WHERE h.issue_id IN (SELECT id FROM issues) AND h.id NOT IN (SELECT id FROM events);
   -- Restore: keep this machine's rows, put the historical table back.
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
   CALL DOLT_COMMIT('-m', 'recovery: restore versioned events table', '--author', 'bd recovery <recovery@beads.invalid>');
   SQL
   ```

   Afterwards `events` holds exactly this machine's audit rows. Historical
   rows this machine no longer has, the events of issues deleted since the
   upgrade, are deleted, not recovered.

   If a check fails, the batch stops with an error naming it, before any
   change:

   - `events_history_found`: there is no v1.2.1 removal to undo here. Either
     this step already succeeded (step 6 confirms it), the database never ran
     the v1.2.1 migration, its history no longer reaches that commit, or the
     old re-track command from an earlier version of this guide already ran
     ([finish that instead](#if-you-already-ran-the-old-re-track-command)).
     Do not continue to step 7 until step 6 passes.
   - `nothing_else_staged`: something else is staged and the commit would
     sweep it in. Commit or unstage it first.
   - `local_events_not_empty` or `table not found: events`: this machine
     has no audit rows to keep. See
     [If `events` is missing or empty](#if-events-is-missing-or-empty).
   - `local_events_carry_history`: this machine's `events` table lacks
     audit rows that the history still has for existing issues, so it was not
     carried over by the migration (for example, it was recreated by hand).
     The batch cannot tell which rows were meant to go, so it stops rather
     than delete them. Keep the backup and
     [open an issue](https://github.com/gastownhall/beads/issues).

   The batch is not a transaction. An error after the checks (a crash, a
   full disk, a Dolt error from the restore itself) can leave the database
   half-changed: restore the untouched backup from step 3 before you try
   again.

6. Verify. Every line must succeed; a check that fails names itself:

   ```sh
   dolt sql <<'SQL'
   SELECT COUNT(*) AS events_rows_in_head FROM events AS OF 'HEAD';
   CREATE TEMPORARY TABLE recovery_verify (cursor_at_53 INT NOT NULL, no_events_ignore_rule_in_head INT NOT NULL, nothing_pending INT NOT NULL);
   INSERT INTO recovery_verify VALUES (
     IF((SELECT MAX(version) FROM schema_migrations) = 53, 1, NULL),
     IF((SELECT COUNT(*) FROM dolt_ignore AS OF 'HEAD' WHERE pattern = 'events') = 0, 1, NULL),
     IF((SELECT COUNT(*) FROM dolt_status WHERE table_name IN ('events', 'dolt_ignore', 'schema_migrations')) = 0, 1, NULL));
   SQL
   ```

7. Run any `bd` command from the workspace. It should work with no
   warnings and no `BD_IGNORE_SCHEMA_SKEW` needed.

This also works for databases **created** by v1.2.1 (not just upgraded
ones): the v65 schema is a superset of everything the 1.1 line needs.

Do not stop after step 4. The recovered machine works either way: it keeps
writing audit rows to its own, unversioned `events` table. But that table
never reaches the remote, and v1.2.2 has no way to recreate it, so every
clone of a database pushed in that state can read but not write issues:
`bd status`, `bd list`, `bd show` and even `bd kv set` succeed, while the
first `bd create` or `bd import` fails with `table not found: events`. A
smoke test of reads or `bd kv set` does not show that recovery is complete;
step 6 does.

### Machines that share the database

The migrated cursor and the removal of `events` both replicate, so the
order matters when you push and pull issue data with teammates or other
machines:

1. Recover the machine that holds the audit rows written since the upgrade
   (steps 2 to 7), then `bd dolt push`.
2. Every other machine that ran v1.2.1 itself recovers on its own, steps 2
   to 7, **before** it pulls; then `bd dolt pull` and `bd dolt push`. A
   machine that pulls first fails the pull, because the pull would overwrite
   its local `events` table or because its schema is ahead.
3. Machines that never ran v1.2.1 just `bd dolt pull`.

A machine that got the migration only by pulling it has no local `events`
table; follow [If `events` is missing or empty](#if-events-is-missing-or-empty).

### If `events` is missing or empty

A clone of a database pushed after v1.2.1 has no `events` table: the remote
does not have one, and v1.2.2 does not create it. Step 5 stops on such a
machine with `table not found: events`, which is expected; do not write
issues to it until it is fixed. There are two ways:

- **Another machine still has the audit rows.** Recover that machine first
  (steps 2 to 7) and push. On this machine, take the backup (step 3), run
  step 4 if `bd` still reports a schema mismatch, then `bd dolt pull`
  before any issue write.
- **No machine has them** (every copy lacks the table, or has an empty one,
  such as the empty table the issue report's workaround creates). Restore
  the table from history on **one** machine with this batch, from the
  database directory after steps 2 to 4, then push; every other machine
  pulls.

  ```sh
  dolt sql <<'SQL'
  SET @flip = (SELECT commit_hash FROM dolt_log
    WHERE message = 'schema: drop versioned events table for ignored-plane flip (bd-red8u)'
      AND EXISTS (SELECT 1 FROM dolt_ignore WHERE pattern = 'events')
      AND NOT EXISTS (SELECT 1 FROM dolt_log WHERE message = 'recovery: re-track events table')
    ORDER BY date LIMIT 1);
  SET @preflip = CONCAT(@flip, '~1');
  -- Checks. Each one stops the batch before anything changes.
  CREATE TEMPORARY TABLE recovery_check_history (events_history_found VARCHAR(64) NOT NULL);
  INSERT INTO recovery_check_history VALUES (@flip);
  CREATE TEMPORARY TABLE recovery_check_staged (nothing_else_staged INT NOT NULL);
  INSERT INTO recovery_check_staged SELECT IF(COUNT(*) = 0, 1, NULL) FROM dolt_status WHERE staged = 1;
  SET @present = (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE() AND table_name = 'events');
  SET @probe = IF(@present = 0, 'SET @local_rows = 0', 'SET @local_rows = (SELECT COUNT(*) FROM events)');
  PREPARE probe FROM @probe;
  EXECUTE probe;
  DEALLOCATE PREPARE probe;
  CREATE TEMPORARY TABLE recovery_check_local (local_events_absent_or_empty INT NOT NULL);
  INSERT INTO recovery_check_local VALUES (IF(@local_rows = 0, 1, NULL));
  -- Restore the historical table, minus the events of deleted issues.
  DROP TABLE IF EXISTS events;
  DELETE FROM dolt_ignore WHERE pattern = 'events';
  CALL DOLT_ADD('dolt_ignore');
  CALL DOLT_CHECKOUT(@preflip, '--', 'events');
  DELETE FROM events WHERE issue_id NOT IN (SELECT id FROM issues);
  CALL DOLT_ADD('events');
  CALL DOLT_COMMIT('-m', 'recovery: restore versioned events table from history', '--author', 'bd recovery <recovery@beads.invalid>');
  SQL
  ```

  The table comes back with the audit rows from before the upgrade, for
  every issue that still exists. Audit rows written after the upgrade that
  no machine or backup still holds are not in the history and cannot be
  reconstructed. The batch refuses (`local_events_absent_or_empty`) a
  machine whose `events` table has rows; use step 5 there instead. Then run
  step 6 to verify.

Newer bd releases recreate a missing `events` table on their own. Do not
run one briefly to fix a clone and then go back to v1.2.2: it migrates the
database again.

### If you already ran the old re-track command

Earlier versions of this guide ended with an optional step that re-tracked
`events` with `CALL DOLT_ADD('-f', 'events')` but never staged the removal
of the ignore rule. A database pushed after it carries `events` in history
together with the rule that ignores it, so bd's own commits skip the table
and every write leaves a pending `events` change behind that blocks the next
pull. Finish the re-track from the database directory, on the machine that
ran it first and then on each clone, before any of them pulls:

```sh
dolt sql -q "DELETE FROM dolt_ignore WHERE pattern = 'events'; CALL DOLT_ADD('dolt_ignore'); CALL DOLT_ADD('-f', 'events'); CALL DOLT_COMMIT('--skip-empty', '-m', 'recovery: finish re-tracking events', '--author', 'bd recovery <recovery@beads.invalid>')"
```

Then push from the first machine and pull everywhere else. Step 5 refuses
these databases (`events_history_found`) because they already have a
versioned `events` table.

The old command re-tracked the table v1.2.1 recreated, not the original one,
so a pull between such a machine and one that still has the original table
can fail with `different primary keys`. This guide has no automatic repair
for that: keep the backup and the failing workspace, and
[open an issue](https://github.com/gastownhall/beads/issues).

## Stopgap: keep working before you recover

If you need `bd` this minute, the skew guard has an escape hatch:

```sh
BD_IGNORE_SCHEMA_SKEW=1 bd <command>
```

This has been verified against the exact v53-binary/v65-database
combination: on this machine reads are identical and writes work, because
the v1.2.x schema additions are invisible to the 1.1 line. It does not
restore `events` versioning, so the database is not fit to be pushed and
cloned yet, and reads or `bd kv set` working is no sign that it is. Treat
it as a stopgap, not a destination, and do the full recovery above.

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
