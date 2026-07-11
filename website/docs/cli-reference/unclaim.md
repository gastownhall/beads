---
id: unclaim
title: bd unclaim
slug: /cli-reference/unclaim
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc unclaim`

## bd unclaim

Release a claimed issue by clearing the assignee and resetting status to 'open'.

Use this when an agent crashes mid-work or you need to abandon a claimed task.
The issue becomes available for re-claiming by other agents.

Examples:
  bd unclaim bd-123
  bd unclaim bd-123 --reason "Agent crashed"
  bd unclaim bd-123 bd-456

```
bd unclaim [id...] [flags]
```

**Flags:**

```
  -r, --reason string   Reason for unclaiming
```
