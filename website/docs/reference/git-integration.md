---
id: git-integration
title: Git Integration
sidebar_position: 2
---

# Git Integration

How beads integrates with git.

## Overview

Beads uses git for:
- **Project hosting** - Your code repository also hosts beads configuration
- **Hooks** - Auto-sync on git operations

Data storage and sync are handled by Dolt (a version-controlled SQL database).

## File Structure

```
.beads/
├── config.toml        # Project config (git-tracked)
├── metadata.json      # Backend metadata (git-tracked)
└── dolt/              # Dolt database and server data (gitignored)
```

## Git Hooks

### Installation

```bash
bd hooks install
```

Installs:
- **pre-commit** - Triggers Dolt commit
- **post-merge** - Triggers Dolt sync after pull
- **pre-push** - Ensures Dolt sync before push

### Status

```bash
bd hooks status
```

### Uninstall

```bash
bd hooks uninstall
```

## Conflict Resolution

Dolt handles merge conflicts at the database level using its built-in
merge capabilities. When conflicts arise during sync, Dolt identifies
conflicting rows and allows resolution through SQL.

```bash
# Check for and fix conflicts
bd doctor --fix
```

## Protected Branches

Dolt stores data under `refs/dolt/data`, separate from Git refs. This means beads data doesn't conflict with protected Git branches — no special branch flag is needed.

## Git Worktrees

Beads works in git worktrees using embedded mode:

```bash
# In worktree — just run commands directly
bd create "Task"
bd list
```

## Branch Workflows

### Feature Branch

```bash
git checkout -b feature-x
bd create "Feature X" -t feature
# Work...
bd dolt push
git push
```

### Fork Workflow

```bash
# In fork
bd init --contributor
# Work in separate planning repo...
bd dolt push
```

### Team Workflow

```bash
bd init --team
# All team members share the Dolt database
bd dolt pull   # Pull latest changes from Dolt remote
bd dolt push   # Push your changes to Dolt remote
```

### Duplicate Detection

After merging branches:

```bash
bd duplicates --auto-merge
```

## Best Practices

1. **Install hooks** - `bd hooks install`
2. **Push regularly** - `bd dolt push` at session end
3. **Pull before work** - `bd dolt pull` to get latest issues
4. **Worktrees use embedded mode automatically**
