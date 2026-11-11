# Junie Integration for Beads

Integration for [Junie](https://www.jetbrains.com/junie/) (JetBrains AI Agent) with beads issue tracking.

## Prerequisites

```bash
# Install beads
curl -fsSL https://raw.githubusercontent.com/gastownhall/beads/main/scripts/install.sh | bash

# Initialize beads in your project
bd init
```

For the MCP server, Junie also needs `uvx` from
[uv](https://docs.astral.sh/uv/getting-started/installation/) on the `PATH` it
starts the server with.

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

Configures the beads MCP server (the `beads-mcp` package, launched with `uvx`
from [uv](https://docs.astral.sh/uv/)) so Junie can use beads tools directly:

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

`uvx` downloads `beads-mcp` the first time Junie starts it, and the server runs
the `bd` CLI, so `bd` must be on the same `PATH`. `bd setup junie` prints a note
when it cannot find `uvx` on your shell's `PATH`, but writes the config anyway.
If you installed `beads-mcp` yourself (`uv tool install beads-mcp` or
`pip install beads-mcp`), set `"command": "beads-mcp"` and `"args": []` instead.

bd v1.3.4 and earlier write a `beads` entry that runs `bd mcp`, a command bd
does not have. With bd v1.4.0 or later, re-running `bd setup junie` replaces
it, but the re-run rewrites both files whole: other MCP servers you added to
`mcp.json` and your edits to `.junie/guidelines.md` are lost, and a symlinked
`guidelines.md` stops it before it reaches `mcp.json`. If you changed either
file, or run v1.3.4 or earlier, edit only the `beads` entry by hand to the
form above instead.

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

`--check` reports whether both files exist; it does not read the MCP config or
start the server. See the
[Junie integration guide](../../docs/integrations/junie.md#troubleshooting)
for MCP troubleshooting.

## Removal

```bash
bd setup junie --remove
```

## Related

- `bd prime` - Get full workflow context
- `bd ready` - Find unblocked work
- `bd dolt push` - Push issue changes to Dolt remote (run at session end)

## License

Same as beads (see repository root).
