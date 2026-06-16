---
id: cursor
title: Cursor
---

# Cursor Integration

Use Beads with Cursor through a project rules file and MCP tools.

```bash
bd setup cursor
bd setup cursor --check
```

The setup command creates:

- `.cursor/rules/beads.mdc` — always-applied agent safety rails and workflow
- `.cursor/mcp.json` — merged `bd mcp` MCP server entry (skipped if beads MCP is already configured)

Restart Cursor after setup so the rules file and MCP config load into new agent sessions.

## Remove

```bash
bd setup cursor --remove
```
