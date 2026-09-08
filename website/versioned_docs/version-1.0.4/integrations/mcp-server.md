---
id: mcp-server
title: MCP Server
sidebar_position: 2
---

# MCP Server

Use beads in MCP-only environments.

## When to Use MCP

Use MCP server when CLI is unavailable:
- Claude Desktop (no shell access)
- Sourcegraph Amp without shell
- Other MCP-only environments

**Prefer CLI + hooks** when shell is available - it's more context efficient.

## Installation

### Using uv (Recommended)

```bash
uv tool install beads-mcp
```

### Using pip

```bash
pip install beads-mcp
```

## Configuration

### Claude Desktop (macOS)

Add to `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

### Claude Desktop (Windows)

Add to `%APPDATA%\Claude\claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

### Sourcegraph Amp

Add to MCP settings:

```json
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

### VS Code / GitHub Copilot

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

**Note:** Requires VS Code 1.96+ with MCP support enabled.

See [GitHub Copilot Integration](/integrations/github-copilot) for complete setup guide.

## Available Tools

The MCP server exposes these tools:

| Tool | Description |
|------|-------------|
| `beads_create` | Create new issue |
| `beads_list` | List issues |
| `beads_show` | Show issue details |
| `beads_update` | Update issue |
| `beads_close` | Close issue |
| `beads_ready` | Show ready work |
| `beads_sync` | Sync to git |
| `beads_dep_add` | Add dependency |
| `beads_dep_tree` | Show dependency tree |

## Usage

Once configured, use naturally:

```
Create an issue for fixing the login bug with priority 1
```

The MCP server translates to appropriate `bd` commands.

## Trade-offs

| Aspect | CLI + Hooks | MCP Server |
|--------|-------------|------------|
| Context overhead | ~1-2k tokens | 10-50k tokens |
| Latency | Direct calls | MCP protocol |
| Setup | Hooks config | MCP config |
| Availability | Shell required | MCP environments |

## Troubleshooting

### Server won't start

Check if `beads-mcp` is in PATH:

```bash
which beads-mcp
```

If not found:

```bash
# Reinstall
pip uninstall beads-mcp
pip install beads-mcp
```

### Tools not appearing

1. Restart Claude Desktop
2. Check MCP config JSON syntax
3. Verify server path

### Permission errors

```bash
# Check directory permissions
ls -la .beads/

# Initialize if needed
bd init --quiet
```

## See Also

- [Claude Code](/integrations/claude-code) - CLI integration
- [Installation](/getting-started/installation) - Full install guide
