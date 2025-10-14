---
description: Show detailed information about an issue
argument-hint: [issue-id]
---

Display detailed information about a beads issue.

If an issue ID is provided as $1, use it. Otherwise, ask the user for the issue ID.

Use the beads MCP `show` tool to retrieve issue details and present them clearly, including:
- Issue ID, title, and description
- Status, priority, and type
- Creation and update timestamps
- Dependencies (what this issue blocks or is blocked by)
- Related issues

If the issue has dependencies, offer to show the full dependency tree.
