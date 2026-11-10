---
title: Schema-Encoding Drift
description: Preserve and recover a database whose schema migration stopped on a stored value the engine cannot read
---

This runbook is for a schema migration that bd refused because it stopped on a
stored value the Dolt engine cannot read. The error is consistent with
schema-encoding drift: text values written under an older Dolt that the
current engine cannot decode
([dolthub/dolt#11131](https://github.com/dolthub/dolt/issues/11131)). There is
no flag that skips the migration and no automatic repair. Recovery is
supervised by an operator and starts from a preserved copy.

## Symptoms

The first command after an upgrade, or an explicit `bd migrate schema`, exits
1 with a refusal that names the migration it stopped in:

```
schema migration 0035_migrate_infra_to_wisps.up.sql (schema_migrations v35) stopped: a stored value could not be read

  Symptom: malformed out-of-band adaptive value: invalid address length: expected 20-byte address, got 19
```

The symptom is one of the engine errors `invalid hash length` or
`malformed out-of-band adaptive value`. With `--json`, the same refusal is a
JSON error on stderr whose `schema_encoding_drift` object carries
`migration`, `version`, `cursor_source`, `symptom` and `recovery_url`. It
sits beside `error` and `hint`, inside `data` when `BD_JSON_ENVELOPE=1`.

The reported case
([gastownhall/beads#5699](https://github.com/gastownhall/beads/issues/5699))
is migration 0035 on a store whose migration cursor predates it: 0035 stops
while it copies `events` rows. The refusal covers every numbered migration in
both migration series.

## Before any further writes

The migration stopped on its first error and nothing was rolled back. Earlier
migrations in the same pass may have completed, and partial changes may
remain from the one that stopped. Leave the store as it is:

1. Stop every bd process, agent and script that uses the store.
2. Preserve a full copy of the store and its configuration with the
   [whole-store preservation procedure](/recovery/database-corruption#2-preserve-the-damaged-store),
   keep the preserved copy outside any server `data_dir`, and keep the
   original untouched as your fallback. The store is not always inside
   `.beads`: `bd context --json` shows where it lives.
3. Do not commit the working set, do not delete rows, and do not drop or
   reset tables to get past the error. Each of these can make unreadable
   values permanent or discard the evidence a recovery needs.
4. A later run can stop before it reaches the migration, with
   `pending schema migrations alter pre-existing dirty tables: <tables>; run 'bd dolt commit' ...`
   instead of this refusal. Those tables hold the stopped migration's partial
   changes: for 0059, `__bd_0059_recompute_wisp_deps`,
   `__bd_0059_recompute_wisps` and, once its reset ran, `issues`. Do not run
   `bd dolt commit` on this store. It would record the half-applied migration
   in history, and a push would publish it.
5. Do not push from this store or pull into it until it is recovered.

Work only on the copy from here on, and make a fresh copy before each attempt.

## Diagnosis

The refusal tells you which migration stopped and with what engine error. It
does not tell you which table or row holds the unreadable value. The candidate
tables are the ones that migration reads: the `FROM`, `JOIN` and `ALTER TABLE`
targets in the file it names, which you can find among the
[migration files](https://github.com/gastownhall/beads/tree/main/internal/storage/schema/migrations)
(the `ignored/` directory holds the `ignored_schema_migrations` series). Treat
them as candidate tables, not confirmed corruption locations. For example:

- **0035, the reported case:** it copies infra rows into the wisp tables, and
  reads `issues`, `labels`, `dependencies`, `events` and `comments` to do it.
  The reported store stopped while 0035 copied `events` rows into
  `wisp_events`, and `events` is where its unreadable text lives. Migrations
  0057 and 0062 rewrite `events` as well.
- **0059:** it recomputes `is_blocked`, and reads `issues`, `dependencies`,
  `wisps` and `wisp_dependencies`.

The wisp tables (`wisps` and every `wisp_*` table) are clone-local: bd keeps
them out of Dolt history. From migration 0062 on, `events` is clone-local
too, and history keeps only the `events` rows written before it.

bd recognizes drift only by the error text the engine returns. It cannot
detect readable garbage: a value that decodes without an error but holds the
wrong content passes every read and every migration. A clean migration on a
recovered copy is therefore not proof that the data is correct (see
[Validate before production use](#validate-before-production-use)).

## Recovery options

Neither option below is a bd command or a supported repair, and neither can
guarantee that every value survives. Choose with the people who own the data,
and run every step on a preserved copy.

### Restore from readable history

The [reported procedure](https://github.com/gastownhall/beads/issues/4380#issuecomment-4899301060)
uses stock Dolt history. On the copy, find an earlier commit at which the
candidate tables read cleanly. Readability needs a full-value read of each
table, for example `SELECT * FROM events AS OF '<commit>'`, and the same for
every other candidate table: a `COUNT(*)` can succeed without decoding a
single stored value, so it proves nothing here. Then restore an appropriate
table root from that version and reconcile it against current data.

History holds only versioned tables, so this route cannot restore a
clone-local table: not the wisp tables, and not `events` rows written after
migration 0062. If the unreadable value is in one of those, this route does
not apply.

History restores an older version, so account for:

- **Deleted rows:** rows deleted since that version come back unless you
  remove them again.
- **Rows created since:** rows created after that version are missing from
  the restored table and must be carried over from current data, where they
  may be the unreadable ones.
- **Schema differences:** the older version may predate columns or tables
  that later migrations added, so the restored table may not match the schema
  the migration cursor expects.

A version that reads cleanly cannot guarantee that it holds the latest value
of each row. Expect to lose or re-enter edits made after that version, and
record what you reconciled.

### Dolt schema repair tool (experimental)

[dolthub/dolt PR 11133](https://github.com/dolthub/dolt/pull/11133) proposed
an experimental schema repair tool. As of 2026-11-08 it is closed and
unmerged, titled DO NOT MERGE, on branch `zachmu/schema-repair-tool`. Treat it
as a historical experimental route, not a released or supported tool, and do
not infer from its state whether a replacement exists. Verify current upstream
guidance with the Dolt maintainers before running any version of it.

Reports from its use are useful context, not a guarantee:
[validation of a repaired copy](https://github.com/gastownhall/beads/issues/4380#issuecomment-4898979147)
and [the limits of sample-based checks](https://github.com/gastownhall/beads/issues/4380#issuecomment-4899140648).

## Validate before production use

Validate the recovered copy before it replaces anything:

1. **Full reads:** read every row and every column of every relevant table,
   not a sample: every candidate table of the stopped migration, and at least
   `issues`, `dependencies`, `labels`, `events`, `comments`, `wisps` and every
   `wisp_*` table, with full-value queries such as `SELECT * FROM events` on
   the copy. bd commands such as `bd list` or `bd export` do not read every
   row of every table, so they are not this check. Any read error, including
   either drift symptom, means the copy is not recovered.
2. **Semantics:** separately, check issue and dependency semantics against
   what you expect, with bd pointed at the copy (`BEADS_DIR` set to the copy's
   `.beads`): issue counts by status, the dependency graph (`bd blocked`,
   `bd ready`), and the rows you reconciled by hand. Sampling alone is not a
   full validation.
3. **Migration:** rerun the migration on the copy with `bd migrate schema` and
   confirm it completes. On a remote-backed store, follow the
   [designated migrator flow](/getting-started/upgrading#remote-backed-databases-and-multiple-clones)
   before publishing.
4. **After migrating:** run `bd recompute-blocked --json` (see
   [Repair blocked flags after migrating](/getting-started/upgrading#repair-blocked-flags-after-migrating)).

Only then switch to the recovered copy, keeping the preserved original until
you are sure nothing is missing.

## What this refusal does not cover

The refusal converts a returned error from a migration into this diagnosis.
A raw engine panic is a different path: it does not reach bd as a returned
error, so bd prints the panic instead of this refusal and does not intercept
it. This release improves the diagnosis of returned errors and does not make
every embedded panic path safe. If a command panics with
`invalid hash length`, the same preservation steps apply: stop, preserve a
full copy, and do not write to the store.
