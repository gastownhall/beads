# Beads Quickstart

Get up and running with Beads in 2 minutes.

## Why Beads?

Flat issue trackers (GitHub Issues, Jira, etc.) show you a list of open items. You pick one. But if that item depends on something else that isn't done yet, you've wasted time. Multiply this across a team of AI agents and humans, and you get thrashing.

Beads tracks **dependencies between issues** and computes a **ready queue** — only items with no active blockers appear. Here's the difference:

**Flat tracker (GitHub Issues):**
```
Open issues: Set up database, Create API, Add authentication
→ An agent picks "Add authentication" and gets stuck immediately
```

**Beads:**
```bash
$ bd ready
1. [P1] [task] bd-1: Set up database

$ bd ready --explain --json | jq '.blocked[0]'
{
  "id": "bd-3",
  "title": "Add authentication",
  "blocked_by": [{"id": "bd-2", "title": "Create API", "status": "open"}]
}
```

The agent picks the right task every time. No wasted cycles.

## Installation

Install bd using your preferred method (see [INSTALLING.md](INSTALLING.md) for all options):

```bash
brew install beads        # macOS/Linux
# or
npm install -g @beads/bd  # Node.js users
```

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
- Create `.beads/` directory and embedded Dolt database
- **Prompt for your role** (maintainer or contributor) unless a flag is provided
- Import existing issues from git (if any)
- Prompt to install git hooks (recommended)
- Prompt to configure git merge driver (recommended)

Notes:
- Dolt is the default (and only) storage backend. Data is stored in `.beads/embeddeddolt/`.
- By default, Dolt runs in **embedded mode** (in-process, no server needed).
- For multi-writer setups, use `bd init --server` to connect to a `dolt sql-server` instead.
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

**Understanding why:** Use `--explain` to see the full graph reasoning:

```bash
bd ready --explain
```

Output:
```
📊 Ready Work Explanation

● Ready (1 issues):

  bd-1 [P1] Set up database
    Reason: no blocking dependencies
    Unblocks: 1 issue(s)

● Blocked (2 issues):

  bd-2 [P2] Create API
    ← blocked by bd-1: Set up database [open]

  bd-3 [P2] Add authentication
    ← blocked by bd-2: Create API [open]

─ Summary: 1 ready, 2 blocked
```

**Note:** `bd ready` is not the same as `bd list --status open`. The `list` command shows all open issues regardless of blockers. The `ready` command computes the dependency graph and only shows truly unblocked work.

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

By default (embedded mode), data is stored in `.beads/embeddeddolt/` within your repository.
In server mode, data is managed by the external `dolt sql-server`.

## Next Steps

- Add labels: `bd create "Task" -l "backend,urgent"`
- Filter ready work: `bd ready --priority 1`
- Explain the graph: `bd ready --explain`
- Check graph integrity: `bd graph check`
- Search issues: `bd list --status open`
- Detect cycles: `bd dep cycles`
- Use gates for PR/CI sync: See [DEPENDENCIES.md](DEPENDENCIES.md)
- Sync across computers: See [SYNC_SETUP.md](SYNC_SETUP.md)
- Database maintenance: See [ADVANCED.md](ADVANCED.md)

See [README.md](../README.md) for full documentation.
