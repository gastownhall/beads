---
title: "bd remember"
description: "Store a persistent memory"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc remember`.

Store a memory that persists across sessions and account rotations.

Memories are injected at prime time (bd prime) so you have them
in every session without manual loading.

The positional arg is the memory CONTENT (the key is auto-generated from it
unless --key is given). As a convenience, if the arg is a bare key naming an
existing memory, it is RECALLED instead of stored (same as 'bd recall');
a bare key naming nothing is refused. Use --key to store slug-like content.

Corpus budget (opt-in, off by default): set the memories.budget-chars config
key to a byte ceiling for the whole memory corpus (sum of len(key)+len(content)).
A write that would cross it is refused, and --force overrides; a write that
lands at or above 80% of the budget warns. With the key unset nothing changes.
bd prime's --max-memory-chars also counts bytes, but of each memory as
injected, formatting included, so a budget equal to that cap does not
guarantee prime injects every memory; set the budget below it for that.

Examples:
  bd remember "always run tests with -race flag"
  bd remember "Dolt phantom DBs hide in three places" --key dolt-phantoms
  bd remember "auth module uses JWT not sessions" --key auth-jwt
  bd remember dolt-phantoms        # bare existing key: reads it (= bd recall)

```
bd remember "<insight>" [flags]
```

**Flags:**

```
      --force        Store even when the write would cross the memories.budget-chars corpus budget (no effect when the budget is unset)
      --key string   Explicit key for the memory (auto-generated from content if not set). If a memory with this key already exists, it will be updated in place
```
