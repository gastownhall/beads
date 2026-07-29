---
title: "bd reopen"
description: "Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp."
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc reopen`.

Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp.
This is more explicit than 'bd update --status open' and emits a Reopened event.

```
bd reopen [id...] [flags]
```

**Flags:**

```
  -r, --reason string   Reason for reopening
```
