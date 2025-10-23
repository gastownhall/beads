# beads-mcp

MCP server for [beads](https://github.com/steveyegge/beads) issue tracker and agentic memory system.
Enables AI agents to manage tasks using bd CLI through Model Context Protocol.

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

**New in v0.9.11:** Work across multiple beads projects seamlessly!

### Option 1: Global Daemon (Recommended)

Start a single daemon to serve all your projects:

```bash
# Start global daemon (serves all repos)
bd daemon --global
```

The MCP server automatically detects the global daemon and routes requests based on your working directory. No configuration changes needed!

**How it works:**
1. MCP server checks for local daemon socket (`.beads/bd.sock`) — on Windows this file contains the TCP endpoint metadata
2. Falls back to global daemon socket (`~/.beads/bd.sock`)
3. Routes requests to correct database based on working directory
4. Each project keeps its own database at `.beads/*.db`

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

### Option 2: Per-Project MCP Instances

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

Each instance will discover and use the database in its `BEADS_WORKING_DIR` path.

**Which should you use?**
- ✅ **Global daemon**: 3+ projects, better resource usage, automatic routing
- ✅ **Per-project instances**: 1-2 main projects, explicit control
- ✅ **Hybrid**: Run global daemon for convenience + per-project for main projects

## Features

**Resource:**
- `beads://quickstart` - Quickstart guide for using beads

**Tools:**
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
./bd daemon start

# Run multi-repo test
cd integrations/beads-mcp
uv run python test_multi_repo.py
```

This test verifies that the daemon can handle operations across multiple repositories simultaneously using per-request context routing.
