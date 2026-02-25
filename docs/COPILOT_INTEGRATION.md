# GitHub Copilot Integration Guide

This guide explains how to use beads with GitHub Copilot in VS Code.

## Overview

Beads provides a persistent, structured memory for coding agents through the MCP (Model Context Protocol) server. With Copilot, you can use natural language to create, update, and track issues without leaving your editor.

**Important:** Beads is a system-wide CLI tool. You install it once and use it in any project. Do NOT clone the beads repository into your project.

## Prerequisites

- VS Code 1.96+ with GitHub Copilot extension
- GitHub Copilot subscription (Individual, Business, or Enterprise)
- beads CLI installed (`brew install beads` or `npm install -g @beads/bd`)
- Python 3.10+ OR uv package manager

## Quick Setup

### Step 1: Install beads-mcp

```bash
# Using uv (recommended)
uv tool install beads-mcp

# Or using pip
pip install beads-mcp

# Or using pipx
pipx install beads-mcp
```

### Step 2: Configure VS Code MCP

Create or edit `.vscode/mcp.json` in your project:

```json
{
  "servers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**For all projects:** Add to VS Code user-level MCP config:

| Platform | Path |
|----------|------|
| macOS | `~/Library/Application Support/Code/User/mcp.json` |
| Linux | `~/.config/Code/User/mcp.json` |
| Windows | `%APPDATA%\Code\User\mcp.json` |

```json
{
  "servers": {
    "beads": {
      "command": "beads-mcp",
      "args": []
    }
  }
}
```

### Step 3: Initialize beads in your project

```bash
cd your-project
bd init --quiet
```

This creates a `.beads/` directory with the issue database. The init wizard will ask about git hooks—these are optional and you can skip them if unfamiliar.

### Step 4: Add Copilot instructions (optional but recommended)

Create `.github/copilot-instructions.md`:

```markdown
## Issue Tracking

This project uses **bd (beads)** for issue tracking.
Run `bd prime` for workflow context.

**Quick reference:**
- `bd ready` - Find unblocked work
- `bd create "Title" --type task --priority 2` - Create issue
- `bd close <id>` - Complete work
- `bd sync` - Sync with git (run at session end)
```

### Step 5: Restart VS Code

Reload the VS Code window for MCP configuration to take effect.

## Using Beads with Copilot

### Natural Language Commands

With MCP configured, ask Copilot Chat:

| You say | Copilot does |
|---------|--------------|
| "What issues are ready to work on?" | Calls `beads_ready` |
| "Create a bug for the login timeout" | Calls `beads_create` with type=bug |
| "Show me issue bd-42" | Calls `beads_show` |
| "Mark bd-42 as complete" | Calls `beads_close` |
| "What's blocking bd-15?" | Calls `beads_dep_tree` |

### MCP Tools Reference

| Tool | Description | Example |
|------|-------------|---------|
| `beads_ready` | List unblocked issues | "What can I work on?" |
| `beads_list` | List issues with filters | "Show all open bugs" |
| `beads_create` | Create new issue | "Create a task for refactoring" |
| `beads_show` | Show issue details | "Show bd-42 details" |
| `beads_update` | Update issue fields | "Set bd-42 to in progress" |
| `beads_close` | Close an issue | "Complete bd-42" |
| `beads_sync` | Sync to git | "Sync my changes" |
| `beads_dep_add` | Add dependency | "bd-99 blocks bd-42" |
| `beads_dep_tree` | Show dependency tree | "What depends on bd-42?" |

### Example Workflow

```
You: What issues are ready to work on?

Copilot: [Calls beads_ready]
There are 3 issues ready:
1. [P1] bd-42: Fix authentication timeout
2. [P2] bd-99: Add password reset flow
3. [P3] bd-17: Update API documentation

You: Let me work on bd-42. Claim it.

Copilot: [Calls beads_claim]
Claimed bd-42 and started work.

You: [... work on the code ...]

You: I found a related bug - the session token isn't being refreshed.
     Create a bug for that, linked to bd-42.

Copilot: [Calls beads_create]
Created bd-103: Session token not refreshed
Linked as discovered-from bd-42.

You: Done with bd-42. Close it with reason "Fixed timeout handling"

Copilot: [Calls beads_close]
Closed bd-42: Fixed timeout handling

You: Sync everything to git

Copilot: [Calls beads_sync]
Synced: 2 issues updated, committed to git.
```

## CLI vs MCP: When to Use Each

| Approach | Best For | Trade-offs |
|----------|----------|------------|
| **MCP (Copilot Chat)** | Natural language, discovery | Higher token overhead |
| **CLI (Terminal)** | Scripting, precision, speed | Requires terminal context |

You can use both! MCP for conversational work, CLI for quick commands.

## Troubleshooting

### MCP tools not appearing in Copilot

1. **Check VS Code version** - MCP requires VS Code 1.96+
2. **Verify mcp.json syntax** - JSON must be valid
3. **Check beads-mcp is installed:**
   ```bash
   which beads-mcp
   beads-mcp --version
   ```
4. **Reload VS Code** - MCP config requires window reload
5. **Check Output panel** - Look for MCP-related errors

### "beads-mcp: command not found"

The MCP server isn't in your PATH:

```bash
# If installed with uv
export PATH="$HOME/.local/bin:$PATH"

# If installed with pip, find it
pip show beads-mcp | grep Location

# Reinstall if needed
uv tool install beads-mcp --force
```

### "No beads database found"

Initialize beads in your project:

```bash
cd your-project
bd init --quiet
```

### Changes not persisting

Run sync at end of session:

```bash
bd sync
```

Or ask Copilot: "Sync my beads changes to git"

### Organization policies blocking MCP

For Copilot Enterprise, your organization must enable "MCP servers in Copilot" policy. Contact your admin if MCP tools don't appear.

## FAQ

### Do I need to clone the beads repository?

**No.** Beads is a system-wide CLI tool. You install it once (via Homebrew, npm, or pip) and use it in any project. The `.beads/` directory in your project only contains the issue database, not beads itself.

### What are the git hooks and are they safe?

When you run `bd init`, beads can install git hooks that:
- **post-merge**: Import issues when you pull
- **pre-push**: Sync issues before you push

These hooks are safe—they only read/write the `.beads/` directory and never modify your code. You can opt out with `bd init --no-hooks` or skip them during the interactive setup.

### Can I use beads without Copilot?

Yes! Beads works with:
- Terminal (direct CLI)
- Claude Code
- Cursor
- Aider
- Any editor with MCP or shell access

### MCP vs CLI - which should I use?

Use **MCP** when you want natural language interaction through Copilot Chat.
Use **CLI** when you want speed, scripting, or precise control.

Both approaches work with the same database—use whichever fits your workflow.

### Does this work with Copilot in other editors?

This guide is for VS Code. For other editors:
- **JetBrains IDEs**: Check if MCP is supported, config may differ
- **Neovim**: Use CLI integration instead

## See Also

- [MCP Server Documentation](../website/docs/integrations/mcp-server.md)
- [CLI Reference](QUICKSTART.md)
- [Installation Guide](INSTALLING.md)
- [Agent Instructions](../AGENT_INSTRUCTIONS.md)
