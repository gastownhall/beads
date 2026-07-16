---
title: "bd unclaim"
description: "Release a claimed issue"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc unclaim`.

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
      --force                Release the claim even if held by a different actor (admin/reaper use)
      --if-assignee string   Only mutate while the issue is still assigned to this actor; empty asserts unassigned (mismatch: exit 9, unsupported path: exit 13)
      --if-fence int         Only mutate while claim_fence still equals this snapshot value (mismatch: exit 9, unsupported path: exit 13)
  -r, --reason string        Reason for unclaiming
```
