---
description: Manage background sync daemon
argument-hint: [start] [stop] [status]
---

Manage the per-project background daemon that handles database connections and syncs with git.

## Per-Project Daemon (LSP Model)

Each project runs its own daemon at `.beads/bd.sock` for complete database isolation.

> On Windows this file stores the daemon's loopback TCP endpoint metadata—leave it in place so bd can reconnect.

**Why per-project daemons?**
- Complete database isolation between projects
- No cross-project pollution or git worktree conflicts
- Simpler mental model: one project = one database = one daemon
- Follows LSP (Language Server Protocol) architecture

**Note:** Global daemon support was removed in v0.16.0. The `--global` flag is no longer functional.

## When to Use Daemon Mode

**✅ You SHOULD use daemon mode if:**
- Working in a team with git remote sync
- Want automatic commit/push of issue changes
- Need background auto-sync (5-second debounce)
- Making frequent bd commands (performance benefit from connection pooling)

**❌ You DON'T need daemon mode if:**
- Solo developer with local-only tracking
- Working in git worktrees (use --no-daemon to avoid conflicts)
- Running one-off commands or scripts
- Debugging database issues (direct mode is simpler)

**Local-only users:** Direct mode (default without daemon) is perfectly fine. The daemon mainly helps with git sync automation. You can still use `bd sync` manually when needed.

**Performance note:** For most operations, the daemon provides minimal performance benefit. The main value is automatic JSONL export (5s debounce) and optional git sync (--auto-commit, --auto-push).

## Common Operations

- **Start**: `bd daemon start` (or auto-starts on first `bd` command)
- **Stop**: `bd daemon stop`
- **Status**: `bd daemon status`
- **Health**: `bd daemon status --all` - shows uptime, cache stats, performance metrics
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
