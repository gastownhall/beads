---
description: Run background sync daemon
argument-hint: [--stop] [--status] [--health]
---

Run a per-project background daemon that manages database connections and syncs with git.

## Per-Project Daemon (LSP Model)

Each project runs its own daemon at `.beads/bd.sock` for complete database isolation.

> On Windows this file stores the daemon's loopback TCP endpoint metadata—leave it in place so bd can reconnect.

**Why per-project daemons?**
- Complete database isolation between projects
- No cross-project pollution or git worktree conflicts
- Simpler mental model: one project = one database = one daemon
- Follows LSP (Language Server Protocol) architecture

**Note:** Global daemon support was removed in v0.16.0. The `--global` flag is no longer functional.

## Common Operations

- **Start**: `bd daemon` (auto-starts on first `bd` command)
- **Stop**: `bd daemon --stop`
- **Status**: `bd daemon --status`
- **Health**: `bd daemon --health` - shows uptime, cache stats, performance metrics
- **Metrics**: `bd daemon --metrics` - detailed operational telemetry

## Sync Options

- **--auto-commit**: Automatically commit JSONL changes
- **--auto-push**: Automatically push commits to remote
- **--interval**: Sync check interval (default: 5m)

The daemon provides:
- Connection pooling and caching
- Better performance for frequent operations
- Automatic JSONL sync (5-second debounce)
- Optional git sync
