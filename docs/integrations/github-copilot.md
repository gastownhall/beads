---
title: GitHub Copilot
description: Use beads from Copilot Chat in VS Code via the beads-mcp server to track issues in natural language
---

Beads gives Copilot a persistent, structured memory for tracking work: with the MCP server configured, you create, update, and track issues in natural language without leaving the editor.

This page covers **Copilot Chat in VS Code via MCP**. For the terminal-based Copilot CLI plugin installed by `bd setup copilot`, see [Copilot CLI](/integrations/copilot-cli).

## Prerequisites

- VS Code 1.96+ with the GitHub Copilot extension
- A GitHub Copilot subscription (Individual, Business, or Enterprise)
- The beads CLI installed ([installation guide](/getting-started/installation))
- Python 3.10+ or the `uv` package manager

## Setup

### Quick Setup

1. Install beads-mcp:
   ```bash
   # Using uv (recommended)
   uv tool install beads-mcp

   # Or using pip / pipx
   pip install beads-mcp
   pipx install beads-mcp
   ```

2. Create `.vscode/mcp.json` in your project:
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

3. Initialize beads:
   ```bash
   bd init --quiet
   ```

   This creates a `.beads/` directory with the issue database.

4. Reload VS Code

### Verify Setup

Ask Copilot Chat: "What beads issues are ready to work on?"

## Using Natural Language

With MCP configured, interact naturally:

```
You: Create a bug for the login timeout
Copilot: Created bd-42: Login timeout bug

You: What issues are ready?
Copilot: 3 issues ready: bd-42, bd-99, bd-17

You: Claim bd-42, I'll take it
Copilot: Claimed bd-42 and started work

You: I found a related bug - the session token isn't refreshed.
     File it, linked to bd-42.
Copilot: Created bd-103: Session token not refreshed
         Linked as discovered-from bd-42

You: Close bd-42 with reason "Fixed timeout handling"
Copilot: Closed bd-42: Fixed timeout handling
```

Syncing stays on the CLI: run `bd dolt push` at the end of a session. There is no MCP push tool.

## MCP Tools

| Tool | Description | You say |
|------|-------------|---------|
| `ready` | List unblocked issues | "What can I work on?" |
| `list` | List issues with filters | "Show all open bugs" |
| `show` | Show issue details, including dependencies and dependents | "Show bd-42 details" |
| `create` | Create new issue | "Create a task for refactoring" |
| `claim` | Atomically claim an issue (assignee + in_progress) | "I'll take bd-42" |
| `update` | Update issue fields | "Set bd-42 to priority 1" |
| `close` | Close an issue | "Complete bd-42" |
| `dep` | Add dependency | "bd-99 blocks bd-42" |
| `blocked` | Show blocked issues and their blockers | "What's blocking my work?" |
| `stats` | Issue counts and average lead time | "How's the backlog?" |

The server also exposes `reopen`, `comment`, `comments`, `note`, `context`, and `admin`; call `discover_tools` for the full catalog.

## Copilot Instructions

Optionally add `.github/copilot-instructions.md`:

```markdown
## Issue Tracking

This project uses **bd (beads)** for issue tracking.
Run `bd prime` for workflow context.

Quick reference:
- `bd ready` - Find unblocked work
- `bd create "Title" --type task --priority 2` - Create issue
- `bd close <id>` - Complete work
- `bd dolt push` - Push changes to Dolt remote (run at session end)
```

## CLI vs MCP

| Approach | Best for | Trade-off |
|----------|----------|-----------|
| **MCP (Copilot Chat)** | Natural language, discovery | Higher token overhead |
| **CLI (terminal)** | Scripting, precision, speed | Requires shell access |

Both work against the same database - use MCP for conversational work, the CLI for quick commands. See [MCP Server](/integrations/mcp-server) for the full trade-off discussion.

## Troubleshooting

### Tools not appearing

1. Check VS Code 1.96+
2. Verify mcp.json syntax is valid JSON
3. Reload VS Code window
4. Check Output panel for MCP errors

### "beads-mcp not found"

```bash
# Check installation
which beads-mcp
pip show beads-mcp

# uv installs to ~/.local/bin - make sure it's on PATH
export PATH="$HOME/.local/bin:$PATH"

# If installed with pip, find it
pip show beads-mcp | grep Location

# Reinstall if needed
uv tool install beads-mcp --force
```

### No database found

```bash
bd init --quiet
```

### Changes not persisting

Push to the Dolt remote at the end of your session, from the terminal:

```bash
bd dolt push
```

### Organization policies blocking MCP

For Copilot Business/Enterprise, your organization must enable the "MCP servers in Copilot" policy. Contact your admin if MCP tools don't appear despite a correct config.

## FAQ

### Do I need to clone beads?

**No.** Beads is a system-wide CLI tool. Install once, use everywhere. The `.beads/` directory in your project only contains the issue database.

### What about git hooks?

Git hooks are optional. They refresh exports and legacy fallback checks, while issue sync uses `bd dolt push` / `bd dolt pull`. They never modify your source code; skip them with `bd init --skip-hooks`.

### Can I use beads without Copilot?

Yes. The same database works from the terminal, [Claude Code](/integrations/claude-code), [Cursor](/integrations/cursor), [Aider](/integrations/aider), and any editor with MCP or shell access.

### Does this work with Copilot in other editors?

This page covers VS Code. For JetBrains IDEs, check whether your IDE supports MCP; the config location differs. For Neovim, use the CLI directly. For the terminal, see [Copilot CLI](/integrations/copilot-cli).

## See Also

- [MCP Server](/integrations/mcp-server) - Detailed MCP configuration
- [Copilot CLI](/integrations/copilot-cli) - Terminal-based Copilot integration
- [Quickstart](/getting-started/quickstart) - bd command basics
- [Installation](/getting-started/installation) - Full install guide
- [Agent Instructions](https://github.com/gastownhall/beads/blob/main/AGENT_INSTRUCTIONS.md) - Full agent workflow reference
