---
id: promote
title: bd promote
slug: /cli-reference/promote
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc promote` (bd version 1.0.3)

## bd promote

Promote a wisp (ephemeral issue) to a permanent bead.

This copies the issue from the wisps table (dolt_ignored) to the permanent
issues table (Dolt-versioned), preserving labels, dependencies, events, and
comments. The original ID is preserved so all links keep working.

A comment is added recording the promotion and optional reason.

Examples:
  bd promote bd-wisp-abc123
  bd promote bd-wisp-abc123 --reason "Worth tracking long-term"

```
bd promote <wisp-id> [flags]
```

**Flags:**

```
  -r, --reason string   Reason for promotion
```
