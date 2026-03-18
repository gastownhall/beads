# Beads Quickstart

Get up and running with Beads in 2 minutes.

## Installation

```bash
go install github.com/steveyegge/beads/cmd/bd@latest
bd --help
```

If you're developing inside a clone of the Beads repo, use `make install` from
that checkout instead of leaving a hand-built `./bd` in the repo root.

## Initialize

First time in a repository:

```bash
# Basic setup (prompts for contributor mode)
bd init

# OSS contributor (fork workflow with separate planning repo)
bd init --contributor

# Team member (branch workflow for collaboration)
bd init --team
```

The wizard will:
- Create `.beads/` directory and Dolt database
- **Prompt for your role** (maintainer or contributor) unless a flag is provided
- Import existing issues from git (if any)
- Prompt to install git hooks (recommended)
- Prompt to configure git merge driver (recommended)
- Auto-start Dolt server for database operations

Notes:
- Dolt is the default (and only) storage backend. Data is stored in `.beads/dolt/`.
- Dolt uses a `dolt sql-server` for database operations.
- To import issues from an older installation, run `bd init --from-jsonl`.

### Role Configuration

During `bd init`, you'll be asked: "Contributing to someone else's repo? [y/N]"

- Answer **Y** if you're contributing to a fork (runs contributor wizard)
- Answer **N** if you're the maintainer or have push access

This sets `git config beads.role` which determines how beads routes issues:

| Role | Use Case | Issue Storage |
|------|----------|---------------|
| `maintainer` | Repo owner, team with push access | In-repo `.beads/` |
| `contributor` | Fork contributor, OSS contributor | Separate planning repo |

You can also configure manually:

```bash
# Set as contributor
git config beads.role contributor

# Set as maintainer
git config beads.role maintainer

# Check current role
git config --get beads.role
```

**Note:** If `beads.role` is not configured, beads falls back to URL-based detection (deprecated). Run `bd doctor` to check configuration status.

## Your First Issues

```bash
# Create a few issues
bd create "Set up database" -p 1 -t task
bd create "Create API" -p 2 -t feature
bd create "Add authentication" -p 2 -t feature

# List them
bd list
```

**Note:** Issue IDs are hash-based (e.g., `bd-a1b2`, `bd-f14c`) to prevent collisions when multiple agents/branches work concurrently.

## Hierarchical Issues (Epics)

For large features, use hierarchical IDs to organize work:

```bash
# Create epic (generates parent hash ID)
bd create "Auth System" -t epic -p 1
# Returns: bd-a3f8e9

# Create child tasks (automatically get .1, .2, .3 suffixes)
bd create "Design login UI" -p 1 --parent bd-a3f8e9       # bd-a3f8e9.1
bd create "Backend validation" -p 1 --parent bd-a3f8e9    # bd-a3f8e9.2
bd create "Integration tests" -p 1 --parent bd-a3f8e9     # bd-a3f8e9.3

# View hierarchy
bd dep tree bd-a3f8e9
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
bd dep add bd-2 bd-1

# Auth depends on API
bd dep add bd-3 bd-2

# View the tree
bd dep tree bd-3
```

Output:
```
🌲 Dependency tree for bd-3:

→ bd-3: Add authentication [P2] (open)
  → bd-2: Create API [P2] (open)
    → bd-1: Set up database [P1] (open)
```

**Dependency visibility:** `bd list` shows blocking dependencies inline:
```
○ bd-a1b2 [P1] [task] - Set up database
○ bd-f14c [P2] [feature] - Create API (blocked by: bd-a1b2)
○ bd-g25d [P2] [feature] - Add authentication (blocked by: bd-f14c)
```

## Find Ready Work

```bash
bd ready
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
bd update bd-1 --claim

# Complete it
bd close bd-1 --reason "Database setup complete"

# Check ready work again
bd ready
```

Now bd-2 is ready! 🎉

## Track Progress

```bash
# See blocked issues
bd blocked

# View statistics
bd stats
```

## Team Sync

Share issues with your team using Dolt remotes. Dolt stores data under `refs/dolt/data` on the same Git remote, separate from standard Git refs.

```bash
# Add a remote (GitHub example — also supports DoltHub, S3, GCS, local paths)
bd dolt remote add origin git+ssh://git@github.com/org/repo.git

# Push your issues
bd dolt push

# Pull teammates' changes
bd dolt pull
```

When a teammate clones the repo, `bd bootstrap` auto-detects the existing database on `refs/dolt/data` and clones it — no manual remote setup needed.

See [DOLT-BACKEND.md](DOLT-BACKEND.md#dolt-remotes) for remote configuration details and [FEDERATION-SETUP.md](../FEDERATION-SETUP.md) for multi-team sync.

## Database Location

By default, data is stored in `.beads/dolt/` within your repository.

## Migrating Databases

After upgrading bd, use `bd migrate` to check for and migrate old database files:

```bash
# Inspect migration plan (AI agents)
bd migrate --inspect --json

# Check schema and config
bd info --schema --json

# Preview migration changes
bd migrate --dry-run

# Migrate old databases to beads.db
bd migrate

# Migrate and clean up old files
bd migrate --cleanup --yes
```

**AI agents:** Use `--inspect` to analyze migration safety before running. The system verifies required config keys and data integrity invariants.

## Database Maintenance

As your project accumulates closed issues, the database grows. Manage size with these commands:

```bash
# View compaction statistics
bd admin compact --stats

# Preview compaction candidates (30+ days closed)
bd admin compact --analyze --json

# Apply agent-generated summary
bd admin compact --apply --id bd-42 --summary summary.txt

# Immediately delete closed issues (CAUTION: permanent!)
bd admin cleanup --force
```

**When to compact:**
- Database file > 10MB with many old closed issues
- After major project milestones when old issues are no longer relevant
- Before archiving a project phase

**Note:** Compaction is permanent graceful decay. Original content is discarded but viewable via `bd restore <id>` from git history.

## Next Steps

- Add labels: `bd create "Task" -l "backend,urgent"`
- Filter ready work: `bd ready --priority 1`
- Search issues: `bd list --status open`
- Detect cycles: `bd dep cycles`
- Use gates for PR/CI sync: See [DEPENDENCIES.md](DEPENDENCIES.md)

See [README.md](../README.md) for full documentation.
