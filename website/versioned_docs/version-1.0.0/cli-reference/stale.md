---
id: stale
title: bd stale
slug: /cli-reference/stale
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc stale`

## bd stale

Show issues that haven't been updated recently and may need attention.
This helps identify:
- In-progress issues with no recent activity (may be abandoned)
- Open issues that have been forgotten
- Issues that might be outdated or no longer relevant

```
bd stale [flags]
```

**Flags:**

```
  -d, --days int        Issues not updated in this many days (default 30)
  -n, --limit int       Maximum issues to show (default 50)
  -s, --status string   Filter by status (open|in_progress|blocked|deferred)
```
