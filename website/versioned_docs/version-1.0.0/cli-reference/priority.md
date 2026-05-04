---
id: priority
title: bd priority
slug: /cli-reference/priority
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc priority` (bd version 1.0.3)

## bd priority

Set the priority of an issue.

Shorthand for 'bd update &lt;id&gt; --priority &lt;n&gt;'.

Priority levels:
  0 - Critical (security, data loss, broken builds)
  1 - High (major features, important bugs)
  2 - Medium (default)
  3 - Low (polish, optimization)
  4 - Backlog (future ideas)

Examples:
  bd priority bd-123 0    # Critical
  bd priority bd-123 2    # Medium

```
bd priority <id> <n>
```
