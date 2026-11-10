---
title: Schema Encoding Drift
description: Recover a database whose migration stops on the schema-encoding-drift refusal (dolthub/dolt#11131)
---

A database whose schema cursor predates migration 0059 can hit a Dolt engine
bug class called **schema encoding drift**
([dolthub/dolt#11131](https://github.com/dolthub/dolt/issues/11131)): a TEXT
or LONGTEXT column's on-disk storage encoding was re-derived without
rewriting the rows beneath it, so the engine can no longer read some stored
values. When that happens mid-migration, `bd` stops the pass and refuses to
continue on its own. This runbook is the operator-supervised recovery it
points at.

## Symptoms

The first command that opens the database with pending migrations — or an
explicit `bd migrate schema` — stops with a refusal like:

```
Schema encoding drift (dolthub/dolt#11131) stopped migration
0059_recompute_null_gate_is_blocked.up.sql on schema_migrations.

  Engine symptom: Error 1105 (HY000): panic recovered: invalid hash length: 19
  ...
  Recovery is operator-supervised, never automatic:
        https://beads.gascity.com/recovery/schema-encoding-drift
```

The engine symptom is one of two lines: `invalid hash length` (server mode,
where the engine recovers the panic per query), or `malformed out-of-band
adaptive value` (embedded mode). Under `--json` the refusal carries a
`schema_encoding_drift` object with the migration, version, cursor table,
symptom, and this page's URL.

The same drift also has a silent form: drifted cells can decode to garbage
without erroring at all
([#4380](https://github.com/gastownhall/beads/issues/4380)). A database that
migrated "successfully" can still hold unread values, so a validation pass
below is worthwhile on any store known to have drifted.

## What happened

Migration 0059 recomputes blocked state, and to do that it reads `issues`,
`dependencies`, `wisps` and `wisp_dependencies`. Those are the *candidate*
tables, not confirmed corruption locations: the refusal names the error class
and the migration that tripped over it, not a specific corrupted table.
Which tables actually carry drifted values is a question the recovery below
answers per table.

`bd` refuses instead of skipping the unreadable rows, because a migration
that continues past unreadable values leaves itself half-applied with
silently wrong results. Recovery is by design not automatic.

## Stop and preserve first

1. Do not retry the migration, delete rows, or commit partial results. The
   migration stopped mid-step; statements that already ran may have changed
   the database, and partial changes may remain.
2. Stop every `bd` process, agent and script that uses the workspace, then
   preserve the whole store and its configuration with the [whole-store
   preservation procedure](/recovery/database-corruption#2-preserve-the-damaged-store),
   keeping the preserved copy outside any server data_dir. Work only on a
   copy from here on.

## Recovery route 1: rebuild the drifted table from Dolt history

The stock-Dolt route, first worked out by the community in [the recovery
recipe](https://github.com/gastownhall/beads/issues/4380#issuecomment-4899301060)
on #4380. Dolt keeps table history, so for each drifted table you can find
the last version that reads back cleanly and restore it:

```bash
dolt log --oneline <table>          # candidate commits, newest first
dolt sql -q "SELECT * FROM <table> AS OF '<commit>'"   # bisect to the last readable version
dolt checkout <commit> -- <table>   # restore that version of the table
```

Then reconcile the restored copy with what the current version should contain.
The last readable commit is a floor, not the answer:

- rows deleted after that commit are back — re-delete them;
- rows created after it are gone — re-enter them;
- schema changes after it are reverted — re-apply them;
- a cell that reads *historically* is not necessarily correct at its latest
  value: historical readability is not latest-value recovery, so verify the
  content you actually depend on, not just that the query returns.

## Recovery route 2: the upstream experimental repair tool (closed, unmerged)

Dolt's own attempt at automating this repair class lives on the
`zachmu/schema-repair-tool` branch
([dolthub/dolt#11133](https://github.com/dolthub/dolt/pull/11133)). As of
2026-11-08 that pull request is **closed and unmerged**, its title carries an
explicit *do not merge*, and there is no supported install or release — treat
it as an archived experiment, not a tool. The [validation it
posted](https://github.com/gastownhall/beads/issues/4380#issuecomment-4898979147)
showed promising results on a *sample*, with the sampling limitations spelled
out in the [follow-up](https://github.com/gastownhall/beads/issues/4380#issuecomment-4899140648)
— and a sampled check is not a full validation. If you evaluate it anyway,
run it against the preserved copy only, and validate the result fully as
below before trusting it.

## Validate, then rerun the migration

On the recovered copy, before returning it to production use:

- Read the whole recovered table — a full pass, not a sample — and confirm
  neither drift signature (`invalid hash length`, `malformed out-of-band
  adaptive value`) returns anywhere.
- Validate semantics, not just readability: `bd list --all` and `bd stats`
  must match what you expect, and the dependency graph must be intact
  (dependency rows are what 0059 recomputes from, so a silently mangled
  `depends_on` value surfaces later as wrong blocked state).
- Rerun the migration and let it finish:

  ```bash
  bd migrate schema
  ```

If the refusal returns for a different table, loop back to the recovery
routes: each drifted table is rebuilt separately.

## Prevention

- Keep a current recovery source — push to a Dolt remote (`bd dolt push`) or
  take Dolt-native backups (`bd backup init <path>` once, then
  `bd backup sync`) — so a history rebuild has history to rebuild from.
- Back up before every migration that crosses pending versions, as the
  [upgrade guide](/getting-started/upgrading) directs.

## Residual raw-panic paths

The refusal above covers the drift's *returned errors* — the forms the
engine reports to `bd`. In embedded mode the raw engine panic on some read
paths is still a raw panic: `bd` installs no recovery seam for it, so the
command crashes instead of printing the refusal, and the database is left in
the same mid-migration state ([#5699](https://github.com/gastownhall/beads/issues/5699)
tracks the residual driver limitation). The response is the one this page
already gave: preserve the store, then recover on the copy. This release
improves the diagnosis of the returned-error forms; it does not make the
raw-panic paths safe.
