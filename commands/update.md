---
description: Update an issue's status, priority, or other fields
argument-hint: [issue-id] [status]
---

Update a beads issue.

If arguments are provided:
- $1: Issue ID
- $2: New status (open, in_progress, blocked, closed)

If arguments are missing, ask the user for:
1. Issue ID
2. What to update (status, priority, assignee, title, description)
3. New value

Use the beads MCP `update` tool to apply the changes. Show the updated issue to confirm the change.

**Note:** Comments are managed separately with `bd comments add`. The `update` command is for singular, versioned properties (title, status, priority, etc.), while comments form a discussion thread that's appended to, not updated.

Common workflows:
- Start work: Update status to `in_progress`
- Mark blocked: Update status to `blocked`
- Reprioritize: Update priority (0-4)
