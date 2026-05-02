---
description: Close a completed issue
argument-hint: [issue-id] [reason]
---

Close a beads issue that's been completed.

If arguments are provided:
- $1: Issue ID
- $2+: Completion reason (optional)

If the issue ID is missing, ask for it. Optionally ask for a reason describing what was done.

Use the beads MCP `close` tool to close the issue. Show confirmation with the issue details.

After closing, suggest checking for:
- Dependent issues that might now be unblocked (use `ready` tool)
- New work discovered during this task (use `create` tool with `discovered-from` link)
