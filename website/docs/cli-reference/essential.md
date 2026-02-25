---
id: essential
title: Essential Commands
sidebar_position: 2
---

# Essential Commands

The most important commands for daily use.

## bd create

Create a new issue.

```bash
bd create <title> [flags]
```

**Flags:**
| Flag | Short | Description |
|------|-------|-------------|
| `--type` | `-t` | Issue type: bug, feature, task, epic, chore |
| `--priority` | `-p` | Priority: 0-4 (0=critical, 4=backlog) |
| `--description` | `-d` | Detailed description |
| `--labels` | `-l` | Comma-separated labels |
| `--parent` | | Parent issue ID (for hierarchical) |
| `--deps` | | Dependencies (e.g., `discovered-from:bd-42`) |
| `--json` | | JSON output |

**Examples:**
```bash
bd create "Fix login bug" -t bug -p 1
bd create "Add dark mode" -t feature -p 2 --description="User requested"
bd create "Subtask" --parent bd-42 -p 2
bd create "Found during work" --deps discovered-from:bd-42 --json
```

## bd list

List issues with filters.

```bash
bd list [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--status` | Filter by status: open, in_progress, closed |
| `--priority` | Filter by priority (comma-separated) |
| `--type` | Filter by type (comma-separated) |
| `--label-any` | Issues with any of these labels |
| `--label-all` | Issues with all of these labels |
| `--json` | JSON output |

**Examples:**
```bash
bd list --status open
bd list --priority 0,1 --type bug
bd list --label-any urgent,critical --json
```

## bd show

Show issue details.

```bash
bd show <id> [flags]
```

**Examples:**
```bash
bd show bd-42
bd show bd-42 --json
bd show bd-42 bd-43 bd-44  # Multiple issues
```

## bd update

Update issue fields.

```bash
bd update <id> [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--status` | New status |
| `--priority` | New priority |
| `--title` | New title |
| `--description` | New description |
| `--add-label` | Add label |
| `--remove-label` | Remove label |
| `--json` | JSON output |

**Examples:**
```bash
bd update bd-42 --claim
bd update bd-42 --priority 0 --add-label urgent
bd update bd-42 --title "Updated title" --json
```

## bd close

Close an issue.

```bash
bd close <id> [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--reason` | Closure reason |
| `--json` | JSON output |

**Examples:**
```bash
bd close bd-42
bd close bd-42 --reason "Fixed in PR #123"
bd close bd-42 --json
```

## bd ready

Show issues ready to work on (no blockers).

```bash
bd ready [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--priority` | Filter by priority |
| `--type` | Filter by type |
| `--json` | JSON output |

**Examples:**
```bash
bd ready
bd ready --priority 1
bd ready --json
```

## bd blocked

Show blocked issues and their blockers.

```bash
bd blocked [flags]
```

**Examples:**
```bash
bd blocked
bd blocked --json
```

## bd sync

Force immediate sync to git.

```bash
bd sync [flags]
```

Performs:
1. Dolt commit (snapshot current database state)
2. Dolt push to remote

**Examples:**
```bash
bd sync
bd sync --json
```

## bd info

Show system information.

```bash
bd info [flags]
```

**Flags:**
| Flag | Description |
|------|-------------|
| `--whats-new` | Show recent version changes |
| `--schema` | Show database schema |
| `--json` | JSON output |

**Examples:**
```bash
bd info
bd info --whats-new
bd info --json
```

## bd stats

Show project statistics.

```bash
bd stats [flags]
```

**Examples:**
```bash
bd stats
bd stats --json
```
