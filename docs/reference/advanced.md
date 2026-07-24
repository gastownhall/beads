---
title: Advanced Features
description: Advanced bd operations for renaming issues and prefixes, merging duplicates, compaction, database redirects, and performance tuning.
---

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

## Prefix Rename

Change the issue prefix for every issue in the database — for example,
shortening `knowledge-work-` to `kw-`:

```bash
bd rename-prefix kw- --dry-run  # Preview without applying
bd rename-prefix kw-            # Every knowledge-work-* ID becomes kw-*
```

The rename updates all issue IDs and all text references across all fields.
Prefixes are at most 8 characters of lowercase letters, numbers, and hyphens,
must start with a letter, and must end with a hyphen. If a corrupted database
contains issues with multiple prefixes, `bd rename-prefix <prefix> --repair`
consolidates them. See [bd rename-prefix](/cli-reference/rename-prefix).

## Duplicate Detection and Merge

Find issues with identical content (title, description, design, acceptance
criteria) and consolidate them:

```bash
bd duplicates              # Report duplicate groups with suggested actions
bd duplicates --dry-run    # Preview what --auto-merge would do
bd duplicates --auto-merge # Merge every duplicate group
```

Issues are grouped by content hash, and only when their statuses match (open
with open, closed with closed). The merge target is the most-referenced issue
in each group, falling back to the smallest ID. For each group, `--auto-merge`:

- Re-parents children of the duplicates onto the target
- Closes the duplicates with reason `Duplicate of <target>`
- Links each duplicate to the target with a `related` dependency

To mark a single known duplicate manually:

```bash
bd duplicate bd-42 --of bd-41  # Close bd-42 as a duplicate of bd-41
```

Closing is permanent, but Dolt version history preserves the original state.
Verify results with `bd show bd-41` and `bd dep tree bd-41`.

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

Recover the pre-compaction content of a compacted issue:

```bash
bd restore bd-42          # Display the archived original content
bd restore bd-42 --apply  # Write the original content back into the issue
```

If no archived snapshot exists, `bd restore` falls back to a best-effort
reconstruction from Dolt version history, which can only be displayed, not
applied.

## Database Inspection

`bd sql` requires Dolt server mode (`bd dolt start`, see Performance Tuning
below); it is not available against the default embedded-mode database.

```bash
# Schema info
bd info --schema --json

# Raw database query (server mode only)
bd sql "SELECT * FROM issues LIMIT 5"
```

## Database Redirects

Multiple git clones can share one beads database — useful when several agents
or checkout directories work the same issues. Create a `.beads/redirect` file
in the secondary clone containing a single path (relative or absolute) to the
target `.beads` directory:

```bash
# In the secondary clone
mkdir -p .beads
echo "../main-clone/.beads" > .beads/redirect
```

Check which database is actually in use:

```bash
bd where          # Active .beads location, including redirect info
bd where --json
```

Limitations and guidance:

- Redirect chains are not followed — only a single level works, so a redirect
  must point directly at the real `.beads` directory.
- The target directory must exist and contain a valid database.
- Give separate projects and long-lived forks their own databases instead of
  redirects.
- Git worktrees don't need redirects — linked worktrees discover the
  repository's `.beads` workspace automatically. See
  [Git Worktrees](/reference/worktrees).

## Extensible Database

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

Bootstrap a new database from a JSONL export:

```bash
# In the source project
bd export -o issues.jsonl

# In the new project: place the export at .beads/issues.jsonl
# (or the configured import.path), then initialize from it
bd init --from-jsonl
```

Importing records whose IDs already exist updates those issues in place —
hash IDs are content-derived and stable, so a matching ID is an update, not a
collision.

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

The [MCP server](/integrations/mcp-server) is a stateless adapter over the
same boundary: it translates MCP calls into `bd` CLI invocations and routes
each call to the correct `.beads` workspace based on the working directory.
It never caches or stores issue data itself.

## Performance Tuning

### Large Databases

```bash
# Summarize old closed issues (see Database Compaction above)
bd admin compact --stats

# Reclaim disk space with Dolt garbage collection
bd admin compact --dolt

# Squash Dolt commits older than 30 days (preview first)
bd compact --dry-run
bd compact --force
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
