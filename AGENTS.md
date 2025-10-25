# Instructions for AI Agents Working on Beads

## Project Overview

This is **beads** (command: `bd`), an issue tracker designed for AI-supervised coding workflows. We dogfood our own tool!

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
- No manual `bd daemon --stop` required after upgrades
- Works transparently with MCP server and CLI

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
# Find ready work (no blockers)
bd ready --json

# Create new issue
bd create "Issue title" -t bug|feature|task -p 0-4 -d "Description" --json

# Create with explicit ID (for parallel workers)
bd create "Issue title" --id worker1-100 -p 1 --json

# Create with labels
bd create "Issue title" -t bug -p 1 -l bug,critical --json

# Create multiple issues from markdown file
bd create -f feature-plan.md --json

# Update one or more issues
bd update <id> [<id>...] --status in_progress --json
bd update <id> [<id>...] --priority 1 --json

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

# Import with collision detection
bd import -i .beads/issues.jsonl --dry-run             # Preview only
bd import -i .beads/issues.jsonl --resolve-collisions  # Auto-resolve
bd import -i .beads/issues.jsonl --resolve-collisions --dedupe-after  # Auto-resolve + detect duplicates

# Find and merge duplicate issues
bd duplicates                                          # Show all duplicates
bd duplicates --auto-merge                             # Automatically merge all
bd duplicates --dry-run                                # Preview merge operations

# Merge specific duplicate issues
bd merge <source-id...> --into <target-id> --json      # Consolidate duplicates
bd merge bd-42 bd-43 --into bd-41 --dry-run            # Preview merge
```

### Workflow

1. **Check for ready work**: Run `bd ready` to see what's unblocked
2. **Claim your task**: `bd update <id> --status in_progress`
3. **Work on it**: Implement, test, document
4. **Discover new work**: If you find bugs or TODOs, create issues:
   - Old way (two commands): `bd create "Found bug in auth" -t bug -p 1 --json` then `bd dep add <new-id> <current-id> --type discovered-from`
   - New way (one command): `bd create "Found bug in auth" -t bug -p 1 --deps discovered-from:<current-id> --json`
5. **Complete**: `bd close <id> --reason "Implemented"`
6. **Export**: Changes auto-sync to `.beads/issues.jsonl` (5-second debounce)

### Issue Types

- `bug` - Something broken that needs fixing
- `feature` - New functionality
- `task` - Work item (tests, docs, refactoring)
- `epic` - Large feature composed of multiple issues
- `chore` - Maintenance work (dependencies, tooling)

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
- `discovered-from` - Track issues discovered during work

Only `blocks` dependencies affect the ready work queue.

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

# During import (after collision resolution)
bd import -i issues.jsonl --resolve-collisions --dedupe-after
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
- **Linting**: `golangci-lint run ./...` (baseline warnings documented in LINTING.md)
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

**Auto-sync is now automatic!** bd automatically:
- **Exports** to JSONL after any CRUD operation (5-second debounce)
- **Imports** from JSONL when it's newer than DB (e.g., after `git pull`)

```bash
# Make changes and create/update issues
bd create "Fix bug" -p 1
bd update bd-42 --status in_progress

# JSONL is automatically updated after 5 seconds

# Commit (JSONL is already up-to-date)
git add .
git commit -m "Your message"

# After pull - JSONL is automatically imported
git pull  # bd commands will auto-import the updated JSONL
bd ready  # Fresh data from git!
```

**Recommended**: Install git hooks to eliminate the race condition between auto-flush and commits:

```bash
# One-time setup
./examples/git-hooks/install.sh
```

This installs:
- **pre-commit** - Flushes pending changes immediately before commit (eliminates 5-second wait)
- **post-merge** - Imports updated JSONL after pull/merge (guaranteed sync)

Benefits:
- ✅ No more dirty working tree after commits
- ✅ Immediate flush (no 5-second debounce wait)
- ✅ Automatic collision resolution on merge
- ✅ Works alongside auto-sync

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

### Handling Import Collisions

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
- Read the docs: README.md, TEXT_FORMATS.md, EXTENDING.md
- Create an issue if unsure: `bd create "Question: ..." -t task -p 2`

## Important Files

- **README.md** - Main documentation (keep this updated!)
- **EXTENDING.md** - Database extension guide
- **TEXT_FORMATS.md** - JSONL format analysis
- **CONTRIBUTING.md** - Contribution guidelines
- **SECURITY.md** - Security policy

## Pro Tips for Agents

- Always use `--json` flags for programmatic use
- Link discoveries with `discovered-from` to maintain context
- Check `bd ready` before asking "what next?"
- Auto-sync is automatic! JSONL updates after CRUD ops, imports after git pull
- Use `--no-auto-flush` or `--no-auto-import` to disable automatic sync if needed
- Use `bd dep tree` to understand complex dependencies
- Priority 0-1 issues are usually more important than 2-4
- Use `--dry-run` to preview import collisions before resolving
- Use `--resolve-collisions` for safe automatic branch merges
- Use `--id` flag with `bd create` to partition ID space for parallel workers (e.g., `worker1-100`, `worker2-500`)

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

### Important Rules

- ✅ Use bd for ALL task tracking
- ✅ Always use `--json` flag for programmatic use
- ✅ Link discovered work with `discovered-from` dependencies
- ✅ Check `bd ready` before asking "what should I work on?"
- ❌ Do NOT create markdown TODO lists
- ❌ Do NOT use external issue trackers
- ❌ Do NOT duplicate tracking systems

For more details, see README.md and QUICKSTART.md.
<!-- /bd onboard section -->
