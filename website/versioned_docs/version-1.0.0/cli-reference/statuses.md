---
id: statuses
title: bd statuses
slug: /cli-reference/statuses
sidebar_position: 999
---

<!-- AUTO-GENERATED: do not edit manually -->
Generated from `bd help --doc statuses`

## bd statuses

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

Examples:
  bd statuses            # List all statuses with icons and categories
  bd statuses --json     # Output as JSON


```
bd statuses
```
