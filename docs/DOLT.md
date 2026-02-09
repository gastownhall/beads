# Dolt Backend for Beads

Beads supports Dolt as an alternative storage backend to SQLite. Dolt provides version-controlled SQL database capabilities, enabling powerful workflows for multi-agent environments and team collaboration.

## Why Use Dolt?

| Feature | SQLite | Dolt |
|---------|--------|------|
| Version control | Via JSONL export | Native (cell-level) |
| Multi-writer | Single process | Server mode supported |
| Merge conflicts | Line-based JSONL | Cell-level 3-way merge |
| History | Git commits | Dolt commits + Git |
| Branching | Via Git branches | Native Dolt branches |

**Recommended for:**
- Multi-agent environments (Gas Town)
- Teams wanting database-level version control
- Projects needing cell-level conflict resolution

**Stick with SQLite for:**
- Simple single-user setups
- Maximum compatibility
- Minimal dependencies

## Getting Started

### New Project with Dolt

```bash
# Embedded mode (single writer)
bd init --backend dolt

# Server mode (multi-writer)
gt dolt start                    # Start the Dolt server
bd init --backend dolt --server  # Initialize with server mode
```

### Migrate Existing Project to Dolt

```bash
# Preview the migration
bd migrate --to-dolt --dry-run

# Run the migration
bd migrate --to-dolt

# Optionally clean up SQLite files
bd migrate --to-dolt --cleanup
```

Migration creates backups automatically. Your original SQLite database is preserved as `beads.backup-pre-dolt-*.db`.

### Migrate Back to SQLite (Escape Hatch)

If you need to revert:

```bash
bd migrate --to-sqlite
```

## Modes of Operation

### Embedded Mode (Default)

Single-process access to the Dolt database. Good for development and single-agent use.

```yaml
# .beads/config.yaml (or auto-detected)
database: dolt
```

Characteristics:
- No server process needed
- Single writer at a time
- Daemon mode disabled (direct access only)

### Server Mode (Multi-Writer)

Connects to a running `dolt sql-server` for multi-client access.

```bash
# Start the server (Gas Town)
gt dolt start

# Or manually
cd ~/.dolt-data/beads && dolt sql-server --port 3307
```

```yaml
# .beads/config.yaml
database: dolt
dolt:
  mode: server
  host: 127.0.0.1
  port: 3307
  user: root
```

Server mode is required for:
- Multiple agents writing simultaneously
- Gas Town multi-rig setups
- Federation with remote peers

## Federation (Peer-to-Peer Sync)

Federation enables direct sync between Dolt installations without a central hub.

### Architecture

```
┌─────────────────┐         ┌─────────────────┐
│   Gas Town A    │◄───────►│   Gas Town B    │
│  dolt sql-server│  sync   │  dolt sql-server│
│  :3306 (sql)    │         │  :3306 (sql)    │
│  :8080 (remote) │         │  :8080 (remote) │
└─────────────────┘         └─────────────────┘
```

The daemon in federation mode exposes two ports:
- **MySQL (3306)**: Multi-writer SQL access
- **remotesapi (8080)**: Peer-to-peer push/pull

### Quick Start

```bash
# Start daemon in federation mode
bd daemon start --federation

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

# View daemon federation logs
bd daemon logs | grep -i federation
```

## Contributor Onboarding (Clone Bootstrap)

When someone clones a repository that uses Dolt backend:

1. They see the `issues.jsonl` file (committed to git)
2. On first `bd` command (e.g., `bd list`), bootstrap runs automatically
3. JSONL is imported into a fresh Dolt database
4. Work continues normally

**No manual steps required.** The bootstrap:
- Detects fresh clone (JSONL exists, Dolt doesn't)
- Acquires a lock to prevent race conditions
- Imports issues, routes, interactions, labels, dependencies
- Creates initial Dolt commit "Bootstrap from JSONL"

### Verifying Bootstrap Worked

```bash
bd list              # Should show issues
bd vc log            # Should show "Bootstrap from JSONL" commit
```

## Git Hooks Integration

Dolt uses specialized hooks for JSONL synchronization:

| Hook | Purpose |
|------|---------|
| pre-commit | Export Dolt changes to JSONL, stage for commit |
| post-merge | Import pulled JSONL changes into Dolt |

### Installing Hooks

```bash
# Recommended for Dolt projects
bd hooks install --beads

# Or shared across team
bd hooks install --shared
```

### How Hooks Work

**Pre-commit (export):**
1. Checks if Dolt has changes since last export
2. Exports database to `issues.jsonl`
3. Stages JSONL file for git commit
4. Tracks export state per-worktree

**Post-merge (import):**
1. Creates temporary Dolt branch
2. Imports JSONL to that branch
3. Merges using Dolt's cell-level 3-way merge
4. Deletes temporary branch

The branch-then-merge pattern provides better conflict resolution than line-based JSONL merging.

### Verifying Hooks

```bash
bd hooks list        # Shows installed hooks
bd doctor            # Checks hook health
```

## Troubleshooting

### Server Not Running

**Symptom:** Connection refused errors when using server mode.

```
failed to create database: dial tcp 127.0.0.1:3307: connect: connection refused
```

**Fix:**
```bash
gt dolt start        # Gas Town command
# Or
gt dolt status       # Check if running
```

### Bootstrap Not Running

**Symptom:** `bd list` shows nothing on fresh clone.

**Check:**
```bash
ls .beads/issues.jsonl     # Should exist
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

2. **Nuclear option (rebuild from JSONL):**
   ```bash
   rm -rf .beads/dolt
   bd sync                  # Rebuilds from JSONL
   ```

3. **Restore from backup:**
   ```bash
   # If you have a pre-migration backup
   ls .beads/*.backup-*.db
   ```

### Hooks Not Firing

**Symptom:** JSONL not updating on commit, or Dolt not updating on pull.

**Check:**
```bash
bd hooks list              # See what's installed
git config core.hooksPath  # May override .git/hooks
bd doctor                  # Checks hook health
```

**Reinstall:**
```bash
bd hooks install --beads --force
```

### Migration Failed Halfway

**Symptom:** Both SQLite and Dolt exist, unclear state.

**Recovery:**
```bash
# Check what exists
ls .beads/*.db .beads/dolt/

# If Dolt looks incomplete, restart migration
rm -rf .beads/dolt
bd migrate --to-dolt

# If you want to abandon migration
rm -rf .beads/dolt
# SQLite remains as primary
```

### Lock Contention (Embedded Mode)

**Symptom:** "database is locked" errors.

Embedded mode is single-writer. If you need concurrent access:

```bash
# Switch to server mode
gt dolt start
bd config set dolt.mode server
```

## Configuration Reference

```yaml
# .beads/config.yaml

# Database backend
database: dolt           # sqlite | dolt

# Dolt-specific settings
dolt:
  # Auto-commit Dolt history after writes (default: on for embedded, off for server)
  auto-commit: on        # on | off

  # Server mode settings (when mode: server)
  mode: embedded         # embedded | server
  host: 127.0.0.1
  port: 3307
  user: root
  # Password via BEADS_DOLT_PASSWORD env var

# Sync mode (how JSONL and database stay in sync)
sync:
  mode: git-portable     # git-portable | dolt-native | belt-and-suspenders
```

### Environment Variables

| Variable | Purpose |
|----------|---------|
| `BEADS_DOLT_PASSWORD` | Server mode password |
| `BEADS_DOLT_SERVER_MODE` | Enable server mode (set to "1") |
| `BEADS_DOLT_SERVER_HOST` | Server host (default: 127.0.0.1) |
| `BEADS_DOLT_SERVER_PORT` | Server port (default: 3307) |
| `BD_DOLT_AUTO_COMMIT` | Override auto-commit setting |

**Note**: Backend selection (`sqlite` vs `dolt`) is controlled by `metadata.json`,
not environment variables. This prevents accidental data fragmentation across backends.

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

In **embedded mode** (default), each `bd` write command creates a Dolt commit:

```bash
bd create "New issue"    # Creates issue + Dolt commit
```

In **server mode**, auto-commit defaults to OFF because the server manages its
own transaction lifecycle. Firing `DOLT_COMMIT` after every write under
concurrent load causes 'database is read only' errors.

Override for batch operations (embedded) or explicit commits (server):

```bash
bd --dolt-auto-commit off create "Issue 1"
bd --dolt-auto-commit off create "Issue 2"
bd vc commit -m "Batch: created issues"
```

## Server Management (Gas Town)

Gas Town provides integrated Dolt server management:

```bash
gt dolt start            # Start server (background)
gt dolt stop             # Stop server
gt dolt status           # Show server status
gt dolt logs             # View server logs
gt dolt sql              # Open SQL shell
```

Server runs on port 3307 (avoids MySQL conflict on 3306).

### Data Location

```
~/.dolt-data/
├── beads/               # HQ database
├── beads_rig/           # Beads rig database
└── gastown/             # Gas Town database
```

## Migration Cleanup

After successful migration, you may have backup files:

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

- [CONFIG.md](CONFIG.md) - Full configuration reference
- [GIT_INTEGRATION.md](GIT_INTEGRATION.md) - Git hooks and sync workflows
- [TROUBLESHOOTING.md](TROUBLESHOOTING.md) - General troubleshooting
- [SYNC.md](SYNC.md) - Sync modes and strategies
