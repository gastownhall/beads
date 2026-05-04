---
id: link
title: bd link
slug: /cli-reference/link
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc link` (bd version 1.0.3)

## bd link

Link two issues with a dependency.

Shorthand for 'bd dep add &lt;id1&gt; &lt;id2&gt;'. By default creates a "blocks"
dependency (id2 blocks id1). Use --type to specify a different relationship.

Examples:
  bd link bd-123 bd-456                    # bd-456 blocks bd-123
  bd link bd-123 bd-456 --type related     # bd-123 related to bd-456
  bd link bd-123 bd-456 --type parent-child

```
bd link <id1> <id2> [flags]
```

**Flags:**

```
  -t, --type string   Dependency type (blocks|tracks|related|parent-child|discovered-from) (default "blocks")
```
