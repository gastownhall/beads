---
id: reopen
title: bd reopen
slug: /cli-reference/reopen
sidebar_position: 80
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc reopen` (bd version 1.0.3)

## bd reopen

Reopen closed issues by setting status to 'open' and clearing the closed_at timestamp.
This is more explicit than 'bd update --status open' and emits a Reopened event.

```
bd reopen [id...] [flags]
```

**Flags:**

```
  -r, --reason string   Reason for reopening
```
