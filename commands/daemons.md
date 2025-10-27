# bd daemons - Daemon Management

Manage bd daemon processes across all repositories and worktrees.

## Synopsis

```bash
bd daemons <subcommand> [flags]
```

## Description

The `bd daemons` command provides tools for discovering, monitoring, and managing multiple bd daemon processes across your system. This is useful when working with multiple repositories or git worktrees.

## Subcommands

### list

List all running bd daemons with metadata.

```bash
bd daemons list [--search DIRS] [--json] [--no-cleanup]
```

**Flags:**
- `--search` - Directories to search for daemons (default: home, /tmp, cwd)
- `--json` - Output in JSON format
- `--no-cleanup` - Skip auto-cleanup of stale sockets

**Example:**
```bash
bd daemons list
bd daemons list --search /Users/me/projects --json
```

### health

Check health of all bd daemons and report issues.

```bash
bd daemons health [--search DIRS] [--json]
```

Reports:
- Stale sockets (dead processes)
- Version mismatches between daemon and CLI
- Unresponsive daemons

**Flags:**
- `--search` - Directories to search for daemons
- `--json` - Output in JSON format

**Example:**
```bash
bd daemons health
bd daemons health --json
```

### stop

Stop a specific daemon gracefully.

```bash
bd daemons stop <workspace-path|pid> [--json]
```

**Arguments:**
- `<workspace-path|pid>` - Workspace path or PID of daemon to stop

**Flags:**
- `--json` - Output in JSON format

**Example:**
```bash
bd daemons stop /Users/me/projects/myapp
bd daemons stop 12345
bd daemons stop /Users/me/projects/myapp --json
```

### logs

View logs for a specific daemon.

```bash
bd daemons logs <workspace-path|pid> [-f] [-n LINES] [--json]
```

**Arguments:**
- `<workspace-path|pid>` - Workspace path or PID of daemon

**Flags:**
- `-f, --follow` - Follow log output (like tail -f)
- `-n, --lines INT` - Number of lines to show from end (default: 50)
- `--json` - Output in JSON format

**Example:**
```bash
bd daemons logs /Users/me/projects/myapp
bd daemons logs 12345 -n 100
bd daemons logs /Users/me/projects/myapp -f
bd daemons logs 12345 --json
```

### killall

Stop all running bd daemons.

```bash
bd daemons killall [--search DIRS] [--force] [--json]
```

Uses escalating shutdown strategy:
1. RPC shutdown (2 second timeout)
2. SIGTERM (3 second timeout)
3. SIGKILL (1 second timeout)

**Flags:**
- `--search` - Directories to search for daemons
- `--force` - Use SIGKILL immediately if graceful shutdown fails
- `--json` - Output in JSON format

**Example:**
```bash
bd daemons killall
bd daemons killall --force
bd daemons killall --json
```

## Common Use Cases

### Version Upgrade

After upgrading bd, restart all daemons to use the new version:

```bash
bd daemons health  # Check for version mismatches
bd daemons killall # Stop all old daemons
# Daemons will auto-start with new version on next bd command
```

### Debugging

Check daemon status and view logs:

```bash
bd daemons list
bd daemons health
bd daemons logs /path/to/workspace -n 100
```

### Cleanup

Remove stale daemon sockets:

```bash
bd daemons list  # Auto-cleanup happens by default
bd daemons list --no-cleanup  # Skip cleanup
```

### Multi-Workspace Management

Discover daemons in specific directories:

```bash
bd daemons list --search /Users/me/projects
bd daemons health --search /Users/me/work
```

## Troubleshooting

### Stale Sockets

If you see stale sockets (dead process but socket file exists):

```bash
bd daemons list  # Auto-cleanup removes stale sockets
```

### Version Mismatch

If daemon version != CLI version:

```bash
bd daemons health  # Identify mismatched daemons
bd daemons killall # Stop all daemons
# Next bd command will auto-start new version
```

### Daemon Won't Stop

If graceful shutdown fails:

```bash
bd daemons killall --force  # Force kill with SIGKILL
```

### Can't Find Daemon

If daemon isn't discovered:

```bash
bd daemons list --search /path/to/workspace
```

Or check the socket manually:

```bash
ls -la /path/to/workspace/.beads/bd.sock
```

## See Also

- [bd daemon](daemon.md) - Start a daemon manually
- [AGENTS.md](../AGENTS.md) - Agent workflow guide
- [README.md](../README.md) - Main documentation
