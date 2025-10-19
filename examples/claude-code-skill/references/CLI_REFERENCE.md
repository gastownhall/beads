# CLI Reference

Complete command reference for bd (beads) CLI tool. All commands support `--json` flag for structured output.

## Contents

- [Quick Reference](#quick-reference)
- [Global Flags](#global-flags)
- [Core Commands](#core-commands)
  - [bd ready](#bd-ready) - Find unblocked work
  - [bd create](#bd-create) - Create new issues
  - [bd update](#bd-update) - Update issue status, priority, assignee
  - [bd close](#bd-close) - Close completed work
  - [bd show](#bd-show) - Show issue details
  - [bd list](#bd-list) - List issues with filters
- [Dependency Commands](#dependency-commands)
  - [bd dep add](#bd-dep-add) - Create dependencies
  - [bd dep tree](#bd-dep-tree) - Visualize dependency trees
  - [bd dep cycles](#bd-dep-cycles) - Detect circular dependencies
- [Monitoring Commands](#monitoring-commands)
  - [bd stats](#bd-stats) - Project statistics
  - [bd blocked](#bd-blocked) - Find blocked work
- [Data Management Commands](#data-management-commands)
  - [bd export](#bd-export) - Export database to JSONL
  - [bd import](#bd-import) - Import issues from JSONL
- [Setup Commands](#setup-commands)
  - [bd init](#bd-init) - Initialize database
  - [bd quickstart](#bd-quickstart) - Show quick start guide
- [Common Workflows](#common-workflows)
- [JSON Output](#json-output)
- [Database Auto-Discovery](#database-auto-discovery)
- [Git Integration](#git-integration)
- [Tips](#tips)

## Quick Reference

| Command | Purpose | Key Flags |
|---------|---------|-----------|
| `bd ready` | Find unblocked work | `--priority`, `--assignee`, `--limit`, `--json` |
| `bd list` | List all issues with filters | `--status`, `--priority`, `--type`, `--assignee` |
| `bd show` | Show issue details | `--json` |
| `bd create` | Create new issue | `-t`, `-p`, `-d`, `--design`, `--acceptance` |
| `bd update` | Update existing issue | `--status`, `--priority`, `--design` |
| `bd close` | Close completed issue | `--reason` |
| `bd dep add` | Add dependency | `--type` (blocks, related, parent-child, discovered-from) |
| `bd dep tree` | Visualize dependency tree | (no flags) |
| `bd dep cycles` | Detect circular dependencies | (no flags) |
| `bd stats` | Get project statistics | `--json` |
| `bd blocked` | Find blocked issues | `--json` |
| `bd export` | Export issues to JSONL | `--json` |
| `bd import` | Import issues from JSONL | `--resolve-collisions` |
| `bd init` | Initialize bd in directory | `--prefix` |
| `bd quickstart` | Show quick start guide | (no flags) |

## Global Flags

Available for all commands:

```bash
--json                 # Output in JSON format
--db /path/to/db       # Specify database path (default: auto-discover)
--actor "name"         # Actor name for audit trail
--no-auto-flush        # Disable automatic JSONL sync
--no-auto-import       # Disable automatic JSONL import
```

## Core Commands

### bd ready

Find tasks with no blockers - ready to be worked on.

```bash
bd ready                      # All ready work
bd ready --json               # JSON format
bd ready --priority 0         # Only priority 0 (critical)
bd ready --assignee alice     # Only assigned to alice
bd ready --limit 5            # Limit to 5 results
```

**Use at session start** to see available work.

---

### bd create

Create a new issue with optional metadata.

```bash
bd create "Title"
bd create "Title" -t bug -p 0
bd create "Title" -d "Description"
bd create "Title" --design "Design notes"
bd create "Title" --acceptance "Definition of done"
bd create "Title" --assignee alice
```

**Flags**:
- `-t, --type`: task (default), bug, feature, epic, chore
- `-p, --priority`: 0-3 (default: 2)
- `-d, --description`: Issue description
- `--design`: Design notes
- `--acceptance`: Acceptance criteria
- `--assignee`: Who should work on this

---

### bd update

Update an existing issue's metadata.

```bash
bd update issue-123 --status in_progress
bd update issue-123 --priority 0
bd update issue-123 --design "Decided to use Redis"
bd update issue-123 --acceptance "Tests passing"
```

**Status values**: open, in_progress, blocked, closed

---

### bd close

Close (complete) an issue.

```bash
bd close issue-123
bd close issue-123 --reason "Implemented in PR #42"
bd close issue-1 issue-2 issue-3 --reason "Bulk close"
```

**Note**: Closed issues remain in database for history.

---

### bd show

Show detailed information about a specific issue.

```bash
bd show issue-123
bd show issue-123 --json
```

Shows: all fields, dependencies, dependents, audit history.

---

### bd list

List all issues with optional filters.

```bash
bd list                          # All issues
bd list --status open            # Only open
bd list --priority 0             # Critical
bd list --type bug               # Only bugs
bd list --assignee alice         # By assignee
bd list --status closed --limit 10  # Recent completions
```

---

## Dependency Commands

### bd dep add

Add a dependency between issues.

```bash
bd dep add from-issue to-issue                      # blocks (default)
bd dep add from-issue to-issue --type blocks
bd dep add from-issue to-issue --type related
bd dep add epic-id task-id --type parent-child
bd dep add original-id found-id --type discovered-from
```

**Dependency types**:
1. **blocks**: from-issue blocks to-issue (hard blocker)
2. **related**: Soft link (no blocking)
3. **parent-child**: Epic/subtask hierarchy
4. **discovered-from**: Tracks origin of discovery

---

### bd dep tree

Visualize full dependency tree for an issue.

```bash
bd dep tree issue-123

# Limit tree depth (default: 50)
bd dep tree issue-123 --max-depth 10
bd dep tree issue-123 -d 10
```

Shows all dependencies and dependents in tree format. Use `--max-depth` to limit traversal depth for very deep trees.

---

### bd dep cycles

Detect circular dependencies.

```bash
bd dep cycles
```

Finds dependency cycles that would prevent work from being ready.

---

## Monitoring Commands

### bd stats

Get project statistics.

```bash
bd stats
bd stats --json
```

Returns: total, open, in_progress, closed, blocked, ready, avg lead time.

---

### bd blocked

Get blocked issues with blocker information.

```bash
bd blocked
bd blocked --json
```

Use to identify bottlenecks when ready list is empty.

---

## Data Management Commands

### bd export

Export all issues to JSONL format.

```bash
bd export > issues.jsonl
bd export --json  # Same output, explicit flag
```

**Use cases:**
- Manual backup before risky operations
- Sharing issues across databases
- Version control / git tracking
- Data migration or analysis

**Note**: bd auto-exports to `.beads/*.jsonl` after each operation (5s debounce). Manual export is rarely needed.

---

### bd import

Import issues from JSONL format.

```bash
bd import < issues.jsonl
bd import --resolve-collisions < issues.jsonl
```

**Flags:**
- `--resolve-collisions` - Automatically remap conflicting issue IDs

**Use cases for --resolve-collisions:**
- **Reimporting** after manual JSONL edits - if you closed an issue in the JSONL that's still open in DB
- **Merging databases** - importing issues from another database with overlapping IDs
- **Restoring from backup** - when database state has diverged from JSONL

**What --resolve-collisions does:**
1. Detects ID conflicts (same ID, different status/content)
2. Remaps conflicting imports to new IDs
3. Updates all references and dependencies to use new IDs
4. Reports remapping (e.g., "mit-1 → bd-4")

**Without --resolve-collisions**: Import fails on first conflict.

**Example scenario:**
```bash
# You have: mit-1 (open) in database
# Importing: mit-1 (closed) from JSONL
# Result: Import creates bd-4 with closed status, preserves existing mit-1
```

---

## Setup Commands

### bd init

Initialize bd in current directory.

```bash
bd init                    # Auto-detect prefix
bd init --prefix api       # Custom prefix
```

Creates `.beads/` directory and database.

---

### bd quickstart

Show comprehensive quick start guide.

```bash
bd quickstart
```

Displays built-in reference for command syntax and workflows.

---

## Common Workflows

### Session Start

```bash
bd ready --json
bd show issue-123
bd update issue-123 --status in_progress
```

### Discovery During Work

```bash
bd create "Found: bug in auth" -t bug
bd dep add current-issue new-issue --type discovered-from
```

### Completing Work

```bash
bd close issue-123 --reason "Implemented with tests passing"
bd ready  # See what unblocked
```

### Planning Epic

```bash
bd create "OAuth Integration" -t epic
bd create "Set up credentials" -t task
bd create "Implement flow" -t task

bd dep add oauth-epic oauth-creds --type parent-child
bd dep add oauth-epic oauth-flow --type parent-child
bd dep add oauth-creds oauth-flow  # creds blocks flow

bd dep tree oauth-epic
```

---

## JSON Output

All commands support `--json` for structured output:

```bash
bd ready --json
bd show issue-123 --json
bd list --status open --json
bd stats --json
```

Use when parsing programmatically or extracting specific fields.

---

## Database Auto-Discovery

bd finds database in this order:

1. `--db` flag: `bd ready --db /path/to/db.db`
2. `$BEADS_DB` environment variable
3. `.beads/*.db` in current directory or ancestors
4. `~/.beads/default.db` as fallback

**Project-local** (`.beads/`): Project-specific work, git-tracked

**Global fallback** (`~/.beads/`): Cross-project tracking, personal tasks

---

## Git Integration

bd automatically syncs with git:

- **After each operation**: Exports to JSONL (5s debounce)
- **After git pull**: Imports from JSONL if newer than DB

**Files**:
- `.beads/*.jsonl` - Source of truth (git-tracked)
- `.beads/*.db` - Local cache (gitignored)

### Git Integration Troubleshooting

**Problem: `.gitignore` ignores entire `.beads/` directory**

**Symptom**: JSONL file not tracked in git, can't commit beads

**Cause**: Incorrect `.gitignore` pattern blocks everything

**Fix**:
```bash
# Check .gitignore
cat .gitignore | grep beads

# ❌ WRONG (ignores everything including JSONL):
.beads/

# ✅ CORRECT (ignores only SQLite cache):
.beads/*.db
.beads/*.db-*
```

**After fixing**: Remove the `.beads/` line and add the specific patterns. Then `git add .beads/issues.jsonl`.

---

### Permission Troubleshooting

**Problem: bd commands prompt for permission despite whitelist**

**Symptom**: `bd` commands ask for confirmation even with `Bash(bd:*)` in settings.local.json

**Root Cause**: Wildcard patterns in settings.local.json don't actually work - not for bd, not for git, not for any Bash commands. This is a general Claude Code limitation, not bd-specific.

**How It Actually Works**:
- Individual command approvals (like `Bash(bd ready)`) DO persist across sessions
- These are stored server-side by Claude Code, not in local config files
- Commands like `git status` work without prompting because they've been individually approved many times, creating the illusion of a working wildcard pattern

**Permanent Solution**:
1. Trigger each bd subcommand you use frequently (see command list below)
2. When prompted, click "Yes, and don't ask again" (NOT "Allow this time")
3. That specific command will be permanently approved across all future sessions

**Common bd Commands to Approve**:
```bash
bd ready
bd list
bd stats
bd blocked
bd export
bd version
bd quickstart
bd dep cycles
bd --help
bd [command] --help  # For any subcommand help
```

**Note**: Dynamic commands with arguments (like `bd show <issue-id>`, `bd create "title"`) must be approved per-use since arguments vary. Only static commands can be permanently whitelisted.

---

## Tips

**Use JSON for parsing**:
```bash
bd ready --json | jq '.[0].id'
```

**Bulk operations**:
```bash
bd close issue-1 issue-2 issue-3 --reason "Sprint complete"
```

**Quick filtering**:
```bash
bd list --status open --priority 0 --type bug
```

**Built-in help**:
```bash
bd quickstart       # Comprehensive guide
bd create --help    # Command-specific help
```
