---
title: Junie
description: Set up beads for Junie, the JetBrains AI agent, with a guidelines file and an MCP server configuration
---

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
4. **Session ends** → Run `bd dolt push` to push changes to Dolt remote

## Configuration Files

### Guidelines (`.junie/guidelines.md`)

Contains workflow instructions that Junie reads automatically:
- Core workflow rules
- Command reference
- Issue types and priorities
- MCP tool documentation

### MCP Config (`.junie/mcp/mcp.json`)

<Warning>
`bd setup junie` currently writes an MCP config that invokes `bd mcp`, a
command that does not exist in current bd builds — that config will not
start a server. Until the recipe is fixed, point Junie at the standalone
`beads-mcp` server instead:
</Warning>

```json
{
  "mcpServers": {
    "beads": {
      "command": "uvx",
      "args": ["beads-mcp"]
    }
  }
}
```

See [MCP Server](/integrations/mcp-server) for the server's tool catalog
and other install options (pip/pipx).

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
bd update bd-42 --claim --json

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
bd dolt push
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

### Push Before Session End

```bash
# ALWAYS run before ending
bd dolt push
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
# Verify MCP config exists and points at the beads-mcp server
cat .junie/mcp/mcp.json

# Verify the server package is installed
pip show beads-mcp
```

### Changes not syncing

```bash
# Force push
bd dolt push

# Check system health
bd doctor
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
