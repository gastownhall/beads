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
Every other delete — a plain `bd purge` or `bd prune`, `bd delete`,
`bd gc`, `bd admin cleanup`, `bd mol wisp gc`, `bd mol burn`, the
stale-closed-issues `bd doctor --fix`, and `bd sql` — leaves deleted ids
reusable; `types.retained` (below) keeps the retention commands away from the
types whose ids must stay retired. Over HTTP, the server must advertise `issues.sweep.retainIds`;
without it, bd refuses the sweep before sending anything.

### Keeping key-bearing types out of plain sweeps

A plain `bd prune` that happens to match key-bearing rows would delete them
without tombstones. To prevent that, name their issue types in the workspace
setting `types.retained`:

```bash
bd config set types.retained op
```

**Which commands honor it.** The retention commands: the ones whose
selection is a retention policy over closed beads. They keep every closed
bead of a listed type and report how many they kept (`retention_required`
under `--json`):

- `bd prune` and `bd purge` (including `--wisps-plane`) without
  `--retain-ids`, their `--dry-run` previews, and
  `POST /v0/beads/issues:sweep` without `retain_ids`;
- the decay phase of `bd gc`;
- `bd admin cleanup`, which also refuses `--cascade` while the setting names
  a type;
- the stale-closed-issues `bd doctor --fix`, whose check counts only the
  beads the fix would delete.

A sweep with `--retain-ids` keeps nothing back: it deletes those beads and
retires their ids, which is the one retention command they leave by.

**What it means.**

- A retained bead protects itself only, never its dependents or its parents.
  None of the commands above cascades.
- It applies to closed beads; every command above selects closed beads only.
- In a sweep the hold-back runs after the pinned and `closed_at` rechecks and
  before the live-dependent and reference protections and `--limit`, so held
  back beads never use up a sweep's limit.
- It is read from the workspace database, never from `config.yaml`, so
  every client of a shared workspace sees the same rule; a `types.retained`
  key in `config.yaml` is ignored, and the commands above warn that it is. If
  bd cannot read it, the command fails and deletes nothing.
- A sweep reads it in the transaction that deletes. `bd gc`,
  `bd admin cleanup` and `bd doctor --fix` read it together with the beads
  they select, from one snapshot (over HTTP, `bd gc` reads it right after),
  and delete afterwards: a change to the setting made after they select
  applies from their next run.
- Names are matched through one canonicalizing read: each name protects
  itself, its canonical form under bd's type aliases, and its lower-case form
  when that is a built-in type. So `feat` protects `feature` beads, `mol`
  protects `molecule` and `Chore` protects `chore`. A custom type matches as
  written. Names need not be registered types yet; a name no bead carries
  matches nothing.
- The value takes a comma-separated list or a JSON array. `bd config set`
  refuses only a value whose syntax would read as something other than what
  you wrote: a bracketed list that is not JSON (`[op]`), a name with a space,
  quote, bracket or comma in it (`"op"`, `op fact`), and a non-blank value
  that names no type (`null`, `,`). `[]` retains nothing, and an empty value
  unsets the key. A value stored past that check, by an older bd or by
  `bd sql`, still protects the names in it: `[op]` and `op fact` protect
  `op`.

**What it does not cover, by design.** These commands do not consult the
setting:

- `bd mol wisp gc`, which selects only ephemeral wisps (`ephemeral = 1`); a
  no-history bead is never one of them, so a retained ephemeral type gets no
  protection from it;
- the Patrol Pollution and test-pollution deletes of `bd doctor`, which are
  heuristics;
- `bd migrate-personal`, a move between two databases, which takes your own
  beads of a retained type with the rest;
- the deletes that name what they delete: `bd delete` (`--cascade`
  included), `bd mol burn` and `bd mol squash`, `bd cook --force` and
  `bd repo remove`; and `bd sql`, which bypasses bd.

Over HTTP, a server that honors the setting advertises
`issues.sweep.retainedTypes`. Against an older server, which would delete
those beads without retiring their ids, bd reads the setting first and
refuses a plain `bd prune` or `bd purge` when it names a type; upgrade the
server, or use `--retain-ids` if the server supports it.

**Upgrade every bd first.** A bd that predates the setting ignores it, so
its `bd prune`, `bd purge`, `bd gc` or `bd admin cleanup` can delete those
beads without retiring their ids, and a `bd dolt push` carries the deletes to
every clone. The setting protects a workspace only once every bd that runs
these commands on it honors the setting.

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
