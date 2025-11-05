# Instructions for AI Agents Working on Beads

## Project Overview

This is **beads** (command: `bd`), an issue tracker designed for AI-supervised coding workflows. We dogfood our own tool!

## Human Setup vs Agent Usage

**IMPORTANT:** If you need to initialize bd, use the `--quiet` flag:

```bash
bd init --quiet  # Non-interactive, auto-installs git hooks, no prompts
```

**Why `--quiet`?** Regular `bd init` has interactive prompts (git hooks) that confuse agents. The `--quiet` flag makes it fully non-interactive:
- Automatically installs git hooks
- No prompts for user input
- Safe for agent-driven repo setup

**If the human already initialized:** Just use bd normally with `bd create`, `bd ready`, `bd update`, `bd close`, etc.

**If you see "database not found":** Run `bd init --quiet` yourself, or ask the human to run `bd init`.

## Issue Tracking

We use bd (beads) for issue tracking instead of Markdown TODOs or external tools.

### MCP Server (Recommended)

**RECOMMENDED**: Use the MCP (Model Context Protocol) server for the best experience! The beads MCP server provides native integration with Claude and other MCP-compatible AI assistants.

**Installation:**
```bash
# Install the MCP server
pip install beads-mcp

# Add to your MCP settings (e.g., Claude Desktop config)
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

**Benefits:**
- Native function calls instead of shell commands
- Automatic workspace detection
- Better error handling and validation
- Structured JSON responses
- No need for `--json` flags

**All bd commands are available as MCP functions** with the prefix `mcp__beads-*__`. For example:
- `bd ready` → `mcp__beads__ready()`
- `bd create` → `mcp__beads__create(title="...", priority=1)`
- `bd update` → `mcp__beads__update(issue_id="bd-42", status="in_progress")`

See `integrations/beads-mcp/README.md` for complete documentation.

### Multi-Repo Configuration (MCP Server)

**RECOMMENDED: Use a single MCP server for all beads projects** - it automatically routes to per-project local daemons.

**Setup (one-time):**
```bash
# MCP config in ~/.config/amp/settings.json or Claude Desktop config:
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

**How it works (LSP model):**
The single MCP server instance automatically:
1. Checks for local daemon socket (`.beads/bd.sock`) in your current workspace
2. Routes requests to the correct **per-project daemon** based on working directory
3. Auto-starts the local daemon if not running (with exponential backoff)
4. **Each project gets its own isolated daemon** serving only its database

**Architecture:**
```
MCP Server (one instance)
    ↓
Per-Project Daemons (one per workspace)
    ↓
SQLite Databases (complete isolation)
```

**Why per-project daemons?**
- ✅ Complete database isolation between projects
- ✅ No cross-project pollution or git worktree conflicts
- ✅ Simpler mental model: one project = one database = one daemon
- ✅ Follows LSP (Language Server Protocol) architecture

**Note:** The daemon **auto-starts automatically** when you run any `bd` command (v0.9.11+). To disable auto-start, set `BEADS_AUTO_START_DAEMON=false`.

**Version Management:** bd automatically handles daemon version mismatches (v0.16.0+):
- When you upgrade bd, old daemons are automatically detected and restarted
- Version compatibility is checked on every connection
- No manual intervention required after upgrades
- Works transparently with MCP server and CLI
- Use `bd daemons health` to check for version mismatches
- Use `bd daemons killall` to force-restart all daemons if needed

**Alternative (not recommended): Multiple MCP Server Instances**
If you must use separate MCP servers:
```json
{
  "beads-webapp": {
    "command": "beads-mcp",
    "env": {
      "BEADS_WORKING_DIR": "/Users/you/projects/webapp"
    }
  },
  "beads-api": {
    "command": "beads-mcp",
    "env": {
      "BEADS_WORKING_DIR": "/Users/you/projects/api"
    }
  }
}
```
⚠️ **Problem**: AI may select the wrong MCP server for your workspace, causing commands to operate on the wrong database.

### CLI Quick Reference

If you're not using the MCP server, here are the CLI commands:

```bash
# Check database path and daemon status
bd info --json

# Find ready work (no blockers)
bd ready --json

# Find stale issues (not updated recently)
bd stale --days 30 --json                    # Default: 30 days
bd stale --days 90 --status in_progress --json  # Filter by status
bd stale --limit 20 --json                   # Limit results

# Create new issue
# IMPORTANT: Always quote titles and descriptions with double quotes
bd create "Issue title" -t bug|feature|task -p 0-4 -d "Description" --json

# Create with explicit ID (for parallel workers)
bd create "Issue title" --id worker1-100 -p 1 --json

# Create with labels
bd create "Issue title" -t bug -p 1 -l bug,critical --json

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

# Update one or more issues
bd update <id> [<id>...] --status in_progress --json
bd update <id> [<id>...] --priority 1 --json

# Edit issue fields in $EDITOR (HUMANS ONLY - not for agents)
bd edit <id>                    # Edit description
bd edit <id> --title            # Edit title
bd edit <id> --design           # Edit design notes
bd edit <id> --notes            # Edit notes
bd edit <id> --acceptance       # Edit acceptance criteria

# Link discovered work (old way)
bd dep add <discovered-id> <parent-id> --type discovered-from

# Create and link in one command (new way)
bd create "Issue title" -t bug -p 1 --deps discovered-from:<parent-id> --json

# Label management (supports multiple IDs)
bd label add <id> [<id>...] <label> --json
bd label remove <id> [<id>...] <label> --json
bd label list <id> --json
bd label list-all --json

# Filter issues by label
bd list --label bug,critical --json

# Complete work (supports multiple IDs)
bd close <id> [<id>...] --reason "Done" --json

# Reopen closed issues (supports multiple IDs)
bd reopen <id> [<id>...] --reason "Reopening" --json

# Show dependency tree
bd dep tree <id>

# Get issue details (supports multiple IDs)
bd show <id> [<id>...] --json

# Rename issue prefix (e.g., from 'knowledge-work-' to 'kw-')
bd rename-prefix kw- --dry-run  # Preview changes
bd rename-prefix kw- --json     # Apply rename

# Restore compacted issue from git history
bd restore <id>  # View full history at time of compaction

# Import issues from JSONL
bd import -i .beads/issues.jsonl --dry-run      # Preview changes
bd import -i .beads/issues.jsonl                # Import and update issues
bd import -i .beads/issues.jsonl --dedupe-after # Import + detect duplicates

# Note: Import automatically handles missing parents!
# - If a hierarchical child's parent is missing (e.g., bd-abc.1 but no bd-abc)
# - bd will search the JSONL history for the parent
# - If found, creates a tombstone placeholder (Status=Closed, Priority=4)
# - Dependencies are also resurrected on best-effort basis
# - This prevents import failures after parent deletion

# Find and merge duplicate issues
bd duplicates                                          # Show all duplicates
bd duplicates --auto-merge                             # Automatically merge all
bd duplicates --dry-run                                # Preview merge operations

# Merge specific duplicate issues
bd merge <source-id...> --into <target-id> --json      # Consolidate duplicates
bd merge bd-42 bd-43 --into bd-41 --dry-run            # Preview merge

# Migrate databases after version upgrade
bd migrate                                             # Detect and migrate old databases
bd migrate --dry-run                                   # Preview migration
bd migrate --cleanup --yes                             # Migrate and remove old files

# AI-supervised migration (check before running bd migrate)
bd migrate --inspect --json                            # Show migration plan for AI agents
bd info --schema --json                                # Get schema, tables, config, sample IDs

# Workflow: AI agents should inspect first, then migrate
# 1. Run --inspect to see pending migrations and warnings
# 2. Check for missing_config (like issue_prefix)
# 3. Review invariants_to_check for safety guarantees
# 4. If warnings exist, fix config issues first
# 5. Then run bd migrate safely
```

**Migration safety:** The system verifies data integrity invariants after migrations:
- **required_config_present**: Ensures issue_prefix and schema_version are set
- **foreign_keys_valid**: No orphaned dependencies or labels
- **issue_count_stable**: Issue count doesn't decrease unexpectedly

These invariants prevent data loss and would have caught issues like GH #201 (missing issue_prefix after migration).

### Managing Daemons

bd runs a background daemon per workspace for auto-sync and RPC operations. Use `bd daemons` to manage multiple daemons:

```bash
# List all running daemons
bd daemons list --json

# Check health (version mismatches, stale sockets)
bd daemons health --json

# Stop a specific daemon
bd daemons stop /path/to/workspace --json
bd daemons stop 12345 --json  # By PID

# View daemon logs
bd daemons logs /path/to/workspace -n 100
bd daemons logs 12345 -f  # Follow mode

# Stop all daemons
bd daemons killall --json
bd daemons killall --force --json  # Force kill if graceful fails
```

**When to use:**
- **After upgrading bd**: Run `bd daemons health` to check for version mismatches, then `bd daemons killall` to restart all daemons with the new version
- **Debugging**: Use `bd daemons logs <workspace>` to view daemon logs
- **Cleanup**: `bd daemons list` auto-removes stale sockets

**Troubleshooting:**
- **Stale sockets**: `bd daemons list` auto-cleans them
- **Version mismatch**: `bd daemons killall` then let daemons auto-start on next command
- **Daemon won't stop**: `bd daemons killall --force`

See [commands/daemons.md](commands/daemons.md) for detailed documentation.

### Event-Driven Daemon Mode (Experimental)

**NEW in v0.16+**: The daemon supports an experimental event-driven mode that replaces 5-second polling with instant reactivity.

**Benefits:**
- ⚡ **<500ms latency** (vs ~5000ms with polling)
- 🔋 **~60% less CPU usage** (no continuous polling)
- 🎯 **Instant sync** on mutations and file changes
- 🛡️ **Dropped events safety net** prevents data loss

**How it works:**
- **FileWatcher** monitors `.beads/issues.jsonl` and `.git/refs/heads` using platform-native APIs:
  - Linux: `inotify`
  - macOS: `FSEvents` (via kqueue)
  - Windows: `ReadDirectoryChangesW`
- **Mutation events** from RPC operations (create, update, close) trigger immediate export
- **Debouncer** batches rapid changes (500ms window) to avoid export storms
- **Polling fallback** if fsnotify unavailable (e.g., network filesystems)

**Opt-In (Phase 1):**

Event-driven mode is opt-in during Phase 1. To enable:

```bash
# Enable event-driven mode for a single daemon
BEADS_DAEMON_MODE=events bd daemon start

# Or set globally in your shell profile
export BEADS_DAEMON_MODE=events

# Restart all daemons to apply
bd daemons killall
# Next bd command will auto-start daemon with new mode
```

**Available modes:**
- `poll` (default) - Traditional 5-second polling, stable and battle-tested
- `events` - New event-driven mode, experimental but thoroughly tested

**Troubleshooting:**

If the watcher fails to start:
- Check daemon logs: `bd daemons logs /path/to/workspace -n 100`
- Look for "File watcher unavailable" warnings
- Common causes:
  - Network filesystem (NFS, SMB) - fsnotify may not work
  - Container environment - may need privileged mode
  - Resource limits - check `ulimit -n` (open file descriptors)

**Fallback behavior:**
- If `BEADS_DAEMON_MODE=events` but watcher fails, daemon falls back to polling automatically
- Set `BEADS_WATCHER_FALLBACK=false` to disable fallback and require fsnotify

**Disable polling fallback:**
```bash
# Require fsnotify, fail if unavailable
BEADS_WATCHER_FALLBACK=false BEADS_DAEMON_MODE=events bd daemon start
```

**Switch back to polling:**
```bash
# Explicitly use polling mode
BEADS_DAEMON_MODE=poll bd daemon start

# Or unset to use default
unset BEADS_DAEMON_MODE
bd daemons killall  # Restart with default (poll) mode
```

**Future (Phase 2):** Event-driven mode will become the default once it's proven stable in production use.

### Workflow

1. **Check for ready work**: Run `bd ready` to see what's unblocked (or `bd stale` to find forgotten issues)
2. **Claim your task**: `bd update <id> --status in_progress`
3. **Work on it**: Implement, test, document
4. **Discover new work**: If you find bugs or TODOs, create issues:
   - Old way (two commands): `bd create "Found bug in auth" -t bug -p 1 --json` then `bd dep add <new-id> <current-id> --type discovered-from`
   - New way (one command): `bd create "Found bug in auth" -t bug -p 1 --deps discovered-from:<current-id> --json`
5. **Complete**: `bd close <id> --reason "Implemented"`
6. **Sync at end of session**: `bd sync` (see "Agent Session Workflow" below)

### Issue Types

- `bug` - Something broken that needs fixing
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature composed of multiple issues (supports hierarchical children)
- `chore` - Maintenance work (dependencies, tooling)

**Hierarchical children:** Epics can have child issues with dotted IDs (e.g., `bd-a3f8e9.1`, `bd-a3f8e9.2`). Children are auto-numbered sequentially. Up to 3 levels of nesting supported. The parent hash ensures unique namespace - no coordination needed between agents working on different epics.

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (nice-to-have features, minor bugs)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Dependency Types

- `blocks` - Hard dependency (issue X blocks issue Y)
- `related` - Soft relationship (issues are connected)
- `parent-child` - Epic/subtask relationship
- `discovered-from` - Track issues discovered during work (automatically inherits parent's `source_repo`)

Only `blocks` dependencies affect the ready work queue.

**Note:** When creating an issue with a `discovered-from` dependency, the new issue automatically inherits the parent's `source_repo` field. This ensures discovered work stays in the same repository as the parent task.

### Duplicate Detection & Merging

AI agents should proactively detect and merge duplicate issues to keep the database clean:

**Automated duplicate detection:**

```bash
# Find all content duplicates in the database
bd duplicates

# Automatically merge all duplicates
bd duplicates --auto-merge

# Preview what would be merged
bd duplicates --dry-run

# During import
bd import -i issues.jsonl --dedupe-after
```

**Detection strategies:**

1. **Before creating new issues**: Search for similar existing issues
   ```bash
   bd list --json | grep -i "authentication"
   bd show bd-41 bd-42 --json  # Compare candidates
   ```

2. **Periodic duplicate scans**: Review issues by type or priority
   ```bash
   bd list --status open --priority 1 --json  # High-priority issues
   bd list --issue-type bug --json             # All bugs
   ```

3. **During work discovery**: Check for duplicates when filing discovered-from issues
   ```bash
   # Before: bd create "Fix auth bug" --deps discovered-from:bd-100
   # First: bd list --json | grep -i "auth bug"
   # Then decide: create new or link to existing
   ```

**Merge workflow:**

```bash
# Step 1: Identify duplicates (bd-42 and bd-43 duplicate bd-41)
bd show bd-41 bd-42 bd-43 --json

# Step 2: Preview merge to verify
bd merge bd-42 bd-43 --into bd-41 --dry-run

# Step 3: Execute merge
bd merge bd-42 bd-43 --into bd-41 --json

# Step 4: Verify result
bd dep tree bd-41  # Check unified dependency tree
bd show bd-41 --json  # Verify merged content
```

**What gets merged:**
- ✅ All dependencies from source → target
- ✅ Text references updated across ALL issues (descriptions, notes, design, acceptance criteria)
- ✅ Source issues closed with "Merged into bd-X" reason
- ❌ Source issue content NOT copied (target keeps its original content)

**Important notes:**
- Merge preserves target issue completely; only dependencies/references migrate
- If source issues have valuable content, manually copy it to target BEFORE merging
- Cannot merge in daemon mode yet (bd-190); use `--no-daemon` flag
- Operation cannot be undone (but git history preserves the original)

**Best practices:**
- Merge early to prevent dependency fragmentation
- Choose the oldest or most complete issue as merge target
- Add labels like `duplicate` to source issues before merging (for tracking)
- File a discovered-from issue if you found duplicates during work:
  ```bash
  bd create "Found duplicates during bd-X" -p 2 --deps discovered-from:bd-X --json
  ```

## Development Guidelines

### Code Standards

- **Go version**: 1.21+
- **Linting**: `golangci-lint run ./...` (baseline warnings documented in [docs/LINTING.md](docs/LINTING.md))
- **Testing**: All new features need tests (`go test ./...`)
- **Documentation**: Update relevant .md files

### File Organization

```
beads/
├── cmd/bd/              # CLI commands
├── internal/
│   ├── types/           # Core data types
│   └── storage/         # Storage layer
│       └── sqlite/      # SQLite implementation
├── examples/            # Integration examples
└── *.md                 # Documentation
```

### Before Committing

1. **Run tests**: `go test ./...`
2. **Run linter**: `golangci-lint run ./...` (ignore baseline warnings)
3. **Update docs**: If you changed behavior, update README.md or other docs
4. **Commit**: Issues auto-sync to `.beads/issues.jsonl` and import after pull

### Git Workflow

**Auto-sync provides batching!** bd automatically:
- **Exports** to JSONL after CRUD operations (30-second debounce for batching)
- **Imports** from JSONL when it's newer than DB (e.g., after `git pull`)
- **Daemon commits/pushes** every 5 seconds (if `--auto-commit` / `--auto-push` enabled)

The 30-second debounce provides a **transaction window** for batch operations - multiple issue changes within 30 seconds get flushed together, avoiding commit spam.

### Protected Branch Workflow

**If your repository uses protected branches (GitHub, GitLab, etc.)**, beads can commit to a separate branch instead of `main`:

```bash
# Initialize with separate sync branch
bd init --branch beads-metadata

# Or configure existing setup
bd config set sync.branch beads-metadata
```

**How it works:**
- Beads commits issue updates to `beads-metadata` instead of `main`
- Uses git worktrees (lightweight checkouts) in `.git/beads-worktrees/`
- Your main working directory is never affected
- Periodically merge `beads-metadata` back to `main` via pull request

**Daily workflow (unchanged for agents):**

```bash
# Agents work normally - no changes needed!
bd create "Fix authentication" -t bug -p 1
bd update bd-a1b2 --status in_progress
bd close bd-a1b2 "Fixed"
```

All changes automatically commit to `beads-metadata` branch (if daemon is running with `--auto-commit`).

**Merging to main (humans):**

```bash
# Check what's changed
bd sync --status

# Option 1: Create pull request
git push origin beads-metadata
# Then create PR on GitHub/GitLab

# Option 2: Direct merge (if allowed)
bd sync --merge
```

**Benefits:**
- ✅ Works with protected `main` branches
- ✅ No disruption to agent workflows
- ✅ Platform-agnostic (works on any git platform)
- ✅ Backward compatible (opt-in via config)

**See [docs/PROTECTED_BRANCHES.md](docs/PROTECTED_BRANCHES.md) for complete setup guide, troubleshooting, and examples.**

### Agent Session Workflow

**IMPORTANT for AI agents:** When you finish making issue changes, always run:

```bash
bd sync
```

This immediately:
1. Exports pending changes to JSONL (no 30s wait)
2. Commits to git
3. Pulls from remote
4. Imports any updates
5. Pushes to remote

**Example agent session:**
```bash
# Make multiple changes (batched in 30-second window)
bd create "Fix bug" -p 1
bd create "Add tests" -p 1
bd update bd-42 --status in_progress
bd close bd-40 --reason "Completed"

# Force immediate sync at end of session
bd sync

# Now safe to end session - everything is committed and pushed
```

**Why this matters:**
- Without `bd sync`, changes sit in 30-second debounce window
- User might think you pushed but JSONL is still dirty
- `bd sync` forces immediate flush/commit/push

**STRONGLY RECOMMENDED: Install git hooks for automatic sync** (prevents stale JSONL problems):

```bash
# One-time setup - run this in each beads workspace
./examples/git-hooks/install.sh
```

This installs:
- **pre-commit** - Flushes pending changes immediately before commit (bypasses 30s debounce)
- **post-merge** - Imports updated JSONL after pull/merge (guaranteed sync)
- **pre-push** - Exports database to JSONL before push (prevents stale JSONL from reaching remote)

**Why git hooks matter:**
Without the pre-push hook, you can have database changes committed locally but stale JSONL pushed to remote, causing multi-workspace divergence. The hooks guarantee DB ↔ JSONL consistency.

See [examples/git-hooks/README.md](examples/git-hooks/README.md) for details.

### Git Worktrees

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

### Handling Git Merge Conflicts

**With hash-based IDs (v0.20.1+), ID collisions are eliminated!** Different issues get different hash IDs, so most git merges succeed cleanly.

**When git merge conflicts occur:**
Git conflicts in `.beads/beads.jsonl` happen when the same issue is modified on both branches (different timestamps/fields). This is a **same-issue update conflict**, not an ID collision. Conflicts are rare in practice since hash IDs prevent structural collisions.

**Automatic detection:**
bd automatically detects conflict markers (`<<<<<<<`, `=======`, `>>>>>>>`) and shows clear resolution steps:
- `bd import` rejects files with conflict markers and shows resolution commands
- `bd validate --checks=conflicts` scans for conflicts in JSONL

**Resolution workflow:**
```bash
# After git merge creates conflict in .beads/beads.jsonl

# Option 1: Accept their version (remote)
git checkout --theirs .beads/beads.jsonl
bd import -i .beads/beads.jsonl

# Option 2: Keep our version (local)
git checkout --ours .beads/beads.jsonl  
bd import -i .beads/beads.jsonl

# Option 3: Manual resolution in editor
# Edit .beads/beads.jsonl to remove conflict markers
bd import -i .beads/beads.jsonl

# Commit the merge
git add .beads/beads.jsonl
git commit
```

**Note:** `bd import` automatically handles updates - same ID with different content is a normal update operation. No special flags needed. If you accidentally modified the same issue in both branches, just pick whichever version is more complete.

### Advanced: Intelligent Merge Tools

For Git merge conflicts in `.beads/issues.jsonl`, consider using **[beads-merge](https://github.com/neongreen/mono/tree/main/beads-merge)** - a specialized merge tool by @neongreen that:

- Matches issues across conflicted JSONL files
- Merges fields intelligently (e.g., combines labels, picks newer timestamps)
- Resolves conflicts automatically where possible
- Leaves remaining conflicts for manual resolution
- Works as a Git/jujutsu merge driver

**Beads-merge** helps with intelligent field-level merging during git merge. After resolving, just `bd import` to update your database.

## Current Project Status

Run `bd stats` to see overall progress.

### Active Areas

- **Core CLI**: Mature, but always room for polish
- **Examples**: Growing collection of agent integrations
- **Documentation**: Comprehensive but can always improve
- **MCP Server**: Implemented at `integrations/beads-mcp/` with Claude Code plugin
- **Migration Tools**: Planned (see bd-6)

### 1.0 Milestone

We're working toward 1.0. Key blockers tracked in bd. Run:
```bash
bd dep tree bd-8  # Show 1.0 epic dependencies
```

## Exclusive Lock Protocol (Advanced)

**For external tools that need full database control** (e.g., CI/CD, deterministic execution systems):

The bd daemon respects exclusive locks via `.beads/.exclusive-lock` file. When this lock exists:
- Daemon skips all operations for the locked database
- External tool has complete control over git sync and database operations
- Stale locks (dead process) are automatically cleaned up

**Use case:** Tools like VibeCoder that need deterministic execution without daemon interference.

See [EXCLUSIVE_LOCK.md](EXCLUSIVE_LOCK.md) for:
- Lock file format (JSON schema)
- Creating and releasing locks (Go/shell examples)
- Stale lock detection behavior
- Integration testing guidance

**Quick example:**
```bash
# Create lock
echo '{"holder":"my-tool","pid":'$$',"hostname":"'$(hostname)'","started_at":"'$(date -u +%Y-%m-%dT%H:%M:%SZ)'","version":"1.0.0"}' > .beads/.exclusive-lock

# Do work...
bd create "My issue" -p 1

# Release lock
rm .beads/.exclusive-lock
```

## Common Tasks

### Adding a New Command

1. Create file in `cmd/bd/`
2. Add to root command in `cmd/bd/main.go`
3. Implement with Cobra framework
4. Add `--json` flag for agent use
5. Add tests in `cmd/bd/*_test.go`
6. Document in README.md

### Adding Storage Features

1. Update schema in `internal/storage/sqlite/schema.go`
2. Add migration if needed
3. Update `internal/types/types.go` if new types
4. Implement in `internal/storage/sqlite/sqlite.go`
5. Add tests
6. Update export/import in `cmd/bd/export.go` and `cmd/bd/import.go`

### Adding Examples

1. Create directory in `examples/`
2. Add README.md explaining the example
3. Include working code
4. Link from `examples/README.md`
5. Mention in main README.md

## Questions?

- Check existing issues: `bd list`
- Look at recent commits: `git log --oneline -20`
- Read the docs: README.md, ADVANCED.md, EXTENDING.md
- Create an issue if unsure: `bd create "Question: ..." -t task -p 2`

## Important Files

- **README.md** - Main documentation (keep this updated!)
- **EXTENDING.md** - Database extension guide
- **ADVANCED.md** - JSONL format analysis
- **CONTRIBUTING.md** - Contribution guidelines
- **SECURITY.md** - Security policy

## Pro Tips for Agents

- Always use `--json` flags for programmatic use
- **Always run `bd sync` at end of session** to flush/commit/push immediately
- Link discoveries with `discovered-from` to maintain context
- Check `bd ready` before asking "what next?"
- Auto-sync batches changes in 30-second window - use `bd sync` to force immediate flush
- Use `--no-auto-flush` or `--no-auto-import` to disable automatic sync if needed
- Use `bd dep tree` to understand complex dependencies
- Priority 0-1 issues are usually more important than 2-4
- Use `--dry-run` to preview import changes before applying
- Hash IDs eliminate collisions - same ID with different content is a normal update
- Use `--id` flag with `bd create` to partition ID space for parallel workers (e.g., `worker1-100`, `worker2-500`)

### Checking GitHub Issues and PRs

**IMPORTANT**: When asked to check GitHub issues or PRs, use command-line tools like `gh` instead of browser/playwright tools.

**Preferred approach:**
```bash
# List open issues with details
gh issue list --limit 30

# List open PRs
gh pr list --limit 30

# View specific issue
gh issue view 201
```

**Then provide an in-conversation summary** highlighting:
- Urgent/critical issues (regressions, bugs, broken builds)
- Common themes or patterns
- Feature requests with high engagement
- Items that need immediate attention

**Why this matters:**
- Browser tools consume more tokens and are slower
- CLI summaries are easier to scan and discuss
- Keeps the conversation focused and efficient
- Better for quick triage and prioritization

**Do NOT use:** `browser_navigate`, `browser_snapshot`, or other playwright tools for GitHub PR/issue reviews unless specifically requested by the user.

## Building and Testing

```bash
# Build
go build -o bd ./cmd/bd

# Test
go test ./...

# Test with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run locally
./bd init --prefix test
./bd create "Test issue" -p 1
./bd ready
```

## Version Management

**IMPORTANT**: When the user asks to "bump the version" or mentions a new version number (e.g., "bump to 0.9.3"), use the version bump script:

```bash
# Preview changes (shows diff, doesn't commit)
./scripts/bump-version.sh 0.9.3

# Auto-commit the version bump
./scripts/bump-version.sh 0.9.3 --commit
git push origin main
```

**What it does:**
- Updates ALL version files (CLI, plugin, MCP server, docs) in one command
- Validates semantic versioning format
- Shows diff preview
- Verifies all versions match after update
- Creates standardized commit message

**User will typically say:**
- "Bump to 0.9.3"
- "Update version to 1.0.0"
- "Rev the project to 0.9.4"
- "Increment the version"

**You should:**
1. Run `./scripts/bump-version.sh <version> --commit`
2. Push to GitHub
3. Confirm all versions updated correctly

**Files updated automatically:**
- `cmd/bd/version.go` - CLI version
- `.claude-plugin/plugin.json` - Plugin version
- `.claude-plugin/marketplace.json` - Marketplace version
- `integrations/beads-mcp/pyproject.toml` - MCP server version
- `README.md` - Documentation version
- `PLUGIN.md` - Version requirements

**Why this matters:** We had version mismatches (bd-66) when only `version.go` was updated. This script prevents that by updating all components atomically.

See `scripts/README.md` for more details.

## Release Process (Maintainers)

1. Bump version with `./scripts/bump-version.sh <version> --commit`
2. Update CHANGELOG.md with release notes
3. Run full test suite: `go test ./...`
4. Push version bump: `git push origin main`
5. Tag release: `git tag v<version>`
6. Push tag: `git push origin v<version>`
7. GitHub Actions handles the rest

---

**Remember**: We're building this tool to help AI agents like you! If you find the workflow confusing or have ideas for improvement, create an issue with your feedback.

Happy coding! 🔗

<!-- bd onboard section -->
## Issue Tracking with bd (beads)

**IMPORTANT**: This project uses **bd (beads)** for ALL issue tracking. Do NOT use markdown TODOs, task lists, or other tracking methods.

### Why bd?

- Dependency-aware: Track blockers and relationships between issues
- Git-friendly: Auto-syncs to JSONL for version control
- Agent-optimized: JSON output, ready work detection, discovered-from links
- Prevents duplicate tracking systems and confusion

### Quick Start

**FIRST TIME?** Just run `bd init` - it auto-imports issues from git:
```bash
bd init --prefix bd
```

**Check for ready work:**
```bash
bd ready --json
```

**Create new issues:**
```bash
bd create "Issue title" -t bug|feature|task -p 0-4 --json
bd create "Issue title" -p 1 --deps discovered-from:bd-123 --json
```

**Claim and update:**
```bash
bd update bd-42 --status in_progress --json
bd update bd-42 --priority 1 --json
```

**Complete work:**
```bash
bd close bd-42 --reason "Completed" --json
```

### Issue Types

- `bug` - Something broken
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature with subtasks
- `chore` - Maintenance (dependencies, tooling)

### Priorities

- `0` - Critical (security, data loss, broken builds)
- `1` - High (major features, important bugs)
- `2` - Medium (default, nice-to-have)
- `3` - Low (polish, optimization)
- `4` - Backlog (future ideas)

### Workflow for AI Agents

1. **Check ready work**: `bd ready` shows unblocked issues
2. **Claim your task**: `bd update <id> --status in_progress`
3. **Work on it**: Implement, test, document
4. **Discover new work?** Create linked issue:
   - `bd create "Found bug" -p 1 --deps discovered-from:<parent-id>`
5. **Complete**: `bd close <id> --reason "Done"`

### Auto-Sync

bd automatically syncs with git:
- Exports to `.beads/issues.jsonl` after changes (5s debounce)
- Imports from JSONL when newer (e.g., after `git pull`)
- No manual export/import needed!

### MCP Server (Recommended)

If using Claude or MCP-compatible clients, install the beads MCP server:

```bash
pip install beads-mcp
```

Add to MCP config (e.g., `~/.config/claude/config.json`):
```json
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

Then use `mcp__beads__*` functions instead of CLI commands.

### Managing AI-Generated Planning Documents

AI assistants often create planning and design documents during development:
- PLAN.md, IMPLEMENTATION.md, ARCHITECTURE.md
- DESIGN.md, CODEBASE_SUMMARY.md, INTEGRATION_PLAN.md
- TESTING_GUIDE.md, TECHNICAL_DESIGN.md, and similar files

**Best Practice: Use a dedicated directory for these ephemeral files**

**Recommended approach:**
- Create a `history/` directory in the project root
- Store ALL AI-generated planning/design docs in `history/`
- Keep the repository root clean and focused on permanent project files
- Only access `history/` when explicitly asked to review past planning

**Example .gitignore entry (optional):**
```
# AI planning documents (ephemeral)
history/
```

**Benefits:**
- ✅ Clean repository root
- ✅ Clear separation between ephemeral and permanent documentation
- ✅ Easy to exclude from version control if desired
- ✅ Preserves planning history for archaeological research
- ✅ Reduces noise when browsing the project

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ✅ Store AI planning docs in `history/` directory
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems
- ❌ Do NOT clutter repo root with planning documents

For more details, see README.md and QUICKSTART.md.
<!-- /bd onboard section -->
