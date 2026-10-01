---
title: "bd purge"
description: "Delete closed ephemeral beads to reclaim space"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc purge`.

Permanently delete closed ephemeral beads and their associated data.

Closed ephemeral beads (wisps, transient molecules) accumulate rapidly and
have no value once closed. This command removes them to reclaim storage.

Deletes: issues, dependencies, labels, events, and comments for matching beads.
Skips: pinned beads, and closed beads a live bead still depends on through a
parent-child, tracks or blocks edge (a closed molecule root whose step is open,
a closed bead a live convoy tracks). Live means any status that is not done.

--wisps-plane selects by storage plane instead: every closed row stored in the
wisps table, including --no-history beads, which the default ephemeral
selection leaves to `bd prune`. It reaches durable-tier rows, so it requires
--older-than or --pattern, like `bd prune`.

--limit N caps one run at N beads, oldest-closed first, so a large backlog can
be drained in bounded transactions; --json then reports "remaining" and
"has_more". Loop while has_more is true.

--older-than takes days (7, 7d), weeks (2w) or a duration with hour precision
(36h, 168h, 90m).

To delete closed non-ephemeral beads (regular tasks, features, bugs, etc.)
use `bd prune` instead.

For full Dolt storage reclaim after deleting many rows, follow with `bd flatten`
so history can be collapsed and old chunks can be garbage-collected.

EXAMPLES:
  bd purge                           # Preview what would be purged
  bd purge --force                   # Delete all closed ephemeral beads
  bd purge --older-than 7d --force   # Only purge items closed 7+ days ago
  bd purge --pattern "*-wisp-*"      # Only purge matching ID pattern
  bd purge --older-than 36h --force  # Hour precision: closed 36+ hours ago
  bd purge --wisps-plane --older-than 168h --force
                                     # Every closed wisps-table row, incl. no-history
  bd purge --dry-run                 # Detailed preview with stats

```
bd purge [flags]
```

**Flags:**

```
      --dry-run             Preview what would be purged with stats
  -f, --force               Actually purge (without this, shows preview)
      --limit int           Purge at most N beads this run, oldest-closed first (0 = no limit); --json reports remaining/has_more
      --older-than string   Only purge beads closed more than N ago (e.g., 7d, 2w, 30, 36h)
      --pattern string      Only purge beads matching ID glob pattern (e.g., *-wisp-*)
      --wisps-plane         Select every closed row in the wisps table, including --no-history beads (requires --older-than or --pattern)
```
