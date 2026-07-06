---
id: merge-slot
title: bd merge-slot
slug: /cli-reference/merge-slot
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc merge-slot`

## bd merge-slot

Merge-slot gates serialize conflict resolution in the merge queue.

A merge slot is an exclusive access primitive: only one agent can hold it at a time.
This prevents "monkey knife fights" where multiple polecats race to resolve conflicts
and create cascading conflicts.

Each rig has one merge slot bead: &lt;prefix&gt;-merge-slot (labeled gt:slot).
The slot uses:
  - status=open: slot is available
  - status=in_progress: slot is held
  - metadata.holder: who currently holds the slot
  - metadata.waiters: priority-ordered queue of waiters

Examples:
  bd merge-slot create              # Create merge slot for current rig
  bd merge-slot check               # Check if slot is available
  bd merge-slot acquire             # Try to acquire the slot
  bd merge-slot release             # Release the slot

```
bd merge-slot
```

### bd merge-slot acquire

Attempt to acquire the merge slot for exclusive access.

If the slot is available (status=open), it will be acquired:
  - status set to in_progress
  - holder set to the requester

If the slot is held (status=in_progress), the command fails unless
--wait is passed, which adds the requester to the waiters queue.

Use --holder to specify who is acquiring (default: BEADS_ACTOR env var).

```
bd merge-slot acquire [flags]
```

**Flags:**

```
      --holder string   Who is acquiring the slot (default: BEADS_ACTOR)
      --wait            Add to waiters list if slot is held
```

### bd merge-slot check

Check if the merge slot is available or held.

Returns:
  - available: slot can be acquired
  - held by &lt;holder&gt;: slot is currently held
  - not found: no merge slot exists for this rig

```
bd merge-slot check
```

### bd merge-slot create

Create a merge slot bead for serialized conflict resolution.

The slot ID is automatically generated based on the beads prefix (e.g., gt-merge-slot).
The slot is created with status=open (available).

```
bd merge-slot create
```

### bd merge-slot release

Release the merge slot after conflict resolution is complete.

Sets status back to open and clears the holder field.
If there are waiters, the highest-priority waiter should then acquire.

```
bd merge-slot release [flags]
```

**Flags:**

```
      --holder string   Who is releasing the slot (for verification)
```
