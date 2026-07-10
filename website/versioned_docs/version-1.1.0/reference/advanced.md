---
id: advanced
title: Advanced Features
sidebar_position: 3
---

# Advanced Features

Advanced beads functionality.

## Issue Rename

Rename issues while preserving references:

```bash
bd rename bd-42 bd-new-id
bd rename bd-42 bd-new-id --dry-run  # Preview
```

Updates:
- All dependencies pointing to old ID
- All references in other issues
- Comments and descriptions

## Issue Merge

Merge duplicate issues:

```bash
bd merge bd-42 bd-43 --into bd-41
bd merge bd-42 bd-43 --into bd-41 --dry-run
```

What gets merged:
- Dependencies → target
- Text references updated across all issues
- Source issues closed with merge reason

## Database Compaction

Reduce database size by compacting old issues:

```bash
# View compaction statistics
bd admin compact --stats

# Preview candidates (30+ days closed)
bd admin compact --analyze --json

# Apply agent-generated summary
bd admin compact --apply --id bd-42 --summary summary.txt

# Immediate deletion (CAUTION!)
bd admin cleanup --force
```

**When to compact:**
- Database > 10MB with old closed issues
- After major milestones
- Before archiving project phase

## Restore from History

View deleted or compacted issues from git:

```bash
bd restore bd-42 --show
bd restore bd-42 --to-file issue.json
```

## Database Inspection

`bd sql` requires Dolt server mode (`bd dolt start`, see Performance Tuning
below); it is not available against the default embedded-mode database.

```bash
# Schema info
bd info --schema --json

# Raw database query (server mode only)
bd sql "SELECT * FROM issues LIMIT 5"
```

## Extension Data

For Dolt-backed projects, keep extension state outside the beads database and
connect it to beads through stable CLI surfaces:

```bash
# Query issues for integration workflows
bd list --json
bd query "status=open AND priority<=2" --json

# Run direct SQL for inspection (server mode only)
bd sql "SELECT id, title, status FROM issues LIMIT 5"
```

Custom tables through direct storage access are a legacy SQLite-only pattern.
See the [bd-example-extension-go example](https://github.com/gastownhall/beads/blob/main/examples/bd-example-extension-go/README.md)
only if you are maintaining a SQLite-backed extension.

## Audit Data

Beads records issue lifecycle events in the database for audit and recovery
workflows. There is no standalone `bd events` command; inspect current issue
state through JSON output, or query the audit tables directly when needed:

```bash
# Current issue state
bd show bd-a1b2 --json

# Recent stored events for one issue (server mode only)
bd sql "SELECT event_type, actor, created_at FROM events WHERE issue_id = 'bd-a1b2' ORDER BY created_at DESC LIMIT 20"
```

Events:
- `issue.created`
- `issue.updated`
- `issue.closed`
- `dependency.added`
- `sync.completed`

## Batch Operations

### Create Multiple

```bash
# Bootstrap a new database from JSONL
bd init --from-jsonl issues.jsonl
```

### Update Multiple

```bash
bd list --status open --priority 4 --json | \
  jq -r '.[].id' | \
  xargs -I {} bd update {} --priority 3
```

### Close Multiple

```bash
bd list --label "sprint-1" --status open --json | \
  jq -r '.[].id' | \
  xargs -I {} bd close {} --reason "Sprint complete"
```

## Integration Access

Use the CLI as the supported integration boundary:

```bash
# Machine-readable issue data
bd show bd-a1b2 --json

# Ready-work queue for automation
bd ready --json

# Direct SQL inspection against the active Dolt database (server mode only)
bd sql "SELECT id, priority, status FROM issues WHERE status != 'closed'"
```

The storage packages under `internal/` are not a public Go API.

## Performance Tuning

### Large Databases

```bash
# Enable WAL mode
bd config set database.wal_mode true

# Increase cache
bd config set database.cache_size 10000
```

### Many Concurrent Agents

Beads uses Dolt server mode to handle concurrent access from multiple agents.
The server manages transaction isolation automatically.

```bash
# Start the Dolt server
bd dolt start

# Check server health
bd doctor
```

### CI/CD Optimization

In CI/CD environments, beads uses embedded mode by default (no server required):

```bash
# Just run commands directly — no special flags needed
bd list
```
