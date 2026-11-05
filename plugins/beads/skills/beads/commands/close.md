---
description: Close a completed issue
argument-hint: "[issue-id] [reason]"
---

Close a beads issue that's been completed.

If arguments are provided:
- $1: Issue ID
- $2+: Completion reason (optional)

If the issue ID is missing, ask for it. Optionally ask for a reason describing what was done.

Run `bd close <id> --reason "<reason>"` to close the issue. Show confirmation with the issue details.

After closing, suggest checking for:
- Dependent issues that might now be unblocked (run `bd ready`)
- New work discovered during this task (run `bd create`, then link it with `discovered-from`)
