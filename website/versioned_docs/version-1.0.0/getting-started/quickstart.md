---
id: quickstart
title: Quick Start
sidebar_position: 2
---

# Beads Quick Start

Get up and running with beads in a few minutes.

## Why Beads?

Flat issue trackers show you all open items. You pick one - but if it depends on something else that isn't done yet, you've wasted time. Beads tracks **dependencies between issues** and computes a **ready queue** - only items with no active blockers appear.

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

The agent picks the right task every time.

## Initialize

```bash
cd your-project
bd init          # Interactive setup (prompts for role)
bd init --quiet  # Non-interactive (for AI agents)
```

The wizard creates `.beads/`, sets up the embedded Dolt database, and optionally installs git hooks. During init, you'll be asked whether you're a maintainer or contributor - this determines how beads routes issues. You can change it later with `git config beads.role`.

## Create Issues and Dependencies

```bash
# Create issues
bd create "Set up database" -p 1 -t task
bd create "Create API" -p 2 -t feature
bd create "Add authentication" -p 2 -t feature

# Add dependencies (API needs database, auth needs API)
bd dep add bd-2 bd-1
bd dep add bd-3 bd-2

# View the dependency tree
bd dep tree bd-3
```

Issue IDs are hash-based (e.g., `bd-a1b2`) to prevent collisions when multiple agents work concurrently.

## Find and Work the Ready Queue

```bash
# What's unblocked right now?
bd ready

# Why is something blocked?
bd ready --explain

# Claim and complete work
bd update bd-1 --claim
bd close bd-1 --reason "Database setup complete"

# Now bd-2 is ready
bd ready
```

`bd ready` is not the same as `bd list --status open` - the list command shows all open issues regardless of blockers. The ready command computes the dependency graph and only shows truly unblocked work.

## Epics

Group related work under an epic:

```bash
bd create "Auth System" -t epic -p 1       # Returns: bd-a3f8e9
bd create "Design login UI" -p 1 --parent bd-a3f8e9
bd create "Backend validation" -p 1 --parent bd-a3f8e9
bd dep tree bd-a3f8e9
```

## Team Sync

Share issues using Dolt remotes (works over the same Git remote):

```bash
bd dolt remote add origin git+ssh://git@github.com/org/repo.git
bd dolt push
bd dolt pull
```

When a teammate clones the repo, `bd bootstrap` auto-detects the existing database. See [Sync](/cli-reference/sync) for details.

## Track Progress

```bash
bd blocked    # See blocked issues
bd stats      # Project statistics
bd list       # All issues
bd doctor     # Health check
```

## Next Steps

- Add labels: `bd create "Task" -l "backend,urgent"`
- Filter ready work: `bd ready --priority 1`
- Check graph integrity: `bd graph check`
- Gates for PR/CI sync: [Dependencies](/cli-reference/dependencies)
- IDE integration: [IDE Setup](/getting-started/ide-setup)
- Full command list: [CLI Reference](/cli-reference)
