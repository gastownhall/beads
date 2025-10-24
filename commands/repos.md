---
description: DEPRECATED - Multi-repository management
argument-hint: [command]
---

**DEPRECATED:** This command is no longer functional.

Global daemon support was removed in v0.16.0. bd now uses per-project local daemons (LSP model) for complete database isolation.

## Why Was This Removed?

- Cross-project database pollution risks
- Git worktree conflicts
- Complexity in multi-workspace scenarios

## Multi-Repo Workflows Now

For working across multiple beads projects:
- Use your editor/shell to switch between project directories
- Each project has its own daemon at `.beads/bd.sock`
- Run `bd ready` in each project individually
- Use single MCP server instance that routes to per-project daemons

See [ADVANCED.md](../ADVANCED.md#architecture-daemon-vs-mcp-vs-beads) for architecture details.
