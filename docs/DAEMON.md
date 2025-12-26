# Daemon Management Guide

**For:** AI agents and developers managing bd background processes
**Version:** 0.21.0+

## Overview

bd runs a background daemon per workspace for auto-sync, RPC operations, and real-time monitoring. This guide covers daemon management, event-driven mode, and troubleshooting.

## Do I Need the Daemon?

**TL;DR:** For most users, the daemon runs automatically and you don't need to think about it.

### When Daemon Helps (default: enabled)

| Scenario | Benefit |
|----------|---------|
| **Multi-agent workflows** | Prevents database locking conflicts |
| **Team collaboration** | Auto-syncs JSONL to git in background |
| **Long coding sessions** | Changes saved even if you forget `bd sync` |
| **Real-time monitoring** | Enables `bd watch` and status updates |

### When to Disable Daemon

| Scenario | How to Disable |
|----------|----------------|
| **Git worktrees (no sync-branch)** | Auto-disabled for safety |
| **CI/CD pipelines** | `BEADS_NO_DAEMON=true` |
| **Offline work** | `--no-daemon` (no git push available) |
| **Resource-constrained** | `BEADS_NO_DAEMON=true` |
| **Deterministic testing** | Use exclusive lock (see below) |

### Git Worktrees and Daemon

**Automatic safety:** Daemon is automatically disabled in git worktrees unless sync-branch is configured. This prevents commits going to the wrong branch.

**Enable daemon in worktrees:** Configure sync-branch to safely use daemon across all worktrees:
```bash
bd config set sync-branch beads-sync
```

With sync-branch configured, daemon commits to a dedicated branch using an internal worktree, so your current branch is never affected. See [WORKTREES.md](WORKTREES.md) for details.

### Local-Only Users

If you're working alone on a local project with no git remote:
- **Daemon still helps**: Batches writes, handles auto-export to JSONL
- **But optional**: Use `--no-daemon` if you prefer direct database access
- **No network calls**: Daemon doesn't phone home or require internet

```bash
# Check if daemon is running
bd info | grep daemon

# Force direct mode for one command
bd --no-daemon list

# Disable for entire session
export BEADS_NO_DAEMON=true
```

## Architecture

**Per-Workspace Model (LSP-style):**
```
MCP Server (one instance)
    ↓
Per-Project Daemons (one per workspace)
    ↓
SQLite Databases (complete isolation)
```

Each workspace gets its own daemon:
- Socket at `.beads/bd.sock` (`.beads/bd.pipe` on Windows)
- Auto-starts on first command (unless disabled)
- Handles auto-sync, batching, background operations
- Complete database isolation (no cross-project pollution)

## Managing Daemons

### List All Running Daemons

```bash
# See all daemons across workspaces
bd daemons list --json

# Example output:
# [
#   {
#     "workspace": "/Users/alice/projects/webapp",
#     "pid": 12345,
#     "socket": "/Users/alice/projects/webapp/.beads/bd.sock",
#     "version": "0.21.0",
#     "uptime_seconds": 3600
#   }
# ]
```

### Check Daemon Health

```bash
# Check for version mismatches, stale sockets
bd daemons health --json

# Example output:
# {
#   "healthy": false,
#   "issues": [
#     {
#       "workspace": "/Users/alice/old-project",
#       "issue": "version_mismatch",
#       "daemon_version": "0.20.0",
#       "cli_version": "0.21.0"
#     }
#   ]
# }
```

**When to use:**
- After upgrading bd (check for version mismatches)
- Debugging sync issues
- Periodic health monitoring

### Stop/Restart Daemons

```bash
# Stop specific daemon by workspace path
bd daemons stop /path/to/workspace --json

# Stop by PID
bd daemons stop 12345 --json

# Restart (stop + auto-start on next command)
bd daemons restart /path/to/workspace --json
bd daemons restart 12345 --json

# Stop ALL daemons
bd daemons killall --json
bd daemons killall --force --json  # Force kill if graceful fails
```

### View Daemon Logs

```bash
# View last 100 lines
bd daemons logs /path/to/workspace -n 100

# Follow mode (tail -f style)
bd daemons logs 12345 -f

# Debug sync issues
bd daemons logs . -n 500 | grep -i "export\|import\|sync"
```

**Common log patterns:**
- `[INFO] Auto-sync: export complete` - Successful JSONL export
- `[WARN] Git push failed: ...` - Push error (auto-retry)
- `[ERROR] Version mismatch` - Daemon/CLI version out of sync

## Version Management

**Automatic Version Checking (v0.16.0+):**

bd automatically handles daemon version mismatches:
- Version compatibility checked on every connection
- Old daemons automatically detected and restarted
- No manual intervention needed after upgrades
- Works with MCP server and CLI

**After upgrading bd:**

```bash
# 1. Check for mismatches
bd daemons health --json

# 2. Restart all daemons with new version
bd daemons killall

# 3. Next bd command auto-starts daemon with new version
bd ready
```

**Troubleshooting version mismatches:**
- Daemon won't stop: `bd daemons killall --force`
- Socket file stale: `rm .beads/bd.sock` (auto-cleans on next start)
- Multiple bd versions installed: `which bd` and `bd version`

## Event-Driven Daemon Mode (Default)

**Default since v0.21.0**: Event-driven mode replaces 5-second polling with instant reactivity.

### Benefits

- ⚡ **<500ms latency** (vs ~5000ms with polling)
- 🔋 **~60% less CPU usage** (no continuous polling)
- 🎯 **Instant sync** on mutations and file changes
- 🛡️ **Dropped events safety net** prevents data loss
- 🔄 **Periodic remote sync** pulls updates from other clones

### How It Works

**Architecture:**
```
┌─────────────────────────────────────────────────────────────────┐
│                    EVENT-DRIVEN MODE                             │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  EXPORT FLOW (Mutation-Triggered)                         │   │
│  │                                                           │   │
│  │  FileWatcher (platform-native)                            │   │
│  │      ├─ .beads/issues.jsonl (file changes)                │   │
│  │      └─ RPC mutations (create, update, close)             │   │
│  │           ↓                                               │   │
│  │      Debouncer (500ms batch window)                       │   │
│  │           ↓                                               │   │
│  │      Export → Git Commit → Git Push (if --auto-push)      │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  IMPORT FLOW (Periodic Remote Sync)                       │   │
│  │                                                           │   │
│  │  remoteSyncTicker (default: 30s, configurable)            │   │
│  │           ↓                                               │   │
│  │      Git Pull (from sync branch or origin)                │   │
│  │           ↓                                               │   │
│  │      Import JSONL → Database                              │   │
│  └──────────────────────────────────────────────────────────┘   │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

**Platform-native APIs:**
- Linux: `inotify`
- macOS: `FSEvents` (via kqueue)
- Windows: `ReadDirectoryChangesW`

**Key behaviors:**
- **Mutation events** from RPC trigger immediate export (debounced 500ms)
- **Periodic remote sync** pulls updates from other clones (default 30s interval)
- **Polling fallback** if fsnotify unavailable (network filesystems)

### Enabling Event-Driven Mode

Event-driven mode is the **default** as of v0.21.0. No configuration needed.

```bash
# Event-driven mode starts automatically
bd daemon --start

# Explicitly enable (same as default)
BEADS_DAEMON_MODE=events bd daemon --start
```

**Available modes:**
- `events` (default) - Event-driven mode with instant reactivity
- `poll` - Traditional 5-second polling, fallback for edge cases

### Configuration

**Environment Variables:**

| Variable | Values | Default | Description |
|----------|--------|---------|-------------|
| `BEADS_DAEMON_MODE` | `poll`, `events` | `events` | Daemon operation mode |
| `BEADS_WATCHER_FALLBACK` | `true`, `false` | `true` | Fall back to polling if fsnotify fails |
| `BEADS_REMOTE_SYNC_INTERVAL` | duration | `30s` | How often to pull from remote (event mode) |

**config.yaml settings:**

```yaml
# .beads/config.yaml

# Interval for daemon to pull remote sync branch updates
# Accepts Go duration strings: "30s", "1m", "5m", etc.
# Minimum: 5s (values below are clamped)
# Set to "0" to disable periodic remote sync (not recommended)
remote-sync-interval: "30s"
```

**Configuration precedence:**
1. `BEADS_REMOTE_SYNC_INTERVAL` environment variable (highest)
2. `remote-sync-interval` in `.beads/config.yaml`
3. Default: 30 seconds

### Remote Sync Interval

The `remote-sync-interval` controls how often the daemon pulls from remote to check for updates from other clones.

| Value | Use Case |
|-------|----------|
| `30s` (default) | Good balance for most workflows |
| `1m` | Lower network traffic, acceptable for solo work |
| `5m` | Very low traffic, for slow-changing projects |
| `5s` (minimum) | Fastest updates, higher network usage |

**Minimum value:** 5 seconds (lower values are clamped to prevent git rate limiting and excessive network traffic)

**Disabling remote sync:**
```bash
# Set to 0 to disable (not recommended - other clones' changes won't sync)
export BEADS_REMOTE_SYNC_INTERVAL=0
```

### Switch to Polling Mode

For edge cases (NFS, containers, WSL) where fsnotify is unreliable:

```bash
# Explicitly use polling mode
BEADS_DAEMON_MODE=poll bd daemon --start

# With custom interval
bd daemon --start --interval 10s
```

### Troubleshooting Event-Driven Mode

**If watcher fails to start:**

```bash
# Check daemon logs for errors
bd daemons logs /path/to/workspace -n 100

# Common error patterns:
# - "File watcher unavailable: ..." - fsnotify init failed
# - "Falling back to polling" - watcher disabled, using polls
# - "Resource limit exceeded" - too many open files
```

**Common causes:**

1. **Network filesystem** (NFS, SMB) - fsnotify may not work
   - Solution: Use polling mode or local filesystem

2. **Container environment** - may need privileged mode
   - Solution: Add `--privileged` or specific capabilities

3. **Resource limits** - check `ulimit -n` (open file descriptors)
   - Solution: Increase limit: `ulimit -n 4096`

4. **WSL/virtualization** - reduced fsnotify reliability
   - Solution: Test in native environment or use polling

**Fallback behavior:**

If `BEADS_DAEMON_MODE=events` but watcher fails:
- Daemon automatically falls back to polling (if `BEADS_WATCHER_FALLBACK=true`)
- Warning logged: `File watcher unavailable, falling back to polling`
- All functionality works normally (just higher latency)

### Performance Comparison

| Metric | Polling Mode | Event-Driven Mode |
|--------|--------------|-------------------|
| Sync Latency | ~5000ms | <500ms |
| CPU Usage | ~2-3% (continuous) | ~0.5% (idle) |
| Memory | 30MB | 35MB (+5MB for watcher) |
| File Events | Polled every 5s | Instant detection |
| Git Updates | Polled every 5s | Instant detection |

**Future (Phase 2):** Event-driven mode will become default once proven stable in production.

## Auto-Start Behavior

**Default (v0.9.11+):** Daemon auto-starts on first bd command

```bash
# No manual start needed
bd ready  # Daemon starts automatically if not running

# Check status
bd info --json | grep daemon_running
```

**Disable auto-start:**

```bash
# Require manual daemon start
export BEADS_AUTO_START_DAEMON=false

# Start manually
bd daemon --start
```

**Auto-start with exponential backoff:**
- 1st attempt: immediate
- 2nd attempt: 100ms delay
- 3rd attempt: 200ms delay
- Max retries: 5
- Logs available: `bd daemons logs . -n 50`

## Daemon Configuration

**Environment Variables:**

| Variable | Values | Default | Description |
|----------|--------|---------|-------------|
| `BEADS_AUTO_START_DAEMON` | `true`, `false` | `true` | Auto-start daemon on commands |
| `BEADS_DAEMON_MODE` | `poll`, `events` | `poll` | Sync mode (polling vs events) |
| `BEADS_WATCHER_FALLBACK` | `true`, `false` | `true` | Fall back to poll if events fail |
| `BEADS_NO_DAEMON` | `true`, `false` | `false` | Disable daemon entirely (direct DB) |

**Example configurations:**

```bash
# Force direct mode (no daemon)
export BEADS_NO_DAEMON=true

# Event-driven with strict requirements
export BEADS_DAEMON_MODE=events
export BEADS_WATCHER_FALLBACK=false

# Disable auto-start (manual control)
export BEADS_AUTO_START_DAEMON=false
```

## Git Worktrees Warning

**⚠️ Important Limitation:** Daemon mode does NOT work correctly with `git worktree`.

**The Problem:**
- Git worktrees share the same `.git` directory and `.beads` database
- Daemon doesn't know which branch each worktree has checked out
- Can commit/push to wrong branch

**Solutions:**

1. **Use `--no-daemon` flag** (recommended):
   ```bash
   bd --no-daemon ready
   bd --no-daemon create "Fix bug" -p 1
   ```

2. **Disable via environment** (entire session):
   ```bash
   export BEADS_NO_DAEMON=1
   bd ready  # All commands use direct mode
   ```

3. **Disable auto-start** (less safe):
   ```bash
   export BEADS_AUTO_START_DAEMON=false
   ```

**Automatic detection:** bd detects worktrees and warns if daemon is active.

See [GIT_INTEGRATION.md](GIT_INTEGRATION.md) for more details.

## Exclusive Lock Protocol (Advanced)

**For external tools that need full database control** (e.g., CI/CD, deterministic execution).

When `.beads/.exclusive-lock` file exists:
- Daemon skips all operations for the locked database
- External tool has complete control over git sync and database
- Stale locks (dead process) auto-cleaned

**Lock file format (JSON):**
```json
{
  "holder": "my-tool",
  "pid": 12345,
  "hostname": "build-server",
  "started_at": "2025-11-08T08:00:00Z",
  "version": "1.0.0"
}
```

**Quick example:**
```bash
# Create lock
echo '{"holder":"my-tool","pid":'$$',"hostname":"'$(hostname)'","started_at":"'$(date -u +%Y-%m-%dT%H:%M:%SZ)'","version":"1.0.0"}' > .beads/.exclusive-lock

# Do work (daemon won't interfere)
bd create "My issue" -p 1

# Release lock
rm .beads/.exclusive-lock
```

**Use cases:**
- VibeCoder (deterministic execution)
- CI/CD pipelines (controlled sync timing)
- Testing frameworks (isolated test runs)

See [EXCLUSIVE_LOCK.md](EXCLUSIVE_LOCK.md) for complete documentation.

## Common Daemon Issues

### Stale Sockets

**Symptoms:** `bd ready` shows "daemon not responding"

**Solutions:**
```bash
# Auto-cleanup on next command
bd daemons list  # Removes stale sockets

# Manual cleanup
rm .beads/bd.sock
bd ready  # Auto-starts fresh daemon
```

### Version Mismatch

**Symptoms:** `bd ready` shows "version mismatch" error

**Solutions:**
```bash
# Check versions
bd version
bd daemons health --json

# Restart all daemons
bd daemons killall
bd ready  # Auto-starts with CLI version
```

### Daemon Won't Stop

**Symptoms:** `bd daemons stop` hangs or times out

**Solutions:**
```bash
# Force kill
bd daemons killall --force

# Nuclear option (all bd processes)
pkill -9 bd

# Clean up socket
rm .beads/bd.sock
```

### Memory Leaks

**Symptoms:** Daemon process grows to 100+ MB

**Solutions:**
```bash
# Check current memory usage
ps aux | grep "bd daemon"

# Restart daemon
bd daemons restart .

# Check logs for issues
bd daemons logs . -n 200 | grep -i "memory\|leak\|oom"
```

**Expected memory usage:**
- Baseline: ~30MB
- With watcher: ~35MB
- Per issue: ~500 bytes (10K issues = ~5MB)

## Multi-Workspace Best Practices

### When managing multiple projects:

```bash
# Check all daemons
bd daemons list --json

# Stop unused workspaces to free resources
bd daemons stop /path/to/old-project

# Health check before critical work
bd daemons health --json

# Clean restart after major upgrades
bd daemons killall
# Daemons restart on next command per workspace
```

### Resource limits:

- Each daemon: ~30-35MB memory
- 10 workspaces: ~300-350MB total
- CPU: <1% per daemon (idle), 2-3% (active sync)
- File descriptors: ~10 per daemon

### When to disable daemons:

- ✅ Git worktrees (use `--no-daemon`)
- ✅ Embedded/resource-constrained environments
- ✅ Testing/CI (deterministic execution)
- ✅ Offline work (no git push available)

## See Also

- [AGENTS.md](../AGENTS.md) - Main agent workflow guide
- [EXCLUSIVE_LOCK.md](EXCLUSIVE_LOCK.md) - External tool integration
- [GIT_INTEGRATION.md](GIT_INTEGRATION.md) - Git workflow and merge strategies
- [commands/daemons.md](../commands/daemons.md) - Daemon command reference
