# Claude Desktop MCP Server for Beads

> **Note**: The beads MCP server is now fully implemented! See [integrations/beads-mcp](../../integrations/beads-mcp/) for the production implementation.

> **Recommendation**: For environments with shell access (Claude Code, Cursor, Windsurf), use **CLI + hooks** instead of MCP. It uses ~1-2k tokens vs 10-50k for MCP schemas, resulting in lower compute cost and latency. **Use MCP only for MCP-only environments** like Claude Desktop where CLI is unavailable.

## What This Provides

An MCP server that exposes bd functionality to Claude Desktop and other MCP clients, allowing Claude to:
- Query ready work
- Create and update issues
- Manage dependencies
- Track discovered work

## Quick Start

Install the beads MCP server:

```bash
# Using uv (recommended)
uv tool install beads-mcp

# Or using pip
pip install beads-mcp
```

Add to your Claude Desktop config (`~/Library/Application Support/Claude/claude_desktop_config.json` on macOS):

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

Restart Claude Desktop and you're done! Claude can now manage your beads issues.

## Full Documentation

See the [beads-mcp README](../../integrations/beads-mcp/README.md) for:
- Installation instructions
- Configuration options
- Environment variables
- Development guide

---

## Original Design Documentation (Historical)

## Planned Features

```typescript
// MCP server will expose these tools to Claude:

// Find ready work
{
  "name": "beads_ready_work",
  "description": "Find issues with no blocking dependencies",
  "parameters": {
    "limit": "number",
    "priority": "number (0-4)",
    "assignee": "string"
  }
}

// Create issue
{
  "name": "beads_create_issue",
  "description": "Create a new issue",
  "parameters": {
    "title": "string",
    "description": "string",
    "priority": "number (0-4)",
    "type": "bug|feature|task|epic|chore"
  }
}

// Update issue
{
  "name": "beads_update_issue",
  "description": "Update issue status or fields",
  "parameters": {
    "id": "string",
    "status": "open|in_progress|blocked|closed",
    "priority": "number",
    "assignee": "string"
  }
}

// Add dependency
{
  "name": "beads_add_dependency",
  "description": "Link issues with dependencies",
  "parameters": {
    "from": "string (issue ID)",
    "to": "string (issue ID)",
    "type": "blocks|related|parent-child|discovered-from"
  }
}
```

## How It Would Work

1. Install the MCP server for Claude Desktop
2. Claude gains access to bd commands
3. During coding sessions, Claude can:
   - Check for ready work: "What should I work on next?"
   - Create issues: "I found a bug, let me track it"
   - Link discoveries: "This is related to the issue we're working on"
   - Update status: "Marking bd-5 as completed"

## Example Conversation

```
User: What's ready to work on?

Claude: Let me check the ready work queue...
[Calls beads_ready_work()]

I found 3 ready issues:
1. bd-12: Fix authentication bug (priority 0, bug)
2. bd-15: Add dark mode toggle (priority 1, feature)
3. bd-18: Update documentation (priority 2, task)

Which would you like to work on?

User: Let's do bd-12

Claude: Great! I'll mark bd-12 as in progress.
[Calls beads_update_issue("bd-12", status="in_progress")]

Let me investigate the authentication code...
[Works on the issue]

I found the bug in auth.js:45 - there's a race condition. While fixing this, I noticed we're also missing rate limiting. Should I track that as a separate issue?

User: Yes please

Claude: [Calls beads_create_issue("Add rate limiting to auth endpoints", type="task", priority=1)]
Created bd-23: Add rate limiting to auth endpoints

[Calls beads_add_dependency("bd-23", "bd-12", type="discovered-from")]
Linked bd-23 as discovered from bd-12

Now I'll complete the auth bug fix...
```

## Implementation Guide

To implement this MCP server:

1. Create a Node.js/TypeScript project
2. Use the MCP SDK: `npm install @anthropic/mcp-sdk`
3. Implement tool handlers that call `bd` commands via child_process
4. Register with Claude Desktop's config

Example skeleton:

```typescript
import { Server } from "@anthropic/mcp-sdk";
import { exec } from "child_process";
import { promisify } from "util";

const execAsync = promisify(exec);

const server = new Server({
  name: "beads",
  version: "1.0.0"
});

// Register ready work tool
server.tool("beads_ready_work", async (params) => {
  const { stdout } = await execAsync(
    `bd ready --json --limit ${params.limit || 10}`
  );
  return JSON.parse(stdout);
});

// Register create issue tool
server.tool("beads_create_issue", async (params) => {
  const { stdout } = await execAsync(
    `bd create "${params.title}" -d "${params.description}" -p ${params.priority} -t ${params.type} --json`
  );
  return JSON.parse(stdout);
});

// ... more tools ...

server.start();
```

## Installation (Future)

```bash
# Install the MCP server
npm install -g beads-mcp-server

# Configure Claude Desktop
# Add to ~/Library/Application Support/Claude/claude_desktop_config.json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp-server",
      "args": []
    }
  }
}

# Restart Claude Desktop
```

## Alternative: Direct bd Usage

Until the MCP server is available, you can instruct Claude to use bd directly:

```markdown
# In your CLAUDE.md or project instructions:

We use Beads (bd) for issue tracking. Available commands:

- `bd ready --json` - Find ready work
- `bd create "title" -p 1 -t bug --json` - Create issue
- `bd update bd-1 --status in_progress --json` - Update status
- `bd dep add bd-2 bd-1 --type discovered-from` - Link issues
- `bd close bd-1 --reason "Done" --json` - Complete work

All commands support --json for parsing. Please use bd to track work during our sessions.
```

## Contributing

Interested in building this MCP server? We welcome contributions!

See [CONTRIBUTING.md](../../CONTRIBUTING.md) for guidelines.

## See Also

- [MCP Documentation](https://docs.anthropic.com/claude/docs/model-context-protocol)
- [MCP SDK](https://github.com/anthropics/mcp-sdk)
- [Claude Desktop](https://claude.ai/desktop)
- [../python-agent/](../python-agent/) - Python implementation pattern
