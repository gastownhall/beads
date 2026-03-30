# Dolt Backend for Beads

Beads uses Dolt as its storage backend. Dolt provides a version-controlled SQL database with cell-level merge, native branching, and two deployment modes.

## Why Dolt?

- **Native version control** — cell-level diffs and merges, not line-based
- **Multi-writer support** — server mode enables concurrent agents
- **Built-in history** — every write creates a Dolt commit
- **Native branching** — Dolt branches independent of git branches
- **Single-binary option** — embedded mode for solo users (no server needed)

## Getting Started

### New Project

```bash
# Embedded mode (single writer, no server — default for standalone)
bd init

# Server mode (multi-writer, e.g. orchestrator)
gt dolt start           # Start the Dolt server
bd init --server        # Initialize with server mode
```

### Migrate from SQLite (Legacy)

If upgrading from an older version that used SQLite:

> **Note:** The `bd migrate --to-dolt` command was removed in v0.58.0.
> For pre-0.50 installations with JSONL data, use the migration script:
>
> ```bash
> scripts/migrate-jsonl-to-dolt.sh
> ```
>
> See [Troubleshooting](TROUBLESHOOTING.md#circuit-breaker-server-appears-down-failing-fast) if you encounter connection errors after migration.

Migration creates backups automatically. Your original SQLite database is preserved as `beads.backup-pre-dolt-*.db`.

## Modes of Operation

### Embedded Mode (Solo / Standalone)

In-process Dolt engine — no separate server needed. This is the default for
standalone Beads users. The `bd` binary includes everything; just `bd init` and go.

- Single-writer (one process at a time)
- Data lives in `.beads/dolt/` alongside your code
- Push to GitHub with `bd dolt push` — code and issues in one repo
- Zero ops: no server, no ports, no PID files

### Server Mode (Multi-Writer / Orchestrator)

Connects to a running `dolt sql-server` for multi-client access.

```bash
# Start the server (orchestrator)
gt dolt start

# Or manually
cd ~/.dolt-data/beads && dolt sql-server --port 3307
```

```bash
# Initialize in server mode
bd init --server

# Or switch via environment variable
export BEADS_DOLT_SERVER_MODE=1
```

```yaml
# .beads/config.yaml (server mode settings)
dolt:
  mode: server
  host: 127.0.0.1
  port: 3307
  user: root
```

Switch to server mode when you need:
- Multiple agents writing simultaneously
- Orchestrator multi-rig setups
- Federation with remote peers

## Migrating Between Backends

You can migrate data between embedded mode and server mode using `bd backup`.
Both directions preserve full Dolt commit history.

### Server → Embedded

1. **Create a backup from the server-mode project:**

   ```bash
   # In the server-mode project directory
   bd backup init /path/to/backup-dir
   bd backup sync
   ```

2. **Create a new embedded-mode project and restore:**

   ```bash
   mkdir new-project && cd new-project
   bd init                  # creates an embedded-mode project by default
   bd backup restore --force /path/to/backup-dir
   ```

   `--force` overwrites the freshly-initialized database with the backup
   contents. The restore automatically:
   - Updates `metadata.json` to match the restored project identity
   - Registers the backup directory for future `bd backup sync`
   - Backfills the embedded migration tracker (`schema_migrations`)

3. **Verify:**

   ```bash
   bd list
   bd backup status
   ```

### Embedded → Server

1. **Create a backup from the embedded-mode project:**

   ```bash
   # In the embedded-mode project directory
   bd backup init /path/to/backup-dir
   bd backup sync
   ```

2. **Create a new server-mode project and restore:**

   ```bash
   mkdir new-project && cd new-project
   bd init --server         # creates a server-mode project
   bd backup restore --force /path/to/backup-dir
   ```

3. **Verify:**

   ```bash
   bd list
   bd backup status
   ```

### Backup Commands Reference

| Command | Description |
|---------|-------------|
| `bd backup init <path>` | Register a backup destination (filesystem or DoltHub URL) |
| `bd backup sync` | Push database to the configured backup destination |
| `bd backup restore [path]` | Restore from a backup directory (`--force` to overwrite) |
| `bd backup remove` | Unregister the backup destination |
| `bd backup status` | Show backup configuration and last sync time |

### Notes

- Data locations differ between modes: `.beads/embeddeddolt/` (embedded) vs `.beads/dolt/` (server)
- The backup directory is a full Dolt backup — it can be on a local drive, NAS, or DoltHub
- You can also migrate via Dolt remotes (`bd dolt push` / `bd dolt pull`) if both projects share a remote

See also [DOLT-BACKEND.md](DOLT-BACKEND.md#migrating-between-backends).

## Federation (Peer-to-Peer Sync)

Federation enables direct sync between Dolt installations without a central hub.

### Architecture

```
┌─────────────────┐         ┌─────────────────┐
│  Workspace A    │◄───────►│  Workspace B    │
│  dolt sql-server│  sync   │  dolt sql-server│
│  :3306 (sql)    │         │  :3306 (sql)    │
│  :8080 (remote) │         │  :8080 (remote) │
└─────────────────┘         └─────────────────┘
```

In federation mode, the server exposes two ports:
- **MySQL (3306)**: Multi-writer SQL access
- **remotesapi (8080)**: Peer-to-peer push/pull

### Quick Start

```bash
# Add a peer
bd federation add-peer town-beta 192.168.1.100:8080/beads

# With authentication
bd federation add-peer town-beta host:8080/beads --user sync-bot

# Sync with all peers
bd federation sync

# Handle conflicts
bd federation sync --strategy theirs  # or 'ours'

# Check status
bd federation status
```

### Topologies

| Pattern | Description | Use Case |
|---------|-------------|----------|
| Hub-spoke | Central hub, satellites sync to hub | Team with central coordination |
| Mesh | All peers sync with each other | Decentralized collaboration |
| Hierarchical | Tree of hubs | Multi-team organizations |

### Credentials

Peer credentials are AES-256 encrypted, stored locally, and used automatically during sync:

```bash
# Credentials prompted interactively
bd federation add-peer name url --user admin

# Stored in federation_peers table (encrypted)
```

### Troubleshooting

```bash
# Check federation health
bd doctor --deep

# Verify peer connectivity
bd federation status
```

## Contributor Onboarding (Clone Bootstrap)

When someone clones a repository that uses Dolt backend:

1. Run `bd bootstrap` in the clone
2. If the git remote has `refs/dolt/data` (pushed via `bd dolt push`),
   `bd bootstrap` auto-detects it and clones the database from the remote
3. Work continues normally — all existing issues are available

**No manual steps required** beyond `bd bootstrap`. The auto-detect:
- Probes `origin` for `refs/dolt/data`
- Clones the Dolt database from the remote (instead of creating a fresh one)
- Configures the Dolt remote for future `bd dolt push`/`pull`

If `sync.git-remote` is set in `.beads/config.yaml`, that takes precedence
over auto-detection. `bd init` will warn if it detects `refs/dolt/data` on
origin and suggest using `bd bootstrap` instead.

### Verifying Bootstrap Worked

```bash
bd list              # Should show issues
bd vc log            # Should show initial commit
```

## Troubleshooting

### Server Not Running

**Symptom:** Connection refused errors when using server mode.

```
failed to create database: dial tcp 127.0.0.1:3307: connect: connection refused
```

**Fix:**
```bash
gt dolt start        # Orchestrator command
# Or
gt dolt status       # Check if running
```

### Bootstrap Not Running

**Symptom:** `bd list` shows nothing on fresh clone.

**Check:**
```bash
ls .beads/dolt/            # Should NOT exist (pre-bootstrap)
BD_DEBUG=1 bd list         # See bootstrap output
```

**Force bootstrap:**
```bash
rm -rf .beads/dolt         # Remove broken state
bd list                    # Re-triggers bootstrap
```

### Database Corruption

**Symptom:** Queries fail, inconsistent data.

**Diagnosis:**
```bash
bd doctor                  # Basic checks
bd doctor --deep           # Full validation
bd doctor --server         # Server mode checks (if applicable)
```

**Recovery options:**

1. **Repair what's fixable:**
   ```bash
   bd doctor --fix
   ```

2. **Rebuild from remote:**
   ```bash
   rm -rf .beads/dolt
   bd list                  # Re-triggers bootstrap
   ```

### Lock Contention (Embedded Mode)

**Symptom:** "database is locked" errors.

Embedded mode is single-writer (enforced via file lock). If you need concurrent
access, switch to server mode. See [Migrating Between Backends](#migrating-between-backends).

## Configuration Reference

```yaml
# .beads/config.yaml

# Dolt settings
dolt:
  # Auto-commit Dolt history after writes (default: on for embedded, off for server)
  auto-commit: on        # on | off

  # Storage mode (default: embedded)
  mode: embedded         # embedded | server
  # Server mode settings (only used when mode: server)
  host: 127.0.0.1
  port: 3307
  user: root
  # Password: env var or credentials file (see below)

  # Shared server mode (GH#2377): all projects share a single Dolt server
  # at ~/.beads/shared-server/. Each project uses its own database (prefix-based).
  # Eliminates port conflicts and reduces resource usage on multi-project machines.
  shared-server: false   # true | false

  # Idle auto-stop timeout for the Dolt server (default: "30m", "0" disables)
  idle-timeout: 30m
```

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `BEADS_DOLT_PASSWORD` | Server mode password (highest priority) |
| `BEADS_CREDENTIALS_FILE` | Path to credentials file (overrides default location) |
| `BEADS_DOLT_SERVER_MODE` | Enable server mode (set to "1") |
| `BEADS_DOLT_SERVER_HOST` | Server host (default: 127.0.0.1) |
| `BEADS_DOLT_SERVER_PORT` | Server port (default: 3307, or 3308 in shared mode) |
| `BEADS_DOLT_SERVER_TLS` | Enable TLS (set to "1" or "true") |
| `BEADS_DOLT_SERVER_USER` | MySQL connection user |
| `BEADS_DOLT_SHARED_SERVER` | Enable shared server mode (set to "1" or "true") |
| `DOLT_REMOTE_USER` | Push/pull auth user |
| `DOLT_REMOTE_PASSWORD` | Push/pull auth password |
| `BD_DOLT_AUTO_COMMIT` | Override auto-commit setting |

### Credentials File

For multi-server setups, you can store passwords in an INI-style credentials file
instead of juggling environment variables per project. Passwords are looked up by
`[host:port]` section, so each project automatically gets the right password based
on its configured server.

**Password resolution order:**
1. `BEADS_DOLT_PASSWORD` env var (highest priority, existing behavior)
2. Credentials file lookup by `[host:port]` (using the resolved runtime port)
3. Empty string (no password)

**Port resolution note:** The `[host:port]` used for credential lookup matches the
resolved runtime port (from the port file, env var, or config — in that priority
order), not necessarily the port stored in `metadata.json`. This matters when using
IAP tunnels: if your tunnel maps remote:3307 to localhost:3308, store your password
under `[127.0.0.1:3308]` and the credentials file will match the actual connection.

**Default location:** `~/.config/beads/credentials` (Linux/macOS), `%APPDATA%\beads\credentials` (Windows)

**Override location:** Set `BEADS_CREDENTIALS_FILE` env var.

**File format:**

```ini
# ~/.config/beads/credentials
[127.0.0.1:3307]
password=localDevPassword

[beads.company.com:3307]
password=teamServerPassword

[10.0.1.50:3308]
password=officePassword
```

**Permissions:** On Linux/macOS, a warning is printed to stderr if the file is
readable by group or others (mirrors ssh behavior). Set permissions with:

```bash
chmod 600 ~/.config/beads/credentials
```

## Dolt Version Control

Dolt maintains its own version history, separate from Git:

```bash
# View Dolt commit history
bd vc log

# Show diff between Dolt commits
bd vc diff HEAD~1 HEAD

# Create manual checkpoint
bd vc commit -m "Checkpoint before refactor"
```

### Auto-Commit Behavior

In **embedded mode** (standalone default), each `bd` write command creates a Dolt commit:

```bash
bd create "New issue"    # Creates issue + Dolt commit
```

In **server mode** (orchestrator), auto-commit defaults to OFF because the server
manages its own transaction lifecycle. Firing `DOLT_COMMIT` after every write
under concurrent load causes 'database is read only' errors.

Override for batch operations (embedded) or explicit commits (server):

```bash
bd --dolt-auto-commit off create "Issue 1"
bd --dolt-auto-commit off create "Issue 2"
bd vc commit -m "Batch: created issues"
```

## Server Management (Orchestrator)

The orchestrator provides integrated Dolt server management:

```bash
gt dolt start            # Start server (background)
gt dolt stop             # Stop server
gt dolt status           # Show server status
gt dolt logs             # View server logs
gt dolt sql              # Open SQL shell
```

Server runs on port 3307 (avoids MySQL conflict on 3306).

### Shared Server Mode

On machines with multiple beads projects, each project normally starts its own Dolt server.
Shared server mode runs a single Dolt server at `~/.beads/shared-server/` that serves all projects:

```bash
# Enable for this project
bd dolt set shared-server true

# Or enable machine-wide via environment variable
export BEADS_DOLT_SHARED_SERVER=1

# Or enable during init
bd init --prefix myproject --shared-server
```

**Benefits:**
- No port conflicts between projects (single server on port 3308, avoids orchestrator on 3307)
- Reduced resource usage (one process instead of many)
- Automatic database isolation (each project uses its own database name)

**How it works:**
- Server state files (PID, port, lock, log) live in `~/.beads/shared-server/`
- Dolt data directory: `~/.beads/shared-server/dolt/`
- Each project's database is stored as a subdirectory (e.g., `~/.beads/shared-server/dolt/myproject/`)
- The file lock mechanism ensures safe concurrent access from multiple projects
- Default port is 3308 (not 3307) to avoid conflict with the orchestrator. Override with `BEADS_DOLT_SERVER_PORT` or `dolt.port` in config.yaml

**Important:** Each project on a shared server **must have a unique prefix** (database name).
Two projects with the same prefix share the same database — if this happens accidentally,
the project identity check will detect the mismatch and refuse to connect, preventing
silent data corruption. Always use distinct prefixes when running `bd init --shared-server`.

```bash
# Check shared server status from any project
bd dolt status

# Show full configuration including shared mode
bd dolt show
```

### Data Location (Orchestrator)

```
<town-root>/.dolt-data/
├── hq/                  # Town beads (hq-*)
├── my-project/          # Project rig (mp-*)
├── beads/               # Beads rig (bd-*)
└── other-project/       # Other rig (op-*)
```

## Migration Cleanup

After successful migration from SQLite, you may have backup files:

```
.beads/beads.backup-pre-dolt-20260122-213600.db
.beads/sqlite.backup-pre-dolt-20260123-192812.db
```

These are safe to delete once you've verified Dolt is working:

```bash
# Verify Dolt works
bd list
bd doctor

# Then clean up (after appropriate waiting period)
rm .beads/*.backup-*.db
```

**Recommendation:** Keep backups for at least a week before deleting.

## See Also

- [SYNC_SETUP.md](SYNC_SETUP.md) - Setting up sync across multiple computers
- [CONFIG.md](CONFIG.md) - Full configuration reference
- [DEPENDENCIES.md](DEPENDENCIES.md) - Dependencies and gates
- [GIT_INTEGRATION.md](GIT_INTEGRATION.md) - Git worktrees and protected branches
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) - General troubleshooting
