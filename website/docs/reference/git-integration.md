---
id: git-integration
title: Git Integration
sidebar_position: 2
---

# Git Integration

How beads integrates with git.

## Overview

Beads uses git for:
- **JSONL sync** - Issues stored in `.beads/issues.jsonl`
- **Deletion tracking** - `.beads/deletions.jsonl`
- **Conflict resolution** - Custom merge driver
- **Hooks** - Auto-sync on git operations

## File Structure

```
.beads/
├── beads.db           # SQLite database (gitignored)
├── issues.jsonl       # Issue data (git-tracked)
├── deletions.jsonl    # Deletion manifest (git-tracked)
├── config.toml        # Project config (git-tracked)
└── dolt/              # Dolt server data (gitignored)
```

## Git Hooks

### Installation

```bash
bd hooks install
```

Installs:
- **pre-commit** - Exports database to JSONL
- **post-merge** - Imports from JSONL after pull
- **pre-push** - Ensures sync before push

### Status

```bash
bd hooks status
```

### Uninstall

```bash
bd hooks uninstall
```

## Merge Driver

### Purpose

The beads merge driver handles JSONL conflicts automatically:
- Merges non-conflicting changes
- Uses latest timestamp for same-issue edits
- Preserves both sides for real conflicts

### Installation

```bash
bd init  # Prompts for merge driver setup
```

Or manually add to `.gitattributes`:

```gitattributes
.beads/issues.jsonl merge=beads
.beads/deletions.jsonl merge=beads
```

And `.git/config`:

```ini
[merge "beads"]
    name = Beads JSONL merge driver
    driver = bd merge-driver %O %A %B
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
# All team members share issues.jsonl
git pull  # Auto-imports via hook
```

## Conflict Resolution

### With Merge Driver

Automatic - driver handles most conflicts.

### Manual Resolution

```bash
# After conflict
git checkout --ours .beads/issues.jsonl
bd import -i .beads/issues.jsonl
bd sync
git add .beads/
git commit
```

### Duplicate Detection

After merge:

```bash
bd duplicates --auto-merge
```

## Best Practices

1. **Install hooks** - `bd hooks install`
2. **Use merge driver** - Avoid manual conflict resolution
3. **Sync regularly** - `bd sync` at session end
4. **Pull before work** - Get latest issues
5. **Worktrees use embedded mode automatically**
