---
title: "bd blocked"
description: "Show blocked issues"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc blocked`.

Show blocked issues

```
bd blocked [flags]
```

**Flags:**

```
      --exclude-label strings   Exclude issues that have ANY of these labels
      --flags                   Print the blocked flag for each id read from stdin (one per line) instead of the listing; with --json, [{"id","is_blocked"}]
  -l, --label strings           Filter by labels (AND: must have ALL). Can combine with --label-any
      --label-any strings       Filter by labels (OR: must have AT LEAST ONE). Can combine with --label
      --parent string           Filter to descendants of this bead/epic
      --raw                     With --flags: the stored blocked column itself, without the external-dependency policy (an unsatisfied external: blocker does not set it)
```
