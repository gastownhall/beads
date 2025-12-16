# Setting Up Daemon-Based MCP Server

## Quick Start

Replace your multiple MCP server configs with a single daemon-based one:

### 1. Claude Desktop Config

Edit `~/Library/Application Support/Claude/claude_desktop_config.json`:

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp",
      "env": {
        "BEADS_USE_DAEMON": "1"
      }
    }
  }
}
```

**Remove old configs** like `beads-wyvern`, `beads-adar`, etc.

### 2. Start the Daemon

In your beads project directory:

```bash
bd daemon --start
```

The daemon will:
- Listen on `.beads/bd.sock` (Windows: file stores loopback TCP metadata)
- Route operations to correct database based on request cwd
- Handle multiple repos simultaneously

### 3. Test It

```bash
# Test with beads repo
cd ~/src/vc/adar/beads
bd list

# Test with another repo
cd ~/src/vc/wyvern
bd list

# Both should show correct issues for their respective databases
```

### 4. Restart Claude Desktop

After updating the config, restart Claude Desktop to load the new MCP server configuration.

## How It Works

```
Claude/Amp → Single MCP Server → Daemon Client → Daemon → Correct Database
                  ↓ 
          Uses set_context to pass workspace_root
                  ↓
          Daemon uses cwd to find .beads/*.db
```

- **No more multiple MCP servers** - one server handles all repos
- **Per-request routing** - daemon finds correct database for each operation
- **Automatic fallback** - if daemon not running, falls back to CLI mode
- **Concurrent access** - daemon handles multiple repos at once

## Advanced Configuration

### Optional Environment Variables

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp",
      "env": {
        "BEADS_USE_DAEMON": "1",
        "BEADS_REQUIRE_CONTEXT": "1",
        "BEADS_ACTOR": "claude"
      }
    }
  }
}
```

- `BEADS_USE_DAEMON` - Use daemon (default: `1`)
- `BEADS_REQUIRE_CONTEXT` - Enforce set_context before writes (default: `0`)
- `BEADS_ACTOR` - Actor name for audit trail (default: `$USER`)

### Disable Daemon (Fallback to CLI)

If you want to temporarily use CLI mode:

```json
{
  "env": {
    "BEADS_USE_DAEMON": "0"
  }
}
```

## Daemon Management

```bash
# Start daemon
bd daemon --start

# Check status
bd daemon --status

# View logs
bd daemons logs .

# Stop daemon
bd daemon --stop

# Restart daemon
bd daemon --stop && bd daemon --start
```

## Troubleshooting

### "Daemon not running" errors

Start the daemon in your beads project:
```bash
cd ~/src/vc/adar/beads
bd daemon --start
```

### Wrong database being used

1. Check where daemon is running:
   ```bash
   bd daemon --status
   ```

2. Use `set_context` tool in Claude to set workspace root:
   ```
   set_context /path/to/your/project
   ```

3. Verify with `where_am_i` tool

### Multiple repos not working

Ensure:
- Daemon is running in a parent directory that can reach all repos
- Each repo has `.beads/*.db` properly initialized
- MCP server is passing correct workspace_root via `set_context`

## Migration from Multi-Server Setup

### Old Config (Remove This)

```json
{
  "mcpServers": {
    "beads-adar": {
      "command": "beads-mcp",
      "env": {
        "BEADS_DB": "/path/to/adar/.beads/bd.db"
      }
    },
    "beads-wyvern": {
      "command": "beads-mcp",
      "env": {
        "BEADS_DB": "/path/to/wyvern/.beads/wy.db"
      }
    }
  }
}
```

### New Config (Use This)

```json
{
  "mcpServers": {
    "beads": {
      "command": "beads-mcp",
      "env": {
        "BEADS_USE_DAEMON": "1"
      }
    }
  }
}
```

## Benefits

✅ Single MCP server for all repos
✅ No manual BEADS_DB configuration per repo
✅ Automatic context switching
✅ Better performance (no process spawning per operation)
✅ Concurrent multi-repo operations
✅ Simpler configuration
