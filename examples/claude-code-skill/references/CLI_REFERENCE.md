# CLI Command Reference

**For:** AI agents and developers using bd command-line interface
**Version:** 0.21.0+

## Quick Navigation

- [Basic Operations](#basic-operations)
- [Issue Management](#issue-management)
- [Dependencies & Labels](#dependencies--labels)
- [Filtering & Search](#filtering--search)
- [Advanced Operations](#advanced-operations)
- [Database Management](#database-management)

## Basic Operations

### Check Status

```bash
# Check database path and daemon status
bd info --json

# Example output:
# {
#   "database_path": "/path/to/.beads/beads.db",
#   "issue_prefix": "bd",
#   "daemon_running": true,
#   "agent_mail_enabled": false
# }
```

### Find Work

```bash
# Find ready work (no blockers)
bd ready --json

# Find stale issues (not updated recently)
bd stale --days 30 --json                    # Default: 30 days
bd stale --days 90 --status in_progress --json  # Filter by status
bd stale --limit 20 --json                   # Limit results
```

## Issue Management

### Create Issues

```bash
# Basic creation
# IMPORTANT: Always quote titles and descriptions with double quotes
bd create "Issue title" -t bug|feature|task -p 0-4 -d "Description" --json

# Create with explicit ID (for parallel workers)
bd create "Issue title" --id worker1-100 -p 1 --json

# Create with labels (--labels or --label work)
bd create "Issue title" -t bug -p 1 -l bug,critical --json
bd create "Issue title" -t bug -p 1 --label bug,critical --json

# Examples with special characters (all require quoting):
bd create "Fix: auth doesn't validate tokens" -t bug -p 1 --json
bd create "Add support for OAuth 2.0" -d "Implement RFC 6749 (OAuth 2.0 spec)" --json

# Create multiple issues from markdown file
bd create -f feature-plan.md --json

# Create epic with hierarchical child tasks
bd create "Auth System" -t epic -p 1 --json         # Returns: bd-a3f8e9
bd create "Login UI" -p 1 --json                     # Auto-assigned: bd-a3f8e9.1
bd create "Backend validation" -p 1 --json           # Auto-assigned: bd-a3f8e9.2
bd create "Tests" -p 1 --json                        # Auto-assigned: bd-a3f8e9.3

# Create and link discovered work (one command)
bd create "Found bug" -t bug -p 1 --deps discovered-from:<parent-id> --json
```

### Update Issues

```bash
# Update one or more issues
bd update <id> [<id>...] --status in_progress --json
bd update <id> [<id>...] --priority 1 --json

# Edit issue fields in $EDITOR (HUMANS ONLY - not for agents)
# NOTE: This command is intentionally NOT exposed via the MCP server
# Agents should use 'bd update' with field-specific parameters instead
bd edit <id>                    # Edit description
bd edit <id> --title            # Edit title
bd edit <id> --design           # Edit design notes
bd edit <id> --notes            # Edit notes
bd edit <id> --acceptance       # Edit acceptance criteria
```

### Close/Reopen Issues

```bash
# Complete work (supports multiple IDs)
bd close <id> [<id>...] --reason "Done" --json

# Reopen closed issues (supports multiple IDs)
bd reopen <id> [<id>...] --reason "Reopening" --json
```

### View Issues

```bash
# Show dependency tree
bd dep tree <id>

# Get issue details (supports multiple IDs)
bd show <id> [<id>...] --json
```

## Dependencies & Labels

### Dependencies

```bash
# Link discovered work (old way - two commands)
bd dep add <discovered-id> <parent-id> --type discovered-from

# Create and link in one command (new way - preferred)
bd create "Issue title" -t bug -p 1 --deps discovered-from:<parent-id> --json
```

### Labels

```bash
# Label management (supports multiple IDs)
bd label add <id> [<id>...] <label> --json
bd label remove <id> [<id>...] <label> --json
bd label list <id> --json
bd label list-all --json
```

## Filtering & Search

### Basic Filters

```bash
# Filter by status, priority, type
bd list --status open --priority 1 --json               # Status and priority
bd list --assignee alice --json                         # By assignee
bd list --type bug --json                               # By issue type
bd list --id bd-123,bd-456 --json                       # Specific IDs
```

### Label Filters

```bash
# Labels (AND: must have ALL)
bd list --label bug,critical --json

# Labels (OR: has ANY)
bd list --label-any frontend,backend --json
```

### Text Search

```bash
# Title search (substring)
bd list --title "auth" --json

# Pattern matching (case-insensitive substring)
bd list --title-contains "auth" --json                  # Search in title
bd list --desc-contains "implement" --json              # Search in description
bd list --notes-contains "TODO" --json                  # Search in notes
```

### Date Range Filters

```bash
# Date range filters (YYYY-MM-DD or RFC3339)
bd list --created-after 2024-01-01 --json               # Created after date
bd list --created-before 2024-12-31 --json              # Created before date
bd list --updated-after 2024-06-01 --json               # Updated after date
bd list --updated-before 2024-12-31 --json              # Updated before date
bd list --closed-after 2024-01-01 --json                # Closed after date
bd list --closed-before 2024-12-31 --json               # Closed before date
```

### Empty/Null Checks

```bash
# Empty/null checks
bd list --empty-description --json                      # Issues with no description
bd list --no-assignee --json                            # Unassigned issues
bd list --no-labels --json                              # Issues with no labels
```

### Priority Ranges

```bash
# Priority ranges
bd list --priority-min 0 --priority-max 1 --json        # P0 and P1 only
bd list --priority-min 2 --json                         # P2 and below
```

### Combine Filters

```bash
# Combine multiple filters
bd list --status open --priority 1 --label-any urgent,critical --no-assignee --json
```

## Global Flags

Global flags work with any bd command and must appear **before** the subcommand.

### Sandbox Mode

**Auto-detection (v0.21.1+):** bd automatically detects sandboxed environments and enables sandbox mode.

When detected, you'll see: `ℹ️  Sandbox detected, using direct mode`

**Manual override:**

```bash
# Explicitly enable sandbox mode
bd --sandbox <command>

# Equivalent to combining these flags:
bd --no-daemon --no-auto-flush --no-auto-import <command>
```

**What it does:**
- Disables daemon (uses direct SQLite mode)
- Disables auto-export to JSONL
- Disables auto-import from JSONL

**When to use:** Sandboxed environments where daemon can't be controlled (permission restrictions), or when auto-detection doesn't trigger.

### Staleness Control

```bash
# Skip staleness check (emergency escape hatch)
bd --allow-stale <command>

# Example: access database even if out of sync with JSONL
bd --allow-stale ready --json
bd --allow-stale list --status open --json
```

**Shows:** `⚠️  Staleness check skipped (--allow-stale), data may be out of sync`

**⚠️ Caution:** May show stale or incomplete data. Use only when stuck and other options fail.

### Force Import

```bash
# Force metadata update even when DB appears synced
bd import --force -i .beads/beads.jsonl
```

**When to use:** `bd import` reports "0 created, 0 updated" but staleness errors persist.

**Shows:** `Metadata updated (database already in sync with JSONL)`

### Other Global Flags

```bash
# JSON output for programmatic use
bd --json <command>

# Force direct mode (bypass daemon)
bd --no-daemon <command>

# Disable auto-sync
bd --no-auto-flush <command>    # Disable auto-export to JSONL
bd --no-auto-import <command>   # Disable auto-import from JSONL

# Custom database path
bd --db /path/to/.beads/beads.db <command>

# Custom actor for audit trail
bd --actor alice <command>
```

**See also:**
- [TROUBLESHOOTING.md - Sandboxed environments](TROUBLESHOOTING.md#sandboxed-environments-codex-claude-code-etc) for detailed sandbox troubleshooting
- [DAEMON.md](DAEMON.md) for daemon mode details

## Advanced Operations

### Cleanup

```bash
# Clean up closed issues (bulk deletion)
bd cleanup --force --json                                   # Delete ALL closed issues
bd cleanup --older-than 30 --force --json                   # Delete closed >30 days ago
bd cleanup --dry-run --json                                 # Preview what would be deleted
bd cleanup --older-than 90 --cascade --force --json         # Delete old + dependents
```

### Duplicate Detection & Merging

```bash
# Find and merge duplicate issues
bd duplicates                                          # Show all duplicates
bd duplicates --auto-merge                             # Automatically merge all
bd duplicates --dry-run                                # Preview merge operations

# Merge specific duplicate issues
bd merge <source-id...> --into <target-id> --json      # Consolidate duplicates
bd merge bd-42 bd-43 --into bd-41 --dry-run            # Preview merge
```

### Compaction (Memory Decay)

```bash
# Agent-driven compaction
bd compact --analyze --json                           # Get candidates for review
bd compact --analyze --tier 1 --limit 10 --json       # Limited batch
bd compact --apply --id bd-42 --summary summary.txt   # Apply compaction
bd compact --apply --id bd-42 --summary - < summary.txt  # From stdin
bd compact --stats --json                             # Show statistics

# Legacy AI-powered compaction (requires ANTHROPIC_API_KEY)
bd compact --auto --dry-run --all                     # Preview
bd compact --auto --all --tier 1                      # Auto-compact tier 1

# Restore compacted issue from git history
bd restore <id>  # View full history at time of compaction
```

### Rename Prefix

```bash
# Rename issue prefix (e.g., from 'knowledge-work-' to 'kw-')
bd rename-prefix kw- --dry-run  # Preview changes
bd rename-prefix kw- --json     # Apply rename
```

## Database Management

### Import/Export

```bash
# Import issues from JSONL
bd import -i .beads/issues.jsonl --dry-run      # Preview changes
bd import -i .beads/issues.jsonl                # Import and update issues
bd import -i .beads/issues.jsonl --dedupe-after # Import + detect duplicates

# Handle missing parents during import
bd import -i issues.jsonl --orphan-handling allow      # Default: import orphans without validation
bd import -i issues.jsonl --orphan-handling resurrect  # Auto-resurrect deleted parents as tombstones
bd import -i issues.jsonl --orphan-handling skip       # Skip orphans with warning
bd import -i issues.jsonl --orphan-handling strict     # Fail if parent is missing

# Configure default orphan handling behavior
bd config set import.orphan_handling "resurrect"
bd sync  # Now uses resurrect mode by default
```

**Orphan handling modes:**

- **`allow` (default)** - Import orphaned children without parent validation. Most permissive, ensures no data loss even if hierarchy is temporarily broken.
- **`resurrect`** - Search JSONL history for deleted parents and recreate them as tombstones (Status=Closed, Priority=4). Preserves hierarchy with minimal data. Dependencies are also resurrected on best-effort basis.
- **`skip`** - Skip orphaned children with warning. Partial import succeeds but some issues are excluded.
- **`strict`** - Fail import immediately if a child's parent is missing. Use when database integrity is critical.

**When to use:**
- Use `allow` (default) for daily imports and auto-sync
- Use `resurrect` when importing from databases with deleted parents
- Use `strict` for controlled imports requiring guaranteed parent existence
- Use `skip` rarely - only for selective imports

See [CONFIG.md](CONFIG.md#example-import-orphan-handling) and [TROUBLESHOOTING.md](TROUBLESHOOTING.md#import-fails-with-missing-parent-errors) for more details.

### Migration

```bash
# Migrate databases after version upgrade
bd migrate                                             # Detect and migrate old databases
bd migrate --dry-run                                   # Preview migration
bd migrate --cleanup --yes                             # Migrate and remove old files

# AI-supervised migration (check before running bd migrate)
bd migrate --inspect --json                            # Show migration plan for AI agents
bd info --schema --json                                # Get schema, tables, config, sample IDs
```

**Migration workflow for AI agents:**

1. Run `--inspect` to see pending migrations and warnings
2. Check for `missing_config` (like issue_prefix)
3. Review `invariants_to_check` for safety guarantees
4. If warnings exist, fix config issues first
5. Then run `bd migrate` safely

**Migration safety invariants:**

- **required_config_present**: Ensures issue_prefix and schema_version are set
- **foreign_keys_valid**: No orphaned dependencies or labels
- **issue_count_stable**: Issue count doesn't decrease unexpectedly

These invariants prevent data loss and would have caught issues like GH #201 (missing issue_prefix after migration).

### Daemon Management

See [docs/DAEMON.md](DAEMON.md) for complete daemon management reference.

```bash
# List all running daemons
bd daemons list --json

# Check health (version mismatches, stale sockets)
bd daemons health --json

# Stop/restart specific daemon
bd daemons stop /path/to/workspace --json
bd daemons restart 12345 --json  # By PID

# View daemon logs
bd daemons logs /path/to/workspace -n 100
bd daemons logs 12345 -f  # Follow mode

# Stop all daemons
bd daemons killall --json
bd daemons killall --force --json  # Force kill if graceful fails
```

### Sync Operations

```bash
# Manual sync (force immediate export/import/commit/push)
bd sync

# What it does:
# 1. Export pending changes to JSONL
# 2. Commit to git
# 3. Pull from remote
# 4. Import any updates
# 5. Push to remote
```

## Issue Types

- `bug` - Something broken that needs fixing
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature composed of multiple issues (supports hierarchical children)
- `chore` - Maintenance work (dependencies, tooling)

**Hierarchical children:** Epics can have child issues with dotted IDs (e.g., `bd-a3f8e9.1`, `bd-a3f8e9.2`). Children are auto-numbered sequentially. Up to 3 levels of nesting supported.

## Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (nice-to-have features, minor bugs)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

## Dependency Types

- `blocks` - Hard dependency (issue X blocks issue Y)
- `related` - Soft relationship (issues are connected)
- `parent-child` - Epic/subtask relationship
- `discovered-from` - Track issues discovered during work

Only `blocks` dependencies affect the ready work queue.

**Note:** When creating an issue with a `discovered-from` dependency, the new issue automatically inherits the parent's `source_repo` field.

## Output Formats

### JSON Output (Recommended for Agents)

Always use `--json` flag for programmatic use:

```bash
# Single issue
bd show bd-42 --json

# List of issues
bd ready --json

# Operation result
bd create "Issue" -p 1 --json
```

### Human-Readable Output

Default output without `--json`:

```bash
bd ready
# bd-42  Fix authentication bug  [P1, bug, in_progress]
# bd-43  Add user settings page  [P2, feature, open]
```

## Common Patterns for AI Agents

### Claim and Complete Work

```bash
# 1. Find available work
bd ready --json

# 2. Claim issue
bd update bd-42 --status in_progress --json

# 3. Work on it...

# 4. Close when done
bd close bd-42 --reason "Implemented and tested" --json
```

### Discover and Link Work

```bash
# While working on bd-100, discover a bug

# Old way (two commands):
bd create "Found auth bug" -t bug -p 1 --json  # Returns bd-101
bd dep add bd-101 bd-100 --type discovered-from

# New way (one command):
bd create "Found auth bug" -t bug -p 1 --deps discovered-from:bd-100 --json
```

### Batch Operations

```bash
# Update multiple issues at once
bd update bd-41 bd-42 bd-43 --priority 0 --json

# Close multiple issues
bd close bd-41 bd-42 bd-43 --reason "Batch completion" --json

# Add label to multiple issues
bd label add bd-41 bd-42 bd-43 urgent --json
```

### Session Workflow

```bash
# Start of session
bd ready --json  # Find work

# During session
bd create "..." -p 1 --json
bd update bd-42 --status in_progress --json
# ... work ...

# End of session (IMPORTANT!)
bd sync  # Force immediate sync, bypass debounce
```

**ALWAYS run `bd sync` at end of agent sessions** to ensure changes are committed/pushed immediately.

## See Also

- [AGENTS.md](../AGENTS.md) - Main agent workflow guide
- [DAEMON.md](DAEMON.md) - Daemon management and event-driven mode
- [GIT_INTEGRATION.md](GIT_INTEGRATION.md) - Git workflows and merge strategies
- [LABELS.md](../LABELS.md) - Label system guide
- [README.md](../README.md) - User documentation
