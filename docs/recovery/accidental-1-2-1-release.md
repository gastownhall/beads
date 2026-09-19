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

This guide fixes that. The v1.2.x schema changes are strictly additive —
nothing the 1.1 line reads or writes was dropped, renamed, or narrowed — so
recovery is a two-minute metadata fix, not a data migration.

## Am I affected?

Only if you both ran v1.2.1 at least once **and** see the schema-mismatch
error above with v1.2.2 (or any 1.1.x binary). Users who upgraded but never
ran `bd`, and users whose workspace has a Dolt remote configured (the
remote-migrate gate blocked silent migration), are typically not affected.

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

5. Run any `bd` command from the workspace. It should work with no
   warnings and no `BD_IGNORE_SCHEMA_SKEW` needed.

This also works for databases **created** by v1.2.1 (not just upgraded
ones): the v65 schema is a superset of everything the 1.1 line needs.

If you push/pull issue data with teammates, note that the migrated cursor
replicates: either recover every clone, or recover one and push, then have
the others pull.

### Optional: restore audit-event versioning

One v1.2.x migration moved the `events` audit table off Dolt's versioned
plane, and after the cursor rollback the 1.1 line keeps writing audit
events without versioning or syncing them (everything else syncs
normally). If you rely on the versioned audit trail, re-track the table
from the same database directory:

```sh
dolt sql -q "DELETE FROM dolt_ignore WHERE pattern = 'events'; CALL DOLT_ADD('-f', 'events'); CALL DOLT_COMMIT('-m', 'recovery: re-track events table', '--author', 'bd recovery <recovery@beads.invalid>')"
```

## Stopgap: keep working before you recover

If you need `bd` this minute, the skew guard has an escape hatch:

```sh
BD_IGNORE_SCHEMA_SKEW=1 bd <command>
```

This has been verified against the exact v53-binary/v65-database
combination: reads are identical and writes work, because the v1.2.x
schema additions are invisible to the 1.1 line. Audit-event versioning is
paused (see above) until you do the cursor rollback, so treat this as a
stopgap, not a destination.

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
