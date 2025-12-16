# beads-mcp

MCP server for [beads](https://github.com/steveyegge/beads) issue tracker and agentic memory system.
Enables AI agents to manage tasks using bd CLI through Model Context Protocol.

> **Note:** For environments with shell access (Claude Code, Cursor, Windsurf), the **CLI + hooks approach is recommended** over MCP. It uses ~1-2k tokens vs 10-50k for MCP schemas, resulting in lower compute cost and latency. See the [main README](../../README.md) for CLI setup.
>
> **Use this MCP server** for MCP-only environments like Claude Desktop where CLI access is unavailable.

## Installing

Install from PyPI:

```bash
# Using uv (recommended)
uv tool install beads-mcp

# Or using pip
pip install beads-mcp
```

Add to your Claude Desktop config:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

### Development Installation

For development, clone the repository:

```bash
git clone https://github.com/steveyegge/beads
cd beads/integrations/beads-mcp
uv sync
```

Then use in Claude Desktop config:

```json
{
  "mcpServers": {
    "beads": {
      "command": "uv",
      "args": [
        "--directory",
        "/path/to/beads-mcp",
        "run",
        "beads-mcp"
      ]
    }
  }
}
```

**Environment Variables** (all optional):
- `BEADS_USE_DAEMON` - Use daemon RPC instead of CLI (default: `1`, set to `0` to disable)
- `BEADS_PATH` - Path to bd executable (default: `~/.local/bin/bd`)
- `BEADS_DB` - Path to beads database file (default: auto-discover from cwd)
- `BEADS_WORKING_DIR` - Working directory for bd commands (default: `$PWD` or current directory). Used for multi-repo setups - see below
- `BEADS_ACTOR` - Actor name for audit trail (default: `$USER`)
- `BEADS_NO_AUTO_FLUSH` - Disable automatic JSONL sync (default: `false`)
- `BEADS_NO_AUTO_IMPORT` - Disable automatic JSONL import (default: `false`)

## Multi-Repository Setup

**Recommended:** Use a single MCP server instance for all beads projects - it automatically routes to per-project local daemons.

### Single MCP Server (Recommended)

**Simple config - works for all projects:**
```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp"
    }
  }
}
```

**How it works (LSP model):**
1. MCP server checks for local daemon socket (`.beads/bd.sock`) in your current workspace
2. Routes requests to the **per-project daemon** based on working directory
3. Auto-starts the local daemon if not running
4. **Each project gets its own isolated daemon** serving only its database

**Architecture:**
```
MCP Server (one instance)
    ↓
Per-Project Daemons (one per workspace)
    ↓
SQLite Databases (complete isolation)
```

**Why per-project daemons?**
- ✅ Complete database isolation between projects
- ✅ No cross-project pollution or git worktree conflicts
- ✅ Simpler mental model: one project = one database = one daemon
- ✅ Follows LSP (Language Server Protocol) architecture
- ✅ One MCP config works for unlimited projects

**Note:** Global daemon support was removed in v0.16.0 to prevent cross-project database pollution.

### Alternative: Per-Project MCP Instances (Not Recommended)

Configure separate MCP servers for specific projects using `BEADS_WORKING_DIR`:

```json
{
  "mcpServers": {
    "beads-webapp": {
      "command": "beads-mcp",
      "env": {
        "BEADS_WORKING_DIR": "/Users/yourname/projects/webapp"
      }
    },
    "beads-api": {
      "command": "beads-mcp",
      "env": {
        "BEADS_WORKING_DIR": "/Users/yourname/projects/api"
      }
    }
  }
}
```

⚠️ **Problem**: AI may select the wrong MCP server for your workspace, causing commands to operate on the wrong database. Use single MCP server instead.

## Multi-Project Support

The MCP server supports managing multiple beads projects in a single session using per-request workspace routing.

### Using `workspace_root` Parameter

Every tool accepts an optional `workspace_root` parameter for explicit project targeting:

```python
# Query issues from different projects concurrently
results = await asyncio.gather(
    beads_ready_work(workspace_root="/Users/you/project-a"),
    beads_ready_work(workspace_root="/Users/you/project-b"),
)

# Create issue in specific project
await beads_create_issue(
    title="Fix auth bug",
    priority=1,
    workspace_root="/Users/you/project-a"
)
```

### Architecture

**Connection Pool**: The MCP server maintains a connection pool keyed by canonical workspace path:
- Each workspace gets its own daemon socket connection
- Paths are canonicalized (symlinks resolved, git toplevel detected)
- Concurrent requests use `asyncio.Lock` to prevent race conditions
- No LRU eviction (keeps all connections open for session)

**ContextVar Routing**: Per-request workspace context is managed via Python's `ContextVar`:
- Each tool call sets the workspace for its duration
- Properly isolated for concurrent calls (no cross-contamination)
- Falls back to `BEADS_WORKING_DIR` if `workspace_root` not provided

**Path Canonicalization**:
- Symlinks are resolved to physical paths (prevents duplicate connections)
- Git submodules with `.beads` directories use local context
- Git toplevel is used for non-initialized directories
- Results are cached for performance

### Backward Compatibility

The `set_context()` tool still works and sets a default workspace:

```python
# Old way (still supported)
await set_context(workspace_root="/Users/you/project-a")
await beads_ready_work()  # Uses project-a

# New way (more flexible)
await beads_ready_work(workspace_root="/Users/you/project-a")
```

### Concurrency Gotchas

⚠️ **IMPORTANT**: Tool implementations must NOT spawn background tasks using `asyncio.create_task()`.

**Why?** ContextVar doesn't propagate to spawned tasks, which can cause cross-project data leakage.

**Solution**: Keep all tool logic synchronous or use sequential `await` calls.

### Troubleshooting

**Symlink aliasing**: Different paths to same project are deduplicated automatically via `realpath`.

**Submodule handling**: Submodules with their own `.beads` directory are treated as separate projects.

**Stale sockets**: Currently no health checks. Phase 2 will add retry-on-failure if monitoring shows need.

**Version mismatches**: Daemon version is auto-checked since v0.16.0. Mismatched daemons are automatically restarted.

## Features

**Resource:**
- `beads://quickstart` - Quickstart guide for using beads

**Tools (all support `workspace_root` parameter):**
- `init` - Initialize bd in current directory
- `create` - Create new issue (bug, feature, task, epic, chore)
- `list` - List issues with filters (status, priority, type, assignee)
- `ready` - Find tasks with no blockers ready to work on
- `show` - Show detailed issue info including dependencies
- `update` - Update issue (status, priority, design, notes, etc). Note: `status="closed"` or `status="open"` automatically route to `close` or `reopen` tools to respect approval workflows
- `close` - Close completed issue
- `dep` - Add dependency (blocks, related, parent-child, discovered-from)
- `blocked` - Get blocked issues
- `stats` - Get project statistics
- `reopen` - Reopen a closed issue with optional reason
- `set_context` - Set default workspace for subsequent calls (backward compatibility)

## Known Issues

### ~~MCP Tools Not Loading in Claude Code~~ (Issue [#346](https://github.com/steveyegge/beads/issues/346)) - RESOLVED

**Status:** ✅ Fixed in v0.24.0+

This issue affected versions prior to v0.24.0. The problem was caused by self-referential Pydantic models (`Issue` with `dependencies: list["Issue"]`) generating invalid MCP schemas with `$ref` at root level.

**Solution:** The issue was fixed in commit f3a678f by refactoring the data models:
- Created `IssueBase` with common fields
- Created `LinkedIssue(IssueBase)` for dependency references
- Changed `Issue` to use `list[LinkedIssue]` instead of `list["Issue"]`

This breaks the circular reference and ensures all tool outputSchemas have `type: object` at root level.

**Upgrade:** If you're running beads-mcp < 0.24.0:
```bash
pip install --upgrade beads-mcp
```

All MCP tools now load correctly in Claude Code with v0.24.0+.


## Development

Run MCP inspector:
```bash
# inside beads-mcp dir
uv run fastmcp dev src/beads_mcp/server.py
```

Type checking:
```bash
uv run mypy src/beads_mcp
```

Linting and formatting:
```bash
uv run ruff check src/beads_mcp
uv run ruff format src/beads_mcp
```

## Testing

Run all tests:
```bash
uv run pytest
```

With coverage:
```bash
uv run pytest --cov=beads_mcp tests/
```

Test suite includes both mocked unit tests and integration tests with real `bd` CLI.

### Multi-Repo Integration Test

Test daemon RPC with multiple repositories:
```bash
# Start the daemon first
cd /path/to/beads
./bd daemon --start

# Run multi-repo test
cd integrations/beads-mcp
uv run python test_multi_repo.py
```

This test verifies that the daemon can handle operations across multiple repositories simultaneously using per-request context routing.
