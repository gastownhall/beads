---
id: branch
title: bd branch
slug: /cli-reference/branch
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc branch`

## bd branch

List all branches, create a new branch, or delete an existing branch.

This command requires the Dolt storage backend. Without arguments,
it lists all branches. With an argument, it creates a new branch.
With -d, it deletes the named branch.

Examples:
  bd branch                    # List all branches
  bd branch feature-xyz        # Create a new branch named feature-xyz
  bd branch -d feature-xyz     # Delete branch feature-xyz

```
bd branch [name] [flags]
```

**Flags:**

```
  -d, --delete   Delete the named branch
```
