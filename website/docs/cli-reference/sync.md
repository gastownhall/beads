---
id: sync
title: Sync & Export
sidebar_position: 6
---

# Sync & Export Commands

Commands for synchronizing with Dolt.

## bd sync

Full sync cycle: Dolt commit and push.

```bash
bd sync [flags]
```

**What it does:**
1. Dolt commit (snapshot current database state)
2. Dolt push to remote

**Flags:**
```bash
--json     JSON output
--dry-run  Preview without changes
```

**Examples:**
```bash
bd sync
bd sync --json
```

**When to use:**
- End of work session
- Before switching branches
- After significant changes

## bd export

Export database to JSONL format (for backup and migration).

```bash
bd export [flags]
```

**Flags:**
```bash
--output, -o    Output file (default: stdout)
--dry-run       Preview without writing
--json          JSON output
```

**Examples:**
```bash
bd export
bd export -o backup.jsonl
bd export --dry-run
```

**When to use:** `bd export` is for backup and data migration, not day-to-day sync. Dolt handles sync natively via `bd dolt push`/`bd dolt pull`.

## bd import

Import from JSONL file (for migration and recovery).

```bash
bd import -i <file> [flags]
```

**Flags:**
```bash
--input, -i           Input file (required)
--dry-run             Preview without changes
--orphan-handling     How to handle missing parents
--dedupe-after        Run duplicate detection after import
--json                JSON output
```

**Orphan handling modes:**
| Mode | Behavior |
|------|----------|
| `allow` | Import orphans without validation (default) |
| `resurrect` | Restore deleted parents as tombstones |
| `skip` | Skip orphaned children with warning |
| `strict` | Fail if parent missing |

**Examples:**
```bash
bd import -i backup.jsonl
bd import -i backup.jsonl --dry-run
bd import -i issues.jsonl --orphan-handling resurrect
bd import -i issues.jsonl --dedupe-after --json
```

**When to use:** `bd import` is for loading data from external JSONL files or migrating from a legacy setup. For day-to-day sync, use `bd dolt push`/`bd dolt pull`.

## bd migrate

Migrate database schema.

```bash
bd migrate [flags]
```

**Flags:**
```bash
--inspect    Show migration plan (for agents)
--dry-run    Preview without changes
--cleanup    Remove old files after migration
--yes        Skip confirmation
--json       JSON output
```

**Examples:**
```bash
bd migrate --inspect --json
bd migrate --dry-run
bd migrate
bd migrate --cleanup --yes
```

## bd hooks

Manage git hooks.

```bash
bd hooks <subcommand> [flags]
```

**Subcommands:**
| Command | Description |
|---------|-------------|
| `install` | Install git hooks |
| `uninstall` | Remove git hooks |
| `status` | Check hook status |

**Examples:**
```bash
bd hooks install
bd hooks status
bd hooks uninstall
```

## Auto-Sync Behavior

### With Dolt Server Mode (Default)

When the Dolt server is running, sync is handled automatically:
- Dolt auto-commit tracks changes
- Dolt-native replication handles remote sync

Start the Dolt server with `bd dolt start`.

### Embedded Mode (No Server)

In CI/CD pipelines and ephemeral environments, no server is needed:
- Changes written directly to the database
- Must manually sync

```bash
bd create "CI-generated task"
bd sync  # Manual sync needed
```

## Conflict Resolution

Dolt handles conflict resolution at the database level using its built-in
merge capabilities. When conflicts arise during `dolt pull`, Dolt identifies
conflicting rows and allows resolution through SQL.

```bash
# Check for conflicts after sync
bd doctor --fix
```

## Deletion Tracking

Deletions are tracked in the Dolt database:

```bash
# Delete issue
bd delete bd-42

# View deletions
bd deleted
bd deleted --since=30d

# Deletions propagate via Dolt sync
bd sync
```

## Best Practices

1. **Always sync at session end** - `bd sync`
2. **Install git hooks** - `bd hooks install`
3. **Check sync status** - `bd info` shows sync state
