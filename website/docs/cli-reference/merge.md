---
id: merge
title: bd merge
slug: /cli-reference/merge
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc merge`

## bd merge

Merge the specified branch into the current branch.

If there are merge conflicts, they will be reported. You can resolve
conflicts automatically with --strategy ours|theirs.

Examples:
  bd merge feature-xyz                    # Merge feature-xyz into current branch
  bd merge feature-xyz --strategy ours    # Merge, preferring our changes on conflict
  bd merge feature-xyz --strategy theirs  # Merge, preferring their changes on conflict

```
bd merge <branch> [flags]
```

**Flags:**

```
      --strategy string   Conflict resolution strategy: 'ours' or 'theirs'
```
