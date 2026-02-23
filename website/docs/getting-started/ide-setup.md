---
id: ide-setup
title: IDE Setup
sidebar_position: 3
---

# IDE Setup for AI Agents

Configure your IDE for optimal beads integration.

## Claude Code

The recommended approach for Claude Code:

```bash
# Setup Claude Code integration
bd setup claude
```

This installs:
- **SessionStart hook** - Runs `bd prime` when Claude Code starts
- **PreCompact hook** - Ensures `bd sync` before context compaction

**How it works:**
1. SessionStart hook runs `bd prime` automatically
2. `bd prime` injects ~1-2k tokens of workflow context
3. You use `bd` CLI commands directly
4. Git hooks auto-sync the database

**Verify installation:**
```bash
bd setup claude --check
```

### Manual Setup

If you prefer manual configuration, add to your Claude Code hooks:

```json
{
  "hooks": {
    "SessionStart": ["bd prime"],
    "PreCompact": ["bd sync"]
  }
}
```

## Cursor IDE

```bash
# Setup Cursor integration
bd setup cursor
```

This creates `.cursor/rules/beads.mdc` with beads-aware rules.

**Verify:**
```bash
bd setup cursor --check
```

## Aider

```bash
# Setup Aider integration
bd setup aider
```

This creates/updates `.aider.conf.yml` with beads context.

**Verify:**
```bash
bd setup aider --check
```

## GitHub Copilot

For VS Code with GitHub Copilot, use the MCP server:

```bash
# Install MCP server
uv tool install beads-mcp
```

Create `.vscode/mcp.json` in your project:

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

Initialize beads and reload VS Code:

```bash
bd init --quiet
```

See [GitHub Copilot Integration](/integrations/github-copilot) for detailed setup.

## Context Injection with `bd prime`

All integrations use `bd prime` to inject context:

```bash
bd prime
```

This outputs a compact (~1-2k tokens) workflow reference including:
- Available commands
- Current project status
- Workflow patterns
- Best practices

**Why context efficiency matters:**
- Compute cost scales with tokens
- Latency increases with context size
- Models attend better to smaller, focused contexts

## MCP Server (Alternative)

For MCP-only environments (Claude Desktop, no shell access):

```bash
# Install MCP server
pip install beads-mcp
```

Add to Claude Desktop config:
```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**Trade-offs:**
- Works in MCP-only environments
- Higher context overhead (10-50k tokens for tool schemas)
- Additional latency from MCP protocol

See [MCP Server](/integrations/mcp-server) for detailed configuration.

## Git Hooks

Ensure git hooks are installed for auto-sync:

```bash
bd hooks install
```

This installs:
- **pre-commit** - Validates changes before commit
- **post-merge** - Imports changes after pull
- **pre-push** - Ensures sync before push

**Check hook status:**
```bash
bd info  # Shows warnings if hooks are outdated
```

## Verifying Your Setup

Run a complete health check:

```bash
# Check version
bd version

# Check project health
bd doctor

# Check hooks
bd hooks status

# Check editor integration
bd setup claude --check   # or cursor, aider
```
