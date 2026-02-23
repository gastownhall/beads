---
id: index
title: CLI Reference
sidebar_position: 1
---

# CLI Reference

Complete reference for all `bd` commands.

## Command Structure

```bash
bd [global-flags] <command> [command-flags] [arguments]
```

### Global Flags

| Flag | Description |
|------|-------------|
| `--db <path>` | Use specific database file |
| `--json` | Output in JSON format |
| `--quiet` | Suppress non-essential output |
| `--verbose` | Verbose output |
| `--version` | Show version |
| `--help` | Show help |

## Command Categories

### Essential Commands

Most frequently used:

| Command | Description |
|---------|-------------|
| `bd create` | Create new issue |
| `bd list` | List issues with filters |
| `bd show` | Show issue details |
| `bd update` | Update issue fields |
| `bd close` | Close an issue |
| `bd ready` | Show unblocked work |
| `bd sync` | Force sync to git |

### Issue Management

| Command | Description |
|---------|-------------|
| `bd create` | Create issue |
| `bd show` | Show details |
| `bd update` | Update fields |
| `bd close` | Close issue |
| `bd delete` | Delete issue |
| `bd reopen` | Reopen closed issue |

### Dependencies

| Command | Description |
|---------|-------------|
| `bd dep add` | Add dependency |
| `bd dep remove` | Remove dependency |
| `bd dep tree` | Show dependency tree |
| `bd dep cycles` | Detect circular dependencies |
| `bd blocked` | Show blocked issues |
| `bd ready` | Show unblocked issues |

### Labels & Comments

| Command | Description |
|---------|-------------|
| `bd label add` | Add label to issue |
| `bd label remove` | Remove label |
| `bd label list` | List all labels |
| `bd comment add` | Add comment |
| `bd comment list` | List comments |

### Sync & Export

| Command | Description |
|---------|-------------|
| `bd sync` | Full sync cycle |
| `bd export` | Export to JSONL |
| `bd import` | Import from JSONL |
| `bd migrate` | Migrate database schema |

### System

| Command | Description |
|---------|-------------|
| `bd init` | Initialize beads in project |
| `bd info` | Show system info |
| `bd version` | Show version |
| `bd config` | Manage configuration |
| `bd doctor` | Check system health |
| `bd hooks` | Manage git hooks |

### Workflows

| Command | Description |
|---------|-------------|
| `bd pour` | Instantiate formula as molecule |
| `bd wisp` | Create ephemeral wisp |
| `bd mol` | Manage molecules |
| `bd pin` | Pin work to agent |
| `bd hook` | Show pinned work |

## Quick Reference

### Creating Issues

```bash
# Basic
bd create "Title" -t task -p 2

# With description
bd create "Title" --description="Details here" -t bug -p 1

# With labels
bd create "Title" -l "backend,urgent"

# As child of epic
bd create "Subtask" --parent bd-42

# With discovered-from link
bd create "Found bug" --deps discovered-from:bd-42

# JSON output
bd create "Title" --json
```

### Querying Issues

```bash
# All open issues
bd list --status open

# High priority bugs
bd list --status open --priority 0,1 --type bug

# With specific labels
bd list --label-any urgent,critical

# JSON output
bd list --json
```

### Working with Dependencies

```bash
# Add: bd-2 depends on bd-1
bd dep add bd-2 bd-1

# View tree
bd dep tree bd-2

# Find cycles
bd dep cycles

# What's ready to work?
bd ready

# What's blocked?
bd blocked
```

### Syncing

```bash
# Full sync (export + commit + push)
bd sync

# Force export
bd export

# Import from file
bd import -i .beads/issues.jsonl
```

## See Also

- [Essential Commands](/cli-reference/essential)
- [Issue Commands](/cli-reference/issues)
- [Dependency Commands](/cli-reference/dependencies)
- [Label Commands](/cli-reference/labels)
- [Sync Commands](/cli-reference/sync)
