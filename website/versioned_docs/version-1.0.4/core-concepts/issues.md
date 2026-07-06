---
id: issues
title: Issues & Dependencies
sidebar_position: 2
---

# Issues & Dependencies

Understanding the issue model in beads.

## Issue Structure

Every issue has:

```bash
bd show bd-42 --json
```

```json
{
  "id": "bd-42",
  "title": "Implement authentication",
  "description": "Add JWT-based auth",
  "type": "feature",
  "status": "open",
  "priority": 1,
  "labels": ["backend", "security"],
  "created_at": "2024-01-15T10:30:00Z",
  "updated_at": "2024-01-15T10:30:00Z"
}
```

## Issue Types

| Type | Use Case |
|------|----------|
| `bug` | Something broken that needs fixing |
| `feature` | New functionality |
| `task` | Work item (tests, docs, refactoring) |
| `epic` | Large feature with subtasks |
| `chore` | Maintenance (dependencies, tooling) |

## Priorities

| Priority | Level | Examples |
|----------|-------|----------|
| 0 | Critical | Security, data loss, broken builds |
| 1 | High | Major features, important bugs |
| 2 | Medium | Nice-to-have features, minor bugs |
| 3 | Low | Polish, optimization |
| 4 | Backlog | Future ideas |

## Creating Issues

```bash
# Basic issue
bd create "Fix login bug" -t bug -p 1

# With description
bd create "Add password reset" \
  --description="Users need to reset forgotten passwords via email" \
  -t feature -p 2

# With labels
bd create "Update dependencies" -t chore -l "maintenance,security"

# JSON output for agents
bd create "Task" -t task --json
```

## Dependencies

### Blocking Dependencies

The `blocks` relationship affects the ready queue:

```bash
# Add dependency: bd-2 depends on bd-1
bd dep add bd-2 bd-1

# View dependencies
bd dep tree bd-2

# See blocked issues
bd blocked

# See ready work (not blocked)
bd ready
```

### Structural Relationships

These don't affect the ready queue:

```bash
# Parent-child (epic subtasks)
bd create "Epic" -t epic
bd create "Subtask" --parent bd-42

# Discovered-from (found during work)
bd create "Found bug" --deps discovered-from:bd-42

# Related (soft link)
bd relate bd-1 bd-2
```

### Dependency Types

| Type | Description | Ready Queue Impact |
|------|-------------|-------------------|
| `blocks` | Hard dependency | Yes - blocked items not ready |
| `parent-child` | Epic/subtask hierarchy | No |
| `discovered-from` | Tracks origin of discovery | No |
| `related` | Soft relationship | No |

## Hierarchical Issues

For large features, use hierarchical IDs:

```bash
# Create epic
bd create "Auth System" -t epic -p 1
# Returns: bd-a3f8e9

# Child tasks auto-number
bd create "Design login UI" --parent bd-a3f8e9     # bd-a3f8e9.1
bd create "Backend validation" --parent bd-a3f8e9  # bd-a3f8e9.2

# View hierarchy
bd dep tree bd-a3f8e9
```

## Updating Issues

```bash
# Change status
bd update bd-42 --claim

# Change priority
bd update bd-42 --priority 0

# Add labels
bd update bd-42 --add-label urgent

# Multiple changes
bd update bd-42 --claim --priority 1 --add-label "in-review"
```

## Closing Issues

```bash
# Simple close
bd close bd-42

# With reason
bd close bd-42 --reason "Implemented in PR #123"

# JSON output
bd close bd-42 --json
```

## Searching and Filtering

```bash
# By status
bd list --status open
bd list --status in_progress

# By priority
bd list --priority 1
bd list --priority 0,1  # Multiple

# By type
bd list --type bug
bd list --type feature,task

# By label
bd list --label-any urgent,critical
bd list --label-all backend,security

# Combined filters
bd list --status open --priority 1 --type bug --json
```
