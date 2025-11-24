# Instructions for AI Agents Working on Beads

> **📖 For detailed development instructions**, see [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md)
>
> This file provides a quick overview and reference. For in-depth operational details (development, testing, releases, git workflow), consult the detailed instructions.

## Project Overview

This is **beads** (command: `bd`), an issue tracker designed for AI-supervised coding workflows. We dogfood our own tool!

> **🤖 Using GitHub Copilot?** See [.github/copilot-instructions.md](.github/copilot-instructions.md) for a concise, Copilot-optimized version of these instructions that GitHub Copilot will automatically load.

## 🆕 What's New?

**New to bd or upgrading?** Run `bd info --whats-new` to see agent-relevant changes from recent versions:

```bash
bd info --whats-new          # Human-readable output
bd info --whats-new --json   # Machine-readable output
```

This shows the last 3 versions with workflow-impacting changes, avoiding the need to re-read all documentation. Examples:
- New commands and flags that improve agent workflows
- Breaking changes that require workflow updates
- Performance improvements and bug fixes
- Integration features (MCP, Agent Mail, git hooks)

**Why this matters:** bd releases weekly with major versions. This command helps you quickly understand what changed without parsing the full CHANGELOG.

### 🔄 After Upgrading bd

When bd is upgraded to a new version, follow this workflow:

```bash
# 1. Check what changed
bd info --whats-new

# 2. Update git hooks to match new bd version
bd hooks install

# 3. Check for any outdated hooks (optional)
bd info  # Shows warnings if hooks are outdated
```

**Why update hooks?** Git hooks (pre-commit, post-merge, pre-push) are versioned with bd. Outdated hooks may miss new auto-sync features or bug fixes. Running `bd hooks install` ensures hooks match your bd version.

**Related:** See GitHub Discussion #239 for background on agent upgrade workflows.

## Human Setup vs Agent Usage

**IMPORTANT:** If you need to initialize bd, use the `--quiet` flag:

```bash
bd init --quiet  # Non-interactive, auto-installs git hooks, no prompts
```

**Why `--quiet`?** Regular `bd init` has interactive prompts (git hooks, merge driver) that confuse agents. The `--quiet` flag makes it fully non-interactive:

- Automatically installs git hooks
- Automatically configures git merge driver for intelligent JSONL merging
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

**For AI agent multi-repo patterns**, see [docs/MULTI_REPO_AGENTS.md](docs/MULTI_REPO_AGENTS.md) (config options, routing, troubleshooting, best practices).

**For complete multi-repo workflow guide**, see [docs/MULTI_REPO_MIGRATION.md](docs/MULTI_REPO_MIGRATION.md) (OSS contributors, teams, multi-phase development).

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

**Essential commands for AI agents:**

```bash
# Find work
bd ready --json                                    # Unblocked issues
bd stale --days 30 --json                          # Forgotten issues

# Create and manage issues
bd create "Issue title" --description="Detailed context about the issue" -t bug|feature|task -p 0-4 --json
bd create "Found bug" --description="What the bug is and how it was discovered" -p 1 --deps discovered-from:<parent-id> --json
bd update <id> --status in_progress --json
bd close <id> --reason "Done" --json

# Search and filter
bd list --status open --priority 1 --json
bd list --label-any urgent,critical --json
bd show <id> --json

# Sync (CRITICAL at end of session!)
bd sync  # Force immediate export/commit/push
```

**For comprehensive CLI documentation**, see [docs/CLI_REFERENCE.md](docs/CLI_REFERENCE.md).

### Managing Daemons

bd runs a background daemon per workspace for auto-sync and RPC operations:

```bash
bd daemons list --json          # List all running daemons
bd daemons health --json        # Check for version mismatches
bd daemons logs . -n 100        # View daemon logs
bd daemons killall --json       # Restart all daemons
```

**After upgrading bd**: Run `bd daemons killall` to restart all daemons with new version.

**For complete daemon management**, see [docs/DAEMON.md](docs/DAEMON.md).

### Web Interface (Monitor)

bd includes a built-in web interface for human visualization:

```bash
bd monitor                  # Start on localhost:8080
bd monitor --port 3000      # Custom port
```

**AI agents**: Continue using CLI with `--json` flags. The monitor is for human supervision only.

### Workflow

1. **Check for ready work**: Run `bd ready` to see what's unblocked (or `bd stale` to find forgotten issues)
2. **Claim your task**: `bd update <id> --status in_progress`
3. **Work on it**: Implement, test, document
4. **Discover new work**: If you find bugs or TODOs, create issues:
   - Old way (two commands): `bd create "Found bug in auth" --description="Details about the bug" -t bug -p 1 --json` then `bd dep add <new-id> <current-id> --type discovered-from`
   - New way (one command): `bd create "Found bug in auth" --description="Login fails with 500 when password has special chars" -t bug -p 1 --deps discovered-from:<current-id> --json`
5. **Complete**: `bd close <id> --reason "Implemented"`
6. **Sync at end of session**: `bd sync` (see "Agent Session Workflow" below)

### IMPORTANT: Always Include Issue Descriptions

**Issues without descriptions lack context for future work.** When creating issues, always include a meaningful description with:

- **Why** the issue exists (problem statement or need)
- **What** needs to be done (scope and approach)
- **How** you discovered it (if applicable during work)

**Good examples:**

```bash
# Bug discovered during work
bd create "Fix auth bug in login handler" \
  --description="Login fails with 500 error when password contains special characters like quotes. Found while testing GH#123 feature. Stack trace shows unescaped SQL in auth/login.go:45." \
  -t bug -p 1 --deps discovered-from:bd-abc --json

# Feature request
bd create "Add password reset flow" \
  --description="Users need ability to reset forgotten passwords via email. Should follow OAuth best practices and include rate limiting to prevent abuse." \
  -t feature -p 2 --json

# Technical debt
bd create "Refactor auth package for testability" \
  --description="Current auth code has tight DB coupling making unit tests difficult. Need to extract interfaces and add dependency injection. Blocks writing tests for bd-xyz." \
  -t task -p 3 --json
```

**Bad examples (missing context):**

```bash
bd create "Fix auth bug" -t bug -p 1 --json  # What bug? Where? Why?
bd create "Add feature" -t feature --json     # What feature? Why needed?
bd create "Refactor code" -t task --json      # What code? Why refactor?
```

### Optional: Agent Mail for Multi-Agent Coordination

**⚠️ NOT CURRENTLY CONFIGURED** - The mcp-agent-mail server is not set up for this project. Do not attempt to use mcp-agent-mail tools.

**For multi-agent workflows only** - if multiple AI agents work on the same repository simultaneously, consider using Agent Mail for real-time coordination:

**With Agent Mail enabled:**
```bash
# Configure environment (one-time per session)
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=assistant-alpha
export BEADS_PROJECT_ID=my-project

# Workflow (identical commands)
bd ready                                    # Shows available work
bd update bd-42 --status in_progress       # Reserves issue instantly (<100ms)
# ... work on issue ...
bd close bd-42 "Done"                       # Releases reservation automatically
```

**Without Agent Mail (git-only mode):**
```bash
# No environment variables needed
bd ready                                    # Shows available work
bd update bd-42 --status in_progress       # Updates via git sync (2-5s latency)
# ... work on issue ...
bd close bd-42 "Done"                       # Updates via git sync
```

**Key differences:**
- **Latency**: <100ms (Agent Mail) vs 2-5s (git-only)
- **Collision prevention**: Instant reservation (Agent Mail) vs eventual consistency (git)
- **Setup**: Requires server + env vars (Agent Mail) vs zero config (git-only)

**When to use Agent Mail:**
- ✅ Multiple agents working concurrently
- ✅ Frequent status updates (high collision risk)
- ✅ Real-time coordination needed

**When to skip:**
- ✅ Single agent workflows
- ✅ Infrequent updates (low collision risk)
- ✅ Simplicity preferred over latency

See [docs/AGENT_MAIL_QUICKSTART.md](docs/AGENT_MAIL_QUICKSTART.md) for 5-minute setup, or [docs/AGENT_MAIL.md](docs/AGENT_MAIL.md) for complete documentation. Example code in [examples/python-agent/AGENT_MAIL_EXAMPLE.md](examples/python-agent/AGENT_MAIL_EXAMPLE.md).

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
   # Before: bd create "Fix auth bug" --description="Details..." --deps discovered-from:bd-100
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
  bd create "Found duplicates during bd-X" \
    --description="Issues bd-A, bd-B, and bd-C are duplicates and need merging" \
    -p 2 --deps discovered-from:bd-X --json
  ```

## Development Guidelines

> **📋 For complete development instructions**, see [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md)

**Quick reference:**

- **Go version**: 1.21+
- **Testing**: Use `BEADS_DB=/tmp/test.db` to avoid polluting production database
- **Before committing**: Run tests (`go test -short ./...`) and linter (`golangci-lint run ./...`)
- **End of session**: Always run `bd sync` to flush/commit/push changes
- **Git hooks**: Run `bd hooks install` to ensure DB ↔ JSONL consistency

See [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for detailed workflows, testing patterns, and operational procedures.



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



## Common Development Tasks

See [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for detailed instructions on:

- Adding new commands
- Adding storage features
- Adding examples
- Building and testing
- Version management
- Release process

## Pro Tips for Agents

- Always use `--json` flags for programmatic use
- **Always run `bd sync` at end of session** to flush/commit/push immediately
- **Check `bd info --whats-new` at session start** if bd was recently upgraded
- **Run `bd hooks install`** if `bd info` warns about outdated git hooks
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

Use `gh` CLI tools for checking issues/PRs (see [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for details).

## Building, Testing, Versioning, and Releases

See [AGENT_INSTRUCTIONS.md](AGENT_INSTRUCTIONS.md) for complete details on:

- Building and testing (`go build`, `go test`)
- Version management (`./scripts/bump-version.sh`)
- Release process (`./scripts/release.sh`)

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

**OSS Contributor?** Use the contributor wizard for fork workflows:

```bash
bd init --contributor  # Interactive setup for separate planning repo
```

**Team Member?** Use the team wizard for branch workflows:

```bash
bd init --team  # Interactive setup for team collaboration
```

**Check for ready work:**

```bash
bd ready --json
```

**Create new issues:**

```bash
bd create "Issue title" --description="Detailed context" -t bug|feature|task -p 0-4 --json
bd create "Issue title" --description="What this issue is about" -p 1 --deps discovered-from:bd-123 --json
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
   - `bd create "Found bug" --description="Details about what was found" -p 1 --deps discovered-from:<parent-id>`
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
