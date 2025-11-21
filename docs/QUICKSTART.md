# Beads Quickstart

Get up and running with Beads in 2 minutes.

## Installation

```bash
cd ~/src/beads
go build -o bd ./cmd/bd
./bd --help
```

## Initialize

First time in a repository:

```bash
# Basic setup
bd init

# OSS contributor (fork workflow with separate planning repo)
bd init --contributor

# Team member (branch workflow for collaboration)
bd init --team

# Protected main branch (GitHub/GitLab)
bd init --branch beads-metadata
```

The wizard will:
- Create `.beads/` directory and database
- Import existing issues from git (if any)
- Prompt to install git hooks (recommended)
- Prompt to configure git merge driver (recommended)
- Auto-start daemon for sync

## Your First Issues

```bash
# Create a few issues
./bd create "Set up database" -p 1 -t task
./bd create "Create API" -p 2 -t feature
./bd create "Add authentication" -p 2 -t feature

# List them
./bd list
```

**Note:** Issue IDs are hash-based (e.g., `bd-a1b2`, `bd-f14c`) to prevent collisions when multiple agents/branches work concurrently.

## Hierarchical Issues (Epics)

For large features, use hierarchical IDs to organize work:

```bash
# Create epic (generates parent hash ID)
./bd create "Auth System" -t epic -p 1
# Returns: bd-a3f8e9

# Create child tasks (automatically get .1, .2, .3 suffixes)
./bd create "Design login UI" -p 1       # bd-a3f8e9.1
./bd create "Backend validation" -p 1    # bd-a3f8e9.2
./bd create "Integration tests" -p 1     # bd-a3f8e9.3

# View hierarchy
./bd dep tree bd-a3f8e9
```

Output:
```
🌲 Dependency tree for bd-a3f8e9:

→ bd-a3f8e9: Auth System [epic] [P1] (open)
  → bd-a3f8e9.1: Design login UI [P1] (open)
  → bd-a3f8e9.2: Backend validation [P1] (open)
  → bd-a3f8e9.3: Integration tests [P1] (open)
```

## Add Dependencies

```bash
# API depends on database
./bd dep add bd-2 bd-1

# Auth depends on API
./bd dep add bd-3 bd-2

# View the tree
./bd dep tree bd-3
```

Output:
```
🌲 Dependency tree for bd-3:

→ bd-3: Add authentication [P2] (open)
  → bd-2: Create API [P2] (open)
    → bd-1: Set up database [P1] (open)
```

## Find Ready Work

```bash
./bd ready
```

Output:
```
📋 Ready work (1 issues with no blockers):

1. [P1] bd-1: Set up database
```

Only bd-1 is ready because bd-2 and bd-3 are blocked!

## Work the Queue

```bash
# Start working on bd-1
./bd update bd-1 --status in_progress

# Complete it
./bd close bd-1 --reason "Database setup complete"

# Check ready work again
./bd ready
```

Now bd-2 is ready! 🎉

## Track Progress

```bash
# See blocked issues
./bd blocked

# View statistics
./bd stats
```

## Database Location

By default: `~/.beads/default.db`

You can use project-specific databases:

```bash
./bd --db ./my-project.db create "Task"
```

## Migrating Databases

After upgrading bd, use `bd migrate` to check for and migrate old database files:

```bash
# Inspect migration plan (AI agents)
./bd migrate --inspect --json

# Check schema and config
./bd info --schema --json

# Preview migration changes
./bd migrate --dry-run

# Migrate old databases to beads.db
./bd migrate

# Migrate and clean up old files
./bd migrate --cleanup --yes
```

**AI agents:** Use `--inspect` to analyze migration safety before running. The system verifies required config keys and data integrity invariants.

## Database Maintenance

As your project accumulates closed issues, the database grows. Manage size with these commands:

```bash
# View compaction statistics
bd compact --stats

# Preview compaction candidates (30+ days closed)
bd compact --analyze --json --no-daemon

# Apply agent-generated summary
bd compact --apply --id bd-42 --summary summary.txt --no-daemon

# Immediately delete closed issues (CAUTION: permanent!)
bd cleanup --force
```

**When to compact:**
- Database file > 10MB with many old closed issues
- After major project milestones when old issues are no longer relevant
- Before archiving a project phase

**Note:** Compaction is permanent graceful decay. Original content is discarded but viewable via `bd restore <id>` from git history.

## Advanced: Agent Mail (Optional)

For **multi-agent workflows** (2+ AI agents working concurrently), Agent Mail provides real-time coordination:

**Benefits:**
- 20-50x latency reduction (<100ms vs 2-5s git sync)
- Collision prevention via file reservations
- Agents can't accidentally claim same issue

**Quick setup:**
```bash
# Install and start server (one-time)
git clone https://github.com/Dicklesworthstone/mcp_agent_mail.git
cd mcp_agent_mail && python3 -m venv .venv && source .venv/bin/activate
pip install -e .
python -m mcp_agent_mail.cli serve-http

# Configure each agent (environment variables)
export BEADS_AGENT_MAIL_URL=http://127.0.0.1:8765
export BEADS_AGENT_NAME=assistant-alpha  # Unique per agent
export BEADS_PROJECT_ID=my-project

# Use bd normally - Agent Mail auto-activates
bd ready
bd update bd-42 --status in_progress  # Reserves instantly
```

**When to use:**
- ✅ Multiple agents working simultaneously
- ✅ High collision risk (frequent status updates)
- ❌ Single agent workflows (unnecessary overhead)

See [AGENT_MAIL.md](AGENT_MAIL.md) for complete guide.

## Next Steps

- Add labels: `./bd create "Task" -l "backend,urgent"`
- Filter ready work: `./bd ready --priority 1`
- Search issues: `./bd list --status open`
- Detect cycles: `./bd dep cycles`

See [README.md](README.md) for full documentation.
