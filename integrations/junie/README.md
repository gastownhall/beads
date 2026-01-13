# Junie Integration for Beads

Integration for [Junie](https://www.jetbrains.com/junie/) (JetBrains AI Agent) with beads issue tracking.

## Prerequisites

```bash
# Install beads
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash

# Initialize beads in your project
bd init
```

## Installation

```bash
bd setup junie
```

This creates:
- `.junie/guidelines.md` - Agent instructions for beads workflow
- `.junie/mcp/mcp.json` - MCP server configuration

## What Gets Installed

### Guidelines (`.junie/guidelines.md`)

Junie automatically reads this file on session start. It contains:
- Core workflow rules for using beads
- Command reference for the `bd` CLI
- Issue types and priorities
- MCP tool documentation

### MCP Config (`.junie/mcp/mcp.json`)

Configures the beads MCP server so Junie can use beads tools directly:

```json
{
  "mcpServers": {
    "beads": {
      "command": "bd",
      "args": ["mcp"]
    }
  }
}
```

## Usage

Once installed, Junie will:
1. Read workflow instructions from `.junie/guidelines.md`
2. Have access to beads MCP tools for direct issue management
3. Be able to use `bd` CLI commands

### MCP Tools Available

- `mcp_beads_ready` - Find tasks ready for work
- `mcp_beads_list` - List issues with filters
- `mcp_beads_show` - Show issue details
- `mcp_beads_create` - Create new issues
- `mcp_beads_update` - Update issue status/priority
- `mcp_beads_close` - Close completed issues
- `mcp_beads_dep` - Manage dependencies
- `mcp_beads_blocked` - Show blocked issues
- `mcp_beads_stats` - Get issue statistics

## Verification

```bash
bd setup junie --check
```

## Removal

```bash
bd setup junie --remove
```

## Related

- `bd prime` - Get full workflow context
- `bd ready` - Find unblocked work
- `bd sync` - Sync changes to git (run at session end)

## License

Same as beads (see repository root).
