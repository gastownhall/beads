---
title: "bd flatten"
description: "Squash all Dolt history into a single commit"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc flatten`.

Nuclear option: squash ALL Dolt commit history into a single commit.

This uses the Tim Sehn recipe:
  1. Create a new branch from the current state
  2. Soft-reset to the initial commit (preserving all data)
  3. Commit everything as a single snapshot
  4. Swap main branch to the new flattened branch
  5. Prune remote-tracking refs (they would keep the old history alive;
     the next push or fetch re-creates them at the new tip)
  6. Run a full Dolt GC (all storage generations) to reclaim the old history

The GC pass is a full collection: Dolt storage is generational, and a default
GC never revisits data an earlier GC moved to the old generation. On any store
that has been GC'd before (bd gc, or a previous flatten or compact), only a
full collection reclaims the squashed history. A full GC can take minutes on
multi-gigabyte stores.

This is irreversible — all commit history is lost. The resulting database
has exactly one commit containing all current data.

Use this when:
  - Your .beads/dolt directory has grown very large
  - You don't need commit-level history (time travel)
  - You want to start fresh with minimal storage

Examples:
  bd flatten --dry-run               # Preview: show commit count and disk usage
  bd flatten --force                 # Actually squash all history
  bd flatten --force --json          # JSON output

```
bd flatten [flags]
```

**Flags:**

```
      --dry-run   Preview without making changes
  -f, --force     Confirm irreversible history squash
```
