---
title: "bd unclaim"
description: "Release a claimed issue"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc unclaim`.

Release a claimed issue by clearing the assignee and resetting status to 'open'.

Use this when an agent crashes mid-work or you need to abandon a claimed task.
The issue becomes available for re-claiming by other agents.

Only the current assignee can release its own claim. Releasing another
actor's claim requires --force and should be coordinated with the holder
first — their claim may be live even if the issue looks idle. Prefer
letting lease expiry reclaim genuinely abandoned work.

With --if-assignee, the release is an atomic compare-and-swap (the inverse of
claim): the issue is released only while it is still assigned to the given
assignee. If the holder differs — e.g. the claim was already reclaimed and
re-taken by another worker — nothing is changed and bd exits nonzero with an
error naming the current holder. Use this from supervisors that must return a
specific worker's issue without ever clobbering someone else's live claim.
--if-assignee requires a non-empty assignee and cannot be combined with --force
(they encode contradictory intent).

Exit status: 0 when every issue was released; 1 when any release failed
(including an --if-assignee mismatch).

Examples:
  bd unclaim bd-123
  bd unclaim bd-123 --reason "Agent crashed"
  bd unclaim bd-123 bd-456
  bd unclaim bd-123 --if-assignee worker-7   # only if still held by worker-7

```
bd unclaim [id...] [flags]
```

**Flags:**

```
      --force                Release the claim even if held by a different actor (admin/reaper use)
      --if-assignee string   Only release if still assigned to this assignee (atomic compare-and-swap; exits nonzero without changing the issue when the holder differs)
  -r, --reason string        Reason for unclaiming
```
