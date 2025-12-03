# bd - Beads Issue Tracker 🔗

[![Go Version](https://img.shields.io/github/go-mod/go-version/steveyegge/beads)](https://go.dev/)
[![Release](https://img.shields.io/github/v/release/steveyegge/beads)](https://github.com/steveyegge/beads/releases)
[![npm version](https://img.shields.io/npm/v/@beads/bd)](https://www.npmjs.com/package/@beads/bd)
[![CI](https://img.shields.io/github/actions/workflow/status/steveyegge/beads/ci.yml?branch=main&label=tests)](https://github.com/steveyegge/beads/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/steveyegge/beads)](https://goreportcard.com/report/github.com/steveyegge/beads)
[![License](https://img.shields.io/github/license/steveyegge/beads)](LICENSE)
[![PyPI](https://img.shields.io/pypi/v/beads-mcp)](https://pypi.org/project/beads-mcp/)

**Give your coding agent a memory upgrade**

> ## 🎉 **v0.20.1: Multi-Worker Support Unlocked!** 🎉
>
> **Hash-based IDs eliminate merge conflicts and collision issues!**
>
> Previous versions used sequential IDs (bd-1, bd-2, bd-3...) which caused frequent collisions when multiple agents or branches created issues concurrently. Version 0.20.1 switches to **hash-based IDs** (bd-a1b2, bd-f14c, bd-3e7a...) that are collision-resistant and merge-friendly.
>
> **What's new:** ✅ Multi-clone, multi-branch, multi-agent workflows now work reliably  
> **What changed:** Issue IDs are now short hashes instead of sequential numbers  
> **Migration:** Run `bd migrate` to upgrade existing databases (optional - old DBs still work)
>
> Hash IDs use progressive length scaling (4/5/6 characters) with birthday paradox math to keep collisions extremely rare while maintaining human readability. See "Hash-Based Issue IDs" section below for details.

> **⚠️ Alpha Status**: This project is in active development. The core features work well, but expect API changes before 1.0. Use for development/internal projects first.

Beads is a lightweight memory system for coding agents, using a graph-based issue tracker. Four kinds of dependencies work to chain your issues together like beads, making them easy for agents to follow for long distances, and reliably perform complex task streams in the right order.

Drop Beads into any project where you're using a coding agent, and you'll enjoy an instant upgrade in organization, focus, and your agent's ability to handle long-horizon tasks over multiple compaction sessions. Your agents will use issue tracking with proper epics, rather than creating a swamp of rotten half-implemented markdown plans.

Instant start:

```bash
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

Then tell your coding agent to start using the `bd` tool instead of markdown for all new work, somewhere in your `AGENTS.md` or `CLAUDE.md`. That's all there is to it!

You don't use Beads directly as a human. Your coding agent will file and manage issues on your behalf. They'll file things they notice automatically, and you can ask them at any time to add or update issues for you.

Beads gives agents unprecedented long-term planning capability, solving their amnesia when dealing with complex nested plans. They can trivially query the ready work, orient themselves, and land on their feet as soon as they boot up.

Agents using Beads will no longer silently pass over problems they notice due to lack of context space -- instead, they will automatically file issues for newly-discovered work as they go. No more lost work, ever.

Beads issues are backed by git, but through a clever design it manages to act like a managed, centrally hosted SQL database shared by all of the agents working on a project (repo), even across machines.

Beads even improves work auditability. The issue tracker has a sophisticated audit trail, which agents can use to reconstruct complex operations that may have spanned multiple sessions.

Agents report that they enjoy working with Beads, and they will use it spontaneously for both recording new work and reasoning about your project in novel ways. Whether you are a human or an AI, Beads lets you have more fun and less stress with agentic coding.

![AI Agent using Beads](https://raw.githubusercontent.com/steveyegge/beads/main/.github/images/agent-using-beads.jpg)

## Features

- ✨ **Zero setup** - `bd init` creates project-local database (and your agent will do it)
- 🔗 **Dependency tracking** - Four dependency types (blocks, related, parent-child, discovered-from)
- 📋 **Ready work detection** - Automatically finds issues with no open blockers
- 🤖 **Agent-friendly** - `--json` flags for programmatic integration
- 📦 **Git-versioned** - JSONL records stored in git, synced across machines
- 🌍 **Distributed by design** - Agents on multiple machines share one logical database via git
- 🚀 **Optional Agent Mail** - Real-time multi-agent coordination (<100ms vs 2-5s git sync, 98.5% reduction in git traffic)
- 🔐 **Protected branch support** - Works with GitHub/GitLab protected branches via separate sync branch
- 🏗️ **Extensible** - Add your own tables to the SQLite database
- 🔍 **Multi-project isolation** - Each project gets its own database, auto-discovered by directory
- 🌲 **Dependency trees** - Visualize full dependency graphs
- 🎨 **Beautiful CLI** - Colored output for humans, JSON for bots
- 💾 **Full audit trail** - Every change is logged
- ⚡ **High performance** - Batch operations for bulk imports (1000 issues in ~950ms)
- 🗜️ **Memory decay** - Semantic compaction gracefully reduces old closed issues

## Requirements

**Linux users:** Beads requires **glibc 2.32+** (Ubuntu 22.04+, Debian 11+, RHEL 9+, or equivalent).

- ✅ **Supported:** Ubuntu 22.04+ (Jammy), Debian 11+ (Bullseye), Fedora 34+, RHEL 9+
- ❌ **Not supported:** Ubuntu 20.04 (glibc 2.31), Debian 10 (glibc 2.28), CentOS 7, RHEL 8

**Ubuntu 20.04 users:** Standard support ended April 2025. Please upgrade to Ubuntu 22.04+ or build from source:
```bash
git clone https://github.com/steveyegge/beads.git
cd beads
go build -o bd ./cmd/bd
```

**macOS/Windows:** No special requirements.

## Installation

**npm (Node.js environments, Claude Code for Web):**
```bash
npm install -g @beads/bd
```

**Claude Code web fallback** (if npm postinstall fails due to network restrictions):
```bash
go install github.com/steveyegge/beads/cmd/bd@latest
export PATH="$PATH:$HOME/go/bin"  # Required each session
```

**Quick install (macOS / Linux):**
```bash
curl -fsSL https://raw.githubusercontent.com/steveyegge/beads/main/scripts/install.sh | bash
```

**Quick install (Windows - PowerShell):**
```powershell
irm https://raw.githubusercontent.com/steveyegge/beads/main/install.ps1 | iex
```

**Homebrew (macOS/Linux):**
```bash
brew tap steveyegge/beads
brew install bd
```

For full, platform-specific instructions (Windows, Arch Linux, manual builds, IDE integrations, etc.) see the canonical guide in [docs/INSTALLING.md](docs/INSTALLING.md).

**Claude Code for Web:** See [npm-package/CLAUDE_CODE_WEB.md](npm-package/CLAUDE_CODE_WEB.md) for SessionStart hook setup.

## Quick Start

### For Humans

Beads is designed for **AI coding agents** to use on your behalf. Setup takes 30 seconds:

**You run this once (humans only):**
```bash
# In your project root:
bd init

# For OSS contributors (fork workflow):
bd init --contributor

# For team members (branch workflow):
bd init --team

# For protected branches (GitHub/GitLab):
bd init --branch beads-metadata

# bd will:
# - Create .beads/ directory with database
# - Import existing issues from git (if any)
# - Prompt to install git hooks (recommended: say yes)
# - Prompt to configure git merge driver (recommended: say yes)
# - Auto-start daemon for sync

# Then tell your agent about bd:
echo -e "\nBEFORE ANYTHING ELSE: run 'bd onboard' and follow the instructions" >> AGENTS.md
```

**Protected branches?** If your `main` branch is protected, use `bd init --branch beads-metadata` to commit issue updates to a separate branch. See [docs/PROTECTED_BRANCHES.md](docs/PROTECTED_BRANCHES.md) for details.

**Your agent does the rest:** Next time your agent starts, it will:
1. Run `bd onboard` and receive integration instructions
2. Add bd workflow documentation to AGENTS.md
3. Update CLAUDE.md with a note (if present)
4. Remove the bootstrap instruction

**For agents setting up repos:** Use `bd init --quiet` for non-interactive setup (auto-installs git hooks and merge driver, no prompts).

**For new repo clones:** Run `bd init` (or `bd init --quiet` for agents) to import existing issues from `.beads/issues.jsonl` automatically.

**Git merge driver:** During `bd init`, beads configures git to use `bd merge` for intelligent JSONL merging. This prevents conflicts when multiple branches modify issues. Skip with `--skip-merge-driver` if needed. To configure manually later:
```bash
git config merge.beads.driver "bd merge %A %O %A %B"
git config merge.beads.name "bd JSONL merge driver"
echo ".beads/beads.jsonl merge=beads" >> .gitattributes
```

### Files Created by `bd init`

**`bd init` creates these files in your repository:**

**Should be committed to git:**
- `.gitattributes` - Configures git merge driver for intelligent JSONL merging (critical for team collaboration)
- `.beads/beads.jsonl` - Issue data in JSONL format (source of truth, synced via git)
- `.beads/deletions.jsonl` - Deletion manifest for cross-clone propagation (tracks deleted issues)
- `.beads/config.yaml` - Repository configuration template
- `.beads/README.md` - Documentation about beads for repository visitors
- `.beads/metadata.json` - Database metadata

**Should be in `.gitignore` (local-only):**
- `.beads/beads.db` - SQLite cache (auto-synced with JSONL)
- `.beads/beads.db-*` - SQLite journal files
- `.beads/bd.sock` / `.beads/bd.pipe` - Daemon communication socket
- `.beads/.exclusive-lock` - Daemon lock file
- `.git/beads-worktrees/` - Git worktrees (only created when using protected branch workflows)

The `.gitignore` entries are automatically created inside `.beads/.gitignore` by `bd init`, but your project's root `.gitignore` should also exclude the database and daemon files if you want to keep your git status clean.

**Using devcontainers?** Open the repository in a devcontainer (GitHub Codespaces or VS Code Remote Containers) and bd will be automatically installed with git hooks configured. See [.devcontainer/README.md](.devcontainer/README.md) for details.

### Stealth Mode (Isolated Usage)

Want to use beads in your local clone without other collaborators seeing any beads-related files? Use **stealth mode**:

```bash
bd init --stealth
```

**Stealth mode configures:**
- **Global gitignore** (`~/.config/git/ignore`) - Ignores `**/.beads/` and `**/.claude/settings.local.json` globally
- **Claude Code settings** (`.claude/settings.local.json`) - Adds `bd onboard` instruction for AI agents

**Perfect for:**
- Personal experimentation with beads
- Working on repos where not everyone uses beads yet
- Keeping your issue tracking private while contributing to open source projects
- AI agents that should use beads without affecting the main repo

**What stays invisible to others:**
- No `.beads/` directory tracked in git
- No AGENTS.md or README.md mentions of beads
- No local `.gitattributes` or `.gitignore` modifications
- Your beads database and issues remain local-only

**How it works:** The global git configuration handles beads merging automatically, while the global gitignore ensures beads files never get committed to shared repos. Your AI agents get the onboard instruction automatically without exposing beads to other repo collaborators.

Most tasks will be created and managed by agents during conversations. You can check on things with:

```bash
bd list                  # See what's being tracked
bd show <issue-id>       # Review a specific issue
bd ready                 # See what's ready to work on
bd dep tree <issue-id>   # Visualize dependencies
```

### For AI Agents

Run the interactive guide to learn the full workflow:

```bash
bd quickstart
```

Quick reference for agent workflows:

```bash
# Find ready work
bd ready --json | jq '.[0]'

# Create issues during work
bd create "Discovered bug" -t bug -p 0 --json

# Link discovered work back to parent
bd dep add <new-id> <parent-id> --type discovered-from

# Update status
bd update <issue-id> --status in_progress --json

# Complete work
bd close <issue-id> --reason "Implemented" --json
```

## Configuring Your Own AGENTS.md

**Recommendation for project maintainers:** Add a session-ending protocol to your project's `AGENTS.md` file to ensure agents properly manage issue tracking and sync the database before finishing work.

This pattern has proven invaluable for maintaining database hygiene and preventing lost work. Here's what to include (adapt for your workflow):

**1. File/update issues for remaining work**
- Agents should proactively create issues for discovered bugs, TODOs, and follow-up tasks
- Close completed issues and update status for in-progress work

**2. Run quality gates (if applicable)**
- Tests, linters, builds - only if code changes were made
- File P0 issues if builds are broken

**3. Sync the issue tracker carefully**
- Work methodically to ensure local and remote issues merge safely
- Handle git conflicts thoughtfully (sometimes accepting remote and re-importing)
- Goal: clean reconciliation where no issues are lost

**4. Verify clean state**
- All changes committed and pushed
- No untracked files remain

**5. Choose next work**
- Provide a formatted prompt for the next session with context

See the ["Landing the Plane"](AGENT_INSTRUCTIONS.md#landing-the-plane) section in this project's documentation for a complete example you can adapt. The key insight: explicitly reminding agents to maintain issue tracker hygiene prevents the common problem of agents creating issues during work but forgetting to sync them at session end.

## The Magic: Distributed Database via Git

Here's the crazy part: **bd acts like a centralized database, but it's actually distributed via git.**

When you install bd on any machine with your project repo, you get:
- ✅ Full query capabilities (dependencies, ready work, etc.)
- ✅ Fast local operations (<100ms via SQLite)
- ✅ Shared state across all machines (via git)
- ✅ No server, no daemon required, no configuration
- ✅ AI-assisted merge conflict resolution

**How it works:** Each machine has a local SQLite cache (`.beads/*.db`, gitignored). Source of truth is JSONL (`.beads/issues.jsonl`, committed to git). Auto-sync keeps them in sync: SQLite → JSONL after CRUD operations (5-second debounce), JSONL → SQLite when JSONL is newer (e.g., after `git pull`).

**The result:** Agents on your laptop, your desktop, and your coworker's machine all query and update what *feels* like a single shared database, but it's really just git doing what git does best - syncing text files across machines.

No PostgreSQL instance. No MySQL server. No hosted service. Just install bd, clone the repo, and you're connected to the "database."

### Git Workflow & Auto-Sync

bd automatically syncs your local database with git:

**Making changes (auto-export):**
```bash
bd create "Fix bug" -p 1
bd update bd-a1b2 --status in_progress
# bd automatically exports to .beads/issues.jsonl after 5 seconds

git add .beads/issues.jsonl
git commit -m "Working on bd-a1b2"
git push
```

**Pulling changes (auto-import):**
```bash
git pull
# bd automatically detects JSONL is newer and imports on next command

bd ready  # Fresh data from git!
bd list   # Shows issues from other machines
```

**Manual sync (optional):**
```bash
bd sync  # Immediately flush pending changes and import latest JSONL
```

**For zero-lag sync**, install the git hooks:
```bash
cd examples/git-hooks && ./install.sh
```

This adds:
- **pre-commit** - Immediate flush before commit (no 5-second wait)
- **post-merge** - Guaranteed import after `git pull` or `git merge`

**Disable auto-sync** if needed:
```bash
bd --no-auto-flush create "Issue"   # Skip auto-export
bd --no-auto-import list            # Skip auto-import check
```

## Hash-Based Issue IDs

**Version 0.20.1 introduces collision-resistant hash-based IDs** to enable reliable multi-worker and multi-branch workflows.

### ID Format

Issue IDs now use short hexadecimal hashes instead of sequential numbers:

- **Before (v0.20.0):** `bd-1`, `bd-2`, `bd-152` (sequential numbers)
- **After (v0.20.1):** `bd-a1b2`, `bd-f14c`, `bd-3e7a` (4-6 character hashes)

Hash IDs use **progressive length scaling** based on database size:
- **0-500 issues:** 4-character hashes (e.g., `bd-a1b2`)
- **500-1,500 issues:** 5-character hashes (e.g., `bd-f14c3`)
- **1,500-10,000 issues:** 6-character hashes (e.g., `bd-3e7a5b`)

### Why Hash IDs?

**The problem with sequential IDs:**
When multiple agents or branches create issues concurrently, sequential IDs collide:
- Agent A creates `bd-10` on branch `feature-auth`
- Agent B creates `bd-10` on branch `feature-payments`
- Git merge creates duplicate IDs → collision resolution required

**How hash IDs solve this:**
Hash IDs are generated from random data, making concurrent creation collision-free:
- Agent A creates `bd-a1b2` (hash of random UUID)
- Agent B creates `bd-f14c` (different hash, different UUID)
- Git merge succeeds cleanly → no collision resolution needed

### Birthday Paradox Math

Hash IDs use **birthday paradox probability** to determine length:

| Hash Length | Total Space | 50% Collision at N Issues | 1% Collision at N Issues |
|-------------|-------------|---------------------------|--------------------------|
| 4 chars     | 65,536      | ~304 issues               | ~38 issues               |
| 5 chars     | 1,048,576   | ~1,217 issues             | ~153 issues              |
| 6 chars     | 16,777,216  | ~4,869 issues             | ~612 issues              |

**Our thresholds are conservative:** We switch from 4→5 chars at 500 issues (well past the 4-char 1% threshold of ~38, giving headroom before 50% at ~304) and from 5→6 chars at 1,500 issues (same pattern for 5-char thresholds).

**Progressive extension on collision:** If a hash collision does occur, bd automatically extends the hash to 7 or 8 characters instead of remapping to a new ID.

### Migration

**Existing databases continue to work** - no forced migration. Run `bd migrate` when ready:

```bash
# Inspect migration plan (for AI agents)
bd migrate --inspect --json

# Check schema and config state
bd info --schema --json

# Preview migration
bd migrate --dry-run

# Migrate database schema (removes obsolete issue_counters table)
bd migrate

# Show current database info
bd info
```

**AI-supervised migrations:** The `--inspect` flag provides migration plan analysis for AI agents. The system verifies data integrity invariants (required config keys, foreign key constraints, issue counts) before committing migrations.

**Note:** Hash IDs require schema version 9+. The `bd migrate` command detects old schemas and upgrades automatically.

### Hierarchical Child IDs

Hash IDs support **hierarchical children** for natural work breakdown structures. Child IDs use dot notation:

```
bd-a3f8e9      [epic] Auth System
bd-a3f8e9.1    [task] Design login UI
bd-a3f8e9.2    [task] Backend validation
bd-a3f8e9.3    [epic] Password Reset
bd-a3f8e9.3.1  [task] Email templates
bd-a3f8e9.3.2  [task] Reset flow tests
```

**Benefits:**
- **Collision-free**: Parent hash ensures unique namespace
- **Human-readable**: Clear parent-child relationships
- **Flexible depth**: Up to 3 levels of nesting
- **No coordination needed**: Each epic owns its child ID space

**Common patterns:**
- 1 level: Epic → tasks (most projects)
- 2 levels: Epic → features → tasks (large projects)
- 3 levels: Epic → features → stories → tasks (complex projects)

**Example workflow:**
```bash
# Create parent epic (generates hash ID automatically)
bd create "Auth System" -t epic -p 1
# Returns: bd-a3f8e9

# Create child tasks
bd create "Design login UI" -p 1       # Auto-assigned: bd-a3f8e9.1
bd create "Backend validation" -p 1    # Auto-assigned: bd-a3f8e9.2

# Create nested epic with its own children
bd create "Password Reset" -t epic -p 1  # Auto-assigned: bd-a3f8e9.3
bd create "Email templates" -p 1          # Auto-assigned: bd-a3f8e9.3.1
```

**Note:** Child IDs are automatically assigned sequentially within each parent's namespace. No need to specify parent manually - bd tracks context from git branch/working directory.

## Usage

### Health Check

Check installation health: `bd doctor` validates your `.beads/` setup, database version, ID format, and CLI version. Provides actionable fixes for any issues found.

### Creating Issues

```bash
bd create "Fix bug" -d "Description" -p 1 -t bug
bd create "Add feature" --description "Long description" --priority 2 --type feature
bd create "Task" -l "backend,urgent" --assignee alice

# Get JSON output for programmatic use
bd create "Fix bug" -d "Description" --json

# Create from templates (built-in: epic, bug, feature)
bd create --from-template epic "Q4 Platform Improvements"
bd create --from-template bug "Auth token validation fails"
bd create --from-template feature "Add OAuth support"

# Override template defaults
bd create --from-template bug "Critical issue" -p 0  # Override priority

# Create multiple issues from a markdown file
bd create -f feature-plan.md
```

Options:
- `-f, --file` - Create multiple issues from markdown file
- `--from-template` - Use template (epic, bug, feature, or custom)
- `-d, --description` - Issue description
- `-p, --priority` - Priority (0-4, 0=highest, default=2)
- `-t, --type` - Type (bug|feature|task|epic|chore, default=task)
- `-a, --assignee` - Assign to user
- `-l, --labels` - Comma-separated labels
- `--id` - Explicit issue ID (e.g., `worker1-100` for ID space partitioning)
- `--json` - Output in JSON format

See `bd template list` for available templates and `bd help template` for managing custom templates.

### Viewing Issues

```bash
bd info                                    # Show database path and daemon status
bd show bd-a1b2                            # Show full details
bd list                                    # List all issues
bd list --status open                      # Filter by status
bd list --priority 1                       # Filter by priority
bd list --assignee alice                   # Filter by assignee
bd list --label=backend,urgent             # Filter by labels (AND)
bd list --label-any=frontend,backend       # Filter by labels (OR)

# Advanced filters
bd list --title-contains "auth"            # Search title
bd list --desc-contains "implement"        # Search description
bd list --notes-contains "TODO"            # Search notes
bd list --id bd-123,bd-456                 # Specific IDs (comma-separated)

# Date range filters (YYYY-MM-DD or RFC3339)
bd list --created-after 2024-01-01         # Created after date
bd list --created-before 2024-12-31        # Created before date
bd list --updated-after 2024-06-01         # Updated after date
bd list --updated-before 2024-12-31        # Updated before date
bd list --closed-after 2024-01-01          # Closed after date
bd list --closed-before 2024-12-31         # Closed before date

# Empty/null checks
bd list --empty-description                # Issues with no description
bd list --no-assignee                      # Unassigned issues
bd list --no-labels                        # Issues with no labels

# Priority ranges
bd list --priority-min 0 --priority-max 1  # P0 and P1 only
bd list --priority-min 2                   # P2 and below

# Combine multiple filters
bd list --status open --priority 1 --label-any urgent,critical --no-assignee

# JSON output for agents
bd info --json
bd list --json
bd show bd-a1b2 --json
```

### Updating Issues

```bash
bd update bd-a1b2 --status in_progress
bd update bd-a1b2 --priority 2
bd update bd-a1b2 --assignee bob
bd close bd-a1b2 --reason "Completed"
bd close bd-a1b2 bd-f14c bd-3e7a   # Close multiple

# JSON output
bd update bd-a1b2 --status in_progress --json
```

### Dependencies

```bash
# Add dependency (bd-f14c depends on bd-a1b2)
bd dep add bd-f14c bd-a1b2
bd dep add bd-3e7a bd-a1b2 --type blocks

# Remove dependency
bd dep remove bd-f14c bd-a1b2

# Show dependency tree
bd dep tree bd-f14c

# Detect cycles
bd dep cycles
```

#### Dependency Types

- **blocks**: Hard blocker (default) - issue cannot start until blocker is resolved
- **related**: Soft relationship - issues are connected but not blocking
- **parent-child**: Hierarchical relationship (child depends on parent)
- **discovered-from**: Issue discovered during work on another issue (automatically inherits parent's `source_repo`)

Only `blocks` dependencies affect ready work detection.

> **Note:** Issues created with `discovered-from` dependencies automatically inherit the parent's `source_repo` field, ensuring discovered work stays in the same repository as the parent task.

### Finding Work

```bash
# Show ready work (no blockers)
bd ready
bd ready --limit 20
bd ready --priority 1
bd ready --assignee alice

# Sort policies (hybrid is default)
bd ready --sort priority    # Strict priority order (P0, P1, P2, P3)
bd ready --sort oldest      # Oldest issues first (backlog clearing)
bd ready --sort hybrid      # Recent by priority, old by age (default)

# Show blocked issues
bd blocked

# Statistics
bd stats

# JSON output for agents
bd ready --json
```

### Labels

Add flexible metadata to issues for filtering and organization:

```bash
# Add labels during creation
bd create "Fix auth bug" -t bug -p 1 -l auth,backend,urgent

# Add/remove labels
bd label add bd-a1b2 security
bd label remove bd-a1b2 urgent

# List labels
bd label list bd-a1b2            # Labels on one issue
bd label list-all                # All labels with counts

# Filter by labels
bd list --label backend,auth     # AND: must have ALL labels
bd list --label-any frontend,ui  # OR: must have AT LEAST ONE
```

**See [docs/LABELS.md](docs/LABELS.md) for complete label documentation and best practices.**

### Deleting Issues

```bash
# Single issue deletion (preview mode)
bd delete bd-a1b2

# Force single deletion
bd delete bd-a1b2 --force

# Batch deletion
bd delete bd-a1b2 bd-f14c bd-3e7a --force

# Delete from file (one ID per line)
bd delete --from-file deletions.txt --force

# Cascade deletion (recursively delete dependents)
bd delete bd-a1b2 --cascade --force
```

The delete operation removes all dependency links, updates text references to `[deleted:ID]`, and removes the issue from database and JSONL.

### Configuration

Manage per-project configuration for external integrations:

```bash
# Set configuration
bd config set jira.url "https://company.atlassian.net"
bd config set jira.project "PROJ"

# Get configuration
bd config get jira.url

# List all configuration
bd config list --json

# Unset configuration
bd config unset jira.url
```

**See [docs/CONFIG.md](docs/CONFIG.md) for complete configuration documentation.**

### Compaction (Memory Decay)

Beads provides **agent-driven compaction** - your AI agent decides what to compress, no API keys required:

```bash
# Agent-driven workflow (recommended)
bd compact --analyze --json              # Get candidates with full content
bd compact --apply --id bd-42 --summary summary.txt

# Legacy AI-powered workflow (requires ANTHROPIC_API_KEY)
bd compact --auto --dry-run --all        # Preview candidates
bd compact --auto --all                  # Auto-compact all eligible issues
```

**How it works:**
1. Use `--analyze` to export candidates (closed 30+ days) with full content
2. Summarize the content using any LLM (Claude, GPT, local model, etc.)
3. Use `--apply` to persist the summary and mark as compacted

This is agentic memory decay - your database naturally forgets fine-grained details while preserving essential context. The agent has full control over compression quality.

### Export/Import

```bash
# Export to JSONL (automatic by default)
bd export -o issues.jsonl

# Import from JSONL (automatic when JSONL is newer)
bd import -i issues.jsonl

# Handle missing parents during import
bd import -i issues.jsonl --orphan-handling resurrect  # Auto-recreate deleted parents
bd import -i issues.jsonl --orphan-handling skip       # Skip orphans with warning
bd import -i issues.jsonl --orphan-handling strict     # Fail on missing parents

# Manual sync
bd sync
```

**Import Orphan Handling:**

When importing hierarchical issues (e.g., `bd-abc.1`, `bd-abc.2`), bd needs to handle cases where the parent (`bd-abc`) has been deleted:

- **`allow` (default)** - Import orphans without validation. Most permissive, ensures no data loss.
- **`resurrect`** - Search JSONL history for deleted parents and recreate them as tombstones (Status=Closed, Priority=4). Preserves hierarchy.
- **`skip`** - Skip orphaned children with warning. Partial import.
- **`strict`** - Fail import if parent is missing.

Configure default behavior: `bd config set import.orphan_handling resurrect`

See [docs/CONFIG.md](docs/CONFIG.md) for complete configuration documentation.

**Note:** Auto-sync is enabled by default. Manual export/import is rarely needed.

### Managing Daemons

bd runs a background daemon per workspace for auto-sync and RPC operations. Use `bd daemons` to manage multiple daemons:

```bash
# List all running daemons
bd daemons list

# Check health (version mismatches, stale sockets)
bd daemons health

# Stop a specific daemon
bd daemons stop /path/to/workspace
bd daemons stop 12345  # By PID

# Restart a specific daemon
bd daemons restart /path/to/workspace
bd daemons restart 12345  # By PID

# View daemon logs
bd daemons logs /path/to/workspace -n 100
bd daemons logs 12345 -f  # Follow mode

# Stop all daemons
bd daemons killall
bd daemons killall --force  # Force kill if graceful fails
```

**Common use cases:**
- **After upgrading bd**: Run `bd daemons health` to check for version mismatches, then `bd daemons killall` to restart all daemons with the new version
- **Debugging**: Use `bd daemons logs <workspace>` to view daemon logs
- **Cleanup**: `bd daemons list` auto-removes stale sockets

See [commands/daemons.md](commands/daemons.md) for complete documentation.

### Web Interface

A standalone web interface for real-time issue monitoring is available as an example:

```bash
# Build the monitor-webui
cd examples/monitor-webui
go build

# Start web UI on localhost:8080
./monitor-webui

# Custom port and host
./monitor-webui -port 3000
./monitor-webui -host 0.0.0.0 -port 8080  # Listen on all interfaces
```

The monitor provides:
- **Real-time table view** of all issues with filtering by status and priority
- **Click-through details** - Click any issue to view full details in a modal
- **Live updates** - WebSocket connection for real-time changes via daemon RPC
- **Responsive design** - Mobile-friendly card view on small screens
- **Statistics dashboard** - Quick overview of issue counts and ready work
- **Clean UI** - Simple, fast interface styled with milligram.css

The monitor is particularly useful for:
- **Team visibility** - Share a dashboard view of project status
- **AI agent supervision** - Watch your coding agent create and update issues in real-time
- **Quick browsing** - Faster than CLI for exploring issue details
- **Mobile access** - Check project status from your phone

See [examples/monitor-webui/](examples/monitor-webui/) for complete documentation.

## Examples

Check out the **[examples/](examples/)** directory for:
- **[Python agent](examples/python-agent/)** - Full agent implementation in Python
- **[Bash agent](examples/bash-agent/)** - Shell script agent example
- **[Git hooks](examples/git-hooks/)** - Automatic export/import on git operations
- **[Branch merge workflow](examples/branch-merge/)** - Handle ID collisions when merging branches
- **[Claude Desktop MCP](examples/claude-desktop-mcp/)** - MCP server for Claude Desktop
- **[Claude Code Plugin](docs/PLUGIN.md)** - One-command installation with slash commands

## Advanced Features

For advanced usage, see:

- **[docs/ADVANCED.md](docs/ADVANCED.md)** - Prefix renaming, merging duplicates, daemon configuration
- **[docs/CONFIG.md](docs/CONFIG.md)** - Configuration system for integrations
- **[docs/EXTENDING.md](docs/EXTENDING.md)** - Database extension patterns
- **[docs/ADVANCED.md](docs/ADVANCED.md)** - JSONL format and merge strategies

## Documentation

- **[README.md](README.md)** - You are here! Core features and quick start
- **[docs/INSTALLING.md](docs/INSTALLING.md)** - Complete installation guide for all platforms
- **[docs/QUICKSTART.md](docs/QUICKSTART.md)** - Interactive tutorial (`bd quickstart`)
- **[docs/AGENT_MAIL_QUICKSTART.md](docs/AGENT_MAIL_QUICKSTART.md)** - 5-minute Agent Mail setup guide
- **[docs/AGENT_MAIL.md](docs/AGENT_MAIL.md)** - Complete Agent Mail integration guide
- **[docs/MULTI_REPO_MIGRATION.md](docs/MULTI_REPO_MIGRATION.md)** - Multi-repo workflow guide (OSS, teams, multi-phase)
- **[docs/MULTI_REPO_AGENTS.md](docs/MULTI_REPO_AGENTS.md)** - Multi-repo patterns for AI agents
- **[docs/FAQ.md](docs/FAQ.md)** - Frequently asked questions
- **[docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md)** - Common issues and solutions
- **[docs/ADVANCED.md](docs/ADVANCED.md)** - Advanced features and use cases
- **[docs/LABELS.md](docs/LABELS.md)** - Complete label system guide
- **[docs/CONFIG.md](docs/CONFIG.md)** - Configuration system
- **[docs/EXTENDING.md](docs/EXTENDING.md)** - Database extension patterns
- **[docs/ADVANCED.md](docs/ADVANCED.md)** - JSONL format analysis
- **[docs/PLUGIN.md](docs/PLUGIN.md)** - Claude Code plugin documentation
- **[CONTRIBUTING.md](CONTRIBUTING.md)** - Contribution guidelines
- **[SECURITY.md](SECURITY.md)** - Security policy

## Community & Ecosystem

### Third-Party Tools

- **[beads-ui](https://github.com/mantoni/beads-ui)** - Local web interface with live updates, kanban board, and keyboard navigation. Zero-setup launch with `npx beads-ui start`. Built by [@mantoni](https://github.com/mantoni).
- **[bdui](https://github.com/assimelha/bdui)** - Real-time terminal UI with kanban board, tree view, dependency graph, and statistics dashboard. Vim-style navigation, search/filter, themes, and native notifications. Built by [@assimelha](https://github.com/assimelha).
- **[perles](https://github.com/zjrosen/perles)** - Terminal UI with BQL (Beads Query Language) for local search and multi-view kanban boards with custom colors. Built by [@zjrosen](https://github.com/zjrosen).
- **[vscode-beads](https://marketplace.visualstudio.com/items?itemName=planet57.vscode-beads)** - VS Code extension with issues panel, details view, and daemon management. Built by [@jdillon](https://github.com/jdillon).

Have you built something cool with bd? [Open an issue](https://github.com/steveyegge/beads/issues) to get it featured here!

## Development

```bash
# Run tests
go test ./...

# Build
go build -o bd ./cmd/bd

# Run
./bd create "Test issue"

# Bump version
./scripts/bump-version.sh 0.9.3           # Update all versions, show diff
./scripts/bump-version.sh 0.9.3 --commit  # Update and auto-commit
```

See [scripts/README.md](scripts/README.md) for more development scripts.

## License

MIT

## Credits

Built with ❤️ by developers who love tracking dependencies and finding ready work.

Inspired by the need for a simpler, dependency-aware issue tracker.
