# Dolt Backend Guide

Beads supports [Dolt](https://www.dolthub.com/) as an alternative storage backend to SQLite. Dolt provides Git-like version control for your database, enabling advanced workflows like branch-based development, time travel queries, and distributed sync without JSONL files.

## Overview

| Feature | SQLite | Dolt |
|---------|--------|------|
| Single-file storage | Yes | No (directory) |
| Version control | Via JSONL | Native |
| Branching | No | Yes |
| Time travel | No | Yes |
| Merge conflicts | JSONL-based | SQL-based |
| Multi-user concurrent | Limited | Server mode |
| Git sync required | Yes | Optional |

## Quick Start

### 1. Install Dolt

```bash
# macOS
brew install dolt

# Linux
curl -L https://github.com/dolthub/dolt/releases/latest/download/install.sh | bash

# Verify installation
dolt version
```

### 2. Initialize with Dolt Backend

```bash
# New project
bd init --backend=dolt

# Or convert existing SQLite database
bd migrate --to-dolt
```

### 3. Configure Sync Mode

```yaml
# .beads/config.yaml
backend: dolt

sync:
  mode: dolt-native  # Skip JSONL entirely
```

## Server Mode (Recommended)

Server mode provides 5-10x faster operations by running a persistent `dolt sql-server` that handles connections. This eliminates the 500-1000ms bootstrap overhead of embedded mode.

### Server Mode is Enabled by Default

When Dolt backend is detected, server mode is automatically enabled. The server auto-starts if not already running.

### Disable Server Mode (Not Recommended)

```bash
# Via environment variable
export BEADS_DOLT_SERVER_MODE=0

# Or in config.yaml
dolt:
  server_mode: false
```

### Server Configuration

| Environment Variable | Default | Description |
|---------------------|---------|-------------|
| `BEADS_DOLT_SERVER_MODE` | `1` | Enable/disable server mode (`1`/`0`) |
| `BEADS_DOLT_SERVER_HOST` | `127.0.0.1` | Server bind address |
| `BEADS_DOLT_SERVER_PORT` | `3306` | Server port (MySQL protocol) |
| `BEADS_DOLT_SERVER_USER` | `root` | MySQL username |
| `BEADS_DOLT_SERVER_PASS` | (empty) | MySQL password |

### Server Lifecycle

```bash
# Check server status
bd doctor

# Server auto-starts when needed
# PID stored in: .beads/dolt/sql-server.pid
# Logs written to: .beads/dolt/sql-server.log

# Manual stop (rarely needed)
kill $(cat .beads/dolt/sql-server.pid)
```

## Sync Modes

Dolt supports multiple sync strategies:

### `dolt-native` (Recommended for Dolt)

```yaml
sync:
  mode: dolt-native
```

- Uses Dolt remotes (DoltHub, S3, GCS, etc.)
- No JSONL files needed
- Native database-level sync
- Supports branching and merging

### `belt-and-suspenders`

```yaml
sync:
  mode: belt-and-suspenders
```

- Uses BOTH Dolt remotes AND JSONL
- Maximum redundancy
- Useful for migration periods

### `git-portable`

```yaml
sync:
  mode: git-portable
```

- Traditional JSONL-based sync
- Compatible with SQLite workflows
- Dolt provides local version history only

## Dolt Remotes

### Configure a Remote

```bash
# DoltHub (public or private)
cd .beads/dolt
dolt remote add origin https://doltremoteapi.dolthub.com/org/beads

# S3
dolt remote add origin aws://[bucket]/path/to/repo

# GCS
dolt remote add origin gs://[bucket]/path/to/repo

# Local file system
dolt remote add origin file:///path/to/remote
```

### Push/Pull

```bash
# Via bd sync
bd sync

# Direct dolt commands (if needed)
cd .beads/dolt
dolt push origin main
dolt pull origin main
```

## Migration from SQLite

### Option 1: Fresh Start

```bash
# Archive existing beads
mv .beads .beads-sqlite-backup

# Initialize with Dolt
bd init --backend=dolt

# Import from JSONL (if you have one)
bd import .beads-sqlite-backup/issues.jsonl
```

### Option 2: In-Place Migration

```bash
# Export current state
bd export --full issues.jsonl

# Reconfigure backend
# Edit .beads/config.yaml to set backend: dolt

# Re-initialize
bd init --backend=dolt

# Import
bd import issues.jsonl
```

## Troubleshooting

### Already Committed dolt/ to Git

If you committed `.beads/dolt/` before this fix:

1. Update gitignore: `bd doctor --fix`
2. Remove from git tracking: `git rm --cached -r .beads/dolt/ .beads/dolt-access.lock`
3. Commit the removal: `git commit -m "fix: remove accidentally committed dolt data"`
4. To purge from history (optional): use [BFG Repo-Cleaner](https://rtyley.github.io/bfg-repo-cleaner/) or `git filter-repo`

### Server Won't Start

```bash
# Check if port is in use
lsof -i :3306

# Check server logs
cat .beads/dolt/sql-server.log

# Verify dolt installation
dolt version

# Try manual start
cd .beads/dolt && dolt sql-server --host 127.0.0.1 --port 3306
```

### Connection Issues

```bash
# Test connection
mysql -h 127.0.0.1 -P 3306 -u root beads

# Check server is running
bd doctor

# Force restart
kill $(cat .beads/dolt/sql-server.pid) 2>/dev/null
bd list  # Triggers auto-start
```

### Performance Issues

1. **Ensure server mode is enabled** (default)
2. Check server logs for errors
3. Run `bd doctor` for diagnostics
4. Consider `dolt gc` for database maintenance:
   ```bash
   cd .beads/dolt && dolt gc
   ```

## Advanced Usage

### Branching

```bash
cd .beads/dolt

# Create feature branch
dolt checkout -b feature/experiment

# Make changes via bd commands
bd create "experimental issue"

# Merge back
dolt checkout main
dolt merge feature/experiment
```

### Time Travel

```bash
cd .beads/dolt

# List commits
dolt log --oneline

# Query at specific commit
dolt sql -q "SELECT * FROM issues AS OF 'abc123'"

# Checkout historical state
dolt checkout abc123
```

### Diff and Blame

```bash
cd .beads/dolt

# See changes since last commit
dolt diff

# Diff between commits
dolt diff HEAD~5 HEAD -- issues

# Blame (who changed what)
dolt blame issues
```

## Configuration Reference

### Full Config Example

```yaml
# .beads/config.yaml
backend: dolt

sync:
  mode: dolt-native
  auto_dolt_commit: true   # Auto-commit after sync (default: true)
  auto_dolt_push: false    # Auto-push after sync (default: false)

dolt:
  server_mode: true        # Use sql-server (default: true)
  server_host: "127.0.0.1"
  server_port: 3306
  server_user: "root"
  server_pass: ""

  # Embedded mode settings (when server_mode: false)
  lock_retries: 30
  lock_retry_delay: "100ms"
  idle_timeout: "30s"

federation:
  remote: "dolthub://myorg/beads"
  sovereignty: "T3"  # T1-T4
```

### Environment Variables

| Variable | Description |
|----------|-------------|
| `BEADS_BACKEND` | Force backend: `sqlite` or `dolt` |
| `BEADS_DOLT_SERVER_MODE` | Server mode: `1` or `0` |
| `BEADS_DOLT_SERVER_HOST` | Server host |
| `BEADS_DOLT_SERVER_PORT` | Server port |
| `BEADS_DOLT_SERVER_USER` | Server user |
| `BEADS_DOLT_SERVER_PASS` | Server password |

## See Also

- [Sync Modes](CONFIG.md#sync-mode-configuration) - Detailed sync configuration
- [Daemon](DAEMON.md) - Background sync daemon
- [Troubleshooting](TROUBLESHOOTING.md) - General troubleshooting
- [Dolt Documentation](https://docs.dolthub.com/) - Official Dolt docs
