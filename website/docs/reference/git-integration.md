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

For protected main branches:

```bash
bd init --branch beads-sync
```

This:
- Creates a separate `beads-sync` branch
- Syncs issues to that branch
- Avoids direct commits to main

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
bd sync
git push
```

### Fork Workflow

```bash
# In fork
bd init --contributor
# Work in separate planning repo...
bd sync
```

### Team Workflow

```bash
bd init --team
# All team members share the Dolt database
bd sync  # Pulls latest changes via Dolt replication
```

### Duplicate Detection

After merging branches:

```bash
bd duplicates --auto-merge
```

## Best Practices

1. **Install hooks** - `bd hooks install`
2. **Sync regularly** - `bd sync` at session end
3. **Pull before work** - Get latest issues
4. **Worktrees use embedded mode automatically**
