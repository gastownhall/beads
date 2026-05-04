---
id: set-state
title: bd set-state
slug: /cli-reference/set-state
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc set-state` (bd version 1.0.3)

## bd set-state

Atomically set operational state on an issue.

This command:
1. Creates an event bead recording the state change (source of truth)
2. Removes any existing label for the dimension
3. Adds the new dimension:value label (fast lookup cache)

State labels follow the convention &lt;dimension&gt;:&lt;value&gt;, for example:
  patrol:active, patrol:muted
  mode:normal, mode:degraded
  health:healthy, health:failing

Examples:
  bd set-state agent-abc patrol=muted --reason "Investigating stuck worker"
  bd set-state agent-abc mode=degraded --reason "High error rate detected"
  bd set-state agent-abc health=healthy

The --reason flag provides context for the event bead (recommended).

```
bd set-state <issue-id> <dimension>=<value> [flags]
```

**Flags:**

```
      --reason string   Reason for the state change (recorded in event)
```
