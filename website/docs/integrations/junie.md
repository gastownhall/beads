---
id: junie
title: Junie
sidebar_position: 4
---

# Junie Integration

How to use beads with Junie (JetBrains AI Agent).

## Setup

### Quick Setup

```bash
bd setup junie
```

This creates:
- **`.junie/guidelines.md`** - Agent instructions for beads workflow
- **`.junie/mcp/mcp.json`** - MCP server configuration

### Verify Setup

```bash
bd setup junie --check
```

## How It Works

1. **Session starts** → Junie reads `.junie/guidelines.md` for workflow context
2. **MCP tools available** → Junie can use beads MCP tools directly
3. **You work** → Use `bd` CLI commands or MCP tools
4. **Session ends** → Run `bd sync` to save work to git

## Configuration Files

### Guidelines (`.junie/guidelines.md`)

Contains workflow instructions that Junie reads automatically:
- Core workflow rules
- Command reference
- Issue types and priorities
- MCP tool documentation

### MCP Config (`.junie/mcp/mcp.json`)

Configures the beads MCP server:

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

## MCP Tools

With MCP configured, Junie can use these tools directly:

| Tool | Description |
| --- | --- |
| `mcp_beads_ready` | Find tasks ready for work |
| `mcp_beads_list` | List issues with filters |
| `mcp_beads_show` | Show issue details |
| `mcp_beads_create` | Create new issues |
| `mcp_beads_update` | Update issue status/priority |
| `mcp_beads_close` | Close completed issues |
| `mcp_beads_dep` | Manage dependencies |
| `mcp_beads_blocked` | Show blocked issues |
| `mcp_beads_stats` | Get issue statistics |

## CLI Commands

You can also use the `bd` CLI directly:

### Creating Issues

```bash
# Always include description for context
bd create "Fix authentication bug" \
  --description="Login fails with special characters in password" \
  -t bug -p 1 --json

# Link discovered issues
bd create "Found SQL injection" \
  --description="User input not sanitized in query builder" \
  --deps discovered-from:bd-42 --json
```

### Working on Issues

```bash
# Find ready work
bd ready --json

# Start work
bd update bd-42 --status in_progress --json

# Complete work
bd close bd-42 --reason "Fixed in commit abc123" --json
```

### Querying

```bash
# List open issues
bd list --status open --json

# Show issue details
bd show bd-42 --json

# Check blocked issues
bd blocked --json
```

### Syncing

```bash
# ALWAYS run at session end
bd sync
```

## Best Practices

### Always Use `--json`

```bash
bd list --json          # Parse programmatically
bd create "Task" --json # Get issue ID from output
bd show bd-42 --json    # Structured data
```

### Always Include Descriptions

```bash
# Good
bd create "Fix auth bug" \
  --description="Login fails when password contains quotes" \
  -t bug -p 1 --json

# Bad - no context for future work
bd create "Fix auth bug" -t bug -p 1 --json
```

### Link Related Work

```bash
# When you discover issues during work
bd create "Found related bug" \
  --deps discovered-from:bd-current --json
```

### Sync Before Session End

```bash
# ALWAYS run before ending
bd sync
```

## Troubleshooting

### Guidelines not loaded

```bash
# Check setup
bd setup junie --check

# Reinstall if needed
bd setup junie
```

### MCP tools not available

```bash
# Verify MCP config exists
cat .junie/mcp/mcp.json

# Test MCP server
bd mcp --help
```

### Changes not syncing

```bash
# Force sync
bd sync

# Check daemon
bd info
bd daemons health
```

### Database not found

```bash
# Initialize beads
bd init --quiet
```

## Removing Integration

```bash
bd setup junie --remove
```

This removes:
- `.junie/guidelines.md`
- `.junie/mcp/mcp.json`
- Empty `.junie/mcp/` and `.junie/` directories

## See Also

- [MCP Server](/integrations/mcp-server) - MCP server details
- [Claude Code](/integrations/claude-code) - Similar hook-based integration
- [IDE Setup](/getting-started/ide-setup) - Other editors
