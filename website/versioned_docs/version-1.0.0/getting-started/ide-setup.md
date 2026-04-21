---
id: ide-setup
title: IDE Setup
sidebar_position: 3
---

# IDE Setup

Configure your editor for beads integration. All integrations use `bd prime` to inject ~1-2k tokens of workflow context on session start.

## Claude Code

```bash
bd setup claude
bd setup claude --check  # Verify
```

Installs SessionStart hook (`bd prime`) and PreCompact hook (`bd dolt push`).

## Cursor IDE

```bash
bd setup cursor
bd setup cursor --check  # Verify
```

Creates `.cursor/rules/beads.mdc` with beads-aware rules.

## Aider

```bash
bd setup aider
bd setup aider --check  # Verify
```

Creates/updates `.aider.conf.yml` with beads context.

## GitHub Copilot (VS Code)

Use the MCP server. Install with `uv tool install beads-mcp`, then create `.vscode/mcp.json`:

```json
{
  "servers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

See [GitHub Copilot Integration](/integrations/github-copilot) for platform-specific paths and detailed setup.

## MCP Server (Claude Desktop, no shell)

For environments without CLI access:

```bash
pip install beads-mcp
```

See [MCP Server](/integrations/mcp-server) for configuration.

## Git Hooks

Ensure git hooks are installed for auto-sync:

```bash
bd hooks install
bd info  # Shows warnings if hooks are outdated
```

## Verify Setup

```bash
bd version
bd doctor
bd hooks status
bd setup claude --check  # or cursor, aider
```
