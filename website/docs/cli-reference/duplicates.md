---
id: duplicates
title: bd duplicates
slug: /cli-reference/duplicates
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc duplicates` (bd version 1.0.3)

## bd duplicates

Find issues with identical content (title, description, design, acceptance criteria).
Groups issues by content hash and reports duplicates with suggested merge targets.
The merge target is chosen by:
1. Reference count (most referenced issue wins)
2. Lexicographically smallest ID if reference counts are equal
Only groups issues with matching status (open with open, closed with closed).
Example:
  bd duplicates                    # Show all duplicate groups
  bd duplicates --auto-merge       # Automatically merge all duplicates
  bd duplicates --dry-run          # Show what would be merged

```
bd duplicates [flags]
```

**Flags:**

```
      --auto-merge   Automatically merge all duplicates
      --dry-run      Show what would be merged without making changes
```
