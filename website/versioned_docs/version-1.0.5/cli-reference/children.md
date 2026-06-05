---
id: children
title: bd children
slug: /cli-reference/children
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc children`

## bd children

List all beads that are children of the specified parent bead.

This is a convenience alias for 'bd list --parent &lt;id&gt; --status all'.
Unlike plain 'bd list', children includes closed issues by default,
since the primary use case is inspecting all work under a parent.

Examples:
  bd children hq-abc123        # List all children of hq-abc123
  bd children hq-abc123 --json # List children in JSON format
  bd children hq-abc123 --pretty # Show children in tree format

```
bd children <parent-id>
```
