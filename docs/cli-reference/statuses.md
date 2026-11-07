---
title: "bd statuses"
description: "List valid issue statuses"
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc statuses`.

List all valid issue statuses and their categories.

Built-in statuses (open, in_progress, blocked, etc.) are always valid.
Additional statuses can be configured via status.custom:

  bd config set status.custom "in_review:active,qa_testing:wip,on_hold:frozen"

Categories control behavior:
  active  — appears in 'bd ready' and default 'bd list'
  wip     — excluded from 'bd ready', visible in default 'bd list'
  done    — excluded from 'bd ready' and default 'bd list'
  frozen  — excluded from 'bd ready' and default 'bd list'

Statuses without a category (legacy format) are valid but excluded from 'bd ready'.

The blocked status is an explicit hold, set by hand ('bd update <id> --status
blocked') or by a tracker sync. Dependency blocking is separate: an issue with
an open blocker is listed by 'bd blocked' and left out of 'bd ready', whatever
its status. Closing an issue's last blocker clears its dependency blocking but
leaves the status as it is ('bd close --suggest-next' flags it); after review,
release the issue with 'bd update <id> --status open'. 'bd doctor' (Blocked
Status) and 'bd doctor --check=conventions' (conventions.blocked-status) list
issues in status blocked whose stored dependency-blocked flag is clear.

Examples:
  bd statuses            # List all statuses with icons and categories
  bd statuses --json     # Output as JSON


```
bd statuses [flags]
```
