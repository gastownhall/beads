---
id: wisps
title: Wisps
sidebar_position: 5
---

# Wisps

Wisps are ephemeral workflows that don't sync to git.

## What are Wisps?

Wisps are "vapor phase" molecules - issues stored with `Ephemeral=true` in the main database. They are local-only and not synced via Dolt push/pull.

## Use Cases

| Scenario | Why Wisp? |
|----------|-----------|
| Release workflows | One-time execution, no audit trail needed |
| Operational loops | Recurring cycles that auto-clean up |
| Health checks | Diagnostics that shouldn't clutter history |
| Local experiments | Quick throwaway work |
| Agent coordination | Local-only parallel coordination |

## Creating Wisps

```bash
# Create wisp from formula
bd mol wisp <formula> [--var key=value]

# Examples
bd mol wisp quick-check
bd mol wisp release --var version=1.0.0
```

Formulas can recommend wisp usage with `phase = "vapor"` in the formula definition. If you use `bd mol pour` on a vapor-phase formula, you'll get a warning suggesting `bd mol wisp` instead.

## Wisp Lifecycle

```
Formula (template)
    | bd mol wisp
    v
Wisp (ephemeral, Ephemeral=true)
    | normal bd operations
    v
Completed Wisp
    |
    +---> bd mol squash  (promote to persistent molecule)
    +---> bd mol burn    (delete without trace)
    +---> bd mol wisp gc (garbage collect old wisps)
```

### Squash (Promote to Persistent)

Convert a wisp into a regular persistent molecule. Clears the `Ephemeral` flag so the issue syncs to git. Use when ephemeral work turns out to be worth preserving.

```bash
bd mol squash <wisp-id>
```

### Burn (Delete Without Trace)

Delete a wisp and all its children without creating a digest or summary. Use for discarding failed or abandoned ephemeral work.

```bash
bd mol burn <wisp-id>
```

### Garbage Collection

Clean up old, orphaned, or completed wisps automatically:

```bash
# List all wisps (flags old ones > 24h)
bd mol wisp list

# Garbage collect orphaned wisps
bd mol wisp gc
```

Wisps older than 24 hours are flagged as "old" in list output.

## Wisp Commands

```bash
bd mol wisp <formula>       # Create wisp from formula
bd mol wisp list             # List all wisps
bd mol wisp list --json      # List in JSON format
bd mol wisp gc               # Garbage collect old wisps
bd mol squash <wisp-id>      # Promote to persistent
bd mol burn <wisp-id>        # Delete without digest
```

## Wisp vs Molecule

| Aspect | Molecule (pour) | Wisp |
|--------|-----------------|------|
| Phase | Liquid | Vapor |
| Persistence | Permanent, syncs via Dolt | Ephemeral, local-only |
| Use case | Tracked work, audit trail | Temporary ops, one-time runs |
| Cleanup | Manual close/archive | Squash, burn, or GC |

## Phase Control

Use `bd mol bond` to control phase when bonding formulas together:

```bash
# Force liquid (persistent molecule)
bd mol bond <formula> <target> --pour

# Force vapor (ephemeral wisp)
bd mol bond <formula> <target> --wisp
```

## Best Practices

1. **Default to wisp for operational work** - releases, checks, diagnostics
2. **Use pour for tracked work** - features, bugs, anything worth preserving
3. **Squash if work becomes valuable** - promote ephemeral to persistent
4. **Burn failed experiments** - clean up without cluttering history
5. **Run GC periodically** - `bd mol wisp gc` keeps the database lean
