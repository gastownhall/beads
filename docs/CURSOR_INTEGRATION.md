# Cursor Integration

Use beads with Cursor through a project rules file and optional MCP tools.

## Quick Start

```bash
bd setup cursor
bd setup cursor --check
```

Restart Cursor after setup so the rules file and MCP config load into new agent sessions.

## What Gets Installed

| File | Purpose |
|------|---------|
| `.cursor/rules/beads.mdc` | Always-applied agent safety rails and workflow |
| `.cursor/mcp.json` | Registers `bd mcp` as a native MCP tool server |

`bd setup cursor` writes the rules file on every run. For MCP, it merges a `beads` entry into `.cursor/mcp.json` and skips injection when:

- `mcpServers.beads` already exists, or
- any configured server already runs `bd mcp`

## Rules Content

The rules file is prohibition-first: every line prevents a documented agent failure mode. Full reference material lives in `bd prime`, which the rules tell the agent to run at session start.

## Customization

You can edit `.cursor/rules/beads.mdc` to add project-specific conventions. Re-run `bd setup cursor` to refresh the managed template.

If you use a custom beads MCP server (for example Jawnt), register it under the `beads` key in `.cursor/mcp.json` so `bd setup cursor` defers instead of adding a duplicate entry.

## Remove

```bash
bd setup cursor --remove
```

This removes the rules file and the `beads` MCP entry. Other MCP servers are preserved.

## Community Context

This integration answers the long-standing question in [Discussion #206](https://github.com/gastownhall/beads/discussions/206). See also [SETUP.md](SETUP.md#cursor-ide) for the full setup guide.
