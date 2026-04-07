---
id: sync
title: Sync & Export
sidebar_position: 6
---

# Sync & Export Commands

Commands for synchronizing with Dolt.

## bd dolt push

Push changes to a Dolt remote.

```bash
bd dolt push [flags]
```

**What it does:**
1. Dolt commit (snapshot current database state)
2. Push commits to Dolt remote

**Examples:**
```bash
bd dolt push
```

**When to use:**
- End of work session
- Before switching machines
- After significant changes

## bd dolt pull

Pull changes from a Dolt remote.

```bash
bd dolt pull [flags]
```

**What it does:**
1. Fetches commits from Dolt remote
2. Merges into local database

**Examples:**
```bash
bd dolt pull
```

**When to use:**
- Start of work session
- After switching machines
- Before creating new issues (to avoid duplicates)

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
- Must manually push to remote

```bash
bd create "CI-generated task"
bd dolt push  # Manual push needed
```

## Conflict Resolution

Dolt handles conflict resolution at the database level using its built-in
merge capabilities. When conflicts arise during `bd dolt pull`, Dolt identifies
conflicting rows and allows resolution through SQL.

```bash
# Check for conflicts after pull
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

# Deletions propagate via Dolt push
bd dolt push
```

## Best Practices

1. **Always push at session end** - `bd dolt push`
2. **Always pull at session start** - `bd dolt pull`
3. **Install git hooks** - `bd hooks install`
4. **Check sync status** - `bd info` shows sync state
