---
title: "bd history"
description: "Show the complete version history of an issue, including all commits"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc history`.

Show the complete version history of an issue, including all commits
where the issue was modified.

Examples:
  bd history bd-123           # Show all history for issue bd-123
  bd history bd-123 --limit 5 # Show last 5 changes

```
bd history <id> [flags]
```

**Flags:**

```
      --limit int   Limit number of history entries (0 = all)
```
