---
id: restore
title: bd restore
slug: /cli-reference/restore
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc restore` (bd version 1.0.3)

## bd restore

Restore full history of a compacted issue from Dolt version history.

When an issue is compacted, its description and notes are truncated.
This command queries Dolt's history tables to find the pre-compaction
version and displays the full issue content.

This is read-only and does not modify the database.

```
bd restore <issue-id> [flags]
```

**Flags:**

```
      --json   Output restore results in JSON format
```
