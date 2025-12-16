# Multi-Repo Migration Guide

This guide helps you adopt beads' multi-repo workflow for OSS contributions, team collaboration, and multi-phase development.

## Quick Start

**Already have beads installed?** Jump to your scenario:
- [OSS Contributor](#oss-contributor-workflow) - Keep planning out of upstream PRs
- [Team Member](#team-workflow) - Shared planning on branches
- [Multi-Phase Development](#multi-phase-development) - Separate repos per phase
- [Multiple Personas](#multiple-personas) - Architect vs. implementer separation

**New to beads?** See [QUICKSTART.md](QUICKSTART.md) first.

## What is Multi-Repo Mode?

By default, beads stores issues in `.beads/issues.jsonl` in your current repository. Multi-repo mode lets you:

- **Route issues to different repositories** based on your role (maintainer vs. contributor)
- **Aggregate issues from multiple repos** into a unified view
- **Keep contributor planning separate** from upstream projects
- **Maintain git ledger everywhere** - no gitignored files

## When Do You Need Multi-Repo?

### You DON'T need multi-repo if:
- ✅ Working solo on your own project
- ✅ Team with shared repository and trust model
- ✅ All issues belong in the project's git history

### You DO need multi-repo if:
- 🔴 Contributing to OSS - don't pollute upstream with planning
- 🔴 Fork workflow - planning shouldn't appear in PRs
- 🔴 Multiple work phases - design vs. implementation repos
- 🔴 Multiple personas - architect planning vs. implementer tasks

## Core Concepts

### 1. Source Repository (`source_repo`)

Every issue has a `source_repo` field indicating which repository owns it:

```jsonl
{"id":"bd-abc","source_repo":".","title":"Core issue"}
{"id":"bd-xyz","source_repo":"~/.beads-planning","title":"Planning issue"}
```

- `.` = Current repository (default)
- `~/.beads-planning` = Contributor planning repo
- `/path/to/repo` = Absolute path to another repo

### 2. Auto-Routing

Beads automatically routes new issues to the right repository based on your role:

```bash
# Maintainer (has SSH push access)
bd create "Fix bug" -p 1
# → Creates in current repo (source_repo = ".")

# Contributor (HTTPS or no push access)
bd create "Fix bug" -p 1  
# → Creates in ~/.beads-planning (source_repo = "~/.beads-planning")
```

### 3. Multi-Repo Hydration

Beads can aggregate issues from multiple repositories into a unified database:

```bash
bd list --json
# Shows issues from:
# - Current repo (.)
# - Planning repo (~/.beads-planning)
# - Any configured additional repos
```

## OSS Contributor Workflow

**Problem:** You're contributing to an OSS project but don't want your experimental planning to appear in PRs.

**Solution:** Use a separate planning repository that's never committed to upstream.

### Setup (One-Time)

```bash
# 1. Fork and clone the upstream project
git clone https://github.com/you/project.git
cd project

# 2. Initialize beads (if not already done)
bd init

# 3. Run the contributor setup wizard
bd init --contributor

# The wizard will:
# - Detect that you're in a fork (checks for 'upstream' remote)
# - Prompt you to create a planning repo (~/.beads-planning by default)
# - Configure auto-routing (contributor → planning repo)
# - Set up multi-repo hydration
```

### Manual Configuration

If you prefer manual setup:

```bash
# 1. Create planning repository
mkdir -p ~/.beads-planning
cd ~/.beads-planning
git init
bd init --prefix plan

# 2. Configure routing in your fork
cd ~/projects/project
bd config set routing.mode auto
bd config set routing.contributor "~/.beads-planning"

# 3. Add planning repo to hydration sources
bd config set repos.additional "~/.beads-planning"
```

### Daily Workflow

```bash
# Work in your fork
cd ~/projects/project

# Create planning issues (auto-routed to ~/.beads-planning)
bd create "Investigate auth implementation" -p 1
bd create "Draft RFC for new feature" -p 2

# View all issues (current repo + planning repo)
bd ready
bd list --json

# Work on an issue
bd update plan-42 --status in_progress

# Complete work
bd close plan-42 --reason "Completed"

# Create PR - your planning issues never appear!
git add .
git commit -m "Fix authentication bug"
git push origin my-feature-branch
# ✅ PR only contains code changes, no .beads/ pollution
```

### Proposing Issues Upstream

If you want to share a planning issue with upstream:

```bash
# Option 1: Manually copy issue to upstream repo
bd show plan-42 --json > /tmp/issue.json
# (Send to maintainers or create GitHub issue)

# Option 2: Migrate issue (future feature, see bd-mlcz)
bd migrate plan-42 --to . --dry-run
bd migrate plan-42 --to .
```

## Team Workflow

**Problem:** Team members working on shared repository with branches, but different levels of planning granularity.

**Solution:** Use branch-based workflow with optional personal planning repos.

### Setup (Team Lead)

```bash
# 1. Initialize beads in main repo
cd ~/projects/team-project
bd init --prefix team

# 2. Run team setup wizard  
bd init --team

# The wizard will:
# - Detect shared repository (SSH push access)
# - Configure auto-routing (maintainer → current repo)
# - Set up protected branch workflow (if using GitHub/GitLab)
# - Create example workflows
```

### Setup (Team Member)

```bash
# 1. Clone team repo
git clone git@github.com:team/project.git
cd project

# 2. Beads auto-detects you're a maintainer (SSH access)
bd create "Implement feature X" -p 1
# → Creates in current repo (team-123)

# 3. Optional: Create personal planning repo for experiments
mkdir -p ~/.beads-planning-personal
cd ~/.beads-planning-personal
git init
bd init --prefix exp

# 4. Configure multi-repo in team project
cd ~/projects/project
bd config set repos.additional "~/.beads-planning-personal"
```

### Daily Workflow

```bash
# Shared team planning (committed to repo)
bd create "Implement auth" -p 1 --repo .
# → team-42 (visible to entire team)

# Personal experiments (not committed to team repo)
bd create "Try alternative approach" -p 2 --repo ~/.beads-planning-personal
# → exp-99 (private planning)

# View all work
bd ready
bd list --json

# Complete team work
git add .beads/issues.jsonl
git commit -m "Updated issue tracker"
git push origin main
```

## Multi-Phase Development

**Problem:** Project has distinct phases (planning, implementation, maintenance) that need separate issue spaces.

**Solution:** Use separate repositories for each phase.

### Setup

```bash
# 1. Create phase repositories
mkdir -p ~/projects/myapp-planning
mkdir -p ~/projects/myapp-implementation
mkdir -p ~/projects/myapp-maintenance

# 2. Initialize each phase
cd ~/projects/myapp-planning
git init
bd init --prefix plan

cd ~/projects/myapp-implementation  
git init
bd init --prefix impl

cd ~/projects/myapp-maintenance
git init
bd init --prefix maint

# 3. Configure aggregation in main workspace
cd ~/projects/myapp-implementation
bd config set repos.additional "~/projects/myapp-planning,~/projects/myapp-maintenance"
```

### Workflow

```bash
# Phase 1: Planning
cd ~/projects/myapp-planning
bd create "Design auth system" -p 1 -t epic
bd create "Research OAuth providers" -p 1

# Phase 2: Implementation (view planning + implementation issues)
cd ~/projects/myapp-implementation
bd ready  # Shows issues from both repos
bd create "Implement auth backend" -p 1
bd dep add impl-42 plan-10 --type blocks  # Link across repos

# Phase 3: Maintenance
cd ~/projects/myapp-maintenance
bd create "Security patch for auth" -p 0 -t bug
```

## Multiple Personas

**Problem:** You work as both architect (high-level planning) and implementer (detailed tasks).

**Solution:** Separate repositories for each persona's work.

### Setup

```bash
# 1. Create persona repos
mkdir -p ~/architect-planning
mkdir -p ~/implementer-tasks

cd ~/architect-planning
git init
bd init --prefix arch

cd ~/implementer-tasks
git init  
bd init --prefix impl

# 2. Configure aggregation
cd ~/implementer-tasks
bd config set repos.additional "~/architect-planning"
```

### Workflow

```bash
# Architect mode
cd ~/architect-planning
bd create "System architecture for feature X" -p 1 -t epic
bd create "Database schema design" -p 1

# Implementer mode (sees both architect + implementation tasks)
cd ~/implementer-tasks
bd ready
bd create "Implement user table" -p 1
bd dep add impl-10 arch-42 --type blocks

# Complete implementation
bd close impl-10 --reason "Completed"
```

## Configuration Reference

### Routing Settings

```bash
# Auto-detect role and route accordingly
bd config set routing.mode auto

# Always use default repo (ignore role detection)
bd config set routing.mode explicit  
bd config set routing.default "."

# Configure repos for each role
bd config set routing.maintainer "."
bd config set routing.contributor "~/.beads-planning"
```

### Multi-Repo Hydration

```bash
# Add additional repos to aggregate
bd config set repos.additional "~/repo1,~/repo2,~/repo3"

# Set primary repo (optional)
bd config set repos.primary "."
```

### Override Auto-Routing

```bash
# Force issue to specific repo (ignores auto-routing)
bd create "Issue" -p 1 --repo /path/to/repo
```

## Troubleshooting

### Issues appearing in wrong repository

**Problem:** `bd create` routes issues to unexpected repository.

**Solution:**
```bash
# Check current routing configuration
bd config get routing.mode
bd config get routing.maintainer
bd config get routing.contributor

# Check detected role
bd info --json | jq '.role'

# Override with explicit flag
bd create "Issue" -p 1 --repo .
```

### Can't see issues from other repos

**Problem:** `bd list` only shows issues from current repo.

**Solution:**
```bash
# Check multi-repo configuration
bd config get repos.additional

# Add missing repos
bd config set repos.additional "~/repo1,~/repo2"

# Verify hydration
bd sync
bd list --json
```

### Git merge conflicts in .beads/issues.jsonl

**Problem:** Multiple repos modifying same JSONL file.

**Solution:** See [TROUBLESHOOTING.md](TROUBLESHOOTING.md#git-merge-conflicts) and consider [beads-merge](https://github.com/neongreen/mono/tree/main/beads-merge) tool.

### Discovered issues in wrong repository

**Problem:** Issues created with `discovered-from` dependency appear in wrong repo.

**Solution:** Discovered issues automatically inherit parent's `source_repo`. This is intentional. To override:
```bash
bd create "Issue" -p 1 --deps discovered-from:bd-42 --repo /different/repo
```

### Planning repo polluting PRs

**Problem:** Your `~/.beads-planning` changes appear in PRs to upstream.

**Solution:** This shouldn't happen if configured correctly. Verify:
```bash
# Check that planning repo is separate from fork
ls -la ~/.beads-planning/.git  # Should exist
ls -la ~/projects/fork/.beads/  # Should NOT contain planning issues

# Verify routing
bd config get routing.contributor  # Should be ~/.beads-planning
```

## Backward Compatibility

### Migrating from Single-Repo

No migration needed! Multi-repo mode is opt-in:

```bash
# Before (single repo)
bd create "Issue" -p 1
# → Creates in .beads/issues.jsonl

# After (multi-repo configured)
bd create "Issue" -p 1
# → Auto-routed based on role
# → Old issues in .beads/issues.jsonl still work
```

### Disabling Multi-Repo

```bash
# Remove routing configuration
bd config unset routing.mode
bd config unset repos.additional

# All issues go to current repo again
bd create "Issue" -p 1
# → Back to single-repo mode
```

## Best Practices

### OSS Contributors
- ✅ Always use `~/.beads-planning` or similar for personal planning
- ✅ Never commit `.beads/` changes to upstream PRs
- ✅ Use descriptive prefixes (`plan-`, `exp-`) for clarity
- ❌ Don't mix planning and implementation in the same repo

### Teams
- ✅ Commit `.beads/issues.jsonl` to shared repository
- ✅ Use protected branch workflow for main/master
- ✅ Review issue changes in PRs like code changes
- ❌ Don't gitignore `.beads/` - you lose the git ledger

### Multi-Phase Projects
- ✅ Use clear phase naming (`planning`, `impl`, `maint`)
- ✅ Link issues across phases with dependencies
- ✅ Archive completed phases periodically
- ❌ Don't duplicate issues across phases

## Next Steps

- **CLI Reference:** See [README.md](../README.md) for command details
- **Configuration Guide:** See [CONFIG.md](CONFIG.md) for all config options
- **Troubleshooting:** See [TROUBLESHOOTING.md](TROUBLESHOOTING.md)
- **Multi-Repo Internals:** See [MULTI_REPO_HYDRATION.md](MULTI_REPO_HYDRATION.md) and [ROUTING.md](ROUTING.md)

## Related Issues

<<<<<<< HEAD
- `bd-8rd` - Migration and onboarding epic
- `bd-mlcz` - `bd migrate` command (planned)
- `bd-kla1` - `bd init --contributor` wizard ✅ implemented
- `bd-twlr` - `bd init --team` wizard ✅ implemented
=======
- [bd-8rd](/.beads/issues.jsonl#bd-8rd) - Migration and onboarding epic
- [bd-mlcz](/.beads/issues.jsonl#bd-mlcz) - `bd migrate` command (planned)
- [bd-kla1](/.beads/issues.jsonl#bd-kla1) - `bd init --contributor` wizard ✅ implemented
- [bd-twlr](/.beads/issues.jsonl#bd-twlr) - `bd init --team` wizard ✅ implemented
>>>>>>> origin/bd-l0pg-slit
