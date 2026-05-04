---
id: orphans
title: bd orphans
slug: /cli-reference/orphans
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc orphans`

## bd orphans

Identify orphaned issues - issues that are referenced in commit messages but remain open or in_progress in the database.

This helps identify work that has been implemented but not formally closed.

Examples:
  bd orphans              # Show orphaned issues
  bd orphans --json       # Machine-readable output
  bd orphans --details    # Show full commit information
  bd orphans --fix        # Close orphaned issues with confirmation
  bd orphans --label theme:personal             # Only orphans with this label
  bd orphans --label-any theme:personal,theme:ventures  # Orphans with either label

```
bd orphans [flags]
```

**Flags:**

```
      --details             Show full commit information
  -f, --fix                 Close orphaned issues with confirmation
  -l, --label strings       Filter by labels (AND: must have ALL). Can combine with --label-any
      --label-any strings   Filter by labels (OR: must have AT LEAST ONE). Can combine with --label
```
