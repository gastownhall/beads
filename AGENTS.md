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

**Working on multiple projects?** You can configure beads MCP to work across multiple repositories.

**Option 1: Global Daemon (Recommended - v0.9.11+)**
Start a single daemon to serve all your projects:
```bash
# Start global daemon
bd daemon --global

# MCP server automatically discovers and uses the global daemon
```

**Note:** As of v0.9.11, the daemon **auto-starts automatically** when you run any `bd` command. You typically don't need to manually start it. To disable auto-start, set `BEADS_AUTO_START_DAEMON=false`.

Your MCP config stays simple:
```json
{
  "beads": {
    "command": "beads-mcp",
    "args": []
  }
}
```

The MCP server will:
1. Check for local daemon socket (`.beads/bd.sock`)
2. Fall back to global daemon socket (`~/.beads/bd.sock`)
3. Automatically route requests to the correct database based on your current working directory
4. Auto-start the daemon if it's not running (with exponential backoff on failures)
5. Auto-detect multiple repositories and prefer global daemon when 4+ repos are found

**Option 2: Multiple MCP Server Instances**
Configure separate MCP servers for each major project:
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

Each MCP instance will use its specified working directory to find the correct `.beads/*.db` database.

**Which approach should you use?**
- ✅ **Global daemon**: Best for 3+ projects, better resource usage, automatic routing (auto-started when 4+ repos detected)
- ✅ **Multiple instances**: Best for 1-2 projects you switch between frequently
- ✅ **Hybrid**: Run global daemon + use MCP instances for convenience

**Migration helper:**
```bash
# Migrate from local to global daemon
bd daemon --migrate-to-global

# Or set environment variable for permanent preference
export BEADS_PREFER_GLOBAL_DAEMON=1
```

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

# Update issue status
bd update <id> --status in_progress --json

# Link discovered work (old way)
bd dep add <discovered-id> <parent-id> --type discovered-from

# Create and link in one command (new way)
bd create "Issue title" -t bug -p 1 --deps discovered-from:<parent-id> --json

# Label management
bd label add <id> <label> --json
bd label remove <id> <label> --json
bd label list <id> --json
bd label list-all --json

# Filter issues by label
bd list --label bug,critical --json

# Complete work
bd close <id> --reason "Done" --json

# Show dependency tree
bd dep tree <id>

# Get issue details
bd show <id> --json

# Rename issue prefix (e.g., from 'knowledge-work-' to 'kw-')
bd rename-prefix kw- --dry-run  # Preview changes
bd rename-prefix kw- --json     # Apply rename

# Restore compacted issue from git history
bd restore <id>  # View full history at time of compaction

# Import with collision detection
bd import -i .beads/issues.jsonl --dry-run             # Preview only
bd import -i .beads/issues.jsonl --resolve-collisions  # Auto-resolve

# Multi-repo management (requires global daemon)
bd repos list                    # List all cached repositories
bd repos ready                   # View ready work across all repos
bd repos ready --group           # Group by repository
bd repos stats                   # Combined statistics
bd repos clear-cache             # Clear repository cache
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

**Optional**: Use the git hooks in `examples/git-hooks/` for immediate export (no 5-second wait) and guaranteed import after git operations. Not required with auto-sync enabled.

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
