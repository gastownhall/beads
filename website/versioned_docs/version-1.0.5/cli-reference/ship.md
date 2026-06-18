---
id: ship
title: bd ship
slug: /cli-reference/ship
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc ship`

## bd ship

Ship a capability to satisfy cross-project dependencies.

This command:
  1. Finds issue with export:&lt;capability&gt; label
  2. Validates issue is closed (or --force to override)
  3. Adds provides:&lt;capability&gt; label

External projects can depend on this capability using:
  bd dep add &lt;issue&gt; external:&lt;project&gt;:&lt;capability&gt;

The capability is resolved when the external project has a closed issue
with the provides:&lt;capability&gt; label.

Examples:
  bd ship mol-run-assignee              # Ship the mol-run-assignee capability
  bd ship mol-run-assignee --force      # Ship even if issue is not closed
  bd ship mol-run-assignee --dry-run    # Preview without making changes

```
bd ship <capability> [flags]
```

**Flags:**

```
      --dry-run   Preview without making changes
      --force     Ship even if issue is not closed
```
