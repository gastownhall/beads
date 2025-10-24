# Advanced bd Features

This guide covers advanced features for power users and specific use cases.

## Table of Contents

- [Renaming Prefix](#renaming-prefix)
- [Merging Duplicate Issues](#merging-duplicate-issues)
- [Global Daemon for Multiple Projects](#global-daemon-for-multiple-projects)
- [Multi-Repository Commands](#multi-repository-commands)
- [Git Worktrees](#git-worktrees)
- [Handling Import Collisions](#handling-import-collisions)
- [Custom Git Hooks](#custom-git-hooks)
- [Extensible Database](#extensible-database)

## Renaming Prefix

Change the issue prefix for all issues in your database. This is useful if your prefix is too long or you want to standardize naming.

```bash
# Preview changes without applying
bd rename-prefix kw- --dry-run

# Rename from current prefix to new prefix
bd rename-prefix kw-

# JSON output
bd rename-prefix kw- --json
```

The rename operation:
- Updates all issue IDs (e.g., `knowledge-work-1` → `kw-1`)
- Updates all text references in titles, descriptions, design notes, etc.
- Updates dependencies and labels
- Updates the counter table and config

**Prefix validation rules:**
- Max length: 8 characters
- Allowed characters: lowercase letters, numbers, hyphens
- Must start with a letter
- Must end with a hyphen (or will be trimmed to add one)
- Cannot be empty or just a hyphen

Example workflow:
```bash
# You have issues like knowledge-work-1, knowledge-work-2, etc.
bd list  # Shows knowledge-work-* issues

# Preview the rename
bd rename-prefix kw- --dry-run

# Apply the rename
bd rename-prefix kw-

# Now you have kw-1, kw-2, etc.
bd list  # Shows kw-* issues
```

## Merging Duplicate Issues

Consolidate duplicate issues into a single issue while preserving dependencies and references:

```bash
# Merge bd-42 and bd-43 into bd-41
bd merge bd-42 bd-43 --into bd-41

# Merge multiple duplicates at once
bd merge bd-10 bd-11 bd-12 --into bd-10

# Preview merge without making changes
bd merge bd-42 bd-43 --into bd-41 --dry-run

# JSON output
bd merge bd-42 bd-43 --into bd-41 --json
```

**What the merge command does:**
1. **Validates** all issues exist and prevents self-merge
2. **Closes** source issues with reason `Merged into bd-X`
3. **Migrates** all dependencies from source issues to target
4. **Updates** text references across all issue descriptions, notes, design, and acceptance criteria

**Example workflow:**

```bash
# You discover bd-42 and bd-43 are duplicates of bd-41
bd show bd-41 bd-42 bd-43

# Preview the merge
bd merge bd-42 bd-43 --into bd-41 --dry-run

# Execute the merge
bd merge bd-42 bd-43 --into bd-41
# ✓ Merged 2 issue(s) into bd-41

# Verify the result
bd show bd-41  # Now has dependencies from bd-42 and bd-43
bd dep tree bd-41  # Shows unified dependency tree
```

**Important notes:**
- Source issues are permanently closed (status: `closed`)
- All dependencies pointing to source issues are redirected to target
- Text references like "see bd-42" are automatically rewritten to "see bd-41"
- Operation cannot be undone (but git history preserves the original state)
- Not yet supported in daemon mode (use `--no-daemon` flag)

**AI Agent Workflow:**

When agents discover duplicate issues, they should:
1. Search for similar issues: `bd list --json | grep "similar text"`
2. Compare issue details: `bd show bd-41 bd-42 --json`
3. Merge duplicates: `bd merge bd-42 --into bd-41`
4. File a discovered-from issue if needed: `bd create "Found duplicates during bd-X" --deps discovered-from:bd-X`

## Global Daemon for Multiple Projects

**New in v0.9.11:** Use a single daemon process to serve all projects on your machine.

### Starting the Global Daemon

```bash
# Start global daemon (one per machine)
bd daemon --global

# Verify it's running
ps aux | grep "bd daemon"

# Stop it
pkill -f "bd daemon --global"
```

### Benefits

- **Single process** serves all bd databases on your machine
- **Automatic workspace detection** - no configuration needed
- **Persistent background service** - survives terminal restarts
- **Lower memory footprint** than per-project daemons

### How It Works

```bash
# In any project directory
cd ~/projects/webapp && bd ready         # Uses global daemon
cd ~/projects/api && bd ready            # Uses global daemon
```

The global daemon:
1. Checks for local daemon socket (`.beads/bd.sock`) in your current workspace
2. Routes requests to the correct database based on your current working directory
3. Auto-starts the local daemon if it's not running (with exponential backoff on failures)
4. Each project gets its own isolated daemon serving only its database

**Note:** Global daemon doesn't require git repos, making it suitable for non-git projects or multi-repo setups.

## Multi-Repository Commands

**New in v0.9.12:** When using a global daemon, use `bd repos` to view and manage work across all cached repositories.

```bash
# List all cached repositories
bd repos list

# View ready work across all repos
bd repos ready

# Group ready work by repository
bd repos ready --group

# Filter by priority
bd repos ready --priority 1

# Filter by assignee
bd repos ready --assignee alice

# View combined statistics
bd repos stats

# Clear repository cache (free resources)
bd repos clear-cache
```

**Example output:**

```bash
$ bd repos list

📁 Cached Repositories (3):

/Users/alice/projects/webapp
  Prefix:       webapp-
  Issue Count:  45
  Status:       active

/Users/alice/projects/api
  Prefix:       api-
  Issue Count:  12
  Status:       active

/Users/alice/projects/docs
  Prefix:       docs-
  Issue Count:  8
  Status:       active

$ bd repos ready --group

📋 Ready work across 3 repositories:

/Users/alice/projects/webapp (4 issues):
  1. [P1] webapp-23: Fix navigation bug
     Estimate: 30 min
  2. [P2] webapp-45: Add loading spinner
     Estimate: 15 min
  ...

/Users/alice/projects/api (2 issues):
  1. [P0] api-10: Fix critical auth bug
     Estimate: 60 min
  2. [P1] api-12: Add rate limiting
     Estimate: 45 min

$ bd repos stats

📊 Combined Statistics Across All Repositories:

Total Issues:      65
Open:              23
In Progress:       5
Closed:            37
Blocked:           3
Ready:             15

📁 Per-Repository Breakdown:

/Users/alice/projects/webapp:
  Total: 45  Ready: 10  Blocked: 2

/Users/alice/projects/api:
  Total: 12  Ready: 3  Blocked: 1

/Users/alice/projects/docs:
  Total: 8  Ready: 2  Blocked: 0
```

**Requirements:**
- Global daemon must be running (`bd daemon --global`)
- At least one command has been run in each repository (to cache it)
- `--json` flag available for programmatic use

**Use cases:**
- Get an overview of all active projects
- Find highest-priority work across all repos
- Balance workload across multiple projects
- Track overall progress and statistics
- Identify which repos need attention

## Git Worktrees

**⚠️ Important Limitation:** Daemon mode does not work correctly with `git worktree`.

**The Problem:**
Git worktrees share the same `.git` directory and thus share the same `.beads` database. The daemon doesn't know which branch each worktree has checked out, which can cause it to commit/push to the wrong branch.

**What you lose without daemon mode:**
- **Auto-sync** - No automatic commit/push of changes (use `bd sync` manually)
- **MCP server** - The beads-mcp server requires daemon mode for multi-repo support
- **Background watching** - No automatic detection of remote changes

**Solutions for Worktree Users:**

1. **Use `--no-daemon` flag** (recommended):
   ```bash
   bd --no-daemon ready
   bd --no-daemon create "Fix bug" -p 1
   bd --no-daemon update bd-42 --status in_progress
   ```

2. **Disable daemon via environment variable** (for entire worktree session):
   ```bash
   export BEADS_NO_DAEMON=1
   bd ready  # All commands use direct mode
   ```

3. **Disable auto-start** (less safe, still warns):
   ```bash
   export BEADS_AUTO_START_DAEMON=false
   ```

**Automatic Detection:**
bd automatically detects when you're in a worktree and shows a prominent warning if daemon mode is active. The `--no-daemon` mode works correctly with worktrees since it operates directly on the database without shared state.

**Why It Matters:**
The daemon maintains its own view of the current working directory and git state. When multiple worktrees share the same `.beads` database, the daemon may commit changes intended for one branch to a different branch, leading to confusion and incorrect git history.

## Handling Import Collisions

When merging branches or pulling changes, you may encounter ID collisions (same ID, different content). bd detects and safely handles these:

**Check for collisions after merge:**
```bash
# After git merge or pull
bd import -i .beads/issues.jsonl --dry-run

# Output shows:
# === Collision Detection Report ===
# Exact matches (idempotent): 15
# New issues: 5
# COLLISIONS DETECTED: 3
#
# Colliding issues:
#   bd-10: Fix authentication (conflicting fields: [title, priority])
#   bd-12: Add feature (conflicting fields: [description, status])
```

**Resolve collisions automatically:**
```bash
# Let bd resolve collisions by remapping incoming issues to new IDs
bd import -i .beads/issues.jsonl --resolve-collisions

# bd will:
# - Keep existing issues unchanged
# - Assign new IDs to colliding issues (bd-25, bd-26, etc.)
# - Update ALL text references and dependencies automatically
# - Report the remapping with reference counts
```

**Important**: The `--resolve-collisions` flag is safe and recommended for branch merges. It preserves the existing database and only renumbers the incoming colliding issues. All text mentions like "see bd-10" and dependency links are automatically updated to use the new IDs.

**Manual resolution** (alternative):
If you prefer manual control, resolve the Git conflict in `.beads/issues.jsonl` directly, then import normally without `--resolve-collisions`.

### Advanced: Intelligent Merge Tools

For Git merge conflicts in `.beads/issues.jsonl`, consider using **[beads-merge](https://github.com/neongreen/mono/tree/main/beads-merge)** - a specialized merge tool by @neongreen that:

- Matches issues across conflicted JSONL files
- Merges fields intelligently (e.g., combines labels, picks newer timestamps)
- Resolves conflicts automatically where possible
- Leaves remaining conflicts for manual resolution
- Works as a Git/jujutsu merge driver

**Two types of conflicts, two tools:**
- **Git merge conflicts** (same issue modified in two branches) → Use beads-merge during git merge
- **ID collisions** (different issues with same ID) → Use `bd import --resolve-collisions` after merge

## Custom Git Hooks

For immediate export (no 5-second wait) and guaranteed import after git operations, install the git hooks:

### Using the Installer

```bash
cd examples/git-hooks
./install.sh
```

### Manual Setup

Create `.git/hooks/pre-commit`:
```bash
#!/bin/bash
bd export -o .beads/issues.jsonl
git add .beads/issues.jsonl
```

Create `.git/hooks/post-merge`:
```bash
#!/bin/bash
bd import -i .beads/issues.jsonl
```

Create `.git/hooks/post-checkout`:
```bash
#!/bin/bash
bd import -i .beads/issues.jsonl
```

Make hooks executable:
```bash
chmod +x .git/hooks/pre-commit .git/hooks/post-merge .git/hooks/post-checkout
```

**Note:** Auto-sync is already enabled by default, so git hooks are optional. They're useful if you need immediate export or guaranteed import after git operations.

## Extensible Database

bd uses SQLite, which you can extend with your own tables and queries. This allows you to:

- Add custom metadata to issues
- Build integrations with other tools
- Implement custom workflows
- Create reports and analytics

**See [EXTENDING.md](EXTENDING.md) for complete documentation:**
- Database schema and structure
- Adding custom tables
- Joining with issue data
- Example integrations
- Best practices

**Example use case:**
```sql
-- Add time tracking table
CREATE TABLE time_entries (
    id INTEGER PRIMARY KEY,
    issue_id TEXT NOT NULL,
    duration_minutes INTEGER NOT NULL,
    recorded_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY(issue_id) REFERENCES issues(id)
);

-- Query total time per issue
SELECT i.id, i.title, SUM(t.duration_minutes) as total_minutes
FROM issues i
LEFT JOIN time_entries t ON i.id = t.issue_id
GROUP BY i.id;
```

## Next Steps

- **[README.md](README.md)** - Core features and quick start
- **[TROUBLESHOOTING.md](TROUBLESHOOTING.md)** - Common issues and solutions
- **[FAQ.md](FAQ.md)** - Frequently asked questions
- **[CONFIG.md](CONFIG.md)** - Configuration system guide
- **[EXTENDING.md](EXTENDING.md)** - Database extension patterns
