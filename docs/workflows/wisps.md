---
title: Wisps
description: Ephemeral molecules for operational work that has no audit value once it's done.
---

Operational workflows — release checklists, health patrols, diagnostics —
create beads that are worthless the moment they close. **Wisps** are
molecules instantiated in the *vapor phase*: real beads you work through
normally, flagged `Ephemeral=true` so they stay out of sync and can be
deleted wholesale later.

## What are Wisps?

- Issues in the main database with the ephemeral flag set — worked on with
  normal `bd` commands.
- Local by design: excluded from federation push by default
  (`federation.exclude_types` defaults to `[wisp]`) and not part of the
  shared audit trail.
- Deleted in bulk by `bd purge` or `bd mol wisp gc` once closed.

## Wisp vs Pour

| Aspect | Molecule (`bd mol pour`) | Wisp (`bd mol wisp`) |
|--------|--------------------------|----------------------|
| Persistence | permanent, part of history | ephemeral, purged when done |
| Sync | synced like any bead | excluded from federation push |
| Use case | feature work, anything worth referencing later | release runs, operational loops, health checks |

Formulas can declare `phase = "vapor"` to recommend wisp instantiation —
pouring a vapor-phase formula warns.

## The Wisp Lifecycle

```bash
# 1. Create — from a proto, or ad-hoc
bd mol wisp <proto-id> [--var key=value]
bd create "One-off check" --ephemeral

# 2. Execute — normal bd operations work on wisp issues
bd ready --mol <wisp-id>
bd update <id> --claim
bd close <id>

# 3a. Keep it after all: squash promotes to persistent (clears the flag)
bd mol squash <wisp-id>

# 3b. Or burn: delete without creating a digest
bd mol burn <wisp-id>
```

## Managing Wisps

```bash
bd mol wisp list      # list all wisps in the current context
bd mol wisp gc        # garbage collect old/abandoned wisps
bd purge --force      # delete all closed ephemeral beads
```

### Retiring ids with `--retain-ids`

By default a purged or pruned id is free again: a later `bd create --id`
with that id succeeds and writes a new issue. If your ids are idempotency
keys, such as an orchestrator's pinned operation ids, pass `--retain-ids` to
`bd purge` or `bd prune`:

```bash
bd purge --force --retain-ids
bd prune --older-than 30d --force --retain-ids
```

In the same transaction as the delete, bd records a small permanent tombstone
for every deleted id: the id and its `closed_at`, nothing else. After that:

- `bd create --id <id>` fails with `idempotency_expired` (HTTP 410). It never
  writes a new issue.
- `bd import` skips the row and lists it under `retired_ids`.
- Generated ids never reuse a retired id.

No command removes a tombstone. Only a sweep with `--retain-ids` retires ids.
A plain `bd purge` or `bd prune`, `bd delete`, and `bd gc` all leave deleted
ids reusable; `types.retained` (below) keeps the plain sweeps and `bd gc` away
from the types whose ids must stay retired. Over HTTP, the server must advertise `issues.sweep.retainIds`;
without it, bd refuses the sweep before sending anything.

### Keeping key-bearing types out of plain sweeps

A plain `bd prune` that happens to match key-bearing rows would delete them
without tombstones. To prevent that, name their issue types in the workspace
setting `types.retained`:

```bash
bd config set types.retained op
```

From then on a sweep without `--retain-ids` keeps every closed bead of those
types and reports how many it kept (`retention_required` under `--json`).
This applies to `bd prune`, `bd purge` (including `--wisps-plane`), their
`--dry-run` previews, and the decay phase of `bd gc`. A sweep with
`--retain-ids` keeps nothing back: it deletes those beads and retires their
ids, which is the only way they leave through a sweep.

The setting takes a comma-separated list or a JSON array. Names need not be
registered types yet; a name no bead carries matches nothing. It is read from
the workspace database on every sweep, never from `config.yaml`, so every
client of a shared workspace sees the same rule. If bd cannot read it, the
sweep fails and deletes nothing. `bd delete` does not consult it: it deletes
the ids you name. Neither does `bd mol wisp gc`. Over HTTP, a server that
honors the setting advertises `issues.sweep.retainedTypes`; an older server
deletes those beads without retiring their ids.

A sweep with `--retain-ids` briefly blocks or conflicts with creates of
unrelated ids that hash to the same coordination shard as one it retires,
until the sweep commits. Its reach grows with its size, so keep large sweeps
in batches of about 100 rows: `bd purge --limit 100`, repeated until nothing
remains. `bd prune` has no `--limit`; narrow it with `--older-than` instead.

## Forcing a Phase

`bd mol bond` accepts phase overrides when combining work:

```bash
bd mol bond mol-critical-bug wisp-patrol --pour   # persist a bug found during a patrol
```

## Best Practices

1. **Wisps for operational loops** — patrols, release runs, diagnostics.
2. **Molecules for tracked work** — anything with audit value gets poured,
   not wisped.
3. **Squash before you delete** — if a wisp surfaced something durable,
   `bd mol squash` promotes it; burning is irreversible.
4. **Garbage collect regularly** — `bd mol wisp gc` or `bd purge --force`.
