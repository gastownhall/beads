---
description: Create a new issue interactively
argument-hint: [title] [type] [priority]
---

Create a new beads issue. If arguments are provided:
- $1: Issue title
- $2: Issue type (bug, feature, task, epic, chore)
- $3: Priority (0-4, where 0=critical, 4=backlog)

If arguments are missing, ask the user for:
1. Issue title (required)
2. Issue type (default: task)
3. Priority (default: 2)
4. Description (optional)

Use the beads MCP `create` tool to create the issue. Show the created issue ID and details to the user.

Optionally ask if this issue should be linked to another issue (discovered-from, blocks, parent-child, related).
