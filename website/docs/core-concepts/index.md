---
id: index
title: Core Concepts
sidebar_position: 1
---

# Core Concepts

Understanding the fundamental concepts behind beads.

## Design Philosophy

Beads was built with these principles:

1. **Dolt as source of truth** - Issues stored in a version-controlled SQL database (Dolt), enabling collaboration via Dolt-native replication
2. **AI-native workflows** - Hash-based IDs, JSON output, dependency-aware execution
3. **Local-first operation** - Dolt database for fast queries, background sync
4. **Declarative workflows** - Formulas define repeatable patterns

## Key Components

### Issues

Work items with:
- **ID** - Hash-based (e.g., `bd-a1b2`) or hierarchical (e.g., `bd-a1b2.1`)
- **Type** - `bug`, `feature`, `task`, `epic`, `chore`
- **Priority** - 0 (critical) to 4 (backlog)
- **Status** - `open`, `in_progress`, `closed`
- **Labels** - Flexible tagging
- **Dependencies** - Blocking relationships

### Dependencies

Four types of relationships:

| Type | Description | Affects Ready Queue |
|------|-------------|---------------------|
| `blocks` | Hard dependency (X blocks Y) | Yes |
| `parent-child` | Epic/subtask relationship | No |
| `discovered-from` | Track issues found during work | No |
| `related` | Soft relationship | No |

### Dolt Server Mode

Dolt provides the database backend for beads:
- Start with `bd dolt start`
- Handles auto-commit and sync
- Logs available at `.beads/dolt/sql-server.log`
- Check health with `bd doctor`

### Dolt Sync

The synchronization mechanism:

```
Dolt DB (.beads/dolt/)
    ↕ dolt commit
Local Dolt history
    ↕ dolt push/pull
Remote Dolt repository
```

### Formulas

Declarative workflow templates:
- Define steps with dependencies
- Variable substitution
- Gates for async coordination
- Aspect-oriented transformations

## Navigation

- [Issues & Dependencies](/core-concepts/issues)
- [Dolt Server Mode](/core-concepts/dolt-server)
- [Dolt Sync](/core-concepts/dolt-sync)
- [Hash-based IDs](/core-concepts/hash-ids)
