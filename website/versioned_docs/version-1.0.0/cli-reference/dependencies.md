---
id: dependencies
title: Dependency Commands
sidebar_position: 4
---

# Dependency Commands

Commands for managing issue dependencies.

## bd dep add

Add a dependency between issues.

```bash
bd dep add <dependent> <dependency> [flags]
```

**Semantics:** `<dependent>` depends on `<dependency>` (dependency blocks dependent).

**Flags:**
```bash
--type    Dependency type (blocks|related|discovered-from)
--json    JSON output
```

**Examples:**
```bash
# bd-2 depends on bd-1 (bd-1 blocks bd-2)
bd dep add bd-2 bd-1

# Soft relationship
bd dep add bd-2 bd-1 --type related

# JSON output
bd dep add bd-2 bd-1 --json
```

## bd dep remove

Remove a dependency.

```bash
bd dep remove <dependent> <dependency> [flags]
```

**Examples:**
```bash
bd dep remove bd-2 bd-1
bd dep remove bd-2 bd-1 --json
```

## bd dep tree

Display dependency tree.

```bash
bd dep tree <id> [flags]
```

**Flags:**
```bash
--depth    Maximum depth to display
--json     JSON output
```

**Examples:**
```bash
bd dep tree bd-42
bd dep tree bd-42 --depth 3
bd dep tree bd-42 --json
```

**Output:**
```
Dependency tree for bd-42:

> bd-42: Add authentication [P2] (open)
  > bd-41: Create API [P2] (open)
    > bd-40: Set up database [P1] (closed)
```

## bd dep cycles

Detect circular dependencies.

```bash
bd dep cycles [flags]
```

**Flags:**
```bash
--json    JSON output
```

**Examples:**
```bash
bd dep cycles
bd dep cycles --json
```

## bd ready

Show issues with no blockers.

```bash
bd ready [flags]
```

**Flags:**
```bash
--priority    Filter by priority
--type        Filter by type
--label       Filter by label
--json        JSON output
```

**Examples:**
```bash
bd ready
bd ready --priority 1
bd ready --type bug
bd ready --json
```

**Output:**
```
Ready work (3 issues with no blockers):

1. [P1] bd-40: Set up database
2. [P2] bd-45: Write tests
3. [P3] bd-46: Update docs
```

## bd blocked

Show blocked issues and their blockers.

```bash
bd blocked [flags]
```

**Flags:**
```bash
--json    JSON output
```

**Examples:**
```bash
bd blocked
bd blocked --json
```

**Output:**
```
Blocked issues (2 issues):

bd-42: Add authentication
  Blocked by: bd-41 (open)

bd-41: Create API
  Blocked by: bd-40 (in_progress)
```

## bd relate

Create a soft relationship between issues.

```bash
bd relate <id1> <id2> [flags]
```

**Examples:**
```bash
bd relate bd-42 bd-43
bd relate bd-42 bd-43 --json
```

## bd duplicate

Mark an issue as duplicate.

```bash
bd duplicate <id> --of <canonical> [flags]
```

**Examples:**
```bash
bd duplicate bd-43 --of bd-42
bd duplicate bd-43 --of bd-42 --json
```

## bd supersede

Mark an issue as superseding another.

```bash
bd supersede <old> --with <new> [flags]
```

**Examples:**
```bash
bd supersede bd-42 --with bd-50
bd supersede bd-42 --with bd-50 --json
```

## Understanding Dependencies

### Blocking vs Non-blocking

| Type | Blocks Ready Queue | Use Case |
|------|-------------------|----------|
| `blocks` | Yes | Hard dependency |
| `parent-child` | No | Epic/subtask hierarchy |
| `discovered-from` | No | Track origin |
| `related` | No | Soft link |
| `duplicates` | No | Mark duplicate |
| `supersedes` | No | Version chain |

### Dependency Direction

```bash
# bd-2 depends on bd-1
# Meaning: bd-1 must complete before bd-2 can start
bd dep add bd-2 bd-1

# After bd-1 closes:
bd close bd-1
bd ready  # bd-2 now appears
```

### Avoiding Cycles

```bash
# Check before adding complex dependencies
bd dep cycles

# If cycle detected, remove one dependency
bd dep remove bd-A bd-B
```
