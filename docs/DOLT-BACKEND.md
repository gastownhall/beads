# Dolt Storage Backend

Beads uses [Dolt](https://www.dolthub.com/) as its default storage backend. Dolt provides Git-like version control for your database, enabling advanced workflows like branch-based development, time travel queries, and distributed sync.

> **Note:** Dolt is the only supported backend. The legacy SQLite backend has been removed.
> To migrate from SQLite, see [Migration from SQLite](#migration-from-sqlite-legacy) below.

## Overview

| Feature | Dolt |
|---------|------|
| Storage | Directory-based |
| Version control | Native (cell-level) |
| Branching | Yes |
| Time travel | Yes |
| Merge conflicts | SQL-based (cell-level merge) |
| Multi-user concurrent | Server mode |
| Sync | Native push/pull to Dolt remotes |

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

### 2. Initialize

```bash
# New project (Dolt is the default backend)
bd init

# For legacy SQLite installations, see Migration from SQLite below
```

### 3. Configure Sync Mode

```yaml
# .beads/config.yaml
sync:
  mode: dolt-native  # Default: use Dolt remotes
```

## Server Mode (Recommended)

Server mode provides fast operations by running a persistent `dolt sql-server` that handles connections.

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
| `BEADS_DOLT_SERVER_PORT` | `3307` | Server port (MySQL protocol) |
| `BEADS_DOLT_SERVER_USER` | `root` | MySQL username |
| `BEADS_DOLT_SERVER_PASS` | (empty) | MySQL password |
| `BEADS_DOLT_SHARED_SERVER` | (empty) | Shared server mode: `1` or `true` to enable |

### Server Lifecycle

```bash
# Check server status
bd doctor

# Server auto-starts when needed
# PID stored in: .beads/dolt-server.pid
# Logs written to: .beads/dolt-server.log

# Start/stop/status
bd dolt start
bd dolt stop
bd dolt status
```

### Shared Server Mode

On multi-project machines, enable shared server mode to use a single Dolt server
for all projects (instead of one server per project):

```bash
# Enable via config
bd dolt set shared-server true

# Or via environment variable (machine-wide)
export BEADS_DOLT_SHARED_SERVER=1
```

Shared server state lives in `~/.beads/shared-server/` and uses port 3308 by default
(avoiding conflict with Gas Town on 3307). Each project's data remains isolated in its
own database (named by project prefix). See [DOLT.md](DOLT.md) for details.

## Central Dolt Server (macOS)

If you plan to use Gas Town or manage multiple beads projects from a single
machine, you can run a central persistent Dolt server instead of per-project
embedded instances.

### Embedded vs Central Server

| | Embedded (default) | Central Server |
|---|---|---|
| **Setup** | Zero-config — `bd init` handles everything | One-time server setup required |
| **Data location** | `.beads/dolt/` per project | Central directory (e.g. `/opt/homebrew/var/dolt`) |
| **Concurrency** | Single writer per project | Multi-writer via MySQL protocol |
| **Use case** | Solo development, single project | Gas Town, multiple projects, multiple agents |

For single-project solo use, **embedded mode is recommended** — it requires no
setup. Switch to a central server when you need Gas Town or concurrent access.

### Why Not `brew services start dolt`?

After installing Dolt with `brew install dolt`, the natural next step is
`brew services start dolt`. However, this **silently ignores your config file**.

The Homebrew formula runs `dolt sql-server` without the `--config` flag. Dolt
does **not** auto-discover `config.yaml` from its working directory — the config
file must be passed explicitly via `--config <file>`. Any edits to
`/opt/homebrew/var/dolt/config.yaml` (port, host, etc.) have no effect when
started through `brew services`.

### Setup with a Custom LaunchAgent

Instead of `brew services`, create a custom LaunchAgent that passes the config
file explicitly.

**1. Install Dolt and initialize its data directory:**

```bash
brew install dolt

# Initialize the dolt data directory (if not already done)
cd /opt/homebrew/var/dolt && dolt init
```

**2. Configure Dolt for port 3307:**

```yaml
# /opt/homebrew/var/dolt/config.yaml
log_level: info

listener:
  host: 127.0.0.1
  port: 3307
  max_connections: 100

behavior:
  autocommit: true
```

**3. Create the LaunchAgent plist:**

```bash
cat > ~/Library/LaunchAgents/com.local.dolt-server.plist << 'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.local.dolt-server</string>
    <key>ProgramArguments</key>
    <array>
        <string>/opt/homebrew/opt/dolt/bin/dolt</string>
        <string>sql-server</string>
        <string>--config</string>
        <string>config.yaml</string>
    </array>
    <key>WorkingDirectory</key>
    <string>/opt/homebrew/var/dolt</string>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <true/>
    <key>StandardOutPath</key>
    <string>/opt/homebrew/var/log/dolt.log</string>
    <key>StandardErrorPath</key>
    <string>/opt/homebrew/var/log/dolt.error.log</string>
</dict>
</plist>
EOF
```

**4. Load the service:**

```bash
launchctl load ~/Library/LaunchAgents/com.local.dolt-server.plist

# Verify it's running
mysql -h 127.0.0.1 -P 3307 -u root -e "SELECT 1"
```

**5. Point beads at the central server** — add to `~/.zshrc` (or `~/.bashrc`):

```bash
export BEADS_DOLT_SERVER_HOST="127.0.0.1"
export BEADS_DOLT_SERVER_PORT="3307"
export BEADS_DOLT_SERVER_MODE=1
```

Now `bd init` in any project will connect to the central server instead of
spawning an embedded instance.

### Managing the Service

```bash
# Stop
launchctl unload ~/Library/LaunchAgents/com.local.dolt-server.plist

# Restart (unload + load)
launchctl unload ~/Library/LaunchAgents/com.local.dolt-server.plist
launchctl load ~/Library/LaunchAgents/com.local.dolt-server.plist

# Check logs
tail -f /opt/homebrew/var/log/dolt.log
```

## Sync Modes

Dolt supports multiple sync strategies:

### Sync Mode

Beads uses `dolt-native` sync mode exclusively:

- Uses Dolt remotes (DoltHub, S3, GCS, etc.)
- Native database-level sync with cell-level merge
- Supports branching and merging
- `bd export` available for data portability; `bd init --from-jsonl` for bootstrapping from exports

## Dolt Remotes

### Configure a Remote

Use `bd dolt remote add` to configure remotes. This ensures the running Dolt SQL
server sees the remote immediately. Remotes added via the `dolt` CLI directly
are written to the filesystem but are not visible to the server until restart.

```bash
# DoltHub (public or private)
bd dolt remote add origin https://doltremoteapi.dolthub.com/org/beads

# S3
bd dolt remote add origin aws://[bucket]/path/to/repo

# GCS
bd dolt remote add origin gs://[bucket]/path/to/repo

# Git SSH (GitHub, GitLab, etc.)
bd dolt remote add origin git+ssh://git@github.com/org/repo.git

# Local file system
bd dolt remote add origin file:///path/to/remote
```

### Push/Pull

```bash
bd dolt push
bd dolt pull
```

For SSH remotes, `bd dolt push` and `bd dolt pull` automatically use the `dolt`
CLI instead of the SQL server to avoid MySQL connection timeouts during transfer.

`bd dolt remote add` registers the remote on both the SQL server and the
filesystem (CLI) config. This ensures `dolt push`/`dolt pull` via CLI can find
the remote. If either surface already has a remote with that name, you'll be
prompted before overwriting.

> **Also supports sharing a Git repo**: Dolt stores data under `refs/dolt/data`,
> separate from standard Git refs (`refs/heads/`, `refs/tags/`). You can safely
> point a `git+ssh://` remote at the same repository as your project source code.
> See [Dolt Git Remotes](https://docs.dolthub.com/concepts/dolt/git/remotes) for details.

### List/Remove Remotes

```bash
bd dolt remote list    # Shows remotes from both SQL server and CLI, flags discrepancies
bd dolt remote remove origin   # Removes from both surfaces
```

Use `bd doctor --fix` to resolve any discrepancies between SQL and CLI remote configs.

## Migration from SQLite (Legacy)

If upgrading from an older version that used SQLite:

### Option 1: Migration Script

> **Note:** The `bd migrate --to-dolt` command was removed in v0.58.0.
> For pre-0.50 installations with JSONL data, use the migration script:
>
> ```bash
> scripts/migrate-jsonl-to-dolt.sh
> ```
>
> See [Troubleshooting](TROUBLESHOOTING.md#circuit-breaker-server-appears-down-failing-fast) if you encounter connection errors after migration.

### Option 2: Fresh Start

```bash
# Export current state
bd export -o backup.jsonl

# Archive existing beads
mv .beads .beads-sqlite-backup

# Initialize fresh from backup
bd init --from-jsonl
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

  # Lock settings
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
| `BEADS_DOLT_SERVER_MODE` | Server mode: `1` or `0` |
| `BEADS_DOLT_SERVER_HOST` | Server host |
| `BEADS_DOLT_SERVER_PORT` | Server port |
| `BEADS_DOLT_SERVER_USER` | Server user |
| `BEADS_DOLT_SERVER_PASS` | Server password |

## See Also

- [Troubleshooting](TROUBLESHOOTING.md) - General troubleshooting
- [Dolt Documentation](https://docs.dolthub.com/) - Official Dolt docs
