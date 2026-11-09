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

`bd setup junie` points Junie at the standalone `beads-mcp` server, launched
with `uvx`:

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

This needs:
- **`uvx` on the `PATH` Junie starts the server with.** It ships with
  [uv](https://docs.astral.sh/uv/getting-started/installation/). `bd setup junie`
  prints a note when it cannot find `uvx` on your shell's `PATH`; it still
  writes the config, since Junie may start the server with a different `PATH`.
- **Network access on first start.** `uvx` downloads `beads-mcp` and its
  Python dependencies the first time Junie starts it, then reuses its cache.
- **`bd` reachable from the server.** `beads-mcp` runs the `bd` CLI, so `bd`
  must be on the same `PATH`, or set `BEADS_PATH` to its full path in the
  entry's `env`.

If you installed `beads-mcp` yourself (`uv tool install beads-mcp` or
`pip install beads-mcp`), you can launch it directly instead of through `uvx`:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp",
      "args": []
    }
  }
}
```

See [MCP Server](/integrations/mcp-server) for the server's tool catalog,
environment variables and other install options.

### Updating an existing config

`bd setup junie` replaces the whole of `.junie/mcp/mcp.json` each time it
runs. Older bd releases wrote a `beads` entry that runs `bd mcp`, a command bd
does not have, so Junie could not start the server. If your file holds only
that entry, re-run `bd setup junie` to replace it. If you added other MCP
servers to the file, re-running would drop them: edit only the `beads` entry
by hand to the `uvx` / `beads-mcp` form above instead.

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
# Verify the config launches beads-mcp (not "bd mcp"); re-run
# bd setup junie or edit the beads entry if it does not
cat .junie/mcp/mcp.json

# Verify the launcher and bd are on PATH
command -v uvx
command -v bd
```

Junie starts the server with its own environment: if these commands work in
your shell but Junie still has no beads tools, make `uvx` and `bd` visible to
the IDE (for example by starting it from that shell) or put full paths in the
entry. `bd setup junie --check` only reports whether the guidelines and config
files exist; it does not read the config or start the server.

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
- Empty `.junie/mcp/` and `.junie/` directories (only real directories this
  removal emptied; a symlinked one stays)

## See Also

- [MCP Server](/integrations/mcp-server) - MCP server details
- [Claude Code](/integrations/claude-code) - Similar hook-based integration
- [IDE Setup](/getting-started/ide-setup) - Other editors
