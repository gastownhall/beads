---
id: vc
title: bd vc
slug: /cli-reference/vc
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc vc` (bd version 1.0.3)

## bd vc

Version control operations for the beads database.

These commands provide git-like version control for your issue data, including branching, merging, and
viewing history.

Note: 'bd history', 'bd diff', and 'bd branch' also work for quick access.
This subcommand provides additional operations like merge and commit.

```
bd vc
```

### bd vc commit

Create a new Dolt commit with all current changes.

Examples:
  bd vc commit -m "Added new feature issues"
  bd vc commit --message "Fixed priority on several issues"
  echo "Multi-line message" | bd vc commit --stdin

```
bd vc commit [flags]
```

**Flags:**

```
  -m, --message string   Commit message
      --stdin            Read commit message from stdin
```

### bd vc merge

Merge the specified branch into the current branch.

If there are merge conflicts, they will be reported. You can resolve
conflicts with --strategy.

Examples:
  bd vc merge feature-xyz                    # Merge feature-xyz into current branch
  bd vc merge feature-xyz --strategy ours    # Merge, preferring our changes on conflict
  bd vc merge feature-xyz --strategy theirs  # Merge, preferring their changes on conflict

```
bd vc merge <branch> [flags]
```

**Flags:**

```
      --strategy string   Conflict resolution strategy: 'ours' or 'theirs'
```

### bd vc status

Show the current branch, commit hash, and any uncommitted changes.

Examples:
  bd vc status

```
bd vc status
```
