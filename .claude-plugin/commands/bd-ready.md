---
description: Find ready-to-work tasks with no blockers
---

Use the beads MCP server to find tasks that are ready to work on (no blocking dependencies).

Call the `ready` tool to get a list of unblocked issues. Then present them to the user in a clear format showing:
- Issue ID
- Title
- Priority
- Issue type

If there are ready tasks, ask the user which one they'd like to work on. If they choose one, use the `update` tool to set its status to `in_progress`.

If there are no ready tasks, suggest checking `blocked` issues or creating a new issue with the `create` tool.
