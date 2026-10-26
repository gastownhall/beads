---
title: "bd reopen"
description: "Reopen one or more closed issues"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc reopen`.

Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp.
This is more explicit than 'bd update --status open' and emits a Reopened event.

Exit codes: 1 for general failures; 13 when the failure is a stale
--if-revision guard (the precondition no longer held, nothing was written —
another actor won the race, so retrying the same guard is pointless).

```
bd reopen [id...] [flags]
```

**Flags:**

```
      --if-revision bd show --json   Apply the write only if the issue's current revision equals this value (the decimal int64 from bd show --json's "revision" field). One id only: refused before any write when combined with more than one issue id. A mismatch writes nothing and exits 13 (vs 1 for other failures).
  -r, --reason string                Reason for reopening
```
