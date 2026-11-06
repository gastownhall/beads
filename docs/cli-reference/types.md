---
title: "bd types"
description: "List valid issue types"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc types`.

List all valid issue types that can be used with bd create --type.

Core work types (bug, task, feature, chore, epic, decision, spike, story, milestone)
and system types (message, molecule, gate, event) are always valid.
Custom types are registered with 'bd config set types.custom' or declared under
types.custom in .beads/config.yaml; the list shown is exactly the set that
bd create --type accepts.

Examples:
  bd types              # List all types with descriptions
  bd types --sections   # List required sections for each type
  bd types --json       # Output as JSON


```
bd types [flags]
```

**Flags:**

```
      --sections   Show required sections for each issue type
```
