---
title: "bd undefer"
description: "Undefer one or more issues (restore to open)"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc undefer`.

Undefer issues to restore them to open status.

This brings issues back from the icebox so they can be worked on again.
Issues will appear in 'bd ready' if they have no blockers.

If an issue carries a defer_until timestamp but its status isn't
"deferred" (e.g. after an explicit --status change), undefer clears
the stray timestamp without touching status.

Examples:
  bd undefer bd-abc        # Undefer a single issue
  bd undefer bd-abc bd-def # Undefer multiple issues

```
bd undefer [id...] [flags]
```
