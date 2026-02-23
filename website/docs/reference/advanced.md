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

```bash
# Schema info
bd info --schema --json

# Raw database query (advanced)
sqlite3 .beads/beads.db "SELECT * FROM issues LIMIT 5"
```

## Custom Tables

Extend the database with custom tables:

```go
// In Go code using beads as library
storage.UnderlyingDB().Exec(`
  CREATE TABLE IF NOT EXISTS custom_table (...)
`)
```

See [EXTENDING.md](https://github.com/steveyegge/beads/blob/main/docs/EXTENDING.md).

## Event System

Subscribe to beads events:

```bash
# View recent events
bd events list --since 1h

# Watch events in real-time
bd events watch
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
cat issues.jsonl | bd import -i -
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

## API Access

Use beads as a Go library:

```go
import "github.com/steveyegge/beads/internal/storage"

db, _ := storage.NewSQLite(".beads/beads.db")
issues, _ := db.ListIssues(storage.ListOptions{
    Status: "open",
})
```

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
