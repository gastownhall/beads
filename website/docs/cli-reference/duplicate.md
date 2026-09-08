---
id: duplicate
title: bd duplicate
slug: /cli-reference/duplicate
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc duplicate`

## bd duplicate

Mark an issue as a duplicate of a canonical issue.

The duplicate issue is automatically closed with a reference to the canonical.
This is essential for large issue databases with many similar reports.

Examples:
  bd duplicate bd-abc --of bd-xyz    # Mark bd-abc as duplicate of bd-xyz

```
bd duplicate <id> --of <canonical> [flags]
```

**Flags:**

```
      --of string   Canonical issue ID (required)
```
