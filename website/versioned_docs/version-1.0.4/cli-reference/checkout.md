---
id: checkout
title: bd checkout
slug: /cli-reference/checkout
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc checkout`

## bd checkout

Switch the Dolt database to a different branch.

This command requires the Dolt storage backend. The target branch
must already exist (create one with 'bd branch &lt;name&gt;').

Examples:
  bd checkout main             # Switch to the main branch
  bd checkout feature-xyz      # Switch to feature-xyz branch

```
bd checkout <branch>
```
