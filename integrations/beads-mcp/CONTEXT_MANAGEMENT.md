# Context Management for Multi-Repo Support

## Problem

MCP servers don't receive working directory context from AI clients (Claude Code/Amp), causing database routing issues:

1. MCP server process starts with its own CWD
2. `bd` uses tree-walking to discover databases based on CWD
3. Without correct CWD, `bd` discovers wrong database or falls back to `~/.beads`
4. Result: Issues get misrouted across repositories

## Current Implementation (Partial Solution)

We've added two new MCP tools to allow explicit context management:

### Tools

#### `set_context`
Sets the workspace root directory for all bd operations.

**Parameters:**
- `workspace_root` (string): Absolute path to workspace/project root directory

**Returns:**
Confirmation message with resolved paths (workspace root and database)

**Behavior:**
1. Resolves to git repo root if inside a git repository
2. Walks up directory tree to find `.beads/*.db`
3. Sets `BEADS_WORKING_DIR`, `BEADS_DB`, and `BEADS_CONTEXT_SET` environment variables

#### `where_am_i`
Shows current workspace context and database path for debugging.

**Returns:**
Current context information including workspace root, database path, and actor

### Write Operation Protection

All write operations (`create`, `update`, `close`, `reopen`, `dep`, `init`) are decorated with `@require_context`.

**Enforcement:** Only enforced when `BEADS_REQUIRE_CONTEXT=1` environment variable is set.
This allows backward compatibility while adding safety for multi-repo setups.

## Limitations

**Environment Variable Persistence:** FastMCP's architecture doesn't guarantee environment variables persist between tool calls. This means:

- `set_context` sets env vars for that tool call
- Subsequent tool calls may not see those env vars
- Context needs to be re-established for each session

## Recommended Usage

### For Single Repository (Current Default)
No changes needed. The MCP server works as before with auto-discovery.

### For Multiple Repositories (Future)

**Option 1: Explicit Database Path (Current Workaround)**
```json
{
  "mcpServers": {
    "beads-repo1": {
      "command": "uvx",
      "args": ["beads-mcp"],
      "env": {
        "BEADS_DB": "/path/to/repo1/.beads/prefix.db"
      }
    },
    "beads-repo2": {
      "command": "uvx",
      "args": ["beads-mcp"],
      "env": {
        "BEADS_DB": "/path/to/repo2/.beads/prefix.db"
      }
    }
  }
}
```

**Option 2: Client-Side Context Management (Future)**
AI clients would need to:
1. Call `set_context` at session start with workspace root
2. MCP protocol would need to support persistent session state

**Option 3: Dolt Server with RPC (Future - Path 1.5 from bd-105)**
- Add `cwd` parameter to Dolt server RPC protocol
- Server performs tree-walking per request
- MCP server passes workspace_root via RPC
- Benefits: Centralized routing, supports multiple contexts per server

**Option 4: Advanced Routing Server (Future - Path 2 from bd-105)**
For >50 repos:
- Dedicated routing server with repo->DB mappings
- MCP becomes thin shim
- Enables shared connection pooling, cross-repo queries

## Testing

The context management tools are tested in:
- `tests/test_mcp_server_integration.py`: MCP tool tests
- Manual testing: See `/tmp/test-repo-{1,2}` example

Run tests:
```bash
uv run pytest tests/test_mcp_server_integration.py -v
```

## Future Work

See [bd-105](https://github.com/steveyegge/beads/issues/105) for full architectural analysis and roadmap.

Priority: P0/P1 - Active data corruption risk in multi-repo setups.
