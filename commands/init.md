---
description: Initialize beads in the current project
argument-hint: [prefix]
---

Initialize beads issue tracking in the current directory.

If a prefix is provided as $1, use it as the issue prefix (e.g., "myproject" creates issues like myproject-1, myproject-2). If not provided, the default is the current directory name.

Use the beads MCP `init` tool with the prefix parameter (if provided) to set up a new beads database.

After initialization:
1. Show the database location
2. Show the issue prefix that will be used
3. Explain the basic workflow (or suggest running `/beads:workflow`)
4. Suggest creating the first issue with `/beads:create`

If beads is already initialized, inform the user and show project stats using the `stats` tool.
