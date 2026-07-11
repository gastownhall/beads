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
