---
title: "bd cas"
description: "Conditional (compare-and-swap) metadata writes"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc cas`.

Atomic compare-and-swap on a single metadata key.

Use 'cas set' to set a key only if its current value matches an expectation —
the building block for lock-free fences, epoch counters, and claim-once
reservations across concurrent writers.

On a precondition mismatch the command exits 9 (distinct from the generic
error code 1) with the current value, so a caller can re-read and retry.

```
bd cas [command]
```

## bd cas set

Atomically set a metadata key iff a precondition holds.

Exactly one precondition is required:
  --if &lt;value&gt;   set only if the key currently equals &lt;value&gt;
  --if-absent    set only if the key is currently absent (claim-once)

Examples:
  bd cas set bd-1 gc.control_epoch 5 --if 4     # advance epoch 4 -&gt; 5
  bd cas set bd-1 gc.drain.reserved_by me --if-absent   # claim once

Exit codes: 0 success, 9 precondition mismatch, 1 other error.

```
bd cas set <id> <key> <value> [flags]
```

**Flags:**

```
      --if string   set only if the key currently equals this value
      --if-absent   set only if the key is currently absent (claim-once)
```

## bd cas unset

Atomically remove a metadata key iff it currently equals --if &lt;value&gt;.

The symmetric release for 'cas set --if-absent': release a reservation or lease
you hold. Idempotent — removing an already-absent key succeeds.

Example:
  bd cas unset bd-1 gc.drain.reserved_by --if me   # release my reservation

Exit codes: 0 success, 9 held by a different value, 1 other error.

```
bd cas unset <id> <key> [flags]
```

**Flags:**

```
      --if string   remove only if the key currently equals this value
```
