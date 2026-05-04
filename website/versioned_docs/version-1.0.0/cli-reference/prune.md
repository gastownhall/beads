---
id: prune
title: bd prune
slug: /cli-reference/prune
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc prune` (bd version 1.0.3)

## bd prune

Permanently delete closed non-ephemeral beads and their associated data.

Use this to trim closed regular beads (tasks, features, bugs, chores, etc.)
that are no longer useful. The common case is a long-lived repo where
closed work has piled up and is bloating auto-export or slowing queries.

Requires --older-than or --pattern. The flag is a safety gate — without
it, a muscle-memory `--force` could wipe every closed bead in the repo.
Use `--pattern '*'` if you really do want to sweep everything closed.

Deletes: issues, dependencies, labels, events, and comments for matching beads.
Skips: pinned beads (protected), open/in-progress beads, and ephemeral beads.

To delete closed ephemeral beads (wisps, transient molecules) use
`bd purge` instead.

For full Dolt storage reclaim after deleting many rows, follow with `bd flatten`
so history can be collapsed and old chunks can be garbage-collected.

EXAMPLES:
  bd prune --older-than 30d              # Preview closed beads &gt;30d old
  bd prune --older-than 30d --force      # Delete them
  bd prune --older-than 90d --dry-run    # Detailed preview with stats
  bd prune --pattern "*" --force         # Delete all closed regular beads
  bd prune --pattern "gm-temp-*" --force # Scope to a pattern

```
bd prune [flags]
```

**Flags:**

```
      --dry-run             Preview what would be pruned with stats
  -f, --force               Actually prune (without this, shows preview)
      --older-than string   Only prune beads closed more than N ago (e.g., 30d, 2w, 60)
      --pattern string      Only prune beads matching ID glob pattern (e.g., 'gm-old-*')
```
